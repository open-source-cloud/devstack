package orchestrate

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	dbpkg "github.com/open-source-cloud/devstack/internal/db"
	"github.com/open-source-cloud/devstack/internal/provision"
	"github.com/open-source-cloud/devstack/internal/state"
)

// resetConn is a fake provision.Conn for the reset path: it records every Exec
// (so a test can assert the terminate/DROP/CREATE DDL ran) and answers the
// live-connection probe from a configurable flag. All other Exists queries (the
// EnsureProject role/db existence guards) return false so re-provision takes the
// CREATE path.
type resetConn struct {
	execs     []string
	connected bool // pg_stat_activity live-backend probe result
}

func (c *resetConn) Exec(_ context.Context, sql string, _ ...any) error {
	c.execs = append(c.execs, sql)
	return nil
}

func (c *resetConn) Exists(_ context.Context, sql string, _ ...any) (bool, error) {
	if strings.Contains(sql, "pg_stat_activity") {
		return c.connected, nil
	}
	return false, nil
}

func (c *resetConn) joined() string { return strings.Join(c.execs, " | ") }

// resetConnector hands out a single shared resetConn so the test inspects the SQL.
type resetConnector struct{ conn *resetConn }

func (rc *resetConnector) connect(_ context.Context, _ string) (provision.Conn, func() error, error) {
	return rc.conn, func() error { return nil }, nil
}

func TestResetDropsAndReprovisions(t *testing.T) {
	d, fr, ledger := upFixture(t)
	rc := &resetConnector{conn: &resetConn{}}
	d.PgConnect = rc.connect

	res, err := Reset(context.Background(), d, ResetOptions{Project: "app"})
	if err != nil {
		t.Fatalf("Reset: %v", err)
	}
	if res.Database != "app" || res.Role != "app" || res.Project != "app" {
		t.Errorf("unexpected reset result: %+v", res)
	}

	// The destructive DDL ran in order: terminate → DROP DATABASE → re-provision.
	j := rc.conn.joined()
	for _, want := range []string{"pg_terminate_backend", "DROP DATABASE IF EXISTS", "CREATE ROLE", "CREATE DATABASE"} {
		if !strings.Contains(j, want) {
			t.Errorf("reset DDL missing %q: %s", want, j)
		}
	}
	if ti := strings.Index(j, "pg_terminate_backend"); ti < 0 || ti > strings.Index(j, "DROP DATABASE") {
		t.Errorf("terminate must precede DROP DATABASE: %s", j)
	}
	if di := strings.Index(j, "DROP DATABASE"); di < 0 || di > strings.Index(j, "CREATE DATABASE") {
		t.Errorf("DROP DATABASE must precede the re-provision CREATE DATABASE: %s", j)
	}

	// The ledger re-recorded the role + database ownership rows.
	rows, _ := ledger.ProvisionedFor("app")
	var kinds []string
	for _, r := range rows {
		kinds = append(kinds, r.Kind+":"+r.Name)
	}
	if !slices.Contains(kinds, "role:app") || !slices.Contains(kinds, "database:app") {
		t.Errorf("provisioned rows = %v, want role:app + database:app", kinds)
	}

	// The reset was event-logged.
	if n := countEvents(t, ledger, "db.reset"); n == 0 {
		t.Error("db.reset event not logged")
	}

	// The loopback overlay was applied via compose up on the shared stack (same
	// host-reachability path as the provision phase).
	if !fr.saw("-p "+"devstack-shared", "compose.provision.yaml") {
		t.Errorf("reset did not apply the provision overlay via compose up: %v", fr.cmds)
	}
}

func TestResetRefusesStillConnectedWithoutForce(t *testing.T) {
	d, _, _ := upFixture(t)
	rc := &resetConnector{conn: &resetConn{connected: true}}
	d.PgConnect = rc.connect

	_, err := Reset(context.Background(), d, ResetOptions{Project: "app"})
	if err == nil {
		t.Fatal("Reset should refuse a still-connected database without --force")
	}
	if !strings.Contains(err.Error(), "active connections") {
		t.Errorf("unexpected error: %v", err)
	}
	// No destructive DDL may have run.
	if strings.Contains(rc.conn.joined(), "DROP DATABASE") {
		t.Errorf("DROP DATABASE ran despite the still-connected refusal: %s", rc.conn.joined())
	}

	// With --force it terminates + proceeds.
	rc2 := &resetConnector{conn: &resetConn{connected: true}}
	d.PgConnect = rc2.connect
	if _, err := Reset(context.Background(), d, ResetOptions{Project: "app", Force: true}); err != nil {
		t.Fatalf("Reset --force: %v", err)
	}
	if !strings.Contains(rc2.conn.joined(), "DROP DATABASE") {
		t.Errorf("Reset --force did not DROP DATABASE: %s", rc2.conn.joined())
	}
}

func TestResetJSONShape(t *testing.T) {
	d, _, _ := upFixture(t)
	rc := &resetConnector{conn: &resetConn{}}
	d.PgConnect = rc.connect

	res, err := Reset(context.Background(), d, ResetOptions{Project: "app"})
	if err != nil {
		t.Fatalf("Reset: %v", err)
	}
	b, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"project", "instance", "database", "role"} {
		if _, ok := got[key]; !ok {
			t.Errorf("reset json missing key %q: %s", key, b)
		}
	}
}

func TestPullRestoresNamedSnapshot(t *testing.T) {
	newSnapEnv(t)
	d, _, ledger := upFixture(t)
	dr := &dumpRunner{}
	dumper := dbpkg.PgDumper{Runner: dr, LookPath: func(string) (string, error) { return "/usr/bin/x", nil }}

	// Capture a snapshot to seed the local store.
	if _, err := Snapshot(context.Background(), d, dumper, SnapshotOptions{Project: "app", Name: "seed"}); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	// Pull it into a fresh (empty) tenant.
	dr.tables = 0
	meta, err := Pull(context.Background(), d, dumper, PullOptions{Project: "app", Name: "seed"})
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	if meta.Name != "seed" || meta.Database != "app" || meta.Digest == "" {
		t.Errorf("unexpected pull meta: %+v", meta)
	}
	pgr := dr.sawTool("pg_restore")
	if pgr == nil || !dr.restored {
		t.Fatalf("pg_restore never ran on pull: %v", dr.cmds)
	}
	rjoined := strings.Join(pgr, " ")
	for _, want := range []string{"-d app", "--clean", "--if-exists"} {
		if !strings.Contains(rjoined, want) {
			t.Errorf("pull pg_restore argv missing %q: %s", want, rjoined)
		}
	}

	// The pull was event-logged distinctly from a restore.
	if n := countEvents(t, ledger, "db.pull"); n == 0 {
		t.Error("db.pull event not logged")
	}
}

// countEvents counts event_log rows of a given kind (direct query — the ledger
// exposes no event-read API in this milestone).
func countEvents(t *testing.T, db *state.DB, kind string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM event_log WHERE kind=?`, kind).Scan(&n); err != nil {
		t.Fatalf("count events %q: %v", kind, err)
	}
	return n
}

func TestPullRefusesNonEmptyTenant(t *testing.T) {
	newSnapEnv(t)
	d, _, _ := upFixture(t)
	dr := &dumpRunner{}
	dumper := dbpkg.PgDumper{Runner: dr, LookPath: func(string) (string, error) { return "/usr/bin/x", nil }}

	if _, err := Snapshot(context.Background(), d, dumper, SnapshotOptions{Project: "app", Name: "seed"}); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	// A non-empty tenant → pull refuses (it seeds fresh tenants, never clobbers).
	dr.tables = 5
	if _, err := Pull(context.Background(), d, dumper, PullOptions{Project: "app", Name: "seed"}); err == nil {
		t.Fatal("Pull should refuse to overwrite a non-empty tenant")
	}
	if dr.restored {
		t.Error("pg_restore ran despite the non-empty refusal")
	}
}
