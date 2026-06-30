package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/open-source-cloud/devstack/internal/config"
	"github.com/open-source-cloud/devstack/internal/store"
)

// runInit executes `init` with args via the root command, capturing output.
// DEVSTACK_WORKSPACE is cleared so config.Discover does not short-circuit to an
// unrelated workspace during the test.
func runInit(t *testing.T, args ...string) (string, error) {
	t.Helper()
	t.Setenv("DEVSTACK_WORKSPACE", "")
	var buf strings.Builder
	root := NewRootCmd(Options{})
	root.SetArgs(append([]string{"init"}, args...))
	root.SetOut(&buf)
	root.SetErr(&buf)
	err := root.Execute() // run before reading buf (return-arg eval order would read it empty)
	return buf.String(), err
}

func initArgs(out string, extra ...string) []string {
	return append([]string{"--name", "demo", "--service", "postgres@16", "--service", "redis", "--out", out}, extra...)
}

func TestInitRegistered(t *testing.T) {
	root := NewRootCmd(Options{})
	c, _, err := root.Find([]string{"init"})
	if err != nil || c.Name() != "init" || c.RunE == nil {
		t.Fatalf("init not registered as a real command: %v", err)
	}
}

func TestInit_NonInteractiveHappyPath(t *testing.T) {
	d := t.TempDir()
	out, err := runInit(t, "--name", "app", "--service", "postgres@16", "--service", "redis", "--service", "minio", "--out", d)
	if err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	_, ws, err := config.LoadWorkspaceOnly(d)
	if err != nil {
		t.Fatalf("emitted workspace does not load: %v", err)
	}
	if ws.Name != "app" {
		t.Errorf("name = %q, want app", ws.Name)
	}
	for _, e := range []string{"postgres", "redis", "minio"} {
		if _, ok := ws.Shared[e]; !ok {
			t.Errorf("missing shared engine %q", e)
		}
	}
	if v := ws.Shared["postgres"].Params["version"]; v != "16" {
		t.Errorf("postgres version = %v, want \"16\"", v)
	}
}

func TestInit_Deterministic(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	if _, err := runInit(t, initArgs(a, "--alias", "rq")...); err != nil {
		t.Fatal(err)
	}
	if _, err := runInit(t, initArgs(b, "--alias", "rq")...); err != nil {
		t.Fatal(err)
	}
	ba, _ := os.ReadFile(filepath.Join(a, "workspace.yaml"))
	bb, _ := os.ReadFile(filepath.Join(b, "workspace.yaml"))
	if !bytes.Equal(ba, bb) {
		t.Errorf("init output not deterministic:\n--- a ---\n%s\n--- b ---\n%s", ba, bb)
	}
}

func TestInit_JSONDryRun(t *testing.T) {
	d := t.TempDir()
	out, err := runInit(t, initArgs(d, "--json", "--dry-run")...)
	if err != nil {
		t.Fatalf("init --json --dry-run: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(d, "workspace.yaml")); !os.IsNotExist(err) {
		t.Error("--dry-run must not write workspace.yaml")
	}
	var s struct {
		Shared   []string `json:"shared"`
		Projects []string `json:"projects"`
		Wrote    bool     `json:"wrote"`
	}
	if err := json.NewDecoder(strings.NewReader(out)).Decode(&s); err != nil {
		t.Fatalf("decode summary: %v\n%s", err, out)
	}
	if s.Wrote {
		t.Error("summary.wrote should be false on --dry-run")
	}
	if strings.Join(s.Shared, ",") != "postgres,redis" {
		t.Errorf("shared = %v, want [postgres redis]", s.Shared)
	}
}

func TestInit_NoClobberAndForceBackup(t *testing.T) {
	d := t.TempDir()
	if _, err := runInit(t, initArgs(d)...); err != nil {
		t.Fatal(err)
	}
	// Second run without --force must refuse.
	if _, err := runInit(t, initArgs(d)...); err == nil {
		t.Error("expected no-clobber refusal without --force")
	}
	// With --force it backs up the original.
	if _, err := runInit(t, initArgs(d, "--force")...); err != nil {
		t.Fatalf("init --force: %v", err)
	}
	entries, _ := os.ReadDir(d)
	var backups int
	for _, e := range entries {
		if strings.Contains(e.Name(), "workspace.yaml.bak.") {
			backups++
		}
	}
	if backups == 0 {
		t.Error("--force should leave a workspace.yaml.bak.* backup")
	}
}

func TestInit_ParentWorkspaceRefused(t *testing.T) {
	parent := t.TempDir()
	if err := os.WriteFile(filepath.Join(parent, "workspace.yaml"),
		[]byte("apiVersion: devstack/v1\nkind: Workspace\nname: parent\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	child := filepath.Join(parent, "child")
	out, err := runInit(t, initArgs(child)...)
	if err == nil {
		t.Errorf("init inside an existing workspace should be refused:\n%s", out)
	}
}

// TestInit_NoFlagsNoInputDoesNotLaunchWizard asserts the non-interactive gate:
// with --no-input and no flags, init takes the flag path (never the bubbletea
// runtime) and writes a minimal valid workspace — it must not hang on a prompt.
func TestInit_NoFlagsNoInputDoesNotLaunchWizard(t *testing.T) {
	d := t.TempDir()
	if _, err := runInit(t, "--out", d, "--no-input"); err != nil {
		t.Fatalf("init --no-input: %v", err)
	}
	_, ws, err := config.LoadWorkspaceOnly(d)
	if err != nil {
		t.Fatalf("minimal workspace does not load: %v", err)
	}
	if ws.Name == "" {
		t.Error("expected a sanitized default name")
	}
	if len(ws.Shared) != 0 {
		t.Errorf("expected no shared services, got %v", ws.Shared)
	}
}

func TestInit_FromStoreSeedsAndLeavesStore(t *testing.T) {
	home := t.TempDir()
	t.Setenv("DEVSTACK_HOME", home)
	if err := store.DefaultConfig().Save(); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(store.ConfigPath())

	d := t.TempDir()
	if _, err := runInit(t, "--name", "demo", "--from-store", "--out", d); err != nil {
		t.Fatalf("init --from-store: %v", err)
	}
	_, ws, err := config.LoadWorkspaceOnly(d)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range []string{"postgres", "redis", "minio"} {
		if _, ok := ws.Shared[e]; !ok {
			t.Errorf("--from-store should seed %q", e)
		}
	}
	after, _ := os.ReadFile(store.ConfigPath())
	if !bytes.Equal(before, after) {
		t.Error("init must never modify the store config")
	}
}
