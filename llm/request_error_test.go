package llm

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

type testRequestError struct {
	info RequestErrorInfo
}

func (e *testRequestError) Error() string { return "request failed" }

func (e *testRequestError) RequestErrorInfo() RequestErrorInfo { return e.info }

func TestRequestErrorInfoFromError(t *testing.T) {
	want := RequestErrorInfo{Retryable: true, IdleStallDuration: 3 * time.Minute}
	err := errors.Join(errors.New("earlier attempt"), fmt.Errorf("latest attempt: %w", &testRequestError{info: want}))
	got, ok := RequestErrorInfoFromError(err)
	if !ok || got != want {
		t.Fatalf("RequestErrorInfoFromError = %+v, %v; want %+v, true", got, ok, want)
	}
}

func TestRequestErrorInfoFromErrorWithoutMetadata(t *testing.T) {
	got, ok := RequestErrorInfoFromError(errors.New("request failed"))
	if ok || got != (RequestErrorInfo{}) {
		t.Fatalf("RequestErrorInfoFromError = %+v, %v; want zero value, false", got, ok)
	}
}

func TestMarkStreamInterrupted(t *testing.T) {
	// Providers wrap mid-response failures in nested errors (attempt history,
	// URL/model context); the marker must survive that so retry classifiers
	// see it as retryable without pattern-matching provider text.
	err := errors.Join(
		errors.New("attempt 1 at 2026-09-15 21:02:02: status 500"),
		fmt.Errorf("attempt 2 at 2026-09-15 21:02:05: url=https://example.com/v1/chat/completions model=m: %w",
			MarkStreamInterrupted(errors.New("stream error: stream ID 15; INTERNAL_ERROR; received from peer"))),
	)
	info, ok := RequestErrorInfoFromError(err)
	if !ok || !info.Retryable {
		t.Fatalf("RequestErrorInfoFromError = %+v, %v; want Retryable true", info, ok)
	}
	// The message is preserved verbatim: this text reaches the user's error bubble.
	want := "chat completion stream failed after response started"
	wrapped := MarkStreamInterrupted(fmt.Errorf("%s: stream error: stream ID 15", want))
	if !strings.Contains(wrapped.Error(), want) {
		t.Fatalf("MarkStreamInterrupted().Error() = %q, want it to contain %q", wrapped.Error(), want)
	}
	if MarkStreamInterrupted(nil) != nil {
		t.Fatal("MarkStreamInterrupted(nil) should be nil")
	}
}
