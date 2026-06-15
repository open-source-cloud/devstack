# Spec 15 — DB seed, snapshot & restore for shared services

**Module:** `internal/db` (+ `internal/provision`) · **Milestone:** v2 · **Effort:** ~3w (feature #7)

## Purpose
Give each project safe, fast, **per-project** data lifecycle on the *shared* engines: `db snapshot` to capture, `db restore` to roll back, `db reset` to drop+recreate+re-seed, and `db pull` to seed from a sanitized remote dump. Branch-switching and "I broke my local data" are daily pain, and per-project DB-on-shared-Postgres is precisely what makes a tenant-scoped snapshot possible without a duplicate stack. This is **post-1.0**: it builds directly on the v1 per-project role/db/bucket isolation ([spec 03](03-workspaces-and-shared-services.md)), the `provisioned` ownership ledger and flock spine ([spec 08](08-state-locking-and-lifecycle.md)), and the ledger-backed `firstRun` seed hook ([spec 11](11-lifecycle-hooks.md)) — none of which exist until M2/M6, so the data workflow that sits *on top* of them is deferred to v2.

> **Distinct from v1 `db gc`** ([spec 13](13-doctor-diagnostics-and-teardown.md)): `db gc` is the orphan-*reaper* (reclaim provisioned roles/dbs/buckets a removed project left behind). Spec 15 is the data *workflow* for a live project's own tenant. They share the `provisioned` ledger but never overlap in verbs.

## Why per-project (not per-instance) is the whole point
The shared Postgres hosts N tenants (`<proj>` role owns `<proj>_db`); Redis uses a per-project logical DB index or key prefix; MinIO a per-project bucket. A snapshot therefore captures **exactly one tenant's namespace** and a restore touches **only** that namespace — `pg_dump <proj_db>` as the project role, `redis --scan` of the prefix / a per-DB dump, `mc mirror` of one bucket. **A snapshot of project A can never read or clobber project B's data on the same warm instance** — the isolation that makes the shared stack the differentiator also makes data ops tenant-safe. Snapshots are keyed by `(ctx, project, kind)` and are never global to the engine.

## Verbs (`devstack db …`)
Extends the v1 `db gc` surface ([spec 07](07-cli-and-aliasing.md)); all four respect `--json`/`--quiet` and the global flock.

| Verb | Does | Data loss? |
|---|---|---|
| `db snapshot [name] [--kind pg\|redis\|minio\|all] [--project P]` | dump the project's tenant namespace → content-addressed store + a `snapshot` ledger row. `name` defaults to a timestamp+short-digest label. | no |
| `db restore <name> [--kind …]` | terminate the tenant's live connections, drop+recreate the schema/keys/bucket, replay the dump. | **yes — explicit confirm** |
| `db reset [--seed]` | drop+recreate role/db (or flush index / empty bucket) to a clean tenant, then **re-arm + replay `firstRun`** ([spec 11](11-lifecycle-hooks.md)) when `--seed`. | **yes — explicit confirm** |
| `db list [--project P] [--kind …]` | list snapshots (name, kind, digest, size, created_at) from the ledger; `--json` machine-readable. | no |
| `db pull <source> [--sanitize profile]` | fetch a remote sanitized dump, store it, and wire it as the `firstRun` seed source for next `up`/`reset`. | no (seeds fresh tenants) |

Default `--project` is the current workspace project; default `--kind` is `pg` (the only engine with a true dump/restore tool in v1-substrate; `redis`/`minio` opt-in). `db restore`/`reset` refuse without a TTY confirm unless `--yes`.

## Content-addressed store + ledger
Dumps live under `XDG_DATA_HOME/devstack/snapshots/<sha256>` — **content-addressed by the SHA-256 of the dump bytes**, so identical dumps dedupe and a corrupted file is detectable on read (re-hash before restore; mismatch → hard fail). Metadata lands in a new, append-only ledger table (additive migration, [spec 08](08-state-locking-and-lifecycle.md) §migrations):

```sql
snapshot(
  ctx        TEXT,    -- Docker context (same keying as the rest of the ledger)
  project    TEXT,
  kind       TEXT,    -- pg | redis | minio
  name       TEXT,    -- human label (unique per ctx,project,kind)
  digest     TEXT,    -- sha256 of the dump file  → store path
  size       INTEGER, -- bytes (for retention/gc)
  engine_major INTEGER, -- server major at capture time (restore-compat check)
  created_at TEXT,
  PRIMARY KEY (ctx, project, kind, name)
);
```

- The label→digest indirection means `restore <name>` is stable while the file is shared; the file is GC'd only when **no** ledger row references its digest (ref-by-digest, like the build-context hash store in [spec 02](02-templating-and-generation.md)).
- All writes (insert/delete rows, the drop/recreate SQL) happen **inside the flock**; the dump/restore *process* itself runs **outside** the lock (it is long; holding the flock for a multi-GB `pg_restore` would serialize every other invocation — same rule as long hooks, [spec 11](11-lifecycle-hooks.md)).

## Engine dumpers behind an interface
Each engine's tooling is an **external binary** shelled out behind one small interface, mirroring the docker/git wrapping discipline (every risky external tool gets an `internal/` seam + a mock):

```go
type Dumper interface {
    Snapshot(ctx, tenant Tenant, w io.Writer) (meta, error) // streams dump bytes
    Restore(ctx, tenant Tenant, r io.Reader) error          // replays into a clean tenant
    Reset(ctx, tenant Tenant) error                         // drop+recreate the namespace
    Preflight() error                                        // tool present + version-compatible
}
```

| kind | snapshot | restore | reset |
|---|---|---|---|
| `pg` | `pg_dump --format=custom --no-owner <proj_db>` as the project role | `pg_restore --clean --if-exists --no-owner` into the recreated db | `pg_terminate_backend` → `DROP DATABASE` → `CREATE DATABASE … OWNER <proj>` (guarded, `pgx/v5`) |
| `redis` | `BGSAVE` then copy the RDB (or a `--scan`+`DUMP` of the project's key prefix for index-shared instances) | flush the index / delete the prefix, then `RESTORE` keys | `FLUSHDB` on the project's logical index (never `FLUSHALL`) |
| `minio` | `mc mirror <alias>/<proj-bucket> <local>` (or `mc admin` for IAM if scoped keys are snapshotted) | `mc mirror --overwrite` back into an emptied bucket | `mc rb --force` + recreate the bucket + re-apply the scoped policy |

The pg dumper uses the SDK **read-only** only to locate/enumerate the shared container; it never runs dump tooling *via* the SDK — Compose owns containers, the tools run as host binaries against the published/DNS endpoint (preferring the same connection path provisioning uses, [spec 03](03-workspaces-and-shared-services.md)). `doctor` adds `info`-level `bin.pg_dump`/`bin.mc` probes ([spec 13](13-doctor-diagnostics-and-teardown.md)).

## `db pull` + sanitization (seed from a real dataset)
`db pull` fetches a dump from a remote source (an S3/HTTP URL or a teammate's exported file), stores it content-addressed, and registers it as the project's **seed source** so the next `firstRun` ([spec 11](11-lifecycle-hooks.md)) replays it into a fresh tenant instead of running an empty migration. Because production-derived data is involved, a **sanitization step is mandatory before the dump lands locally** (or is run on import for an already-fetched file): a declarative `sanitize:` profile in `devstack.yaml` lists tables/columns to NULL, mask, or hash, and rows to drop (PII, tokens, payment data). Sanitization runs as a transform pass over the dump (load into a throwaway scratch db, scrub, re-dump) so the on-disk artifact is **already scrubbed** — the raw production dump is never persisted in the store. A workspace policy may **require** a non-empty `sanitize` profile for any `db pull` from a non-local source (refuse otherwise).

## Data-loss safety & the never-recreate guard
- `restore`/`reset` are destructive: they require **explicit data-loss confirmation** (interactive prompt naming the project+kind, or `--yes`), and log a `db.restore`/`db.reset` `event_log` row with the digest restored ([spec 08](08-state-locking-and-lifecycle.md) event log → answers "what happened to my data").
- They operate on the **tenant**, never the engine: drop/recreate the *database/index/bucket*, **never** the shared container or its volume. The **never-recreate-a-stateful-shared-service** guard ([spec 03](03-workspaces-and-shared-services.md)) still holds — restoring project A must not bounce the Postgres other tenants are live on. Restore terminates only `<proj_db>`'s backends (`pg_terminate_backend WHERE datname=<proj_db>`), not the whole server.
- The **PG18+ PGDATA path** caveat ([spec 03](03-workspaces-and-shared-services.md), [DECISIONS D8](../DECISIONS.md)) is inherited unchanged: snapshot/restore work at the logical-dump layer, so they sidestep the volume-path footgun entirely — but a `reset` that re-arms `firstRun` must use the `(db,role)` scope key ([Q-HOOK-SCOPE](../OPEN-QUESTIONS.md)) so seeding re-fires on the recreated database.

## Verified constraints / gotchas
- **`pg_dump`/`pg_restore` client major must be ≥ the server major** — a 16 client cannot reliably restore an 18 dump; a too-old client errors outright. Store `engine_major` at capture and refuse a restore where the available client is older than the dump's server (clear remediation: upgrade the client tooling); `doctor` probes the client version against the running shared Postgres major.
- **A consistent dump needs a single transaction snapshot while connections are live** — `pg_dump` already opens a `REPEATABLE READ` snapshot, so it is safe against a busy tenant *without* stopping the service; do **not** stop Postgres to dump (that would hit every other tenant).
- **Redis `BGSAVE` is asynchronous** — it forks and returns immediately; you must poll `INFO persistence` until `rdb_last_save_time` **advances past** the pre-call value (and `rdb_bgsave_in_progress` is 0) before copying the RDB, or you snapshot a stale/partial file. `SAVE` (synchronous) blocks the whole server and is unacceptable on a shared instance.
- **Restoring into an in-use database requires terminating connections first** — `DROP DATABASE` fails while sessions are attached; run `pg_terminate_backend` over `pg_stat_activity WHERE datname=<proj_db>` (and revoke `CONNECT` momentarily) before drop+recreate, all inside the flock.
- **`CREATE DATABASE` is not idempotent** ([DECISIONS D8](../DECISIONS.md)) — `reset`/`restore` recreate via the same existence-guarded `pgx/v5` SQL provisioning uses; never `initdb.d`.
- **MinIO needs `mc`/`madmin`** — there is no in-process pure-Go bucket-mirror in v1-substrate; `mc mirror` (data) and `mc admin`/`madmin-go` (scoped-key/policy capture) are the path, shelled out and version-checked. Bucket names are globally unique per instance — restore into the *same* per-project bucket, never a new one.
- **Per-engine tools are external binaries**, not pure-Go — they break the "single static binary needs nothing installed" promise for *these* verbs only. They are `info`-level `doctor` probes (absence degrades `db` verbs, never blocks `up`), consistent with the `mkcert`/`cloudflared` external-binary posture ([DECISIONS D11/D12](../DECISIONS.md)).
- **No daemon (v1 model holds)** — snapshots are explicit/manual. Scheduled or on-`down` auto-snapshots would *want* a background agent; that is out of scope here and tracked under [Q-DAEMON](../OPEN-QUESTIONS.md), not argued for in this spec.

## Acceptance criteria
- [ ] `db snapshot` on project A captures only A's `<proj_db>`; the dump contains no row owned by project B sharing the same Postgres.
- [ ] `db snapshot` then mutate then `db restore <name>` returns A's tenant to the captured state, with B's data untouched and B's connections never dropped.
- [ ] Two identical-content snapshots produce **one** file in the store (digest dedupe); deleting one ledger row keeps the file until the last referencing row is gone.
- [ ] A restore whose stored `engine_major` exceeds the available `pg_restore` client major fails fast with the version-mismatch remediation, never a partial restore.
- [ ] `db reset --seed` drops+recreates the tenant, re-arms the `(db,role)`-scoped `firstRun`, and the next replay re-seeds from the registered source ([spec 11](11-lifecycle-hooks.md)).
- [ ] `redis` snapshot waits for `rdb_last_save_time` to advance before copying the RDB (no stale/partial file).
- [ ] `db pull` from a non-local source refuses without a `sanitize` profile when workspace policy requires one; with a profile, the stored artifact contains no value matching the masked columns (CI assertion).
- [ ] `db restore`/`db reset` require explicit confirmation (or `--yes`), log an `event_log` row with the digest, and run drop/recreate inside the flock while the dump/restore process runs outside it.
- [ ] A concurrent `up` in another terminal is not blocked for the duration of a multi-GB restore (flock held only around ledger/SQL mutations, not the streaming).
- [ ] `db list --json` emits the documented schema (name, kind, digest, size, created_at).

## Dependencies / consumers
Consumes `internal/provision` (the `pgx/v5` existence-guarded role/db SQL, redis index, minio bucket/key — reused verbatim for drop+recreate), `internal/state` + `internal/lock` (the `snapshot` table + flock; every row write and SQL mutation is locked), `internal/hooks` (re-arming + replaying `firstRun` for `reset --seed`/`db pull`, [spec 11](11-lifecycle-hooks.md)), `internal/docker` (read-only container enumeration for the shared engine), `internal/config` (`sanitize:` profile + seed-source declaration in `devstack.yaml`, [spec 01](01-config-schema.md)), `internal/secrets` (provider creds for a remote `db pull` source, [spec 04](04-secrets.md)). Consumed by `internal/cli` (`db snapshot|restore|reset|list|pull`) and `internal/doctor` (the `bin.pg_dump`/`bin.mc` info probes). **Thinner v2 (~1.5w):** Postgres-only `snapshot`/`restore`/`reset`/`list` against the per-project tenant with the content-addressed store. **Full (~3w):** adds Redis + MinIO dumpers, `db pull` + the sanitization transform, retention/prune policy, and the client/server version-compat gate.

## Open questions
[Q-DAEMON](../OPEN-QUESTIONS.md) (no daemon → no auto/scheduled snapshots; all `db` ops are explicit), [Q-HOOK-SCOPE](../OPEN-QUESTIONS.md) (the `firstRun` scope key that `reset --seed` re-arms). **New:** **Q-SNAP-RETENTION** — what is the default snapshot retention/prune policy (keep-last-N per `(project,kind)`, max-age, or total-store-size budget), and should `db snapshot` auto-prune or only `db gc --snapshots`? **New:** **Q-SANITIZE-ENGINE** — is the sanitization transform a built-in declarative `sanitize:` profile (NULL/mask/hash/drop), a shell-out to an external anonymizer, or both behind one interface, given the single-static-binary constraint?
