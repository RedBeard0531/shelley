package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"shelley.exe.dev/db"
	"shelley.exe.dev/llm"
)

func seedManualInterruption(t *testing.T, database *db.DB, trailingError bool) string {
	t.Helper()
	ctx := context.Background()
	model := "predictable"
	conv, err := database.CreateConversation(ctx, nil, true, nil, &model, db.ConversationOptions{})
	if err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}
	if _, err := database.CreateMessage(ctx, db.CreateMessageParams{
		ConversationID: conv.ConversationID,
		Type:           db.MessageTypeSystem,
		LLMData:        llm.UserStringMessage("system"),
	}); err != nil {
		t.Fatalf("Create system message: %v", err)
	}
	if _, err := database.CreateMessage(ctx, db.CreateMessageParams{
		ConversationID: conv.ConversationID,
		Type:           db.MessageTypeUser,
		LLMData:        llm.UserStringMessage("echo: resumed"),
	}); err != nil {
		t.Fatalf("Create user message: %v", err)
	}
	if trailingError {
		if _, err := database.CreateMessage(ctx, db.CreateMessageParams{
			ConversationID: conv.ConversationID,
			Type:           db.MessageTypeError,
			LLMData: llm.Message{
				Role:      llm.MessageRoleAssistant,
				Content:   []llm.Content{{Type: llm.ContentTypeText, Text: "temporary failure"}},
				EndOfTurn: true,
			},
			UserData: map[string]any{"retryable": true},
		}); err != nil {
			t.Fatalf("Create error message: %v", err)
		}
	}
	if err := database.SetConversationAgentWorking(ctx, conv.ConversationID, true); err != nil {
		t.Fatalf("SetConversationAgentWorking: %v", err)
	}
	if ids, err := database.ConsumeResumeAfterUpgrade(ctx); err != nil || len(ids) != 0 {
		t.Fatalf("mark ordinary restart interruption: ids=%v err=%v", ids, err)
	}
	got, err := database.GetConversationByID(ctx, conv.ConversationID)
	if err != nil {
		t.Fatalf("GetConversationByID: %v", err)
	}
	if !got.TurnInterrupted || got.AgentWorking {
		t.Fatalf("restart state interrupted=%v working=%v, want true/false", got.TurnInterrupted, got.AgentWorking)
	}
	return conv.ConversationID
}

func TestResumeInterruptedConversation(t *testing.T) {
	t.Parallel()
	srv, database, predictableService := newTestServer(t)
	predictableService.SetResponseDelay(300 * time.Millisecond)
	ctx := context.Background()
	conversationID := seedManualInterruption(t, database, false)

	req := httptest.NewRequest(http.MethodPost, "/api/conversation/"+conversationID+"/resume", nil)
	w := httptest.NewRecorder()
	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("resume status = %d, want 202: %s", w.Code, w.Body.String())
	}
	claimed, err := database.GetConversationByID(ctx, conversationID)
	if err != nil {
		t.Fatalf("GetConversationByID after resume: %v", err)
	}
	if claimed.TurnInterrupted || !claimed.AgentWorking {
		t.Fatalf("claimed state interrupted=%v working=%v, want false/true", claimed.TurnInterrupted, claimed.AgentWorking)
	}

	// A second click while the retry is live must not enqueue another LLM call.
	w = httptest.NewRecorder()
	srv.handleResumeConversation(w, httptest.NewRequest(http.MethodPost, "/", nil), conversationID)
	if w.Code != http.StatusAccepted || !strings.Contains(w.Body.String(), "not_applicable") {
		t.Fatalf("duplicate resume = %d %q, want accepted not_applicable", w.Code, w.Body.String())
	}

	waitFor(t, 10*time.Second, func() bool {
		messages, err := database.ListMessages(ctx, conversationID)
		if err != nil {
			return false
		}
		for _, message := range messages {
			if message.Type == string(db.MessageTypeAgent) && isAgentEndOfTurn(&message) {
				return true
			}
		}
		return false
	})
	waitFor(t, 5*time.Second, func() bool { return !srv.IsAgentWorking(conversationID) })

	messages, err := database.ListMessages(ctx, conversationID)
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	var users, agents int
	for _, message := range messages {
		switch message.Type {
		case string(db.MessageTypeUser):
			users++
		case string(db.MessageTypeAgent):
			agents++
		}
	}
	if users != 1 {
		t.Fatalf("user messages = %d, want 1 (resume must not add a user message)", users)
	}
	if agents != 1 {
		t.Fatalf("agent messages = %d, want 1", agents)
	}

	// Once a terminal message exists, another click remains a harmless no-op.
	w = httptest.NewRecorder()
	srv.handleResumeConversation(w, httptest.NewRequest(http.MethodPost, "/", nil), conversationID)
	if w.Code != http.StatusAccepted || !strings.Contains(w.Body.String(), "not_applicable") {
		t.Fatalf("completed resume = %d %q, want accepted not_applicable", w.Code, w.Body.String())
	}
}

func TestResumeInterruptedRetryAfterTerminalError(t *testing.T) {
	t.Parallel()
	srv, database, predictableService := newTestServer(t)
	predictableService.SetResponseDelay(300 * time.Millisecond)
	conversationID := seedManualInterruption(t, database, true)

	w := httptest.NewRecorder()
	srv.handleResumeConversation(w, httptest.NewRequest(http.MethodPost, "/", nil), conversationID)
	if w.Code != http.StatusAccepted || !strings.Contains(w.Body.String(), "resuming") {
		t.Fatalf("resume retry = %d %q, want accepted resuming", w.Code, w.Body.String())
	}

	// The old retry affordance remains visible until a new assistant row lands.
	// It must share Continue's dedupe stamp rather than scheduling a second run.
	retry := httptest.NewRecorder()
	srv.handleRetryConversation(retry, httptest.NewRequest(http.MethodPost, "/", nil), conversationID)
	if retry.Code != http.StatusAccepted || !strings.Contains(retry.Body.String(), "not_applicable") {
		t.Fatalf("retry after Continue = %d %q, want accepted not_applicable", retry.Code, retry.Body.String())
	}

	waitFor(t, 10*time.Second, func() bool {
		messages, err := database.ListMessages(context.Background(), conversationID)
		if err != nil {
			return false
		}
		for _, message := range messages {
			if message.Type == string(db.MessageTypeAgent) && isAgentEndOfTurn(&message) {
				return true
			}
		}
		return false
	})
	messages, err := database.ListMessages(context.Background(), conversationID)
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	agents := 0
	for _, message := range messages {
		if message.Type == string(db.MessageTypeAgent) {
			agents++
		}
	}
	if agents != 1 {
		t.Fatalf("agent messages = %d, want 1 after Continue+Retry race", agents)
	}
}

func TestResumeInterruptedConversationUsesPersistedModel(t *testing.T) {
	t.Parallel()
	srv, database, _ := newTestServer(t)
	conversationID := seedManualInterruption(t, database, false)
	manager, err := srv.getOrCreateConversationManager(context.Background(), conversationID, "")
	if err != nil {
		t.Fatalf("get manager: %v", err)
	}
	if got := manager.GetModel(); got != "predictable" {
		t.Fatalf("initial model = %q, want predictable", got)
	}
	if err := database.ForceUpdateConversationModel(context.Background(), conversationID, "model-after-switch"); err != nil {
		t.Fatalf("ForceUpdateConversationModel: %v", err)
	}
	// Mirrors an idle /model change: DB is current, while ResetLoop leaves the
	// old in-memory model ID until Hydrate runs again.
	manager.ResetLoop()
	if got := manager.GetModel(); got != "predictable" {
		t.Fatalf("test setup lost stale model, got %q", got)
	}

	w := httptest.NewRecorder()
	srv.handleResumeConversation(w, httptest.NewRequest(http.MethodPost, "/", nil), conversationID)
	if w.Code != http.StatusAccepted {
		t.Fatalf("resume status = %d, want 202: %s", w.Code, w.Body.String())
	}
	if got := manager.GetModel(); got != "model-after-switch" {
		t.Fatalf("resumed model = %q, want model-after-switch", got)
	}
}
