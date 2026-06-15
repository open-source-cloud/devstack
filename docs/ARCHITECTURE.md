# Architecture

This document describes the runtime model, the core pipeline, the network topology, the module breakdown, and the cross-cutting concerns that no single component owns. Component detail lives in [`docs/specs/`](specs/).

---

## 1. Runtime model: a stateless CLI

`devstack` is a **stateless command-line tool with no background daemon** (for v1 — see the daemon trade-off in [OPEN-QUESTIONS](OPEN-QUESTIONS.md)). Every invocation:

1. loads configuration and the global state ledger,
2. takes a coarse cross-process lock before mutating any shared state,
3. drives Docker (via the `docker compose` CLI and the Engine SDK read-only),
4. and exits.

Consequences of "no daemon":
- There is **no automatic autostop** of shared services when their last consumer goes down. Reference-count drift is reconciled **lazily** on the next command (and via `devstack shared gc` / `devstack doctor --fix`).
- Long-running behaviors (health-gated `up`, log streaming, the dashboard) run **only for the duration of the command**.

The binary is a single static, CGO-free executable (`CGO_ENABLED=0`) cross-compiled to `darwin/{amd64,arm64}` and `linux/{amd64,arm64}`. WSL2 runs the ordinary `linux` build — it is **not** a separate build target, but it **is** a distinct *runtime-behavior* target (see §7).

---

## 2. State lives in exactly two places

| Location | What | Committed to git? |
|---|---|---|
| **Per-project, in the repo** | `workspace.yaml` (workspace root) + each repo's `devstack.yaml` — the declarative source of truth. | ✅ Yes |
| **Machine-global, under XDG dirs** | A SQLite ledger keyed by **Docker context**: shared-service reference counts, port-allocation registry, per-project DB/role/bucket provisioning ledger, a rolling event log; plus the alias registry, template cache, and the generated artifacts under `.devstack/` in the project. | ❌ No |

Keying the ledger by Docker context (not just hostname) avoids the WSL2 "two daemons" problem (Docker Desktop's daemon vs an in-distro `dockerd`) where ref-counting would otherwise enumerate the wrong containers.

The SQLite store uses **`modernc.org/sqlite`** (pure-Go, preserves the static binary), opened with **WAL + `busy_timeout=5s` + `foreign_keys=ON`**, with a versioned migrations table and a *backup-before-migrate* step. Full detail: [spec 08](specs/08-state-locking-and-lifecycle.md).

---

## 3. The generation pipeline (one owned path)

There is exactly **one** ordered pipeline from config to running containers. It must have a single owner and one set of golden integration tests; the four "stages" are not independently designed subsystems.

```
discover workspace            walk up from CWD for workspace.yaml (git/go.mod style)
        │
        ▼
load + validate config        goccy/go-yaml parse (source positions)
        │                     → jsonschema/v6 structural
        │                     → validator/v10 semantic + custom resolver (cross-refs, cycles)
        ▼
resolve template `extends`     render each layer, then deep-merge (explicit list strategy)
        │
        ▼
render artifacts              text-template the Dockerfiles / proxy conf / scripts;
        │                     build the compose model programmatically
        ▼
resolve ${ref:...}            cross-service refs resolved against the WORKSPACE graph
        │                     (not a single project) — this is what makes sharing work
        ▼
resolve secret:// refs        one batched pass per provider; values never written to disk
        │
        ▼
build + validate compose      typed model → compose-go/v2 validate/normalize
        │
        ▼
writeIfChanged + atomic        write each generated file only on diff (tmp + os.Rename);
        │                     SHA-256 each build context into the ledger
        ▼
selective rebuild + up         `docker compose build --no-cache <changed>` then `up -d`
```

**Key principle:** the compose document is built as a **typed model and validated through `compose-go/v2`**, not produced by string-templating YAML. Text templating is reserved for unstructured artifacts (Dockerfiles, proxy/entrypoint configs) and user-authored templates. See [spec 02](specs/02-templating-and-generation.md) and the open templating decision in [DECISIONS](DECISIONS.md#d2).

**Determinism is a hard requirement:** identical inputs must produce byte-identical generated files (sorted keys, fixed newline handling), because the rebuild-hash and any "commit generated artifacts" decision depend on it. CI verifies this.

---

## 4. Network topology — shared infra + per-project stacks

```
                    ┌─────────────────────── devstack_shared (external bridge network) ──────────────────────┐
                    │   pinned name:, created idempotently by devstack (NEVER by compose)                     │
                    │                                                                                          │
   ┌────────────────┴───────────────┐     ┌───────────────────────┐      ┌───────────────────────┐           │
   │  shared stack                  │     │  project stack: api    │      │  project stack: web    │           │
   │  (compose project              │     │  (compose project       │      │  (compose project      │          │
   │   `devstack-shared`)           │     │   `devstack-api`)        │      │   `devstack-web`)       │          │
   │                                │     │                         │      │                         │          │
   │  shared-postgres  (alias)      │◄────┤  api  ──uses──► postgres │      │  web ──uses──► postgres │          │
   │  shared-redis     (alias)      │◄────┤  worker                 │      │  (its own internal net  │          │
   │  shared-minio     (alias)      │     │  (api's own internal    │      │   for web-internal svcs)│          │
   │  caddy   (reverse proxy)       │     │   network for private   │      │                         │          │
   │  cloudflared (optional)        │     │   services)             │      │                         │          │
   └────────────────────────────────┘    └─────────────────────────┘      └─────────────────────────┘         │
                    └──────────────────────────────────────────────────────────────────────────────────────────┘
```

- **One tool-owned external bridge network** (`devstack_shared`, name pinned). Both the shared stack and every project stack join it. Compose refuses to create `external: true` networks and won't remove them on `down`, so **devstack owns create *and* cleanup** (`docker network create … || true` idempotently; removal only on `workspace destroy`).
- **Shared services are reached by stable DNS aliases** (`shared-postgres`, `shared-redis`, `shared-minio`) over that network — *not* by bare service name (two projects might each have an internal service literally named `postgres`; the `shared-` alias is the collision guardrail, enforced by a generate-time lint).
- **In the common case shared services publish no host ports** — projects talk to them over the network by DNS. Host ports are allocated (from a registry, inside the lock) only when the developer wants host reachability (psql, a GUI). See port allocation in [spec 03](specs/03-workspaces-and-shared-services.md) and [spec 08](specs/08-state-locking-and-lifecycle.md).
- **Lifecycle verbs shell out to `docker compose`** (`-p <project>` + tool-owned labels, require v2.20+). The **Engine SDK (`moby/moby/client`) is used read-only** for network ensure, label-filtered container enumeration, health polling, and ref-count reconciliation. devstack never *runs* containers via the SDK — Compose owns container lifecycle; the SDK is plumbing only. This split avoids ownership drift.

> Cross-platform note: on Docker Desktop (macOS/WSL2) containers run in a VM, so bridge IPs are **not** routable from the host — you must publish ports for host reachability. On native Linux the bridge subnet *is* host-reachable. The DNS-over-shared-network path behaves identically on all three.

---

## 5. Per-project data isolation on shared services

One shared Postgres serves many projects via **provision-on-demand**, *not* `initdb.d` scripts (those run only on a first-init empty volume, so they can't onboard a project added later):

- After the shared Postgres is healthy, devstack connects as superuser via **`jackc/pgx/v5`** and runs **idempotent, existence-guarded** SQL: `CREATE ROLE <proj>` / `CREATE DATABASE <proj_db> OWNER <proj>` / `REVOKE`+`GRANT` so each project role sees only its own DB. `CREATE DATABASE` is **not** idempotent — guard with `pg_database`/`pg_roles` checks.
- ⚠️ **Postgres 18+ volume path:** PGDATA moved to `/var/lib/postgresql` (was `/var/lib/postgresql/data`). Mounting the old path on PG18 silently loses persistence. Templates must mount the correct path for the pinned version.
- Redis: per-project logical DB index (0–15), or key prefix beyond 16 projects. MinIO: per-project bucket + scoped access key.
- An **ownership ledger** ties each provisioned DB/role/bucket to a project so a `db gc` reaper can reclaim orphans (with explicit data-loss confirmation) when a project is removed.

---

## 6. Module breakdown

Standard Go layout: `/cmd/devstack/main.go` (thin), `/internal/*` (all implementation), `/pkg/pluginsdk` (public plugin contract, deferred to v2), `/templates` (built-ins via `go:embed`).

| Module | Responsibility |
|---|---|
| `cmd/devstack/main.go` | `argv[0]` alias dispatch (`--as` override), build the cobra tree, `fang.Execute`; inject version/commit/date via ldflags. |
| `internal/cli` | Cobra command tree, tech-gated dynamic subcommand registration, `ValidArgsFunction` completions resolving service/project names, global `--json/--quiet/--debug/--verbose` and unified error rendering. |
| `internal/config` | Workspace discovery (walk-up), goccy parse with positions, JSON-Schema + validator/v10 + custom cross-ref/cycle resolver, profile/env overlay merge, immutable loaded model, embedded schemas, the `${env/self/ref/profile}` grammar. |
| `internal/template` | Text-template engine + ported filters; `renderText`/`renderYAML` modes; `extends`-chain resolution; `TemplateSource` interface (embedded for v1; git/OCI later); golden-test harness. |
| `internal/merge` | Layered deep-merge with explicit list strategy (replace default, `$merge: append` opt-in); guards shared-reference mutation. |
| `internal/generate` | Compose-model build + `compose-go` validate/normalize, `${ref}` resolution against the workspace graph, secret-key emission, `writeIfChanged` + atomic rename, SHA-256 rebuild-hash ledger. |
| `internal/docker` | `moby/moby/client` wrapper (read-only) + `docker compose` CLI driver with explicit `-p` and tool-owned labels; compose/version/context preflight. |
| `internal/workspace` | **The differentiator:** shared-stack lifecycle, ref-counting + self-healing reconcile, port allocation, per-(engine,major-version) shared instances, profile/selective-up interaction (the reference-graph walk behind `--profile`, [spec 12](specs/12-service-profiles-and-selective-up.md)). |
| `internal/state` | SQLite ledger (keyed by Docker context), WAL+busy_timeout, versioned migrations + backup, rolling event log, `doctor --rebuild-state` from live Docker labels. |
| `internal/lock` | `gofrs/flock` cross-process advisory lock wrapping all global mutations; detects 9p/networked state dirs and warns. |
| `internal/provision` | `pgx/v5` idempotent per-project Postgres role/db; redis index/prefix; minio bucket+key; ownership ledger + orphan gc. |
| `internal/git` (`gitx`) | Hardened system-git exec wrapper, `--porcelain=v2` status parse, shorthand expansion, `GIT_ASKPASS` token shim, errgroup parallel clone/sync/status. |
| `internal/secrets` | Provider interface + Registry + Factory, `secret://` ref parser, batched post-render resolution; v1 providers + OS-keyring (optional, in-memory fallback). |
| `internal/proxy` | Caddy shared-container orchestration via labels; `Proxy` interface (Traefik/nginx pluggable later). |
| `internal/trust` | mkcert (or owned CA) host/Firefox trust install; WSL2 `certutil.exe` interop; trust status/install/uninstall. |
| `internal/dns` | `*.localhost` guidance + idempotent marker-fenced `/etc/hosts` edits (sudo), removable on uninstall. |
| `internal/tunnel` | cloudflared shared container, named-tunnel create/route/up/down, `Tunnel` interface; secret-bearing-service refusal guard. |
| `internal/doctor` | Cross-platform capability-probe matrix with one-line remediations and `--fix`; also owns recovery (`--rebuild-state`) and teardown (`workspace destroy`/`uninstall`/`db gc`). See [spec 13](specs/13-doctor-diagnostics-and-teardown.md). |
| `internal/hooks` | Lifecycle hooks (`preUp`/`postUp`/`preDown`/`firstRun`/`postPull`) with an idempotency ledger; powers migrations/seeding. See [spec 11](specs/11-lifecycle-hooks.md). |
| `internal/health` | Typed healthchecks + `dependsOn: healthy` graph gating (cross-project read-only poll + compose-native ordering). See [spec 10](specs/10-health-readiness-and-ordering.md). |
| `internal/orchestrate` | The multi-phase `up`/bootstrap **saga**: named resumable phases with durable phase-state + compensating rollback; TUI checklist + plain fallback. See [spec 09](specs/09-orchestration-and-onboarding.md). |
| `internal/xdg` | XDG path resolution; WSL2 detection; refuse `/mnt/*` working dirs; template-cache GC/TTL. |
| `internal/migrate` | `devstack import` reading an old devdock `project.yaml` → new `workspace.yaml` + `devstack.yaml` split (optional; high-leverage for the existing user base); plus the three-artifact versioning policy + self-update choreography. See [spec 14](specs/14-self-update-and-migration.md). |
| `internal/version` | Build-stamped identity (`Version`/`Commit`/`Date` via ldflags); consumed by `--version` and the update notifier. See [spec 14](specs/14-self-update-and-migration.md). |
| `pkg/pluginsdk` | Public plugin contract — **deferred to v2** (out-of-tree secrets/tunnel providers). |

Every fast-moving or risky dependency (docker client, template engine, secrets provider, git, trust store) sits behind an internal interface so it can be swapped or vendored.

---

## 7. Cross-cutting concerns (owned by nobody by default — assign them explicitly)

These emerged from adversarial review and are the things most likely to bite. Each needs a named owner.

### 7.1 Concurrency & global-state locking — **the #1 risk**
Two simultaneous `up` invocations (two terminals; IDE + terminal; a watch script) race on network-ensure (TOCTOU between inspect and create), port allocation, ref-count rows, and `CREATE ROLE`. SQLite file locking is unreliable on WSL2/9p. **Mitigation (build first, in M0):** a coarse `gofrs/flock` advisory lock around *every* operation that mutates the ledger or the shared stack; WAL + `busy_timeout`; idempotent guarded SQL; port allocation done inside the lock; `doctor` warns on 9p state dirs. Reads are lock-free snapshots. See [spec 08](specs/08-state-locking-and-lifecycle.md).

### 7.2 Partial-failure / rollback for the multi-phase `up`
`up` runs ~8 phases (clone → network → shared services → provision DB → secrets → CA → generate → compose up). A failure in phase 4 after phase 3 started the shared stack and inserted ref rows leaves a half-up workspace and a lying ledger. **Mitigation:** model `up` as a **saga** with durable phase-state and compensating actions; `doctor --rebuild-state` reconstructs the ledger from live Docker labels. The saga's named phases, the `saga_phase` resumability table, and the lock-granularity rule are detailed in [spec 09](specs/09-orchestration-and-onboarding.md).

### 7.3 Teardown / uninstall
devstack creates artifacts everywhere: the external network, shared volumes (Postgres data!), the SQLite DB, **root CA in host + Firefox + Windows trust stores**, alias symlinks, cloudflared creds, keyring entries, the cache. `devstack workspace destroy` / `devstack uninstall` must reverse **all** of it, with explicit data-loss confirmation for volumes, and must never orphan a CA (a security artifact) or dangling symlinks. The teardown order and the `--fix`-vs-destroy boundary are owned by [spec 13](specs/13-doctor-diagnostics-and-teardown.md).

### 7.4 Migration / versioning (three things evolve independently)
1. **Config schema** — `apiVersion: devstack/v1`; define the unknown-key / `additionalProperties` forward-compat policy.
2. **State-DB schema** — versioned migrations table, backup-before-migrate, strictly additive within a major.
3. **Generated compose format** — `compose-go` re-normalization causes churny diffs; decide whether generated artifacts are committed (deterministic, reviewable) or gitignored (regenerated freely). Either way, golden output controls the churn. The compatibility guarantees for all three artifacts (config `apiVersion`, state `schema_version`, generated-compose byte-stability) and the self-update choreography tying them together are specified in [spec 14](specs/14-self-update-and-migration.md).

### 7.5 Secrets ↔ generation coupling, and in-memory exposure
The "host env auto-propagates into compose" assumption is **false** (verified). Each secret name must be emitted **per service** into the generated compose (as a valueless `environment: [NAME]` key) and the value passed via `exec.Cmd.Env` — so secrets and generation are coupled and need one owner plus a CI test asserting **no secret value ever lands in any generated file**. Resolved secrets also live as plain Go strings (GC'd, swappable, visible via `/proc/<pid>/environ` to same-user processes). Decision: document the threat model honestly rather than over-engineering `mlock` for a local dev tool. See [spec 04](specs/04-secrets.md).

### 7.6 Error UX & self-debuggability
~10 subsystems shell out to external tools. One error model: wrap every external-command failure with the exact command + exit code + captured stderr; map known signatures (daemon down, compose too old, port in use, git auth prompt, missing `certutil`) to actionable remediations; render config errors as `file:line:col` with a caret (goccy positions); `--debug` logs every external command run (`structured` via `log/slog`). This is what actually closes GitHub issues.

### 7.7 Testing strategy (a Docker-heavy, multi-writer, cross-platform tool)
- **Unit:** golden-file tests for templating/merge/config/parse.
- **Integration:** real daemon via `testcontainers-go` or a dind CI lane (network/ref-count/provisioning/health), behind a build tag so unit tests stay fast.
- **Race:** mock the docker client behind `internal/docker` to test concurrent-invocation logic.
- **Manual matrix:** a documented release checklist for the paths that can't run in hosted CI (macOS trust/resolver, WSL2 dual trust store, cloudflared). "Verified on my machine" must become a regression suite.

### 7.8 CI / release matrix
goreleaser cross-compiles from one Linux runner (easy). The hard part is the **test** matrix: a macOS arm64 runner (trust/resolver/Desktop-VM behavior), a Docker-enabled Linux lane (dind) for integration, `govulncheck` (go-git CVE class), a single enforced **Go 1.25** toolchain floor, and a JSON-Schema↔Go-struct drift check. Deterministic-generation must be asserted in CI.

### 7.9 Machine-readable output
The headline TUIs (git status, `up` checklist, dashboard) must each have a non-TTY fallback with a **defined `--json`/`--quiet` contract** so `up`/`status` are scriptable in CI. Detect TTY via `golang.org/x/term`.

### 7.10 Shared-service governance
One instance for many tenants needs explicit policy: tenant name collisions (two projects want db `app`), redis DB-index exhaustion beyond 16, MinIO global bucket-name uniqueness, and **version conflicts** — a single shared Postgres cannot serve project A pinned to 16 and project B pinned to 15. Policy: **one shared instance per `(engine, majorVersion)` pair**, keyed in the ledger; fall back to a dedicated instance when a project needs a unique version.

---

## 8. WSL2 is a first-class *runtime* target

WSL2 is the weakest path across nearly every dimension simultaneously, and it's a primary target. A single WSL-detection utility (`uname` contains `microsoft` / `/proc` check) feeds capability branches; `doctor` exercises the *real* branch logic, not docs. Hard rules: state/cache live on the **Linux** side; **refuse `/mnt/*` working dirs**; pin/select the Docker **context** loudly; keyring is optional everywhere with in-memory fallback; trust install drives **`certutil.exe`** interop for the Windows store (browsers run on Windows, not in the distro).

---

## 9. Build-vs-buy summary

Build only the genuinely novel layer (shared-services orchestration, workspace model, the generation pipeline). **Buy/reuse** where mature tools dominate: the `docker compose` CLI (don't embed `docker/compose/v2` — it drags in all of BuildKit), the system `git` binary, `mkcert` for trust, `compose-go` for compose validation, Caddy/cloudflared as managed containers. Re-implementing any of these worse is a primary failure mode.
