package server

import (
	"testing"

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

func requestHasTool(request *llm.Request, name string) bool {
	for _, tool := range request.Tools {
		if tool.Name == name {
			return true
		}
	}
	return false
}
