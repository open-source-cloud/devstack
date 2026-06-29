# PROGRESS — milestone completion frontier

Tracks the nightly autonomous build of M2-remainder → M7. See the approved plan
(`~/.claude/plans/purring-booping-cerf.md`) for the full chunk DAG, per-night recipe,
and verification. **Merge model:** PR-per-chunk, auto-merge on green CI.

## Status legend
`TODO` not started · `WIP` in a worktree/branch · `REVIEW` PR open, awaiting CI ·
`DONE` merged to main · `BLOCKED` waiting on a dep or a human step.

## Locked decisions
1. PR-per-chunk, auto-merge on green CI; milestone tags on completion.
2. `workspace.yaml shared:` is truth; `~/.devstack/config.yaml` = defaults/overrides
   merged *under* the workspace (deterministically, golden-tested).
3. Account/sudo-gated features: build logic + mock/localstack/temp-file tests; flag
   human steps + add a `doctor` probe. No interactive sudo / real cloud creds nightly.
4. Apache-2.0 license; flag `gh repo edit --visibility public` for the owner (M7).

## Frontier

### M2-remainder (core saga)
- [x] C1  state v2 `saga_phase` table + CRUD — REVIEW (PR `nightly/C1`)
- [ ] C2  docker `ContainerInspect`/`ContainerLogs` + mock — TODO  *(kickoff)*
- [ ] C3a config `healthcheck:`/`hooks:` structs — TODO  *(kickoff)*
- [ ] C3b `internal/health` thin poller — BLOCKED (C2, C3a)
- [ ] C3c generate emits `healthcheck:` — BLOCKED (C3a)
- [ ] C4  `internal/hooks` thin runner — BLOCKED (C3a)
- [ ] C5  `internal/orchestrate` core saga — BLOCKED (C1,C2,C3b,C4)
- [ ] C6  CLI `up`/`down` — BLOCKED (C5)
- [ ] C7  CLI `status` — BLOCKED (C5,C3b)
- [ ] C8  `shared gc` + `doctor --rebuild-state` — BLOCKED (C2)

### M4 secrets (parallel track)
- [ ] S1 core (`secret://` parser, Provider iface, Registry, batched Resolve) — TODO
- [ ] S2 SOPS+age — BLOCKED (S1)
- [ ] S3 AWS SM+SSM — BLOCKED (S1)
- [ ] S4 Infisical (gated) — BLOCKED (S1)
- [ ] S5 keyring + `secrets login/keygen` — BLOCKED (S1)
- [ ] S6 post-render resolve + env injection + leak test — BLOCKED (S1,S2,C5)

### M5 networking (parallel track)
- [ ] N1 `internal/proxy` (Caddy labels) — TODO
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
- [ ] G1 integration lane (`//go:build integration`) — TODO (can scaffold early)
- [ ] G2 macOS arm64 CI runner — TODO
- [ ] G3 docs (quickstart/migration/threat-model/troubleshooting) — TODO
- [ ] G4 goreleaser tap + `.deb`/`.rpm` + Apache LICENSE + tag `v1.0` — partial (LICENSE done)
- [ ] G5 two-terminal race tests — BLOCKED (C5)

## Human steps pending (owner)
- Make the repo public when GA bits land: `gh repo edit open-source-cloud/devstack --visibility public --accept-visibility-change-consequences` (history is secret-clean).
- (M5, when those land) run `sudo devstack trust install`, verify HTTPS in a browser; real cloudflared route needs a Cloudflare account + manual wildcard CNAME.

## Night log
- (init) scaffolding: PROGRESS.md + Apache-2.0 LICENSE/NOTICE + nightly cron. Next ready: C1, C2, C3a, S1, N1..N4, X1, X8, X9, G1.
