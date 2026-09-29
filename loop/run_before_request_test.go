package loop

import (
	"context"
	"fmt"
	"testing"

	"shelley.exe.dev/llm"
)

type beforeRequestService struct {
	request *llm.Request
	calls   int
}

func (s *beforeRequestService) Do(_ context.Context, request *llm.Request) (*llm.Response, error) {
	s.calls++
	s.request = request
	return &llm.Response{Role: llm.MessageRoleAssistant, StopReason: llm.StopReasonEndTurn, Content: llm.TextContent("done")}, nil
}
func (*beforeRequestService) Provider() string        { return "test" }
func (*beforeRequestService) TokenContextWindow() int { return 1_000_000 }
func (*beforeRequestService) MaxImageDimension() int  { return 0 }
func (*beforeRequestService) MaxImageBytes() int      { return 0 }
func (*beforeRequestService) SupportsImages() bool    { return false }

func TestExcludedSystemMessageBecomesRequestSystem(t *testing.T) {
	service := &beforeRequestService{}
	err := Run(context.Background(), RunConfig{
		LLM: service,
		Messages: []llm.Message{
			{Role: llm.MessageRoleUser, Content: llm.TextContent("[wake]")},
			{Role: llm.MessageRoleSystem, Content: llm.TextContent("resume safely"), ExcludedFromContext: true},
		},
		System: []llm.SystemContent{{Type: "text", Text: "base"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if service.request == nil {
		t.Fatal("model was not called")
	}
	if len(service.request.Messages) != 1 || service.request.Messages[0].Role != llm.MessageRoleUser {
		t.Fatalf("request messages = %+v", service.request.Messages)
	}
	if len(service.request.System) != 2 || service.request.System[0].Text != "base" || service.request.System[1].Text != "resume safely" {
		t.Fatalf("request system = %+v", service.request.System)
	}
}

func TestBeforeRequestAppendsEphemeralTailMessage(t *testing.T) {
	service := &beforeRequestService{}
	tail := llm.Message{Role: llm.MessageRoleSystem, Content: llm.TextContent("reminder")}
	err := Run(context.Background(), RunConfig{
		LLM:      service,
		Messages: []llm.Message{{Role: llm.MessageRoleUser, Content: llm.TextContent("hello")}},
		System:   []llm.SystemContent{{Type: "text", Text: "base"}},
		BeforeRequest: func(_ context.Context, messages []llm.Message) (BeforeRequestPolicy, error) {
			messages[0].Content[0].Text = "mutated"
			return BeforeRequestPolicy{ExtraTail: &tail}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if service.request == nil || len(service.request.System) != 1 || service.request.System[0].Text != "base" {
		t.Fatalf("request system = %+v", service.request)
	}
	messages := service.request.Messages
	if len(messages) != 2 || messages[1].Role != llm.MessageRoleSystem || messages[1].Content[0].Text != "reminder" {
		t.Fatalf("request messages = %+v", messages)
	}
	if !messages[0].Content[len(messages[0].Content)-1].Cache {
		t.Fatalf("cache breakpoint left the last real user message: %+v", messages[0])
	}
	if messages[1].Content[0].Cache {
		t.Fatalf("ephemeral tail carries the cache breakpoint: %+v", messages[1])
	}
	if messages[0].Content[0].Text != "hello" {
		t.Fatalf("callback mutated run history: %+v", messages)
	}
}

func TestBeforeRequestSystemTailFollowsRequestOnlyUserContext(t *testing.T) {
	service := &beforeRequestService{}
	err := Run(t.Context(), RunConfig{
		LLM:      service,
		Messages: []llm.Message{{Role: llm.MessageRoleUser, Content: llm.TextContent("hello")}},
		BeforeRequest: func(context.Context, []llm.Message) (BeforeRequestPolicy, error) {
			return BeforeRequestPolicy{
				UserContext: llm.TextContent("linked question"),
				ExtraTail:   &llm.Message{Role: llm.MessageRoleSystem, Content: llm.TextContent("context nudge")},
			}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	messages := service.request.Messages
	if len(messages) != 3 || messages[1].Role != llm.MessageRoleUser || messages[1].Content[0].Text != "linked question" ||
		messages[2].Role != llm.MessageRoleSystem || messages[2].Content[0].Text != "context nudge" {
		t.Fatalf("Claude inline system must follow the user context and end the request: %+v", messages)
	}
}

func TestBeforeRequestSkipsTailWhenLastMessageNotUser(t *testing.T) {
	service := &beforeRequestService{}
	err := Run(context.Background(), RunConfig{
		LLM: service,
		Messages: []llm.Message{
			{Role: llm.MessageRoleUser, Content: llm.TextContent("hello")},
			{Role: llm.MessageRoleAssistant, Content: llm.TextContent("working on it")},
		},
		BeforeRequest: func(context.Context, []llm.Message) (BeforeRequestPolicy, error) {
			return BeforeRequestPolicy{ExtraTail: &llm.Message{Role: llm.MessageRoleSystem, Content: llm.TextContent("reminder")}}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, msg := range service.request.Messages {
		if msg.Role == llm.MessageRoleSystem {
			t.Fatalf("tail appended after non-user message: %+v", service.request.Messages)
		}
	}
}

func TestBeforeRequestDivertsWithoutCallingModel(t *testing.T) {
	service := &beforeRequestService{}
	err := Run(context.Background(), RunConfig{
		LLM:      service,
		Messages: []llm.Message{{Role: llm.MessageRoleUser, Content: llm.TextContent("hello")}},
		BeforeRequest: func(context.Context, []llm.Message) (BeforeRequestPolicy, error) {
			return BeforeRequestPolicy{Divert: true}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if service.calls != 0 {
		t.Fatalf("model calls = %d, want 0", service.calls)
	}
}

func TestBeforeRequestErrorStopsRun(t *testing.T) {
	service := &beforeRequestService{}
	err := Run(context.Background(), RunConfig{
		LLM:      service,
		Messages: []llm.Message{{Role: llm.MessageRoleUser, Content: llm.TextContent("hello")}},
		BeforeRequest: func(context.Context, []llm.Message) (BeforeRequestPolicy, error) {
			return BeforeRequestPolicy{}, fmt.Errorf("policy failed")
		},
	})
	if err == nil || err.Error() != "before model request: policy failed" {
		t.Fatalf("error = %v", err)
	}
	if service.calls != 0 {
		t.Fatalf("model calls = %d, want 0", service.calls)
	}
}

func TestRequestOnlyUserContextAndCompletionValidation(t *testing.T) {
	service := &beforeRequestService{}
	err := Run(t.Context(), RunConfig{
		LLM:      service,
		Messages: []llm.Message{{Role: llm.MessageRoleAssistant, Content: llm.TextContent("previous turn")}},
		BeforeRequest: func(context.Context, []llm.Message) (BeforeRequestPolicy, error) {
			return BeforeRequestPolicy{UserContext: llm.TextContent("linked data")}, nil
		},
		ValidateCompletion: func(context.Context) error { return fmt.Errorf("unresolved obligation") },
	})
	if err == nil || err.Error() != "unresolved obligation" {
		t.Fatalf("completion: %v", err)
	}
	if service.calls != 1 {
		t.Fatalf("validation failure retried model: %d", service.calls)
	}
	messages := service.request.Messages
	if len(messages) != 2 || messages[1].Role != llm.MessageRoleUser || messages[1].Content[0].Text != "linked data" || messages[1].Content[0].Cache {
		t.Fatalf("transient context: %+v", messages)
	}
}
