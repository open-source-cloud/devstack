package state

import "testing"

func TestSagaPhaseLifecycle(t *testing.T) {
	db := openTestDB(t)

	// Unrecorded phase: not satisfied, GetPhase ok=false.
	if ok, _ := db.PhaseSatisfied("acme", "", "network", "fp1"); ok {
		t.Fatal("unrecorded phase should not be satisfied")
	}
	if _, ok, _ := db.GetPhase("acme", "", "network"); ok {
		t.Fatal("unrecorded phase should report ok=false")
	}

	// Start → satisfied for fingerprint fp1.
	if err := db.StartPhase("acme", "", "network", "fp1"); err != nil {
		t.Fatal(err)
	}
	if err := db.SatisfyPhase("acme", "", "network"); err != nil {
		t.Fatal(err)
	}
	if ok, _ := db.PhaseSatisfied("acme", "", "network", "fp1"); !ok {
		t.Error("phase should be satisfied for the same fingerprint")
	}
	// A changed fingerprint re-arms the phase (skip check fails).
	if ok, _ := db.PhaseSatisfied("acme", "", "network", "fp2"); ok {
		t.Error("a changed fingerprint must NOT count as satisfied")
	}

	p, ok, err := db.GetPhase("acme", "", "network")
	if err != nil || !ok {
		t.Fatalf("get: ok=%v err=%v", ok, err)
	}
	if p.Status != PhaseSatisfied || p.SatisfiedAt == "" || p.StartedAt == "" {
		t.Errorf("phase row = %+v", p)
	}
}

func TestSagaPhaseRestartClearsState(t *testing.T) {
	db := openTestDB(t)
	_ = db.StartPhase("acme", "api", "provision", "fpA")
	_ = db.FailPhase("acme", "api", "provision", "boom")

	p, _, _ := db.GetPhase("acme", "api", "provision")
	if p.Status != PhaseFailed || p.Error != "boom" {
		t.Fatalf("expected failed with error, got %+v", p)
	}
	// Re-start clears the prior error + satisfied_at.
	if err := db.StartPhase("acme", "api", "provision", "fpB"); err != nil {
		t.Fatal(err)
	}
	p, _, _ = db.GetPhase("acme", "api", "provision")
	if p.Status != PhaseStarted || p.Error != "" || p.Fingerprint != "fpB" {
		t.Errorf("restart did not reset state: %+v", p)
	}
}

func TestSagaPhasesForAndClear(t *testing.T) {
	db := openTestDB(t)
	_ = db.StartPhase("acme", "", "network", "f")
	_ = db.SatisfyPhase("acme", "", "network")
	_ = db.StartPhase("acme", "api", "compose-up", "f")

	got, err := db.PhasesFor("acme")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("phases = %d, want 2", len(got))
	}

	if err := db.ClearPhase("acme", "", "network"); err != nil {
		t.Fatal(err)
	}
	if got, _ := db.PhasesFor("acme"); len(got) != 1 {
		t.Fatalf("phases after clear = %d, want 1", len(got))
	}
}

func TestSchemaVersionIsCurrent(t *testing.T) {
	db := openTestDB(t)
	v, err := db.SchemaVersion()
	if err != nil {
		t.Fatal(err)
	}
	if v != len(migrations) {
		t.Errorf("schema version = %d, want %d (all migrations applied)", v, len(migrations))
	}
}
