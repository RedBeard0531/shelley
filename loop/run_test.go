package loop

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"testing/synctest"

	"shelley.exe.dev/llm"
)

type runTestService struct {
	requests []*llm.Request
	first    []llm.Content
	response *llm.Response
	failure  error
}

func (s *runTestService) Do(_ context.Context, req *llm.Request) (*llm.Response, error) {
	s.requests = append(s.requests, req)
	if len(s.requests) == 1 {
		if s.response != nil || s.failure != nil {
			return s.response, s.failure
		}
		content := s.first
		if content == nil {
			content = []llm.Content{{Type: llm.ContentTypeToolUse, ID: "job", ToolName: "work", ToolInput: json.RawMessage(`{}`)}}
		}
		return &llm.Response{Role: llm.MessageRoleAssistant, StopReason: llm.StopReasonToolUse, Content: content}, nil
	}
	return &llm.Response{Role: llm.MessageRoleAssistant, StopReason: llm.StopReasonEndTurn, Content: llm.TextContent("done")}, nil
}

func (*runTestService) Provider() string       { return "test" }
func (*runTestService) MaxImageDimension() int { return 0 }
func (*runTestService) MaxImageBytes() int     { return 0 }
func (*runTestService) SupportsImages() bool   { return false }

func TestRunDrainsPendingBetweenModelRequests(t *testing.T) {
	service := &runTestService{}
	var recorded []llm.Message
	checks := 0
	err := Run(t.Context(), RunConfig{
		LLM:      service,
		Messages: []llm.Message{{Role: llm.MessageRoleUser, Content: llm.TextContent("start")}},
		Tools: []*llm.Tool{{Name: "work", InputSchema: json.RawMessage(`{"type":"object","properties":{}}`), Run: func(context.Context, json.RawMessage) llm.ToolOut {
			return llm.ToolOut{LLMContent: llm.TextContent("finished")}
		}}},
		Pending: func(context.Context) ([]llm.Message, error) {
			checks++
			if checks == 2 {
				return []llm.Message{{Role: llm.MessageRoleUser, Content: llm.TextContent("child completed")}}, nil
			}
			return nil, nil
		},
		Hooks: Hooks{
			OnResponse: func(_ context.Context, response Response) error {
				recorded = append(recorded, response.Message)
				return nil
			},
			OnToolResponse: func(_ context.Context, response ToolResponse) error {
				recorded = append(recorded, response.Message)
				return nil
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if checks != 2 || len(service.requests) != 2 {
		t.Fatalf("pending checks = %d, requests = %d; want two of each", checks, len(service.requests))
	}
	messages := service.requests[1].Messages
	if len(messages) != 4 || messages[2].Content[0].ToolUseID != "job" || messages[3].Content[0].Text != "child completed" {
		t.Fatalf("second request lost tool-result / pending order: %+v", messages)
	}
	if len(recorded) != 3 || recorded[0].Role != llm.MessageRoleAssistant || recorded[1].Content[0].ToolUseID != "job" || recorded[2].Content[0].Text != "done" {
		t.Fatalf("persisted responses out of order: %+v", recorded)
	}
}

func TestRunConcurrentToolsPreserveResponseOrder(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		started := make(chan string, 2)
		release := make(chan struct{})
		tools := make([]*llm.Tool, 0, 2)
		service := &runTestService{}
		for _, name := range []string{"first", "second"} {
			tools = append(tools, &llm.Tool{
				Name: name, InputSchema: llm.MustSchema(`{"type":"object","properties":{}}`),
				Run: func(context.Context, json.RawMessage) llm.ToolOut {
					started <- name
					<-release
					return llm.ToolOut{LLMContent: llm.TextContent(name)}
				},
			})
			service.first = append(service.first, llm.Content{Type: llm.ContentTypeToolUse, ID: name, ToolName: name, ToolInput: json.RawMessage(`{}`)})
		}
		done := make(chan error, 1)
		go func() {
			done <- Run(t.Context(), RunConfig{LLM: service, Messages: []llm.Message{{Role: llm.MessageRoleUser, Content: llm.TextContent("work")}}, Tools: tools})
		}()
		synctest.Wait()
		if len(started) != 2 {
			t.Fatalf("only %d of 2 tools started concurrently", len(started))
		}
		close(release)
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		results := service.requests[1].Messages[2].Content
		for i, name := range []string{"first", "second"} {
			if results[i].ToolUseID != name || results[i].ToolResult[0].Text != name {
				t.Fatalf("result %d = %+v, want %s", i, results[i], name)
			}
		}
	})
}

type cancellingRunService struct{ entered chan struct{} }

func (s *cancellingRunService) Do(ctx context.Context, _ *llm.Request) (*llm.Response, error) {
	close(s.entered)
	<-ctx.Done()
	return nil, ctx.Err()
}
func (*cancellingRunService) Provider() string       { return "test" }
func (*cancellingRunService) MaxImageDimension() int { return 0 }
func (*cancellingRunService) MaxImageBytes() int     { return 0 }
func (*cancellingRunService) SupportsImages() bool   { return false }

func TestRunCancellationDoesNotPublishModelFailure(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		service := &cancellingRunService{entered: make(chan struct{})}
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		var recorded []llm.Message
		done := make(chan error, 1)
		go func() {
			done <- Run(ctx, RunConfig{
				LLM: service, Messages: []llm.Message{{Role: llm.MessageRoleUser, Content: llm.TextContent("hello")}},
				Hooks: Hooks{OnResponse: func(_ context.Context, response Response) error {
					recorded = append(recorded, response.Message)
					return nil
				}},
			})
		}()
		<-service.entered
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatalf("Run error = %v, want cancellation", err)
		}
		if len(recorded) != 0 {
			t.Fatalf("canceled request recorded a model failure: %+v", recorded)
		}
	})
}

func TestRunDoesNotExecuteUnpersistedToolUse(t *testing.T) {
	service := &runTestService{}
	called := false
	err := Run(t.Context(), RunConfig{
		LLM: service,
		Tools: []*llm.Tool{{Name: "work", Run: func(context.Context, json.RawMessage) llm.ToolOut {
			called = true
			return llm.ToolOut{}
		}}},
		Hooks: Hooks{OnResponse: func(context.Context, Response) error { return errors.New("storage unavailable") }},
	})
	if !errors.Is(err, errMessagePersistence) || called || len(service.requests) != 1 {
		t.Fatalf("error = %v, tool ran = %v, requests = %d; tool-use must be stored first", err, called, len(service.requests))
	}
}

func TestRunDoesNotRequestModelWithUnpersistedToolResult(t *testing.T) {
	service := &runTestService{}
	err := Run(t.Context(), RunConfig{
		LLM: service,
		Tools: []*llm.Tool{{Name: "work", Run: func(context.Context, json.RawMessage) llm.ToolOut {
			return llm.ToolOut{LLMContent: llm.TextContent("result")}
		}}},
		Hooks: Hooks{OnToolResponse: func(context.Context, ToolResponse) error { return errors.New("storage unavailable") }},
	})
	if !errors.Is(err, errMessagePersistence) || len(service.requests) != 1 {
		t.Fatalf("error = %v, requests = %d; failed tool result must not reach model", err, len(service.requests))
	}
}

func TestRunPassesWorkingDirToTools(t *testing.T) {
	service := &runTestService{}
	var got string
	err := Run(t.Context(), RunConfig{
		LLM: service, WorkingDir: "/workspace/project",
		Tools: []*llm.Tool{{Name: "work", Run: func(ctx context.Context, _ json.RawMessage) llm.ToolOut {
			got = llm.WorkingDir(ctx)
			return llm.ToolOut{LLMContent: llm.TextContent("done")}
		}}},
	})
	if err != nil || got != "/workspace/project" {
		t.Fatalf("error = %v, tool working dir = %q", err, got)
	}
}

func TestRunRefusalCarriesModelChoice(t *testing.T) {
	for _, tc := range []struct{ configured, want string }{
		{configured: "chosen-model", want: "chosen-model"},
		{want: "provider-model"},
	} {
		t.Run(tc.want, func(t *testing.T) {
			service := &runTestService{response: &llm.Response{
				Role: llm.MessageRoleAssistant, Model: "provider-model", StopReason: llm.StopReasonRefusal,
			}}
			var notice llm.Message
			err := Run(t.Context(), RunConfig{
				LLM: service, ModelID: tc.configured,
				Hooks: Hooks{OnResponse: func(_ context.Context, response Response) error {
					if response.Message.ErrorType == llm.ErrorTypeRefusal {
						notice = response.Message
					}
					return nil
				}},
			})
			if err != nil || notice.RefusalModel != tc.want || !strings.Contains(notice.Content[0].Text, "Choose another model") || strings.Contains(notice.Content[0].Text, "Opus") {
				t.Fatalf("error = %v, refusal notice = %+v", err, notice)
			}
		})
	}
}

func TestRunFlushesStreamBeforeRecording(t *testing.T) {
	for _, failed := range []bool{false, true} {
		service := &runTestService{response: &llm.Response{Role: llm.MessageRoleAssistant, StopReason: llm.StopReasonEndTurn}}
		if failed {
			service.failure = errors.New("not retryable")
			service.response = nil
		}
		var events []string
		err := Run(t.Context(), RunConfig{LLM: service, Hooks: Hooks{
			OnStreamDone: func() { events = append(events, "flush") },
			OnResponse: func(context.Context, Response) error {
				events = append(events, "record")
				return nil
			},
		}})
		if (err != nil) != failed || !slices.Equal(events, []string{"flush", "record"}) {
			t.Fatalf("failed = %v, error = %v, events = %v", failed, err, events)
		}
	}
}
