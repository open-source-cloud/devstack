# Spec 25 — Release automation, conventional-commit versioning & the v0.2.0 cut

**Module:** `.github/workflows`, `.goreleaser.yaml`, `.svu.yaml`, `internal/release`, `internal/prompt`, `internal/selfupdate`, `internal/version`, `internal/cli` (`release`, `version`) · **Milestone:** M7+ (release-engineering / beta polish lane — gates the v0.2.0 cut) · **Effort:** ~1.5w

## Purpose
Releases today are 100% manual: a human picks a tag and pushes it; `release.yml` then runs goreleaser. There is no commit analysis, no automatic version, no grouped changelog. This spec turns **conventional commits on `main`** into an **automatic `v<MAJOR.MINOR.PATCH>` tag → grouped changelog → goreleaser release**, wired so the existing tag-triggered release engine and `self update`/notifier ([spec 14](14-self-update-and-migration.md)) keep working unchanged. The project is **BETA**: the machinery is pinned to **0.x** (a breaking change bumps the *minor*, never to 1.0.0), and the next release this enables is **v0.2.0** — not v1.0.0. It also fixes a latent, now load-bearing bug: the ldflags inject a **v-stripped** version that `x/mod/semver` rejects, which would silently mute both the notifier and `self update` the moment automated comparison becomes the whole point. A maintainer-facing `devstack release` wizard (huh v2 TUI, fully `--json`/flag-degradable) previews the computed version + changelog and optionally cuts the tag.

## Decisions
- **Version computation = `caarlos0/svu` (pure-Go, by the goreleaser author), always `--v0`.** CI runs `svu next --v0` to derive the next tag from conventional-commit history; the existing `release.yml` (`on: push: tags: ["v*"]`) stays the release engine. Rejected: release-please (Node action, opens a "release PR", owns the tag — reshapes the flow off goreleaser-on-tag), go-semantic-release (wants to own publishing, overlaps goreleaser), git-cliff (Rust, changelog-only, no version decision — goreleaser already does conventional changelogs), node semantic-release (heaviest, full Node toolchain, duplicates goreleaser). svu is the idiomatic goreleaser pairing and a single static binary.
- **Stay on 0.x via `--v0` (KeepV0), enforced twice.** `svu --v0` makes a BREAKING change bump the minor (0.1.x → 0.2.0), never 1.0.0. A CI **guard step fails the run** if the computed/about-to-push tag has `MAJOR != 0`. `.svu.yaml` sets `v0: true` so the flag can never be forgotten.
- **Two-workflow split, App-token tag push.** A new `tag.yml` (`on: push: main` + `workflow_dispatch`) computes the version and pushes the `v*` tag using a **GitHub App / fine-grained PAT** secret (`RELEASE_TOKEN`); the existing `release.yml` fires on the tag and runs goreleaser unchanged. Reason: a tag pushed with the default `GITHUB_TOKEN` does **not** re-trigger workflows (Actions suppresses events from `GITHUB_TOKEN`), so a plain bot push would never fire `release.yml`. The App token is owner-gated (least-privilege, no expiry) and **its absence is the kill-switch**: `tag.yml` computes-and-logs but **does not push** when `RELEASE_TOKEN` is unset, so a private / pre-public repo never auto-releases (honors the owner-only release-flip, [PROGRESS](../PROGRESS.md) decision #4). The secret is exposed at **job level** so the push step's `if:` can actually read it (a step-level `env:` is invisible to that same step's `if:`).
- **Single changelog source of truth = goreleaser, grouped by conventional type.** Upgrade `.goreleaser.yaml` `changelog` to `groups:` (Features/Fixes/Performance/…) with `filters.exclude` for `chore|docs|test|ci|build|style`. Grouping/filtering apply with `use: github` (and `use: git`) but are **ignored** under `use: github-native`, so the config must stay on `github`. `@semantic-release/release-notes-generator` is **not** added — two changelog generators is two sources of truth. goreleaser remains the sole creator of the GitHub Release + notes.
- **Fix the ldflags v-prefix (load-bearing).** Change `.goreleaser.yaml` ldflags from `version.Version={{.Version}}` to `version.Version=v{{ .Version }}` so the stamped `internal/version.Version` is a clean, **v-prefixed** semver that `x/mod/semver` accepts. The archive `name_template` stays on the **v-stripped** `{{ .Version }}` to match `assetName`'s `TrimPrefix` ([spec 14](14-self-update-and-migration.md), `update.go:78`). A CI step asserts the built binary's `version --short` output is `semver.IsValid`.
- **Conventional-commit input is guarded.** Squash-merge makes the PR title the commit subject, so commit analysis is only as reliable as PR titles. Add a pure-shell **PR-title lint** (`pull_request` types `opened|edited|synchronize`) — no Node dep — that fails on a non-`type(scope)?:` subject. Repo setting: **squash-merge only**, "PR title" as the squash commit message.
- **`devstack release` (maintainer command) wraps svu; never the only path.** A new `internal/release` shells to the `svu` binary behind an interface (mockable, like `internal/git`/`internal/docker`); `internal/cli` adds `release` which previews next version + grouped changelog and optionally creates/pushes the tag. Interactive confirm via `charm.land/huh/v2` behind `internal/prompt`, with a non-TTY/`--yes`/`--json`/`--dry-run` fallback that never starts the bubbletea runtime.
- **Extends (not redefines) the spec-14 pre-release rule.** Spec 14 already mandates "ignore pre-releases unless the running build is itself a pre-release." This spec adds an **additive, forward-tolerant** `update.channel: stable|prerelease` key (default `stable`) under `apiVersion: devstack/v1` so a maintainer can opt into `-rc`/`-beta` tags. Spec 14 remains the owner of the notifier behavior; this spec only wires the config knob. The plain 0.x.y plan never emits pre-releases, but the knob makes a future `v0.3.0-beta.1` safe rather than a surprise to stable users.

```yaml
# .svu.yaml — v0 is non-negotiable while BETA. Keep this minimal: only `v0` is a
# verified key here. svu's DEFAULT tag format is already v<semver> — do NOT add a
# channel/range/build-meta suffix, which would break release-dryrun for every PR.
v0: true   # KeepV0: a BREAKING change bumps the MINOR (0.1.x → 0.2.0), never 1.0.0
```

```diff
# .goreleaser.yaml — the v-prefix fix (the rest of builds: unchanged)
  ldflags:
    - -s -w
-   - -X github.com/open-source-cloud/devstack/internal/version.Version={{.Version}}
+   - -X github.com/open-source-cloud/devstack/internal/version.Version=v{{ .Version }}
    - -X github.com/open-source-cloud/devstack/internal/version.Commit={{.ShortCommit}}
    - -X github.com/open-source-cloud/devstack/internal/version.Date={{.Date}}

  archives:
    - id: default
      formats: [tar.gz]
      name_template: "{{ .ProjectName }}_{{ .Version }}_{{ .Os }}_{{ .Arch }}"   # UNCHANGED — stays v-STRIPPED to match assetName's TrimPrefix

  changelog:
    use: github     # MUST stay `github` (or `git`); `github-native` ignores groups/filters
    sort: asc
+   groups:
+     - { title: "Features",     regexp: '^.*?feat(\(.+\))?!?:.*$',  order: 0 }
+     - { title: "Bug fixes",    regexp: '^.*?fix(\(.+\))?!?:.*$',   order: 1 }
+     - { title: "Performance",  regexp: '^.*?perf(\(.+\))?!?:.*$',  order: 2 }
+     - { title: "Others",       order: 99 }
+   filters:
+     exclude: ['^chore', '^docs', '^test', '^ci', '^build', '^style', '^Merge ']
```

```yaml
# .github/workflows/tag.yml — NEW. Computes + pushes the v* tag; release.yml then fires.
name: Tag
on:
  push: { branches: [main], paths-ignore: ["**/*.md", "docs/**", "LICENSE", "NOTICE"] }
  workflow_dispatch: {}
permissions: { contents: write }
concurrency: { group: tag-main, cancel-in-progress: false }  # never race two taggers
jobs:
  tag:
    runs-on: ubuntu-latest
    env:
      RELEASE_TOKEN: ${{ secrets.RELEASE_TOKEN }}    # JOB-level: required so the push step's `if:` can read it
    steps:
      - uses: actions/checkout@v4
        with: { fetch-depth: 0 }                       # full history + tags for svu
      - uses: actions/setup-go@v5
        with: { go-version: "1.25", check-latest: true }
      - name: install svu (pinned)
        # Resolve the real latest v3.x once and pin the exact tag here; the v3
        # module path is fixed, the patch must be confirmed before merge.
        run: go install github.com/caarlos0/svu/v3@v3.2.3
      - name: compute next version
        id: svu
        run: |
          CUR="$(git describe --tags --abbrev=0 2>/dev/null || echo v0.0.0)"
          NEXT="$(svu next --v0)"
          echo "current=$CUR"  >> "$GITHUB_OUTPUT"
          echo "next=$NEXT"    >> "$GITHUB_OUTPUT"
      - name: 0.x guard (a stray feat! must NEVER yield v1.0.0)
        run: |
          case "${{ steps.svu.outputs.next }}" in
            v0.*) : ;;
            *) echo "::error::refusing non-0.x tag ${{ steps.svu.outputs.next }} while in BETA"; exit 1 ;;
          esac
      - name: push tag (only when enabled + a real bump)
        if: ${{ steps.svu.outputs.next != steps.svu.outputs.current && env.RELEASE_TOKEN != '' }}
        run: |
          git config user.name  "devstack-release[bot]"
          git config user.email "release@devstack.local"
          git remote set-url origin "https://x-access-token:${RELEASE_TOKEN}@github.com/${{ github.repository }}.git"
          git tag "${{ steps.svu.outputs.next }}"
          git push origin "${{ steps.svu.outputs.next }}"     # fires release.yml (App token ≠ GITHUB_TOKEN)
```

## Behavior
**Automated release pipeline (CI):**
1. PR merges to `main` (squash; PR title is the commit, already conventional-lint-checked on the PR).
2. `tag.yml` checks out full history, installs the pinned `svu`, runs `svu next --v0` → e.g. `v0.2.0`.
3. **0.x guard:** if the computed tag is not `v0.*`, fail the job (a `feat!`/`BREAKING CHANGE` during BETA must bump the minor, not major).
4. If the computed tag differs from the latest existing tag **and** `RELEASE_TOKEN` is set, push the tag with the App token. Otherwise log "no release due" / "tagging disabled" and exit 0 (no push — the pre-public kill-switch).
5. The pushed `v*` tag triggers the unchanged `release.yml` → goreleaser `release --clean`: builds the 4 CGO-free targets, stamps `version.Version=v0.2.0`, emits the **grouped** changelog, archives (v-stripped names) + `checksums.txt` + `.deb`/`.rpm`, creates the GitHub Release.
6. A user on v0.1.0 runs any command → the notifier's `LatestTag` resolves `v0.2.0` (v-prefixed `tag_name`), `semver.Compare("v0.2.0","v0.1.0")>0` → footer shown. `devstack self update` downloads `devstack_0.2.0_<os>_<arch>.tar.gz` (v-stripped), verifies `checksums.txt`, atomically replaces — all unchanged from [spec 14](14-self-update-and-migration.md), now actually reachable because the stamped version is semver-valid.

**`devstack release` (maintainer, interactive — local convenience over the same svu):**
1. Resolve current tag + `svu next --v0`; render a grouped changelog preview (commits since the last tag, bucketed by conventional type).
2. **TTY + interactive:** a huh v2 form shows `current → next`, the changelog, and a confirm (`Create and push tag v0.2.0?`). On confirm: `git tag` + (optional) `git push`.
3. **Non-TTY / `--yes` / `--json` / CI:** never start the TUI. `--json` prints `{current,next,bump,changelog,wouldPush}` and exits; `--dry-run` prints the plan and writes nothing; `--yes` tags non-interactively; `--no-push` tags locally only.
4. `release --check` prints the next version and exits **0 if a release is due**, **non-zero if not** (scriptable gate, reads as `if devstack release --check; then ...`). **Note the deliberate inversion:** unlike the drift-style `generate --check`/`ws --check` (non-zero = action needed), here exit 0 = a release is due; both codes are documented so it is never confused with drift detection. `--quiet` suppresses all but the version.

**The one-time v0.2.0 cut (owner):** the current sole tag is `v0.1.0`; the accumulated `feat:` history since makes `svu next --v0` resolve `v0.2.0`. The owner enables auto-release by adding the `RELEASE_TOKEN` secret (or runs `devstack release --yes` / `git tag v0.2.0 && git push origin v0.2.0` manually). No `0.0.0` seeding hazard exists here — the repo already has a non-zero `v0.1.0` baseline.

## Verified constraints / gotchas
- **The v-stripped ldflags is a silent muter, not cosmetic.** goreleaser's `{{ .Version }}` is the tag *without* the leading `v` (`0.2.0`), but `x/mod/semver` requires the `v` and treats malformed input as lowest. Confirmed against the code: `internal/selfupdate/selfupdate.go` `IsDevBuild(v)` returns true when `!semver.IsValid(v)`, and `notify.go`/`update.go` short-circuit on `IsDevBuild`. A goreleaser binary stamped `0.2.0` → `IsDevBuild=true` → **the notifier goes silent and `self update` never sees an update**. Fix the ldflags to `v{{ .Version }}`; keep the archive `name_template` v-stripped (it must match `assetName = devstack_<TrimPrefix v>_<os>_<arch>.tar.gz`, `update.go:78`). These two opposite conventions are both load-bearing — do not "unify" them.
- **A bot tag pushed with `GITHUB_TOKEN` will not fire `release.yml`.** GitHub deliberately suppresses workflow events from the default token to prevent recursion. Use a GitHub App / fine-grained PAT (`RELEASE_TOKEN`) for the tag push, or collapse tag-compute + goreleaser into one job. The two-workflow + App-token split keeps `release.yml` untouched and owner-gated. **`release.yml` itself still uses `GITHUB_TOKEN`** for goreleaser (it only creates a release; it does not need to re-trigger anything), so it is unchanged.
- **A step-level `env:` is invisible to that step's own `if:`.** The `env` context in an `if:` expression resolves only workflow- and job-level env. Therefore `RELEASE_TOKEN` must be declared at **job level** (as in the `tag.yml` above), not on the push step, or the kill-switch `if: env.RELEASE_TOKEN != ''` always reads empty and the tag never pushes.
- **Only ever push `v<MAJOR.MINOR.PATCH>` tags.** A non-semver tag (a channel/range/build-meta suffix, or a milestone label like `m2-done`) breaks the `release-dryrun` job's `goreleaser` `git describe` for *every* PR (ci.yml job `release-dryrun` runs `release --snapshot --clean`; [PROGRESS](../PROGRESS.md) decision #1). svu's default `tagFormat` `v${version}` satisfies this; `.svu.yaml` must never add a suffix.
- **`--v0` is not the default — forget it and BETA breaks.** Plain `svu next` bumps a BREAKING change to 1.0.0. Always pass `--v0` (and set `v0: true` in `.svu.yaml`) **and** keep the CI `v0.*` guard as defense-in-depth. (Note the release-please trap if you ever switch tools: with a `0.0.0` manifest its `bump-*-pre-major` flags are ignored and it recommends 1.0.0 — you must seed at `0.1.0`. svu reading the existing `v0.1.0` tag avoids this entirely.)
- **`x/mod/semver` orders 0.x and pre-releases correctly** (repo vendors v0.37.0): `Compare("v0.2.0","v1.0.0")<0`, `Compare("v0.2.0-beta.1","v0.2.0")<0`, dotted identifiers numeric (`beta.2 < beta.10`). Staying `MAJOR=0` keeps every notifier/self-update comparison valid; tag any beta as `vX.Y.Z-beta.N` so it sorts *before* the final.
- **huh v2 must be the v2/charm.land line, never v1.** Add `charm.land/huh/v2` (latest v2.0.x, e.g. v2.0.3) + its transitive `charm.land/bubbletea/v2` and `charm.land/bubbles/v2`, byte-aligned with the already-vendored `charm.land/lipgloss/v2 v2.0.1` + `charm.land/fang/v2 v2.0.1`. `github.com/charmbracelet/huh` (v1) pulls bubbletea v1 / lipgloss v1 and **double-vendors a conflicting charm stack**. The whole family is pure-Go terminal I/O (x/term, x/ansi, ultraviolet — already vendored) → **CGO_ENABLED=0 safe, no build tags**; re-run `make vuln` after adding. *(See residual risks: a stdlib y/n prompt is a viable zero-dep alternative for a confirm this simple.)*
- **huh is a bubbletea program — gate it on a TTY.** `form.Run()` errors when stdin is not a TTY; wrap it behind `internal/prompt` with a non-TTY/`--json`/`--quiet`/`CI` fallback to flags so the headline-output contract ([ARCHITECTURE §7.9](../ARCHITECTURE.md)) holds and a CI invocation never hangs waiting for input. huh's `.WithAccessible()` is a degraded-but-usable middle path.
- **Conventional-commit input is only as good as squash-merge hygiene.** Under squash-merge the PR title becomes the commit subject; an unlinted title silently corrupts version computation (a `fix` typo'd as `bugfix` → no bump). Lint PR titles (pure shell regex, no Node action) and require "PR title" as the squash message in repo settings.
- **`devstack release` mutates nothing shared — no flock.** It reads git, computes a version, and creates a git tag; it touches neither the SQLite ledger nor the shared stack, so (like `devstack import`, [spec 14](14-self-update-and-migration.md)) it takes **no** `internal/lock`. Determinism/golden artifacts are untouched: the ldflags change alters only the stamped version string, never generated compose output. No token or secret value is ever written to a generated file or printed by `release`/`version` (the no-plaintext rule, [spec 04](04-secrets.md)).
- **Don't add a redundant `.env`/changelog parser.** Conventional-commit grouping lives in goreleaser config (regex on subjects); no new Go parser is needed. If structured env handling is ever required in CI scripts, reuse the already-vendored `github.com/compose-spec/compose-go/v2/dotenv` — not `joho/godotenv`/`hashicorp/go-envparse`.

## Acceptance criteria
- [ ] Merging a `feat:` PR to `main` makes `tag.yml` compute `v0.2.0` via `svu next --v0`; with `RELEASE_TOKEN` set it pushes the tag, which triggers `release.yml` → a GitHub Release with grouped (Features/Fixes/…) notes.
- [ ] A `feat!:` / `BREAKING CHANGE` commit while in BETA computes a **minor** bump (e.g. `v0.2.0`), never `v1.0.0`; the CI `v0.*` guard fails the run if any non-0.x tag would be produced.
- [ ] When `RELEASE_TOKEN` is unset, `tag.yml` logs the computed version and exits 0 **without pushing** (pre-public kill-switch); when the computed tag equals the latest existing tag it also no-ops. The push-step `if:` resolves `RELEASE_TOKEN` from job-level env (a deliberate test: an empty secret must skip the push).
- [ ] A goreleaser-built binary reports `version --short` = a v-prefixed string that `semver.IsValid` accepts (verified on both a real tag and the `--snapshot` build, whose `v0.1.1-dev-<sha>` is still valid semver); a CI step asserts it. The release archive filename stays v-stripped (`devstack_0.2.0_<os>_<arch>.tar.gz`).
- [ ] After v0.2.0 is published, a v0.1.0 user sees the notifier footer and `self update` installs v0.2.0 (checksum-verified) with no other change to [spec 14](14-self-update-and-migration.md) code — i.e. the previously-muted path now fires.
- [ ] A PR whose title is not `type(scope)?: subject` fails the PR-title lint; a conventional title passes. No Node toolchain is added to CI.
- [ ] `devstack release --json` prints `{current,next,bump,changelog,wouldPush}` and writes nothing; `--dry-run` prints the plan; `--yes` tags non-interactively; on a non-TTY / `CI`, `release` never starts the bubbletea runtime.
- [ ] `devstack release --check` exits 0 when a release is due and non-zero otherwise (gate semantics, documented as inverted from `generate --check`).
- [ ] `release-dryrun` (`goreleaser release --snapshot --clean`), `make determinism`, and the golden tests stay green after the ldflags/changelog/`.svu.yaml` changes; only `v*` tags exist in the repo.
- [ ] The notifier suppresses `-rc`/`-beta` tags unless `update.channel: prerelease` (or the running build is itself a pre-release); a `v0.3.0-beta.1` tag does not nag a stable user. The `update.channel` key loads forward-tolerantly under `apiVersion: devstack/v1` ([spec 01](01-config-schema.md)/[spec 14](14-self-update-and-migration.md)).
- [ ] Adding `charm.land/huh/v2` + transitive bubbletea/bubbles v2 keeps `CGO_ENABLED=0 go build ./...`, the 4-target cross-compile, and `make vuln` green; no charm v1 packages enter `go.mod`.

## Dependencies / consumers
Consumes `internal/version` (the corrected ldflags target it stamps and every comparison reads; the existing `version` command in `root.go` gains `--short`/`--json`), `internal/git` ([spec 06](06-git.md), reading commit history + creating/pushing the tag for `devstack release`), `internal/config` (the additive `update.channel` key under `apiVersion: devstack/v1`, forward-tolerant per [spec 14](14-self-update-and-migration.md)/[spec 01](01-config-schema.md)). New `internal/release` (svu wrapper + mock) and `internal/prompt` (huh v2 wrapper + non-TTY fallback) are consumed by `internal/cli` (`release`, and the `version --short`/`--json` flags). `internal/selfupdate` ([spec 14](14-self-update-and-migration.md)) is the direct beneficiary — the v-prefix fix and the `update.channel` knob make its notifier + `self update` actually reachable. CI consumers: `tag.yml` (new), `release.yml` (unchanged engine), `ci.yml` (new PR-title lint + the `version --short` semver assertion; the existing `release-dryrun` job stays). External tools (CI-only, not in `go.mod`): pinned `github.com/caarlos0/svu/v3` and the existing `goreleaser`. New module deps: `charm.land/huh/v2` (latest v2.0.x) + transitive `charm.land/bubbletea/v2` / `charm.land/bubbles/v2` (v2 line). **Thin vs full:** the thin slice (`.svu.yaml` + `tag.yml` + ldflags/changelog fix + 0.x guard + PR-title lint) is ~0.75w and is all that's needed to cut v0.2.0; the `devstack release` huh wizard + `internal/release`/`internal/prompt` + `update.channel` filter add ~0.75w.

## Open questions
- **Orchestration shape — two-workflow (App-token) vs single-job.** Two workflows keep `release.yml` untouched and the owner kill-switch clean, at the cost of one App-token secret. A single job (svu + goreleaser together) needs no extra token but folds tagging into the release run. *Recommendation: two-workflow + `RELEASE_TOKEN`.* **Decision:** two-workflow split; revisit only if App-token management proves heavier than the recursion-avoidance it buys.
- **Confirm UI — huh v2 vs stdlib y/n.** huh aligns with the existing fang/lipgloss v2 stack but vendors a full bubbletea/v2 runtime for one confirm. A `bufio.Scanner` y/n prompt behind the same `internal/prompt` interface adds zero module deps. *Recommendation: ship the stdlib prompt for the thin slice; adopt huh only if/when the wizard grows multi-field input.* **Decision:** keep `internal/prompt` as the seam either way; default to stdlib, leave huh as a drop-in upgrade.
- **Committed `CHANGELOG.md` vs release-notes-only.** goreleaser grouped notes live on the GitHub Release; a committed `CHANGELOG.md` would need git-cliff/semantic-release (a second generator) and a bot commit to `main`. *Recommendation: release-notes-only — single source of truth, no second tool, no determinism/bot-commit churn.* **Decision:** release-notes-only for v0.2.0; reconsider a generated `CHANGELOG.md` artifact (not committed) post-1.0.
- **PR-title lint: shell regex vs `amannn/action-semantic-pull-request`.** *Recommendation: pure-shell regex* (no Node action, consistent with the "no Node toolchain" stance). **Decision:** shell regex now; swap to the marketplace action only if richer scopes/config are needed.
- **`devstack release` source of truth: shell-to-`svu` vs reimplement bump in pure Go.** Shelling matches the repo's "wrap external tool behind an `internal/` interface" rule and guarantees lockstep with CI; reimplementing avoids a runtime dep but risks divergence. *Recommendation: shell to `svu` behind `internal/release` with a "svu not found" remediation (maintainer command).* **Decision:** wrap `svu`; a CI test asserts `devstack release --check` agrees with `svu next --v0`.
- **Pre-release channels in 0.x.** The plain plan never emits `-beta`/`-rc`, but spec 14 mandates the ignore-pre-releases rule. *Recommendation: implement the `update.channel` knob now (cheap) so a future beta is safe.* **Decision:** implement additively; default `update.channel: stable`, spec 14 owns the behavior.