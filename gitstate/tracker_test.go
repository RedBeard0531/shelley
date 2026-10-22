package gitstate

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestTracker(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cwd, other := newRepo(t, root, "cwd"), newRepo(t, root, "other")
	if err := os.Mkdir(filepath.Join(other, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	dir := cwd
	tracker := NewTracker(func() string { return dir })
	wantChanges := func(want ...string) {
		t.Helper()
		var got []string
		for _, state := range tracker.Changes() {
			got = append(got, state.Worktree+" "+state.Subject)
		}
		if !slices.Equal(got, want) {
			t.Fatalf("changes = %q, want %q", got, want)
		}
	}
	commit := func(dir, subject string) {
		t.Helper()
		runGit(t, dir, "commit", "--allow-empty", "-m", subject)
	}

	wantChanges()
	commit(cwd, "here")
	wantChanges(cwd + " here")
	wantChanges()

	// A repository git runs in is reported when it changes, once.
	tracker.Touch(other)()
	wantChanges()
	release := tracker.Touch(filepath.Join(other, "sub"))
	tracker.Touch(other)()
	commit(other, "there")
	tracker.Touch(filepath.Join(other, "sub"))()
	wantChanges(other + " there")
	wantChanges()

	// It is watched while a command that touched it runs, as one moved to
	// the background does, even if the directory git ran in is gone.
	if err := os.Remove(filepath.Join(other, "sub")); err != nil {
		t.Fatal(err)
	}
	commit(other, "later")
	release()
	wantChanges(other + " later")

	// Then it is not: others' changes are not this conversation's, even
	// once it runs git there again.
	commit(other, "elsewhere")
	wantChanges()
	tracker.Touch(other)()
	wantChanges()

	// Running git in a new directory inside a watched repository keeps
	// that repository's changes.
	tracker.Touch(other)()
	commit(other, "mine")
	tracker.Touch(filepath.Join(other, "newpkg"))()
	wantChanges(other + " mine")

	// A repository created inside a watched one is reported.
	nested := filepath.Join(other, "nested")
	if err := os.Mkdir(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	release = tracker.Touch(nested)
	initRepoIn(t, nested)
	release()
	wantChanges(nested + " initial")

	// The working directory's repository is always watched, and touching
	// it does not report it twice.
	commit(cwd, "outside")
	tracker.Touch(cwd)()
	wantChanges(cwd + " outside")

	// Moving the working directory to a repository already reported in
	// its current state reports nothing; moving it to a new one does.
	dir = other
	wantChanges()
	dir = newRepo(t, root, "fresh")
	wantChanges(dir + " initial")
	dir = cwd
	wantChanges()

	// A repository reached through a symlink is the same repository.
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(other, alias); err != nil {
		t.Fatal(err)
	}
	dir = alias
	release = tracker.Touch(alias)
	commit(other, "aliased")
	release()
	wantChanges(other + " aliased")
	dir = cwd

	// A directory that becomes a repository is reported. One that
	// disappears says nothing about the repository around it.
	later := filepath.Join(root, "later")
	release = tracker.Touch(later)
	newRepo(t, root, "later")
	release()
	wantChanges(later + " initial")
	inner := newRepo(t, newRepo(t, root, "outer"), "inner")
	release = tracker.Touch(inner)
	if err := os.RemoveAll(inner); err != nil {
		t.Fatal(err)
	}
	release()
	wantChanges()
}

func newRepo(t *testing.T, root, name string) string {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	initRepoIn(t, dir)
	return dir
}

func initRepoIn(t *testing.T, dir string) {
	t.Helper()
	runGit(t, dir, "init", "-q")
	runGit(t, dir, "config", "user.email", "test@test.com")
	runGit(t, dir, "config", "user.name", "Test")
	runGit(t, dir, "commit", "--allow-empty", "-m", "initial")
}
