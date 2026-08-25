package llmhttp

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/iotest"
	"time"
)

// Both recorders must observe the same exchange without sharing redacted
// headers or imposing the ring's capture limits on the disk recorder.
func TestFlightAndRingCoexistence(t *testing.T) {
	for _, tc := range []struct {
		name      string
		idle      time.Duration
		truncated bool
	}{
		{"without-watchdog", 0, false},
		{"with-watchdog", DefaultIdleTimeout, false},
		{"capture-limit", DefaultIdleTimeout, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			ring := NewRing(10, 32<<20)
			wantURL := "https://user:url-password@example.invalid/v1?api_key=url-secret&beta=true"
			requestBody, responseBody := "request\x00payload\n", "response\x00payload\n"
			if tc.truncated {
				requestBody = strings.Repeat("q", maxBodyCapture) + requestBody
				responseBody = strings.Repeat("s", maxBodyCapture) + responseBody
			}
			requestHeaders := http.Header{
				"Authorization":  {"Bearer request-secret"},
				"X-Api-Key":      {"key-secret"},
				"X-Access-Token": {"token-secret"},
				"Cookie":         {"session=cookie-secret"},
				"Content-Type":   {"application/json"},
			}
			responseHeaders := http.Header{
				"Set-Cookie":                           {"session=response-secret"},
				"X-Request-Id":                         {"upstream-id"},
				"Content-Type":                         {"text/event-stream"},
				"Anthropic-Ratelimit-Tokens-Remaining": {"1000"},
			}
			client := NewClientWithIdleTimeout(&http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				got, err := io.ReadAll(req.Body)
				if err != nil || string(got) != requestBody || req.URL.String() != wantURL {
					t.Fatalf("outgoing request: body=%q URL=%s error=%v", got, req.URL, err)
				}
				for key := range requestHeaders {
					if req.Header.Get(key) != requestHeaders.Get(key) {
						t.Fatalf("outgoing %s was changed", key)
					}
				}
				return &http.Response{StatusCode: http.StatusOK, Header: responseHeaders.Clone(), Body: io.NopCloser(strings.NewReader(responseBody))}, nil
			})}, tc.idle)
			client.Transport.(*Transport).Log = ring
			if _, err := EnableFlightRecorder(client, dir); err != nil {
				t.Fatal(err)
			}
			if client.Transport.(*Transport).Log != ring {
				t.Fatal("enabling disk recording replaced the ring")
			}
			req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, wantURL, strings.NewReader(requestBody))
			if err != nil {
				t.Fatal(err)
			}
			req.Header = requestHeaders.Clone()
			resp, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			got, readErr := io.ReadAll(resp.Body)
			closeErr := resp.Body.Close()
			if readErr != nil || closeErr != nil || string(got) != responseBody {
				t.Fatalf("response=%q read=%v close=%v", got, readErr, closeErr)
			}
			files := metaFiles(t, dir)
			if len(files) != 1 || len(ring.List()) != 1 {
				t.Fatalf("disk=%v ring=%+v", files, ring.List())
			}
			meta := readMeta(t, dir, files[0])
			ex, ok := ring.Get(ring.List()[0].ID)
			if !ok || !ex.Done || ex.Error != "" || meta.Error != "" || meta.Response == nil || !meta.Response.BodyComplete {
				t.Fatalf("unfinished exchange: disk=%+v ring=%+v", meta, ex)
			}
			if meta.Request.URL != wantURL || ex.RequestID == "" || ex.RequestID != meta.ShelleyRequestID || meta.UpstreamRequestID != "upstream-id" || ex.Status != http.StatusOK || meta.Response.StatusCode != http.StatusOK {
				t.Fatalf("exchange identity: disk=%+v ring=%+v", meta, ex)
			}
			u, err := url.Parse(ex.URL)
			if err != nil || u.User != nil || u.Query().Get("api_key") != redacted || u.Query().Get("beta") != "true" {
				t.Fatalf("ring URL=%q error=%v", ex.URL, err)
			}
			for _, headers := range []struct {
				raw, disk, memory, caller http.Header
			}{
				{requestHeaders, http.Header(meta.Request.Headers), ex.RequestHeader, req.Header},
				{responseHeaders, http.Header(meta.Response.Headers), ex.ResponseHeader, resp.Header},
			} {
				for key := range headers.raw {
					want := headers.raw.Get(key)
					if headers.disk.Get(key) != want || headers.caller.Get(key) != want {
						t.Fatalf("%s: disk/caller lost the original value", key)
					}
					switch key {
					case "Authorization", "X-Api-Key", "X-Access-Token", "Cookie", "Set-Cookie":
						want = redacted
					}
					if headers.memory.Get(key) != want {
						t.Fatalf("ring %s=%q, want %q", key, headers.memory.Get(key), want)
					}
				}
			}
			wantRequest := requestBody[:min(len(requestBody), maxBodyCapture)]
			wantResponse := responseBody[:min(len(responseBody), maxBodyCapture)]
			if ex.RequestBody != wantRequest || ex.ResponseBody != wantResponse || ex.RequestBytes != len(wantRequest) || ex.ResponseBytes != len(wantResponse) || ex.RequestTruncated != tc.truncated || ex.ResponseTruncated != tc.truncated {
				t.Fatal("ring did not retain the expected bounded bodies")
			}
			if meta.Request.BodyBytes != len(requestBody) || meta.Response.BodyBytes != len(responseBody) {
				t.Fatal("ring capture limits changed the disk body lengths")
			}
			assertCoexistenceBody(t, dir, meta.Request.BodyFile, requestBody)
			assertCoexistenceBody(t, dir, meta.Response.BodyFile, responseBody)
		})
	}
}

func TestFlightAndRingConcurrentFailures(t *testing.T) {
	cases := []struct {
		name, diskError, ringError, body string
	}{
		{"transport", "transport boom", "transport boom", ""},
		{"early-close", "response body closed before EOF", "", "abc"},
		{"read-error", "read boom", "read boom", "abc"},
	}
	dir := t.TempDir()
	rec, err := NewFlightRecorder(dir)
	if err != nil {
		t.Fatal(err)
	}
	ring := NewRing(len(cases), 1<<20)
	started, release := make(chan struct{}, len(cases)), make(chan struct{})
	client := &http.Client{Transport: &Transport{Log: ring, FlightRecorder: rec, IdleTimeout: DefaultIdleTimeout, Base: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		started <- struct{}{}
		<-release // All exchanges are in flight before any can finish.
		if req.URL.Path == "/transport" {
			return nil, errors.New("transport boom")
		}
		var body io.Reader = strings.NewReader("abcdef")
		if req.URL.Path == "/read-error" {
			body = io.MultiReader(strings.NewReader("abc"), iotest.ErrReader(errors.New("read boom")))
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(body)}, nil
	})}}
	errs := make(chan error, len(cases))
	var workers sync.WaitGroup
	for i, tc := range cases {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "https://example.invalid/"+tc.name, strings.NewReader(tc.name))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set(shelleyRequestIDHeader, fmt.Sprintf("%016x", i+1))
		workers.Go(func() {
			resp, err := client.Do(req)
			if tc.name == "transport" {
				if err == nil || !strings.Contains(err.Error(), tc.diskError) {
					errs <- fmt.Errorf("%s: Do error=%v", tc.name, err)
				}
				return
			}
			if err != nil {
				errs <- err
				return
			}
			var got []byte
			if tc.name == "early-close" {
				got = make([]byte, 3)
				_, err = io.ReadFull(resp.Body, got)
			} else {
				got, err = io.ReadAll(resp.Body)
			}
			closeErr := resp.Body.Close()
			if string(got) != tc.body || closeErr != nil || (tc.name == "early-close" && err != nil) || (tc.name == "read-error" && (err == nil || err.Error() != tc.ringError)) {
				errs <- fmt.Errorf("%s: body=%q read=%v close=%v", tc.name, got, err, closeErr)
			}
		})
	}
	for range cases {
		<-started
	}
	close(release)
	workers.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	files, exchanges := metaFiles(t, dir), ring.List()
	if len(files) != len(cases) || len(exchanges) != len(cases) {
		t.Fatalf("disk=%v ring=%+v", files, exchanges)
	}
	for _, tc := range cases {
		var meta flightRecord
		var ex Exchange
		for _, file := range files {
			m := readMeta(t, dir, file)
			if strings.HasSuffix(m.Request.URL, "/"+tc.name) {
				meta = m
			}
		}
		for _, e := range exchanges {
			if e.RequestID == meta.ShelleyRequestID {
				ex, _ = ring.Get(e.ID)
			}
		}
		if meta.ShelleyRequestID == "" || !ex.Done || meta.Error != tc.diskError || ex.Error != tc.ringError || ex.RequestBody != tc.name || ex.ResponseBody != tc.body {
			t.Fatalf("%s: disk=%+v ring=%+v", tc.name, meta, ex)
		}
		assertCoexistenceBody(t, dir, meta.Request.BodyFile, tc.name)
		if tc.name == "transport" {
			if meta.Response != nil || ex.Status != 0 {
				t.Fatal("failed transport recorded a response")
			}
		} else {
			if meta.Response == nil || meta.Response.BodyComplete || meta.Response.BodyBytes != len(tc.body) {
				t.Fatalf("%s: unexpected completeness: %+v", tc.name, meta.Response)
			}
			assertCoexistenceBody(t, dir, meta.Response.BodyFile, tc.body)
		}
	}
}

func assertCoexistenceBody(t *testing.T, dir, file, want string) {
	t.Helper()
	got, err := os.ReadFile(filepath.Join(dir, file))
	if err != nil || !bytes.Equal(got, []byte(want)) {
		t.Fatalf("%s: body bytes=%d, want %d; error=%v", file, len(got), len(want), err)
	}
}
