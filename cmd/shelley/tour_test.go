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
		Hash   string `json:"hash"`
		Chunks []struct {
			Patch string `json:"patch"`
		} `json:"chunks"`
	}
	if err := json.Unmarshal([]byte(out), &response); err != nil {
		t.Fatalf("chunks output: %v\n%s", err, out)
	}
	if response.Hash != hash || len(response.Chunks) != 1 || response.Chunks[0].Patch == "" {
		t.Fatalf("chunks output = %s", out)
	}
	if strings.Contains(out, `"index"`) || strings.Contains(out, `"file"`) {
		t.Fatalf("old chunk metadata remains: %s", out)
	}

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
}
