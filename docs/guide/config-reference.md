# Full config reference

[devstack](../../README.md) · [Guide](./README.md) › Full config reference

This is the field-by-field reference for both config files, the validator rules,
and the `${...}` / reference-path / `secret://` grammars. It's the deep reference
the topic pages link into. For the readable walkthroughs, start at
[workspaces.md](workspaces.md) and [projects.md](projects.md).

devstack uses a **two-file declarative model**:

- **`workspace.yaml`** — one at the workspace root. Shared engines + project list.
- **`devstack.yaml`** — one per project repo. The services that repo runs.

Both are parsed with `goccy/go-yaml`, so errors render as `file:line:col`.
Discovery walks up from the current directory to the nearest `workspace.yaml`
(stopping at the filesystem root or `$HOME`); `DEVSTACK_WORKSPACE` short-circuits
the walk.

Header constants (both files):

```yaml
apiVersion: devstack/v1     # required, must equal devstack/v1
kind: Workspace             # or Project
```

## Validator rules

Custom `validator/v10` rules used throughout the schema:

| Rule | Meaning |
|---|---|
| `dsname` | `^[a-z][a-z0-9_-]{0,62}$` — devstack resource name. |
| `duration` | Parses with Go `time.ParseDuration` (e.g. `5s`, `20s`, `5m`, `168h`). |
| `cpus` | A positive float string (e.g. `"1.5"`, `"2"`). |
| `platform` | `^[a-z0-9]+/[a-z0-9]+(/[a-z0-9]+)?$` (e.g. `linux/amd64`). |
| `dockerhost` | `^(ssh\|tcp\|unix\|npipe\|fd)://.+` — a Docker host URL. |

---

## workspace.yaml — Workspace

| YAML key | Type | Notes |
|---|---|---|
| `apiVersion` | string | required, `== devstack/v1` |
| `kind` | string | required, `== Workspace` |
| `name` | string | required, `dsname` |
| `aliases` | []string | dive `dsname` — advertised binary aliases (see [aliases.md](aliases.md)) |
| `profiles` | Profiles | env-overlay selector; `{ default: dev }` |
| `defaultProfile` | string | the service **slice** `up` starts with no `--profile` — a group name or `all` |
| `groups` | map[string]Group | named workspace service slices (spec 12) |
| `memoryBudgetMB` | int | warn when active services' `memoryMB` sum exceeds this |
| `secrets` | Secrets | `{ providers: [...] }` |
| `network` | Network | proxy + tunnel config |
| `backend` | BackendConfig | where the shared stack runs (spec 21); omit for local Docker |
| `hooks` | Hooks | workspace-scope lifecycle hooks |
| `shared` | map[string]SharedSvc | the shared engines |
| `projects` | []ProjectRef | dive — the repos in the workspace |

Sub-structs:

- **Profiles** — `default` (string, default overlay `dev`).
- **Group** — `services []string`, `memoryHintMB int`.
- **BackendConfig** — `context string` XOR `host string` (`dockerhost`); omit for local Docker.
- **Secrets** — `providers []SecretProvider`.
- **SecretProvider** — `name` (`dsname`), `kind` (`sops`\|`aws-sm`\|`aws-ssm`\|`infisical`\|`keyring`), `env`, `projectId`, `region`.
- **Network** — `proxy` + `tunnel`.
  - **Proxy** — `engine` (`caddy`\|`traefik`\|`nginx`), `httpsLocal bool`.
  - **Tunnel** — `provider`, `hostname`.
- **SharedSvc** — `template` (required), `params map[string]any`, `resources *Resources`, `platform`.
- **ProjectRef** — `name` (required, `dsname`), `path` (required, relative to workspace root), `git` (optional).

### Example (`workspace.yaml`)

```yaml
apiVersion: devstack/v1
kind: Workspace
name: acme
aliases: [rq, uranus]
profiles: { default: dev }
defaultProfile: core
groups:
  core:     { services: [api] }
  frontend: { services: [web], memoryHintMB: 1024 }
memoryBudgetMB: 4096
hooks:
  preUp:
    - { name: banner, run: host, command: ["true"] }
shared:
  postgres:
    template: postgres
    params: { version: "16" }
    resources: { cpus: "2", memoryMB: 1024, memoryReserveMB: 512 }
  redis:    { template: redis,    params: { version: "7" } }
  minio:    { template: minio }
projects:
  - { name: api, path: services/api }
  - { name: web, path: services/web }
```

---

## devstack.yaml — Project

| YAML key | Type | Notes |
|---|---|---|
| `apiVersion` | string | required, `== devstack/v1` |
| `kind` | string | required, `== Project` |
| `name` | string | required, `dsname` |
| `services` | map[string]Service | required, dive |
| `hooks` | Hooks | project-scope lifecycle hooks |
| `resources` | []ResourceDecl | dive — declarative data-plane resources (spec 27) |

### Service

| Field | Type | Notes |
|---|---|---|
| `template` | string | required — the service template |
| `params` | map[string]any | template parameter overrides |
| `uses` | []string | consume shared engines: `workspace.shared.<name>` (no attr) |
| `env` | Env | environment vars (see below) |
| `ports` | map[string]int | named container ports |
| `profiles` | []string | Compose profile tags |
| `memoryMB` | int | memory hint (budget accounting) |
| `resources` | Resources | container resource limits (below) |
| `platform` | string | `platform` rule, e.g. `linux/amd64` |
| `healthcheck` | Healthcheck | typed readiness check (below) |
| `dependsOn` | []DependsOn | ordering + readiness gates (below) |

### Resources (limits)

Lowers to compose `deploy.resources.limits` plus legacy `cpus`/`mem_limit`/`pids_limit`.

| Field | Type | Notes |
|---|---|---|
| `cpus` | string | `cpus` rule, e.g. `"1.5"` |
| `memoryMB` | int | hard memory limit |
| `memoryReserveMB` | int | soft reservation |
| `pidsLimit` | int | max PIDs |

### Healthcheck

| Field | Type | Notes |
|---|---|---|
| `kind` | string | required — `tcp`\|`http`\|`https`\|`exec`\|`pg_isready`\|`redis` |
| `port` | int | probe port |
| `path` | string | http/https path |
| `expectStatus` | string | `"200"` or a range `"200-399"` |
| `host` | string | probe host |
| `command` | []string | exec probe argv |
| `user`, `db` | string | pg_isready parameters |
| `auth` | string | redis auth (may be a `secret://`) |
| `interval`, `timeout`, `startPeriod` | duration | timing |
| `retries` | int | attempts before unhealthy |

### DependsOn

| Field | Type | Notes |
|---|---|---|
| `service` | string | required — intra-project name **or** `workspace.shared.<name>` |
| `condition` | string | `healthy`\|`started` (default `healthy`) |

### Env

| Field | Type | Notes |
|---|---|---|
| `raw` | map[string]string | literal KV, emitted verbatim, with `${...}` interpolation |
| `prefixed` | map[string]string | same, but the compose key becomes `<SERVICE>_<KEY>` |
| `import` | []Import | pull attributes from another service |

- **Import** — `from` (required, a reference path), `vars []string`.

See [environment.md](environment.md) for the zero-setup local-KV story.

### ResourceDecl (declarative resources)

| Field | Type | Notes |
|---|---|---|
| `uses` | string | required — `workspace.shared.<name>` |
| `kind` | string | `database`\|`user`\|`bucket`\|`lifecycle`\|`queue`\|`stream`\|`topic` |
| `name` | string | default = project name |
| `engine` | string | inferred from `uses` |
| `params` | map[string]any | kind-specific parameters |
| `credentials` | string | `predictable`\|`generated` |

No two resources may collide on `(engine, kind, name)` within a project. See
[resources.md](resources.md).

### Hooks & Hook

`Hooks` is a set of phase-keyed lists: `preUp`, `firstRun`, `postUp`,
`postPull`, `preDown` (each `[]Hook`).

| Hook field | Type | Notes |
|---|---|---|
| `name` | string | required, `dsname` |
| `run` | string | required — `host` or `exec` |
| `service` | string | target service (for `exec`) |
| `command` | []string | required, min 1 — argv, never shell-split |
| `workdir` | string | working directory |
| `env` | map[string]string | extra env for the hook |
| `timeout` | duration | per-run deadline |
| `retries` | int | retry count |
| `onFailure` | string | `abort`\|`warn`\|`continue` |
| `once` | bool | run at most once (idempotency ledger) |

### Example (`services/api/devstack.yaml`)

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

### Example (`services/web/devstack.yaml` — cross-project import)

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

---

## The `${...}` reference grammar

### Interpolation

Resolved by devstack at generate time. `StrictUndefined` — an unknown reference
is a hard error. Escape a literal `$` as `$$`.

| Expression | Resolves to |
|---|---|
| `${profile}` | the active env-overlay profile (e.g. `dev`) |
| `${workspace.name}` | the workspace name |
| `${env.NAME}` | host environment variable `NAME` (**error if unset**) |
| `${self.ATTR}` | an attribute of the current service |
| `${ref:PATH}` | a cross-service reference, e.g. `${ref:workspace.shared.postgres.host}` |

Note the sub-delimiters differ: `env.` and `self.` use a **dot**; `ref:` uses a
**colon**.

### Reference paths

Used by `uses`, `import.from`, and inside `${ref:...}`:

```
workspace.shared.<name>[.<attr>]
workspace.<project>.<service>[.<attr>]
```

`uses` must name a shared engine with **no** attribute. `import.from` may name a
shared engine **or** another project's service. Cycles over `import.from` edges
are rejected.

### Env resolution order

When generating a service's `environment:`, devstack layers in this order
(later wins on key collision):

```
template's own env → env.raw (interpolated, verbatim) →
env.prefixed (compose key <SERVICE>_<KEY>) → env.import
```

A `secret://` value in `env.raw` becomes a **valueless** env key; the value is
passed to the container via process env, never written to a generated file.

### `secret://` grammar

```
secret://<provider>/<path>#<key>?opt=val
```

Provider and path are required; the `#key` fragment is optional. `<provider>`
names an entry in `workspace.yaml` `secrets.providers[].name`.

| Backend (`kind`) | Form |
|---|---|
| `infisical` | `secret://p/NAME[#json.key]` |
| `aws-sm` | `secret://p/secret-id#json.key` |
| `aws-ssm` | `secret://p/param-name[#json.key]` |
| `sops` | `secret://p/path/to/file.enc.yaml#dot.path.key` |

See [secrets.md](secrets.md) for the full workflow.

## See also

- [Workspaces](workspaces.md) · [Projects](projects.md) — the readable walkthroughs.
- [Environment variables](environment.md) — `env.raw` / `prefixed` / `import` in depth.
- [Templates](templates.md) — `template.yaml`, params, `extends`, the built-in catalog.
- [Secrets](secrets.md) — `secret://` and providers.
- [Command reference](command-reference.md) — every command, terse.

---

◀ [Aliases & argv[0] dispatch](aliases.md) · [Guide index](./README.md) · [Command reference](command-reference.md) ▶
