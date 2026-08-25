package llmhttp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Flight data recorder: persists byte-exact copies of LLM HTTP
// request/response traffic to disk so any request (including failures and
// stalls) can be reconstructed later — headers, bodies, URLs, status codes,
// correlation ids, and timings. See the README written into the recording
// directory for the on-disk layout.

// FlightsDirEnv names the environment variable that overrides the flight
// recorder directory. When unset, the default is $HOME/llm-flights.
const FlightsDirEnv = "SHELLEY_FLIGHT_RECORDER_DIR"

// DefaultFlightsDir returns the flight recorder directory: the override from
// FlightsDirEnv when set, else a dedicated "llm-flights" directory in the
// user's home directory.
func DefaultFlightsDir() (string, error) {
	if dir := os.Getenv(FlightsDirEnv); dir != "" {
		return dir, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("flight recorder home directory: %w", err)
	}
	return filepath.Join(home, "llm-flights"), nil
}

// maxFlights bounds how many request/response pairs are retained on disk;
// older records are pruned as new ones land.
const maxFlights = 100

const flightMetaSuffix = ".json"
const flightRequestBodySuffix = ".request.body"
const flightResponseBodySuffix = ".response.body"

// flightBaseRe matches the basename (without extension) that flight files
// use: a UTC timestamp with 9-digit fraction, an underscore, and the 16-hex
// Shelley request id.
var flightBaseRe = regexp.MustCompile(`^\d{8}T\d{6}\.\d{9}Z_[0-9a-f]{16}$`)

// FlightRecorder persists captured LLM request/response pairs under one
// directory. Safe for concurrent use.
type FlightRecorder struct {
	dir string

	mu    sync.Mutex
	files []string // basenames of retained *.json meta files, oldest first
}

// NewFlightRecorder creates the recording directory (and its README) and
// loads the current set of retained records so pruning can start correct.
func NewFlightRecorder(dir string) (*FlightRecorder, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("flight recorder mkdir %s: %w", dir, err)
	}
	r := &FlightRecorder{dir: dir}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("flight recorder readdir %s: %w", dir, err)
	}
	for _, e := range entries {
		if e.Type().IsRegular() && strings.HasSuffix(e.Name(), flightMetaSuffix) && flightBaseRe.MatchString(strings.TrimSuffix(e.Name(), flightMetaSuffix)) {
			r.files = append(r.files, e.Name())
		}
	}
	sort.Strings(r.files)
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte(flightReadme), 0o644); err != nil {
		return nil, fmt.Errorf("flight recorder write README: %w", err)
	}
	return r, nil
}

// EnableFlightRecorder attaches a fresh flight data recorder to a client
// created by NewClient or NewClientWithIdleTimeout, returning the recorder.
func EnableFlightRecorder(client *http.Client, dir string) (*FlightRecorder, error) {
	tr, ok := client.Transport.(*Transport)
	if !ok {
		return nil, fmt.Errorf("flight recorder: client transport is %T, want *llmhttp.Transport", client.Transport)
	}
	rec, err := NewFlightRecorder(dir)
	if err != nil {
		return nil, err
	}
	tr.FlightRecorder = rec
	return rec, nil
}

// flightSession tracks one request/response pair until the response body is
// fully consumed (or the attempt fails), then writes it to disk.
type flightSession struct {
	rec *FlightRecorder
	id  string // Shelley request id (the Shelley-Request-Id header value)

	started        time.Time
	requestBody    []byte
	conversationID string
	provider       string
	modelID        string
	purpose        string
	requestURL     string
	requestMethod  string
	requestHeaders http.Header

	mu           sync.Mutex
	response     *http.Response // headers/status/proto captured at attach; nil until a response arrives
	responseBody bytes.Buffer
	responseErr  error // non-EOF error that ended the read, if any
	responseEOF  bool  // body was read to a clean EOF
	closedNoEOF  bool  // body was closed before reaching EOF
	finishOnce   sync.Once
}

// begin captures the request body byte-exactly and swaps in a replayable
// body so the request can still go out (and be re-read for redirects/retries).
// Returns nil when the body cannot be captured (the request is then sent
// untouched and unrecorded).
func (r *FlightRecorder) begin(req *http.Request) *flightSession {
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil
	}
	req.Body = io.NopCloser(bytes.NewReader(body))
	if req.GetBody == nil {
		req.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
	}
	return &flightSession{
		rec:            r,
		id:             req.Header.Get(shelleyRequestIDHeader),
		started:        time.Now(),
		requestBody:    body,
		conversationID: ConversationIDFromContext(req.Context()),
		provider:       ProviderFromContext(req.Context()),
		modelID:        ModelIDFromContext(req.Context()),
		purpose:        PurposeFromContext(req.Context()),
		requestURL:     req.URL.String(),
		requestMethod:  req.Method,
		requestHeaders: req.Header.Clone(),
	}
}

// attachResponse records the response metadata and wraps its body so every
// byte the caller consumes is captured. The record is finalized when the
// body is read to EOF, hits a read error, or is closed early.
func (s *flightSession) attachResponse(resp *http.Response) {
	s.mu.Lock()
	s.response = resp
	s.mu.Unlock()
	resp.Body = &flightBody{rc: resp.Body, s: s}
}

// fail finalizes the record for a request that never got a response.
func (s *flightSession) fail(err error) {
	s.finishOnce.Do(func() {
		s.mu.Lock()
		s.responseErr = err
		s.mu.Unlock()
		s.write()
	})
}

// flightBody captures reads from a response body into the session.
type flightBody struct {
	rc io.ReadCloser
	s  *flightSession
}

func (f *flightBody) Read(p []byte) (int, error) {
	n, err := f.rc.Read(p)
	s := f.s
	if n > 0 {
		s.mu.Lock()
		s.responseBody.Write(p[:n])
		s.mu.Unlock()
	}
	if err != nil {
		s.finishOnce.Do(func() {
			s.mu.Lock()
			if err == io.EOF {
				s.responseEOF = true
			} else {
				s.responseErr = err
			}
			s.mu.Unlock()
			s.write()
		})
	}
	return n, err
}

func (f *flightBody) Close() error {
	err := f.rc.Close()
	f.s.finishOnce.Do(func() {
		f.s.mu.Lock()
		f.s.closedNoEOF = true
		if err != nil {
			f.s.responseErr = err
		}
		f.s.mu.Unlock()
		f.s.write()
	})
	return err
}

type flightRecord struct {
	SchemaVersion     int    `json:"schema_version"`
	CapturedAt        string `json:"captured_at"`
	ShelleyRequestID  string `json:"shelley_request_id,omitempty"`
	UpstreamRequestID string `json:"upstream_request_id,omitempty"`
	ConversationID    string `json:"conversation_id,omitempty"`
	Provider          string `json:"provider,omitempty"`
	ModelID           string `json:"model_id,omitempty"`
	Purpose           string `json:"purpose,omitempty"`
	DurationMs        int64  `json:"duration_ms,omitempty"`
	Error             string `json:"error,omitempty"`

	Request  flightRecordRequest   `json:"request"`
	Response *flightRecordResponse `json:"response,omitempty"`
}

type flightRecordRequest struct {
	Method    string              `json:"method"`
	URL       string              `json:"url"`
	Headers   map[string][]string `json:"headers"`
	BodyBytes int                 `json:"body_bytes"`
	BodyFile  string              `json:"body_file"`
}

type flightRecordResponse struct {
	StatusCode       int                 `json:"status_code"`
	Status           string              `json:"status"`
	Proto            string              `json:"proto"`
	Headers          map[string][]string `json:"headers"`
	BodyDecompressed bool                `json:"body_decompressed,omitempty"`
	BodyBytes        int                 `json:"body_bytes"`
	BodyFile         string              `json:"body_file"`
	BodyComplete     bool                `json:"body_complete"`
}

// write persists the record: a meta JSON file plus byte-exact .request.body
// and .response.body files, then prunes the directory to maxFlights records.
func (s *flightSession) write() {
	rec := s.rec
	rec.mu.Lock()
	defer rec.mu.Unlock()

	base := s.started.UTC().Format("20060102T150405.000000000Z") + "_" + s.id
	metaFile := base + flightMetaSuffix
	reqBodyFile := base + flightRequestBodySuffix
	respBodyFile := base + flightResponseBodySuffix

	s.mu.Lock()
	meta := flightRecord{
		SchemaVersion:    1,
		CapturedAt:       s.started.UTC().Format(time.RFC3339Nano),
		ShelleyRequestID: s.id,
		ConversationID:   s.conversationID,
		Provider:         s.provider,
		ModelID:          s.modelID,
		Purpose:          s.purpose,
		DurationMs:       time.Since(s.started).Milliseconds(),
		Request: flightRecordRequest{
			Method:    s.requestMethod,
			URL:       s.requestURL,
			Headers:   s.requestHeaders,
			BodyBytes: len(s.requestBody),
			BodyFile:  reqBodyFile,
		},
	}
	if s.response != nil {
		meta.UpstreamRequestID = upstreamRequestID(s.response.Header)
		meta.Response = &flightRecordResponse{
			StatusCode:       s.response.StatusCode,
			Status:           s.response.Status,
			Proto:            s.response.Proto,
			Headers:          s.response.Header,
			BodyDecompressed: s.response.Uncompressed,
			BodyBytes:        s.responseBody.Len(),
			BodyFile:         respBodyFile,
			BodyComplete:     s.responseEOF,
		}
	}
	switch {
	case s.responseErr != nil:
		meta.Error = s.responseErr.Error()
	case s.closedNoEOF:
		meta.Error = "response body closed before EOF"
	}
	metaJSON, err := json.MarshalIndent(meta, "", "  ")
	body := s.requestBody
	respBody := append([]byte(nil), s.responseBody.Bytes()...)
	s.mu.Unlock()
	if err != nil {
		return // meta unserializable; drop the record
	}

	writeAtomic(filepath.Join(rec.dir, metaFile), metaJSON)
	writeAtomic(filepath.Join(rec.dir, reqBodyFile), body)
	if s.response != nil {
		writeAtomic(filepath.Join(rec.dir, respBodyFile), respBody)
	}

	rec.files = append(rec.files, metaFile)
	sort.Strings(rec.files)
	rec.pruneLocked()
}

// upstreamRequestID returns the provider's request id from response headers,
// in the same priority order the transport uses for RequestTrace.
func upstreamRequestID(h http.Header) string {
	for _, name := range upstreamRequestIDHeaders {
		if v := h.Get(name); v != "" {
			return v
		}
	}
	return ""
}

func writeAtomic(path string, data []byte) {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return
	}
	os.Rename(tmp, path)
}

// pruneLocked deletes the oldest records beyond maxFlights. rec.mu must be held.
func (r *FlightRecorder) pruneLocked() {
	if len(r.files) <= maxFlights {
		return
	}
	drop := r.files[:len(r.files)-maxFlights]
	for _, f := range drop {
		base := strings.TrimSuffix(f, flightMetaSuffix)
		os.Remove(filepath.Join(r.dir, base+flightRequestBodySuffix))
		os.Remove(filepath.Join(r.dir, base+flightResponseBodySuffix))
		os.Remove(filepath.Join(r.dir, base+flightMetaSuffix))
	}
	r.files = r.files[len(r.files)-maxFlights:]
}

const flightReadme = `# Shelley LLM flight data recorder

Every LLM HTTP request (and its reply) made by this Shelley instance is
captured here as three timestamped files per request:

    <timestamp>_<shelley-request-id>.json             record metadata
    <timestamp>_<shelley-request-id>.request.body     byte-exact request body
    <timestamp>_<shelley-request-id>.response.body    response body as consumed

The <timestamp> is the UTC moment the request was issued (9-digit fractional
seconds); <shelley-request-id> is the Shelley-Request-Id header value that can
be correlated with service logs.

The .json metadata holds everything needed to reconstruct the exchange:
method, URL, and request headers; status code, protocol, and response headers
(including the provider's x-request-id as "upstream_request_id"); the error
string when the attempt failed (transport error, stall/abort, or early body
close); and timing. Response bodies that were decompressed by the HTTP layer
(e.g. gzip) are stored in their plaintext logical form and flagged with
"body_decompressed".

The recorder keeps only the most recent 100 requests; older records are
pruned automatically. Set the SHELLEY_FLIGHT_RECORDER_DIR environment
variable to relocate the directory.
`
