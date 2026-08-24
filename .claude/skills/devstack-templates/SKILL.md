---
name: devstack-templates
description: >-
  Use when authoring or editing a devstack service template — a directory
  holding template.yaml, an optional build/ tree and golden.yaml — or when
  asked to add support for a new database, engine, language or framework to
  devstack. Covers the engine-versus-app split, the [[ ]] delimiters and the
  deterministic FuncMap, params/extends/provides/exports, the rule that
  metadata keys are never templated, list-merge semantics, and the template
  new → lint → test loop.
license: Apache-2.0
allowed-tools: Bash(devstack:*)
---

Commands below are written as `devstack`. If this machine installed an alias
(`rq`, `uranus`), substitute it.

## What a template is

A template is a **directory** that renders one compose service. Every service in
a devstack workspace comes from one.

```
<name>/
  template.yaml     REQUIRED — metadata plus the compose service fragment
  build/            optional — Dockerfile, entrypoint.sh, nginx.conf, rendered verbatim
  golden.yaml       optional — a byte-for-byte fixture asserted by `devstack template test`
  post_init.yaml    optional — merged after the whole extends chain
```

The directory name is the template ref. Dots are allowed and are how families are
named: `php.nginx`, `php.laravel.nginx`.

Templates resolve from three sources, first match wins: an OCI-pinned remote
template, then `~/.devstack/templates/<name>/`, then the built-ins compiled into
the binary. Dropping a directory into `~/.devstack/templates/postgres/` therefore
shadows the built-in `postgres` for every workspace on the machine.

## Engines and apps are different things

This is a hard branch, not a style preference.

**An engine** is shared infrastructure (postgres, redis, minio, kafka). It uses
`image:`, declares `provides:` and `exports:` and `defaultPort:`, and usually
declares a named volume. It must **never** have a `build:` key — generation
rejects a shared service that tries to build.

**An app** is a project service (node.next, php.laravel.nginx). It uses
`build: { context: build, dockerfile: Dockerfile }` with a `build/` tree, and
must **never** declare `provides:` — `provides:` is what marks a template as
usable in `workspace.yaml`'s `shared:` block.

## Read a real one first

The built-ins are correct, current, and the best possible reference. Before
authoring, read one of the same kind:

```bash
devstack template list --json                # every template with its metadata
devstack ai docs guide/templates             # the full authoring guide
```

## The manifest

```yaml
schemaVersion: 1
extends: php.nginx            # optional parent ref
description: "One line describing the service."
provides: postgres            # ENGINES ONLY — the capability it satisfies
exports: [host, port, user, password, database]   # attrs consumers may import
defaultPort: 5432             # the in-network port ${ref:...port} resolves to
params:
  version:
    type: string              # string | int | bool (advisory in v1)
    default: "18"
    required: false
    description: "Image tag."
service:                      # the compose service fragment
  image: "postgres:[[ .params.version ]]"
volumes:                      # top-level named volumes
  pgdata: {}
```

## Templating: `[[ ]]`, not `{{ }}`

The engine is Go's `text/template` with the delimiters changed to `[[` and `]]`.
That is deliberate: it lets shell `${VAR}`, Dockerfile `$TAG` and compose
`${VAR:-default}` pass through untouched, so a `build/Dockerfile` can use both
syntaxes at once.

The only data in scope is `.params`:

```yaml
image: "postgres:[[ .params.version ]]"
```

`missingkey=error` is set, so referencing an undeclared param is a hard failure,
never a silent empty string.

**Three rules that will bite you:**

1. **Metadata keys are parsed UNRENDERED.** `schemaVersion`, `extends`,
   `description`, `provides`, `exports`, `defaultPort` and `params` are read
   before any templating runs. A `[[ ]]` action in any of them is silently
   meaningless — so the linter makes it a hard error. Only `service:` and
   `volumes:` are rendered.
2. **The FuncMap is deterministic on purpose.** There is no `now`, no `uuid`, no
   `randAlphaNum`, no environment access, because byte-identical output is a
   CI-asserted requirement.
3. **Argument order is pipeline-style — the data comes last**, which is the
   opposite of the `strings` package: `trimPrefix "v" .params.tag`,
   `replace "-" "_" .params.name`, `contains "alpine" .params.image`,
   `join "," .params.list`, `indent 4 .params.block`.

Available functions: `default` `coalesce` · `upper` `lower` `title` · `trim`
`trimPrefix` `trimSuffix` · `replace` `contains` `hasPrefix` `hasSuffix` ·
`join` `split` · `quote` `squote` · `indent` `nindent` `repeat` · `atoi`.

`atoi` parses the *leading* integer (`"9.6"` → 9), which is what makes
version-conditional fragments work with the builtin `lt`/`ge`:

```yaml
volumes:
  - "pgdata:[[ if lt (atoi .params.version) 18 ]]/var/lib/postgresql/data[[ else ]]/var/lib/postgresql[[ end ]]"
```

## extends and the merge

`extends` renders the parent, then deep-merges the child over it. Order is:
parent → child `template.yaml` → child `post_init.yaml` → the project's overrides.

**Lists REPLACE by default.** A child declaring `volumes:` replaces the parent's
list entirely. Opt into appending with `$merge: append`. This is the single most
surprising merge behavior; check it whenever a parent's list vanishes.

`provides`, `exports` and `defaultPort` inherit leaf-wins. Files in `build/`
merge by path, so a child's `build/Dockerfile` replaces the parent's.

## The authoring loop

Use the builder rather than hand-writing the directory — it emits a deterministic,
correct skeleton for the kind you pick:

```bash
devstack template new mysvc --kind engine --print-spec > spec.yaml   # inspect the plan
devstack template new mysvc --kind engine --from spec.yaml           # materialize it
devstack template lint <dir> --show                                  # lints + rendered compose
devstack template test <dir>                                         # compare against golden.yaml
```

`--print-spec` → `--from` round-trips byte-stably, so an agent can generate the
spec, show it to a human, and materialize exactly what was reviewed.

`lint` runs three checks and then validates the rendered service through
`compose-go`:

| Check | Severity | Meaning |
|---|---|---|
| meta-templating | **error** | a `[[ ]]` action outside `service:`/`volumes:` |
| param-type | warning | a `default` that does not parse as its declared `type` |
| delimiter-collision | warning | a `build/` file containing a literal `[[` that is not a valid action |

## Using a template

```yaml
# workspace.yaml — engines only (templates that declare provides:)
shared:
  postgres: { template: postgres, params: { version: "18" } }
```

```yaml
# devstack.yaml — apps
services:
  api:
    template: node.next
    params: { nodeVersion: "22" }
    uses: [workspace.shared.postgres]
```
