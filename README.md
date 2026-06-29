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
| `GITHUB_TOKEN` / `GH_TOKEN` | *(none)* | Auth for the GitHub API (higher rate limits). |

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

Then, **working today**:

```bash
devstack doctor                 # probe docker / compose≥2.20 / git≥2.30 / ports / state dir
devstack config validate        # validate workspace.yaml + every devstack.yaml (file:line:col errors)
devstack generate               # render compose + Dockerfiles deterministically into .devstack/
devstack generate --check       # CI gate: fail if generated artifacts are stale
devstack template list          # the built-in service templates
devstack ws clone               # clone every repo in parallel (your SSH/credential setup)
devstack ws status              # cross-repo git table (branch, ahead/behind, dirty); --check for CI
devstack shared status          # shared-service ref counts + consuming projects
```

## Commands

| Command | Status | What it does |
|---|---|---|
| `doctor` | ✅ | Environment capability matrix with one-line remediations (`--json`). |
| `config validate` / `show` | ✅ | Load + validate the two-file config; `file:line:col` errors. |
| `generate` | ✅ | Render + compose-go-validate compose/Dockerfiles; `--check`, `--project`, `--profile`. |
| `template list/lint/test/init` | ✅ | Author and validate service templates. |
| `ws clone/sync/status/git` | ✅ | Bounded-parallel multi-repo git over the workspace. |
| `shared status` | ✅ | Shared-service ref counts and consuming projects. |
| `self check` / `self update` | ✅ | Version check and checksum-verified self-update. |
| `alias add/remove/list` | ✅ | `argv[0]` alias symlinks (`rq`, `uranus`, …). |
| `up` / `down` / `shell` / `logs` | 🚧 | Lifecycle saga — shared infra + provisioning + compose up (M2/M6). |
| `secrets` / `trust` / `dns` / `tunnel` | 🚧 | Secrets (M4) and networking (M5). |

Every headline command supports `--json` and `--quiet` for scripting/CI.

## How it works

`devstack` is a **stateless CLI — no daemon**. Each invocation discovers the
workspace → validates `workspace.yaml` + each `devstack.yaml` → renders a typed
Compose model (validated by `compose-go`, written deterministically) → ensures a
tool-owned external Docker network and the shared stack → provisions per-project
DB/role/bucket on demand → drives `docker compose`. A machine-global SQLite ledger,
guarded by a cross-process `flock` and keyed by Docker context, tracks shared-service
**reference counts** and port allocations so infra starts on demand and is reclaimed
when unused. See **[ARCHITECTURE.md](docs/ARCHITECTURE.md)**.

## Status

🚧 **Pre-1.0, in active development.** Implemented and green on `make ci` + `make determinism`:

- **M0** — CLI tree + `argv[0]` aliasing, `flock` lock, SQLite ledger (Docker-context-keyed),
  XDG/WSL2 handling, read-only Docker client + `doctor`.
- **M1** — two-file config loader; the full deterministic **templating + generation** pipeline.
- **M2 (core)** — ledger ref-counting + self-healing reconcile, Postgres provisioning,
  `shared status`. *Remaining:* the `up`/`down` daemon saga.
- **M3** — multi-repo git (`gitx` + `ws clone/sync/status/git`).

Then: secrets (M4) · networking/local-HTTPS (M5) · orchestrated onboarding + health
+ hooks (M6) · hardening + GA (M7). See **[ROADMAP.md](docs/ROADMAP.md)**.

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

**TBD** (intended open source). Note: bundling `compose-go` (Apache-2.0) requires
carrying its `NOTICE`/attribution.
