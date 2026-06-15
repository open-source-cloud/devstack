# Spec 11 — Lifecycle hooks

**Module:** `internal/hooks` · **Milestone:** M6 · **Effort:** ~2w (feature #5)

## Purpose
Declarative hooks that run user-supplied commands at well-defined points in the `up`/`down`
saga — on the host or inside a service container — with the same `${ref}` and `secret://`
interpolation the rest of the config gets. Hooks are the missing glue between "containers are
up" and "the app actually works": DB migrations, first-run seeding, app-key generation,
`npm/composer/bundle install`. The load-bearing design move is a **ledger-backed `firstRun`**
that runs **exactly once per provisioned data volume and survives restarts** — the correct
replacement for Postgres `initdb.d`, which never re-runs on an already-populated shared volume
([spec 03](03-workspaces-and-shared-services.md), [DECISIONS D8](../DECISIONS.md)).

## Hook points and where they fire in the saga
Hooks slot into named phases of the `up`/`down` saga ([spec 08](08-state-locking-and-lifecycle.md) §up-saga,
[spec 09](09-orchestration-and-onboarding.md)). They run **after their gating phase succeeds**,
never inside the global flock (see gotchas). DB-touching hooks fire only after the shared DB is
**healthy** ([spec 10](10-health-readiness-and-ordering.md)).

| Hook | Fires | Scope | Re-runs? |
|---|---|---|---|
| `preUp` *(optional)* | after generate, before `compose up -d` | per-project | every `up` |
| `firstRun` | once after provision + DB healthy, before `postUp` | per-project, **keyed by data-volume identity** | **never** once satisfied (ledger) |
| `postUp` | after `compose up -d` + dependents healthy | per-project | every `up` (or guard with own ledger key) |
| `postPull` | after `ws sync`/clone advances a repo's HEAD | per-project, **keyed by resolved commit** | once per new commit |
| `preDown` | before `compose down`/`stop` | per-project | every `down` |

- **Workspace-scope hooks** (declared in `workspace.yaml`) wrap the whole bootstrap: a
  workspace `firstRun` runs once per workspace (scope_key = workspace name + shared-stack
  identity), a workspace `postUp` after **all** projects are up. Project-scope hooks
  (`devstack.yaml`) run per project. Ordering: workspace `preUp` → per-project phases →
  workspace `postUp`; mirrored in reverse for `preDown`.
- `firstRun` is **not** "first `up`" — it is "first time on **this** data volume". A reset of
  the shared Postgres volume (or a fresh machine) re-arms it (see scope_key below).

## Config schema
Hooks attach to a project or the workspace, and (for service-targeted hooks) to a service
([spec 01](01-config-schema.md) grammar; lists **replace** on overlay merge unless `$merge: append`).

```yaml
# devstack.yaml (project scope)
hooks:
  firstRun:
    - name: migrate-and-seed
      run: exec                       # host | exec
      service: api                    # required for run: exec
      command: ["sh","-lc","php artisan migrate --force && php artisan db:seed --force"]
      workdir: /var/www/html          # in-container path for exec; repo-root-relative for host
      env: { APP_ENV: "${profile}", DB_URL: "secret://aws/api#db_url" }
      timeout: 5m                     # Go duration; default 120s
      retries: 0                      # default 0; firstRun/migrate often want >0
      onFailure: abort                # abort (default) | warn | continue
  postUp:
    - { name: assets, run: exec, service: web, command: ["npm","run","build"], timeout: 10m }
  preDown:
    - { name: flush, run: host, command: ["./scripts/flush.sh"], onFailure: warn }
```

- `run: host` executes via `os/exec` from the documented working directory; `run: exec` shells
  into a **running** service via `docker compose -p <proj> exec -T <service> <command>`.
- `command` is an **argv array** (no shell parsing by us; use `sh -lc "…"` explicitly if you
  want a shell). `${ref}`/`${env}`/`${self}` are resolved by us before exec; `secret://` is
  resolved in the same batched pass as generation ([spec 04](04-secrets.md)).
- Defaults: `timeout: 120s`, `retries: 0`, `onFailure: abort` (`warn` for `preDown`).

## Execution model
Both transports converge on one `Runner` that (1) interpolates `command`/`env`/`workdir`,
(2) resolves `secret://` to values held only in memory, (3) builds the child environment, and
(4) runs under a `context.WithTimeout`.

```
host:  exec.CommandContext(ctx, argv[0], argv[1:]...)
       cmd.Dir = <repo-root | workspace-root>          // documented, never CWD
       cmd.Env = base ++ interpolatedEnv ++ resolvedSecrets   // secrets last
exec:  docker compose -p devstack-<proj> exec -T \
         -w <workdir> -e NAME (valueless, value via our process env? NO — see below) \
         <service> <argv...>
```

- **Secrets reach `run: host` via `cmd.Env`** and `run: exec` via repeated `-e NAME=VALUE` flags
  on `compose exec` (note: `compose exec -e` **does** accept `NAME=VALUE`, unlike `up`). Secret
  **values are never written to any generated file** and never logged ([ARCHITECTURE §7.5](../ARCHITECTURE.md),
  [spec 04](04-secrets.md)); `--debug` redacts hook env values.
- `-T` disables TTY allocation so hooks are deterministic in non-interactive/CI contexts; output
  is captured (combined stdout/stderr) for the saga checklist and the failure remediation.
- `run: exec` requires the target service **already running** — that is why `postUp`/`firstRun`
  fire *after* `compose up -d`; a `preUp` `run: exec` is a config error (lint: service not yet up).

## Idempotency ledger
`firstRun` and `postPull` are tracked in the `hook_run(ctx, project, hook, scope_key)` table
already reserved in [spec 08](08-state-locking-and-lifecycle.md). A row exists **iff** that hook
is satisfied for that scope.

```sql
-- satisfied check (lock-free read) before running:
SELECT 1 FROM hook_run WHERE ctx=? AND project=? AND hook=? AND scope_key=?;
-- recorded ONLY on success, inside the flock:
INSERT INTO hook_run(ctx,project,hook,scope_key,ran_at) VALUES(?,?,?,?,?);
```

- **`firstRun` scope_key = identity of the provisioned data volume.** Concretely the tuple
  `(shared_service, provisioned db/role name)` from the `provisioned` ledger ([spec 03](03-workspaces-and-shared-services.md) §isolation),
  or the Docker named-volume’s `Mountpoint`/created-at for non-DB volumes. Because the key is the
  volume — not the container or the `up` count — `firstRun` **survives `down`/`up` and container
  recreation**, and **re-arms** when the volume is dropped (`db gc`, `workspace destroy`, or a
  manual `docker volume rm`). This is precisely the gap `initdb.d` leaves open.
- **`postPull` scope_key = resolved commit SHA** (from `gitx` after `ws sync`). Runs once per new
  HEAD; a no-op pull (same SHA) skips it.
- **`preUp`/`postUp` are unconditional by default** (every `up`); a hook may opt into once-only
  semantics by declaring `once: true`, which gives it a stable scope_key (`name@digest-of-command`).
- **Flags:** `--skip-hooks[=firstRun,postUp]` suppresses execution (records nothing); `--force-hooks[=…]`
  deletes the matching `hook_run` rows first, forcing a re-run. `up --no-firstRun` is sugar.

## Failure semantics
| Hook | Default `onFailure` | Effect on saga | Ledger on failure |
|---|---|---|---|
| `firstRun` | `abort` | fails `up`; compensating actions per [spec 08](08-state-locking-and-lifecycle.md) | **no row written** → retried next `up` |
| `postUp` | `abort` | fails `up` (stack stays up; phase marked unsatisfied) | n/a (or row deleted if `once`) |
| `preUp` | `abort` | fails `up` before any container starts | n/a |
| `postPull` | `warn` | logs, continues | **no row** → retried next sync |
| `preDown` | `warn` | logs, `down` proceeds | n/a |

- The cardinal rule: **a failed `firstRun`/`postPull` records nothing**, so the operation is
  naturally retried on the next run — the ledger only ever reflects *success*. This makes hooks
  resumable for free (re-running `up` reruns only unsatisfied hooks).
- **Timeout** kills the process group (`SIGTERM` then `SIGKILL` after a grace) and counts as a
  failure; **retries** apply a fixed backoff between attempts, capped by the per-hook `timeout`
  for the whole sequence. A hook that the user `Ctrl-C`s is a failure, not a success.
- `onFailure: continue` runs remaining hooks then still fails the phase; `warn` downgrades to a
  non-fatal warning. `preDown` defaults to `warn` so a broken teardown hook can never trap a
  workspace in the "can't go down" state.

## Security / trust model
Hooks execute **arbitrary commands declared in committed config** — the same trust level as
running the repo's `Makefile`, an `entrypoint`, or a compose `command:`. v1 adds **no sandbox**:
no namespacing, no seccomp, no allow-list. The threat model is documented, not engineered around,
consistent with the secrets posture ([ARCHITECTURE §7.5](../ARCHITECTURE.md)):

- Resolved `secret://` values are visible to the hook child process and, while it runs, via
  `/proc/<pid>/environ` to same-user processes. This is identical to how the app container already
  receives them. Do not pass non-local secrets to a hook you would not trust with the app's env.
- `run: host` hooks run with the **invoking user's** privileges on the host (not in a container);
  treat a workspace from an untrusted source exactly like cloning and `make`-ing it.
- A CI test asserts no hook path ever writes a secret value to disk or to a generated file
  ([spec 04](04-secrets.md) test parity).

## Canonical uses (and their ordering vs health)
| Use | Hook | run | Ordering note |
|---|---|---|---|
| DB schema migration | `firstRun` (+`postPull` for new migrations) | `exec` | **after** shared DB healthy ([spec 10](10-health-readiness-and-ordering.md)) |
| First-run seed data | `firstRun` | `exec` | after migrate; once per volume |
| App-key / secret generation | `firstRun` | `exec` or `host` | before `postUp`; persist to secrets provider, not git |
| `npm`/`composer`/`bundle install` | `postPull` (deps changed) or `firstRun` | `exec`/`host` | gate on lockfile-hash via `once`/scope_key |
| wait-for-it style gating | prefer `dependsOn: healthy` ([spec 10](10-health-readiness-and-ordering.md)); hook only for non-modeled deps | `exec` | health graph is the first-class mechanism |

Migrations and seeds are `firstRun` (idempotent-by-volume) plus a `postPull` keyed to the new
commit, so pulling a branch with new migrations re-runs them without forcing a full re-seed.

## Verified constraints / gotchas
- **`initdb.d` runs only on an empty PGDATA**, and the freshness marker is a **non-empty
  `PG_VERSION`** — a volume containing only `lost+found` still counts as fresh, so a host-mounted
  dir can mis-trigger or mis-skip it. `firstRun` is ledger-tracked precisely to avoid relying on
  this marker ([DECISIONS D8](../DECISIONS.md)). PG18+ also moved PGDATA to `/var/lib/postgresql`.
- **`compose exec` requires the service running** (it has no implicit start, unlike `run`); enumerate
  the target with the read-only SDK (`All=true`, exclude `oneoff=true`, correct context — [spec 03](03-workspaces-and-shared-services.md))
  before exec'ing and emit a clear remediation if it's down.
- **Host hooks need a documented working directory**: `run: host` defaults to the **repo root** for
  project hooks and the **workspace root** for workspace hooks — never the process CWD (which moves
  with the user). This is part of the `--json` contract for reproducibility.
- **Long hooks must respect the timeout and must NOT hold the global flock.** The flock is taken only
  around ledger mutations (the `hook_run` insert, port/ref rows); the hook body runs **outside** it,
  or a 10-minute `npm install` would serialize every other invocation on the machine ([spec 08](08-state-locking-and-lifecycle.md) #1 rule).
- **`compose exec -e NAME=VALUE` works; `compose up` has no per-run env injection** — that asymmetry
  is why generation emits valueless per-service `environment:` keys but hooks can pass values inline
  ([DECISIONS D10](../DECISIONS.md), [ARCHITECTURE §7.5](../ARCHITECTURE.md)).
- **argv, not a shell string** — we never word-split `command`; users who need globbing/pipes write
  `["sh","-lc","…"]`. This avoids the classic injection/quoting footguns and keeps `--json` faithful.
- **A `kill -9` between hook success and the ledger insert** re-runs the hook next time (idempotent
  body assumed) — safe by design, and the reason hook bodies for `firstRun` must themselves be
  existence-guarded where they touch shared state.

## Acceptance criteria
- [ ] A project `firstRun` migration runs exactly once across `up`→`down`→`up` (no container reuse);
      no second run while the data volume persists.
- [ ] Dropping the shared Postgres volume (`db gc`/`workspace destroy`) re-arms `firstRun`; the next
      `up` re-migrates and re-seeds.
- [ ] A failed `firstRun` aborts `up`, writes **no** `hook_run` row, and is retried (and can succeed)
      on the next `up`.
- [ ] `run: exec` against a stopped service yields a clear "service not running" remediation, not a
      raw compose error.
- [ ] A `postPull` hook runs once for a new HEAD and is skipped on a no-op pull (same SHA).
- [ ] No generated file and no `--debug` log line ever contains a `secret://`-resolved value used by a hook.
- [ ] A 60s hook does not block a concurrent `up` in another terminal (flock not held during the hook body).
- [ ] `--skip-hooks` runs the saga with hooks suppressed; `--force-hooks=firstRun` re-runs a satisfied `firstRun`.
- [ ] A `preDown` hook failure warns but allows `down` to complete.

## Dependencies / consumers
Consumes `internal/orchestrate` (phase boundaries + saga state), `internal/state` + `internal/lock`
(the `hook_run` ledger, written inside the flock), `internal/config` (hook schema + `${ref}`/`secret://`
grammar), `internal/secrets` (batched `Resolve`), `internal/docker` (compose `exec` driver + read-only
service-state probe), `internal/provision` (the data-volume identity that keys `firstRun`),
`internal/health` ([spec 10](10-health-readiness-and-ordering.md), DB-healthy gate), and `internal/gitx`
([spec 06](06-git.md), commit SHA for `postPull`). Consumed by `internal/cli` (`--skip-hooks`/`--force-hooks`
flags, `up`/`down`/`ws sync` surface) and surfaced in the `up` checklist ([spec 09](09-orchestration-and-onboarding.md)).
**Effort:** a thinner v1 (project-scope `firstRun`/`postUp` only, `run: exec`, no retries/workspace-scope)
lands in ~1w; full (workspace scope, `postPull`/`preUp`/`preDown`, retries, host hooks, `--force-hooks`) is ~2w.

## Open questions
[Q-DAEMON](../OPEN-QUESTIONS.md) (without a daemon, hooks run only for the duration of `up`/`down`; there is
no autostop-triggered hook). New: **Q-HOOK-SCOPE** — should `firstRun`'s data-volume identity be the
provisioned `(db,role)` tuple (survives Postgres image upgrades that keep the volume) **or** the Docker
named-volume id (re-arms whenever the volume object is recreated, e.g. a compose volume rename)? They
diverge on the "upgraded the Postgres major but kept the data" path; v1 recommends the `(db,role)` tuple,
keyed alongside the `provisioned` ledger, and revisits if image-upgrade re-seeding becomes a real workflow.
