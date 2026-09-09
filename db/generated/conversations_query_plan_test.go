package generated

import (
	"database/sql"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func TestSubagentUsageQueriesSearchMessagesByConversation(t *testing.T) {
	t.Parallel()
	database, err := sql.Open("sqlite", "file:"+t.TempDir()+"/query-plan.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	for _, statement := range []string{
		`CREATE TABLE conversations (conversation_id TEXT PRIMARY KEY, parent_conversation_id TEXT)`,
		`CREATE INDEX idx_conversations_parent_id ON conversations(parent_conversation_id)`,
		`CREATE TABLE messages (conversation_id TEXT, sequence_id INTEGER, type TEXT, usage_data TEXT, other_usage_data TEXT, model_name TEXT, llm_api_url TEXT)`,
		`CREATE INDEX idx_messages_conversation_id ON messages(conversation_id)`,
		`CREATE INDEX idx_messages_type ON messages(type)`,
		`CREATE INDEX idx_messages_conv_type_seq ON messages(conversation_id, type, sequence_id)`,
	} {
		if _, err := database.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}

	tests := []struct {
		name  string
		query string
		want  string
	}{
		{"direct usage", getSubagentUsage, "SEARCH m USING INDEX idx_messages_conv_type_seq (conversation_id=? AND type=?)"},
		{"indirect usage", getSubagentOtherUsage, "SEARCH m USING INDEX idx_messages_conversation_id (conversation_id=?)"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rows, err := database.Query("EXPLAIN QUERY PLAN "+test.query, "parent")
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()

			var plan []string
			for rows.Next() {
				var id, parent, unused int
				var detail string
				if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
					t.Fatal(err)
				}
				plan = append(plan, detail)
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			joined := strings.Join(plan, "\n")
			if !strings.Contains(joined, test.want) {
				t.Fatalf("query plan does not search messages by conversation:\n%s", joined)
			}
		})
	}
}
