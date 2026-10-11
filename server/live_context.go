package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/llm"
)

const (
	liveContextMessages  = 6   // user requests and final assistant replies kept
	liveContextTailRows  = 100 // rows scanned (tool rows included) to find them
	liveContextItemRunes = 800 // per-item cap; keeps well under Live's input limit
)

// liveHistoryItem is one prior text turn in Live's session.input format.
type liveHistoryItem struct {
	Type    string            `json:"type"`
	Role    string            `json:"role"`
	Content []liveHistoryText `json:"content"`
}

type liveHistoryText struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// liveContext returns only the user's typed messages and final backend
// replies. Tool results, subagent reports, background jobs and other machine
// notices must not enter the Live session.
func (s *Server) liveContext(ctx context.Context, conversationID string) ([]liveHistoryItem, error) {
	var rows []generated.Message
	err := s.db.Queries(ctx, func(q *generated.Queries) error {
		var err error
		rows, err = q.ListMessagesTail(ctx, generated.ListMessagesTailParams{ConversationID: conversationID, Limit: liveContextTailRows})
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("load live context: %w", err)
	}
	var items []liveHistoryItem
	for _, row := range rows {
		if row.LlmData == nil || row.ExcludedFromContext || (row.Type != "user" && row.Type != "agent") {
			continue
		}
		var message llm.Message
		if err := json.Unmarshal([]byte(*row.LlmData), &message); err != nil {
			return nil, fmt.Errorf("decode message %s: %w", row.MessageID, err)
		}
		role, contentType := "assistant", "output_text"
		if row.Type == "user" {
			role, contentType = "user", "input_text"
			if !typedUserForLLM(message, []byte(derefString(row.UserData))) {
				continue
			}
		} else if !message.EndOfTurn {
			continue
		}
		var text []string
		for _, c := range message.Content {
			if c.Type == llm.ContentTypeText {
				// A delegated task carries the voice exchange it came from
				// for the backend; Live already has that speech.
				t, _, _ := strings.Cut(c.Text, liveVoiceExchange)
				if t = strings.TrimSpace(t); t != "" {
					text = append(text, t)
				}
			}
		}
		if len(text) != 0 {
			items = append(items, liveHistoryItem{Type: "message", Role: role, Content: []liveHistoryText{{Type: contentType, Text: truncateRunes(strings.Join(text, "\n\n"), liveContextItemRunes)}}})
		}
	}
	return items[max(0, len(items)-liveContextMessages):], nil
}

func truncateRunes(s string, limit int) string {
	r := []rune(s)
	if len(r) <= limit {
		return s
	}
	return string(r[:limit]) + "…"
}

// liveSituation is the bounded current state line for Live's instructions.
func (s *Server) liveSituation(conversation *generated.Conversation) string {
	state := "idle"
	if conversation.AgentWorking {
		state = "working"
	}
	situation := "Backend state: " + state + "."
	if cwd := derefString(conversation.Cwd); cwd != "" {
		situation += " Working directory: " + truncateRunes(cwd, 300) + "."
	}
	model := derefString(conversation.Model)
	if model == "" {
		model = s.effectiveDefaultModel(s.getModelList())
	}
	return situation + " Backend model: " + truncateRunes(model, 100) + "."
}
