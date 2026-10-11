package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
)

const maxLiveRequestBytes = 128 << 10

// liveVoiceExchange separates the spoken request from the voice exchange it
// came from in the visible message (see liveBackendMessage and liveContext).
const liveVoiceExchange = "\n\nVoice exchange (for context only):\n"

// handleLiveMessage delivers speech delegated by Live through the ordinary
// chat endpoint, so the backend reads the raw request with its full
// conversation and a draft's first send is as from the composer. It returns
// chat's receipt: the status and the message as submitted.
func (s *Server) handleLiveMessage(w http.ResponseWriter, r *http.Request, conversationID string) {
	var req struct {
		Transcript          string          `json:"transcript"`
		VoiceContext        string          `json:"voice_context"`
		Model               string          `json:"model"`
		Cwd                 string          `json:"cwd"`
		ConversationOptions json.RawMessage `json:"conversation_options"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxLiveRequestBytes)).Decode(&req); err != nil {
		http.Error(w, "Invalid live message", http.StatusBadRequest)
		return
	}
	req.Transcript = strings.TrimSpace(req.Transcript)
	if req.Transcript == "" {
		http.Error(w, "transcript is required", http.StatusBadRequest)
		return
	}
	// The speech and its verbatim exchange stay visible to both the user and
	// the backend. The prefix makes even a spoken slash command ordinary text.
	message := "Spoken request:\n" + req.Transcript
	if strings.TrimSpace(req.VoiceContext) != "" {
		message += liveVoiceExchange + req.VoiceContext
	}
	body, err := json.Marshal(map[string]any{
		"message": message, "model": req.Model, "cwd": req.Cwd, "conversation_options": req.ConversationOptions,
	})
	if err != nil {
		s.internalError(w, "Encode live message", err)
		return
	}
	chatReq := r.Clone(r.Context())
	chatReq.Body, chatReq.ContentLength = io.NopCloser(bytes.NewReader(body)), int64(len(body))
	chatReq.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	s.handleChatConversation(recorder, chatReq, conversationID)
	s.logger.Info("Live delegation submitted", "conversationID", conversationID, "status", recorder.Code)
	for k, v := range recorder.Header() {
		w.Header()[k] = v
	}
	w.WriteHeader(recorder.Code)
	w.Write(recorder.Body.Bytes())
}
