package envingest

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goccy/go-yaml"
)

// fakeSops is a reversible, plaintext-hiding stand-in for the sops binary: it
// base64-encodes the JSON of the plaintext YAML so the "ciphertext" never embeds
// a literal secret value, and reverses it on decrypt.
func fakeEncrypt(_ context.Context, _ string, plaintext []byte) ([]byte, error) {
	var ms yaml.MapSlice
	if err := yaml.Unmarshal(plaintext, &ms); err != nil {
		return nil, err
	}
	m := map[string]string{}
	for _, it := range ms {
		m[it.Key.(string)] = toStr(it.Value)
	}
	j, _ := json.Marshal(m)
	enc := base64.StdEncoding.EncodeToString(j)
	return []byte("sops-fake:\n  data: " + enc + "\n"), nil
}

func fakeDecrypt(_ context.Context, ciphertext []byte) ([]byte, error) {
	s := strings.TrimSpace(string(ciphertext))
	const pfx = "sops-fake:\n  data: "
	s = strings.TrimPrefix(s, pfx)
	j, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
	if err != nil {
		return nil, err
	}
	return j, nil
}

func toStr(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

const fixtureEnv = `export DB_PASSWORD="s3cr3t-p@ss"
REDIS_URL=redis://shared-redis:6379/0
APP_ENV=local
STRIPE_SECRET_KEY=sk_live_51Hxxxxxxxxxxxxabcdef
PORT=8080
`

const fixtureWorkspace = `apiVersion: devstack/v1
kind: Workspace
name: demo
shared: {}
projects:
  - name: api
    path: api
`

const fixtureProject = `apiVersion: devstack/v1
kind: Project
name: api
services:
  api:
    template: node.vite
    env:
      raw:
        EXISTING: "keep" # keepme comment
`

// setupFixture writes a workspace/project/.env tree and returns Options.
func setupFixture(t *testing.T) (string, Options) {
	t.Helper()
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "workspace.yaml"), fixtureWorkspace)
	apiDir := filepath.Join(root, "api")
	if err := os.MkdirAll(apiDir, 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(apiDir, "devstack.yaml"), fixtureProject)
	mustWrite(t, filepath.Join(apiDir, ".env"), fixtureEnv)

	return root, Options{
		EnvPath:       filepath.Join(apiDir, ".env"),
		WorkspaceRoot: root,
		WorkspaceFile: filepath.Join(root, "workspace.yaml"),
		ProjectFile:   filepath.Join(apiDir, "devstack.yaml"),
		Service:       "api",
		Dest:          DestSOPS,
		DestPath:      DefaultSOPSFile,
		Provider:      "sops",
		Kind:          "sops",
		Recipient:     "age1testrecipient",
		AgeKey:        "/home/dev/.devstack/age/keys.txt",
	}
}

func fakeDeps() Deps {
	return Deps{
		EncryptYAML: fakeEncrypt,
		DecryptYAML: fakeDecrypt,
		GitTracked:  func(context.Context, string) (bool, error) { return false, nil },
	}
}

func mustWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestRunSopsHappyPath(t *testing.T) {
	root, opts := setupFixture(t)
	res, err := Run(context.Background(), opts, fakeDeps())
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	// secrets.enc.yaml written.
	enc := readFile(t, filepath.Join(root, DefaultSOPSFile))
	if enc == "" {
		t.Fatal("secrets.enc.yaml not written")
	}
	// devstack.yaml rewritten: refs for secrets, literals for config, comment kept.
	proj := readFile(t, opts.ProjectFile)
	for _, want := range []string{
		`DB_PASSWORD: "secret://sops/secrets.enc.yaml#DB_PASSWORD"`,
		`STRIPE_SECRET_KEY: "secret://sops/secrets.enc.yaml#STRIPE_SECRET_KEY"`,
		"APP_ENV: local",
		`PORT: "8080"`,
		"REDIS_URL: redis://shared-redis:6379/0",
		"keepme comment",
	} {
		if !strings.Contains(proj, want) {
			t.Errorf("devstack.yaml missing %q:\n%s", want, proj)
		}
	}
	// workspace.yaml scaffolded a sops provider.
	ws := readFile(t, opts.WorkspaceFile)
	if !strings.Contains(ws, "kind: sops") || !strings.Contains(ws, "name: sops") {
		t.Errorf("workspace.yaml not scaffolded:\n%s", ws)
	}
	// .gitignore fenced; .env removed.
	gi := readFile(t, filepath.Join(root, "api", ".gitignore"))
	if !strings.Contains(gi, ".env") || !strings.Contains(gi, gitignoreMarker) {
		t.Errorf(".gitignore not fenced: %q", gi)
	}
	if !res.EnvRemoved {
		t.Error("env not removed")
	}
	if _, err := os.Stat(opts.EnvPath); !os.IsNotExist(err) {
		t.Error(".env still present")
	}
}

func TestRunNoSecretLeak(t *testing.T) {
	root, opts := setupFixture(t)
	res, err := Run(context.Background(), opts, fakeDeps())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	plaintexts := []string{"s3cr3t-p@ss", "sk_live_51Hxxxxxxxxxxxxabcdef"}

	// Walk every file under root (committed files + any .bak) and assert none
	// contains an original secret value — the spec-24 leak test.
	err = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		body := readFile(t, p)
		for _, secret := range plaintexts {
			if strings.Contains(body, secret) {
				t.Errorf("LEAK: %q found in %s", secret, p)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// The fixture has pre-existing committed files, so .bak files are produced and
	// were included in the walk above — proving backups are leak-free too.
	if len(res.Backups) == 0 {
		t.Error("expected backups for the pre-existing committed files")
	}
}

func TestRunDryRunWritesNothing(t *testing.T) {
	root, opts := setupFixture(t)
	opts.DryRun = true
	before := snapshot(t, root)
	res, err := Run(context.Background(), opts, fakeDeps())
	if err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	if len(res.Plan.Decisions) != 5 {
		t.Fatalf("want 5 decisions, got %d", len(res.Plan.Decisions))
	}
	after := snapshot(t, root)
	if before != after {
		t.Fatalf("dry-run mutated the tree:\nbefore=%v\nafter=%v", before, after)
	}
	// .env still present.
	if _, err := os.Stat(opts.EnvPath); err != nil {
		t.Error("dry-run removed .env")
	}
}

func TestRunRefusesGitTrackedEnv(t *testing.T) {
	_, opts := setupFixture(t)
	deps := fakeDeps()
	deps.GitTracked = func(context.Context, string) (bool, error) { return true, nil }
	if _, err := Run(context.Background(), opts, deps); err == nil || !strings.Contains(err.Error(), "tracked by git") {
		t.Fatalf("want git-tracked refusal, got %v", err)
	}
}

func TestRunIdempotentDecryptCompare(t *testing.T) {
	root, opts := setupFixture(t)
	opts.KeepEnv = true // keep .env so the second run can re-read it
	if _, err := Run(context.Background(), opts, fakeDeps()); err != nil {
		t.Fatalf("first run: %v", err)
	}
	encPath := filepath.Join(root, DefaultSOPSFile)
	first := readFile(t, encPath)

	// Drop the scaffolded-provider need so the second run is a pure no-op compare.
	opts.ExistingProviders = []string{"sops"}
	res2, err := Run(context.Background(), opts, fakeDeps())
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	second := readFile(t, encPath)
	if first != second {
		t.Error("secrets.enc.yaml changed on identical re-run (decrypt-compare should skip)")
	}
	for _, w := range res2.Wrote {
		if strings.HasSuffix(w, DefaultSOPSFile) {
			t.Errorf("re-run rewrote the sops file: %v", res2.Wrote)
		}
	}
	if len(res2.Backups) != 0 {
		t.Errorf("re-run produced backups (not a clean diff): %v", res2.Backups)
	}
}

func TestRunRemoteRefusedWhenNoPusher(t *testing.T) {
	_, opts := setupFixture(t)
	opts.Dest = DestInfisical
	opts.Provider = "inf"
	opts.Kind = "infisical"
	opts.ExistingProviders = []string{"inf"}
	deps := fakeDeps()
	deps.Push = nil // read-only provider
	if _, err := Run(context.Background(), opts, deps); err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("want read-only refusal, got %v", err)
	}
}

func snapshot(t *testing.T, root string) string {
	t.Helper()
	var b strings.Builder
	_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		b.WriteString(rel + ":" + readFile(t, p) + "\n")
		return nil
	})
	return b.String()
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return string(b)
}
