package llmhttp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"shelley.exe.dev/llm"
	"strings"
	"sync"
	"testing"
	"time"
)

// roundTripperFunc adapts a function to http.RoundTripper.
type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func record(t *testing.T, dir string, base http.RoundTripper) (*FlightRecorder, *http.Client) {
	t.Helper()
	rec, err := NewFlightRecorder(dir)
	if err != nil {
		t.Fatalf("NewFlightRecorder: %v", err)
	}
	return rec, &http.Client{Transport: &Transport{Base: base, FlightRecorder: rec}}
}

func metaFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	var out []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), flightMetaSuffix) {
			out = append(out, e.Name())
		}
	}
	return out
}

func readMeta(t *testing.T, dir, name string) flightRecord {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("read meta %s: %v", name, err)
	}
	var rec flightRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		t.Fatalf("unmarshal meta %s: %v", name, err)
	}
	return rec
}

// TestFlightRecorderCapturesExchange verifies a request/response round trip
// is captured: the request body file is byte-identical to what the server
// received, the response body file matches what the server sent, and the
// meta carries headers, status, URL, correlation ids, and context tags.
func TestFlightRecorderCapturesExchange(t *testing.T) {
	dir := t.TempDir()
	wantBody := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("X-Request-Id", "req_upstream_123")
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"ok":true}`)
	}))
	defer srv.Close()

	_, client := record(t, dir, srv.Client().Transport)
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/v1/chat/completions", bytes.NewReader(wantBody))
	if err != nil {
		t.Fatal(err)
	}
	ctx, _ := llm.WithRequestTrace(context.Background())
	ctx = WithConversationID(ctx, "conv-1")
	ctx = WithProvider(ctx, "fireworks")
	ctx = WithModelID(ctx, "model-1")
	ctx = llm.WithPurpose(ctx, "keyword_search")
	resp, err := client.Do(req.WithContext(ctx))
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	respBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(respBody) != `{"ok":true}` {
		t.Fatalf("unexpected response body %q", respBody)
	}
	// Byte-exact on the wire: the server must have seen the same bytes.
	if !bytes.Equal(gotBody, wantBody) {
		t.Fatalf("wire body %q != sent body %q", gotBody, wantBody)
	}

	files := metaFiles(t, dir)
	if len(files) != 1 {
		t.Fatalf("want 1 meta file, got %v", files)
	}
	meta := readMeta(t, dir, files[0])
	if meta.SchemaVersion != 1 || meta.ShelleyRequestID == "" {
		t.Fatalf("bad id fields: %+v", meta)
	}
	if meta.Request.Method != http.MethodPost || meta.Request.URL != srv.URL+"/v1/chat/completions" {
		t.Fatalf("bad request meta: %+v", meta.Request)
	}
	if meta.Request.BodyBytes != len(wantBody) {
		t.Fatalf("body_bytes %d != %d", meta.Request.BodyBytes, len(wantBody))
	}
	if meta.ConversationID != "conv-1" || meta.Provider != "fireworks" || meta.ModelID != "model-1" || meta.Purpose != "keyword_search" {
		t.Fatalf("bad context tags: %+v", meta)
	}
	if meta.UpstreamRequestID != "req_upstream_123" {
		t.Fatalf("upstream id %q", meta.UpstreamRequestID)
	}
	if meta.Response == nil || meta.Response.StatusCode != 200 || meta.Response.Status != "200 OK" {
		t.Fatalf("bad response meta: %+v", meta.Response)
	}
	if !meta.Response.BodyComplete {
		t.Fatalf("response body not marked complete")
	}
	if meta.Response.Headers["Content-Type"][0] != "application/json" {
		t.Fatalf("response headers not captured: %v", meta.Response.Headers)
	}
	if meta.Request.Headers["User-Agent"][0] == "" || meta.Request.Headers["Shelley-Request-Id"][0] == "" {
		t.Fatalf("request headers not captured: %v", meta.Request.Headers)
	}

	// Byte-exact body files.
	base := strings.TrimSuffix(files[0], flightMetaSuffix)
	reqBytes, err := os.ReadFile(filepath.Join(dir, base+flightRequestBodySuffix))
	if err != nil {
		t.Fatalf("read request body file: %v", err)
	}
	if !bytes.Equal(reqBytes, wantBody) {
		t.Fatalf("recorded request body %q != sent %q", reqBytes, wantBody)
	}
	respBytes, err := os.ReadFile(filepath.Join(dir, base+flightResponseBodySuffix))
	if err != nil {
		t.Fatalf("read response body file: %v", err)
	}
	if !bytes.Equal(respBytes, []byte(`{"ok":true}`)) {
		t.Fatalf("recorded response body %q", respBytes)
	}
}

// TestFlightRecorderStreaming verifies the response body is captured
// byte-exactly as it streams, chunk by chunk.
func TestFlightRecorderStreaming(t *testing.T) {
	dir := t.TempDir()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		for _, chunk := range []string{"data: a\n\n", "data: b\n\n", "data: c\n\n"} {
			io.WriteString(w, chunk)
			fl.Flush()
		}
	}))
	defer srv.Close()

	_, client := record(t, dir, srv.Client().Transport)
	resp, err := client.Post(srv.URL, "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), "data: b") {
		t.Fatalf("unexpected streamed body %q", body)
	}

	files := metaFiles(t, dir)
	base := strings.TrimSuffix(files[0], flightMetaSuffix)
	got, _ := os.ReadFile(filepath.Join(dir, base+flightResponseBodySuffix))
	if string(got) != "data: a\n\ndata: b\n\ndata: c\n\n" {
		t.Fatalf("recorded stream %q", got)
	}
}

// TestFlightRecorderPartialClose verifies that a body closed before EOF is
// stored as an incomplete capture with the bytes read so far.
func TestFlightRecorderPartialClose(t *testing.T) {
	dir := t.TempDir()
	release := make(chan struct{})
	releaseOnce := sync.OnceFunc(func() { close(release) })
	defer releaseOnce()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "abcdef")
		w.(http.Flusher).Flush()
		<-release
	}))
	defer srv.Close()

	_, client := record(t, dir, srv.Client().Transport)
	resp, err := client.Post(srv.URL, "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	first := make([]byte, 3)
	if _, err := io.ReadFull(resp.Body, first); err != nil {
		t.Fatalf("read: %v", err)
	}
	resp.Body.Close()
	releaseOnce()

	files := metaFiles(t, dir)
	meta := readMeta(t, dir, files[0])
	if meta.Response == nil || meta.Response.BodyComplete {
		t.Fatalf("expected incomplete response, got %+v", meta.Response)
	}
	if meta.Error != "response body closed before EOF" {
		t.Fatalf("error field %q", meta.Error)
	}
	base := strings.TrimSuffix(files[0], flightMetaSuffix)
	got, _ := os.ReadFile(filepath.Join(dir, base+flightResponseBodySuffix))
	if string(got) != "abc" {
		t.Fatalf("recorded partial body %q", got)
	}
}

// TestFlightRecorderTransportError verifies an attempt that never gets a
// response is recorded with the error and no response section.
func TestFlightRecorderTransportError(t *testing.T) {
	dir := t.TempDir()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close() // nothing listening: dial will refuse

	_, client := record(t, dir, http.DefaultTransport)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+addr, strings.NewReader(`{"x":1}`))
	if _, err := client.Do(req); err == nil {
		t.Fatal("expected dial error")
	}

	files := metaFiles(t, dir)
	if len(files) != 1 {
		t.Fatalf("want 1 record, got %v", files)
	}
	meta := readMeta(t, dir, files[0])
	if meta.Response != nil {
		t.Fatalf("unexpected response section: %+v", meta.Response)
	}
	if meta.Error == "" {
		t.Fatalf("missing error in meta")
	}
	base := strings.TrimSuffix(files[0], flightMetaSuffix)
	reqBytes, _ := os.ReadFile(filepath.Join(dir, base+flightRequestBodySuffix))
	if string(reqBytes) != `{"x":1}` {
		t.Fatalf("request body %q", reqBytes)
	}
	if _, err := os.Stat(filepath.Join(dir, base+flightResponseBodySuffix)); !os.IsNotExist(err) {
		t.Fatalf("response body file should not exist")
	}
}

// TestFlightRecorderPrunesToMax verifies only the newest maxFlights records
// survive.
func TestFlightRecorderPrunesToMax(t *testing.T) {
	dir := t.TempDir()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "ok")
	}))
	defer srv.Close()

	_, client := record(t, dir, srv.Client().Transport)
	total := maxFlights + 7
	for i := 0; i < total; i++ {
		resp, err := client.Post(srv.URL, "application/json", strings.NewReader(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
	files := metaFiles(t, dir)
	if len(files) != maxFlights {
		t.Fatalf("want %d records, got %d", maxFlights, len(files))
	}
	for _, f := range files {
		if _, err := os.Stat(filepath.Join(dir, strings.TrimSuffix(f, flightMetaSuffix)+flightRequestBodySuffix)); err != nil {
			t.Fatalf("missing body file for %s: %v", f, err)
		}
	}
}

// TestFlightRecorderRestoresGetBody verifies the swapped-in request body is
// replayable on the transport's own copy of the request (so base transports
// that re-read bodies — retries, HTTP/2, custom round-trippers — still see
// the exact bytes) and that a fresh recorder on an existing directory picks
// up prior records.
func TestFlightRecorderRestoresGetBody(t *testing.T) {
	dir := t.TempDir()
	payload := []byte(`{"a":1,"b":2}`)
	req, _ := http.NewRequest(http.MethodPost, "http://recorder.test/v1/chat/completions", nil)
	req.Body = io.NopCloser(bytes.NewReader(payload))
	req.GetBody = nil
	req.ContentLength = int64(len(payload))

	// The transport swaps in a replayable body on the clone it sends; verify
	// via a re-reading base transport that the bytes survive a re-read.
	var bodySeen, replaySeen bool
	var firstRead, secondRead []byte
	replayBase := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		b, err := io.ReadAll(req.Body)
		if err != nil {
			t.Fatalf("base read body: %v", err)
		}
		firstRead = b
		bodySeen = true
		if req.GetBody != nil {
			rc, err := req.GetBody()
			if err != nil {
				t.Fatalf("GetBody: %v", err)
			}
			b2, _ := io.ReadAll(rc)
			secondRead = b2
			replaySeen = true
		}
		return &http.Response{
			StatusCode: 200,
			Status:     "200 OK",
			Body:       io.NopCloser(strings.NewReader("ok")),
			Header:     make(http.Header),
		}, nil
	})
	_, client := record(t, dir, replayBase)

	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	if !bodySeen || !bytes.Equal(firstRead, payload) {
		t.Fatalf("base saw %q, want %q", firstRead, payload)
	}
	if !replaySeen || !bytes.Equal(secondRead, payload) {
		t.Fatalf("replayed body %q, want %q", secondRead, payload)
	}

	// NewFlightRecorder on an existing dir keeps prior records.
	rec2, err := NewFlightRecorder(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(rec2.files) != 1 {
		t.Fatalf("restart should see 1 record, got %d", len(rec2.files))
	}
}

// TestFlightRecorderSkipsNilBody verifies GET-style requests (nil body) pass
// through untouched and unrecorded.
func TestFlightRecorderSkipsNilBody(t *testing.T) {
	dir := t.TempDir()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "ok")
	}))
	defer srv.Close()

	_, client := record(t, dir, srv.Client().Transport)
	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if files := metaFiles(t, dir); len(files) != 0 {
		t.Fatalf("nil-body request should not be recorded, got %v", files)
	}
}

// TestEnableFlightRecorder verifies the wiring helper.
func TestEnableFlightRecorder(t *testing.T) {
	client := NewClient(nil)
	dir := t.TempDir()
	if _, err := EnableFlightRecorder(client, dir); err != nil {
		t.Fatalf("EnableFlightRecorder: %v", err)
	}
	tr := client.Transport.(*Transport)
	if tr.FlightRecorder == nil {
		t.Fatal("recorder not attached")
	}
	if _, err := EnableFlightRecorder(&http.Client{}, dir); err == nil {
		t.Fatal("expected error for non-transport client")
	}
}
