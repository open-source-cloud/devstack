# Databases (Postgres)

[devstack](../../README.md) · [Guide](./README.md) › Databases (Postgres)

The `db` command group manages **tenant-scoped databases, login roles and grants**
on the shared Postgres engine. Every project gets its own role and database for
free at `up`; the `db` verbs are for the extra databases, users and grants a
project needs on top of that.

```bash
devstack db list                       # what this project owns
devstack db create reporting           # → acme_reporting, owned by the project role
devstack db user create readonly --role read --db acme_reporting
```

> Snapshots, restore, reset and seed-from-store (`db snapshot`, `db restore`,
> `db reset`, `db pull`) are covered separately in
> [data-lifecycle.md](data-lifecycle.md) — this page is provisioning only.

## The automatic per-project role + database

When you run `devstack up`, devstack provisions **one Postgres role and one
database named after the project** on the shared engine (unless you pass
`--no-provision`). The role owns the database, and both the role name and its
password default to the project name.

Project `my-api` therefore gets:

- role `my_api` (password `my_api`)
- database `my_api`

### Why the underscore?

The project name comes from `devstack.yaml`, where `name` follows the `dsname`
rule and **may contain hyphens** (`my-api`). Postgres, however, treats a hyphen
as an operator — `my-api` is only a legal identifier if you double-quote it
everywhere, forever. To keep every generated DSN and every `psql` session
quote-free, devstack maps **hyphens → underscores** for Postgres identifiers.
So `my-api` → role/db `my_api`. (Buckets and messaging keep the hyphen and use a
`<project>-<name>` form instead — see [object-storage.md](object-storage.md).)

Connect from a container over the shared network:

```
postgres://my_api:my_api@shared-postgres:5432/my_api
```

...or from the host after `devstack shared expose` — see
[shared-services.md](shared-services.md).

## `db create <name>` — an extra database

```bash
devstack db create reporting            # → database "acme_reporting" (project "acme")
devstack db create analytics --owner acme
devstack db create legacydb --no-prefix # → literal "legacydb"
```

By default the physical name is **`<project>_<name>`** (hyphens in the project
name already normalized to underscores), so `acme` + `reporting` → `acme_reporting`.
This namespaces every project's databases on the one shared engine. The new
database is owned by the project role and creation is idempotent.

| Flag          | Default              | Description |
|---------------|----------------------|-------------|
| `--project`   | single/first project | which project owns it (default: the workspace's only project, else first alphabetically) |
| `--owner`     | project role         | owning role for the new database |
| `--no-prefix` | false                | use the literal name (skip the `<project>_` prefix) |

## `db user create <name>` — a login role

```bash
devstack db user create readonly --role read --db acme_reporting
devstack db user create svc --role write --db acme_reporting --generate
devstack db user create admin --role admin --password s3cret --db acme_reporting
```

Creates a tenant login role (physical name `<project>_<name>` unless
`--no-prefix`), optionally granting it a privilege tier on a database in the same
call.

| Flag          | Default              | Description |
|---------------|----------------------|-------------|
| `--project`   | single/first project | owning project |
| `--db`        | ""                   | database to grant on (its **physical** name, e.g. `acme_reporting`) |
| `--role`      | `read`               | privilege tier: `read` \| `write` \| `admin` |
| `--password`  | predictable          | explicit password (default: predictable == the role name) |
| `--generate`  | false                | generate a random password via `crypto/rand` |
| `--no-prefix` | false                | use the literal name (skip the `<project>_` prefix) |

## `db grant <role>` — grant a tier on a database

```bash
devstack db grant acme_readonly --on acme_reporting --as read
devstack db grant acme_svc --on acme_reporting --as write
```

Grants a privilege tier to an **existing** role on a database.

| Flag        | Default              | Description |
|-------------|----------------------|-------------|
| `--project` | single/first project | owning project |
| `--on`      | "" (required)        | target database (physical name) |
| `--as`      | `read`               | privilege tier: `read` \| `write` \| `admin` |

> The `--as` here is a local grant tier — unrelated to the global `--as <name>`
> binary-branding flag.

## `db list` — what's provisioned

```bash
devstack db list                    # databases and roles this project owns
devstack db list --kind db          # databases only
devstack db list --kind role        # roles only
```

Lock-free (read-only).

| Flag        | Default              | Description |
|-------------|----------------------|-------------|
| `--project` | single/first project | scope to a project |
| `--kind`    | "" (both)            | `db` \| `role` |

## `db drop <name>` — remove a database or role

```bash
devstack db drop reporting                    # drops database "acme_reporting"
devstack db drop acme_readonly --kind role
devstack db drop legacydb --no-prefix --yes
```

Destructive — confirmation is required. Under `--json` / non-interactively you
**must** pass `--yes`.

| Flag          | Default              | Description |
|---------------|----------------------|-------------|
| `--project`   | single/first project | owning project |
| `--kind`      | `db`                 | `db` \| `role` |
| `--yes`       | false                | confirm (required for `--json`) |
| `--no-prefix` | false                | treat `<name>` as literal |

## `db gc` — reclaim after a project leaves

```bash
devstack db gc --yes
```

Drops databases and roles whose owning project is no longer in the workspace.
Destructive; `--yes` required for `--json`.

| Flag    | Default | Description |
|---------|---------|-------------|
| `--yes` | false   | confirm (required for `--json`) |

## Troubleshooting: why `db create` fails

> **The shared Postgres engine must be running.** The `db` verbs connect to a
> live engine (via `pgx` from the host) — they do not start it. If you see a
> connection error:
>
> 1. **Run `devstack up` first.** This starts the shared Postgres and provisions
>    the per-project role/database. `db create` on a cold stack has nothing to
>    connect to.
> 2. **Check the engine is actually up:** `devstack shared status` should list a
>    running `postgres` instance with a live ref count. If it's stopped, `up`
>    (or `shared gc` may have stopped an idle one).
> 3. **Ambiguous `--project`.** With more than one project in the workspace and
>    no `--project`, the owner defaults to the first project alphabetically —
>    which may not be the one you meant. Pass `--project <name>` explicitly so
>    the `<project>_` prefix and ownership land on the right tenant.
> 4. Host-side provisioning needs the engine reachable from the host. devstack
>    handles the ledger-allocated port for this during `up`; if you disabled
>    provisioning with `up --no-provision`, run a plain `devstack up` first.

## See also

- [Shared services & host access](shared-services.md) — reach Postgres from a GUI.
- [Object storage](object-storage.md) — the parallel `s3` group on MinIO.
- [The resource layer](resources.md) — declare databases/users in `devstack.yaml`.
- [Snapshots, restore & reset](data-lifecycle.md) — `db snapshot`/`restore`/`reset`/`pull`.
- [Command reference](command-reference.md) — every command, terse.

---

◀ [Shared services & host access](shared-services.md) · [Guide index](./README.md) · [Object storage (S3/MinIO)](object-storage.md) ▶
