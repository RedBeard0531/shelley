// VAD (voice activity detection) unit tests: expands the session's feed loop
// with synthetic PCM16 audio and a stub transcriber, verifying when
// utterances finalize without needing the real whisper library.

package stt

import (
	"math"
	"testing"
)

// pcmChunk returns nSamples of constant-amplitude (or zero) PCM16 audio.
func pcmChunk(nSamples int, amp float64) []byte {
	out := make([]byte, nSamples*2)
	v := int16(amp * 32767)
	for i := 0; i < nSamples; i++ {
		out[i*2] = byte(v)
		out[i*2+1] = byte(uint16(v) >> 8)
	}
	return out
}

func newTestSession(transcribe func([]float32) string, silenceSec float32) *session {
	e := &Engine{silence: silenceSec}
	s := &session{e: e}
	if transcribe != nil {
		s.transcribe = transcribe
	} else {
		s.transcribe = func(samples []float32) string { return "transcribed" }
	}
	return s
}

func TestVADPureSilenceNeverFinalizes(t *testing.T) {
	s := newTestSession(nil, 1.2)
	for i := 0; i < 30; i++ { // 5 seconds of silence
		if _, final := s.Feed(pcmChunk(SampleRate/10, 0)); final != "" {
			t.Fatalf("silence produced a final: %q", final)
		}
	}
	if final := s.Close(); final != "" {
		t.Fatalf("Close on pure silence produced %q", final)
	}
}

func TestVADFinalizesAfterTrailingSilence(t *testing.T) {
	s := newTestSession(nil, 1.2)
	// 1 s of speech.
	if _, final := s.Feed(pcmChunk(SampleRate, 0.1)); final != "" {
		t.Fatalf("speech alone finalizes too early: %q", final)
	}
	// 1.4 s of silence → one final.
	var finals []string
	for i := 0; i < 14; i++ {
		_, f := s.Feed(pcmChunk(SampleRate/10, 0))
		if f != "" {
			finals = append(finals, f)
		}
	}
	if len(finals) != 1 || finals[0] != "transcribed" {
		t.Fatalf("got finals %v, want exactly one", finals)
	}
	// More silence: nothing further (buffer was reset).
	if _, f := s.Feed(pcmChunk(SampleRate, 0)); f != "" {
		t.Fatalf("got %q after utterance already finalized", f)
	}
	if f := s.Close(); f != "" {
		t.Fatalf("Close after finalize produced %q", f)
	}
}

func TestVADContinuousSpeechNoFinal(t *testing.T) {
	s := newTestSession(nil, 1.2)
	// 10 s of near-continuous speech (short gaps well under the limit).
	for i := 0; i < 50; i++ {
		if _, f := s.Feed(pcmChunk(SampleRate/5, 0.1)); f != "" {
			t.Fatalf("continuous speech finalized early: %q", f)
		}
		if _, f := s.Feed(pcmChunk(SampleRate/10, 0.03)); f != "" { // brief gap
			t.Fatalf("brief gap finalized: %q", f)
		}
	}
	// Stopping mid-stream flushes the whole thing as one final.
	if f := s.Close(); f != "transcribed" {
		t.Fatalf("Close returned %q, want transcribed", f)
	}
}

func TestVADMultipleUtterances(t *testing.T) {
	var calls int
	s := newTestSession(func(samples []float32) string {
		calls++
		return "utt"
	}, 1.2)
	// First utterance.
	if _, f := s.Feed(pcmChunk(SampleRate, 0.1)); f != "" {
		t.Fatal("early final")
	}
	if _, f := s.Feed(pcmChunk(int(1.4*SampleRate), 0)); f != "utt" {
		t.Fatalf("first utterance final = %q", f)
	}
	// Second utterance, flushed by Close.
	if _, f := s.Feed(pcmChunk(SampleRate, 0.1)); f != "" {
		t.Fatal("early final")
	}
	if f := s.Close(); f != "utt" {
		t.Fatalf("second utterance final = %q", f)
	}
	if calls != 2 {
		t.Fatalf("transcriber called %d times, want 2", calls)
	}
}

func TestVADTrimsTrailingSilence(t *testing.T) {
	var gotLen int
	s := newTestSession(func(samples []float32) string {
		gotLen = len(samples)
		return "x"
	}, 1.2)
	s.Feed(pcmChunk(SampleRate, 0.1))        // 1 s speech
	s.Feed(pcmChunk(int(0.8*SampleRate), 0)) // 0.8 s silence (below limit)
	s.Feed(pcmChunk(int(0.6*SampleRate), 0)) // crosses 1.2 s limit
	if gotLen != SampleRate {                // only the speech should be transcribed
		t.Fatalf("transcribed %d samples, want %d (trailing silence trimmed)", gotLen, SampleRate)
	}
}

func TestVADTooShortToTrustIsDropped(t *testing.T) {
	var calls int
	s := newTestSession(func(samples []float32) string {
		calls++
		return "junk"
	}, 1.2)
	// 50 ms of "speech" — below the 0.3 s trust floor.
	s.Feed(pcmChunk(int(0.05*SampleRate), 0.2))
	if f := s.Close(); f != "" {
		t.Fatalf("short blip transcribed as %q", f)
	}
	if calls != 0 {
		t.Fatalf("transcriber called %d times for a blip", calls)
	}
}

func TestFrameRMS(t *testing.T) {
	// Sinusoid at amplitude 0.5 → RMS ≈ 0.354.
	frame := make([]float32, 320)
	for i := range frame {
		frame[i] = float32(0.5 * math.Sin(2*math.Pi*float64(i)/32))
	}
	rms := frameRMS(frame)
	if rms < 0.35 || rms > 0.36 {
		t.Fatalf("rms = %f, want ~0.354", rms)
	}
	// Silence → 0.
	if rms := frameRMS(make([]float32, 320)); rms != 0 {
		t.Fatalf("silence rms = %v, want 0", rms)
	}
}
