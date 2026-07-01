package cli

import (
	"context"
	"testing"

	"github.com/open-source-cloud/devstack/internal/config"
	"github.com/open-source-cloud/devstack/internal/state"
)

func TestUseContextRegistered(t *testing.T) {
	for _, name := range []string{"use", "context"} {
		if !findCmd(t, name) {
			t.Fatalf("%q must be a real RunE command", name)
		}
	}
}

// twoProjectModel loads a workspace with projects api + web (api sorts first).
func twoProjectModel(t *testing.T) *config.Model {
	t.Helper()
	proj := func(n string) string {
		return "apiVersion: devstack/v1\nkind: Project\nname: " + n +
			"\nservices:\n  app: { template: node.vite }\n"
	}
	root := writeWS(t,
		"apiVersion: devstack/v1\nkind: Workspace\nname: demo\n"+
			"shared:\n  postgres: { template: postgres }\n"+
			"projects:\n  - { name: api, path: api }\n  - { name: web, path: web }\n",
		map[string]string{"api": proj("api"), "web": proj("web")},
	)
	m, err := config.LoadAt(root)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	return m
}

func TestResolveActiveProject(t *testing.T) {
	m := twoProjectModel(t)
	db, err := state.Open(context.Background(), t.TempDir(), "ctx")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	// Fallback: first project by name.
	if got := resolveActiveProject(m, db); got != "api" {
		t.Errorf("fallback = %q, want api", got)
	}
	// nil db must not panic and still falls back.
	if got := resolveActiveProject(m, nil); got != "api" {
		t.Errorf("nil-db fallback = %q, want api", got)
	}

	// Persisted active (matching root) wins over the fallback.
	if err := db.SetActiveContext(m.Root, "web"); err != nil {
		t.Fatal(err)
	}
	if got := resolveActiveProject(m, db); got != "web" {
		t.Errorf("persisted = %q, want web", got)
	}

	// A persisted project for a DIFFERENT workspace root is ignored.
	if err := db.SetActiveContext("/some/other/root", "web"); err != nil {
		t.Fatal(err)
	}
	if got := resolveActiveProject(m, db); got != "api" {
		t.Errorf("stale-root persisted = %q, want api (ignored)", got)
	}

	// DEVSTACK_PROJECT env wins over everything (when it names a real project).
	_ = db.SetActiveContext(m.Root, "api")
	t.Setenv("DEVSTACK_PROJECT", "web")
	if got := resolveActiveProject(m, db); got != "web" {
		t.Errorf("env override = %q, want web", got)
	}
	// An env value that is not a project in the model is ignored.
	t.Setenv("DEVSTACK_PROJECT", "ghost")
	if got := resolveActiveProject(m, db); got != "api" {
		t.Errorf("bad env = %q, want api (persisted)", got)
	}
}
