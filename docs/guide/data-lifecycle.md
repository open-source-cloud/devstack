# Snapshots, restore & reset

[devstack](../../README.md) · [Guide](./README.md) › Snapshots, restore & reset

Capture a project's tenant data to a local snapshot, wipe it back to empty, and
restore a known-good state — the loop you want between test runs. These verbs
live under the `db` group but cover Postgres, Redis, and MinIO tenant namespaces
via `--kind`.

```bash
devstack db snapshot before-migration      # capture current state
devstack db reset --yes                     # wipe to empty
devstack db restore before-migration --yes  # roll back to the snapshot
```

## Where snapshots live

Snapshots are written to the local store:

```
$DEVSTACK_HOME/snapshots/<workspace>/
```

(`$DEVSTACK_HOME` defaults to `~/.devstack`.) They are per-workspace and stay on
your machine. A **remote / team snapshot store** — pushing and pulling snapshots
between machines — is deferred; today `db pull` seeds from a snapshot already
present in your **local** store.

## Choosing the data plane: `--kind`

Every verb below defaults to Postgres (`--kind pg`) and accepts `pg | redis |
minio` to target the matching tenant namespace. The owning project defaults to
the workspace's single project (else the first alphabetically); override with
`--project`. `--instance` selects a specific shared instance when more than one
engine of a kind is running.

---

## `db snapshot [name]`

Capture a project's tenant namespace to the snapshot store. Omit the name for a
generated one.

```bash
devstack db snapshot                        # auto-named pg snapshot
devstack db snapshot seed-v3                 # named
devstack db snapshot cache-warm --kind redis
devstack db snapshot assets --kind minio
```

| Flag | Default | Description |
|---|---|---|
| `--kind` | `pg` | `pg` \| `redis` \| `minio` |
| `--project` | "" | owning project |
| `--db` | "" | tenant namespace (default: derived from the project) |
| `--instance` | "" | specific shared instance |

### `db snapshot ls`

List captured snapshots (lock-free read).

```bash
devstack db snapshot ls
devstack db snapshot ls --project api
```

| Flag | Default | Description |
|---|---|---|
| `--project` | "" | filter to one project |

---

## `db restore <name>`

Restore a project's tenant namespace from a snapshot. Destructive — it overwrites
current data.

```bash
devstack db restore seed-v3 --yes
devstack db restore seed-v3 --force        # replay over a non-empty namespace
```

| Flag | Default | Description |
|---|---|---|
| `--kind` | `pg` | `pg` \| `redis` \| `minio` |
| `--project` | "" | owning project |
| `--db` | "" | tenant namespace |
| `--instance` | "" | specific shared instance |
| `--force` | false | replay over a non-empty namespace |
| `--yes` | false | required with `--json` / non-interactive |

---

## `db reset`

Drop and re-provision a project's tenant database to **empty**. Destructive.

```bash
devstack db reset --yes
devstack db reset --force --yes     # terminate live connections first
```

| Flag | Default | Description |
|---|---|---|
| `--project` | "" | owning project |
| `--instance` | "" | specific shared instance |
| `--force` | false | terminate live connections before dropping |
| `--yes` | false | required with `--json` / non-interactive |

Use `--force` when a running app still holds connections to the tenant database
(otherwise the drop is refused while sessions are open).

---

## `db pull <name>`

Seed a project's tenant from a named snapshot already in your **local** store —
the fast way to prime a fresh checkout with fixtures.

```bash
devstack db pull seed-v3
devstack db pull assets --kind minio       # (kind via the same flag family)
```

| Flag | Default | Description |
|---|---|---|
| `--project` | "" | owning project |
| `--db` | "" | tenant namespace |
| `--instance` | "" | specific shared instance |

`pull` reads from `$DEVSTACK_HOME/snapshots/<workspace>/`. There is no remote
fetch yet — see the note at the top of this page.

---

## Recipe: snapshot → mutate → reset → restore between test runs

A repeatable inner loop for tests that dirty the database:

```bash
# 1. Capture a clean baseline once
devstack db snapshot baseline

# 2. Run a test that mutates data
npm test                          # writes rows, uploads objects, etc.

# 3. Reset to empty (kill any lingering connections)
devstack db reset --force --yes

# 4. Restore the baseline for the next run
devstack db restore baseline --yes
```

For a Redis cache or a MinIO bucket, add `--kind redis` / `--kind minio` to each
verb. Snapshot names are shared across kinds only by convention — keep them
distinct (`baseline-pg`, `baseline-redis`) if you snapshot several planes.

## See also

- [Databases](databases.md) — the rest of the `db` group (create, users, grants)
- [The resource layer](resources.md) — how tenant namespaces are provisioned
- [Object storage](object-storage.md) — MinIO buckets you can snapshot with `--kind minio`
- [The global store](store.md) — where `$DEVSTACK_HOME/snapshots/` lives

---

◀ [The resource layer](resources.md) · [Guide index](./README.md) · [Multi-repo git](multi-repo-git.md) ▶
