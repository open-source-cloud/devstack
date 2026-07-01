package state

import (
	"context"
	"testing"
)

func TestActiveContextCRUD(t *testing.T) {
	db := openTestDB(t)

	// Nothing set yet.
	if _, ok, err := db.ActiveContext(); err != nil || ok {
		t.Fatalf("fresh ActiveContext = ok %v err %v, want ok=false", ok, err)
	}

	// Set a workspace + project.
	if err := db.SetActiveContext("/src/acme", "api"); err != nil {
		t.Fatalf("set: %v", err)
	}
	a, ok, err := db.ActiveContext()
	if err != nil || !ok {
		t.Fatalf("read = ok %v err %v, want ok=true", ok, err)
	}
	if a.WorkspaceRoot != "/src/acme" || a.Project != "api" {
		t.Fatalf("read = %+v, want /src/acme/api", a)
	}
	if a.UpdatedAt == "" {
		t.Error("updated_at should be stamped")
	}

	// Upsert: single row per ctx, values replaced.
	if err := db.SetActiveContext("/src/demo", "web"); err != nil {
		t.Fatalf("re-set: %v", err)
	}
	a, _, _ = db.ActiveContext()
	if a.WorkspaceRoot != "/src/demo" || a.Project != "web" {
		t.Fatalf("after upsert = %+v, want /src/demo/web", a)
	}

	// Empty project clears the project but keeps the workspace root.
	if err := db.SetActiveContext("/src/demo", ""); err != nil {
		t.Fatalf("clear project: %v", err)
	}
	a, _, _ = db.ActiveContext()
	if a.WorkspaceRoot != "/src/demo" || a.Project != "" {
		t.Fatalf("after clear-project = %+v, want /src/demo with empty project", a)
	}

	// Clear removes the row entirely.
	if err := db.ClearActiveContext(); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if _, ok, _ := db.ActiveContext(); ok {
		t.Error("after clear, ActiveContext should report unset")
	}
	// Clearing again is a no-op.
	if err := db.ClearActiveContext(); err != nil {
		t.Fatalf("second clear: %v", err)
	}
}

func TestActiveContextScopedByDockerContext(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	dbA, err := Open(ctx, dir, "ctxA")
	if err != nil {
		t.Fatalf("open ctxA: %v", err)
	}
	defer dbA.Close()
	dbB, err := Open(ctx, dir, "ctxB")
	if err != nil {
		t.Fatalf("open ctxB: %v", err)
	}
	defer dbB.Close()

	if err := dbA.SetActiveContext("/src/acme", "api"); err != nil {
		t.Fatalf("set A: %v", err)
	}
	// ctxB sees no active context — rows are keyed by Docker context.
	if _, ok, _ := dbB.ActiveContext(); ok {
		t.Error("ctxB should not see ctxA's active context")
	}
	if err := dbB.SetActiveContext("/src/other", "worker"); err != nil {
		t.Fatalf("set B: %v", err)
	}
	a, _, _ := dbA.ActiveContext()
	if a.Project != "api" {
		t.Errorf("ctxA active project = %q, want api (isolated from ctxB)", a.Project)
	}
}
