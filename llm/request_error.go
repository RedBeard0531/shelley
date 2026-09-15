package llm

import (
	"errors"
	"time"
)

// RequestErrorInfo describes a provider-neutral LLM request failure.
type RequestErrorInfo struct {
	// Retryable reports whether manually repeating the request is safe.
	Retryable bool
	// NoImmediateRetry reports that the provider already handled or deliberately
	// declined automatic retries. Callers may still offer a manual retry.
	NoImmediateRetry bool
	// IdleStallDuration is the no-progress window before an idle abort.
	IdleStallDuration time.Duration
}

// RequestError exposes provider-neutral LLM request failure metadata.
type RequestError interface {
	error
	RequestErrorInfo() RequestErrorInfo
}

// streamInterruptedError marks a request that died after the response body had
// started. Partial output may already have reached the caller, so re-sending
// the request is safe and usually worthwhile, but whatever the failed attempt
// streamed has to be thrown away before the new attempt's deltas arrive.
type streamInterruptedError struct {
	err error
}

func (e *streamInterruptedError) Error() string { return e.err.Error() }

func (e *streamInterruptedError) Unwrap() error { return e.err }

func (e *streamInterruptedError) RequestErrorInfo() RequestErrorInfo {
	// A provider that already classified this failure (an idle-stall timeout,
	// say) has more to say than "retryable" — keep its verdict, including its
	// idle window. Otherwise the mid-response death itself is the news: the
	// request is worth retrying from scratch.
	if inner, ok := RequestErrorInfoFromError(e.err); ok {
		return inner
	}
	return RequestErrorInfo{Retryable: true}
}

// MarkStreamInterrupted marks err as a failure that happened mid-response
// (after the response body started). Consumers can retry the request as sent,
// but must discard the partial output of the failed attempt — see
// Request.OnStreamRestart, which providers call before re-issuing such a
// request themselves.
func MarkStreamInterrupted(err error) error {
	if err == nil {
		return nil
	}
	return &streamInterruptedError{err: err}
}

// RequestErrorInfoFromError finds request failure metadata in err.
func RequestErrorInfoFromError(err error) (RequestErrorInfo, bool) {
	var requestErr RequestError
	if !errors.As(err, &requestErr) {
		return RequestErrorInfo{}, false
	}
	return requestErr.RequestErrorInfo(), true
}
