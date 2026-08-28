package claudetool

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"shelley.exe.dev/llm"
)

// The hosted MCP server exposes exactly two anonymous tools (rate-limited,
// no API key required): web_search_exa and web_fetch_exa. These Shelley tools
// are a thin JSON-RPC client for that endpoint. The tool names, descriptions,
// and UI are deliberately vendor-neutral; only this implementation knows the
// backend is Exa.
const (
	exaMcpEndpoint = "https://mcp.exa.ai/mcp"
	webSearchName  = "WebSearch"
	webFetchName   = "WebFetch"
	exaHTTPTimeout = 60 * time.Second
)

// webSearchDescription is templated with the current year so the model anchors
// recency-sensitive queries correctly (the same directive opencode uses).
func webSearchDescription(year int) string {
	return fmt.Sprintf(`Search the web for any topic. Returns top results with title, URL, published date, author, and highlight snippets. Describe the ideal page, not keywords. Prefix 'category:people' or 'category:company' to focus on LinkedIn profiles / company pages.

The current year is %d. Use it when searching for recent information or current events.`, year)
}

const webSearchInputSchema = `{
  "type": "object",
  "required": ["query"],
  "additionalProperties": false,
  "properties": {
    "query": {
      "type": "string",
      "minLength": 1,
      "description": "Natural-language description of the ideal page (optionally prefixed with category:people or category:company)."
    },
    "numResults": {
      "type": "number",
      "description": "Results to return (default 10)."
    }
  }
}`

const webFetchDescription = `Read one or more web pages and return their content as clean markdown. Batch multiple URLs in one call.`

const webFetchInputSchema = `{
  "type": "object",
  "required": ["urls"],
  "additionalProperties": false,
  "properties": {
    "urls": {
      "type": "array",
      "items": { "type": "string" },
      "description": "URLs to read (batching supported)."
    },
    "maxCharacters": {
      "type": "number",
      "description": "Max characters per page (default 3000)."
    }
  }
}`

type webSearchInput struct {
	Query      string  `json:"query"`
	NumResults *uint64 `json:"numResults,omitempty"`
}

type webFetchInput struct {
	URLs          []string `json:"urls"`
	MaxCharacters *uint64  `json:"maxCharacters,omitempty"`
}

// WebSearchTool searches the web via the hosted MCP server. It is only
// registered for model services that have no native server-side web search.
type WebSearchTool struct {
	// Endpoint defaults to the hosted MCP server; overridable in tests.
	Endpoint string
	// Client defaults to a 60s client; overridable in tests.
	Client *http.Client
}

// WebFetchTool reads page content via the hosted MCP server. There is no
// native server-side fetch tool, so it is always registered.
type WebFetchTool struct {
	Endpoint string
	Client   *http.Client
}

// Tool returns an llm.Tool for the WebSearch client tool.
func (t *WebSearchTool) Tool() *llm.Tool {
	return &llm.Tool{
		Name:        webSearchName,
		Description: webSearchDescription(time.Now().Year()),
		InputSchema: llm.MustSchema(webSearchInputSchema),
		Run:         llm.RunJSON(t.run),
	}
}

// Tool returns an llm.Tool for the WebFetch client tool.
func (t *WebFetchTool) Tool() *llm.Tool {
	return &llm.Tool{
		Name:        webFetchName,
		Description: webFetchDescription,
		InputSchema: llm.MustSchema(webFetchInputSchema),
		Run:         llm.RunJSON(t.run),
	}
}

func (t *WebSearchTool) run(ctx context.Context, in webSearchInput) llm.ToolOut {
	if strings.TrimSpace(in.Query) == "" {
		return llm.ErrorfToolOut("query is required")
	}
	args := map[string]any{"query": strings.TrimSpace(in.Query)}
	if in.NumResults != nil && *in.NumResults > 0 {
		args["numResults"] = *in.NumResults
	}
	text, err := callMcpTool(ctx, t.client(), t.endpoint(), "web_search_exa", "web search", args)
	if err != nil {
		return llm.ErrorfToolOut("%v", err)
	}
	results := parseSearchResults(text)
	if len(results) == 0 {
		return llm.ToolOut{LLMContent: llm.TextContent("No search results found. Please try a different query.")}
	}
	blocks := make([]llm.Content, 0, len(results))
	for _, r := range results {
		// One block per result. Text is the FULL per-result text: providers
		// serialize only .Text of tool_result blocks to the model, so the
		// title/URL/date must live there; Title/URL/PageAge are UI-only
		// extras. (Per-block emission also changes the OpenAI/Gemini join
		// from the old "---" separators to plain "\n" — negligible.)
		blocks = append(blocks, llm.Content{
			Type:    llm.ContentTypeText,
			Text:    r.Full,
			Title:   r.Title,
			URL:     sanitizeURL(r.URL),
			PageAge: relativePublishedAge(r.Published), // published date, not crawl age (see comment)
		})
	}
	return llm.ToolOut{LLMContent: blocks}
}

func (t *WebFetchTool) run(ctx context.Context, in webFetchInput) llm.ToolOut {
	if len(in.URLs) == 0 {
		return llm.ErrorfToolOut("urls is required")
	}
	args := map[string]any{"urls": in.URLs}
	if in.MaxCharacters != nil && *in.MaxCharacters > 0 {
		args["maxCharacters"] = *in.MaxCharacters
	}
	text, err := callMcpTool(ctx, t.client(), t.endpoint(), "web_fetch_exa", "web fetch", args)
	if err != nil {
		return llm.ErrorfToolOut("%v", err)
	}
	pages := parseFetchedPages(text)
	if len(pages) == 0 {
		return llm.ToolOut{LLMContent: llm.TextContent("No content found.")}
	}
	blocks := make([]llm.Content, 0, len(pages))
	for _, p := range pages {
		blocks = append(blocks, llm.Content{
			Type:  llm.ContentTypeText,
			Text:  p.Full, // keeps the "# title / URL:" header so the model retains source attribution
			Title: p.Title,
			URL:   sanitizeURL(p.URL),
		})
	}
	return llm.ToolOut{LLMContent: blocks}
}

func (t *WebSearchTool) client() *http.Client {
	if t.Client != nil {
		return t.Client
	}
	return &http.Client{Timeout: exaHTTPTimeout}
}

func (t *WebSearchTool) endpoint() string {
	return exaEndpoint(t.Endpoint)
}

func (t *WebFetchTool) client() *http.Client {
	if t.Client != nil {
		return t.Client
	}
	return &http.Client{Timeout: exaHTTPTimeout}
}

func (t *WebFetchTool) endpoint() string {
	return exaEndpoint(t.Endpoint)
}

// exaEndpoint returns the MCP endpoint, appending an API key from the
// EXA_API_KEY environment variable when present (anonymous otherwise).
func exaEndpoint(override string) string {
	endpoint := override
	if endpoint == "" {
		endpoint = exaMcpEndpoint
	}
	key := strings.TrimSpace(os.Getenv("EXA_API_KEY"))
	if key == "" {
		return endpoint
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return endpoint
	}
	q := u.Query()
	q.Set("exaApiKey", key)
	u.RawQuery = q.Encode()
	return u.String()
}

// searchResult is one parsed result from the MCP search response text.
// Full preserves the original chunk verbatim (what the LLM sees); the other
// fields are UI-facing structure.
type searchResult struct {
	Full      string
	Title     string
	URL       string
	Published string
}

// parseSearchResults splits the server's deterministic per-result format:
//
//	Title: <title>
//	URL: <url>
//	Published: <ISO date or N/A>
//	Author: <author or N/A>
//	Highlights:
//	<highlight lines...>
//
// separated by blank lines + "---".
func parseSearchResults(text string) []searchResult {
	var out []searchResult
	for _, chunk := range strings.Split(text, "\n\n---\n\n") {
		chunk = strings.TrimSpace(chunk)
		if chunk == "" {
			continue
		}
		r := searchResult{Full: chunk}
		for _, ln := range strings.Split(chunk, "\n") {
			switch {
			case strings.HasPrefix(ln, "Title: "):
				r.Title = strings.TrimPrefix(ln, "Title: ")
			case strings.HasPrefix(ln, "URL: "):
				r.URL = strings.TrimPrefix(ln, "URL: ")
			case strings.HasPrefix(ln, "Published: "):
				r.Published = strings.TrimPrefix(ln, "Published: ")
			}
		}
		if r.Title == "N/A" {
			r.Title = "" // let the UI fall back to the URL
		}
		out = append(out, r)
	}
	return out
}

// fetchedPage is one page parsed from the MCP fetch response text. Full keeps
// the original chunk verbatim; Failed marks URLs the server could not load.
type fetchedPage struct {
	Full   string
	Title  string
	URL    string
	Failed bool
}

// parseFetchedPages splits the server's per-page format:
//
//	# <title>
//	URL: <url>
//	[Published: <date>]
//	[Author: <author>]
//	<blank>
//	<page content>
//
// Pages are separated by blank lines. A new page starts at a "# " line whose
// IMMEDIATELY following line (the server emits "# <title>\nURL: <url>"
// adjacently, with no blank between) is a valid http(s) "URL: " line; requiring
// adjacency keeps a "# heading\n\nURL: ..." pattern inside page text from
// being misread as a new page head. "Error fetching <url>: <tag>" lines
// become Failed pages. Full preserves each page's original chunk verbatim
// (what the LLM sees).
func parseFetchedPages(text string) []fetchedPage {
	var pages []fetchedPage
	lines := strings.Split(text, "\n")

	isPageHead := func(i int) bool {
		if i >= len(lines) || !strings.HasPrefix(lines[i], "# ") {
			return false
		}
		j := i + 1
		if j >= len(lines) || !strings.HasPrefix(lines[j], "URL: ") {
			return false
		}
		return sanitizeURL(strings.TrimPrefix(lines[j], "URL: ")) != ""
	}

	i := 0
	for i < len(lines) {
		ln := lines[i]
		if strings.HasPrefix(ln, "Error fetching ") {
			rest := strings.TrimPrefix(ln, "Error fetching ")
			u, _, _ := strings.Cut(rest, ": ")
			pages = append(pages, fetchedPage{Full: ln, URL: u, Failed: true})
			i++
			continue
		}
		if isPageHead(i) {
			start := i
			title := strings.TrimPrefix(lines[i], "# ")
			j := i + 1
			for strings.TrimSpace(lines[j]) == "" {
				j++
			}
			url := strings.TrimPrefix(lines[j], "URL: ")
			k := j + 1
			for k < len(lines) && (strings.HasPrefix(lines[k], "Published: ") || strings.HasPrefix(lines[k], "Author: ")) {
				k++
			}
			i = k
			for i < len(lines) && !isPageHead(i) && !strings.HasPrefix(lines[i], "Error fetching ") {
				i++
			}
			end := i
			pages = append(pages, fetchedPage{
				Full:  strings.TrimSpace(strings.Join(lines[start:end], "\n")),
				Title: title,
				URL:   url,
			})
			continue
		}
		i++
	}
	return pages
}

// sanitizeURL accepts only http/https URLs (for safe <a href> rendering).
func sanitizeURL(raw string) string {
	if raw == "" || raw == "N/A" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return ""
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return ""
	}
	return raw
}

// relativePublishedAge renders a published date as a compact relative age
// ("2 hours ago", "5 days ago"). Items older than ~30 days show a calendar
// date ("Published 2026-04-12") instead of a large relative age, which would
// misleadingly imply crawl recency. Note: this is the page's PUBLISHED date,
// not the search index's crawl age (Anthropic's PageAge is crawl age) — we
// deliberately show published age because that is what a user judging a
// source cares about. Empty for N/A or unparseable dates.
func relativePublishedAge(published string) string {
	if published == "" || published == "N/A" {
		return ""
	}
	t, err := time.Parse(time.RFC3339, published)
	if err != nil {
		if t2, err2 := time.Parse("2006-01-02", published); err2 == nil {
			t = t2
		} else {
			return ""
		}
	}
	age := time.Since(t)
	if age < 0 {
		age = 0
	}
	if age >= 30*24*time.Hour {
		return "Published " + t.Format("2006-01-02")
	}
	switch {
	case age < time.Minute:
		return "just now"
	case age < time.Hour:
		return fmt.Sprintf("%d minutes ago", int(age/time.Minute))
	case age < 24*time.Hour:
		return fmt.Sprintf("%d hours ago", int(age/time.Hour))
	default:
		return fmt.Sprintf("%d days ago", int(age/(24*time.Hour)))
	}
}

// jsonrpcRequest is the request envelope the MCP server expects.
type jsonrpcRequest struct {
	JSONRPC string         `json:"jsonrpc"`
	ID      int            `json:"id"`
	Method  string         `json:"method"`
	Params  map[string]any `json:"params"`
}

type jsonrpcToolContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type jsonrpcCallResult struct {
	Content []jsonrpcToolContent `json:"content"`
	IsError bool                 `json:"isError"`
}

type jsonrpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type jsonrpcResponse struct {
	Result *jsonrpcCallResult `json:"result"`
	Error  *jsonrpcError      `json:"error"`
}

// callMcpTool invokes an MCP tool via JSON-RPC over streamable HTTP and
// returns the first text content. label names the tool in error messages
// (e.g. "web search" vs "web fetch"). The server answers SSE-style events; a
// plain HTTP POST returns the whole body at once, so we scan "data: " lines.
func callMcpTool(ctx context.Context, client *http.Client, endpoint, tool, label string, args map[string]any) (string, error) {
	payload, err := json.Marshal(jsonrpcRequest{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "tools/call",
		Params:  map[string]any{"name": tool, "arguments": args},
	})
	if err != nil {
		return "", fmt.Errorf("%s: encode request: %w", label, err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return "", fmt.Errorf("%s: build request: %w", label, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("%s: request failed: %w", label, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return "", fmt.Errorf("%s: read response: %w", label, err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s: server returned HTTP %d", label, resp.StatusCode)
	}

	rpc := parseMcpResponse(body)
	if rpc == nil {
		return "", fmt.Errorf("%s: unexpected response from MCP server", label)
	}
	if rpc.Error != nil {
		return "", fmt.Errorf("%s: %s", label, rpc.Error.Message)
	}
	if rpc.Result == nil {
		return "", fmt.Errorf("%s: empty result (the server may require authentication; set EXA_API_KEY)", label)
	}
	text := ""
	for _, c := range rpc.Result.Content {
		if c.Type == "text" && c.Text != "" {
			text += c.Text + "\n"
		}
	}
	text = strings.TrimSpace(text)
	if rpc.Result.IsError {
		if text == "" {
			text = "MCP tool returned an error"
		}
		return "", fmt.Errorf("%s: %s", label, text)
	}
	return text, nil
}

// parseMcpResponse extracts the JSON-RPC object from either a plain JSON body
// or an SSE stream ("event: message" / "data: {...}" lines).
func parseMcpResponse(body []byte) *jsonrpcResponse {
	trimmed := strings.TrimSpace(string(body))
	var direct jsonrpcResponse
	if strings.HasPrefix(trimmed, "{") && json.Unmarshal([]byte(trimmed), &direct) == nil {
		return &direct
	}
	for _, line := range strings.Split(trimmed, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var rpc jsonrpcResponse
		if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &rpc) == nil {
			if rpc.Result != nil || rpc.Error != nil {
				return &rpc
			}
		}
	}
	return nil
}
