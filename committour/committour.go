// Package committour parses and stores guided tours of Git commits.
package committour

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
)

const notesRef = "shelley-tour"

type Tour struct {
	Version int    `json:"version"`
	Title   string `json:"title,omitempty"`
	Intro   string `json:"intro,omitempty"`
	// Decisions are the key design decisions behind the commit.
	Decisions []TourItem `json:"decisions,omitempty"`
	// Questions are open questions for the reader to answer.
	Questions []TourItem  `json:"questions,omitempty"`
	Chunks    []TourChunk `json:"chunks"`
}

// TourItem is a decision or question: a one-line title with an optional
// body, both markdown.
type TourItem struct {
	Title string `json:"title"`
	Body  string `json:"body,omitempty"`
}

// TourChunk is one narrative entry: a markdown section header, a
// self-contained patch, a reference to a suggested chunk id (resolved to a
// patch by Resolve), or an image or recording. Media names a file to embed;
// ResolveMedia replaces it with its Blob and sniffed MIME type, and defaults
// Name, the label readers see, to the file's base name.
type TourChunk struct {
	Header  string `json:"header,omitempty"`
	Patch   string `json:"patch,omitempty"`
	Ref     *int   `json:"ref,omitempty"`
	Media   string `json:"media,omitempty"`
	Blob    string `json:"blob,omitempty"`
	MIME    string `json:"mime,omitempty"`
	Name    string `json:"name,omitempty"`
	Comment string `json:"comment,omitempty"`
	Trivial bool   `json:"trivial,omitempty"`
}

// FragmentMeta describes a patch fragment for compact listings.
type FragmentMeta struct {
	File string // new path (or old path for deletions)
	Hunk string // "@@ ... @@" header, empty for hunkless fragments
	Adds int
	Dels int
}

// Meta extracts listing metadata from a patch fragment. File headers are only
// honored before the first hunk: added lines whose content begins with "++ "
// or "-- " would otherwise masquerade as +++/--- headers.
func Meta(fragment string) FragmentMeta {
	var m FragmentMeta
	var oldFile string
	for _, line := range splitLines(fragment) {
		switch {
		case strings.HasPrefix(line, "@@"):
			if m.Hunk == "" {
				m.Hunk = strings.TrimSuffix(line, "\n")
			}
		case m.Hunk != "":
			if strings.HasPrefix(line, "+") {
				m.Adds++
			} else if strings.HasPrefix(line, "-") {
				m.Dels++
			}
		case strings.HasPrefix(line, "+++ "):
			m.File = cutPathPrefix(strings.TrimSuffix(line[4:], "\n"), "b/")
		case strings.HasPrefix(line, "--- "):
			oldFile = cutPathPrefix(strings.TrimSuffix(line[4:], "\n"), "a/")
		case strings.HasPrefix(line, "rename to "), strings.HasPrefix(line, "copy to "):
			if m.File == "" {
				_, m.File, _ = strings.Cut(strings.TrimSuffix(line, "\n"), " to ")
			}
		}
	}
	if m.File == "" || m.File == "/dev/null" {
		m.File = oldFile
	}
	if m.File == "" {
		// Fall back to the "diff --git a/x b/y" line (e.g. binary changes).
		for _, line := range splitLines(fragment) {
			if rest, ok := strings.CutPrefix(line, "diff --git a/"); ok {
				if _, after, found := strings.Cut(strings.TrimSuffix(rest, "\n"), " b/"); found {
					m.File = after
				}
				break
			}
		}
	}
	return m
}

func cutPathPrefix(path, prefix string) string {
	if rest, ok := strings.CutPrefix(path, prefix); ok {
		return rest
	}
	return path
}

// Subject returns the commit's subject line.
func Subject(dir, commit string) (string, error) {
	out, err := gitOutput(dir, "", nil, "log", "-1", "--format=%s", commit+"^{commit}", "--")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// Resolve replaces ref entries with the referenced suggested patches for the
// given commit. It reports whether any refs were resolved. Tours without refs
// are returned unchanged.
func Resolve(dir, commit string, tour *Tour) (bool, error) {
	if tour == nil {
		return false, errors.New("tour is nil")
	}
	var refs []int
	for i, entry := range tour.Chunks {
		if entry.Ref == nil {
			continue
		}
		if entry.Header != "" || entry.Patch != "" || entry.Media != "" || entry.Blob != "" {
			return false, fmt.Errorf("chunks[%d] has ref alongside another entry kind", i)
		}
		refs = append(refs, i)
	}
	if len(refs) == 0 {
		return false, nil
	}
	_, fragments, err := Chunks(dir, commit)
	if err != nil {
		return false, err
	}
	for _, i := range refs {
		id := *tour.Chunks[i].Ref
		if len(fragments) == 0 {
			return false, fmt.Errorf("chunks[%d] references chunk %d, but the commit has no chunks", i, id)
		}
		if id < 0 || id >= len(fragments) {
			return false, fmt.Errorf("chunks[%d] references chunk %d, but the commit has chunks 0..%d", i, id, len(fragments)-1)
		}
		tour.Chunks[i].Patch = fragments[id]
		tour.Chunks[i].Ref = nil
	}
	return true, nil
}

// CommitHash resolves rev to a full commit hash.
func CommitHash(dir, rev string) (string, error) {
	hashBytes, err := gitOutput(dir, "", nil, "rev-parse", rev+"^{commit}")
	if err != nil {
		return "", err
	}
	hash := strings.TrimSpace(string(hashBytes))
	if hash == "" || strings.ContainsAny(hash, "\r\n") {
		return "", fmt.Errorf("git rev-parse returned invalid hash %q", hash)
	}
	return hash, nil
}

// Chunks returns the full commit hash and one suggested patch fragment per hunk.
func Chunks(dir, commit string) (string, []string, error) {
	hash, err := CommitHash(dir, commit)
	if err != nil {
		return "", nil, err
	}

	diff, err := gitOutput(
		dir, "", nil,
		"-c", "diff.noprefix=false",
		"-c", "diff.mnemonicPrefix=false",
		"-c", "diff.srcPrefix=a/",
		"-c", "diff.dstPrefix=b/",
		"-c", "diff.submodule=short",
		"-c", "diff.ignoreSubmodules=none",
		"-c", "log.showRoot=true",
		"show", hash,
		"--format=", "--no-color", "--no-show-signature", "--binary",
		"--no-ext-diff", "--no-textconv", "--find-renames",
		"--diff-merges=first-parent", "-O/dev/null",
	)
	if err != nil {
		return "", nil, err
	}
	fragments, err := splitDiff(string(diff))
	if err != nil {
		return "", nil, err
	}
	return hash, fragments, nil
}

func splitDiff(diff string) ([]string, error) {
	lines := splitLines(diff)
	files := make([][]string, 0)
	for _, line := range lines {
		if strings.HasPrefix(line, "diff --git ") {
			files = append(files, nil)
		}
		if len(files) == 0 {
			if strings.TrimSpace(line) != "" {
				return nil, fmt.Errorf("unexpected diff content before file header: %q", strings.TrimSpace(line))
			}
			continue
		}
		files[len(files)-1] = append(files[len(files)-1], line)
	}

	fragments := make([]string, 0)
	for _, file := range files {
		var hunks []int
		renamed := false
		for i, line := range file {
			if strings.HasPrefix(line, "@@") {
				hunks = append(hunks, i)
			}
			if len(hunks) == 0 && (strings.HasPrefix(line, "rename from ") || strings.HasPrefix(line, "copy from ")) {
				renamed = true
			}
		}
		// Renames and copies stay whole: later hunks target a path that
		// does not exist until the fragment carrying the rename applies,
		// and repeating the rename metadata per hunk breaks git apply.
		if len(hunks) == 0 || renamed {
			fragments = append(fragments, strings.Join(file, ""))
			continue
		}
		header := strings.Join(file[:hunks[0]], "")
		for i, start := range hunks {
			end := len(file)
			if i+1 < len(hunks) {
				end = hunks[i+1]
			}
			fragments = append(fragments, header+strings.Join(file[start:end], ""))
		}
	}
	return fragments, nil
}

func ParseTour(data []byte) (*Tour, error) {
	var tour *Tour
	if err := json.Unmarshal(data, &tour); err != nil {
		return nil, fmt.Errorf("parse tour: %w", err)
	}
	if tour == nil {
		return nil, errors.New("parse tour: expected JSON object")
	}
	return tour, nil
}

// Verify applies the tour patches to a temporary index containing the commit's
// first-parent tree and requires the resulting tree to equal the commit tree.
// Chunk-index references are resolved against the commit's suggested chunks.
func Verify(dir, commit string, tour *Tour) ([]string, error) {
	if _, err := Resolve(dir, commit, tour); err != nil {
		return nil, err
	}
	warnings, patches, err := validateTour(tour)
	if err != nil {
		return warnings, err
	}

	tmp, err := os.MkdirTemp("", "shelley-commit-tour-*")
	if err != nil {
		return warnings, fmt.Errorf("create temporary directory: %w", err)
	}
	defer os.RemoveAll(tmp)
	index := filepath.Join(tmp, "index")

	hashBytes, err := gitOutput(dir, index, nil, "rev-parse", commit+"^{commit}")
	if err != nil {
		return warnings, err
	}
	hash := strings.TrimSpace(string(hashBytes))
	wantBytes, err := gitOutput(dir, index, nil, "rev-parse", hash+"^{tree}")
	if err != nil {
		return warnings, err
	}
	want := strings.TrimSpace(string(wantBytes))

	// Determine the base tree: the first parent, or the empty tree for a
	// parentless commit. Read parents from the raw commit object; traversal
	// commands hide parents at shallow-clone boundaries.
	rawBytes, err := gitOutput(dir, index, nil, "cat-file", "commit", hash)
	if err != nil {
		return warnings, err
	}
	var parentHashes []string
	for _, line := range strings.Split(string(rawBytes), "\n") {
		if line == "" {
			break // end of commit header
		}
		if p, ok := strings.CutPrefix(line, "parent "); ok {
			parentHashes = append(parentHashes, p)
		}
	}
	var parent string
	if len(parentHashes) == 0 {
		emptyBytes, err := gitOutput(dir, index, nil, "hash-object", "-t", "tree", os.DevNull)
		if err != nil {
			return warnings, err
		}
		parent = strings.TrimSpace(string(emptyBytes))
	} else {
		parentBytes, err := gitOutput(dir, index, nil, "rev-parse", parentHashes[0]+"^{tree}")
		if err != nil {
			return warnings, err
		}
		parent = strings.TrimSpace(string(parentBytes))
	}

	if _, err := gitOutput(dir, index, nil, "read-tree", parent); err != nil {
		return warnings, err
	}
	if len(patches) > 0 {
		var patch strings.Builder
		for _, p := range patches {
			patch.WriteString(p)
			if !strings.HasSuffix(p, "\n") {
				patch.WriteString("\n")
			}
		}
		if _, err := gitOutput(dir, index, strings.NewReader(patch.String()),
			"apply", "--cached", "--unidiff-zero", "--binary", "--whitespace=nowarn", "-"); err != nil {
			return warnings, err
		}
	}
	gotBytes, err := gitOutput(dir, index, nil, "write-tree")
	if err != nil {
		return warnings, err
	}
	got := strings.TrimSpace(string(gotBytes))
	if got != want {
		return warnings, fmt.Errorf("tour produces tree %s, want %s", got, want)
	}
	return warnings, nil
}

func validateTour(tour *Tour) ([]string, []string, error) {
	if tour == nil {
		return nil, nil, errors.New("tour is nil")
	}
	if tour.Version != 1 {
		return nil, nil, fmt.Errorf("version is %d, want 1", tour.Version)
	}
	for _, list := range []struct {
		name  string
		items []TourItem
	}{{"decisions", tour.Decisions}, {"questions", tour.Questions}} {
		for i, item := range list.items {
			if strings.TrimSpace(item.Title) == "" {
				return nil, nil, fmt.Errorf("%s[%d] has an empty title", list.name, i)
			}
			if strings.ContainsAny(item.Title, "\r\n") {
				return nil, nil, fmt.Errorf("%s[%d] title spans lines; move detail into body", list.name, i)
			}
		}
	}
	var warnings, patches []string
	for i, entry := range tour.Chunks {
		kinds := 0
		for _, set := range []bool{entry.Header != "", entry.Patch != "", entry.Media != "", entry.Blob != ""} {
			if set {
				kinds++
			}
		}
		if kinds != 1 {
			return warnings, nil, fmt.Errorf("chunks[%d] must have exactly one of header, patch, and media", i)
		}
		if entry.Blob == "" && (entry.MIME != "" || entry.Name != "" && entry.Media == "") {
			return warnings, nil, fmt.Errorf("chunks[%d] has mime or name but is not media", i)
		}
		if strings.ContainsAny(entry.Name, "\r\n") {
			return warnings, nil, fmt.Errorf("chunks[%d] media name spans lines", i)
		}
		switch {
		case entry.Header != "":
			if strings.TrimSpace(entry.Header) == "" {
				return warnings, nil, fmt.Errorf("chunks[%d] has an empty header", i)
			}
		case entry.Media != "":
			return warnings, nil, fmt.Errorf("chunks[%d] has unresolved media %q", i, entry.Media)
		case entry.Blob != "":
			if !validBlobHash(entry.Blob) {
				return warnings, nil, fmt.Errorf("chunks[%d] blob %q is not a full object hash", i, entry.Blob)
			}
			if !mediaTypes[entry.MIME] {
				return warnings, nil, fmt.Errorf("chunks[%d] has unsupported media type %q", i, entry.MIME)
			}
			if strings.TrimSpace(entry.Name) == "" {
				return warnings, nil, fmt.Errorf("chunks[%d] media has no name", i)
			}
			if entry.Trivial {
				return warnings, nil, fmt.Errorf("chunks[%d] media cannot be trivial", i)
			}
			if strings.TrimSpace(entry.Comment) == "" {
				warnings = append(warnings, fmt.Sprintf("chunks[%d] (%s) is media but has no comment", i, entry.Name))
			}
		default:
			if strings.TrimSpace(entry.Patch) == "" {
				return warnings, nil, fmt.Errorf("chunks[%d] has an empty patch", i)
			}
			patches = append(patches, entry.Patch)
			if !entry.Trivial && strings.TrimSpace(entry.Comment) == "" {
				meta := Meta(entry.Patch)
				warnings = append(warnings, fmt.Sprintf("chunks[%d] (%s %s) is non-trivial but has no comment", i, meta.File, meta.Hunk))
			}
		}
	}
	return warnings, patches, nil
}

// ErrNoNote reports that a commit has no tour note attached.
var ErrNoNote = errors.New("no tour note")

func ReadNote(dir, commit string) ([]byte, error) {
	data, err := gitOutput(dir, "", nil, "notes", "--ref="+notesRef, "show", commit)
	if err != nil {
		if strings.Contains(err.Error(), "no note found") {
			return nil, fmt.Errorf("%w for %s", ErrNoNote, commit)
		}
		return nil, err
	}
	return data, nil
}

// WriteNote stores data as commit's tour note. The given media blobs are
// pinned in the same update of the notes ref (see pinnedIn).
func WriteNote(dir, commit string, data []byte, media ...string) error {
	file, err := os.CreateTemp("", "shelley-tour-*.json")
	if err != nil {
		return fmt.Errorf("create tour note file: %w", err)
	}
	name := file.Name()
	defer os.Remove(name)
	if _, err := file.Write(data); err != nil {
		file.Close()
		return fmt.Errorf("write tour note file: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close tour note file: %w", err)
	}
	return updateNotes(dir, commit, name, media)
}

// updateNotes adds noteFile as commit's note and pins media, atomically.
// git notes overwrites its ref without checking the old value, so
// concurrent writers (parallel subagents annotating different commits)
// would silently drop each other's notes. Instead the edits run against a
// private copy of the ref, which is then compare-and-swapped in; on
// contention the whole update is redone from the new value.
func updateNotes(dir, commit, noteFile string, media []string) error {
	deadline := time.Now().Add(15 * time.Second)
	backoff := 10 * time.Millisecond
	for {
		err := tryUpdateNotes(dir, commit, noteFile, media)
		if err == nil || time.Now().After(deadline) || !strings.Contains(err.Error(), "cannot lock ref") {
			return err
		}
		// Jitter keeps writers that lost the same round from colliding again.
		time.Sleep(backoff/2 + rand.N(backoff))
		backoff = min(2*backoff, time.Second)
	}
}

// tmpNotesRefs numbers this process's private notes refs.
var tmpNotesRefs atomic.Int64

func tryUpdateNotes(dir, commit, noteFile string, media []string) error {
	ref := "refs/notes/" + notesRef
	out, err := gitOutput(dir, "", nil, "for-each-ref", "--format=%(objectname)", ref)
	if err != nil {
		return err
	}
	old := strings.TrimSpace(string(out))
	// Unique even against refs left behind by a crashed process that had
	// this pid.
	tmp := fmt.Sprintf("%s-tmp-%d-%d-%d", notesRef, os.Getpid(), time.Now().UnixNano(), tmpNotesRefs.Add(1))
	defer gitOutput(dir, "", nil, "update-ref", "-d", "refs/notes/"+tmp)
	if old != "" {
		if _, err := gitOutput(dir, "", nil, "update-ref", "refs/notes/"+tmp, old); err != nil {
			return err
		}
	}
	for _, blob := range media {
		pinned, err := pinnedIn(dir, tmp, blob)
		if err != nil {
			return err
		}
		if pinned {
			continue
		}
		if _, err := gitOutput(dir, "", nil, "notes", "--ref="+tmp, "add", "-f", "-C", blob, blob); err != nil {
			return err
		}
	}
	if _, err := gitOutput(dir, "", nil, "notes", "--ref="+tmp, "add", "-f", "-F", noteFile, commit); err != nil {
		return err
	}
	out, err = gitOutput(dir, "", nil, "rev-parse", "refs/notes/"+tmp)
	if err != nil {
		return err
	}
	updated := strings.TrimSpace(string(out))
	// If something deleted the private ref midway, the edits started from an
	// empty tree; swapping that in would drop every other note.
	if old != "" {
		if _, err := gitOutput(dir, "", nil, "merge-base", "--is-ancestor", old, updated); err != nil {
			return fmt.Errorf("notes update lost its base %s: %w", old, err)
		}
	}
	// An empty old value requires that the ref still not exist.
	_, err = gitOutput(dir, "", nil, "update-ref", ref, updated, old)
	return err
}

func ListNotes(dir string) (map[string]bool, error) {
	output, err := gitOutput(dir, "", nil, "notes", "--ref="+notesRef, "list")
	if err != nil {
		return nil, err
	}
	notes := make(map[string]bool)
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 {
			return nil, fmt.Errorf("invalid git notes list line %q", line)
		}
		notes[fields[1]] = true
	}
	return notes, nil
}

// gitOutput runs git -C dir with a sanitized environment: repo-locating
// variables are stripped so dir always wins, and index (if non-empty) is used
// as GIT_INDEX_FILE so plumbing never touches the real index.
func gitOutput(dir, index string, stdin io.Reader, args ...string) ([]byte, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = gitEnv(index)
	if stdin != nil {
		cmd.Stdin = stdin
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			return nil, fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
		}
		return nil, fmt.Errorf("git %s: %s", strings.Join(args, " "), message)
	}
	return output, nil
}

func gitEnv(index string) []string {
	env := make([]string, 0, len(os.Environ())+1)
	for _, value := range os.Environ() {
		name, _, _ := strings.Cut(value, "=")
		switch name {
		case "GIT_INDEX_FILE", "GIT_DIR", "GIT_WORK_TREE", "GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES":
			continue
		}
		env = append(env, value)
	}
	if index != "" {
		env = append(env, "GIT_INDEX_FILE="+index)
	}
	// Errors are matched on their English text.
	return append(env, "LC_ALL=C")
}

func splitLines(text string) []string {
	if text == "" {
		return nil
	}
	lines := strings.SplitAfter(text, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}
