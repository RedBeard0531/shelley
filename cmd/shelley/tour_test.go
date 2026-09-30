package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"shelley.exe.dev/committour"
)

func TestTourCLI(t *testing.T) {
	tempDir := t.TempDir()
	binary := filepath.Join(tempDir, "shelley")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}

	repo := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{
			"-C", repo,
			"-c", "core.hooksPath=/dev/null",
			"-c", "user.name=Test",
			"-c", "user.email=test@example.com",
		}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	git("init", "-q", "-b", "main")
	git("config", "core.hooksPath", "/dev/null")
	if err := os.WriteFile(filepath.Join(repo, "f.txt"), []byte("a\nb\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", "f.txt")
	git("commit", "-q", "-m", "base", "-m", "Prompt: tour CLI fixture")
	if err := os.WriteFile(filepath.Join(repo, "f.txt"), []byte("a\nB\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", "f.txt")
	git("commit", "-q", "-m", "change", "-m", "Prompt: tour CLI fixture")
	hash := strings.TrimSpace(git("rev-parse", "HEAD"))

	run := func(wantExit int, args ...string) string {
		t.Helper()
		out, err := exec.Command(binary, args...).CombinedOutput()
		exit := 0
		if err != nil {
			exitErr, ok := err.(*exec.ExitError)
			if !ok {
				t.Fatalf("shelley %v: %v\n%s", args, err, out)
			}
			exit = exitErr.ExitCode()
		}
		if exit != wantExit {
			t.Fatalf("shelley %v exited %d, want %d\n%s", args, exit, wantExit, out)
		}
		return string(out)
	}

	out := run(0, "tour", "chunks", "-C", repo, "HEAD")
	var response struct {
		Hash    string `json:"hash"`
		Subject string `json:"subject"`
		Chunks  []struct {
			ID    int    `json:"id"`
			File  string `json:"file"`
			Patch string `json:"patch"`
		} `json:"chunks"`
	}
	if err := json.Unmarshal([]byte(out), &response); err != nil {
		t.Fatalf("chunks output: %v\n%s", err, out)
	}
	if response.Hash != hash || response.Subject != "change" || len(response.Chunks) != 1 ||
		response.Chunks[0].Patch == "" || response.Chunks[0].File != "f.txt" || response.Chunks[0].ID != 0 {
		t.Fatalf("chunks output = %s", out)
	}

	indexOut := run(0, "tour", "chunks", "-C", repo, "-index", "HEAD")
	if !strings.Contains(indexOut, "commit "+hash+" change") || !strings.Contains(indexOut, "f.txt") ||
		!strings.Contains(indexOut, "0  +1 -1  @@") || strings.Contains(indexOut, "diff --git") {
		t.Fatalf("index output = %q", indexOut)
	}

	onlyOut := run(0, "tour", "chunks", "-C", repo, "-only", "f.txt", "HEAD")
	if !strings.Contains(onlyOut, `"file": "f.txt"`) {
		t.Fatalf("only output = %q", onlyOut)
	}
	textOut := run(0, "tour", "chunks", "-C", repo, "-text", "HEAD")
	if !strings.HasPrefix(textOut, "=== chunk 0 f.txt\ndiff --git") || strings.Contains(textOut, `\n`) {
		t.Fatalf("text output = %q", textOut)
	}
	run(1, "tour", "chunks", "-C", repo, "-only", "5", "HEAD")
	run(1, "tour", "chunks", "-C", repo, "-only", "nope.txt", "HEAD")
	if got := run(1, "tour", "chunks", "-C", repo, "-only", "3-1", "HEAD"); !strings.Contains(got, "start exceeds end") {
		t.Fatalf("inverted range output = %q", got)
	}
	if got := run(1, "tour", "chunks", "-C", repo, "-only", "0,x", "HEAD"); !strings.Contains(got, "invalid chunk selection") {
		t.Fatalf("malformed selection output = %q", got)
	}

	// scaffold emits a complete, immediately verifiable all-refs tour.
	scaffoldOut := run(0, "tour", "scaffold", "-C", repo, "HEAD")
	var scaffold committour.Tour
	if err := json.Unmarshal([]byte(scaffoldOut), &scaffold); err != nil {
		t.Fatalf("scaffold output: %v\n%s", err, scaffoldOut)
	}
	if scaffold.Title != "change" || len(scaffold.Chunks) != 1 || scaffold.Chunks[0].Ref == nil || *scaffold.Chunks[0].Ref != 0 {
		t.Fatalf("scaffold = %s", scaffoldOut)
	}
	scaffoldPath := filepath.Join(tempDir, "scaffold.json")
	if err := os.WriteFile(scaffoldPath, []byte(scaffoldOut), 0o644); err != nil {
		t.Fatal(err)
	}
	run(0, "tour", "verify", "-C", repo, "HEAD", scaffoldPath)

	tour := &committour.Tour{Version: 1, Title: "T", Chunks: []committour.TourChunk{{
		Patch: response.Chunks[0].Patch, Comment: "explanation",
	}}}
	tourData, err := json.Marshal(tour)
	if err != nil {
		t.Fatal(err)
	}
	tourPath := filepath.Join(tempDir, "tour.json")
	if err := os.WriteFile(tourPath, tourData, 0o644); err != nil {
		t.Fatal(err)
	}

	run(1, "tour", "show", "-C", repo, "HEAD")
	run(0, "tour", "verify", "-C", repo, "HEAD", tourPath)
	run(0, "tour", "attach", "-C", repo, "HEAD", tourPath)
	if got := run(0, "tour", "show", "-C", repo, "HEAD"); strings.TrimSpace(got) != string(tourData) {
		t.Fatalf("show = %q, want %q", got, tourData)
	}

	bad := *tour
	bad.Chunks = append([]committour.TourChunk(nil), tour.Chunks...)
	bad.Chunks[0].Patch = strings.Replace(bad.Chunks[0].Patch, "+B", "+wrong", 1)
	badData, err := json.Marshal(&bad)
	if err != nil {
		t.Fatal(err)
	}
	badPath := filepath.Join(tempDir, "bad.json")
	if err := os.WriteFile(badPath, badData, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := run(1, "tour", "verify", "-C", repo, "HEAD", badPath); !strings.Contains(got, "Error:") {
		t.Fatalf("verify bad output = %q", got)
	}
	run(1, "tour", "attach", "-C", repo, "HEAD", badPath)
	if got := run(0, "tour", "show", "-C", repo, "HEAD"); strings.TrimSpace(got) != string(tourData) {
		t.Fatalf("note overwritten by failing attach: %q", got)
	}

	// Chunk references resolve at verify time and attach stores resolved patches.
	refData := []byte(`{"version":1,"title":"R","chunks":[{"ref":0,"comment":"by reference"}]}`)
	refPath := filepath.Join(tempDir, "ref.json")
	if err := os.WriteFile(refPath, refData, 0o644); err != nil {
		t.Fatal(err)
	}
	run(0, "tour", "verify", "-C", repo, "HEAD", refPath)
	run(0, "tour", "attach", "-C", repo, "HEAD", refPath)
	shown := run(0, "tour", "show", "-C", repo, "HEAD")
	if strings.Contains(shown, `"ref"`) || !strings.Contains(shown, "diff --git") || !strings.Contains(shown, "by reference") {
		t.Fatalf("resolved note = %q", shown)
	}

	// Media paths resolve to blobs; attach pins them so the note is
	// self-contained and the blobs survive gc.
	shotPath := filepath.Join(tempDir, "shot.png")
	if err := os.WriteFile(shotPath, []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"), 0o644); err != nil {
		t.Fatal(err)
	}
	mediaData, err := json.Marshal(map[string]any{
		"version":   1,
		"decisions": []map[string]string{{"title": "Pin media in git", "body": "So the note stands alone."}},
		"questions": []map[string]string{{"title": "Keep the old screen?"}},
		"chunks": []map[string]any{
			{"media": shotPath, "comment": "After."},
			{"ref": 0, "comment": "by reference"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	mediaPath := filepath.Join(tempDir, "media.json")
	if err := os.WriteFile(mediaPath, mediaData, 0o644); err != nil {
		t.Fatal(err)
	}
	run(0, "tour", "verify", "-C", repo, "HEAD", mediaPath)
	shotHash := strings.TrimSpace(git("hash-object", shotPath))
	if got := git("cat-file", "--batch-check", "--batch-all-objects"); strings.Contains(got, shotHash) {
		t.Fatal("verify wrote the media blob")
	}
	run(0, "tour", "attach", "-C", repo, "HEAD", mediaPath)
	shownTour, err := committour.ParseTour([]byte(run(0, "tour", "show", "-C", repo, "HEAD")))
	if err != nil {
		t.Fatal(err)
	}
	shot := shownTour.Chunks[0]
	if shot.Blob != shotHash || shot.Media != "" || shot.MIME != "image/png" || shot.Name != "shot.png" ||
		len(shownTour.Decisions) != 1 || len(shownTour.Questions) != 1 {
		t.Fatalf("media note = %+v", shownTour)
	}
	git("gc", "-q", "--prune=now")
	if data, _, err := committour.ReadMedia(repo, shot.Blob); err != nil || !strings.HasPrefix(string(data), "\x89PNG") {
		t.Fatalf("ReadMedia after attach = %q, %v", data, err)
	}
	unsupportedPath := filepath.Join(tempDir, "unsupported.json")
	if err := os.WriteFile(unsupportedPath, []byte(`{"version":1,"chunks":[{"media":"`+tourPath+`"},{"ref":0}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := run(1, "tour", "verify", "-C", repo, "HEAD", unsupportedPath); !strings.Contains(got, "unsupported type") {
		t.Fatalf("unsupported media output = %q", got)
	}

	badRefData := []byte(`{"version":1,"chunks":[{"ref":7}]}`)
	badRefPath := filepath.Join(tempDir, "badref.json")
	if err := os.WriteFile(badRefPath, badRefData, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := run(1, "tour", "verify", "-C", repo, "HEAD", badRefPath); !strings.Contains(got, "references chunk 7") {
		t.Fatalf("bad ref output = %q", got)
	}
}
