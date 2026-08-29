package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"shelley.exe.dev/db"
	"shelley.exe.dev/llm"
)

func TestSyntheticEndMarkerDoesNotCountLLMCall(t *testing.T) {
	t.Parallel()
	srv, database, _ := newTestServer(t)
	ctx := t.Context()
	parent, err := database.CreateConversation(ctx, nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	child, err := database.CreateSubagentConversation(ctx, "reporter", parent.ConversationID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.SetConversationAgentWorking(ctx, child.ConversationID, true); err != nil {
		t.Fatal(err)
	}
	paid := llm.Usage{InputTokens: 1000, OutputTokens: 1000, CostUSD: 0.125,
		Model: "claude-opus-4-6", URL: "https://llm.int.exe.xyz/v1/messages"}
	call := llm.Message{Role: llm.MessageRoleAssistant, Content: []llm.Content{{
		Type: llm.ContentTypeToolUse, ID: "report", ToolName: "message_parent",
		ToolInput: json.RawMessage(`{"text":"done","end_turn":true}`),
	}}}
	if err := srv.recordMessage(ctx, child.ConversationID, call, paid, nil); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name    string
		message llm.Message
		usage   llm.Usage
		calls   int64
	}{
		{"synthetic marker", llm.Message{Role: llm.MessageRoleAssistant, EndOfTurn: true, ExcludedFromContext: true}, llm.Usage{}, 1},
		{"paid excluded output", llm.Message{Role: llm.MessageRoleAssistant, EndOfTurn: true, ExcludedFromContext: true, Content: llm.TextContent("paid truncated output")}, paid, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := srv.recordMessage(ctx, child.ConversationID, tc.message, tc.usage, nil); err != nil {
				t.Fatal(err)
			}
			messages := listMessages(t, database, child.ConversationID)
			if len(messages) != int(tc.calls)+1 {
				t.Fatalf("persisted %d messages, want %d", len(messages), tc.calls+1)
			}
			marker := messages[1]
			stored := storedLLMMessage(t, &marker)
			if marker.UsageData != nil || !marker.ExcludedFromContext || !stored.ExcludedFromContext || !stored.EndOfTurn {
				t.Fatalf("synthetic marker lost state or gained billing usage: %+v", marker)
			}
			conv, err := database.GetConversationByID(ctx, child.ConversationID)
			if err != nil || conv.AgentWorking {
				t.Fatalf("marker did not clear working state: conversation=%+v error=%v", conv, err)
			}
			wantCost := float64(tc.calls) * paid.CostUSD
			for _, id := range []string{parent.ConversationID, child.ConversationID} {
				rows, err := database.GetSubtreeUsage(ctx, id)
				if err != nil || len(rows) != 1 || rows[0].LlmCalls != tc.calls || rows[0].CostUsd != wantCost {
					t.Fatalf("subtree %s: rows=%+v error=%v", id, rows, err)
				}
			}
			rows, err := database.GetSubagentUsage(ctx, parent.ConversationID)
			if err != nil || len(rows) != 1 || rows[0].LlmCalls != tc.calls || rows[0].CostUsd != wantCost {
				t.Fatalf("subagent usage: rows=%+v error=%v", rows, err)
			}
			w := httptest.NewRecorder()
			srv.handleSubagentUsage(w, httptest.NewRequest(http.MethodGet, "/", nil).WithContext(ctx), parent.ConversationID)
			if w.Code != http.StatusOK {
				t.Fatalf("usage endpoint: %d %s", w.Code, w.Body.String())
			}
			var usage struct {
				LLMCalls    int64              `json:"llm_calls"`
				ReportedUsd float64            `json:"reported_usd"`
				Subagents   []subagentUsageDTO `json:"subagents"`
				PerModel    []struct {
					Model    string `json:"model"`
					LLMCalls int64  `json:"llm_calls"`
				} `json:"per_model"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &usage); err != nil {
				t.Fatal(err)
			}
			if usage.LLMCalls != tc.calls || usage.ReportedUsd != wantCost ||
				len(usage.PerModel) != 1 || usage.PerModel[0].Model != paid.Model || usage.PerModel[0].LLMCalls != tc.calls ||
				len(usage.Subagents) != 1 || usage.Subagents[0].ConversationID != child.ConversationID ||
				usage.Subagents[0].LLMCalls != tc.calls || usage.Subagents[0].ReportedUsd != wantCost {
				t.Fatalf("aggregate/per-child billing=%+v, want %d calls costing %v", usage, tc.calls, wantCost)
			}
		})
	}
}

func TestSyntheticEndMarkerUsageGuard(t *testing.T) {
	t.Parallel()
	srv, _, _ := newTestServer(t)
	marker := llm.Message{Role: llm.MessageRoleAssistant, EndOfTurn: true, ExcludedFromContext: true}
	stamp := time.Unix(1, 0)
	for _, tc := range []struct {
		name    string
		message llm.Message
		usage   llm.Usage
		wantNil bool
	}{
		{"synthetic", marker, llm.Usage{}, true},
		{"visible output", llm.Message{Role: llm.MessageRoleAssistant, EndOfTurn: true, ExcludedFromContext: true, Content: llm.TextContent("output")}, llm.Usage{}, false},
		{"included end marker", llm.Message{Role: llm.MessageRoleAssistant, EndOfTurn: true}, llm.Usage{}, false},
		{"nonterminal excluded", llm.Message{Role: llm.MessageRoleAssistant, ExcludedFromContext: true}, llm.Usage{}, false},
		{"user", llm.Message{Role: llm.MessageRoleUser, EndOfTurn: true, ExcludedFromContext: true}, llm.Usage{}, false},
		{"failure marker", llm.Message{Role: llm.MessageRoleAssistant, EndOfTurn: true, ExcludedFromContext: true, ErrorType: llm.ErrorTypeLLMRequest}, llm.Usage{}, false},
		{"input tokens", marker, llm.Usage{InputTokens: 1}, false},
		{"output tokens", marker, llm.Usage{OutputTokens: 1}, false},
		{"cache write", marker, llm.Usage{CacheCreationInputTokens: 1}, false},
		{"cache read", marker, llm.Usage{CacheReadInputTokens: 1}, false},
		{"reported cost", marker, llm.Usage{CostUSD: 0.125}, false},
		{"model", marker, llm.Usage{Model: "failed-model"}, false},
		{"URL", marker, llm.Usage{URL: "https://example.invalid"}, false},
		{"start time", marker, llm.Usage{StartTime: &stamp}, false},
		{"end time", marker, llm.Usage{EndTime: &stamp}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			params, err := srv.buildCreateMessageParams("conversation", tc.message, tc.usage, nil)
			if err != nil {
				t.Fatal(err)
			}
			if (params.UsageData == nil) != tc.wantNil || (!tc.wantNil && params.UsageData != tc.usage) {
				t.Fatalf("usage=%+v, want nil=%v (otherwise %+v)", params.UsageData, tc.wantNil, tc.usage)
			}
		})
	}
}
