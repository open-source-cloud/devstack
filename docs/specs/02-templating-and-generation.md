# Spec 02 — Templating & generation engine

**Modules:** `internal/template`, `internal/merge`, `internal/generate` · **Milestone:** M1 · **Effort:** ~7w

## Purpose
Turn service templates + resolved config into deterministic, valid Docker Compose + build artifacts. Two distinct jobs that must not be conflated:
1. **Structured generation** → the compose document. Built **programmatically** as a typed model, validated through `compose-go/v2`. *Not* string-templated YAML.
2. **Text generation** → unstructured files (Dockerfiles, proxy/entrypoint configs, scripts) and **user-authored templates**. This is where a text-template engine is used.

> This split is the key refinement over devdock (which Jinja-rendered everything, including compose YAML). Typed schemas are the backbone; the template engine is a small, contained tool for text artifacts + extensibility.

## Decisions
- **Compose model is programmatic + validated by `compose-spec/compose-go/v2`** (Apache-2.0 — carry NOTICE). Marshal once with stable key ordering.
- **Text-template engine — OPEN ([Q-T](../OPEN-QUESTIONS.md)):**
  - **Recommended:** stdlib `text/template` + `Masterminds/sprig`, with **custom delimiters** (`[[ ]]` or `%{ }%`/`%= =%`/`%# #%`) so template syntax never collides with shell `${VAR}` / Dockerfile `$TAG`.
  - **Alternative:** `nikolalohinski/gonja/v2` (pure-Go Jinja2, first-class custom delimiters, inheritance/macros/filters) if you want richer file-based authoring for third-party templates.
  - Either way, **wrap behind `pkg/template`** so the engine is swappable/vendorable; reimplement the few custom filters yourself with golden tests.
- **Two render modes:** `renderText(src, ctx) -> []byte` (passthrough, for Dockerfiles/conf/scripts) and `renderYAML(src, ctx) -> map[string]any` (render → goccy unmarshal) — the latter only for *template* fragments, never the user's config.
- **Template = a directory** with `template.yaml` (carries `extends:`, `schemaVersion`, typed `params:` with defaults), an optional `build/` tree of file templates, and an optional `post_init.yaml`.
- **Inheritance at the structured layer**, not via the text engine: resolve the `extends` chain into an ordered list, render each layer with the same params, **deep-merge** in order: `extends` base → leaf `template.yaml` → `post_init.yaml` → project overrides.
- **Sources** behind a `TemplateSource { Resolve(ref) (fs.FS, error) }` interface: **embedded (`go:embed`)** for built-ins in v1; **git** and **OCI** (`oras-go/v2`) deferred to the versioned registry ([spec 19](19-template-registry.md), feature #11).
- **Change detection:** SHA-256 each build context's full rendered input set → `.devstack/state.json` / the ledger; only changed contexts get `docker compose build --no-cache <svc>`. Improves on devdock's flat `.rebuild` JSON list and is timestamp-independent (WSL2/9p-safe).
- **Writing:** single `writeIfChanged(path, bytes)` → compare, write only on diff, **atomic** (tmp in same dir + `os.Rename`). Deterministic output (sorted keys, `KeepTrailingNewline`).

## `${ref}` cross-service resolution
After merge, resolve cross-service references (`${ref:workspace.shared.postgres.host}`) in a post-render pass **against the workspace service graph** (injected resolver), not a single project — this is what lets a project template reference shared Postgres. Emits the env injection (e.g. `DATABASE_URL=postgres://<u>:<p>@shared-postgres:5432/<db>`).

## Deep-merge (`internal/merge`)
- Recursive map merge + scalar overwrite, **left-to-right**.
- ⚠️ **Lists:** the Python predecessor's `always_merger` **appends**; `knadh/koanf/maps.Merge` **replaces**. Default to **replace**, expose opt-in append via `$merge: append` (or `!append`/`!replace` tags). Document loudly — getting this wrong silently drops inherited volumes/env entries.
- ⚠️ `maps.Merge` does no copying (retains references into the source) — guard against shared-reference mutation.

## Verified constraints / gotchas
- **`compose-go/v2` is Apache-2.0** (not MIT); requires **Go 1.24**.
- Compose **round-trip normalization** (load→mutate→marshal) reorders/normalizes fields → churny diffs; control with golden output + the [Q-GEN](../OPEN-QUESTIONS.md) commit/gitignore decision.
- `goccy/go-yaml` randomizes map order — use **ordered structs / `MapSlice`** for deterministic output; it is **not byte-compatible** with yaml.v3 (e.g. `1.0` vs `1`) — regenerate golden files if you ever switch.
- If gonja is chosen: it's single-maintainer; set `StrictUndefined: true` so missing params fail fast (devdock's lax default hid bugs); custom-filter/whitespace behavior may diverge from CPython Jinja2 — golden-diff every template.
- Remote sources (later) execute as build instructions — same trust model as any compose file; pin by tag/digest, verify OCI digests.

## Acceptance criteria
- [ ] A `php.laravel.nginx` template that `extends: php.nginx` renders correctly merged compose + Dockerfiles.
- [ ] A Dockerfile template containing literal `${XDEBUG_HOST}`, `$TAG`, and `${VAR:-""}` renders without the template engine mangling them (delimiters don't collide).
- [ ] Byte-identical inputs produce byte-identical generated files (CI determinism check).
- [ ] Changing one Dockerfile's content marks only that build context for `--no-cache` rebuild; unrelated services are untouched.
- [ ] A missing required `param` fails fast with a clear error (not an empty-string render).
- [ ] `writeIfChanged` never leaves a half-written compose file on a simulated crash (atomic rename).
- [ ] The compose document validates against `compose-go/v2` before being written.

## Dependencies / consumers
Consumes `internal/config` (resolved model), `internal/workspace` (the service graph for `${ref}` + the external-network stanza), `internal/secrets` (emits the per-service secret env keys — see [spec 04](04-secrets.md)). Consumed by `internal/docker` (drives build/up on the generated files).

## Open questions
[Q-T](../OPEN-QUESTIONS.md) (engine choice), [Q-GEN](../OPEN-QUESTIONS.md) (commit vs gitignore generated artifacts).
