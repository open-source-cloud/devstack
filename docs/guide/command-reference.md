# Command reference

[devstack](../../README.md) · [Guide](./README.md) › Command reference

Every command, terse — one line each, grouped by area, with the key flags. For
the full story on any area, follow the links to the topic pages. Global flags
(`--json`, `--quiet`, `--debug`, `--verbose`, and pre-parsed `--as <name>`) are
covered in [global-flags.md](global-flags.md).

> **The `--yes` rule.** Destructive verbs (`workspace destroy`, `uninstall`,
> `db reset`/`restore`/`drop`/`gc`, `resource rm`/`gc`, `s3 rb`,
> `queue`/`topic`/`stream rm`) **require `--yes`** when run under `--json` or
> non-interactively.

> **Naming note.** `expose` / `ports` exist at the top level (canonical) and as
> `shared expose` / `shared ports` (backward-compatible aliases) — same behavior.

## Lifecycle

See [lifecycle.md](lifecycle.md).

| Command | Does | Key flags |
|---|---|---|
| `up [project...]` | Bring the workspace up (network, shared infra, generate, compose up, hooks) — idempotent saga. | `--build`, `--rebuild`, `--skip-clone`, `--health-timeout`, `--no-hooks`, `--no-preflight`, `--no-provision`, `--profile`/`-p` |
| `down [project...]` | Stop this workspace's project stacks and release their refs (data preserved). | — |
| `shell [service] [-- cmd...]` | Open a shell (or run a command) in a service container. | `--project` |
| `status` | Service health + last saga outcome + shared-service ref graph. | — |
| `logs [service...]` | Stream logs across project + shared stacks (color-keyed). | `--follow`/`-f`, `--tail` (200), `--since`, `--timestamps`, `--no-color` |
| `dashboard` | Live TUI cockpit: services, health, log tail. | `--no-stats` |

## Configuration & generation

See [projects.md](projects.md), [templates.md](templates.md).

| Command | Does | Key flags |
|---|---|---|
| `init` | Author a `workspace.yaml` (pick shared services + params); wizard on a bare TTY. | `--name`, `--profile`, `--service`, `--param`, `--alias`, `--project`, `--from-store`, `--out`, `--dry-run`, `--force`, `--no-input` |
| `config validate` | Validate workspace + project config (errors as `file:line:col`). | — |
| `config show` | Print a summary of the resolved workspace config. | — |
| `generate` | Render compose + build artifacts from config and templates. | `--project`, `--profile`, `--check` |
| `ide` | Generate devcontainer / `.code-workspace` / editor configs. | `--devcontainer`, `--vscode`, `--all`, `--check` |
| `import <project.yaml>` | Convert a legacy devdock `project.yaml` into workspace + per-repo config. | `--dry-run`, `--out`, `--force` |

## Templates

See [templates.md](templates.md).

| Command | Does | Key flags |
|---|---|---|
| `template list` | List the built-in service templates. | — |
| `template lint <dir>` | Render with defaults + validate through compose-go. | `--show` |
| `template test <dir>` | Render with defaults, assert it validates (and matches golden). | — |
| `template init <name>` | Scaffold a new template directory. | `--dir` |
| `template new [name]` | Author a template interactively (or from flags/`--from`). | `--kind`, `--name`, `--from`, `--extends`, `--param`, `--port`, `--provides`, `--exports`, `--golden`/`--no-golden`, `--dir`, `--dry-run`, `--force`, `--no-input` |
| `template push <dir> <ref>` | Package + push a template as an OCI artifact. | `--sign`, `--key` |
| `template add <ref>` | Register a remote template, pinning its digest. | `--allow-floating`, `--identity`, `--issuer`, `--key` |
| `template update [name]` | Re-resolve pinned templates to a new digest. | `--to`, `--dry-run` |
| `template diff <name>` | Render-diff a pinned template vs latest remote tag. | `--against` |
| `template verify [name]` | Re-pull pinned templates, re-check digest + signature. | `--identity`, `--issuer`, `--key` |
| `template ls` | List pinned remote templates and cache state. | — |

## Shared services & host access

See [shared-services.md](shared-services.md).

| Command | Does | Key flags |
|---|---|---|
| `shared status` | Show shared-service ref counts and consuming projects. | — |
| `shared gc` | Report (or stop) shared services at zero references. | `--stop` |
| `shared doctor` | Reconcile the ledger against live containers (prune dead refs). | — |
| `shared expose [services...]` | Publish shared services on stable `127.0.0.1` ports for GUI clients. | `--off` |
| `shared ports` | Show published `127.0.0.1` host ports + connection strings. | — |

## Resource layer

See [resources.md](resources.md).

| Command | Does | Key flags |
|---|---|---|
| `resource list` | List provisioned resources from the ownership ledger. | `--project`, `--engine`, `--kind` |
| `resource show <name>` | Show a resource's connection attributes (secrets masked). | `--project`, `--engine`, `--show-secrets` |
| `resource create <engine> <kind> <name>` | Create a resource on a running shared engine (idempotent). | `--project`, `--param`, `--credentials` |
| `resource rm <name>` | Un-track a resource (or drop its data). | `--project`, `--engine`, `--purge-data`, `--yes` |
| `resource gc` | Reclaim resources whose owner project left the workspace. | `--yes` |

## Databases (Postgres)

See [databases.md](databases.md).

| Command | Does | Key flags |
|---|---|---|
| `db create <name>` | Create a tenant database owned by the project role (idempotent). | `--project`, `--owner`, `--no-prefix` |
| `db user create <name>` | Create a tenant login role, optionally granted on a database. | `--project`, `--db`, `--role`, `--password`, `--generate`, `--no-prefix` |
| `db grant <role>` | Grant a privilege tier to a role on a database. | `--project`, `--on` (required), `--as` (read\|write\|admin) |
| `db list` | List provisioned databases and roles (lock-free). | `--project`, `--kind` |
| `db drop <name>` | Drop a tenant database or role (destructive). | `--project`, `--kind`, `--yes`, `--no-prefix` |
| `db gc` | Reclaim resources whose owner project left (all kinds). | `--yes` |

### Data lifecycle

See [data-lifecycle.md](data-lifecycle.md).

| Command | Does | Key flags |
|---|---|---|
| `db snapshot [name]` | Capture a project's tenant namespace to the snapshot store. | `--kind` (pg\|redis\|minio), `--project`, `--db`, `--instance` |
| `db snapshot ls` | List captured snapshots (lock-free). | `--project` |
| `db restore <name>` | Restore a tenant namespace from a snapshot (destructive). | `--kind`, `--project`, `--db`, `--instance`, `--force`, `--yes` |
| `db reset` | Drop and re-provision a project's tenant DB to empty (destructive). | `--project`, `--instance`, `--yes`, `--force` |
| `db pull <name>` | Seed a project's tenant from a named local snapshot. | `--project`, `--db`, `--instance` |

## Object storage (S3 / MinIO)

See [object-storage.md](object-storage.md).

| Command | Does | Key flags |
|---|---|---|
| `s3 mb <bucket>` | Make a tenant bucket (idempotent). | `--project`, `--versioning`, `--no-prefix` |
| `s3 rb <bucket>` | Remove a tenant bucket (destructive). | `--project`, `--force`, `--yes`, `--no-prefix` |
| `s3 ls` | List the project's buckets (lock-free). | `--project`, `--all` |
| `s3 lifecycle set <bucket>` | Set an expiry (+optional transition) rule. | `--project`, `--expire-days`, `--transition`, `--prefix` |
| `s3 lifecycle get <bucket>` | Show a bucket's lifecycle rules. | — |
| `s3 lifecycle rm <bucket>` | Remove lifecycle configuration. | `--project` |
| `s3 versioning <bucket> on\|off` | Enable or suspend bucket versioning. | `--project` |
| `s3 policy set <bucket>` | Set a bucket policy. | `--project`, `--file`, `--public-read` |
| `s3 policy get <bucket>` | Print a bucket's policy JSON. | — |
| `s3 cors set <bucket>` | Set CORS rules from a JSON file. | `--project`, `--file` |
| `s3 cors get <bucket>` | Print a bucket's CORS rules. | — |

## Messaging (queues / topics / streams)

See [messaging.md](messaging.md). Engine inference: queue `nats→redis→localstack`;
topic `kafka→nats→localstack`; stream `nats→kafka`.

| Command | Does | Key flags |
|---|---|---|
| `queue create <name>` | Create a tenant queue (idempotent). | `--project`, `--engine` (nats\|sqs\|redis), `--fifo`, `--dlq`, `--max-receive`, `--no-prefix` |
| `queue list` | List the project's queues (lock-free). | `--project`, `--all` |
| `queue rm <name>` | Remove a tenant queue (destructive). | `--project`, `--engine`, `--yes`, `--no-prefix` |
| `topic create <name>` | Create a tenant pub/sub topic (idempotent). | `--project`, `--engine` (sns\|kafka\|nats), `--subscribe`, `--no-prefix` |
| `topic list` | List the project's topics. | `--project`, `--all` |
| `topic rm <name>` | Remove a tenant topic (destructive). | `--project`, `--engine`, `--yes`, `--no-prefix` |
| `stream create <name>` | Create a tenant durable stream (idempotent). | `--project`, `--engine` (nats\|kafka), `--partitions`, `--replicas`, `--retention`, `--no-prefix` |
| `stream list` | List the project's streams. | `--project`, `--all` |
| `stream rm <name>` | Remove a tenant stream (destructive). | `--project`, `--engine`, `--yes`, `--no-prefix` |
| `aws -- <args...>` | Run the host `aws` CLI against local LocalStack/MinIO (injects endpoint + dev creds). | (passthrough) |

## Multi-repo git

See [multi-repo-git.md](multi-repo-git.md).

| Command | Does | Key flags |
|---|---|---|
| `ws status [names...]` | Cross-repo git status table (branch, ahead/behind, dirty). | `--check`, `--jobs` |
| `ws clone [names...]` | Clone workspace repos in parallel (skips existing). | `--jobs` |
| `ws sync [names...]` | Fetch + fast-forward pull every repo (runs `postPull` hooks). | `--jobs`, `--no-hooks` |
| `ws git <git args...> [-- names...]` | Run an arbitrary git command across repos. | `--jobs` |

## Networking & tunnels

See [networking.md](networking.md), [tunnels.md](tunnels.md).

| Command | Does | Key flags |
|---|---|---|
| `dns setup` / `status` / `remove` | Manage the devstack-managed `/etc/hosts` block for `*.localhost` (setup/remove need sudo). | — |
| `trust status` / `install` / `uninstall` | Manage the local HTTPS CA (mkcert) for `*.localhost` (install/uninstall need sudo). | — |
| `tunnel login` | Authenticate cloudflared with your Cloudflare account. | — |
| `tunnel create <name>` | Create a named tunnel (writes credentials). | — |
| `tunnel route <name> <hostname>` | Route a hostname to the tunnel. | — |
| `tunnel up [name]` | Bring the managed cloudflared tunnel up (default down). | `--detach` (true), `--allow-secrets` |
| `tunnel down` | Stop the managed tunnel (credentials/routes preserved). | — |

## Secrets

See [secrets.md](secrets.md).

| Command | Does | Key flags |
|---|---|---|
| `secrets keygen` | Generate an age keypair for SOPS+age (offline). | `--output`/`-o` |
| `secrets ingest [.env]` | Convert a committed `.env` into SOPS/provider secrets + inline config. | `--to`, `--dest`, `--service`, `--recipient`, `--secret`, `--public`, `--from-host`, `--prefixed`, `--keep-env`, `--dry-run`, `--yes`, `--force` |
| `secrets login <provider>` | Store a provider credential in the OS keyring. | `--token` (required) |
| `secrets logout <provider>` | Remove a stored provider credential. | — |
| `secrets status [provider...]` | Report keyring availability + per-provider credential source. | — |

## Recovery, health & housekeeping

See [recovery.md](recovery.md).

| Command | Does | Key flags |
|---|---|---|
| `doctor` | Probe the environment, report capabilities + remediations. | `--fix`, `--rebuild-state` |
| `workspace destroy` | Tear down this workspace's stacks, release refs/ports (data preserved). | `--yes`, `--purge-data` |
| `workspace list` | List every registered workspace for this Docker context. | `--prune` |
| `uninstall` | Remove every machine-global devstack artifact (irreversible). | `--yes` |

## The global store & aliases

See [store.md](store.md), [aliases.md](aliases.md).

| Command | Does | Key flags |
|---|---|---|
| `store init` | Create `~/.devstack` with config, templates/, shared/. | `--force` |
| `store path` | Print the devstack home directory. | — |
| `store show` | Show the store config (the global shared services). | — |
| `alias add <name>` | Install an alias symlink in `XDG_BIN_HOME`. | — |
| `alias remove <name>` (alias `rm`) | Remove an alias symlink + registry entry. | — |
| `alias list` | List installed aliases. | — |

## Binary & meta

See [installation.md](installation.md).

| Command | Does | Key flags |
|---|---|---|
| `self check` | Check whether a newer release is available. | — |
| `self update` | Download + install the latest release (cosign + SHA-256 verified). | `--check`, `--version`, `--force`, `--insecure-skip-verify` |
| `telemetry status` / `enable` / `disable` / `show` | Opt-in anonymous usage telemetry (default off; ship-empty). | — |
| `version` | Print version, commit and build date. | — |
| `completion`, `man` | Shell completion + man pages (added by fang). | — |

## See also

- [Global flags & scripting](global-flags.md) — `--json`/`--quiet`/`--debug`/`--verbose`, the `--yes` rule, `--check`.
- [config-reference.md](config-reference.md) — the two-file schema and grammars.
- [What's next](whats-next.md) — roadmap and current gaps.

---

◀ [Full config reference](config-reference.md) · [Guide index](./README.md) · [Global flags & scripting](global-flags.md) ▶
