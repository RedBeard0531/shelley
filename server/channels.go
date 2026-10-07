package server

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"

	"shelley.exe.dev/claudetool"
	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/exeenv"
	"shelley.exe.dev/llm"
)

// A channel connects conversations to an external chat service, such as the
// exe.dev messages integration (the VM owner's iMessage, RCS, or SMS chat
// with exe.dev). Each external chat maps to one conversation, created on the
// chat's first message. The conversation records the chat's id
// (external_conversation_id) and the endpoint its replies go to
// (external_endpoint); each delivered message records the channel's id for
// it (external_message_id). The agent answers with message_user, whose
// messages and reactions go to the chat (see channelChat).
//
// An endpoint speaks the exe.dev messages gateway's API: POST /send
// {"text":"...","reply_to":"<message_id>"} answers {"message_id":"..."}, POST /react
// {"message_id":"<message_id>","reaction":"<emoji>"} answers {"ok":true}, and a
// refusal answers {"error":"<code>","message":"..."}.

// messagesIntegration is the exe.dev integration whose chat exed delivers to
// POST /api/channels/messages, and which takes that chat's replies.
const messagesIntegration = "messages"

// channelEvent is one inbound event on an external chat: the JSON exed posts
// to POST /api/channels/messages (exe.dev's messages/wire.Event). Only what
// Shelley uses is decoded; reactions and attachments are not delivered yet.
type channelEvent struct {
	// Type is "message" or "reaction".
	Type string `json:"type"`
	// ID is the channel's id for the message.
	ID     string `json:"id"`
	ChatID string `json:"chat_id"`
	// Sender is the handle (phone number or email) that sent the message.
	Sender string `json:"sender"`
	Text   string `json:"text"`
	// Web marks a web chat: one shown on a web page that reads the
	// conversation's stream (GET /api/channels/messages/{chat}/stream)
	// instead of taking replies. Its conversation has no endpoint, so
	// message_user's messages stay in the conversation. The chat's first
	// message decides.
	Web bool `json:"web"`
	// OptOut marks a message (like "STOP") with which the user opted out.
	// exed recorded it and answered; replies are refused until they write
	// again.
	OptOut bool `json:"opt_out"`
}

// handleChannelMessage serves POST /api/channels/messages: exed delivers an
// event on one of the VM owner's chats with exe.dev, their messages chat or
// a web chat. A non-2xx answer makes exed's provider retry the delivery.
func (s *Server) handleChannelMessage(w http.ResponseWriter, r *http.Request) {
	var ev channelEvent
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&ev); err != nil {
		http.Error(w, "invalid channel event", http.StatusBadRequest)
		return
	}
	if debugChatIDPattern.MatchString(ev.ChatID) {
		http.Error(w, "chat ids like "+debugChatIDPattern.String()+" are reserved for /debug/channels", http.StatusBadRequest)
		return
	}
	env, err := exeenv.Current()
	if err != nil {
		s.internalError(w, "Cannot determine exe.dev environment", err)
		return
	}
	endpoint := env.IntegrationURL(messagesIntegration, false)
	if ev.Web {
		endpoint = ""
	}
	if err := s.receiveChannelEvent(r.Context(), endpoint, ev); err != nil {
		if errors.Is(err, errInvalidChannelEvent) {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		s.internalError(w, "Failed to deliver channel event", err, "chat_id", ev.ChatID, "message_id", ev.ID)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

var errInvalidChannelEvent = errors.New("channel event has no chat_id")

// handleChannelStream serves GET /api/channels/messages/{chat}/stream: the
// chat's conversation stream, as GET /api/conversation/{id}/stream serves
// it, or 204 No Content before the chat's first message.
func (s *Server) handleChannelStream(w http.ResponseWriter, r *http.Request) {
	chatID := r.PathValue("chat")
	var conv generated.Conversation
	err := s.db.Queries(r.Context(), func(q *generated.Queries) (err error) {
		conv, err = q.GetConversationByExternalID(r.Context(), &chatID)
		return err
	})
	if errors.Is(err, sql.ErrNoRows) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err != nil {
		s.internalError(w, "Failed to look up chat conversation", err, "chat_id", chatID)
		return
	}
	s.handleStreamConversation(w, r, conv.ConversationID)
}

// receiveChannelEvent delivers ev, an event on the external chat whose
// replies go to endpoint ("" for a web chat, whose replies stay in the
// conversation), to the chat's conversation as a user message. A message
// that was already delivered (a retry) is dropped.
func (s *Server) receiveChannelEvent(ctx context.Context, endpoint string, ev channelEvent) error {
	if ev.ChatID == "" {
		return errInvalidChannelEvent
	}
	if ev.Type != "message" || ev.OptOut || strings.TrimSpace(ev.Text) == "" {
		return nil
	}

	// One delivery per chat at a time, so that a chat's first messages
	// create one conversation and a retry racing its original is seen as
	// delivered.
	unlock, err := s.lockChannelChat(ctx, ev.ChatID)
	if err != nil {
		return err
	}
	defer unlock()

	conv, err := s.channelConversation(ctx, endpoint, ev.ChatID)
	if err != nil {
		return err
	}
	conversationID := conv.ConversationID
	manager, err := s.getOrCreateConversationManager(ctx, conversationID, "")
	if err != nil {
		return err
	}
	if ev.ID != "" {
		delivered, err := s.channelMessageDelivered(ctx, conversationID, ev.ID)
		if err != nil {
			return err
		}
		if delivered {
			// The original may wait in a queue nothing drains, as after
			// a restart.
			manager.drainQueueIfIdle(s)
			return nil
		}
	}

	if conv.Model == nil {
		return fmt.Errorf("conversation %s has no model", conversationID)
	}
	modelID := *conv.Model
	service, err := s.llmManager.GetService(modelID)
	if err != nil {
		return fmt.Errorf("model %s: %w", modelID, err)
	}
	ctx = contextWithExternalMessageID(ctx, ev.ID)
	// A busy turn takes channel messages at its next model request, after
	// the current tool round finishes, even with UI messages queued for later.
	// Distillation still holds them for the new generation.
	message := llm.UserStringMessage(ev.Text)
	hasQueued, err := manager.HasQueuedMessages(ctx)
	if err != nil {
		return err
	}
	if hasQueued || manager.IsAgentWorking() || manager.IsDistilling() {
		return manager.InjectMessage(ctx, s, modelID, message)
	}
	first, err := manager.AcceptUserMessage(ctx, service, modelID, message)
	if errors.Is(err, errQueuedMessagesPending) {
		return manager.InjectMessage(ctx, s, modelID, message)
	}
	if err != nil {
		return err
	}
	if first {
		s.generateSlugAsync(conversationID, ev.Text, modelID)
	}
	return nil
}

// channelChatLock serializes deliveries to one chat; refs counts holders
// and waiters, and the last one out removes it.
type channelChatLock struct {
	held chan struct{}
	refs int
}

// lockChannelChat waits for chatID's delivery lock, or for ctx to end, and
// returns its unlock.
func (s *Server) lockChannelChat(ctx context.Context, chatID string) (func(), error) {
	s.mu.Lock()
	l := s.channelChatLocks[chatID]
	if l == nil {
		l = &channelChatLock{held: make(chan struct{}, 1)}
		s.channelChatLocks[chatID] = l
	}
	l.refs++
	s.mu.Unlock()
	release := func() {
		s.mu.Lock()
		if l.refs--; l.refs == 0 {
			delete(s.channelChatLocks, chatID)
		}
		s.mu.Unlock()
	}
	select {
	case l.held <- struct{}{}:
		return func() { <-l.held; release() }, nil
	case <-ctx.Done():
		release()
		return nil, ctx.Err()
	}
}

// recoverChannelQueues drains the queues of channel conversations at
// startup. Nobody watches those conversations' UI, so messages left queued
// by the previous process would otherwise wait for the chat's next message.
func (s *Server) recoverChannelQueues(ctx context.Context) {
	var conversations []generated.Conversation
	err := s.db.Queries(ctx, func(q *generated.Queries) (err error) {
		conversations, err = q.ListChannelConversationsWithQueuedMessages(ctx)
		return err
	})
	if err != nil {
		s.logger.Error("Failed to scan channel queues", "error", err)
		return
	}
	for _, conv := range conversations {
		manager, err := s.getOrCreateConversationManager(ctx, conv.ConversationID, "")
		if err != nil {
			s.logger.Error("Failed to restore channel conversation", "conversationID", conv.ConversationID, "error", err)
			continue
		}
		manager.drainQueueIfIdle(s)
	}
}

// channelConversation returns the conversation of the external chat chatID,
// creating it, with the default model and message_user, on the chat's
// first message. It creates none if the model is unavailable, so failing
// deliveries leave no empty conversations.
func (s *Server) channelConversation(ctx context.Context, endpoint, chatID string) (*generated.Conversation, error) {
	var conv generated.Conversation
	err := s.db.Queries(ctx, func(q *generated.Queries) (err error) {
		conv, err = q.GetConversationByExternalID(ctx, &chatID)
		return err
	})
	if !errors.Is(err, sql.ErrNoRows) {
		return &conv, err
	}
	modelID := s.effectiveDefaultModel(s.getModelList())
	if _, err := s.llmManager.GetService(modelID); err != nil {
		return nil, fmt.Errorf("model %s: %w", modelID, err)
	}
	// The agent answers the chat with message_user.
	opts := db.ConversationOptions{ToolOverrides: map[string]string{claudetool.MessageUserName: "on"}}
	return s.db.CreateChannelConversation(ctx, chatID, endpoint, modelID, opts)
}

// channelMessageDelivered reports whether the channel message id is already
// in the conversation, as a message or still queued. The queue is read
// first: an item drains into messages atomically, so it is seen in one or
// the other.
func (s *Server) channelMessageDelivered(ctx context.Context, conversationID, id string) (bool, error) {
	queued, err := s.db.GetQueuedMessages(ctx, conversationID)
	if err != nil {
		return false, err
	}
	if slices.ContainsFunc(queued, func(qm db.QueuedMessage) bool { return qm.ExternalMessageID == id }) {
		return true, nil
	}
	var n int64
	err = s.db.Queries(ctx, func(q *generated.Queries) (err error) {
		n, err = q.HasExternalMessage(ctx, generated.HasExternalMessageParams{ConversationID: conversationID, ExternalMessageID: &id})
		return err
	})
	return n != 0, err
}

// channelSendError is an endpoint's refusal to send. The messages gateway
// refuses with 403 opted_out, 409 no_chat, 429 rate_limited or unanswered,
// and 503 line_paused or chat_critical; its message says what to do.
type channelSendError struct {
	Status  int
	Code    string
	Message string
}

func (e *channelSendError) Error() string {
	return fmt.Sprintf("%s (HTTP %d): %s", e.Code, e.Status, e.Message)
}

// sendChannelMessage sends text to the external chat that conversationID is
// bound to, and returns the channel's id for the sent message.
func (s *Server) sendChannelMessage(ctx context.Context, conversationID, text, replyTo string) (string, error) {
	var sent struct {
		MessageID string `json:"message_id"`
	}
	body := map[string]string{"text": text}
	if replyTo != "" {
		body["reply_to"] = replyTo
	}
	err := s.callChannel(ctx, conversationID, "/send", body, &sent)
	return sent.MessageID, err
}

// callChannel POSTs body to path at the endpoint of the external chat that
// conversationID is bound to, and decodes the answer into out. A failure
// the endpoint may have acted on anyway wraps errChannelMaybeDelivered;
// otherwise a refusal is a *channelSendError.
func (s *Server) callChannel(ctx context.Context, conversationID, path string, body, out any) error {
	conv, err := s.db.GetConversationByID(ctx, conversationID)
	if err != nil {
		return err
	}
	if conv.ExternalEndpoint == nil {
		return fmt.Errorf("conversation %s is not bound to an external chat", conversationID)
	}
	reqBody, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, *conv.ExternalEndpoint+path, bytes.NewReader(reqBody))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.channelClient.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %w", errChannelMaybeDelivered, err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return fmt.Errorf("%w: %w", errChannelMaybeDelivered, err)
	}
	if resp.StatusCode != http.StatusOK {
		var refusal struct {
			Error   string `json:"error"`
			Message string `json:"message"`
		}
		if json.Unmarshal(respBody, &refusal) != nil || refusal.Error == "" {
			refusal.Message = strings.TrimSpace(string(respBody))
		}
		err := &channelSendError{Status: resp.StatusCode, Code: refusal.Error, Message: refusal.Message}
		if !err.refused() {
			return fmt.Errorf("%w: %w", errChannelMaybeDelivered, err)
		}
		return err
	}
	if err := json.Unmarshal(respBody, out); err != nil {
		return fmt.Errorf("%w: decode %s response: %w", errChannelMaybeDelivered, path, err)
	}
	return nil
}

// refused reports whether e is one of the gateway's refusals before
// sending, rather than a failure that may follow delivery: a server error,
// its provider's server-side failure (passed on as upstream_error), or an
// answer without a code, perhaps a proxy's.
func (e *channelSendError) refused() bool {
	switch e.Code {
	case "opted_out", "line_paused", "chat_critical", "rate_limited", "unanswered", "no_chat", "bad_request", "not_found", "not_configured":
		return true
	case "upstream_error":
		return e.Status < 500
	}
	return false
}

// errChannelMaybeDelivered marks a failed endpoint call that the endpoint
// may have carried out.
var errChannelMaybeDelivered = errors.New("it may have been delivered anyway")

// channelChat is the claudetool.UserChat of a conversation bound to an
// external chat: message_user's messages go to the chat.
type channelChat struct {
	s              *Server
	conversationID string
}

func (c channelChat) Send(ctx context.Context, text string, target claudetool.UserMessage) error {
	_, err := c.s.sendChannelMessage(ctx, c.conversationID, text, target.ExternalID)
	return chatDeliveryError(err)
}

// React reacts to target in the chat, as a tapback if emoji is one. A
// message typed in Shelley's UI rather than the chat has no chat message to
// react to; the reaction stays in the UI.
func (c channelChat) React(ctx context.Context, target claudetool.UserMessage, emoji string) error {
	if target.ExternalID == "" {
		return nil
	}
	reaction := emoji
	if name, ok := tapbacks[strings.TrimSuffix(emoji, "\uFE0F")]; ok {
		reaction = name
	}
	var answer struct{}
	body := map[string]string{"message_id": target.ExternalID, "reaction": reaction}
	return chatDeliveryError(c.s.callChannel(ctx, c.conversationID, "/react", body, &answer))
}

// tapbacks names the emoji of the gateway's native reactions, without
// variation selectors; it takes other emoji as custom reactions.
var tapbacks = map[string]string{
	"❤": "love",
	"👍": "like",
	"👎": "dislike",
	"😂": "laugh",
	"‼": "emphasize",
	"❓": "question",
}

// chatDeliveryError tells the agent what err, from an endpoint call, means
// for delivery: an endpoint's refusal, like a failure before the call went
// out, delivered nothing, but a lost or cancelled call may have.
func chatDeliveryError(err error) error {
	switch refusal := (*channelSendError)(nil); {
	case err == nil:
		return nil
	case errors.Is(err, errChannelMaybeDelivered):
		return err
	case errors.As(err, &refusal):
		return fmt.Errorf("the chat refused it: %w", err)
	default:
		return fmt.Errorf("nothing was sent: %w", err)
	}
}
