package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const importSampleYAML = `name: shop
services:
  postgres:
    template: postgres
    params: { version: "16" }
  api:
    template: php.laravel.nginx
    repo: shop/api
    uses: [postgres]
`

func writeSample(t *testing.T) (dir, src string) {
	t.Helper()
	dir = t.TempDir()
	src = filepath.Join(dir, "project.yaml")
	if err := os.WriteFile(src, []byte(importSampleYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir, src
}

func TestImportRegistered(t *testing.T) {
	root := NewRootCmd(Options{})
	c, _, err := root.Find([]string{"import"})
	if err != nil || c.Name() != "import" || c.RunE == nil {
		t.Fatalf("import not registered as a real command: %v", err)
	}
}

func TestImportDryRunWritesNothing(t *testing.T) {
	dir, src := writeSample(t)
	out := filepath.Join(dir, "ws")
	var buf strings.Builder
	root := NewRootCmd(Options{})
	root.SetArgs([]string{"import", src, "--out", out, "--dry-run"})
	root.SetOut(&buf)
	root.SetErr(&buf)
	if err := root.Execute(); err != nil {
		t.Fatalf("import --dry-run: %v\n%s", err, buf.String())
	}
	if _, err := os.Stat(filepath.Join(out, "workspace.yaml")); !os.IsNotExist(err) {
		t.Error("--dry-run must not write workspace.yaml")
	}
	if !strings.Contains(buf.String(), "workspace.yaml") || !strings.Contains(buf.String(), "shared") {
		t.Errorf("dry-run should preview the workspace:\n%s", buf.String())
	}
}

func TestImportWritesSplitAndReport(t *testing.T) {
	dir, src := writeSample(t)
	out := filepath.Join(dir, "ws")
	var buf strings.Builder
	root := NewRootCmd(Options{})
	root.SetArgs([]string{"import", src, "--out", out})
	root.SetOut(&buf)
	root.SetErr(&buf)
	if err := root.Execute(); err != nil {
		t.Fatalf("import: %v\n%s", err, buf.String())
	}
	for _, rel := range []string{"workspace.yaml", filepath.Join("api", "devstack.yaml"), "devstack-import-report.txt"} {
		if _, err := os.Stat(filepath.Join(out, rel)); err != nil {
			t.Errorf("expected %s to be written: %v", rel, err)
		}
	}
	ws, _ := os.ReadFile(filepath.Join(out, "workspace.yaml"))
	if !strings.Contains(string(ws), "postgres") {
		t.Errorf("workspace missing shared postgres:\n%s", ws)
	}
	api, _ := os.ReadFile(filepath.Join(out, "api", "devstack.yaml"))
	if !strings.Contains(string(api), "workspace.shared.postgres") {
		t.Errorf("api uses not rewritten:\n%s", api)
	}
}

func TestImportNoClobberWithoutForce(t *testing.T) {
	dir, src := writeSample(t)
	out := filepath.Join(dir, "ws")
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(out, "workspace.yaml"), []byte("existing: keep\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	root := NewRootCmd(Options{})
	root.SetArgs([]string{"import", src, "--out", out})
	root.SetOut(&strings.Builder{})
	root.SetErr(&strings.Builder{})
	if err := root.Execute(); err == nil {
		t.Fatal("import must refuse to overwrite an existing workspace.yaml without --force")
	}
	// The original is untouched.
	if b, _ := os.ReadFile(filepath.Join(out, "workspace.yaml")); !strings.Contains(string(b), "existing: keep") {
		t.Error("existing workspace.yaml was modified despite no --force")
	}

	// With --force it backs up the original and writes the new one.
	root2 := NewRootCmd(Options{})
	root2.SetArgs([]string{"import", src, "--out", out, "--force"})
	root2.SetOut(&strings.Builder{})
	root2.SetErr(&strings.Builder{})
	if err := root2.Execute(); err != nil {
		t.Fatalf("import --force: %v", err)
	}
	matches, _ := filepath.Glob(filepath.Join(out, "workspace.yaml.bak.*"))
	if len(matches) == 0 {
		t.Error("--force should back up the original workspace.yaml")
	}
}
