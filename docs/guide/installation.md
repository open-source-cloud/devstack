# Installation, doctor & updates

[devstack](../../README.md) · [Guide](./README.md) › Installation, doctor & updates

## Install (curl | sh)

```bash
curl -fsSL https://raw.githubusercontent.com/open-source-cloud/devstack/main/install.sh | sh
```

The installer downloads the right release binary for your OS/arch from GitHub
Releases, verifies its SHA-256 checksum, and installs it. It is POSIX `sh`, needs
only curl-or-wget + tar + sha256sum/shasum, and **never requires root** unless you
point the install dir at a system path.

### Installer environment variables

| Variable | Default | Description |
|---|---|---|
| `DEVSTACK_VERSION` | latest release | Pin a version, e.g. `v0.1.0`. |
| `DEVSTACK_INSTALL_DIR` | `$XDG_BIN_HOME` or `~/.local/bin` | Where to install the binary. |
| `DEVSTACK_ALIASES` | *(none)* | Space-separated `argv[0]` alias symlinks to also install, e.g. `"rq uranus"`. |
| `DEVSTACK_NO_VERIFY` | *(unset)* | Set to `1` to skip the checksum verification. |
| `GITHUB_TOKEN` / `GH_TOKEN` | *(none)* | Auth for the GitHub API (raises rate limits; required while the repo/releases are private). |

```bash
# Pin a version, install to /usr/local/bin, and add the rq + uranus aliases
DEVSTACK_VERSION=v0.1.0 \
DEVSTACK_INSTALL_DIR=/usr/local/bin \
DEVSTACK_ALIASES="rq uranus" \
  sh -c "$(curl -fsSL https://raw.githubusercontent.com/open-source-cloud/devstack/main/install.sh)"
```

Aliases are just alternate names for the same binary — the command tree is
identical, only the branding changes. See [Aliases & argv[0] dispatch](aliases.md).

## Requirements

| Requirement | Minimum | Notes |
|---|---|---|
| Docker Engine | running | Lifecycle is driven by the `docker compose` CLI; the Engine SDK is used read-only. |
| Docker Compose | ≥ 2.20 | Checked at preflight. |
| git | ≥ 2.30 | devstack shells out to system git (inherits your SSH/credential setup). |
| OS | Linux, macOS, or WSL2 | Cross-compiled for `darwin/{amd64,arm64}` + `linux/{amd64,arm64}`. |

**WSL2 note.** WSL2 uses the ordinary Linux build. devstack **refuses to operate
under `/mnt/*`** (Windows-mounted drives): the 9p/networked filesystem there is
not safe for the ledger's SQLite + advisory-lock spine. Keep your workspaces on
the Linux filesystem (e.g. under `~`). The ledger is also **keyed by Docker
context**, so WSL2's two daemons (Docker Desktop vs an in-distro `dockerd`) never
share ref counts.

## Verify your host: `doctor`

```bash
devstack doctor
```

`doctor` probes the environment and reports each capability with a one-line
remediation. It exits non-zero when any **critical** check fails, so it doubles as
a CI gate.

| Flag | Default | Description |
|---|---|---|
| `--fix` | false | Apply safe, idempotent, non-destructive remediations under the lock. |
| `--rebuild-state` | false | Reconstruct the ledger from config + live container labels. |
| `--json` | false | Machine-readable output. |

```bash
devstack doctor --json                 # scriptable capability matrix
devstack doctor --fix                  # apply the safe fixes it found
devstack doctor --rebuild-state        # rebuild the ledger after drift
```

`--rebuild-state` is the escape hatch when the ledger and reality disagree (a
container was removed out-of-band, a machine was restored, etc.): it walks live
container labels and rebuilds the ref/ownership rows. See
[Recovery, teardown & housekeeping](recovery.md).

## Updates: `self check` / `self update`

```bash
devstack self check          # is a newer release available?
devstack self update         # download + install the latest release
```

`self update` verifies a cosign keyless signature **and** the SHA-256 checksum,
then does an atomic in-place replace. It **refuses on package-managed installs**
(use your package manager there).

| Flag | Default | Description |
|---|---|---|
| `--check` | false | Only check; don't install. |
| `--version` | "" | Install a specific tag, e.g. `v0.2.0`. |
| `--force` | false | Re-install even if already current. |
| `--insecure-skip-verify` | false | Bypass the cosign signature check (the checksum is still enforced). |

```bash
devstack self update --version v0.2.0
```

## Uninstall

```bash
devstack uninstall
```

This removes **every machine-global devstack artifact** — stacks, the
`devstack_shared` network, volumes, the local CA, `/etc/hosts` entries, aliases,
and the ledger — in a 6-step reversal. It **destroys data and is irreversible**,
but it does **not** touch your committed `workspace.yaml` / `devstack.yaml`. The
interactive prompt requires typing `uninstall`; under `--json` / non-interactive
it requires `--yes`.

```bash
devstack uninstall --yes     # non-interactive (CI/scripts)
```

To tear down just one workspace (keeping the machine-global install), use
`devstack workspace destroy` instead — see [Recovery](recovery.md).

## See also

- [Concepts & the mental model](concepts.md) — what devstack manages and why.
- [Workspaces](workspaces.md) — author `workspace.yaml` and run `init`.
- [Recovery, teardown & housekeeping](recovery.md) — `doctor --fix`, `gc`, teardown.
- [Aliases & argv[0] dispatch](aliases.md) — the `rq` / `uranus` alias names.

---

◀ [Concepts](concepts.md) · [Guide index](./README.md) · [Workspaces ▶](workspaces.md)
