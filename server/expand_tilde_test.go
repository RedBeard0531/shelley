package server

import (
	"os"
	"path/filepath"
	"testing"
)

// expandTilde resolves a leading ~ to the user's home directory so the web
// editor can open home-relative paths from file references.
func TestExpandTilde(t *testing.T) {
	t.Parallel()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}
	for _, tc := range []struct{ in, want string }{
		{"~/foo/bar", filepath.Join(home, "foo/bar")},
		{"~", home},
		{"~/.config/shelley/AGENTS.md", filepath.Join(home, ".config/shelley/AGENTS.md")},
		{"/abs/path", "/abs/path"},
		{"relative/path", "relative/path"},
		{"~user/x", "~user/x"}, // ~user is not expanded
		{"a~b/c", "a~b/c"},     // mid-path tilde is not expanded
	} {
		if got := expandTilde(tc.in); got != tc.want {
			t.Errorf("expandTilde(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
