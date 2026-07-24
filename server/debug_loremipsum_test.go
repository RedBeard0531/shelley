package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"shelley.exe.dev/db"
)

// TestGenerateLoremConversation verifies the synthetic conversation
// generator produces a loadable conversation containing every message type
// the UI renders, and that the /debug/loremipsum handler persists and
// redirects to it.
func TestGenerateLoremConversation(t *testing.T) {
	t.Parallel()
	server, database, _ := newTestServer(t)
	ctx := context.Background()

	// Use enough turns to trigger at least two compactions (compactEvery=40).
	convID, err := server.generateLoremConversation(ctx, 100, "claude-opus-4-5")
	if err != nil {
		t.Fatalf("generateLoremConversation: %v", err)
	}

	// Every message type should be represented. Tool calls and results are
	// embedded in agent (tool_use) and user (tool_result) messages, so the
	// standalone "tool" message type is intentionally not produced — this
	// matches how the real loop records messages. System messages appear
	// once per generation.
	for _, mt := range []db.MessageType{
		db.MessageTypeUser, db.MessageTypeAgent, db.MessageTypeSystem,
		db.MessageTypeGitInfo, db.MessageTypeWarning, db.MessageTypeError,
		db.MessageTypeModelChange,
	} {
		msgs, err := database.ListMessagesByType(ctx, convID, mt)
		if err != nil {
			t.Fatalf("ListMessagesByType(%s): %v", mt, err)
		}
		if len(msgs) == 0 {
			t.Errorf("no messages of type %s were generated", mt)
		}
	}

	// The conversation must load through the real handler and its messages
	// must unmarshal (this is what the UI does).
	req := httptest.NewRequest("GET", "/api/conversation/"+convID, nil)
	w := httptest.NewRecorder()
	server.handleGetConversation(w, req, convID)
	if w.Code != http.StatusOK {
		t.Fatalf("handleGetConversation status = %d, body: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Messages []struct {
			MessageID string  `json:"message_id"`
			LLMData   *string `json:"llm_data"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal conversation response: %v", err)
	}
	if len(resp.Messages) < 40 {
		t.Fatalf("expected many messages, got %d", len(resp.Messages))
	}

	// Every message's llm_data must be valid JSON so the client can parse it.
	toolUses := 0
	for _, m := range resp.Messages {
		if m.LLMData == nil {
			continue
		}
		var v struct {
			Content []struct {
				Type     int    `json:"Type"`
				ToolName string `json:"ToolName"`
			} `json:"Content"`
		}
		if err := json.Unmarshal([]byte(*m.LLMData), &v); err != nil {
			t.Fatalf("message %s has invalid llm_data: %v", m.MessageID, err)
		}
		for _, c := range v.Content {
			if c.Type == 5 && c.ToolName != "" { // ContentTypeToolUse
				toolUses++
			}
		}
	}
	if toolUses == 0 {
		t.Fatal("expected tool_use content blocks, found none")
	}

	// Compaction must have advanced the generation and produced compaction
	// summaries + carried-forward messages spanning multiple generations.
	conv, err := database.GetConversationByID(ctx, convID)
	if err != nil {
		t.Fatalf("GetConversationByID: %v", err)
	}
	if conv.CurrentGeneration < 3 {
		t.Errorf("expected current_generation >= 3 after compactions, got %d", conv.CurrentGeneration)
	}

	msgs, err := database.ListMessages(ctx, convID)
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	var distilled, carried, statusMsgs int
	seenGenerations := map[int64]bool{}
	for _, m := range msgs {
		seenGenerations[m.Generation] = true
		if m.UserData == nil {
			continue
		}
		var ud map[string]string
		if err := json.Unmarshal([]byte(*m.UserData), &ud); err != nil {
			continue
		}
		if ud["distilled"] == "true" {
			distilled++
		}
		if ud["compaction_carried"] == "true" {
			carried++
		}
		if ud["distill_status"] != "" {
			statusMsgs++
		}
	}
	if distilled == 0 {
		t.Error("expected compaction summary messages (distilled=true), found none")
	}
	if carried == 0 {
		t.Error("expected carried-forward messages (compaction_carried=true), found none")
	}
	if statusMsgs == 0 {
		t.Error("expected distill status messages, found none")
	}
	if len(seenGenerations) < 3 {
		t.Errorf("expected messages spanning >=3 generations, saw %d", len(seenGenerations))
	}
}

// TestHandleDebugLoremIpsum exercises the HTTP entry point: the GET landing
// page (which must NOT generate) and POST generation, including preset sizes,
// raw counts, and invalid input.
func TestHandleDebugLoremIpsum(t *testing.T) {
	t.Parallel()
	server, database, _ := newTestServer(t)
	ctx := context.Background()

	// A bare GET must render the landing page and must NOT create anything.
	req := httptest.NewRequest("GET", "/debug/loremipsum", nil)
	w := httptest.NewRecorder()
	server.handleDebugLoremIpsum(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET landing status = %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("GET landing content-type = %q, want html", ct)
	}
	if body := w.Body.String(); !strings.Contains(body, "Generate") || !strings.Contains(body, "Custom size") {
		t.Error("GET landing page missing expected controls")
	}
	if convs, err := database.ListConversations(ctx, 10, 0); err != nil {
		t.Fatalf("ListConversations: %v", err)
	} else if len(convs) != 0 {
		t.Fatalf("GET created %d conversations; a GET must have no side effects", len(convs))
	}

	// POST with a preset size and json output returns a conversation id.
	req = httptest.NewRequest("POST", "/debug/loremipsum?json=1", strings.NewReader("size=tiny"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w = httptest.NewRecorder()
	server.handleDebugLoremIpsum(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("tiny status = %d: %s", w.Code, w.Body.String())
	}
	var out struct {
		ConversationID string `json:"conversation_id"`
		Turns          int    `json:"turns"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.ConversationID == "" || out.Turns != 2 {
		t.Fatalf("unexpected response: %+v", out)
	}

	// POST with a raw count redirects to the conversation.
	req = httptest.NewRequest("POST", "/debug/loremipsum", strings.NewReader("size=3"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w = httptest.NewRecorder()
	server.handleDebugLoremIpsum(w, req)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("raw-count status = %d, want 303", w.Code)
	}

	// Invalid size re-renders the landing page with an error banner (200).
	req = httptest.NewRequest("POST", "/debug/loremipsum", strings.NewReader("size=nope"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w = httptest.NewRecorder()
	server.handleDebugLoremIpsum(w, req)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Invalid size") {
		t.Fatalf("bad size: status=%d, want 200 with banner", w.Code)
	}

	// Over-large size re-renders the landing page with an error banner (200).
	req = httptest.NewRequest("POST", "/debug/loremipsum", strings.NewReader("size=200000"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w = httptest.NewRecorder()
	server.handleDebugLoremIpsum(w, req)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "too large") {
		t.Fatalf("over-large: status=%d, want 200 with banner", w.Code)
	}
}
