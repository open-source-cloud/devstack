# PROGRESS — milestone completion frontier

Tracks the nightly autonomous build of M2-remainder → M7. See the approved plan
(`~/.claude/plans/purring-booping-cerf.md`) for the full chunk DAG, per-night recipe,
and verification. **Merge model:** PR-per-chunk, auto-merge on green CI.

## Status legend
`TODO` not started · `WIP` in a worktree/branch · `REVIEW` PR open, awaiting CI ·
`DONE` merged to main · `BLOCKED` waiting on a dep or a human step.

## Locked decisions
1. PR-per-chunk, **merge only when ALL ≥9 CI checks are green** (main is unprotected,
   so plain `gh pr merge --auto` merges instantly — poll the checks first). Milestones
   are recorded in this night log, **NOT as git tags**: a non-semver tag (e.g. `m2-done`)
   breaks the `release-dryrun` job for every PR (goreleaser `git describe`). Only push
   `v*` tags, and only to cut a release.
2. `workspace.yaml shared:` is truth; `~/.devstack/config.yaml` = defaults/overrides
   merged *under* the workspace (deterministically, golden-tested).
3. Account/sudo-gated features: build logic + mock/localstack/temp-file tests; flag
   human steps + add a `doctor` probe. No interactive sudo / real cloud creds nightly.
4. Apache-2.0 license; flag `gh repo edit --visibility public` for the owner (M7).

## Frontier

### M2-remainder (core saga)
- [x] C1  state v2 `saga_phase` table + CRUD — **DONE** (PR #1, `9900b87`)
- [x] C2  docker `ContainerInspect`/`ContainerLogs` + mock — **DONE** (PR #2, `d291ef5`)
- [x] C3a config `healthcheck:`/`hooks:`/`dependsOn:` structs — **DONE** (PR #3, `901aaad`)
- [x] C3b `internal/health` thin poller — **DONE** (PR #4, `fa338aa`)
- [x] C3c generate emits `healthcheck:` + intra-project `depends_on` — **DONE** (PR #6, `a6f3195`)
- [x] C4  `internal/hooks` thin runner + `hook_run` CRUD — **DONE** (PR #5, `de08588`)
- [x] C5  `internal/orchestrate` core saga — **DONE** (C5a engine PR #8 `cb128b4`; C5b wiring PR #9 `301061a`)
- [x] C6  CLI `up`/`down` — **DONE** (PR #10, `855488f`) — verified e2e vs real Engine 29.5.3
- [x] C7  CLI `status` — **DONE** (PR #11, `92769be`)
- [x] C8  `shared gc` + `doctor --rebuild-state` — **DONE** (PR #12, `c34fb4b`)

**M2-remainder COMPLETE.** `up`→network→shared(health-gated)→generate→compose-up→hooks
proven green end-to-end against the host daemon (then torn down); re-run skips
satisfied phases; `--json` matches the spec contract; `down` decrements refs;
`shared gc`/`doctor --rebuild-state` maintain the ledger.

**Deferred from C5 (flagged, slot in as more saga phases — no engine change):**
- **provision** phase — needs the shared-Postgres **host-port coupling** (pgx
  connects from the host; default is no published ports). Decision is known
  (ledger-allocated published port wired into generate + the saga); it's the next
  M2 follow-up. Until then projects use the shared engine's root creds.
- **clone** (gitx), **secrets** (M4/S6), **trust** (N5), **firstRun** hooks
  (need the provisioned-volume scope_key) — each adds one phase.
- **Saga daemon e2e in CI** — needs **G1**'s isolation harness (parameterized
  `devstack-it-<pid>` network/prefix; the saga must never touch a real
  `devstack_shared`). Unit wiring is mock-tested; e2e was verified manually.

### M4 secrets (parallel track)
- [x] S1 core (`secret://` parser, Provider iface, Registry, batched Resolve) — **DONE** (PR #14, `bdba2ab`)
- [x] S2 SOPS+age — **DONE** (PR #31, shells `sops -d`, batch-per-file, RegisterBuiltins)
- [ ] S3 AWS SM+SSM — TODO  *(ready: S1 in; localstack-testable)*
- [ ] S4 Infisical (gated) — TODO  *(ready: S1 in)*
- [~] S5 keyring + `secrets login/keygen` — **PARTIAL**: `secrets keygen` (age keypair, PR #39) done. Remaining: `secrets login` keyring (zalando/go-keyring + WSL2 in-mem fallback).
- [x] S6 post-render resolve + env injection + leak test — **DONE** (generate valueless keys PR #41 + saga secrets phase PR #44: collect→Resolve→Compose.Env, value never on disk)

### M5 networking (parallel track)
- [x] N1 `internal/proxy` (Caddy route table + labels) — **DONE** (PR #15, `50e6686`)
- [x] N2 `internal/trust` (mkcert; sudo-gated) — **DONE** (PR #21, `ba3cb54`)
- [x] N3 `internal/dns` (/etc/hosts; sudo-gated) — **DONE** (PR #20, `8628134`)
- [x] N4 `internal/tunnel` (cloudflared; account-gated) — **DONE** (PR #23, `91f206f`)
- [x] N5 saga trust/proxy phase + doctor probes — **DONE** (proxy labels PR #29; doctor trust/dns probes PRs #33/#35; fenced trust saga phase PR #51) — **M5 COMPLETE**

### M6 (saga completion + glue)
- [x] X1 config completion — **DONE** (PR #27, `1c77d44`)
- [x] X2 `internal/health` full DAG — **DONE** (PR #37: BuildGraph/Cycle/Waves/RequireHealthchecks)
- [~] X3 hooks full — **PARTIAL**: workspace + project preUp/postUp wired into the saga (PR #46). Remaining: firstRun/postPull (need provision scope_key) + --skip-hooks/--force-hooks flags.
- [x] X4 profiles/selective-up — **DONE** (PR #53: internal/profile.Resolve — Q-PROFILE resolved). Saga --profile wiring is X5.
- [x] X5 orchestrate completion — **DONE** (PR #55: `up --profile` service-slicing wired into BuildUp — inactive projects drop out, compose-up restricted to active services, shared phase + health gate pruned to `active.Shared`; PR #57: spec-12 `memoryBudgetMB` over-budget warning). Follow-up (small): spec-native `COMPOSE_PROFILES`/`profiles:` emission.
- [x] X6 `internal/doctor` full matrix + `--fix` — **DONE** (trust/dns/shared probes PRs #33/#35/#48 + safe reconcile `--fix` PR #49)
- [~] X7 `workspace destroy`/`uninstall` — **PARTIAL**: `workspace destroy` done (PR #56: data-preserving teardown — stacks down, ref/port rows dropped under lock, orphaned shared warm-stopped via GC, `.devstack/` removed). Remaining: `--purge-data` (needs `docker volume rm` capability) + `uninstall` (machine-global incl. CA removal across host/Firefox/Windows).
- [x] X8 self-update notifier — **DONE** (PR #25, `28c4a78`)
- [ ] X9 `internal/migrate` + `import` — TODO

### M7 GA (rolling)
- [x] G1 integration lane (`//go:build integration`) — **DONE** (PR #17, `9b6dfca`); CI overhaul + `tests/` folder (functional + daemon e2e) — **DONE** (PR #18, `79f4eef`)
- [ ] G2 macOS arm64 CI runner — TODO
- [~] G3 docs — **PARTIAL**: QUICKSTART + TROUBLESHOOTING (PR #43). Remaining: migration + threat-model.
- [ ] G4 goreleaser tap + `.deb`/`.rpm` + Apache LICENSE + tag `v1.0` — partial (LICENSE done)
- [ ] G5 two-terminal race tests — TODO  *(unblocked: C5 in; add to `tests/e2e` behind DEVSTACK_E2E)*

## Human steps pending (owner)
- Make the repo public when GA bits land: `gh repo edit open-source-cloud/devstack --visibility public --accept-visibility-change-consequences` (history is secret-clean).
- (M5, when those land) run `sudo devstack trust install`, verify HTTPS in a browser; real cloudflared route needs a Cloudflare account + manual wildcard CNAME.

## Night log
- (init) scaffolding: PROGRESS.md + Apache-2.0 LICENSE/NOTICE + nightly cron + repo auto-merge enabled.
- (night 1) **C1 merged** (PR #1, `9900b87`) — `saga_phase` v2 migration + CRUD, race-clean, merge-on-green proven. Next ready (parallel): C2, C3a, S1, N1..N4, X1, X8, X9, G1.
- (night 2) **C2, C3a, C3b, C3c, C4 merged** (PRs #2–6) — the entire health/hooks substrate: read-only docker inspect/logs, config health/hooks/dependsOn structs, the `internal/health` poller, generate's compose `healthcheck:`/`depends_on` lowering, and the `internal/hooks` runner + `hook_run` ledger. Each green via `make ci`+determinism+`-tags=integration` against the local Engine 29.5.3; PR-poll-then-merge enforces the green gate (main is unprotected, so plain `--auto` would merge instantly). **C5 (core saga) is now unblocked** — its deps C1,C2,C3b,C4 are all in. Also ready: C8, S1, N1..N4, X1, X8, X9, G1.
- (night 2 cont.) **C5a, C5b, C6, C7, C8 merged** (PRs #8–12) — **M2-remainder complete**. The resumable/compensating orchestrate engine + the real up phases, the `up`/`down`/`status` CLI, and `shared gc`/`doctor --rebuild-state`. Verified the whole `up` saga end-to-end against the host daemon (shared-postgres came up *healthy* via the cross-project gate; re-run all-skips; `--json` matched; `down` dropped refs) then fully tore it down — the machine was clean before and after. Process note: poll ALL ≥9 PR checks to green before merge (an early poll once merged C6 before the slow checks registered — it passed retroactively, but the lesson stuck).
- (night 2 cont.) **G1 + CI/test overhaul merged** (PRs #17–18) — owner asked for a `tests/` folder + better CI mid-night. Added `tests/e2e` (functional CLI flows + a real `up→status→re-up→down` daemon e2e, `//go:build e2e`, daemon tier gated on `DEVSTACK_E2E=1` with self-cleanup); **consolidated CI from 7 jobs → 2** (`ci` cheap→expensive fail-fast ladder with module/build caching + Docker for the integration & e2e steps; `release-dryrun` separate). The full `ci` lane (incl. real-daemon integration + e2e) runs green on GitHub's runner in ~4.5 min. This also delivers the saga **daemon e2e** that C5 deferred. `make integration` / `make e2e` added.
- (night 2 cont.) **S1 + N1 merged** (PRs #14–15) — started the M4 + M5 parallel tracks: the `secret://` core (parser/Provider/registry/batched Resolve) and the Caddy proxy route table + labels. **Gotcha learned the hard way:** pushing a non-semver milestone tag (`m2-done`) broke `release-dryrun` on the next PR (goreleaser `git describe`); deleted the tag, re-ran, green. Decision #1 updated — milestones go in this log, never as tags. **Next ready (all parallel): the provision saga phase (host-port coupling), S2 (SOPS via shelling to `sops`), S3/S5, N2–N4 (sudo/account-gated → mock/temp-file tests), X1 config completion, X8 self-update notifier, X9 migrate/import, G1 integration lane (also unblocks the saga daemon e2e).** Consider G1 next — it activates the already-written `-tags=integration` tests (docker/health/hooks) in CI and provides the isolation harness the saga e2e needs.
- (night 2 cont.) **owner CI/test request + G1 + N3 + N2 merged** (PRs #17–21) — consolidated CI (7→2 jobs, fail-fast cheap→expensive, module/build cache, Docker for integration+e2e) + a `tests/` folder (functional + real-daemon e2e CLI, green on GitHub's runner ~4.5 min); then `internal/dns` (marker-fenced /etc/hosts) and `internal/trust` (mkcert wrapper), both behind injectable runners + fully temp-file/fake tested, with `dns setup|status|remove` and `trust install|uninstall|status` CLIs. **M5 has N1/N2/N3 done; N4 (tunnel) + N5 (saga trust phase) remain.** Per-PR `ci` now runs the e2e lane too, so docs PRs also take ~4.5 min — acceptable; add path filters later if noisy. **Next ready: N4 tunnel, N5 trust saga-phase, S2 (sops), the provision saga phase (host-port coupling), X1 config completion, X8 notifier, X9 import.**
- (night 2 cont.) **N4 merged** (PR #23, `91f206f`) — `internal/tunnel` (cloudflared wrapper: login/create/route, wildcard-route refusal, deterministic ingress→Caddy, non-local-secret refusal) + `tunnel` CLI. Also added CI `paths-ignore` (`**/*.md`/docs/LICENSE/NOTICE) so **docs-only PRs now skip the heavy lane entirely** (merge with no checks). **M5 networking N1–N4 complete; N5 (wire proxy labels into generate + saga trust phase) is the remaining M5 piece.** Broad frontier still open: S2/S3/S5 secrets, the provision saga phase (host-port coupling), X1 config completion + the M6 fan-out (X2–X9), G2–G5.
- (night 2 cont.) **X8 merged** (PR #25, `28c4a78`) — `selfupdate.Notifier`: throttled (≤1 network check/24h, XDG-cached), fail-silent, dev-build/`--json`/`--quiet`/`DEVSTACK_NO_UPDATE_NOTIFIER`-aware update notice wired into the CLI root's `PersistentPostRun`. **Session tally: 25 PRs merged, all green** — M2 complete (C1–C8), M4 S1, M5 N1–N4, M6 X8, M7 G1 + the consolidated CI/`tests` overhaul. **Remaining for `done`: N5; S2–S6; the provision saga phase (host-port coupling, the flagged M2 follow-up); X1–X7+X9 (M6); G2–G5 (M7).** Next-ready picks: X1 (config completion — unblocks X2/X3/X4), S2 (sops via shelling), X9 (devdock import), N5 (proxy-into-generate + trust saga phase), the provision phase.
- (night 2 cont.) **X1 merged** (PR #27, `1c77d44`) — config completion: `Service.MemoryMB` + `Workspace.MemoryBudgetMB` (spec 12/18) and `validateProfiles` (groups reference real services; `defaultProfile` names a defined group or reserved `all`), positioned. Unblocks **X2 (health DAG), X3 (hooks full), X6 (doctor matrix)** — all now ready; X4 waits on X2. **27 PRs merged this session.** Remaining for `done`: N5; S2–S6; provision saga phase (host-port coupling); X2–X7+X9; G2–G5.
- (night 2 cont.) **proxy labels wired into generate** (PR #29, `d0e04e8`, N5 part 1) — `proxy.LabelsForService` → caddy-docker-proxy labels merged onto routed services in `buildProjectService` (no-op when proxy disabled, golden/determinism unchanged). The proxy feature (N1 route table → labels) is now end-to-end. **29 PRs merged this session.** N5 remaining = saga trust phase + doctor trust/dns probes. Broad frontier still open: S2–S6, provision saga phase (host-port coupling), X2/X3/X4/X6/X7/X9, G2–G5.
- (night 2 cont.) **S2 merged** (PR #31) — SOPS+age secrets provider (shells `sops -d --output-type json`, batch-per-file, `RegisterBuiltins`), fake-runner tested (sops not on the runner yet). **M4 has S1+S2; S3/S4/S5/S6 now ready.** **31 PRs merged this session — M2 complete; M4 S1/S2; M5 N1–N4 + proxy-generate; M6 X1/X8; M7 G1 + CI/tests overhaul.** Remaining for `done`: S3–S6, the provision saga phase (host-port coupling), N5 trust phase + doctor probes, X2/X3/X4/X6/X7/X9, G2–G5.
- (night 2 cont.) **X6 trust probe merged** (PR #33) — `doctor` now reports local-CA readiness (mkcert/CA/certutil) as a non-fatal warning with remediation (decision-#3 self-verify for N2). **33 PRs merged this session.** X6 remaining: dns/shared doctor probes + a safe `--fix`. Frontier for `done`: S3–S6, provision saga phase (host-port coupling), N5 trust saga phase, X2/X3/X4/X5/X7/X9, G2–G5.
- (night 2 cont.) **X2 merged** (PR #37) — the workspace dependsOn DAG in `internal/health` (BuildGraph, Cycle with path, stable topo Waves, RequireHealthchecks generate-time guard), pure/unit-tested. **Unblocks X4 (profiles) + X5 (orchestrate consumes Waves).** 37 PRs merged.
- (night 2 cont.) **X2 + S5-keygen merged** (PRs #37, #39) — the health dependency DAG (cycles/waves/healthy-needs-healthcheck) and `secrets keygen` (offline age keypair via filippo.io/age, pairs with the S2 SOPS+age provider). Added the first new runtime dep (filippo.io/age, pure-Go; CI govulncheck clean — local go1.26.0 stdlib advisories do not apply to CI Go 1.25.x). **39 PRs merged.** Frontier: S3/S4/S6, S5-login(keyring), provision saga phase, N5 trust saga phase, X3/X4/X5/X7/X9, G2–G5.
- (night 2 cont.) **S6-generate merged** (PR #41) — `secret://` values in env.raw/prefixed now emit valueless compose keys (no ref/value in generated files), with a leak test. Remaining S6: the saga secrets phase (collect refs → batched Resolve → Compose.Env). **41 PRs merged.** Frontier: S3/S4, S5-login, S6-saga, provision phase, N5 trust saga, X3/X4/X5/X7/X9, G2–G5.
- (night 2 cont.) **G3-docs, S6-saga merged** (PRs #43–44) — quickstart/troubleshooting docs, and the **M4 capstone**: the up sagas secrets phase resolves secret:// refs (batched per provider) and injects values via the compose-up process env — values never on disk (§7.5), proven by tests. **44 PRs merged.** M4 now: S1,S2,S6 done; S3/S4 (cloud providers) + S5-login (keyring) remain. Frontier: S3/S4, S5-login, provision saga phase, N5 trust saga, X3/X4/X5/X7/X9, G2/G4/G5.
- (night 2 cont.) **X3-hooks merged** (PR #46) — full hook ordering in the saga (workspace preUp → per-project preUp→up→postUp → workspace postUp) via a generalized hookPhase. **46 PRs merged.** Frontier: S3/S4, S5-login, provision saga phase (unblocks firstRun + per-project DB isolation), N5 trust saga, X4/X5/X7/X9, G2/G4/G5.
- (night 2 cont.) **X6 complete** (PRs #48–49) — shared-ledger doctor probe + safe `doctor --fix` (non-destructive reconcile). doctor now has the full matrix (working-dir/fs/daemon/compose/git/state/trust/dns/shared) + a safe fix. **49 PRs merged.** Remaining (flagged/large/external-dep): provision saga phase (host-port coupling — flagged design call, D8 pgx-from-host), S3/S4 cloud providers (aws-sdk/infisical deps), S5-login (keyring), X4 (Q-PROFILE fork), X5 (needs provision), X7 (teardown), X9 (devdock format), N5 trust saga, G2/G5.
- (night 2 cont.) **N5 merged — M5 networking COMPLETE** (PR #51) — fenced trust phase in the up saga (opt-in mkcert install when httpsLocal; never aborts up). M5 N1–N5 all done. **51 PRs merged.** Remaining frontier (all flagged / heavy-dep / large-scope / needs-fresh-context): provision saga phase (D8 pgx host-port — flagged design call), S3/S4 (aws-sdk/infisical deps + localstack), S5-login (keyring), X4 (Q-PROFILE fork), X5 (needs provision+X4), X7 (teardown), X9 (devdock format), G2 (macOS CI), G5 (two-terminal race e2e).
- (night 2 cont.) **X4 merged** (PR #53) — the selective-up profile resolver (internal/profile.Resolve; Q-PROFILE RESOLVED = both planes unioned, `all` default). **52 PRs merged.** X5 (saga --profile slicing + DAG-pruned health) now ready. Remaining: X5, X7 (teardown), provision phase (flagged D8 host-port), S3/S4 (deps+services), S5-login (keyring), X9 (devdock format), G2 (macOS CI), G5 (race e2e).
- (night 3) **X5 + X7-destroy + memory-budget merged** (PRs #55–57) — selective-up is now end-to-end: `up --profile` slices the saga (inactive projects drop out, compose-up restricted to active services, shared phase + health gate pruned to `active.Shared`), plus the spec-12 `memoryBudgetMB` over-budget warning. `workspace destroy` lands the data-preserving teardown (stacks down, ref/port rows dropped under lock, orphaned shared warm-stopped via GC, `.devstack/` removed; volumes/DBs + machine-global state preserved). **57 PRs merged.** Remaining: X7 remainder (`--purge-data` needs `docker volume rm`; `uninstall` = machine-global incl. CA removal), S5-login (keyring + WSL2 fallback), S3/S4 (aws-sdk/infisical + localstack), provision saga phase (flagged D8 host-port), X9 (devdock format — flagged), G2 (macOS CI), G5 (race e2e), G3 docs remainder (migration/threat-model).
