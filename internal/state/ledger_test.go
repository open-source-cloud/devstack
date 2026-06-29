package state

import (
	"context"
	"testing"
)

func openTestDB(t *testing.T) *DB {
	t.Helper()
	db, err := Open(context.Background(), t.TempDir(), "ctx")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestRefCounting(t *testing.T) {
	db := openTestDB(t)

	// up api → ref shared-postgres; up web → another ref.
	if err := db.AddRef("api", "api", "shared-postgres-16"); err != nil {
		t.Fatal(err)
	}
	if err := db.AddRef("web", "web", "shared-postgres-16"); err != nil {
		t.Fatal(err)
	}
	// AddRef is idempotent (re-up must not double-count).
	if err := db.AddRef("api", "api", "shared-postgres-16"); err != nil {
		t.Fatal(err)
	}
	if n, _ := db.RefCount("shared-postgres-16"); n != 2 {
		t.Fatalf("ref count = %d, want 2", n)
	}
	projs, _ := db.ProjectsUsing("shared-postgres-16")
	if len(projs) != 2 || projs[0] != "api" || projs[1] != "web" {
		t.Fatalf("projects using = %v, want [api web]", projs)
	}

	// down web → 1; down api → 0.
	if n, _ := db.RemoveProjectRefs("web"); n != 1 {
		t.Fatalf("removed = %d, want 1", n)
	}
	if n, _ := db.RefCount("shared-postgres-16"); n != 1 {
		t.Fatalf("ref count after down web = %d, want 1", n)
	}
	if _, err := db.RemoveProjectRefs("api"); err != nil {
		t.Fatal(err)
	}
	if n, _ := db.RefCount("shared-postgres-16"); n != 0 {
		t.Fatalf("ref count after down api = %d, want 0", n)
	}
}

func TestPruneRefsReconcile(t *testing.T) {
	db := openTestDB(t)
	_ = db.AddRef("api", "api", "shared-postgres-16")
	_ = db.AddRef("ghost", "svc", "shared-postgres-16") // stale: project no longer live

	pruned, err := db.PruneRefsForProjectsNotIn(map[string]bool{"api": true})
	if err != nil {
		t.Fatal(err)
	}
	if len(pruned) != 1 || pruned[0].Project != "ghost" {
		t.Fatalf("pruned = %v, want [ghost]", pruned)
	}
	if n, _ := db.RefCount("shared-postgres-16"); n != 1 {
		t.Fatalf("ref count after prune = %d, want 1", n)
	}
}

func TestSharedServiceLifecycle(t *testing.T) {
	db := openTestDB(t)
	s := SharedService{Name: "shared-postgres-16", Engine: "postgres", MajorVersion: "16", Status: "stopped"}
	if err := db.UpsertSharedService(s); err != nil {
		t.Fatal(err)
	}
	// Upsert again with running status (keyed by engine+major).
	if err := db.SetSharedStatus("postgres", "16", "running"); err != nil {
		t.Fatal(err)
	}
	got, ok, err := db.GetSharedService("postgres", "16")
	if err != nil || !ok {
		t.Fatalf("get: ok=%v err=%v", ok, err)
	}
	if got.Status != "running" || got.StartedAt == "" {
		t.Errorf("status=%q startedAt=%q, want running + a timestamp", got.Status, got.StartedAt)
	}
	// A second engine/version is a distinct instance.
	_ = db.UpsertSharedService(SharedService{Name: "shared-postgres-15", Engine: "postgres", MajorVersion: "15", Status: "running"})
	list, _ := db.ListSharedServices()
	if len(list) != 2 {
		t.Fatalf("shared services = %d, want 2 (one per (engine,major))", len(list))
	}
}

func TestPortAllocation(t *testing.T) {
	db := openTestDB(t)

	// First allocation picks the base port; same owner/purpose is stable.
	p1, err := db.AllocatePort("api", "app", 13000, 13100, func(int) bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	if p1 != 13000 {
		t.Fatalf("first port = %d, want 13000", p1)
	}
	p1again, _ := db.AllocatePort("api", "app", 13000, 13100, func(int) bool { return true })
	if p1again != p1 {
		t.Fatalf("re-allocation not stable: %d vs %d", p1again, p1)
	}

	// A different owner skips the taken port.
	p2, _ := db.AllocatePort("web", "app", 13000, 13100, func(int) bool { return true })
	if p2 != 13001 {
		t.Fatalf("second owner port = %d, want 13001", p2)
	}

	// isFree=false on a port is skipped (advisory bind-test/Docker union).
	p3, _ := db.AllocatePort("worker", "app", 13000, 13100, func(p int) bool { return p != 13002 })
	if p3 != 13003 {
		t.Fatalf("port with 13002 busy = %d, want 13003", p3)
	}

	// Release frees an owner's ports.
	if err := db.ReleasePortsFor("api"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := db.PortFor("api", "app"); ok {
		t.Error("port not released")
	}
}

func TestPortExhaustion(t *testing.T) {
	db := openTestDB(t)
	if _, err := db.AllocatePort("a", "app", 100, 100, func(int) bool { return false }); err == nil {
		t.Fatal("want exhaustion error when no port is free")
	}
}

func TestProvisionedLedger(t *testing.T) {
	db := openTestDB(t)
	_ = db.RecordProvisioned("api", "db", "api")
	_ = db.RecordProvisioned("api", "role", "api")
	_ = db.RecordProvisioned("api", "db", "api") // idempotent

	rows, _ := db.ProvisionedFor("api")
	if len(rows) != 2 {
		t.Fatalf("provisioned for api = %d, want 2", len(rows))
	}

	// Orphan detection: a provisioned project that's no longer active.
	_ = db.RecordProvisioned("removed", "db", "removed")
	orphans, _ := db.OrphanedProvisioned(map[string]bool{"api": true})
	if len(orphans) != 1 || orphans[0].Project != "removed" {
		t.Fatalf("orphans = %v, want [removed]", orphans)
	}

	if n, _ := db.RemoveProvisionedForProject("removed"); n != 1 {
		t.Fatalf("removed provisioned = %d, want 1", n)
	}
}

func TestRedisIndexAllocation(t *testing.T) {
	db := openTestDB(t)
	a, err := db.AllocateRedisIndex("api")
	if err != nil {
		t.Fatal(err)
	}
	if a != 0 {
		t.Fatalf("first redis index = %d, want 0", a)
	}
	// Stable for the same project.
	if again, _ := db.AllocateRedisIndex("api"); again != 0 {
		t.Fatalf("redis index not stable: %d", again)
	}
	// Next project gets the next free index.
	b, _ := db.AllocateRedisIndex("web")
	if b != 1 {
		t.Fatalf("second redis index = %d, want 1", b)
	}
}
