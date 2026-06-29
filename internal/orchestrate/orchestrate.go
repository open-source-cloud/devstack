// Package orchestrate sequences the devstack subsystems into the resumable,
// crash-safe `up` saga (spec 09). It owns no domain logic — it drives ordered,
// named Phases under the concurrency spine (internal/lock + internal/state), and
// makes the multi-phase operation resumable (skip phases already satisfied for an
// unchanged input fingerprint), crash-safe (a phase interrupted mid-run re-runs
// because only `satisfied` skips), and observable (a Record per phase for the
// plain/--json contract + an event_log trail).
//
// This file is the engine (C5a): the Phase model and the Saga driver. The
// concrete daemon phases (clone/network/shared/provision/generate/compose-up/
// hooks) that wire the real modules are assembled on top (C5b).
package orchestrate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"time"

	"github.com/open-source-cloud/devstack/internal/lock"
	"github.com/open-source-cloud/devstack/internal/state"
)

// Phase statuses in the output contract (spec 09 §output-contract).
const (
	StatusOK      = "ok"
	StatusSkipped = "skipped"
	StatusFailed  = "failed"
)

// Phase is one named, idempotent, resumable step of the saga (spec 09 §phases).
type Phase struct {
	// Name is the saga_phase key + the Record label (preflight|clone|network|…).
	Name string
	// Scope is "" for a workspace-wide phase or a project name for a per-project one.
	Scope string
	// Mutating marks a phase that changed global state; only mutating phases with a
	// Compensate are unwound (in reverse) when a LATER phase fails.
	Mutating bool
	// AlwaysRun forces the phase to run every time, never skipped or fingerprinted
	// (the `secrets` phase: values are resolved in memory and never cached, spec 09).
	AlwaysRun bool
	// Fingerprint returns the SHA-256-able digest of the phase's resolved inputs; a
	// changed digest re-arms the phase. nil ⇒ the empty fingerprint (a config-free
	// phase like network-ensure, which is idempotent regardless).
	Fingerprint func(context.Context) (string, error)
	// Run executes the phase. It manages its own short, lock-held mutating critical
	// sections (network/port/ref/provision); long work (pulls, clones, health
	// polling) runs lock-free — the saga does not hold the flock across Run.
	Run func(context.Context) (detail any, err error)
	// Compensate undoes this phase's global mutation when a downstream phase fails.
	// nil ⇒ no compensation (non-mutating phases, or mutations intentionally kept,
	// e.g. the shared network and provisioned data — spec 09 §compensation).
	Compensate func(context.Context) error
}

// Record is one phase's machine-readable outcome (spec 09 §output-contract). It
// serializes to the documented `--json` element; Error is null on success.
type Record struct {
	Phase      string  `json:"phase"`
	Scope      string  `json:"scope,omitempty"`
	Status     string  `json:"status"` // ok | skipped | failed
	DurationMs int64   `json:"durationMs"`
	Error      *string `json:"error"`
	Detail     any     `json:"detail,omitempty"`

	fingerprint string // internal: carried to the satisfied row
}

// Saga drives an ordered phase list with resumability + compensation.
type Saga struct {
	Workspace string
	DB        *state.DB
	LockPath  string
	// Emit, if set, receives each Record as it completes (live streaming for the
	// CLI checklist); rendering must never block — keep it cheap.
	Emit func(Record)
	// clock is injectable for tests; defaults to time.Now.
	clock func() time.Time
}

func (s *Saga) now() time.Time {
	if s.clock != nil {
		return s.clock()
	}
	return time.Now()
}

// Run executes the phases in order. It returns the Record for every attempted
// phase and a non-nil error iff a phase failed (after compensation has unwound
// the mutating phases that had succeeded, in reverse order). Phases after a
// failure are not attempted.
func (s *Saga) Run(ctx context.Context, phases []Phase) ([]Record, error) {
	records := make([]Record, 0, len(phases))
	var done []Phase // succeeded mutating phases, in execution order (for unwind)

	for _, p := range phases {
		rec, err := s.runPhase(ctx, p)
		records = append(records, rec)
		s.emit(rec)

		if err != nil {
			s.compensate(ctx, &p, done)
			return records, fmt.Errorf("phase %q failed: %w", p.Name, err)
		}
		if rec.Status == StatusOK && p.Mutating && p.Compensate != nil {
			done = append(done, p)
		}
	}
	return records, nil
}

func (s *Saga) runPhase(ctx context.Context, p Phase) (Record, error) {
	start := s.now()
	rec := Record{Phase: p.Name, Scope: p.Scope}

	fp, err := s.fingerprint(ctx, p)
	if err != nil {
		return s.finishFail(rec, start, fmt.Errorf("fingerprint: %w", err)), err
	}
	rec.fingerprint = fp

	// Skip iff already satisfied for this exact fingerprint (spec 09 resumability).
	if !p.AlwaysRun {
		satisfied, err := s.DB.PhaseSatisfied(s.Workspace, p.Scope, p.Name, fp)
		if err != nil {
			return s.finishFail(rec, start, err), err
		}
		if satisfied {
			rec.Status = StatusSkipped
			rec.DurationMs = s.since(start)
			return rec, nil
		}
	}

	// Mark started (under the flock) — a `started`-but-not-`satisfied` row is what a
	// crashed run re-runs on the next invocation.
	if err := s.withLock(ctx, func() error {
		return s.DB.StartPhase(s.Workspace, p.Scope, p.Name, fp)
	}); err != nil {
		return s.finishFail(rec, start, err), err
	}
	s.DB.LogEvent("saga", s.qualified(p), "started")

	detail, runErr := s.runBody(ctx, p)
	rec.Detail = detail
	if runErr != nil {
		_ = s.withLock(ctx, func() error {
			return s.DB.FailPhase(s.Workspace, p.Scope, p.Name, runErr.Error())
		})
		s.DB.LogEvent("saga", s.qualified(p), "failed: "+runErr.Error())
		return s.finishFail(rec, start, runErr), runErr
	}

	if err := s.withLock(ctx, func() error {
		return s.DB.SatisfyPhase(s.Workspace, p.Scope, p.Name)
	}); err != nil {
		return s.finishFail(rec, start, err), err
	}
	rec.Status = StatusOK
	rec.DurationMs = s.since(start)
	s.DB.LogEvent("saga", s.qualified(p), fmt.Sprintf("satisfied in %dms", rec.DurationMs))
	return rec, nil
}

// runBody invokes a phase's Run, converting a panic into an error so one phase
// can never crash the whole CLI (the saga must always reach compensation).
func (s *Saga) runBody(ctx context.Context, p Phase) (detail any, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic in phase %q: %v", p.Name, r)
		}
	}()
	if p.Run == nil {
		return nil, nil
	}
	return p.Run(ctx)
}

// compensate unwinds mutating work after a failure. The FAILED phase's own
// compensation runs first (it may have partially applied — e.g. compose-up
// created some containers) but its row is KEPT as failed so `status` can surface
// it. Then the succeeded mutating phases unwind in reverse, each with its row
// cleared so a re-run redoes it. A compensation error is logged, not fatal —
// best-effort cleanup must not mask the original failure.
func (s *Saga) compensate(ctx context.Context, failed *Phase, done []Phase) {
	if failed != nil && failed.Mutating && failed.Compensate != nil {
		s.runCompensation(ctx, *failed)
	}
	for i := len(done) - 1; i >= 0; i-- {
		p := done[i]
		s.runCompensation(ctx, p)
		_ = s.withLock(ctx, func() error {
			return s.DB.ClearPhase(s.Workspace, p.Scope, p.Name)
		})
	}
}

func (s *Saga) runCompensation(ctx context.Context, p Phase) {
	if p.Compensate == nil {
		return
	}
	if err := p.Compensate(ctx); err != nil {
		s.DB.LogEvent("saga", s.qualified(p), "compensation failed: "+err.Error())
	} else {
		s.DB.LogEvent("saga", s.qualified(p), "compensated")
	}
}

func (s *Saga) fingerprint(ctx context.Context, p Phase) (string, error) {
	if p.AlwaysRun || p.Fingerprint == nil {
		return "", nil
	}
	return p.Fingerprint(ctx)
}

func (s *Saga) withLock(ctx context.Context, fn func() error) error {
	if s.LockPath == "" {
		return fn() // tests without a lock path run unlocked
	}
	return lock.WithLock(ctx, s.LockPath, fn)
}

func (s *Saga) finishFail(rec Record, start time.Time, err error) Record {
	rec.Status = StatusFailed
	rec.DurationMs = s.since(start)
	msg := err.Error()
	rec.Error = &msg
	return rec
}

func (s *Saga) since(start time.Time) int64 { return s.now().Sub(start).Milliseconds() }

func (s *Saga) emit(rec Record) {
	if s.Emit != nil {
		s.Emit(rec)
	}
}

func (s *Saga) qualified(p Phase) string {
	if p.Scope == "" {
		return p.Name
	}
	return p.Scope + "/" + p.Name
}

// Fingerprint hashes its parts into a stable hex digest for a phase's
// Fingerprint func (config bytes, params, resolved versions). Order-sensitive.
func Fingerprint(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		_, _ = io.WriteString(h, p)
		_, _ = h.Write([]byte{0}) // length-independent separator
	}
	return hex.EncodeToString(h.Sum(nil))
}

// FormatPlain renders one Record as a single non-TTY line (spec 09 plain mode):
//
//	[ok]      network (12ms)
//	[skipped] generate
//	[failed]  compose-up: service api exited (1)
func FormatPlain(r Record) string {
	label := r.Phase
	if r.Scope != "" {
		label = r.Scope + "/" + r.Phase
	}
	switch r.Status {
	case StatusSkipped:
		return fmt.Sprintf("[skipped] %s", label)
	case StatusFailed:
		msg := ""
		if r.Error != nil {
			msg = ": " + *r.Error
		}
		return fmt.Sprintf("[failed]  %s (%dms)%s", label, r.DurationMs, msg)
	default:
		return fmt.Sprintf("[ok]      %s (%dms)", label, r.DurationMs)
	}
}

// AnyFailed reports whether any record failed (the saga's process exit code is
// non-zero iff this is true, spec 09).
func AnyFailed(records []Record) bool {
	for _, r := range records {
		if r.Status == StatusFailed {
			return true
		}
	}
	return false
}
