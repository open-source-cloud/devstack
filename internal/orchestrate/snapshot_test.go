package orchestrate

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	dbpkg "github.com/open-source-cloud/devstack/internal/db"
	"github.com/open-source-cloud/devstack/internal/store"
)

// dumpRunner is a fake db.Runner: it records argv, materializes a dump file when
// it sees pg_dump --file (so digest/size work), replays it on pg_restore, and
// drives the psql emptiness probe from a configurable table count.
type dumpRunner struct {
	cmds     [][]string
	envs     [][]string
	tables   int  // psql count(*) result for IsEmpty
	restored bool // set when pg_restore ran
}

func (r *dumpRunner) Run(_ context.Context, env []string, _, name string, args ...string) error {
	r.cmds = append(r.cmds, append([]string{name}, args...))
	r.envs = append(r.envs, env)
	if name == "pg_dump" {
		// Honor --file <path>: write a deterministic dump payload.
		for i, a := range args {
			if a == "--file" && i+1 < len(args) {
				_ = os.WriteFile(args[i+1], []byte("PGDMP-fake-dump"), 0o644)
			}
		}
	}
	if name == "pg_restore" {
		r.restored = true
	}
	return nil
}

func (r *dumpRunner) Output(_ context.Context, env []string, _, name string, args ...string) ([]byte, error) {
	r.cmds = append(r.cmds, append([]string{name}, args...))
	r.envs = append(r.envs, env)
	if name == "psql" {
		if r.tables == 0 {
			return []byte("0\n"), nil
		}
		return []byte("5\n"), nil
	}
	return nil, nil
}

func (r *dumpRunner) sawTool(tool string) []string {
	for _, c := range r.cmds {
		if c[0] == tool {
			return c
		}
	}
	return nil
}

func newSnapEnv(t *testing.T) {
	t.Helper()
	// Isolate the snapshot store so we never touch the real ~/.devstack.
	t.Setenv(store.HomeEnv, t.TempDir())
}

func TestSnapshotRestoreRoundTrip(t *testing.T) {
	newSnapEnv(t)
	d, fr, ledger := upFixture(t)
	dr := &dumpRunner{}
	dumper := dbpkg.PgDumper{Runner: dr, LookPath: func(string) (string, error) { return "/usr/bin/x", nil }}

	meta, err := Snapshot(context.Background(), d, dumper, SnapshotOptions{Project: "app", Name: "before"})
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	// pg_dump ran with the tenant db + custom format, over the loopback overlay.
	pgd := dr.sawTool("pg_dump")
	if pgd == nil {
		t.Fatalf("pg_dump never ran: %v", dr.cmds)
	}
	joined := strings.Join(pgd, " ")
	for _, want := range []string{"-h 127.0.0.1", "-d app", "--format=custom", "--no-owner", "--file"} {
		if !strings.Contains(joined, want) {
			t.Errorf("pg_dump argv missing %q: %s", want, joined)
		}
	}
	// The password rode PGPASSWORD in the env, never on the argv.
	if strings.Contains(joined, "PGPASSWORD") || strings.Contains(joined, "devstack") && strings.Contains(joined, "password") {
		t.Errorf("password leaked into pg_dump argv: %s", joined)
	}
	if !slices.ContainsFunc(dr.envs, func(e []string) bool {
		return slices.ContainsFunc(e, func(kv string) bool { return strings.HasPrefix(kv, "PGPASSWORD=") })
	}) {
		t.Error("PGPASSWORD not passed via env")
	}

	// The host-port overlay was allocated in the ledger and applied via compose up.
	port, ok, _ := ledger.PortFor("shared-postgres", "pg-provision")
	if !ok || port == 0 {
		t.Errorf("host port not allocated for the snapshot overlay: port=%d ok=%v", port, ok)
	}
	if !fr.saw("-p devstack-shared", "compose.provision.yaml") {
		t.Errorf("loopback overlay not applied via compose up: %v", fr.cmds)
	}

	// The ledger recorded the snapshot row.
	rows, _ := ledger.ProvisionedFor("app")
	found := false
	for _, r := range rows {
		if r.Kind == snapshotKind && r.Name == "before" {
			found = true
		}
	}
	if !found {
		t.Errorf("snapshot ledger row not recorded: %v", rows)
	}

	// The dump + sidecar landed in the workspace store.
	if _, err := os.Stat(meta.Path); err != nil {
		t.Errorf("dump file missing: %v", err)
	}
	if meta.Digest == "" || meta.Size == 0 {
		t.Errorf("meta digest/size not computed: %+v", meta)
	}
	wantDir := store.SnapshotsPath("demo")
	if filepath.Dir(meta.Path) != wantDir {
		t.Errorf("dump dir = %q, want %q", filepath.Dir(meta.Path), wantDir)
	}

	// Restore round-trips (tenant is empty → no --force needed).
	dr.tables = 0
	rmeta, err := Restore(context.Background(), d, dumper, RestoreOptions{Project: "app", Name: "before"})
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if rmeta.Digest != meta.Digest {
		t.Errorf("restore digest %q != snapshot digest %q", rmeta.Digest, meta.Digest)
	}
	pgr := dr.sawTool("pg_restore")
	if pgr == nil {
		t.Fatalf("pg_restore never ran: %v", dr.cmds)
	}
	rjoined := strings.Join(pgr, " ")
	for _, want := range []string{"-d app", "--clean", "--if-exists", "--no-owner"} {
		if !strings.Contains(rjoined, want) {
			t.Errorf("pg_restore argv missing %q: %s", want, rjoined)
		}
	}
	if !dr.restored {
		t.Error("pg_restore was not invoked")
	}
}

func TestRestoreRefusesNonEmptyWithoutForce(t *testing.T) {
	newSnapEnv(t)
	d, _, _ := upFixture(t)
	dr := &dumpRunner{}
	dumper := dbpkg.PgDumper{Runner: dr, LookPath: func(string) (string, error) { return "/usr/bin/x", nil }}

	if _, err := Snapshot(context.Background(), d, dumper, SnapshotOptions{Project: "app", Name: "snap"}); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	// Tenant now reports 5 tables → restore must refuse without --force.
	dr.tables = 5
	_, err := Restore(context.Background(), d, dumper, RestoreOptions{Project: "app", Name: "snap"})
	if err == nil {
		t.Fatal("Restore should refuse a non-empty tenant without --force")
	}
	if !strings.Contains(err.Error(), "non-empty") {
		t.Errorf("unexpected error: %v", err)
	}
	// pg_restore must NOT have run.
	if dr.restored {
		t.Error("pg_restore ran despite the non-empty refusal")
	}

	// With --force it proceeds.
	if _, err := Restore(context.Background(), d, dumper, RestoreOptions{Project: "app", Name: "snap", Force: true}); err != nil {
		t.Fatalf("Restore --force: %v", err)
	}
	if !dr.restored {
		t.Error("pg_restore did not run under --force")
	}
}

// TestSnapshotJSONContract asserts the documented --json schema (name, kind,
// digest, size, created_at) round-trips through the marshaled SnapshotMeta — the
// non-TTY contract the `db snapshot --json` / `db snapshot ls --json` verbs emit.
func TestSnapshotJSONContract(t *testing.T) {
	newSnapEnv(t)
	d, _, _ := upFixture(t)
	dr := &dumpRunner{}
	dumper := dbpkg.PgDumper{Runner: dr, LookPath: func(string) (string, error) { return "/usr/bin/x", nil }}

	meta, err := Snapshot(context.Background(), d, dumper, SnapshotOptions{Project: "app", Name: "j1"})
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	b, err := json.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"name", "project", "kind", "database", "digest", "size", "created_at", "path"} {
		if _, ok := got[key]; !ok {
			t.Errorf("snapshot json missing key %q: %s", key, b)
		}
	}
	if got["name"] != "j1" || got["kind"] != "pg" {
		t.Errorf("unexpected json values: %s", b)
	}
}

func TestListSnapshots(t *testing.T) {
	newSnapEnv(t)
	d, _, _ := upFixture(t)
	dr := &dumpRunner{}
	dumper := dbpkg.PgDumper{Runner: dr, LookPath: func(string) (string, error) { return "/usr/bin/x", nil }}

	for _, n := range []string{"one", "two"} {
		if _, err := Snapshot(context.Background(), d, dumper, SnapshotOptions{Project: "app", Name: n}); err != nil {
			t.Fatalf("Snapshot %s: %v", n, err)
		}
	}
	snaps, err := ListSnapshots(d, "app")
	if err != nil {
		t.Fatalf("ListSnapshots: %v", err)
	}
	if len(snaps) != 2 {
		t.Fatalf("want 2 snapshots, got %d: %+v", len(snaps), snaps)
	}
	names := []string{snaps[0].Name, snaps[1].Name}
	if !slices.Contains(names, "one") || !slices.Contains(names, "two") {
		t.Errorf("snapshot names = %v, want one+two", names)
	}
	for _, s := range snaps {
		if s.Database != "app" || s.Digest == "" {
			t.Errorf("snapshot meta incomplete: %+v", s)
		}
	}
}
