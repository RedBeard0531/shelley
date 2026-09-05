//go:build !linux || !cgo

package stt

import "fmt"

// library is unavailable on non-Linux platforms (or with cgo disabled).
// Open fails with a clear message instead of silently degrading.
type library struct{}

func openLibrary(libDir string) (*library, error) {
	return nil, fmt.Errorf("whisper transcription requires cgo and Linux; this build does not support it")
}
