# Spec 06 — Multi-repo git management

**Module:** `internal/git` (`gitx`) · **Milestone:** M3 · **Effort:** ~4.5w

## Purpose
Reliably clone/sync/inspect the set of repos in a workspace, using the developer's **existing** git auth, with a fast parallel UX and a headline cross-repo status table.

## Decisions
- **Shell out to the system `git` binary** (require **≥ 2.30**) behind a small internal `gitx` package — **not** go-git. `go-git/v5` only as a build-tagged, read-only, offline fallback (never on the auth/network path).
- **Why exec(git):** it inherits the SSH agent, `~/.ssh/config` (IdentityFile, Host aliases, ProxyJump/Include), `known_hosts` (with host-key verification), and OS credential helpers (osxkeychain, libsecret, GCM) **for free**. That "your existing `git push` setup just works" is the entire value proposition.
- **Hardened exec env on every call:** `GIT_TERMINAL_PROMPT=0`, `GCM_INTERACTIVE=never` (never block on a hidden prompt — fail fast per-repo), `LC_ALL=C` (stable parseable output), `--no-optional-locks` on reads. `exec.LookPath("git")` once at startup with an actionable error if missing/old.
- **Status via `git status --porcelain=v2 --branch -z`**, parsed directly (~80 lines, no dependency): dirty/staged/unstaged/untracked + ahead/behind + upstream-gone in one cheap call.
- **HTTPS token injection via a generated `GIT_ASKPASS` shim** (tiny `0600` temp script that echoes the token from the secrets provider), never embedding tokens in the URL or `.git/config`.
- **Shorthand expansion:** `github:/gitlab:/bitbucket: org/repo` + generic `repo: <url>` + pluggable self-hosted `hosts:` map; transport (`ssh|https`) chosen by workspace config. Supersets devdock's `!Repo` model so existing users map over directly.
- **Parallelism:** `golang.org/x/sync/errgroup` with `SetLimit(--jobs, default min(8, 2*GOMAXPROCS))`, per-repo `context` for cancel/timeout.
- **UX:** `bubbletea`+`bubbles`+`lipgloss` row-per-repo table when attached to a TTY; **plain prefixed line logging** (`[repo] cloning…`) + `--json` when not (CI/WSL2), detected via `golang.org/x/term`. Dual-mode is mandatory.
- **Submodules/shallow opt-in per repo:** `submodules: true` → `--recurse-submodules`; prefer `filter: blob:none` (partial clone) over `--depth` for large repos; document the shallow+submodule pitfall; auto-unshallow on a missing-ref error.

## Commands
`ws clone [all|<name>…]` · `ws sync` (fetch + `--ff-only` pull, parallel) · `ws status` (the headline table; `--check` exits non-zero if any repo dirty) · `ws git <args> -- [all|<name>…]` (arbitrary git across many repos). Idempotent: clone skips existing dirs and validates the remote matches the expected URL.

## Verified constraints / gotchas
- **go-git does parse `~/.ssh/config` but only honors `Hostname`/`Port`** — it ignores `IdentityFile`/`User`/`ProxyJump`/`Include`, so deploy-key + bastion setups silently break; it also fails when no `known_hosts` exists. (This is *why* we exec system git.)
- **go-git CVEs (2026):** pin **≥ v5.19.x** (not just v5.17.1) for the full advisory set; v6 is still alpha. The "shallow leaves `.git` full-size" claim is a *config default* (`Tags: AllTags`) — set `Tags: NoTags` + `SingleBranch`. go-git has **no true partial clone** (`--filter`), only shallow.
- **`--porcelain=v2` stability** is documented for **v1** only — treat v2 as de-facto stable, tolerate added headers, pin to observed fields. **No explicit upstream-gone field** — detect via `branch.upstream` present + `branch.ab` absent. Min git for v2/`--branch` is ~2.13; the 2.30 floor is conservative (note Ubuntu 20.04 ships 2.25.1, below it).
- **`GIT_ASKPASS` shim** could leak the token to disk — write `0600` in an `os.MkdirTemp` dir and remove with `defer`.
- Progress parsing is locale/version-sensitive — `LC_ALL=C`; treat clone/fetch progress as best-effort cosmetic (phase + spinner), not parsed percentages.

## Acceptance criteria
- [ ] `ws clone all` clones every repo in parallel using the user's existing SSH key + `~/.ssh/config` Host alias, with no credential prompt.
- [ ] A repo behind a `ProxyJump` bastion clones successfully (proving system-git inheritance).
- [ ] `ws status` shows dirty/ahead/behind/branch/upstream-gone for every repo in one table; `--check` exits non-zero when any is dirty.
- [ ] HTTPS token from the secrets provider authenticates a clone via the `GIT_ASKPASS` shim, with the token never written to `.git/config` or visible in `ps`.
- [ ] In a non-TTY/CI context, output is plain prefixed lines (or `--json`), not TUI escape codes.
- [ ] A missing-credential repo fails fast with a clear per-repo error instead of hanging the parallel batch.

## Dependencies / consumers
Consumes `internal/secrets` (token for the `GIT_ASKPASS` shim) + `internal/config` (the repo set). Feeds `internal/workspace` (which repos to manage) and the onboarding flow (feature #1).
