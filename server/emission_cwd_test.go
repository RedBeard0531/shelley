package server

import (
	"encoding/json"
	"testing"

	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/llm"
)

// The loop wrapper stamps the emission-time working directory into the record
// context (see ensureLoopLocked); agent messages must carry it into user_data
// so the UI can resolve file references against the cwd of their time. User
// and system messages are not stamped.
func TestBuildCreateMessageParamsStampsEmissionCwd(t *testing.T) {
	t.Parallel()
	svr, _, _ := newTestServer(t)

	ctx := contextWithEmissionCwd(t.Context(), "/repo")
	agentMsg := llm.Message{Role: llm.MessageRoleAssistant, Content: llm.TextContent("hi")}

	params, err := svr.buildCreateMessageParams(ctx, "conv", agentMsg, llm.Usage{}, nil)
	if err != nil {
		t.Fatalf("buildCreateMessageParams: %v", err)
	}
	ud, ok := params.UserData.(map[string]any)
	if !ok {
		t.Fatalf("agent message user_data = %#v, want a map with the emission cwd", params.UserData)
	}
	if ud["cwd"] != "/repo" {
		t.Errorf("agent user_data cwd = %v, want /repo", ud["cwd"])
	}

	// Without the context stamp, agent user_data stays empty.
	params, err = svr.buildCreateMessageParams(t.Context(), "conv", agentMsg, llm.Usage{}, nil)
	if err != nil {
		t.Fatalf("buildCreateMessageParams: %v", err)
	}
	if params.UserData != nil {
		t.Errorf("unstamped agent message should have no user_data, got %#v", params.UserData)
	}

	// User messages are not stamped: their text is not model output.
	userMsg := llm.Message{Role: llm.MessageRoleUser, Content: llm.TextContent("hello")}
	params, err = svr.buildCreateMessageParams(ctx, "conv", userMsg, llm.Usage{}, nil)
	if err != nil {
		t.Fatalf("buildCreateMessageParams: %v", err)
	}
	if params.UserData != nil {
		t.Errorf("user message should not be stamped, got %#v", params.UserData)
	}
}

// The stamp comes from the conversation manager's recordMessage override: every
// message it records carries the cwd the conversation was in at record time.
// Only the manager knows that directory, so nothing else can put the stamp on a
// message — dropping the wrapper silently disables file-reference resolution.
func TestConversationManagerStampsEmissionCwd(t *testing.T) {
	t.Parallel()
	h := NewTestHarness(t)
	ctx := t.Context()
	dir := t.TempDir()
	h.NewConversation("Hello", dir)
	h.WaitResponse()
	convID := h.ConversationID()

	cm, err := h.server.getOrCreateConversationManager(ctx, convID, "")
	if err != nil {
		t.Fatalf("getOrCreateConversationManager: %v", err)
	}
	if cm.Cwd() != dir {
		t.Fatalf("manager cwd = %q, want %q", cm.Cwd(), dir)
	}

	msg := llm.Message{Role: llm.MessageRoleAssistant, Content: llm.TextContent("see the file")}
	if err := cm.recordMessage(ctx, msg, llm.Usage{}, nil); err != nil {
		t.Fatalf("recordMessage: %v", err)
	}

	var messages []generated.Message
	err = h.db.Queries(ctx, func(q *generated.Queries) error {
		var qerr error
		messages, qerr = q.ListMessages(ctx, convID)
		return qerr
	})
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	// The unstamped system prompt is a system message, so any agent message
	// here came through the manager and must carry the stamp.
	for _, m := range messages {
		if m.Type != string(db.MessageTypeAgent) {
			continue
		}
		var ud map[string]any
		if m.UserData != nil && *m.UserData != "" {
			if err := json.Unmarshal([]byte(*m.UserData), &ud); err != nil {
				t.Fatalf("unmarshal user_data %q: %v", *m.UserData, err)
			}
		}
		if ud["cwd"] != dir {
			t.Fatalf("recorded agent message cwd = %v, want %q", ud["cwd"], dir)
		}
		return
	}
	t.Fatal("no agent message was recorded")
}
