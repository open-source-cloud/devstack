# Concepts & the mental model

[devstack](../../README.md) · [Guide](./README.md) › Concepts & the mental model

devstack is a single static Go binary that manages Docker-based dev environments
and **shares infrastructure across projects** — one warm Postgres/Redis/MinIO for
many repos, instead of a duplicate stack per repo. This page is the mental model
everything else in the guide builds on.

## The four nouns

| Term | What it is |
|---|---|
| **Workspace** | A directory tree with a `workspace.yaml` at its root. It declares the shared engines and lists the projects. This is the unit devstack operates on. |
| **Project** | A repo (a directory listed under `projects:` in `workspace.yaml`) with its own `devstack.yaml`. It declares the app services that repo runs. |
| **Shared service** | A warm engine (Postgres, Redis, MinIO, Kafka, …) declared once under `shared:` and consumed by many projects. Reached over the shared network by DNS. |
| **Ledger** | A machine-global SQLite database (under XDG, not committed) that ref-counts shared services, tracks allocated host ports, and records provisioning ownership. It is **keyed by Docker context** so WSL2's two daemons never share counts. |

```yaml
# workspace.yaml (root) — the shared engines + the project list
apiVersion: devstack/v1
kind: Workspace
name: acme
shared:
  postgres: { template: postgres, params: { version: "16" } }
  redis:    { template: redis }
projects:
  - { name: api, path: services/api }
  - { name: web, path: services/web }
```

```yaml
# services/api/devstack.yaml (per repo) — the app services
apiVersion: devstack/v1
kind: Project
name: api
services:
  api:
    template: php.laravel.nginx
    uses: [workspace.shared.postgres, workspace.shared.redis]
```

## The two-file model

State that you own lives in exactly two committed files:

1. **`workspace.yaml`** — one per workspace, at the root. Shared engines + project
   list + workspace-wide settings.
2. **`devstack.yaml`** — one per project repo. The services that repo runs.

Everything else devstack needs (ref counts, port allocations, provisioning
ownership, the alias registry, the template cache) is **machine-global, under
XDG, and not committed** — it is derived state that devstack can rebuild. See
[Workspaces](workspaces.md) and [Projects](projects.md) for the field-by-field
walkthrough, and [config-reference.md](config-reference.md) for the full schema.

## The `devstack_shared` network + `shared-*` DNS aliases

There is exactly one tool-owned bridge network, pinned by name:

```
devstack_shared
```

Both the shared stack and every project stack join it. Because Compose refuses to
create or remove `external: true` networks, **devstack owns creating and cleaning
it up**. Shared services are reached by **stable DNS aliases**, never the bare
service name:

```
shared-postgres    shared-redis    shared-minio    …
```

So a project's app connects to `shared-postgres:5432` over the shared network —
the same alias in every workspace, no matter which container is backing it.

## No host ports by default

By default devstack publishes **no host ports**. Services talk to shared engines
over `devstack_shared` via the `shared-*` DNS aliases — that is all container
traffic needs. Host ports are opt-in, allocated from the ledger (inside the lock)
only when you explicitly ask for host reachability:

```bash
devstack shared expose postgres     # publish shared Postgres on a stable 127.0.0.1 port
devstack shared ports               # show the published ports + connection strings
```

See [Shared services & host access](shared-services.md) for the details. (These
two verbs are currently spelled `shared expose` / `shared ports`; a branch is
promoting them to top-level `expose` / `ports`.)

## Stateless CLI, no daemon

There is no background process (in v1). Every invocation does the same thing:

```
load config + ledger → take a coarse cross-process lock before mutating shared
state → drive Docker (docker compose for lifecycle; the Engine SDK read-only) → exit
```

Consequences worth internalising:

- **No autostop.** Shared engines stay warm until you reclaim them
  (`shared gc --stop`).
- **Ref-count drift self-heals lazily** on the next command, `shared gc`, or
  `doctor --fix` / `doctor --rebuild-state`.
- **Concurrency is safe by design.** Two terminals, an IDE + a terminal, a watch
  script — anything that mutates the ledger or the shared stack goes through one
  coarse advisory lock. Reads are lock-free snapshots.

## The `up` saga, phase by phase

`devstack up` is an **idempotent saga**: each phase records its state, so a re-run
skips satisfied work and a crashed run resumes where it stopped. Failure
compensates the mutating phases (refs, containers) but **never destroys data** —
volumes and databases survive.

```
devstack up
  │
  ├─ 1. Preflight ............ probe Docker/compose/git       (skip: --no-preflight)
  ├─ 2. Network .............. ensure the devstack_shared external network exists
  ├─ 3. Clone / sync ......... bring project repos onto disk  (skip: --skip-clone)
  ├─ 4. Generate ............. render compose + build artifacts from config+templates
  ├─ 5. Shared infra ......... up the shared engines, then health-gate them
  │                            (deadline: --health-timeout)
  ├─ 6. Provision ............ per-project Postgres role + database
  │                            (skip: --no-provision)
  ├─ 7. Project stacks ....... compose up -d the selected services
  │                            (--build / --rebuild, slice with --profile/-p)
  └─ 8. Hooks ................ run preUp / firstRun / postUp   (skip: --no-hooks)
```

Because the saga is idempotent, `devstack up` is also the everyday "make sure
everything is running" command — see [Lifecycle](lifecycle.md).

## See also

- [Installation, doctor & updates](installation.md) — get the binary and verify your host.
- [Workspaces](workspaces.md) — author `workspace.yaml` and run `init`.
- [Projects](projects.md) — author `devstack.yaml` and its services.
- [Shared services & host access](shared-services.md) — the shared engines, `expose`, `ports`.
- [config-reference.md](config-reference.md) — the full two-file schema.

---

◀ [devstack index](../../README.md) · [Guide index](./README.md) · [Installation ▶](installation.md)
