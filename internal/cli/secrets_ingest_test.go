package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func runIngest(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	t.Setenv("DEVSTACK_WORKSPACE", "")
	t.Setenv("DEVSTACK_HOME", t.TempDir())
	var buf strings.Builder
	root := NewRootCmd(Options{})
	root.SetArgs(append([]string{"secrets", "ingest"}, args...))
	root.SetOut(&buf)
	root.SetErr(&buf)
	err := root.Execute()
	return buf.String(), err
}

func writeIngestFixture(t *testing.T) (root, envPath string) {
	t.Helper()
	root = t.TempDir()
	mustWriteFile(t, filepath.Join(root, "workspace.yaml"), `apiVersion: devstack/v1
kind: Workspace
name: demo
shared: {}
projects:
  - name: api
    path: api
`)
	apiDir := filepath.Join(root, "api")
	if err := os.MkdirAll(apiDir, 0o755); err != nil {
		t.Fatal(err)
	}
	mustWriteFile(t, filepath.Join(apiDir, "devstack.yaml"), `apiVersion: devstack/v1
kind: Project
name: api
services:
  api:
    template: node.vite
`)
	envPath = filepath.Join(apiDir, ".env")
	mustWriteFile(t, envPath, `DB_PASSWORD=s3cr3t-p@ss
APP_ENV=local
PORT=8080
`)
	return root, envPath
}

func mustWriteFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSecretsIngestRegistered(t *testing.T) {
	root := NewRootCmd(Options{})
	c, _, err := root.Find([]string{"secrets", "ingest"})
	if err != nil || c.Name() != "ingest" || c.RunE == nil {
		t.Fatalf("secrets ingest not registered: %v", err)
	}
}

// TestSecretsIngestDryRunNonTTY proves the non-TTY/--json path skips the wizard,
// emits the plan, and writes nothing.
func TestSecretsIngestDryRunNonTTY(t *testing.T) {
	root, envPath := writeIngestFixture(t)
	before := readIfExists(envPath)

	out, err := runIngest(t, root, envPath, "--dry-run", "--json")
	if err != nil {
		t.Fatalf("ingest dry-run: %v\n%s", err, out)
	}

	var res struct {
		Plan struct {
			Decisions []struct {
				Key, Class, Reason, Ref string
			}
		}
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("parse json: %v\n%s", err, out)
	}
	if len(res.Plan.Decisions) != 3 {
		t.Fatalf("want 3 decisions, got %d", len(res.Plan.Decisions))
	}
	classByKey := map[string]string{}
	for _, d := range res.Plan.Decisions {
		classByKey[d.Key] = d.Class
	}
	if classByKey["DB_PASSWORD"] != "secret" {
		t.Errorf("DB_PASSWORD should be secret, got %s", classByKey["DB_PASSWORD"])
	}
	if classByKey["PORT"] != "config" || classByKey["APP_ENV"] != "config" {
		t.Errorf("PORT/APP_ENV should be config: %v", classByKey)
	}

	// Nothing written: .env unchanged, no secrets.enc.yaml.
	if readIfExists(envPath) != before {
		t.Error("dry-run mutated .env")
	}
	if _, err := os.Stat(filepath.Join(root, "secrets.enc.yaml")); !os.IsNotExist(err) {
		t.Error("dry-run wrote secrets.enc.yaml")
	}
}

func TestSecretsIngestRequiresServiceWhenAmbiguous(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "workspace.yaml"), `apiVersion: devstack/v1
kind: Workspace
name: demo
shared: {}
projects:
  - name: api
    path: api
`)
	apiDir := filepath.Join(root, "api")
	if err := os.MkdirAll(apiDir, 0o755); err != nil {
		t.Fatal(err)
	}
	mustWriteFile(t, filepath.Join(apiDir, "devstack.yaml"), `apiVersion: devstack/v1
kind: Project
name: api
services:
  web:
    template: node.vite
  worker:
    template: node.vite
`)
	envPath := filepath.Join(apiDir, ".env")
	mustWriteFile(t, envPath, "FOO=bar\n")

	out, err := runIngest(t, root, envPath, "--dry-run")
	if err == nil || !strings.Contains(err.Error(), "--service") {
		t.Fatalf("want ambiguous-service error, got %v\n%s", err, out)
	}
}

func readIfExists(p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return string(b)
}
