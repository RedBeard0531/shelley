package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
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
// lists the older part of the LLM's current view, "compact" records an
// in-place compaction (collapsing message ranges into notes, trimming tool
// outputs) and the turn continues on the compacted history. While the tool is
// enabled the agent is also nudged with the context size (see contextNudger),
// unless it was enabled mid-conversation (see EnableCompactInPlace).

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
	return compactIndex(items, c.cm.keepRecentTokens)
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

var errCompactInPlaceUnavailable = errors.New("compact_in_place is not available in this conversation")

// errCompactInPlaceAlreadyOn ends EnableCompactInPlace's mutation early,
// leaving the loop alone.
var errCompactInPlaceAlreadyOn = errors.New("compact_in_place is already on")

// EnableCompactInPlace turns compact_in_place on in a conversation started
// without it, for the Compact in Place button; the agent is not nudged (see
// DisableCompactNudges). Tool definitions are not in the log: every loop
// builds them from the conversation options. So this updates the options,
// drops the loop so the next turn is built with the tool, and appends a
// marker that says where it appeared. Enabling it twice is a no-op.
//
// It returns errAgentWorking mid-turn: dropping the loop would cut the turn
// short.
func (cm *ConversationManager) EnableCompactInPlace(ctx context.Context) error {
	if cm.role == roleBtwReader {
		// Its loop has a fixed toolset; see ensureLoop.
		return errCompactInPlaceUnavailable
	}
	// Serializes with ApplyModelSettings, which writes back the in-memory
	// options it read.
	cm.modelSettingsMu.Lock()
	defer cm.modelSettingsMu.Unlock()
	err := cm.resetLoopAfter(true, func() error {
		if cm.IsAgentWorking() {
			return errAgentWorking
		}
		opts, changed, err := cm.db.ModifyConversationOptions(ctx, cm.conversationID, func(o *db.ConversationOptions) bool {
			if claudetool.IsToolEnabled(claudetool.CompactInPlaceName, o.ToolOverrides, o.DisableAllTools) {
				return false
			}
			if o.ToolOverrides == nil {
				o.ToolOverrides = map[string]string{}
			}
			o.ToolOverrides[claudetool.CompactInPlaceName] = "on"
			o.DisableCompactNudges = true
			return true
		})
		if err != nil {
			return err
		}
		if !changed {
			return errCompactInPlaceAlreadyOn
		}
		cm.mu.Lock()
		cm.conversationOptions = opts
		cm.mu.Unlock()
		return nil
	})
	if errors.Is(err, errCompactInPlaceAlreadyOn) {
		return nil
	}
	if err != nil {
		return err
	}
	// The tool is on whether or not the marker lands, and a retry would find
	// nothing to do. So don't fail the caller: the cost is a log that doesn't
	// show where the tool appeared.
	if err := cm.recordModelChangeMarker(ctx, ModelChangeUserData{
		ToolEnabled: claudetool.CompactInPlaceName,
		Text:        "Enabled the compact_in_place tool.",
	}); err != nil {
		cm.logger.Error("compact_in_place enabled, but its marker was not recorded",
			"conversationID", cm.conversationID, "error", err)
	}
	return nil
}

// handleEnableCompactInPlace handles POST
// /conversation/<id>/enable-compact-in-place: in a conversation started
// without compact_in_place, the Compact in Place button enables the tool
// before asking the agent to use it.
func (s *Server) handleEnableCompactInPlace(w http.ResponseWriter, r *http.Request, conversationID string) {
	ctx := r.Context()
	conversation, err := s.db.GetConversationByID(ctx, conversationID)
	if err != nil {
		http.Error(w, "Conversation not found", http.StatusNotFound)
		return
	}
	if conversation.Archived {
		http.Error(w, "conversation is archived", http.StatusConflict)
		return
	}
	// A draft's options still travel with its first send.
	if conversation.IsDraft {
		http.Error(w, "conversation is still a draft", http.StatusConflict)
		return
	}
	userEmail := r.Header.Get("X-ExeDev-Email")
	ctx = contextWithUserEmail(ctx, userEmail)
	manager, err := s.getOrCreateConversationManager(ctx, conversationID, userEmail)
	if err != nil {
		s.internalError(w, "Failed to get conversation manager", err, "conversationID", conversationID)
		return
	}
	if err := manager.EnableCompactInPlace(ctx); err != nil {
		switch {
		case errors.Is(err, errAgentWorking):
			http.Error(w, "Finish or stop the current turn to compact in place", http.StatusConflict)
		case errors.Is(err, errCompactInPlaceUnavailable):
			http.Error(w, err.Error(), http.StatusConflict)
		default:
			s.internalError(w, "Failed to enable compact_in_place", err, "conversationID", conversationID)
		}
		return
	}
	updated, err := s.db.GetConversationByID(ctx, conversationID)
	if err != nil {
		s.internalError(w, "Failed to reload conversation", err, "conversationID", conversationID)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(updated)
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
// against the compaction view of history and its recent part. It reports
// every problem at once.
func buildCompaction(history []contextItem, keepRecentTokens int, in claudetool.CompactInPlaceInput) (db.InPlaceCompaction, error) {
	var c db.InPlaceCompaction
	if len(in.Trim) == 0 && len(in.Collapse) == 0 {
		return c, fmt.Errorf("nothing to compact: give trim and/or collapse")
	}
	items, err := compactionView(history)
	if err != nil {
		return c, err
	}
	alone, err := aloneRows(items)
	if err != nil {
		return c, err
	}
	recent := recentStart(items, keepRecentTokens)
	var problems []string
	for _, id := range in.Trim {
		i, ok := findToolResult(items, id)
		switch {
		case !ok:
			problems = append(problems, fmt.Sprintf("trim %s: no tool output with that id in the index", id))
		case i >= recent:
			problems = append(problems, fmt.Sprintf("trim %s: in the recent part, which is kept as is", id))
		case slices.ContainsFunc(c.Trims, func(t db.CompactionTrim) bool { return t.ToolUseID == id }):
			problems = append(problems, fmt.Sprintf("trim %s: listed twice", id))
		default:
			c.Trims = append(c.Trims, db.CompactionTrim{SequenceID: items[i].from, ToolUseID: id})
		}
	}

	type span struct{ i, j int }
	var spans []span
	for _, col := range in.Collapse {
		name := fmt.Sprintf("collapse %s-%s", col.From, col.To)
		problem := func(format string, args ...any) {
			problems = append(problems, name+": "+fmt.Sprintf(format, args...))
		}
		i, j := indexOfIndexID(items, col.From), indexOfIndexID(items, col.To)
		switch {
		case i < 0 || j < 0:
			problem("unknown id; use ids from the index")
			continue
		case j < i:
			problem("from comes after to")
			continue
		case j >= recent:
			problem("reaches into the recent part (from %s on), which is kept as is", itemID(items[recent]))
			continue
		case hasContent(items[i].message, llm.ContentTypeToolResult):
			problem("starts with a tool output, which must stay with its call; start at the call before it, or after the output")
			continue
		case hasContent(items[j].message, llm.ContentTypeToolUse):
			problem("ends with a tool call, which must stay with its output; extend it to include the output, or end before the call")
			continue
		case slices.ContainsFunc(spans, func(s span) bool { return i <= s.j && s.i <= j }):
			problem("overlaps another collapse")
			continue
		}
		// A row that can only be collapsed on its own may not share a range.
		if k := slices.Index(alone[i:j+1], true); k >= 0 && j > i {
			instead := "collapse each of those rows on its own"
			if r := suggestRanges(items, alone, i, j); r != "" {
				instead = "collapse e.g. " + r + " instead"
			}
			problem("covers %s, which can only be collapsed on its own; %s", itemID(items[i+k]), instead)
			continue
		}
		// The range is sound; whatever else is wrong with it, it is taken.
		spans = append(spans, span{i, j})
		tokens := itemTokens(items[i : j+1])
		switch {
		case strings.TrimSpace(col.Note) == "":
			problem("note is empty")
			continue
		case tokens > maxCollapseTokens && j-i > 1:
			problem("covers ~%d tokens; at most ~%d per collapse unless it is one tool call and its output, so split it", tokens, maxCollapseTokens)
			continue
		case (len(col.Note)+3)/4 >= tokens:
			problem("note is not shorter than the ~%d tokens it replaces", tokens)
			continue
		}
		c.Squishes = append(c.Squishes, db.CompactionSquish{FromSequenceID: items[i].from, ToSequenceID: items[j].to, Summary: col.Note})
	}
	switch len(problems) {
	case 0:
	case 1:
		return c, fmt.Errorf("%s. Nothing was compacted; fix this and send the whole request again", problems[0])
	default:
		return c, fmt.Errorf("%d problems. Nothing was compacted; fix them and send the whole request again:\n- %s", len(problems), strings.Join(problems, "\n- "))
	}
	// Squishes apply in order, each to the view the previous ones left; they
	// are disjoint, so order them as they appear.
	slices.SortFunc(c.Squishes, func(a, b db.CompactionSquish) int { return int(a.FromSequenceID - b.FromSequenceID) })
	return c, nil
}

// collapsesAlone reports whether a collapse may cover it only on its own: a
// message from the user (or the parent agent), or a note or summary from an
// earlier compaction. They carry the task and its rules, and the agent
// tends to fold them into notes that keep little of them.
func collapsesAlone(it contextItem) (bool, error) {
	switch {
	case it.source == nil:
		return true, nil
	case it.source.Type != string(db.MessageTypeUser) || isToolResultMessage(it.message):
		return false, nil
	case it.source.UserData == nil:
		return true, nil
	}
	tag, _, err := provenanceTag([]byte(*it.source.UserData))
	if err != nil {
		return false, fmt.Errorf("message %s: %w", itemID(it), err)
	}
	return tag == "" || tag == "parent_message", nil
}

// aloneRows applies collapsesAlone to items.
func aloneRows(items []contextItem) ([]bool, error) {
	alone := make([]bool, len(items))
	for i, it := range items {
		var err error
		if alone[i], err = collapsesAlone(it); err != nil {
			return nil, err
		}
	}
	return alone, nil
}

// suggestRanges lists the ranges of items[i..j] between the rows that can
// only be collapsed on their own.
func suggestRanges(items []contextItem, alone []bool, i, j int) string {
	var ranges []string
	start := -1
	for k := i; k <= j+1; k++ {
		switch {
		case k <= j && !alone[k]:
			if start < 0 {
				start = k
			}
		case start >= 0:
			r := itemID(items[start])
			if start < k-1 {
				r += "-" + itemID(items[k-1])
			}
			ranges = append(ranges, r)
			start = -1
		}
	}
	return strings.Join(ranges, ", ")
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

// compactionView returns history as the agent can compact it: without the
// evidence of compacting (see compactionEvidence), which the compaction
// hides anyway, and without the call now running, which has no output yet.
// Neither moves the recent part while the agent works.
func compactionView(history []contextItem) ([]contextItem, error) {
	seqs, toolUseIDs := compactionEvidence(history)
	view, err := hideItems(slices.Clone(history), seqs, toolUseIDs)
	if err != nil {
		return nil, err
	}
	// Like hideItems, remove only the call: other calls in its message stay,
	// and a message left without a call goes.
	for i := range view {
		content := slices.DeleteFunc(slices.Clone(view[i].message.Content), func(c llm.Content) bool {
			return c.Type == llm.ContentTypeToolUse && c.ToolName == claudetool.CompactInPlaceName
		})
		if len(content) == len(view[i].message.Content) {
			continue
		}
		view[i].message.Content = content
		if !hasContent(view[i].message, llm.ContentTypeToolUse) {
			view[i].message.Content = nil
		}
	}
	return slices.DeleteFunc(view, func(it contextItem) bool { return len(it.message.Content) == 0 }), nil
}

// recentStart returns the index of the first item of the recent part, which
// is kept as is: about keepRecentTokens of the newest history, starting on a
// message that is not a tool result.
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
one tool call and its output. Rows marked * are messages from the user or the
parent and notes and summaries from earlier compactions: they carry the task
and its rules, so a range may not cover one unless it is just that row.`

// compactIndex renders the index the agent compacts from.
func compactIndex(history []contextItem, keepRecentTokens int) (string, error) {
	items, err := compactionView(history)
	if err != nil {
		return "", err
	}
	alone, err := aloneRows(items)
	if err != nil {
		return "", err
	}
	recent := recentStart(items, keepRecentTokens)
	var b strings.Builder
	fmt.Fprintf(&b, "## Index\n\n%d messages, ~%d tokens.\n", len(items), itemTokens(items))
	if recent == 0 {
		b.WriteString("All of it is recent, so nothing can be compacted yet.\n")
		return b.String(), nil
	}
	fmt.Fprintf(&b, "The recent part, from %s on (%d messages, ~%d tokens), is kept as is and not listed.\n",
		itemID(items[recent]), len(items)-recent, itemTokens(items[recent:]))
	b.WriteString("\n")
	fmt.Fprintf(&b, compactIndexGuidance, maxCollapseTokens/1000)
	b.WriteString("\n\nOne row per message, oldest first. id is the sequence_id, or s<n>.<i> for a note\n" +
		"from an earlier compaction; tokens are estimated; `trim <tool_use_id>` marks a\n" +
		"tool output you may trim.\n\n```\n")

	tw := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "id\trole\ttokens\tcontent")
	toolNames := map[string]string{}
	for i, it := range items[:recent] {
		role := "note"
		switch {
		case it.source == nil:
		case it.message.Role == llm.MessageRoleAssistant:
			role = "assistant"
		default:
			role = "user"
		}
		if alone[i] {
			role += "*"
		}
		fmt.Fprintf(tw, "%s\t%s\t%d\t%s\n", itemID(it), role, estimatePiMessageTokens(it.message), describeItem(it, toolNames))
	}
	tw.Flush()
	b.WriteString("```\n")
	return b.String(), nil
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
