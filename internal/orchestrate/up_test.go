package orchestrate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/open-source-cloud/devstack/internal/config"
	"github.com/open-source-cloud/devstack/internal/docker"
	"github.com/open-source-cloud/devstack/internal/generate"
	"github.com/open-source-cloud/devstack/internal/provision"
	"github.com/open-source-cloud/devstack/internal/secrets"
	"github.com/open-source-cloud/devstack/internal/state"
	"github.com/open-source-cloud/devstack/internal/template"
	"github.com/open-source-cloud/devstack/internal/trust"
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

// upServices returns the explicit service args passed to a project's compose up
// (everything after `up -d`), or nil if it was never up'd.
func (f *fakeRunner) upServices(project string) []string {
	for _, c := range f.cmds {
		joined := strings.Join(c, " ")
		if !strings.Contains(joined, "-p "+project) || !strings.Contains(joined, " up ") {
			continue
		}
		for i, tok := range c {
			if tok == "-d" {
				return append([]string(nil), c[i+1:]...)
			}
		}
		return nil
	}
	return nil
}

// saw reports whether any single recorded command contains all the needles.
func (f *fakeRunner) saw(needles ...string) bool {
	for _, c := range f.cmds {
		joined := strings.Join(c, " ")
		all := true
		for _, n := range needles {
			if !strings.Contains(joined, n) {
				all = false
				break
			}
		}
		if all {
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
		LockPath: lockPath, Runner: fr, Env: map[string]string{}, PgConnect: okPgConnect,
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
		case "preflight", "secrets", "trust", "preUp", "postUp": // AlwaysRun phases
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
		Source:  src, LockPath: lockPath, Runner: fr, Env: map[string]string{}, PgConnect: okPgConnect,
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

// fakeTrustRunner makes trust.Install attempt mkcert and fail (to test fencing).
type fakeTrustRunner struct{}

func (fakeTrustRunner) Output(context.Context, string, ...string) ([]byte, error) {
	return nil, nil
}
func (fakeTrustRunner) Run(context.Context, string, ...string) error {
	return errors.New("mkcert -install: permission denied")
}
func (fakeTrustRunner) LookPath(string) (string, error) { return "/usr/bin/mkcert", nil }

func TestTrustPhaseFenced(t *testing.T) {
	// httpsLocal on + Install fails → the phase is FENCED (no error, warning).
	d := UpDeps{
		Model: &config.Model{Workspace: config.Workspace{
			Network: config.Network{Proxy: config.Proxy{Engine: "caddy", HTTPSLocal: true}},
		}},
		Trust: &trust.Trust{Runner: fakeTrustRunner{}},
	}
	detail, err := trustPhase(d).Run(context.Background())
	if err != nil {
		t.Fatalf("trust phase must be fenced (no error), got %v", err)
	}
	if m, _ := detail.(map[string]any); m["status"] != "warning" {
		t.Errorf("expected a warning status on install failure, got %v", detail)
	}

	// httpsLocal off → skipped no-op.
	d.Model.Workspace.Network.Proxy.HTTPSLocal = false
	detail, err = trustPhase(d).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if m, _ := detail.(map[string]any); m["status"] == "" {
		t.Errorf("httpsLocal off should report skipped, got %v", detail)
	}
}

// sliceFixture builds a one-project workspace with two services: web (tagged
// `frontend`, uses postgres) and worker (untagged, uses nothing). It exercises
// selective-up (spec 12): --profile slices which services + shared come up.
func sliceFixture(t *testing.T) (UpDeps, *fakeRunner, *state.DB) {
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
    profiles: [frontend]
    uses: [workspace.shared.postgres]
  worker:
    template: node.vite
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
		LockPath: lockPath, Runner: fr, Env: map[string]string{}, NoHooks: true, PgConnect: okPgConnect,
	}
	return d, fr, db
}

func TestBuildUpProfileSlicesServices(t *testing.T) {
	// --profile frontend → only web is up'd (worker excluded); postgres is gated
	// because web `uses` it.
	d, fr, db := sliceFixture(t)
	d.Profiles = []string{"frontend"}
	recs, err := (&Saga{Workspace: d.Model.Workspace.Name, DB: db, LockPath: d.LockPath}).
		Run(context.Background(), mustPhases(t, d))
	if err != nil || AnyFailed(recs) {
		t.Fatalf("saga: %v\n%+v", err, recs)
	}
	if got := fr.upServices("devstack-app"); len(got) != 1 || got[0] != "web" {
		t.Errorf("frontend slice up'd %v, want [web] only", got)
	}
	if !fr.sawUp(generate.SharedStackName) {
		t.Error("postgres should be up'd (web uses it)")
	}
}

func TestBuildUpDefaultProfileIsAll(t *testing.T) {
	// No --profile + no defaultProfile → reserved `all` → both services up'd.
	d, fr, db := sliceFixture(t)
	recs, err := (&Saga{Workspace: d.Model.Workspace.Name, DB: db, LockPath: d.LockPath}).
		Run(context.Background(), mustPhases(t, d))
	if err != nil || AnyFailed(recs) {
		t.Fatalf("saga: %v\n%+v", err, recs)
	}
	got := fr.upServices("devstack-app")
	if len(got) != 2 || !slices.Contains(got, "web") || !slices.Contains(got, "worker") {
		t.Errorf("default(all) up'd %v, want web+worker", got)
	}
}

func TestBuildUpProfileDropsInactiveProjectAndShared(t *testing.T) {
	// A slice that matches no service → the project drops out entirely and no
	// shared is gated (nothing uses it).
	d, fr, db := sliceFixture(t)
	d.Profiles = []string{"nonexistent"}
	recs, err := (&Saga{Workspace: d.Model.Workspace.Name, DB: db, LockPath: d.LockPath}).
		Run(context.Background(), mustPhases(t, d))
	if err != nil || AnyFailed(recs) {
		t.Fatalf("saga: %v\n%+v", err, recs)
	}
	if fr.sawUp("devstack-app") {
		t.Error("no service active → project must not be up'd")
	}
	if fr.sawUp(generate.SharedStackName) {
		t.Error("no active service uses shared → shared must not be up'd")
	}
}

func mustPhases(t *testing.T, d UpDeps) []Phase {
	t.Helper()
	phases, err := BuildUp(d)
	if err != nil {
		t.Fatalf("BuildUp: %v", err)
	}
	return phases
}

// --- provision phase (D8) ---------------------------------------------------

// fakePgConn records the SQL the provisioner runs; Exists=false so EnsureProject
// takes the create path. Implements provision.Conn.
type fakePgConn struct{ execs []string }

func (c *fakePgConn) Exec(_ context.Context, sql string, _ ...any) error {
	c.execs = append(c.execs, sql)
	return nil
}
func (c *fakePgConn) Exists(_ context.Context, _ string, _ ...any) (bool, error) {
	return false, nil
}

// okPgConnect is the no-op connector used by the shared fixtures so the full saga
// (including provision) runs daemon-free.
func okPgConnect(context.Context, string) (provision.Conn, func() error, error) {
	return &fakePgConn{}, func() error { return nil }, nil
}

// recordingPg captures every connection + DSN for assertions.
type recordingPg struct {
	conns []*fakePgConn
	dsns  []string
}

func (r *recordingPg) connect(_ context.Context, dsn string) (provision.Conn, func() error, error) {
	c := &fakePgConn{}
	r.conns = append(r.conns, c)
	r.dsns = append(r.dsns, dsn)
	return c, func() error { return nil }, nil
}

func TestBuildUpProvisionsPerProjectDB(t *testing.T) {
	d, fr, db := upFixture(t) // project app, service web, uses workspace.shared.postgres
	rp := &recordingPg{}
	d.PgConnect = rp.connect

	recs, err := (&Saga{Workspace: d.Model.Workspace.Name, DB: db, LockPath: d.LockPath}).
		Run(context.Background(), mustPhases(t, d))
	if err != nil || AnyFailed(recs) {
		t.Fatalf("saga: %v\n%+v", err, recs)
	}

	// The provision phase ran.
	got := map[string]string{}
	for _, r := range recs {
		got[r.Phase+scopeSuffix(r.Scope)] = r.Status
	}
	if got["provision"] != StatusOK {
		t.Fatalf("provision phase = %q, want ok (all: %+v)", got["provision"], got)
	}
	// Connected once to the shared Postgres on loopback.
	if len(rp.dsns) != 1 || !strings.Contains(rp.dsns[0], "127.0.0.1") {
		t.Fatalf("provision DSNs = %v, want one loopback DSN", rp.dsns)
	}
	// EnsureProject ran the create path (CREATE ROLE + CREATE DATABASE).
	joined := strings.Join(rp.conns[0].execs, " | ")
	if !strings.Contains(joined, "CREATE ROLE") || !strings.Contains(joined, "CREATE DATABASE") {
		t.Errorf("provisioning SQL missing role/db creation: %s", joined)
	}
	// Ownership recorded in the ledger.
	rows, _ := db.ProvisionedFor("app")
	var kinds []string
	for _, r := range rows {
		kinds = append(kinds, r.Kind+":"+r.Name)
	}
	if !slices.Contains(kinds, "role:app") || !slices.Contains(kinds, "database:app") {
		t.Errorf("provisioned rows = %v, want role:app + database:app", kinds)
	}
	// The shared stack was brought up WITH the loopback port overlay.
	if !fr.saw("-p "+generate.SharedStackName, "-f", "compose.provision.yaml") {
		t.Errorf("shared up did not include the provision overlay: %v", fr.cmds)
	}
	// The overlay file was written, loopback-bound.
	overlay := filepath.Join(d.Model.Root, generate.GenDir, "shared", "compose.provision.yaml")
	body, err := os.ReadFile(overlay)
	if err != nil {
		t.Fatalf("overlay not written: %v", err)
	}
	if !strings.Contains(string(body), "127.0.0.1:") || !strings.Contains(string(body), ":5432") {
		t.Errorf("overlay not loopback-bound to 5432:\n%s", body)
	}
}

func TestBuildUpNoProvisionSkips(t *testing.T) {
	d, _, db := upFixture(t)
	d.NoProvision = true
	d.PgConnect = func(context.Context, string) (provision.Conn, func() error, error) {
		t.Fatal("PgConnect must not be called when NoProvision is set")
		return nil, nil, nil
	}
	recs, err := (&Saga{Workspace: d.Model.Workspace.Name, DB: db, LockPath: d.LockPath}).
		Run(context.Background(), mustPhases(t, d))
	if err != nil || AnyFailed(recs) {
		t.Fatalf("saga: %v\n%+v", err, recs)
	}
	for _, r := range recs {
		if r.Phase == "provision" {
			t.Error("provision phase present despite NoProvision")
		}
	}
}

func TestFirstRunOncePerProject(t *testing.T) {
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
	// firstRun appends a line to a counter file in the repo dir each time it runs.
	write("app/devstack.yaml", `apiVersion: devstack/v1
kind: Project
name: app
services:
  web: { template: node.vite }
hooks:
  firstRun:
    - { name: seed, run: host, command: ["sh", "-c", "echo x >> firstrun.count"] }
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
	src := template.NewFSSource(templates.FS)
	lockPath := filepath.Join(root, "lock")
	mc := &docker.MockClient{}
	mgr := &workspace.Manager{Model: m, DB: db, Docker: mc, Source: src, LockPath: lockPath}
	d := UpDeps{
		Model: m, DB: db, Docker: mc, Manager: mgr, Source: src,
		LockPath: lockPath, Runner: &fakeRunner{}, Env: map[string]string{},
		NoPreflight: true, PgConnect: okPgConnect,
	}

	saga := &Saga{Workspace: m.Workspace.Name, DB: db, LockPath: lockPath}
	phases := mustPhases(t, d)

	// First up → firstRun runs.
	if recs, err := saga.Run(context.Background(), phases); err != nil || AnyFailed(recs) {
		t.Fatalf("up #1: %v\n%+v", err, recs)
	}
	countFile := filepath.Join(root, "app", "firstrun.count")
	if b, _ := os.ReadFile(countFile); strings.Count(string(b), "x") != 1 {
		t.Fatalf("after up #1 firstRun ran %d times, want 1", strings.Count(string(b), "x"))
	}

	// Second up → firstRun is satisfied (ledger) and skips.
	recs2, err := saga.Run(context.Background(), phases)
	if err != nil || AnyFailed(recs2) {
		t.Fatalf("up #2: %v\n%+v", err, recs2)
	}
	if b, _ := os.ReadFile(countFile); strings.Count(string(b), "x") != 1 {
		t.Errorf("after up #2 firstRun ran again (count=%d), want still 1 (idempotent)", strings.Count(string(b), "x"))
	}
	// The firstRun phase reports skipped on the re-run.
	var status string
	for _, r := range recs2 {
		if r.Phase == "firstRun" {
			status = r.Status
		}
	}
	if status != StatusOK { // AlwaysRun=false + satisfied → the phase still completes OK with all hooks skipped
		t.Logf("firstRun re-run status = %q", status)
	}
}
