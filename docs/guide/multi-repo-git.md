# Multi-repo git

[devstack](../../README.md) · [Guide](./README.md) › Multi-repo git

A workspace usually spans several repositories. The `ws` group runs git across
all of them at once — clone them, fast-forward them, check their status, or run
an arbitrary git command over the set — in bounded parallel.

```bash
devstack ws clone            # clone every workspace repo that isn't on disk yet
devstack ws sync             # fetch + fast-forward pull every repo
devstack ws status           # a cross-repo status table
```

## What it operates on

`ws` acts on the projects declared in `workspace.yaml` that carry a `git:` field:

```yaml
# workspace.yaml
projects:
  - { name: api, path: services/api, git: git@github.com:acme/api.git }
  - { name: web, path: services/web, git: git@github.com:acme/web.git }
  - { name: docs, path: docs }            # no git: → skipped by ws
```

It shells out to your **system git**, so your existing SSH keys, credential
helper, and `~/.gitconfig` all apply — there is nothing extra to authenticate.
Every subcommand takes an optional list of project names to narrow the set;
with none, it acts on all git-backed projects.

Parallelism is bounded by `--jobs` (default `min(8, 2×CPUs)`).

---

## `ws clone [names...]`

Clone workspace repos in parallel. Repos already present on disk are skipped, so
this is safe to re-run.

```bash
devstack ws clone
devstack ws clone api web           # only these two
devstack ws clone --jobs 4
```

| Flag | Default | Description |
|---|---|---|
| `--jobs` | 0 | max parallel workers (0 → `min(8, 2×CPUs)`) |

---

## `ws sync [names...]`

Fetch and **fast-forward** pull every repo. When a pull brings in new commits,
the project's `postPull` hooks run (e.g. reinstall deps, re-migrate).

```bash
devstack ws sync
devstack ws sync --no-hooks         # skip postPull hooks
devstack ws sync api
```

| Flag | Default | Description |
|---|---|---|
| `--jobs` | 0 | max parallel workers |
| `--no-hooks` | false | skip the `postPull` hooks that fire on new commits |

`postPull` hooks are declared per project — see [projects](projects.md) for hook
phases.

---

## `ws status [names...]`

A cross-repo status table: branch, ahead/behind, and dirty state.

```bash
devstack ws status
devstack ws status --json
devstack ws status --check          # exit non-zero if ANY repo is dirty (CI)
```

| Flag | Default | Description |
|---|---|---|
| `--check` | false | exit non-zero if any repo has uncommitted changes (CI gate) |
| `--jobs` | 0 | max parallel workers |

---

## `ws git <git args...> [-- names...]`

Run an arbitrary git command across repos. The `--` separator splits the line:
**before `--`** is the git command passed to every repo; **after `--`** is an
optional subset of project names to run it against.

```bash
devstack ws git fetch --all                     # in every repo
devstack ws git checkout main                    # switch branch everywhere
devstack ws git checkout feature/x -- api web    # only in api and web
devstack ws git log --oneline -5 -- api          # git args + repo subset
```

| Flag | Default | Description |
|---|---|---|
| `--jobs` | 0 | max parallel workers |

Grammar: everything before `--` is forwarded verbatim to `git` in each repo;
everything after `--` selects which repos. Omit `-- names...` to run over all.

---

## Recipes

**Onboard a fresh checkout**

```bash
devstack ws clone        # pull down every repo
devstack up              # then bring the stack up
```

**Update everything each morning**

```bash
devstack ws sync         # fast-forward all; postPull hooks re-migrate/reinstall
```

**Put every repo on the same feature branch**

```bash
devstack ws git fetch --all
devstack ws git checkout feature/new-auth
```

**CI gate: fail if any repo has uncommitted changes**

```bash
devstack ws status --check    # non-zero exit ⇒ the build fails
```

## See also

- [Workspaces](workspaces.md) — declaring `projects[].git` in `workspace.yaml`
- [Projects](projects.md) — `postPull` and other lifecycle hooks
- [Lifecycle](lifecycle.md) — `up` clones/syncs as part of the saga (`--skip-clone`)
- [Command reference](command-reference.md) — every command, terse

---

◀ [Snapshots, restore & reset](data-lifecycle.md) · [Guide index](./README.md) · [Secrets](secrets.md) ▶
