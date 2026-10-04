package server

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"text/tabwriter"

	"shelley.exe.dev/claudetool"
	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/llm"
)

// The compact_in_place tool lets the agent compact its own context: "index"
// lists the LLM's current view, "compact" records an in-place compaction
// (collapsing message ranges into notes, trimming tool outputs) and the turn
// continues on the compacted history. While the tool is enabled the agent is
// also nudged with the context size (see contextNudger).

const (
	// defaultCompactNudgeTokens is where the first context nudge fires when
	// ConversationOptions.CompactNudgeTokens is unset.
	defaultCompactNudgeTokens = 160_000
	// compactNudgeStep is the distance between later nudges.
	compactNudgeStep = 50_000
	// maxCollapseTokens bounds the history one collapse may cover, so that a
	// single note never stands for too much. A single message, or a single
	// tool call and its output, may exceed it.
	maxCollapseTokens = 20_000
)

// inPlaceCompactor is the claudetool.InPlaceCompactor of the loop of one
// generation.
type inPlaceCompactor struct {
	cm         *ConversationManager
	generation uint64
}

func (c inPlaceCompactor) Index(ctx context.Context) (string, error) {
	items, err := c.cm.loadContextItems(ctx)
	if err != nil {
		return "", err
	}
	return compactIndex(items, c.cm.keepRecentTokens), nil
}

func (c inPlaceCompactor) Compact(ctx context.Context, in claudetool.CompactInPlaceInput) (string, error) {
	return c.cm.compactMidTurn(ctx, c.generation, in)
}

// compactMidTurn validates in against the current context and records it.
// The running loop picks up the compacted history before its next request.
func (cm *ConversationManager) compactMidTurn(ctx context.Context, generation uint64, in claudetool.CompactInPlaceInput) (string, error) {
	// Like takeInjectable, this runs in the loop goroutine: hold the lifecycle
	// lock only to validate and record, so a concurrent teardown either sees
	// the record or the record is never made.
	cm.loopLifecycleMu.Lock()
	cm.mu.Lock()
	stale := cm.loopTearingDown || cm.loop == nil || cm.loopGeneration != generation || cm.distilling || cm.cancelling
	cm.mu.Unlock()
	if stale {
		cm.loopLifecycleMu.Unlock()
		return "", fmt.Errorf("the conversation is being stopped or rewritten; not compacted")
	}
	items, err := cm.loadContextItems(ctx)
	var c db.InPlaceCompaction
	if err == nil {
		c, err = buildCompaction(items, cm.keepRecentTokens, in)
	}
	var after []contextItem
	if err == nil {
		after, err = applyInPlaceCompaction(items, c, cm.conversationID, 0)
	}
	if err == nil {
		// Compaction hides the evidence of compacting: the nudges, and the
		// earlier calls of this tool (this call stays, as a receipt).
		c.HiddenSequenceIDs, c.HiddenToolUseIDs = compactionEvidence(after)
		after, err = applyInPlaceCompaction(items, c, cm.conversationID, 0)
	}
	var created *generated.Message
	if err == nil {
		created, err = cm.createInPlaceCompaction(context.WithoutCancel(ctx), c)
	}
	if err == nil {
		cm.mu.Lock()
		cm.compactedGeneration = generation
		cm.mu.Unlock()
	}
	cm.loopLifecycleMu.Unlock()
	if err != nil {
		return "", err
	}
	if err := cm.publishCreated(ctx, created); err != nil {
		return "", err
	}
	return fmt.Sprintf("Compacted: %d collapsed, %d trimmed; history ~%d -> ~%d tokens.",
		len(c.Squishes), len(c.Trims), itemTokens(items), itemTokens(after)), nil
}

// compactionEvidence returns the context nudges in items, and the
// compact_in_place calls whose results are in items.
func compactionEvidence(items []contextItem) (seqs []int64, toolUseIDs []string) {
	results := map[string]bool{}
	for _, it := range items {
		if it.source == nil {
			continue
		}
		if it.source.UserData != nil {
			var ud struct {
				ContextNudge bool `json:"context_nudge"`
			}
			if json.Unmarshal([]byte(*it.source.UserData), &ud) == nil && ud.ContextNudge {
				seqs = append(seqs, it.from)
			}
		}
		for _, c := range it.message.Content {
			if c.Type == llm.ContentTypeToolResult {
				results[c.ToolUseID] = true
			}
		}
	}
	for _, it := range items {
		for _, c := range it.message.Content {
			if c.Type == llm.ContentTypeToolUse && c.ToolName == claudetool.CompactInPlaceName && results[c.ID] {
				toolUseIDs = append(toolUseIDs, c.ID)
			}
		}
	}
	return seqs, toolUseIDs
}

// buildCompaction turns the agent's request into a record, validating it
// against items (the current view) and its recent part.
func buildCompaction(items []contextItem, keepRecentTokens int, in claudetool.CompactInPlaceInput) (db.InPlaceCompaction, error) {
	var c db.InPlaceCompaction
	if len(in.Trim) == 0 && len(in.Collapse) == 0 {
		return c, fmt.Errorf("nothing to compact: give trim and/or collapse")
	}
	for _, id := range in.Trim {
		i, ok := findToolResult(items, id)
		if !ok {
			return c, fmt.Errorf("trim %s: no tool output with that id in the index", id)
		}
		if slices.ContainsFunc(c.Trims, func(t db.CompactionTrim) bool { return t.ToolUseID == id }) {
			return c, fmt.Errorf("trim %s: listed twice", id)
		}
		c.Trims = append(c.Trims, db.CompactionTrim{SequenceID: items[i].from, ToolUseID: id})
	}

	recent := recentStart(items, keepRecentTokens)
	type span struct{ i, j int }
	var spans []span
	for _, col := range in.Collapse {
		i, j := indexOfIndexID(items, col.From), indexOfIndexID(items, col.To)
		name := fmt.Sprintf("collapse %s-%s", col.From, col.To)
		switch {
		case i < 0 || j < 0:
			return c, fmt.Errorf("%s: unknown id; use ids from the index", name)
		case j < i:
			return c, fmt.Errorf("%s: from comes after to", name)
		case j >= recent:
			return c, fmt.Errorf("%s: reaches into the recent part (from %s on), which cannot be collapsed", name, itemID(items[recent]))
		case strings.TrimSpace(col.Note) == "":
			return c, fmt.Errorf("%s: note is empty", name)
		}
		tokens := itemTokens(items[i : j+1])
		if tokens > maxCollapseTokens && j-i > 1 {
			return c, fmt.Errorf("%s: covers ~%d tokens; at most ~%d per collapse unless it is one tool call and its output, so split it", name, tokens, maxCollapseTokens)
		}
		if (len(col.Note)+3)/4 >= tokens {
			return c, fmt.Errorf("%s: note is not shorter than the ~%d tokens it replaces", name, tokens)
		}
		for _, s := range spans {
			if i <= s.j && s.i <= j {
				return c, fmt.Errorf("%s: overlaps another collapse", name)
			}
		}
		spans = append(spans, span{i, j})
		c.Squishes = append(c.Squishes, db.CompactionSquish{FromSequenceID: items[i].from, ToSequenceID: items[j].to, Summary: col.Note})
	}
	// Squishes apply in order, each to the view the previous ones left; they
	// are disjoint, so order them as they appear.
	slices.SortFunc(c.Squishes, func(a, b db.CompactionSquish) int { return int(a.FromSequenceID - b.FromSequenceID) })
	return c, nil
}

func indexOfIndexID(items []contextItem, id claudetool.IndexID) int {
	return indexOfItem(items, func(it contextItem) bool { return itemID(it) == string(id) })
}

// findToolResult returns the index of the original message carrying the tool
// result for toolUseID.
func findToolResult(items []contextItem, toolUseID string) (int, bool) {
	for i, it := range items {
		if it.source == nil {
			continue
		}
		for _, c := range it.message.Content {
			if c.Type == llm.ContentTypeToolResult && c.ToolUseID == toolUseID {
				return i, true
			}
		}
	}
	return 0, false
}

// recentStart returns the index of the first item of the recent part, which
// cannot be collapsed: about keepRecentTokens of the newest history, starting
// on a message that is not a tool result.
func recentStart(items []contextItem, keepRecentTokens int) int {
	msgs := make([]llm.Message, len(items))
	for i, it := range items {
		msgs[i] = it.message
	}
	return findPiCutPoint(msgs, keepRecentTokens)
}

func itemID(it contextItem) string {
	if it.source == nil {
		return it.noteID
	}
	return strconv.FormatInt(it.from, 10)
}

func itemTokens(items []contextItem) int {
	n := 0
	for _, it := range items {
		n += estimatePiMessageTokens(it.message)
	}
	return n
}

const compactIndexGuidance = `Look for dead ends, out-of-date tool outputs (superseded file reads, old
build and test logs), side quests and finished subprojects. Collapse each into
a short note that keeps what still matters: decisions, findings, paths,
commands, ids. Trim tool outputs you no longer need. Big tool inputs and
outputs are most of the context, so focus there. Compacting only a little is
fine; when unsure, leave it. Originals stay retrievable by conversation_id and
sequence_id (see the previous-conversations skill).

Then call compact_in_place with action "compact": trim lists tool_use_ids;
collapse lists {from, to, note} ranges of ids from the table. A range may not
split a call from its output, nor cover more than ~%dk tokens unless it is just
one tool call and its output.`

// compactIndex renders the index the agent compacts from.
func compactIndex(items []contextItem, keepRecentTokens int) string {
	recent := recentStart(items, keepRecentTokens)
	var b strings.Builder
	fmt.Fprintf(&b, "## Index\n\n%d messages, ~%d tokens.\n", len(items), itemTokens(items))
	if recent < len(items) {
		if recent == 0 {
			b.WriteString("All of it is recent, so nothing can be collapsed yet; tool outputs can still be trimmed.\n")
		} else {
			fmt.Fprintf(&b, "The recent part, from %s on (%d messages, ~%d tokens), cannot be collapsed; its tool outputs can still be trimmed.\n",
				itemID(items[recent]), len(items)-recent, itemTokens(items[recent:]))
		}
	}
	b.WriteString("\n")
	fmt.Fprintf(&b, compactIndexGuidance, maxCollapseTokens/1000)
	b.WriteString("\n\nOne row per message, oldest first. id is the sequence_id, or s<n>.<i> for a note\n" +
		"from an earlier compaction; tokens are estimated; `trim <tool_use_id>` marks a\n" +
		"tool output you may trim.\n\n```\n")

	tw := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "id\trole\ttokens\tcontent")
	toolNames := map[string]string{}
	for i, it := range items {
		if i == recent && i > 0 {
			tw.Flush()
			b.WriteString("--- recent part: cannot be collapsed ---\n")
		}
		role := "note"
		switch {
		case it.source == nil:
		case it.message.Role == llm.MessageRoleAssistant:
			role = "assistant"
		default:
			role = "user"
		}
		fmt.Fprintf(tw, "%s\t%s\t%d\t%s\n", itemID(it), role, estimatePiMessageTokens(it.message), describeItem(it, toolNames))
	}
	tw.Flush()
	b.WriteString("```\n")
	return b.String()
}

// describeItem lists an item's blocks for the index. toolNames maps tool_use
// ids seen so far to tool names, so outputs can be named after their calls.
func describeItem(it contextItem, toolNames map[string]string) string {
	if it.source == nil {
		return "note " + quoteStart(it.note)
	}
	results := 0
	for _, c := range it.message.Content {
		if c.Type == llm.ContentTypeToolResult {
			results++
		}
	}
	var parts []string
	for _, c := range it.message.Content {
		switch c.Type {
		case llm.ContentTypeText:
			if c.MediaType != "" {
				parts = append(parts, "image")
			} else {
				parts = append(parts, "text "+quoteStart(c.Text))
			}
		case llm.ContentTypeThinking, llm.ContentTypeRedactedThinking:
			parts = append(parts, "thinking")
		case llm.ContentTypeToolUse:
			toolNames[c.ID] = c.ToolName
			parts = append(parts, c.ToolName+" call "+quoteStart(string(c.ToolInput)))
		case llm.ContentTypeToolResult:
			name := toolNames[c.ToolUseID]
			if name == "" {
				name = "tool"
			}
			part := name + " output"
			switch {
			case len(c.ToolResult) == 1 && strings.HasPrefix(c.ToolResult[0].Text, trimmedToolOutputPrefix):
				part += " (trimmed)"
			case results > 1:
				part += fmt.Sprintf(" (trim %s, ~%d)", c.ToolUseID, estimatePiMessageTokens(llm.Message{Content: []llm.Content{c}}))
			default:
				part += " (trim " + c.ToolUseID + ")"
			}
			parts = append(parts, part)
		}
	}
	return strings.Join(parts, ", ")
}

// quoteStart quotes the start of s on one line.
func quoteStart(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	const n = 60
	if r := []rune(s); len(r) > n {
		s = string(r[:n]) + "…"
	}
	return `"` + s + `"`
}

// contextNudger tells the agent the context size once it reaches at tokens,
// and again every compactNudgeStep beyond. A loop owns one; its level starts
// at the size the conversation already had, so a rebuilt loop does not
// repeat a nudge.
type contextNudger struct {
	mu     sync.Mutex
	at     uint64
	tokens uint64
	level  int
}

func newContextNudger(at int, tokens uint64) *contextNudger {
	if at <= 0 {
		at = defaultCompactNudgeTokens
	}
	n := &contextNudger{at: uint64(at), tokens: tokens}
	n.level = n.levelOf(tokens)
	return n
}

func (n *contextNudger) levelOf(tokens uint64) int {
	if tokens < n.at {
		return -1
	}
	return int((tokens - n.at) / compactNudgeStep)
}

// observe records the context size reported by a model response.
func (n *contextNudger) observe(usage llm.Usage) {
	if usage.IsZero() {
		return
	}
	n.mu.Lock()
	n.tokens = usage.ContextWindowUsed()
	n.mu.Unlock()
}

// take returns the nudge due now, if any. Dropping below a level (after a
// compaction) re-arms it.
func (n *contextNudger) take() (string, bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	level := n.levelOf(n.tokens)
	due := level > n.level
	n.level = level
	if !due {
		return "", false
	}
	return fmt.Sprintf("Context is %dk.", (n.tokens+5000)/10000*10), true
}

// recordContextNudge records text as a user message marked context_nudge.
func (cm *ConversationManager) recordContextNudge(ctx context.Context, text string) (llm.Message, error) {
	message := llm.Message{Role: llm.MessageRoleUser, Content: llm.TextContent(text)}
	created, err := cm.db.CreateMessage(ctx, db.CreateMessageParams{
		ConversationID: cm.conversationID,
		Type:           db.MessageTypeUser,
		LLMData:        message,
		UserData:       map[string]any{"context_nudge": true},
		UsageData:      llm.Usage{},
	})
	if err != nil {
		return message, fmt.Errorf("record context nudge: %w", err)
	}
	return message, cm.publishCreated(ctx, created)
}

// lastContextWindowSize returns the context size reported by the newest of
// rows (one generation's context rows) that carries usage.
func lastContextWindowSize(rows []generated.Message) uint64 {
	for i := len(rows) - 1; i >= 0; i-- {
		if n := calculateContextWindowSizeFromMsg(&rows[i]); n > 0 {
			return n
		}
	}
	return 0
}
