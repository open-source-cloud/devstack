# Projects

[devstack](../../README.md) · [Guide](./README.md) › Projects (devstack.yaml)

A **project** is a repo listed under `projects:` in `workspace.yaml`, with its own
`devstack.yaml` at its root. `workspace.yaml` says *which* repos are in the
workspace and *what shared engines* they can reach; `devstack.yaml` says *what
services* that one repo runs and *how they wire into the shared engines*.

## Add a project: the two-step flow

### Step 1 — register it in `workspace.yaml`

```yaml
# workspace.yaml
projects:
  - { name: api, path: services/api }
  - { name: web, path: services/web, git: git@github.com:acme/web.git }
```

`name` (an identifier matching `^[a-z][a-z0-9_-]{0,62}$`) and `path` (relative to
the workspace root) are required; `git` is optional and enables the multi-repo
clone/sync flow (see [Multi-repo git](multi-repo-git.md)).

### Step 2 — author the repo's `devstack.yaml`

```yaml
# services/api/devstack.yaml
apiVersion: devstack/v1
kind: Project
name: api
services:
  api:
    template: php.laravel.nginx
    uses: [workspace.shared.postgres, workspace.shared.redis]
```

Then validate and bring it up:

```bash
devstack config validate
devstack up api
```

## The `Service` fields

Each entry under `services:` is a service. These are the fields you'll use:

| Field | Type | What it does |
|---|---|---|
| `template` | string (required) | The service template to render, e.g. `php.laravel.nginx`, `node.vite`. |
| `params` | map | Template parameters, e.g. `{ phpVersion: "8.3" }`. |
| `uses` | []string | Shared engines this service consumes: `workspace.shared.<name>` (no attribute). |
| `ports` | map[string]int | Named container ports, e.g. `{ http: 8080 }`. |
| `env` | Env | Environment variables: `raw`, `prefixed`, `import` (see below). |
| `profiles` | []string | Compose profile tags — used with workspace `groups`/`--profile`. |
| `memoryMB` | int | Memory hint (counts toward the workspace `memoryBudgetMB`). |
| `resources` | Resources | `cpus`, `memoryMB`, `memoryReserveMB`, `pidsLimit`. |
| `platform` | string | Force a platform, e.g. `linux/amd64`. |
| `healthcheck` | Healthcheck | `kind` (tcp/http/https/exec/pg_isready/redis) + timing. |
| `dependsOn` | []DependsOn | Ordering: `service` + `condition` (healthy \| started). |

Full type details (every healthcheck sub-field, every validator rule) are in
[config-reference.md](config-reference.md).

### `uses` vs `env.import`

Both reference other services, but they do different jobs:

- **`uses`** declares a **dependency on a shared engine** — `workspace.shared.<name>`,
  no attribute. It's how a project *consumes* a shared engine (and how ref-counting
  knows the project needs it).
- **`env.import`** pulls **specific attributes** (host, port, user, …) from another
  service into this service's environment. Its `from` may be a shared engine **or
  another project's service**, and you list the `vars` you want.

Use `uses` to say "I depend on shared Postgres"; use `env.import` (or `env.raw`
with `${ref:...}`) to say "inject its host/port/credentials as env vars". Plain
literal config that isn't imported from anywhere goes in `env.raw`. See
[Environment variables](environment.md) for the full `env` story and
[Secrets](secrets.md) for `secret://`.

## Full example: Laravel + Postgres + Redis

```yaml
apiVersion: devstack/v1
kind: Project
name: api
services:
  api:
    template: php.laravel.nginx
    params: { phpVersion: "8.3" }
    memoryMB: 768
    platform: linux/amd64
    resources: { cpus: "1.5", memoryReserveMB: 256, pidsLimit: 512 }
    uses:
      - workspace.shared.postgres
      - workspace.shared.redis
    env:
      raw: { APP_ENV: "${profile}" }
      prefixed: { URL: "https://${self.host}" }
      import:
        - { from: workspace.shared.postgres, vars: [host, port, user, password, database] }
    ports: { http: 8080 }
    healthcheck:
      kind: http
      port: 8080
      path: /healthz
      expectStatus: "200-399"
      interval: 5s
      timeout: 3s
      retries: 12
      startPeriod: 20s
    dependsOn:
      - { service: workspace.shared.postgres, condition: healthy }
      - { service: workspace.shared.redis, condition: healthy }
hooks:
  firstRun:
    - name: migrate-and-seed
      run: exec
      service: api
      command: ["sh", "-lc", "php artisan migrate --force && php artisan db:seed --force"]
      workdir: /var/www/html
      env: { APP_ENV: "${profile}" }
      timeout: 5m
      retries: 3
      onFailure: abort
  postUp:
    - { name: cache-warm, run: host, command: ["true"], onFailure: warn }
```

At `up`, this project automatically gets a Postgres role + database named after it
(`api`), and `env.import` injects the connection attributes — no manual wiring.
Project-scope `hooks` run in the saga's hooks phase.

## Full example: Node front-end importing from another project

`env.import.from` can point at **another project's service**, which is how a
front-end reads, say, the API's host without hard-coding it.

```yaml
apiVersion: devstack/v1
kind: Project
name: web
services:
  web:
    template: node.vite
    profiles: [frontend, core]
    uses:
      - workspace.shared.postgres
    env:
      import:
        - { from: workspace.api.api, vars: [host] }
```

Here `workspace.api.api` is `workspace.<project>.<service>`. Cross-project
`import` edges are validated for cycles at config load. The `profiles: [frontend,
core]` tags let workspace `groups` / `up --profile` select this service.

## Recipes

**Bring up just one project (and its shared deps):**

```bash
devstack up api
```

**Open a shell in a project's service:**

```bash
devstack shell --project api          # or: devstack shell api
```

**Validate a project's config in isolation while editing:**

```bash
devstack config validate
devstack generate --project api --check   # report drift, write nothing
```

## See also

- [Environment variables](environment.md) — `env.raw` / `prefixed` / `import` in depth.
- [Templates](templates.md) — the service templates a project's `template:` names.
- [Databases](databases.md) — the per-project Postgres role/db and the `db` group.
- [The resource layer](resources.md) — declarative `resources:` in `devstack.yaml`.
- [config-reference.md](config-reference.md) — the full `devstack.yaml` schema.

---

◀ [Workspaces](workspaces.md) · [Guide index](./README.md) · [Templates ▶](templates.md)
