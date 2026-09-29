package committour

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Media blobs are pinned in the tour notes ref itself, each noted with
// itself: the notes tree maps the blob's hash to the blob. That keeps them
// reachable through gc and travels with the tours wherever the ref goes.

// maxMediaBytes caps one embedded file. Pins are permanent (notes history
// keeps them), so recordings should be short.
const maxMediaBytes = 10 << 20

// mediaTypes are the embeddable content types, as sniffed by
// http.DetectContentType. SVG is excluded: it can carry script.
var mediaTypes = map[string]bool{
	"image/png":  true,
	"image/jpeg": true,
	"image/gif":  true,
	"image/webp": true,
	"video/mp4":  true,
	"video/webm": true,
}

// ErrNoMedia reports that a blob is not pinned tour media.
var ErrNoMedia = errors.New("not tour media")

// ErrInvalidMedia reports media that cannot be embedded: a malformed hash,
// an oversized object, or an unsupported type.
var ErrInvalidMedia = errors.New("invalid media")

func sniffMedia(data []byte) (string, error) {
	mime := http.DetectContentType(data)
	if !mediaTypes[mime] {
		return "", fmt.Errorf("%w: unsupported type %s (want PNG, JPEG, GIF, WebP, MP4, or WebM)", ErrInvalidMedia, mime)
	}
	return mime, nil
}

func validBlobHash(hash string) bool {
	if len(hash) != 40 && len(hash) != 64 {
		return false
	}
	return strings.Trim(hash, "0123456789abcdef") == ""
}

// ResolveMedia replaces each media path with the file's blob hash and
// sniffed MIME type, defaulting its name to the file's base name, and checks
// already-resolved media against the object store. It reports whether any
// path was resolved. With write, the blobs are written to the object store,
// where they stay unreachable until WriteNote pins them.
func ResolveMedia(dir string, tour *Tour, write bool) (bool, error) {
	if tour == nil {
		return false, errors.New("tour is nil")
	}
	resolved := false
	for i := range tour.Chunks {
		entry := &tour.Chunks[i]
		if entry.Blob != "" && entry.Media == "" {
			data, err := readMediaBlob(dir, entry.Blob)
			if err != nil {
				return false, fmt.Errorf("chunks[%d]: %w", i, err)
			}
			if mime, err := sniffMedia(data); err != nil || mime != entry.MIME {
				return false, fmt.Errorf("chunks[%d] blob %s is not %s", i, entry.Blob, entry.MIME)
			}
			continue
		}
		if entry.Media == "" {
			continue
		}
		if entry.Header != "" || entry.Patch != "" || entry.Ref != nil || entry.Blob != "" || entry.MIME != "" {
			return false, fmt.Errorf("chunks[%d] has media alongside another entry kind or a mime", i)
		}
		data, err := readMediaFile(entry.Media)
		if err != nil {
			return false, fmt.Errorf("chunks[%d] media: %w", i, err)
		}
		mime, err := sniffMedia(data)
		if err != nil {
			return false, fmt.Errorf("chunks[%d] media %s: %w", i, entry.Media, err)
		}
		args := []string{"hash-object", "--no-filters", "--stdin"}
		if write {
			args = append(args, "-w")
		}
		out, err := gitOutput(dir, "", bytes.NewReader(data), args...)
		if err != nil {
			return false, err
		}
		entry.Blob = strings.TrimSpace(string(out))
		entry.MIME = mime
		if entry.Name == "" {
			entry.Name = filepath.Base(entry.Media)
		}
		entry.Media = ""
		resolved = true
	}
	return resolved, nil
}

// readMediaFile reads a regular file of at most maxMediaBytes, measured as
// read so a file growing after a stat cannot slip past the limit.
func readMediaFile(path string) ([]byte, error) {
	// Stat before opening: opening a FIFO blocks.
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxMediaBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxMediaBytes {
		return nil, fmt.Errorf("%w: %s is over the %d MiB limit", ErrInvalidMedia, path, maxMediaBytes>>20)
	}
	return data, nil
}

// MediaBlobs lists the blobs of a resolved tour's media entries.
func (t *Tour) MediaBlobs() []string {
	var blobs []string
	for _, entry := range t.Chunks {
		if entry.Blob != "" {
			blobs = append(blobs, entry.Blob)
		}
	}
	return blobs
}

// readMediaBlob reads a stored blob of at most maxMediaBytes.
func readMediaBlob(dir, blob string) ([]byte, error) {
	if !validBlobHash(blob) {
		return nil, fmt.Errorf("%w: bad blob hash %q", ErrInvalidMedia, blob)
	}
	out, err := gitOutput(dir, "", strings.NewReader(blob+"\n"), "cat-file", "--batch-check=%(objecttype) %(objectsize)")
	if err != nil {
		return nil, err
	}
	kind, sizeText, _ := strings.Cut(strings.TrimSpace(string(out)), " ")
	if kind != "blob" {
		return nil, fmt.Errorf("media %s is not a stored blob", blob)
	}
	size, err := strconv.ParseInt(sizeText, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("media %s: bad size %q", blob, sizeText)
	}
	if size > maxMediaBytes {
		return nil, fmt.Errorf("%w: %s is over the %d MiB limit", ErrInvalidMedia, blob, maxMediaBytes>>20)
	}
	return gitOutput(dir, "", nil, "cat-file", "blob", blob)
}

// pinnedIn reports whether blob is pinned tour media in the notes ref
// named ref: noted with itself.
func pinnedIn(dir, ref, blob string) (bool, error) {
	out, err := gitOutput(dir, "", nil, "notes", "--ref="+ref, "list", blob)
	if err != nil {
		if strings.Contains(err.Error(), "no note found") {
			return false, nil
		}
		return false, err
	}
	return strings.TrimSpace(string(out)) == blob, nil
}

// ReadMedia returns a pinned media blob and its sniffed content type.
func ReadMedia(dir, blob string) ([]byte, string, error) {
	if !validBlobHash(blob) {
		return nil, "", fmt.Errorf("%w: bad blob hash %q", ErrInvalidMedia, blob)
	}
	pinned, err := pinnedIn(dir, notesRef, blob)
	if err != nil {
		return nil, "", err
	}
	if !pinned {
		return nil, "", fmt.Errorf("%w: %s", ErrNoMedia, blob)
	}
	data, err := readMediaBlob(dir, blob)
	if err != nil {
		return nil, "", err
	}
	mime, err := sniffMedia(data)
	if err != nil {
		return nil, "", err
	}
	return data, mime, nil
}
