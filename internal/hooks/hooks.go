// Package hooks runs the declarative lifecycle hooks (spec 11) at saga phase
// boundaries: user commands on the host (run: host) or inside a running service
// (run: exec via `compose exec -T`), with per-hook timeout, retries, and
// onFailure semantics. firstRun/postPull hooks are made idempotent by the
// `hook_run` ledger — recorded ONLY on success and ONLY inside the flock, so a
// failed or interrupted hook re-runs next time (the correct replacement for
// Postgres initdb.d, DECISIONS D8).
//
// The hook BODY runs OUTSIDE the global flock (a 10-minute `npm install` must not
// serialize every other invocation, spec 08 #1 rule); only the ledger record is
// taken under the lock. This is the thin runner — ${ref}/${env}/${self}/secret://
// interpolation happens upstream in the saga (the resolved values arrive via
// PhaseOpts.ExtraEnv); the full workspace-scope ordering is X3.
package hooks

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"time"

	"github.com/open-source-cloud/devstack/internal/config"
)

// Defaults (spec 11).
const (
	DefaultTimeout = 120 * time.Second
	defaultBackoff = 2 * time.Second
)

// onFailure modes (spec 11).
const (
	OnAbort    = "abort"
	OnWarn     = "warn"
	OnContinue = "continue"
)

// Result statuses for a single hook.
const (
	StatusRan     = "ran"     // executed successfully
	StatusSkipped = "skipped" // ledger already satisfied (idempotent hook)
	StatusWarned  = "warned"  // failed but onFailure was warn/continue
	StatusFailed  = "failed"  // failed with onFailure=abort
)

// Execer runs the two hook transports, returning combined stdout+stderr. It is
// injectable so the runner is testable without a daemon. Implementations MUST
// honor ctx cancellation (the runner enforces each hook's timeout via ctx).
type Execer interface {
	// Host runs argv on the host. workdir is hook-relative (joined under the
	// documented base dir); env is KEY=VALUE pairs appended to the process env.
	Host(ctx context.Context, workdir string, env, argv []string) (string, error)
	// Exec runs argv inside a RUNNING service via `compose exec -T`. workdir is the
	// in-container -w path; env becomes repeated -e NAME=VALUE flags.
	Exec(ctx context.Context, service, workdir string, env, argv []string) (string, error)
}

// OSExecer is the production Execer: os/exec for host hooks, the `docker compose
// exec` CLI for service hooks (DECISIONS D5 — devstack owns compose CLI
// construction). -T disables TTY for deterministic non-interactive runs; combined
// output is captured for the saga checklist and the failure remediation.
type OSExecer struct {
	BaseDir string // documented working dir for run:host (repo root / workspace root)
	Project string // compose -p
	File    string // compose -f
}

// Host runs argv on the host from the hook's working directory.
func (e OSExecer) Host(ctx context.Context, workdir string, env, argv []string) (string, error) {
	if len(argv) == 0 {
		return "", fmt.Errorf("empty command")
	}
	dir := e.BaseDir
	if workdir != "" {
		if filepath.IsAbs(workdir) {
			dir = workdir
		} else {
			dir = filepath.Join(e.BaseDir, workdir)
		}
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// Exec shells argv into a running service. compose exec -e accepts NAME=VALUE
// (unlike `up`), so hook secrets are passed inline without ever touching a file.
func (e OSExecer) Exec(ctx context.Context, service, workdir string, env, argv []string) (string, error) {
	if len(argv) == 0 {
		return "", fmt.Errorf("empty command")
	}
	args := []string{"compose", "-p", e.Project, "-f", e.File, "exec", "-T"}
	if workdir != "" {
		args = append(args, "-w", workdir)
	}
	for _, kv := range env {
		args = append(args, "-e", kv)
	}
	args = append(args, service)
	args = append(args, argv...)
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Dir = e.BaseDir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// Ledger is the idempotency store the runner needs (satisfied by *state.DB). Kept
// as an interface so internal/hooks does not import internal/state.
type Ledger interface {
	HookSatisfied(project, hook, scopeKey string) (bool, error)
	RecordHookRun(project, hook, scopeKey string) error
}

// Locker runs fn while holding the machine-global flock (wraps lock.WithLock).
type Locker func(ctx context.Context, fn func() error) error

// Runner executes lifecycle hooks. Execer is required; Ledger+Lock are needed
// only for idempotent phases; Backoff/Logf are optional.
type Runner struct {
	Execer  Execer
	Ledger  Ledger
	Lock    Locker
	Backoff time.Duration                 // between retry attempts (0 → default 2s)
	Logf    func(format string, a ...any) // optional progress/warn sink
}

// Result is the outcome of one hook (for the saga checklist / --json).
type Result struct {
	Hook     string
	Status   string
	Attempts int
	Output   string
	Err      error
}

// Run executes one hook with its timeout and retries, returning combined output,
// the attempt count, and the failure error (after retries) if any. It applies NO
// onFailure or idempotency semantics — RunPhase layers those on. extraEnv carries
// resolved ${ref}/secret values (saga-supplied), appended after the hook's own
// env so secrets land last.
func (r *Runner) Run(ctx context.Context, h config.Hook, extraEnv []string) (string, int, error) {
	if err := validate(h); err != nil {
		return "", 0, err
	}
	timeout := DefaultTimeout
	if d, err := time.ParseDuration(h.Timeout); err == nil && h.Timeout != "" {
		timeout = d
	}
	env := buildEnv(h.Env, extraEnv)

	var (
		out      string
		err      error
		attempts int
	)
	for attempt := 0; attempt <= max(h.Retries, 0); attempt++ {
		if attempt > 0 {
			if e := sleep(ctx, r.backoff()); e != nil {
				return out, attempts, e
			}
		}
		attempts++
		actx, cancel := context.WithTimeout(ctx, timeout)
		if h.Run == "exec" {
			out, err = r.Execer.Exec(actx, h.Service, h.Workdir, env, h.Command)
		} else {
			out, err = r.Execer.Host(actx, h.Workdir, env, h.Command)
		}
		cancel()
		if err == nil {
			return out, attempts, nil
		}
	}
	return out, attempts, fmt.Errorf("hook %q failed after %d attempt(s): %w", h.Name, attempts, err)
}

// PhaseOpts configures a phase run.
type PhaseOpts struct {
	Project          string                   // ledger 'project'
	Phase            string                   // ledger 'hook' column, e.g. "firstRun"
	Idempotent       bool                     // guard every hook via the ledger (firstRun/postPull)
	ScopeKey         func(config.Hook) string // scope_key for idempotent / `once` hooks
	DefaultOnFailure string                   // applied when a hook omits onFailure
	ExtraEnv         []string                 // resolved env/secret values for every hook
}

// RunPhase runs the phase's hooks in order, applying idempotency and onFailure:
//   - abort   : stop immediately, return the error (phase fails).
//   - warn    : log and continue; the phase still succeeds.
//   - continue: run remaining hooks, but the phase ultimately fails.
//
// A successful run of an idempotent (or `once`) hook is recorded in the ledger
// INSIDE the flock; nothing is recorded on failure (spec 11).
func (r *Runner) RunPhase(ctx context.Context, hookList []config.Hook, o PhaseOpts) ([]Result, error) {
	var (
		results     []Result
		deferredErr error // from onFailure: continue
	)
	for _, h := range hookList {
		guarded := o.Idempotent || h.Once
		var scope string
		if guarded {
			if o.ScopeKey == nil {
				return results, fmt.Errorf("hook %q: idempotent phase %q has no scope_key function", h.Name, o.Phase)
			}
			scope = o.ScopeKey(h)
			if r.Ledger != nil {
				ok, err := r.Ledger.HookSatisfied(o.Project, o.Phase, scope)
				if err != nil {
					return results, err
				}
				if ok {
					results = append(results, Result{Hook: h.Name, Status: StatusSkipped})
					continue
				}
			}
		}

		out, attempts, err := r.Run(ctx, h, o.ExtraEnv)
		res := Result{Hook: h.Name, Attempts: attempts, Output: out, Err: err}
		if err == nil {
			res.Status = StatusRan
			results = append(results, res)
			if guarded {
				if e := r.record(ctx, o.Project, o.Phase, scope); e != nil {
					return results, e
				}
			}
			continue
		}

		switch onFailure(h, o.DefaultOnFailure) {
		case OnWarn:
			res.Status = StatusWarned
			r.warn("hook %q failed (onFailure=warn): %v", h.Name, err)
			results = append(results, res)
		case OnContinue:
			res.Status = StatusWarned
			deferredErr = err
			r.warn("hook %q failed (onFailure=continue): %v", h.Name, err)
			results = append(results, res)
		default: // abort
			res.Status = StatusFailed
			results = append(results, res)
			return results, err
		}
	}
	return results, deferredErr
}

func (r *Runner) record(ctx context.Context, project, phase, scope string) error {
	if r.Ledger == nil {
		return nil
	}
	rec := func() error { return r.Ledger.RecordHookRun(project, phase, scope) }
	if r.Lock != nil {
		return r.Lock(ctx, rec)
	}
	return rec()
}

func (r *Runner) backoff() time.Duration {
	if r.Backoff > 0 {
		return r.Backoff
	}
	return defaultBackoff
}

func (r *Runner) warn(format string, a ...any) {
	if r.Logf != nil {
		r.Logf(format, a...)
	}
}

// validate enforces the transport-specific rule config can't (spec 11): run:exec
// needs a target service. (Structural shape — name/run/command — is already
// validated at config load, C3a.)
func validate(h config.Hook) error {
	if len(h.Command) == 0 {
		return fmt.Errorf("hook %q: command is empty", h.Name)
	}
	if h.Run == "exec" && h.Service == "" {
		return fmt.Errorf("hook %q: run: exec requires a `service`", h.Name)
	}
	return nil
}

func onFailure(h config.Hook, def string) string {
	if h.OnFailure != "" {
		return h.OnFailure
	}
	if def != "" {
		return def
	}
	return OnAbort
}

// buildEnv renders the hook's env map (sorted for determinism) followed by the
// saga-supplied extra env (secrets last, spec 11).
func buildEnv(m map[string]string, extra []string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	env := make([]string, 0, len(m)+len(extra))
	for _, k := range keys {
		env = append(env, k+"="+m[k])
	}
	return append(env, extra...)
}

// sleep is the cancelable inter-retry wait, indirected for tests.
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
