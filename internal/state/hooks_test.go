package state

import "testing"

func TestHookRunLedger(t *testing.T) {
	db := openTestDB(t)

	// Unsatisfied before any run.
	if ok, err := db.HookSatisfied("api", "firstRun", "vol-abc"); err != nil || ok {
		t.Fatalf("HookSatisfied before run = %v, %v; want false, nil", ok, err)
	}

	// Record success → satisfied.
	if err := db.RecordHookRun("api", "firstRun", "vol-abc"); err != nil {
		t.Fatal(err)
	}
	if ok, _ := db.HookSatisfied("api", "firstRun", "vol-abc"); !ok {
		t.Error("HookSatisfied after record should be true")
	}
	// Idempotent record.
	if err := db.RecordHookRun("api", "firstRun", "vol-abc"); err != nil {
		t.Fatalf("re-record should be a no-op: %v", err)
	}

	// A different scope_key (e.g. volume reset) re-arms.
	if ok, _ := db.HookSatisfied("api", "firstRun", "vol-xyz"); ok {
		t.Error("a different scope_key must be unsatisfied (re-armed)")
	}
	// A different hook name is independent.
	if ok, _ := db.HookSatisfied("api", "postPull", "vol-abc"); ok {
		t.Error("a different hook must be unsatisfied")
	}

	// Force-rearm just this hook.
	_ = db.RecordHookRun("api", "postPull", "sha-1")
	n, err := db.DeleteHookRuns("api", "firstRun")
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("DeleteHookRuns(firstRun) removed %d, want 1", n)
	}
	if ok, _ := db.HookSatisfied("api", "firstRun", "vol-abc"); ok {
		t.Error("firstRun should be re-armed after delete")
	}
	if ok, _ := db.HookSatisfied("api", "postPull", "sha-1"); !ok {
		t.Error("postPull should survive a firstRun-only delete")
	}

	// Delete all for the project.
	if n, _ := db.DeleteHookRuns("api", ""); n != 1 {
		t.Errorf("DeleteHookRuns(all) removed %d, want 1", n)
	}
	if ok, _ := db.HookSatisfied("api", "postPull", "sha-1"); ok {
		t.Error("all hooks should be re-armed after project-wide delete")
	}
}
