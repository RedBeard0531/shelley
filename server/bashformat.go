package server

import (
	"encoding/json"
	"strings"
	"unicode/utf8"

	"mvdan.cc/sh/v3/syntax"

	"shelley.exe.dev/llm"
)

// foldMaxBytes caps how much of the folded (single-line) command form we
// transmit. It overshoots the UI's display window so the UI can detect
// truncation for its "..." marker; the slack also absorbs some UTF-8/UTF-16
// drift (the UI truncates in UTF-16 code units). Keep in sync with
// SUMMARY_MAX_LEN in ui/src/vue/components/tools/BashTool.vue.
const foldMaxBytes = 450

// bashDisplayForms returns human-readable display forms of a bash command:
// expanded (pretty-printed, multi-line) and folded (single-line, truncated).
// ok is false when the command does not parse, in which case callers should
// display the raw text. This never runs the command.
func bashDisplayForms(cmd string) (expanded, folded string, ok bool) {
	p := syntax.NewParser(syntax.KeepComments(true))
	file, err := p.Parse(strings.NewReader(cmd), "")
	if err != nil {
		return "", "", false
	}
	var expBuf strings.Builder
	if err := syntax.NewPrinter(syntax.Indent(4), syntax.BinaryNextLine(true)).Print(&expBuf, file); err != nil {
		return "", "", false
	}
	expanded = strings.TrimSuffix(expBuf.String(), "\n")

	var foldBuf strings.Builder
	if err := syntax.NewPrinter(syntax.SingleLine(true)).Print(&foldBuf, file); err != nil {
		return "", "", false
	}
	return expanded, truncateFolded(strings.TrimSuffix(foldBuf.String(), "\n")), true
}

// truncateFolded cuts s at foldMaxBytes without tearing a UTF-8 codepoint,
// rounding up to include the character that straddles the limit.
func truncateFolded(s string) string {
	if len(s) <= foldMaxBytes {
		return s
	}
	n := foldMaxBytes
	for n < len(s) && !utf8.RuneStart(s[n]) {
		n++
	}
	return s[:n]
}

// addBashDisplayForms injects formattedCommand/foldedCommand entries into a
// bash-family tool_use input JSON so the UI can render human-formatted
// commands. The raw command key is never modified. Reports whether the input
// JSON changed.
func addBashDisplayForms(content *llm.Content) bool {
	if content.Type != llm.ContentTypeToolUse || content.ToolInput == nil {
		return false
	}
	switch content.ToolName {
	case "bash", "shell":
	default:
		return false
	}
	var input map[string]any
	if err := json.Unmarshal(content.ToolInput, &input); err != nil {
		return false
	}
	cmd, _ := input["command"].(string)
	if cmd == "" {
		return false
	}
	expanded, folded, ok := bashDisplayForms(cmd)
	if !ok {
		return false
	}
	if expanded != cmd {
		input["formattedCommand"] = expanded
	}
	// Compare within the truncated window: differences past it are invisible
	// to the UI, so they are not worth transmitting.
	if folded != truncateFolded(cmd) {
		input["foldedCommand"] = folded
	}
	out, err := json.Marshal(input)
	if err != nil {
		return false
	}
	content.ToolInput = out
	return true
}
