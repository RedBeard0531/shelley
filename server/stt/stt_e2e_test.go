//go:build e2e

package stt

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

// TestRealModel drives the actual sherpa-onnx library + model end to end:
// feed PCM16 audio in 200 ms chunks the way the websocket handler does and
// expect live partials and at least one finalized sentence.
//
// Skipped unless STT_MODEL_DIR points at a downloaded sherpa-onnx streaming
// model (e.g. sherpa-onnx-streaming-zipformer-en-2023-06-26):
//
//	STT_MODEL_DIR=/usr/local/share/sherpa-onnx/sherpa-onnx-streaming-zipformer-en-2023-06-26 go test -tags e2e ./server/stt -run TestRealModel -v
func TestRealModel(t *testing.T) {
	dir := os.Getenv("STT_MODEL_DIR")
	if dir == "" {
		t.Skip("set STT_MODEL_DIR to a sherpa-onnx streaming model dir to run")
	}
	eng, err := Open(dir, Options{NumThreads: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	if v := eng.Version(); v == "" {
		t.Log("sherpa-onnx version: (unknown)")
	} else {
		t.Logf("sherpa-onnx version: %s", v)
	}

	// Feed every wave file in <model>/test_wavs and require that each
	// produces at least some recognized text with live partials.
	wavs, err := filepath.Glob(filepath.Join(dir, "test_wavs", "*.wav"))
	if err != nil || len(wavs) == 0 {
		t.Fatalf("no test_wavs/*.wav in %s", dir)
	}
	anyPartial := false
	anyFinal := false
	for _, wav := range wavs {
		pcm := readWAVPCM(t, wav)
		sess := eng.NewSession()
		const chunkSamples = 3200 // 0.2 s at 16 kHz
		for start := 0; start < len(pcm); start += chunkSamples {
			end := start + chunkSamples
			if end > len(pcm) {
				end = len(pcm)
			}
			partial, final := sess.Feed(pcm[start:end])
			if partial != "" {
				anyPartial = true
				t.Logf("%s partial: %q", filepath.Base(wav), partial)
			}
			if final != "" {
				anyFinal = true
				t.Logf("%s final: %q", filepath.Base(wav), final)
			}
		}
		if tail := sess.Close(); tail != "" {
			anyFinal = true
			t.Logf("%s close-final: %q", filepath.Base(wav), tail)
		}
	}
	t.Logf("saw partials=%v finals=%v", anyPartial, anyFinal)
	if !anyPartial || !anyFinal {
		t.Errorf("expected both live partials and finalized text")
	}
}

// readWAVPCM extracts the PCM16 samples from a RIFF wave file. The model
// expects 16 kHz mono PCM16; the bundled sherpa-onnx test_wavs ship as such,
// so the "data" chunk bytes feed straight into Feed.
func readWAVPCM(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) < 12 || !bytes.Equal(raw[:4], []byte("RIFF")) {
		t.Fatalf("%s: not a RIFF file", path)
	}
	pos := 12
	for pos+8 <= len(raw) {
		size := int(binary.LittleEndian.Uint32(raw[pos+4 : pos+8]))
		if pos+8+size > len(raw) {
			break
		}
		if string(raw[pos:pos+4]) == "data" {
			return raw[pos+8 : pos+8+size]
		}
		pos += 8 + size + (size & 1)
	}
	t.Fatalf("%s: no data chunk", path)
	return nil
}
