# devstack — Migration guide

How to move an existing per-repo Docker dev setup onto devstack's **shared
infrastructure** model. The win: one warm Postgres/Redis/MinIO on a tool-owned
network for *all* your repos, instead of a duplicate database container per repo.

> **Automated import is planned.** `devstack import` (converting a legacy
> `devdock` `project.yaml` into `workspace.yaml` + `devstack.yaml`) is tracked as
> a roadmap item; the exact field mapping will follow the upstream `devdock`
> schema. Until it ships, use the manual mapping below — it applies to any
> `docker-compose`-based dev setup, not just devdock.

## The mental shift

| Legacy per-repo setup | devstack |
|---|---|
| Each repo ships its own `docker-compose.yml` with `db`, `redis`, … | Stateful engines are declared **once** in `workspace.yaml` under `shared:` |
| Every repo runs a duplicate Postgres | One shared Postgres; repos `uses:` it and reach it by the `shared-postgres` DNS alias |
| App services live in the same compose file | App services live in each repo's `devstack.yaml` under `services:` |
| Ports published to `localhost` per repo (and collide) | **No host ports by default** — DNS over the shared network; ports allocated from the ledger only on explicit request |
| Bring up: `docker compose up` per repo | Bring up: `devstack up` from anywhere in the workspace |

## Step 1 — split shared infra out of the repo

For each stateful service in your old compose file (databases, caches, object
stores), declare it once at the **workspace** level. Create a `workspace.yaml` at
the directory that contains your repos:

```yaml
apiVersion: devstack/v1
kind: Workspace
name: my-org
shared:
  postgres: { template: postgres, params: { version: "16" } }
  redis:    { template: redis }
projects:
  - { name: api, path: api }
  - { name: web, path: web }
```

`template:` selects a built-in shared engine (`postgres`, `redis`, `minio`). Drop
the corresponding `db`/`redis` services from each repo's old compose file.

## Step 2 — express each repo's app services

In each repo, add a portable `devstack.yaml`. Translate the *application*
services from the old compose file (not the databases — those are shared now):

```yaml
apiVersion: devstack/v1
kind: Project
name: api
services:
  api:
    template: php.laravel.nginx        # or node.vite, etc.
    uses: [workspace.shared.postgres]  # attach to the shared engine
    env:
      DATABASE_URL: "postgres://app:app@shared-postgres:5432/api"
```

Key translations:
- **`depends_on: [db]`** → `uses: [workspace.shared.postgres]`. The dependency is
  now a *workspace-graph* edge; devstack health-gates the shared engine before
  your service starts.
- **DB host** → the stable DNS alias `shared-postgres` (never the bare service
  name), reachable over the `devstack_shared` network.
- **Build context / Dockerfile** → carries over; reference it via the template or
  a custom service definition.
- **Named volumes for app data** → keep them in `devstack.yaml`; volumes for the
  *shared* engines are owned by the shared stack.

## Step 3 — secrets

Replace inline secret values and `.env` files with references:

```yaml
    env:
      STRIPE_KEY: "secret://sops/secrets.enc.yaml#stripe_key"
```

The value is resolved at `up` and injected only into the container's process env —
it never lands in a generated file. Authenticate providers once with
`devstack secrets login <provider>` (or set `DEVSTACK_<PROVIDER>_TOKEN`). See
[spec 04](specs/04-secrets.md) and [THREAT-MODEL.md](THREAT-MODEL.md).

## Step 4 — profiles (optional, for large workspaces)

If your old setup ran "everything at once", tag service slices so you can start
only what you need:

```yaml
# in a service:
    profiles: [frontend]
# or workspace-level cross-repo groups in workspace.yaml:
groups:   { core: { services: [api] } }
defaultProfile: core
```

Then `devstack up --profile frontend` starts only that slice plus the shared
services it transitively `uses`. Set `memoryBudgetMB` to get a warning when a
slice won't fit. ([spec 12](specs/12-service-profiles-and-selective-up.md))

## Step 5 — bring it up

```bash
devstack doctor      # verify Docker/compose/git + paths
devstack up          # network → shared (health-gated) → generate → compose up → hooks
devstack status      # what's running + shared ref counts
```

Re-running `up` is idempotent (satisfied phases skip). To tear a workspace down
without losing data, `devstack down`; to remove everything for a workspace,
`devstack workspace destroy`; to remove every machine-global artifact,
`devstack uninstall`. ([spec 13](specs/13-doctor-diagnostics-and-teardown.md))

## Rollback

devstack never edits your committed `workspace.yaml`/`devstack.yaml` on teardown,
and the old `docker-compose.yml` is untouched — keep it until you're confident.
The shared engines hold your dev data in named volumes; `down`/`workspace destroy`
(without `--purge`) and `doctor` preserve them.
