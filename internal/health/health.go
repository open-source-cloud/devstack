// Package health compiles a service's declarative healthcheck (spec 10) into
// timing parameters and polls the read-only Engine SDK (.State.Health.Status)
// until a target is ready or fails fast. It is strictly read-only and therefore
// lock-free (ARCHITECTURE §4, spec 10 §gotchas): the up saga takes the flock
// only for the start/provision steps a green poll unblocks.
//
// This is the thin v1 — a single-target Poll plus Compile, consumed linearly by
// the caller. The full workspace DAG (cycle paths, topo waves, profile-aware
// pruning, generate-time "condition:healthy needs a healthcheck") is X2.
package health

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/open-source-cloud/devstack/internal/config"
	"github.com/open-source-cloud/devstack/internal/docker"
)

// Defaults per spec 10 (Compose-equivalent). StartPeriod stays 30s here: the
// "60s for stateful images" guidance lives in the engine TEMPLATES, not in this
// package, which carries no hardcoded engine knowledge — it only compiles/polls.
const (
	DefaultInterval    = 5 * time.Second
	DefaultTimeout     = 3 * time.Second
	DefaultRetries     = 10
	DefaultStartPeriod = 30 * time.Second
	// DefaultLogTail is how many trailing log lines the fail-fast diagnostic inlines.
	DefaultLogTail = 20
)

// Condition is the readiness bar for a target (spec 10): Healthy waits for a
// passing healthcheck; Started waits only for the container to be running (the
// only honest gate when no healthcheck is declared).
type Condition string

const (
	Healthy Condition = "healthy"
	Started Condition = "started"
)

// Timing is the compiled, defaulted timing for a probe.
type Timing struct {
	Interval    time.Duration
	Timeout     time.Duration
	Retries     int
	StartPeriod time.Duration
}

// Budget is the overall wall-clock a poll may take before timing out:
// startPeriod + interval*retries (spec 10 §blocking-UX). The caller caps it
// further with --health-timeout via the context deadline.
func (t Timing) Budget() time.Duration {
	return t.StartPeriod + t.Interval*time.Duration(max(t.Retries, 0))
}

// Compile applies spec-10 defaults to a (possibly nil or partial) healthcheck.
// Duration strings are already validated at config-load (the `duration`
// validator), so a parse error here falls back to the default rather than erroring.
func Compile(hc *config.Healthcheck) Timing {
	t := Timing{
		Interval:    DefaultInterval,
		Timeout:     DefaultTimeout,
		Retries:     DefaultRetries,
		StartPeriod: DefaultStartPeriod,
	}
	if hc == nil {
		return t
	}
	if d, ok := parseDur(hc.Interval); ok {
		t.Interval = d
	}
	if d, ok := parseDur(hc.Timeout); ok {
		t.Timeout = d
	}
	if d, ok := parseDur(hc.StartPeriod); ok {
		t.StartPeriod = d
	}
	if hc.Retries > 0 {
		t.Retries = hc.Retries
	}
	return t
}

func parseDur(s string) (time.Duration, bool) {
	if s == "" {
		return 0, false
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 0, false
	}
	return d, true
}

// Target names what to poll and labels the resulting record/diagnostic.
type Target struct {
	ContainerID string
	Service     string    // service name (for the record + diagnostic)
	Project     string    // compose project (for the record)
	Kind        string    // healthcheck kind, e.g. "pg_isready" (for the record)
	Condition   Condition // Healthy (default) | Started
}

// Record is the machine-readable health result (spec 10 §--json). LastError is
// null on success.
type Record struct {
	Service   string  `json:"service"`
	Project   string  `json:"project"`
	Kind      string  `json:"kind"`
	Status    string  `json:"status"` // healthy | started | unhealthy | exited | timeout
	Attempts  int     `json:"attempts"`
	ElapsedMs int64   `json:"elapsedMs"`
	LastError *string `json:"lastError"`
}

// Result statuses.
const (
	statusHealthy   = "healthy"
	statusStarted   = "started"
	statusUnhealthy = "unhealthy"
	statusExited    = "exited"
	statusTimeout   = "timeout"
)

// ProbeError is the fail-fast diagnostic returned when a target goes unhealthy,
// exits, or times out: it carries the final Record and the last N log lines, and
// renders a one-line remediation (ARCHITECTURE §7.6).
type ProbeError struct {
	Record Record
	Logs   string
}

func (e *ProbeError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "service %q (project %s) is %s after %d attempt(s)",
		e.Record.Service, e.Record.Project, e.Record.Status, e.Record.Attempts)
	if e.Record.Kind != "" {
		fmt.Fprintf(&b, " [healthcheck: %s]", e.Record.Kind)
	}
	if e.Logs != "" {
		fmt.Fprintf(&b, "\nlast %d log lines:\n%s", DefaultLogTail, e.Logs)
	}
	b.WriteString("\nhint: inspect the service's logs and healthcheck; a slow stateful image may need a larger startPeriod (spec 10)")
	return b.String()
}

// sleep is the cancelable inter-poll wait, indirected for tests.
var sleep = func(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// now is indirected for tests; in production it is time.Now.
var now = time.Now

// Poll blocks until tgt reaches its Condition or fails fast (unhealthy / exited /
// timeout / context cancellation). On success it returns a Record with a nil
// error; on failure it returns the Record AND a *ProbeError carrying the last
// log lines. It never mutates state — safe to call outside the flock.
func Poll(ctx context.Context, cli docker.Client, tgt Target, tm Timing) (Record, error) {
	if tgt.Condition == "" {
		tgt.Condition = Healthy
	}
	start := now()
	deadline := start.Add(tm.Budget())
	if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
		deadline = dl
	}
	rec := Record{Service: tgt.Service, Project: tgt.Project, Kind: tgt.Kind, Status: statusTimeout}

	for {
		rec.Attempts++
		d, err := cli.ContainerInspect(ctx, tgt.ContainerID)
		switch {
		case err != nil:
			// The container may not exist yet (compose still creating it); keep
			// waiting until the deadline, recording the last transient error.
			setLastErr(&rec, err)
		case fatalState(d.State):
			return fail(ctx, cli, tgt, rec, start, statusExited)
		case tgt.Condition == Started:
			if d.Running {
				return finish(rec, start, statusStarted), nil
			}
		default: // Healthy
			rec.LastError = nil
			switch d.Health {
			case docker.HealthHealthy:
				return finish(rec, start, statusHealthy), nil
			case docker.HealthUnhealthy:
				return fail(ctx, cli, tgt, rec, start, statusUnhealthy)
			}
		}

		if !now().Before(deadline) {
			return fail(ctx, cli, tgt, rec, start, statusTimeout)
		}
		if err := sleep(ctx, tm.Interval); err != nil {
			return fail(ctx, cli, tgt, rec, start, statusTimeout)
		}
	}
}

// fatalState reports container states from which readiness can never be reached
// (spec 10: a container that exits/restarts before reporting health fails fast).
func fatalState(state string) bool {
	switch state {
	case "exited", "dead", "restarting", "removing":
		return true
	default:
		return false
	}
}

func setLastErr(rec *Record, err error) {
	msg := err.Error()
	rec.LastError = &msg
}

func finish(rec Record, start time.Time, status string) Record {
	rec.Status = status
	rec.ElapsedMs = now().Sub(start).Milliseconds()
	rec.LastError = nil
	return rec
}

// fail finalizes a failing record and attaches the container's last log lines.
// Logs are fetched on a detached context so a deadline-exceeded poll still
// produces a diagnostic.
func fail(ctx context.Context, cli docker.Client, tgt Target, rec Record, start time.Time, status string) (Record, error) {
	rec.Status = status
	rec.ElapsedMs = now().Sub(start).Milliseconds()
	lctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	logs, _ := cli.ContainerLogs(lctx, tgt.ContainerID, DefaultLogTail)
	return rec, &ProbeError{Record: rec, Logs: strings.TrimSpace(logs)}
}
