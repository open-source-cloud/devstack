# Global flags & scripting

[devstack](../../README.md) · [Guide](./README.md) › Global flags & scripting

Four persistent flags are available on **every** command, plus a pre-parsed
`--as`. This page covers what they do, the logging levels, the `--yes`
requirement under `--json`, the `--check` drift flags, the recurring local flags,
and scripting/CI patterns.

## The global flags

| Flag | Type | Default | Description |
|---|---|---|---|
| `--json` | bool | false | machine-readable JSON output where supported |
| `--quiet` | bool | false | suppress non-essential output |
| `--debug` | bool | false | log every external command (docker/git/sql) + internals |
| `--verbose` | bool | false | more detailed human output |

```bash
devstack status --json | jq .
devstack up --debug          # see every docker/git/sql invocation
devstack shared status --quiet
```

`--as <name>` (or `--as=<name>`) is **pre-parsed** before cobra and stripped from
the args — it overrides the binary's branding (see [aliases.md](aliases.md)). A
literal `--` stops the scan. fang also adds `--version`/`-v` and `--help`/`-h`.

## Logging levels

The verbosity flags map to slog levels:

| Flag | Level |
|---|---|
| `--debug` | Debug (+ source) |
| `--verbose` | Info |
| *(default)* | Warn |
| `--quiet` | Error |

`--debug` is the one to reach for when something fails: it prints every external
command devstack shells out to, which is usually enough to reproduce the failure
by hand.

## Destructive verbs need `--yes` under `--json`

Any command that can destroy data prompts interactively for confirmation. Under
`--json` (or otherwise non-interactively) there's no prompt, so you **must** pass
`--yes` — otherwise the command refuses:

```bash
devstack db reset --project api --json --yes
devstack workspace destroy --json --yes
```

Verbs that require it: `workspace destroy`, `uninstall`, `db reset` /
`db restore` / `db drop` / `db gc`, `resource rm` / `resource gc`, `s3 rb`,
`queue rm` / `topic rm` / `stream rm`. (`uninstall` additionally requires typing
`uninstall` at an interactive prompt.)

## `--check` — drift gates for CI

Three read-only commands take `--check`: they write nothing and exit non-zero if
something is out of date. Ideal as CI gates.

| Command | `--check` means |
|---|---|
| `generate --check` | report drift between config and generated artifacts; write nothing. |
| `ide --check` | report drift in editor configs. |
| `ws status --check` | exit non-zero if any repo is dirty. |

```bash
devstack generate --check     # fails the build if artifacts are stale
devstack ws status --check    # fails if any repo has uncommitted changes
```

`doctor` and `self check` also exit non-zero on failure/available-update, which
makes them usable as gates too.

## Recurring local flags

These aren't global, but they show up on many commands — worth knowing once:

| Flag | Appears on | Meaning |
|---|---|---|
| `--project` | resource/db/s3/queue/topic/stream, `shell`, `generate` | which project owns the operation. Default: the workspace's single project, else the first alphabetically. |
| `--no-prefix` | tenant create/drop verbs (db/s3/queue/topic/stream) | use the literal name instead of the default `<project>_<name>` (Postgres) / `<project>-<name>` (bucket/messaging). |
| `--engine` | resource + messaging verbs | pin the target shared engine instead of inferring it. |
| `--profile`/`-p` | `up`, `generate` | the env-overlay / service-slice profile (`up -p` is repeatable & comma-separated). |
| `--jobs` | `ws` group | max parallel repos (default `min(8, 2×CPUs)`). |

## Scripting & CI recipes

**Parse JSON output with `jq`.**

```bash
devstack status --json | jq -r '.shared[] | "\(.engine) refs=\(.refs)"'
devstack shared ports --json | jq -r '.[] | "\(.service) \(.address)"'
```

**Gate a PR on stale generated artifacts.**

```bash
devstack generate --check      # non-zero exit → artifacts drifted, fail CI
```

**Fail if any repo in the workspace is dirty.**

```bash
devstack ws status --check
```

**Non-interactive teardown in a throwaway CI environment.**

```bash
devstack workspace destroy --json --yes
# or the full machine-global wipe:
devstack uninstall --json --yes
```

**Trace exactly what devstack runs.**

```bash
devstack up --debug 2>debug.log    # every docker/git/sql command is logged
```

**Run under a branded alias in CI (no symlink needed).**

```bash
devstack --as rq up
```

## See also

- [Command reference](command-reference.md) — every command and its flags.
- [Aliases & argv[0] dispatch](aliases.md) — how `--as` and branding work.
- [Recovery, teardown & housekeeping](recovery.md) — the destructive verbs and `doctor`.
- [Lifecycle](lifecycle.md) — `up`/`down`/`status` and their JSON output.

---

◀ [Command reference](command-reference.md) · [Guide index](./README.md) · [What's next: roadmap & current gaps](whats-next.md) ▶
