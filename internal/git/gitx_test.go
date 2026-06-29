package git

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// testGit returns a Git handle, skipping the test when git is missing/too old.
func testGit(t *testing.T) *Git {
	t.Helper()
	g, err := New()
	if err != nil {
		t.Skipf("git unavailable: %v", err)
	}
	return g
}

// initRepo creates a temp git repo with one commit on branch `main` and returns
// its path. It configures a deterministic identity and no global hooks.
func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.email", "t@example.com"},
		{"config", "user.name", "Tester"},
		{"config", "commit.gpgsign", "false"},
	} {
		run(t, dir, args...)
	}
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, dir, "add", "-A")
	run(t, dir, "commit", "-q", "-m", "init")
	return dir
}

func run(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func TestNewChecksVersion(t *testing.T) {
	testGit(t) // New() succeeds with a modern git; the skip covers the old/missing case.
}

func TestCloneAndIsRepo(t *testing.T) {
	g := testGit(t)
	ctx := context.Background()
	origin := initRepo(t)

	dst := filepath.Join(t.TempDir(), "clone")
	if err := g.Clone(ctx, origin, dst, CloneOptions{}); err != nil {
		t.Fatalf("clone: %v", err)
	}
	if !g.IsRepo(ctx, dst) {
		t.Error("cloned dir should be a repo")
	}
	if !g.IsRepo(ctx, origin) {
		t.Error("origin should be a repo")
	}
	if g.IsRepo(ctx, t.TempDir()) {
		t.Error("empty dir should not be a repo")
	}

	url, err := g.RemoteURL(ctx, dst)
	if err != nil {
		t.Fatalf("remote url: %v", err)
	}
	if url != origin {
		t.Errorf("remote url = %q, want %q", url, origin)
	}
}

func TestStatusClean(t *testing.T) {
	g := testGit(t)
	st, err := g.Status(context.Background(), initRepo(t))
	if err != nil {
		t.Fatal(err)
	}
	if st.Branch != "main" {
		t.Errorf("branch = %q, want main", st.Branch)
	}
	if st.Dirty() {
		t.Errorf("fresh repo should be clean, got %+v", st)
	}
}

func TestStatusDirty(t *testing.T) {
	g := testGit(t)
	dir := initRepo(t)
	// One untracked, one modified-staged.
	os.WriteFile(filepath.Join(dir, "new.txt"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(dir, "README.md"), []byte("changed\n"), 0o644)
	run(t, dir, "add", "README.md")

	st, err := g.Status(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if !st.Dirty() {
		t.Fatal("repo should be dirty")
	}
	if st.Untracked != 1 {
		t.Errorf("untracked = %d, want 1", st.Untracked)
	}
	if st.Staged != 1 {
		t.Errorf("staged = %d, want 1", st.Staged)
	}
}

func TestStatusAheadBehind(t *testing.T) {
	g := testGit(t)
	ctx := context.Background()
	origin := initRepo(t)
	clone := filepath.Join(t.TempDir(), "c")
	if err := g.Clone(ctx, origin, clone, CloneOptions{}); err != nil {
		t.Fatal(err)
	}
	// Commit locally → ahead by 1.
	os.WriteFile(filepath.Join(clone, "f.txt"), []byte("y"), 0o644)
	run(t, clone, "add", "-A")
	run(t, clone, "commit", "-q", "-m", "local")

	st, err := g.Status(ctx, clone)
	if err != nil {
		t.Fatal(err)
	}
	if st.Ahead != 1 || st.Behind != 0 {
		t.Errorf("ahead/behind = %d/%d, want 1/0", st.Ahead, st.Behind)
	}
	if st.Upstream == "" {
		t.Error("clone should have an upstream")
	}
}

func TestFetchAndPullFastForward(t *testing.T) {
	g := testGit(t)
	ctx := context.Background()
	origin := initRepo(t)
	clone := filepath.Join(t.TempDir(), "c")
	if err := g.Clone(ctx, origin, clone, CloneOptions{}); err != nil {
		t.Fatal(err)
	}
	// Advance origin.
	os.WriteFile(filepath.Join(origin, "f2.txt"), []byte("z"), 0o644)
	run(t, origin, "add", "-A")
	run(t, origin, "commit", "-q", "-m", "upstream change")

	if err := g.Fetch(ctx, clone); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if err := g.Pull(ctx, clone); err != nil {
		t.Fatalf("pull --ff-only: %v", err)
	}
	if _, err := os.Stat(filepath.Join(clone, "f2.txt")); err != nil {
		t.Error("fast-forward pull should have brought f2.txt")
	}
}

func TestErrorCarriesStderr(t *testing.T) {
	g := testGit(t)
	// Cloning a nonexistent local path fails with stderr.
	err := g.Clone(context.Background(), filepath.Join(t.TempDir(), "nope"), filepath.Join(t.TempDir(), "x"), CloneOptions{})
	if err == nil {
		t.Fatal("want clone error")
	}
	var ge *Error
	if !asError(err, &ge) {
		t.Fatalf("want *git.Error, got %T", err)
	}
	if ge.Stderr == "" {
		t.Error("error should capture git stderr")
	}
}

// asError is a tiny errors.As wrapper kept local to avoid an import in the table above.
func asError(err error, target **Error) bool {
	for err != nil {
		if e, ok := err.(*Error); ok {
			*target = e
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}
