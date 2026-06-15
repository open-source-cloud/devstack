package alias

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/open-source-cloud/devstack/internal/xdg"
)

// hermeticHome isolates XDG_BIN_HOME/XDG_CONFIG_HOME to temp dirs for a test.
func hermeticHome(t *testing.T) string {
	t.Helper()
	bin := t.TempDir()
	t.Setenv("XDG_BIN_HOME", bin)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	return bin
}

func TestRemoveLeavesForeignFile(t *testing.T) {
	bin := hermeticHome(t)
	if _, err := xdg.EnsureDir(bin); err != nil {
		t.Fatal(err)
	}
	foreign := filepath.Join(bin, "rq")
	if err := os.WriteFile(foreign, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Remove("rq"); err == nil {
		t.Fatal("Remove deleted/accepted a non-symlink file; want error")
	}
	if _, err := os.Stat(foreign); err != nil {
		t.Fatalf("foreign file was removed: %v", err)
	}
}

func TestRemoveMissingIsIdempotent(t *testing.T) {
	hermeticHome(t)
	if err := Remove("ghost"); err != nil {
		t.Fatalf("removing a missing alias should be idempotent, got: %v", err)
	}
}

func TestAddRemoveRoundTrip(t *testing.T) {
	hermeticHome(t)
	if _, err := Add("rq"); err != nil {
		t.Fatalf("add: %v", err)
	}
	reg, err := Load()
	if err != nil || !reg.Has("rq") {
		t.Fatalf("registry missing rq after add (err=%v)", err)
	}
	if err := Remove("rq"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	reg, _ = Load()
	if reg.Has("rq") {
		t.Fatal("registry still has rq after remove")
	}
}

func TestResolveInvocation(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantName string
		wantArgs []string
	}{
		{"plain", []string{"/usr/local/bin/devstack", "up"}, "devstack", []string{"/usr/local/bin/devstack", "up"}},
		{"alias argv0", []string{"/home/u/.local/bin/rq", "status"}, "rq", []string{"/home/u/.local/bin/rq", "status"}},
		{"--as space", []string{"devstack", "--as", "uranus", "doctor"}, "uranus", []string{"devstack", "doctor"}},
		{"--as equals", []string{"devstack", "--as=uranus", "doctor"}, "uranus", []string{"devstack", "doctor"}},
		{"--as wins over argv0", []string{"rq", "--as", "uranus"}, "uranus", []string{"rq"}},
		{"-- terminator passes through", []string{"devstack", "shell", "--", "--as", "foo"}, "devstack", []string{"devstack", "shell", "--", "--as", "foo"}},
		{"--as before -- still parsed", []string{"devstack", "--as", "uranus", "shell", "--", "--as", "x"}, "uranus", []string{"devstack", "shell", "--", "--as", "x"}},
		{"empty", []string{}, Canonical, []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotName, gotArgs := ResolveInvocation(tt.args)
			if gotName != tt.wantName {
				t.Errorf("name = %q, want %q", gotName, tt.wantName)
			}
			if !slices.Equal(gotArgs, tt.wantArgs) {
				t.Errorf("args = %v, want %v", gotArgs, tt.wantArgs)
			}
		})
	}
}

func TestValidateName(t *testing.T) {
	valid := []string{"rq", "uranus", "dev2", "my-tool", "my_tool"}
	for _, n := range valid {
		if err := ValidateName(n); err != nil {
			t.Errorf("ValidateName(%q) = %v, want nil", n, err)
		}
	}
	invalid := []string{"devstack", "1tool", "-bad", "UPPER", "has space", "with/slash", ""}
	for _, n := range invalid {
		if err := ValidateName(n); err == nil {
			t.Errorf("ValidateName(%q) = nil, want error", n)
		}
	}
}

func TestBrandFor(t *testing.T) {
	b := BrandFor("my-tool")
	if b.Name != "my-tool" {
		t.Errorf("Name = %q", b.Name)
	}
	if b.EnvPrefix != "MY_TOOL" {
		t.Errorf("EnvPrefix = %q, want MY_TOOL", b.EnvPrefix)
	}
	if BrandFor("").Name != Canonical {
		t.Error("empty name should default to canonical")
	}
}
