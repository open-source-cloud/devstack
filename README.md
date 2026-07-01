<div align="center">

# devstack

**One warm Postgres/Redis/MinIO for many repos — not a duplicate stack per project.**

A single static Go binary that manages Docker-based dev environments and **shares
infrastructure across projects**: a workspace of shared services that independent
project stacks attach to, with per-project data isolation.

[![CI](https://github.com/open-source-cloud/devstack/actions/workflows/ci.yml/badge.svg)](https://github.com/open-source-cloud/devstack/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/open-source-cloud/devstack?sort=semver)](https://github.com/open-source-cloud/devstack/releases)
[![Go](https://img.shields.io/github/go-mod/go-version/open-source-cloud/devstack)](go.mod)
[![Platforms](https://img.shields.io/badge/platforms-linux%20%7C%20macOS%20%7C%20WSL2-blue)](#requirements)

</div>

---

## Why

A developer working across 8 microservices today runs 8 Postgres containers, 8
Redis containers, and hand-rolls compose files, `/etc/hosts` entries, local TLS,
and secret wiring per repo. `devstack` replaces that with:

- **Shared services** — one Postgres/Redis/MinIO on a tool-owned Docker network,
  with per-project database/role/bucket isolation. *The differentiator.*
- **Workspaces** — `workspace.yaml` declares what infra is *provided*; each repo's
  `devstack.yaml` declares what it *consumes*. Repos stay portable across workspaces.
- **Deterministic generation** — service templates render Compose + Dockerfiles
  byte-identically (CI-asserted), validated through `compose-go`.
- **Multi-repo git** — clone/sync/status across every repo in a workspace in one
  parallel command, using your existing SSH / credential-helper setup.
- **Secrets, networking, onboarding** — pluggable secret providers, local HTTPS at
  `https://<service>.<project>.localhost`, and one-command onboarding (on the roadmap).

It is a clean-slate Go reimplementation of the ideas behind a Python predecessor
(`devdock`), redesigned around the shared-services workspace model.

## Install

```bash
# Linux & macOS (amd64/arm64); on Windows use WSL2:
curl -fsSL https://raw.githubusercontent.com/open-source-cloud/devstack/main/install.sh | sh
```

The installer detects your OS/arch, downloads the matching archive from
[GitHub Releases](https://github.com/open-source-cloud/devstack/releases),
**verifies its SHA-256 checksum**, and installs to `$XDG_BIN_HOME` (or `~/.local/bin`).

> **Note:** once the repository's releases are public this works with no auth.
> While the repo is private, export a token first — e.g.
> `export GITHUB_TOKEN="$(gh auth token)"`.

<details>
<summary>Installer options &amp; alternatives</summary>

```bash
# Pin a version, choose the install dir, and install argv[0] aliases in one go:
DEVSTACK_VERSION=v0.1.0 \
DEVSTACK_INSTALL_DIR=/usr/local/bin \
DEVSTACK_ALIASES="rq uranus" \
  sh -c "$(curl -fsSL https://raw.githubusercontent.com/open-source-cloud/devstack/main/install.sh)"
```

| Env var | Default | Purpose |
|---|---|---|
| `DEVSTACK_VERSION` | latest release | Pin a specific tag (e.g. `v0.1.0`). |
| `DEVSTACK_INSTALL_DIR` | `$XDG_BIN_HOME` or `~/.local/bin` | Where to install the binary. |
| `DEVSTACK_ALIASES` | *(none)* | Space-separated `argv[0]` alias symlinks. |
| `DEVSTACK_NO_VERIFY` | `0` | Skip checksum verification (not recommended). |
| `GITHUB_TOKEN` / `GH_TOKEN` | *(none)* | GitHub auth — **required while the repo is private**; also raises API rate limits. |

**From source** (needs Go 1.25+): `make install` (builds a CGO-free static binary
into `$XDG_BIN_HOME`). **Linux packages**: `.deb` / `.rpm` are attached to each release.
</details>

### Staying up to date

```bash
devstack self check     # is a newer release available?
devstack self update    # download + checksum-verify + atomically replace in place
```

`self update` refuses to overwrite a Homebrew/dpkg/rpm-managed binary, printing the
right `brew`/`apt`/`dnf` command instead.

### Uninstall

```bash
rm "$(command -v devstack)"                       # remove the binary
rm -f "$HOME/.local/bin/rq" "$HOME/.local/bin/uranus"   # remove any argv[0] alias symlinks you added
```

From a source install, `make uninstall` removes the binary. Machine-global state
(the SQLite ledger, alias registry, template cache) lives under your XDG data/
config/cache dirs (`~/.local/share/devstack`, `~/.config/devstack`, …); a managed
teardown (`workspace destroy`) is on the roadmap.

## Quickstart

`devstack` discovers your workspace by walking up from the current directory for a
`workspace.yaml`. A minimal workspace:

```yaml
# workspace.yaml
apiVersion: devstack/v1
kind: Workspace
name: acme
shared:
  postgres: { template: postgres, params: { version: "16" } }
  redis:    { template: redis }
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
    uses: [workspace.shared.postgres, workspace.shared.redis]
    env:
      import:
        - { from: workspace.shared.postgres, vars: [host, port, user, password, database] }
    ports: { http: 8080 }
```

Then, the **headline flow**:

```bash
devstack init                   # author workspace.yaml (interactive wizard, or --flags)
devstack doctor                 # probe docker / compose≥2.20 / git≥2.30 / ports / state dir
devstack ws clone               # clone every repo in parallel (your SSH/credential setup)
devstack up                     # network → shared services (health-gated) → provision → compose up
devstack status                 # composite: services + health + cross-repo git + ref graph
devstack down                   # tear the project stacks down (shared infra stays warm, ref-counted)

devstack generate --check       # CI gate: fail if generated artifacts are stale
devstack shared status          # shared-service ref counts + consuming projects
devstack secrets ingest .env    # convert a committed .env into secret:// refs + config vars
```

## Commands

| Command | Status | What it does |
|---|---|---|
| `up` / `down` / `status` | ✅ | The lifecycle saga: ensure network → shared services (health-gated) → provision per-project DB → `compose up`; resumable, ref-counted. `status` is the composite services + git + ref-graph view. |
| `init` | ✅ | Author a `workspace.yaml` — an interactive wizard on a TTY, flag-driven otherwise. |
| `doctor` | ✅ | Environment capability matrix with one-line remediations (`--json`, `--fix`, `--rebuild-state`). |
| `config validate` / `show` | ✅ | Load + validate the two-file config; `file:line:col` errors. |
| `generate` | ✅ | Render + compose-go-validate compose/Dockerfiles; `--check`, `--project`, `--profile`. |
| `template list/lint/test/init/new` | ✅ | Author and validate service templates; `new` is the authoring wizard. |
| `ws clone/sync/status/git` | ✅ | Bounded-parallel multi-repo git over the workspace. |
| `shared status/gc/doctor` | ✅ | Shared-service ref counts + consuming projects; reclaim zero-ref services; reconcile the ledger. |
| `secrets keygen/ingest/login/logout/status` | ✅ | SOPS+age / AWS / Infisical secrets; `ingest` converts a `.env` into `secret://` refs + vars. |
| `trust install/uninstall/status` | ✅ | Local-HTTPS CA via `mkcert`. |
| `dns setup/status/remove` | ✅ | `*.localhost` resolver wiring. |
| `tunnel login/create/route/up/down` | ✅ | Cloudflare tunnel (default down; refuses secret-bearing services). |
| `self check/update` | ✅ | Version check and checksum-verified self-update. |
| `store init/path/show` | ✅ | The global `~/.devstack` store: config + custom templates + shared defs. |
| `alias add/remove/list` | ✅ | `argv[0]` alias symlinks (`rq`, `uranus`, …). |
| `import` | ✅ | Convert a legacy devdock `project.yaml` → the clean-slate two-file schema. |
| `workspace list/destroy` / `uninstall` | ✅ | List every registered workspace; tear down this workspace's stacks (`--purge-data` drops provisioned resources) / reverse all machine-global artifacts. |
| `shell <service>` | ✅ | Open an interactive shell (or `-- <cmd>`) in a service container. |
| **`db` / `s3` / `queue` / `topic` / `stream`** | ✅ | **Local-cloud data plane** — create tenant-scoped databases + users, S3 buckets + object lifecycle, queues, pub/sub topics, and streams on the shared engines (or declare them in `devstack.yaml resources:`, provisioned at `up`). |
| `resource list/show/create/rm/gc` | ✅ | Engine-agnostic view + management of every provisioned resource in the ledger. |
| `aws -- <args>` | ✅ | Run the host `aws` CLI against the local LocalStack/MinIO endpoint (dev creds prefilled). |
| `logs` | 🚧 | Stream / aggregate service logs (planned — [spec 16](docs/specs/16-logs-and-dashboard.md)). |

Every headline command supports `--json` and `--quiet` for scripting/CI. **`logs` is the only remaining stub.**

## The global store (`~/.devstack`)

`devstack store init` creates a machine-global home (override with `$DEVSTACK_HOME`)
shared across all your workspaces:

```
~/.devstack/
  config.yaml     # the store config: the global shared services (postgres, redis, minio/S3…)
  templates/      # custom service templates — override the embedded built-ins by name
  shared/         # the global shared stack's generated artifacts
```

Drop a template under `~/.devstack/templates/<name>/` to customize or add a
service for *every* workspace — e.g. a `postgres/` there overrides the built-in
Postgres. `devstack store show` lists the configured shared services;
`devstack template list` shows your store templates alongside the built-ins.

## How it works

`devstack` is a **stateless CLI — no daemon**. Each invocation discovers the
workspace → validates `workspace.yaml` + each `devstack.yaml` → renders a typed
Compose model (validated by `compose-go`, written deterministically) → ensures a
tool-owned external Docker network and the shared stack → provisions per-project
Postgres role/db on demand → drives `docker compose`. A machine-global SQLite ledger,
guarded by a cross-process `flock` and keyed by Docker context, tracks shared-service
**reference counts** and port allocations so infra starts on demand and is reclaimed
when unused. See **[ARCHITECTURE.md](docs/ARCHITECTURE.md)**.

## Status

🧪 **Beta (0.x), on the `v0.2+` line.** Everything below is implemented and green on `make ci` + `make determinism`:

- **M0–M1** — CLI tree + `argv[0]` aliasing, `flock` lock, SQLite ledger (Docker-context-keyed), XDG/WSL2 handling, the read-only Docker client + `doctor`; the two-file config loader and the full deterministic **templating + generation** pipeline.
- **M2** — shared-services lifecycle: tool-owned network, ledger ref-counting + self-healing reconcile, per-project Postgres provisioning, port allocation, `shared status/gc/doctor`, and the **`up`/`down` saga** (network → shared → provision → compose-up → hooks, resumable).
- **M3** — multi-repo git (`gitx` + `ws clone/sync/status/git`).
- **M4–M7** — secrets (`secret://` + SOPS+age/AWS/Infisical, no plaintext on disk), networking (Caddy proxy, `mkcert` trust, cloudflared tunnel, `dns`), orchestration glue (health gating, lifecycle hooks, profiles), `doctor --fix`, `workspace destroy`/`uninstall`, and self-update + `devstack import`.
- **M8 (beta DX)** — `init` wizard, `template new` authoring, `secrets ingest` (`.env` → secrets), and conventional-commit **release automation** (released **v0.2.0+** automatically).

- **M9 (local cloud)** — the CLI-completeness pass (`shell`, `workspace list`, `tunnel up/down`, `up --rebuild/--skip-clone`), a data-plane **resource layer** (a per-engine `Provisioner` family + a declarative `resources:` block + `resource`/`db`/`s3`/`queue`/`topic`/`stream` verbs), and net-new cloud-emulation **engine templates** (LocalStack, NATS, Kafka/Redpanda, RabbitMQ). Databases, users, buckets + object lifecycle, queues, topics, and streams are all tenant-scoped, ledger-tracked, and reachable over `devstack_shared`.

**The only remaining command stub is `logs`** (the full log/dashboard cockpit is [spec 16](docs/specs/16-logs-and-dashboard.md), still v2). Roadmap detail: **[ROADMAP.md](docs/ROADMAP.md)**.

## Requirements

- **Docker Engine** + the **`docker compose` plugin ≥ 2.20**.
- **git ≥ 2.30** for multi-repo features.
- **OS**: Linux & macOS (amd64/arm64). On Windows, use **WSL2** (run from the Linux
  filesystem — `/mnt/*` working dirs are refused). Podman/rootless/Colima/Lima are
  out of scope for v1.

Run `devstack doctor` to check all of the above.

## Documentation

- **[ARCHITECTURE.md](docs/ARCHITECTURE.md)** — runtime model, the generation pipeline, topology, module map.
- **[DECISIONS.md](docs/DECISIONS.md)** — the chosen stack + the *verified* corrections from research.
- **[ROADMAP.md](docs/ROADMAP.md)** — milestones, effort, honest calendar.
- **[OPEN-QUESTIONS.md](docs/OPEN-QUESTIONS.md)** — decisions, all resolved.
- **Component specs** — [`docs/specs/01`](docs/specs/01-config-schema.md)…[`21`](docs/specs/21-remote-shared-backend.md), each self-contained and implementation-ready.

## Development

```bash
make build         # CGO-free static binary → ./dist/devstack
make ci            # what CI runs: fmt-check + vet + build + test-race
make determinism   # assert generation is byte-identical across runs/paths
make smoke         # exercise the built binary end-to-end in an XDG sandbox
make help          # list all targets
```

CI (GitHub Actions) runs build/test-race, `govulncheck`, a 4-target cross-compile,
a deterministic-generation gate, installer shellcheck, and a goreleaser dry-run.
The release binary **must** be `CGO_ENABLED=0`; `go test -race` needs `CGO_ENABLED=1`
— the Makefile sets CGO per target.

Contributions welcome: open an issue or PR. New external-tool integrations go behind
an `internal/` interface with a mock so tests run without the real dependency.

## License

[Apache-2.0](LICENSE). Bundled `compose-go` (Apache-2.0) is attributed in [NOTICE](NOTICE).
