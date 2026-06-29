package cli

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func gitInitRepo(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	gitRun := func(args ...string) {
		c := exec.Command("git", args...)
		c.Dir = dir
		c.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	gitRun("init", "-q", "-b", "main")
	gitRun("config", "user.email", "t@example.com")
	gitRun("config", "user.name", "Tester")
	gitRun("config", "commit.gpgsign", "false")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun("add", "-A")
	gitRun("commit", "-q", "-m", "init")
}

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
}

func TestWsStatusAndCheck(t *testing.T) {
	requireGit(t)
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "workspace.yaml"), []byte(
		"apiVersion: devstack/v1\nkind: Workspace\nname: demo\nprojects:\n"+
			"  - { name: api, path: services/api }\n  - { name: web, path: services/web }\n"), 0o644)
	gitInitRepo(t, filepath.Join(root, "services", "api"))
	gitInitRepo(t, filepath.Join(root, "services", "web"))
	t.Setenv("DEVSTACK_WORKSPACE", root)

	out, err := runCmd(t, "ws", "status", "--json")
	if err != nil {
		t.Fatalf("ws status: %v\n%s", err, out)
	}
	var res struct {
		Repos []struct {
			Name    string `json:"name"`
			Present bool   `json:"present"`
			Status  *struct {
				Branch string `json:"branch"`
			} `json:"status"`
		} `json:"repos"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("parse: %v\n%s", err, out)
	}
	if len(res.Repos) != 2 {
		t.Fatalf("want 2 repos, got %d", len(res.Repos))
	}
	for _, r := range res.Repos {
		if !r.Present || r.Status == nil || r.Status.Branch != "main" {
			t.Errorf("repo %s: present=%v status=%+v, want present on main", r.Name, r.Present, r.Status)
		}
	}

	// Clean → --check passes.
	if _, err := runCmd(t, "ws", "status", "--check"); err != nil {
		t.Errorf("--check on clean repos should pass: %v", err)
	}
	// Dirty one → --check fails.
	os.WriteFile(filepath.Join(root, "services", "api", "new.txt"), []byte("x"), 0o644)
	if _, err := runCmd(t, "ws", "status", "--check"); err == nil {
		t.Error("--check should fail when a repo is dirty")
	}
}

func TestWsCloneIdempotent(t *testing.T) {
	requireGit(t)
	origin := filepath.Join(t.TempDir(), "origin")
	gitInitRepo(t, origin)

	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "workspace.yaml"), []byte(
		"apiVersion: devstack/v1\nkind: Workspace\nname: demo\nprojects:\n"+
			"  - { name: api, path: api, git: \""+origin+"\" }\n"), 0o644)
	t.Setenv("DEVSTACK_WORKSPACE", root)

	if out, err := runCmd(t, "ws", "clone"); err != nil {
		t.Fatalf("ws clone: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(root, "api", ".git")); err != nil {
		t.Fatalf("repo not cloned: %v", err)
	}
	// Idempotent: a second clone skips the existing repo with no error.
	if out, err := runCmd(t, "ws", "clone"); err != nil {
		t.Fatalf("idempotent re-clone should succeed: %v\n%s", err, out)
	}
}
