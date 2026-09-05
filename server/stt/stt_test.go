package stt

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPCMToFloat32(t *testing.T) {
	cases := []struct {
		name string
		pcm  []byte
		want []float32
	}{
		{"silence", []byte{0, 0}, []float32{0}},
		{"positive max", []byte{0xff, 0x7f}, []float32{0.9999695}},
		{"negative max", []byte{0x00, 0x80}, []float32{-1.0}},
		{"multi-byte LE", []byte{0xcd, 0x00, 0x33, 0x01}, []float32{0.0062561, 0.0093689}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := pcmToFloat32(tc.pcm)
			if len(got) != len(tc.want) {
				t.Fatalf("len = %d, want %d", len(got), len(tc.want))
			}
			for i := range got {
				if diff := got[i] - tc.want[i]; diff > 1e-4 || diff < -1e-4 {
					t.Errorf("sample %d = %f, want ~%f", i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestFindModelFiles(t *testing.T) {
	dir := t.TempDir()
	write := func(name string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Nothing there yet.
	if _, err := FindModelFiles(dir); err == nil {
		t.Fatal("expected error for empty dir")
	}
	write("encoder-epoch-99-avg-1-chunk-16-left-128.onnx")
	write("decoder-epoch-99-avg-1-chunk-16-left-128.onnx")
	write("joiner-epoch-99-avg-1-chunk-16-left-128.onnx")
	write("tokens.txt")
	mf, err := FindModelFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	wantSuffixes := []struct{ kind, suffix string }{
		{mf.Encoder, "encoder-epoch-99-avg-1-chunk-16-left-128.onnx"},
		{mf.Decoder, "decoder-epoch-99-avg-1-chunk-16-left-128.onnx"},
		{mf.Joiner, "joiner-epoch-99-avg-1-chunk-16-left-128.onnx"},
		{mf.Tokens, "tokens.txt"},
	}
	for _, w := range wantSuffixes {
		if filepath.Base(w.kind) != w.suffix {
			t.Errorf("resolved %q, want suffix %q", w.kind, w.suffix)
		}
	}
	// int8 variants should be preferred over fp32.
	write("encoder-epoch-99-avg-1-chunk-16-left-128.int8.onnx")
	write("joiner-epoch-99-avg-1-chunk-16-left-128.int8.onnx")
	mf, err = FindModelFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(mf.Encoder) != "encoder-epoch-99-avg-1-chunk-16-left-128.int8.onnx" {
		t.Errorf("encoder = %q, want int8 variant", mf.Encoder)
	}
	if filepath.Base(mf.Joiner) != "joiner-epoch-99-avg-1-chunk-16-left-128.int8.onnx" {
		t.Errorf("joiner = %q, want int8 variant", mf.Joiner)
	}
	// Missing joiner should error with a useful message.
	os.Remove(filepath.Join(dir, "joiner-epoch-99-avg-1-chunk-16-left-128.onnx"))
	os.Remove(filepath.Join(dir, "joiner-epoch-99-avg-1-chunk-16-left-128.int8.onnx"))
	_, err = FindModelFiles(dir)
	if err == nil {
		t.Fatal("expected error when joiner is missing")
	}
}

func TestFindModelFilesMissingDirsError(t *testing.T) {
	if _, err := FindModelFiles(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Fatal("expected error for nonexistent dir")
	}
}
