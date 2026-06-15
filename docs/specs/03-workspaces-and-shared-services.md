# Spec 03 — Workspaces & shared services (the differentiator)

**Module:** `internal/workspace` (+ `internal/docker`, `internal/provision`) · **Milestone:** M2 · **Effort:** ~9w

## Purpose
Run shared infrastructure (Postgres/Redis/MinIO/proxy) **once** and let many independent project stacks attach to it — with per-project data isolation — instead of a duplicate stack per repo. This is the reason the tool exists. **Prototype this before writing templates** — it validates the whole premise.

## Topology
- **One tool-owned external bridge network** `devstack_shared` (name **pinned** via `name:`, else Compose prefixes it per-project). Created idempotently by devstack (`docker network create … || true`), **never** by Compose. Compose won't remove an external network on `down` — devstack owns create *and* cleanup (cleanup only on `workspace destroy`).
- **Shared stack** = its own Compose project `devstack-shared` (`~/.devstack/shared/docker-compose.yaml`): postgres, redis, minio, mailpit, the reverse proxy, optional cloudflared. Each shared service joins `devstack_shared` with a stable alias.
- **Each project** = its own Compose project `devstack-<name>`; references `devstack_shared` as `external: true`; only the containers needing shared infra join it. Project-internal services stay on a per-project network (so two projects can both have an internal `postgres` without DNS collision).
- **Shared services reached by alias DNS** (`shared-postgres`, `shared-redis`, `shared-minio`), not bare service name. A generate-time **lint** enforces no project internal service shadows a `shared-*` alias.

## Build/run split (the key call)
- **Build** compose programmatically + validate with `compose-go/v2` (see [spec 02](02-templating-and-generation.md)).
- **Run** verbs by shelling out to **`docker compose`** (require **v2.20+**) with explicit `-p <normalized-name>` + a **tool-owned label namespace**.
- **Engine SDK (`moby/moby/client`) read-only** for: idempotent network ensure (`NetworkInspect`→`NetworkCreate`), label-filtered `ContainerList`, health/state polling, ref-count reconciliation. **Never** run containers via the SDK — Compose owns lifecycle; SDK is plumbing. Avoids ownership drift.

## Reference counting & lifecycle
State in the SQLite ledger ([spec 08](08-state-locking-and-lifecycle.md)): `shared_service(name, engine, major_version, status)`, `service_ref(project, service, shared_service)`. All mutations **inside the `gofrs/flock` lock**.

- **`up <project>`:** resolve declared `uses:` → ensure network → for each needed shared service insert a ref row + `docker compose -p devstack-shared up -d <those services>` (Compose is idempotent) → provision per-project data → bring project stack up. Ref count = COUNT of ref rows.
- **`down <project>`:** delete that project's ref rows → for each shared service now at 0 refs, **optionally** `stop` it, gated by `shared.autostop` (**default false** — dev DBs are cheap to keep warm; stop/start churn is annoying). `shared gc` stops all zero-ref services on demand.
- **Self-healing reconcile on every command:** cross-check ref rows against live containers (SDK `ContainerList` by tool label) and running project stacks; prune orphan refs. The count is *derived from reality*, not a trusted counter.
- **`shared status`** reports each shared service, its state, and the referencing projects.

## Per-project data isolation
**Provision-on-demand**, not `initdb.d` (those run only on first-init empty PGDATA). After the shared Postgres is healthy, connect as superuser via **`jackc/pgx/v5`** and run **idempotent, existence-guarded** SQL:
```sql
-- guarded: SELECT 1 FROM pg_roles WHERE rolname=$1 ; then CREATE ROLE ...
-- guarded: SELECT 1 FROM pg_database WHERE datname=$1 ; then CREATE DATABASE ... OWNER ...
-- REVOKE on PUBLIC; GRANT so each role sees only its own DB
```
Generated credentials are stored via the secrets provider and injected as env. Redis: per-project logical DB index (0–15) or key prefix beyond 16. MinIO: per-project bucket + scoped access key (`mc`/`madmin-go`). An **ownership ledger** ties each DB/role/bucket to a project → a `db gc` reaper reclaims orphans (with explicit data-loss confirmation) when a project is removed.

## Version-conflict policy
A single shared Postgres **cannot** serve project A@16 and project B@15. Policy: **one shared instance per `(engine, majorVersion)` pair**, keyed in the ledger (`shared-postgres-16`, `shared-postgres-15`); ref-counting/provisioning keyed accordingly. Fall back to a dedicated instance only when a project pins a version no one else uses.

## Port allocation
- **Default: shared services publish NO host port** (reached by DNS over the shared net). Host ports only when the dev wants host reachability (psql, a GUI).
- Allocation happens **inside the lock**, from a configurable base range (app `13000+`, DB GUI `15432+`), persisted for stability.
- ⚠️ Bind-test (`net.Listen("tcp","127.0.0.1:port")`) is **advisory only** (TOCTOU) — eliminate the inter-invocation race by allocating inside the lock and persisting immediately; **union** the bind-test with ports already published by Docker (SDK `ContainerList` port bindings) because on Docker Desktop a host bind-test doesn't reflect the VM proxy. See [spec 08](08-state-locking-and-lifecycle.md).

## Verified constraints / gotchas
- **`com.docker.compose.project` label** is the *normalized* name (`-p` > `COMPOSE_PROJECT_NAME` > `name:` > dir basename) — compute it exactly or filter on **your own label** instead, or enumeration silently no-ops. Set `ListOptions.All=true` (else exited containers invisible) and **exclude `com.docker.compose.oneoff=true`** (else `compose run`/`exec` inflate counts). A service can map to N containers (scale) — count containers.
- **Daemon scope:** the SDK sees only the context you connect to; on WSL2 (Desktop daemon vs in-distro dockerd) pin/select the context loudly, and key the ledger by context.
- **PG18+ PGDATA path** moved to `/var/lib/postgresql` — mounting the old `/var/lib/postgresql/data` silently loses persistence. The `initdb` marker is a non-empty `PG_VERSION`, so a volume with only `lost+found` still counts as fresh.
- **External-network DNS** works only on user-defined networks; `network_mode: host` breaks name resolution; corporate DNS search domains + `ndots>0` can append a suffix and break short-name resolution — use exact aliases.
- **Bridge IPs are not host-routable on Docker Desktop** (macOS/WSL2) — only the DNS-over-network path is consistent across all three OSes.
- **Never recreate a stateful shared service** (to apply a new compose field, or during self-update re-normalization) without explicit confirmation — it drops every project's live DB connections and risks the PG18 volume footgun.

## Acceptance criteria
- [ ] Two projects both `uses: workspace.shared.postgres` → exactly **one** Postgres container; each gets an isolated role+db and cannot see the other's data.
- [ ] `up api` then `up web` → ref count on shared-postgres is 2; `down web` → 1; `down api` → 0 (and, with autostop on, it stops).
- [ ] `kill -9` during `up`, then any next command → reconcile prunes the stale ref and reports a consistent `shared status`.
- [ ] Project A@postgres16 + project B@postgres15 → two shared instances `shared-postgres-16/15`, each ref-counted independently.
- [ ] Removing a project + `db gc` → its orphaned db/role is reclaimed only after explicit confirmation.
- [ ] A project with an internal service named `postgres` joining the shared net → generate-time lint error (must alias).
- [ ] Two concurrent `up` invocations (two terminals) → no `database is locked`, no duplicate port assignment, no `role already exists` crash (lock + idempotent SQL).

## Dependencies / consumers
Consumes `internal/template` (env injection + external-network stanza), `internal/secrets` (store/inject generated creds), `internal/state` + `internal/lock` (the spine), `internal/docker`. The reverse proxy *lives in* this shared stack but its config/HTTPS/tunnel logic belongs to [spec 05](05-networking.md). A thinner v1 (no autostop, Postgres-only provisioning, manual gc) lands in ~5–6w; full is ~9w.

## Open questions
[Q-DAEMON](../OPEN-QUESTIONS.md) (autostop needs a daemon to be automatic), [Q-RUNTIME](../OPEN-QUESTIONS.md) (Docker-only).
