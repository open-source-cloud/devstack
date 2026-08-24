# Templates

[devstack](../../README.md) · [Guide](./README.md) › Templates

A **template** is a reusable service definition — a compose service fragment plus
its parameters, build files and (for engines) the connection attributes it
exports. You *use* a template by name in `devstack.yaml`/`workspace.yaml`, and you
can *author* your own with the `template` command group.

---

## Using a template

Reference a template by name and pass `params:`:

```yaml
# devstack.yaml
services:
  api:
    template: php.laravel.nginx
    params:
      phpVersion: "8.3"
      appEnv: local
```

```yaml
# workspace.yaml — shared engines are templates too
shared:
  postgres:
    template: postgres
    params: { version: "16" }
  redis:  { template: redis }
  minio:  { template: minio }
```

Every template declares its `params` with types and defaults; you only override
what you need. A missing **required** param fails fast.

### Built-in templates

| Template | provides | exports | defaultPort | key params (default) |
|---|---|---|---|---|
| `postgres` | postgres | host, port, user, password, database | 5432 | `version` ("18"), `rootUser` (devstack), `rootPassword` (devstack) |
| `redis` | redis | host, port | 6379 | `version` ("7") |
| `minio` | minio | host, port, accessKey, secretKey | 9000 | `rootUser` (devstackadmin), `rootPassword` (devstackadmin) |
| `mysql` | mysql | host, port, user, password, database | 3306 | `version` ("8"), `rootUser`, `rootPassword` |
| `mariadb` | mariadb | host, port, user, password, database | 3306 | `version`, `rootUser`, `rootPassword` |
| `mongodb` | mongodb | host, port, user, password, database | 27017 | `version`, `rootUser`, `rootPassword` |
| `cassandra` | cassandra | host, port | 9042 | `version` |
| `arangodb` | arangodb | host, port, user, password | 8529 | `version`, `rootPassword` |
| `valkey` | valkey | host, port | 6379 | `version` ("9"); Redis-compatible fork |
| `timescaledb` | timescaledb | host, port, user, password, database | 5432 | `version` ("pg18"); PGDATA is `/home/postgres/pgdata`, not the postgres image's path |
| `clickhouse` | clickhouse | host, port, user, password, database | 8123 | `version`; user/password are REQUIRED or the server refuses network access |
| `neo4j` | neo4j | host, port, user, password, database | 7687 | `rootPassword` (min 8 chars); Bolt on 7687, browser on 7474 |
| `etcd` | etcd | host, port, endpoint | 2379 | `version` (full `vX.Y.Z` — there is no `:latest`), `advertiseHost` |
| `consul` | consul | host, port, endpoint | 8500 | `version`, `logLevel`; `-dev` keeps state in RAM (BUSL-1.1) |
| `rustfs` | rustfs | host, port, accessKey, secretKey, endpoint | 9000 | `image`, `rootUser`, `rootPassword`; S3-compatible, MinIO-shaped defaults |
| `meilisearch` | meilisearch | host, port, password, endpoint | 7700 | `version`, `masterKey`; DB format is tied to the minor version |
| `opensearch` | opensearch | host, port, endpoint | 9200 | `version`, `heapSize`; needs host `vm.max_map_count` >= 262144 |
| `mailpit` | mailpit | host, port, endpoint | 1025 | `version` (leading `v`), `maxMessages`; SMTP on 1025, inbox UI on 8025 |
| `keycloak` | keycloak | host, port, user, password, endpoint | 8080 | `version` (no bare major tag), `rootUser`, `rootPassword` |
| `jaeger` | jaeger | host, port, endpoint | 4317 | `version`; OTLP gRPC 4317, UI 16686, spans held in memory |
| `mosquitto` | mosquitto | host, port | 1883 | `version`; 2.1 ships a working anonymous config, no volume (no persistence) |
| `php.nginx` | — (parent) | — | — | `phpVersion` ("8.3") |
| `php.laravel.nginx` | — (extends `php.nginx`) | — | — | `appEnv` (local) |
| `node.vite` | — | — | — | `nodeVersion` ("20"); runs `npm run dev` |
| `node.express` | — | — | — | `nodeVersion` ("20"); `npm run dev`, hot reload |
| `node.nestjs` | — | — | — | `nodeVersion` ("20"); `nest start --watch` |
| `node.next` | — | — | — | `nodeVersion` ("20"); `next dev`, `WATCHPACK_POLLING` |
| `react.vite` | — | — | — | `nodeVersion` ("20"); Vite HMR |
| `bun.app` | — | — | — | `bunVersion` ("1"); `bun run dev` |
| `turborepo` | — | — | — | `nodeVersion` ("20"); `turbo run dev` (monorepo) |
| `go.app` | — | — | — | `goVersion` ("1.27"), `port`; `air` hot reload with polling |
| `rust.app` | — | — | — | `rustVersion` ("1.98"), `port`; `bacon --headless -j run-long` |
| `python.app` | — (parent) | — | — | `pythonVersion` ("3.13"), `port`; uv + a venv outside the bind mount |
| `python.fastapi` | — (extends `python.app`) | — | — | `appModule`; `uvicorn --reload`, polling forced |
| `python.django` | — (extends `python.app`) | — | — | `allowedHosts`; StatReloader already polls |
| `python.flask` | — (extends `python.app`) | — | — | `appModule`, `port` (5000) |
| `elixir.phoenix` | — | — | — | `elixirVersion`, `port`; **edit `config/dev.exs` to bind 0.0.0.0** |
| `ruby.rails` | — | — | — | `rubyVersion`, `port`; clears the stale `server.pid` on start |
| `java.spring` | — | — | — | `image`, `port`; devtools polls, but a `.java` edit needs a recompile |
| `dotnet.aspnet` | — | — | — | `sdkVersion`, `port`; `dotnet watch --no-launch-profile` |
| `deno.app` | — | — | — | `denoVersion`, `entrypoint`, `port`; no polling fallback |
| `vue.vite` | — | — | — | `nodeVersion` ("24"), `port` (5173) |
| `svelte.kit` | — | — | — | `nodeVersion`, `port` (5173); `npm install` runs `svelte-kit sync` |
| `nuxt` | — | — | — | `nodeVersion`, `port` (3000), `hmrPort` (24678 — Nuxt needs TWO ports) |
| `astro` | — | — | — | `nodeVersion`, `port` (4321 — Astro's default, not Vite's) |
| `smee` | — | — | — | `target`, `channel`; forwards smee.io webhooks inward, binds no port |
| `kafka` (Redpanda) | kafka | host, port, adminPort | 9092 | `image` |
| `nats` | nats | host, port, monitorPort | 4222 | `version` ("2") |
| `rabbitmq` | amqp | host, port, mgmtPort | 5672 | `version` ("3"), `user` (devstack) |
| `localstack` | aws | host, port, endpoint, region | 4566 | `services` (s3,sqs,sns,kinesis,dynamodb), `region` (us-east-1) |
| `ministack` | aws | host, port, endpoint, region | 4566 | `services`, `region` |

List them any time:

```bash
devstack template list
```

The `exports` are exactly the attributes another service can pull via
[`env.import` / `${ref:...}`](environment.md).

---

## The `extends` mechanism

A template can build on another with `extends`. The chain is loaded root→leaf and
**deep-merged** (lists replace by default; opt in to appending with
`$merge: append`). Identity attributes are leaf-wins; a child's `build/` file
overrides the parent's same-path file.

`php.laravel.nginx` is the canonical example — it extends `php.nginx` and only
adds what's Laravel-specific:

```yaml
# php.laravel.nginx/template.yaml
schemaVersion: 1
extends: php.nginx
description: "Laravel application on PHP-FPM (extends php.nginx)."
params:
  appEnv: { type: string, default: local }
service:
  environment:
    APP_ENV: "[[ .params.appEnv ]]"
  entrypoint: ["/entrypoint.sh"]
  command: ["php-fpm"]
```

Merge order overall: `extends` base → leaf `template.yaml` → leaf
`post_init.yaml` → your project-level `params:`/overrides.

### `[[ ]]` delimiters

Templates render with Go `text/template` but **custom `[[ ]]` delimiters** (not
`{{ }}`), so template syntax never collides with shell/compose `${...}`. The data
context is `{"params": <effective params>}`, so you write `[[ .params.version ]]`.

Rendering is **deterministic**: the FuncMap has no clock or randomness. Available
functions: `default`, `coalesce`, `upper`, `lower`, `title`, `trim`,
`trimPrefix`, `trimSuffix`, `replace`, `contains`, `hasPrefix`, `hasSuffix`,
`join`, `split`, `quote`, `squote`, `indent`, `nindent`, `repeat`, `atoi`.
`missingkey=error` — referencing an undefined key is a hard failure.

---

## Authoring your own template

A template is a directory:

```
<name>/
  template.yaml     # required — the service fragment + params
  post_init.yaml    # optional — merged after template.yaml
  build/            # optional — Dockerfile/entrypoint/conf, rendered verbatim
```

`template.yaml` fields: `schemaVersion`, `extends`, `description`, `provides`
(capability string), `exports []string`, `defaultPort`, `params` (map of
`ParamSpec`), `service` (the compose service fragment), `volumes`. A `ParamSpec`
is `{ type, default, required, description }`.

A minimal custom template:

```yaml
# ~/.devstack/templates/mailhog/template.yaml
schemaVersion: 1
description: "MailHog SMTP capture for local email testing."
provides: smtp
exports: [host, port]
defaultPort: 1025
params:
  version: { type: string, default: "latest" }
service:
  image: "mailhog/mailhog:[[ .params.version ]]"
  restart: unless-stopped
```

Templates under `~/.devstack/templates/<name>/` **override a built-in of the same
name** and are chained ahead of the embedded set. See [The global store](store.md).

### Author + validate commands

```bash
devstack template init mailhog            # scaffold a new template directory
devstack template lint ./mailhog --show   # render with defaults + validate; print the doc
devstack template test ./mailhog          # render + assert it validates (and matches golden)
```

| Command | Key flag | Purpose |
|---|---|---|
| `template init <name>` | `--dir` (store templates dir) | scaffold a new template directory |
| `template lint <dir>` | `--show` | render with defaults, validate through compose-go, optionally print the rendered single-service doc |
| `template test <dir>` | — | render with defaults and assert it validates (and matches golden, if present) |

### `template new` — guided/flag-driven authoring

`template new` authors a template interactively (bare TTY) or fully from flags:

```bash
# an app template that extends the Node base and runs Next.js dev
devstack template new node.next \
  --kind app --extends node.vite \
  --param nodeVersion:string:20 \
  --port 3000 --dry-run

# an engine template that provides a capability and exports connection attrs
devstack template new clickhouse \
  --kind engine --provides clickhouse \
  --exports host,port --port 8123 \
  --param version:string:24 --dry-run
```

Key `template new` flags:

| Flag | Description |
|---|---|
| `--kind` | `app` or `engine` |
| `--extends` | base template to extend |
| `--param` | `name:type[:default][:required]` (repeatable) |
| `--provides` | capability string (engine) |
| `--exports` | exported connection attributes (engine) |
| `--port` | default port |
| `--dry-run` | print what would be written, write nothing |

See [Command reference](command-reference.md) for the complete flag set
(`--from`, `--base-image`, `--entrypoint`, `--golden`/`--no-golden`, `--regold`,
`--print-spec`, `--no-input`, …).

---

## Sharing templates (OCI registry)

Templates can be packaged and distributed as OCI artifacts, pinned by digest:

| Command | Purpose |
|---|---|
| `template push <dir> <ref>` | package a template dir and push it (optionally cosign-sign) |
| `template add <ref>` | register a remote template, pinning its resolved digest |
| `template update [name]` | re-resolve pinned templates to a new digest |
| `template diff <name>` | render-diff a pinned template vs. the latest remote tag |
| `template verify [name]` | re-pull and re-check digest (and signature) |
| `template ls` | list pinned remote templates and their cache state |

Full flags (signing, keyless-verify identity/issuer, `--allow-floating`) are in
[Command reference](command-reference.md).

---

## Recipes

### Bump a shared engine version

```yaml
shared:
  postgres: { template: postgres, params: { version: "17" } }
```

### A watch-mode framework dev server (author it now)

The `node.vite` pattern already builds from `build/Dockerfile` and runs a watch
command — a Next.js or NestJS template is just a new `template new` app that
changes the `command:` and bind-mounts source. See [What's next](whats-next.md)
for the framework-template outlook.

### Override a built-in locally

Drop a `~/.devstack/templates/postgres/template.yaml` to shadow the built-in
`postgres` for every workspace on this machine.

---

## See also

- [Projects](projects.md) — where services reference templates
- [Environment variables](environment.md) — `exports`, `${ref:...}`, `env.import`
- [Shared services & host access](shared-services.md) — engine templates in the workspace
- [The global store](store.md) — `~/.devstack/templates/` overrides
- [Command reference](command-reference.md) — every `template` flag

---

◀ [Environment variables](environment.md) · [Guide index](./README.md) · [Lifecycle](lifecycle.md) ▶
