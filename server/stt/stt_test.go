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

func TestFindModel(t *testing.T) {
	dir := t.TempDir()
	if _, err := FindModel(dir); err == nil {
		t.Fatal("expected error for empty dir")
	}
	if err := os.WriteFile(filepath.Join(dir, "ggml-base.en-q5_1.bin"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	mf, err := FindModel(dir)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(mf.Model) != "ggml-base.en-q5_1.bin" {
		t.Errorf("resolved %q, want ggml-base.en-q5_1.bin", mf.Model)
	}
	// Non-ggml files are ignored.
	if err := os.WriteFile(filepath.Join(dir, "tokens.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	mf, err = FindModel(dir)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(mf.Model) != "ggml-base.en-q5_1.bin" {
		t.Errorf("resolved %q, want the single ggml model", mf.Model)
	}
}

func TestFindModelMissingDirsError(t *testing.T) {
	if _, err := FindModel(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Fatal("expected error for nonexistent dir")
	}
}
