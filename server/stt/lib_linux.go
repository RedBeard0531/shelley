//go:build linux && cgo

package stt

/*
#cgo LDFLAGS: -ldl
#include <stdlib.h>
#include <dlfcn.h>
#include <stdint.h>
#include <stdbool.h>
#include <stddef.h>
#include <stdio.h>
#include <string.h>

// Mirrors of the public whisper.cpp ABI (include/whisper.h at tag b4938,
// libwhisper 1.9.x). Layout must match that header; names are local so the
// real header is not required at build time. Function-pointer fields are
// declared as void* (identical size/alignment); they are never dereferenced
// - we only set the scalar fields we care about, in C.

typedef int32_t whisper_token;

typedef struct sox_vad_params {
	float threshold;
	int32_t min_speech_duration_ms;
	int32_t min_silence_duration_ms;
	float max_speech_duration_s;
	int32_t speech_pad_ms;
	float samples_overlap;
} sox_vad_params;

typedef struct sox_full_params {
	int32_t strategy;

	int32_t n_threads;
	int32_t n_max_text_ctx;
	int32_t offset_ms;
	int32_t duration_ms;

	bool translate;
	bool no_context;
	bool no_timestamps;
	bool single_segment;
	bool print_special;
	bool print_progress;
	bool print_realtime;
	bool print_timestamps;

	bool token_timestamps;
	float thold_pt;
	float thold_ptsum;
	int32_t max_len;
	bool split_on_word;
	int32_t max_tokens;

	bool debug_mode;
	int32_t audio_ctx;

	bool tdrz_enable;

	const char *suppress_regex;

	const char *initial_prompt;
	bool carry_initial_prompt;
	const whisper_token *prompt_tokens;
	int32_t prompt_n_tokens;

	const char *language;
	bool detect_language;

	bool suppress_blank;
	bool suppress_nst;

	float temperature;
	float max_initial_ts;
	float length_penalty;

	float temperature_inc;
	float entropy_thold;
	float logprob_thold;
	float no_speech_thold;

	struct { int32_t best_of; } greedy;
	struct { int32_t beam_size; float patience; } beam_search;

	void *new_segment_callback;
	void *new_segment_callback_user_data;
	void *progress_callback;
	void *progress_callback_user_data;
	void *encoder_begin_callback;
	void *encoder_begin_callback_user_data;
	void *abort_callback;
	void *abort_callback_user_data;
	void *logits_filter_callback;
	void *logits_filter_callback_user_data;
	void *grammar_rules;
	size_t n_grammar_rules;
	size_t i_start_rule;
	float grammar_penalty;

	bool vad;
	const char *vad_model_path;
	sox_vad_params vad_params;
} sox_full_params;

// All dlsym'd function pointers are resolved and invoked ONLY from C: a bare
// dlsym pointer is not callable from Go (different calling convention), so
// every entry point below is a static wrapper that cgo calls normally. The
// static-local fn cache is fine: one library per process.

static void *sox_sym(const void *lib, const char *name) {
	return dlsym((void *)lib, name);
}

static void *sox_init_from_file(const void *lib, const char *path) {
	static void *(*fn)(const char *);
	if (!fn) fn = (void *(*)(const char *))sox_sym(lib, "whisper_init_from_file");
	return fn(path);
}

static void sox_free(const void *lib, void *ctx) {
	static void (*fn)(void *);
	if (!fn) fn = (void (*)(void *))sox_sym(lib, "whisper_free");
	fn(ctx);
}

// Returns a fully-initialized whisper_full params with the fields we need
// pre-set (clean text output, English, no timestamps, our own VAD off).
static sox_full_params sox_default_params(const void *lib, int32_t n_threads) {
	static sox_full_params (*fn)(int32_t);
	if (!fn) fn = (sox_full_params (*)(int32_t))sox_sym(lib, "whisper_full_default_params");
	sox_full_params p = fn(0); // 0 = WHISPER_SAMPLING_GREEDY
	p.n_threads = n_threads;
	p.no_timestamps = true;
	p.single_segment = false;
	p.print_special = false;
	p.print_progress = false;
	p.print_realtime = false;
	p.print_timestamps = false;
	p.language = "en";
	p.vad = false;
	return p;
}

static int sox_full(const void *lib, void *ctx, sox_full_params params, const float *samples, int32_t n_samples, int32_t n_processors) {
	static int (*fn)(void *, sox_full_params, const float *, int32_t, int32_t);
	if (!fn) fn = (int (*)(void *, sox_full_params, const float *, int32_t, int32_t))sox_sym(lib, "whisper_full_parallel");
	return fn(ctx, params, samples, n_samples, n_processors);
}

static int32_t sox_n_segments(const void *lib, void *ctx) {
	static int32_t (*fn)(void *);
	if (!fn) fn = (int32_t (*)(void *))sox_sym(lib, "whisper_full_n_segments");
	return fn(ctx);
}

static const char *sox_segment_text(const void *lib, void *ctx, int32_t i) {
	static const char *(*fn)(void *, int32_t);
	if (!fn) fn = (const char *(*)(void *, int32_t))sox_sym(lib, "whisper_full_get_segment_text");
	return fn(ctx, i);
}

// Silence whisper's own logging (it prints model metadata + progress to
// stderr on every call otherwise).
typedef void (*sox_log_cb)(int level, const char *text, void *user_data);
static void sox_null_log(int level, const char *text, void *user_data) {
	(void)level; (void)text; (void)user_data;
}
static void sox_silence_logs(const void *lib) {
	static void (*fn)(sox_log_cb);
	if (!fn) {
		fn = (void (*)(sox_log_cb))sox_sym(lib, "whisper_log_set");
		if (!fn) return; // older libs without logger control: leave default
	}
	fn(sox_null_log);
}

// ggml discovers CPU backends by scanning [lib]ggml-cpu-*.so next to the
// *executable*, which for the Shelley binary is not where the models live.
// Ask it to scan our library directory explicitly before whisper_init runs.
static void sox_load_backends(const char *dir) {
	static void (*fn)(const char *);
	if (!fn) fn = (void (*)(const char *))dlsym(RTLD_DEFAULT, "ggml_backend_load_all_from_path");
	if (fn) fn(dir);
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
	CHECK("whisper_init_from_file");
	CHECK("whisper_free");
	CHECK("whisper_full_default_params");
	CHECK("whisper_full_parallel");
	CHECK("whisper_full_n_segments");
	CHECK("whisper_full_get_segment_text");
#undef CHECK
	return 0;
}
*/
import "C"

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unsafe"
)

// library holds the dlopen'd whisper.cpp shared library. All calls go
// through the C wrappers, so no function pointer is ever invoked from Go.
type library struct {
	handle unsafe.Pointer
}

// openLibrary loads libwhisper.so and preloads its ggml dependencies from
// the same directory. The library may live either in --stt-lib-dir or
// /usr/local/lib.
func openLibrary(libDir string) (*library, error) {
	dirs := []string{libDir}
	if libDir != "/usr/local/lib" {
		dirs = append(dirs, "/usr/local/lib")
	}

	var lastErr error
	for _, dir := range dirs {
		if dir == "" {
			continue
		}
		preloadBaseDeps(dir)
		scanDir := filteredBackendDir(dir)
		cpath := C.CString(scanDir)
		C.sox_load_backends(cpath) // ggml backend registry scan
		C.free(unsafe.Pointer(cpath))
		lib, err := openLibPath(filepath.Join(dir, "libwhisper.so"))
		if err == nil {
			return lib, nil
		}
		lastErr = err
	}
	return nil, fmt.Errorf("load whisper.cpp: %w", lastErr)
}

// filteredBackendDir builds a temp dir (symlinks) containing only the
// libggml-cpu-<variant>.so files this CPU can actually run, then returns its
// path. ggml's own scan of the raw lib dir would pick the highest-scoring
// variant (e.g. zen4) without checking its ISA requirements are met.
func filteredBackendDir(libDir string) string {
	dir, err := os.MkdirTemp("", "ggml-backends-")
	if err != nil {
		return libDir
	}
	enabled := permittedVariants(cpuFlags())
	for _, name := range enabled {
		src := filepath.Join(libDir, "libggml-cpu-"+name+".so")
		if _, err := os.Lstat(src); err != nil {
			continue
		}
		_ = os.Symlink(src, filepath.Join(dir, "libggml-cpu-"+name+".so"))
	}
	return dir
}

// preloadBaseDeps loads libggml.so and libggml-base.so with RTLD_GLOBAL so
// libwhisper.so's DT_NEEDED deps resolve and ggml's backend registry is
// reachable for the explicit load_all_from_path above.
func preloadBaseDeps(dir string) {
	for _, dep := range []string{"libggml.so", "libggml-base.so"} {
		cpath := C.CString(filepath.Join(dir, dep))
		C.dlopen(cpath, C.RTLD_NOW|C.RTLD_GLOBAL)
		C.free(unsafe.Pointer(cpath))
	}
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
		return nil, fmt.Errorf("whisper: %s", C.GoString((*C.char)(unsafe.Pointer(&errbuf[0]))))
	}
	return &library{handle: unsafe.Pointer(handle)}, nil
}

func (l *library) initFromFile(path string) (unsafe.Pointer, error) {
	cpath := C.CString(path)
	defer C.free(unsafe.Pointer(cpath))
	ctx := C.sox_init_from_file(l.handle, cpath)
	if ctx == nil {
		return nil, fmt.Errorf("whisper_init_from_file failed for %s (corrupt or unsupported model?)", path)
	}
	C.sox_silence_logs(l.handle)
	return ctx, nil
}

func (l *library) destroy(ctx unsafe.Pointer) { C.sox_free(l.handle, ctx) }

func (l *library) close() {
	if l.handle != nil {
		C.dlclose(l.handle)
		l.handle = nil
	}
}

func (l *library) transcribe(ctx unsafe.Pointer, samples []float32, nThreads, nProcessors int) (string, error) {
	params := C.sox_default_params(l.handle, C.int32_t(nThreads))
	rc := C.sox_full(l.handle, ctx, params, (*C.float)(unsafe.Pointer(&samples[0])), C.int32_t(len(samples)), C.int32_t(nProcessors))
	if rc != 0 {
		return "", fmt.Errorf("whisper_full_parallel failed: %d", rc)
	}
	n := int(C.sox_n_segments(l.handle, ctx))
	out := make([]byte, 0, 256)
	for i := 0; i < n; i++ {
		seg := C.GoString(C.sox_segment_text(l.handle, ctx, C.int32_t(i)))
		if seg == "" {
			continue
		}
		if len(out) > 0 {
			out = append(out, ' ')
		}
		out = append(out, seg...)
	}
	return strings.TrimSpace(string(out)), nil
}
