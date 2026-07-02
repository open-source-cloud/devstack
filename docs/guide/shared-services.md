# Shared services & host access

[devstack](../../README.md) · [Guide](./README.md) › Shared services & host access

Shared services are the whole point of devstack: **one warm Postgres/Redis/MinIO
(and friends) shared by every project in the workspace**, instead of a duplicate
stack per repo. This page covers what they are, how projects reach them, how
they're reclaimed, and how to reach them from **host tools** (a GUI like DataGrip)
when you need to.

```bash
devstack shared status          # who's using what, and the ref counts
devstack shared expose          # publish stable 127.0.0.1 ports for GUI clients
devstack shared ports           # show those ports + ready-to-paste connection strings
```

## What a shared service is

You declare shared engines once, in `workspace.yaml`:

```yaml
apiVersion: devstack/v1
kind: Workspace
name: acme
shared:
  postgres:
    template: postgres
    params: { version: "16" }
  redis:  { template: redis }
  minio:  { template: minio }
```

At `up`, devstack starts these on a tool-owned external bridge network,
`devstack_shared`. Every project stack joins the same network, so a project's
containers reach the shared engines directly — no host ports involved.

See [projects.md](projects.md) for how a service opts into a shared engine with
`uses:` / `env.import`, and [templates.md](templates.md) for the engine catalog.

## Reaching them by DNS (the default)

Inside the `devstack_shared` network, shared engines answer on **stable DNS
aliases**, never the bare service name (that avoids collisions with a project's
own `postgres` service):

| Engine   | DNS alias         | Port  |
|----------|-------------------|-------|
| Postgres | `shared-postgres` | 5432  |
| Redis    | `shared-redis`    | 6379  |
| MinIO    | `shared-minio`    | 9000  |

So a container connects to `shared-postgres:5432`, `shared-redis:6379`,
`shared-minio:9000`. **There are no published host ports by default** — the
network is the contract. This is why you don't get port clashes when three repos
are up at once.

The cleanest way to wire a service is to import the engine's exported attributes
rather than hard-code the alias (see [environment.md](environment.md)):

```yaml
services:
  api:
    template: php.laravel.nginx
    uses:
      - workspace.shared.postgres
    env:
      import:
        - { from: workspace.shared.postgres, vars: [host, port, user, password, database] }
```

## Ref-counting: `shared status`

devstack keeps a ledger (keyed by Docker context) counting how many project
stacks currently reference each shared engine. `up` increments; `down` releases.
An engine at **zero references** is a candidate for reclamation — but it is never
stopped implicitly (there is no daemon, no autostop).

```bash
devstack shared status
```

Shows each shared instance, its `(engine, major)` identity, ref count, and the
projects consuming it. Read-only; safe to run any time. Add `--json` to script it.

If the count ever drifts (a container was killed out-of-band), reconcile it:

```bash
devstack shared doctor          # reconcile the ledger against live containers, prune dead refs
```

## Reclaiming idle engines: `shared gc`

```bash
devstack shared gc              # REPORT shared services sitting at zero references
devstack shared gc --stop       # actually stop the zero-ref services
```

| Flag     | Default | Description |
|----------|---------|-------------|
| `--stop` | false   | actually stop the zero-ref services (default is report-only) |

`gc` never removes volumes or data — a stopped engine's Postgres volume, buckets
and keys all survive the next `up`. See [recovery.md](recovery.md) for the wider
housekeeping story (`doctor --fix`, `workspace destroy`, `uninstall`).

---

## Host access: `shared expose`

By default the engines are DNS-only, which is great for containers but useless
for a GUI running on your host (DataGrip, TablePlus, a Redis browser, the MinIO
console). `shared expose` opts in to **stable, loopback-only host ports**:

```bash
devstack shared expose            # publish every exposable shared service on 127.0.0.1
devstack shared expose postgres   # scope it to one (or more) engines
devstack shared expose --off      # remove the publish, back to DNS-only
```

| Flag    | Default | Description |
|---------|---------|-------------|
| `--off` | false   | remove the host-port publish (back to DNS-only) |

Key properties:

- **Loopback-only** — ports bind `127.0.0.1`, never `0.0.0.0`.
- **Stable** — ports are ledger-allocated and reused across runs, so your saved
  GUI connection keeps working. Bases sit in the 5xxxx range (e.g. Postgres →
  `55432`) to avoid clashes.
- **Persistent** — the publish is an up-time overlay that survives `up`/`down`.
  It does **not** touch the deterministic generated compose.
- **Opt-in per service** — with no arguments every exposable engine is published;
  name engines to narrow it.

> **Naming note.** These are available **both** as top-level `devstack expose` /
> `devstack ports` (the canonical spelling) **and** as `devstack shared expose` /
> `devstack shared ports` (backward-compatible aliases). Use whichever you prefer —
> they run the same logic.

## Seeing the ports: `shared ports`

```bash
devstack shared ports
```

Read-only projection of what's currently published, with a ready-to-paste
connection string per port. No flags. Example output:

```
SERVICE           PORT        ADDRESS           CONNECT
shared-postgres   postgres    127.0.0.1:55432   postgres://devstack:devstack@127.0.0.1:55432/postgres?sslmode=disable
shared-redis      redis       127.0.0.1:56379   redis://127.0.0.1:56379
shared-minio      s3          127.0.0.1:59000   http://127.0.0.1:59000
shared-minio (console)  console  127.0.0.1:59001   http://127.0.0.1:59001

per-project database: postgres://<project>:<project>@127.0.0.1:55432/<project>?sslmode=disable
```

The credentials shown for the top-level string are the **shared engine's dev
admin creds** (loopback-only threat model). Your **per-project** database has its
own role and password (both default to the project name) — connect with the
`postgres://<project>:<project>@…/<project>` DSN. See [databases.md](databases.md)
for how those per-project roles and databases are created.

## Connect a GUI (DataGrip / TablePlus)

```bash
devstack up                 # engines running
devstack shared expose      # publish loopback ports
devstack shared ports       # copy the printed connection string
```

Then in DataGrip → **New → Data Source → PostgreSQL**, either paste the URL into
the connection-string field, or fill in the parts:

| Field    | Value                          |
|----------|--------------------------------|
| Host     | `127.0.0.1`                    |
| Port     | `55432` (from `shared ports`)  |
| User     | `devstack` (admin) or `<project>` |
| Password | `devstack` (admin) or `<project>` |
| Database | `postgres` (admin) or `<project>` |

For MinIO, point an S3 browser at `http://127.0.0.1:59000` (API) and open the web
console at the `console` URL. For host-side AWS CLI calls against LocalStack/MinIO,
use `devstack aws -- s3 ls` instead of exposing ports — see
[object-storage.md](object-storage.md).

## Recipes

**"I want to browse the dev DB in a GUI right now."**

```bash
devstack shared expose postgres
devstack shared ports          # paste the postgres:// string into your client
```

**"Free up RAM — stop engines nothing is using."**

```bash
devstack shared status         # confirm the ref counts are zero
devstack shared gc --stop
```

**"Ref counts look wrong after a crash."**

```bash
devstack shared doctor         # reconcile ledger ↔ live containers
```

**"Go back to DNS-only (no host ports)."**

```bash
devstack shared expose --off
```

## See also

- [Lifecycle](lifecycle.md) — `up`/`down`/`status` drive the ref counts.
- [Databases](databases.md) — per-project Postgres roles, databases and grants.
- [Object storage](object-storage.md) — tenant buckets on the shared MinIO.
- [Recovery, teardown & housekeeping](recovery.md) — `doctor --fix`, `workspace destroy`.
- [Command reference](command-reference.md) — every command, terse.

---

◀ [Lifecycle](lifecycle.md) · [Guide index](./README.md) · [Databases](databases.md) ▶
