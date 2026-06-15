# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

`devstack` is a single static Go binary (CLI) that manages Docker-based dev
environments and **shares infrastructure across projects** — one warm
Postgres/Redis/MinIO on a tool-owned network for many repos, instead of a
duplicate stack per repo. The Go module and GitHub repo are
`github.com/open-source-cloud/devstack` (local checkout folder `devstack`); the binary built from `./cmd/devstack`
is `devstack`. The same binary is invocable under aliases (`rq`, `uranus`) via
`argv[0]` dispatch.

The project is **spec-driven**: `docs/` (ARCHITECTURE, DECISIONS, ROADMAP,
FEATURES, OPEN-QUESTIONS, and `docs/specs/01..14`) is the source of truth. When
behavior is ambiguous, those docs win — read them before designing. Implementation
is at the **M0 (foundations) stage**; see "Current status" below.

## Commands

```bash
make build              # CGO_ENABLED=0 static binary → ./dist/devstack (version-stamped)
make run ARGS="doctor"  # build + run with ldflags
make test               # unit tests
make test-race          # tests with the race detector (forces CGO_ENABLED=1)
make test-one RUN=TestSerializesAcquire PKG=./internal/lock   # single test
make ci                 # fmt-check + vet + build + test-race (what CI runs)
make fmt                # gofmt -w .
make vuln               # govulncheck ./...
make help               # list targets
```

Plain `go` works too, but mind the CGO split: **the release binary must be
`CGO_ENABLED=0`** (pure-Go static), yet **`go test -race` requires
`CGO_ENABLED=1`**. The Makefile sets CGO per-target; don't set it globally.

## Hard project constraints (gate everything)

- **`CGO_ENABLED=0`, single static binary**, cross-compiled to
  `darwin/{amd64,arm64}` + `linux/{amd64,arm64}` from one Linux runner. Every dep
  must be pure-Go or behind a build tag / external binary. WSL2 uses the ordinary
  `linux` build (not a separate target) but is a distinct *runtime-behavior* target.
- **Go 1.25 toolchain floor** (max required by fang + validator/v10). Enforced in CI.
- **Every fast-moving / risky dependency sits behind an `internal/` interface** so
  it can be swapped or vendored (docker client, template engine, secrets, git, trust).

## Architecture (the big picture)

**Stateless CLI, no daemon (v1).** Every invocation: load config + the global
ledger → take a coarse cross-process lock before mutating shared state → drive
Docker (the `docker compose` CLI for lifecycle; the Engine SDK **read-only**) →
exit. Consequence: no autostop; ref-count drift is reconciled lazily on the next
command / `shared gc` / `doctor --fix`.

**State lives in exactly two places** (ARCHITECTURE §2):
1. **In the repo, committed:** `workspace.yaml` (workspace root) + each repo's
   `devstack.yaml` — the declarative source of truth.
2. **Machine-global under XDG, not committed:** a SQLite ledger **keyed by Docker
   context** (so WSL2's two daemons — Desktop vs in-distro `dockerd` — never share
   counts), plus the alias registry, template cache, and per-project generated
   artifacts under `.devstack/`.

**One generation pipeline, single owner** (ARCHITECTURE §3): discover workspace
(walk up for `workspace.yaml`) → parse+validate config → resolve template
`extends` (render then deep-merge) → render text artifacts + build the compose
model **programmatically** → resolve `${ref}` against the **workspace graph** (not
one project — this is what makes sharing work) → resolve `secret://` (batched, never
written to disk) → validate via `compose-go/v2` → `writeIfChanged` (atomic, SHA-256
build contexts) → selective rebuild + `up -d`. **Determinism is a hard requirement**
(byte-identical output; CI asserts it).

**Network topology** (ARCHITECTURE §4): one tool-owned **external** bridge network
`devstack_shared` (pinned name) that both the shared stack and every project stack
join. Compose refuses to create/remove `external: true` networks, so **devstack
owns create + cleanup**. Shared services are reached by **stable DNS aliases**
(`shared-postgres`, …), never bare service name (collision guardrail). Default is
**no published host ports** (DNS over the shared network); ports are allocated from
the ledger, inside the lock, only on explicit host-reachability requests.

**Module map** (`internal/*`, ARCHITECTURE §6 is the full table): `cli` (cobra tree
+ global flag/error/log contract), `config`, `template`, `merge`, `generate`,
`docker` (SDK read-only + compose CLI driver), `workspace` (the differentiator:
shared-stack lifecycle + ref-counting), `state` (SQLite ledger), `lock` (flock spine),
`provision` (pgx), `git`/`gitx`, `secrets`, `proxy`, `trust`, `dns`, `tunnel`,
`doctor`, `hooks`, `health`, `orchestrate` (the up saga), `xdg`, `migrate`. Public
plugin contract `pkg/pluginsdk` is **deferred to v2**.

## The #1 rule: lock-first concurrency

Concurrent invocations (two terminals, IDE + terminal, a watch script) are the
top architectural risk. Every operation that mutates the ledger **or** the shared
stack MUST go through `internal/lock` (coarse `gofrs/flock` advisory lock) — from
the first commit, never retrofitted. SQLite alone is **not** safe under concurrent
writers (especially on WSL2/9p); WAL + `busy_timeout=5s` + the flock are required
together. Reads are lock-free snapshots. Port allocation, network-ensure, ref-count
rows, and `CREATE ROLE` all happen inside the lock.

## Load-bearing "verified corrections" (these change the naive choice)

DECISIONS.md records these; they are the difference between "compiles in a tutorial"
and "compiles today". The ones most likely to bite:

- **Docker SDK is `github.com/moby/moby/client`, NOT `docker/docker/client`** (the
  latter is deprecated as of Docker v29 and fails as a hard module-path error). In
  `moby/moby/client` v0.4+, use `client.New(...)` and pass option structs
  (`Ping(ctx, PingOptions{})`, `ServerVersion(ctx, ServerVersionOptions{})`).
- **fang import path is `charm.land/fang/v2`** (vanity domain), not
  `github.com/charmbracelet/fang`. fang is a thin, removable wrapper over cobra.
- **YAML: `goccy/go-yaml`** (for `file:line:col` errors) is NOT byte-compatible with
  `yaml.v3` (emits `1.0` for `1`; randomizes map order — use ordered structs).
  `gopkg.in/yaml.v3` is archived.
- **Config layering merge REPLACES lists by default** (koanf), whereas the Python
  predecessor appended — supply a custom append merge or document `$merge: append`.
- **koanf, not viper** (viper force-lowercases keys → breaks Docker env names and
  `${ref}` keys). koanf is not goroutine-safe: load once, treat immutable; env keys
  arrive lowercased (needs an env-transform on merge).
- **Compose is built as a typed model + `compose-go/v2`**, never string-templated.
  Filter container enumeration on **your own label** (not just
  `com.docker.compose.project`), set `All=true`, exclude `oneoff=true`.
- **Postgres 18+ moved PGDATA** to `/var/lib/postgresql` (was `.../data`) — mounting
  the old path silently loses persistence. Provisioning uses `pgx/v5` with
  existence-guarded SQL (`CREATE DATABASE` is **not** idempotent); never `initdb.d`.
- **Secrets ↔ generation are coupled:** a host env var reaches a container only if
  the compose file references it; emit each secret name **per service** as a
  valueless `environment: [NAME]` key and pass the value via `exec.Cmd.Env`. A CI
  test must assert no secret value lands in any generated file.
- **`*.localhost` is not uniformly zero-config**; marker-fenced `/etc/hosts` edits
  are the only consistently reliable path. Trust uses the external `mkcert` binary,
  not `smallstep/truststore`.
- **git: shell out to system git** (≥2.30) behind `internal/git` (`gitx`) to inherit
  the user's SSH/credential setup; `go-git` is a build-tagged read-only fallback only.

Full dependency-risk register is at the bottom of DECISIONS.md — pin everything,
wrap risky ones.

## Current status (what exists vs. spec)

M0 foundations are implemented and green (`make ci`):

- `cmd/devstack` — thin entry: `argv[0]` alias dispatch + `--as` override + fang.
- `internal/cli` — cobra tree wrapped by fang; global `--json/--quiet/--debug/--verbose`;
  slog logging; `doctor` (real preflight matrix + `--json`), `alias add/remove/list`,
  `version` are functional. The rest of the spec-07 surface (`up`, `down`, `status`,
  `ws`, `shared`, `secrets`, `trust`, `dns`, `tunnel`, `template`, `import`, `self`,
  `workspace`) are **milestone-tagged stubs** in `internal/cli/stubs.go`.
- `internal/lock` — the flock spine (with a concurrency test).
- `internal/state` — modernc SQLite ledger, WAL/busy_timeout/foreign_keys, versioned
  migrations + backup, the spec-08 tables, keyed by Docker context.
- `internal/xdg` — XDG paths, WSL2 detection, `/mnt/*` refusal, 9p/networked-FS detection.
- `internal/docker` — read-only `Client` interface (+ `MockClient`), moby-backed impl,
  compose≥2.20 / git≥2.30 preflight.
- `internal/alias` — registry + symlink installer; `internal/version` — ldflags target.
- CI (`.github/workflows/`), `.goreleaser.yaml`, `Makefile`.

**Build order follows the ROADMAP**: M0 spine → M1 config+templating+generation →
M2 shared services + workspace lifecycle (the differentiator) → M3 multi-repo git →
then secrets (M4) / networking (M5) / onboarding+doctor+health+hooks (M6) / GA (M7).
Anything that mutates shared state goes through `internal/lock` from its first commit.

## Conventions specific to this repo

- New external-tool integrations go behind an `internal/` interface with a mock
  (see `internal/docker`), so race/unit tests run without a real daemon.
- Errors that wrap an external command should carry the command + exit code +
  stderr and map known signatures to one-line remediations (`doctor` is the model;
  ARCHITECTURE §7.6). Config errors render as `file:line:col`.
- Headline outputs need a non-TTY `--json`/`--quiet` contract (ARCHITECTURE §7.9);
  `doctor --json` is the existing example.
- Migrations in `internal/state` are append-only and forward-only; never edit a
  released one.
