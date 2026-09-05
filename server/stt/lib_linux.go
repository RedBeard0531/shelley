//go:build linux && cgo

package stt

/*
#cgo LDFLAGS: -ldl
#include <stdlib.h>
#include <dlfcn.h>
#include <stdint.h>
#include <stdio.h>
#include <string.h>

// Mirror of the public sherpa-onnx C ABI structs (c-api.h). The layout must
// match the official header; these names are local so the real header is not
// required at build time.

typedef struct sox_online_transducer_cfg {
	const char *encoder;
	const char *decoder;
	const char *joiner;
} sox_online_transducer_cfg;

typedef struct sox_online_paraformer_cfg {
	const char *encoder;
	const char *decoder;
} sox_online_paraformer_cfg;

typedef struct sox_online_zipformer2_ctc_cfg {
	const char *model;
} sox_online_zipformer2_ctc_cfg;

typedef struct sox_online_nemo_ctc_cfg {
	const char *model;
} sox_online_nemo_ctc_cfg;

typedef struct sox_online_tone_ctc_cfg {
	const char *model;
} sox_online_tone_ctc_cfg;

typedef struct sox_online_model_cfg {
	sox_online_transducer_cfg transducer;
	sox_online_paraformer_cfg paraformer;
	sox_online_zipformer2_ctc_cfg zipformer2_ctc;
	const char *tokens;
	int32_t num_threads;
	const char *provider;
	int32_t debug;
	const char *model_type;
	const char *modeling_unit;
	const char *bpe_vocab;
	const char *tokens_buf;
	int32_t tokens_buf_size;
	sox_online_nemo_ctc_cfg nemo_ctc;
	sox_online_tone_ctc_cfg t_one_ctc;
} sox_online_model_cfg;

typedef struct sox_feat_cfg {
	int32_t sample_rate;
	int32_t feature_dim;
} sox_feat_cfg;

typedef struct sox_ctc_fst_cfg {
	const char *graph;
	int32_t max_active;
} sox_ctc_fst_cfg;

typedef struct sox_hr_cfg {
	const char *dict_dir;
	const char *lexicon;
	const char *rule_fsts;
} sox_hr_cfg;

typedef struct sox_online_rec_cfg {
	sox_feat_cfg feat_config;
	sox_online_model_cfg model_config;
	const char *decoding_method;
	int32_t max_active_paths;
	int32_t enable_endpoint;
	float rule1_min_trailing_silence;
	float rule2_min_trailing_silence;
	float rule3_min_utterance_length;
	const char *hotwords_file;
	float hotwords_score;
	sox_ctc_fst_cfg ctc_fst_decoder_config;
	const char *rule_fsts;
	const char *rule_fars;
	float blank_penalty;
	const char *hotwords_buf;
	int32_t hotwords_buf_size;
	sox_hr_cfg hr;
} sox_online_rec_cfg;

typedef struct sox_online_rec_result {
	const char *text;
	const char *tokens;
	const char *const *tokens_arr;
	float *timestamps;
	int32_t count;
	const char *json;
} sox_online_rec_result;

// All dlsym'd function pointers are resolved and invoked ONLY from C: a bare
// dlsym pointer is not callable from Go (different calling convention), so
// every entry point below is a static wrapper that cgo calls normally. The
// static-local fn cache is fine: one library per process.

static void *sox_sym(const void *lib, const char *name) {
	return dlsym((void *)lib, name);
}

static void *sox_create_recognizer(const void *lib, const sox_online_rec_cfg *cfg) {
	static void *(*fn)(const sox_online_rec_cfg *);
	if (!fn) fn = (void *(*)(const sox_online_rec_cfg *))sox_sym(lib, "SherpaOnnxCreateOnlineRecognizer");
	return fn(cfg);
}

static void sox_destroy_recognizer(const void *lib, void *r) {
	static void (*fn)(void *);
	if (!fn) fn = (void (*)(void *))sox_sym(lib, "SherpaOnnxDestroyOnlineRecognizer");
	fn(r);
}

static void *sox_create_stream(const void *lib, void *r) {
	static void *(*fn)(void *);
	if (!fn) fn = (void *(*)(void *))sox_sym(lib, "SherpaOnnxCreateOnlineStream");
	return fn(r);
}

static void sox_destroy_stream(const void *lib, void *s) {
	static void (*fn)(void *);
	if (!fn) fn = (void (*)(void *))sox_sym(lib, "SherpaOnnxDestroyOnlineStream");
	fn(s);
}

static void sox_accept_waveform(const void *lib, const void *s, int32_t rate, const float *samples, int32_t n) {
	static void (*fn)(const void *, int32_t, const float *, int32_t);
	if (!fn) fn = (void (*)(const void *, int32_t, const float *, int32_t))sox_sym(lib, "SherpaOnnxOnlineStreamAcceptWaveform");
	fn(s, rate, samples, n);
}

static int32_t sox_is_ready(const void *lib, const void *r, const void *s) {
	static int32_t (*fn)(const void *, const void *);
	if (!fn) fn = (int32_t (*)(const void *, const void *))sox_sym(lib, "SherpaOnnxIsOnlineStreamReady");
	return fn(r, s);
}

static void sox_decode(const void *lib, const void *r, const void *s) {
	static void (*fn)(const void *, const void *);
	if (!fn) fn = (void (*)(const void *, const void *))sox_sym(lib, "SherpaOnnxDecodeOnlineStream");
	fn(r, s);
}

static const sox_online_rec_result *sox_get_result(const void *lib, const void *r, const void *s) {
	static const sox_online_rec_result *(*fn)(const void *, const void *);
	if (!fn) fn = (const sox_online_rec_result *(*)(const void *, const void *))sox_sym(lib, "SherpaOnnxGetOnlineStreamResult");
	return fn(r, s);
}

static void sox_destroy_result(const void *lib, const sox_online_rec_result *x) {
	static void (*fn)(const sox_online_rec_result *);
	if (!fn) fn = (void (*)(const sox_online_rec_result *))sox_sym(lib, "SherpaOnnxDestroyOnlineRecognizerResult");
	fn(x);
}

static int32_t sox_is_endpoint(const void *lib, const void *r, const void *s) {
	static int32_t (*fn)(const void *, const void *);
	if (!fn) fn = (int32_t (*)(const void *, const void *))sox_sym(lib, "SherpaOnnxOnlineStreamIsEndpoint");
	return fn(r, s);
}

static void sox_reset(const void *lib, const void *r, const void *s) {
	static void (*fn)(const void *, const void *);
	if (!fn) fn = (void (*)(const void *, const void *))sox_sym(lib, "SherpaOnnxOnlineStreamReset");
	fn(r, s);
}

static const char *sox_get_version(const void *lib) {
	static const char *(*fn)(void);
	if (!fn) fn = (const char *(*)(void))sox_sym(lib, "SherpaOnnxGetVersionStr");
	return fn ? fn() : NULL;
}

// Eagerly verify every required symbol exists at dlopen time so a bad
// library fails with a message instead of a segfault on first use.
static int sox_check_symbols(const void *lib, char *err, size_t errsz) {
#define CHECK(name)                                                                  \
	do {                                                                          \
		if (!dlsym((void *)lib, name)) {                                      \
			snprintf(err, errsz, "%s: %s", name, dlerror());              \
			return -1;                                                    \
		}                                                                     \
	} while (0)
	CHECK("SherpaOnnxCreateOnlineRecognizer");
	CHECK("SherpaOnnxDestroyOnlineRecognizer");
	CHECK("SherpaOnnxCreateOnlineStream");
	CHECK("SherpaOnnxDestroyOnlineStream");
	CHECK("SherpaOnnxOnlineStreamAcceptWaveform");
	CHECK("SherpaOnnxIsOnlineStreamReady");
	CHECK("SherpaOnnxDecodeOnlineStream");
	CHECK("SherpaOnnxGetOnlineStreamResult");
	CHECK("SherpaOnnxDestroyOnlineRecognizerResult");
	CHECK("SherpaOnnxOnlineStreamIsEndpoint");
	CHECK("SherpaOnnxOnlineStreamReset");
#undef CHECK
	return 0;
}
*/
import "C"

import (
	"fmt"
	"os"
	"path/filepath"
	"unsafe"
)

// library holds the dlopen'd sherpa-onnx library. Handles are opaque: all
// object types are unsafe.Pointer. All calls go through the C wrappers, so
// no function pointer is ever invoked from Go.
type library struct {
	handle unsafe.Pointer
}

// openLibrary loads libsherpa-onnx-c-api.so, preloading its onnxruntime
// dependency from the same directory when one is supplied (dlopen resolves
// DT_NEEDED deps through the loader search paths, so a side-by-side
// libonnxruntime.so only resolves if it is also on the search path).
func openLibrary(libDir string) (*library, error) {
	if libDir != "" {
		ort := filepath.Join(libDir, "libonnxruntime.so")
		if _, err := os.Stat(ort); err == nil {
			cname := C.CString(ort)
			C.dlopen(cname, C.RTLD_NOW|C.RTLD_GLOBAL)
			C.free(unsafe.Pointer(cname))
		}
	}

	candidates := []string{}
	if libDir != "" {
		candidates = append(candidates, filepath.Join(libDir, "libsherpa-onnx-c-api.so"))
	}
	if libDir != "/usr/local/lib" {
		candidates = append(candidates, "/usr/local/lib/libsherpa-onnx-c-api.so")
	}
	candidates = append(candidates, "libsherpa-onnx-c-api.so")

	var lastErr error
	for _, cand := range candidates {
		lib, err := openLibPath(cand)
		if err == nil {
			return lib, nil
		}
		lastErr = err
	}
	return nil, fmt.Errorf("load sherpa-onnx (%v): %w", candidates[0], lastErr)
}

func openLibPath(path string) (*library, error) {
	cpath := C.CString(path)
	defer C.free(unsafe.Pointer(cpath))
	handle := C.dlopen(cpath, C.RTLD_NOW|C.RTLD_GLOBAL)
	if handle == nil {
		return nil, fmt.Errorf("dlopen: %s", C.GoString(C.dlerror()))
	}
	errbuf := make([]byte, 256)
	if rc := C.sox_check_symbols(handle, (*C.char)(unsafe.Pointer(&errbuf[0])), C.size_t(len(errbuf))); rc != 0 {
		C.dlclose(handle)
		return nil, fmt.Errorf("sherpa-onnx: %s", C.GoString((*C.char)(unsafe.Pointer(&errbuf[0]))))
	}
	return &library{handle: unsafe.Pointer(handle)}, nil
}

func (l *library) newRecognizer(mf ModelFiles, threads int, endpointSilence float32) (unsafe.Pointer, error) {
	cfg := &C.sox_online_rec_cfg{}
	cfg.feat_config.sample_rate = C.int32_t(SampleRate)
	cfg.feat_config.feature_dim = 80

	setStr := func(dst **C.char, s string) {
		if s != "" {
			*dst = C.CString(s)
		}
	}
	setStr(&cfg.model_config.transducer.encoder, mf.Encoder)
	setStr(&cfg.model_config.transducer.decoder, mf.Decoder)
	setStr(&cfg.model_config.transducer.joiner, mf.Joiner)
	setStr(&cfg.model_config.tokens, mf.Tokens)
	setStr(&cfg.model_config.provider, "cpu")
	cfg.model_config.num_threads = C.int32_t(threads)
	cfg.model_config.model_type = C.CString("zipformer2")

	setStr(&cfg.decoding_method, "greedy_search")
	cfg.enable_endpoint = 1
	cfg.rule1_min_trailing_silence = 2.4
	cfg.rule2_min_trailing_silence = C.float(endpointSilence)
	cfg.rule3_min_utterance_length = 20.0

	rec := C.sox_create_recognizer(l.handle, cfg)
	// The C strings are copied by CreateOnlineRecognizer; free ours regardless.
	C.free(unsafe.Pointer(cfg.model_config.transducer.encoder))
	C.free(unsafe.Pointer(cfg.model_config.transducer.decoder))
	C.free(unsafe.Pointer(cfg.model_config.transducer.joiner))
	C.free(unsafe.Pointer(cfg.model_config.tokens))
	C.free(unsafe.Pointer(cfg.model_config.provider))
	C.free(unsafe.Pointer(cfg.model_config.model_type))
	C.free(unsafe.Pointer(cfg.decoding_method))
	if rec == nil {
		return nil, fmt.Errorf("SherpaOnnxCreateOnlineRecognizer failed (invalid model files?)")
	}
	return rec, nil
}

func (l *library) destroyRecognizer(rec unsafe.Pointer) { C.sox_destroy_recognizer(l.handle, rec) }

func (l *library) newStream(rec unsafe.Pointer) unsafe.Pointer {
	return C.sox_create_stream(l.handle, rec)
}

func (l *library) destroyStream(stream unsafe.Pointer) { C.sox_destroy_stream(l.handle, stream) }

// acceptWaveform feeds float samples into the stream.
func (l *library) acceptWaveform(stream unsafe.Pointer, samples []float32) {
	C.sox_accept_waveform(l.handle, stream, C.int32_t(SampleRate), (*C.float)(unsafe.Pointer(&samples[0])), C.int32_t(len(samples)))
}

func (l *library) isReady(rec, stream unsafe.Pointer) bool {
	return C.sox_is_ready(l.handle, rec, stream) == 1
}

func (l *library) decode(rec, stream unsafe.Pointer) { C.sox_decode(l.handle, rec, stream) }

func (l *library) isEndpoint(rec, stream unsafe.Pointer) bool {
	return C.sox_is_endpoint(l.handle, rec, stream) == 1
}

func (l *library) reset(rec, stream unsafe.Pointer) { C.sox_reset(l.handle, rec, stream) }

// resultText snapshots and frees the current result, returning its text.
func (l *library) resultText(rec, stream unsafe.Pointer) string {
	r := C.sox_get_result(l.handle, rec, stream)
	if r == nil {
		return ""
	}
	defer C.sox_destroy_result(l.handle, r)
	return C.GoString(r.text)
}

func (l *library) versionStr() string {
	return C.GoString(C.sox_get_version(l.handle))
}

func (l *library) close() {
	if l.handle != nil {
		C.dlclose(l.handle)
		l.handle = nil
	}
}
