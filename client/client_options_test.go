package client

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
)

func stringPointer(value string) *string { return &value }

func TestValidateReasoningLevel(t *testing.T) {
	for _, level := range append([]string{""}, reasoningLevels...) {
		if err := validateReasoningLevel(level); err != nil {
			t.Errorf("validateReasoningLevel(%q): %v", level, err)
		}
	}
	if err := validateReasoningLevel("bogus"); err == nil || !strings.Contains(err.Error(), "off, minimal, low, medium, high, xhigh, max") {
		t.Fatalf("bogus level error = %v", err)
	}
}

func TestToolOverridesFlag(t *testing.T) {
	var overrides toolOverridesFlag
	if err := overrides.Set(" bash = off "); err != nil {
		t.Fatal(err)
	}
	if err := overrides.Set("browser=on"); err != nil {
		t.Fatal(err)
	}
	want := toolOverridesFlag{"bash": "off", "browser": "on"}
	if !reflect.DeepEqual(overrides, want) {
		t.Fatalf("overrides = %v, want %v", overrides, want)
	}
	for _, invalid := range []string{"bash", "=off", "bash=maybe"} {
		if err := overrides.Set(invalid); err == nil {
			t.Errorf("Set(%q) unexpectedly succeeded", invalid)
		}
	}
}

func TestBuildConversationOptions(t *testing.T) {
	if options, err := buildConversationOptions("", nil, false, false); err != nil || options != nil {
		t.Fatalf("empty options = %#v, %v; want nil, nil", options, err)
	}
	options, err := buildConversationOptions("high", map[string]string{"bash": "off"}, true, true)
	if err != nil {
		t.Fatal(err)
	}
	got, err := json.Marshal(options)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"tool_overrides":{"bash":"off"},"disable_all_tools":true,"thinking_level":"high","disable_notifications":true}`
	if string(got) != want {
		t.Fatalf("options JSON = %s, want %s", got, want)
	}
}

func TestValidateChatTarget(t *testing.T) {
	options := &conversationOptionsWire{ThinkingLevel: "high"}
	if err := validateChatTarget("", options); err != nil {
		t.Fatal(err)
	}
	if err := validateChatTarget("existing", nil); err != nil {
		t.Fatal(err)
	}
	if err := validateChatTarget("existing", options); err == nil || !strings.Contains(err.Error(), "only apply to new conversations") {
		t.Fatalf("existing conversation error = %v", err)
	}
}

func TestHTTPResponseErrorIncludesPlainText(t *testing.T) {
	recorder := httptest.NewRecorder()
	recorder.WriteHeader(http.StatusBadRequest)
	recorder.WriteString("Model example does not support reasoning level max.\n")
	resp := recorder.Result()
	defer resp.Body.Close()
	got := httpResponseError(resp).Error()
	if !strings.Contains(got, "HTTP 400") || !strings.Contains(got, "does not support reasoning level max") {
		t.Fatalf("error = %q", got)
	}
}

func TestChatTagFailureStillPrintsConversationID(t *testing.T) {
	if os.Getenv("SHELLEY_CHAT_TAG_FAILURE_HELPER") == "1" {
		Run([]string{"-url", os.Getenv("SHELLEY_CHAT_TAG_FAILURE_URL"), "chat", "-p", "hello", "-tag", "port-smoke"})
		t.Fatal("chat unexpectedly succeeded")
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/conversations/new", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]any{"conversation_id": "conv-created"})
	})
	mux.HandleFunc("POST /api/conversation/conv-created/tags", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "tag write failed", http.StatusInternalServerError)
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	cmd := exec.Command(os.Args[0], "-test.run=^TestChatTagFailureStillPrintsConversationID$")
	cmd.Env = append(
		os.Environ(),
		"SHELLEY_CHAT_TAG_FAILURE_HELPER=1",
		"SHELLEY_CHAT_TAG_FAILURE_URL="+server.URL,
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
		t.Fatalf("chat exit error = %v, stderr=%q", err, stderr.String())
	}

	decoder := json.NewDecoder(&stdout)
	var output map[string]any
	if err := decoder.Decode(&output); err != nil {
		t.Fatalf("decode stdout %q: %v", stdout.String(), err)
	}
	if output["conversation_id"] != "conv-created" {
		t.Fatalf("stdout = %v, want retained conversation ID", output)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		t.Fatalf("stdout contains duplicate output: %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "Error adding tags") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestFullMessage(t *testing.T) {
	llmData := `{"Role":1,"Content":[{"Type":3,"Thinking":"plan"},{"Type":5,"ToolName":"bash","ToolInput":{"command":"ls"}},{"Type":6,"ToolUseID":"call-1","ToolResult":[{"Type":2,"Text":"ok"}]}],"EndOfTurn":true,"ExcludedFromContext":true}`
	usageData := `{"input_tokens":10,"output_tokens":4,"model":"provider-model"}`
	otherUsageData := `[{"purpose":"slug","input_tokens":2,"model":"slug-model"}]`
	conversation, err := decodeRawJSON(json.RawMessage(`{"conversation_id":"conv","model":"configured-model","created_at":"2026-09-18T10:00:00Z"}`))
	if err != nil {
		t.Fatal(err)
	}
	record, err := fullMessage(messageWire{
		MessageID:      "msg",
		ConversationID: "conv",
		SequenceID:     7,
		Type:           "agent",
		LlmData:        &llmData,
		UsageData:      &usageData,
		OtherUsageData: &otherUsageData,
		CreatedAt:      "2026-09-18T10:01:00Z",
		Generation:     2,
		ModelName:      stringPointer("provider-model"),
	}, conversation)
	if err != nil {
		t.Fatal(err)
	}
	if record.Role != "assistant" || !record.EndOfTurn || record.Model != "provider-model" || !record.ExcludedFromContext {
		t.Fatalf("record metadata = %+v", record)
	}
	content, ok := record.Content.([]any)
	if !ok || len(content) != 3 {
		t.Fatalf("content = %#v", record.Content)
	}
	if got := content[0].(map[string]any)["type"]; got != "thinking" {
		t.Errorf("thinking type = %v", got)
	}
	if got := content[1].(map[string]any)["type"]; got != "tool_use" {
		t.Errorf("tool-use type = %v", got)
	}
	toolResult := content[2].(map[string]any)
	if got := toolResult["type"]; got != "tool_result" {
		t.Errorf("tool-result type = %v", got)
	}
	nested := toolResult["ToolResult"].([]any)[0].(map[string]any)
	if nested["type"] != "text" || nested["Text"] != "ok" {
		t.Errorf("nested tool result = %#v", nested)
	}
	if _, ok := record.UsageData.(map[string]any); !ok {
		t.Fatalf("usage_data was not decoded: %#v", record.UsageData)
	}
	if _, ok := record.Conversation.(map[string]any); !ok {
		t.Fatalf("conversation was not decoded: %#v", record.Conversation)
	}
}

func TestAggregateMessageUsage(t *testing.T) {
	direct := `{"input_tokens":10,"cache_creation_input_tokens":2,"cache_read_input_tokens":3,"output_tokens":4,"model":"model-a"}`
	indirect := `[{"purpose":"slug","input_tokens":5,"cache_read_input_tokens":1,"output_tokens":2,"model":"model-b"},{"purpose":"unknown","output_tokens":1}]`
	copied := "source-message"
	summary := conversationUsage{PerModel: make(map[string]*usageBreakdown)}
	messages := []messageWire{
		{SequenceID: 1, Type: "agent", UsageData: &direct, ModelName: stringPointer("model-a")},
		{SequenceID: 2, Type: "user", OtherUsageData: &indirect},
		{SequenceID: 3, Type: "agent", UsageData: &direct, ForkedFromMessageID: &copied},
	}
	if err := aggregateMessageUsage(&summary, messages, "configured"); err != nil {
		t.Fatal(err)
	}
	want := usageBreakdown{
		InputTokens:              17,
		CachedInputTokens:        4,
		OutputTokens:             7,
		LLMCalls:                 3,
		RawInputTokens:           15,
		CacheCreationInputTokens: 2,
		CacheReadInputTokens:     4,
	}
	if !reflect.DeepEqual(summary.usageBreakdown, want) {
		t.Fatalf("usage = %+v, want %+v", summary.usageBreakdown, want)
	}
	if summary.PerModel["model-a"].LLMCalls != 1 || summary.PerModel["model-b"].LLMCalls != 1 || summary.PerModel[unknownModel].LLMCalls != 1 {
		t.Fatalf("per_model = %+v", summary.PerModel)
	}
}

func TestSimplifyMessageOutputUnchanged(t *testing.T) {
	endOfTurn := true
	llmData := `{"Role":1,"Content":[{"Type":3,"Thinking":"hidden"},{"Type":2,"Text":"answer"},{"Type":5,"ToolName":"bash"}],"EndOfTurn":true}`
	got := simplifyMessage(messageWire{
		MessageID:      "ignored",
		ConversationID: "ignored",
		SequenceID:     9,
		Type:           "agent",
		LlmData:        &llmData,
		EndOfTurn:      &endOfTurn,
		UsageData:      stringPointer(`{"input_tokens":99}`),
	})
	want := streamEvent{SequenceID: 9, Type: "agent", Text: "answer", ToolName: "bash", EndOfTurn: true}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("simplifyMessage = %+v, want %+v", got, want)
	}
}

func TestCollectConversationUsageIncludesDescendants(t *testing.T) {
	usageByID := map[string]string{
		"root":  `{"input_tokens":1,"model":"root-model"}`,
		"child": `{"input_tokens":2,"model":"child-model"}`,
		"grand": `{"input_tokens":3,"model":"grand-model"}`,
	}
	children := map[string][]string{
		"root":  {"child"},
		"child": {"grand"},
		"grand": {},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/conversation/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		usage, ok := usageByID[id]
		if !ok {
			http.NotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"conversation": map[string]any{"conversation_id": id, "model": id + "-configured"},
			"messages": []map[string]any{{
				"message_id": id + "-message", "conversation_id": id, "sequence_id": 1,
				"type": "agent", "usage_data": usage, "model_name": id + "-model",
			}},
		})
	})
	mux.HandleFunc("GET /api/conversation/{id}/subagents", func(w http.ResponseWriter, r *http.Request) {
		rows := make([]map[string]string, 0, len(children[r.PathValue("id")]))
		for _, id := range children[r.PathValue("id")] {
			rows = append(rows, map[string]string{"conversation_id": id})
		}
		json.NewEncoder(w).Encode(rows)
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	cc := &clientConfig{serverURL: server.URL}
	httpClient, baseURL, err := cc.newHTTPClient()
	if err != nil {
		t.Fatal(err)
	}
	summary, err := collectConversationUsage(cc, httpClient, baseURL, "root")
	if err != nil {
		t.Fatal(err)
	}
	if summary.ConversationCount != 3 || summary.SubagentCount != 2 || summary.LLMCalls != 3 || summary.InputTokens != 6 {
		t.Fatalf("summary = %+v", summary)
	}
	for _, model := range []string{"root-model", "child-model", "grand-model"} {
		if summary.PerModel[model] == nil {
			t.Errorf("per_model missing %q: %+v", model, summary.PerModel)
		}
	}
}
