package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/open-source-cloud/devstack/internal/config"
	"github.com/open-source-cloud/devstack/internal/docker"
	"github.com/open-source-cloud/devstack/internal/generate"
	"github.com/open-source-cloud/devstack/internal/orchestrate"
	"github.com/open-source-cloud/devstack/internal/state"
	"github.com/open-source-cloud/devstack/internal/template"
	"github.com/open-source-cloud/devstack/internal/workspace"
	"github.com/open-source-cloud/devstack/templates"
)

// destroyFakeRunner records compose invocations so teardown can assert on them.
type destroyFakeRunner struct{ cmds [][]string }

func (f *destroyFakeRunner) Run(_ context.Context, _ []string, _, name string, args ...string) error {
	f.cmds = append(f.cmds, append([]string{name}, args...))
	return nil
}
func (f *destroyFakeRunner) Output(_ context.Context, _ []string, _, name string, args ...string) ([]byte, error) {
	f.cmds = append(f.cmds, append([]string{name}, args...))
	return nil, nil
}
func (f *destroyFakeRunner) saw(needles ...string) bool {
	for _, c := range f.cmds {
		joined := strings.Join(c, " ")
		all := true
		for _, n := range needles {
			if !strings.Contains(joined, n) {
				all = false
				break
			}
		}
		if all {
			return true
		}
	}
	return false
}

// destroyFixture builds a one-project workspace whose shared postgres is live and
// referenced by the project, with a generated .devstack/ on disk.
func destroyFixture(t *testing.T) (orchestrate.UpDeps, *destroyFakeRunner) {
	t.Helper()
	root := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("workspace.yaml", "apiVersion: devstack/v1\nkind: Workspace\nname: demo\nshared:\n  postgres: { template: postgres, params: { version: \"16\" } }\nprojects:\n  - { name: app, path: app }\n")
	write("app/devstack.yaml", "apiVersion: devstack/v1\nkind: Project\nname: app\nservices:\n  web:\n    template: node.vite\n    uses: [workspace.shared.postgres]\n")
	// Generated artifacts that destroy must remove.
	write(filepath.Join("app", generate.GenDir, generate.ComposeFile), "services: {}\n")
	write(filepath.Join(generate.GenDir, "shared", generate.ComposeFile), "services: {}\n")

	m, err := config.LoadAt(root)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	db, err := state.Open(context.Background(), filepath.Join(root, "state"), "ctx")
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	mc := &docker.MockClient{
		Context: "ctx",
		Containers: []docker.Container{{
			ID: "pg1", Name: "devstack-shared-postgres-1", State: "running",
			Labels: map[string]string{generate.LabelManaged: "true", generate.LabelShared: "postgres"},
		}},
	}
	src := template.NewFSSource(templates.FS)
	lockPath := filepath.Join(root, "lock")
	fr := &destroyFakeRunner{}
	mgr := &workspace.Manager{Model: m, DB: db, Docker: mc, Source: src, LockPath: lockPath, Runner: fr}
	d := orchestrate.UpDeps{
		Model: m, DB: db, Docker: mc, Manager: mgr, Source: src, LockPath: lockPath, Runner: fr,
	}
	return d, fr
}

func TestWorkspaceDestroyRegistered(t *testing.T) {
	root := NewRootCmd(Options{})
	c, _, err := root.Find([]string{"workspace", "destroy"})
	if err != nil || c.Name() != "destroy" || c.RunE == nil {
		t.Fatalf("workspace destroy not registered as a real command: %v", err)
	}
}

func TestWorkspaceDestroyJSONRequiresYes(t *testing.T) {
	t.Chdir(t.TempDir())
	var out strings.Builder
	root := NewRootCmd(Options{})
	root.SetArgs([]string{"workspace", "destroy", "--json"})
	root.SetOut(&out)
	root.SetErr(&out)
	// The --json-without-yes guard fires before any Docker/workspace access.
	if err := root.Execute(); err == nil {
		t.Fatal("workspace destroy --json without --yes must error")
	}
}

func TestDestroyWorkspaceTeardown(t *testing.T) {
	d, fr := destroyFixture(t)
	ctx := context.Background()

	// Seed live state: a ref row for the project + a port allocation it owns.
	if err := d.Manager.RegisterUp(ctx, "app"); err != nil {
		t.Fatalf("register up: %v", err)
	}
	if _, err := d.DB.AllocatePort("app", "web", 30000, 30100, func(int) bool { return true }); err != nil {
		t.Fatalf("alloc port: %v", err)
	}
	if n, _ := d.DB.RefCount("shared-postgres"); n != 1 {
		t.Fatalf("precondition: ref count = %d, want 1", n)
	}

	res := destroyWorkspace(ctx, d, []string{"app"})
	if len(res.Errors) != 0 {
		t.Fatalf("destroy errors: %v", res.Errors)
	}

	// Project stack was composed down (never with --volumes).
	if !fr.saw("-p devstack-app", "down") {
		t.Errorf("project stack was not composed down: %v", fr.cmds)
	}
	if fr.saw("down", "--volumes") {
		t.Error("destroy must never pass --volumes (data preservation)")
	}
	// Project is in the result.
	if len(res.Projects) != 1 || res.Projects[0] != "app" {
		t.Errorf("projects = %v, want [app]", res.Projects)
	}
	// Ref rows + port rows for the project are gone.
	if n, _ := d.DB.RefCount("shared-postgres"); n != 0 {
		t.Errorf("ref count after destroy = %d, want 0", n)
	}
	if _, ok, _ := d.DB.PortFor("app", "web"); ok {
		t.Error("port allocation for app/web should be released")
	}
	// The now-orphaned shared service was warm-stopped (compose stop on the shared
	// stack), not downed.
	if !fr.saw("-p "+generate.SharedStackName, "stop") {
		t.Errorf("orphaned shared service was not warm-stopped: %v", fr.cmds)
	}
	if len(res.SharedStopped) != 1 || res.SharedStopped[0] != generate.SharedAlias("postgres") {
		t.Errorf("shared stopped = %v, want [%s]", res.SharedStopped, generate.SharedAlias("postgres"))
	}
	// Generated artifacts were removed.
	if _, err := os.Stat(filepath.Join(d.Model.Root, generate.GenDir)); !os.IsNotExist(err) {
		t.Error("workspace .devstack/ should be removed")
	}
	if _, err := os.Stat(filepath.Join(d.Model.ProjectDir("app"), generate.GenDir)); !os.IsNotExist(err) {
		t.Error("project .devstack/ should be removed")
	}
}
