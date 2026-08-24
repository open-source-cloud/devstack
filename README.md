<div align="center">

# devstack

**One warm Postgres/Redis/MinIO for many repos — not a duplicate stack per project.**

A single static Go binary that runs shared dev infrastructure on a tool-owned
Docker network and lets every project attach to it — with per-project data
isolation, deterministic Compose generation, and a batteries-included developer
experience (context switching, shell integration, framework templates, a task
runner).

[![CI](https://github.com/open-source-cloud/devstack/actions/workflows/ci.yml/badge.svg)](https://github.com/open-source-cloud/devstack/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/open-source-cloud/devstack?sort=semver)](https://github.com/open-source-cloud/devstack/releases)
[![Go](https://img.shields.io/github/go-mod/go-version/open-source-cloud/devstack)](go.mod)
[![Platforms](https://img.shields.io/badge/platforms-linux%20%7C%20macOS%20%7C%20WSL2-blue)](#requirements)

</div>

---

## The idea

Working across 8 microservices used to mean 8 Postgres containers, 8 Redis
containers, and hand-rolled compose files per repo. devstack replaces that with
**one** warm set of shared engines that every project shares — while each project
still gets its **own** database, role, and bucket.

- **`workspace.yaml`** declares the shared infra (what's *provided*).
- **`devstack.yaml`** (one per repo) declares what a project *consumes*.
- `devstack up` starts the shared engines once, provisions each project's isolated
  data, and brings the project stacks up on the shared network.

## Quickstart

```bash
# 1. Install (Linux/macOS; WSL2 on Windows)
curl -fsSL https://raw.githubusercontent.com/open-source-cloud/devstack/main/install.sh | sh

# 2. Turn on shell integration (context switching + completions + prompt)
eval "$(devstack shell-init zsh)"        # or bash / fish

# 3. Scaffold and run
devstack init                 # author workspace.yaml (wizard on a TTY)
devstack doctor               # verify docker / compose / git / ports
devstack up                   # network → shared engines → provision → compose up
devstack status               # health + the active-context header
```

A minimal workspace:

```yaml
# workspace.yaml
apiVersion: devstack/v1
kind: Workspace
name: acme
shared:
  postgres: { template: postgres, params: { version: "18" } }
  redis:    { template: redis }
projects:
  - { name: api, path: services/api }
  - { name: web, path: services/web }
```

```yaml
# services/api/devstack.yaml
apiVersion: devstack/v1
kind: Project
name: api
services:
  api:
    template: node.next            # or php.laravel.nginx, node.express, bun.app, …
    uses: [workspace.shared.postgres, workspace.shared.redis]
    env:
      import:
        - { from: workspace.shared.postgres, vars: [host, port, user, password, database] }
    ports: { http: 3000 }
tasks:
  build: { run: host, command: ["pnpm", "build"] }
  test:  { run: host, command: ["pnpm", "test"], deps: [build] }
```

## What you get

- **Shared services, isolated data** — one Postgres/Redis/MinIO (plus
  Kafka/NATS/RabbitMQ/LocalStack) on the `devstack_shared` network; every project
  gets its own DB/role/bucket. *The differentiator.*
- **Active context + switching** — `devstack use <project>` sets the current
  project; with shell integration it `cd`s and sets env in your live shell.
  `devstack context` shows where you are; a prompt segment keeps it visible.
- **51 built-in templates with hot reload** — apps for Go, Rust, Python
  (FastAPI/Django/Flask), Node, Bun, Deno, PHP/Laravel, Ruby on Rails, Elixir
  Phoenix, Java Spring, ASP.NET, and the Vite family (React, Vue, Svelte, Nuxt,
  Astro). App templates bind-mount your source, force the dev server to bind
  `0.0.0.0`, and enable file-watch polling for WSL2.
- **Shared engines for most of what you'd reach for** — Postgres, MySQL, MariaDB,
  MongoDB, Cassandra, ArangoDB, Redis, Valkey, TimescaleDB, ClickHouse, Neo4j,
  MinIO, RustFS, OpenSearch, Meilisearch, Kafka, NATS, RabbitMQ, etcd, Consul,
  Keycloak, Jaeger, Mailpit, Mosquitto, LocalStack.
- **Your AI tools learn devstack from devstack** — `devstack ai install` writes
  Claude Code skills, a fenced `AGENTS.md` block (read by Codex, Cursor, Copilot,
  Gemini CLI, Windsurf, Zed) and an MCP registration into the repo; `devstack ai
  mcp` serves the whole CLI over the Model Context Protocol.
- **A task runner** — declare `tasks:` with `deps:` and run the graph with
  `devstack run <task>` (dependency-ordered, parallel, streamed).
- **Deterministic generation** — templates render Compose + Dockerfiles
  byte-identically (CI-asserted), validated through `compose-go`.
- **The data plane** — `db` / `s3` / `queue` / `topic` / `stream` create
  tenant-scoped resources on the shared engines (or declare them in `resources:`).
- **Multi-repo git** — clone/sync/status across every repo in one parallel command.
- **Secrets & networking** — `secret://` providers (SOPS+age/AWS/Infisical, no
  plaintext on disk), local HTTPS at `https://<svc>.<project>.localhost`, and
  cloudflared tunnels.
- **Authoring without corruption** — `project new` and `env set` edit your YAML
  through a comment- and order-preserving rewriter.

## Commands

| Area | Commands |
|---|---|
| **Lifecycle** | `up` · `down` · `status` · `shell` · `run` · `logs` · `dashboard` |
| **Context & DX** | `use` · `context` · `shell-init` · `project list/new` · `env list/set/unset` |
| **Config & templates** | `init` · `config validate/show/schema` · `generate` · `ide` · `template list/lint/test/new` · `import` |
| **AI agents** | `ai install/check` · `ai mcp` · `ai docs` · `ai commands` |
| **Shared & host access** | `shared status/gc/doctor` · `expose` · `ports` |
| **Data plane** | `db` · `s3` · `queue` · `topic` · `stream` · `resource` · `aws -- …` |
| **Multi-repo git** | `ws clone/sync/status/git` |
| **Secrets & networking** | `secrets keygen/ingest/login/logout/status` · `trust` · `dns` · `tunnel` |
| **Machine & lifecycle** | `workspace list/destroy` · `uninstall` · `self check/update` · `store` · `alias` |

Every headline command supports `--json` and `--quiet` for scripting/CI. Full
reference: **[the usage guide](docs/guide/command-reference.md)**.

## How it works

devstack is a **stateless CLI — no daemon**. Each invocation: discover the
workspace → validate config → render a typed Compose model (validated by
`compose-go`, written deterministically) → take a cross-process `flock` → ensure the
tool-owned external network + shared stack → provision per-project data → drive
`docker compose`. A machine-global SQLite ledger (keyed by Docker context) tracks
reference counts, port allocations, and the active context. See
**[ARCHITECTURE.md](docs/ARCHITECTURE.md)**.

## Install

```bash
curl -fsSL https://raw.githubusercontent.com/open-source-cloud/devstack/main/install.sh | sh
```

The installer detects your OS/arch, downloads the matching archive from
[Releases](https://github.com/open-source-cloud/devstack/releases), **verifies its
SHA-256 checksum**, installs to `$XDG_BIN_HOME` (or `~/.local/bin`), and prints the
`shell-init` line for your shell.

<details>
<summary>Options, updates &amp; uninstall</summary>

```bash
# Pin a version, choose the dir, install argv[0] aliases:
DEVSTACK_VERSION=v0.19.0 DEVSTACK_INSTALL_DIR=/usr/local/bin DEVSTACK_ALIASES="rq uranus" \
  sh -c "$(curl -fsSL https://raw.githubusercontent.com/open-source-cloud/devstack/main/install.sh)"

devstack self check        # newer release available?
devstack self update       # checksum-verified, atomic, in-place (refuses package-managed installs)
```

| Env var | Default | Purpose |
|---|---|---|
| `DEVSTACK_VERSION` | latest | Pin a tag (e.g. `v0.19.0`). |
| `DEVSTACK_INSTALL_DIR` | `$XDG_BIN_HOME` / `~/.local/bin` | Install dir. |
| `DEVSTACK_ALIASES` | *(none)* | Space-separated `argv[0]` alias symlinks. |
| `GITHUB_TOKEN` / `GH_TOKEN` | *(none)* | Required while the repo is private; raises API limits. |

**From source** (Go 1.25+): `make install`. **Linux packages**: `.deb`/`.rpm` on each release.
Uninstall: `devstack uninstall` (removes every machine-global artifact), or `rm "$(command -v devstack)"`.
</details>

## Requirements

- **Docker Engine** + the **`docker compose` plugin ≥ 2.20**.
- **git ≥ 2.30** for multi-repo features.
- **OS**: Linux & macOS (amd64/arm64); on Windows use **WSL2** (run from the Linux
  filesystem — `/mnt/*` working dirs are refused). Podman/rootless/Colima are out of scope.

Run `devstack doctor` to check everything.

## Documentation

- **[Usage guide](docs/guide/README.md)** — the task-oriented book: concepts,
  workspaces, projects, env vars, templates, the data plane, every command, and the
  full config reference.
- **[QUICKSTART.md](docs/QUICKSTART.md)** — the 5-minute path.
- **[AI agents](docs/guide/ai-agents.md)** — the `ai` group: MCP, skills, the
  embedded docs corpus and the JSON Schemas.
- **[ARCHITECTURE.md](docs/ARCHITECTURE.md)** · **[DECISIONS.md](docs/DECISIONS.md)** · **[ROADMAP.md](docs/ROADMAP.md)** — design, chosen stack, milestones.
- **Component specs** — [`docs/specs/`](docs/specs/) (01…32), each self-contained.

You can also read the documentation without leaving the terminal — the whole
corpus is compiled into the binary:

```bash
devstack ai docs                    # list every page
devstack ai docs guide/templates    # print one
devstack ai docs --search "port"    # search it
```

## Status

🧪 **Beta (0.x).** The shared-services core, the deterministic generation pipeline,
the data plane, multi-repo git, secrets, networking, the interactive-DX lane
(active context, shell integration, templates, `devstack run`) and the AI-agent
surface ([spec 32](docs/specs/32-ai-agent-integration.md)) are all implemented and
green on `make ci` + `make determinism`. The full log/dashboard cockpit
([spec 16](docs/specs/16-logs-and-dashboard.md)) is the main in-flight item.

## Development

```bash
make build         # CGO-free static binary → ./dist/devstack
make ci            # fmt-check + vet + build + test-race (what CI runs)
make determinism   # assert generation is byte-identical
make help          # all targets
```

The release binary **must** be `CGO_ENABLED=0`; `go test -race` needs
`CGO_ENABLED=1` — the Makefile sets CGO per target. New external-tool integrations
go behind an `internal/` interface with a mock. Contributions welcome.

## License

[Apache-2.0](LICENSE). Bundled `compose-go` (Apache-2.0) is attributed in [NOTICE](NOTICE).
