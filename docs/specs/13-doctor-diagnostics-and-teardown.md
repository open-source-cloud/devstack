# Spec 13 — Doctor, diagnostics & teardown

**Module:** `internal/doctor` (+ `internal/docker`, `internal/state`, `internal/lock`, `internal/trust`, `internal/dns`, `internal/workspace`) · **Milestone:** M0 (preflight, exists) → M6 (`--fix`, recovery, teardown) · **Effort:** ~3w (feature #2 + [ARCHITECTURE §7.3](../ARCHITECTURE.md))

## Purpose
`doctor` is the support-load killer: it runs the **real branch logic** for every part of the runtime contract devstack depends on, prints a categorized, actionable report, and applies safe automatic remediations under `--fix`. This spec also **owns teardown** — `workspace destroy` (one workspace's stacks/refs/data) and `uninstall` (every machine-global artifact, including the root CA in all trust stores) — the reverse of everything the tool creates. The boundary between "doctor diagnoses / `--fix` repairs" and "teardown destroys" is the spine of this module; orphaning a CA or a dangling symlink is a defect.

## Status & scope
The M0 `doctor` already exists ([`internal/cli/doctor.go`](../../internal/cli/doctor.go)): a real probe matrix (working-dir `/mnt` refusal, 9p state/lock-dir warnings, `docker.Preflight`, state-ledger open) emitting `[]docker.Check{name,status,detail,remediation}` over a `--json` envelope, with a non-zero exit on any `fail`. This spec **extends** that matrix to the full set below, gives each probe a stable `id`/`category`/`fixable`, wires `--fix`, and adds the recovery (`--rebuild-state`) and teardown commands. The `docker.Check` struct gains `ID string`, `Category string`, and `Fixable bool` (additive — existing JSON consumers keep working).

## The full capability-probe matrix
Grouped **critical** (a `fail` → non-zero exit, gates `up`/CI), **warn** (degraded, runnable), **info** (context). Each probe runs **real branch logic** keyed off the live platform, never generic advice. `fix` column: `safe` runs under `--fix`; `—` is diagnose-only; `destroy` is teardown-only (never `--fix`).

| id | cat | what the probe actually does | remediation (one line) | fix |
|---|---|---|---|---|
| `docker.daemon` | critical | SDK `Ping(ctx, PingOptions{})` on the resolved context | start Docker / fix `DOCKER_HOST` | — |
| `docker.context` | critical | resolved context name == ledger's keyed context; on WSL2 detect Desktop-vs-`dockerd` mismatch loudly | `docker context use <ctx>` or set `DOCKER_HOST` | — |
| `compose.version` | critical | `docker compose version` parse ≥ **2.20** | upgrade the compose plugin to ≥ 2.20 | — |
| `git.version` | critical | `git --version` parse ≥ **2.30** | upgrade git to ≥ 2.30 | — |
| `disk.volumes` | warn | free bytes on the Docker data-root FS (SDK `Info().DockerRootDir`) vs a floor | free space / move Docker data-root | — |
| `net.shared` | critical | SDK `NetworkInspect("devstack_shared")`: exists, `bridge`, `external` semantics | create the missing external network | **safe** |
| `port.conflicts` | warn | for each ledger `port_alloc`: host bind-test `net.Listen("127.0.0.1",p)` **UNIONed** with SDK published bindings | free the port / reassign in the ledger | — |
| `trust.host` | warn | host CA store contains the devstack/mkcert root (platform store query) | `devstack trust install` | — |
| `trust.firefox` | warn | NSS DB (`certutil -d sql:~/.mozilla/...`) contains the root | `devstack trust install` (drives NSS) | — |
| `trust.windows` | warn | WSL2 only: `certutil.exe -store -user Root` lists the root | `devstack trust install` (drives `certutil.exe`) | — |
| `secrets.provider` | warn | per configured provider: cheap reachability/auth (SOPS keyfile present, AWS `LoadDefaultConfig` creds, Infisical login) | `devstack secrets login <provider>` | — |
| `state.refs` | warn | **stale ref rows vs live containers**: ledger `service_ref` not matched by a label-filtered `ContainerList` | prune via `--fix` / `shared doctor` | **safe** |
| `dns.resolver` | warn | platform-correct `*.localhost` path: systemd-resolved active (Linux), macOS ≥ 26, marker-fenced `/etc/hosts` present | add `/etc/hosts` entries via `--fix` | **safe** |
| `fs.statedir` | warn | XDG state/lock dir filesystem type ∈ {9p, networked} → unreliable SQLite/flock | move state to a local ext4/apfs path | — |
| `fs.workdir` | critical | CWD under `/mnt/*` (WSL2) → refuse | move the workspace onto the Linux FS | — |
| `keyring.present` | info | OS keyring reachable (else in-memory fallback) | install gnome-keyring/keychain or accept in-memory | — |
| `bin.mkcert` | info | `mkcert -version` resolvable on `$PATH` | install mkcert (trust install needs it) | — |
| `bin.cloudflared` | info | `cloudflared --version` resolvable | install cloudflared (only if tunnels used) | — |
| `alias.symlinks` | info | each registered alias in the ledger points at the current binary | relink via `--fix` | **safe** |

Every probe is independent (one failure never hides the rest) and stateless except `state.refs`/`net.shared`, which **take the `gofrs/flock`** before any read that `--fix` may mutate.

## Output & exit-code contract
- **Human:** categorized, icon-prefixed report (`✓`/`!`/`✗`), failures followed by `→ remediation`, grouped critical→warn→info. TTY detection via `golang.org/x/term`; `--quiet` prints only non-OK lines.
- **`--json`:** an array of objects with the documented stable schema — scriptable in CI ([ARCHITECTURE §7.9](../ARCHITECTURE.md)):
```json
[{ "id": "net.shared", "category": "critical", "status": "fail",
   "detail": "network devstack_shared not found",
   "remediation": "run with --fix to create it", "fixable": true }]
```
- **Exit codes** (so CI can `devstack doctor || exit 1` as a gate):

| code | meaning |
|---|---|
| `0` | no critical failure (warns/infos allowed) |
| `1` | ≥1 critical probe failed |
| `2` | doctor itself could not run (couldn't take lock, couldn't open state) |

`--fix` re-probes after each remediation and exits on the **post-fix** state, so `doctor --fix` is the idempotent "make it green where safe" verb.

## The `--fix` safe subset vs the hard boundary
`--fix` may only perform **reconstructible, non-destructive** repairs — those that recreate tool-owned plumbing or prune derived state, never anything carrying user data:

| `--fix` MAY (safe) | `--fix` MUST NEVER |
|---|---|
| create the missing `devstack_shared` external network | stop/recreate a stateful shared service (drops live DB connections — [spec 03](03-workspaces-and-shared-services.md)) |
| prune stale `service_ref` rows with no live container | remove any named volume or Postgres/Redis/MinIO data |
| add marker-fenced `/etc/hosts` entries (sudo, idempotent) | drop a role/database/bucket (that's `db gc`, with confirm) |
| relink alias symlinks to the current binary | uninstall the CA from any trust store (that's teardown) |
| reallocate a conflicting port row inside the lock | delete or migrate `state.db` destructively |

All `--fix` mutations run **inside the flock** and are logged to the `event_log` with a reason. Anything that destroys data is **out of `--fix` by construction** and lives in teardown below.

## Recovery commands
- **`doctor --rebuild-state`** — reconstruct the **entire ledger** from live Docker labels + on-disk config when `state.db` is lost/corrupt (the expected WSL2/9p `kill -9` outcome — [spec 08](08-state-locking-and-lifecycle.md)). Backs up the old `state.db`, re-derives `shared_service`/`service_ref` from a label-filtered `ContainerList` (`All=true`, exclude `oneoff=true`, correct context), and re-derives `provisioned`/`port_alloc` from live container env + published bindings. Lossy by definition (the event log can't be reconstructed) — prints what it inferred.
- **`shared doctor`** — narrower: reconcile ref counts against live containers and report each shared service + its referencing projects ([spec 03](03-workspaces-and-shared-services.md)). `doctor --fix` calls into the same reconcile for the `state.refs` probe.

## Teardown (this spec owns it)
Two distinct destructive verbs, both gated by explicit data-loss confirmation (`--yes` to skip, `--json` for scripted output), both holding the flock for the whole operation:

**`workspace destroy`** — scope = *this workspace only*. Order:
1. `compose down` each project stack (`-p devstack-<name>`), then the shared stack iff no **other** workspace references it.
2. Delete this workspace's `service_ref` + `port_alloc` rows.
3. **Optionally** (`--purge-data`, separate confirm) drop this workspace's provisioned roles/dbs/buckets (`provisioned` ledger) and remove its named volumes.
4. Remove the project's `.devstack/` generated artifacts.

Leaves machine-global artifacts (network, shared CA, alias symlinks) intact — other workspaces may still use them.

**`uninstall`** — scope = *every machine-global artifact*. Strict order (network/volumes before the ledger that records them; CA last because it's the security-critical one):
```
1. compose down ALL stacks (shared + every known project)
2. docker network rm devstack_shared            (devstack owns it; compose won't)
3. docker volume rm <shared named volumes>       ← Postgres data! explicit confirm
4. remove root CA: host store + Firefox/NSS + Windows store (certutil.exe on WSL2)
5. remove marker-fenced /etc/hosts entries
6. remove alias symlinks from XDG_BIN_HOME
7. delete keyring entries + cloudflared creds
8. delete template cache + the SQLite ledger (state.db + backups)
```
Step 4 is the load-bearing one: **a CA left in any trust store after `uninstall` is a security defect** ([ARCHITECTURE §7.3](../ARCHITECTURE.md)) — uninstall must report each store it cleared and warn loudly if any is unreachable (corporate-managed Windows can refuse). `uninstall` does **not** touch any committed `workspace.yaml`/`devstack.yaml` (those are the user's source).

## Cross-platform probe matrix
`doctor` exercises the **real** branch per platform — a single WSL-detection utility (`uname` contains `microsoft`) feeds the switch ([ARCHITECTURE §8](../ARCHITECTURE.md)):

| probe | linux | WSL2 | macOS |
|---|---|---|---|
| `docker.context` | single daemon | **Desktop vs in-distro `dockerd`** — the high-value check | Desktop VM context |
| `trust.host` | NSS/p11-kit system store | Linux store **+ Windows store via `certutil.exe`** | macOS Keychain (`security`) |
| `trust.firefox` | `certutil -d sql:~/.mozilla` | same (Linux-side Firefox) **and** Windows Firefox | `certutil` if installed |
| `dns.resolver` | systemd-resolved active? | resolved often absent → `/etc/hosts` | macOS ≥ 26 native, else `/etc/hosts` |
| `fs.workdir` | n/a | **refuse `/mnt/*`** | n/a |
| `fs.statedir` | warn on NFS | **warn on 9p** | warn on networked |
| `port.conflicts` | bridge host-reachable | Desktop-VM proxy → SDK union mandatory | Desktop-VM proxy → SDK union mandatory |

## Verified constraints / gotchas
- **A host bind-test does not reflect Docker Desktop / WSL2 VM-published ports** — `net.Listen("127.0.0.1",p)` succeeds while the VM proxy already holds the port. **Union** the bind-test with SDK `ContainerList` published bindings or `port.conflicts` reports false greens ([spec 08](08-state-locking-and-lifecycle.md)).
- **Reconcile correctness depends on the label-filter rules:** `ListOptions.All=true` (else exited containers invisible → false-stale refs), **exclude `com.docker.compose.oneoff=true`** (else `compose run`/`exec` inflate counts), filter on **our own label**, and connect to the **correct context** ([spec 03](03-workspaces-and-shared-services.md), [DECISIONS D5](../DECISIONS.md)).
- **SQLite/flock locking is unreliable on 9p** — `fs.statedir` **warns** (doesn't fail); working dirs under `/mnt/*` are **refused** outright (`fs.workdir` critical). WAL + `busy_timeout` + flock are required together but still degrade on 9p ([DECISIONS D6/D7](../DECISIONS.md)).
- **A CA left in any trust store after `uninstall` is a security defect** — Firefox uses its own NSS DB (ignores `/etc/hosts` and the OS store), and on WSL2 the browser-trusted CA lives in the **Windows** store reached via `certutil.exe`. All three must be cleared and reported.
- **Compose refuses to create or remove `external: true` networks** — so `net.shared` `--fix` (create) and `uninstall` step 2 (remove) must use the SDK / `docker network` directly, never `compose up/down`.
- **`CREATE DATABASE` is not idempotent and PG18+ moved PGDATA** — `--rebuild-state` re-derives the `provisioned` ledger by reading live state, never by re-running provisioning SQL ([DECISIONS D8](../DECISIONS.md)).
- **`--fix` must never recreate a stateful shared service** to apply a drifted compose field — it drops every project's live DB connections and risks the PG18 volume footgun; that requires explicit confirm, out of `--fix` ([spec 03](03-workspaces-and-shared-services.md)).

## Acceptance criteria
- [ ] `doctor` emits the full matrix with stable `id`/`category`/`status`/`detail`/`remediation`/`fixable`; `--json` validates against the documented schema.
- [ ] Exit code is `1` iff ≥1 **critical** probe failed, `0` with only warns/infos, `2` if doctor couldn't take the lock or open state — CI can gate on it.
- [ ] On WSL2 with the wrong active context, `docker.context` fails with the Desktop-vs-`dockerd` remediation (not a generic message).
- [ ] `port.conflicts` flags a port already held by a Docker-Desktop-VM-published binding that a bare host bind-test reports as free.
- [ ] `doctor --fix` creates a missing `devstack_shared` network, prunes stale ref rows, adds `/etc/hosts` entries, and relinks alias symlinks — and re-probes green — all inside the flock.
- [ ] `doctor --fix` **never** removes a volume, drops a db/role/bucket, recreates a stateful service, or removes a CA.
- [ ] Deleting `state.db` then `doctor --rebuild-state` reconstructs `shared_service`/`service_ref`/`port_alloc`/`provisioned` from live labels + config, after backing up the corrupt file.
- [ ] `workspace destroy` removes this workspace's stacks + ref/port rows (and, with `--purge-data` + confirm, its dbs/volumes) while leaving the shared network and CA intact for other workspaces.
- [ ] `uninstall` leaves **no** external network, shared volume, CA (host + Firefox + Windows), alias symlink, keyring entry, cloudflared cred, template cache, or ledger behind; it reports each trust store cleared.
- [ ] `uninstall` and `workspace destroy` require explicit confirmation for any data loss; `--yes` skips it; `--json` emits a machine-readable result.

## Dependencies / consumers
Consumes `internal/docker` (SDK probes + `Preflight`), `internal/state` + `internal/lock` (the spine — every `--fix`/teardown mutation is locked), `internal/trust` (CA store query + removal), `internal/dns` (`/etc/hosts`), `internal/workspace` (ref reconcile, shared-stack lifecycle), `internal/alias` (symlink relink/removal), `internal/xdg` (path + FS-type detection). Consumed by `internal/cli` (`doctor`, `workspace destroy`, `uninstall`) and by `internal/orchestrate` (the `up` saga runs the critical subset as preflight, [spec 09](09-orchestration-and-onboarding.md)). **Thinner v1 (~1.5w):** the existing M0 matrix + the `state.refs`/`net.shared`/`alias.symlinks` `--fix` subset + `uninstall`. **Full (~3w):** all probes, `--rebuild-state`, `workspace destroy --purge-data`, and the complete cross-platform trust-store teardown.

## Open questions
[Q-DAEMON](../OPEN-QUESTIONS.md) (no daemon → all reconcile is lazy, on-command; `doctor`/`shared gc` are the only cleanup), [Q-PLATFORM](../OPEN-QUESTIONS.md) (native-Windows teardown of `.cmd` shims + Windows CA store is best-effort), [Q-CA](../OPEN-QUESTIONS.md) (mkcert-owned vs self-owned CA changes how `trust.*` probes query and how `uninstall` removes it). **New:** [Q-DOCTOR-FIX] — should `doctor --fix` ever offer an *interactive* (non-`--yes`) tier for borderline-destructive repairs (e.g. reassigning a port a foreign process holds, recreating a drifted *stateless* shared service), or is the safe/destroy line absolute? Recommendation: keep `--fix` strictly non-destructive; route everything else through the named teardown/`shared` verbs.
