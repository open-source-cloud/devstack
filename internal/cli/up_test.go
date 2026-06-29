package cli

import (
	"strings"
	"testing"
)

func findCmd(t *testing.T, name string) bool {
	t.Helper()
	root := NewRootCmd(Options{})
	for _, c := range root.Commands() {
		if c.Name() == name {
			return c.RunE != nil // a real command, not a stub group
		}
	}
	return false
}

func TestUpDownRegistered(t *testing.T) {
	for _, name := range []string{"up", "down"} {
		if !findCmd(t, name) {
			t.Errorf("command %q is not registered as a real RunE command", name)
		}
	}
}

func TestUpOutsideWorkspaceErrors(t *testing.T) {
	t.Chdir(t.TempDir())
	root := NewRootCmd(Options{})
	root.SetArgs([]string{"up"})
	root.SetOut(&strings.Builder{})
	root.SetErr(&strings.Builder{})
	if err := root.Execute(); err == nil {
		t.Fatal("up outside a workspace should error (no workspace.yaml)")
	}
}

func TestDownOutsideWorkspaceErrors(t *testing.T) {
	t.Chdir(t.TempDir())
	root := NewRootCmd(Options{})
	root.SetArgs([]string{"down"})
	root.SetOut(&strings.Builder{})
	root.SetErr(&strings.Builder{})
	if err := root.Execute(); err == nil {
		t.Fatal("down outside a workspace should error (no workspace.yaml)")
	}
}
