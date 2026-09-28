// Package claudetool provides tools for Claude AI models.
//
// When adding, removing, or modifying tools in this package,
// remember to update the tool display template in termui/termui.go
// to ensure proper tool output formatting.
package claudetool

import (
	"bytes"
	"context"

	"shelley.exe.dev/llm"
)

// lastLines returns the final n lines of b, trailing newlines included, or all
// of b if it holds fewer than n lines. A final line without a newline counts
// as a line of its own. n <= 0 keeps everything.
func lastLines(b []byte, n int) []byte {
	// The newline terminating the last line does not begin one, so the
	// search for line starts begins before it.
	end := len(b)
	if end > 0 && b[end-1] == '\n' {
		end--
	}
	for i := end; n > 0; n-- {
		j := bytes.LastIndexByte(b[:i], '\n')
		if j < 0 {
			return b // b holds fewer than n lines
		}
		if n == 1 {
			return b[j+1:]
		}
		i = j
	}
	return b
}

// lastLinesString is lastLines for a string.
func lastLinesString(s string, n int) string {
	return string(lastLines([]byte(s), n))
}

func WithWorkingDir(ctx context.Context, wd string) context.Context {
	return llm.WithWorkingDir(ctx, wd)
}

func WorkingDir(ctx context.Context) string {
	return llm.WorkingDir(ctx)
}

func WithSessionID(ctx context.Context, sessionID string) context.Context {
	return llm.WithSessionID(ctx, sessionID)
}

func SessionID(ctx context.Context) string {
	return llm.SessionID(ctx)
}

// WithToolProgress returns a context with the given ToolProgressFunc.
func WithToolProgress(ctx context.Context, fn llm.ToolProgressFunc) context.Context {
	return llm.WithToolProgress(ctx, fn)
}

// GetToolProgress retrieves the ToolProgressFunc from the context, or nil.
func GetToolProgress(ctx context.Context) llm.ToolProgressFunc {
	return llm.GetToolProgress(ctx)
}

// WithToolUseID returns a context with the given tool use ID.
func WithToolUseID(ctx context.Context, id string) context.Context {
	return llm.WithToolUseID(ctx, id)
}

func ToolUseID(ctx context.Context) string {
	return llm.ToolUseID(ctx)
}
