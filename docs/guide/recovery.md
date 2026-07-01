# Recovery, teardown & housekeeping

[devstack](../../README.md) · [Guide](./README.md) › Recovery, teardown & housekeeping

The **ledger is a cache of reality**, not the source of truth. The real state is
your committed config plus the live Docker containers, networks, and volumes.
When the two drift — a container was killed out-of-band, a workspace directory
was deleted, ref counts look wrong — devstack reconciles the ledger back to what
Docker actually shows. Nothing here loses data unless you explicitly ask it to.

```bash
devstack doctor --fix          # apply safe, non-destructive remediations
devstack shared doctor         # reconcile shared-service refs against live containers
devstack shared gc --stop      # stop shared engines nobody references anymore
```

## Reconcile & self-heal (non-destructive)

Start here when something looks inconsistent. None of these destroy data.

| Command | What it does |
|---|---|
| `doctor --fix` | apply safe, idempotent, **non-destructive** remediations under the lock |
| `doctor --rebuild-state` | reconstruct the ledger from config + live container labels |
| `shared doctor` | reconcile the ledger against live containers (prune dead refs) |

```bash
# Ledger looks wrong but data is fine → rebuild the ledger from reality
devstack doctor --rebuild-state

# Shared ref counts drifted (a project stack died) → prune dead refs
devstack shared doctor
```

`doctor --fix` fixes what it safely can (network missing, stale rows) and leaves
anything destructive to you. `doctor --rebuild-state` throws away the ledger's
bookkeeping and re-derives it from container labels — useful after a ledger
corruption or a manual `docker` intervention.

## Reclaiming resources (housekeeping)

Shared engines stay **warm** (there is no autostop). Reclaim them, and clean up
orphaned per-project resources, on demand:

```bash
devstack shared gc            # report shared services at zero references
devstack shared gc --stop     # actually stop the zero-ref services
```

| Command | Flag | Notes |
|---|---|---|
| `shared gc` | `--stop` (false) | report (default) or stop zero-reference shared services |
| `db gc` | `--yes` (false) | reclaim DB/role resources whose owner project left the workspace |
| `resource gc` | `--yes` (false) | reclaim any resources whose owner project left the workspace |

`db gc` and `resource gc` are **destructive** (they drop provisioned resources
belonging to projects no longer in the workspace) and require `--yes` under
`--json`/non-interactive use.

```bash
devstack db gc --yes          # drop DBs/roles orphaned by removed projects
devstack resource gc --yes    # same, across every resource kind
```

## The teardown ladder: narrow → total

Pick the smallest rung that does what you need. Higher rungs preserve less.

### 1. `down` — stop this workspace's stacks (data + shared preserved)

```bash
devstack down [project...]
```

Stops the workspace's project stacks and releases their refs. **Never removes
volumes; shared services keep running.** The everyday "I'm done for now" command.
Named projects only stop those; no args stops all.

### 2. `workspace destroy` — tear down this workspace (data preserved by default)

```bash
devstack workspace destroy            # stacks + refs + ports released; volumes/DBs kept
devstack workspace destroy --purge-data --yes   # ALSO drop every provisioned resource
```

| Flag | Default | Description |
|---|---|---|
| `--yes` | false | required for `--json`/non-interactive |
| `--purge-data` | false | also **DROP** every provisioned resource (DESTRUCTIVE) |

Removes this workspace's stacks and releases its refs and allocated host ports.
**Volumes and databases are preserved** unless you add `--purge-data`, which
drops every provisioned resource this workspace owns.

### 3. `workspace list --prune` — clean the workspace registry

```bash
devstack workspace list             # every registered workspace for this Docker context
devstack workspace list --prune     # drop registry rows whose root no longer exists
```

| Flag | Default | Description |
|---|---|---|
| `--prune` | false | drop registry rows whose root directory no longer exists |

Housekeeping for the machine-global registry after you've deleted workspace
directories on disk. `--prune` only removes stale bookkeeping rows — it doesn't
touch Docker resources.

### 4. `uninstall` — remove EVERYTHING machine-global

```bash
devstack uninstall            # interactive: prompts you to type `uninstall`
devstack uninstall --yes      # non-interactive (required with --json)
```

| Flag | Default | Description |
|---|---|---|
| `--yes` | false | required for `--json`; skips the typed confirmation |

The nuclear option. A 6-step reversal that removes **every machine-global
devstack artifact**: shared stacks, the `devstack_shared` network, volumes, the
local CA, the `/etc/hosts` block, installed aliases, and the ledger. It
**destroys data and is irreversible**. The interactive path makes you type
`uninstall` to confirm. It does **not** touch your committed `workspace.yaml` /
`devstack.yaml` — those are yours.

## What each rung preserves vs destroys

| Command | Stops stacks | Releases refs/ports | Volumes/DBs | Provisioned resources | Network/CA/hosts/aliases/ledger |
|---|---|---|---|---|---|
| `down` | ✅ | ✅ | **kept** | kept | kept |
| `workspace destroy` | ✅ | ✅ | **kept** | kept | kept |
| `workspace destroy --purge-data` | ✅ | ✅ | **kept** | **DROPPED** | kept |
| `uninstall` | ✅ (all) | ✅ (all) | **DESTROYED** | **DESTROYED** | **REMOVED** |

Shared engines stopped by leaving them at zero refs can always be reclaimed later
with `shared gc --stop`; committed config is never touched by any of these.

## Recipes

**"Something's off but I don't want to lose data"**

```bash
devstack doctor --fix
devstack shared doctor
devstack status            # confirm health + the ref graph
```

**"I deleted a project directory by hand"**

```bash
devstack shared doctor          # prune the dead refs
devstack workspace list --prune # drop the stale registry row
```

**"Free the RAM — nothing is using the shared engines"**

```bash
devstack shared gc --stop
```

**"Wipe this machine's devstack state completely"**

```bash
devstack uninstall     # type `uninstall` to confirm
```

## See also

- [Shared services & host access](shared-services.md) — ref counts, `shared status`, `shared gc`.
- [Data lifecycle](data-lifecycle.md) — snapshots and restore before you purge data.
- [Installation, doctor & updates](installation.md) — the full `doctor` preflight matrix.
- [command-reference.md](command-reference.md) — every teardown/housekeeping verb, terse.

---

◀ [Public tunnels](tunnels.md) · [Guide index](./README.md) · [The global store ▶](store.md)
