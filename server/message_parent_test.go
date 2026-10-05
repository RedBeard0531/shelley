package server

import (
	"encoding/json"
	"testing"
	"time"

	"shelley.exe.dev/db"
	"shelley.exe.dev/llm"
)

// A subagent's message_parent call lands in the parent as a user message
// attributed to the subagent: stored raw with sender user_data, and shown to
// the parent model wrapped in <subagent_message>. Only the subagent has the
// tool.
func TestMessageParentTool(t *testing.T) {
	server, database, _ := newTestServer(t)
	held := newHeldLLMService()
	server.llmManager = &testLLMManager{service: held}
	t.Cleanup(func() { stopActiveConversationLoops(server) })

	parent, err := database.CreateConversation(t.Context(), nil, true, nil, strPtr("predictable"), db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	child, err := database.CreateSubagentConversation(t.Context(), "scout", parent.ConversationID, nil)
	if err != nil {
		t.Fatal(err)
	}

	prompt := "message_parent: halfway <there>"
	if _, err := NewSubagentRunner(server).RunSubagent(t.Context(), child.ConversationID, prompt, "predictable", ""); err != nil {
		t.Fatal(err)
	}
	call := held.waitCall(t, prompt)
	if !requestHasTool(call.request, "message_parent") {
		t.Fatal("subagent request lacks message_parent tool")
	}
	call.Release()

	wrapped := `<subagent_message conversation_id="` + child.ConversationID + `" slug="scout">` +
		"\nhalfway &lt;there&gt;\n</subagent_message>"
	parentCall := held.waitCall(t, wrapped)
	if requestHasTool(parentCall.request, "message_parent") {
		t.Fatal("top-level parent request has message_parent tool")
	}
	message := userMessageContaining(t, database, parent.ConversationID, "halfway <there>")
	if message == nil {
		t.Fatal("parent has no stored message from the subagent")
	}
	if got := messageText(storedLLMMessage(t, message)); got != "halfway <there>" {
		t.Fatalf("stored text = %q, want raw text", got)
	}
	assertSenderSource(t, storedUserData(t, message), child.ConversationID, "scout", "subagent", "halfway <there>")
}

// A final message_parent report must stop the child without another model
// round or a second completion message. The parent's copy is the report.
func TestMessageParentFinalEndsSubagentTurn(t *testing.T) {
	server, database, _ := newTestServer(t)
	held := newHeldLLMService()
	server.llmManager = &testLLMManager{service: held}
	t.Cleanup(func() { stopActiveConversationLoops(server) })

	parent, err := database.CreateConversation(t.Context(), nil, true, nil, strPtr("predictable"), db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	child, err := database.CreateSubagentConversation(t.Context(), "scout-final", parent.ConversationID, nil)
	if err != nil {
		t.Fatal(err)
	}
	prompt := "message_parent_final: finished task"
	if _, err := NewSubagentRunner(server).RunSubagent(t.Context(), child.ConversationID, prompt, "predictable", ""); err != nil {
		t.Fatal(err)
	}
	call := held.waitCall(t, prompt)
	if !requestHasTool(call.request, "message_parent") {
		t.Fatal("child lacks message_parent")
	}
	call.Release()

	wrapped := `<subagent_message conversation_id="` + child.ConversationID + `" slug="scout-final">` +
		"\nfinished task\n</subagent_message>"
	parentCall := held.waitCall(t, wrapped)
	childMgr, err := server.getOrCreateConversationManager(t.Context(), child.ConversationID, "")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-childMgr.idle():
	case <-time.After(5 * time.Second):
		t.Fatal("end_turn=true did not finish the child turn")
	}
	parentCall.Release()

	endCount := 0
	for _, m := range listMessages(t, database, child.ConversationID) {
		if m.Type == string(db.MessageTypeAgent) && storedLLMMessage(t, &m).EndOfTurn {
			endCount++
		}
	}
	if endCount != 1 {
		t.Fatalf("child end-of-turn markers = %d, want 1", endCount)
	}
	parentConv, err := database.GetConversationByID(t.Context(), parent.ConversationID)
	if err != nil {
		t.Fatal(err)
	}
	if parentConv.QueuedMessages != "[]" {
		t.Fatalf("final report also queued a completion notice: %s", parentConv.QueuedMessages)
	}
}

// A progress report does not finish the delegated turn. If the child later
// returns a normal final answer, the parent must still get a completion
// notice that can be counted in its persisted message user_data.
func TestSubagentRegularFinalAfterProgressNotifiesParent(t *testing.T) {
	server, database, _ := newTestServer(t)
	held := newHeldLLMService()
	server.llmManager = &testLLMManager{service: held}
	t.Cleanup(func() { stopActiveConversationLoops(server) })

	parent, err := database.CreateConversation(t.Context(), nil, true, nil, strPtr("predictable"), db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	child, err := database.CreateSubagentConversation(t.Context(), "scout-progress", parent.ConversationID, nil)
	if err != nil {
		t.Fatal(err)
	}
	prompt := "message_parent: halfway"
	if _, err := NewSubagentRunner(server).RunSubagent(t.Context(), child.ConversationID, prompt, "predictable", ""); err != nil {
		t.Fatal(err)
	}
	held.waitCall(t, prompt).Release()
	progress := `<subagent_message conversation_id="` + child.ConversationID + `" slug="scout-progress">` +
		"\nhalfway\n</subagent_message>"
	progressCall := held.waitCall(t, progress)
	parentMgr, err := server.getOrCreateConversationManager(t.Context(), parent.ConversationID, "")
	if err != nil {
		t.Fatal(err)
	}
	parentIdle := parentMgr.idle()
	progressCall.Release()
	select {
	case <-parentIdle:
	case <-time.After(5 * time.Second):
		t.Fatal("parent did not finish progress turn")
	}
	held.waitCall(t, prompt).Release() // Child's tool-result round.

	noticeText := `Subagent "scout-progress" finished a turn. Inspect its latest reply and working state.`
	notice := `<subagent_message conversation_id="` + child.ConversationID + `" slug="scout-progress">` +
		"\n" + noticeText + "\n</subagent_message>"
	completionCall := held.waitCall(t, notice)
	defer completionCall.Release()
	message := userMessageContaining(t, database, parent.ConversationID, noticeText)
	if message == nil || message.UserData == nil {
		t.Fatal("no persisted completion notice")
	}
	var data senderMessageUserData
	if err := json.Unmarshal([]byte(*message.UserData), &data); err != nil {
		t.Fatal(err)
	}
	if data.CompletionKind != "regular_final" {
		t.Fatalf("completion kind = %q, want regular_final", data.CompletionKind)
	}
}

func TestSubagentRefusalNotifiesAfterVisibleError(t *testing.T) {
	server, database, _ := newTestServer(t)
	held := newHeldLLMService()
	server.llmManager = &testLLMManager{service: held}
	t.Cleanup(func() { stopActiveConversationLoops(server) })
	parent, err := database.CreateConversation(t.Context(), nil, true, nil, strPtr("predictable"), db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	child, err := database.CreateSubagentConversation(t.Context(), "scout-error", parent.ConversationID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewSubagentRunner(server).RunSubagent(t.Context(), child.ConversationID, "refusal", "predictable", ""); err != nil {
		t.Fatal(err)
	}
	held.waitCall(t, "refusal").Release()
	notice := `Subagent "scout-error" ended a turn with an error. Inspect its conversation for details.`
	wrapped := `<subagent_message conversation_id="` + child.ConversationID + `" slug="scout-error">` +
		"\n" + notice + "\n</subagent_message>"
	parentCall := held.waitCall(t, wrapped)
	defer parentCall.Release()
	visibleError := false
	for _, m := range listMessages(t, database, child.ConversationID) {
		visibleError = visibleError || m.Type == string(db.MessageTypeError)
	}
	if !visibleError {
		t.Fatal("parent notified before the visible refusal error")
	}
	message := userMessageContaining(t, database, parent.ConversationID, notice)
	if message == nil || message.UserData == nil {
		t.Fatal("error completion notice missing")
	}
	var data senderMessageUserData
	if err := json.Unmarshal([]byte(*message.UserData), &data); err != nil {
		t.Fatal(err)
	}
	if data.CompletionKind != "error" {
		t.Fatalf("completion kind = %q, want error", data.CompletionKind)
	}
}

func TestSubagentCompletionDoesNotRestartStoppedParent(t *testing.T) {
	server, database, _ := newTestServer(t)
	parent, err := database.CreateConversation(t.Context(), nil, true, nil, strPtr("predictable"), db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	child, err := database.CreateSubagentConversation(t.Context(), "stopped-child", parent.ConversationID, nil)
	if err != nil {
		t.Fatal(err)
	}
	cancelConversation(t, server, parent.ConversationID)

	// This represents the completion goroutine running only after Stop has
	// cleared the parent's queue and returned.
	server.notifySubagentCompletion(child.ConversationID, "regular_final")
	conv, err := database.GetConversationByID(t.Context(), parent.ConversationID)
	if err != nil {
		t.Fatal(err)
	}
	if conv.AgentWorking || conv.QueuedMessages != "[]" {
		t.Fatalf("late completion restarted stopped parent: working=%v queued=%s", conv.AgentWorking, conv.QueuedMessages)
	}
	for _, m := range listMessages(t, database, parent.ConversationID) {
		if m.UserData != nil {
			var data senderMessageUserData
			if json.Unmarshal([]byte(*m.UserData), &data) == nil && data.CompletionKind != "" {
				t.Fatal("late completion was delivered after Stop")
			}
		}
	}
}

func TestNewDelegationAfterStopCanNotifyParent(t *testing.T) {
	server, database, _ := newTestServer(t)
	held := newHeldLLMService()
	server.llmManager = &testLLMManager{service: held}
	t.Cleanup(func() { stopActiveConversationLoops(server) })
	parent, err := database.CreateConversation(t.Context(), nil, true, nil, strPtr("predictable"), db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	child, err := database.CreateSubagentConversation(t.Context(), "new-task", parent.ConversationID, nil)
	if err != nil {
		t.Fatal(err)
	}
	cancelConversation(t, server, parent.ConversationID)
	if _, err := NewSubagentRunner(server).RunSubagent(t.Context(), child.ConversationID, "echo: new work", "predictable", ""); err != nil {
		t.Fatal(err)
	}
	held.waitCall(t, "echo: new work").Release()
	notice := `Subagent "new-task" finished a turn. Inspect its latest reply and working state.`
	held.waitCall(t, `<subagent_message conversation_id="`+child.ConversationID+`" slug="new-task">`+"\n"+notice+"\n</subagent_message>").Release()
}

func TestResumedParentAcceptsChildReportsAfterStop(t *testing.T) {
	server, database, _ := newTestServer(t)
	held := newHeldLLMService()
	server.llmManager = &testLLMManager{service: held}
	t.Cleanup(func() { stopActiveConversationLoops(server) })
	parent, err := database.CreateConversation(t.Context(), nil, true, nil, strPtr("predictable"), db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	child, err := database.CreateSubagentConversation(t.Context(), "resumed-child", parent.ConversationID, nil)
	if err != nil {
		t.Fatal(err)
	}
	cancelConversation(t, server, parent.ConversationID)
	parentManager, err := server.getOrCreateConversationManager(t.Context(), parent.ConversationID, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parentManager.AcceptUserMessage(t.Context(), held, "predictable", llm.UserStringMessage("echo: resume parent")); err != nil {
		t.Fatal(err)
	}
	releaseAndWaitIdle(t, server, parent.ConversationID, held.waitCall(t, "echo: resume parent"))

	if err := NewSubagentRunner(server).MessageParent(t.Context(), child.ConversationID, "resumed report"); err != nil {
		t.Fatalf("child report rejected after parent resumed: %v", err)
	}
	wrapped := `<subagent_message conversation_id="` + child.ConversationID + `" slug="resumed-child">` +
		"\nresumed report\n</subagent_message>"
	releaseAndWaitIdle(t, server, parent.ConversationID, held.waitCall(t, wrapped))

	response := postBtwChat(t, server, child.ConversationID, ChatRequest{Message: "echo: child finished", Model: "predictable"})
	if response.Code != 202 {
		t.Fatalf("direct child chat status=%d body=%s", response.Code, response.Body.String())
	}
	releaseAndWaitIdle(t, server, child.ConversationID, held.waitCall(t, "echo: child finished"))
	notice := `Subagent "resumed-child" finished a turn. Inspect its latest reply and working state.`
	parentCall := held.waitCall(t, `<subagent_message conversation_id="`+child.ConversationID+`" slug="resumed-child">`+"\n"+notice+"\n</subagent_message>")
	defer parentCall.Release()
	message := userMessageContaining(t, database, parent.ConversationID, notice)
	if message == nil || message.UserData == nil {
		t.Fatal("ordinary child final produced no parent notice after resuming")
	}
	var data senderMessageUserData
	if err := json.Unmarshal([]byte(*message.UserData), &data); err != nil {
		t.Fatal(err)
	}
	if data.CompletionKind != "regular_final" {
		t.Fatalf("completion kind = %q, want regular_final", data.CompletionKind)
	}
}

func TestQueuedParentWorkReopensChildReportsAfterStop(t *testing.T) {
	server, database, _ := newTestServer(t)
	held := newHeldLLMService()
	server.llmManager = &testLLMManager{service: held}
	t.Cleanup(func() { stopActiveConversationLoops(server) })
	parent, err := database.CreateConversation(t.Context(), nil, true, nil, strPtr("predictable"), db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	child, err := database.CreateSubagentConversation(t.Context(), "queue-child", parent.ConversationID, nil)
	if err != nil {
		t.Fatal(err)
	}
	cancelConversation(t, server, parent.ConversationID)
	manager, err := server.getOrCreateConversationManager(t.Context(), parent.ConversationID, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.QueueMessage(t.Context(), server, "predictable", llm.UserStringMessage("echo: queued restart")); err != nil {
		t.Fatal(err)
	}
	releaseAndWaitIdle(t, server, parent.ConversationID, held.waitCall(t, "echo: queued restart"))
	if err := NewSubagentRunner(server).MessageParent(t.Context(), child.ConversationID, "queued report"); err != nil {
		t.Fatalf("parent rejected report after queued user work: %v", err)
	}
	wrapped := `<subagent_message conversation_id="` + child.ConversationID + `" slug="queue-child">` +
		"\nqueued report\n</subagent_message>"
	held.waitCall(t, wrapped).Release()
}

func TestDelegationDuringStopCannotReopenParentFence(t *testing.T) {
	server, database, _ := newTestServer(t)
	held := newHeldLLMService()
	server.llmManager = &testLLMManager{service: held}
	t.Cleanup(func() { stopActiveConversationLoops(server) })
	parent, err := database.CreateConversation(t.Context(), nil, true, nil, strPtr("predictable"), db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	child, err := database.CreateSubagentConversation(t.Context(), "racing-task", parent.ConversationID, nil)
	if err != nil {
		t.Fatal(err)
	}
	server.mu.Lock()
	server.stoppedParents[parent.ConversationID] = true
	server.stoppingParents[parent.ConversationID] = true
	server.mu.Unlock()
	if _, err := NewSubagentRunner(server).RunSubagent(t.Context(), child.ConversationID, "echo: late work", "predictable", ""); err != nil {
		t.Fatal(err)
	}
	server.mu.Lock()
	stillStopped := server.stoppedParents[parent.ConversationID]
	server.mu.Unlock()
	if !stillStopped {
		t.Fatal("in-flight delegation reopened the parent completion fence")
	}
	releaseAndWaitIdle(t, server, child.ConversationID, held.waitCall(t, "echo: late work"))
	conv, err := database.GetConversationByID(t.Context(), parent.ConversationID)
	if err != nil {
		t.Fatal(err)
	}
	if conv.AgentWorking || conv.QueuedMessages != "[]" {
		t.Fatalf("in-flight child completion restarted stopped parent: %+v", conv)
	}
}

func TestMessageParentDoesNotRestartStoppedParent(t *testing.T) {
	server, database, _ := newTestServer(t)
	parent, err := database.CreateConversation(t.Context(), nil, true, nil, strPtr("predictable"), db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	child, err := database.CreateSubagentConversation(t.Context(), "stopped-reporter", parent.ConversationID, nil)
	if err != nil {
		t.Fatal(err)
	}
	cancelConversation(t, server, parent.ConversationID)
	if err := NewSubagentRunner(server).MessageParent(t.Context(), child.ConversationID, "late report"); err == nil {
		t.Fatal("message_parent accepted a report after parent Stop")
	}
	conv, err := database.GetConversationByID(t.Context(), parent.ConversationID)
	if err != nil {
		t.Fatal(err)
	}
	if conv.AgentWorking || conv.QueuedMessages != "[]" {
		t.Fatalf("stopped parent restarted: working=%v queued=%s", conv.AgentWorking, conv.QueuedMessages)
	}
}

// Stop and the last queue write must agree even if Stop begins after the
// messenger's first stopped check but before InjectMessage takes its lock.
func TestSubagentReportAdmissionIsAtomicWithParentStop(t *testing.T) {
	server, database, _ := newTestServer(t)
	parent, err := database.CreateConversation(t.Context(), nil, true, nil, strPtr("predictable"), db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	child, err := database.CreateSubagentConversation(t.Context(), "late-reporter", parent.ConversationID, nil)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := server.getOrCreateConversationManager(t.Context(), parent.ConversationID, "")
	if err != nil {
		t.Fatal(err)
	}
	cancelConversation(t, server, parent.ConversationID)
	ctx := contextWithTurnUserData(t.Context(), senderMessageUserData{
		SenderConversationID: child.ConversationID,
		SenderRelationship:   senderRelationshipSubagent,
		Text:                 "late report",
	})
	if err := manager.InjectMessage(ctx, server, "predictable", llm.UserStringMessage("late report")); err == nil {
		t.Fatal("late report entered the parent queue after Stop")
	}
	conv, err := database.GetConversationByID(t.Context(), parent.ConversationID)
	if err != nil {
		t.Fatal(err)
	}
	if conv.QueuedMessages != "[]" {
		t.Fatalf("Stop left a queued subagent report: %s", conv.QueuedMessages)
	}
}

func requestHasTool(request *llm.Request, name string) bool {
	for _, tool := range request.Tools {
		if tool.Name == name {
			return true
		}
	}
	return false
}
