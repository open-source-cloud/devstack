package orchestrate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/open-source-cloud/devstack/internal/config"
	"github.com/open-source-cloud/devstack/internal/docker"
	"github.com/open-source-cloud/devstack/internal/generate"
	"github.com/open-source-cloud/devstack/internal/secrets"
	"github.com/open-source-cloud/devstack/internal/state"
	"github.com/open-source-cloud/devstack/internal/template"
	"github.com/open-source-cloud/devstack/internal/workspace"
	"github.com/open-source-cloud/devstack/templates"
)

// fakeRunner records compose invocations (with their injected env) and can fail
// selectively.
type fakeRunner struct {
	cmds [][]string
	envs [][]string // env passed alongside each cmd (same index)
	fail func(args []string) bool
}

func (f *fakeRunner) record(env []string, name string, args []string) error {
	f.cmds = append(f.cmds, append([]string{name}, args...))
	f.envs = append(f.envs, env)
	if f.fail != nil && f.fail(args) {
		return errors.New("compose failed")
	}
	return nil
}
func (f *fakeRunner) Run(_ context.Context, env []string, _, name string, args ...string) error {
	return f.record(env, name, args)
}
func (f *fakeRunner) Output(_ context.Context, env []string, _, name string, args ...string) ([]byte, error) {
	return nil, f.record(env, name, args)
}

// envForUp returns the env injected for a project's compose up (or nil).
func (f *fakeRunner) envForUp(project string) []string {
	for i, c := range f.cmds {
		joined := strings.Join(c, " ")
		if strings.Contains(joined, "-p "+project) && strings.Contains(joined, " up ") {
			return f.envs[i]
		}
	}
	return nil
}
func (f *fakeRunner) sawUp(project string) bool {
	for _, c := range f.cmds {
		joined := strings.Join(c, " ")
		if strings.Contains(joined, "-p "+project) && strings.Contains(joined, " up ") {
			return true
		}
	}
	return false
}
func (f *fakeRunner) sawDown(project string) bool {
	for _, c := range f.cmds {
		joined := strings.Join(c, " ")
		if strings.Contains(joined, "-p "+project) && strings.Contains(joined, " down") {
			return true
		}
	}
	return false
}

func upFixture(t *testing.T) (UpDeps, *fakeRunner, *state.DB) {
	t.Helper()
	root := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("workspace.yaml", "apiVersion: devstack/v1\nkind: Workspace\nname: demo\nshared:\n  postgres: { template: postgres, params: { version: \"16\" } }\nprojects:\n  - { name: app, path: app }\n")
	write("app/devstack.yaml", `apiVersion: devstack/v1
kind: Project
name: app
services:
  web:
    template: node.vite
    uses: [workspace.shared.postgres]
hooks:
  postUp:
    - { name: warm, run: host, command: ["true"], onFailure: warn }
`)

	m, err := config.LoadAt(root)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	db, err := state.Open(context.Background(), filepath.Join(root, "state"), "ctx")
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	mc := &docker.MockClient{
		Containers: []docker.Container{{
			ID: "pg1", Name: "devstack-shared-postgres-1", State: "running",
			Labels: map[string]string{generate.LabelManaged: "true", generate.LabelShared: "postgres"},
		}},
		Details: map[string]docker.ContainerDetails{
			"pg1": {ID: "pg1", State: "running", Running: true, Health: docker.HealthHealthy},
		},
	}
	src := template.NewFSSource(templates.FS)
	lockPath := filepath.Join(root, "lock")
	mgr := &workspace.Manager{Model: m, DB: db, Docker: mc, Source: src, LockPath: lockPath}
	fr := &fakeRunner{}
	d := UpDeps{
		Model: m, DB: db, Docker: mc, Manager: mgr, Source: src,
		LockPath: lockPath, Runner: fr, Env: map[string]string{},
	}
	return d, fr, db
}

func TestBuildUpHappyPath(t *testing.T) {
	d, fr, db := upFixture(t)
	phases, err := BuildUp(d)
	if err != nil {
		t.Fatalf("BuildUp: %v", err)
	}
	saga := &Saga{Workspace: d.Model.Workspace.Name, DB: db, LockPath: d.LockPath}
	recs, err := saga.Run(context.Background(), phases)
	if err != nil {
		t.Fatalf("saga: %v\n%+v", err, recs)
	}
	if AnyFailed(recs) {
		t.Fatalf("a phase failed: %+v", recs)
	}

	// Phase coverage: preflight, network, generate, shared, compose-up(app), hooks(app).
	got := map[string]string{}
	for _, r := range recs {
		got[r.Phase+scopeSuffix(r.Scope)] = r.Status
	}
	for _, want := range []string{"preflight", "network", "generate", "shared", "compose-up@app", "postUp@app"} {
		if got[want] != StatusOK {
			t.Errorf("phase %q = %q, want ok (all: %+v)", want, got[want], got)
		}
	}

	// Network ensured.
	if ok, _ := d.Docker.(*docker.MockClient).NetworkExists(context.Background(), generate.SharedNetwork); !ok {
		t.Error("shared network was not ensured")
	}
	// Ref row added for the shared instance.
	if n, _ := db.RefCount("shared-postgres"); n != 1 {
		t.Errorf("ref count for shared-postgres = %d, want 1", n)
	}
	// Compose up ran for both stacks.
	if !fr.sawUp(generate.SharedStackName) {
		t.Error("did not compose up the shared stack")
	}
	if !fr.sawUp("devstack-app") {
		t.Error("did not compose up the project stack")
	}
	// Generated compose files exist on disk.
	if _, err := os.Stat(filepath.Join(d.Model.Root, generate.GenDir, "shared", generate.ComposeFile)); err != nil {
		t.Errorf("shared compose not written: %v", err)
	}

	// Re-run is all skips (except AlwaysRun preflight/hooks).
	recs2, err := saga.Run(context.Background(), phases)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range recs2 {
		switch r.Phase {
		case "preflight", "secrets", "preUp", "postUp": // AlwaysRun phases
			if r.Status != StatusOK {
				t.Errorf("%s should re-run ok, got %q", r.Phase, r.Status)
			}
		default:
			if r.Status != StatusSkipped {
				t.Errorf("%s should skip on re-run, got %q", r.Phase, r.Status)
			}
		}
	}
}

func TestBuildUpCompensatesOnProjectFailure(t *testing.T) {
	d, fr, db := upFixture(t)
	// Fail the PROJECT compose up (not the shared one).
	fr.fail = func(args []string) bool {
		joined := strings.Join(args, " ")
		return strings.Contains(joined, "-p devstack-app") && strings.Contains(joined, "up")
	}
	phases, err := BuildUp(d)
	if err != nil {
		t.Fatal(err)
	}
	saga := &Saga{Workspace: d.Model.Workspace.Name, DB: db, LockPath: d.LockPath}
	recs, err := saga.Run(context.Background(), phases)
	if err == nil {
		t.Fatal("expected the saga to fail at compose-up")
	}
	// compose-up failed.
	var failed string
	for _, r := range recs {
		if r.Status == StatusFailed {
			failed = r.Phase
		}
	}
	if failed != "compose-up" {
		t.Errorf("failed phase = %q, want compose-up", failed)
	}
	// The shared phase compensated → ref rows dropped back to zero.
	if n, _ := db.RefCount("shared-postgres"); n != 0 {
		t.Errorf("ref count after compensation = %d, want 0", n)
	}
	// The failed project's stack was torn down (compose-up compensation).
	if !fr.sawDown("devstack-app") {
		t.Error("failed project stack was not composed down on compensation")
	}
}

func TestBuildUpUnknownProject(t *testing.T) {
	d, _, _ := upFixture(t)
	d.Projects = []string{"ghost"}
	if _, err := BuildUp(d); err == nil {
		t.Fatal("BuildUp should reject an unknown project")
	}
}

func scopeSuffix(scope string) string {
	if scope == "" {
		return ""
	}
	return "@" + scope
}

// fakeSecretProvider resolves any ref to a fixed value (S6 injection test).
type fakeSecretProvider struct{ val string }

func (fakeSecretProvider) Name() string { return "fake" }
func (p fakeSecretProvider) Resolve(_ context.Context, refs []secrets.Ref) (map[string]string, error) {
	out := map[string]string{}
	for _, r := range refs {
		out[r.Raw] = p.val
	}
	return out, nil
}

func TestBuildUpInjectsSecretEnv(t *testing.T) {
	root := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("workspace.yaml", "apiVersion: devstack/v1\nkind: Workspace\nname: demo\nprojects:\n  - { name: app, path: app }\n")
	write("app/devstack.yaml", "apiVersion: devstack/v1\nkind: Project\nname: app\nservices:\n  web:\n    template: node.vite\n    env:\n      raw: { DB_PASSWORD: \"secret://vault/f#k\" }\n")

	m, err := config.LoadAt(root)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	db, err := state.Open(context.Background(), filepath.Join(root, "state"), "ctx")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	mc := &docker.MockClient{}
	src := template.NewFSSource(templates.FS)
	lockPath := filepath.Join(root, "lock")
	fr := &fakeRunner{}

	// Inject a registry whose provider resolves the ref to a known value.
	reg := secrets.NewRegistry()
	reg.RegisterFactory("fake", func(secrets.ProviderConfig) (secrets.Provider, error) {
		return fakeSecretProvider{val: "resolved-pw"}, nil
	})
	reg.Configure(secrets.ProviderConfig{Name: "vault", Kind: "fake"})

	d := UpDeps{
		Model: m, DB: db, Docker: mc,
		Manager: &workspace.Manager{Model: m, DB: db, Docker: mc, Source: src, LockPath: lockPath},
		Source:  src, LockPath: lockPath, Runner: fr, Env: map[string]string{},
		Secrets: reg,
	}
	phases, err := BuildUp(d)
	if err != nil {
		t.Fatal(err)
	}
	saga := &Saga{Workspace: m.Workspace.Name, DB: db, LockPath: lockPath}
	if _, err := saga.Run(context.Background(), phases); err != nil {
		t.Fatalf("saga: %v", err)
	}
	// The resolved secret reaches compose up via its process env — and only there.
	env := fr.envForUp("devstack-app")
	found := false
	for _, kv := range env {
		if kv == "DB_PASSWORD=resolved-pw" {
			found = true
		}
	}
	if !found {
		t.Errorf("compose-up env should carry DB_PASSWORD=resolved-pw, got %v", env)
	}
}

func TestBuildUpHookOrdering(t *testing.T) {
	root := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("workspace.yaml", "apiVersion: devstack/v1\nkind: Workspace\nname: demo\nhooks:\n  preUp:\n    - { name: ws-banner, run: host, command: [\"true\"] }\nprojects:\n  - { name: app, path: app }\n")
	write("app/devstack.yaml", "apiVersion: devstack/v1\nkind: Project\nname: app\nservices:\n  web: { template: node.vite }\nhooks:\n  preUp:\n    - { name: migrate, run: host, command: [\"true\"] }\n")

	m, err := config.LoadAt(root)
	if err != nil {
		t.Fatal(err)
	}
	db, _ := state.Open(context.Background(), filepath.Join(root, "state"), "ctx")
	t.Cleanup(func() { db.Close() })
	mc := &docker.MockClient{}
	src := template.NewFSSource(templates.FS)
	lockPath := filepath.Join(root, "lock")
	d := UpDeps{
		Model: m, DB: db, Docker: mc,
		Manager: &workspace.Manager{Model: m, DB: db, Docker: mc, Source: src, LockPath: lockPath},
		Source:  src, LockPath: lockPath, Runner: &fakeRunner{}, Env: map[string]string{},
	}
	phases, err := BuildUp(d)
	if err != nil {
		t.Fatal(err)
	}
	saga := &Saga{Workspace: "demo", DB: db, LockPath: lockPath}
	recs, err := saga.Run(context.Background(), phases)
	if err != nil {
		t.Fatalf("saga: %v", err)
	}
	// Record the order of the phases we care about.
	idx := map[string]int{}
	for i, r := range recs {
		idx[r.Phase+scopeSuffix(r.Scope)] = i
	}
	// workspace preUp ("preUp", no scope) before project preUp before compose-up.
	if !(idx["preUp"] < idx["preUp@app"] && idx["preUp@app"] < idx["compose-up@app"]) {
		t.Errorf("hook ordering wrong: ws-preUp=%d app-preUp=%d compose-up=%d",
			idx["preUp"], idx["preUp@app"], idx["compose-up@app"])
	}
}
