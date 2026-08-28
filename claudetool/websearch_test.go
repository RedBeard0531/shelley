package claudetool

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"shelley.exe.dev/llm"
)

func TestWebSearchDescriptionHasCurrentYear(t *testing.T) {
	desc := webSearchDescription(time.Now().Year())
	if !strings.Contains(desc, fmt.Sprintf("The current year is %d", time.Now().Year())) {
		t.Fatalf("description missing current year: %s", desc)
	}
	if strings.Contains(desc, "Exa") {
		t.Fatalf("description must be vendor neutral: %s", desc)
	}
}

// mcpTestServer returns a server that records the last tools/call request and
// answers with the given body (either plain JSON or SSE lines).
func mcpTestServer(t *testing.T, body string, status int) (*httptest.Server, *jsonrpcRequest) {
	t.Helper()
	var last jsonrpcRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req jsonrpcRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
		}
		last = req
		w.Header().Set("Content-Type", "text/event-stream")
		if status != http.StatusOK {
			w.WriteHeader(status)
			return
		}
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv, &last
}

const sseFrame = "event: message\ndata: %s\n"

func sse(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return fmt.Sprintf(sseFrame, string(b))
}

const sampleSearchText = `Title: Example Article
URL: https://example.com/article
Published: 2026-08-27T10:00:00.000Z
Author: Jane Doe
Highlights:
First highlight sentence.
Second highlight sentence.

---

Title: N/A
URL: https://example.org/other
Published: N/A
Author: N/A
Highlights:
Only highlight.`

func TestWebSearchRunStructuredBlocks(t *testing.T) {
	rpc := map[string]any{
		"jsonrpc": "2.0", "id": 1,
		"result": map[string]any{"content": []map[string]any{{"type": "text", "text": sampleSearchText}}},
	}
	srv, _ := mcpTestServer(t, sse(t, rpc), http.StatusOK)
	tool := &WebSearchTool{Endpoint: srv.URL, Client: srv.Client()}
	out := tool.run(context.Background(), webSearchInput{Query: "test query"})
	if out.Error != nil {
		t.Fatalf("unexpected error: %v", out.Error)
	}
	if len(out.LLMContent) != 2 {
		t.Fatalf("want 2 result blocks, got %d", len(out.LLMContent))
	}
	first := out.LLMContent[0]
	if first.Title != "Example Article" || first.URL != "https://example.com/article" {
		t.Errorf("structured fields wrong: %q %q", first.Title, first.URL)
	}
	if !strings.Contains(first.Text, "URL: https://example.com/article") {
		t.Errorf("LLM-visible Text must contain the full result incl. URL header: %q", first.Text)
	}
	second := out.LLMContent[1]
	if second.Title != "" || second.URL != "https://example.org/other" || second.PageAge != "" {
		t.Errorf("second block fields wrong: %q %q %q", second.Title, second.URL, second.PageAge)
	}
}

func TestWebSearchArgsAndToolName(t *testing.T) {
	srv, last := mcpTestServer(t, sse(t, map[string]any{
		"jsonrpc": "2.0", "id": 1,
		"result": map[string]any{"content": []map[string]any{{"type": "text", "text": sampleSearchText}}},
	}), http.StatusOK)

	tool := &WebSearchTool{Endpoint: srv.URL, Client: srv.Client()}

	n := uint64(3)
	out := tool.run(context.Background(), webSearchInput{Query: "  spaced query  ", NumResults: &n})
	if out.Error != nil {
		t.Fatalf("unexpected error: %v", out.Error)
	}
	args, ok := last.Params["arguments"].(map[string]any)
	if !ok {
		t.Fatalf("no arguments in request: %#v", last.Params)
	}
	if args["query"] != "spaced query" {
		t.Errorf("query = %#v, want trimmed", args["query"])
	}
	if args["numResults"] != float64(3) {
		t.Errorf("numResults = %#v, want 3", args["numResults"])
	}
	if last.Params["name"] != "web_search_exa" {
		t.Errorf("backend tool name = %#v", last.Params["name"])
	}

	// Untouched numResults must be omitted so the server keeps its default.
	out = tool.run(context.Background(), webSearchInput{Query: "q"})
	if out.Error != nil {
		t.Fatalf("unexpected error: %v", out.Error)
	}
	args = last.Params["arguments"].(map[string]any)
	if _, present := args["numResults"]; present {
		t.Errorf("numResults present when zero: %#v", args)
	}
}

func TestWebSearchEmptyQuery(t *testing.T) {
	tool := &WebSearchTool{}
	out := tool.run(context.Background(), webSearchInput{Query: "  "})
	if out.Error == nil {
		t.Fatal("expected error for empty query")
	}
}

func TestWebSearchEmptyResults(t *testing.T) {
	srv, _ := mcpTestServer(t, sse(t, map[string]any{
		"jsonrpc": "2.0", "id": 1,
		"result": map[string]any{"content": []map[string]any{{"type": "text", "text": ""}}},
	}), http.StatusOK)
	tool := &WebSearchTool{Endpoint: srv.URL, Client: srv.Client()}
	out := tool.run(context.Background(), webSearchInput{Query: "q"})
	if out.Error != nil {
		t.Fatalf("unexpected error: %v", out.Error)
	}
	if text := llmText(out); !strings.Contains(text, "No search results") {
		t.Fatalf("want no-results message, got %q", text)
	}
}

func TestWebSearchJsonRpcError(t *testing.T) {
	body := sse(t, map[string]any{
		"jsonrpc": "2.0", "id": 1,
		"error": map[string]any{"code": -32000, "message": "Rate limited. Create an API key at https://dashboard.exa.ai/api-keys"},
	})
	srv, _ := mcpTestServer(t, body, http.StatusOK)
	tool := &WebSearchTool{Endpoint: srv.URL, Client: srv.Client()}
	out := tool.run(context.Background(), webSearchInput{Query: "q"})
	if out.Error == nil || !strings.Contains(out.Error.Error(), "Rate limited") {
		t.Fatalf("want rate-limit error, got: %v", out.Error)
	}
}

func TestWebSearchIsErrorResult(t *testing.T) {
	body := sse(t, map[string]any{
		"jsonrpc": "2.0", "id": 1,
		"result": map[string]any{
			"isError": true,
			"content": []map[string]any{{"type": "text", "text": "web_search_exa error (401): Invalid API key"}},
		},
	})
	srv, _ := mcpTestServer(t, body, http.StatusOK)
	tool := &WebSearchTool{Endpoint: srv.URL, Client: srv.Client()}
	out := tool.run(context.Background(), webSearchInput{Query: "q"})
	if out.Error == nil || !strings.Contains(out.Error.Error(), "401") {
		t.Fatalf("want 401 error, got: %v", out.Error)
	}
}

func TestWebSearchPlainJSONResponse(t *testing.T) {
	plain := `{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"Title: T\nURL: https://example.com\n"}]}}`
	srv, _ := mcpTestServer(t, plain, http.StatusOK)
	tool := &WebSearchTool{Endpoint: srv.URL, Client: srv.Client()}
	out := tool.run(context.Background(), webSearchInput{Query: "q"})
	if out.Error != nil {
		t.Fatalf("unexpected error: %v", out.Error)
	}
	if text := llmText(out); !strings.Contains(text, "https://example.com") {
		t.Fatalf("missing text: %q", text)
	}
}

func TestWebSearchHTTPError(t *testing.T) {
	srv, _ := mcpTestServer(t, "boom", http.StatusTooManyRequests)
	tool := &WebSearchTool{Endpoint: srv.URL, Client: srv.Client()}
	out := tool.run(context.Background(), webSearchInput{Query: "q"})
	if out.Error == nil || !strings.Contains(out.Error.Error(), "429") {
		t.Fatalf("want 429 error, got: %v", out.Error)
	}
}

const sampleFetchText = `# Example Domain
URL: https://example.com

This domain is for use in documentation examples.

# Second Page
URL: https://example.org
Published: 2026-08-01
Author: A. N. Author

Body with a # heading that is not a new page.

Error fetching https://broken.example: 403 Forbidden`

func TestWebFetchRun(t *testing.T) {
	rpc := map[string]any{
		"jsonrpc": "2.0", "id": 1,
		"result": map[string]any{"content": []map[string]any{{"type": "text", "text": sampleFetchText}}},
	}
	srv, last := mcpTestServer(t, sse(t, rpc), http.StatusOK)
	tool := &WebFetchTool{Endpoint: srv.URL, Client: srv.Client()}

	mc := uint64(500)
	out := tool.run(context.Background(), webFetchInput{URLs: []string{"https://example.com", "https://example.org"}, MaxCharacters: &mc})
	if out.Error != nil {
		t.Fatalf("unexpected error: %v", out.Error)
	}
	if len(out.LLMContent) != 3 {
		t.Fatalf("want 2 pages + 1 failed page, got %d", len(out.LLMContent))
	}
	if first := out.LLMContent[0]; first.Title != "Example Domain" || first.URL != "https://example.com" {
		t.Errorf("first page wrong: %q %q", first.Title, first.URL)
	}
	second := out.LLMContent[1]
	if second.Title != "Second Page" || second.URL != "https://example.org" {
		t.Errorf("second page wrong: %q %q", second.Title, second.URL)
	}
	if !strings.Contains(second.Text, "Published: 2026-08-01") {
		t.Errorf("LLM-visible Text must keep per-page meta headers: %q", second.Text)
	}
	if failed := out.LLMContent[2]; failed.Title != "" || failed.URL != "https://broken.example" || !strings.Contains(failed.Text, "Error fetching") {
		t.Errorf("failed page wrong: %q %q", failed.Title, failed.URL)
	}
	args := last.Params["arguments"].(map[string]any)
	if last.Params["name"] != "web_fetch_exa" {
		t.Errorf("backend tool name = %#v", last.Params["name"])
	}
	if args["maxCharacters"] != float64(500) {
		t.Errorf("maxCharacters = %#v", args["maxCharacters"])
	}
}

// TestParseFetchedPagesHeadingFalsePositive guards against misreading a
// "# heading" inside a page body as a new page head. The server emits page
// heads as "# <title>" immediately followed by "URL: <url>" (no blank line),
// so a heading that is NOT immediately followed by a URL line — or is
// separated from its URL by a blank line — must not start a new page.
func TestParseFetchedPagesHeadingFalsePositive(t *testing.T) {
	text := `# Real Page
URL: https://example.com

Intro text with a heading:

# Body Heading

URL: https://not-a-new-page.example

More content.`
	pages := parseFetchedPages(text)
	if len(pages) != 1 {
		t.Fatalf("want 1 page, got %d: %#v", len(pages), pages)
	}
	if pages[0].URL != "https://example.com" {
		t.Errorf("page URL = %q", pages[0].URL)
	}
}

func TestParseSearchResultsTitleNA(t *testing.T) {
	results := parseSearchResults(sampleSearchText)
	if r1 := results[1]; r1.Title != "" {
		t.Errorf("N/A title should become empty for UI fallback, got %q", r1.Title)
	}
}

func TestWebFetchNoURLs(t *testing.T) {
	tool := &WebFetchTool{}
	out := tool.run(context.Background(), webFetchInput{})
	if out.Error == nil {
		t.Fatal("expected error for empty urls")
	}
}

func TestEndpointAPIKeyEnv(t *testing.T) {
	t.Setenv("EXA_API_KEY", "secret-key")
	endpoint := exaEndpoint("")
	if !strings.Contains(endpoint, "exaApiKey=secret-key") {
		t.Fatalf("endpoint missing key param: %s", endpoint)
	}
	if !strings.HasPrefix(endpoint, "https://mcp.exa.ai/mcp") {
		t.Fatalf("unexpected endpoint: %s", endpoint)
	}

	t.Setenv("EXA_API_KEY", "")
	if got := exaEndpoint(""); got != "https://mcp.exa.ai/mcp" {
		t.Fatalf("anonymous endpoint changed: %s", got)
	}
	if got := exaEndpoint("http://localhost:9999/mcp"); got != "http://localhost:9999/mcp" {
		t.Fatalf("override endpoint changed: %s", got)
	}
}

func TestParseSearchResults(t *testing.T) {
	results := parseSearchResults(sampleSearchText)
	if len(results) != 2 {
		t.Fatalf("want 2 results, got %d", len(results))
	}
	r0 := results[0]
	if r0.Title != "Example Article" || r0.URL != "https://example.com/article" || r0.Published != "2026-08-27T10:00:00.000Z" {
		t.Errorf("result 0 fields wrong: %#v", r0)
	}
	if r1 := results[1]; r1.Title != "" || r1.Published != "N/A" {
		t.Errorf("result 1 fields wrong (N/A title should be empty for the UI fallback): %#v", r1)
	}
	if got := parseSearchResults(""); len(got) != 0 {
		t.Errorf("empty input should yield no results, got %d", len(got))
	}
}

func TestRelativePublishedAge(t *testing.T) {
	now := time.Now().UTC()
	cases := []struct {
		in   string
		want string
	}{
		{now.Format(time.RFC3339), "just now"},
		{now.Add(-2 * time.Hour).Format(time.RFC3339), "2 hours ago"},
		{now.Add(-3 * 24 * time.Hour).Format(time.RFC3339), "3 days ago"},
		{now.Add(-40 * 24 * time.Hour).Format("2006-01-02"), "Published " + now.Add(-40*24*time.Hour).Format("2006-01-02")},
		{now.Add(time.Hour).Format(time.RFC3339), "just now"}, // future clock skew
		{"N/A", ""},
		{"not-a-date", ""},
		{"", ""},
	}
	for _, c := range cases {
		if got := relativePublishedAge(c.in); got != c.want {
			t.Errorf("relativePublishedAge(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestSanitizeURL(t *testing.T) {
	cases := []struct{ in, want string }{
		{"https://example.com/a?b=c", "https://example.com/a?b=c"},
		{"http://example.com", "http://example.com"},
		{"javascript:alert(1)", ""},
		{"ftp://example.com/x", ""},
		{"https://", ""},
		{"", ""},
		{"N/A", ""},
	}
	for _, c := range cases {
		if got := sanitizeURL(c.in); got != c.want {
			t.Errorf("sanitizeURL(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestWebSearchRegistrationGating verifies the vendor-neutral web_search tool
// is registered for services WITHOUT native server-side search, and skipped
// when the service has it (where the native tool takes over). web_fetch is
// always present.
func TestWebSearchRegistrationGating(t *testing.T) {
	names := func(ts *ToolSet) map[string]bool {
		m := map[string]bool{}
		for _, tool := range ts.Tools() {
			if tool.Name == "web_search" || tool.Name == "web_fetch" {
				m[tool.Name+fmt.Sprintf("(%v)", tool.ServerSide)] = true
			}
		}
		return m
	}

	// Non-capable OpenAI chat-completions service -> both client tools.
	ts := NewToolSet(context.Background(), ToolSetConfig{
		LLMProvider: &plainOpenAIProvider{}, ModelID: "openai-chat", WorkingDir: "/test",
	})
	got := names(ts)
	if !got["web_search(false)"] || !got["web_fetch(false)"] {
		t.Errorf("non-capable service missing client tools: %#v", got)
	}

	// Non-capable anthropic-protocol service -> both client tools.
	ts = NewToolSet(context.Background(), ToolSetConfig{
		LLMProvider: &plainAnthropicProvider{}, ModelID: "third-party", WorkingDir: "/test",
	})
	got = names(ts)
	if !got["web_search(false)"] || !got["web_fetch(false)"] {
		t.Errorf("non-capable anthropic service missing client tools: %#v", got)
	}

	// Capable service -> no client web_search (native present), fetch still there.
	capable := &mockLLMProviderWithProviders{providers: map[string]string{"claude-sonnet-4.5": "anthropic"}}
	ts = NewToolSet(context.Background(), ToolSetConfig{
		LLMProvider: capable, ModelID: "claude-sonnet-4.5", WorkingDir: "/test",
	})
	got = names(ts)
	if got["web_search(false)"] {
		t.Errorf("capable service should not get client web_search: %#v", got)
	}
	if !got["web_fetch(false)"] {
		t.Errorf("capable service missing web_fetch: %#v", got)
	}

	// Unknown model (GetService error) -> client tools (search still better than none).
	ts = NewToolSet(context.Background(), ToolSetConfig{
		LLMProvider: capable, ModelID: "unknown-model", WorkingDir: "/test",
	})
	got = names(ts)
	if !got["web_search(false)"] || !got["web_fetch(false)"] {
		t.Errorf("unknown model missing client tools: %#v", got)
	}

	// Nil provider -> client tools.
	ts = NewToolSet(context.Background(), ToolSetConfig{WorkingDir: "/test"})
	got = names(ts)
	if !got["web_search(false)"] || !got["web_fetch(false)"] {
		t.Errorf("nil provider missing client tools: %#v", got)
	}
}

// llmText extracts the tool output text for assertions.
func llmText(out llm.ToolOut) string {
	var b strings.Builder
	for _, c := range out.LLMContent {
		if c.Type == llm.ContentTypeText {
			b.WriteString(c.Text)
		}
	}
	return b.String()
}
