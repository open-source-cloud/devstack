# The global store (~/.devstack)

[devstack](../../README.md) · [Guide](./README.md) › The global store (~/.devstack)

The **store** is devstack's machine-global home directory. It holds the
definition of the shared services every workspace can attach to, your custom
templates, the global shared stack's generated artifacts, and snapshots. It
lives at `~/.devstack` (overridable with `$DEVSTACK_HOME`).

```bash
devstack store init      # create ~/.devstack with config.yaml, templates/, shared/
devstack store path      # print the home directory
devstack store show      # print the store config (the global shared services)
```

The store is **human-editable and not tied to any one workspace** — it is the
place to keep templates and shared-service defaults you want to reuse across
every repo on the machine.

## Layout

```
~/.devstack/               # or $DEVSTACK_HOME
├── config.yaml            # store config: the global shared services + defaults
├── templates/             # custom templates; override built-ins by name
├── shared/                # the global shared stack's generated artifacts
└── snapshots/             # db snapshot/restore captures, keyed by <workspace>/
```

| Path | What lives here |
|---|---|
| `config.yaml` | The store config (`kind: Store`) — global shared engines, backend, template lockfile, telemetry consent. |
| `templates/` | Custom service templates. A template here **overrides a built-in of the same name** and is available to every workspace. |
| `shared/` | Generated compose + build artifacts for the global shared stack. |
| `snapshots/` | Captures from [`db snapshot`](data-lifecycle.md), keyed by workspace so two checkouts never collide. |

## `store init`

Creates `~/.devstack` (or `$DEVSTACK_HOME`) with a seed `config.yaml` and the
`templates/` and `shared/` directories. No lock, no Docker.

```bash
devstack store init
devstack store init --force    # overwrite an existing config.yaml
```

| Flag | Default | Description |
|---|---|---|
| `--force` | false | overwrite an existing config file |

The seeded `config.yaml`:

```yaml
apiVersion: devstack/v1
kind: Store
shared:
  postgres: { template: postgres, params: { version: "16" } }
  redis:    { template: redis }
  minio:    { template: minio }
telemetry: { enabled: false }
```

## `store path` and `store show`

```bash
devstack store path      # e.g. /home/you/.devstack   (respects $DEVSTACK_HOME)
devstack store show      # print the resolved store config; add --json to script it
```

`store path` is handy in scripts (`cd "$(devstack store path)/templates"`);
`store show` renders the global shared-service definition.

## config.yaml schema (`kind: Store`)

| YAML key | Type | Notes |
|---|---|---|
| `apiVersion` | string | `devstack/v1` |
| `kind` | string | `Store` |
| `shared` | map[string]SharedSvc | global shared engines — same shape as `workspace.yaml`'s `shared:` (`template`, `params`, `resources`, `platform`). |
| `backend` | BackendConfig | machine-global default for **where** the shared stack runs; a `workspace.yaml` `backend:` overrides it. Omit for local Docker. |
| `templates` | []RemoteTemplate | digest-pinned remote-template lockfile (managed by `template add`/`update`/`verify`). |
| `telemetry` | `{ enabled, installId, consentAt }` | opt-in usage telemetry consent — **default off**. |

`shared` uses the identical `SharedSvc` shape documented in
[config-reference.md](config-reference.md#workspaceyaml--workspace).

## Custom templates override built-ins by name

Drop a template directory under `~/.devstack/templates/<name>/` and it takes
precedence over the embedded built-in with the same name, everywhere. The
generate/template source chains custom templates **ahead of** the embedded set.

```bash
# author a template straight into the store's templates dir (the default parent)
devstack template new node.next --kind app
ls "$(devstack store path)/templates"     # node.next/  ...
```

Now any workspace that references `template: node.next` resolves it from the
store. Naming a directory `postgres/` would shadow the built-in `postgres`
engine — useful for pinning an image or tweaking defaults org-wide. See
[templates.md](templates.md) for authoring details.

## Seeding a workspace from the store: `init --from-store`

`devstack init --from-store` copies the store's `shared:` block into the new
`workspace.yaml`, so every workspace starts from your machine-wide engine
defaults instead of the built-in seed:

```bash
devstack store init                       # once, per machine
# edit ~/.devstack/config.yaml to taste (pg version, extra engines, params)
devstack init --name acme --from-store    # new workspace inherits store shared:
```

The store itself is left untouched — `--from-store` only seeds. See
[workspaces.md](workspaces.md) for the rest of `init`.

## Recipes

**Pin Postgres 16 for every new workspace on this machine.**

```bash
devstack store init
# in ~/.devstack/config.yaml set shared.postgres.params.version: "16"
devstack init --name acme --from-store
```

**Share a custom engine template across all repos.**

```bash
devstack template new kafka-lite --kind engine   # writes to the store templates dir
# reference `template: kafka-lite` from any workspace.yaml shared: block
```

**Find where the store lives (custom $DEVSTACK_HOME).**

```bash
DEVSTACK_HOME=/data/devstack devstack store path   # /data/devstack
```

## See also

- [Workspaces](workspaces.md) — `init` and `init --from-store`.
- [Templates](templates.md) — authoring templates that override built-ins.
- [Aliases & argv[0] dispatch](aliases.md) — the alias registry (installed under XDG bin).
- [Data lifecycle](data-lifecycle.md) — snapshots stored under `snapshots/`.
- [config-reference.md](config-reference.md) — the full schema, including `SharedSvc`.

---

◀ [Recovery, teardown & housekeeping](recovery.md) · [Guide index](./README.md) · [Aliases & argv[0] dispatch](aliases.md) ▶
