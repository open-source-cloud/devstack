# Roadmap & Effort Estimate

> **Honest headline:** building all four pillars (shared services + git/aliases + secrets + networking) as a polished, cross-platform **open-source** product is a **~54 person-week (~1.2 person-year) program**, not a "v1." At solo full-time (~40h/week) with a realistic 0.6–0.75 throughput factor on integration-heavy cross-platform work, that's a **~13–15 month calendar** (range: 12 best-case → 18 if WSL2 trust/networking and the shared-data lifecycle prove as fiddly as the research warns).

## Strong recommendation: ship a "core 1.0" at M3, layer the rest

You asked for all four pillars in v1. The internals sequence so that **M0→M3 delivers the irreducible differentiator and a demoable tool**, and secrets/networking/onboarding layer on **without rework** because each sits behind an interface. So:

- **`1.0` = M0–M3** (≈ **28 person-weeks ≈ 8–9 months** solo): shared services + workspaces + templating + multi-repo git + aliases + **HTTP-on-localhost**. This is a tool people can adopt.
- **`1.1` = M4** secrets · **`1.2` = M5** local HTTPS + proxy + tunnel · **`1.3` = M6** orchestrated onboarding/doctor/health/hooks.
- **`1.0` GA polish = M7**.

This is a sequencing recommendation, not a scope cut — every pillar still ships. If you truly want all four before any release, the calendar is the full ~13–15 months.

---

## Milestones

Effort is **person-weeks at production OSS quality** (tests + docs + cross-platform). Build cross-cutting foundations (locking, state, preflight) **first** — they are the spine, and retrofitting them is far costlier.

### M0 — Spine, contracts & cross-cutting foundations · **6w**
- Repo layout (`/cmd`,`/internal`,`/pkg`,`/templates` via `go:embed`), Go 1.25 floor, goreleaser + GitHub Actions skeleton with `govulncheck` and a JSON-Schema↔struct drift check.
- cobra+fang root, `argv[0]` alias dispatch + `--as`, alias registry + symlink installer/uninstaller.
- XDG paths + WSL2 detection + `/mnt` refusal; SQLite state store (WAL/busy_timeout, versioned migrations + backup), **keyed by Docker context**.
- **`gofrs/flock` global lock helper wrapping all mutations — the concurrency spine.**
- `internal/docker` preflight (daemon, context, compose≥2.20, git≥2.30) + a mockable `moby/moby/client` interface.
- Decide & document: CLI-only (no daemon) model, the v1 cut-line, unknown-key/forward-compat policy, unified error/`--debug`/`--json` contract.

### M1 — Config + templating + generation (the deterministic pipeline) · **9w**
- Two-file schema (`workspace.yaml` + `devstack.yaml`) with `apiVersion` header; goccy parse w/ positions; jsonschema + validator/v10 + custom resolver (cross-refs, cycles).
- Template engine with custom delimiters + ported filters; `renderText`/`renderYAML`; `extends`-chain render-then-merge; layered merge with explicit list strategy.
- Compose model + `compose-go` validate/normalize; `${ref}` resolution against the workspace graph; `writeIfChanged` + atomic write; SHA-256 rebuild-hash ledger.
- Golden-file conformance suite; deterministic-output CI check; *(optional)* `devstack import` from devdock `project.yaml`.

### M2 — Shared services + workspace lifecycle (the differentiator) · **9w**
- Tool-owned external network (pinned name, idempotent ensure/teardown); shared stack + per-project stacks with `shared-*` aliases; collision lint.
- Ref-counting in SQLite (inside the lock) + self-healing reconcile from live Compose labels (`All=true`, exclude one-off); per-`(engine,major-version)` shared instances.
- Provision-on-demand per-project Postgres role/db (pgx, idempotent, PG18 volume path), redis index/prefix, minio bucket+key; ownership ledger + orphan gc.
- Port allocation inside the lock (host bind-test unioned with SDK port bindings); `shared status`/`doctor`/`gc`; never-recreate-stateful-shared guard.

### M3 — Multi-repo git + alias polish · **4w**
- `gitx` system-git wrapper (hardened env, `porcelain=v2` status, `GIT_ASKPASS` token shim, shorthand expansion, submodule/shallow opt-in).
- errgroup bounded-parallel clone/sync/status with per-repo context; bubbletea TUI + plain non-TTY/`--json` fallback.
- `ws clone/sync/status` (the headline status table) integrated with workspace + secrets token injection.

> **★ `1.0` core ships here (≈28w).**

### M4 — Secrets (interface + 2 providers) · **5w**
- Provider interface + Registry + Factory; `secret://` URI parser; batched post-render resolution coupled to generation.
- Two providers (confirm which — see OPEN-QUESTIONS): SOPS+age offline default + AWS (Secrets Manager + SSM via `aws-sdk-go-v2`), or Infisical + AWS; keygen/login.
- OS-keyring optional with in-memory fallback (WSL2 degrade); integration test asserting **no secret value lands in any generated artifact**.

### M5 — Networking: local HTTPS + proxy + tunnel · **7w**
- Caddy shared container via `caddy-docker-proxy`, label-driven routing; `<service>.<project>.localhost` default.
- Host CA trust via `mkcert` + WSL2 `certutil.exe` interop; trust status/install/uninstall; idempotent `/etc/hosts` edits for OS-resolver clients; local HTTPS **opt-in**.
- cloudflared optional managed tunnel (default DOWN, loud confirm, manual wildcard CNAME doc, secret-bearing-service refusal).

### M6 — Orchestrated onboarding, doctor, health, hooks, profiles · **8w**
- Multi-phase resumable `up`/bootstrap **saga** with durable phase-state + compensating rollback; bubbletea checklist + plain fallback.
- Health/readiness gating (typed healthchecks, `dependsOn: healthy`); lifecycle hooks (`postUp`/`preDown`/`firstRun`/`postPull`) with idempotency ledger (solves the `initdb.d` gap); profiles/selective-up.
- `doctor` capability-probe matrix with remediations and `--fix`; `doctor --rebuild-state`; self-update with install-method detection + alias relink + state migration.
- `workspace destroy`/`uninstall` reversing **all** artifacts (network, volumes w/ confirm, CA from all stores, symlinks, keyring, cloudflared creds, cache).

### M7 — Hardening, docs, cross-platform test matrix, 1.0 GA · **6w**
- Integration tests via `testcontainers-go`/dind lane (network/ref-count/provision/health) behind build tags; concurrency-race tests; documented manual macOS+WSL2 checklist.
- macOS arm64 CI runner for trust/resolver/Desktop-VM behavior; cache GC; document Docker Desktop licensing + Podman/rootless out-of-scope.
- Quickstart + migration guide, secrets threat model, troubleshooting; goreleaser tap + `.deb`/`.rpm`; tag 1.0.

---

## Totals

| Phase group | Person-weeks | Solo calendar (≈) |
|---|---|---|
| **Core 1.0** (M0–M3) | **28** | ~8–9 months |
| Secrets (M4) | 5 | +~1.5 months |
| Networking (M5) | 7 | +~2 months |
| Onboarding/glue (M6) | 8 | +~2.5 months |
| Hardening/GA (M7) | 6 | +~2 months |
| **Full v1 (all pillars)** | **54** | **~13–15 months** |

Calendar applies a 0.6–0.75 throughput factor (context-switching, Docker/WSL2/macOS debugging, dependency churn, docs, CI). Treat as planning ranges, not commitments.

## What makes it slower than it looks
- **Cross-platform is most of the cost.** macOS resolver/trust quirks, the WSL2 dual trust store, and Desktop-VM networking each need their own testing; hosted Linux CI can't cover them.
- **The shared-data lifecycle** (provision *and* reclaim, version conflicts, partial-failure rollback) is genuinely novel work, not glue.
- **Dependency treadmill** on pre-1.0/risky deps (see [DECISIONS](DECISIONS.md) risk register).

## Sequencing rule
Build M0's locking/state/preflight **before** anything that mutates shared state. Everything that touches the ledger or the shared stack goes through the lock from the first commit — retrofitting concurrency safety is the most expensive mistake available here.
