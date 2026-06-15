# devstack

> The binary is **`devstack`** (Go module + GitHub repo `github.com/open-source-cloud/devstack`; the local checkout folder is `devstack`). The same binary is invocable under aliases (e.g. `rq`, `uranus`) via the [alias system](docs/specs/07-cli-and-aliasing.md). Config files are `workspace.yaml` + `devstack.yaml` (`apiVersion: devstack/v1`).

A single-binary CLI that manages Docker-based development environments and **shares infrastructure across projects** — one warm Postgres / Redis / MinIO for many repos instead of a duplicate stack per project. It is a clean-slate, Go reimplementation of the ideas behind [`devdock`](../devdock) (the Python predecessor), redesigned around a *workspace* of shared services that independent project stacks attach to.

## Why

A developer working across 8 microservices today runs 8 Postgres containers, 8 Redis containers, and hand-rolls compose files, `/etc/hosts` entries, local TLS, and secret wiring per repo. `devstack` replaces that with:

- **Shared services** — one Postgres/Redis/MinIO on a tool-owned Docker network, with per-project database/role/bucket isolation. The differentiator.
- **Workspaces** — a `workspace.yaml` declares what infra is *provided*; each repo's `devstack.yaml` declares what it *consumes*. Repos are portable across workspaces.
- **Templating** — reusable service templates render compose + Dockerfiles deterministically.
- **Multi-repo git** — clone/sync/status across all repos in a workspace in one command, using your existing SSH/credential setup.
- **Secrets** — pluggable providers (Infisical, AWS, SOPS/age) resolve into containers without writing plaintext to disk.
- **Networking** — automatic local HTTPS at `https://<service>.<project>.localhost`, a label-driven reverse proxy, and optional Cloudflare Tunnel for public URLs.
- **One-command onboarding** — a new dev clones one repo and runs one command to get the whole platform running.

## Status

🚧 **M0 foundations implemented; design ongoing.** The spine is in place and green (`make ci`): the CLI tree + `argv[0]` aliasing, the `flock` cross-process lock, the SQLite ledger, XDG/WSL2 handling, a read-only Docker client, and a real `doctor` preflight. The rest of the surface is spec-driven (see the doc index). Try it: `make build && make smoke`.

## How it works (one paragraph)

`devstack` is a **stateless CLI** (no daemon in v1). Each invocation: discovers the workspace by walking up from the CWD → loads & validates `workspace.yaml` + each repo's `devstack.yaml` → resolves service/template references and secrets → builds a typed Docker Compose model and writes generated artifacts → ensures a tool-owned external Docker network and the shared-service stack → provisions per-project DB/role/bucket on demand → brings project stacks up via the `docker compose` CLI. A machine-global SQLite ledger (guarded by a cross-process lock) tracks shared-service reference counts and port allocations so infra starts on demand and can be reclaimed when unused. See [ARCHITECTURE](docs/ARCHITECTURE.md).

## Documentation

**Start here:**
- [ARCHITECTURE.md](docs/ARCHITECTURE.md) — runtime model, the generation pipeline, topology, module breakdown, cross-cutting concerns.
- [DECISIONS.md](docs/DECISIONS.md) — the chosen tech stack, with rationale and the *verified* corrections from research (e.g. `moby/moby/client`, not the deprecated `docker/docker`).
- [ROADMAP.md](docs/ROADMAP.md) — milestones M0–M7, effort, and the honest calendar estimate (with a strong recommendation to phase the four pillars).
- [FEATURES.md](docs/FEATURES.md) — the feature backlog, tagged v1 / v2 / later.
- [OPEN-QUESTIONS.md](docs/OPEN-QUESTIONS.md) — decisions to lock before/while building.

**Component specs** (each is self-contained and implementation-ready):
1. [Config schema, validation & DX](docs/specs/01-config-schema.md)
2. [Templating & generation engine](docs/specs/02-templating-and-generation.md)
3. [Workspaces & shared services](docs/specs/03-workspaces-and-shared-services.md) — the differentiator
4. [Secrets providers](docs/specs/04-secrets.md)
5. [Networking: local HTTPS, reverse proxy & tunnel](docs/specs/05-networking.md)
6. [Multi-repo git](docs/specs/06-git.md)
7. [CLI & aliasing](docs/specs/07-cli-and-aliasing.md)
8. [State, locking & lifecycle](docs/specs/08-state-locking-and-lifecycle.md) — the concurrency spine
9. [Orchestration & one-command onboarding](docs/specs/09-orchestration-and-onboarding.md) — the `up` saga
10. [Health, readiness & dependency ordering](docs/specs/10-health-readiness-and-ordering.md)
11. [Lifecycle hooks](docs/specs/11-lifecycle-hooks.md)
12. [Service profiles & selective up](docs/specs/12-service-profiles-and-selective-up.md)
13. [Doctor, diagnostics & teardown](docs/specs/13-doctor-diagnostics-and-teardown.md)
14. [Self-update, notifications & migration](docs/specs/14-self-update-and-migration.md)

**Post-1.0 specs** (v2 / later — the design frontier beyond the four pillars):
15. [DB seed, snapshot & restore for shared services](docs/specs/15-db-snapshot-restore.md) — v2
16. [Log aggregation & live TUI dashboard](docs/specs/16-logs-and-dashboard.md) — v2
17. [Devcontainer & IDE integration](docs/specs/17-devcontainer-ide-integration.md) — v2
18. [Resource limits & multi-arch image strategy](docs/specs/18-resource-limits-and-multi-arch.md) — v2
19. [Versioned template registry](docs/specs/19-template-registry.md) — v2
20. [Opt-in anonymous telemetry](docs/specs/20-telemetry.md) — later
21. [Remote / cloud shared-services backend](docs/specs/21-remote-shared-backend.md) — later (exploratory)

## Quickstart (target UX — not yet implemented)

```bash
# Install (one of)
brew install <org>/tap/devstack
curl -sSL https://.../install.sh | sh

# In a workspace repo containing workspace.yaml:
devstack doctor              # check docker, compose>=2.20, git>=2.30, ports, trust store
devstack up                  # clone repos, ensure shared infra, provision DBs, generate, compose up
devstack status              # multi-repo git + service health table
devstack shell api           # open a shell in the 'api' service
devstack down                # stop this workspace's project stacks
```

## Non-goals (v1)

- Not a Docker/Compose replacement — it *drives* the `docker compose` CLI (v2.20+).
- Podman / rootless / Colima / Lima are **out of scope** for v1 (tracked in [OPEN-QUESTIONS](docs/OPEN-QUESTIONS.md)).
- Native Windows is best-effort; **WSL2** is the supported Windows path.

## License

TBD (intended OSS — likely Apache-2.0 or MIT). Note: bundling `compose-go` (Apache-2.0) requires carrying its `NOTICE`/attribution.
