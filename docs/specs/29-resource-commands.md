# Spec 29 — Imperative resource commands (db / s3 / queue / stream)

**Module:** `internal/resource` (+ `internal/provision`, `internal/cli`) · **Milestone:** M2+ (v0.x; queue/stream gated on [spec 28](28-cloud-engine-templates.md) engines) · **Effort:** ~5w (DB+S3 ~2w on the existing M2 substrate; messaging ~3w once the engines land)

## Purpose
Give each project a **tenant-scoped, imperative resource surface** on the shared engines: create the extra databases, SQL roles/grants, object-storage buckets (with lifecycle/versioning/policy/CORS), queues, pub/sub topics, and durable streams a real app needs — without hand-driving `psql`, `mc`, or `aws --endpoint-url` and without a duplicate stack per repo. Today the only data a project gets is one auto-provisioned role+db ([spec 03](03-workspaces-and-shared-services.md)); a Laravel app that wants three databases, a `reports` read-only role, an `uploads` bucket with a 30-day expiry rule, and an SQS queue has to leave the tool and run engine CLIs by hand. This spec is the concrete verb-by-verb command layer that sits on the **architecture** in [spec 27](27-resource-layer.md) (the `resources:` provisioner contract + host-reachability overlay) and the **engines** in [spec 28](28-cloud-engine-templates.md) (LocalStack/NATS/Kafka templates). Every imperative verb is the *same* provisioner the declarative `resources:` block runs at `up` — built once, callable two ways.

> **Distinct from `db gc` ([spec 13](13-doctor-diagnostics-and-teardown.md)) and `db snapshot` ([spec 15](15-db-snapshot-restore.md)):** `db gc` reaps orphaned ownership rows a removed project left behind; `db snapshot/restore` is the per-tenant *data* workflow (`internal/db`, still unbuilt). Spec 29 is the *structure* layer — it creates and inspects the resources themselves. All three share the `provisioned` ownership ledger but never overlap in verbs. None of these command groups exist in the tree yet — they are net-new on top of the M2 ledger/provision/lock substrate.

## Decisions
- **One new module, `internal/resource`, owns the verb→provisioner dispatch.** It does **not** re-implement provisioning: Postgres reuses `provision.Postgres` (extended with new `EnsureRole`/`Grant` methods on the existing `Conn` interface); MinIO/LocalStack/NATS/Kafka each get a `Provisioner` behind the same small interface (all net-new — `internal/provision` is Postgres-only today). The CLI commands are **standalone cobra commands that mirror the `provisionPhase` body** (`internal/orchestrate/provision.go:132-207`), **not** new saga phases — imperative resource ops are invoked directly; the saga phase handles the declarative `resources:` block.
- **Tenant-scoped naming is mandatory and enforced, not advisory.** A bucket/queue/stream/db name the user types is prefixed with the project (`<project>_<name>` for SQL idents, `<project>-<name>` for DNS/bucket/queue names) so project A can never name-collide with or reach project B's resources — the same isolation guarantee that makes the shared stack safe ([spec 03](03-workspaces-and-shared-services.md) §isolation). `--no-prefix` is an explicit escape hatch (Q-RES-NAMING).
- **Every mutation goes through the flock and records a `provisioned` row; reads are lock-free.** The canonical shape is `lock.WithLock(ctx, LockPath, func() error { …driver call…; DB.RecordProvisioned(project, kind, name); DB.LogEvent(kind, name, reason) })` — exactly `provisionPhase` (`provision.go:182,186`). The `provisioned.kind` column is **free-text** (`state/ledger.go:290`; existing values `database`/`role`/`redis_index`), so new kinds (`bucket`, `queue`, `topic`, `stream`, `lifecycle`) need **no migration**.
- **Single-resource teardown needs one net-new ledger op.** Reclaiming a *single* row (`db drop`, `s3 rb`, `queue rm`, …) cannot reuse `RemoveProvisionedForProject` (`state/ledger.go:329`), which drops a project's *entire* ownership set during teardown. Add a sibling `RemoveProvisioned(project, kind, name)` (same table, no migration) used by the destructive verbs; `db gc` keeps using `OrphanedProvisioned`/`RemoveProvisionedForProject` unchanged.
- **Engine tooling is external binaries behind one interface**, mirroring the docker/git/`mc` discipline (the pure-Go static-binary rule means these tools cannot be in-process). `mc` (MinIO/S3), `aws` (LocalStack), `nats` (NATS), `rpk`/`kafka-topics` (Kafka) shell out via `internal/resource`'s `Tool` seam with an injectable `Runner` and a `CmdError` carrying cmd+code+stderr — the same `internal/docker` Compose-driver pattern. `pgx/v5` stays the only in-process runtime SDK.
- **Native vs LocalStack is an endpoint swap, not a branch.** `s3 mb` against MinIO and against LocalStack S3 run the *same* `mc`/`aws` call against a different `--endpoint-url`; `queue create --engine sqs` always means LocalStack. The provisioner picks the endpoint from the active shared engine's host-port overlay ([spec 27](27-resource-layer.md)), never from a per-command flag.
- **Host reachability reuses the up-time overlay, not the deterministic generate output.** Host-side tools (`mc`, `aws`, `pgx`) run **from the host**, so each engine that needs host ops publishes a `127.0.0.1`-only port via a `compose.provision.yaml` overlay with a ledger-allocated port (`Manager.FreeHostPort`, a new `purpose`+base per engine), exactly as Postgres does (`provisionPurpose="pg-provision"`, base `45432`, `provision.go:29-31`). The golden `generate` output stays byte-identical.
- **Two credential patterns, per-resource policy.** (a) **predictable dev cred** — like Postgres' `password = project name` (`provision.go`): nothing secret stored, attrs reach containers via the `env.import` resolver. (b) **generated secret** via the secrets Pusher ([spec 04](04-secrets.md), feature #16: aws-sm/aws-ssm/infisical + SOPS) for resources that mint a real key (a scoped MinIO access key, an SQS-consumer IAM key) → stored in the provider, injected as a **valueless** per-service env key. Never plaintext on disk.
- **Imperative and declarative are the same provisioner.** A project declares what it needs in `devstack.yaml`'s `resources:` block ([spec 27](27-resource-layer.md)); `up` provisions it via a new resources saga phase. `devstack <domain> <verb>` calls the identical provisioner ad-hoc. `resources:` is the reproducible source of truth; the imperative verbs are for exploration and one-offs.
- **`db gc` is the single reaper for every kind.** New kinds (`bucket`/`queue`/`stream`/`role`) drop into the existing `OrphanedProvisioned`/`RemoveProvisionedForProject` machinery (`state/ledger.go:313,329`). `db gc` is **owned by [spec 13](13-doctor-diagnostics-and-teardown.md)** (the substrate exists; the verb is still v1-scoped/unbuilt); spec 29 only **extends** it with a per-kind delete call so the reaper can drop the orphan's actual engine object — no new reaper verb.

## Domain × engine provisioner map
The core table — each cell is the host-side tool call the `Provisioner` shells (or the in-process `pgx` SQL), against the ledger-allocated `127.0.0.1` overlay port. **Phase legend:** `now` = buildable on the current M2 substrate (no spec 28); the provisioner code is still net-new. `later` = needs the [spec 28](28-cloud-engine-templates.md) engines.

| Domain | Engine | `provides` / instance key | create call | ledger `kind` | phase |
|---|---|---|---|---|---|
| **database** | postgres | `postgres`/`(postgres,major)` | `pgx` guarded `CREATE DATABASE … OWNER` | `database` | now |
| **db role/grant** | postgres | `postgres` | `pgx` guarded `CREATE ROLE` + `GRANT` | `role` | now |
| **database** | mysql *(later)* | `mysql`/`(mysql,major)` | `CREATE DATABASE` + `CREATE USER`+`GRANT` (mysql driver behind `Conn`) | `database` | later |
| **object store** | minio | `minio` | `mc mb <alias>/<proj-bucket>` | `bucket` | now |
| **object store** | localstack (S3) | `localstack`/`s3` | `aws s3 mb` / `aws s3api create-bucket` | `bucket` | later |
| **s3 lifecycle** | minio | `minio` | `mc ilm rule add --expiry-days N` | `lifecycle` | now |
| **s3 lifecycle** | localstack (S3) | `localstack` | `aws s3api put-bucket-lifecycle-configuration` | `lifecycle` | later |
| **queue** | sqs (localstack) | `localstack`/`sqs` | `aws sqs create-queue` (+ redrive→DLQ) | `queue` | later |
| **queue** | redis | `redis` | logical: register a list/stream key namespace + reserve index (`AllocateRedisIndex`) | `queue` | now* |
| **queue** | nats | `nats` | `nats stream add` (work-queue retention) | `queue` | later |
| **topic** (pub/sub) | sns (localstack) | `localstack`/`sns` | `aws sns create-topic` (+ `subscribe` to a queue) | `topic` | later |
| **topic** | nats | `nats` | subject convention `<proj>.<name>.>` (+ optional stream) | `topic` | later |
| **topic** | redis | `redis` | Pub/Sub channel namespace `<proj>:<name>` (ephemeral; no provision row needed) | `topic` | now* |
| **stream** | nats | `nats`/JetStream | `nats stream add --subjects … --retention limits --max-age …` | `stream` | later |
| **stream** | kafka | `kafka` | `rpk topic create -p N -r M` / `kafka-topics --create` | `stream` | later |

\\* *redis queue/topic are thin in v0.x — Redis only allocates a logical index (`state/ledger.go:341`, the one existing non-PG allocator); the "queue" is a key-prefix convention the app honours, not a server-side object. Full SQS-grade semantics need LocalStack ([spec 28](28-cloud-engine-templates.md)).*

## CLI surface
### Databases (postgres now; mysql later)
```
devstack db create <name> [--owner <role>] [--project P]
devstack db user create <name> [--db <db>] [--role read|write|admin] [--password <pw>|--generate]
devstack db grant <role> --on <db> --as read|write|admin
devstack db list [--kind db|role] [--project P] [--json]
devstack db drop <name> [--kind db|role] [--yes]
devstack db gc [--yes] [--json]        # spec 13 verb (substrate exists); extended here to ALL kinds
```
- `db create orders` on project `api` → `pgx` guarded `CREATE DATABASE api_orders OWNER api` → `RecordProvisioned("api","database","api_orders")`. Idempotent (existence-guarded; `CREATE DATABASE` is **not** idempotent on its own — [DECISIONS D8](../DECISIONS.md)). (The user-facing `--kind db` is a display alias; the stored ledger kind is `database`, matching `provision.go:186`.)
- `db user create reports --db api_orders --role read` → guarded `CREATE ROLE api_reports LOGIN` + `GRANT CONNECT`+`USAGE`+`SELECT` (read), `…+INSERT/UPDATE/DELETE` (write), `…+ALL`/owner (admin). Extends `provision.Postgres` with new `EnsureRole(ctx, conn, role, pw)` + `Grant(ctx, conn, role, db, level)` methods, reusing the existing `Conn` interface (`{Exec, Exists}`, `provision.go:18`) so it stays unit-testable without a live server.
- `--generate` mints a password via the secrets Pusher and stores it; default is the predictable `password = <role>` dev cred (loopback-only, container-isolation-is-a-non-goal threat model, per `provision.go`).

### Object storage (minio now; localstack S3 later)
```
devstack s3 mb <bucket> [--versioning] [--no-prefix]
devstack s3 rb <bucket> [--force] [--yes]
devstack s3 ls [bucket] [--json]
devstack s3 lifecycle set <bucket> --expire-days N | --transition days=N,tier=T [--prefix P]
devstack s3 lifecycle get|rm <bucket>
devstack s3 versioning <bucket> --enable|--suspend
devstack s3 policy set <bucket> --public-read | --file policy.json
devstack s3 policy get <bucket>
devstack s3 cors  set <bucket> --file cors.json | s3 cors get <bucket>
```
- `s3 mb uploads` on project `web` → `mc mb local/web-uploads` (project-prefixed for the globally-unique bucket namespace) → `RecordProvisioned("web","bucket","web-uploads")`. `--no-prefix` for an external-contract name (Q-RES-NAMING).
- `s3 lifecycle set web-uploads --expire-days 30` → `mc ilm rule add --expiry-days 30 local/web-uploads` (the user's explicit object-lifecycle ask). `--transition` is engine-conditional (Q-RES-LIFECYCLE-PORTABILITY).
- `s3 versioning … --enable`, `s3 policy`, `s3 cors` map 1:1 to `mc version enable`, `mc anonymous set`/`mc policy`, and `mc cors set`. Against LocalStack the identical intents run as `aws s3api put-bucket-versioning|put-bucket-policy|put-bucket-cors` — endpoint swap only.

### Messaging — queues & topics (gated on spec 28 engines)
```
devstack queue create <name> [--engine sqs|redis|nats] [--fifo] [--dlq <name>] [--max-receive N]
devstack queue list [--engine ...] [--json]
devstack queue rm <name> [--yes]
devstack topic  create <name> [--engine sns|nats|redis] [--subscribe <queue>]
```
- `queue create jobs --engine sqs --dlq jobs-dead --max-receive 5` → `aws sqs create-queue --queue-name web-jobs` + a redrive policy pointing at `web-jobs-dead` (created first) → `RecordProvisioned("web","queue","web-jobs")`. `--fifo` appends `.fifo` and sets `FifoQueue=true` (AWS requires the suffix).
- `--engine redis` → reserves a key namespace `<proj>:queue:<name>` (app uses `LPUSH`/`BRPOP` or a Stream); thin, no server object.
- `topic create events --engine sns --subscribe web-jobs` → `aws sns create-topic` + `aws sns subscribe` wiring the SQS queue (the classic SNS→SQS fan-out).
- Default `--engine` is inferred from the active shared engines, never auto-started (Q-RES-ENGINE-DEFAULT).

### Messaging — streams (gated on spec 28 engines)
```
devstack stream create <name> [--engine nats|kafka] [--partitions N] [--retention DURATION] [--replicas N]
devstack stream list [--json]
devstack stream rm <name> [--yes]
```
- `stream create orders --engine nats --retention 168h` → `nats stream add web_orders --subjects 'web.orders.>' --retention limits --max-age 168h` → `RecordProvisioned("web","stream","web_orders")`.
- `stream create orders --engine kafka --partitions 6 --replicas 1` → `rpk topic create web.orders -p 6 -r 1` (Redpanda) or `kafka-topics --create` (Apache). `--partitions`/`--replicas` are Kafka-only; `--retention` maps to NATS `--max-age` or Kafka `retention.ms` — validated per-engine at parse time.

### The thin `aws` ergonomic surface
```
devstack aws -- <args...>    # e.g. devstack aws -- s3 ls   /   devstack aws -- sqs list-queues
```
A pure **argv shim** over the user's own `aws` binary: devstack resolves the LocalStack host-port from the ledger overlay and prepends `--endpoint-url=http://127.0.0.1:<port>`, `--region`, and dev creds (`AWS_ACCESS_KEY_ID=test` etc. via `exec.Cmd.Env`, never argv). It does **not** reimplement any AWS call (Q-AWS-WRAP). Recommendation in the docs is unchanged: real `aws --endpoint-url` works too; the shim just removes four flags and the port lookup.

## Declarative complement — the `resources:` block
The same provisioners run at `up` from an additive `resources:` block in `devstack.yaml` ([spec 27](27-resource-layer.md); idiomatic per [ARCHITECTURE §7.4](../ARCHITECTURE.md) additive blocks, validated by the existing custom cross-ref resolver):
```yaml
# devstack.yaml
resources:
  databases:
    - name: orders                       # → <project>_orders, owner = project role
  roles:
    - name: reports
      grant: { on: orders, as: read }
  buckets:
    - name: uploads
      versioning: true
      lifecycle: { expire_days: 30 }
  queues:
    - name: jobs
      engine: sqs
      dlq: { name: jobs-dead, max_receive: 5 }
  streams:
    - name: events
      engine: nats
      retention: 168h
```
A new **resources saga phase** (after `provisionPhase`, same `lock.WithLock` shape) walks this block and calls the identical `Provisioner`s, recording the same ledger rows — so `up` is reproducible and `devstack s3 mb …` is the exploratory equivalent. A resource removed from `resources:` is left in place (never auto-dropped; `db gc` / explicit `rm` reclaims), honouring the never-destroy-silently posture.

## Behavior
1. **Resolve workspace + project + engine instance.** Walk up for `workspace.yaml`, load the model, pick the project (`--project` or cwd). Resolve which shared instance backs the domain (e.g. `(postgres,major)` for `db`, the `minio`/`localstack` instance for `s3`). Error with a `uses:`/`resources:` remediation if the engine isn't in the workspace.
2. **Ensure host reachability.** Confirm the engine is healthy (reconcile from live containers, [spec 03](03-workspaces-and-shared-services.md)); resolve/allocate its `127.0.0.1` overlay port via `Manager.FreeHostPort(ctx, alias, purpose, base)` (idempotent — returns the port the up saga already allocated) and ensure the `compose.provision.yaml` overlay is applied. No daemon; this is on-demand per invocation.
3. **Compute the tenant-scoped physical name** (`<project>_<name>` SQL / `<project>-<name>` DNS-safe), unless `--no-prefix`.
4. **Preflight the engine tool** (`Tool.Preflight()` — binary present + version-compatible). Absence is an `info`-level `doctor` probe that degrades only this verb, never `up` (the `mkcert`/`cloudflared` external-binary posture, [DECISIONS D11/D12](../DECISIONS.md)).
5. **Take the flock and mutate** (`lock.WithLock`): run the provisioner's create call (`pgx` SQL / `mc` / `aws` / `nats` / `rpk`) — **existence-guarded / idempotent** — then `RecordProvisioned(project, kind, physicalName)` and `LogEvent(kind, name, reason)`. Reads (`list`) skip the lock entirely (snapshot).
6. **Mint/store credentials per policy.** Predictable dev cred → nothing stored, attrs flow via the `env.import` resolver (`generate/resolver.go` `sharedAttr`). Generated secret → push to the secrets provider, register a **valueless** per-service env key for compose-up injection.
7. **Emit the connection block.** Plain TTY: a human DSN/endpoint/queue-URL; `--json`: the machine schema (`{kind,name,physical,endpoint,dsn?,owner?}`); `--quiet`: the one connection string. Secret values are redacted unless `--show-secrets` (diagnostics only). For `db create orders`: `postgres://api:api@shared-postgres:5432/api_orders`.
8. **Destructive verbs (`drop`/`rb`/`rm`) confirm.** Require a TTY confirm naming project+kind+name, or `--yes`; run the drop inside the flock, then call the **new** `RemoveProvisioned(project, kind, name)` ledger op (sibling to `RemoveProvisionedForProject`, `ledger.go:329`), then write a `*.drop` `event_log` row. Never recreate/bounce the shared engine — operate only on the tenant object (the never-recreate-a-stateful-shared-service guard, [spec 03](03-workspaces-and-shared-services.md) §50).

## Verified constraints / gotchas
- **`CREATE DATABASE` / `CREATE ROLE` are not idempotent — guard them.** The naive `CREATE DATABASE x` fails on the second run and crashes a concurrent second terminal with `role already exists`. Reuse the existence-guarded `pgx/v5` SQL (`SELECT 1 FROM pg_database…` then create) that `provision.Postgres` already uses (`provision.go:48,64`; [DECISIONS D8](../DECISIONS.md)); `db user create`/`grant` extend the same guarded pattern, never `initdb.d`.
- **Host tools run from the host, so the engine needs a published port — but the default is none.** The naive choice (publish `5432`/`9000` in the generate output) breaks determinism and the "no host ports by default" posture. Do what Postgres provisioning does: a `127.0.0.1`-only **up-time overlay** (`compose.provision.yaml`, ledger-allocated port via `FreeHostPort`, new `purpose`+base per engine, e.g. `minio-provision`/`localstack-provision`) so `generate`'s golden output is untouched ([spec 27](27-resource-layer.md), `orchestrate/provision.go:102-105`).
- **Secret attributes must not be inline `${ref}`s.** `generate/resolver.go` **rejects** `password`/`secretkey`/`secret`/`token` as inline refs (`secretAttrs` set, lines 16-20; rejection at 66-67) — a generated MinIO/SQS key cannot be string-templated into a compose file (it would land in plaintext on disk, violating the [spec 04](04-secrets.md) coupling). Mint it via the Pusher, store in the provider, and emit it as a **valueless** per-service `environment: [NAME]` key filled from `exec.Cmd.Env` at compose-up — a CI test asserts no secret value lands in any generated file.
- **Bucket/queue/stream names are a flat global namespace per engine — prefix or collide.** Two projects both wanting `uploads` would clobber each other on the shared MinIO. Tenant-prefix every name (`<project>-uploads`) so isolation holds, the same reason the shared Postgres uses `<proj>_db`. `--no-prefix` is opt-in and the user owns the collision.
- **SQS FIFO queues require the `.fifo` suffix and `FifoQueue=true` together** — setting one without the other is an AWS API error LocalStack faithfully reproduces. The provisioner sets both atomically when `--fifo`.
- **MinIO ILM tiers ≠ S3 storage classes.** `mc ilm` transition tiers are admin-configured remote targets; S3 `--transition` takes `STANDARD_IA`/`GLACIER`. `--expire-days` is portable across both; `--transition` is engine-conditional and refused with a remediation on MinIO unless a tier is pre-configured (Q-RES-LIFECYCLE-PORTABILITY).
- **NATS work-queue vs Kafka partitions are different retention models.** A NATS JetStream "queue" is a stream with `WorkQueuePolicy` retention; a Kafka "stream" is a partitioned topic. Don't pretend `--partitions` is universal — validate it as Kafka-only at parse time, and map `--retention` to NATS `--max-age` vs Kafka `retention.ms` per engine.
- **Every engine tool is an external binary, not pure-Go** — `mc`/`aws`/`nats`/`rpk` break the "single static binary needs nothing installed" promise for *these verbs only* (the `CGO_ENABLED=0` rule forbids in-process SDKs that aren't pure-Go). They sit behind the `internal/resource` `Tool` seam with an injectable `Runner` (so race/unit tests run without the binary, like `internal/docker`'s `MockClient`) and are `info`-level `doctor` probes — consistent with `bin.pg_dump`/`bin.mc` ([spec 15](15-db-snapshot-restore.md)). Only `pgx` stays in-process.
- **Filter container enumeration on the tool's own label, `All=true`, exclude `oneoff=true`** when locating the engine for the overlay — the `com.docker.compose.project` label is the normalized name and silently no-ops if mis-computed ([spec 03](03-workspaces-and-shared-services.md) §45).
- **Ledger ops are not determinism-gated, but they ARE flock-gated.** Resource creation is runtime content-mutation (like provision-on-demand) — it's *not* subject to the byte-identical-output rule, but every `provisioned`/`event_log` write and every engine mutation MUST be inside `lock.WithLock` (SQLite + WAL + `busy_timeout` alone is not safe under concurrent writers on WSL2/9p). Reads stay lock-free.

## Acceptance criteria
- [ ] `db create orders` on project `api` produces `api_orders` owned by the `api` role, records a `provisioned(kind=database)` row, is idempotent on re-run, and prints the DSN; project `web` running the same command gets `web_orders` and cannot see `api_orders`.
- [ ] `db user create reports --db api_orders --role read` creates `api_reports` with SELECT-only grants; `db grant api_reports --on api_orders --as write` upgrades it; both run guarded SQL via the new `EnsureRole`/`Grant` methods inside the flock.
- [ ] `s3 mb uploads --versioning` creates `web-uploads` with versioning enabled via `mc`, records `provisioned(kind=bucket)`, and `s3 lifecycle set web-uploads --expire-days 30` adds an ILM expiry rule; `s3 ls --json` lists exactly `web`'s buckets.
- [ ] The same `s3 mb`/`s3 lifecycle` intents run against a LocalStack S3 instance with only an endpoint swap (no code-path branch), asserted by a test that runs both backends through one `Provisioner`.
- [ ] `queue create jobs --engine sqs --dlq jobs-dead --max-receive 5` creates `web-jobs` + `web-jobs-dead` with a redrive policy against LocalStack; `--engine redis` reserves a key namespace and a logical index instead.
- [ ] `stream create events --engine nats --retention 168h` creates a JetStream stream with `max-age=168h`; `--engine kafka --partitions 6` creates a 6-partition topic; `--partitions` is rejected for `--engine nats`.
- [ ] A `resources:` block in `devstack.yaml` provisions the identical databases/buckets/queues/streams at `up` via the resources saga phase, recording the same ledger rows as the imperative verbs (one provisioner, two entry points).
- [ ] A generated MinIO/SQS access key is pushed to the secrets provider and injected as a **valueless** env key; CI asserts no secret value appears in any generated compose/env file.
- [ ] `db drop`/`s3 rb`/`queue rm`/`stream rm` require a TTY confirm or `--yes`, operate only on the tenant object (never bounce the shared engine), log an `event_log` row, and reclaim the row via the new `RemoveProvisioned(project,kind,name)`.
- [ ] `db gc` (spec 13 verb) reaps orphaned rows of **every** kind (database, role, bucket, queue, stream) for removed projects with explicit confirmation — no new reaper code per kind beyond the per-kind engine-delete call.
- [ ] Two concurrent `s3 mb` invocations in two terminals → no `database is locked`, no duplicate port allocation, no double-create crash (flock + idempotent engine calls).
- [ ] All engine tools are reachable behind `internal/resource`'s mockable `Tool`/`Runner`; the full verb matrix has unit/race coverage with no real `mc`/`aws`/`nats`/`rpk` binary present.

## Dependencies / consumers
Consumes `internal/provision` (the `pgx/v5` existence-guarded role/db SQL — extended with net-new `EnsureRole`/`Grant` methods on the existing `Conn` interface, reused verbatim by the resources phase), `internal/state` + `internal/lock` (the `provisioned` ledger — free-text `kind`, `FreeHostPort`, `RecordProvisioned`/`OrphanedProvisioned`/`RemoveProvisionedForProject` + a new `RemoveProvisioned(project,kind,name)` — and the flock spine), `internal/workspace` (`Manager.FreeHostPort` for the overlay port), `internal/docker` (read-only engine enumeration + the Compose driver that applies the host-port overlay), `internal/orchestrate` (the resources saga phase mirroring `provisionPhase`), `internal/config` (the additive `resources:` block + cross-ref validation, [spec 01](01-config-schema.md)), `internal/secrets` (the Pusher for generated keys, [spec 04](04-secrets.md)), [spec 27](27-resource-layer.md) (the provisioner contract + overlay), [spec 28](28-cloud-engine-templates.md) (the LocalStack/NATS/Kafka engine templates the messaging verbs target). Consumed by `internal/cli` (`db`/`s3`/`queue`/`topic`/`stream`/`aws` command groups — all net-new), `internal/doctor` (the `bin.mc`/`bin.aws`/`bin.nats`/`bin.rpk` info probes), and `db gc` ([spec 13](13-doctor-diagnostics-and-teardown.md), the cross-kind reaper). **Thinner v0.x (~2w):** `db create|user|grant|list|drop` (postgres) + `s3 mb|rb|ls|lifecycle|versioning|policy|cors` (minio) — net-new provisioners on the existing M2 ledger/lock/provision substrate. **Full (~5w):** adds the LocalStack/NATS/Kafka `queue`/`topic`/`stream` verbs, the `aws` shim, generated-secret keys, and the declarative `resources:` saga phase — gated on [spec 28](28-cloud-engine-templates.md).

## Open questions
[Q-DAEMON](../OPEN-QUESTIONS.md) (no daemon → all resource ops are explicit/on-demand; no background reconcile of drift between `resources:` and live state), [Q-RUNTIME](../OPEN-QUESTIONS.md) (Docker-only engines). **New:** **Q-RES-NAMING** — transparent vs explicit tenant prefixing (recommend transparent + `--no-prefix`, ledger holds logical+physical). **Decision:** transparent-by-default. **New:** **Q-RES-ENGINE-DEFAULT** — default backend when `--engine` is omitted (recommend infer-from-active-engines, never auto-start). **Decision:** infer, error if unsatisfiable. **New:** **Q-AWS-WRAP** — ship the `aws` shim vs document raw `aws --endpoint-url` (recommend a pure argv shim, info-level probe). **Decision:** thin shim, never reimplement AWS. **New:** **Q-RES-LIFECYCLE-PORTABILITY** — MinIO ILM vs S3 lifecycle transition semantics (recommend portable `--expire-days`, engine-conditional `--transition`). **Decision:** expiry portable now, transitions engine-gated.