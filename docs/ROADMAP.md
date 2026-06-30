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
- Golden-file conformance suite; deterministic-output CI check; *(optional)* `devstack import` from devdock `project.yaml` ([spec 14](specs/14-self-update-and-migration.md) `internal/migrate` — pure YAML→YAML, no lock, ships early).

### M2 — Shared services + workspace lifecycle (the differentiator) · **9w**
- Tool-owned external network (pinned name, idempotent ensure/teardown); shared stack + per-project stacks with `shared-*` aliases; collision lint.
- Ref-counting in SQLite (inside the lock) + self-healing reconcile from live Compose labels (`All=true`, exclude one-off); per-`(engine,major-version)` shared instances.
- Provision-on-demand per-project Postgres role/db (pgx, idempotent, PG18 volume path), redis index/prefix, minio bucket+key; ownership ledger + orphan gc.
- Port allocation inside the lock (host bind-test unioned with SDK port bindings); `shared status`/`doctor`/`gc`; never-recreate-stateful-shared guard.
- The `up`/bootstrap **saga** ([spec 09](specs/09-orchestration-and-onboarding.md)) begins landing here — its network/shared/provision/compose-up phases sit on this milestone's workspace+provision substrate; the health/hooks/profiles phases complete in M6.

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
> Specs: [09](specs/09-orchestration-and-onboarding.md) (saga) · [10](specs/10-health-readiness-and-ordering.md) (health) · [11](specs/11-lifecycle-hooks.md) (hooks) · [12](specs/12-service-profiles-and-selective-up.md) (profiles) · [13](specs/13-doctor-diagnostics-and-teardown.md) (doctor/teardown) · [14](specs/14-self-update-and-migration.md) (self-update/migration).
- Multi-phase resumable `up`/bootstrap **saga** with durable phase-state + compensating rollback; bubbletea checklist + plain fallback ([spec 09](specs/09-orchestration-and-onboarding.md)).
- Health/readiness gating (typed healthchecks, `dependsOn: healthy`, [spec 10](specs/10-health-readiness-and-ordering.md)); lifecycle hooks (`preUp`/`postUp`/`preDown`/`firstRun`/`postPull`) with idempotency ledger (solves the `initdb.d` gap, [spec 11](specs/11-lifecycle-hooks.md)); profiles/selective-up ([spec 12](specs/12-service-profiles-and-selective-up.md)).
- `doctor` capability-probe matrix with remediations and `--fix`; `doctor --rebuild-state` ([spec 13](specs/13-doctor-diagnostics-and-teardown.md)); self-update with install-method detection + alias relink + state migration ([spec 14](specs/14-self-update-and-migration.md)).
- `workspace destroy`/`uninstall` reversing **all** artifacts (network, volumes w/ confirm, CA from all stores, symlinks, keyring, cloudflared creds, cache) ([spec 13](specs/13-doctor-diagnostics-and-teardown.md)).

### M7 — Hardening, docs, cross-platform test matrix, 1.0 GA · **6w**
- Integration tests via `testcontainers-go`/dind lane (network/ref-count/provision/health) behind build tags; concurrency-race tests; documented manual macOS+WSL2 checklist.
- macOS arm64 CI runner for trust/resolver/Desktop-VM behavior; cache GC; document Docker Desktop licensing + Podman/rootless out-of-scope.
- Quickstart + migration guide, secrets threat model, troubleshooting; goreleaser tap + `.deb`/`.rpm`; tag 1.0.

### M8 — Beta DX & release-engineering lane (post-M7, v0.2.x 0.x line) · **~8w**
> Core M0–M7 has shipped (see [PROGRESS](../PROGRESS.md)). This lane is **strictly additive** over the existing substrate and **stays on the 0.x beta line** — it does NOT cut v1.0. The "tag 1.0" in M7 above is **superseded by the beta decision**: the project ships in beta (next release **v0.2.0**); 1.0 is a deliberate, later owner call, never reached by automation. "M8" is a *sequencing* label, not a version commitment.
> Specs: [25](specs/25-release-automation.md) (release automation) · [22](specs/22-init-wizard.md) (init wizard) · [23](specs/23-template-authoring.md) (template authoring) · [24](specs/24-env-ingestion.md) (.env ingestion). All TUIs are Bubble Tea v2 + Charm (`bubbles`/`lipgloss/v2`/`huh/v2`), CGO-free, behind one shared `internal/prompt` theme with a non-TTY/`--json` fallback.

**M8.0 — Release automation + 0.x versioning (the v0.2.0 gate) · 0.75w thin (+0.75w wizard).** ([spec 25](specs/25-release-automation.md))
- `.svu.yaml` (`v0: true`) + a `tag.yml` workflow: push-to-`main` → `svu next --v0` → push a `v*` tag (owner-gated `RELEASE_TOKEN` at **job** level so the kill-switch `if:` can read it) → the existing `release.yml`/goreleaser on the tag.
- Fix the ldflags v-prefix bug (un-mutes the shipped spec-14 self-update/notifier); add a grouped goreleaser changelog (`use: github` + `groups`/`filters`); PR-title conventional-commit lint; CI `v0.*` guard.
- Optional: the `devstack release` maintainer wizard + an additive `update.channel` (stable|prerelease) knob (extends spec 14, does not redefine it).
- **Sequence first** — every later item ships through it; the thin slice (~0.75w) is all that is needed to cut v0.2.0.

**M8.1 — `internal/prompt` + interactive `init` · 2w.** ([spec 22](specs/22-init-wizard.md))
- Introduce the Bubble Tea v2 + Charm stack behind `internal/prompt` (shared theme + non-TTY/`--json`/`--no-input` fallback so the headline-output contract holds and CI never drives Bubble Tea).
- `devstack init` authors a structurally-valid `workspace.yaml` via a shared `scaffold.EmitWorkspaceYAML` ordered emitter; **refactor `internal/migrate` onto the same emitter** (re-baseline its golden output intentionally). No flock, no Docker.

**M8.2 — `template new` authoring TUI · 2.5w.** ([spec 23](specs/23-template-authoring.md))
- One `scaffold.Build(Spec)` pure builder fed by both the wizard and the flag/`--from`/`--json` path; live `template.Resolve` → `generate.LintResolved` preview; app-vs-engine branch; `writeIfChanged` byte-stability.
- Implement the delimiter-collision / param-type / meta-templating lints **once** in shared lint code consumed by both `template new` preview and `template lint`. Reconcile spec-19's overstated `template init` description.

**M8.3 — `.env` ingestion + secrets Pusher · 2.5w.** ([spec 24](specs/24-env-ingestion.md))
- `secrets ingest` over the shipped SOPS+age/AWS/Infisical providers; net-new **Pusher** write capability (aws-sm/ssm/infisical) reusing existing auth plumbing; `compose-go/v2/dotenv` parse; decrypt-and-compare idempotency; default-sops-provider scaffold into `workspace.yaml`. No flock.
- Add a `doctor` probe for the `sops` binary min-version (stdin / `--input-type` support).

**Sequencing within M8:** M8.0 → M8.1 (lands `internal/prompt` + the shared emitter) → M8.2 (reuses both) → M8.3 (reuses prompt, adds the heaviest net-new backend). After each charm-dep add: re-run `make vuln` + the `CGO_ENABLED=0` static cross-build; no build tags may creep in.

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
| Beta DX lane (M8, 0.x — post-GA) | ~8 | +~2.5 months |

Calendar applies a 0.6–0.75 throughput factor (context-switching, Docker/WSL2/macOS debugging, dependency churn, docs, CI). Treat as planning ranges, not commitments.

## What makes it slower than it looks
- **Cross-platform is most of the cost.** macOS resolver/trust quirks, the WSL2 dual trust store, and Desktop-VM networking each need their own testing; hosted Linux CI can't cover them.
- **The shared-data lifecycle** (provision *and* reclaim, version conflicts, partial-failure rollback) is genuinely novel work, not glue.
- **Dependency treadmill** on pre-1.0/risky deps (see [DECISIONS](DECISIONS.md) risk register).

## Sequencing rule
Build M0's locking/state/preflight **before** anything that mutates shared state. Everything that touches the ledger or the shared stack goes through the lock from the first commit — retrofitting concurrency safety is the most expensive mistake available here.
