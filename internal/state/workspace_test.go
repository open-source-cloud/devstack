package state

import (
	"context"
	"testing"
)

func TestWorkspaceRegistryCRUD(t *testing.T) {
	db := openTestDB(t)

	// Record two workspaces; RecordWorkspace is an upsert keyed by (ctx, root).
	if err := db.RecordWorkspace("acme", "/src/acme"); err != nil {
		t.Fatalf("record acme: %v", err)
	}
	if err := db.RecordWorkspace("demo", "/play/demo"); err != nil {
		t.Fatalf("record demo: %v", err)
	}

	ws, err := db.ListWorkspaces()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(ws) != 2 {
		t.Fatalf("len = %d, want 2 (%v)", len(ws), ws)
	}
	// Ordered by name: acme before demo.
	if ws[0].Name != "acme" || ws[0].Root != "/src/acme" {
		t.Errorf("row 0 = %+v, want acme/src/acme", ws[0])
	}
	if ws[0].LastUpAt == "" {
		t.Error("last_up_at should be stamped on record")
	}

	// Upsert the same root with a new name: still one row, name refreshed.
	firstUp := ws[0].LastUpAt
	if err := db.RecordWorkspace("acme-renamed", "/src/acme"); err != nil {
		t.Fatalf("re-record acme: %v", err)
	}
	ws, _ = db.ListWorkspaces()
	if len(ws) != 2 {
		t.Fatalf("after upsert len = %d, want 2", len(ws))
	}
	// The row for /src/acme now carries the new name (it may sort after demo now).
	var found *Workspace
	for i := range ws {
		if ws[i].Root == "/src/acme" {
			found = &ws[i]
		}
	}
	if found == nil || found.Name != "acme-renamed" {
		t.Fatalf("upsert did not refresh name: %+v", found)
	}
	_ = firstUp // last_up_at is refreshed too (datetime granularity may match)

	// Remove a vanished root.
	removed, err := db.RemoveWorkspace("/play/demo")
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	if !removed {
		t.Error("RemoveWorkspace should report a row was removed")
	}
	// Removing again is a no-op (idempotent).
	removed, _ = db.RemoveWorkspace("/play/demo")
	if removed {
		t.Error("second remove should report no row removed")
	}

	ws, _ = db.ListWorkspaces()
	if len(ws) != 1 || ws[0].Root != "/src/acme" {
		t.Fatalf("after remove = %v, want just /src/acme", ws)
	}
}

func TestWorkspaceRegistryCascadesWithContext(t *testing.T) {
	db := openTestDB(t) // context "ctx"
	if err := db.RecordWorkspace("acme", "/src/acme"); err != nil {
		t.Fatal(err)
	}
	// Deleting the docker_context row must CASCADE-remove its workspace rows.
	if _, err := db.Exec(`DELETE FROM docker_context WHERE name=?`, db.Ctx); err != nil {
		t.Fatalf("delete context: %v", err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM workspace`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("workspace rows after context delete = %d, want 0 (CASCADE)", n)
	}
}

func TestWorkspaceRegistryScopedByContext(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	dbA, err := Open(ctx, dir, "ctxA")
	if err != nil {
		t.Fatal(err)
	}
	defer dbA.Close()
	if err := dbA.RecordWorkspace("a", "/root/a"); err != nil {
		t.Fatal(err)
	}

	// A second context over the SAME ledger file must not see ctxA's row (WSL2
	// Desktop-vs-dockerd isolation).
	dbB, err := Open(ctx, dir, "ctxB")
	if err != nil {
		t.Fatal(err)
	}
	defer dbB.Close()
	ws, err := dbB.ListWorkspaces()
	if err != nil {
		t.Fatal(err)
	}
	if len(ws) != 0 {
		t.Fatalf("ctxB sees %d rows, want 0 (context isolation)", len(ws))
	}
}
