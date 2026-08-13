package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// findFiles issues a GET /api/find-files and decodes the response.
func findFiles(t *testing.T, h *TestHarness, dir, query string) FindFilesResponse {
	t.Helper()
	u := "/api/find-files?dir=" + url.QueryEscape(dir)
	if query != "" {
		u += "&q=" + url.QueryEscape(query)
	}
	req := httptest.NewRequest(http.MethodGet, u, nil)
	w := httptest.NewRecorder()
	h.server.handleFindFiles(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("find-files %q %q: expected 200, got %d: %s", dir, query, w.Code, w.Body.String())
	}
	var resp FindFilesResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return resp
}

func hasPath(matches []FindFilesMatch, p string) bool {
	for _, m := range matches {
		if m.Path == p {
			return true
		}
	}
	return false
}

// inRun reports whether idx falls in [start, start+length), with start < 0
// ("not found") never matching.
func inRun(idx, start, length int) bool {
	return start >= 0 && idx >= start && idx < start+length
}

// TestFindFilesGitRepo verifies fuzzy file finding inside a git repo honors
// .gitignore, ranks by the query, and returns highlight indexes.
func TestFindFilesGitRepo(t *testing.T) {
	t.Parallel()
	h := NewTestHarness(t)

	dir := t.TempDir()
	mustGitInit(t, dir)
	writeFile(t, filepath.Join(dir, "main.go"), "package main\n")
	writeFile(t, filepath.Join(dir, "README.md"), "# hi\n")
	writeFile(t, filepath.Join(dir, "internal", "server", "handler.go"), "package server\n")
	writeFile(t, filepath.Join(dir, ".gitignore"), "ignored.txt\n")
	writeFile(t, filepath.Join(dir, "ignored.txt"), "secret\n")

	t.Run("no_query_lists_files", func(t *testing.T) {
		resp := findFiles(t, h, dir, "")
		if !hasPath(resp.Matches, "main.go") {
			t.Errorf("expected main.go in %+v", resp.Matches)
		}
		if hasPath(resp.Matches, "ignored.txt") {
			t.Errorf("gitignored file should not appear: %+v", resp.Matches)
		}
	})

	t.Run("fuzzy_query", func(t *testing.T) {
		resp := findFiles(t, h, dir, "handler")
		if len(resp.Matches) == 0 || resp.Matches[0].Path != "internal/server/handler.go" {
			t.Errorf("expected handler.go ranked first, got %+v", resp.Matches)
		}
		if len(resp.Matches[0].MatchedIndexes) == 0 {
			t.Errorf("expected match highlight indexes, got none")
		}
	})

	t.Run("subsequence_match", func(t *testing.T) {
		resp := findFiles(t, h, dir, "mgo")
		if !hasPath(resp.Matches, "main.go") {
			t.Errorf("expected fuzzy subsequence match main.go, got %+v", resp.Matches)
		}
	})
}

// TestFindFilesMultiTermQuery verifies a space-separated query ANDs its terms:
// each term is fuzzy-matched independently (in any order) against the path, so
// "vm storage s3" finds vm-storage-s3-backup-design.md.
func TestFindFilesMultiTermQuery(t *testing.T) {
	t.Parallel()
	h := NewTestHarness(t)

	dir := t.TempDir()
	mustGitInit(t, dir)
	s3 := "exelet/docs/vm-storage-s3-backup-design.md"
	for _, p := range []string{
		s3,
		"exelet/docs/vm-storage-replication.md",
		"exelet/docs/vm-storage-manual-resize.md",
		"exelet/docs/s3-uploads.md",
	} {
		writeFile(t, filepath.Join(dir, filepath.FromSlash(p)), "x\n")
	}

	t.Run("terms_are_anded", func(t *testing.T) {
		resp := findFiles(t, h, dir, "vm storage s3")
		if len(resp.Matches) != 1 || resp.Matches[0].Path != s3 {
			t.Fatalf("expected only %s, got %+v", s3, resp.Matches)
		}
		if len(resp.Matches[0].MatchedIndexes) == 0 {
			t.Errorf("expected highlight indexes, got none")
		}
		runes := []rune(resp.Matches[0].Path)
		for _, idx := range resp.Matches[0].MatchedIndexes {
			if idx < 0 || idx >= len(runes) {
				t.Fatalf("match index %d out of range [0,%d)", idx, len(runes))
			}
		}
	})

	t.Run("highlights_literal_runs", func(t *testing.T) {
		resp := findFiles(t, h, dir, "vm storage s3")
		if len(resp.Matches) != 1 {
			t.Fatalf("expected 1 match, got %+v", resp.Matches)
		}
		// Each term should underline its literal occurrence, not a scattered
		// subsequence (fuzzy alone highlights the 'm' of ".md" for "vm").
		// The fixture path is ASCII, so rune offsets == index offsets here;
		// TestFindFilesMultibyteHighlight covers the non-ASCII case.
		path := resp.Matches[0].Path
		for _, term := range []string{"vm", "storage", "s3"} {
			start := strings.Index(path, term)
			if start < 0 {
				t.Fatalf("term %q not in %q", term, path)
			}
			for i := start; i < start+len(term); i++ {
				if !slices.Contains(resp.Matches[0].MatchedIndexes, i) {
					t.Errorf("expected index %d (term %q) highlighted, got %v", i, term, resp.Matches[0].MatchedIndexes)
				}
			}
		}
		// Nothing outside the three terms should light up.
		for _, idx := range resp.Matches[0].MatchedIndexes {
			switch {
			case inRun(idx, strings.Index(path, "vm"), 2),
				inRun(idx, strings.Index(path, "storage"), 7),
				inRun(idx, strings.Index(path, "s3"), 2):
			default:
				t.Errorf("unrelated index %d highlighted in %q (all: %v)", idx, path, resp.Matches[0].MatchedIndexes)
			}
		}
	})

	t.Run("repeated_term_scores_once", func(t *testing.T) {
		// A repeated term must not inflate the score (or the highlights).
		one := findFiles(t, h, dir, "vm storage s3")
		twice := findFiles(t, h, dir, "vm storage s3 storage")
		if len(twice.Matches) != 1 || twice.Matches[0].Path != one.Matches[0].Path {
			t.Fatalf("expected the same single match, got %+v", twice.Matches)
		}
		if !slices.Equal(one.Matches[0].MatchedIndexes, twice.Matches[0].MatchedIndexes) {
			t.Errorf("highlights differ: %v vs %v", one.Matches[0].MatchedIndexes, twice.Matches[0].MatchedIndexes)
		}
	})

	t.Run("terms_out_of_order", func(t *testing.T) {
		resp := findFiles(t, h, dir, "s3 storage")
		if len(resp.Matches) != 1 || resp.Matches[0].Path != s3 {
			t.Fatalf("expected only %s, got %+v", s3, resp.Matches)
		}
	})

	t.Run("trailing_space_ignored", func(t *testing.T) {
		resp := findFiles(t, h, dir, "vm-storage ")
		if len(resp.Matches) != 3 {
			t.Fatalf("expected 3 vm-storage matches, got %+v", resp.Matches)
		}
	})

	t.Run("unmatched_term_excludes", func(t *testing.T) {
		resp := findFiles(t, h, dir, "vm storage zzzz")
		if len(resp.Matches) != 0 {
			t.Fatalf("expected no matches, got %+v", resp.Matches)
		}
	})
}

// TestFindFilesMultiTermRanking verifies a multi-term query doesn't punish long
// paths once per term. sahilm/fuzzy charges "unmatched characters" per call, so
// naively summing per-term scores buries the well-separated deep path under a
// short run-together name — exactly the file the user was aiming for.
func TestFindFilesMultiTermRanking(t *testing.T) {
	t.Parallel()
	h := NewTestHarness(t)

	dir := t.TempDir()
	mustGitInit(t, dir)
	want := "exelet/docs/vm-storage-s3-backup-design.md"
	for _, p := range []string{want, "a/vmstorages3.md"} {
		writeFile(t, filepath.Join(dir, filepath.FromSlash(p)), "x\n")
	}

	resp := findFiles(t, h, dir, "vm storage s3")
	if len(resp.Matches) != 2 {
		t.Fatalf("expected both files to match, got %+v", resp.Matches)
	}
	if resp.Matches[0].Path != want {
		t.Errorf("expected %q ranked first, got %+v", want, resp.Matches)
	}
}

// TestFindFilesDuplicatePaths verifies a path listed more than once (as
// `git ls-files` does for each stage of an unresolved merge conflict) yields a
// single result row, for both the single- and multi-term paths. The UI keys its
// list on the path, so duplicates would collide.
func TestFindFilesDuplicatePaths(t *testing.T) {
	t.Parallel()

	dup := []string{"a/vm-storage.md", "a/vm-storage.md", "b/other.md"}
	for _, query := range []string{"vm-storage", "vm storage"} {
		matches := findFuzzyMulti(query, dup)
		count := 0
		for _, m := range matches {
			if m.str == "a/vm-storage.md" {
				count++
			}
		}
		if count != 1 {
			t.Errorf("query %q: got %d rows for the duplicated path, want 1 (%+v)", query, count, matches)
		}
	}

	// A duplicate must not double-score the path either, which would let it
	// outrank a better match.
	single := findFuzzyMulti("vm storage", []string{"a/vm-storage.md"})
	doubled := findFuzzyMulti("vm storage", []string{"a/vm-storage.md", "a/vm-storage.md"})
	if len(single) != 1 || len(doubled) != 1 {
		t.Fatalf("expected one match each, got %+v and %+v", single, doubled)
	}
	if single[0].score != doubled[0].score {
		t.Errorf("duplicate changed the score: %d vs %d", single[0].score, doubled[0].score)
	}
}

// TestFindFilesMultibyteHighlight verifies match indexes are rune (not byte)
// offsets so the UI highlights the right characters in non-ASCII paths.
func TestFindFilesMultibyteHighlight(t *testing.T) {
	t.Parallel()
	h := NewTestHarness(t)

	dir := t.TempDir()
	mustGitInit(t, dir)
	// "café" is 5 bytes (é = 2 bytes) but 4 runes, so a byte offset for the
	// "m" in main.go (byte 6) differs from its rune offset (5).
	writeFile(t, filepath.Join(dir, "café", "main.go"), "package main\n")

	resp := findFiles(t, h, dir, "main")
	if len(resp.Matches) == 0 {
		t.Fatalf("expected a match, got none")
	}
	m := resp.Matches[0]
	if m.Path != "café/main.go" {
		t.Fatalf("unexpected path %q", m.Path)
	}
	runes := []rune(m.Path)
	for _, idx := range m.MatchedIndexes {
		if idx < 0 || idx >= len(runes) {
			t.Fatalf("match index %d out of rune range [0,%d)", idx, len(runes))
		}
	}
	if len(m.MatchedIndexes) == 0 || runes[m.MatchedIndexes[0]] != 'm' {
		t.Errorf("expected first match index to point at 'm', got indexes %v in %q", m.MatchedIndexes, m.Path)
	}

	// Multi-term queries union the per-term highlights, and a term can itself be
	// multibyte: refineHighlights works in bytes, so this is where a term's
	// interior byte offsets could leak through as bogus rune offsets.
	resp = findFiles(t, h, dir, "café main")
	if len(resp.Matches) != 1 {
		t.Fatalf("expected 1 match for the multi-term query, got %+v", resp.Matches)
	}
	m = resp.Matches[0]
	runes = []rune(m.Path)
	var highlighted string
	for _, idx := range m.MatchedIndexes {
		if idx < 0 || idx >= len(runes) {
			t.Fatalf("match index %d out of rune range [0,%d)", idx, len(runes))
		}
		highlighted += string(runes[idx])
	}
	if highlighted != "cafémain" {
		t.Errorf("highlighted runes = %q, want %q (indexes %v in %q)", highlighted, "cafémain", m.MatchedIndexes, m.Path)
	}
}

// TestFindFilesCacheNoPoisonOnFailure verifies a failed listing isn't cached,
// so a subsequent successful request still sees the files.
func TestFindFilesCacheNoPoisonOnFailure(t *testing.T) {
	t.Parallel()
	c := newFileListCache()

	files, _ := c.get("/some/dir", func() ([]string, bool, bool) {
		return nil, false, false // ok=false: must not be cached
	})
	if len(files) != 0 {
		t.Fatalf("expected empty result from failed load, got %v", files)
	}
	if _, ok := c.entries["/some/dir"]; ok {
		t.Fatalf("failed load should not be cached")
	}

	files, _ = c.get("/some/dir", func() ([]string, bool, bool) {
		return []string{"a.go", "b.go"}, false, true
	})
	if len(files) != 2 {
		t.Fatalf("expected 2 files after successful load, got %v", files)
	}
	if _, ok := c.entries["/some/dir"]; !ok {
		t.Fatalf("successful load should be cached")
	}
}

// TestFileListCacheEviction verifies the cache stays under its dir cap.
func TestFileListCacheEviction(t *testing.T) {
	t.Parallel()
	c := newFileListCache()
	for i := 0; i < fileListCacheMaxDirs*2; i++ {
		dir := fmt.Sprintf("/dir/%d", i)
		c.get(dir, func() ([]string, bool, bool) { return []string{"x"}, false, true })
	}
	if len(c.entries) > fileListCacheMaxDirs {
		t.Errorf("cache exceeded cap: %d > %d", len(c.entries), fileListCacheMaxDirs)
	}
}

// TestFindFilesNonGit verifies the filesystem-walk fallback works outside a
// git repo and skips heavy directories.
func TestFindFilesNonGit(t *testing.T) {
	t.Parallel()
	h := NewTestHarness(t)

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "alpha.txt"), "a\n")
	writeFile(t, filepath.Join(dir, "sub", "beta.txt"), "b\n")
	writeFile(t, filepath.Join(dir, "node_modules", "junk.js"), "junk\n")

	resp := findFiles(t, h, dir, "")
	if !hasPath(resp.Matches, "alpha.txt") || !hasPath(resp.Matches, "sub/beta.txt") {
		t.Errorf("expected alpha.txt and sub/beta.txt, got %+v", resp.Matches)
	}
	if hasPath(resp.Matches, "node_modules/junk.js") {
		t.Errorf("node_modules should be skipped: %+v", resp.Matches)
	}
}

// TestFindFilesBadRequests verifies input validation.
func TestFindFilesBadRequests(t *testing.T) {
	t.Parallel()
	h := NewTestHarness(t)

	t.Run("relative_dir", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/find-files?dir=relative/path", nil)
		w := httptest.NewRecorder()
		h.server.handleFindFiles(w, req)
		if w.Code != http.StatusBadRequest {
			t.Errorf("expected 400, got %d", w.Code)
		}
	})

	t.Run("missing_dir", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/find-files?dir=/nonexistent/xyz/123", nil)
		w := httptest.NewRecorder()
		h.server.handleFindFiles(w, req)
		if w.Code != http.StatusBadRequest {
			t.Errorf("expected 400, got %d", w.Code)
		}
	})

	t.Run("method_not_allowed", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/find-files?dir=/tmp", nil)
		w := httptest.NewRecorder()
		h.server.handleFindFiles(w, req)
		if w.Code != http.StatusMethodNotAllowed {
			t.Errorf("expected 405, got %d", w.Code)
		}
	})
}

// TestHandleReadFile verifies the arbitrary-text-file read endpoint.
func TestHandleReadFile(t *testing.T) {
	t.Parallel()
	h := NewTestHarness(t)

	dir := t.TempDir()
	path := filepath.Join(dir, "hello.txt")
	writeFile(t, path, "hello world\n")

	t.Run("reads_content", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/read-file?path="+path, nil)
		w := httptest.NewRecorder()
		h.server.handleReadFile(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
		}
		var resp struct {
			Path    string `json:"path"`
			Content string `json:"content"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if resp.Content != "hello world\n" {
			t.Errorf("content = %q", resp.Content)
		}
		if resp.Path != path {
			t.Errorf("path = %q, want %q", resp.Path, path)
		}
	})

	t.Run("relative_rejected", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/read-file?path=relative.txt", nil)
		w := httptest.NewRecorder()
		h.server.handleReadFile(w, req)
		if w.Code != http.StatusBadRequest {
			t.Errorf("expected 400, got %d", w.Code)
		}
	})

	t.Run("missing_file", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/read-file?path="+filepath.Join(dir, "nope.txt"), nil)
		w := httptest.NewRecorder()
		h.server.handleReadFile(w, req)
		if w.Code != http.StatusNotFound {
			t.Errorf("expected 404, got %d", w.Code)
		}
	})

	t.Run("directory_rejected", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/read-file?path="+dir, nil)
		w := httptest.NewRecorder()
		h.server.handleReadFile(w, req)
		if w.Code != http.StatusBadRequest {
			t.Errorf("expected 400, got %d", w.Code)
		}
	})
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mustGitInit(t *testing.T, dir string) {
	t.Helper()
	for _, args := range [][]string{
		{"init"},
		{"config", "user.email", "t@example.com"},
		{"config", "user.name", "t"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
}
