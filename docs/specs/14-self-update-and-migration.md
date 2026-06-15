# Spec 14 — Self-update, notifications & migration

**Module:** `internal/cli` (`self`), `internal/state`, `internal/migrate` · **Milestone:** M0 (versioning policy + import scaffold) → M6 (notify + signed self-update) · **Effort:** ~1w (feature #6 + Q-MIGRATE)

## Purpose
Keep a CVE-prone single-binary tool ([dependency-risk register](../DECISIONS.md)) honestly up to date without bricking package-managed installs, and give the existing `devdock` user base a lossless-or-loud path onto the clean-slate schema. Three things evolve independently — config `apiVersion`, the state-DB schema, and the generated-compose format ([ARCHITECTURE §7.4](../ARCHITECTURE.md)) — and this spec owns the compatibility guarantees and the upgrade choreography that ties them together. It implements feature #6 (notifications + signed `self update`) plus `devstack import` (Q-MIGRATE).

## Update notifications
A throttled background version check, rendered as one unobtrusive footer line. Never blocks or slows the actual command.

- **Throttle:** at most one check per `update.checkInterval` (default `24h`). Persisted as a single cache row, not in the per-context ledger (a version is global to the binary, not to a Docker context):

  ```sql
  -- under XDG_CACHE_HOME/devstack/, separate from state.db (cache is disposable, ledger is not)
  update_check(last_checked_at INTEGER, latest_version TEXT, etag TEXT)
  ```
- **Mechanism:** fire the check in a goroutine at command start with a **hard 800ms budget**; if it doesn't return before the command finishes, drop it (no footer this run). Use the GitHub Releases API with the stored `etag` (304 → no body, no rate-limit burn). Compare against the ldflags `version` ([spec 07](07-cli-and-aliasing.md)) with `golang.org/x/mod/semver`; ignore pre-releases unless the running build is itself a pre-release.
- **Render:** a single dim footer line `A new devstack vX.Y.Z is available — run \`devstack self update\` (or \`brew upgrade\` on Homebrew).` The remediation verb is **install-method aware** (see below).
- **Suppression (any one suffices):** `--json` / `--quiet`; non-TTY stdout (`golang.org/x/term.IsTerminal`); `CI` env set; `update.notify: false` in `workspace.yaml`; `DEVSTACK_NO_UPDATE_NOTIFIER` / `DEVSTACK_DISABLE_UPDATE_CHECK` env. The env opt-out also **skips the network call entirely** (cache untouched), so locked-down/air-gapped CI never reaches out.
- **Branding:** aliases (`rq`, `uranus`) render the footer under the active alias name, reusing the [spec 07](07-cli-and-aliasing.md) branding, but the upgrade target is always the one binary.

## `self update` flow
`creativeprojects/go-selfupdate` against the goreleaser GitHub releases ([DECISIONS D13](../DECISIONS.md)). The ordering is non-negotiable: **detect → verify → replace → re-point aliases → migrate-on-next-launch.**

```
detect install method ──► package-managed? ──► REFUSE, print exact pkg cmd, exit 0 (not an error)
        │ (self-managed)
        ▼
fetch release for runtime GOOS/GOARCH ──► download asset + checksums.txt + checksums.txt.sig
        │
        ▼
verify checksum (SHA-256) ──► verify minisign/cosign signature over checksums.txt  ──► MISMATCH/UNSIGNED ► abort, leave binary untouched
        │ (both pass)
        ▼
atomic replace: write new binary to a temp file beside the target, fsync,
   rename-over (unix) — never truncate-in-place
        │
        ▼
re-point every registered alias symlink (idempotent)  +  drop a "migrate pending" marker
        │
        ▼
next launch: forward-only state-DB migration (backup first) runs before any command logic
```

- **Install-method detection (must be precise — a false negative bricks a managed install):**
  | Signal | Verdict |
  |---|---|
  | Resolved binary path under a Homebrew `Cellar`/`opt` prefix (`brew --prefix` or path contains `/Cellar/`) | managed → `brew upgrade <formula>` |
  | `dpkg -S <abs-path>` exits 0 (file owned by a package) | managed → `apt upgrade devstack` |
  | `rpm -qf <abs-path>` exits 0 | managed → `dnf upgrade devstack` |
  | target file/dir not writable by the current user (`unix.Access … W_OK`) | refuse → "binary not writable; use your package manager or reinstall" |
  | none of the above, file writable | self-managed → proceed |
  Detection resolves `os.Executable()` through `filepath.EvalSymlinks` first, so an alias symlink doesn't mask the real install. A managed-install refusal exits **0** with guidance (it's a correct outcome, not a failure).
- **Signature verification is mandatory:** an unsigned or altered release is refused even with `--force`. Signing is deferrable post-v1 ([DECISIONS D13](../DECISIONS.md)); until keys exist, `self update` runs in **checksum-only mode** but prints a one-line warning that signatures are not yet enforced — it never silently downgrades to "no integrity check."
- **Re-point aliases:** after replace, walk the alias registry ([spec 07](07-cli-and-aliasing.md), `internal/alias`) and re-create each symlink in `XDG_BIN_HOME` pointing at the new binary. Idempotent and retry-safe: removing a stale/dangling link then re-linking is a no-op when already correct.
- **Migrate-on-next-launch:** the running process never migrates its own DB mid-flight; it drops a marker and the **next** invocation runs the forward-only migration with backup ([spec 08](08-state-locking-and-lifecycle.md)) under the flock. This keeps schema changes off the hot path and out of a half-replaced process.

## Versioning policy — three artifacts, three guarantees
| Artifact | Identifier | Compatibility guarantee | Mechanism |
|---|---|---|---|
| **Config schema** | `apiVersion: devstack/v1` ([spec 01](01-config-schema.md)) | Forward-tolerant within a major: **unknown keys warn, never fail**; a newer binary reads an older `v1` file unchanged; an older binary tolerates additive keys it doesn't know. A major bump (`devstack/v2`) is the only breaking gate. | JSON-Schema `additionalProperties: true` at object roots + a soft "unknown key at `path`" lint; `apiVersion` mismatch on the major → hard error with the required version. |
| **State-DB schema** | `schema_version` row ([spec 08](08-state-locking-and-lifecycle.md)) | **Additive within a major, forward-only, backup-before-migrate.** Never edit a released migration. A binary refuses to run against a DB whose `schema_version` is **newer** than it understands (a downgrade), with a clear "this state was written by a newer devstack" message. | Append-only numbered migrations; `cp state.db state.db.bak.<ts>` before applying; all under the flock. |
| **Generated compose** | the `.devstack/` artifact format | Not an API; **byte-stability across a binary version is the guarantee**, enforced by golden output (sorted keys, fixed newlines — [ARCHITECTURE §3](../ARCHITECTURE.md)). `compose-go/v2` normalization churn is absorbed by golden tests, not exposed as semver. | `writeIfChanged` + deterministic build; a version bump that changes normalization is a reviewed golden-file diff. Commit-vs-gitignore is **Q-GEN**. |

Self-update touches all three: a new binary may carry a new state migration (handled above) and may re-normalize compose output — and **re-generation must never silently recreate a stateful shared service** ([spec 03](03-workspaces-and-shared-services.md) gotcha) just because the normalized YAML changed.

## `devstack import` (`internal/migrate`)
A **converter plus migration guide**, not a drop-in: the clean-slate two-file schema is intentionally not byte-compatible with `devdock`'s single `project.yaml` ([spec 01](01-config-schema.md)). It reads an old `project.yaml` and emits a `workspace.yaml` (shared layer) plus a per-repo `devstack.yaml` split (portable project layer).

```
devstack import <path/to/project.yaml> [--dry-run] [--out <dir>] [--force]
```
- **Mapping:** devdock global services → `workspace.yaml shared:`; devdock `!Repo` shorthand → the `internal/git` shorthand **superset** ([spec 06](06-git.md)) so existing repo entries map over directly; per-service template/params/env/`uses` → each repo's `devstack.yaml`; devdock `${service.var}` interpolation → the narrower typed `${ref:...}` grammar ([spec 01](01-config-schema.md)), flagging any expression that can't be expressed.
- **Lossless-or-loud:** every field that cannot be converted is reported in a **conversion report** (path, original value, reason) — printed and written next to the output. Nothing is dropped silently.
- **`--dry-run` / diff:** prints the would-be `workspace.yaml` + each `devstack.yaml` as a unified diff against any existing files; performs no writes.
- **No clobber:** refuses to overwrite an existing `workspace.yaml` (or any target `devstack.yaml`) without `--force`; with `--force`, backs up the originals first. Generated files carry an `apiVersion: devstack/v1` header and a comment pointing at the migration guide.
- This is an **M0/M1-scope scaffold** that can ship before the full pipeline, because it's pure YAML→YAML and depends on nothing that mutates shared state (no lock needed).

## Verified constraints / gotchas
- **Replacing a running binary:** on unix, write-temp-then-`rename(2)` over the target works while the old image is executing (the inode stays mapped until exit) — this is the only safe in-place path; **never truncate-and-rewrite** the live file. On native Windows the running `.exe` is locked and can't be renamed-over (rename-self-aside + replace, or defer to the package manager); native Windows is out of scope here ([Q-PLATFORM](../OPEN-QUESTIONS.md)), WSL2 uses the unix path.
- **Homebrew/dpkg/rpm detection must be precise:** a missed managed-install detection that proceeds to overwrite a Cellar/dpkg file corrupts the package manager's view and bricks the next `brew upgrade`/`apt` operation. Resolve symlinks first, prefer the package-DB query (`dpkg -S`/`rpm -qf`) over path heuristics, and default to **refuse** when ambiguous.
- **Signature verification is non-negotiable:** auto-updating a binary that drives Docker, writes `/etc/hosts`, and installs a root CA ([spec 08](08-state-locking-and-lifecycle.md) teardown) is a supply-chain target; an altered/unsigned release must be refused, not warned-and-installed.
- **Alias re-point and state migration must both be idempotent and retry-safe:** `self update` can be interrupted (network, `kill -9`) at any step. Re-running converges — symlinks are removed-then-relinked, the migration is gated by `schema_version` + backup under the flock, and a half-applied state never corrupts the ledger ([spec 08](08-state-locking-and-lifecycle.md)).
- **Import must be lossless-or-loud:** a silently dropped `!Repo` host alias, env var, or `uses` edge produces a broken-but-plausible config — every unconvertible field is reported, and `--dry-run` lets the user inspect before committing.
- **The update check must never gate the command:** GitHub API latency/outage, rate limits, or no network must not slow or fail `up`/`status`/`doctor` — hence the 800ms budget, the `etag`/304 path, and full suppression under CI/non-TTY.

## Acceptance criteria
- [ ] `self update` on a Homebrew-installed binary refuses and prints `brew upgrade <formula>`, exits 0, and leaves the binary untouched.
- [ ] `self update` on a `dpkg`/`rpm`-owned binary refuses and directs to `apt`/`dnf`; on a non-writable target it refuses with the reinstall hint.
- [ ] A tampered release asset (checksum mismatch) or a missing/invalid signature aborts `self update` with the original binary intact — even with `--force`.
- [ ] After a successful self-managed update, every registered alias symlink (`rq`, `uranus`) resolves to the new binary; re-running the re-point step is a no-op.
- [ ] After an update that bumps the state schema, the next launch backs up `state.db`, applies the forward-only migration under the flock, and runs with no schema mismatch ([spec 08](08-state-locking-and-lifecycle.md)).
- [ ] Running a binary against a `state.db` with a newer `schema_version` refuses with a "written by a newer devstack" message rather than corrupting it.
- [ ] The update notifier prints at most once per `checkInterval`, never under `--json`/`--quiet`/non-TTY/`CI`, and is fully disabled (no network call) by `DEVSTACK_NO_UPDATE_NOTIFIER` or `update.notify: false`.
- [ ] A config with an unknown additive key under `devstack/v1` loads with a soft warning, not a hard failure; an `apiVersion: devstack/v2` file fails with the required-version error.
- [ ] `devstack import project.yaml --dry-run` emits a `workspace.yaml` + per-repo `devstack.yaml` diff and a conversion report listing every unconvertible field, writing nothing.
- [ ] `devstack import` refuses to overwrite an existing `workspace.yaml` without `--force`; with `--force` it backs up originals first.

## Dependencies / consumers
Consumes `internal/version` (ldflags target — the running version it compares and stamps), `internal/alias` + `internal/xdg` (symlink re-point in `XDG_BIN_HOME`, cache path for the notifier), `internal/state` + `internal/lock` (the backup-before-migrate step runs under the flock — [spec 08](08-state-locking-and-lifecycle.md)), `internal/config` (the `apiVersion`/unknown-key policy — [spec 01](01-config-schema.md)), and `internal/git` shorthand for `import` ([spec 06](06-git.md)). Consumed by `internal/cli` (the `self` and `import` commands, [spec 07](07-cli-and-aliasing.md)) and by every command's footer-render path (the notifier). **Thinner v1 vs full:** a thin v1 (notifier + checksum-only `self update` with brew/dpkg refusal + a single-pass `import` with report) lands in ~1w; the full version (minisign/cosign signature enforcement once release signing is wired, `import --dry-run` diff rendering, and the cross-version golden-diff guard on generated compose) is ~2w.

## Open questions
- [Q-MIGRATE](../OPEN-QUESTIONS.md) — include `devstack import` + migration guide in v1 (recommendation: yes); this spec is its design.
- [Q-GEN](../OPEN-QUESTIONS.md) — committed vs gitignored generated artifacts determines whether a self-update's compose re-normalization shows up as a reviewable diff or is invisible (regenerated freely).
- [Q-PLATFORM](../OPEN-QUESTIONS.md) — native-Windows self-replace (locked running `.exe`) is out of scope; WSL2/macOS/Linux use the unix rename path.
- [Q-NAME](../OPEN-QUESTIONS.md) — the canonical binary name and Homebrew formula / `.deb` package name must be fixed before install-method detection and the upgrade-remediation strings can be wired exactly.
