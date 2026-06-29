package orchestrate

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/open-source-cloud/devstack/internal/state"
)

func newSaga(t *testing.T) *Saga {
	t.Helper()
	dir := t.TempDir()
	db, err := state.Open(context.Background(), dir, "ctx")
	if err != nil {
		t.Fatalf("open state: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return &Saga{Workspace: "demo", DB: db, LockPath: filepath.Join(dir, "lock")}
}

// recordingPhase builds a Phase that counts its runs/compensations via the given
// pointers, with a fixed fingerprint.
func recordingPhase(name string, runs, comps *int, fp string, fail bool) Phase {
	return Phase{
		Name:     name,
		Mutating: true,
		Fingerprint: func(context.Context) (string, error) {
			if fp == "" {
				return "", nil
			}
			return Fingerprint(fp), nil
		},
		Run: func(context.Context) (any, error) {
			*runs++
			if fail {
				return nil, errors.New("boom in " + name)
			}
			return map[string]any{"name": name}, nil
		},
		Compensate: func(context.Context) error {
			*comps++
			return nil
		},
	}
}

func TestSagaHappyPath(t *testing.T) {
	s := newSaga(t)
	var aRuns, aComp, bRuns, bComp int
	phases := []Phase{
		recordingPhase("network", &aRuns, &aComp, "fp-a", false),
		recordingPhase("shared", &bRuns, &bComp, "fp-b", false),
	}
	recs, err := s.Run(context.Background(), phases)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(recs) != 2 || recs[0].Status != StatusOK || recs[1].Status != StatusOK {
		t.Fatalf("records = %+v, want two ok", recs)
	}
	if aRuns != 1 || bRuns != 1 {
		t.Errorf("runs a=%d b=%d, want 1 each", aRuns, bRuns)
	}
	if aComp != 0 || bComp != 0 {
		t.Errorf("no compensation on success; got a=%d b=%d", aComp, bComp)
	}
	if recs[0].Error != nil {
		t.Error("ok record should have null error")
	}
}

func TestSagaSkipsSatisfiedOnRerun(t *testing.T) {
	s := newSaga(t)
	var runs, comps int
	mk := func() []Phase {
		return []Phase{recordingPhase("generate", &runs, &comps, "fp", false)}
	}
	if _, err := s.Run(context.Background(), mk()); err != nil {
		t.Fatal(err)
	}
	recs, err := s.Run(context.Background(), mk())
	if err != nil {
		t.Fatal(err)
	}
	if recs[0].Status != StatusSkipped {
		t.Errorf("re-run status = %q, want skipped", recs[0].Status)
	}
	if runs != 1 {
		t.Errorf("Run executed %d times, want 1 (second was skipped)", runs)
	}
}

func TestSagaFingerprintRearm(t *testing.T) {
	s := newSaga(t)
	var runs, comps int
	first := []Phase{recordingPhase("generate", &runs, &comps, "v1", false)}
	if _, err := s.Run(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	// Same name, different fingerprint → must re-run.
	second := []Phase{recordingPhase("generate", &runs, &comps, "v2", false)}
	recs, err := s.Run(context.Background(), second)
	if err != nil {
		t.Fatal(err)
	}
	if recs[0].Status != StatusOK || runs != 2 {
		t.Errorf("changed fingerprint should re-run: status=%q runs=%d", recs[0].Status, runs)
	}
}

func TestSagaAlwaysRun(t *testing.T) {
	s := newSaga(t)
	runs := 0
	p := Phase{Name: "secrets", AlwaysRun: true, Run: func(context.Context) (any, error) {
		runs++
		return nil, nil
	}}
	for range 3 {
		if _, err := s.Run(context.Background(), []Phase{p}); err != nil {
			t.Fatal(err)
		}
	}
	if runs != 3 {
		t.Errorf("AlwaysRun executed %d times, want 3", runs)
	}
}

func TestSagaFailureCompensatesInReverse(t *testing.T) {
	s := newSaga(t)
	var aRuns, aComp, bRuns, bComp, cRuns, cComp int
	var order []string
	mkComp := func(name string, comp *int) func(context.Context) error {
		return func(context.Context) error {
			*comp++
			order = append(order, name)
			return nil
		}
	}
	a := recordingPhase("network", &aRuns, &aComp, "a", false)
	a.Compensate = mkComp("network", &aComp)
	b := recordingPhase("shared", &bRuns, &bComp, "b", false)
	b.Compensate = mkComp("shared", &bComp)
	c := recordingPhase("compose-up", &cRuns, &cComp, "c", true) // fails

	recs, err := s.Run(context.Background(), []Phase{a, b, c})
	if err == nil {
		t.Fatal("expected the saga to fail")
	}
	if len(recs) != 3 || recs[2].Status != StatusFailed {
		t.Fatalf("records = %+v, want a,b ok + c failed", recs)
	}
	// Compensation runs the SUCCEEDED mutating phases in reverse: shared then network.
	if len(order) != 2 || order[0] != "shared" || order[1] != "network" {
		t.Fatalf("compensation order = %v, want [shared network]", order)
	}
	// The compensated phases' rows are cleared → a re-run redoes them.
	for _, name := range []string{"network", "shared"} {
		if ok, _ := s.DB.PhaseSatisfied("demo", "", name, Fingerprint(name[:1])); ok {
			t.Errorf("phase %q should not be satisfied after compensation", name)
		}
	}
}

func TestSagaResumesAfterFailure(t *testing.T) {
	s := newSaga(t)
	var aRuns, aComp, bRuns, bComp int
	// First run: A ok, B fails.
	a1 := recordingPhase("network", &aRuns, &aComp, "a", false)
	a1.Compensate = nil // keep A's row (network is never auto-removed, spec 09)
	a1.Mutating = false
	b1 := recordingPhase("shared", &bRuns, &bComp, "b", true)
	if _, err := s.Run(context.Background(), []Phase{a1, b1}); err == nil {
		t.Fatal("first run should fail at shared")
	}
	// Second run: A skips (satisfied, unchanged), B now succeeds.
	a2 := recordingPhase("network", &aRuns, &aComp, "a", false)
	a2.Compensate = nil
	a2.Mutating = false
	b2 := recordingPhase("shared", &bRuns, &bComp, "b", false)
	recs, err := s.Run(context.Background(), []Phase{a2, b2})
	if err != nil {
		t.Fatalf("resume run: %v", err)
	}
	if recs[0].Status != StatusSkipped {
		t.Errorf("network should skip on resume, got %q", recs[0].Status)
	}
	if recs[1].Status != StatusOK {
		t.Errorf("shared should run on resume, got %q", recs[1].Status)
	}
	if aRuns != 1 {
		t.Errorf("network ran %d times, want 1 (resumed = skipped)", aRuns)
	}
	if bRuns != 2 {
		t.Errorf("shared ran %d times, want 2 (failed then succeeded)", bRuns)
	}
}

func TestSagaPanicBecomesFailure(t *testing.T) {
	s := newSaga(t)
	comp := 0
	a := Phase{Name: "network", Mutating: true,
		Run:        func(context.Context) (any, error) { return nil, nil },
		Compensate: func(context.Context) error { comp++; return nil }}
	boom := Phase{Name: "shared", Run: func(context.Context) (any, error) { panic("kaboom") }}
	recs, err := s.Run(context.Background(), []Phase{a, boom})
	if err == nil {
		t.Fatal("a panicking phase must fail the saga")
	}
	if recs[1].Status != StatusFailed || recs[1].Error == nil || !strings.Contains(*recs[1].Error, "panic") {
		t.Errorf("panic record = %+v, want failed with panic error", recs[1])
	}
	if comp != 1 {
		t.Errorf("compensation should run after a panic; comp=%d", comp)
	}
}

func TestRecordJSONContract(t *testing.T) {
	ok := Record{Phase: "clone", Status: StatusOK, DurationMs: 1200}
	b, _ := json.Marshal(ok)
	if !strings.Contains(string(b), `"error":null`) {
		t.Errorf("ok record JSON = %s, want error:null", b)
	}
	msg := "service api exited (1)"
	fail := Record{Phase: "compose-up", Status: StatusFailed, DurationMs: 900, Error: &msg}
	b2, _ := json.Marshal(fail)
	if !strings.Contains(string(b2), `"status":"failed"`) || !strings.Contains(string(b2), msg) {
		t.Errorf("failed record JSON = %s", b2)
	}
}

func TestFormatPlain(t *testing.T) {
	msg := "boom"
	cases := []struct {
		rec  Record
		want string
	}{
		{Record{Phase: "network", Status: StatusOK, DurationMs: 12}, "[ok]      network (12ms)"},
		{Record{Phase: "generate", Status: StatusSkipped}, "[skipped] generate"},
		{Record{Phase: "compose-up", Scope: "api", Status: StatusFailed, DurationMs: 5, Error: &msg}, "[failed]  api/compose-up (5ms): boom"},
	}
	for _, c := range cases {
		if got := FormatPlain(c.rec); got != c.want {
			t.Errorf("FormatPlain = %q, want %q", got, c.want)
		}
	}
}

func TestFingerprintStable(t *testing.T) {
	ab := Fingerprint("a", "b")
	if ab != Fingerprint("a", "b") {
		t.Error("Fingerprint not stable for equal inputs")
	}
	// Order- and boundary-sensitive: ["a","b"] != ["ab"] != ["b","a"].
	if ab == Fingerprint("ab") {
		t.Error("Fingerprint collides across part boundaries")
	}
	if ab == Fingerprint("b", "a") {
		t.Error("Fingerprint should be order-sensitive")
	}
}

func TestAnyFailed(t *testing.T) {
	if AnyFailed([]Record{{Status: StatusOK}, {Status: StatusSkipped}}) {
		t.Error("no failed records → AnyFailed false")
	}
	if !AnyFailed([]Record{{Status: StatusOK}, {Status: StatusFailed}}) {
		t.Error("a failed record → AnyFailed true")
	}
}

func TestEmitStreamsRecords(t *testing.T) {
	s := newSaga(t)
	var emitted []string
	s.Emit = func(r Record) { emitted = append(emitted, r.Phase+":"+r.Status) }
	var runs, comps int
	_, err := s.Run(context.Background(), []Phase{recordingPhase("network", &runs, &comps, "a", false)})
	if err != nil {
		t.Fatal(err)
	}
	if len(emitted) != 1 || emitted[0] != "network:ok" {
		t.Errorf("emitted = %v, want [network:ok]", emitted)
	}
}
