# Spec 10 — Health, readiness & dependency ordering

**Module:** `internal/health` (+ `internal/orchestrate`, `internal/docker`) · **Milestone:** M6 · **Effort:** ~2w (feature #3)

## Purpose
Make `up` deterministic by gating each service on the *readiness* of what it depends on — not on container "running" state, which says nothing about whether Postgres accepts connections. A typed healthcheck per service feeds a dependency DAG; the orchestrator brings shared services up first, polls them healthy, then starts dependents, blocking with per-service spinners and failing fast (with the unhealthy service's last logs) on timeout. This eliminates the "app crashed because the DB wasn't ready" flake and is the substrate the seed/migrate hooks ([spec 11](11-lifecycle-hooks.md)) and provisioning ([spec 03](03-workspaces-and-shared-services.md)) stand on.

## Healthcheck kinds (typed, per service/template)
A `healthcheck` block is declared on a service (or baked into a template). It compiles to **two** representations: a Compose-native `healthcheck:` emitted into the generated compose, and a tool-side prober used by the orchestrator. Kinds:

| kind | params | compose `test` lowering (runs **in-container**; Compose owns it) | tool-side probe (**read-only**) |
|---|---|---|---|
| `tcp` | `port` | `CMD-SHELL` `nc -z` / bash `/dev/tcp` | read `.State.Health.Status`; or `net.DialTimeout` to the **published host port** when one is allocated |
| `http` / `https` | `port`, `path`, `expectStatus` (default `200-399`), `host` | `CMD` `curl -fsS …` (image must have curl/wget) | read `.State.Health.Status`; or a host `http.Client` to the **published port** (skip-verify for `https` local CA) |
| `exec` | `command` (argv), exit `0` = healthy | `CMD` `<argv>` | read `.State.Health.Status` (the verdict of the compose `test`) — the tool never runs the exec itself |
| `pg_isready` | `port` (5432), `user`, `db` | `CMD-SHELL` `pg_isready -U $u -d $db` | read `.State.Health.Status`; or, when the DB port is published, a host `pgx` `Ping` *as the app role* |
| `redis` | `port` (6379), `auth?` | `CMD-SHELL` `redis-cli [-a $pw] PING` | read `.State.Health.Status`; or a host TCP `PING`/`+PONG` to the **published port** |

> **The tool-side layer is strictly read-only** (ARCHITECTURE §4 / [DECISIONS D4](../DECISIONS.md)): it reads `.State.Health.Status` — the verdict of the Compose-native `test`, which runs *inside* the container and is owned by Compose — and otherwise only opens **host-side** connections to **published** ports. devstack never calls `ContainerExec*`; running a process inside a container is Compose's job, not the SDK's. When a shared service publishes no host port (the default DNS-over-shared-network case), the only honest tool-side signal is `.State.Health.Status`.

Common timing fields (Compose-equivalent): `interval` (default `5s`), `timeout` (`3s`), `retries` (`10`), `startPeriod` (`30s`; **`60s` for stateful images** — see gotchas). The compile step normalizes to Go `time.Duration`; the compose lowering uses Compose's duration strings.

### Config-schema additions (cross-link [spec 01](01-config-schema.md))
```yaml
# devstack.yaml (or a template's defaults)
services:
  api:
    template: php.laravel.nginx
    uses: [workspace.shared.postgres, workspace.shared.redis]
    healthcheck:
      kind: http
      port: 8080
      path: /healthz
      expectStatus: 200
      interval: 5s
      timeout: 3s
      retries: 12
      startPeriod: 20s
    dependsOn:
      - { service: workspace.shared.postgres, condition: healthy }   # healthy|started
      - { service: workspace.shared.redis,    condition: healthy }
```
`dependsOn` targets may be **intra-project** service names or **shared** services (`workspace.shared.*`). `condition` is `healthy` (default) or `started`. Schema lives with the rest of the config model; `internal/health` owns the *semantic* validation (a `condition: healthy` target **must** declare a healthcheck — see gotchas).

## Two enforcement layers (both are required)
Compose's own `depends_on … condition: service_healthy` is necessary but **insufficient** here, because it cannot cross compose projects — and the whole point of this tool is one shared stack + many *separate* compose projects ([spec 03](03-workspaces-and-shared-services.md)).

1. **Compose-native (intra-project).** Emit `healthcheck:` for each service and lower intra-project `dependsOn` to `depends_on: { <dep>: { condition: service_healthy } }` in the generated compose. This gives correct *within-project* startup ordering for free, survives a manual `docker compose up`, and is what `compose up --wait` keys off.

2. **Tool-side polling (cross-project + UX).** The orchestrator polls health via the **read-only SDK** (`ContainerInspect` → `.State.Health.Status`) for: (a) the `up` spinner / `--json` records, and (b) **gating across compose projects** — it must confirm `shared-postgres` is `healthy` *before* it runs `compose -p devstack-api up`, because `depends_on` in project `api` can never reference a service in project `devstack-shared`. The same poll backs `status` and `shared status` health columns.

```
compose-native depends_on:  api ──service_healthy──► api-internal-cache   (same project ✔)
tool-side gate:             devstack-shared.shared-postgres  ──poll healthy──►  start devstack-api
                            (cross compose project — depends_on CANNOT express this)
```

## Dependency ordering (the DAG)
`internal/health` builds one workspace-wide graph from every service's `dependsOn` (shared services are nodes too), then **topologically sorts** it:

- **Cycle detection** is mandatory — a cycle is a config error reported with the full cycle path (`a → b → c → a`) and `file:line:col` on the offending edge, never a hang. (Validation mirrors the cross-ref/cycle resolver in [spec 01](01-config-schema.md).)
- The sort yields ordered **waves**; shared services land in the earliest waves (nothing depends *backward* onto a project from a shared service). The saga consumes this order: it starts shared services, gates them, then starts project waves in order. See the saga phases in [spec 09](09-orchestration-and-onboarding.md) and [spec 08](08-state-locking-and-lifecycle.md).
- Ordering is **deterministic** (stable sort on `(wave, name)`) so the `up` checklist and `--json` are reproducible — consistent with the determinism requirement in ARCHITECTURE §3.
- Only services in the **active profile** are nodes; edges to a not-started service are pruned (see profile interaction below and [spec 12](12-service-profiles-and-selective-up.md)).

## Blocking & failure UX
`up` blocks per wave until every gated service reports `healthy` or the **global timeout** (`startPeriod + interval*retries`, capped by a `--health-timeout` flag, default `120s`) elapses.

- **Polling cadence:** poll `.State.Health.Status` every `interval` with a small jittered backoff; treat `starting` as in-progress, `healthy` as done, `unhealthy` as fail-fast for *that* service. A container that exits (`Restarting`/`Exited`) before reporting health is an immediate failure.
- **Fail-fast diagnostics:** on `unhealthy`/timeout, inline the **last N log lines** (default 20) of the failing service via `docker compose -p <proj> logs --tail 20 <svc>` (SDK `ContainerLogs` fallback), name the failing healthcheck, and emit a one-line remediation (ARCHITECTURE §7.6). The saga then runs compensating actions ([spec 08](08-state-locking-and-lifecycle.md)) — it does **not** leave a half-up workspace with a lying ledger.
- **`--json` health record** (machine-readable contract, ARCHITECTURE §7.9):
```json
{ "service": "shared-postgres", "project": "devstack-shared", "kind": "pg_isready",
  "status": "healthy", "attempts": 3, "elapsedMs": 1840, "lastError": null }
```
- TTY → a bubbletea per-service spinner wave (consistent with the saga checklist); non-TTY/`--quiet`/`--json` → the records above, one per service.

## Interactions
- **Provisioning (spec 03):** the per-project `CREATE ROLE`/`CREATE DATABASE` pass runs **only after** `shared-postgres` is `healthy`. But `pg_isready` healthy ≠ the app role can connect (see gotchas) — `internal/provision`'s `pgx` connect retries on `57P03`/`53300`/auth-not-yet-granted. Health gating is the *first* guard, idempotent-SQL-with-retry the second.
- **Hooks (spec 11):** `firstRun`/`migrate` hooks for a service run **only after** that service's `dependsOn` chain is healthy — health gating is the precondition the hook ledger assumes; a hook never fires against a not-yet-ready dependency.
- **Profiles (spec 12):** health is gated **only for services in the active profile**. `up --profile frontend` builds the DAG over the frontend slice + the shared services it transitively `uses`; it never blocks on a service that was never asked to start. A `dependsOn` edge to an out-of-profile service is pruned (and warned if the dependency is non-shared).

## Verified constraints / gotchas
- **`condition: service_healthy` requires the dependency to *define* a healthcheck.** Without one, Compose errors at validate time (older behavior: silently degrades to `service_started`). The compile step **rejects** a `condition: healthy` edge whose target has no `healthcheck` — caught at generate time, not at `up`.
- **`service_healthy` only works *within one* compose project.** `depends_on` cannot reference another project's service, so cross-project shared-service readiness **must** use the tool-side SDK poll — this is the reason both layers exist.
- **Docker health states are exactly `starting | healthy | unhealthy`** (`.State.Health.Status`). A service with no healthcheck has empty `Health` and reports only `running`/`exited` — for those, `condition: started` (container running) is the only honest gate.
- **`pg_isready` can report ready before the server accepts the *app* role** (it answers during the brief window before roles/grants exist, and Postgres bounces connections during its own startup recovery). Provisioning must retry the `pgx` connect; never treat `pg_isready` as "app can log in."
- **`redis-cli PING` needs `AUTH` when `requirepass` is set** — without it `PING` returns `NOAUTH Authentication required` (non-zero exit → false `unhealthy`). Lower the `redis` kind to include `-a $pw` (from the resolved secret) when auth is configured.
- **Slow stateful image starts cause false `unhealthy` on Docker Desktop** (cold VM, qemu emulation on Apple Silicon, 9p I/O on WSL2). Use a generous `startPeriod` (failures during `startPeriod` don't count toward `retries`) — default `60s` for postgres/minio. Under-setting `startPeriod` is the #1 source of flaky `up`.
- **`http` healthchecks need curl/wget in the image** for the compose-native `test`; many slim images lack both. Prefer a tool-side HTTP probe against a **published port** (no in-image dependency) and emit the compose `test` only when the image is known to carry a client, or fall back to a `tcp` compose test + tool-side read of `.State.Health.Status`.
- **`compose up --wait` is not a substitute** for the tool-side gate: it waits only within the project it's invoked on and exits non-zero on any unhealthy — useful for the intra-project wave, but it cannot order the shared stack relative to project stacks.
- **Health polling is read-only** (SDK `ContainerInspect`/`ContainerLogs`) and therefore **lock-free** (ARCHITECTURE §7.1) — it never mutates the ledger, so it stays outside the `gofrs/flock`; only the saga's start/provision steps it triggers take the lock.

## Acceptance criteria
- [ ] A service with `dependsOn: [{service: workspace.shared.postgres, condition: healthy}]` does not start until `shared-postgres` reports `healthy` via the SDK poll (verified across the two compose projects).
- [ ] Intra-project `dependsOn` lowers to `depends_on: { <dep>: { condition: service_healthy } }` in the generated compose, and the depended-on service carries a `healthcheck:` block.
- [ ] A `condition: healthy` edge whose target declares **no** healthcheck fails at generate time with a `file:line:col` error, not at `up`.
- [ ] A `dependsOn` cycle is reported with the full cycle path and a non-zero exit — never a hang.
- [ ] On an `unhealthy` dependency, `up` fails fast, inlines the last 20 log lines of the failing service, names the failing check, and the saga compensates (no orphan ref rows; truthful `shared status`).
- [ ] `up --json` emits one health record per gated service with `status/attempts/elapsedMs`.
- [ ] `pg_isready` healthy but app role not yet usable → provisioning's `pgx` connect retries and succeeds without crashing `up`.
- [ ] A Redis with `requirepass` set passes its healthcheck (AUTH applied); without AUTH it would (and is shown to) report `unhealthy`.
- [ ] `up --profile frontend` builds the DAG over the frontend slice + transitively-used shared services only; it does not block on out-of-profile services.

## Dependencies / consumers
**Consumes** `internal/config` (the `healthcheck`/`dependsOn` schema, [spec 01](01-config-schema.md)), `internal/generate` (emits compose `healthcheck:`/`depends_on`, [spec 02](02-templating-and-generation.md)), `internal/docker` (read-only `ContainerInspect`/`ContainerLogs`, [spec 03](03-workspaces-and-shared-services.md)). **Feeds** `internal/orchestrate` (the wave order + gate, [spec 09](09-orchestration-and-onboarding.md), [spec 08](08-state-locking-and-lifecycle.md)), `internal/provision` (the "DB healthy" precondition, [spec 03](03-workspaces-and-shared-services.md)), `internal/hooks` ([spec 11](11-lifecycle-hooks.md)), and `internal/cli` `status`/`shared status` health columns ([spec 07](07-cli-and-aliasing.md)). A **thinner v1** (compose-native healthchecks + a single cross-project poll for the shared stack, `tcp`/`pg_isready`/`http` kinds, linear ordering) lands in ~1w; the full DAG with cycle paths, profile-aware pruning, per-kind tool-side probes, and the `--json` records is ~2w.

## Open questions
[Q-DAEMON](../OPEN-QUESTIONS.md) — without a daemon, health is only polled *for the duration of `up`*; there is no continuous readiness watch (a service that goes unhealthy later is caught lazily on the next command / `doctor`). [Q-RUNTIME](../OPEN-QUESTIONS.md) — health semantics assume Docker's `.State.Health` (Compose v2.20+). **New: [Q-HEALTHWAIT]** — should the tool-side cross-project gate prefer `docker compose up --wait` for the intra-project wave and reserve the SDK poll strictly for the cross-project shared gate, or always own the poll for uniform `--json` records and a single timeout/backoff policy? (Recommendation: own the poll uniformly; use `--wait` only as a belt-and-suspenders within a project.)
