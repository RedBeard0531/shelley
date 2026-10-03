package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/llm"
	"shelley.exe.dev/llm/predictable"
)

// subagentDoneFixture sets up a parent conversation with an active manager and
// a child subagent conversation whose manager has the onDone callback wired up
// by getOrCreateSubagentConversationManager. It records a final assistant text
// message into the subagent's DB as the subagent's latest response.
type subagentDoneFixture struct {
	t        *testing.T
	server   *Server
	database *db.DB
	llmSvc   *predictable.Service

	parentID    string
	parentMgr   *ConversationManager
	subagentID  string
	subagentMgr *ConversationManager
	subSlug     string
	subResponse string
}

func newSubagentDoneFixture(t *testing.T, subResponse string) *subagentDoneFixture {
	t.Helper()
	server, database, ps := newTestServer(t)

	ctx := t.Context()

	// Parent conversation.
	parentConv, err := database.CreateConversation(ctx, nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatalf("create parent: %v", err)
	}

	parentMgr, err := server.getOrCreateConversationManager(ctx, parentConv.ConversationID, "")
	if err != nil {
		t.Fatalf("get parent manager: %v", err)
	}

	// Subagent conversation, parented to the above. Use CreateSubagentConversation
	// so ParentConversationID is set; that's what notifyParentSubagentIdle keys off.
	slug := "sub-test"
	subConv, err := database.CreateSubagentConversation(ctx, slug, parentConv.ConversationID, nil)
	if err != nil {
		t.Fatalf("create subagent conv: %v", err)
	}

	subagentMgr, err := server.getOrCreateSubagentConversationManager(ctx, subConv.ConversationID)
	if err != nil {
		t.Fatalf("get subagent manager: %v", err)
	}

	// Record a final assistant text message on the subagent so lastAssistantText
	// has something to read.
	assistantMsg := llm.Message{
		Role:      llm.MessageRoleAssistant,
		Content:   []llm.Content{{Type: llm.ContentTypeText, Text: subResponse}},
		EndOfTurn: true,
	}
	if err := server.recordMessage(ctx, subConv.ConversationID, assistantMsg, llm.Usage{}, nil); err != nil {
		t.Fatalf("record subagent assistant: %v", err)
	}

	return &subagentDoneFixture{
		t:           t,
		server:      server,
		database:    database,
		llmSvc:      ps,
		parentID:    parentConv.ConversationID,
		parentMgr:   parentMgr,
		subagentID:  subConv.ConversationID,
		subagentMgr: subagentMgr,
		subSlug:     slug,
		subResponse: subResponse,
	}
}

// parentMessages returns the list of persisted messages for the parent in DB
// order (ascending sequence id).
func (f *subagentDoneFixture) parentMessages() []generated.Message {
	f.t.Helper()
	var msgs []generated.Message
	err := f.database.Queries(context.Background(), func(q *generated.Queries) error {
		var qerr error
		msgs, qerr = q.ListMessages(context.Background(), f.parentID)
		return qerr
	})
	if err != nil {
		f.t.Fatalf("list parent messages: %v", err)
	}
	return msgs
}

// idleNotices returns the parent's user messages that announce this
// subagent went idle, attributed to it through sender user_data.
func (f *subagentDoneFixture) idleNotices() []generated.Message {
	f.t.Helper()
	var out []generated.Message
	for _, m := range f.parentMessages() {
		if m.Type != string(db.MessageTypeUser) || m.UserData == nil || m.LlmData == nil {
			continue
		}
		data, ok, err := parseSenderMessageUserData([]byte(*m.UserData))
		if err != nil || !ok || data.SenderConversationID != f.subagentID || data.SenderRelationship != senderRelationshipSubagent {
			continue
		}
		var msg llm.Message
		if err := json.Unmarshal([]byte(*m.LlmData), &msg); err != nil {
			f.t.Fatal(err)
		}
		if strings.Contains(messageText(msg), "is idle") {
			out = append(out, m)
		}
	}
	return out
}

// fireOnDone simulates the agent transitioning from working to not working
// (which is what triggers the onDone callback wired in convo.go's
// SetAgentWorking). We toggle through true->false to exercise the real path.
func (f *subagentDoneFixture) fireOnDone() {
	f.subagentMgr.SetAgentWorking(true)
	f.subagentMgr.SetAgentWorking(false)
}

func TestSubagentDone(t *testing.T) {
	t.Run("NotifiesParentWithoutCopyingResponse", testSubagentDone_NotifiesParentWithoutCopyingResponse)
	t.Run("CancellationDoesNotNotifyParent", testSubagentDone_CancellationDoesNotNotifyParent)
	t.Run("EvictedParentManagerStillNotified", testSubagentDone_EvictedParentManagerStillNotified)
}

// When a subagent's turn ends, the parent gets one attributed notice that the
// subagent is idle, and its turn starts. The subagent's reply is not copied.
func testSubagentDone_NotifiesParentWithoutCopyingResponse(t *testing.T) {
	f := newSubagentDoneFixture(t, "SECRET-RESULT-TEXT")
	defer stopActiveConversationLoops(f.server)

	f.fireOnDone()

	waitFor(t, 5*time.Second, func() bool { return len(f.idleNotices()) == 1 })
	for _, m := range f.parentMessages() {
		if m.LlmData != nil && strings.Contains(*m.LlmData, "SECRET-RESULT-TEXT") {
			t.Fatalf("subagent response was copied into the parent: %s", *m.LlmData)
		}
	}
	waitFor(t, 5*time.Second, func() bool {
		for _, req := range f.llmSvc.GetRecentRequests() {
			for _, msg := range req.Messages {
				if strings.Contains(messageText(msg), `<subagent_message conversation_id="`+f.subagentID+`" slug="sub-test">`) {
					return true
				}
			}
		}
		return false
	})
}

// Cancelling a subagent's in-flight turn (e.g. a resend to a busy subagent, or
// a user-initiated stop) records a synthetic "[Operation cancelled]"
// end-of-turn message that flips agentWorking→idle. That transition must NOT
// fire onDone: a cancellation is not a completion, and notifying the parent
// here produces a spurious subagent-done pair (and, when a resend's new turn
// later finishes, a duplicate). The cancelling guard keeps onDone quiet.
func testSubagentDone_CancellationDoesNotNotifyParent(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newSubagentDoneFixture(t, "Should never reach the parent.")
		defer stopActiveConversationLoops(f.server)

		// Bring the subagent's loop up so CancelConversation has something to tear
		// down (it returns early when loop==nil).
		if err := f.subagentMgr.ensureLoop(f.llmSvc, "predictable"); err != nil {
			t.Fatalf("ensureLoop subagent: %v", err)
		}
		f.subagentMgr.SetAgentWorking(true)

		before := len(f.parentMessages())
		if err := f.subagentMgr.CancelConversation(t.Context()); err != nil {
			t.Fatalf("CancelConversation: %v", err)
		}

		// Let any (erroneous) async notification land on the parent.
		synctest.Wait()
		if got := len(f.parentMessages()); got != before {
			t.Fatalf("cancellation added %d parent message(s); want 0", got-before)
		}

		// The subagent itself must be idle after cancel.
		if f.subagentMgr.IsAgentWorking() {
			t.Fatalf("subagent still working after CancelConversation")
		}
	})
}

// Cleanup may evict an idle parent's manager while its subagent works; the
// subagent's idle notice must recreate it rather than be dropped.
func testSubagentDone_EvictedParentManagerStillNotified(t *testing.T) {
	f := newSubagentDoneFixture(t, "Finished after the parent manager was evicted.")
	defer stopActiveConversationLoops(f.server)

	f.server.mu.Lock()
	delete(f.server.activeConversations, f.parentID)
	f.server.mu.Unlock()
	f.parentMgr.stopLoop()

	f.fireOnDone()

	waitFor(t, 5*time.Second, func() bool { return len(f.idleNotices()) == 1 })
	f.server.mu.Lock()
	_, ok := f.server.activeConversations[f.parentID]
	f.server.mu.Unlock()
	if !ok {
		t.Fatal("expected parent manager to be recreated in activeConversations")
	}
}

func TestManualSubagentTurnDoesNotNotifyParent(t *testing.T) {
	server, database, held, parent := newBtwTest(t)
	ctx := t.Context()
	parentManager, err := server.getOrCreateConversationManager(ctx, parent.ConversationID, "")
	if err != nil {
		t.Fatal(err)
	}
	child, err := database.CreateSubagentConversation(ctx, "manual-child", parent.ConversationID, nil)
	if err != nil {
		t.Fatal(err)
	}
	before := len(listMessages(t, database, parent.ConversationID))

	response := postBtwChat(t, server, child.ConversationID,
		ChatRequest{Message: "echo: manual child turn", Model: "predictable"})
	if response.Code != 202 {
		t.Fatalf("manual child turn status=%d body=%s", response.Code, response.Body.String())
	}
	server.mu.Lock()
	childManager := server.activeConversations[child.ConversationID]
	server.mu.Unlock()
	if childManager == nil {
		t.Fatal("manual child turn did not create a manager")
	}
	if childManager.onDone != nil {
		t.Fatal("generic child manager wired parent completion notification")
	}

	releaseAndWaitIdle(t, server, child.ConversationID, held.waitCall(t, "echo: manual child turn"))
	if got := len(listMessages(t, database, parent.ConversationID)); got != before {
		t.Fatalf("manual child turn injected %d parent messages", got-before)
	}
	if parentManager.IsAgentWorking() {
		t.Fatal("manual child turn started a parent turn")
	}
}

// Cleanup must never evict a conversation manager whose agent is mid-turn
// (agentWorking=true). Tool calls — e.g. a shell command that runs
// for many minutes — do not Touch the manager, so lastActivity goes
// stale even though the conversation is very much alive. Evicting it tears
// down the loop context mid-flight (cancelling in-flight tool calls and LLM
// requests) and orphans the turn.
func TestCleanupSkipsWorkingConversations(t *testing.T) {
	t.Parallel()
	server, database, _ := newTestServer(t)
	ctx := t.Context()

	mkStale := func(working bool) (string, *ConversationManager) {
		conv, err := database.CreateConversation(ctx, nil, true, nil, nil, db.ConversationOptions{})
		if err != nil {
			t.Fatalf("create conversation: %v", err)
		}
		mgr, err := server.getOrCreateConversationManager(ctx, conv.ConversationID, "")
		if err != nil {
			t.Fatalf("get manager: %v", err)
		}
		if working {
			mgr.SetAgentWorking(true)
		}
		mgr.mu.Lock()
		mgr.lastActivity = time.Now().Add(-time.Hour) // well past the 30-min cutoff
		mgr.mu.Unlock()
		return conv.ConversationID, mgr
	}

	workingID, _ := mkStale(true)
	idleID, _ := mkStale(false)

	server.Cleanup()

	server.mu.Lock()
	_, workingKept := server.activeConversations[workingID]
	_, idleKept := server.activeConversations[idleID]
	server.mu.Unlock()

	if !workingKept {
		t.Fatalf("Cleanup evicted a conversation whose agent is still working")
	}
	if idleKept {
		t.Fatalf("Cleanup kept a stale idle conversation; want evicted")
	}
}
