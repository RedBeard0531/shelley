package loop

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"shelley.exe.dev/llm"
	"shelley.exe.dev/llm/llmhttp"
)

// PendingMessages drains messages that arrived since the previous check.
// Run checks before every model request.
type PendingMessages func(context.Context) ([]llm.Message, error)

// Response is a completed assistant message and its direct model usage.
type Response struct {
	Message llm.Message
	Usage   llm.Usage
}

// ToolResponse is a completed tool-result message and any indirect LLM usage
// incurred while executing its tools.
type ToolResponse struct {
	Message    llm.Message
	OtherUsage []llm.PurposedUsage
}

// Hooks observe agent-loop lifecycle events. Errors from durable persistence
// hooks are logged and do not abort the run; streaming and tool-progress hooks
// are non-error callbacks.
type Hooks struct {
	OnStreamingResponse func(context.Context, llm.StreamDelta)
	OnToolProgress      func(context.Context, llm.ToolProgress)
	OnResponse          func(context.Context, Response) error
	OnSuccessfulRequest func(*llm.Request)
	OnToolResponse      func(context.Context, ToolResponse) error
	OnWarning           func(context.Context, string) error
}

// BeforeRequestPolicy is transient model-request policy, visible only to this
// request. ExtraTail is an ephemeral message appended to the end of the
// outgoing message copy — never written to run history or persisted. Divert
// ends the run before calling the model.
type BeforeRequestPolicy struct {
	// UserContext is resolved request-only data, appended without changing stored history.
	UserContext []llm.Content
	ExtraTail   *llm.Message
	Divert      bool
}

// BeforeRequest runs after pending input is appended and before every model
// request. The callback receives a defensive copy of the provider-visible
// history, so policy can inspect it without mutating the run.
type BeforeRequest func(context.Context, []llm.Message) (BeforeRequestPolicy, error)

// RunConfig describes one agent turn. It is separate from the long-lived
// Config until the conversation manager switches to Run.
type RunConfig struct {
	LLM           llm.Service
	Messages      []llm.Message
	Tools         []*llm.Tool
	System        []llm.SystemContent
	ThinkingLevel llm.ThinkingLevel
	// PromptCacheKey, when set, overrides provider prompt-cache affinity for
	// model requests. Tool-initiated model calls keep the context's key.
	PromptCacheKey string
	// MaxIterations bounds model requests in one run. Zero means unlimited.
	MaxIterations int
	Pending       PendingMessages
	BeforeRequest BeforeRequest
	// ValidateCompletion rejects successful completion when durable obligations remain.
	ValidateCompletion func(context.Context) error
	Hooks              Hooks
	Logger             *slog.Logger
}

type runner struct {
	RunConfig
}

// Run executes one agent turn. It checks RunConfig.Pending before every model
// request, appending every returned message. A terminal response ends the run.
func Run(ctx context.Context, config RunConfig) error {
	if config.LLM == nil {
		return fmt.Errorf("no LLM service configured")
	}
	if config.Logger == nil {
		config.Logger = slog.Default()
	}
	config.Messages = cloneMessages(config.Messages)
	r := &runner{RunConfig: config}
	config.Logger.Info("starting agent run", "tools", len(config.Tools))
	if err := r.run(ctx); err != nil {
		return err
	}
	if config.ValidateCompletion != nil {
		return config.ValidateCompletion(ctx)
	}
	return nil
}

func cloneMessages(messages []llm.Message) []llm.Message {
	return cloneMessagesWithFilter(messages, false)
}

func cloneMessagesForProvider(messages []llm.Message) []llm.Message {
	return cloneMessagesWithFilter(messages, true)
}

func cloneMessagesWithFilter(messages []llm.Message, filterDisplayOnly bool) []llm.Message {
	out := make([]llm.Message, len(messages))
	for i, message := range messages {
		out[i] = message
		out[i].Content = cloneContents(message.Content, filterDisplayOnly)
	}
	return out
}

func cloneContents(contents []llm.Content, filterDisplayOnly bool) []llm.Content {
	if contents == nil {
		return nil
	}
	out := make([]llm.Content, 0, len(contents))
	for _, content := range contents {
		if filterDisplayOnly && content.DisplayOnly {
			continue
		}
		cloned := content
		if content.ToolResult != nil {
			cloned.ToolResult = cloneContents(content.ToolResult, filterDisplayOnly)
		}
		out = append(out, cloned)
	}
	return out
}

func (l *runner) appendPending(ctx context.Context) (bool, error) {
	if l.Pending == nil {
		return false, nil
	}
	pending, err := l.Pending(ctx)
	if err != nil {
		return false, fmt.Errorf("load pending messages: %w", err)
	}
	l.Messages = append(l.Messages, pending...)
	return len(pending) > 0, nil
}

// splitRequestSystem lifts freshly promoted, context-excluded system messages
// into request-level instructions. They remain visible to BeforeRequest policy
// but never enter provider message history or a later run's context.
func splitRequestSystem(messages []llm.Message) ([]llm.Message, []llm.SystemContent) {
	providerMessages := make([]llm.Message, 0, len(messages))
	var requestSystem []llm.SystemContent
	for _, message := range messages {
		if message.Role != llm.MessageRoleSystem || !message.ExcludedFromContext {
			providerMessages = append(providerMessages, message)
			continue
		}
		textOnly := true
		for _, content := range message.Content {
			if content.Type != llm.ContentTypeText {
				textOnly = false
				break
			}
		}
		if !textOnly {
			providerMessages = append(providerMessages, message)
			continue
		}
		for _, content := range message.Content {
			requestSystem = append(requestSystem, llm.SystemContent{Type: "text", Text: content.Text})
		}
	}
	return providerMessages, requestSystem
}

// run sends requests to the LLM and handles responses until the model finishes
// without requesting another client-side tool call.
func (l *runner) run(ctx context.Context) error {
	iterations := 0
	for {
		if l.MaxIterations > 0 && iterations >= l.MaxIterations {
			return fmt.Errorf("agent loop exceeded %d iterations", l.MaxIterations)
		}
		iterations++
		if _, err := l.appendPending(ctx); err != nil {
			return err
		}

		var policy BeforeRequestPolicy
		if l.BeforeRequest != nil {
			var err error
			policy, err = l.BeforeRequest(ctx, cloneMessagesForProvider(l.Messages))
			if err != nil {
				return fmt.Errorf("before model request: %w", err)
			}
			if policy.Divert {
				return nil
			}
		}

		messages := cloneMessagesForProvider(l.Messages)
		messages, transientSystem := splitRequestSystem(messages)
		tools := l.Tools
		system := append([]llm.SystemContent(nil), l.System...)
		system = append(system, transientSystem...)
		llmService := l.LLM

		// Enable prompt caching: set cache flag on last tool and last user message content
		// See https://docs.anthropic.com/en/docs/build-with-claude/prompt-caching
		if len(tools) > 0 {
			// Make a copy of tools to avoid modifying the shared slice
			tools = append([]*llm.Tool(nil), tools...)
			// Copy the last tool and enable caching
			lastTool := *tools[len(tools)-1]
			lastTool.Cache = true
			tools[len(tools)-1] = &lastTool
		}

		// Set cache flag on the last content block of the last user message
		if len(messages) > 0 {
			for i := len(messages) - 1; i >= 0; i-- {
				if messages[i].Role == llm.MessageRoleUser && len(messages[i].Content) > 0 {
					// Deep copy the message to avoid modifying the shared history
					msg := messages[i]
					msg.Content = append([]llm.Content(nil), msg.Content...)
					msg.Content[len(msg.Content)-1].Cache = true
					messages[i] = msg
					break
				}
			}
		}

		onRetry := l.retryWarningHook(ctx)
		req := &llm.Request{
			Messages:      messages,
			Tools:         tools,
			System:        system,
			ThinkingLevel: l.ThinkingLevel,
			OnRetry:       onRetry,
		}
		if l.Hooks.OnStreamingResponse != nil {
			req.OnStream = func(delta llm.StreamDelta) {
				l.Hooks.OnStreamingResponse(ctx, delta)
			}
		}

		// Insert missing tool results if the previous message had tool_use blocks
		// without corresponding tool_result blocks. This can happen when a request
		// is cancelled or fails after the LLM responds but before tools execute.
		l.insertMissingToolResults(req)

		if len(policy.UserContext) > 0 {
			req.Messages = append(req.Messages, llm.Message{Role: llm.MessageRoleUser, Content: policy.UserContext})
		}

		// Append the ephemeral tail message last: after the cache-flag pass, so
		// the cache breakpoint stays on the last real user message with the tail
		// past it, and after insertMissingToolResults and request-only user context,
		// so the guard sees the final history. Anthropic requires inline system
		// messages to immediately follow a user turn and end the request; skip
		// the tail if the last message is not user-role.
		if policy.ExtraTail != nil && len(req.Messages) > 0 && req.Messages[len(req.Messages)-1].Role == llm.MessageRoleUser {
			req.Messages = append(req.Messages, *policy.ExtraTail)
		}

		systemLen := 0
		for _, sys := range system {
			systemLen += len(sys.Text)
		}
		l.Logger.Debug("sending LLM request", "message_count", len(messages), "tool_count", len(tools), "system_items", len(system), "system_length", systemLen)

		// sendWithRetry issues a single LLM request, retrying transient transport
		// failures (EOF, connection reset). Provider-internal retries own
		// user-visible retry warnings; this outer retry catches transport failures
		// that escape the provider without adding noise.
		//
		// Timeouts are layered:
		//   - The primary bound is the provider transport's idle/stall timeout,
		//     which aborts only when no bytes arrive for the idle window. This lets
		//     a slow-but-progressing turn (a long high-reasoning response, or a
		//     slow ChatGPT-subscription proxy hop) run to completion instead of
		//     dying at a fixed cap.
		//   - maxTurnDuration is a generous absolute backstop so a provider that
		//     keeps the socket warm with heartbeats/keepalives while otherwise
		//     wedged (which would keep resetting the idle timer) can't hang the
		//     turn forever. It is intentionally far larger than the idle window.
		// User cancellation still flows through ctx.
		// requestTrace collects correlation ids (our request id, plus
		// any upstream provider request id) for this turn so we can surface them
		// in a user-facing error — including on the idle/stall-timeout path, where
		// there is no successful response to read an id from.
		var requestTrace *llm.RequestTrace
		sendWithRetry := func(req *llm.Request) (*llm.Response, error) {
			llmCtx, cancel := context.WithTimeout(ctx, maxTurnDuration)
			defer cancel()
			if l.PromptCacheKey != "" {
				llmCtx = llmhttp.WithPromptCacheKey(llmCtx, l.PromptCacheKey)
			}
			llmCtx, requestTrace = llm.WithRequestTrace(llmCtx)
			const maxRetries = 2
			var resp *llm.Response
			var err error
			for attempt := 1; attempt <= maxRetries; attempt++ {
				resp, err = llmService.Do(llmCtx, req)
				if err == nil {
					return resp, nil
				}
				if !isRetryableError(err) || attempt == maxRetries {
					return nil, err
				}
				sleep := time.Second * time.Duration(attempt)
				l.Logger.Warn("LLM request failed with retryable error, retrying",
					"error", err,
					"attempt", attempt,
					"max_retries", maxRetries)
				select {
				case <-time.After(sleep):
				case <-llmCtx.Done():
					return nil, llmCtx.Err()
				}
			}
			return resp, err
		}

		resp, err := sendWithRetry(req)

		// Resolve server-side tool "pause_turn" responses before any further
		// handling. When Anthropic pauses mid-turn to run a server-side tool
		// (e.g. web_search), it returns stop_reason=pause_turn with a
		// server_tool_use block that has no result yet. The continuation arrives
		// in a *separate* response that begins with the matching
		// web_search_tool_result. Anthropic requires the server_tool_use and its
		// result to live in the SAME message, so we re-request and merge the
		// continuation into a single assistant message rather than letting the
		// loop interleave client tool execution (which permanently splits the
		// pair and wedges the conversation). See resolvePausedTurn.
		if err == nil && resp != nil && resp.StopReason == llm.StopReasonPause {
			resp, err = l.resolvePausedTurn(ctx, sendWithRetry, req, resp)
		}
		if err != nil {
			// User cancellation owns its own end-of-turn bookkeeping. Avoid a
			// duplicate LLM error row, and avoid trying to persist it on a dead
			// context.
			if errors.Is(ctx.Err(), context.Canceled) {
				l.Logger.Info("LLM request aborted by loop cancellation", "error", err)
				return fmt.Errorf("LLM request failed: %w", err)
			}

			// Persist genuine failures even if the run context expired. This
			// terminal row clears the durable agent-working state and provides the
			// user-visible error and Retry affordance.
			errorMessage := llm.Message{
				Role: llm.MessageRoleAssistant,
				Content: []llm.Content{
					{
						Type: llm.ContentTypeText,
						Text: userFacingLLMError(err, requestTrace),
					},
				},
				EndOfTurn:      true,
				ErrorType:      llm.ErrorTypeLLMRequest,
				ErrorRetryable: IsRetryableLLMError(err),
			}
			l.emitResponse(context.WithoutCancel(ctx), errorMessage, llm.Usage{}, "failed to record error message")
			return fmt.Errorf("LLM request failed: %w", err)
		}

		l.Logger.Debug("received LLM response", "content_count", len(resp.Content), "stop_reason", resp.StopReason.String(), "usage", resp.Usage.String())

		// Handle max tokens truncation BEFORE adding to history - truncated responses
		// should not be added to history normally (they get special handling)
		if resp.StopReason == llm.StopReasonMaxTokens {
			l.Logger.Warn("LLM response truncated due to max tokens")
			return l.handleMaxTokensTruncation(ctx, resp)
		}

		// Handle refusals BEFORE adding to history. On stop_reason=refusal the
		// model declines to continue and typically returns no visible content
		// (often just a thinking block). Recorded normally it becomes a silent
		// empty end-of-turn bubble, and — worse — re-queuing "continue" replays
		// the same context and refuses again, wedging the conversation in an
		// endless string of blank turns. Surface it as a visible error instead.
		if resp.StopReason == llm.StopReasonRefusal {
			l.Logger.Warn("LLM declined to continue (stop_reason=refusal)")
			return l.handleRefusal(ctx, resp)
		}

		// Retain the exact provider-visible prefix for an idle cache refresh.
		if l.Hooks.OnSuccessfulRequest != nil {
			l.Hooks.OnSuccessfulRequest(req)
		}

		// Convert response to a message, persist it through the response hook,
		// and retain it for the next model request.
		assistantMessage := resp.ToMessage()
		l.emitResponse(ctx, assistantMessage, resp.UsageWithMeta(), "failed to record assistant message")
		l.Messages = append(l.Messages, assistantMessage)

		if resp.StopReason != llm.StopReasonToolUse {
			return nil
		}

		l.Logger.Debug("handling tool calls", "content_count", len(resp.Content))
		if err := l.executeToolCalls(ctx, resp.Content); err != nil {
			if errors.Is(err, errToolEndedTurn) {
				return nil
			}
			return err
		}
	}
}

// maxPauseContinuations bounds how many times we will re-request to resolve a
// chain of server-side tool pauses, guarding against a pathological loop where
// the provider keeps returning pause_turn forever.

// resolvePausedTurn handles a stop_reason=pause_turn response by re-requesting
// the continuation(s) and merging all blocks into a single assistant message.
//
// Anthropic pauses a turn to run a server-side tool (e.g. web_search). The
// paused response ends with a server_tool_use block whose result is not yet
// available; the continuation arrives in a follow-up response that begins with
// the matching web_search_tool_result. Because Anthropic requires the
// server_tool_use and its web_search_tool_result to live in the SAME message,
// we accumulate every block across the pause chain and return a single response
// with the final (non-pause) stop reason. This keeps the stored history valid
// on reload and prevents the client tool loop from interleaving a tool_result
// message between the server_tool_use and its result.
//
// req is the request that produced the initial paused response; it is not
// mutated — each continuation request is a shallow copy with a fresh Messages
// slice that has the running assistant turn appended.
func (l *runner) resolvePausedTurn(
	ctx context.Context,
	send func(*llm.Request) (*llm.Response, error),
	req *llm.Request,
	resp *llm.Response,
) (*llm.Response, error) {
	// Copy the initial content so appends never alias the first response's
	// backing array.
	merged := append([]llm.Content(nil), resp.Content...)
	// Accumulate usage across the whole pause chain, starting with the initial
	// paused response's usage.
	totalUsage := resp.Usage
	// Preserve the start time of the first (paused) leg so the merged turn
	// reflects the full wall-clock duration, not just the last continuation.
	startTime := resp.StartTime
	for i := 0; resp.StopReason == llm.StopReasonPause; i++ {
		if i >= maxPauseContinuations {
			l.Logger.Warn("server-side tool pause did not resolve", "continuations", i)
			break
		}
		l.Logger.Debug("resolving paused turn (server-side tool)", "continuation", i+1)

		// Append the running assistant turn so the provider resumes from it.
		continueReq := *req
		continueReq.Messages = append(append([]llm.Message(nil), req.Messages...),
			llm.Message{Role: llm.MessageRoleAssistant, Content: merged})

		next, err := send(&continueReq)
		if err != nil {
			return nil, err
		}
		totalUsage.Add(next.Usage)
		merged = append(merged, next.Content...)
		resp = next
	}

	// Return a single response carrying every block from the pause chain with
	// the final (resolved) stop reason. Usage is the sum across the whole chain
	// (initial paused response + every continuation) so billing is not lost.
	resolved := *resp
	resolved.Content = merged
	resolved.Usage = totalUsage
	resolved.StartTime = startTime // EndTime stays at the final continuation
	return &resolved, nil
}

func (l *runner) emitResponse(ctx context.Context, message llm.Message, usage llm.Usage, failureMessage string) {
	if l.Hooks.OnResponse == nil {
		return
	}
	if err := l.Hooks.OnResponse(ctx, Response{Message: message, Usage: usage}); err != nil {
		l.Logger.Error(failureMessage, "error", err)
	}
}

func (l *runner) emitToolResponse(ctx context.Context, message llm.Message, otherUsage []llm.PurposedUsage) {
	if l.Hooks.OnToolResponse == nil {
		return
	}
	if err := l.Hooks.OnToolResponse(ctx, ToolResponse{Message: message, OtherUsage: otherUsage}); err != nil {
		l.Logger.Error("failed to record tool result message", "error", err)
	}
}

func (l *runner) retryWarningHook(ctx context.Context) func(llm.RetryEvent) {
	if l.Hooks.OnWarning == nil {
		return nil
	}
	return func(event llm.RetryEvent) {
		if err := l.Hooks.OnWarning(ctx, llm.FormatRetryEvent(event)); err != nil {
			l.Logger.Error("failed to record retry warning", "error", err)
		}
	}
}

// handleMaxTokensTruncation handles the case where the LLM response was truncated
// due to hitting the maximum output token limit. It records the truncated message
// for cost tracking (excluded from context) and an error message for the user.
func (l *runner) handleMaxTokensTruncation(ctx context.Context, resp *llm.Response) error {
	// Record the truncated message for cost tracking, but mark it as excluded from context.
	// This preserves billing information without confusing the LLM on future turns.
	truncatedMessage := resp.ToMessage()
	truncatedMessage.ExcludedFromContext = true

	l.emitResponse(ctx, truncatedMessage, resp.UsageWithMeta(), "failed to record truncated message")

	// Record a truncation error message with EndOfTurn=true to properly signal end of turn.
	errorMessage := llm.Message{
		Role: llm.MessageRoleAssistant,
		Content: []llm.Content{
			{
				Type: llm.ContentTypeText,
				Text: "[SYSTEM ERROR: Your previous response was truncated because it exceeded the maximum output token limit. " +
					"Any tool calls in that response were lost. Please retry with smaller, incremental changes. " +
					"For file operations, break large changes into multiple smaller patches. " +
					"The user can ask you to continue if needed.]",
			},
		},
		EndOfTurn: true,
		ErrorType: llm.ErrorTypeTruncation,
	}
	l.emitResponse(ctx, errorMessage, llm.Usage{}, "failed to record truncation error message")
	return nil
}

// handleRefusal handles a stop_reason=refusal response: the model declined to
// continue. Such responses usually carry no visible content (just a thinking
// block, or nothing), so recording them normally leaves a blank agent bubble
// and, because the empty response ends up in history, every follow-up
// "continue" replays the same context and refuses again. We instead record the
// raw response excluded from context (for cost tracking) and record a visible,
// non-retryable error message that ends the turn. Neither is added to the live
// context history, matching the cold-start rehydration path.
func (l *runner) handleRefusal(ctx context.Context, resp *llm.Response) error {
	// Record the raw refusal for cost tracking, but keep it out of context so it
	// doesn't poison future turns (an empty/near-empty assistant turn biases the
	// model toward refusing again, and empty content blocks can wedge replay).
	rawMessage := resp.ToMessage()
	rawMessage.ExcludedFromContext = true

	l.emitResponse(ctx, rawMessage, resp.UsageWithMeta(), "failed to record refusal message")

	// Build the user-visible notice. Start with the standard guidance, then
	// append every field the provider gave us in the refusal reason (category
	// and full explanation), so nothing is hidden from the user.
	noticeText := "[The model declined to continue this request. Retrying the same " +
		"request will likely be declined again. Switch to Opus to continue, " +
		"or use /model to switch models. You can also try rephrasing or " +
		"clarifying the intent instead.]"
	var refusalCategory, refusalExplanation string
	if resp.RefusalDetails != nil {
		refusalCategory = strings.TrimSpace(resp.RefusalDetails.Category)
		refusalExplanation = strings.TrimSpace(resp.RefusalDetails.Explanation)
		if refusalCategory != "" {
			noticeText += "\n\nCategory: " + refusalCategory
		}
		if refusalExplanation != "" {
			noticeText += "\n\nReason: " + refusalExplanation
		}
	}

	// Record a visible refusal notice with EndOfTurn=true. Marked non-retryable:
	// re-running the identical request just refuses again, so the UI should not
	// offer a Retry button. Rephrasing the request is what actually helps.
	//
	// Deliberately not appended to model-visible messages: like other error
	// system-generated, user-visible artifact that must not be sent back to the
	// model. The cold-start path already excludes it from context
	// (partitionMessages skips MessageTypeError, ListMessagesForContext skips
	// excluded rows), so keeping it out of the live in-memory history too makes
	// active-session and rehydrated behavior identical. Otherwise a rephrase in
	// the same session would show the model an assistant turn narrating its own
	// refusal, biasing it toward refusing again. (Mirrors the llm_request error
	// path above, which also records without appending.)
	errorMessage := llm.Message{
		Role: llm.MessageRoleAssistant,
		Content: []llm.Content{
			{
				Type: llm.ContentTypeText,
				Text: noticeText,
			},
		},
		EndOfTurn:          true,
		ErrorType:          llm.ErrorTypeRefusal,
		ErrorRetryable:     false,
		RefusalCategory:    refusalCategory,
		RefusalExplanation: refusalExplanation,
	}

	l.emitResponse(ctx, errorMessage, llm.Usage{}, "failed to record refusal error message")
	return nil
}

func (l *runner) findTool(name string) *llm.Tool {
	for _, tool := range l.Tools {
		if tool.Name == name {
			return tool
		}
	}
	return nil
}

var errToolEndedTurn = errors.New("tool ended turn")

type toolCallExecution struct {
	content         llm.Content
	responseHandled bool
	endsTurn        bool
}

// executeToolCalls runs all tools from an LLM response as a deterministic
// sibling cohort and appends their results in original tool-call order.
func (l *runner) executeToolCalls(ctx context.Context, content []llm.Content) error {
	var calls []llm.Content
	for _, c := range content {
		if c.Type == llm.ContentTypeToolUse {
			calls = append(calls, c)
		}
	}
	if len(calls) == 0 {
		return nil
	}

	// Collect the usage of indirect LLM calls made by tools (keyword_search,
	// tool install validation, subagent progress summaries, ...)
	// so it can be attached to the tool-result message below. Tool calls run
	// concurrently, so the accumulator is mutex-guarded.
	var otherUsage llm.UsageAccumulator
	ctx = llm.WithUsageCollector(ctx, otherUsage.Collect)

	toolResults := make([]toolCallExecution, len(calls))
	serialTails := make(map[*llm.Tool]<-chan struct{})
	type concurrencyGroupState struct {
		exclusive <-chan struct{}
		shared    []<-chan struct{}
	}
	groupStates := make(map[string]*concurrencyGroupState)

	// Every worker reaches start before any waits on scheduling dependencies.
	// Cancellation before this cohort is released produces a never-started
	// result for every call. Once released, workers still honor serial and
	// concurrency-group dependencies before invoking their tools.
	var ready, finishedWorkers sync.WaitGroup
	ready.Add(len(calls))
	start := make(chan struct{})
	run := false
	for i, call := range calls {
		tool := l.findTool(call.ToolName)

		var dependencies []<-chan struct{}
		var finished chan struct{}
		if tool != nil && (tool.Serial || tool.ConcurrencyGroup != "") {
			finished = make(chan struct{})
		}
		if tool != nil && tool.Serial {
			if previous := serialTails[tool]; previous != nil {
				dependencies = append(dependencies, previous)
			}
			serialTails[tool] = finished
		}
		if tool != nil && tool.ConcurrencyGroup != "" {
			state := groupStates[tool.ConcurrencyGroup]
			if state == nil {
				state = &concurrencyGroupState{}
				groupStates[tool.ConcurrencyGroup] = state
			}
			if state.exclusive != nil {
				dependencies = append(dependencies, state.exclusive)
			}
			if tool.ConcurrencyExclusive {
				dependencies = append(dependencies, state.shared...)
				state.exclusive = finished
				state.shared = nil
			} else {
				state.shared = append(state.shared, finished)
			}
		}

		finishedWorkers.Go(func() {
			if finished != nil {
				defer close(finished)
			}
			ready.Done()
			<-start
			if !run {
				toolResults[i].content = llm.Content{
					Type:       llm.ContentTypeToolResult,
					ToolUseID:  call.ID,
					ToolError:  true,
					ToolResult: llm.TextContent(notExecutedToolResultText),
				}
				return
			}
			for _, dependency := range dependencies {
				<-dependency
			}
			if tool != nil && tool.EndsTurn && len(calls) != 1 {
				toolResults[i].content = llm.Content{
					Type:       llm.ContentTypeToolResult,
					ToolUseID:  call.ID,
					ToolError:  true,
					ToolResult: llm.TextContent("turn-ending tools must be called alone"),
				}
				return
			}
			toolResults[i] = l.executeToolCall(ctx, call, tool)
		})
	}
	ready.Wait()
	run = ctx.Err() == nil
	close(start)
	finishedWorkers.Wait()

	contents := make([]llm.Content, len(toolResults))
	persisted := make([]llm.Content, 0, len(toolResults))
	endsTurn := false
	for i, result := range toolResults {
		contents[i] = result.content
		if !result.responseHandled {
			persisted = append(persisted, result.content)
		}
		endsTurn = endsTurn || result.endsTurn
	}

	// Add every result to in-memory history. Persist only results not already
	// committed by their tool, using a cancellation-free context so completed,
	// interrupted, and never-started results remain durable.
	toolMessage := llm.Message{Role: llm.MessageRoleUser, Content: contents}
	l.Messages = append(l.Messages, toolMessage)
	if len(persisted) > 0 {
		l.emitToolResponse(context.WithoutCancel(ctx), llm.Message{
			Role: llm.MessageRoleUser, Content: persisted,
		}, otherUsage.Take())
	} else {
		otherUsage.Take()
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if endsTurn {
		return errToolEndedTurn
	}
	return nil
}

// executeToolCall runs one client-side tool call after its sibling cohort has
// crossed the start barrier. Do not pre-check ctx: a released sibling is
// logically started even when cancellation reaches it before the scheduler.
func (l *runner) executeToolCall(ctx context.Context, call llm.Content, tool *llm.Tool) toolCallExecution {
	l.Logger.Debug("executing tool", "name", call.ToolName, "id", call.ID)

	if tool == nil {
		l.Logger.Error("tool not found", "name", call.ToolName)
		return toolCallExecution{content: llm.Content{
			Type:      llm.ContentTypeToolResult,
			ToolUseID: call.ID,
			ToolError: true,
			ToolResult: []llm.Content{
				{Type: llm.ContentTypeText, Text: fmt.Sprintf("Tool '%s' not found", call.ToolName)},
			},
		}}
	}

	toolCtx := ctx
	if l.Hooks.OnToolProgress != nil {
		toolCtx = llm.WithToolProgress(toolCtx, func(progress llm.ToolProgress) {
			l.Hooks.OnToolProgress(ctx, progress)
		})
	}
	toolCtx = llm.WithToolUseID(toolCtx, call.ID)
	toolCtx = llm.WithLLMService(toolCtx, l.LLM)

	startTime := time.Now()
	resultCh := make(chan llm.ToolOut, 1)
	go func() { resultCh <- tool.Run(toolCtx, call.ToolInput) }()

	var result llm.ToolOut
	abandoned := false
	// Prefer an already-completed result before looking at cancellation. Once
	// selected, that result remains authoritative even if ctx is cancelled.
	select {
	case result = <-resultCh:
	default:
		select {
		case result = <-resultCh:
		case <-ctx.Done():
			grace := time.NewTimer(toolCancelGrace)
			select {
			case result = <-resultCh:
				if !grace.Stop() {
					<-grace.C
				}
			case <-grace.C:
				abandoned = true
			}
		}
	}
	endTime := time.Now()

	if abandoned {
		// A context-ignoring goroutine cannot be forcefully stopped. Its buffered
		// result has no consumer after this point, so it cannot publish late.
		l.Logger.Warn("tool ignored cancellation; abandoning", "name", call.ToolName, "id", call.ID)
		return toolCallExecution{content: llm.Content{
			Type:             llm.ContentTypeToolResult,
			ToolUseID:        call.ID,
			ToolError:        true,
			ToolResult:       llm.TextContent(abandonedToolResultText),
			ToolUseStartTime: &startTime,
			ToolUseEndTime:   &endTime,
		}}
	}

	toolResultContent := result.LLMContent
	if result.Error != nil {
		text := result.Error.Error()
		// Cancellation is a property of this tool's own result, not the shared
		// context: a sibling may cancel after this tool has already failed.
		if errors.Is(result.Error, context.Canceled) {
			l.Logger.Info("tool cancelled by user", "name", call.ToolName)
			text = strings.TrimRight(text, "\r\n") + "\n\n" + cancelledToolResultText
		} else {
			l.Logger.Error("tool execution failed", "name", call.ToolName, "error", result.Error)
		}
		toolResultContent = llm.TextContent(text)
	} else {
		l.Logger.Debug("tool executed successfully", "name", call.ToolName, "duration", endTime.Sub(startTime))
	}

	return toolCallExecution{
		content: llm.Content{
			Type:             llm.ContentTypeToolResult,
			ToolUseID:        call.ID,
			ToolError:        result.Error != nil,
			ToolResult:       toolResultContent,
			ToolUseStartTime: &startTime,
			ToolUseEndTime:   &endTime,
			Display:          result.Display,
		},
		responseHandled: result.ResponseHandled,
		endsTurn:        (tool.EndsTurn || result.EndsTurn) && result.Error == nil,
	}
}

// insertMissingToolResults fixes tool_result issues in the conversation history:
//  1. Adds error results for tool_uses that were requested but not included in the next message.
//     This can happen when a request is cancelled or fails after the LLM responds with tool_use
//     blocks but before the tools execute.
//  2. Removes orphan tool_results that reference tool_use IDs not present in the immediately
//     preceding assistant message. This can happen when a tool execution completes after
//     CancelConversation has already written cancellation messages.
//
// This prevents API errors like:
//   - "tool_use ids were found without tool_result blocks"
//   - "unexpected tool_use_id found in tool_result blocks ... Each tool_result block must have
//     a corresponding tool_use block in the previous message"
//
// Mutates the request's Messages slice.
func (l *runner) insertMissingToolResults(req *llm.Request) {
	if len(req.Messages) < 1 {
		return
	}

	// Scan through all messages looking for assistant messages with tool_use
	// that are not immediately followed by a user message with corresponding tool_results.
	// We may need to insert synthetic user messages with tool_results or filter orphans.
	var newMessages []llm.Message
	totalInserted := 0
	totalRemoved := 0

	// Track the tool_use IDs from the most recent assistant message
	var prevAssistantToolUseIDs map[string]bool

	for i := 0; i < len(req.Messages); i++ {
		msg := req.Messages[i]

		if msg.Role == llm.MessageRoleAssistant {
			// Handle empty assistant messages - add placeholder content if not the last message
			// The API requires all messages to have non-empty content except for the optional
			// final assistant message. Empty content can happen when the model ends its turn
			// without producing any output.
			if len(msg.Content) == 0 && i < len(req.Messages)-1 {
				req.Messages[i].Content = []llm.Content{{Type: llm.ContentTypeText, Text: "(no response)"}}
				msg = req.Messages[i] // update local copy for subsequent processing
				l.Logger.Debug("added placeholder content to empty assistant message", "index", i)
			}

			// Track all tool_use IDs in this assistant message
			prevAssistantToolUseIDs = make(map[string]bool)
			for _, c := range msg.Content {
				if c.Type == llm.ContentTypeToolUse {
					prevAssistantToolUseIDs[c.ID] = true
				}
			}
			newMessages = append(newMessages, msg)

			// Check if next message needs synthetic tool_results
			var toolUseContents []llm.Content
			for _, c := range msg.Content {
				if c.Type == llm.ContentTypeToolUse {
					toolUseContents = append(toolUseContents, c)
				}
			}

			if len(toolUseContents) == 0 {
				continue
			}

			// Check if next message is a user message with corresponding tool_results
			var nextMsg *llm.Message
			if i+1 < len(req.Messages) {
				nextMsg = &req.Messages[i+1]
			}

			if nextMsg == nil || nextMsg.Role != llm.MessageRoleUser {
				// Next message is not a user message (or there is no next message).
				// Insert a synthetic user message with tool_results for all tool_uses.
				var toolResultContent []llm.Content
				for _, tu := range toolUseContents {
					toolResultContent = append(toolResultContent, llm.Content{
						Type:      llm.ContentTypeToolResult,
						ToolUseID: tu.ID,
						ToolError: true,
						ToolResult: []llm.Content{{
							Type: llm.ContentTypeText,
							Text: "not executed; retry possible",
						}},
					})
				}
				syntheticMsg := llm.Message{
					Role:    llm.MessageRoleUser,
					Content: toolResultContent,
				}
				newMessages = append(newMessages, syntheticMsg)
				totalInserted += len(toolResultContent)
			}
		} else if msg.Role == llm.MessageRoleUser {
			// Filter out orphan tool_results and add missing ones
			var filteredContent []llm.Content
			existingResultIDs := make(map[string]bool)

			for _, c := range msg.Content {
				if c.Type == llm.ContentTypeToolResult {
					// Only keep tool_results that match a tool_use in the previous assistant message
					if prevAssistantToolUseIDs != nil && prevAssistantToolUseIDs[c.ToolUseID] {
						filteredContent = append(filteredContent, c)
						existingResultIDs[c.ToolUseID] = true
					} else {
						// Orphan tool_result - skip it
						totalRemoved++
						l.Logger.Debug("removing orphan tool_result", "tool_use_id", c.ToolUseID)
					}
				} else {
					// Keep non-tool_result content
					filteredContent = append(filteredContent, c)
				}
			}

			// Check if we need to add missing tool_results for this user message
			if prevAssistantToolUseIDs != nil {
				var prefix []llm.Content
				for toolUseID := range prevAssistantToolUseIDs {
					if !existingResultIDs[toolUseID] {
						prefix = append(prefix, llm.Content{
							Type:      llm.ContentTypeToolResult,
							ToolUseID: toolUseID,
							ToolError: true,
							ToolResult: []llm.Content{{
								Type: llm.ContentTypeText,
								Text: "not executed; retry possible",
							}},
						})
						totalInserted++
					}
				}
				if len(prefix) > 0 {
					filteredContent = append(prefix, filteredContent...)
				}
			}

			// Only add the message if it has content
			if len(filteredContent) > 0 {
				msg.Content = filteredContent
				newMessages = append(newMessages, msg)
			} else {
				// Message is now empty after filtering - skip it entirely
				l.Logger.Debug("removing empty user message after filtering orphan tool_results")
			}

			// Reset for next iteration - user message "consumes" the previous tool_uses
			prevAssistantToolUseIDs = nil
		} else {
			newMessages = append(newMessages, msg)
		}
	}

	if totalInserted > 0 || totalRemoved > 0 {
		req.Messages = newMessages
		if totalInserted > 0 {
			l.Logger.Debug("inserted missing tool results", "count", totalInserted)
		}
		if totalRemoved > 0 {
			l.Logger.Debug("removed orphan tool results", "count", totalRemoved)
		}
	}
}
