package server

import (
	"context"
	"testing"
	"testing/synctest"

	"shelley.exe.dev/db"
	"shelley.exe.dev/llm"
)

func TestStartNewGenerationFiltersContext(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := NewTestHarness(t)
		h.NewConversation("old context", "")
		h.WaitResponse()
		ctx := t.Context()
		convID := h.convID

		oldMsgs, err := h.db.ListMessagesForContext(ctx, convID)
		if err != nil {
			t.Fatalf("failed to list old context: %v", err)
		}
		if len(oldMsgs) == 0 {
			t.Fatal("expected old generation messages in context before generation bump")
		}

		conversation, err := h.server.startNewGeneration(ctx, convID)
		if err != nil {
			t.Fatalf("failed to start new generation: %v", err)
		}
		if conversation.CurrentGeneration != 2 {
			t.Fatalf("expected current_generation=2, got %d", conversation.CurrentGeneration)
		}

		afterBump, err := h.db.ListMessagesForContext(ctx, convID)
		if err != nil {
			t.Fatalf("failed to list context after bump: %v", err)
		}
		if len(afterBump) != 1 {
			t.Fatalf("expected 1 generation 2 message (system prompt), got %d", len(afterBump))
		}
		if afterBump[0].Type != string(db.MessageTypeSystem) {
			t.Fatalf("expected new generation context to start with a system prompt, got type %q", afterBump[0].Type)
		}
		if afterBump[0].Generation != 2 {
			t.Fatalf("expected new system prompt at generation 2, got %d", afterBump[0].Generation)
		}

		_, err = h.db.CreateMessage(ctx, db.CreateMessageParams{
			ConversationID: convID,
			Type:           db.MessageTypeUser,
			LLMData: llm.Message{
				Role:    llm.MessageRoleUser,
				Content: []llm.Content{{Type: llm.ContentTypeText, Text: "new context"}},
			},
		})
		if err != nil {
			t.Fatalf("failed to create new generation message: %v", err)
		}

		newMsgs, err := h.db.ListMessagesForContext(ctx, convID)
		if err != nil {
			t.Fatalf("failed to list new context: %v", err)
		}
		if len(newMsgs) != 2 {
			t.Fatalf("expected system prompt + new user message in context, got %d messages", len(newMsgs))
		}
		for _, msg := range newMsgs {
			if msg.Generation != 2 {
				t.Fatalf("expected only generation 2 messages, got %+v", msg)
			}
		}
	})
}

// TestStartNewGenerationPreservesSlug verifies that the actual post-generation
// chat path cannot replace the conversation's existing slug.
func TestStartNewGenerationPreservesSlug(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := NewTestHarness(t)
		defer stopActiveConversationLoops(h.server)
		ctx := t.Context()
		pinned := "pinned-slug"
		model := "predictable"
		conversation, err := h.db.CreateConversation(ctx, &pinned, true, nil, &model, db.ConversationOptions{})
		if err != nil {
			t.Fatal(err)
		}
		h.convID = conversation.ConversationID

		if _, err := h.server.startNewGeneration(ctx, h.convID); err != nil {
			t.Fatalf("startNewGeneration: %v", err)
		}
		h.Chat("new gen first message")
		h.WaitResponse()
		synctest.Wait()

		fresh, err := h.db.GetConversationByID(ctx, h.convID)
		if err != nil {
			t.Fatal(err)
		}
		if fresh.Slug == nil || *fresh.Slug != pinned {
			t.Fatalf("slug after new generation = %v, want %q", fresh.Slug, pinned)
		}
	})
}

// A client that goes away mid-request must not leave the generation bumped but
// unhydrated. The bump commits in its own transaction, so if hydration then
// inherits the (now cancelled) request context and fails, the conversation is
// left on a new generation with no system prompt and /clear returns 500. Seen
// in CI as `hydrate after generation bump: failed to store system prompt:
// context canceled`, which a loaded host makes easy to hit because it widens
// the window between the bump and the write.
func TestStartNewGenerationSurvivesClientDisconnect(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := NewTestHarness(t)
		h.NewConversation("before clear", "")
		h.WaitResponse()
		convID := h.convID

		// A context already cancelled, standing in for a client that
		// disconnected between the bump and hydration.
		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		conversation, err := h.server.startNewGeneration(ctx, convID)
		if err != nil {
			t.Fatalf("startNewGeneration with a cancelled client context: %v", err)
		}
		if conversation.CurrentGeneration != 2 {
			t.Fatalf("expected current_generation=2, got %d", conversation.CurrentGeneration)
		}

		// The new generation must have its system prompt, or the next turn runs
		// without one.
		msgs, err := h.db.ListMessagesForContext(t.Context(), convID)
		if err != nil {
			t.Fatalf("failed to list context: %v", err)
		}
		var system int
		for _, m := range msgs {
			if m.Type == string(db.MessageTypeSystem) {
				system++
			}
		}
		if system == 0 {
			t.Fatal("new generation has no system prompt after the client disconnected")
		}
	})
}

func stopActiveConversationLoops(server *Server) {
	server.mu.Lock()
	managers := make([]*ConversationManager, 0, len(server.activeConversations))
	for _, manager := range server.activeConversations {
		managers = append(managers, manager)
	}
	server.mu.Unlock()

	for _, manager := range managers {
		manager.stopLoop()
	}
}
