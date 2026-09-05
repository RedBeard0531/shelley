//go:build e2e

package stt

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRealModel drives the actual whisper.cpp library + model end to end:
// feed PCM16 audio in 200 ms chunks the way the websocket handler does and
// expect finalized utterances, with proper lowercase + punctuation (the two
// things the old sherpa-onnx backend got wrong).
//
// Skipped unless STT_MODEL_DIR points at a whisper.cpp model directory
// (containing ggml-*.bin); STT_WAVS_DIR points at a directory of 16 kHz
// mono wav files to drive it with (the sherpa-onnx test_wavs work fine):
//
//	STT_MODEL_DIR=/usr/local/share/whisper \
//	STT_WAVS_DIR=/usr/local/share/sherpa-onnx/sherpa-onnx-streaming-zipformer-en-2023-06-26/test_wavs \
//	go test -tags e2e ./server/stt -run TestRealModel -v
func TestRealModel(t *testing.T) {
	modelDir := os.Getenv("STT_MODEL_DIR")
	if modelDir == "" {
		t.Skip("set STT_MODEL_DIR to a whisper.cpp model dir to run")
	}
	eng, err := Open(modelDir, Options{NumThreads: 4, Processors: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()

	wavsDir := os.Getenv("STT_WAVS_DIR")
	if wavsDir == "" {
		t.Skip("set STT_WAVS_DIR to a dir of 16 kHz wav files to drive the model")
	}
	wavs, err := filepath.Glob(filepath.Join(wavsDir, "*.wav"))
	if err != nil || len(wavs) == 0 {
		t.Fatalf("no wav files in %s", wavsDir)
	}

	var allText strings.Builder
	for _, wav := range wavs {
		pcm, rate := readWAVPCM(t, wav)
		if rate != SampleRate {
			t.Logf("skipping %s (sample rate %d)", filepath.Base(wav), rate)
			continue
		}
		sess := eng.NewSession()
		var finals []string
		const chunk = 3200 // 0.2 s at 16 kHz
		for start := 0; start < len(pcm); start += chunk {
			end := start + chunk
			if end > len(pcm) {
				end = len(pcm)
			}
			_, final := sess.Feed(pcm[start:end])
			if final != "" {
				t.Logf("%s final: %q", filepath.Base(wav), final)
				finals = append(finals, final)
			}
		}
		if tail := sess.Close(); tail != "" {
			t.Logf("%s close-final: %q", filepath.Base(wav), tail)
			finals = append(finals, tail)
		}
		if len(finals) == 0 {
			t.Errorf("%s: no finalized text (VAD or whisper failed?)", filepath.Base(wav))
		}
		for _, f := range finals {
			allText.WriteString(f)
			allText.WriteString(" ")
		}
	}

	combined := allText.String()
	t.Logf("combined transcript: %q", combined)
	if combined == "" {
		t.Fatal("no text from any wav")
	}
	if combined == strings.ToUpper(combined) {
		t.Error("transcript is entirely uppercase (expected whisper lowercase output)")
	}
	if !strings.Contains(combined, ".") {
		t.Error("transcript has no periods (expected whisper punctuation)")
	}
}

// readWAVPCM extracts the PCM16 samples and sample rate from a RIFF wave
// file. The "data" chunk bytes feed straight into Feed.
func readWAVPCM(t *testing.T, path string) ([]byte, int) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) < 12 || !bytes.Equal(raw[:4], []byte("RIFF")) {
		t.Fatalf("%s: not a RIFF file", path)
	}
	// fmt chunk: audio_format(2) channels(2) sample_rate(4) ...
	rate := int(binary.LittleEndian.Uint32(raw[24:28]))
	bits := int(binary.LittleEndian.Uint16(raw[34:36]))
	pos := 12
	for pos+8 <= len(raw) {
		size := int(binary.LittleEndian.Uint32(raw[pos+4 : pos+8]))
		if pos+8+size > len(raw) {
			break
		}
		if string(raw[pos:pos+4]) == "data" {
			if bits != 16 {
				t.Fatalf("%s: %d-bit audio, want 16", path, bits)
			}
			return raw[pos+8 : pos+8+size], rate
		}
		pos += 8 + size + (size & 1)
	}
	t.Fatalf("%s: no data chunk", path)
	return nil, 0
}
