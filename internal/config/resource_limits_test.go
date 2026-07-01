package config

import (
	"strings"
	"testing"
)

// resLimitsProject wraps a service body into a minimal but valid two-file tree.
func resLimitsProject(t *testing.T, svcBody string) (*Model, error) {
	t.Helper()
	ws := `apiVersion: devstack/v1
kind: Workspace
name: acme
shared:
  postgres: { template: postgres }
projects:
  - { name: api, path: api }
`
	proj := "apiVersion: devstack/v1\nkind: Project\nname: api\nservices:\n  api:\n    template: t\n" + svcBody
	root := writeTree(t, map[string]string{"workspace.yaml": ws, "api/devstack.yaml": proj})
	return LoadAt(root)
}

// TestResourceLimitsParse — the spec-18 resources block + platform selector parse
// onto a project service, and EffectiveMemoryMB prefers resources.memoryMB.
func TestResourceLimitsParse(t *testing.T) {
	m, err := resLimitsProject(t, `    platform: linux/amd64
    resources:
      cpus: "1.5"
      memoryMB: 512
      memoryReserveMB: 256
      pidsLimit: 128
`)
	if err != nil {
		t.Fatalf("LoadAt: %v", err)
	}
	svc := m.Projects["api"].Services["api"]
	if svc.Platform != "linux/amd64" {
		t.Errorf("platform = %q, want linux/amd64", svc.Platform)
	}
	if svc.Resources == nil {
		t.Fatal("resources block did not parse")
	}
	if svc.Resources.CPUs != "1.5" || svc.Resources.MemoryMB != 512 ||
		svc.Resources.MemoryReserveMB != 256 || svc.Resources.PidsLimit != 128 {
		t.Errorf("resources = %+v", *svc.Resources)
	}
	if got := svc.EffectiveMemoryMB(); got != 512 {
		t.Errorf("EffectiveMemoryMB = %d, want 512 (resources.memoryMB wins)", got)
	}
}

// TestEffectiveMemoryShorthand — with no resources.memoryMB, the top-level
// memoryMB shorthand is the effective limit; with neither, it is 0.
func TestEffectiveMemoryShorthand(t *testing.T) {
	m, err := resLimitsProject(t, "    memoryMB: 768\n")
	if err != nil {
		t.Fatalf("LoadAt: %v", err)
	}
	if got := m.Projects["api"].Services["api"].EffectiveMemoryMB(); got != 768 {
		t.Errorf("EffectiveMemoryMB = %d, want 768 (shorthand)", got)
	}

	m2, err := resLimitsProject(t, "")
	if err != nil {
		t.Fatalf("LoadAt: %v", err)
	}
	if got := m2.Projects["api"].Services["api"].EffectiveMemoryMB(); got != 0 {
		t.Errorf("EffectiveMemoryMB = %d, want 0 (unset)", got)
	}
}

// TestPlatformValidation — a malformed platform selector is rejected with a
// file-scoped error; well-formed selectors pass.
func TestPlatformValidation(t *testing.T) {
	if _, err := resLimitsProject(t, "    platform: not-a-platform\n"); err == nil ||
		!strings.Contains(err.Error(), "platform") {
		t.Fatalf("want a platform validation error, got %v", err)
	}
	for _, p := range []string{"linux/amd64", "linux/arm64/v8", "darwin/arm64"} {
		if _, err := resLimitsProject(t, "    platform: "+p+"\n"); err != nil {
			t.Errorf("platform %q should be valid, got %v", p, err)
		}
	}
}

// TestCPUsValidation — a non-numeric or non-positive cpus quantity is rejected.
func TestCPUsValidation(t *testing.T) {
	for _, bad := range []string{"lots", "0", "-1"} {
		if _, err := resLimitsProject(t, "    resources: { cpus: \""+bad+"\" }\n"); err == nil ||
			!strings.Contains(err.Error(), "cpu") {
			t.Errorf("cpus %q should be rejected, got %v", bad, err)
		}
	}
	if _, err := resLimitsProject(t, "    resources: { cpus: \"1.5\" }\n"); err != nil {
		t.Errorf("cpus 1.5 should be valid, got %v", err)
	}
}

// TestSharedResourcesParse — shared services accept the same resources/platform
// knobs from workspace.yaml (spec 18, shared stack).
func TestSharedResourcesParse(t *testing.T) {
	ws := `apiVersion: devstack/v1
kind: Workspace
name: acme
shared:
  postgres:
    template: postgres
    platform: linux/amd64
    resources: { cpus: "2", memoryMB: 1024 }
projects:
  - { name: api, path: api }
`
	proj := "apiVersion: devstack/v1\nkind: Project\nname: api\nservices:\n  api: { template: t }\n"
	root := writeTree(t, map[string]string{"workspace.yaml": ws, "api/devstack.yaml": proj})
	m, err := LoadAt(root)
	if err != nil {
		t.Fatalf("LoadAt: %v", err)
	}
	pg := m.Workspace.Shared["postgres"]
	if pg.Platform != "linux/amd64" {
		t.Errorf("shared platform = %q, want linux/amd64", pg.Platform)
	}
	if pg.EffectiveMemoryMB() != 1024 {
		t.Errorf("shared EffectiveMemoryMB = %d, want 1024", pg.EffectiveMemoryMB())
	}
}
