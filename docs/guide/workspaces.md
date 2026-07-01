# Workspaces

[devstack](../../README.md) · [Guide](./README.md) › Workspaces (workspace.yaml, init)

A **workspace** is a directory tree with a `workspace.yaml` at its root. It
declares the shared engines (the warm Postgres/Redis/MinIO/… every project reuses)
and lists the projects. devstack discovers it by **walking up from your current
directory** to the first `workspace.yaml` (stopping at the filesystem root or
`$HOME`); the `DEVSTACK_WORKSPACE` environment variable short-circuits discovery.

## Scaffold one: `init`

```bash
devstack init
```

With a TTY and no flags, `init` runs an interactive wizard. With flags (or
`--no-input`) it is fully scriptable. It only authors `workspace.yaml` — **no
lock, no Docker, and it never recreates a stateful service**.

| Flag | Default | Description |
|---|---|---|
| `--name` | "" | Workspace name. |
| `--profile` | dev | Default env-overlay profile. |
| `--service` | *(none)* | Shared services to add, e.g. `postgres@16,redis,minio` (repeatable). |
| `--param` | *(none)* | Per-service param `svc.key=value` (repeatable). |
| `--project` | *(none)* | A project: `name=path[,git=url]` (repeatable). |
| `--alias` | *(none)* | Workspace command alias (repeatable). |
| `--from-store` | false | Seed shared services from the global store. |
| `--out` | "" | Output path (default: `./workspace.yaml`). |
| `--dry-run` | false | Print instead of writing. |
| `--force` | false | Overwrite an existing file (backs it up first). |
| `--no-input` | false | Flags only; never prompt. |

```bash
# Scriptable: two shared engines + two projects, no prompts
devstack init --no-input \
  --name acme \
  --service postgres@16,redis \
  --param postgres.version=16 \
  --project api=services/api \
  --project web=services/web,git=git@github.com:acme/web.git
```

### Minimal `workspace.yaml`

```yaml
apiVersion: devstack/v1
kind: Workspace
name: acme
shared:
  postgres: { template: postgres, params: { version: "16" } }
  redis:    { template: redis }
projects:
  - { name: api, path: services/api }
```

Every file starts with the header `apiVersion: devstack/v1` and `kind: Workspace`.
The `name` must match `^[a-z][a-z0-9_-]{0,62}$`.

## The main fields

Below is a tour of the fields you'll actually reach for. For the exhaustive schema
(every type, every validator rule), see [config-reference.md](config-reference.md).

### `shared` — the shared engines

A map of engine name → service definition. Each entry names a `template` and
optionally overrides `params`, `resources`, and `platform`.

```yaml
shared:
  postgres:
    template: postgres
    params: { version: "16" }
    resources: { cpus: "2", memoryMB: 1024, memoryReserveMB: 512 }
  redis:  { template: redis, params: { version: "7" } }
  minio:  { template: minio }
```

Projects consume these by reference — `workspace.shared.postgres`, reached in
containers at the DNS alias `shared-postgres`. Built-in engine templates include
`postgres`, `redis`, `minio`, `kafka` (Redpanda), `nats`, `rabbitmq`, `localstack`,
and `ministack`. See [Templates](templates.md) and
[Shared services](shared-services.md).

### `projects` — the project list

A list of `name` / `path` (relative to the workspace root) / optional `git`.

```yaml
projects:
  - { name: api, path: services/api }
  - { name: web, path: services/web, git: git@github.com:acme/web.git }
```

Adding a repo here is step one of adding a project — step two is authoring its
`devstack.yaml`. See [Projects](projects.md).

### `profiles`, `groups`, `defaultProfile` — what `up` starts

These control **which** services `up` starts and **which env overlay** they run in.

- `profiles.default` — the active env-overlay profile (default `dev`); this is
  what `${profile}` resolves to in your config.
- `groups` — named service slices (each a list of service names, with an optional
  `memoryHintMB`).
- `defaultProfile` — the slice `up` starts when you don't pass `--profile`: a group
  name, or `all`.

```yaml
profiles: { default: dev }
defaultProfile: core
groups:
  core:     { services: [api] }
  frontend: { services: [web], memoryHintMB: 1024 }
```

```bash
devstack up                 # starts the "core" group (defaultProfile)
devstack up -p frontend     # starts just the "frontend" slice
devstack generate --profile test   # render with the test overlay
```

### `memoryBudgetMB`

A soft budget. devstack **warns** when the summed `memoryMB` of active services
exceeds it.

```yaml
memoryBudgetMB: 4096
```

### `network` — proxy + tunnel

```yaml
network:
  proxy:  { engine: caddy, httpsLocal: true }
  tunnel: { provider: cloudflared, hostname: acme.example.com }
```

`proxy.engine` is `caddy` | `traefik` | `nginx`. See [Local HTTPS & DNS](networking.md)
and [Public tunnels](tunnels.md).

### `backend` — where the shared stack runs

By default (omitted / nil) the shared stack runs on local Docker. Set exactly one
of `context` or `host` to target a remote daemon.

```yaml
backend: { context: my-remote-context }
# or
backend: { host: "ssh://user@host" }
```

`host` must match `^(ssh|tcp|unix|npipe|fd)://.+`. A machine-global default can
also live in the global store; `workspace.yaml` overrides it. See [The global
store](store.md).

### `hooks` — workspace-scope lifecycle hooks

Lists of commands keyed by phase (`preUp`, `firstRun`, `postUp`, `postPull`,
`preDown`). Each hook runs on the `host` or `exec`s in a service.

```yaml
hooks:
  preUp:
    - { name: banner, run: host, command: ["true"] }
```

Hook `command` is an argv list — **never shell-split**. Project-scope hooks live in
`devstack.yaml`; see [Projects](projects.md).

## Inspect & validate: `config`

```bash
devstack config validate     # validate workspace + every referenced devstack.yaml
devstack config show         # print a summary of the resolved configuration
```

`config` loads `workspace.yaml` (walking up from the current directory) plus every
referenced `devstack.yaml`, validates structure and cross-references, and renders
errors as `file:line:col`. Run `config validate` before `up` (and in CI) to catch
bad references early.

## Recipes

**Seed a workspace from your global store's shared defaults:**

```bash
devstack init --no-input --name acme --from-store --project api=services/api
```

**Preview what `init` would write, without touching disk:**

```bash
devstack init --name acme --service postgres@16,redis --dry-run
```

**Start only the frontend slice for a quick UI session:**

```bash
devstack up -p frontend
```

## See also

- [Projects](projects.md) — author `devstack.yaml` and its services.
- [Shared services & host access](shared-services.md) — the shared engines in depth.
- [The resource layer](resources.md) — declarative `resources:` and the `resource` group.
- [Multi-repo git](multi-repo-git.md) — the `ws` group for the `projects[].git` repos.
- [config-reference.md](config-reference.md) — the full `workspace.yaml` schema.

---

◀ [Installation](installation.md) · [Guide index](./README.md) · [Projects ▶](projects.md)
