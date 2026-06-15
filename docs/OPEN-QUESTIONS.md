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

### Q-NAME · canonical tool name + alias set — **blocks M0 installer/completion**
Docs use working name **`devstack`** (matches `devstack.yaml` / `apiVersion: devstack/v1`). The repo folder is `devdock-go`. You mentioned aliases `rq`, `uranus`.
- **Recommendation:** pick the canonical binary name and the full alias list up front so the installer + completion wire correctly. (Heads-up: `devstack` collides with OpenStack DevStack; `devdock` is your friend's tool. Consider a distinct name.)
- **Your answer:** _______ *(canonical name: ____ ; aliases: ____ )*

---

## Decisions already made (recorded from our conversation)
- Ambition: **open-source product**.
- v1 scope: **all four pillars** (with the M0–M3 "core 1.0" phasing recommended in [ROADMAP](ROADMAP.md)).
- Config: **clean-slate schema** (not a devdock drop-in).
- Capacity: **solo, full-time (~40h/week)**.
- Compose generation: **programmatic via compose-go**, not string-templated YAML.
