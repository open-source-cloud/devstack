package cli

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestSharedGcRegistered(t *testing.T) {
	root := NewRootCmd(Options{})
	c, _, err := root.Find([]string{"shared", "gc"})
	if err != nil || c.Name() != "gc" || c.RunE == nil {
		t.Fatalf("shared gc not registered as a real command: %v", err)
	}
}

func TestSharedGcDryRun(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "workspace.yaml"),
		"apiVersion: devstack/v1\nkind: Workspace\nname: demo\nshared:\n  postgres: { template: postgres }\nprojects: []\n")
	t.Chdir(dir)
	t.Setenv("XDG_DATA_HOME", filepath.Join(dir, "data"))
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(dir, "run"))

	var out strings.Builder
	root := NewRootCmd(Options{})
	root.SetArgs([]string{"shared", "gc"})
	root.SetOut(&out)
	root.SetErr(&out)
	if err := root.Execute(); err != nil {
		t.Fatalf("shared gc (dry-run): %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "no shared services at zero references") {
		t.Errorf("unexpected gc output:\n%s", out.String())
	}
}

func TestDoctorRebuildStateFlag(t *testing.T) {
	root := NewRootCmd(Options{})
	c, _, err := root.Find([]string{"doctor"})
	if err != nil {
		t.Fatal(err)
	}
	if c.Flags().Lookup("rebuild-state") == nil {
		t.Error("doctor is missing the --rebuild-state flag")
	}
}

func TestDoctorIncludesTrustProbe(t *testing.T) {
	var out strings.Builder
	root := NewRootCmd(Options{})
	root.SetArgs([]string{"doctor", "--json"})
	root.SetOut(&out)
	root.SetErr(&out)
	// doctor exits non-zero only on a hard FAIL; the trust probe is a warning, so
	// in CI (no mkcert) doctor still succeeds and the JSON lists the probe.
	_ = root.Execute()
	if !strings.Contains(out.String(), `"trust (mkcert)"`) {
		t.Errorf("doctor --json missing the trust probe:\n%s", out.String())
	}
}
