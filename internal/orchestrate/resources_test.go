package orchestrate

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/open-source-cloud/devstack/internal/config"
	"github.com/open-source-cloud/devstack/internal/docker"
	"github.com/open-source-cloud/devstack/internal/generate"
	"github.com/open-source-cloud/devstack/internal/profile"
	"github.com/open-source-cloud/devstack/internal/state"
	"github.com/open-source-cloud/devstack/internal/template"
	"github.com/open-source-cloud/devstack/internal/workspace"
	"github.com/open-source-cloud/devstack/templates"
)

// resourcesFixture builds a one-project workspace whose devstack.yaml declares a
// postgres database resource, so the spec-27 resources phase provisions it.
func resourcesFixture(t *testing.T, resourcesBlock string) (UpDeps, *fakeRunner, *state.DB) {
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
	write("app/devstack.yaml", "apiVersion: devstack/v1\nkind: Project\nname: app\nservices:\n  web:\n    template: node.vite\n    uses: [workspace.shared.postgres]\n"+resourcesBlock)

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

func TestResourcesPhaseProvisionsDeclaredDB(t *testing.T) {
	d, _, db := resourcesFixture(t, `resources:
  - { uses: workspace.shared.postgres, kind: database, name: reports }
`)
	rp := &recordingPg{}
	d.PgConnect = rp.connect

	recs, err := (&Saga{Workspace: d.Model.Workspace.Name, DB: db, LockPath: d.LockPath}).
		Run(context.Background(), mustPhases(t, d))
	if err != nil || AnyFailed(recs) {
		t.Fatalf("saga: %v\n%+v", err, recs)
	}
	got := map[string]string{}
	for _, r := range recs {
		got[r.Phase+scopeSuffix(r.Scope)] = r.Status
	}
	if got["resources"] != StatusOK {
		t.Fatalf("resources phase = %q, want ok (all: %+v)", got["resources"], got)
	}
	// The declared database + its role were provisioned and recorded.
	rows, _ := db.ProvisionedFor("app")
	var kinds []string
	for _, r := range rows {
		kinds = append(kinds, r.Kind+":"+r.Name)
	}
	if !slices.Contains(kinds, "database:reports") {
		t.Errorf("provisioned rows = %v, want database:reports", kinds)
	}
	// The provisioner ran the create path against the shared postgres on loopback.
	joined := ""
	for _, c := range rp.conns {
		joined += strings.Join(c.execs, " | ")
	}
	if !strings.Contains(joined, "CREATE DATABASE") {
		t.Errorf("declared resource DB was not created: %s", joined)
	}
}

func TestResourcesPhaseIdempotent(t *testing.T) {
	d, _, db := resourcesFixture(t, `resources:
  - { uses: workspace.shared.postgres, kind: database, name: reports }
`)
	saga := &Saga{Workspace: d.Model.Workspace.Name, DB: db, LockPath: d.LockPath}
	phases := mustPhases(t, d)
	if recs, err := saga.Run(context.Background(), phases); err != nil || AnyFailed(recs) {
		t.Fatalf("up #1: %v\n%+v", err, recs)
	}
	// Re-run → the resources phase skips on an unchanged fingerprint.
	recs2, err := saga.Run(context.Background(), phases)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range recs2 {
		if r.Phase == "resources" && r.Status != StatusSkipped {
			t.Errorf("resources phase should skip on re-run, got %q", r.Status)
		}
	}
	// Still exactly one database row (idempotent ledger).
	rows, _ := db.ProvisionedFor("app")
	n := 0
	for _, r := range rows {
		if r.Kind == "database" && r.Name == "reports" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("database:reports rows = %d, want 1", n)
	}
}

func TestResourcesPhaseReportsDriftWithoutDropping(t *testing.T) {
	// A bucket row lingers in the ledger but is no longer declared → drift, not drop.
	d, _, db := resourcesFixture(t, `resources:
  - { uses: workspace.shared.postgres, kind: database, name: reports }
`)
	_ = db.RecordProvisioned("app", "bucket", "app-orphan")

	decls := collectResourceDecls(d.Model, profile.Resolve(d.Model, nil))
	phase := resourcesPhase(d, decls)
	detail, err := phase.Run(context.Background())
	if err != nil {
		t.Fatalf("resources phase: %v", err)
	}
	m := detail.(map[string]any)
	drift := m["drift"].([]map[string]any)
	if len(drift) != 1 || drift[0]["name"] != "app-orphan" {
		t.Fatalf("want one drift entry for app-orphan, got %v", drift)
	}
	// NOT dropped: the row survives (never auto-drop on config deletion).
	rows, _ := db.ProvisionedFor("app")
	found := false
	for _, r := range rows {
		if r.Kind == "bucket" && r.Name == "app-orphan" {
			found = true
		}
	}
	if !found {
		t.Error("drifted resource must NOT be dropped (row disappeared)")
	}
}

func TestResourcesPhaseSkipsWhenNoProvision(t *testing.T) {
	d, _, db := resourcesFixture(t, `resources:
  - { uses: workspace.shared.postgres, kind: database, name: reports }
`)
	d.NoProvision = true
	recs, err := (&Saga{Workspace: d.Model.Workspace.Name, DB: db, LockPath: d.LockPath}).
		Run(context.Background(), mustPhases(t, d))
	if err != nil || AnyFailed(recs) {
		t.Fatalf("saga: %v\n%+v", err, recs)
	}
	for _, r := range recs {
		if r.Phase == "resources" {
			t.Error("resources phase present despite NoProvision")
		}
	}
}
