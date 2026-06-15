# Spec 01 — Config schema, validation & DX

**Module:** `internal/config` · **Milestone:** M1 · **Effort:** ~7w

## Purpose
Define the clean-slate, two-file YAML config model and the loading/validation pipeline. Optimize for editor autocomplete and excellent `file:line:col` errors. This is the source of truth the whole tool reads.

## Decisions
- **Two files** (replaces devdock's single `project.yaml`):
  - `workspace.yaml` at the workspace root — the **shared layer**: shared services, aliases, secret providers, network/proxy/tunnel, project list.
  - `devstack.yaml` inside each repo — the **portable project layer**: services → template + params, env, and which shared services it `uses`.
  - Optional overlays `workspace.<env>.yaml` / `devstack.<env>.yaml`, deep-merged over the base when a profile is active (default `dev`).
- **Parse** with `goccy/go-yaml` (chosen for `FormatError` source positions + AST/path access — *not* for yaml.v3 compatibility; it is not a drop-in).
- **Validate in two layers:** JSON Schema draft 2020-12 (`santhosh-tekuri/jsonschema/v6`) for structure **and** as the editor artifact; `go-playground/validator/v10` + a **custom resolver** for semantics (cross-refs, cycles, capability matching).
- **`kind`/`apiVersion: devstack/v1`** header on every file for forward-compat.
- **Discovery:** walk up from CWD for `workspace.yaml`/`.devstack/` (git/go.mod style), stop at FS root or `$HOME`; `DEVSTACK_WORKSPACE` env + `--workspace` flag overrides. Projects resolved via explicit `projects[].path` (glob fallback).

## Schema (illustrative)

```yaml
# workspace.yaml
apiVersion: devstack/v1
kind: Workspace
name: acme
aliases: [rq, uranus]                 # binary invocable under these names
profiles: { default: dev }
secrets:
  providers:
    - { name: vault, kind: infisical, env: prod, projectId: "..." }
    - { name: aws,   kind: aws-secrets-manager, region: us-east-1 }
network:
  proxy:  { engine: caddy, httpsLocal: true }      # caddy|traefik|nginx
  tunnel: { provider: cloudflare, hostname: "*.acme.dev.example.com" }
shared:
  postgres: { template: postgres, params: { version: "16" } }
  redis:    { template: redis,    params: { version: "7" } }
  minio:    { template: minio }
projects:
  - { name: api, path: services/api, git: "git@github.com:acme/api.git" }
  - { name: web, path: services/web, git: "git@github.com:acme/web.git" }
```

```yaml
# services/api/devstack.yaml
apiVersion: devstack/v1
kind: Project
name: api
services:
  api:
    template: php.laravel.nginx
    params: { phpVersion: "8.3" }
    uses:                                   # consume SHARED services
      - workspace.shared.postgres
      - workspace.shared.redis
    env:
      raw:      { APP_ENV: "${profile}" }
      prefixed: { URL: "https://${self.host}" }
      import:                               # cross-service refs
        - { from: workspace.shared.postgres, vars: [host, port, user, password, database] }
    ports: { http: 8080 }
```

## Interpolation grammar (resolved by us — not shell, not the template engine)
A small, typed, testable grammar using `${...}`:
- `${env.NAME}` — host env var
- `${self.<attr>}` — current service attribute (e.g. `host`)
- `${ref:workspace.shared.postgres.host}` — explicit cross-service reference
- `${profile}`, `${workspace.name}`
- `$$` — literal dollar

This is deliberately narrower and more diagnosable than devdock's `${service.var}`. Shell-style `${VAR:-default}` semantics apply **only inside generated compose/env files** (use `mvdan.cc/sh/v3/expand` there if needed), never in config.

## Behavior
1. Discover workspace root (walk-up, cached).
2. Parse `workspace.yaml` + each `devstack.yaml` with goccy (retain positions).
3. **Layer A** — validate each against its embedded JSON Schema; emit json-pointer errors.
4. Apply profile/env overlay merge (maps merge, scalars replace, **lists replace by default**, opt-in `$merge: append`).
5. Unmarshal into Go structs (immutable thereafter — config is loaded once; koanf/the loaded model is not goroutine-safe for concurrent mutation).
6. **Layer B** — `validator/v10` field rules + custom resolver: every `uses`/`import` resolves (`workspace.shared.<name>` exists; imported `vars` are actually `exported` by the target template), template names exist, no cycles. Consider **capability matching** (`provides: postgres`) so `uses` can match by capability, not just name — improves repo portability across workspaces.
7. Render all errors as `file:line:col` + caret snippet via `goccy` `FormatError`.

## Verified constraints / gotchas
- **`goccy/go-yaml` is not yaml.v3-compatible** — different map ordering and stricter parsing; test round-trips; pin Go ≥ 1.21 (project floor is 1.25 anyway).
- **`jsonschema/v6` format assertions are OFF by default** — enable explicitly or `format: uri/email/...` silently passes.
- **The editor's `yaml-language-server` is AJV-based**, not spec-conformant with the Go validator on edge keywords — the **Go validator is the source of truth**; keep the schema to broadly-supported keywords; test in VS Code + JetBrains + neovim.
- **`validator/v10`:** no built-in reference resolution (implement as a struct-level validator reading the name index from `context.Context`); **panics on bad tags** (cover every tag with tests + `recover()` around `Validate`); requires **Go 1.25**; "call for maintainers" — pin it.
- **JSON-Schema↔struct drift** is the maintenance risk — CI round-trip test (validate fixtures against schema *and* unmarshal them); optionally scaffold the schema from structs with `invopop/jsonschema` and diff against the committed file.

## Acceptance criteria
- [ ] A workspace with 2 projects + 3 shared services loads, validates, and resolves all `uses`/`import` refs.
- [ ] A dangling `uses: workspace.shared.kafka` produces a `file:line:col` error naming the missing service and suggesting valid targets.
- [ ] A reference cycle is detected and reported (not a hang/stack overflow).
- [ ] VS Code (redhat.vscode-yaml) gives autocomplete + inline validation from the published schema + modeline.
- [ ] Profile overlay merge: `devstack.prod.yaml` correctly replaces scalars and (default) replaces lists; `$merge: append` appends.
- [ ] CI fails if the committed JSON Schema drifts from the Go structs.

## Dependencies / consumers
Consumes nothing. Consumed by **every** other module. The `${ref}` resolver must resolve against the **workspace** service graph (provided by `internal/workspace`), not a single project — inject a resolver interface.

## Open questions
[Q-GEN](../OPEN-QUESTIONS.md) (committed vs gitignored artifacts affects what config drives), [Q-MIGRATE](../OPEN-QUESTIONS.md) (`devstack import` from devdock `project.yaml`).
