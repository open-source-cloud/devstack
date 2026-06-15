# Spec 16 — Log aggregation & live TUI dashboard

**Module:** `internal/dashboard` (+ `internal/cli` `logs`, `internal/docker`) · **Milestone:** v2 · **Effort:** ~4w (feature #8)

## Purpose
A full-screen bubbletea cockpit (`devstack dashboard`) that shows every shared + project service with its state/health, live CPU/mem, the per-project local + tunnel URL, **which projects reference each shared service**, and a bottom pane that multiplexes and filters logs across services. `devstack logs <svc>… [-f]` is its non-interactive sibling: a color-keyed, multiplexed log stream with a `--json` line contract. Together they make the shared-services graph — the product's core idea — directly observable.

> **Post-1.0, deferred.** This builds entirely on the v1 substrate: the read-only Engine SDK ([spec 03](03-workspaces-and-shared-services.md) §Build/run split), the ledger's `service_ref`/`shared_service`/`port_alloc`/`event_log` tables ([spec 08](08-state-locking-and-lifecycle.md)), the health prober ([spec 10](10-health-readiness-and-ordering.md)), and route data ([spec 05](05-networking.md)). It is deferred because it is **pure observation over what v1 already produces** and adds nothing to the core orchestration; it earns its place only once that substrate is rock-solid.

## Strictly read-only, no daemon
Two hard invariants inherited from ARCHITECTURE §1/§4 and [DECISIONS D4](../DECISIONS.md):

- **Observe, never mutate.** The dashboard and `logs` drive only the read-only Engine SDK streams — `ContainerLogs`, `ContainerStats`, `Events` — plus the `.State.Health.Status` read shared with [spec 10](10-health-readiness-and-ordering.md). It **never** calls `ContainerStart`/`Stop`/`Exec` (Compose owns lifecycle), never writes the ledger, and therefore runs **lock-free** ([spec 08](08-state-locking-and-lifecycle.md) §lock): the ledger is read as a snapshot at startup and refreshed lazily. Any action keys (a future "restart this service") must shell out to `docker compose` under the flock — explicitly out of scope here; the cockpit is a viewer.
- **Only for the duration of the command** ([Q-DAEMON](../OPEN-QUESTIONS.md)). There is no background watch: close the dashboard or `Ctrl-C` the `-f` stream and all SDK streams are torn down. State the absence of a daemon plainly — a service that goes unhealthy after you quit is caught lazily on the next command, exactly as elsewhere. (A daemon would let the dashboard run detached and power autostop; that trade-off stays parked in [Q-DAEMON], not assumed here.)

## Data sources (one read-only collector)
`internal/dashboard` owns one `collector` that fans **in** from four read-only sources and fans **out** to either the TUI model or the `logs` writer:

| Pane / column | Source | Notes |
|---|---|---|
| service rows (shared + per-project) | label-filtered SDK `ContainerList(All=true)` + ledger snapshot | filter on the **tool-owned label** + correct Docker context; exclude `oneoff=true` ([spec 03](03-workspaces-and-shared-services.md)) |
| state / health | `.State.Health.Status` via `ContainerInspect`, shared with [spec 10](10-health-readiness-and-ordering.md) | `starting\|healthy\|unhealthy`; no-healthcheck rows show `running`/`exited` only |
| CPU / mem | SDK `ContainerStats` **stream** (`stream=true`, one goroutine per container) | the expensive one — see gotchas; computed deltas, not the daemon's pre-baked % |
| local + tunnel URL | the `[]Route{host,upstream,port,tls}` ([spec 05](05-networking.md)) + `port_alloc` rows | local `https://<svc>.<proj>.localhost`; tunnel URL only when cloudflared is up |
| **referenced-by** | `service_ref(project, service, shared_service)` ([spec 08](08-state-locking-and-lifecycle.md)) | the ref ledger made visible: each `shared-postgres` row lists the projects holding a ref |
| log lines | SDK `ContainerLogs(Follow, Timestamps, Stdout, Stderr)` per service | demuxed, ring-buffered, color-keyed — see below |
| live transitions | SDK `Events` (`type=container`, label-filtered) | drives row add/remove and **re-attach on restart** without a busy poll |

Live updates are **event-driven**: subscribe to the Engine `Events` API once and translate `start`/`die`/`health_status` events into bubbletea `tea.Msg`s, rather than re-listing on a timer. A slow fallback poll (~2s) covers daemons/contexts where the event stream stalls.

## Log multiplexing & backpressure
- **Demux is mandatory.** A container **without** a TTY returns `ContainerLogs` as a multiplexed stream where stdout and stderr are interleaved with an 8-byte header frame; reading it raw corrupts output. Run every non-TTY stream through `stdcopy.StdCopy` (the `moby` demuxer) to split streams; TTY containers stream raw. Tag each demuxed line with its service + stream (stdout/stderr) for color-keying and filtering.
- **Color key + alignment.** Each service gets a stable color (hash of name → 256-color palette, degrade to ANSI-16 / monochrome by `$TERM`/`NO_COLOR`), a left-aligned `service │` gutter, and the container timestamp when `--timestamps`. stderr is dimmed/italic where the terminal supports it.
- **Ring-buffered, bounded fan-in.** High-cardinality fan-in (a dozen services in `-f`) can outpace the renderer and the terminal. Each stream feeds a **bounded ring buffer** (per-service line cap, e.g. 5–10k); on overflow the **oldest** lines are dropped and a `… N lines dropped` marker is emitted — never block the producer, never grow unbounded. The TUI renders only the visible viewport + scrollback window; `logs` writes straight through with a bounded channel so a slow `| less` applies natural backpressure without OOMing the collector.
- **Re-attach on restart.** A container restart mid-stream ends its `ContainerLogs` reader (EOF). The collector watches the `die`→`start` event pair (or detects EOF) and **re-opens the stream from `since=<last-seen-ts>`**, emitting a `↻ reattached` marker, so a `compose up`/crash-loop during `-f` doesn't silently drop the service. Same logic re-arms a `ContainerStats` stream.

## `dashboard` layout (TTY only)
A bubbletea full-screen app with three regions:

```
┌ services ───────────────────────────────────┬ detail ─────────────┐
│ ● shared-postgres  healthy  3.1%  412MB      │ shared-postgres      │
│     ↳ refs: api, web, billing                │ engine: postgres 18  │
│ ● shared-redis     healthy  0.4%   28MB      │ refs: 3 projects     │
│ ○ api/php          healthy  12%   190MB      │ url: —  (no host port)│
│     url https://api.shop.localhost           │ started 2h ago        │
├ logs (filter: api,shared-postgres) ──────────┴─────────────────────┤
│ api            │ 12:03:01  GET /healthz 200                          │
│ shared-postgres│ 12:03:02  LOG: checkpoint complete                  │
└ [f]ilter [/]search [tab]focus [j/k]scroll [q]uit ────────────────── ┘
```

- **Top-left:** service table grouped shared-first, then per project; columns state · health · CPU% · mem; shared rows carry the `↳ refs:` line. **Top-right:** detail for the selected row (engine+version, ref list, URL, uptime, last event from `event_log`).
- **Bottom:** the multiplexed log pane, filterable to a service subset (`f`), regex/substring search (`/`), follow toggle. `tab` moves focus; `q`/`Ctrl-C` tears everything down.
- Window resize, `lipgloss` styling, and a `--refresh` cadence override (default event-driven + 2s safety poll). No mouse requirement; keyboard-first.

## `logs` — the non-interactive sibling
```
devstack logs [service...] [-f|--follow] [--since DUR|TS] [--tail N] [--timestamps] [--no-color] [--json]
```
Resolves bare service names and `workspace.shared.*` against the parsed workspace config (cobra `ValidArgsFunction` completion, [spec 07](07-cli-and-aliasing.md)); no args = all services in the current workspace. `--since` and `--tail` map straight onto `ContainerLogs` options; `-f` follows with the same demux/ring-buffer/re-attach pipeline as the dashboard. **TTY detection drives presentation** (`golang.org/x/term`, ARCHITECTURE §7.9): a TTY gets the color-keyed gutter, a pipe gets plain prefixed lines, and `--json` emits one object per line:
```json
{ "ts": "2026-06-14T12:03:01.412Z", "service": "api", "project": "devstack-shop",
  "stream": "stdout", "container": "devstack-shop-api-1", "line": "GET /healthz 200" }
```
`--json` implies `--no-color`, never interleaves a TUI, and is the scriptable contract (consistent with `up`/`status`/`doctor`). `dashboard` refuses to start on a non-TTY and prints "use `logs`/`status --json` for non-interactive output."

## Verified constraints / gotchas
- **`ContainerStats` is expensive and its CPU accounting is platform-skewed.** The stream emits a full snapshot per container per interval (~1s); a dozen containers is real overhead. CPU% must be **computed** from `cpu_stats` vs `precpu_stats` deltas over `system_cpu_usage × online_cpus` — and on the **Docker Desktop VM (macOS/WSL2)** `online_cpus`/`system_cpu_usage` reflect the *VM's* allocation, not the host's, so percentages differ from native Linux and from Activity Monitor/Task Manager. Label the column "CPU% (engine)" and don't promise host-accurate numbers. Make the stats stream **opt-out** (`--no-stats`) for low-power machines.
- **`ContainerLogs` needs demux.** Non-TTY containers return the multiplexed stdcopy stream (8-byte frame header); a raw read corrupts the bytes. Always route through `stdcopy.StdCopy` for non-TTY; only TTY containers stream raw. (Mirrors the `ContainerLogs` use in [spec 10](10-health-readiness-and-ordering.md)'s fail-fast log inlining.)
- **High-cardinality fan-in needs backpressure.** Unbounded channels OOM the collector when one chatty service floods; bounded ring buffers + oldest-drop markers are required, not optional (per-stream caps, drop-counted).
- **A container restart mid-stream silently ends the reader.** `ContainerLogs`/`ContainerStats` return EOF on restart; without `die`/`start` event re-attach (`since=<last-ts>`), a crash-looping service vanishes from a live `-f`. Re-attach is a correctness requirement, not polish.
- **Engine logs only work with the `json-file`/`local` logging driver.** If a service sets `logging.driver: syslog/none`, `ContainerLogs` returns an error ("configured logging driver does not support reading"); surface a one-line remediation rather than a stack trace, and skip that row in the dashboard.
- **Multiplexed timestamps are per-container monotonic, not globally ordered.** Merging streams by arrival is best-effort; when `--timestamps` is set, sort within a small bounded window per render — never buffer the whole history to globally sort (defeats `-f`).
- **TTY/`NO_COLOR` detection** uses `golang.org/x/term` on both stdout and stderr (a piped stdout with a TTY stderr still wants plain log lines); honor `NO_COLOR` and `$TERM=dumb` (ARCHITECTURE §7.9, [spec 07](07-cli-and-aliasing.md)).
- **Read-only ≠ free of context pitfalls:** all enumeration uses the active Docker context and the tool-owned label with `All=true`, excluding `oneoff=true` — the same rules that keep ref-counting honest ([spec 03](03-workspaces-and-shared-services.md), [DECISIONS D5](../DECISIONS.md)). The wrong context shows an empty cockpit.

## Acceptance criteria
- [ ] `dashboard` lists every shared + project service for the active workspace/context with live state, health, and CPU%/mem from the stats stream; quitting tears down all SDK streams (no goroutine/stream leak).
- [ ] Each shared-service row shows its **referencing projects** sourced from `service_ref`; `down`-ing a project and refreshing drops it from that list.
- [ ] A project service row shows its `https://<svc>.<proj>.localhost` URL and, when cloudflared is up, its tunnel URL — both from the same `[]Route` as [spec 05](05-networking.md).
- [ ] `logs api shared-postgres -f` multiplexes both, color-keyed by service, with stdout/stderr correctly demuxed (no frame-header corruption).
- [ ] Restarting a followed container (`compose restart`) → its stream re-attaches from the last-seen timestamp with a `↻ reattached` marker; no lines silently lost.
- [ ] A service flooding logs faster than the terminal drains → bounded memory, a `… N lines dropped` marker, and the collector never blocks other services.
- [ ] `logs --json` emits one JSON object per line with `ts/service/project/stream/container/line`; `--json` disables color and never starts the TUI.
- [ ] On a non-TTY (piped) invocation `dashboard` refuses with a redirect to `logs`/`status --json`; `logs` falls back to plain prefixed lines.
- [ ] A service using the `none` logging driver yields a one-line remediation, not a crash; the rest of the dashboard still renders.
- [ ] The dashboard/`logs` take **no flock and issue no SDK mutation** for the duration of the command (verified: lock file untouched, only read calls in the `--debug` log).

## Dependencies / consumers
**Consumes** `internal/docker` (read-only `ContainerLogs`/`ContainerStats`/`Events`/`ContainerInspect` — extends the SDK wrapper with streaming readers + `stdcopy` demux), `internal/state` (lock-free snapshot of `service_ref`/`shared_service`/`port_alloc`/`event_log`, [spec 08](08-state-locking-and-lifecycle.md)), `internal/health` (`.State.Health.Status`, [spec 10](10-health-readiness-and-ordering.md)), `internal/proxy`/`internal/tunnel` (the `[]Route`, [spec 05](05-networking.md)), `internal/config` (service-name resolution + completion), and `internal/cli` (the global `--json/--quiet` + TTY contract, [spec 07](07-cli-and-aliasing.md)). **Consumed by** no one — it is a leaf viewer. A **thinner v2** (`logs <svc>… -f` multiplexing + `--json`, demux, ring buffer, re-attach; **no** TUI) lands in ~1.5w and delivers most of the daily value; the full bubbletea cockpit with stats/refs/URL panes and event-driven rows is the ~4w scope.

## Open questions
[Q-DAEMON](../OPEN-QUESTIONS.md) (a daemon would let the dashboard run detached + watch continuously; v2 keeps it command-scoped), [Q-RUNTIME](../OPEN-QUESTIONS.md) (stats/logs/events semantics assume Docker's Engine API). **New: [Q-DASH-STATS]** — should CPU/mem default **on** (richer cockpit, but a heavy `ContainerStats` stream per container, VM-skewed on Desktop/WSL2) or **opt-in** behind `--stats` (cheaper default, accurate-by-omission)? Recommendation: on for `dashboard`, `--no-stats` to disable; off for `logs` (logs never needs stats).
