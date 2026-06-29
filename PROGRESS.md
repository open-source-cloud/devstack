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
- [ ] S2 SOPS+age — TODO  *(ready; prefer shelling to the `sops` binary — single static binary, no KMS-SDK bloat; integration test build-tagged)*
- [ ] S3 AWS SM+SSM — BLOCKED (S1)
- [ ] S4 Infisical (gated) — BLOCKED (S1)
- [ ] S5 keyring + `secrets login/keygen` — BLOCKED (S1)
- [ ] S6 post-render resolve + env injection + leak test — BLOCKED (S1,S2,C5)

### M5 networking (parallel track)
- [x] N1 `internal/proxy` (Caddy route table + labels) — **DONE** (PR #15, `50e6686`)
- [ ] N2 `internal/trust` (mkcert; sudo-gated) — TODO
- [ ] N3 `internal/dns` (/etc/hosts; sudo-gated) — TODO
- [ ] N4 `internal/tunnel` (cloudflared; account-gated) — TODO
- [ ] N5 saga trust phase + doctor probes — BLOCKED (N2,N3,C5)

### M6 (saga completion + glue)
- [ ] X1 config completion — TODO
- [ ] X2 `internal/health` full DAG — BLOCKED (X1,C3b)
- [ ] X3 hooks full — BLOCKED (X1,C4)
- [ ] X4 profiles/selective-up — BLOCKED (X1,X2)
- [ ] X5 orchestrate completion — BLOCKED (X2,X3,X4)
- [ ] X6 `internal/doctor` full matrix + `--fix` — BLOCKED (C8,N2,N3)
- [ ] X7 `workspace destroy`/`uninstall` — BLOCKED (N2,N3,S5,X6)
- [ ] X8 self-update notifier — TODO
- [ ] X9 `internal/migrate` + `import` — TODO

### M7 GA (rolling)
- [x] G1 integration lane (`//go:build integration`) — **DONE** (PR #17, `9b6dfca`); CI overhaul + `tests/` folder (functional + daemon e2e) — **DONE** (PR #18, `79f4eef`)
- [ ] G2 macOS arm64 CI runner — TODO
- [ ] G3 docs (quickstart/migration/threat-model/troubleshooting) — TODO
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
