# templates/

Built-in service templates, compiled into the binary via `go:embed`
(`embed.go`, ARCHITECTURE §6). Consumed by `internal/template` (resolution) and
`internal/generate` (compose model build). Implemented in **M1**.

## Built-ins

| Template | Kind | Notes |
|---|---|---|
| `postgres` | shared engine | `provides: postgres`; version-aware PGDATA mount (PG18+ moved it — DECISIONS D8) |
| `redis` | shared engine | `provides: redis` |
| `minio` | shared engine | `provides: minio` |
| `php.nginx` | project base | PHP-FPM build (`build/Dockerfile`); parent template |
| `php.laravel.nginx` | project | `extends: php.nginx`; adds Laravel env + entrypoint |
| `node.vite` | project | Node + Vite dev server build |
| `node.express` | project | Express dev server (`npm run dev`), source bind-mount + hot reload |
| `node.nestjs` | project | NestJS (`nest start --watch`), hot reload |
| `node.next` | project | Next.js (`next dev`), `WATCHPACK_POLLING` for WSL2 |
| `react.vite` | project | React + Vite dev server (HMR) |
| `bun.app` | project | Bun app (`bun run dev`), `oven/bun` base |
| `turborepo` | project (monorepo) | `turbo run dev` across packages; pairs with `devstack run` |

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
