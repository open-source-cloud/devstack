package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/open-source-cloud/devstack/internal/lock"
	"github.com/open-source-cloud/devstack/internal/state"
)

func TestWorkspaceListRegistered(t *testing.T) {
	root := NewRootCmd(Options{})
	c, _, err := root.Find([]string{"workspace", "list"})
	if err != nil || c.Name() != "list" || c.RunE == nil {
		t.Fatalf("workspace list not registered as a real command: %v", err)
	}
}

// seedWorkspace opens the ledger the same way the command does and records a
// workspace row under the flock, then closes it.
func seedWorkspace(t *testing.T, name, root string, refs []state.Ref) {
	t.Helper()
	c := &cobra.Command{}
	c.SetContext(context.Background())
	db, closeFn, err := openLedger(c)
	if err != nil {
		t.Fatalf("open ledger: %v", err)
	}
	defer closeFn()
	if err := lock.WithLock(context.Background(), lockPath(), func() error {
		if err := db.RecordWorkspace(name, root); err != nil {
			return err
		}
		for _, r := range refs {
			if err := db.AddRef(r.Project, r.Service, r.SharedService); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
}

func TestWorkspaceListProjectionAndPrune(t *testing.T) {
	// Isolate the ledger + lock under temp XDG dirs.
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())

	// A live workspace with one project that references a shared postgres.
	liveRoot := writeWS(t,
		"apiVersion: devstack/v1\nkind: Workspace\nname: acme\nshared:\n  postgres: { template: postgres, params: { version: \"16\" } }\nprojects:\n  - { name: api, path: api }\n",
		map[string]string{
			"api": "apiVersion: devstack/v1\nkind: Project\nname: api\nservices:\n  web:\n    template: node.vite\n    uses: [workspace.shared.postgres]\n",
		})
	seedWorkspace(t, "acme", liveRoot, []state.Ref{{Project: "api", Service: "web", SharedService: "shared-postgres-16"}})

	// A vanished workspace root (recorded, then removed from disk).
	goneRoot := filepath.Join(t.TempDir(), "gone")
	if err := os.MkdirAll(goneRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	seedWorkspace(t, "demo", goneRoot, nil)
	if err := os.RemoveAll(goneRoot); err != nil {
		t.Fatal(err)
	}

	list := func(args ...string) []wsListRow {
		t.Helper()
		rootCmd := NewRootCmd(Options{})
		var out strings.Builder
		rootCmd.SetArgs(append([]string{"workspace", "list", "--json"}, args...))
		rootCmd.SetOut(&out)
		rootCmd.SetErr(&out)
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("workspace list: %v\n%s", err, out.String())
		}
		var rows []wsListRow
		if err := json.Unmarshal([]byte(out.String()), &rows); err != nil {
			t.Fatalf("unmarshal %q: %v", out.String(), err)
		}
		return rows
	}

	rows := list()
	var live, gone *wsListRow
	for i := range rows {
		switch rows[i].Root {
		case liveRoot:
			live = &rows[i]
		case goneRoot:
			gone = &rows[i]
		}
	}
	if live == nil || gone == nil {
		t.Fatalf("expected both workspaces listed, got %+v", rows)
	}
	// The live row re-derives projects + shared refs from workspace.yaml + ledger.
	if len(live.Projects) != 1 || live.Projects[0] != "api" {
		t.Errorf("live projects = %v, want [api]", live.Projects)
	}
	if len(live.Shared) != 1 || live.Shared[0].Service != "shared-postgres-16" || live.Shared[0].Refs != 1 {
		t.Errorf("live shared = %+v, want shared-postgres-16 (1)", live.Shared)
	}
	if live.Stale {
		t.Error("live workspace should not be stale")
	}
	// The vanished root is flagged stale (but NOT dropped without --prune).
	if !gone.Stale {
		t.Error("vanished workspace root should be flagged stale")
	}

	// A plain (non-JSON) listing still succeeds and shows the stale marker.
	rootCmd := NewRootCmd(Options{})
	var plain strings.Builder
	rootCmd.SetArgs([]string{"workspace", "list"})
	rootCmd.SetOut(&plain)
	rootCmd.SetErr(&plain)
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("plain list: %v", err)
	}
	if !strings.Contains(plain.String(), "stale") {
		t.Errorf("plain output should mark the stale row: %q", plain.String())
	}

	// --prune drops the vanished root and only that.
	pruned := list("--prune")
	for _, r := range pruned {
		if r.Root == goneRoot {
			t.Error("--prune should have removed the vanished root")
		}
	}
	if len(pruned) != 1 || pruned[0].Root != liveRoot {
		t.Fatalf("after prune = %+v, want just the live workspace", pruned)
	}
}

func TestWorkspaceListDegradesUnparseableRoot(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())

	// A root that exists but whose workspace.yaml is garbage.
	badRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(badRoot, "workspace.yaml"), []byte("::: not yaml :::\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	seedWorkspace(t, "bad", badRoot, nil)

	rootCmd := NewRootCmd(Options{})
	var out strings.Builder
	rootCmd.SetArgs([]string{"workspace", "list", "--json"})
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	// An unparseable workspace.yaml must degrade to a flagged row, not fail.
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("list must not fail on an unparseable root: %v", err)
	}
	var rows []wsListRow
	if err := json.Unmarshal([]byte(out.String()), &rows); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(rows) != 1 || rows[0].Issue == "" {
		t.Fatalf("unparseable root should be a flagged row: %+v", rows)
	}
	if rows[0].Stale {
		t.Error("an existing-but-unparseable root is not stale (it is still on disk)")
	}
}
