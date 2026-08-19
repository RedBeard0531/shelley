package server

import (
	"testing"

	"shelley.exe.dev/featureflags"
)

func TestFlagToolPillsRegistered(t *testing.T) {
	f, ok := featureflags.Lookup("tool-pills")
	if !ok {
		t.Fatal("tool-pills not registered")
	}
	if f.Default != false {
		t.Fatalf("default = %v, want false", f.Default)
	}
}

func TestFlagReflectionEmojiFaviconRegistered(t *testing.T) {
	f, ok := featureflags.Lookup("reflection-emoji-favicon")
	if !ok {
		t.Fatal("reflection-emoji-favicon not registered")
	}
	if f.Default != true {
		t.Fatalf("default = %v, want true", f.Default)
	}
}
