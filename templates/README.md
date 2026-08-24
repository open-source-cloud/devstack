# templates/

Built-in service templates, compiled into the binary via `go:embed`
(`embed.go`, ARCHITECTURE §6). Consumed by `internal/template` (resolution) and
`internal/generate` (compose model build). Implemented in **M1**.

## Built-ins

| Template | Kind | Notes |
|---|---|---|
| `postgres` | shared engine | `provides: postgres`; version-aware PGDATA mount (PG18+ moved it — DECISIONS D8); exposes on `5432` |
| `mysql` | shared engine | `provides: mysql`; exposes on `3306` |
| `mariadb` | shared engine | `provides: mariadb` (MySQL-compatible); exposes on `3306` |
| `mongodb` | shared engine | `provides: mongodb`; exposes on `27017` |
| `cassandra` | shared engine | `provides: cassandra` (CQL); exposes on `9042` |
| `arangodb` | shared engine | `provides: arangodb` (multi-model + web UI); exposes on `8529` |
| `redis` | shared engine | `provides: redis`; exposes on `6379` |
| `minio` | shared engine | `provides: minio`; exposes on `9000`/`9001` |
| `php.nginx` | project base | PHP-FPM build (`build/Dockerfile`); parent template |
| `php.laravel.nginx` | project | `extends: php.nginx`; adds Laravel env + entrypoint |
| `node.vite` | project | Node + Vite dev server build |
| `node.express` | project | Express dev server (`npm run dev`), source bind-mount + hot reload |
| `node.nestjs` | project | NestJS (`nest start --watch`), hot reload |
| `node.next` | project | Next.js (`next dev`), `WATCHPACK_POLLING` for WSL2 |
| `react.vite` | project | React + Vite dev server (HMR) |
| `bun.app` | project | Bun app (`bun run dev`), `oven/bun` base |
| `turborepo` | project (monorepo) | `turbo run dev` across packages; pairs with `devstack run` |
| `kafka` | shared engine | `provides: kafka` (Redpanda, single binary, no ZooKeeper); dual advertised listeners |
| `nats` | shared engine | `provides: nats` (JetStream); exposes on `4222`, monitor on `8222` |
| `rabbitmq` | shared engine | `provides: amqp`; AMQP on `5672`, management UI on `15672` |
| `localstack` | shared engine | `provides: aws`; the AWS emulator, on `4566` |
| `ministack` | shared engine | `provides: aws`; a lighter AWS emulator, on `4566` |
| `valkey` | shared engine | `provides: valkey`; the Redis fork. `valkey-cli` healthcheck, no curl in the image |
| `timescaledb` | shared engine | `provides: timescaledb`; PGDATA is `/home/postgres/pgdata` — NOT the postgres image's path |
| `clickhouse` | shared engine | `provides: clickhouse`; HTTP `8123` / native `9000`. User+password are required or the server refuses network access |
| `neo4j` | shared engine | `provides: neo4j`; Bolt `7687`, browser `7474`. Password must be >= 8 chars |
| `etcd` | shared engine | `provides: etcd`; distroless image — exec-form healthcheck only, no shell |
| `consul` | shared engine | `provides: consul`; `-dev` agent, state in RAM. BUSL-1.1 licensed |
| `rustfs` | shared engine | `provides: rustfs`; S3-compatible, same ports and default creds shape as minio |
| `meilisearch` | shared engine | `provides: meilisearch`; on `7700`. DB format is tied to the minor version |
| `opensearch` | shared engine | `provides: opensearch`; on `9200`. Needs host `vm.max_map_count` >= 262144 |
| `mailpit` | shared engine | `provides: mailpit`; SMTP `1025`, inbox UI `8025`. `MP_DATABASE` is the persistence switch |
| `keycloak` | shared engine | `provides: keycloak`; `start-dev` on `8080`. No bare-major image tag exists |
| `jaeger` | shared engine | `provides: jaeger`; OTLP `4317`, UI `16686`. Spans are held in memory |
| `mosquitto` | shared engine | `provides: mosquitto`; MQTT `1883`. 2.1 ships a working anonymous config |
| `go.app` | project | Go + `air`, polling watcher (frameworks are libraries — one template) |
| `rust.app` | project | Rust + `bacon --headless -j run-long` (cargo-watch is unmaintained) |
| `python.app` | project base | Python + uv, venv at `/opt/venv`; parent template |
| `python.fastapi` | project | `extends: python.app`; `uvicorn --reload`, polling forced |
| `python.django` | project | `extends: python.app`; `runserver`, StatReloader already polls |
| `python.flask` | project | `extends: python.app`; `flask run --debug` |
| `elixir.phoenix` | project | `mix phx.server`; needs `inotify-tools`, and `config/dev.exs` must bind 0.0.0.0 |
| `ruby.rails` | project | `bin/rails server -b 0.0.0.0`; clears the stale pidfile on start |
| `java.spring` | project | `mvnw spring-boot:run` + devtools (which polls, but watches `target/classes`) |
| `dotnet.aspnet` | project | `dotnet watch --no-launch-profile`; SDK image already sets polling |
| `deno.app` | project | `deno run --watch`; ENTRYPOINT already execs `deno` |
| `vue.vite` | project | Vue + Vite HMR |
| `svelte.kit` | project | SvelteKit (`vite dev`); `npm install` triggers `svelte-kit sync` |
| `nuxt` | project | Nuxt; needs TWO ports (3000 + 24678 HMR websocket) |
| `astro` | project | Astro; port 4321, not Vite's 5173 |
| `smee` | project | smee.io webhook forwarder; outbound only, binds no port |

**Dev-mode hot reload.** The JS/TS app templates bind-mount the project source
(`..:/app`, since the generated compose lives in `<project>/.devstack/`) with an
anonymous `node_modules` volume, run `npm/bun install` then the dev server, and
set the framework's file-watch polling env for WSL2/9p reliability.

## A template is a directory

```
<name>/
  template.yaml      # metadata + the compose service: fragment (the only required file)
  post_init.yaml     # optional service:/volumes: fragment merged AFTER the extends chain
  build/             # optional file templates (Dockerfile, entrypoint, conf) → renderText
```

`template.yaml` carries:

- `schemaVersion`, `description`
- `extends: <parent>` — resolved into an ordered chain, rendered with the same
  params, then **deep-merged** base→leaf (lists replace by default; `$merge: append`
  opts in).
- `params:` — typed, defaulted, optionally `required: true` (a missing required
  param fails fast — it never renders an empty string).
- `provides` / `exports` / `defaultPort` — the shared-engine capability, the
  attributes a consumer may `import`, and the in-network port for `${ref:…port}`.
- `service:` — the compose service fragment. **Not** string-templated YAML: it is
  rendered for param substitution, decoded to a typed model, and validated through
  `compose-go/v2` before any file is written.

## Rules

- Compose documents are built as a **typed model validated through
  `compose-go/v2`** — never string-templated YAML.
- Text templating uses **custom `[[ ]]` delimiters** so it never collides with the
  shell `${VAR}` / Dockerfile `$TAG` / compose `${VAR:-default}` that legitimately
  appear, untouched, inside rendered files.
- The FuncMap is **deterministic** (no clock/random/uuid): identical inputs produce
  byte-identical output (CI asserts it via `make determinism`).

Scaffold a new template with `devstack template init <name>`, validate it with
`devstack template lint <dir>`.
