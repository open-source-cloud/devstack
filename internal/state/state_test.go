package state

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// TestMain points XDG_RUNTIME_DIR at a throwaway dir so the machine-global
// advisory lockfile created by Open() is hermetic and never touches the real
// user runtime dir.
func TestMain(m *testing.M) {
	tmp, err := os.MkdirTemp("", "devstack-state-runtime-")
	if err != nil {
		panic(err)
	}
	os.Setenv("XDG_RUNTIME_DIR", tmp)
	code := m.Run()
	os.RemoveAll(tmp)
	os.Exit(code)
}

func TestOpenMigratesAndIsIdempotent(t *testing.T) {
	dir := t.TempDir()

	db, err := Open(context.Background(), dir, "test-ctx")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	v, err := db.SchemaVersion()
	if err != nil {
		t.Fatalf("schema version: %v", err)
	}
	if v != len(migrations) {
		t.Fatalf("schema version = %d, want %d", v, len(migrations))
	}

	// The context row is recorded.
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM docker_context WHERE name = ?`, "test-ctx").Scan(&n); err != nil {
		t.Fatalf("query context: %v", err)
	}
	if n != 1 {
		t.Fatalf("docker_context rows = %d, want 1", n)
	}

	// A fresh DB must NOT leave a spurious backup file.
	if _, err := os.Stat(filepath.Join(dir, "state.db.bak.v0")); err == nil {
		t.Fatal("unexpected state.db.bak.v0 created for a fresh DB")
	}

	// Event log append works and is scoped to the context.
	db.LogEvent("test", "subject", "because")
	if err := db.QueryRow(`SELECT COUNT(*) FROM event_log WHERE ctx = ?`, "test-ctx").Scan(&n); err != nil {
		t.Fatalf("query events: %v", err)
	}
	if n != 1 {
		t.Fatalf("event_log rows = %d, want 1", n)
	}
	db.Close()

	// Re-open: must not re-run migrations and must preserve data.
	db2, err := Open(context.Background(), dir, "test-ctx")
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer db2.Close()
	if err := db2.QueryRow(`SELECT COUNT(*) FROM event_log`).Scan(&n); err != nil {
		t.Fatalf("query events after reopen: %v", err)
	}
	if n != 1 {
		t.Fatalf("event_log rows after reopen = %d, want 1 (data lost?)", n)
	}
}

func TestForeignKeysEnforced(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(context.Background(), dir, "ctx")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	// service_ref references docker_context(name); an unknown ctx must fail.
	_, err = db.Exec(`INSERT INTO service_ref(ctx, project, service, shared_service) VALUES(?,?,?,?)`,
		"nonexistent-ctx", "proj", "svc", "shared-postgres")
	if err == nil {
		t.Fatal("expected foreign-key violation for unknown ctx, got nil")
	}
}

func TestDefaultContextFallback(t *testing.T) {
	db, err := Open(context.Background(), t.TempDir(), "")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if db.Ctx != DefaultContext {
		t.Fatalf("Ctx = %q, want %q", db.Ctx, DefaultContext)
	}
}

func TestDBFileCreated(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(context.Background(), dir, "ctx")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if _, err := os.Stat(filepath.Join(dir, "state.db")); err != nil {
		t.Fatalf("state.db not created: %v", err)
	}
}

// TestSpecialCharPath guards the DSN escaping: a data dir containing '#'/'?'/spaces
// must still create state.db at the intended path (raw fmt.Sprintf truncated it).
func TestSpecialCharPath(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "weird dir#with?chars")
	db, err := Open(context.Background(), dir, "ctx")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if _, err := os.Stat(filepath.Join(dir, "state.db")); err != nil {
		t.Fatalf("state.db not at intended path %q: %v", dir, err)
	}
}

// TestConcurrentOpenSerializes is the spec-08 acceptance check: concurrent
// first-runs on a fresh DB serialize on the flock — no `database is locked`, no
// duplicate context row.
func TestConcurrentOpenSerializes(t *testing.T) {
	dir := t.TempDir()
	const n = 8

	var wg sync.WaitGroup
	errs := make([]error, n)
	dbs := make([]*DB, n)
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			dbs[i], errs[i] = Open(context.Background(), dir, "ctx")
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("concurrent open %d failed: %v", i, err)
		}
	}
	defer func() {
		for _, db := range dbs {
			if db != nil {
				db.Close()
			}
		}
	}()

	var cnt int
	if err := dbs[0].QueryRow(`SELECT COUNT(*) FROM docker_context`).Scan(&cnt); err != nil {
		t.Fatalf("count contexts: %v", err)
	}
	if cnt != 1 {
		t.Fatalf("docker_context rows = %d, want 1 (race inserted duplicates?)", cnt)
	}
}
