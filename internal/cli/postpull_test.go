package cli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/open-source-cloud/devstack/internal/config"
	"github.com/open-source-cloud/devstack/internal/git"
)

func TestGitHeadTracksCommits(t *testing.T) {
	requireGit(t)
	dir := t.TempDir()
	gitInitRepo(t, dir)
	gx, err := git.New()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	h1, err := gx.Head(ctx, dir)
	if err != nil || len(h1) < 7 {
		t.Fatalf("Head = %q, err %v", h1, err)
	}
	// A new commit moves HEAD.
	if err := os.WriteFile(filepath.Join(dir, "f"), []byte("y\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-q", "-m", "two"}} {
		c := exec.Command("git", args...)
		c.Dir = dir
		c.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	h2, err := gx.Head(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	if h2 == h1 {
		t.Error("HEAD should change after a new commit")
	}
}

func TestRunPostPullExecutesHostHooks(t *testing.T) {
	dir := t.TempDir()
	r := repo{name: "app", dir: dir}
	hook := config.Hook{
		Name:    "setup",
		Run:     "host",
		Command: []string{"sh", "-c", "echo ran > postpull.marker"},
	}
	if err := runPostPull(context.Background(), r, "deadbeef", []config.Hook{hook}); err != nil {
		t.Fatalf("runPostPull: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "postpull.marker")); err != nil {
		t.Errorf("postPull host hook did not run in the repo dir: %v", err)
	}
}

func TestRunPostPullAbortPropagates(t *testing.T) {
	dir := t.TempDir()
	r := repo{name: "app", dir: dir}
	hook := config.Hook{
		Name:      "boom",
		Run:       "host",
		Command:   []string{"sh", "-c", "exit 7"},
		OnFailure: "abort",
	}
	if err := runPostPull(context.Background(), r, "sha", []config.Hook{hook}); err == nil {
		t.Fatal("an onFailure:abort postPull hook must surface an error")
	}
}

func TestRunPostPullDefaultsToWarn(t *testing.T) {
	// With no onFailure, a failing postPull hook defaults to warn → sync still ok.
	dir := t.TempDir()
	r := repo{name: "app", dir: dir}
	hook := config.Hook{Name: "soft", Run: "host", Command: []string{"sh", "-c", "exit 1"}}
	if err := runPostPull(context.Background(), r, "sha", []config.Hook{hook}); err != nil {
		t.Errorf("default onFailure should be warn (no error), got %v", err)
	}
}
