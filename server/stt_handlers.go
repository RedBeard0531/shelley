package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

// maxSTTMessageBytes caps a single websocket audio frame (about 2 seconds of
// 16 kHz PCM16). The UI sends ~200 ms chunks.
const maxSTTMessageBytes = 128 * 1024

// handleSTTInfo reports whether server-side streaming transcription is
// available. The UI hides the mic button when it is not, instead of showing
// a button that can't work.
func (s *Server) handleSTTInfo(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if s.transcriber == nil {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"available": false,
			"reason":    "Voice transcription is not enabled (set --stt-model-dir on the server)",
		})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"available": true})
}

// handleSTTWS serves a streaming transcription websocket. The client sends
// binary PCM16 little-endian mono 16 kHz audio frames and either text
// {"stop":true} to finalize cleanly or a close frame. The server emits text
// JSON messages {"partial": text} and {"final": text}. "final" messages
// commit an utterance; "partial" messages replace the tail of the current
// utterance.
func (s *Server) handleSTTWS(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		s.logger.Error("Failed to upgrade STT websocket", "error", err)
		return
	}
	closeConn := func(code websocket.StatusCode, reason string) {
		conn.Close(code, reason)
	}
	defer closeConn(websocket.StatusInternalError, "internal error")

	if s.transcriber == nil {
		_ = wsjson.Write(ctx, conn, map[string]string{"error": "transcription not enabled"})
		closeConn(websocket.StatusPolicyViolation, "transcription not enabled")
		return
	}

	session := s.transcriber.NewSession()
	writeFinal := func(final string) {
		if final == "" {
			return
		}
		// The connection may already be gone (client navigated away); a
		// failed write is not worth surfacing.
		_ = wsjson.Write(context.Background(), conn, map[string]string{"final": final})
	}

	for {
		typ, rd, err := conn.Reader(ctx)
		if err != nil {
			break
		}
		switch typ {
		case websocket.MessageBinary:
			data, rerr := io.ReadAll(io.LimitReader(rd, maxSTTMessageBytes))
			if rerr != nil || len(data) == 0 {
				break
			}
			partial, final := session.Feed(data)
			if partial != "" {
				if werr := wsjson.Write(ctx, conn, map[string]string{"partial": partial}); werr != nil {
					break
				}
			}
			if final != "" {
				if werr := wsjson.Write(ctx, conn, map[string]string{"final": final}); werr != nil {
					break
				}
			}
		case websocket.MessageText:
			payload, rerr := io.ReadAll(io.LimitReader(rd, 4096))
			if rerr != nil {
				break
			}
			var msg struct {
				Stop bool `json:"stop"`
			}
			if err := json.Unmarshal(payload, &msg); err == nil && msg.Stop {
				writeFinal(session.Close())
				closeConn(websocket.StatusNormalClosure, "done")
				return
			}
		}
	}

	// The client hung up without an explicit stop; flush whatever is
	// dangling as a final so no dictation is lost.
	writeFinal(session.Close())
}
