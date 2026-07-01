// Package telemetry is the opt-in, privacy-first usage-telemetry scaffold
// (spec 20). It is DEFAULT OFF and, in this build, ships EMPTY: no real network
// endpoint is wired. The only sink that is actually used is the no-op sink, which
// records nothing and makes no network calls. A pluggable HTTP sink exists for a
// self-hoster to attach later, but it is unused and disabled by default.
//
// Design pillars:
//
//   - Strictly opt-in. Nothing is collected, queued, or sent until the user has
//     affirmatively enabled telemetry (persisted consent, default false). The
//     Recorder short-circuits BEFORE constructing or sending an event when
//     disabled — proven by test.
//   - Allowlist, never denylist. The Event struct has ONLY the coarse,
//     non-identifying fields below. There is no free-form string map and no
//     reflection "dump everything" path, so a future field cannot accidentally
//     carry PII. Adding a field is a deliberate, reviewed change.
//   - Never capture paths, repo/project names, secrets, env names/values,
//     ${ref} keys, git remotes, hostnames, usernames, IPs, or raw error strings.
//     Errors are mapped to a small closed enum (CategorizeError) — never the raw
//     err.Error() text.
//   - Best-effort. A sink failure never affects a command's exit code, output,
//     or latency (callers run Record in a detached goroutine with a budget).
//
// What WOULD be sent, exhaustively, is the Event struct — see its fields. Run
// `devstack telemetry show` to print a real sample byte-for-byte.
package telemetry

import (
	"context"
	"runtime"
	"time"

	"github.com/open-source-cloud/devstack/internal/xdg"
)

// DefaultEndpoint is intentionally EMPTY (ship-empty, spec 20 Q-TELEMETRY): no
// project-operated collector exists yet, so telemetry can be enabled but goes
// nowhere until a self-hoster sets an endpoint. When this is empty the CLI wires
// the no-op sink regardless of consent.
//
// TODO(spec 20): once a project-operated OTLP collector exists (with a published
// data-retention + source-IP-dropping policy), set this to that collector's
// OTLP/HTTP URL and switch DefaultSink to return the HTTP sink when an endpoint is
// configured. Until then this MUST stay empty so no traffic can leave a machine.
const DefaultEndpoint = ""

// Outcome classes (closed set).
const (
	OutcomeOK    = "ok"
	OutcomeError = "error"
)

// Error categories — a closed enum mapped from wrapped-error signatures
// (ARCHITECTURE §7.6). The raw err.Error() is NEVER transmitted; only one of
// these stable strings. An unmatched error becomes CategoryOther (no text).
const (
	CategoryDockerDaemonUnreachable = "docker_daemon_unreachable"
	CategoryComposeTooOld           = "compose_too_old"
	CategoryPortInUse               = "port_in_use"
	CategoryGitAuthPrompt           = "git_auth_prompt"
	CategoryStateLocked             = "state_locked"
	CategoryOther                   = "error_other"
)

// Event is the EXHAUSTIVE allowlist of what telemetry would send. Every field is
// a coarse, non-identifying counter. There is deliberately no metadata map and no
// free-form string field, so PII is structurally impossible to attach.
type Event struct {
	// Command is the cobra command path only (e.g. "up", "shared gc") — never
	// argument or flag VALUES.
	Command string `json:"command"`
	// Flags are the NAMES of flags that were set (e.g. ["--build"]) — never their
	// values.
	Flags []string `json:"flags,omitempty"`
	// Outcome is OutcomeOK or OutcomeError.
	Outcome string `json:"outcome"`
	// ErrorCategory is one of the Category* enum values, set only on error. Never
	// a raw error string.
	ErrorCategory string `json:"error_category,omitempty"`
	// DurationMS is the wall time bucketed to the nearest 100ms to blunt
	// fingerprinting.
	DurationMS int64 `json:"duration_ms"`
	// OS/Arch are runtime.GOOS / runtime.GOARCH.
	OS   string `json:"os"`
	Arch string `json:"arch"`
	// IsWSL2 comes from the shared WSL detector — the single highest-value
	// dimension.
	IsWSL2 bool `json:"is_wsl2"`
	// ToolVersion is the ldflags-stamped devstack version.
	ToolVersion string `json:"tool_version"`
	// InstallID is a random UUIDv4 generated once, not derived from anything
	// machine-identifying. It correlates events from one install for funnel
	// analysis, nothing more.
	InstallID string `json:"install_id"`
}

// EventInput carries the ONLY caller-supplied, pre-vetted fields NewEvent will
// accept. Everything else (os/arch/wsl2/duration bucketing) is filled in here so
// a caller cannot smuggle an un-allowlisted value into the payload.
type EventInput struct {
	Command     string
	Flags       []string
	Err         error         // categorized to the enum; the raw text is discarded
	Duration    time.Duration // bucketed to 100ms
	ToolVersion string
	InstallID   string
}

// NewEvent builds a redacted Event from vetted inputs. It is the ONLY constructor:
// it discards the raw error (keeping only the category enum), buckets the
// duration, and fills os/arch/wsl2 from the runtime — so no path, hostname, or
// error string can reach the wire.
func NewEvent(in EventInput) Event {
	outcome := OutcomeOK
	var cat string
	if in.Err != nil {
		outcome = OutcomeError
		cat = CategorizeError(in.Err)
	}
	return Event{
		Command:       in.Command,
		Flags:         in.Flags,
		Outcome:       outcome,
		ErrorCategory: cat,
		DurationMS:    bucketMS(in.Duration),
		OS:            runtime.GOOS,
		Arch:          runtime.GOARCH,
		IsWSL2:        xdg.IsWSL2(),
		ToolVersion:   in.ToolVersion,
		InstallID:     in.InstallID,
	}
}

// bucketMS rounds a duration to the nearest 100ms (never negative).
func bucketMS(d time.Duration) int64 {
	if d <= 0 {
		return 0
	}
	ms := d.Milliseconds()
	return ((ms + 50) / 100) * 100
}

// Sink is the transport contract. Send delivers one event; it MUST be best-effort
// from the caller's perspective (any error is swallowed by the Recorder). No sink
// in this build performs real network I/O by default.
type Sink interface {
	Send(ctx context.Context, ev Event) error
}

// NoopSink is the ONLY sink wired in this ship-empty build: it records nothing and
// makes no network call. It exists so the whole pipeline is exercisable (and
// testable) without ever emitting a byte off-machine.
type NoopSink struct{}

// Send discards the event.
func (NoopSink) Send(context.Context, Event) error { return nil }

// DefaultSink returns the sink the CLI uses. Ship-empty: always NoopSink until a
// real endpoint is configured and wired (see DefaultEndpoint TODO).
func DefaultSink() Sink { return NoopSink{} }

// Recorder decides whether an event is sent. It is the consent gate: when Enabled
// is false (the default) Record short-circuits and NEVER touches the sink — a
// different branch from "sampled out". Both are covered by tests.
type Recorder struct {
	// Enabled reflects persisted consent AND all auto-off conditions resolved by
	// the caller. If false, nothing is sent.
	Enabled bool
	// Sink is the transport; defaults to NoopSink when nil.
	Sink Sink
}

// Record sends ev best-effort. It returns whether the sink was invoked (useful for
// tests and --debug). A nil/absent consent means no send. Any sink error is
// swallowed: telemetry must never change a command's outcome.
func (r Recorder) Record(ctx context.Context, ev Event) (sent bool) {
	if !r.Enabled {
		return false
	}
	sink := r.Sink
	if sink == nil {
		sink = NoopSink{}
	}
	_ = sink.Send(ctx, ev)
	return true
}
