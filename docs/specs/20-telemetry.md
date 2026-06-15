# Spec 20 — Opt-in anonymous telemetry

**Module:** `internal/telemetry` · **Milestone:** later · **Effort:** ~1.5w (feature #12)

## Purpose
Let a small OSS team learn how `devstack` behaves in the wild — which commands run, where they fail, on which OS/arch and Docker/compose versions — **without ever betraying user trust**. The whole point is to answer questions like "WSL2 trust install fails 30% of the time" that issue trackers can't, while emitting *nothing* that could identify a user, a repo, or a secret. Telemetry is **strictly opt-in, default OFF**, fully documented, and best-effort: a telemetry failure must never affect a command's exit code, output, or latency.

> **Builds on:** the [spec 14](14-self-update-and-migration.md) update-notifier substrate — the only other thing in `devstack` that talks to the network on a normal command. It reuses that spec's TTY/CI/env suppression discipline, the cache-not-ledger storage split, and the hard time-budget pattern. **Deferred to "later"** because it touches no pillar, carries real reputational risk if rushed, and is only worth building once the v1 command surface ([spec 07](07-cli-and-aliasing.md)) is stable enough that event names mean something.

## Consent model — strictly opt-in, default OFF
- **Default is OFF.** No event is collected, queued, or sent until the user has *affirmatively* enabled telemetry. There is no silent-on, no "anonymous by default", no soft-opt-out.
- **First-run prompt:** on the **first interactive (TTY) invocation of a mutating command**, devstack prints exactly what would be collected (a one-screen summary, plus `devstack telemetry show` to see a real sample) and asks a single yes/no. The default answer is **No**. A declined or skipped prompt records consent `false` and is never asked again that major version.
- **Consent lives in config, not the ledger.** Persist `telemetry.enabled: true|false` (+ `telemetry.consentAt`, `telemetry.consentVersion`) under `XDG_CONFIG_HOME/devstack/telemetry.yaml`, **not** in `workspace.yaml` (consent is per-user/per-machine, not per-repo, and must never be committed) and **not** in `state.db` (it's user policy, not Docker-context ledger state). A missing file means "never decided" → treat as OFF.
- **Revocable at any time, single command:** `devstack telemetry disable` flips it OFF and is honored on the very next invocation; `devstack telemetry enable` opts back in; `devstack telemetry status` prints the current state, endpoint, and the stable install id.
- **One-flag / one-env disable:** `--no-telemetry` (global flag) and `DEVSTACK_NO_TELEMETRY=1` force OFF for that run regardless of config; the env opt-out, like the [spec 14](14-self-update-and-migration.md) notifier env opt-out, **skips collection and the network call entirely** (nothing queued, nothing sent). There is intentionally **no** `--telemetry` flag that turns it on for one run — enabling is always an explicit, persisted consent action.

## Auto-off conditions (never prompt, never send)
Telemetry is forced OFF — and the first-run prompt is never shown — when **any** of these hold, even if consent is `true`:

| Condition | Detect via | Rationale |
|---|---|---|
| Non-interactive stdout | `golang.org/x/term.IsTerminal` false | can't obtain real consent; scripts shouldn't phone home |
| CI environment | `CI` env set (+ common `GITHUB_ACTIONS`/`GITLAB_CI`/`BUILDKITE`…) | CI runs would swamp and skew data, and aren't a human |
| `DO_NOT_TRACK` set to a truthy value | env `DO_NOT_TRACK=1` | honor the cross-tool [consoledonottrack.com](https://consoledonottrack.com) convention |
| `--json` / `--quiet` machine modes | global flags ([spec 07](07-cli-and-aliasing.md)) | machine-driven, non-interactive by contract |
| `--no-telemetry` / `DEVSTACK_NO_TELEMETRY` | flag/env | explicit per-run kill switch |

The prompt is **also** suppressed when stdin isn't a TTY (no way to read an answer). CI auto-off means the env opt-out is belt-and-suspenders, not the primary guard.

## Payload — allowlist, never denylist
The collected fields are an **exhaustive allowlist**; anything not on this list is structurally impossible to send (the event type has no field for it). This is the inverse of scrubbing-after-the-fact, which always leaks.

| Field | Example | Notes |
|---|---|---|
| `command` | `up`, `down`, `doctor`, `shared gc` | the cobra command path only — **never args/flags values** |
| `subcommand_flags` | `["--build","--profile"]` | flag **names** that were set, never their **values** |
| `outcome` | `ok` / `error` | |
| `error_category` | `docker_daemon_unreachable`, `compose_too_old`, `port_in_use`, `git_auth_prompt`, `state_locked` | a **closed enum** mapped from the wrapped-error signatures ([ARCHITECTURE §7.6](../ARCHITECTURE.md)) — never the raw error string |
| `duration_ms` | `1840` | bucketed (e.g. nearest 100ms) to blunt fingerprinting |
| `os` / `arch` | `linux`/`arm64`, `darwin`/`amd64` | `runtime.GOOS`/`GOARCH` |
| `is_wsl2` | `true` | from the existing [spec 08](08-state-locking-and-lifecycle.md) WSL detection — the single most useful dimension |
| `tool_version` | `1.3.0` | ldflags `version` ([spec 14](14-self-update-and-migration.md)) |
| `docker_version` / `compose_version` | `27.5.1` / `2.31.0` | already probed by `doctor`/preflight ([spec 03](03-workspaces-and-shared-services.md)) |
| `install_id` | random UUIDv4 | generated once, stored beside consent; **rotatable** via `telemetry reset`; not derived from anything machine-identifying |

**Never collected, by construction:** repo names, project names, paths (workspace/CWD/binary), file contents, template names or contents, secret names **or** values, env-var names or values, `${ref}` keys, git remotes/branches, hostnames, usernames, IP addresses (the endpoint may log the source IP — see threat model), MAC addresses, or any free-form string. There is no `metadata: map[string]string` escape hatch.

## `telemetry show` — radical transparency
`devstack telemetry show` runs the **real** collection path for a synthetic invocation (or against the last command if `--last`) and prints the **exact** event(s) that would be sent, as pretty JSON, plus the destination endpoint and whether sending is currently enabled. This is the trust primitive: a skeptical user can verify, byte-for-byte, that nothing sensitive leaves the machine. It performs **no** network I/O. `--format json` emits the raw OTLP body for the truly paranoid.

## Transport — self-hostable OTLP, best-effort, non-blocking
- **Pure-Go OTLP/HTTP exporter** (`go.opentelemetry.io/otel` + `otlptracehttp`/`otlploghttp` over protobuf-or-JSON) so the static `CGO_ENABLED=0` binary is preserved. Events are modeled as OTel **log records** (or span events) on a `devstack.cli.command` scope — structured, queryable, and standard. **No gRPC exporter** (heavier transitive deps); HTTP keeps the binary lean.
- **Self-hostable endpoint.** The default endpoint is a project-run OTLP collector; a self-hosting OSS team overrides it with `telemetry.endpoint` (config) or `OTEL_EXPORTER_OTLP_ENDPOINT` (honor the OTel standard env) and points it at their own collector/Grafana/Honeycomb-compatible sink. No vendor lock-in; the wire format is the open standard.
- **Best-effort + hard budget.** Fire the export in a goroutine with a **short budget (≤500ms)** and a small in-process queue; if the command finishes first, the export is abandoned (no flush-on-exit blocking). Network error, DNS failure, timeout, non-2xx → **swallowed**, logged only under `--debug`. A telemetry failure never changes the command's exit code.
- **Sampling.** Default head sampling at a configurable rate (`telemetry.sampleRate`, default e.g. `0.25`) so a busy user's `up`/`status` loop doesn't flood the collector; **`error` outcomes are always sent (sample rate 1.0)** because failures are the high-value signal. Sampling is deterministic per `(install_id, day)` so a user is consistently in-or-out within a window (cleaner aggregates, less fingerprinting than per-event coin flips).
- **No daemon, no on-disk spool.** Consistent with the stateless-CLI model ([ARCHITECTURE §1](../ARCHITECTURE.md)): events are best-effort fire-and-forget within the process lifetime; **dropped events are acceptable** (telemetry is statistical, not an audit log). A durable spool + batch flush would *want* the v2 daemon ([Q-DAEMON](../OPEN-QUESTIONS.md)) — out of scope, see open questions.

## Privacy threat model
- **PII hides in error strings and paths.** A raw `dial tcp 10.0.0.5:5432: connect: connection refused` or `open /home/alice/work/acme-secrets/.env: no such file` leaks an IP, a username, and a private repo name. Mitigation: we **never** transmit `err.Error()` or any path — only the closed `error_category` enum. The mapping from wrapped error → category happens in `internal/telemetry`, and the category set is small and reviewed; an unmatched error becomes `error_other` (no text).
- **Allowlist, not denylist.** The event struct has only the fields above. There is no generic string map and no reflection-based "dump everything" path, so a future field can't accidentally start carrying PII — adding a field is a deliberate, reviewed code + spec change.
- **Source IP is unavoidable at the network layer.** The OTLP endpoint sees the client IP like any HTTP server. We document this honestly (it is *not* in the payload, and the project collector is configured to drop/not-log source IPs); a user who considers their IP sensitive should leave telemetry OFF (the default).
- **Install id is not identity.** `install_id` is a random UUID with no machine derivation, rotatable with `telemetry reset` (regenerates the id) and erased by `telemetry disable`. It correlates events from one install for funnel analysis, nothing more.
- **Consent is unambiguous and persisted.** Default-No prompt, explicit enable, single-command revoke, honored next run. We never re-prompt within a major after a decision, and never treat "ignored the prompt" as consent.
- **The notifier is a separate call with its own opt-out.** The [spec 14](14-self-update-and-migration.md) update check is **not** telemetry: different endpoint (GitHub Releases), different purpose, different (default-ON, opt-out) policy, different env switches (`DEVSTACK_NO_UPDATE_NOTIFIER`). Disabling one never silently disables the other; `doctor` reports both network behaviors distinctly so users aren't surprised by two outbound calls.

## Verified constraints / gotchas
- **OTLP exporter must stay pure-Go.** `go.opentelemetry.io/otel` + the `otlptracehttp`/`otlploghttp` exporters are pure-Go and cross-compile to all four targets with `CGO_ENABLED=0`; do **not** pull the OTLP **gRPC** exporter or any collector-runtime packages (heavier deps, slower builds). Pin the otel modules — they version in lockstep and move fast ([dependency-risk register](../DECISIONS.md)).
- **`runtime.GOOS == "linux"` does not distinguish WSL2** — reuse the existing [spec 08](08-state-locking-and-lifecycle.md)/[ARCHITECTURE §8](../ARCHITECTURE.md) WSL detection for `is_wsl2`; that dimension is the highest-value field, so don't approximate it.
- **`DO_NOT_TRACK` is a real, widely-honored convention** (consoledonottrack.com); honoring it costs one env check and signals good faith — treat `DO_NOT_TRACK` ∈ {`1`,`true`} (case-insensitive) as auto-off.
- **Never block the command.** Like the notifier's 800ms budget, the exporter runs detached with a hard timeout; a hung collector or captive-portal DNS must not add latency to `up`. Do not call `TracerProvider.Shutdown()` (it flushes synchronously) on the command's hot exit path.
- **Consent must not be inferable from the network.** The first-run prompt and all collection are TTY-gated and CI-gated, so an automated environment never both prompts and sends — avoiding the classic "CI silently enabled telemetry" incident.
- **Don't conflate sample-out with opt-out.** A sampled-out event is silently dropped (normal); an opted-out user sends nothing *and* the code path short-circuits before constructing any event — these are different branches and both need tests.
- **A telemetry config file must never be created by `import`/generation.** It is written only by an explicit `telemetry enable/disable` (or the first-run prompt answer); the generation pipeline ([spec 02](02-templating-and-generation.md)) never emits it, keeping consent out of committed artifacts.

## Acceptance criteria
- [ ] With no consent file present, no command emits any network call to the telemetry endpoint, and `telemetry status` reports OFF.
- [ ] The first-run prompt appears only on an interactive TTY mutating command, defaults to No, and is never shown again after any answer; declining persists `enabled: false`.
- [ ] `--no-telemetry`, `DEVSTACK_NO_TELEMETRY=1`, `DO_NOT_TRACK=1`, `CI`, non-TTY stdout, and `--json`/`--quiet` each independently force OFF with no network call, even when consent is `true`.
- [ ] `telemetry show` prints the exact event JSON that would be sent and the endpoint, performing zero network I/O.
- [ ] A CI test asserts the event payload contains **no** path, repo/project name, secret name/value, env name/value, `${ref}` key, or raw error string — only allowlisted fields (mirrors the [spec 04](04-secrets.md) no-secret-in-output test).
- [ ] A wrapped daemon-unreachable / compose-too-old error maps to its `error_category` enum and the raw `err.Error()` never appears in the payload.
- [ ] A telemetry endpoint that hangs, times out, or returns 500 leaves the command's exit code, stdout, and latency unchanged (best-effort proven under a fault-injected exporter).
- [ ] `telemetry disable` is honored on the very next invocation; `telemetry reset` rotates `install_id`.
- [ ] `error` outcomes are sent regardless of `sampleRate`; a `0.0` sample rate still sends errors and never sends `ok` outcomes.
- [ ] The built binary remains `CGO_ENABLED=0` static with the OTLP/HTTP exporter linked in (no CGO, all four targets).

## Dependencies / consumers
Consumes `internal/version` (the `tool_version` stamp — [spec 14](14-self-update-and-migration.md)), `internal/docker` (the already-probed docker/compose versions — [spec 03](03-workspaces-and-shared-services.md)), `internal/xdg` (consent-file path + WSL2 detection), and `internal/config` (the `telemetry.*` keys + the global `--no-telemetry`/error-render contract — [spec 07](07-cli-and-aliasing.md)). Consumed by `internal/cli` (the `telemetry` command group, the first-run prompt hook on mutating commands, and the per-command emit at exit) and `internal/doctor` ([spec 13](13-doctor-diagnostics-and-teardown.md) reports telemetry + notifier network behavior). It deliberately does **not** touch `internal/lock` or `internal/state` — telemetry never mutates shared ledger state, so it stays off the concurrency spine ([spec 08](08-state-locking-and-lifecycle.md)). **Thinner v2 vs full:** a thin first cut (consent + auto-off + allowlisted event + best-effort OTLP export + `telemetry show/enable/disable/status`) lands in ~1w; the full version (deterministic sampling, `error_category` enum coverage across every subsystem's wrapped errors, `telemetry reset`, and the self-host collector config + docs) is ~1.5w.

## Open questions
- [Q-DAEMON](../OPEN-QUESTIONS.md) — a durable event spool with batched flush (so events survive a quick command and aren't dropped) would *want* the v2 background agent; v1/v2 telemetry stays fire-and-forget within the process. Revisit alongside the dashboard.
- **Q-TELEMETRY** (new) — where does the *default* OTLP endpoint live, who operates it, and what is its data-retention + source-IP-dropping policy? This must be answered (and published) before telemetry ships, because the default endpoint is the project's standing privacy promise. If no project-operated collector exists at ship time, default the endpoint to **empty** (telemetry can be enabled but goes nowhere until a self-hoster sets `telemetry.endpoint`).
