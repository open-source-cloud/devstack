package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// writeWorkspace lays down a minimal but real workspace (one shared engine + one
// project using a built-in template) and points discovery at it.
func writeWorkspace(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"workspace.yaml": "apiVersion: devstack/v1\nkind: Workspace\nname: demo\n" +
			"shared:\n  postgres: { template: postgres, params: { version: \"16\" } }\n" +
			"projects:\n  - { name: web, path: web }\n",
		"web/devstack.yaml": "apiVersion: devstack/v1\nkind: Project\nname: web\n" +
			"services:\n  web:\n    template: node.vite\n    uses: [workspace.shared.postgres]\n",
	}
	for rel, content := range files {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("DEVSTACK_WORKSPACE", root)
	return root
}

func runCmd(t *testing.T, args ...string) (string, error) {
	t.Helper()
	root := NewRootCmd(Options{})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(args)
	err := root.Execute()
	return out.String(), err
}

func TestGenerateCmdEndToEnd(t *testing.T) {
	root := writeWorkspace(t)

	out, err := runCmd(t, "generate", "--json")
	if err != nil {
		t.Fatalf("generate: %v\n%s", err, out)
	}
	var res struct {
		OK     bool `json:"ok"`
		Stacks []struct {
			Stack       string `json:"stack"`
			ComposePath string `json:"composePath"`
		} `json:"stacks"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("parse json: %v\n%s", err, out)
	}
	if !res.OK || len(res.Stacks) != 2 {
		t.Fatalf("want ok + 2 stacks, got %+v", res)
	}

	// Artifacts exist on disk.
	if _, err := os.Stat(filepath.Join(root, ".devstack", "shared", "docker-compose.yaml")); err != nil {
		t.Errorf("shared compose not written: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "web", ".devstack", "docker-compose.yaml")); err != nil {
		t.Errorf("web compose not written: %v", err)
	}

	// --check now reports up to date (exit 0).
	if out, err := runCmd(t, "generate", "--check"); err != nil {
		t.Errorf("generate --check should pass after generate: %v\n%s", err, out)
	}
}

func TestGenerateCheckFailsWhenStale(t *testing.T) {
	writeWorkspace(t)
	// No prior generation → everything is stale → non-zero exit.
	if _, err := runCmd(t, "generate", "--check"); err == nil {
		t.Fatal("generate --check should fail when artifacts are missing")
	}
}

func TestTemplateListCmd(t *testing.T) {
	out, err := runCmd(t, "template", "list", "--json")
	if err != nil {
		t.Fatalf("template list: %v", err)
	}
	var res struct {
		Templates []struct {
			Name string `json:"name"`
		} `json:"templates"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("parse: %v\n%s", err, out)
	}
	found := map[string]bool{}
	for _, it := range res.Templates {
		found[it.Name] = true
	}
	for _, want := range []string{"postgres", "redis", "minio", "php.nginx", "php.laravel.nginx", "node.vite"} {
		if !found[want] {
			t.Errorf("template list missing built-in %q", want)
		}
	}
}

func TestTemplateInitAndLint(t *testing.T) {
	dir := t.TempDir()
	if out, err := runCmd(t, "template", "init", "myimg", "--dir", dir); err != nil {
		t.Fatalf("template init: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(dir, "myimg", "template.yaml")); err != nil {
		t.Fatalf("scaffold missing template.yaml: %v", err)
	}
	// The scaffold must itself lint cleanly (renders + validates through compose-go).
	if out, err := runCmd(t, "template", "lint", filepath.Join(dir, "myimg")); err != nil {
		t.Fatalf("scaffolded template should lint: %v\n%s", err, out)
	}
}
