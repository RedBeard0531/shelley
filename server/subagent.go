package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"shelley.exe.dev/claudetool"
	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/llm"
)

// SubagentRunner implements claudetool.SubagentRunner.
type SubagentRunner struct {
	server *Server
}

// NewSubagentRunner creates a new SubagentRunner.
func NewSubagentRunner(s *Server) *SubagentRunner {
	return &SubagentRunner{server: s}
}

// RunSubagent implements claudetool.SubagentRunner.
func (r *SubagentRunner) RunSubagent(ctx context.Context, conversationID, prompt, modelID, reasoning string) (string, error) {
	s := r.server
	conv, convErr := s.db.GetConversationByID(ctx, conversationID)
	if convErr == nil {
		if _, ok := db.ManagedBtwReaderIdentity(*conv); ok {
			return "", fmt.Errorf("BTW reader %s cannot be used as delegated subagent work", conversationID)
		}
	}

	// Notify the UI about the subagent conversation.
	// This ensures the sidebar shows the subagent even if it's a newly created conversation.
	go r.notifySubagentConversation(ctx, conversationID)

	// Run new-conversation hook for newly created subagent conversations.
	// We detect "new" by checking if the manager already exists.
	s.mu.Lock()
	_, alreadyActive := s.activeConversations[conversationID]
	s.mu.Unlock()
	if !alreadyActive {
		if convErr != nil {
			s.logger.Error("Failed to get conversation for new-conversation hook", "error", convErr, "conversationID", conversationID)
		} else if isManagedChild(*conv) {
			hookResult, hookErr := RunNewConversationHookIn(s.hooksDir, NewConversationHookInput{
				Prompt: prompt,
				Model:  modelID,
				Cwd:    derefString(conv.Cwd),
				Readonly: NewConversationReadonly{
					ConversationID: conversationID,
					IsSubagent:     true,
					ParentID:       *conv.ParentConversationID,
				},
			})
			if hookErr != nil {
				return "", fmt.Errorf("new-conversation hook: %w", hookErr)
			}
			if hookResult.Cwd != derefString(conv.Cwd) {
				if err := s.db.UpdateConversationCwd(ctx, conversationID, hookResult.Cwd); err != nil {
					s.logger.Error("Failed to update subagent cwd from hook", "error", err)
				}
			}
			if hookResult.Prompt != prompt {
				prompt = hookResult.Prompt
			}
			if hookResult.Model != modelID {
				if _, svcErr := s.llmManager.GetService(hookResult.Model); svcErr != nil {
					s.logger.Error("Hook returned unsupported model, keeping original", "hookModel", hookResult.Model, "error", svcErr)
				} else {
					modelID = hookResult.Model
				}
			}
		}
	}

	// Get or create conversation manager for the subagent, with incremented depth
	manager, err := s.getOrCreateSubagentConversationManager(ctx, conversationID)
	if err != nil {
		return "", fmt.Errorf("failed to get conversation manager: %w", err)
	}

	// Apply the requested reasoning level (inherited from the parent when the
	// caller didn't specify one). Must happen before AcceptUserMessage, which
	// builds the loop from the conversation's stored options. Empty reasoning
	// is a no-op, leaving the subagent's existing/default level intact.
	//
	// Fail loudly rather than run the subagent at the wrong reasoning level
	// while reporting success: the level is part of what the caller asked for.
	if err := manager.SetThinkingLevel(ctx, reasoning); err != nil {
		return "", fmt.Errorf("failed to set subagent reasoning level %q: %w", reasoning, err)
	}

	// Use the parent's model if provided, otherwise fall back to server
	// default (preferring a ready model from the catalog; see
	// effectiveDefaultModel).
	if modelID == "" {
		modelID = s.effectiveDefaultModel(s.getModelList())
	}

	// Persist model on the subagent conversation record
	// UpdateConversationModel only sets the model if it's NULL, so this is safe for re-sends
	if modelID != "" {
		if err := s.db.UpdateConversationModel(ctx, conversationID, modelID); err != nil {
			s.logger.Warn("Failed to persist model on subagent conversation", "error", err, "conversationID", conversationID)
		}
	}

	// Get LLM service
	llmService, err := s.llmManager.GetService(modelID)
	if err != nil {
		return "", fmt.Errorf("failed to get LLM service: %w", err)
	}

	// Create user message
	userMessage := llm.Message{
		Role:    llm.MessageRoleUser,
		Content: []llm.Content{{Type: llm.ContentTypeText, Text: prompt}},
	}

	// A busy subagent receives the message at its current turn's next LLM
	// round, without interrupting the turn (see InjectMessage).
	//
	// New work supersedes any completion notification still queued on the
	// parent from an earlier turn of this subagent: the parent is asking for
	// the NEW turn's outcome, whose completion enqueues a fresh notification.
	// Two-step supersession, in this order:
	//  1. watermark: mark every response that ALREADY exists as handled, so a
	//     straggling notifier goroutine that misses the scrub below skips at
	//     enqueue time (see handledResponseSeq). Captured before the send so
	//     the new turn's own response can't be caught.
	//  2. scrub: drop batches already queued. Done BEFORE sending, so it
	//     cannot catch a fast new turn's fresh notification.
	// Accepted loss: if the send below fails, the earlier result's
	// notification has already been suppressed — but the tool returns an
	// explicit error, so the parent knows to re-poll the subagent.
	if seq := r.lastAgentSeq(ctx, conversationID); seq > 0 {
		manager.markResponseHandled(seq)
	}
	r.dropStaleParentNotification(ctx, conversationID)
	if manager.IsAgentWorking() {
		if err := manager.InjectMessage(ctx, s, modelID, userMessage); err != nil {
			return "", fmt.Errorf("failed to send message to busy subagent: %w", err)
		}
		return "message sent into the subagent's current turn; its response will be delivered asynchronously when its turn finishes.", nil
	}
	if _, err := manager.AcceptUserMessage(ctx, llmService, modelID, userMessage); err != nil {
		return "", fmt.Errorf("failed to accept user message: %w", err)
	}
	return "message sent; the subagent works in the background and its response will be delivered asynchronously when its turn finishes.", nil
}

// ListSubagents implements claudetool.SubagentRunner. It lists delegated
// subagents only: BTW readers and internal workers are not addressable with
// the subagent tool.
func (r *SubagentRunner) ListSubagents(ctx context.Context, parentConversationID string) ([]claudetool.SubagentSummary, error) {
	s := r.server
	convs, err := s.db.GetSubagents(ctx, parentConversationID)
	if err != nil {
		return nil, err
	}
	var out []claudetool.SubagentSummary
	for _, conv := range convs {
		kind := db.ParseConversationOptions(conv.ConversationOptions).Kind
		if !isManagedChild(conv) || isBtwReader(conv) || kind == transcriptionKind || kind == commitTourKind || conv.Slug == nil {
			continue
		}
		text, _, err := s.lastAgentText(ctx, conv.ConversationID)
		if err != nil {
			return nil, fmt.Errorf("read subagent %s: %w", *conv.Slug, err)
		}
		out = append(out, claudetool.SubagentSummary{
			Slug:         *conv.Slug,
			Working:      s.IsAgentWorking(conv.ConversationID),
			LastResponse: text,
		})
	}
	return out, nil
}

// MessageParent implements claudetool.ParentMessenger. The message is stored
// in the parent as a user row whose user_data names the sending subagent, so
// the model sees it wrapped in <subagent_message> and the UI attributes it.
func (r *SubagentRunner) MessageParent(ctx context.Context, conversationID, text string) error {
	s := r.server
	conv, err := s.db.GetConversationByID(ctx, conversationID)
	if err != nil {
		return fmt.Errorf("load conversation: %w", err)
	}
	if !isManagedChild(*conv) || isBtwReader(*conv) || db.ParseConversationOptions(conv.ConversationOptions).Kind != "" {
		return fmt.Errorf("conversation %s is not a subagent", conversationID)
	}
	parent, err := s.getOrCreateConversationManager(ctx, *conv.ParentConversationID, "")
	if err != nil {
		return fmt.Errorf("load parent conversation: %w", err)
	}
	parent.mu.Lock()
	modelID := parent.modelID
	parent.mu.Unlock()
	ctx = contextWithTurnUserData(ctx, senderMessageUserData{
		SenderConversationID: conversationID,
		SenderSlug:           derefString(conv.Slug),
		SenderRelationship:   senderRelationshipSubagent,
		Text:                 text,
	})
	return parent.InjectMessage(ctx, s, modelID, llm.UserStringMessage(text))
}

// dropStaleParentNotification removes any queued subagent-done notification
// for the given subagent from its parent's pending-batch queue. RunSubagent
// calls it before sending new work: the new prompt supersedes the earlier
// turn's queued notification, since the parent asked for the new turn's
// outcome.
//
// Without the drop, the stale notification would be injected at the parent's
// next LLM round (or drained at turn end) as a confusing echo of a result
// the parent already has or has moved past.
//
// Only an already-active parent manager is consulted: if the parent has no
// active manager, it has no in-memory pending queue to scrub (queued
// subagent-done batches live only in memory, and a manager holding pending
// batches is never evicted).
func (r *SubagentRunner) dropStaleParentNotification(ctx context.Context, subagentConversationID string) {
	s := r.server

	conv, err := s.db.GetConversationByID(ctx, subagentConversationID)
	if err != nil {
		s.logger.Warn("Failed to look up subagent conversation for stale-notification drop",
			"subagent", subagentConversationID, "error", err)
		return
	}
	if !isManagedChild(*conv) {
		return
	}
	s.mu.Lock()
	parentMgr, ok := s.activeConversations[*conv.ParentConversationID]
	s.mu.Unlock()
	if !ok {
		return
	}
	if dropped := parentMgr.DropPendingSubagentDone(subagentConversationID); dropped > 0 {
		s.logger.Info("Dropped stale queued subagent-done notification",
			"subagent", subagentConversationID, "parent", *conv.ParentConversationID, "dropped", dropped)
	}
}

// lastAgentSeq returns the sequence id of the subagent's most recent agent
// message (0 when none). Used to capture a supersession watermark BEFORE
// sending new work — reading it after the send could catch the new turn's
// own response and wrongly mark it handled.
func (r *SubagentRunner) lastAgentSeq(ctx context.Context, conversationID string) int64 {
	_, seq, err := r.server.lastAgentText(ctx, conversationID)
	if err != nil {
		r.server.logger.Warn("Failed to read last agent message for supersession watermark",
			"subagent", conversationID, "error", err)
		return 0
	}
	return seq
}

// notifySubagentConversation fetches the subagent conversation and publishes it
// to all SSE streams so the UI can update the sidebar.
func (r *SubagentRunner) notifySubagentConversation(ctx context.Context, conversationID string) {
	s := r.server

	// Fetch the conversation from the database
	var conv generated.Conversation
	err := s.db.Queries(ctx, func(q *generated.Queries) error {
		var err error
		conv, err = q.GetConversation(ctx, conversationID)
		return err
	})
	if err != nil {
		s.logger.Error("Failed to get subagent conversation for notification", "error", err, "conversationID", conversationID)
		return
	}

	// Only notify if this is actually a managed child.
	if !isManagedChild(conv) {
		return
	}

	// Internal transcription workers are implementation details, not navigable
	// conversations. Publishing them can steal attention from the composer that
	// is waiting for their synchronous result.
	if db.ParseConversationOptions(conv.ConversationOptions).Kind == transcriptionKind {
		return
	}

	// Publish the subagent conversation to all active streams
	s.publishConversationListUpdate(ConversationListUpdate{
		Type:         "update",
		Conversation: &conv,
	})

	s.logger.Debug("Notified UI about subagent conversation",
		"conversationID", conversationID,
		"parentID", *conv.ParentConversationID,
		"slug", conv.Slug)
}

// dispatchSubagentDone is the entry point for subagent completion
// notifications, called SYNCHRONOUSLY from the completion sites (the onDone
// hook and SubagentRunner.endWait's timeout recovery). It captures the
// completion's identity — the finished turn's response text and sequence id
// — before spawning the (potentially slow: parent hydration, lock waits)
// notification goroutine. Capturing at dispatch rather than inside the
// goroutine fixes WHAT is being announced at the moment of completion: a
// delayed goroutine that read "the subagent's latest agent row" at run time
// could observe a NEWER turn's mid-turn row (e.g. a bare tool_use) and
// announce an in-progress turn as finished with "(no textual response)".
func (s *Server) dispatchSubagentDone(subagentConversationID string) {
	response, responseSeq, ok := s.captureSubagentDone(subagentConversationID)
	if !ok {
		return
	}
	go s.notifyParentSubagentDone(subagentConversationID, response, responseSeq)
}

// captureSubagentDone reads the just-finished turn's response text and
// sequence id. It reports ok=false when the subagent is already working on a
// NEWER turn: this completion has been superseded — announcing it would
// splice stale (or mid-turn) content into the parent — and the newer turn's
// own completion will notify with the real result.
func (s *Server) captureSubagentDone(subagentConversationID string) (response string, responseSeq int64, ok bool) {
	ctx := context.Background()

	s.mu.Lock()
	subMgr, active := s.activeConversations[subagentConversationID]
	s.mu.Unlock()
	if active && subMgr.IsAgentWorking() {
		s.logger.Info("Skipping subagent-done notification: subagent is working on a newer turn",
			"subagent", subagentConversationID)
		return "", 0, false
	}

	response, responseSeq, err := s.lastAgentText(ctx, subagentConversationID)
	if err != nil || response == "" {
		response = "(no textual response)"
	}
	return response, responseSeq, true
}

// notifyParentSubagentDone enqueues a synthetic tool_use/tool_result pair
// onto the parent conversation's pending-batch queue when a subagent
// finishes, so the parent agent knows to check the results. Inspired by
// boldsoftware/shelley#200. response/responseSeq identify the completed
// turn's final answer, captured at dispatch time (see dispatchSubagentDone).
//
// All scheduling — mid-turn injection at the parent's next LLM round,
// waiting out distillation, cooperating with user-typed messages — is
// handled by the pending-batch queue (takeInjectable for a
// running turn, drainPendingMessages otherwise). We just drop a batch onto
// the queue and trust that machinery.
//
// It is invoked from onDone, which SetAgentWorking suppresses for
// cancellations.
func (s *Server) notifyParentSubagentDone(subagentConversationID, response string, responseSeq int64) {
	ctx := context.Background()

	var conv generated.Conversation
	err := s.db.Queries(ctx, func(q *generated.Queries) error {
		var err error
		conv, err = q.GetConversation(ctx, subagentConversationID)
		return err
	})
	if err != nil || !isManagedChild(conv) {
		return
	}
	if isBtwReader(conv) {
		return
	}
	kind := db.ParseConversationOptions(conv.ConversationOptions).Kind
	if kind == transcriptionKind || kind == commitTourKind {
		// Detached and internal workers never inject completion into parent history.
		return
	}

	parentID := *conv.ParentConversationID
	slug := "unknown"
	if conv.Slug != nil {
		slug = *conv.Slug
	}

	// Get or (re)create the parent's manager. The parent may have been
	// evicted from activeConversations by the periodic Cleanup while it sat
	// idle (or blocked — pre-fix — inside this very subagent's tool call)
	// waiting for the subagent to finish. Bailing out here silently dropped
	// the completion and the parent hung until the user typed something.
	// getOrCreateConversationManager hydrates from the DB, and the
	// pending-batch drain below re-establishes the loop, mirroring how a
	// user message wakes a parked conversation.
	parentManager, err := s.getOrCreateConversationManager(ctx, parentID, "")
	if err != nil {
		s.logger.Error("Failed to get parent manager for subagent-done notification",
			"parent", parentID, "subagent", subagentConversationID, "error", err)
		return
	}

	parentManager.mu.Lock()
	parentModelID := parentManager.modelID
	parentManager.mu.Unlock()

	s.mu.Lock()
	subMgr, subMgrActive := s.activeConversations[subagentConversationID]
	s.mu.Unlock()

	// Producer-side invalidation, evaluated at enqueue time (see
	// pendingBatch.isStale). Two conditions, both closing races a delayed
	// notification can hit between dispatch and enqueue:
	//  1. handledSeq: the parent deliberately superseded this response (sent
	//     new work) — the notification is moot.
	//  2. claimNotified: another notification already announced this response
	//     (or a newer one); only the first claim wins. Queue coalescing
	//     cannot catch this case when the earlier batch has already left the
	//     queue (mid-turn injection or drain).
	// The closure runs UNDER THE PARENT MANAGER'S MUTEX — the same mutex
	// dropStaleParentNotification takes to scrub the queue — so enqueue-vs-
	// scrub ordering is irrelevant: either the scrub removes the enqueued
	// batch, or the late enqueue sees the watermark (always published before
	// the scrub) and skips. Claim-and-append are atomic under that mutex.
	var stale func() bool
	if responseSeq > 0 && subMgrActive {
		stale = func() bool {
			return subMgr.handledSeq() >= responseSeq || !subMgr.claimNotified(responseSeq)
		}
	}

	// Cap the subagent text we splice into the parent's history. A runaway
	// subagent reply shouldn't dominate the parent's context window; the
	// parent can always read the full subagent conversation via the
	// dedicated subagent view.
	if len(response) > 500 {
		response = response[:500] + "..."
	}

	// Splice in a synthetic tool_use/tool_result pair as if the parent had
	// just called the subagent tool and received its response. This gives the LLM the
	// information it needs in the tool_result channel (weaker prompt
	// authority than user-voice), avoids the extra round trip that a
	// "please call the subagent tool" nudge would require, and the result
	// is clearly attributed to the subagent.
	toolUseID := fmt.Sprintf("sa_done_%s", uuid.New().String())
	toolInput, _ := json.Marshal(map[string]any{
		"slug":   slug,
		"prompt": "(asynchronous completion notification)",
	})
	assistantMsg := llm.Message{
		Role: llm.MessageRoleAssistant,
		Content: []llm.Content{{
			Type:      llm.ContentTypeToolUse,
			ID:        toolUseID,
			ToolName:  "subagent",
			ToolInput: toolInput,
			Display: claudetool.SubagentDisplayData{
				Slug:           slug,
				ConversationID: subagentConversationID,
			},
		}},
	}
	toolResultMsg := llm.Message{
		Role: llm.MessageRoleUser,
		Content: []llm.Content{{
			Type:      llm.ContentTypeToolResult,
			ToolUseID: toolUseID,
			ToolResult: []llm.Content{{
				Type: llm.ContentTypeText,
				Text: fmt.Sprintf(
					"[Subagent %q has finished asynchronously. "+
						"This tool call was synthesized by the system to surface the "+
						"result; you did not invoke it yourself. Please briefly acknowledge "+
						"the subagent's outcome to the user and decide whether any follow-up "+
						"work is needed.]\n\nSubagent response:\n%s",
					slug, response,
				),
			}},
			Display: claudetool.SubagentDisplayData{
				Slug:           slug,
				ConversationID: subagentConversationID,
			},
		}},
	}

	modelID := parentModelID
	if modelID == "" {
		modelID = s.effectiveDefaultModel(s.getModelList())
	}

	// Enqueue onto the parent's pending-batch queue. Mid-turn injection /
	// drainPendingMessages handle persistence, loop start/wake, and
	// serialization with both distillation and other queued work. We don't
	// need to read or touch the parent's agentWorking/distilling/loop state
	// ourselves — the queue is the single point of coordination.
	parentManager.EnqueueSubagentDone(s, modelID, subagentConversationID, assistantMsg, toolResultMsg, stale)
	s.logger.Info("Queued subagent-done notification for parent", "subagent", slug, "parent", parentID)
}

// lastAgentText returns the concatenated text content of the most recent
// type=agent message in a conversation — specifically the latest such
// message, skipping non-agent rows (gitinfo, user, tool, system, error)
// that may have been appended after it — along with that message's
// sequence id (0 when no agent message exists).
//
// In particular gitinfo messages carry assistant-role llm_data and would
// otherwise be returned as "the subagent's response" when they're really
// user-visible git state notes Shelley itself injected.
//
// If the latest agent message has no text content (e.g. it's a pure
// tool_use), returns "" — we don't walk further back, because earlier
// agent turns are stale: their text was already conveyed via prior
// notifications or tool returns.
func (s *Server) lastAgentText(ctx context.Context, conversationID string) (string, int64, error) {
	msgs, err := s.db.ListMessages(ctx, conversationID)
	if err != nil {
		return "", 0, err
	}
	for i := len(msgs) - 1; i >= 0; i-- {
		m := msgs[i]
		if m.Type != string(db.MessageTypeAgent) {
			continue
		}
		if m.LlmData == nil {
			return "", m.SequenceID, nil
		}
		var llmMsg llm.Message
		if err := json.Unmarshal([]byte(*m.LlmData), &llmMsg); err != nil {
			return "", 0, err
		}
		var texts []string
		for _, content := range llmMsg.Content {
			if content.Type == llm.ContentTypeText && content.Text != "" {
				texts = append(texts, content.Text)
			}
		}
		// Callers splice this into the parent conversation, where it reaches
		// clients through a tool_result rather than through llmDataForAPI's
		// agent-text path. Strip here, before any caller truncates: a byte
		// cut through a marker's 3-byte sequence would leave an orphan that
		// no later strip can recognize.
		return llm.StripInlineCitationMarkers(strings.Join(texts, "\n")), m.SequenceID, nil
	}
	return "", 0, nil
}

// Ensure SubagentRunner implements claudetool.SubagentRunner.
var (
	_ claudetool.SubagentRunner  = (*SubagentRunner)(nil)
	_ claudetool.ParentMessenger = (*SubagentRunner)(nil)
)

// cancelSubagentTree cancels the active turns of all subagent conversations
// beneath parentID (children, grandchildren, ...). When the user cancels a
// conversation they mean "stop all of this work", including work delegated to
// subagents — leaving those running would waste tokens on results nobody will
// consume (the parent's tool call awaiting them was just torn down).
//
// Only actively-working subagents are cancelled: an idle manager may still
// hold a hydrated loop, and CancelConversation would record a spurious
// "[Operation cancelled]" end-of-turn message on a turn that already
// finished.
func (s *Server) cancelSubagentTree(ctx context.Context, parentID string) {
	visited := map[string]bool{parentID: true}
	queue := []string{parentID}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]

		children, err := s.db.GetSubagents(ctx, id)
		if err != nil {
			s.logger.Error("Failed to list subagents for cancellation", "conversationID", id, "error", err)
			continue
		}
		for _, child := range children {
			if !isManagedChild(child) {
				continue
			}
			kind := db.ParseConversationOptions(child.ConversationOptions).Kind
			if isBtwReader(child) || kind == commitTourKind {
				// Detached work is independent of parent-turn cancellation. Do not
				// cancel it or traverse through it.
				continue
			}
			if visited[child.ConversationID] {
				continue
			}
			visited[child.ConversationID] = true
			queue = append(queue, child.ConversationID)

			s.mu.Lock()
			mgr, active := s.activeConversations[child.ConversationID]
			s.mu.Unlock()
			if !active || !mgr.IsAgentWorking() {
				continue
			}
			if err := mgr.CancelConversation(ctx); err != nil {
				s.logger.Error("Failed to cancel subagent conversation", "conversationID", child.ConversationID, "parent", id, "error", err)
				continue
			}
			s.logger.Info("Cancelled subagent conversation", "conversationID", child.ConversationID, "parent", id)
		}
	}
}

// handleGetSubagents returns the list of subagents for a conversation.
func (s *Server) handleGetSubagents(w http.ResponseWriter, r *http.Request, conversationID string) {
	if r.Method != "GET" {
		http.Error(w, "Method not allowed", 405)
		return
	}

	subagents, err := s.db.GetSubagents(r.Context(), conversationID)
	if err != nil {
		s.logger.Error("Failed to get subagents", "conversationID", conversationID, "error", err)
		http.Error(w, "Failed to get subagents", 500)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(subagents)
}
