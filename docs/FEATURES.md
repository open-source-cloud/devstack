# Feature Backlog

The four requested pillars plus features surfaced during design. Each carries a rough effort and a milestone tag. "v1" here means *part of the full v1 vision*; see [ROADMAP](ROADMAP.md) for the recommended release phasing (core 1.0 = M0–M3, then 1.1–1.3).

## Core pillars (requested)

| Pillar | Where | Notes |
|---|---|---|
| Templating + generation | [spec 02](specs/02-templating-and-generation.md) | Typed compose via compose-go; text-template for files. |
| Workspaces + shared services | [spec 03](specs/03-workspaces-and-shared-services.md) | **The differentiator.** One Postgres/Redis/MinIO, per-project isolation. |
| Multi-repo git + CLI aliasing | [spec 06](specs/06-git.md), [spec 07](specs/07-cli-and-aliasing.md) | System-git wrapper; `argv[0]` aliasing (`rq`, `uranus`). |
| Secrets | [spec 04](specs/04-secrets.md) | Pluggable providers; no plaintext on disk. |
| Networking (HTTPS/proxy/tunnel) | [spec 05](specs/05-networking.md) | Caddy + mkcert + cloudflared. |

## Additional features (from the design brainstorm)

Ordered by value. Effort is person-weeks.

### v1 (part of the full vision)

**1. One-command onboarding (`up` / `bootstrap`) · 3w** — the headline DX promise. ([spec 09](specs/09-orchestration-and-onboarding.md))
A single idempotent command takes a fresh checkout (just `workspace.yaml` + per-repo `devstack.yaml`) to a fully running stack: preflight doctor → parallel clone/pull all repos → ensure shared network + services → provision DB roles/buckets → resolve secrets → install local CA → generate compose+Dockerfiles → `compose up -d` every project. Each step is a named, **resumable** phase with a live checklist; re-running skips satisfied phases. Replaces devdock's `config init` + `git clone` + `config docker -g` + `docker up -g` dance with one verb. *This composes every subsystem into one proven path — the strongest reason a team adopts the tool.*

**2. `doctor` diagnostics + preflight · 3w** — the support-load killer. ([spec 13](specs/13-doctor-diagnostics-and-teardown.md))
Checks the whole runtime contract and prints a categorized, actionable report: docker daemon + correct context (critical on WSL2), `docker compose` ≥ v2.20, git ≥ 2.30, disk for volumes, shared-network health, port conflicts (bind-test), CA trust state (host + Firefox/NSS + Windows on WSL2), secrets-provider reachability, stale ref rows vs live containers, `*.localhost`/resolver per platform. Every failure has a one-line remediation and, where safe, `--fix`. Includes `shared doctor` to reconcile the ledger. *Converts "it's broken" into "run doctor, it tells you exactly what to do."*

**3. Health/readiness gating with dependency ordering · 2w.** ([spec 10](specs/10-health-readiness-and-ordering.md))
Each template declares a typed healthcheck (tcp/http/exec/`pg_isready`/redis PING) and `dependsOn … condition: healthy`. The orchestrator brings shared services up first, polls health, and only then starts dependents; `up` blocks (per-service spinners + timeout) until healthy or fails fast with the unhealthy service's last logs inlined. *Eliminates the "app crashed because the DB wasn't ready" flakiness; substrate for seed/migrate hooks.*

**4. Service profiles / groups (selective up) · 1.5w.** ([spec 12](specs/12-service-profiles-and-selective-up.md))
Named groups (`core`, `frontend`, `payments`, `observability`) mapped onto Compose `profiles:` + the shared-service reference graph. `up --profile frontend` starts only that slice + the shared services it transitively `uses`. A `minimal` profile for low-RAM laptops. *On a 16GB laptop nobody runs 12 microservices at once; this leverages the shared-services graph directly.*

**5. Lifecycle hooks (pre/post up, migrate, first-run) · 2w.** ([spec 11](specs/11-lifecycle-hooks.md))
Declarative hooks at `preUp`/`postUp`/`preDown`/`firstRun` (once per provisioned volume, tracked in the state DB so it survives restarts — unlike `initdb.d`)/`postPull`, run on the host or via `compose exec`, with the same `${ref}`/secret interpolation. Canonical uses: DB migrations, first-run seeding, app-key generation, `npm/composer install`. *The missing glue between "containers up" and "app actually works"; generalizes devdock's hard-coded entrypoint logic.*

**6. Update notifications + signed self-update · 1w.** ([spec 14](specs/14-self-update-and-migration.md))
Throttled (~daily), opt-out background version check → one-line footer; `self update` via `go-selfupdate` against goreleaser releases with minisign/cosign verification; **detects Homebrew/dpkg-managed installs and refuses to self-replace**, directing to the package manager. *Painless, trustworthy updates matter for shipping security fixes given CVE-prone deps.*

### v2

**7. DB seed / snapshot / restore for shared services · 3w.** ([spec 15](specs/15-db-snapshot-restore.md)) (distinct from the v1 `db gc` orphan-reaper in [spec 13](specs/13-doctor-diagnostics-and-teardown.md); this item is the richer `db snapshot/restore/reset` data workflow.)
`db snapshot [name]` (pg_dump / redis RDB / `mc mirror`) into a content-addressed store; `db restore <name>`; `db reset` (drop+recreate role/db + replay seed). Per-project (using the per-project role isolation) so snapshots never clobber another project. Optional `db pull` from a sanitized remote dump feeds the `firstRun` hook. *Branch-switching and "I broke my local data" are daily pain; uniquely enabled by per-project-DB-on-shared-postgres.*

**8. Log aggregation + live TUI dashboard · 4w.** ([spec 16](specs/16-logs-and-dashboard.md))
`dashboard` opens a full-screen view: every shared + project service with state/health/CPU/mem (Docker stats stream), per-project local + tunnel URL, and which projects reference each shared service (the ref ledger made visible). Bottom pane multiplexes/filters logs across services. `logs <svc>… [-f]` is the non-interactive sibling. *The day-to-day cockpit; surfaces the shared-services graph that is the product's core idea.*

**9. Devcontainer / IDE integration · 2w.** ([spec 17](specs/17-devcontainer-ide-integration.md))
`ide gen` emits per-repo `.devcontainer/devcontainer.json` pointing at the generated compose service + shared network, a multi-repo `.code-workspace`, per-language debugger attach configs, and the `# yaml-language-server: $schema=` modeline for config autocomplete. *Meets developers in their editor; mostly generation on the existing engine.*

**10. Resource limits & multi-arch image strategy · 1.5w.** ([spec 18](specs/18-resource-limits-and-multi-arch.md))
Per-service CPU/memory limits (compose `deploy.resources`) + a workspace memory budget `doctor`/`up` checks against host RAM (acute on Docker Desktop's fixed VM). Templates declare multi-arch refs; the tool prefers native images and warns on emulated (qemu) ones. *Apple Silicon emulation and unbounded shared services are the two big "why is my machine on fire" complaints.*

**11. Team config sharing via versioned template registry · 2.5w.** ([spec 19](specs/19-template-registry.md))
Turn the OCI/git template sources into a curated workflow: `template push oci://ghcr.io/org/templates:1.4.0`, a pinned `templates:` lockfile (ref+digest) for byte-identical renders, `template diff`/`update`, `template lint`/`test` (golden render), `template init` scaffolding. *How a platform team ships "the way we run services" reproducibly to everyone — turns a personal tool into team infrastructure.*

### Later

**12. Opt-in anonymous telemetry · 1.5w.** ([spec 20](specs/20-telemetry.md))
Strictly opt-in (explicit first-run prompt, default OFF, one-flag disable, documented payload): command names, anonymized error categories, OS/arch, tool/docker/compose versions — never repo names, paths, secrets, or template contents. Self-hostable OTel endpoint; local `telemetry show`. *Lets a small OSS team learn e.g. "WSL2 trust install fails 30% of the time" without betraying trust.*

**13. Remote / cloud shared-services backend · 8w.** ([spec 21](specs/21-remote-shared-backend.md))
Generalize the shared stack to run on a remote Docker host (DOCKER_HOST/SSH context) or a team "shared dev cluster" — one warm, seeded Postgres for a whole team, zero local DB containers. The ref-counting, per-project provisioning, and DNS/alias model are backend-agnostic; this swaps *where* containers run. Pairs with the tunnel work. *The ambitious devbox/Codespaces-class frontier — correctly deferred until the local model is rock-solid.*

### Beta DX lane (post-M7, ships in the v0.2.x 0.x line)

Surfaced after the M0–M7 build landed (see [PROGRESS](../PROGRESS.md)). These add the **interactive layer the tool has lacked** — there is no TUI in the codebase today — plus the release-engineering glue. They are strictly additive over the shipped substrate and all ship in the **0.x beta line**; none cuts v1.0. Every TUI is built on **Bubble Tea v2 + the Charm plugin stack** (`bubbles`/`lipgloss/v2`/`huh/v2`, all CGO-free) behind one shared `internal/prompt` theme, and every interactive flow keeps a `--json`/flag-driven equivalent (the headline-output contract). **Recommended build order: #17 first** (the release thin slice — it is the gate every other item ships through, and it fixes a live self-update bug), then #14 → #15 → #16.

**14. Interactive `init` wizard · 2w — the file-authoring front door.** ([spec 22](specs/22-init-wizard.md))
Guided `devstack init`: a Bubble Tea v2 TUI (left engine-picker + right live `workspace.yaml` preview) picks the shared engines (filtered by template `Provides`), fills typed params from `ParamSpec`, and emits a structurally-validated `workspace.yaml` via one shared goccy ordered emitter (`scaffold.EmitWorkspaceYAML`, which `import` is refactored onto). A fully equivalent flag/`--json`/`--no-input` path runs off-TTY. Pure YAML authorship — no ledger, no Docker, no flock. Introduces the reusable `internal/prompt` substrate the rest of this lane consumes. *Onboarding stops assuming a hand-written config.*

**15. Interactive template & Dockerfile authoring (TUI) · 2.5w.** ([spec 23](specs/23-template-authoring.md))
`template new`: a Bubble Tea v2 wizard with a live preview pane (the real `template.Resolve` → `generate.LintResolved` path) that scaffolds `template.yaml` + an optional `build/` tree (Dockerfile) + a golden fixture into the `$DEVSTACK_HOME/templates` store. A thin front-end over the M1 engine; app-vs-engine branch; byte-stable via `writeIfChanged`; lock/ledger/secret-free. Adds the delimiter-collision / param-type / meta-templating lints (shared with `template lint`, so the two can't drift) and closes a real spec-19 gap. *Turns "the way we run services" into something a platform team authors, not hand-writes.*

**16. `.env` ingestion → secrets/vars · 2.5w.** ([spec 24](specs/24-env-ingestion.md))
`devstack secrets ingest [<.env>]`: converts a committed dotenv into SOPS+age `secret://` refs + **inlined** config-var literals (not `${env.KEY}` — that resolves empty once the `.env` is deleted; `--from-host` opts a key back to ambient host/CI sourcing) and rewrites the target `devstack.yaml`. `--to sops|aws-sm|infisical`; scaffolds a default sops provider when none is declared. Adds the secrets **Pusher** write capability (the existing providers were Resolve-only); parses via the already-vendored `compose-go/v2/dotenv`; idempotency is decrypt-and-compare. No plaintext on disk (CI leak-test). *The migration on-ramp off committed `.env` files.*

**17. Release automation + 0.x conventional-commit versioning · 0.75w thin (+0.75w wizard) — the v0.2.0 gate.** ([spec 25](specs/25-release-automation.md))
Conventional commits on `main` → `svu next --v0` → tag + goreleaser **in one workflow** using the built-in `GITHUB_TOKEN` (no PAT/App token), gated by an owner-set `RELEASE_ENABLED` repo variable (default off = the kill-switch); a human-cut tag still releases via the same workflow. Fixes the load-bearing **ldflags v-prefix bug** (`{{.Version}}` stamps `0.1.0`, which `x/mod/semver` rejects) that currently makes the shipped spec-14 update-notifier + `self update` treat a released binary as a dev build and never offer updates. Adds a grouped goreleaser changelog, a PR-title conventional-commit lint, and a CI `v0.*` guard (stay 0.x: BREAKING → minor, never 1.0.0). Optional `devstack release` maintainer wizard. *Everything else in this lane ships through this pipeline — build it first.*

---

## At a glance

| # | Feature | Effort | Milestone |
|---|---|---|---|
| 1 | One-command onboarding | 3w | v1 (M2/M6) · [spec 09](specs/09-orchestration-and-onboarding.md) |
| 2 | `doctor` diagnostics | 3w | v1 (M0/M6) · [spec 13](specs/13-doctor-diagnostics-and-teardown.md) |
| 3 | Health/readiness gating | 2w | v1 (M6) · [spec 10](specs/10-health-readiness-and-ordering.md) |
| 4 | Service profiles | 1.5w | v1 (M6) · [spec 12](specs/12-service-profiles-and-selective-up.md) |
| 5 | Lifecycle hooks | 2w | v1 (M6) · [spec 11](specs/11-lifecycle-hooks.md) |
| 6 | Self-update + notifications | 1w | v1 (M0/M6) · [spec 14](specs/14-self-update-and-migration.md) |
| 7 | DB snapshot/restore | 3w | v2 · [spec 15](specs/15-db-snapshot-restore.md) |
| 8 | Log aggregation + dashboard | 4w | v2 · [spec 16](specs/16-logs-and-dashboard.md) |
| 9 | Devcontainer/IDE integration | 2w | v2 · [spec 17](specs/17-devcontainer-ide-integration.md) |
| 10 | Resource limits + multi-arch | 1.5w | v2 · [spec 18](specs/18-resource-limits-and-multi-arch.md) |
| 11 | Versioned template registry | 2.5w | v2 · [spec 19](specs/19-template-registry.md) |
| 12 | Opt-in telemetry | 1.5w | later · [spec 20](specs/20-telemetry.md) |
| 13 | Remote/cloud shared backend | 8w | later · [spec 21](specs/21-remote-shared-backend.md) |
| 14 | Interactive `init` wizard (TUI) | 2w | v0.2 beta DX (M8) · [spec 22](specs/22-init-wizard.md) |
| 15 | Template & Dockerfile authoring (TUI) | 2.5w | v0.2 beta DX (M8) · [spec 23](specs/23-template-authoring.md) |
| 16 | `.env` ingestion → secrets/vars | 2.5w | v0.2 beta DX (M8) · [spec 24](specs/24-env-ingestion.md) |
| 17 | Release automation + 0.x versioning | 0.75w+0.75w | v0.2 gate (M8) · [spec 25](specs/25-release-automation.md) |
