# Aliases & argv[0] dispatch

[devstack](../../README.md) · [Guide](./README.md) › Aliases & argv[0] dispatch

The same devstack binary can answer to alternate names — `rq`, `uranus`,
whatever you like. This is pure **branding**: the command tree is byte-identical
under every name; only the displayed name changes. It's how a team can ship
devstack as their own internal tool without forking it.

```bash
devstack alias add rq       # install an `rq` symlink; `rq up` == `devstack up`
devstack alias list         # show installed aliases
devstack alias remove rq    # remove it (alias: `rm`)
```

## How dispatch works

At startup the thin entrypoint runs `alias.ResolveInvocation(os.Args)`:

1. The **invoked name** is `base(argv[0])` — i.e. the name you typed. If you ran
   `rq`, the invoked name is `rq`.
2. A registered alias only changes the branding shown in help/version output; the
   canonical name is always `devstack` and the command tree is the same.
3. `--as <name>` (or `--as=<name>`) **overrides** the detected name and is
   stripped before cobra ever sees it. A literal `--` stops the scan.

```bash
rq up                 # invoked as the alias `rq`
devstack --as rq up   # force the `rq` branding without a symlink
```

`--as` is pre-parsed (it is **not** a cobra flag), so it works on any command and
never collides with subcommand flags. Note: `db grant --as write` uses a
*different*, local `--as` (a privilege tier) — unrelated to argv branding.

## The `alias` group

| Command | Description |
|---|---|
| `alias add <name>` | Install an alias symlink in `XDG_BIN_HOME` pointing at the devstack binary. |
| `alias remove <name>` (alias `rm`) | Remove the alias symlink and its registry entry. |
| `alias list` | List installed aliases. |

```bash
devstack alias add uranus
uranus version        # same binary, "uranus" branding
devstack alias rm uranus
```

The installer drops a symlink in your XDG bin directory (make sure it's on
`$PATH`) and records the alias in the registry so `alias list` and `uninstall`
know about it.

### Alias name rules

Alias names must match:

```
^[a-z][a-z0-9_-]{0,31}$
```

Lowercase start, then letters/digits/`_`/`-`, up to 32 chars total. The name
`devstack` is **rejected** as an alias (it's the canonical name).

## Workspace-declared aliases

`workspace.yaml` can advertise aliases via its `aliases:` field. These are the
names a workspace expects to be available (each validated as a `dsname`):

```yaml
apiVersion: devstack/v1
kind: Workspace
name: acme
aliases: [rq, uranus]
```

Declaring them documents intent; installing the working symlinks is still done
with `alias add` (or by the onboarding flow). See
[config-reference.md](config-reference.md) for the field.

## Recipes

**Ship devstack as your team's `rq` tool.**

```bash
devstack alias add rq
rq doctor            # same preflight, "rq" branding
rq up
```

**One-off run under a different name (no symlink, e.g. in CI).**

```bash
devstack --as rq status --json
```

**List and clean up aliases before uninstalling.**

```bash
devstack alias list
devstack alias remove rq
```

Note: `devstack uninstall` already removes every registered alias as part of its
machine-global teardown — see [recovery.md](recovery.md).

## See also

- [The global store](store.md) — where custom templates and shared defaults live.
- [Installation, doctor & updates](installation.md) — put `XDG_BIN_HOME` on `$PATH`.
- [Global flags & scripting](global-flags.md) — how `--as` is pre-parsed.
- [config-reference.md](config-reference.md) — the `aliases:` field.

---

◀ [The global store (~/.devstack)](store.md) · [Guide index](./README.md) · [Full config reference](config-reference.md) ▶
