# Spec 27 — The data-plane resource layer (resources inside shared services)

**Module:** `internal/resource` (+ `internal/provision`, `internal/state`, `internal/lock`, `internal/generate`) · **Milestone:** v2 · **Effort:** ~6w (feature #27)

## Purpose
The shared stack already hosts more than *containers* — it hosts **per-project resources that live inside those containers**: databases, roles/users, S3 buckets and object-lifecycle policies, message queues, streams, topics. Today exactly one such resource is modelled: the per-project Postgres role+database that `provisionPhase` ([`internal/orchestrate/provision.go`](../../internal/orchestrate/provision.go) lines 132–207) creates on the warm shared engine. This spec **generalizes provision-on-demand into a first-class resource layer** — one `Resource` model, one `Provisioner` interface family, one mutation pattern — so every engine's in-container resources are provisioned, tracked, surfaced, and reclaimed the same tenant-safe way. It is the architecture other data-plane specs build on: spec 28 defines the resource **templates**, spec 29 the ergonomic **per-domain verbs** (`devstack db …`, `devstack s3 …`, `devstack queue …`). It is **post-GA (v2)** — it sits on top of the M2 substrate (the `provisioned` ownership ledger, the flock spine, the up-time port overlay) and the just-landed secrets `Pusher` (feature #16), none of which existed before M2/M4.

> **What this is not.** This is not a new *engine* (that is shared-service lifecycle, [spec 03](03-workspaces-and-shared-services.md)) and not a data *workflow* (snapshot/restore, [spec 15](15-db-snapshot-restore.md)). It is the layer **between** them: the resources a project owns *within* an already-running shared engine. Spec 03 ref-counts the Postgres container; spec 27 owns the `<proj>_db` inside it; spec 15 dumps and restores that db's bytes.

## The resource model
A **Resource** is the tuple `(engine, kind, name, owner-project, attributes)`:

- **engine** — the shared template's `provides:` capability (`postgres`/`redis`/`minio`/…), the same value the ledger already stores in the `engine` column and that `internal/scaffold/build.go` (line ~53) requires a shared template to declare (it rejects any service whose template has an empty `provides:`). A resource always belongs to exactly one shared *instance* keyed `(engine, major)`.
- **kind** — `database | role | user | bucket | lifecycle | queue | stream | topic | redis_index | …`. This is the `provisioned.kind` column, which is **already free-text** (currently `database|role|bucket|redis_index`, the kinds `provisionPhase` and `AllocateRedisIndex` record today), so new kinds need **no migration** ([spec 08](08-state-locking-and-lifecycle.md), migrations are append-only/forward-only).
- **name** — the engine-level identifier (`acme_db`, `acme` role, `acme-uploads` bucket, `acme.events` stream). Derived from the owner project by default (tenant namespacing, below); explicit names are allowed but still recorded against one owner.
- **owner-project** — the `provisioned.project` column. **Tenant-scoping is the load-bearing invariant**: every provisioner call is parameterized by owner, and a project's resources are namespaced by project so one project's commands can **never** read or mutate another's — the same isolation that makes the shared stack the differentiator (`provisionPhase` already creates `<proj>_db OWNER <proj>` with `REVOKE … FROM PUBLIC`).
- **attributes** — the connection-surfacing facts a consumer needs (`host`, `port`, `user`, `database`, `bucket`, `subject`, …) plus a credential reference. Non-secret attrs resolve through the `${ref}` graph resolver; **secret** attrs (`password`/`secretkey`/`secret`/`token`) are *rejected* as inline refs and flow only via `env.import` (the spec-04 coupling — see `secretAttrs` in [`internal/generate/resolver.go`](../../internal/generate/resolver.go)).

The ledger row is unchanged: `provisioned(ctx, project, kind, name, created_at)`, keyed by Docker context like the rest of the ledger. Attributes are **derived, not stored** (re-resolved from the template `exports:` + the owner project on read), so there is exactly one source of truth and nothing to drift.

## Decisions
- **One ledger, one ownership table for everything.** Reuse `provisioned(ctx, project, kind, name, created_at)` verbatim. `kind` is free-text (today `database|role|bucket|redis_index`) → `user|bucket|lifecycle|queue|stream|topic` add zero migrations. CRUD stays `RecordProvisioned` / `ProvisionedFor` / `AllProvisioned` / `OrphanedProvisioned` / `RemoveProvisionedForProject` ([`internal/state/ledger.go`](../../internal/state/ledger.go) lines 290–341).
- **A `Provisioner` per engine, behind a small mockable seam — the `provision.Conn` pattern, repeated.** `provision.Postgres{}.EnsureProject` already proves the shape (existence-guarded, idempotent, interface-injected for tests). New engines get their own minimal seam; the orchestrator only ever sees the `Provisioner` interface.
- **External engine tools are shelled out behind `internal/` interfaces; pure-Go SDKs are preferred where they keep the single static binary.** `pgx/v5` is the only live runtime SDK and stays in-process. S3/MinIO use the pure-Go `aws-sdk-go-v2` S3 client where possible, `mc`/`madmin` only for admin IAM; NATS/Kafka prefer pure-Go clients (`nats.go`, `franz-go`), shelling `nats`/`rpk` only when the SDK can't express the op. Every external binary is an `info`-level `doctor` probe (absence degrades that engine's resource verbs, never blocks `up`) — same posture as `mkcert`/`cloudflared`/`pg_dump` ([DECISIONS D11/D12](../DECISIONS.md), [spec 15](15-db-snapshot-restore.md)).
- **Two consumption paths, one substrate.** Imperative `resource …` commands are **standalone cobra commands**, not saga phases; the declarative `resources:` block is provisioned by **one** new `up` saga phase. Both call the *same* provisioners against the *same* ledger under the *same* flock. There is no second code path.
- **Imperative commands mirror `provisionPhase` exactly.** `lock.WithLock(ctx, LockPath, …)` → resolve the shared instance + a 127.0.0.1 up-time host-port overlay → provisioner call → `RecordProvisioned` + `DB.LogEvent`. Reads (`list`/`show`) are lock-free snapshots.
- **Host reachability is a per-engine overlay, never a change to deterministic `generate` output.** Generalize `writeProvisionOverlay`: a per-engine `(purpose, portBase, containerPort)` registry, each publishing `127.0.0.1:<ledgerPort>:<containerPort>` into `.devstack/shared/compose.provision.yaml`. The golden-asserted generate output stays byte-identical; \"no host ports by default\" still holds. (Today's Postgres overlay uses purpose `pg-provision`, base `45432`, container `5432`.)
- **Per-resource credential policy.** `predictable` (the Postgres dev-cred model: password = project name, nothing secret stored, attrs reach containers via the `env.import` resolver) **or** `generated` (random value via a `crypto/rand` helper in `internal/secrets` → pushed to a provider via the `Pusher`, injected as a valueless env key). Default is `predictable` for loopback dev creds; `generated` is opt-in per resource.
- **Never-recreate-a-stateful guard extends to resources.** A provisioner may create/drop a *resource* (database/bucket/queue) but **never** the shared container or its volume. Teardown is always explicit + confirmed.

## CLI surface
Generic resource verbs (the escape hatch + the substrate spec 29's per-domain verbs delegate to). All respect `--json`/`--quiet` and the global flock. **These are net-new commands** (no `resource` group exists today; the only data-plane CLI today is the `provisionPhase` inside `up`).

```
devstack resource list   [--project P] [--engine E] [--kind K] [--json]
devstack resource show   <name> [--engine E] [--project P] [--json] [--show-secrets]
devstack resource create <engine> <kind> <name> [--project P] [--param k=v ...] [--credentials predictable|generated]
devstack resource rm     <name> [--engine E] [--project P] [--purge-data] [--yes]
devstack resource gc     [--project P] [--engine E] [--kind K] [--prune] [--yes] [--json]
```

- Default `--project` is the current workspace project; `resource list`/`show` default to all engines.
- `resource create` is the engine-generic primitive; spec 29 adds ergonomic aliases (`devstack db create`, `devstack s3 mb`, `devstack queue add`) that resolve to the same provisioners.
- `resource rm --purge-data` calls `Provisioner.Drop` (destructive → TTY confirm or `--yes`); without `--purge-data` it only removes the ledger row (un-tracks, leaves bytes).
- `resource gc` is the **first CLI consumer** of `OrphanedProvisioned(active)` ([`ledger.go`](../../internal/state/ledger.go) line 313) — the query exists in the ledger today but nothing reaps it yet. (It is distinct from the existing `shared gc`, which ref-counts shared *containers*, not in-container resources.) The planned v1 `db gc` ([spec 15](15-db-snapshot-restore.md) / the unbuilt `db` domain group) becomes a `--engine postgres` alias of `resource gc` once both land.

## The Provisioner interface family
Today only `provision.Postgres` exists, over the minimal `provision.Conn` (`Exec`/`Exists`). Generalize to a `Provisioner` the orchestrator depends on, with each engine keeping its own tiny seam underneath:

```go
// internal/resource — the engine-agnostic contract the orchestrator + cobra
// commands depend on. One implementation per shared engine.
type Resource struct {
    Engine   string            // == template provides: ; ledger 'engine' column
    Kind     string            // database|role|user|bucket|lifecycle|queue|stream|topic
    Name     string            // engine-level identifier (tenant-namespaced)
    Owner    string            // owning project (provisioned.project)
    Params   map[string]any    // kind-specific knobs (e.g. lifecycle: expireDays)
    CredKind CredentialPolicy  // predictable | generated
}

type Attrs map[string]string  // host/port/user/database/bucket/subject + a cred ref

// Target is the resolved, host-reachable admin endpoint for ONE shared instance,
// produced by the overlay+ledger-port resolution (mirrors provision.DSN today).
type Target struct {
    Instance string // shared service name (e.g. \"postgres\")
    Host     string // \"127.0.0.1\"
    Port     int    // ledger-allocated published host port
    AdminEnv map[string]string // root creds from the instance's params
}

type Provisioner interface {
    Engine() string                                        // matches template provides:
    Kinds() []string                                       // the kinds it can create
    Ensure(ctx context.Context, t Target, r Resource) (Attrs, error) // idempotent, existence-guarded
    Drop(ctx context.Context, t Target, r Resource) error  // teardown for gc / --purge-data
    Preflight(ctx context.Context, t Target) error         // tool present + version-compatible
}
```

`Ensure` is the generalization of `Postgres.EnsureProject` ([`internal/provision/provision.go`](../../internal/provision/provision.go)): **existence-guarded and idempotent** (the same guard discipline — `SELECT 1 FROM pg_roles …` before `CREATE ROLE`, because `CREATE DATABASE`/`CREATE ROLE` are **not** idempotent, [DECISIONS D8](../DECISIONS.md)). The engine table:

| engine | kinds | underlying seam | tool | purpose / portBase / containerPort |
|---|---|---|---|---|
| `postgres` | `database`, `role`, `user` | `provision.Conn` (existing) | `pgx/v5` in-process | `pg-provision` / `45432` / `5432` |
| `redis` | `redis_index` (logical db), `acl_user` | `redis.Conn` (small seam) | pure-Go client | `redis-provision` / `46379` / `6379` |
| `minio` | `bucket`, `lifecycle`, `access_key` | `minio.Admin` | `aws-sdk-go-v2` S3 (data) + `mc`/`madmin` (IAM) | `minio-provision` / `49000` / `9000` |
| `localstack` | `bucket`, `queue`, `topic`, `stream`, `table` | `aws.Client` | `aws-sdk-go-v2` | `ls-provision` / `44566` / `4566` |
| `nats` | `stream`, `consumer`, `kv` | `nats.JS` | `nats.go` (pure-Go) | `nats-provision` / `44222` / `4222` |
| `kafka` | `topic`, `acl` | `kafka.Admin` | `franz-go` (pure-Go) or `rpk` | `kafka-provision` / `49092` / `9092` |

> **Net-new engines.** `postgres`/`redis`/`minio` ship as built-in engine templates today; `localstack`/`nats`/`kafka` have **zero** references anywhere in the tree and are 100% net-new (template + provisioner + doctor probe), landed in the Full scope below.

Postgres' new `user`/`grant` kinds extend `EnsureProject` (a project may own >1 login role); Redis keeps `AllocateRedisIndex` ([`ledger.go`](../../internal/state/ledger.go) line 341) as its `redis_index` provisioner — the one non-Postgres allocator that already ships. Each seam ships with a mock so race/unit tests run without a live engine (the `internal/docker` `MockClient` discipline; `provision`'s `mockConn` is the existing model).

## The declarative complement — `resources:` in `devstack.yaml`
A project declares the resources it needs additively; `up` provisions them. This is the idiomatic ARCHITECTURE §7.4 additive block — **deterministic config, idempotent runtime**.

```yaml
# devstack.yaml (additive; existing fields unchanged)
apiVersion: devstack/v1
kind: Project
name: acme
services:
  api:
    template: node.vite
    uses: [workspace.shared.postgres, workspace.shared.minio]
    env:
      import:
        - from: workspace.shared.postgres   # password flows here, NEVER inline ${ref}
          vars: [PGPASSWORD]
resources:
  - uses: workspace.shared.postgres
    kind: database
    name: acme                              # default: project name
    credentials: predictable                # dev cred; password = project name
  - uses: workspace.shared.minio
    kind: bucket
    name: acme-uploads
    credentials: generated                  # random key → Pusher → provider → env
  - uses: workspace.shared.minio
    kind: lifecycle
    name: acme-uploads
    params: { expireDays: 7 }
```

Config-model extension (validated by the existing custom cross-ref resolver, [`internal/config/validate.go`](../../internal/config/validate.go)):

```go
// internal/config/model.go — additive on Project
type ResourceDecl struct {
    Uses        string         `yaml:\"uses\" validate:\"required\"`   // workspace.shared.<name>
    Kind        string         `yaml:\"kind\" validate:\"required\"`   // database|user|bucket|lifecycle|queue|stream|topic
    Name        string         `yaml:\"name\"`                       // default: project name
    Params      map[string]any `yaml:\"params\"`
    Credentials string         `yaml:\"credentials\" validate:\"omitempty,oneof=predictable generated\"`
}
// Project gains: Resources []ResourceDecl `yaml:\"resources\" validate:\"dive\"`
```

The resolver checks each `uses:` points at a declared shared instance (the `ParseRef`/`RefShared` path already used for `Service.Uses`), that `kind` is one the target engine's provisioner supports, and that no two resources collide on `(engine, name)`. Lists **replace** on overlay merge unless `$merge: append` ([spec 02](02-templating-and-generation.md)) — so a profile/extends layer overrides the resource set wholesale by default.

## Behavior
**Imperative `resource create`** (the canonical pattern, identical in shape to `provisionPhase`):
1. **Resolve the model** — `buildManager` ([`internal/cli/shared.go`](../../internal/cli/shared.go)) assembles model + docker + ledger + lock + manager; resolve `<engine>` → the live shared instance `(engine, major)` and confirm it is healthy (resources can only be created on a running engine; if down, instruct `devstack up`).
2. **Ensure host reachability** — `Manager.FreeHostPort(ctx, SharedAlias(inst), <engine.purpose>, <engine.portBase>)` (self-locking, idempotent — returns the port a prior overlay already allocated). Write/refresh the per-engine overlay `127.0.0.1:<port>:<containerPort>` via the generalized `writeProvisionOverlay`, then `docker compose -p devstack-shared up -d <inst>` to apply it (idempotent; no recreate).
3. **Take the flock** — `lock.WithLock(ctx, LockPath, func() error { … })`. Inside it only:
4. **Provision** — build the `Target` (admin creds from the instance `Params`, e.g. `rootUser`/`rootPassword`), call `prov.Ensure(ctx, target, resource)`. Existence-guarded → safe to re-run.
5. **Record** — `DB.RecordProvisioned(project, kind, name)` and `DB.LogEvent(\"provision\", project, \"<kind> on \"+SharedAlias(inst))`. (For `credentials: generated`: generate a random value via `crypto/rand` → `Pusher.Push([]SecretEntry{…})` to the provider **before** recording, so the ledger never references a secret that isn't stored.)
6. **Release lock, surface attributes** — outside the lock, resolve and print the connection facts (`--json`/`--quiet`; secrets masked unless `--show-secrets`).

**Declarative at `up`** — a new `resources` saga phase, inserted **after** the existing `provision` phase (which it subsumes for non-Postgres engines) and before the per-project `compose-up`/`hooks` phases (the wired order today is `preflight → network → generate → secrets → trust → shared → provision → compose-up/hooks`, [`internal/orchestrate/up.go`](../../internal/orchestrate/up.go)):
7. Collect every active project's `resources:` entries whose target instance is in the up set; group by instance; resolve each instance's host port (step 2, shared with the existing provision phase so one overlay covers all).
8. Under the flock, for each entry call the engine's `Provisioner.Ensure` and `RecordProvisioned` + `LogEvent`, exactly as steps 4–5. Idempotent and existence-guarded → re-running `up` is a no-op on already-provisioned resources. Compensation is intentionally empty (provisioned resources are *data* and survive a failed `up`, like the Postgres phase).
9. The existing `provisionPhase` becomes the `postgres` provisioner under this generalized phase — its `provTargets`/`pgInstances` logic is the per-engine selector pattern the other engines copy.

**Reads** (`resource list`/`show`) are lock-free: `AllProvisioned` / `ProvisionedFor(project)`, joined with the live model to re-derive attributes; `--show-secrets` additionally fetches the stored value from the provider for diagnostics only.

## Credential / connection-string surfacing
Two policies, chosen per resource, never plaintext on disk either way:

- **`predictable` (dev-cred model).** The Postgres pattern: the credential is derived (`password = project name`), nothing secret is generated or stored, and the consuming container receives it through the `env.import` resolver as a valueless per-service env key filled at compose-up time. The non-secret attrs (`host` → `shared-<name>` DNS alias, `port` → template `defaultPort`, `user`/`database` → the **consumer project name**) resolve inline through `sharedAttr` in [`internal/generate/resolver.go`](../../internal/generate/resolver.go). Documented DSN: `postgres://<project>:<project>@shared-postgres:5432/<project>`.
- **`generated` (secret-via-Pusher).** Random value via a `crypto/rand` generator in `internal/secrets` (a **new** helper — the existing `GenerateAgeKey` in `keygen.go` is age/SOPS key material, not a credential generator) → pushed to the configured provider (`aws-sm`/`aws-ssm`/`infisical`, the `Pusher` interface `Push(ctx, []SecretEntry)` in [`internal/secrets/pusher.go`](../../internal/secrets/pusher.go), optionally SOPS-encrypted) → injected as a **valueless** `environment: [NAME]` key whose value arrives via `exec.Cmd.Env`. A CI test asserts no generated file contains the value (the §7.5 secrets coupling).

In **both** cases the `${ref}` resolver continues to **reject** secret attributes (`password`/`secretkey`/`secret`/`token` — the `secretAttrs` map in `resolver.go`) as inline refs — they exist only as `env.import` flows. `resource show --json` emits the non-secret attrs + a redacted credential marker; `--show-secrets` is the only way to print the value and is documented as diagnostics-only.

## Lifecycle & reclamation
- **`resource gc`** is the generic orphan reaper (the planned `db gc` will delegate to it): `OrphanedProvisioned(active)` ([`ledger.go`](../../internal/state/ledger.go) line 313) returns rows whose owner project is no longer in the workspace; the reaper groups by `(engine, kind)` and calls `Provisioner.Drop`, then `RemoveProvisionedForProject`. Destructive → explicit confirm or `--yes`, and a `gc.drop` `event_log` row per resource. Once the `db` domain group lands ([spec 15](15-db-snapshot-restore.md), currently unbuilt), `db gc` becomes a domain-scoped alias (`--engine postgres`). This is **distinct from today's `shared gc`**, which ref-counts shared *containers*, not in-container resources.
- **Never-recreate-stateful holds.** `Drop` removes the *database/bucket/queue/topic*, **never** the shared container or volume. A drop must not bounce the engine other tenants are live on (terminate only the dropped tenant's sessions, e.g. `pg_terminate_backend WHERE datname=<name>` before `DROP DATABASE`).
- **`workspace destroy --purge-data`** (a **new** flag). `workspace destroy` today is strictly data-preserving — it carries only `--yes`, and [`internal/cli/destroy.go`](../../internal/cli/destroy.go) explicitly defers named-volume / provisioned-DB removal to `uninstall`/the db group. `--purge-data` adds the opt-in destructive path: before tearing down the project stack it enumerates `ProvisionedFor(project)`, calls `Provisioner.Drop` for each (confirm-gated), then `RemoveProvisionedForProject(project)` and `ReleasePortsFor` the overlay ports ([`ledger.go`](../../internal/state/ledger.go) line 278). Without `--purge-data`, destroy keeps its current contract: it leaves the resources (and their bytes) and only removes the project's containers — the data survives a re-clone.

## Verified constraints / gotchas
- **`CREATE DATABASE`/`CREATE ROLE` are not idempotent and can't run in a transaction** — the naive \"just run the DDL\" loses on the second invocation and on concurrent `up`s. Every provisioner's `Ensure` must be existence-guarded (`Exists` before `Exec`) exactly like `Postgres.EnsureProject`, and the SQL must run **unwrapped** ([DECISIONS D8](../DECISIONS.md)).
- **Don't reach the engine over the bridge IP or a default-published port — there is none.** Shared engines publish **no** host port by default ([spec 03](03-workspaces-and-shared-services.md)), and bridge IPs aren't host-routable on Docker Desktop/WSL2. Host-side tooling (pgx, the S3 SDK, `mc`, `nats`, `rpk`) runs **from the host** and must go through a **127.0.0.1 up-time overlay** with a **ledger-allocated** port — replicate `writeProvisionOverlay` per engine with its own `purpose`/`portBase`/`containerPort`, never hardcode `5432`.
- **`FreeHostPort`/`AllocatePort` must self-lock and be idempotent per `(owner, purpose)`** — re-running a command must return the *same* port (`PortFor` lookup before allocation, [`ledger.go`](../../internal/state/ledger.go) line 232), or every invocation leaks a new mapping and the overlay diverges. Bind-tests are advisory (TOCTOU); union with Docker-published ports ([spec 03](03-workspaces-and-shared-services.md) port allocation).
- **The flock wraps the ledger CRUD + the provisioner DDL, not the whole command** — but a *long* provisioning op (e.g. seeding a large bucket) must run **outside** the lock, like long hooks and `pg_restore` ([spec 15](15-db-snapshot-restore.md)); holding the flock for it serializes every other terminal. Hold it only around `Exists`/`Exec`/`RecordProvisioned`/`LogEvent`.
- **Tenant names must be sanitized per engine** — `pgIdent` maps `-`→`_` and quotes; S3 bucket names are globally unique per instance, lowercase, DNS-label-constrained; Kafka topics and NATS subjects have their own charsets. A naive \"use the project name verbatim\" breaks on hyphenated projects. Each provisioner owns its identifier-mapping (and it must be **stable** across runs so the same project always resolves to the same resource).
- **`kind` is free-text, but the set is still validated** — adding `bucket`/`queue` needs no migration, yet the config resolver and `resource create` must reject a `kind` the target engine's provisioner doesn't list (`Provisioner.Kinds()`), or you record a ledger row nothing can ever drop.
- **Secret attributes never become inline refs** — the resolver rejects `${ref:…password}` (see `secretAttrs` in `resolver.go`); a generated credential reaches a container **only** through an `env.import` valueless key. A CI test must assert no generated file contains any secret value (the existing §7.5 assertion, extended to every engine).
- **External engine tools break the single-static-binary promise for their verbs only** — prefer pure-Go SDKs (`pgx`, `aws-sdk-go-v2`, `nats.go`, `franz-go`) so `up` and the common path stay self-contained (`CGO_ENABLED=0`); gate `mc`/`madmin`/`rpk` behind `info`-level `doctor` probes so their absence degrades one engine's resource verbs, never blocks `up` (the `mkcert`/`cloudflared` posture, [DECISIONS D11/D12](../DECISIONS.md)).
- **No daemon (v1 model holds)** — resource provisioning is explicit (`resource create`) or `up`-time (declarative). There is no background reconciler creating resources; drift is reported on the next command and reclaimed only by explicit `gc`.

## Acceptance criteria
- [ ] A new `kind` (`bucket`/`queue`/`stream`/`topic`/`user`/`lifecycle`) is recorded in `provisioned` and reclaimed by `resource gc` with **zero** schema migrations.
- [ ] `resource create minio bucket acme-uploads --project acme` creates the bucket idempotently (second run is a no-op), records the ledger row, logs an `event_log` entry, and runs the create inside the flock with the bucket op (if long) outside it.
- [ ] Provisioning project A's resources can never read or mutate project B's on the same shared engine (tenant-scoping CI assertion across all engines, mirroring the Postgres role isolation).
- [ ] The declarative `resources:` block in `devstack.yaml` is provisioned by the `up` saga phase; re-running `up` provisions nothing new (idempotent), and removing an entry does **not** drop the resource (Q-RESOURCE-DRIFT).
- [ ] `generate` output is **byte-identical** with and without a `resources:` block and with the per-engine host-port overlay present (overlay is an up-time artifact, not generated output; determinism CI green).
- [ ] A `credentials: generated` resource pushes its value to the provider (`Pusher.Push`), injects it as a **valueless** env key, and **no** generated file contains the value (CI assertion); `${ref:…secret}` for that resource is a hard resolver error.
- [ ] Two concurrent `resource create` invocations (two terminals) → no `database is locked`, no duplicate port assignment, no `already exists` crash (flock + existence-guarded `Ensure`).
- [ ] `resource gc` after removing a project reclaims its orphaned resources across engines only after explicit confirmation, calling each engine's `Drop`, and never recreates or bounces the shared container.
- [ ] `workspace destroy --purge-data` (the new flag) drops every `ProvisionedFor(project)` resource (confirm-gated), removes the rows, and releases the overlay ports; without `--purge-data` the resources survive (the command's current data-preserving contract is unchanged).
- [ ] `resource list --json` and `resource show --json` emit the documented attribute schema with secrets redacted; `--show-secrets` is the only path that prints a value.

## Dependencies / consumers
Consumes `internal/provision` (the `Conn`/`EnsureProject` pattern, reused verbatim for Postgres and as the template for every new engine seam), `internal/state` + `internal/lock` (the `provisioned` table + flock — every row write and DDL is locked; reads are lock-free), `internal/generate` (`SharedAlias`, the `${ref}`/`sharedAttr` resolver, the generalized `writeProvisionOverlay`), `internal/docker` (read-only enumeration + the Compose driver to apply the overlay), `internal/config` (the additive `resources:` block + the cross-ref resolver), `internal/secrets` (the `Pusher` + a new `crypto/rand` cred generator + SOPS for `generated` credentials), and `internal/orchestrate` (the `resources` saga phase, generalizing `provisionPhase`). Consumed by `internal/cli` (`resource list|show|create|rm|gc`, and the new `workspace destroy --purge-data` flag), and — crucially — by **spec 28** (resource templates: the `provides:`/`exports:`/`kinds:` template surface each provisioner reads) and **spec 29** (per-domain verbs `db`/`s3`/`queue`/`stream`/`topic` that delegate to these provisioners; the planned `db gc` aliases `resource gc --engine postgres`). **Thinner v2 (~3w):** the `Resource` model + `Provisioner` interface + Postgres (`database`/`user`) and MinIO (`bucket`/`lifecycle`) provisioners + the generic `resource` verbs + the declarative phase. **Full (~6w):** adds Redis ACL, the net-new LocalStack/NATS/Kafka engine templates + provisioners, the `generated` credential path across all engines, and `resource gc` parity with `workspace destroy --purge-data`.

## Open questions
[Q-DAEMON](../OPEN-QUESTIONS.md) (no daemon → all resource ops are explicit or up-time; no background reconcile). **New:** **Q-RESOURCE-SHARING** — can two projects share one resource (a bucket A writes, B reads)? *Recommendation:* v2 stays strictly single-owner (the tenant-scoping invariant); cross-project access is a future explicit `grants:` block, not implicit. **Decision:** single-owner in v2; defer shared-resource grants. **New:** **Q-RESOURCE-DRIFT** — when a `resources:` entry is deleted from `devstack.yaml`, should `up` drop the resource? *Recommendation:* never auto-drop (silent data loss); reconcile reports drift, `resource gc`/`rm --purge-data` reclaim explicitly. **Decision:** declarative removal is non-destructive; teardown is always explicit + confirmed. **New:** **Q-MINIO-DRIVER** — pure-Go `aws-sdk-go-v2` S3 vs shelling `mc`/`madmin` for IAM. *Recommendation:* SDK for bucket/object/lifecycle (keeps the static binary), `mc`/`madmin` only for admin IAM scoped-key policy, both behind one `internal/minio` seam with `info`-level doctor probes. **Decision:** prefer the pure-Go SDK; external `mc` is an opt-in admin-only fallback.