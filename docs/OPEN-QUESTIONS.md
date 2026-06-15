# Open Questions — decisions to lock before / while building

These are the decisions that materially change the design and that only you can make. Each has a recommendation; record your answer inline so the specs can be finalized. Ordered by how early they block work.

---

### Q-DAEMON · CLI-only vs background agent — **blocks M0**
v1 is designed as a **stateless CLI** (no daemon): ref-count drift is reconciled lazily on the next command, and there is **no automatic autostop** of shared services when their last consumer goes down. A background agent (systemd `--user` / launchd) would enable autostop-on-zero-ref and a live dashboard, but adds a whole new install/IPC/cross-platform surface.
- **Recommendation:** CLI-only for v1; accept lazy reconcile; `shared gc` / `doctor --fix` for manual cleanup. Revisit a daemon in v2 alongside the dashboard.
- **Your answer:** _______

### Q-PLATFORM · target priority — **blocks M0/M7 CI**
Brief says macOS + Linux + WSL2, all "undefined" priority. WSL2 needs the most special-casing; macOS needs its own CI runner and has the worst resolver/trust quirks.
- **Recommendation:** Linux + WSL2 first, macOS fast-follow. Native Windows (non-WSL2) explicitly **out of scope** (alias symlinks → `.cmd` shims at best).
- **Your answer:** _______

### Q-RUNTIME · supported container runtimes — **blocks M0 preflight + M2**
Design requires **Docker Engine + `docker compose` plugin v2.20+**. Many OSS devs avoid Docker Desktop for licensing reasons and use Colima/Rancher/Podman/Lima.
- **Recommendation:** Docker + compose v2.20+ only for v1; Podman/rootless/Colima/Lima **out of scope** (documented, with a clear `doctor` error). Revisit if your audience needs it.
- **Your answer:** _______ *(Does your team / target audience need non-Docker support?)*

### Q-T · which text-template engine — **blocks M1** (see [DECISIONS D2](DECISIONS.md#d2))
Compose generation is programmatic (compose-go) either way. The fork is the engine for **text artifacts + user-authored templates**:
- **Option A — stdlib `text/template` + sprig** (recommended for clean-slate: lean, zero engine-maintenance risk; weaker inheritance).
- **Option B — `gonja/v2`** (pure-Go Jinja2: richer authoring, inheritance, mirrors devdock heritage; single-maintainer dep).
- **Recommendation:** A, unless you want Jinja-style authoring for third-party templates.
- **Your answer:** _______

### Q-S · which two secrets providers for v1 — **blocks M4**
Research recommends shipping exactly **two** behind the pluggable interface; Vault/1Password/Doppler defer.
- You originally named **Infisical + AWS**. Research recommends **SOPS+age (offline, no-account default) + AWS** for the best OSS first-run.
- **Recommendation:** ship **SOPS+age + AWS + Infisical** if effort allows (Infisical was an explicit ask); otherwise SOPS+age + AWS and Infisical as the first 1.x plugin.
- **Your answer:** _______ *(Which providers do your teams actually use today?)*

### Q-PROXY · reverse proxy — **blocks M5** (default already chosen, confirm)
- **Recommendation:** **Caddy** (label-driven, one-label local HTTPS via `caddy-docker-proxy`). Traefik/nginx remain pluggable behind the `Proxy` interface.
- **Your answer:** _______ *(Any existing nginx/Traefik investment to honor?)*

### Q-CA · local CA strategy — **blocks M5**
- **Recommendation:** shell out to the maintained **`mkcert`** binary (auto-install or bundle) rather than importing the unmaintained `smallstep/truststore`. Means a runtime dependency on mkcert.
- **Your answer:** _______ *(Is a runtime mkcert dependency acceptable, or must trust be fully self-owned in pure Go?)*

### Q-GEN · are generated artifacts committed or gitignored — **blocks M1**
`docker-compose-<env>.yaml` + Dockerfiles: commit them (stable, reviewable diffs — but you must fight `compose-go` normalization churn with golden output) or gitignore + always regenerate (freer, but no review trail)?
- **Recommendation:** **gitignore + regenerate** by default; offer a `--commit-artifacts` mode for teams that want reviewable diffs.
- **Your answer:** _______

### Q-MIGRATE · devdock import path — **affects M1 scope**
Your friend's existing devdock users are the most likely early adopters. A `devstack import` reading an old `project.yaml` → new `workspace.yaml` + `devstack.yaml` split is low-cost, high-leverage. (Note: not byte-compatible — clean-slate — so it's a converter + migration guide, not a drop-in.)
- **Recommendation:** include `devstack import` + a migration guide in v1.
- **Your answer:** _______

### Q-NAME · canonical tool name + alias set — **RESOLVED**
Canonical name **`devstack`** (matches `devstack.yaml` / `apiVersion: devstack/v1`), built from `./cmd/devstack`. The Go module + GitHub repo are **`github.com/open-source-cloud/devstack`** (the local checkout folder is `devstack`). Aliases **`rq`** and **`uranus`** dispatch via `argv[0]` symlinks.
- **Decision:** binary/package name `devstack`; aliases `rq`, `uranus`. Load-bearing for the install-method detection + upgrade-remediation strings in [spec 14](specs/14-self-update-and-migration.md) (Homebrew formula / `.deb`/`.rpm` package name = `devstack`) and the alias-relink step in [spec 13](specs/13-doctor-diagnostics-and-teardown.md)/[spec 14](specs/14-self-update-and-migration.md). The OpenStack DevStack name collision is accepted for now; revisit only if a public distribution-name conflict arises.

---

### Q-PROFILE · profile definition plane + default-profile policy — **blocks M6 (feature #4)** (surfaced by [spec 12](specs/12-service-profiles-and-selective-up.md))
Should service slices be defined **per-repo** (each `devstack.yaml` carries its own Compose `profiles:` membership tags), **at the workspace level** (`workspace.yaml groups:` naming cross-repo slices like `frontend`), or **both**? And what does `up` start with no `--profile`? Per-repo maximizes repo portability but no single repo can express a cross-repo slice; workspace `groups` express cross-repo slices but couple the workspace to service names inside repos it doesn't own. Sub-question: should `down --profile`/`stop --profile` exist for pause-without-teardown, or is keeping `down` whole-project sufficient for v1?
- **Recommendation:** **both**, unioned by name; `defaultProfile` opt-in in `workspace.yaml` with `all` as the no-config default; keep `down` whole-project (enumerate by tool-owned label, never `--profile`) and defer `stop --profile`.
- **Your answer:** _______

### Q-SAGA-PARALLEL · parallel vs sequential project `compose-up` — **affects M6 (feature #1)** (surfaced by [spec 09](specs/09-orchestration-and-onboarding.md))
Should the saga's `compose-up` phase bring multiple project stacks up **in parallel** (faster on multi-project workspaces, but interleaves image-pull progress and complicates the nested checklist + per-project compensation) or **sequentially** for clarity?
- **Recommendation:** sequential in v1; add parallel behind a `--parallel` flag once the checklist model proves stable.
- **Your answer:** _______

### Q-HEALTHWAIT · who owns the readiness poll — **affects M6 (feature #3)** (surfaced by [spec 10](specs/10-health-readiness-and-ordering.md))
Should the intra-project wave rely on `docker compose up --wait` (reserving the read-only SDK poll strictly for the cross-project shared-stack gate), or should `internal/health` **always** own the poll for uniform `--json` records and a single timeout/backoff policy?
- **Recommendation:** own the poll uniformly; use `--wait` only as belt-and-suspenders within a project.
- **Your answer:** _______

### Q-HOOK-SCOPE · `firstRun` data-volume identity — **affects M6 (feature #5)** (surfaced by [spec 11](specs/11-lifecycle-hooks.md))
Should `firstRun`'s scope key be the provisioned `(db, role)` tuple (survives a Postgres image upgrade that keeps the volume) **or** the Docker named-volume id (re-arms whenever the volume object is recreated)? They diverge on the "upgraded the Postgres major but kept the data" path.
- **Recommendation:** the `(db, role)` tuple, keyed alongside the `provisioned` ledger; revisit if image-upgrade re-seeding becomes a real workflow.
- **Your answer:** _______

### Q-DOCTOR-FIX · interactive tier for borderline repairs — **affects M6 (feature #2)** (surfaced by [spec 13](specs/13-doctor-diagnostics-and-teardown.md))
Should `doctor --fix` ever offer an *interactive* (non-`--yes`) tier for borderline-destructive repairs (reassigning a port a foreign process holds, recreating a drifted *stateless* shared service), or is the safe/destroy line absolute?
- **Recommendation:** keep `--fix` strictly non-destructive; route everything else through the named teardown / `shared` verbs.
- **Your answer:** _______

### Q-SNAP-RETENTION · snapshot retention / prune policy — **affects v2 (feature #7)** (surfaced by [spec 15](specs/15-db-snapshot-restore.md))
The content-addressed snapshot store grows unbounded without a policy. What is the default retention — keep-last-N per `(project,kind)`, a max-age, or a total-store-size budget — and should `db snapshot` auto-prune, or should pruning be an explicit `db gc --snapshots` only?
- **Recommendation:** explicit `db gc --snapshots` only by default (never silently delete a developer's data); offer an opt-in keep-last-N per `(project,kind)` once the workflow is in real use.
- **Your answer:** _______

### Q-SANITIZE-ENGINE · `db pull` sanitization mechanism — **affects v2 (feature #7)** (surfaced by [spec 15](specs/15-db-snapshot-restore.md))
Production-derived `db pull` dumps must be scrubbed before they land on disk. Is the sanitization transform a **built-in declarative `sanitize:` profile** (NULL/mask/hash/drop), a **shell-out to an external anonymizer**, or **both behind one interface** — given the single-static-binary constraint?
- **Recommendation:** built-in declarative `sanitize:` profile for the common PII/token/payment cases (keeps the static-binary promise); an external-anonymizer seam behind the same interface as a later escape hatch.
- **Your answer:** _______

### Q-DASH-STATS · dashboard CPU/mem default on vs opt-in — **affects v2 (feature #8)** (surfaced by [spec 16](specs/16-logs-and-dashboard.md))
A `ContainerStats` stream per container is heavy and VM-skewed on Docker Desktop/WSL2. Should CPU/mem default **on** in the dashboard (richer cockpit) or be **opt-in** behind `--stats` (cheaper, accurate-by-omission)?
- **Recommendation:** on for `dashboard` with `--no-stats` to disable; off for `logs` (it never needs stats).
- **Your answer:** _______

### Q-IDE-DEBUGPORTS · debug-port allocation lifetime — **affects v2 (feature #9)** (surfaced by [spec 17](specs/17-devcontainer-ide-integration.md))
Should `debug: true` allocate a **persistent** published host port (stable across `up`, so a committed `launch.json` stays valid — but consumes the registry + a host port even when no debugger attaches) or an **ephemeral** port requested only by `ide gen`/an explicit `--debug` `up` (frees ports but the attach value can drift between runs)?
- **Recommendation:** persistent per-`(project,service)` debug port in the ledger, gated behind `debug: true`; confirm against the port-budget concern on low-port hosts.
- **Your answer:** _______

### Q-RESOURCES · memory-budget hard-fail tier — **affects v2 (feature #10)** (surfaced by [spec 18](specs/18-resource-limits-and-multi-arch.md))
The budget check warns when the declared sum exceeds the budget. Should `up` ever **hard-fail** above a *second*, higher threshold (e.g. declared sum > VM ceiling, guaranteeing OOM), or stay warn-only forever?
- **Recommendation:** warn-only in v2 (a dev may know their services won't all peak at once); revisit a `--strict-budget` opt-in only if OOM-during-`up` becomes a common support ticket.
- **Your answer:** _______

### Q-REGISTRY · single- vs multi-template bundles — **affects v2 (feature #11)** (surfaced by [spec 19](specs/19-template-registry.md))
Does `template push` support **multi-template bundles** (one artifact carrying a whole `php.*`/`node.*` family addressed by sub-path) or strictly **one template per tag**? Multi-template eases atomic team releases and shared `extends:` bases but complicates per-template digest pinning and partial cache GC.
- **Recommendation:** one artifact per template family with sub-path refs (`oci://…/templates:1.4.0//php.laravel`), pinned by the family digest. Record before wiring `push`.
- **Your answer:** _______

### Q-SIGN-TRUST · cosign keyless vs keyed default — **affects v2 (feature #11)** (surfaced by [spec 19](specs/19-template-registry.md))
Is cosign **keyless** (Fulcio/Rekor; requires reaching the public transparency log to verify) acceptable as the default for OSS teams, or must the default be **keyed** (a team-distributed public key, fully offline-verifiable)? Trades zero-key-management for an online verification dependency (relates to the offline-first posture in [Q-DAEMON](#q-daemon--cli-only-vs-background-agent--blocks-m0)).
- **Recommendation:** keyed by default for offline determinism; keyless opt-in.
- **Your answer:** _______

### Q-TELEMETRY · default OTLP endpoint owner + privacy policy — **blocks shipping telemetry (feature #12)** (surfaced by [spec 20](specs/20-telemetry.md))
Where does the *default* OTLP endpoint live, who operates it, and what is its data-retention + source-IP-dropping policy? This must be answered and published before telemetry ships — the default endpoint is the project's standing privacy promise.
- **Recommendation:** if no project-operated collector exists at ship time, default the endpoint to **empty** (telemetry can be enabled but goes nowhere until a self-hoster sets `telemetry.endpoint`).
- **Your answer:** _______

### Q-REMOTE-LOCK · distributed lock + ledger authority — **affects later (feature #13)** (surfaced by [spec 21](specs/21-remote-shared-backend.md))
A local `gofrs/flock` cannot serialize two machines against one remote backend. What is the `DistLock` + ledger-authority strategy: a **Postgres advisory lock** with the cluster DB as the source of truth, a **dedicated coordinator daemon** (reintroduces a daemon — [Q-DAEMON](#q-daemon--cli-only-vs-background-agent--blocks-m0)), or an **external lease store**? Local SQLite degrades to a cache regardless.
- **Recommendation:** start with `pg_advisory_lock` on the shared cluster Postgres (zero new infra, crash-safe, reuses `pgx/v5`); confirm the ledger becomes remote-authoritative with local SQLite as a cache.
- **Your answer:** _______

### Q-REMOTE-TENANT · tenant identity for per-user isolation — **affects later (feature #13)** (surfaced by [spec 21](specs/21-remote-shared-backend.md))
On a shared cluster, what identifies a tenant for role/db/bucket naming and `db gc` ownership: the **OS username**, an explicit `--as`/config-declared team identity, or the **cluster-auth identity**? Naming the role after the project is *not* isolation when two developers both have a project `app`.
- **Recommendation:** an explicit config-declared/`--as` team identity (stable, collision-free, decoupled from local OS accounts); fall back to OS username only as a default seed.
- **Your answer:** _______

## Decisions already made (recorded from our conversation)
- Ambition: **open-source product**.
- v1 scope: **all four pillars** (with the M0–M3 "core 1.0" phasing recommended in [ROADMAP](ROADMAP.md)).
- Config: **clean-slate schema** (not a devdock drop-in).
- Capacity: **solo, full-time (~40h/week)**.
- Compose generation: **programmatic via compose-go**, not string-templated YAML.
- Name: canonical binary **`devstack`**; aliases **`rq`, `uranus`**; module + GitHub repo **`github.com/open-source-cloud/devstack`** (see [Q-NAME](#q-name--canonical-tool-name--alias-set--resolved)).
