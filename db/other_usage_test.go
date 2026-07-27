package db

import (
	"context"
	"encoding/json"
	"testing"

	"shelley.exe.dev/llm"
)

// TestMessageOtherUsageRoundTrip verifies that CreateMessage persists
// OtherUsageData as a JSON array, that it is NULL when unset, and that a
// forked copy preserves it (usage attribution across forks relies on
// forked_from_message_id for dedup, same as usage_data).
func TestMessageOtherUsageRoundTrip(t *testing.T) {
	database := setupTestDB(t)
	defer database.Close()

	ctx := context.Background()
	conv, err := database.CreateConversation(ctx, stringPtr("other-usage-round-trip"), true, nil, nil, ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}

	otherUsage := []llm.PurposedUsage{
		{Purpose: "keyword_search", Usage: llm.Usage{InputTokens: 100, OutputTokens: 10, CostUSD: 0.01, Model: "m1", URL: "u1"}},
		{Purpose: "llm_one_shot", Usage: llm.Usage{InputTokens: 5, OutputTokens: 1, Model: "m2"}},
	}
	if _, err := database.CreateMessage(ctx, CreateMessageParams{
		ConversationID: conv.ConversationID,
		Type:           MessageTypeUser,
		LLMData:        llm.Message{Role: llm.MessageRoleUser},
		OtherUsageData: otherUsage,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := database.CreateMessage(ctx, CreateMessageParams{
		ConversationID: conv.ConversationID,
		Type:           MessageTypeAgent,
		LLMData:        llm.Message{Role: llm.MessageRoleAssistant},
	}); err != nil {
		t.Fatal(err)
	}

	messages, err := database.ListMessages(ctx, conv.ConversationID)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 {
		t.Fatalf("got %d messages, want 2", len(messages))
	}
	if messages[0].OtherUsageData == nil {
		t.Fatal("message[0].OtherUsageData = nil, want JSON array")
	}
	var back []llm.PurposedUsage
	if err := json.Unmarshal([]byte(*messages[0].OtherUsageData), &back); err != nil {
		t.Fatal(err)
	}
	if len(back) != 2 || back[0].Purpose != "keyword_search" || back[0].InputTokens != 100 || back[1].Purpose != "llm_one_shot" {
		t.Errorf("round-trip = %+v", back)
	}
	if messages[1].OtherUsageData != nil {
		t.Errorf("message[1].OtherUsageData = %v, want nil", *messages[1].OtherUsageData)
	}

	// Forking copies other_usage_data onto the fork's copies.
	forked, err := database.ForkConversation(ctx, conv.ConversationID, messages[1].SequenceID)
	if err != nil {
		t.Fatal(err)
	}
	forkedMsgs, err := database.ListMessages(ctx, forked.ConversationID)
	if err != nil {
		t.Fatal(err)
	}
	if len(forkedMsgs) != 2 {
		t.Fatalf("got %d forked messages, want 2", len(forkedMsgs))
	}
	if forkedMsgs[0].OtherUsageData == nil || *forkedMsgs[0].OtherUsageData != *messages[0].OtherUsageData {
		t.Errorf("forked message[0].OtherUsageData = %v, want %v", forkedMsgs[0].OtherUsageData, *messages[0].OtherUsageData)
	}
	if forkedMsgs[0].ForkedFromMessageID == nil || *forkedMsgs[0].ForkedFromMessageID != messages[0].MessageID {
		t.Errorf("fork provenance missing: %v", forkedMsgs[0].ForkedFromMessageID)
	}
	if forkedMsgs[1].OtherUsageData != nil {
		t.Errorf("forked message[1].OtherUsageData = %v, want nil", *forkedMsgs[1].OtherUsageData)
	}
}

func mustOtherUsageJSON(t *testing.T, entries []llm.PurposedUsage) string {
	t.Helper()
	data, err := json.Marshal(entries)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestSetFirstUserMessageOtherUsage(t *testing.T) {
	database := setupTestDB(t)
	defer database.Close()

	ctx := context.Background()
	conv, err := database.CreateConversation(ctx, stringPtr("slug-usage"), true, nil, nil, ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}

	// No user message yet: 0 rows updated.
	usageJSON := mustOtherUsageJSON(t, []llm.PurposedUsage{{Purpose: "slug", Usage: llm.Usage{InputTokens: 10, OutputTokens: 2, Model: "m"}}})
	n, err := database.SetFirstUserMessageOtherUsage(ctx, conv.ConversationID, usageJSON)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("updated %d rows with no user message, want 0", n)
	}

	// A system message first, then two user messages: the FIRST user message
	// gets the usage.
	if _, err := database.CreateMessage(ctx, CreateMessageParams{
		ConversationID: conv.ConversationID,
		Type:           MessageTypeSystem,
		LLMData:        llm.Message{},
	}); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := database.CreateMessage(ctx, CreateMessageParams{
			ConversationID: conv.ConversationID,
			Type:           MessageTypeUser,
			LLMData:        llm.Message{Role: llm.MessageRoleUser},
		}); err != nil {
			t.Fatal(err)
		}
	}
	n, err = database.SetFirstUserMessageOtherUsage(ctx, conv.ConversationID, usageJSON)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("updated %d rows, want 1", n)
	}

	messages, err := database.ListMessages(ctx, conv.ConversationID)
	if err != nil {
		t.Fatal(err)
	}
	if messages[1].OtherUsageData == nil || *messages[1].OtherUsageData != usageJSON {
		t.Errorf("first user message OtherUsageData = %v, want %s", messages[1].OtherUsageData, usageJSON)
	}
	if messages[0].OtherUsageData != nil || messages[2].OtherUsageData != nil {
		t.Errorf("usage attached to wrong messages: %+v", messages)
	}
}

func TestGetSubagentOtherUsage(t *testing.T) {
	database := setupTestDB(t)
	defer database.Close()

	ctx := context.Background()
	parent, err := database.CreateConversation(ctx, stringPtr("other-usage-parent"), true, nil, nil, ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	child, err := database.CreateSubagentConversation(ctx, "other-usage-child", parent.ConversationID, nil)
	if err != nil {
		t.Fatal(err)
	}
	grandchild, err := database.CreateSubagentConversation(ctx, "other-usage-grandchild", child.ConversationID, nil)
	if err != nil {
		t.Fatal(err)
	}

	add := func(convID string, entries []llm.PurposedUsage) {
		t.Helper()
		if _, err := database.CreateMessage(ctx, CreateMessageParams{
			ConversationID: convID,
			Type:           MessageTypeUser,
			LLMData:        llm.Message{Role: llm.MessageRoleUser},
			OtherUsageData: entries,
		}); err != nil {
			t.Fatal(err)
		}
	}
	// Two entries for the same model on one child message + one on a
	// grandchild message aggregate into a single row; a different model gets
	// its own row. The parent's own entries must NOT be counted.
	add(child.ConversationID, []llm.PurposedUsage{
		{Purpose: "keyword_search", Usage: llm.Usage{InputTokens: 100, CacheReadInputTokens: 7, OutputTokens: 10, CostUSD: 0.01, Model: "m1", URL: "u1"}},
		{Purpose: "llm_one_shot", Usage: llm.Usage{InputTokens: 50, OutputTokens: 5, CostUSD: 0.005, Model: "m1", URL: "u1"}},
	})
	add(grandchild.ConversationID, []llm.PurposedUsage{
		{Purpose: "subagent_progress", Usage: llm.Usage{InputTokens: 30, OutputTokens: 3, CostUSD: 0.003, Model: "m1", URL: "u1"}},
		{Purpose: "keyword_search", Usage: llm.Usage{InputTokens: 9, OutputTokens: 1, Model: "m2", URL: "u2"}},
		// No model/URL (omitempty drops them from the JSON): must aggregate
		// as empty strings, not fail scanning NULL.
		{Purpose: "tool_install", Usage: llm.Usage{InputTokens: 4, OutputTokens: 2}},
	})
	add(parent.ConversationID, []llm.PurposedUsage{
		{Purpose: "compaction", Usage: llm.Usage{InputTokens: 999, CostUSD: 9, Model: "m1", URL: "u1"}},
	})

	rows, err := database.GetSubagentOtherUsage(ctx, parent.ConversationID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("got %d rows, want 3: %+v", len(rows), rows)
	}
	byModel := map[string]int{}
	for i, r := range rows {
		byModel[r.ModelName] = i
	}
	m1 := rows[byModel["m1"]]
	if m1.LlmApiUrl != "u1" || m1.LlmCalls != 3 || m1.InputTokens != 180 || m1.CacheReadInputTokens != 7 ||
		m1.OutputTokens != 18 || m1.CostUsd < 0.0179 || m1.CostUsd > 0.0181 {
		t.Errorf("m1 row = %+v", m1)
	}
	m2 := rows[byModel["m2"]]
	if m2.LlmApiUrl != "u2" || m2.LlmCalls != 1 || m2.InputTokens != 9 {
		t.Errorf("m2 row = %+v", m2)
	}
	noModel := rows[byModel[""]]
	if noModel.LlmApiUrl != "" || noModel.LlmCalls != 1 || noModel.InputTokens != 4 || noModel.OutputTokens != 2 {
		t.Errorf("no-model row = %+v", noModel)
	}
}
