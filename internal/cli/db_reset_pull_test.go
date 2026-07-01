package cli

import (
	"strings"
	"testing"
)

func TestDbResetPullRegistered(t *testing.T) {
	root := NewRootCmd(Options{})
	for _, path := range [][]string{{"db", "reset"}, {"db", "pull"}} {
		c, _, err := root.Find(path)
		if err != nil || c.RunE == nil {
			t.Fatalf("db %v not registered as a real command: %v", path, err)
		}
	}
}

func TestDbResetFlags(t *testing.T) {
	root := NewRootCmd(Options{})
	reset, _, err := root.Find([]string{"db", "reset"})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"project", "instance", "yes", "force"} {
		if reset.Flags().Lookup(f) == nil {
			t.Errorf("db reset missing --%s", f)
		}
	}
	pull, _, err := root.Find([]string{"db", "pull"})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"project", "db", "instance"} {
		if pull.Flags().Lookup(f) == nil {
			t.Errorf("db pull missing --%s", f)
		}
	}
}

// TestDbResetJSONRequiresYes asserts the destructive-verb non-interactive guard:
// `db reset --json` without --yes errors BEFORE touching any workspace/Docker
// state (mirrors the `workspace destroy --json` guard).
func TestDbResetJSONRequiresYes(t *testing.T) {
	t.Chdir(t.TempDir())
	var out strings.Builder
	root := NewRootCmd(Options{})
	root.SetArgs([]string{"db", "reset", "--json"})
	root.SetOut(&out)
	root.SetErr(&out)
	if err := root.Execute(); err == nil {
		t.Fatal("db reset --json without --yes must error")
	}
}
