# devstack — Troubleshooting

Run **`devstack doctor`** first — it runs the real probe matrix (daemon, compose,
git, filesystem, trust, dns) and prints a one-line remediation for each problem.
`devstack doctor --json` emits the machine-readable contract.

## `up` fails or hangs

- **"docker daemon not reachable" (preflight).** Start Docker / fix
  `DOCKER_HOST` or the active `docker context`. `devstack doctor` confirms the
  context the ledger is keyed by.
- **A shared service never goes healthy.** `up` fails fast and inlines the last
  log lines of the failing service plus the failing healthcheck. Slow stateful
  images (Postgres/MinIO) on Docker Desktop/WSL2 may need a larger `startPeriod`
  in the service's `healthcheck:`.
- **"dependsOn cycle: a → b → a".** A `dependsOn` loop is reported with the full
  path at generate time — break the cycle.
- **"condition: healthy … declares no healthcheck".** A `dependsOn` edge with
  `condition: healthy` requires the target to define a `healthcheck:`. Add one or
  use `condition: started`.

## State / ledger

- **Stale ref counts after a crash or manual `docker rm`.** Reconcile:
  `devstack shared doctor` (prunes refs for projects no longer live). Every
  command also self-heals lazily.
- **Corrupt or lost `state.db`.** `devstack doctor --rebuild-state` reconstructs
  the shared-service + ref rows from live container labels + your config. A backup
  is written before any migration.
- **WSL2 "database is locked" / flaky locking.** Keep `XDG_STATE_HOME` and
  `XDG_RUNTIME_DIR` on the Linux filesystem (ext4/tmpfs), not `/mnt/*` (9p).
  `devstack doctor` warns when they're on an unreliable filesystem.

## Concurrency

Two terminals running `devstack` at once is safe: only the four mutating points
(network-ensure, port allocation, ref rows, `CREATE ROLE`) briefly serialize on
the machine-global lock; everything else interleaves.

## Local HTTPS / DNS

- **`*.localhost` doesn't resolve.** `*.localhost` is not zero-config everywhere
  (WSL2/minimal Ubuntu lack systemd-resolved; macOS ≤15 resolves it only in
  browsers; Firefox ignores `/etc/hosts`). Run `sudo devstack dns setup` to write
  the marker-fenced `/etc/hosts` block; `devstack dns status` shows what's
  missing.
- **Browser shows an untrusted cert.** `sudo devstack trust install` (needs
  `mkcert`; `certutil` from `libnss3-tools` for Firefox). `devstack trust status`
  prints the exact missing tool. On WSL2 the CA must also be imported into the
  Windows store (browsers run on Windows).

## Secrets

- **A `secret://` value isn't reaching the container.** Generated files only ever
  carry the *key name* (the value is injected at runtime) — that's by design (no
  secret value is ever written to disk). Check the provider is declared in
  `workspace.yaml secrets.providers` and that its kind's backend is reachable.
- **SOPS+age:** ensure `SOPS_AGE_KEY_FILE` points at your key
  (`devstack secrets keygen -o <file>` generates one) and the `sops` binary is on
  PATH.

## Updates

- A new-release notice may print after a command. Suppress it with
  `DEVSTACK_NO_UPDATE_NOTIFIER=1`. The check is throttled (once/day) and never
  blocks or fails a command.
