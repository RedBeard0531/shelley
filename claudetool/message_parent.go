package claudetool

import (
	"context"
	"encoding/json"
	"strings"

	"shelley.exe.dev/llm"
)

const messageParentName = "message_parent"

// ParentMessenger delivers a subagent's message to its parent conversation.
// This is implemented by the server package to avoid import cycles.
type ParentMessenger interface {
	// MessageParent queues text in the parent of conversationID, attributed
	// to that subagent. A busy parent sees it at its next LLM request; an
	// idle parent starts a turn.
	MessageParent(ctx context.Context, conversationID, text string) error
}

// MessageParentTool lets a subagent message its parent before finishing.
type MessageParentTool struct {
	Messenger      ParentMessenger
	ConversationID string
}

func (t *MessageParentTool) Tool() *llm.Tool {
	return &llm.Tool{
		Name:        messageParentName,
		Description: "Send a message to your parent agent now, without ending your turn: a progress note, an interim finding, or a question you need answered. Keep working after sending; replies arrive as new messages. Your final reply is not forwarded to the parent, so send your results with this tool before you finish.",
		InputSchema: llm.MustSchema(`{
  "type": "object",
  "required": ["text"],
  "properties": {
    "text": {"type": "string", "description": "The message for the parent."}
  }
}`),
		Run: t.run,
	}
}

func (t *MessageParentTool) run(ctx context.Context, input json.RawMessage) llm.ToolOut {
	var req struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(input, &req); err != nil {
		return llm.ErrorfToolOut("parse input: %w", err)
	}
	if strings.TrimSpace(req.Text) == "" {
		return llm.ErrorfToolOut("text is required")
	}
	if err := t.Messenger.MessageParent(ctx, t.ConversationID, req.Text); err != nil {
		return llm.ErrorfToolOut("message parent: %w", err)
	}
	return llm.ToolOut{LLMContent: llm.TextContent("Sent to parent.")}
}
