# Spec 08 — State, locking & lifecycle (the concurrency spine)

**Modules:** `internal/state`, `internal/lock`, `internal/orchestrate`, `internal/hooks`, `internal/doctor` · **Milestone:** M0 (lock+state) → M2/M6 · **Effort:** spread across M0/M2/M6

> **Build the lock + state store FIRST (M0).** Everything that mutates shared state goes through them from the first commit. Retrofitting concurrency safety is the single most expensive mistake available here. This spec addresses the **#1 architectural risk**: concurrent invocations corrupting machine-global shared state.

## Purpose
Own the machine-global ledger, serialize all mutations across processes, and make multi-phase operations crash-safe and self-healing.

## State store (`internal/state`)
- **`modernc.org/sqlite`** (pure-Go, preserves the static binary), under the XDG data/state dir, **keyed by Docker context** (the value of the active `docker context` / `DOCKER_HOST`) so WSL2's two daemons (Desktop vs in-distro `dockerd`) don't share a ledger and mis-count.
- Opened with **WAL + `busy_timeout=5s` + `foreign_keys=ON`**.
- **Versioned migrations table**, forward-only within a major, **backup `state.db` before migrating**.
- **Rolling event log** — every shared-service start/stop and ref change is recorded with a reason + timestamp, so "my DB disappeared" becomes answerable (`service X stopped because project Y went down at T`).
- `doctor --rebuild-state` reconstructs the entire ledger from **live Docker labels + on-disk config** (the recovery path when `state.db` is lost/corrupted — likely on 9p/WSL2 or a `kill -9` mid-write).

### Tables (sketch)
```
docker_context(id, name, endpoint)
shared_service(ctx, name, engine, major_version, status, started_at)
service_ref(ctx, project, service, shared_service)          -- ref count = COUNT(*)
port_alloc(ctx, owner, purpose, port UNIQUE)
provisioned(ctx, project, kind, name, created_at)           -- db/role/bucket ownership ledger
hook_run(ctx, project, hook, scope_key)                     -- idempotency for firstRun etc.
event_log(ctx, ts, kind, subject, reason)
schema_version(version)
```

## Cross-process lock (`internal/lock`)
- A coarse **`gofrs/flock`** advisory lock on a file under `XDG_RUNTIME_DIR` (fallback `XDG_STATE_HOME`), taken around **every** operation that mutates the ledger or the shared stack. **Single writer; reads are lock-free snapshots.**
- Covers the four races that otherwise happen daily: network-ensure TOCTOU (inspect→create), port allocation, ref-count rows, `CREATE ROLE`.
- `doctor` detects 9p/networked state dirs (where SQLite/flock locking is unreliable) and **warns**; refuse `/mnt/*` working dirs entirely.

## Port allocation (inside the lock)
- Allocate from a configurable base range, **persist immediately** (eliminates the inter-invocation TOCTOU).
- Bind-test (`net.Listen 127.0.0.1:port`) is **advisory only**; **union** it with ports already published by Docker (SDK `ContainerList` port bindings) so Docker-Desktop-VM-published ports are detected (a host bind-test doesn't reflect the VM proxy).
- Prefer **not publishing** host ports at all (DNS-over-shared-network default) — allocate only on explicit host-reachability requests.

## The `up` saga (`internal/orchestrate`)
`up` runs ~8 phases: clone → network ensure → shared services → provision DB/bucket → resolve secrets → CA/trust → generate → `compose up`. A failure mid-way (e.g. provisioning fails after the shared stack started and ref rows were inserted) must not leave a half-up workspace + lying ledger.
- Model as a **saga**: each phase is **named, idempotent, and resumable**, with durable phase-state and a **compensating action** on failure (e.g. roll back the ref row if the project stack never came up).
- Re-running `up` skips satisfied phases (powers one-command onboarding, feature #1).
- Live **bubbletea checklist** when TTY; plain/`--json` fallback otherwise.
- On unexplained inconsistency, `doctor --rebuild-state` + `shared doctor` repair.

## Lifecycle hooks (`internal/hooks`)
- Declarative `postUp` / `preDown` / `firstRun` / `postPull`, run on the host or via `compose exec`, with the same `${ref}`/secret interpolation.
- **`firstRun` idempotency tracked in the state DB** (keyed per provisioned data volume) so it survives restarts — this is what correctly replaces Postgres `initdb.d` (which never re-runs on an existing shared volume). Canonical uses: DB migrations, seeding, app-key generation, dependency install.

## `doctor` capability matrix (`internal/doctor`)
Runs the **real branch logic** (not docs) for: docker daemon reachable + **correct context**, `docker compose` ≥ v2.20, git ≥ 2.30, disk for volumes, shared-network health, port conflicts, CA trust state (host + Firefox/NSS + Windows-on-WSL2), secrets-provider reachability, **stale ref rows vs live containers**, `*.localhost`/resolver per platform, **9p state-dir warning**, keyring presence. Every failure → one-line remediation; `--fix` where safe.

## Teardown (`workspace destroy` / `uninstall`)
Reverse **all** machine-global artifacts, with explicit data-loss confirmation:
external network · shared containers + **named volumes (Postgres data!)** · the SQLite ledger · **root CA from host + Firefox + Windows stores** · alias symlinks · cloudflared creds · keyring entries · template cache. Orphaning a CA (a security artifact) or dangling symlinks is a defect.

## Verified constraints / gotchas
- pure-Go SQLite is **not** immune to `database is locked` under concurrent writers — WAL + `busy_timeout` + the flock are all required together.
- **Reconcile correctness** depends on the label-filter rules ([spec 03](03-workspaces-and-shared-services.md)): `All=true`, exclude `oneoff=true`, correct normalized project name (or filter on your own label), correct Docker context.
- A `kill -9` mid-write is the expected failure on WSL2/9p — `doctor --rebuild-state` is the safety net, not an afterthought.

## Acceptance criteria
- [ ] Two concurrent `up` invocations serialize on the flock — no `database is locked`, no duplicate port, no `role already exists`.
- [ ] `kill -9` during phase 4 of `up` → next command reconciles to a consistent ledger; no orphan ref rows; `shared status` is truthful.
- [ ] Deleting `state.db` then `doctor --rebuild-state` → ledger reconstructed from live Docker labels + config.
- [ ] A version-change self-update runs the state migration (with backup) on next launch; no schema mismatch.
- [ ] `firstRun` hook runs exactly once per provisioned volume, even across restarts.
- [ ] `workspace destroy` leaves no network, volume, CA (any store), symlink, keyring entry, or cache behind.
- [ ] `doctor` on a `/mnt/c` working dir refuses with a clear message; on a 9p state dir, warns.

## Dependencies / consumers
The spine consumed by `internal/workspace`, `internal/provision`, `internal/secrets` (cache), and `internal/cli`. Owns the durable truth that makes the shared-services differentiator safe under real-world concurrent use.
