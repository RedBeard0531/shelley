package server

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"shelley.exe.dev/claudetool"
	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/llm"
	"shelley.exe.dev/llm/predictable"
)

// compactTestConversation is a conversation on a predictable model, driven
// through the chat endpoint.
type compactTestConversation struct {
	t        *testing.T
	srv      *Server
	database *db.DB
	ps       *predictable.Service
	id       string
}

func newCompactTestConversation(t *testing.T, opts db.ConversationOptions) *compactTestConversation {
	t.Helper()
	database, cleanup := setupTestDB(t)
	t.Cleanup(cleanup)
	ps := predictable.NewService()
	srv := NewServer(database, &twoModelLLMManager{service: ps},
		claudetool.ToolSetConfig{EnableBrowser: false},
		slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelWarn})),
		false, "model-a", "")
	srv.hooksDir = t.TempDir()
	srv.piDistillKeepRecentTokens = 1
	modelA := "model-a"
	conv, err := database.CreateConversation(t.Context(), nil, true, nil, &modelA, opts)
	if err != nil {
		t.Fatal(err)
	}
	return &compactTestConversation{t: t, srv: srv, database: database, ps: ps, id: conv.ConversationID}
}

func (c *compactTestConversation) endTurns() int {
	n := 0
	for _, m := range listMessages(c.t, c.database, c.id) {
		if m.Type == string(db.MessageTypeAgent) && m.LlmData != nil && strings.Contains(*m.LlmData, `"EndOfTurn":true`) {
			n++
		}
	}
	return n
}

func (c *compactTestConversation) turn(msg string) {
	c.t.Helper()
	before := c.endTurns()
	if w := postChat(c.t, c.srv, c.id, msg); w.Code != http.StatusAccepted {
		c.t.Fatalf("%q: got %d: %s", msg, w.Code, w.Body.String())
	}
	waitFor(c.t, 10*time.Second, func() bool { return c.endTurns() > before })
}

// rows returns the conversation's rows decoded as LLM messages, by sequence id.
func (c *compactTestConversation) rows() map[int64]llm.Message {
	out := map[int64]llm.Message{}
	for _, m := range listMessages(c.t, c.database, c.id) {
		if m.LlmData == nil {
			continue
		}
		var msg llm.Message
		if err := json.Unmarshal([]byte(*m.LlmData), &msg); err != nil {
			c.t.Fatal(err)
		}
		out[m.SequenceID] = msg
	}
	return out
}

// seqWith returns the sequence id of the first row whose LLM data contains s.
func (c *compactTestConversation) seqWith(s string) int64 {
	c.t.Helper()
	for _, m := range listMessages(c.t, c.database, c.id) {
		if m.LlmData != nil && strings.Contains(*m.LlmData, s) {
			return m.SequenceID
		}
	}
	c.t.Fatalf("no row containing %q", s)
	return 0
}

// lastToolOutput returns the text of the newest tool result.
func (c *compactTestConversation) lastToolOutput() (string, bool) {
	c.t.Helper()
	var text string
	var isErr bool
	for _, m := range listMessages(c.t, c.database, c.id) {
		if m.LlmData == nil {
			continue
		}
		var msg llm.Message
		if err := json.Unmarshal([]byte(*m.LlmData), &msg); err != nil {
			c.t.Fatal(err)
		}
		for _, ct := range msg.Content {
			if ct.Type == llm.ContentTypeToolResult {
				text, isErr = ct.ToolResult[0].Text, ct.ToolError
			}
		}
	}
	return text, isErr
}

func (c *compactTestConversation) records() int {
	n := 0
	for _, m := range listMessages(c.t, c.database, c.id) {
		if m.Type == string(db.MessageTypeInPlaceCompaction) {
			n++
		}
	}
	return n
}

func requestDump(req *llm.Request) string {
	var b strings.Builder
	for _, m := range req.Messages {
		j, _ := json.Marshal(m)
		b.Write(j)
		b.WriteString("\n")
	}
	return b.String()
}

// systemCardHasTool reports whether the system prompt card lists name.
func (c *compactTestConversation) systemCardHasTool(name string) bool {
	c.t.Helper()
	for _, m := range listMessages(c.t, c.database, c.id) {
		if m.Type == string(db.MessageTypeSystem) && m.DisplayData != nil {
			return strings.Contains(*m.DisplayData, `"name":"`+name+`"`)
		}
	}
	c.t.Fatal("no system prompt")
	return false
}

func hasTool(req *llm.Request, name string) bool {
	for _, tool := range req.Tools {
		if tool.Name == name {
			return true
		}
	}
	return false
}

func TestCompactInPlaceToolIsOptIn(t *testing.T) {
	t.Parallel()
	c := newCompactTestConversation(t, db.ConversationOptions{})
	c.turn("hello")
	if hasTool(c.ps.GetLastRequest(), claudetool.CompactInPlaceName) || c.systemCardHasTool(claudetool.CompactInPlaceName) {
		t.Fatal("compact_in_place offered without being enabled")
	}
}

// TestCompactInPlaceEndToEnd drives the tool through a conversation: the
// index lists the view, invalid requests come back as tool errors, and a valid
// one compacts the history the rest of the same turn, and later turns, see.
func TestCompactInPlaceEndToEnd(t *testing.T) {
	t.Parallel()
	c := newCompactTestConversation(t, db.ConversationOptions{ToolOverrides: map[string]string{claudetool.CompactInPlaceName: "on"}})
	c.turn("hello")
	c.turn("bash: echo FIRST_OUTPUT")
	c.turn("bash: echo SECOND_OUTPUT")
	if !hasTool(c.ps.GetLastRequest(), claudetool.CompactInPlaceName) || !c.systemCardHasTool(claudetool.CompactInPlaceName) {
		t.Fatal("compact_in_place not offered, or missing from the system prompt card")
	}

	helloSeq := c.seqWith(`"Text":"hello"`)
	hiSeq := c.seqWith("Well, hi there!")
	firstResultSeq := c.seqWith(`FIRST_OUTPUT\n`)
	firstUseID := c.rows()[firstResultSeq].Content[0].ToolUseID

	c.turn(`compact_in_place: {"action":"index"}`)
	index, isErr := c.lastToolOutput()
	if isErr {
		t.Fatalf("index failed: %s", index)
	}
	for _, want := range []string{
		"## Index",
		"dead ends",
		fmt.Sprintf("%d  ", helloSeq),
		"bash output (trim " + firstUseID + ")",
		"--- recent part: cannot be collapsed ---",
	} {
		if !strings.Contains(index, want) {
			t.Fatalf("index missing %q:\n%s", want, index)
		}
	}

	compact := func(trim []string, collapse ...claudetool.CompactCollapse) {
		t.Helper()
		in, _ := json.Marshal(claudetool.CompactInPlaceInput{Action: "compact", Trim: trim, Collapse: collapse})
		c.turn("compact_in_place: " + string(in))
	}
	seq := func(n int64) claudetool.IndexID { return claudetool.IndexID(fmt.Sprint(n)) }
	for want, collapse := range map[string]claudetool.CompactCollapse{
		"unknown id":          {From: "999999", To: seq(hiSeq), Note: "x"},
		"from comes after to": {From: seq(hiSeq), To: seq(helloSeq), Note: "x"},
		"note is not shorter": {From: seq(helloSeq), To: seq(hiSeq), Note: strings.Repeat("long ", 100)},
	} {
		compact(nil, collapse)
		if out, isErr := c.lastToolOutput(); !isErr || !strings.Contains(out, want) {
			t.Fatalf("want error %q, got %q (error %v)", want, out, isErr)
		}
	}
	compact([]string{"nope"})
	if out, isErr := c.lastToolOutput(); !isErr || !strings.Contains(out, "no tool output") {
		t.Fatalf("bad trim: %q", out)
	}
	if n := c.records(); n != 0 {
		t.Fatalf("invalid requests recorded %d compactions", n)
	}

	compact([]string{firstUseID}, claudetool.CompactCollapse{From: seq(helloSeq), To: seq(hiSeq), Note: "GREETED"})
	if out, isErr := c.lastToolOutput(); isErr || !strings.Contains(out, "Compacted: 1 collapsed, 1 trimmed") {
		t.Fatalf("compact: %q", out)
	}
	if n := c.records(); n != 1 {
		t.Fatalf("records = %d", n)
	}

	// The request that followed the tool call, in the same turn, already
	// sees the compacted history, ending with the compact call's output.
	check := func(req *llm.Request) {
		t.Helper()
		got := requestDump(req)
		if first := req.Messages[0].Content[0].Text; first != compactionNoteText(c.id, helloSeq, hiSeq, "GREETED") {
			t.Fatalf("first message is not the note:\n%s", got)
		}
		for _, m := range req.Messages {
			for _, ct := range m.Content {
				if ct.Text == "Well, hi there!" || ct.ToolUseID == firstUseID && ct.ToolResult[0].Text == "FIRST_OUTPUT\n" {
					t.Fatalf("compacted content leaked:\n%s", got)
				}
			}
		}
		if !strings.Contains(got, trimmedToolOutputText(c.id, firstResultSeq)) || !strings.Contains(got, `SECOND_OUTPUT\n`) {
			t.Fatalf("want the first output trimmed and the second intact:\n%s", got)
		}
		if n := strings.Count(got, "Compacted: 1 collapsed"); n != 1 {
			t.Fatalf("compact output appears %d times:\n%s", n, got)
		}
		// Earlier compact_in_place calls (the index, failed attempts) are
		// hidden; only the successful call remains, as a receipt.
		if strings.Contains(got, "## Index") || strings.Contains(got, "unknown id") {
			t.Fatalf("earlier compact_in_place calls not hidden:\n%s", got)
		}
		if n := strings.Count(got, `"ToolName":"compact_in_place"`); n != 1 {
			t.Fatalf("%d compact_in_place calls in context, want 1:\n%s", n, got)
		}
	}
	check(c.ps.GetLastRequest())

	// A later turn on the same loop sees the same history.
	c.turn("echo: AFTER")
	check(c.ps.GetLastRequest())

	// And so does a loop rebuilt from the database.
	manager, err := c.srv.getOrCreateConversationManager(t.Context(), c.id, "")
	if err != nil {
		t.Fatal(err)
	}
	manager.ResetLoop()
	c.turn("echo: REBUILT")
	check(c.ps.GetLastRequest())

	// The next index shows the note and the trimmed output.
	c.turn(`compact_in_place: {"action":"index"}`)
	index, _ = c.lastToolOutput()
	if !strings.Contains(index, `note "GREETED"`) || !strings.Contains(index, "bash output (trimmed)") {
		t.Fatalf("index after compaction:\n%s", index)
	}
}

// TestCompactInPlaceNudge: with the tool enabled, the agent is told the
// context size once it reaches the threshold, and not again until it grows a
// step further.
func TestCompactInPlaceNudge(t *testing.T) {
	t.Parallel()
	c := newCompactTestConversation(t, db.ConversationOptions{
		ToolOverrides:      map[string]string{claudetool.CompactInPlaceName: "on"},
		CompactNudgeTokens: 1,
	})
	nudges := func() []string {
		var out []string
		for _, m := range listMessages(t, c.database, c.id) {
			if m.UserData != nil && strings.Contains(*m.UserData, `"context_nudge":true`) {
				out = append(out, *m.LlmData)
			}
		}
		return out
	}
	c.turn("hello")
	if n := len(nudges()); n != 0 {
		t.Fatalf("nudged before any usage: %d", n)
	}
	c.turn("echo: two")
	got := nudges()
	if len(got) != 1 || !strings.Contains(got[0], `"Text":"Context is 0k."`) {
		t.Fatalf("nudges = %v", got)
	}
	if !strings.Contains(requestDump(c.ps.GetLastRequest()), "Context is 0k.") {
		t.Fatal("nudge not sent to the model")
	}
	c.turn("echo: three")
	if n := len(nudges()); n != 1 {
		t.Fatalf("nudged again below the next step: %d", n)
	}

	// A compaction hides the nudges from the model.
	c.turn("bash: echo OUTPUT")
	useID := c.rows()[c.seqWith(`OUTPUT\n`)].Content[0].ToolUseID
	c.turn(`compact_in_place: {"action":"compact","trim":["` + useID + `"]}`)
	if out, isErr := c.lastToolOutput(); isErr {
		t.Fatalf("compact: %s", out)
	}
	if got := requestDump(c.ps.GetLastRequest()); strings.Contains(got, "Context is") {
		t.Fatalf("nudge still in context after compaction:\n%s", got)
	}

	// The response that makes a compact call crosses the next nudge level
	// (here, as its input carries a long message). Right after the
	// compaction that size is stale: no nudge.
	c.turn("bash: echo AGAIN")
	useID = c.rows()[c.seqWith(`AGAIN\n`)].Content[0].ToolUseID
	size := lastContextWindowSize(listMessages(t, c.database, c.id))
	if err := c.database.UpdateConversationOptions(t.Context(), c.id, db.ConversationOptions{
		ToolOverrides:      map[string]string{claudetool.CompactInPlaceName: "on"},
		CompactNudgeTokens: int(size) + 100,
	}); err != nil {
		t.Fatal(err)
	}
	manager, err := c.srv.getOrCreateConversationManager(t.Context(), c.id, "")
	if err != nil {
		t.Fatal(err)
	}
	manager.ResetLoop()
	before := len(nudges())
	c.turn(`compact_in_place: {"action":"compact","trim":["` + useID + `"]}` + strings.Repeat(" ", 2000))
	if out, isErr := c.lastToolOutput(); isErr {
		t.Fatalf("compact: %s", out)
	}
	if got := nudges(); len(got) != before {
		t.Fatalf("nudged with the size from before the compaction: %v", got[before:])
	}
}

func TestContextNudger(t *testing.T) {
	n := newContextNudger(0, 170_000)
	if _, ok := n.take(); ok {
		t.Fatal("a fresh nudger repeats the level the conversation already had")
	}
	observe := func(tokens uint64) { n.observe(llm.Usage{InputTokens: tokens}) }
	for _, step := range []struct {
		tokens uint64
		want   string
	}{
		{200_000, ""},
		{212_000, "Context is 210k."},
		{230_000, ""},
		{90_000, ""}, // compacted: re-arms
		{163_000, "Context is 160k."},
		{320_000, "Context is 320k."},
	} {
		observe(step.tokens)
		if got, _ := n.take(); got != step.want {
			t.Fatalf("at %d: got %q, want %q", step.tokens, got, step.want)
		}
	}
}

func callItem(seq int64, id string) contextItem {
	return contextItem{from: seq, to: seq, source: &generated.Message{SequenceID: seq, Type: string(db.MessageTypeAgent)}, message: llm.Message{Role: llm.MessageRoleAssistant, Content: []llm.Content{
		{Type: llm.ContentTypeToolUse, ID: id, ToolName: "bash"},
	}}}
}

func outputItem(seq int64, id, text string) contextItem {
	// Like a stored tool output, which is a user message that carries only results.
	return contextItem{from: seq, to: seq, source: &generated.Message{SequenceID: seq, Type: string(db.MessageTypeUser)}, message: llm.Message{Role: llm.MessageRoleUser, Content: []llm.Content{
		{Type: llm.ContentTypeToolResult, ToolUseID: id, ToolResult: []llm.Content{{Type: llm.ContentTypeText, Text: text}}},
	}}}
}

// TestBuildCompactionKeepsCallsWithOutputs: a range may neither start on a
// tool output nor end on a tool call, and the other problems of the request
// are reported with it.
func TestBuildCompactionKeepsCallsWithOutputs(t *testing.T) {
	out := strings.Repeat("x", 400)
	items := []contextItem{
		textItem(1, llm.MessageRoleUser, "do it"),
		callItem(2, "a"), outputItem(3, "a", out),
		callItem(4, "b"), outputItem(5, "b", out),
		textItem(6, llm.MessageRoleAssistant, "done"),
		textItem(7, llm.MessageRoleAssistant, strings.Repeat("x", 4*25_000)),
	}
	col := func(from, to claudetool.IndexID, note string) claudetool.CompactCollapse {
		return claudetool.CompactCollapse{From: from, To: to, Note: note}
	}
	_, err := buildCompaction(items, 20_000, claudetool.CompactInPlaceInput{Collapse: []claudetool.CompactCollapse{
		col("3", "6", "n"), col("4", "4", "n"), col("2", "3", ""), col("3", "4", "n"),
	}})
	want := "4 problems. Nothing was compacted; fix them and send the whole request again:\n" +
		"- collapse 3-6: starts with a tool output, which must stay with its call; start at the call before it, or after the output\n" +
		"- collapse 4-4: ends with a tool call, which must stay with its output; extend it to include the output, or end before the call\n" +
		"- collapse 2-3: note is empty\n" +
		"- collapse 3-4: starts with a tool output"
	if err == nil || !strings.HasPrefix(err.Error(), want) {
		t.Fatalf("got %v", err)
	}
	c, err := buildCompaction(items, 20_000, claudetool.CompactInPlaceInput{Collapse: []claudetool.CompactCollapse{col("2", "3", "n"), col("4", "6", "n")}})
	if err != nil || len(c.Squishes) != 2 {
		t.Fatalf("%+v, %v", c, err)
	}
}

func TestBuildCompaction(t *testing.T) {
	big := strings.Repeat("x", 4*15_000) // ~15k tokens
	items := []contextItem{
		textItem(1, llm.MessageRoleUser, big),
		textItem(2, llm.MessageRoleAssistant, big),
		textItem(3, llm.MessageRoleUser, "a"),
		textItem(4, llm.MessageRoleAssistant, "b"),
		textItem(5, llm.MessageRoleUser, "c"),
		textItem(6, llm.MessageRoleAssistant, big+big),
	}
	// The recent part is the last message.
	if r := recentStart(items, 20_000); r != 5 {
		t.Fatalf("recent start = %d", r)
	}
	col := func(from, to claudetool.IndexID) claudetool.CompactCollapse {
		return claudetool.CompactCollapse{From: from, To: to, Note: "n"}
	}
	for want, in := range map[string]claudetool.CompactInPlaceInput{
		"nothing to compact": {},
		"recent part":        {Collapse: []claudetool.CompactCollapse{col("5", "6")}},
		"at most":            {Collapse: []claudetool.CompactCollapse{col("1", "3")}},
		"overlaps":           {Collapse: []claudetool.CompactCollapse{col("2", "4"), col("4", "5")}},
		"note is empty":      {Collapse: []claudetool.CompactCollapse{{From: "3", To: "4"}}},
	} {
		if _, err := buildCompaction(items, 20_000, in); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("want error %q, got %v", want, err)
		}
	}
	// Every problem is reported at once, and nothing is compacted.
	_, err := buildCompaction(items, 20_000, claudetool.CompactInPlaceInput{
		Trim:     []string{"nope"},
		Collapse: []claudetool.CompactCollapse{col("99", "2"), col("1", "3")},
	})
	want := "3 problems. Nothing was compacted; fix them and send the whole request again:\n" +
		"- trim nope: no tool output with that id in the index\n" +
		"- collapse 99-2: unknown id; use ids from the index\n" +
		"- collapse 1-3: covers ~"
	if err == nil || !strings.HasPrefix(err.Error(), want) {
		t.Errorf("want error starting %q, got %v", want, err)
	}
	// Over the limit is fine for a single message or call-and-output pair.
	c, err := buildCompaction(items, 20_000, claudetool.CompactInPlaceInput{Collapse: []claudetool.CompactCollapse{col("3", "5"), col("1", "2")}})
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Squishes) != 2 || c.Squishes[0].FromSequenceID != 1 || c.Squishes[0].ToSequenceID != 2 || c.Squishes[1].ToSequenceID != 5 {
		t.Fatalf("squishes = %+v", c.Squishes)
	}
}

func TestHideItems(t *testing.T) {
	use := func(seq int64, ids ...string) contextItem {
		it := textItem(seq, llm.MessageRoleAssistant, "calling")
		for _, id := range ids {
			it.message.Content = append(it.message.Content, llm.Content{Type: llm.ContentTypeToolUse, ID: id})
		}
		return it
	}
	result := func(seq int64, ids ...string) contextItem {
		it := contextItem{from: seq, to: seq, source: &generated.Message{SequenceID: seq}, message: llm.Message{Role: llm.MessageRoleUser}}
		for _, id := range ids {
			it.message.Content = append(it.message.Content, llm.Content{Type: llm.ContentTypeToolResult, ToolUseID: id})
		}
		return it
	}
	items := []contextItem{
		textItem(1, llm.MessageRoleUser, "go"),
		use(2, "compact"),
		result(3, "compact"),
		textItem(4, llm.MessageRoleUser, "Context is 160k."),
		use(5, "compact2", "bash"),
		result(6, "compact2", "bash"),
	}
	got, err := hideItems(slices.Clone(items), []int64{4}, []string{"compact", "compact2"})
	if err != nil {
		t.Fatal(err)
	}
	var spans []string
	for _, it := range got {
		spans = append(spans, fmt.Sprintf("%d:%d", it.from, len(it.message.Content)))
	}
	// The parallel bash call and its result stay; so does the text beside it.
	if s := strings.Join(spans, " "); s != "1:1 5:2 6:1" {
		t.Fatalf("got %s", s)
	}
	if len(items[4].message.Content) != 3 {
		t.Fatal("input message modified")
	}
	for _, bad := range [][]string{{"missing"}, {"compact", "compact"}} {
		if _, err := hideItems(slices.Clone(items), nil, bad); err == nil {
			t.Errorf("hide %v: want error", bad)
		}
	}
}
