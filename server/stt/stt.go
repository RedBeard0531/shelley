// Package stt provides streaming speech-to-text via sherpa-onnx.
//
// sherpa-onnx (https://github.com/k2-fsa/sherpa-onnx) is the actively
// maintained successor to Vosk: it runs streaming transducer (zipformer)
// models on CPU in real time. The shared library is loaded with dlopen at
// runtime (see lib_linux.go), so the plain Shelley build does not require
// sherpa-onnx to be installed; the feature simply reports unavailable when
// the library or model is missing.
//
// Wire format: audio is PCM16 little-endian mono at 16 kHz; the recognizer
// emits per-utterance partials and finalized sentences.
package stt

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unsafe"
)

// Transcriber is the server-facing surface of a loaded sherpa-onnx engine.
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

// SampleRate is the sample rate sherpa-onnx streaming models expect.
const SampleRate = 16000

// Options configures an Engine.
type Options struct {
	// NumThreads controls ONNX Runtime worker threads per recognizer.
	// Zero means 1. More threads speed up decoding on multi-core CPUs.
	NumThreads int
	// LibDir is the directory containing libsherpa-onnx-c-api.so and its
	// dependency libonnxruntime.so. Empty means use the system library
	// search path (ldconfig, LD_LIBRARY_PATH, ...).
	LibDir string
	// EndpointSilenceSeconds is how much trailing silence ends an utterance
	// (rule2). 0 means the sherpa-onnx default of 1.2s.
	EndpointSilenceSeconds float32
}

// ModelFiles is the set of files a streaming zipformer model package needs.
type ModelFiles struct {
	Encoder string
	Decoder string
	Joiner  string
	Tokens  string
}

// FindModelFiles locates the model files inside a sherpa-onnx model
// directory. It prefers int8 quantized encoder/joiner when both variants are
// present (faster on CPU with negligible accuracy loss).
func FindModelFiles(dir string) (ModelFiles, error) {
	var mf ModelFiles
	entries, err := os.ReadDir(dir)
	if err != nil {
		return mf, fmt.Errorf("read model dir %s: %w", dir, err)
	}
	var encoders, decoders, joiners, tokenSets []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		switch {
		case strings.HasPrefix(name, "encoder-") && strings.HasSuffix(name, ".onnx"):
			encoders = append(encoders, name)
		case strings.HasPrefix(name, "decoder-") && strings.HasSuffix(name, ".onnx"):
			decoders = append(decoders, name)
		case strings.HasPrefix(name, "joiner-") && strings.HasSuffix(name, ".onnx"):
			joiners = append(joiners, name)
		case name == "tokens.txt":
			tokenSets = append(tokenSets, name)
		}
	}
	// Prefer int8 (quantized) over fp32 files when both exist.
	prefer := func(list []string) string {
		if len(list) == 0 {
			return ""
		}
		for _, n := range list {
			if strings.Contains(n, "int8") {
				return n
			}
		}
		return list[0]
	}
	mf = ModelFiles{
		Encoder: prefer(encoders),
		Decoder: prefer(decoders),
		Joiner:  prefer(joiners),
		Tokens:  prefer(tokenSets),
	}
	missing := []string{}
	if mf.Encoder == "" {
		missing = append(missing, "encoder-*.onnx")
	}
	if mf.Decoder == "" {
		missing = append(missing, "decoder-*.onnx")
	}
	if mf.Joiner == "" {
		missing = append(missing, "joiner-*.onnx")
	}
	if mf.Tokens == "" {
		missing = append(missing, "tokens.txt")
	}
	if len(missing) > 0 {
		return mf, fmt.Errorf("model dir %s is missing %s (not a sherpa-onnx streaming model package?)", dir, strings.Join(missing, ", "))
	}
	mf.Encoder = filepath.Join(dir, mf.Encoder)
	mf.Decoder = filepath.Join(dir, mf.Decoder)
	mf.Joiner = filepath.Join(dir, mf.Joiner)
	mf.Tokens = filepath.Join(dir, mf.Tokens)
	return mf, nil
}

// Engine is a loaded, immutable streaming recognizer shared by all
// transcription sessions.
type Engine struct {
	lib             *library
	recognizer      unsafe.Pointer
	numThreads      int
	endpointSilence float32
}

// Open loads the sherpa-onnx shared library and creates a streaming
// recognizer for the model in dir. It fails with a descriptive error when
// the library, model files, or recognizer construction fail.
func Open(dir string, opts Options) (*Engine, error) {
	mf, err := FindModelFiles(dir)
	if err != nil {
		return nil, err
	}
	lib, err := openLibrary(opts.LibDir)
	if err != nil {
		return nil, err
	}
	threads := opts.NumThreads
	if threads < 1 {
		threads = 1
	}
	silence := opts.EndpointSilenceSeconds
	if silence <= 0 {
		silence = 1.2
	}
	rec, err := lib.newRecognizer(mf, threads, silence)
	if err != nil {
		return nil, err
	}
	return &Engine{
		lib:             lib,
		recognizer:      rec,
		numThreads:      threads,
		endpointSilence: silence,
	}, nil
}

// Close releases the recognizer and the loaded library.
func (e *Engine) Close() {
	if e == nil || e.recognizer == nil {
		return
	}
	e.lib.destroyRecognizer(e.recognizer)
	e.recognizer = nil
	e.lib.close()
	e.lib = nil
}

// NewSession starts a fresh transcription stream.
func (e *Engine) NewSession() Session {
	return &session{
		lib:        e.lib,
		recognizer: e.recognizer,
		stream:     e.lib.newStream(e.recognizer),
	}
}

// Version returns the loaded sherpa-onnx library version string.
func (e *Engine) Version() string {
	if e == nil || e.lib == nil {
		return ""
	}
	return e.lib.versionStr()
}

// session is one live transcription stream. It is not safe for concurrent
// use; callers serialize.
type session struct {
	lib        *library
	recognizer unsafe.Pointer
	stream     unsafe.Pointer
	mu         sync.Mutex
	// lastPartial is the most recently emitted partial text, used to skip
	// sending unchanged partials.
	lastPartial string
	// closed guards double-finalize after Close.
	closed bool
}

// Feed accepts PCM16 little-endian audio and returns any new partial text
// and any finalized utterance text.
func (s *session) Feed(pcm []byte) (partial, final string) {
	samples := pcmToFloat32(pcm)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stream == nil {
		return "", ""
	}
	s.lib.acceptWaveform(s.stream, samples)
	for s.lib.isReady(s.recognizer, s.stream) {
		s.lib.decode(s.recognizer, s.stream)
	}
	text := s.lib.resultText(s.recognizer, s.stream)
	if s.lib.isEndpoint(s.recognizer, s.stream) {
		s.lib.reset(s.recognizer, s.stream)
		s.lastPartial = ""
		return "", text
	}
	if text != "" && text != s.lastPartial {
		s.lastPartial = text
		return text, ""
	}
	return "", ""
}

// Close drains the stream (flushing any dangling utterance as a final
// result and emitting it) and frees the stream.
func (s *session) Close() (final string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.stream == nil {
		return ""
	}
	// Tail padding lets the decoder flush tokens still sitting in its
	// attention window when the user stops mid-phrase.
	pad := make([]float32, int(float32(SampleRate)*0.5))
	s.lib.acceptWaveform(s.stream, pad)
	for s.lib.isReady(s.recognizer, s.stream) {
		s.lib.decode(s.recognizer, s.stream)
	}
	final = s.lib.resultText(s.recognizer, s.stream)
	s.lib.destroyStream(s.stream)
	s.stream = nil
	s.closed = true
	return final
}

// pcmToFloat32 converts little-endian PCM16 samples to the [-1, 1] float
// samples sherpa-onnx expects.
func pcmToFloat32(pcm []byte) []float32 {
	n := len(pcm) / 2
	out := make([]float32, n)
	for i := 0; i < n; i++ {
		s := int16(pcm[i*2]) | int16(pcm[i*2+1])<<8
		out[i] = float32(s) / 32768
	}
	return out
}
