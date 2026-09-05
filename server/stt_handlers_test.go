package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"shelley.exe.dev/server/stt"
)

// fakeSession replays a scripted partial/final sequence per Feed call and
// reports whether Close ran.
type fakeSession struct {
	feeds    int
	partials []string // one per Feed call; empty string means "no partial"
	finals   []string
	closed   bool
}

func (f *fakeSession) Feed([]byte) (string, string) {
	i := f.feeds
	f.feeds++
	var p, fin string
	if i < len(f.partials) {
		p = f.partials[i]
	}
	if i < len(f.finals) {
		fin = f.finals[i]
	}
	return p, fin
}

func (f *fakeSession) Close() string {
	f.closed = true
	return "dangling tail"
}

type fakeEngine struct{ s *fakeSession }

func (e fakeEngine) NewSession() stt.Session { return e.s }

func TestSTTInfoUnavailableByDefault(t *testing.T) {
	t.Parallel()
	h := NewTestHarness(t)
	mux := http.NewServeMux()
	h.server.RegisterRoutes(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/stt")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var info struct {
		Available bool   `json:"available"`
		Reason    string `json:"reason"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		t.Fatal(err)
	}
	if info.Available {
		t.Fatal("expected available=false when no transcriber is configured")
	}
	if info.Reason == "" {
		t.Fatal("expected a reason explaining the mic is off")
	}
}

func TestSTTInfoAvailable(t *testing.T) {
	t.Parallel()
	h := NewTestHarness(t)
	h.server.SetTranscriber(fakeEngine{})
	mux := http.NewServeMux()
	h.server.RegisterRoutes(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/stt")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var info struct {
		Available bool `json:"available"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		t.Fatal(err)
	}
	if !info.Available {
		t.Fatal("expected available=true with a transcriber configured")
	}
}

// TestSTTWSReplaysPartialsAndFinals drives the websocket protocol: binary
// audio frames produce partial/final messages, and an explicit stop finalizes
// and closes the connection.
func TestSTTWSReplaysPartialsAndFinals(t *testing.T) {
	t.Parallel()
	sess := &fakeSession{
		partials: []string{"hel", "hello"},
		finals:   []string{"", "hello"},
	}
	h := NewTestHarness(t)
	h.server.SetTranscriber(fakeEngine{s: sess})
	mux := http.NewServeMux()
	h.server.RegisterRoutes(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/stt/ws"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()

	if err := conn.Write(ctx, websocket.MessageBinary, []byte{1, 0}); err != nil {
		t.Fatal(err)
	}
	var got []map[string]string
	read := func() map[string]string {
		t.Helper()
		var m map[string]string
		if err := wsjson.Read(ctx, conn, &m); err != nil {
			t.Fatalf("ws read: %v", err)
		}
		return m
	}
	got = append(got, read()) // {"partial":"hel"}
	if err := conn.Write(ctx, websocket.MessageBinary, []byte{2, 0}); err != nil {
		t.Fatal(err)
	}
	got = append(got, read()) // {"partial":"hello"}
	if err := conn.Write(ctx, websocket.MessageBinary, []byte{3, 0}); err != nil {
		t.Fatal(err)
	}
	got = append(got, read()) // {"final":"hello"}
	// Explicit stop: server flushes the dangling tail and closes.
	if err := wsjson.Write(ctx, conn, map[string]bool{"stop": true}); err != nil {
		t.Fatal(err)
	}
	got = append(got, read()) // {"final":"dangling tail"}
	if _, _, err := conn.Read(ctx); err == nil {
		t.Fatal("expected connection to close after stop")
	} else if !strings.Contains(err.Error(), "done") {
		t.Fatalf("expected normal close with reason 'done', got %v", err)
	}
	if !sess.closed {
		t.Fatal("expected session.Close to run")
	}

	want := []map[string]string{
		{"partial": "hel"},
		{"partial": "hello"},
		{"final": "hello"},
		{"final": "dangling tail"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d messages, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		for k, v := range want[i] {
			if got[i][k] != v {
				t.Errorf("message %d: %v, want %v", i, got[i], want[i])
			}
		}
	}
}

// TestSTTWSClientHangupFinalizes ensures a client that drops the connection
// without an explicit stop still gets its dangling utterance flushed.
func TestSTTWSClientHangupFinalizes(t *testing.T) {
	t.Parallel()
	sess := &fakeSession{}
	h := NewTestHarness(t)
	h.server.SetTranscriber(fakeEngine{s: sess})
	mux := http.NewServeMux()
	h.server.RegisterRoutes(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/stt/ws"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.Write(ctx, websocket.MessageBinary, []byte{9, 0}); err != nil {
		t.Fatal(err)
	}
	// Drop with a close frame immediately (the normal browser hangup). The
	// server finalizes whether or not it consumed the in-flight frame.
	conn.Close(websocket.StatusNormalClosure, "client hangup")

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if sess.closed {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("expected session.Close after client hangup")
}

// TestSTTWSRejectsOversizedFrames ensures the frame-size cap is enforced
// (a malformed client cannot buffer unbounded audio server-side).
func TestSTTWSRejectsOversizedFrames(t *testing.T) {
	t.Parallel()
	sess := &fakeSession{}
	h := NewTestHarness(t)
	h.server.SetTranscriber(fakeEngine{s: sess})
	mux := http.NewServeMux()
	h.server.RegisterRoutes(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/stt/ws"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()

	big := make([]byte, maxSTTMessageBytes+1)
	if err := conn.Write(ctx, websocket.MessageBinary, big); err != nil {
		t.Fatal(err)
	}
	// Server should drop the connection after LimitReader truncates/exceeds.
	start := time.Now()
	for time.Since(start) < 2*time.Second {
		if _, _, err := conn.Read(ctx); err != nil {
			return // connection closed: expected
		}
	}
	t.Fatal("expected connection close on oversized frame (no message loop forever)")
}
