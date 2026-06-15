# Spec 09 — Orchestration & one-command onboarding

**Module:** `internal/orchestrate` (+ `internal/cli`, `internal/state`, `internal/lock`) · **Milestone:** M2/M6 · **Effort:** ~3w (feature #1)

## Purpose
Compose every subsystem into one proven path: `devstack up` takes a fresh checkout (just `workspace.yaml` + per-repo `devstack.yaml`) to a fully running, health-gated stack in a single idempotent command. This is the headline DX promise — it replaces devdock's `config init` + `git clone` + `config docker -g` + `docker up -g` dance with one verb. The saga itself owns no domain logic; it **sequences** the modules ([git](06-git.md), [workspace](03-workspaces-and-shared-services.md), [secrets](04-secrets.md), [generate](02-templating-and-generation.md), [health](10-health-readiness-and-ordering.md), [hooks](11-lifecycle-hooks.md)) under the concurrency spine ([spec 08](08-state-locking-and-lifecycle.md)), and makes the multi-phase operation **resumable, crash-safe, and observable**.

## The `up` saga — 10 named phases
Each phase is **named, idempotent, and resumable**. A phase records its own start/satisfied/failed transitions in the ledger (below), so a re-run **skips** any phase already `satisfied` for an unchanged input fingerprint. Phases run in order; a hard failure halts the saga and triggers compensation (reverse order) for the phases that mutated global state.

| # | Phase | Module | Mutates global state | Skippable | Lock held |
|---|---|---|---|---|---|
| 1 | `preflight` | [doctor](13-doctor-diagnostics-and-teardown.md) | no | `--no-preflight` (not default) | no |
| 2 | `clone` | [gitx](06-git.md) | no (repo FS only) | `--skip-clone` | **no** |
| 3 | `network` | [workspace](03-workspaces-and-shared-services.md) | **yes** (external net) | implicitly (idempotent ensure) | **yes** |
| 4 | `shared` | [workspace](03-workspaces-and-shared-services.md) + [health](10-health-readiness-and-ordering.md) | **yes** (containers + ref rows) | per-service (compose idempotent) | partial |
| 5 | `provision` | [provision](03-workspaces-and-shared-services.md) | **yes** (role/db/bucket + ownership rows) | guarded SQL (idempotent) | **yes** |
| 6 | `secrets` | [secrets](04-secrets.md) | no (in-memory only) | always re-runs (never cached to disk) | no |
| 7 | `trust` | [trust](05-networking.md) | host CA store | **opt-in only; never blocks** | no |
| 8 | `generate` | [generate](02-templating-and-generation.md) | no (repo `.devstack/` only) | `writeIfChanged` (SHA-256) | no |
| 9 | `compose-up` | [docker](03-workspaces-and-shared-services.md) | yes (project containers + port rows) | recreate only on config change | partial |
| 10 | `hooks` | [hooks](11-lifecycle-hooks.md) | `firstRun` idempotency rows | `--no-hooks`; `firstRun` once/volume | no |

Notes on individual phases:
- **(1) preflight** runs the [doctor](13-doctor-diagnostics-and-teardown.md) capability matrix at *critical* severity (daemon reachable + correct context, compose ≥ v2.20, git ≥ 2.30, disk, 9p state-dir warning). Critical failures abort *before* any mutation; warnings print and continue. `--no-preflight` exists for fast inner loops but is never the default.
- **(2) clone** drives `gitx` with an `errgroup` to clone/pull every `projects[]` repo in parallel (`SingleBranch`, `Tags: NoTags`). Auth flows through the `GIT_ASKPASS` token shim ([spec 06](06-git.md)) — never the URL. Already-present repos are `pull`ed (fast-forward), not re-cloned. **No lock** — this is minutes of network I/O and must not serialize terminals.
- **(3) network** is an idempotent `NetworkInspect`→`NetworkCreate` of the pinned external `devstack_shared` ([spec 03](03-workspaces-and-shared-services.md)). **Must complete before any `compose up`** — Compose refuses to create `external: true` networks.
- **(4) shared** brings up only the shared services transitively `uses`d by the requested projects (`docker compose -p devstack-shared up -d <those services>`), inserts ref rows, then **health-gates** them; readiness detail (per-engine probes, `dependsOn: healthy`, timeouts) is deferred to [spec 10](10-health-readiness-and-ordering.md). Lock is held only for the ref-row insert + network/port mutations, **released during health polling**.
- **(5) provision** runs existence-guarded `pgx/v5` SQL + redis index + minio bucket per project ([spec 03](03-workspaces-and-shared-services.md)); fully inside the lock (`CREATE ROLE`/`CREATE DATABASE` race otherwise).
- **(6) secrets** is one batched `Resolve([]Ref)` pass per provider ([spec 04](04-secrets.md)); values live only as Go strings + `exec.Cmd.Env` and are **never** written to disk or the ledger.
- **(7) trust** installs/verifies the local CA via `mkcert` **only when `network.proxy.httpsLocal: true`** and is **fenced so a broken trust path never aborts `up`** — a trust failure degrades to a warning ([spec 05](05-networking.md)).
- **(8) generate** runs the deterministic pipeline ([ARCHITECTURE §3](../ARCHITECTURE.md)); `writeIfChanged` means a no-op when inputs are unchanged.
- **(9) compose-up** runs `docker compose -p devstack-<name> up -d` per project (optionally `--build`); allocates host ports inside the lock only if the project requests host reachability.
- **(10) hooks** runs `postUp` always and `firstRun` once per provisioned volume (idempotency tracked in `hook_run`, [spec 11](11-lifecycle-hooks.md)).

## Durable phase-state (resumability)
Resumability is what makes one-command onboarding cheap to re-run. We add **one table** to the [spec 08](08-state-locking-and-lifecycle.md) ledger (forward-only migration), and reuse `event_log` for the human-readable trail:

```sql
saga_phase(
  ctx        TEXT NOT NULL,        -- Docker context (the ledger key)
  workspace  TEXT NOT NULL,        -- workspace name
  scope      TEXT NOT NULL,        -- "" for workspace-wide, else project name
  phase      TEXT NOT NULL,        -- preflight|clone|network|shared|...
  status     TEXT NOT NULL,        -- pending|started|satisfied|failed
  fingerprint TEXT NOT NULL,       -- SHA-256 of the phase's resolved inputs
  started_at  INTEGER,
  satisfied_at INTEGER,
  error      TEXT,
  PRIMARY KEY (ctx, workspace, scope, phase)
)
```

A phase is **skipped** iff `status=satisfied` AND its recomputed `fingerprint` matches (so editing `devstack.yaml` re-arms `generate`/`compose-up`; bumping a `params.version` re-arms `shared`/`provision`). The fingerprint, not a bare boolean, is what prevents stale skips. `started` rows older than a deadline (or with no live container backing them) are treated as crashed and re-run after reconcile — never blindly trusted. Every transition also writes an `event_log` row (`kind=saga`, `reason="phase shared satisfied in 4.2s"`) so "why did my DB restart" stays answerable.

## Saga & compensation semantics
Each mutating phase declares a **compensating action**, invoked in reverse order when a *later* phase fails, to avoid a half-up workspace + a lying ledger ([ARCHITECTURE §7.2](../ARCHITECTURE.md)):

| Phase | Compensation on downstream failure |
|---|---|
| `network` | leave the network (shared by other workspaces — never auto-removed) |
| `shared` | delete the **ref rows** we inserted; leave containers running (other projects may reference them) |
| `provision` | nothing — see below |
| `compose-up` | `compose down` *this* project's stack **only** if it never reached created; delete this project's port rows |
| `hooks` | nothing (hooks must be self-idempotent) |

**Explicitly NOT auto-rolled-back — stateful data is never destroyed by a failed `up`:** provisioned roles/databases, MinIO buckets, named volumes (Postgres data!), and shared-service containers persist across a failed run. A failed `up` may leave *ref rows* and *port rows* cleaned up, but **never** drops a volume or a DB — reclaiming those is the explicit-confirmation job of `db gc` / `workspace destroy` ([spec 13](13-doctor-diagnostics-and-teardown.md)). The PG18 volume footgun ([spec 03](03-workspaces-and-shared-services.md)) makes "never auto-destroy data on failure" a hard rule, not a preference.

## Lock granularity (a first-class design point)
The naive "wrap the whole saga in the flock" would serialize two terminals for **minutes** behind image pulls and clones. Instead the saga holds the `gofrs/flock` ([spec 08](08-state-locking-and-lifecycle.md)) **only around ledger / network / port / provision mutations** and **releases it during all long, non-mutating work**:

```
phase 1 preflight ───────────────────────────  (no lock)
phase 2 clone / pull (errgroup, minutes) ─────  (no lock)        ← biggest wall-clock cost
phase 3 network ensure ────────── [LOCK]──────  inspect→create, then release
phase 4 shared:
          insert ref rows ─────── [LOCK]──────  release
          docker compose up -d ──────────────  (no lock; idempotent)
          image pull (minutes) ──────────────  (no lock)          ← second biggest cost
          health poll ───────────────────────  (no lock)
phase 5 provision (CREATE ROLE/DB) [LOCK]─────  release
phase 6 secrets resolve ──────────────────────  (no lock)
phase 7 trust (opt-in) ───────────────────────  (no lock)
phase 8 generate (writeIfChanged) ────────────  (no lock)
phase 9 compose-up:
          allocate host ports ─── [LOCK]──────  release (only if host-reachable requested)
          docker compose up -d ──────────────  (no lock)
phase 10 hooks (firstRun gate) ─── [LOCK]──────  check+set hook_run row, release; run hook (no lock)
```

Each lock acquisition is short, idempotent, and re-entrant-safe across re-runs. Two concurrent `up`s therefore interleave their slow work and only briefly serialize on the four real races (network-ensure TOCTOU, port allocation, ref rows, `CREATE ROLE`) — exactly the four [spec 08](08-state-locking-and-lifecycle.md) names. Compose's own idempotency covers the `up -d` calls that run lock-free.

## Crash recovery
A `kill -9` mid-saga (the expected WSL2/9p failure) must be safe:
- The **next `up` resumes** from `saga_phase`: `satisfied` phases (matching fingerprint) skip; a `started`-but-not-`satisfied` phase whose backing reality is missing is re-run (phases are idempotent, so re-running a partially-applied phase converges).
- A self-healing **reconcile runs before phase 1** on every command ([spec 03](03-workspaces-and-shared-services.md)): cross-check ref rows / port rows against live Docker labels and prune orphans, so a crash that left ref rows but no container is healed lazily.
- `doctor --rebuild-state` reconstructs the entire ledger (including derivable `saga_phase` rows) from **live Docker labels + on-disk config** when `state.db` is lost or corrupt ([ARCHITECTURE §7.2](../ARCHITECTURE.md), [spec 13](13-doctor-diagnostics-and-teardown.md)). The ledger is a cache of reality, never the trusted source.

## Output contract
TTY detection via `golang.org/x/term` selects one of three renderers ([ARCHITECTURE §7.9](../ARCHITECTURE.md)):
- **TTY → bubbletea live checklist:** one line per phase with a spinner / ✓ / ✗, **nested progress** for `clone` (per-repo) and `shared` (per-service health), elapsed time per phase. The model is fed by a channel of phase events; rendering never blocks the saga.
- **Non-TTY plain → one line per phase** (`[ok] clone (3 repos, 12.1s)`), no spinners, no ANSI — safe for CI logs and `2>&1` capture.
- **`--json` → a streamed/array contract** for scripting:

```json
[
  {"phase":"clone","status":"ok","durationMs":12104,"error":null,
   "detail":{"repos":[{"name":"api","action":"clone","ms":8021}]}},
  {"phase":"shared","status":"ok","durationMs":4210,
   "detail":{"services":[{"name":"shared-postgres","health":"healthy"}]}},
  {"phase":"compose-up","status":"failed","durationMs":900,
   "error":"service api exited (1); last 20 log lines inlined"}
]
```

`status ∈ {ok, skipped, failed}`; `skipped` carries the satisfied fingerprint. The final process exit code is non-zero iff any phase is `failed`.

## Companion verbs & flags
- **`up [project...]`** — bootstraps the whole workspace, or only the named projects (+ their transitively `uses`d shared services).
  Flags: `--profile P` (slice via [spec 12](12-service-profiles-and-selective-up.md)), `--build` (compose build), `--rebuild` (`build --no-cache` the changed contexts then up), `--no-hooks`, `--skip-clone`, `--no-preflight`, `--json`/`--quiet`.
- **`down [project...]`** — `docker compose -p devstack-<name> down` the project stack(s), delete their ref rows, and for any shared service now at **0 refs** stop it **only if `shared.autostop: true`** (default **false** — warm DBs are cheap; churn is annoying, [spec 03](03-workspaces-and-shared-services.md)). Runs `preDown` hooks first. The external network and volumes are never touched by `down`.
- **`status`** — composes three views into one table: per-repo **git** state (`gitx --porcelain=v2`, [spec 06](06-git.md)), per-service **health** ([spec 10](10-health-readiness-and-ordering.md)), and the **shared-service ref graph** (which projects reference each shared service). It surfaces the *last saga outcome* per project from `saga_phase` (e.g. "compose-up failed 4m ago") so a partial `up` is visible without re-running. `--json` emits the documented machine schema.

## Verified constraints / gotchas
- **`docker compose up` recreates containers when the rendered config changes** (image/env/labels/mounts diff). For a project stack that's fine; for a **stateful shared service it is a footgun** — recreation drops every project's live DB connections and can re-trigger the PG18 PGDATA relocation. The saga **must not recreate a shared service implicitly**; a shared-service config change requires explicit confirmation ([spec 03](03-workspaces-and-shared-services.md)).
- **Image pulls dominate wall-clock** on a first `up` (gigabytes). This is precisely why phases 2 and 4 run **outside the lock** and why progress reporting is nested — a 4-minute pull with the lock held would block every other terminal.
- **The external network MUST exist before any `compose up`** — Compose treats `external: true` as "assume it exists" and errors otherwise. Phase 3 is therefore a hard predecessor of phases 4 and 9.
- **Clone auth flows through the `gitx` `GIT_ASKPASS` shim** ([spec 06](06-git.md)) — the saga inherits the user's SSH agent / credential helper; it must never embed tokens in URLs or `.git/config`, and must surface a git auth-prompt failure as an actionable remediation, not a silent hang.
- **The saga must be safe to run concurrently from two terminals** — the four mutating phases serialize on the flock; everything else interleaves. A second `up` started mid-first-`up` either skips satisfied phases or waits briefly on the lock, never corrupts the ledger ([spec 08](08-state-locking-and-lifecycle.md)).
- **Secrets must never be cached to disk** to "speed up" resumability — phase 6 always re-resolves in memory; a CI test asserts no secret value lands in any generated artifact ([spec 04](04-secrets.md), [ARCHITECTURE §7.5](../ARCHITECTURE.md)).
- **`firstRun` is keyed per provisioned volume, not per `up`** — a resumed saga must not re-seed an existing DB. Idempotency lives in `hook_run` ([spec 11](11-lifecycle-hooks.md)), checked inside the lock.
- **Reconcile correctness depends on label-filter rules** ([spec 03](03-workspaces-and-shared-services.md)): `All=true`, exclude `oneoff=true`, correct normalized project name (or filter on the tool-owned label), correct Docker context — or the pre-saga reconcile silently no-ops.

## Acceptance criteria
- [ ] On a fresh checkout with only `workspace.yaml` + `devstack.yaml`, a single `up` clones all repos, ensures the network, starts + health-gates shared services, provisions per-project DBs, generates, and brings every project stack up — green end to end.
- [ ] Re-running `up` with no changes skips all satisfied phases (each `status:"skipped"` in `--json`) and is near-instant.
- [ ] Editing a `devstack.yaml` re-arms exactly `generate` + that project's `compose-up` (fingerprint mismatch); unrelated projects stay `skipped`.
- [ ] `kill -9` during phase 5 (`provision`) → the next `up` reconciles, resumes from `provision`, and converges; no orphan ref rows, no `role already exists` crash.
- [ ] Two concurrent `up` invocations (two terminals) interleave clones/pulls and pulls, serialize only on the four mutating phases, and never produce `database is locked`, a duplicate port, or a duplicate role.
- [ ] A failure in `compose-up` for project `api` deletes `api`'s ref + port rows but leaves shared containers, volumes, and provisioned DBs intact (verified by `shared status` + a row count).
- [ ] `--no-hooks` skips phase 10; `--skip-clone` skips phase 2; `--no-preflight` skips phase 1 — each independently.
- [ ] An opt-in `trust` failure (`httpsLocal: true`, broken `mkcert`) degrades to a warning and `up` still completes; `httpsLocal: false` skips phase 7 entirely.
- [ ] `up --json` on a non-TTY emits the documented array `{phase,status,durationMs,error}`; exit code is non-zero iff any phase failed.
- [ ] `down web` decrements `shared-postgres` ref count; with `autostop:false` the container keeps running; with `autostop:true` it stops at zero refs.
- [ ] `status` shows a prior `compose-up` failure for a project without re-running the saga.

## Dependencies / consumers
**Consumes:** `internal/lock` + `internal/state` (the spine), `internal/doctor` (preflight), `internal/git`/`gitx` (clone), `internal/workspace` + `internal/provision` (network/shared/provision), `internal/secrets`, `internal/generate` + `internal/template`, `internal/health` ([spec 10](10-health-readiness-and-ordering.md)), `internal/hooks` ([spec 11](11-lifecycle-hooks.md)), `internal/trust` ([spec 05](05-networking.md)), `internal/docker`. **Consumed by:** `internal/cli` (`up`/`down`/`status` verbs, [spec 07](07-cli-and-aliasing.md)). **Effort:** a thinner v1 — sequential phases (no nested bubbletea, plain + `--json` only), `saga_phase` resumability, single-project `up`, compensation for `shared`/`compose-up` only — lands in **~1.5–2w** on top of the modules it sequences; the full saga (live nested TUI, `--profile` slicing, full compensation matrix, parallel project `compose-up`) is **~3w**. The phases are only as solid as the modules under them, so this spec lands **after** M2 (workspace/provision) and the M6 features it orchestrates (doctor/health/hooks).

## Open questions
- [Q-DAEMON](../OPEN-QUESTIONS.md) — without a daemon there is no autostop and no live re-reconcile; the saga reconciles lazily at the start of each command, which is why `down`'s `autostop` defaults to false.
- [Q-RUNTIME](../OPEN-QUESTIONS.md) — the saga assumes Docker + compose v2.20+; non-Docker runtimes change the network-ensure and compose-driver phases.
- **Q-SAGA-PARALLEL** *(new)* — should phase 9 (`compose-up`) run project stacks **in parallel** (faster on multi-project workspaces, but interleaves image-pull progress and complicates the nested checklist + per-project compensation), or sequentially for clarity? Recommendation: sequential in v1, parallel behind a `--parallel` flag once the checklist model proves stable.
