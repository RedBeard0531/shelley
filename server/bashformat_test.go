package server

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"shelley.exe.dev/llm"
)

func formatInput(t *testing.T, toolName, cmd string) map[string]any {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"command": cmd})
	if err != nil {
		t.Fatal(err)
	}
	content := llm.Content{Type: llm.ContentTypeToolUse, ToolName: toolName, ToolInput: raw}
	if !addBashDisplayForms(&content) {
		t.Fatalf("addBashDisplayForms reported no change for %q", cmd)
	}
	var out map[string]any
	if err := json.Unmarshal(content.ToolInput, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestAddBashDisplayForms(t *testing.T) {
	// Semicolon chain: fold and expand both differ from raw.
	in := formatInput(t, "bash", "cd /tmp && rm -rf foo; echo hi | grep -i hi > out.txt && cat out.txt")
	if in["command"] != "cd /tmp && rm -rf foo; echo hi | grep -i hi > out.txt && cat out.txt" {
		t.Errorf("raw command was modified: %v", in["command"])
	}
	fold, _ := in["foldedCommand"].(string)
	expanded, _ := in["formattedCommand"].(string)
	if fold != "cd /tmp && rm -rf foo; echo hi | grep -i hi >out.txt && cat out.txt" {
		t.Errorf("unexpected fold: %q", fold)
	}
	if !strings.Contains(expanded, "\n") {
		t.Errorf("expected multi-line expansion, got %q", expanded)
	}
	if strings.Contains(expanded, ">out.txt &&") == strings.Contains(expanded, "> out.txt &&") {
		t.Errorf("expanded redirect spacing not SpaceRedirects(false)? %q", expanded)
	}
}

func TestAddBashDisplayFormsOmitsIdentical(t *testing.T) {
	// Already-canonical single-line command: both forms identical, both omitted.
	in := formatInput(t, "bash", "ls -la && echo done")
	if _, has := in["formattedCommand"]; has {
		t.Errorf("formattedCommand should be omitted when identical, got %v", in["formattedCommand"])
	}
	if _, has := in["foldedCommand"]; has {
		t.Errorf("foldedCommand should be omitted when identical, got %v", in["foldedCommand"])
	}
}

func TestAddBashDisplayFormsUnparseable(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{"command": "echo 'unterminated"})
	content := llm.Content{Type: llm.ContentTypeToolUse, ToolName: "bash", ToolInput: raw}
	if addBashDisplayForms(&content) {
		t.Error("expected no change for unparseable command")
	}
}

func TestAddBashDisplayFormsOtherTools(t *testing.T) {
	for _, name := range []string{"patch", "subagent", ""} {
		raw, _ := json.Marshal(map[string]any{"command": "a; b"})
		content := llm.Content{Type: llm.ContentTypeToolUse, ToolName: name, ToolInput: raw}
		if addBashDisplayForms(&content) {
			t.Errorf("expected no change for tool %q", name)
		}
	}
}

func TestTruncateFolded(t *testing.T) {
	long := strings.Repeat("a", 500)
	if got := truncateFolded(long); len(got) != foldMaxBytes {
		t.Errorf("ascii: got %d bytes, want %d", len(got), foldMaxBytes)
	}
	// Multibyte codepoint straddling the limit: round up to include it whole.
	emoji := strings.Repeat("a", 448) + "🙂🙂🙂" // 4 bytes each
	got := truncateFolded(emoji)
	if len(got) != 452 { // 448 + 4: rounded up, whole codepoint
		t.Errorf("astral: got %d bytes, want 452", len(got))
	}
	if !utf8.ValidString(got) {
		t.Error("truncated fold is not valid UTF-8")
	}
	// Strings at or under the limit pass through untouched.
	if got := truncateFolded("short"); got != "short" {
		t.Errorf("short: got %q", got)
	}
}

func TestFoldHeredocPreservesBody(t *testing.T) {
	cmd := "cat <<'EOF' > f\nline one\nline two\nEOF\necho done"
	in := formatInput(t, "bash", cmd)
	fold, _ := in["foldedCommand"].(string)
	if !strings.Contains(fold, "line one\nline two") {
		t.Errorf("heredoc body not preserved verbatim in fold: %q", fold)
	}
}
