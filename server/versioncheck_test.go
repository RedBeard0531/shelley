package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

func TestExtractSHAFromTag(t *testing.T) {
	t.Parallel()
	tests := []struct {
		tag      string
		expected string
	}{
		// Tag format: v0.COUNT.9OCTAL where OCTAL is the SHA in octal
		// For example, 6-char hex SHA "abc123" (hex) = 0xabc123 = 11256099 (decimal)
		// In octal: 52740443
		{"v0.178.952740443", "abc123"}, // SHA abc123 in octal is 52740443
		{"v0.178.933471105", "6e7245"}, // Real release tag
		{"v0.1.90", "000000"},          // SHA 0
		{"", ""},
		{"invalid", ""},
		{"v", ""},
		{"v0", ""},
		{"v0.1", ""},
		{"v0.1.0", ""},  // No '9' prefix
		{"v0.1.8x", ""}, // Invalid octal digit
	}

	for _, tt := range tests {
		t.Run(tt.tag, func(t *testing.T) {
			result := extractSHAFromTag(tt.tag)
			if result != tt.expected {
				t.Errorf("extractSHAFromTag(%q) = %q, want %q", tt.tag, result, tt.expected)
			}
		})
	}
}

func TestParseMinorVersion(t *testing.T) {
	t.Parallel()
	tests := []struct {
		tag      string
		expected int
	}{
		{"v0.1.0", 1},
		{"v0.2.3", 2},
		{"v0.10.5", 10},
		{"v0.100.0", 100},
		{"v1.2.3", 2}, // Should still get minor even with major > 0
		{"", 0},
		{"invalid", 0},
		{"v", 0},
		{"v0", 0},
		{"v0.", 0},
	}

	for _, tt := range tests {
		t.Run(tt.tag, func(t *testing.T) {
			result := parseMinorVersion(tt.tag)
			if result != tt.expected {
				t.Errorf("parseMinorVersion(%q) = %d, want %d", tt.tag, result, tt.expected)
			}
		})
	}
}

func TestIsNewerMinor(t *testing.T) {
	t.Parallel()
	vc := &VersionChecker{}

	tests := []struct {
		name       string
		currentTag string
		latestTag  string
		expected   bool
	}{
		{
			name:       "newer minor version",
			currentTag: "v0.1.0",
			latestTag:  "v0.2.0",
			expected:   true,
		},
		{
			name:       "same version",
			currentTag: "v0.2.0",
			latestTag:  "v0.2.0",
			expected:   false,
		},
		{
			name:       "older version (downgrade)",
			currentTag: "v0.3.0",
			latestTag:  "v0.2.0",
			expected:   false,
		},
		{
			name:       "patch version only",
			currentTag: "v0.2.0",
			latestTag:  "v0.2.5",
			expected:   false, // Minor didn't change
		},
		{
			name:       "multiple minor versions ahead",
			currentTag: "v0.1.0",
			latestTag:  "v0.5.0",
			expected:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := vc.isNewerMinor(tt.currentTag, tt.latestTag)
			if result != tt.expected {
				t.Errorf("isNewerMinor(%q, %q) = %v, want %v",
					tt.currentTag, tt.latestTag, result, tt.expected)
			}
		})
	}
}

func TestVersionCheckerSkipCheck(t *testing.T) {
	t.Setenv("SHELLEY_SKIP_VERSION_CHECK", "true")

	vc := NewVersionChecker()
	if !vc.skipCheck {
		t.Error("Expected skipCheck to be true when SHELLEY_SKIP_VERSION_CHECK=true")
	}

	info, err := vc.Check(t.Context(), false)
	if err != nil {
		t.Errorf("Check() returned error: %v", err)
	}
	if info.HasUpdate {
		t.Error("Expected HasUpdate to be false when skip check is enabled")
	}
}

func TestVersionCheckerCustomized(t *testing.T) {
	t.Setenv("SHELLEY_CUSTOMIZED_OVERRIDE", "true")
	t.Setenv("SHELLEY_SKIP_VERSION_CHECK", "true")
	home := t.TempDir()
	t.Setenv("HOME", home)

	vc := NewVersionChecker()
	info, err := vc.Check(t.Context(), false)
	if err != nil {
		t.Fatalf("Check() returned error: %v", err)
	}
	if !info.Customized {
		t.Error("expected Customized to be true")
	}
	// No checkout on disk: don't advertise a dir the rebase-upgrade
	// conversation can't start in.
	if info.CustomizationDir != "" {
		t.Errorf("expected empty CustomizationDir without a checkout, got %q", info.CustomizationDir)
	}

	dir := CustomizationDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	info, err = vc.Check(t.Context(), false)
	if err != nil {
		t.Fatalf("Check() returned error: %v", err)
	}
	if info.CustomizationDir != dir {
		t.Errorf("expected CustomizationDir %q, got %q", dir, info.CustomizationDir)
	}
}

func TestDoUpgradeRefusesCustomizedBuild(t *testing.T) {
	t.Setenv("SHELLEY_CUSTOMIZED_OVERRIDE", "true")

	vc := &VersionChecker{}
	err := vc.DoUpgrade(t.Context())
	if err == nil {
		t.Fatal("expected DoUpgrade to refuse customized builds")
	}
	if !strings.Contains(err.Error(), "customized") {
		t.Errorf("expected error to mention customized build, got: %v", err)
	}
}

func TestCustomCommits(t *testing.T) {
	t.Parallel()
	if commits := customCommits(""); commits != nil {
		t.Errorf("expected nil for empty dir, got %v", commits)
	}
	if commits := customCommits(t.TempDir()); commits != nil {
		t.Errorf("expected nil for non-repo dir, got %v", commits)
	}

	dir := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-b", "main")
	git("commit", "--allow-empty", "-m", "mainline commit")
	git("update-ref", "refs/remotes/origin/main", "main")
	git("checkout", "-b", "custom")
	git("commit", "--allow-empty", "-m", "custom: first tweak")
	git("commit", "--allow-empty", "-m", "custom: second tweak")

	commits := customCommits(dir)
	if len(commits) != 2 {
		t.Fatalf("expected 2 custom commits, got %d: %v", len(commits), commits)
	}
	if commits[0].Message != "custom: second tweak" || commits[1].Message != "custom: first tweak" {
		t.Errorf("unexpected commit order/messages: %v", commits)
	}
	for _, c := range commits {
		if c.SHA == "" || c.Author != "Test" || c.Date.IsZero() {
			t.Errorf("incomplete commit info: %+v", c)
		}
	}
}

func TestVersionCheckerCache(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// Keep this test serial: the metadata requests use http.DefaultClient.
		oldClient := http.DefaultClient
		t.Cleanup(func() { http.DefaultClient = oldClient })

		requests := 0
		latestTag := "v0.10.0"
		platform := runtime.GOOS + "_" + runtime.GOARCH
		const downloadURL = "https://example.com/shelley"
		http.DefaultClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.Method != http.MethodGet || req.URL.String() != staticMetadataURL+"/release.json" {
				return nil, fmt.Errorf("unexpected metadata request: %s %s", req.Method, req.URL)
			}
			requests++
			body, err := json.Marshal(ReleaseInfo{
				TagName:      latestTag,
				DownloadURLs: map[string]string{platform: downloadURL},
			})
			if err != nil {
				return nil, err
			}
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(body))}, nil
		})}

		vc := &VersionChecker{}
		check := func(force bool, wantTag string, wantRequests int) {
			t.Helper()
			info, err := vc.Check(t.Context(), force)
			if err != nil {
				t.Fatal(err)
			}
			if info.Error != "" {
				t.Fatalf("Check returned error: %s", info.Error)
			}
			if info.LatestTag != wantTag || info.DownloadURL != downloadURL {
				t.Fatalf("Check = tag %q, download %q; want %q, %q", info.LatestTag, info.DownloadURL, wantTag, downloadURL)
			}
			if requests != wantRequests {
				t.Fatalf("metadata requests = %d, want %d", requests, wantRequests)
			}
		}

		check(false, "v0.10.0", 1)
		latestTag = "v0.11.0"
		check(false, "v0.10.0", 1) // A fresh cache must not fetch the newer release.
		check(true, "v0.11.0", 2)  // A forced check must fetch and replace the cache.
		latestTag = "v0.12.0"
		check(false, "v0.11.0", 2)
		time.Sleep(6 * time.Hour)  // synctest advances fake time, not wall time.
		check(false, "v0.12.0", 3) // An expired cache must fetch without forcing.
	})
}

func TestFindDownloadURL(t *testing.T) {
	t.Parallel()
	platform := runtime.GOOS + "_" + runtime.GOARCH
	tests := []struct {
		name string
		urls map[string]string
		want string
	}{
		{
			name: "current platform",
			urls: map[string]string{
				platform:                  "https://example.com/current",
				runtime.GOOS + "_other":   "https://example.com/other-arch",
				"other_" + runtime.GOARCH: "https://example.com/other-os",
			},
			want: "https://example.com/current",
		},
		{name: "different architecture", urls: map[string]string{runtime.GOOS + "_other": "https://example.com/other-arch"}},
		{name: "different OS", urls: map[string]string{"other_" + runtime.GOARCH: "https://example.com/other-os"}},
		{name: "no downloads"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vc := &VersionChecker{}
			got := vc.findDownloadURL(&ReleaseInfo{DownloadURLs: tt.urls})
			if got != tt.want {
				t.Errorf("findDownloadURL = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFetchChangelogPrefixMatching(t *testing.T) {
	t.Parallel()
	// v0.212.925024401 -> extractSHAFromTag returns "542901" (6 chars)
	// v0.213.950002063 -> extractSHAFromTag returns "a00433" (6 chars)
	// commits.json has "542901e" and "a004332" (7 chars)
	// This test verifies the prefix matching logic handles the length mismatch.
	commits := []StaticCommitInfo{
		{SHA: "a004332", Subject: "fix: latest commit"},
		{SHA: "542901e", Subject: "shelley/ui: middle commit"},
		{SHA: "e3ed88a", Subject: "shelley: another commit"},
		{SHA: "60ee3ab", Subject: "shelley/ui: old commit"},
	}

	currentSHA := extractSHAFromTag("v0.212.925024401")
	latestSHA := extractSHAFromTag("v0.213.950002063")

	if currentSHA != "542901" {
		t.Fatalf("expected currentSHA=542901, got %s", currentSHA)
	}
	if latestSHA != "a00433" {
		t.Fatalf("expected latestSHA=a00433, got %s", latestSHA)
	}

	// Verify prefix matching works: "a004332" starts with "a00433"
	if len("a004332") <= len(latestSHA) {
		t.Fatal("test setup wrong: commit SHA should be longer than tag SHA")
	}

	// Simulate the matching logic from FetchChangelog
	var result []CommitInfo
	var foundLatest, foundCurrent bool
	for _, c := range commits {
		if hasPrefix(c.SHA, latestSHA) {
			foundLatest = true
		}
		if foundLatest && !foundCurrent {
			result = append(result, CommitInfo{SHA: c.SHA, Message: c.Subject})
		}
		if hasPrefix(c.SHA, currentSHA) {
			foundCurrent = true
			break
		}
	}

	if !foundLatest {
		t.Error("did not find latest SHA via prefix matching")
	}
	if !foundCurrent {
		t.Error("did not find current SHA via prefix matching")
	}

	// Remove current commit from list
	if len(result) > 0 {
		last := result[len(result)-1].SHA
		if hasPrefix(last, currentSHA) {
			result = result[:len(result)-1]
		}
	}

	// Should have 1 commit: a004332 (the latest). 542901e is the current and
	// was removed. They are adjacent in the list so there's nothing in between.
	if len(result) != 1 {
		t.Errorf("expected 1 commit, got %d: %+v", len(result), result)
	}
	if len(result) > 0 && result[0].SHA != "a004332" {
		t.Errorf("expected first commit SHA=a004332, got %s", result[0].SHA)
	}
}

func TestFetchChangelogPrefixMatchingMultipleCommits(t *testing.T) {
	t.Parallel()
	// Same as above but with commits between current and latest
	commits := []StaticCommitInfo{
		{SHA: "a004332", Subject: "fix: latest commit"},
		{SHA: "1111111", Subject: "middle commit 1"},
		{SHA: "2222222", Subject: "middle commit 2"},
		{SHA: "542901e", Subject: "current commit"},
		{SHA: "60ee3ab", Subject: "old commit"},
	}

	currentSHA := "542901" // 6-char from tag extraction
	latestSHA := "a00433"  // 6-char from tag extraction

	var result []CommitInfo
	var foundLatest, foundCurrent bool
	for _, c := range commits {
		if hasPrefix(c.SHA, latestSHA) {
			foundLatest = true
		}
		if foundLatest && !foundCurrent {
			result = append(result, CommitInfo{SHA: c.SHA, Message: c.Subject})
		}
		if hasPrefix(c.SHA, currentSHA) {
			foundCurrent = true
			break
		}
	}

	if !foundLatest || !foundCurrent {
		t.Fatal("did not find both SHAs")
	}

	// Remove current
	if len(result) > 0 {
		last := result[len(result)-1].SHA
		if hasPrefix(last, currentSHA) {
			result = result[:len(result)-1]
		}
	}

	// Should have 3 commits: a004332, 1111111, 2222222
	if len(result) != 3 {
		t.Fatalf("expected 3 commits, got %d: %+v", len(result), result)
	}
	expected := []string{"a004332", "1111111", "2222222"}
	for i, exp := range expected {
		if result[i].SHA != exp {
			t.Errorf("commit[%d]: expected SHA=%s, got %s", i, exp, result[i].SHA)
		}
	}
}

func hasPrefix(a, b string) bool {
	return len(a) >= len(b) && a[:len(b)] == b || len(b) >= len(a) && b[:len(a)] == a
}

func TestHeadlessShellHasUpdate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		current  string
		latest   string
		expected bool
	}{
		{"newer major", "Chromium 141.0.7390.55", "Chromium 147.0.7727.24", true},
		{"same version", "Chromium 141.0.7390.55", "Chromium 141.0.7390.55", false},
		{"older version", "Chromium 147.0.7727.24", "Chromium 141.0.7390.55", false},
		{"newer patch", "Chromium 141.0.7390.55", "Chromium 141.0.7390.56", true},
		{"numeric not lexicographic", "Chromium 999.0.0.0", "Chromium 1001.0.0.0", true},
		{"numeric not lexicographic reverse", "Chromium 1001.0.0.0", "Chromium 999.0.0.0", false},
		{"empty current", "", "Chromium 141.0.7390.55", false},
		{"empty latest", "Chromium 141.0.7390.55", "", false},
		{"both empty", "", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := headlessShellHasUpdate(tt.current, tt.latest)
			if result != tt.expected {
				t.Errorf("headlessShellHasUpdate(%q, %q) = %v, want %v",
					tt.current, tt.latest, result, tt.expected)
			}
		})
	}
}

func TestIsPermissionError(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{
			name:     "fs.ErrPermission",
			err:      fs.ErrPermission,
			expected: true,
		},
		{
			name:     "os.ErrPermission",
			err:      os.ErrPermission,
			expected: true,
		},
		{
			name:     "wrapped fs.ErrPermission",
			err:      errors.Join(errors.New("outer"), fs.ErrPermission),
			expected: true,
		},
		{
			name:     "other error",
			err:      errors.New("some other error"),
			expected: false,
		},
		{
			name:     "nil error",
			err:      nil,
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isPermissionError(tt.err)
			if result != tt.expected {
				t.Errorf("isPermissionError(%v) = %v, want %v", tt.err, result, tt.expected)
			}
		})
	}
}
