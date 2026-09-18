package server

import (
	"testing"

	"shelley.exe.dev/featureflags"
)

func TestRemovedFlagsNotRegistered(t *testing.T) {
	for _, name := range []string{"tool-pills", "reflection-emoji-favicon"} {
		if _, ok := featureflags.Lookup(name); ok {
			t.Errorf("removed flag %q is still registered", name)
		}
	}
}

func TestFlagCompactSendThresholdsRegisteredOff(t *testing.T) {
	f, ok := featureflags.Lookup("compact-send-thresholds")
	if !ok {
		t.Fatal("compact-send-thresholds not registered")
	}
	if f.Default != false {
		t.Fatalf("default = %v, want false", f.Default)
	}
}

func TestPatchStrategyFlagsRegisteredOff(t *testing.T) {
	for _, name := range []string{"patch-simple", "patch-openai-raw"} {
		flag, ok := featureflags.Lookup(name)
		if !ok {
			t.Fatalf("%s not registered", name)
		}
		if flag.Default != false {
			t.Fatalf("%s default = %v, want false", name, flag.Default)
		}
	}
}
