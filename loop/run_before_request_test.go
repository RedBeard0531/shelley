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

func TestBeforeRequestAddsEphemeralSystemWithoutMutatingHistory(t *testing.T) {
	service := &beforeRequestService{}
	original := []llm.Message{{Role: llm.MessageRoleUser, Content: llm.TextContent("hello")}}
	err := Run(t.Context(), RunConfig{
		LLM: service, Messages: original,
		System: []llm.SystemContent{{Type: "text", Text: "base"}},
		BeforeRequest: func(_ context.Context, messages []llm.Message) (BeforeRequestPolicy, error) {
			messages[0].Content[0].Text = "mutated"
			return BeforeRequestPolicy{System: []llm.SystemContent{{Type: "text", Text: "reminder"}}}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(service.request.System) != 2 || service.request.System[0].Text != "base" || service.request.System[1].Text != "reminder" {
		t.Fatalf("request system = %+v", service.request.System)
	}
	if len(service.request.Messages) != 1 || service.request.Messages[0].Content[0].Text != "hello" || original[0].Content[0].Text != "hello" {
		t.Fatalf("policy mutated messages: request=%+v original=%+v", service.request.Messages, original)
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
