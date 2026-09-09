package server

import (
	"testing"

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
