# Spec 21 — Remote / cloud shared-services backend

**Module:** `internal/workspace` + `internal/docker` (backend abstraction) · **Milestone:** later (exploratory) · **Effort:** ~8w (feature #13)

## Purpose
Generalize the shared stack so it can run on a **remote Docker host** (`DOCKER_HOST` / an SSH `docker context`) or a team "shared dev cluster" — one warm, seeded Postgres for a whole team, zero local DB containers. The ref-counting ([spec 03](03-workspaces-and-shared-services.md)), per-project provisioning, and DNS/alias model are **backend-agnostic**; this feature swaps *where* containers run, not the model. The devbox/Codespaces-class frontier — kept an explicit **design sketch, not a v1 commitment**.

> **Builds on v1, deferred because:** the seam already exists — the ledger is keyed by Docker context ([spec 08](08-state-locking-and-lifecycle.md)) and lifecycle drives `docker compose` over whatever context is active, so a remote context is "just another key". It is deferred because v1's concurrency guarantee — a **local** `gofrs/flock` ([DECISIONS D7](../DECISIONS.md)) — does **not** serialize across machines, and a shared cluster turns the single-user governance of ARCHITECTURE §7.10 into genuine multi-tenant governance. Both are hard and unproven; do not ship them until the local model is rock-solid.

## What changes vs. what is invariant
| Invariant (reuse unchanged) | Changes (this spec) |
|---|---|
| Ref-counting + self-healing reconcile ([spec 03](03-workspaces-and-shared-services.md)) | The **locking primitive** behind those ref rows (local flock → distributed/coordinated) |
| Per-`(engine,major)` shared instance, provision-on-demand SQL ([spec 03](03-workspaces-and-shared-services.md)) | Per-**user** role isolation on one team Postgres (not just per-project) |
| `${ref}` resolution against the workspace graph ([spec 02](02-templating-and-generation.md)) | DNS/alias scope — `shared-postgres` now resolves on the **remote** shared network |
| Compose owns lifecycle; Engine SDK read-only ([ARCHITECTURE §4](../ARCHITECTURE.md)) | The Docker **endpoint** the CLI + SDK target (local socket → SSH/TCP) |
| Stateless CLI, no daemon (v1) | Whether a **server-side coordinator** is needed for the lock (the central open problem) |

## The backend abstraction (`internal/docker`)
Introduce a `Backend` seam so `internal/workspace` is written against capabilities, not a socket:
```go
type Backend interface {
    Context() ContextKey                 // ledger key (already exists)
    ComposeEnv() []string                // DOCKER_HOST/DOCKER_CONTEXT for `docker compose` exec
    SDK() docker.Client                  // read-only moby/moby client, bound to this endpoint
    Reachability() Reachability          // HostRoutable | DNSOnly | ViaProxy — drives port/URL strategy
    Lock() DistLock                      // local flock for `Local`; coordinator for `Remote`
}
```
- **`Local`** wraps today's behavior verbatim (default socket, flock, host-published ports where Reachability allows).
- **`Remote`** carries an SSH/TCP context, a `DistLock`, and `Reachability=ViaProxy` (a remote bridge is not host-routable — §Network reachability).
- Lifecycle still **shells out to `docker compose`** with `DOCKER_HOST`/`DOCKER_CONTEXT` injected via `exec.Cmd.Env`; the SDK stays **read-only** (network ensure, label-filtered `ContainerList`, health poll, reconcile). **Never** run/exec containers via the SDK — Compose owns lifecycle, remote or not.
- The ledger gains nothing structurally: `docker_context(id, name, endpoint)` already keys every table by context; a remote endpoint is a new row. Multi-user adds a `tenant`/`user` dimension to `service_ref`/`provisioned` (see governance).

## The central unsolved problem: distributed locking
v1's `gofrs/flock` is a **local advisory lock on a file under `XDG_RUNTIME_DIR`** — it cannot serialize two developers on two machines mutating the **same remote** ledger rows / provisioning / port allocation. This is *the* reason the feature is exploratory. Candidate `DistLock` implementations, decision-first:
1. **Postgres advisory lock on the shared DB itself** (`pg_advisory_lock`/`pg_try_advisory_lock`). Zero new infra — the team Postgres we already run *is* the coordinator. Session-scoped (auto-released on disconnect → crash-safe), works over the same `pgx/v5` already used for provisioning. **Recommended starting point.** Caveat: only guards operations that can reach that DB; a pgbouncer in *transaction* pooling mode silently breaks session advisory locks (must use a direct/session connection).
2. **A thin server-side coordinator** (a small daemon co-located with the cluster owning the ledger + lock + port registry). Cleanest semantics, but it **introduces a daemon** — contradicting the stateless-CLI model ([Q-DAEMON](../OPEN-QUESTIONS.md)); call it out, do not adopt silently.
3. **Object-store / lease lock** (S3 conditional-put, etcd/Consul lease). General but new infra + new dependency surface; over-built for a dev tool.

Where the ledger itself lives is coupled: a per-developer local SQLite ledger **cannot** be the source of truth for a shared cluster (two laptops, two truths). Either (a) the shared **Postgres holds the authoritative ledger** (reconcilable, single writer via option 1), or (b) the coordinator (option 2) owns it. Local SQLite degrades to a **cache** of the remote truth. This is the deepest design fork and stays open.

## Per-user isolation (governance extends, does not change shape)
ARCHITECTURE §7.10 governance (name collisions, db-index exhaustion, version conflicts) extends from *many-projects-one-user* to *many-users-one-cluster*. The naive "name the role after the project" is **not** isolation on a shared cluster — two developers' `app` collide and can read each other's data.
- Provisioned identity becomes `(tenant=user, project)` — role `u_<user>_<project>`, db `<user>_<project>`, MinIO bucket `<user>-<project>-<rand>`, redis logical DB allocated per-tenant (16-index exhaustion is now realistic → key-prefix fallback or per-user Redis instance).
- **Real role separation, not naming:** `REVOKE ALL ON DATABASE … FROM PUBLIC`, per-role `GRANT`, and `pg_database`/`pg_roles` existence guards exactly as [spec 03](03-workspaces-and-shared-services.md) — but the superuser bootstrap credential is now a **shared-cluster admin secret** ([spec 04](04-secrets.md)), and a tenant must never be handed it. The `provisioned` ledger gains a tenant column so the `db gc` reaper reclaims only the leaving user's artifacts.
- **Data residency / auth:** shared team data lives off the developer's laptop — note (do not solve here) that auth to the cluster, network ACLs, and where seeded PII may legally sit are real concerns a team adopting this must own.

## Network reachability
A **remote bridge network is not host-routable** from the developer's machine (worse than the Docker Desktop VM case in [spec 03](03-workspaces-and-shared-services.md) — it is on another host entirely). Consequences:
- **No host-published ports** on the remote shared services for laptop reachability — port allocation in the ledger governs *cluster-side* ports only. Host access (psql, a GUI) goes through the **proxy/tunnel** ([spec 05](05-networking.md)): an SSH local-forward to the remote service, or the cloudflared path, gated and confirmed exactly as the tunnel is.
- **DNS over the remote shared network** is unchanged for *container-to-container* traffic on the cluster (`shared-postgres` alias resolves there). The developer's **local** project stacks, if any, cannot resolve those aliases — so the realistic topology is **everything runs remote** (project stacks too), or a tunnel bridges a local stack to remote shared services. The all-remote topology is the clean one; the split topology is a footgun to document.
- The `[]Route` single-source-of-truth ([spec 05](05-networking.md)) still renders both proxy labels and tunnel ingress, now pointing at remote upstreams.

## Verified constraints / gotchas
- **SSH `docker context` inherits the user's SSH setup** (`~/.ssh/config`, agent, `ProxyJump`) for free — the same value prop as system-git ([DECISIONS D9](../DECISIONS.md)) — **but adds round-trip latency to every SDK call**; the reconcile loop's many `ContainerList`/`NetworkInspect` calls become the slow path. Batch and cache aggressively; a chatty reconcile that was instant locally can take seconds remotely.
- **A local flock cannot serialize across machines** — the v1 concurrency guarantee silently *breaks* on a shared backend. Two `up`s from two laptops would race ref rows / `CREATE ROLE` / port alloc with no protection. The `DistLock` is **not** optional polish; it is the gate on correctness (mirrors why the flock was M0-first, [spec 08](08-state-locking-and-lifecycle.md)).
- **Per-user isolation on a shared Postgres needs real role separation, not naming** — `REVOKE … FROM PUBLIC` + per-role grants, and never leak the cluster superuser to a tenant.
- **`docker compose` over SSH** requires Docker CLI ≥ the version that supports `ssh://` contexts and a compatible **remote** daemon; `compose v2.20+` must be present **on the remote** (the local plugin still drives, but build/up execute remotely). Preflight must probe the *remote* versions, not the local ones.
- **`pg_advisory_lock` is session-scoped**: a transaction-pooling pgbouncer in front of the cluster Postgres silently defeats it — take the lock on a direct session connection. Advisory-lock keys are `bigint`; hash the lock subject deterministically.
- **Remote builds ship the build context over the wire** — large `build:` contexts that were free locally now upload on every `--no-cache`; the SHA-256 selective-rebuild ([spec 02](02-templating-and-generation.md)) matters more, not less.
- **Stateful services on a *shared* host are riskier**: "never recreate a stateful shared service without confirmation" ([spec 03](03-workspaces-and-shared-services.md)) is now a multi-user blast radius — a recreate drops **every teammate's** connections, not just yours.
- This is the **devbox/Codespaces-class frontier** — keep it a design sketch; it wants a daemon ([Q-DAEMON](../OPEN-QUESTIONS.md)), depends on platform priority ([Q-PLATFORM](../OPEN-QUESTIONS.md)) and runtime scope ([Q-RUNTIME](../OPEN-QUESTIONS.md)), none settled for v1.

## Acceptance criteria
- [ ] `internal/workspace` runs unchanged against a `Local` and a `Remote` backend selected purely by Docker context — no workspace-logic fork (the seam holds).
- [ ] `up` against an SSH `docker context` brings the shared stack + a project stack up **on the remote host**; `shared status` reflects remote container state via the read-only SDK.
- [ ] Two `up` invocations from **two machines** against the same remote backend serialize on the `DistLock` — no duplicate port assignment, no `role already exists`, no double-counted ref rows.
- [ ] Two developers each `uses: shared.postgres` on one team cluster → each gets a role+db that **cannot** read the other's data (verified by a cross-tenant `SELECT` denial), and neither holds the cluster superuser.
- [ ] No remote shared service publishes a host port for laptop reachability; psql access succeeds only via the proxy/tunnel path ([spec 05](05-networking.md)).
- [ ] Preflight reports the **remote** docker/compose/postgres versions and fails clearly if the remote `compose` is < v2.20.
- [ ] A `kill -9` mid-`up` on one machine → another machine's next command reconciles the shared ledger to a consistent state (no orphan ref/role for the dead session).

## Dependencies / consumers
Extends `internal/workspace` (ref/provision/port logic, reused) and `internal/docker` (the new `Backend` seam); leans on `internal/provision` (per-tenant pgx SQL), `internal/state` (context-keyed ledger, now possibly remote-authoritative), `internal/lock` (generalized to `DistLock`), and `internal/proxy`/`internal/tunnel` ([spec 05](05-networking.md)) for the only viable host-reachability path. Builds on the shared-services model ([spec 03](03-workspaces-and-shared-services.md)), the concurrency spine ([spec 08](08-state-locking-and-lifecycle.md)), and the saga ([spec 09](09-orchestration-and-onboarding.md)). **Effort:** a **thin v2 sketch** (SSH-context single-user remote, local flock still, all-remote topology, manual provisioning) is ~3–4w and proves the seam; the **full** multi-tenant cluster (distributed lock, per-user isolation, remote-authoritative ledger) is the ~8w feature — and gated on a daemon decision.

## Open questions
[Q-DAEMON](../OPEN-QUESTIONS.md) (a server-side coordinator is the cleanest `DistLock` but reintroduces a daemon), [Q-PLATFORM](../OPEN-QUESTIONS.md), [Q-RUNTIME](../OPEN-QUESTIONS.md). New:
- **Q-REMOTE-LOCK** — distributed-lock + ledger-authority strategy: Postgres advisory lock with the cluster DB as the source of truth (recommended start), a dedicated coordinator daemon, or an external lease store? Local SQLite degrades to a cache regardless — confirm.
- **Q-REMOTE-TENANT** — tenant identity for per-user isolation: OS username, an explicit `--as`/config-declared team identity, or cluster-auth identity? Determines role/db/bucket naming and `db gc` ownership on a shared cluster.
