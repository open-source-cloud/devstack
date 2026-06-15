package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for rel, content := range files {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestLoadValid(t *testing.T) {
	m, err := LoadAt(filepath.Join("testdata", "valid"))
	if err != nil {
		t.Fatalf("LoadAt: %v", err)
	}
	if m.Workspace.Name != "acme" {
		t.Errorf("workspace name = %q, want acme", m.Workspace.Name)
	}
	if got := len(m.Projects); got != 2 {
		t.Errorf("projects = %d, want 2", got)
	}
	if got := len(m.Workspace.Shared); got != 3 {
		t.Errorf("shared = %d, want 3", got)
	}
	api, ok := m.Projects["api"]
	if !ok {
		t.Fatal("project api missing")
	}
	if got := len(api.Services["api"].Uses); got != 2 {
		t.Errorf("api.uses = %d, want 2", got)
	}
	if dir := m.ProjectDir("api"); !strings.HasSuffix(dir, filepath.Join("services", "api")) {
		t.Errorf("ProjectDir(api) = %q, want suffix services/api", dir)
	}
}

const sharedPGOnly = `apiVersion: devstack/v1
kind: Workspace
name: acme
shared:
  postgres: { template: postgres }
projects:
  - { name: api, path: api }
`

func TestDanglingUses(t *testing.T) {
	root := writeTree(t, map[string]string{
		"workspace.yaml": sharedPGOnly,
		"api/devstack.yaml": `apiVersion: devstack/v1
kind: Project
name: api
services:
  api:
    template: t
    uses:
      - workspace.shared.kafka
`,
	})
	_, err := LoadAt(root)
	if err == nil {
		t.Fatal("expected error for dangling uses")
	}
	var pe *PosError
	if !errors.As(err, &pe) {
		t.Fatalf("want *PosError, got %T: %v", err, err)
	}
	if pe.Line == 0 {
		t.Errorf("want a positioned error (line>0), got %v", pe)
	}
	if !strings.Contains(pe.Msg, "kafka") {
		t.Errorf("error %q should name the missing service kafka", pe.Msg)
	}
}

func TestReferenceCycle(t *testing.T) {
	root := writeTree(t, map[string]string{
		"workspace.yaml": `apiVersion: devstack/v1
kind: Workspace
name: acme
projects:
  - { name: a, path: a }
  - { name: b, path: b }
`,
		"a/devstack.yaml": `apiVersion: devstack/v1
kind: Project
name: a
services:
  svc:
    template: t
    env:
      import:
        - { from: workspace.b.svc }
`,
		"b/devstack.yaml": `apiVersion: devstack/v1
kind: Project
name: b
services:
  svc:
    template: t
    env:
      import:
        - { from: workspace.a.svc }
`,
	})
	_, err := LoadAt(root)
	if err == nil {
		t.Fatal("expected a cycle error")
	}
	if !strings.Contains(err.Error(), "cycle") {
		t.Errorf("error %q should report a cycle", err.Error())
	}
}

func TestBadName(t *testing.T) {
	root := writeTree(t, map[string]string{
		"workspace.yaml": `apiVersion: devstack/v1
kind: Workspace
name: Acme_Bad!
projects: []
`,
	})
	_, err := LoadAt(root)
	if err == nil || !strings.Contains(err.Error(), "name") {
		t.Fatalf("want a name validation error, got %v", err)
	}
}

func TestWrongAPIVersion(t *testing.T) {
	root := writeTree(t, map[string]string{
		"workspace.yaml": `apiVersion: devstack/v2
kind: Workspace
name: acme
projects: []
`,
	})
	if _, err := LoadAt(root); err == nil {
		t.Fatal("want an apiVersion mismatch error")
	}
}

func TestDiscoverWalksUp(t *testing.T) {
	t.Setenv("DEVSTACK_WORKSPACE", "")
	root := writeTree(t, map[string]string{
		"workspace.yaml": "apiVersion: devstack/v1\nkind: Workspace\nname: acme\n",
		"a/b/c/.keep":    "",
	})
	got, err := Discover(filepath.Join(root, "a", "b", "c"))
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if eval(t, got) != eval(t, root) {
		t.Errorf("Discover = %q, want %q", got, root)
	}
}

func TestDiscoverNotFound(t *testing.T) {
	t.Setenv("DEVSTACK_WORKSPACE", "")
	if _, err := Discover(t.TempDir()); err == nil {
		t.Fatal("want a not-found error when no workspace.yaml exists")
	}
}

func eval(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		return p
	}
	return r
}

// fakeResolver implements Resolver for interpolation tests.
type fakeResolver struct{}

func (fakeResolver) Env(n string) (string, bool) {
	if n == "FOO" {
		return "E", true
	}
	return "", false
}
func (fakeResolver) Self(a string) (string, bool) {
	if a == "host" {
		return "h", true
	}
	return "", false
}
func (fakeResolver) Ref(path string) (string, error) { return "R:" + path, nil }
func (fakeResolver) Profile() string                 { return "dev" }
func (fakeResolver) WorkspaceName() string           { return "acme" }

func TestInterpolate(t *testing.T) {
	r := fakeResolver{}
	ok := []struct{ in, want string }{
		{"${profile}", "dev"},
		{"${workspace.name}", "acme"},
		{"${env.FOO}", "E"},
		{"${self.host}", "h"},
		{"${ref:workspace.shared.postgres.host}", "R:workspace.shared.postgres.host"},
		{"a$$b", "a$b"},
		{"x${profile}y", "xdevy"},
		{"no interpolation", "no interpolation"},
		{"price is $5", "price is $5"},
	}
	for _, c := range ok {
		got, err := Interpolate(c.in, r)
		if err != nil {
			t.Errorf("Interpolate(%q) error: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("Interpolate(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	bad := []string{"${env.MISSING}", "${self.nope}", "${bogus}", "${unterminated", "${}"}
	for _, in := range bad {
		if _, err := Interpolate(in, r); err == nil {
			t.Errorf("Interpolate(%q) want error, got nil", in)
		}
	}
}

func TestInterpolationRefs(t *testing.T) {
	got := InterpolationRefs("a ${ref:workspace.shared.pg.host} b ${profile} ${ref:workspace.api.x.port}")
	want := []string{"workspace.shared.pg.host", "workspace.api.x.port"}
	if len(got) != len(want) {
		t.Fatalf("refs = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("ref[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
