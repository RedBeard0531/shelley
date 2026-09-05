// Package stt provides streaming speech-to-text via whisper.cpp.
//
// whisper.cpp (github.com/ggml-org/whisper.cpp) is the CPU-optimized
// whisper runtime; its models produce properly cased, punctuated text,
// which the previous sherpa-onnx streaming zipformer line could not (its
// token vocabulary is uppercase by design and it emits no punctuation).
// The shared library is loaded with dlopen at runtime (see lib_linux.go),
// so the plain Shelley build does not require whisper to be installed; the
// feature simply reports unavailable when the library or model is missing.
//
// Wire format: audio is PCM16 little-endian mono at 16 kHz. A lightweight
// energy-based VAD splits the stream into utterances at ~1.2 s of trailing
// silence; each utterance is transcribed with whisper (proper case +
// punctuation) and emitted as a final. There are no per-word partials —
// text lands per pause, like phone dictation.
package stt

import (
	"fmt"
	"math"
	"path/filepath"
	"sync"
	"unsafe"
)

// SampleRate is the sample rate whisper models expect.
const SampleRate = 16000

// Options configures an Engine.
type Options struct {
	// NumThreads controls whisper decode threads. Zero means whisper's
	// default. On a 2-core VM 4 threads measured best.
	NumThreads int
	// Processors splits each utterance into parallel decoders
	// (whisper_full n_processors). Zero means min(2, NumThreads); a big
	// speedup on multicore CPUs.
	Processors int
	// LibDir is the directory containing libwhisper.so and its libggml*
	// dependencies. Empty means the system library search path.
	LibDir string
	// SilenceSeconds is how much trailing silence ends an utterance.
	// Zero means 1.2.
	SilenceSeconds float32
}

// ModelFiles is the whisper model file a model directory must provide.
type ModelFiles struct {
	Model string
}

// FindModel locates the ggml-*.bin model inside a whisper.cpp model
// directory. (The directory may hold several quantized variants; the first
// ggml-*.bin wins — if you want a specific quant, keep only that file.)
func FindModel(dir string) (ModelFiles, error) {
	matches, err := filepath.Glob(filepath.Join(dir, "ggml-*.bin"))
	if err != nil {
		return ModelFiles{}, fmt.Errorf("scan model dir %s: %w", dir, err)
	}
	if len(matches) == 0 {
		return ModelFiles{}, fmt.Errorf("model dir %s contains no ggml-*.bin (not a whisper.cpp model package?)", dir)
	}
	return ModelFiles{Model: matches[0]}, nil
}

// Transcriber is the server-facing surface of a loaded whisper engine.
// Defined as an interface so server tests can substitute a fake without
// needing the real shared library.
type Transcriber interface {
	NewSession() Session
}

// Session is one live transcription stream.
type Session interface {
	// Feed accepts PCM16 little-endian audio and returns any new partial
	// text and any finalized utterance text.
	Feed(pcm []byte) (partial, final string)
	// Close flushes and frees the stream, returning the final dangling
	// utterance text if any.
	Close() (final string)
}

// Speech detection constants: RMS over 20 ms frames. Input is normalized to
// [-1, 1], so typical speech RMS is 0.01–0.3 while room noise sits far
// below 0.006.
const (
	speechFrameSamples = SampleRate / 50 // 20 ms
	speechRMSThreshold = 0.006
)

// Engine is a loaded, immutable whisper context shared by all transcription
// sessions (serialized — whisper_full is not thread-safe on one context).
type Engine struct {
	lib        *library
	ctx        unsafe.Pointer
	mu         sync.Mutex
	threads    int
	processors int
	silence    float32
}

// Open loads the whisper.cpp shared library and the model in dir.
func Open(dir string, opts Options) (*Engine, error) {
	mf, err := FindModel(dir)
	if err != nil {
		return nil, err
	}
	lib, err := openLibrary(opts.LibDir)
	if err != nil {
		return nil, err
	}
	ctx, err := lib.initFromFile(mf.Model)
	if err != nil {
		return nil, err
	}
	threads := opts.NumThreads
	if threads < 1 {
		threads = 4
	}
	procs := opts.Processors
	if procs < 1 {
		procs = threads
		if procs > 2 {
			procs = 2
		}
	}
	silence := opts.SilenceSeconds
	if silence <= 0 {
		silence = 1.2
	}
	return &Engine{
		lib:        lib,
		ctx:        ctx,
		threads:    threads,
		processors: procs,
		silence:    silence,
	}, nil
}

// Close releases the whisper context and the loaded library.
func (e *Engine) Close() {
	if e == nil || e.ctx == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.lib.destroy(e.ctx)
	e.ctx = nil
	e.lib.close()
	e.lib = nil
}

// NewSession starts a fresh transcription stream.
func (e *Engine) NewSession() Session {
	return &session{e: e, transcribe: e.transcribe}
}

// transcribe runs whisper over samples and returns the joined segment text.
// Errors surface as "" (whisper_full failures are effectively impossible
// with a valid context and float input).
func (e *Engine) transcribe(samples []float32) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	text, err := e.lib.transcribe(e.ctx, samples, e.threads, e.processors)
	if err != nil {
		return ""
	}
	return text
}

// session is one live dictation stream (VAD + buffer + transcription).
// Not safe for concurrent use; the websocket handler owns it.
type session struct {
	e *Engine
	// transcribe is injectable for tests; production sessions use
	// Engine.transcribe.
	transcribe func([]float32) string
	buf        []float32
	speech     bool // currently inside speech
	silenceSec float32
	hasSpeech  bool // any speech yet this utterance
	// lastSpeechEnd is the buffer offset just after the last frame that
	// counted as speech, so trailing silence is trimmed before
	// transcription.
	lastSpeechEnd int
	closed        bool
}

func (s *session) Feed(pcm []byte) (partial, final string) {
	if s.closed {
		return "", ""
	}
	s.buf = append(s.buf, pcmToFloat32(pcm)...)
	// Walk 20 ms frames; a feed chunk may span several.
	for pos := 0; pos+speechFrameSamples <= len(s.buf); pos += speechFrameSamples {
		rms := frameRMS(s.buf[pos : pos+speechFrameSamples])
		if rms >= speechRMSThreshold {
			s.speech = true
			s.hasSpeech = true
			s.silenceSec = 0
			s.lastSpeechEnd = pos + speechFrameSamples
		} else if s.hasSpeech {
			s.silenceSec += 1.0 / 50 // one 20 ms frame
		}
		if s.hasSpeech && s.silenceSec >= s.e.silence {
			return "", s.finalize()
		}
	}
	return "", ""
}

func (s *session) Close() (final string) {
	if s.closed {
		return ""
	}
	s.closed = true
	if !s.hasSpeech || s.lastSpeechEnd < int(0.3*SampleRate) {
		s.buf = nil
		return ""
	}
	return s.finalize()
}

// finalize transcribes everything up to the last speech frame and clears
// the utterance state so the next utterance starts fresh.
func (s *session) finalize() string {
	utterance := s.buf[:s.lastSpeechEnd]
	s.buf = s.buf[:0]
	s.speech = false
	s.silenceSec = 0
	s.hasSpeech = false
	s.lastSpeechEnd = 0
	if len(utterance) < int(0.3*SampleRate) {
		return ""
	}
	samples := make([]float32, len(utterance))
	copy(samples, utterance)
	return s.transcribe(samples)
}

// frameRMS returns the root-mean-square amplitude of a 20 ms frame.
func frameRMS(frame []float32) float32 {
	var sum float32
	for _, v := range frame {
		sum += v * v
	}
	return float32(math.Sqrt(float64(sum / float32(len(frame)))))
}

// pcmToFloat32 converts little-endian PCM16 samples to the [-1, 1] float
// samples whisper expects.
func pcmToFloat32(pcm []byte) []float32 {
	n := len(pcm) / 2
	out := make([]float32, n)
	for i := 0; i < n; i++ {
		s := int16(pcm[i*2]) | int16(pcm[i*2+1])<<8
		out[i] = float32(s) / 32768
	}
	return out
}
