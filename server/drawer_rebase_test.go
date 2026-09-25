package server

import (
	"context"
	"errors"
	"testing"
	"time"

	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/llm"
)

func TestConversationListPreservesCostsAndQueryCounts(t *testing.T) {
	t.Parallel()
	srv, database, _ := newTestServer(t)
	ctx := t.Context()
	parent, err := database.CreateConversation(ctx, nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	child, err := database.CreateSubagentConversation(ctx, "cost-child", parent.ConversationID, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range []struct {
		id               string
		direct, indirect float64
	}{
		{parent.ConversationID, 1, 2}, {child.ConversationID, 3, 4},
	} {
		_, err := database.CreateMessage(ctx, db.CreateMessageParams{
			ConversationID: entry.id, Type: db.MessageTypeAgent,
			LLMData:   llm.Message{Role: llm.MessageRoleAssistant, Content: llm.TextContent("priced reply")},
			ModelName: "rebase-test-unpriced", LLMAPIURL: "https://example.test/v1",
			UsageData:      llm.Usage{CostUSD: entry.direct, Model: "rebase-test-unpriced"},
			OtherUsageData: []llm.PurposedUsage{{Purpose: "compaction", Usage: llm.Usage{CostUSD: entry.indirect, Model: "rebase-test-unpriced"}}},
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := database.QueriesTx(ctx, func(q *generated.Queries) error {
		return q.InsertBackgroundJob(ctx, generated.InsertBackgroundJobParams{
			JobID: "cost-job", ConversationID: parent.ConversationID,
			ToolUseID: "cost-tool", Command: "test", StartedAt: time.Now(),
		})
	}); err != nil {
		t.Fatal(err)
	}
	for _, includeSubagents := range []bool{false, true} {
		rows, err := srv.conversationListWithStateInternal(ctx, 100, 0, "", false, includeSubagents)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, row := range rows {
			if row.ConversationID != parent.ConversationID {
				continue
			}
			found = true
			if row.SubagentCount != 1 || row.RunningBackgroundJobs != 1 || row.CostUsd != 3 || row.TotalCostUsd != 10 {
				t.Fatalf("includeSubagents=%v: counts/costs = %+v", includeSubagents, row)
			}
		}
		if !found {
			t.Fatal("parent missing from conversation list")
		}
	}
}

func TestConversationListDecorationPropagatesCostError(t *testing.T) {
	t.Parallel()
	srv, _, _ := newTestServer(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	rows, err := srv.decorateConversations(ctx, nil)
	if !errors.Is(err, context.Canceled) || rows != nil {
		t.Fatalf("decorate with failed cost query = %v, %v", rows, err)
	}
}
