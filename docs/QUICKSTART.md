# devstack — Quickstart

`devstack` runs one warm set of shared infrastructure (Postgres/Redis/MinIO) on a
tool-owned Docker network and lets many project stacks attach to it — instead of a
duplicate database per repo. This guide covers the commands that work today.

> **Prerequisites:** Docker Engine + Compose v2.20+, git ≥ 2.30. Run
> `devstack doctor` first — it probes everything and prints a one-line fix for
> anything missing.

## 1. Lay out a workspace

A workspace is one `workspace.yaml` (the shared layer) plus a `devstack.yaml` per
repo (the portable project layer):

```yaml
# workspace.yaml
apiVersion: devstack/v1
kind: Workspace
name: acme
shared:
  postgres: { template: postgres, params: { version: "16" } }
  redis:    { template: redis }
projects:
  - { name: api, path: services/api }
```

```yaml
# services/api/devstack.yaml
apiVersion: devstack/v1
kind: Project
name: api
services:
  api:
    template: php.laravel.nginx
    uses: [workspace.shared.postgres, workspace.shared.redis]
    healthcheck: { kind: http, port: 8080, path: /healthz }
    env:
      import:
        - { from: workspace.shared.postgres, vars: [host, port, user, password, database] }
```

## 2. Bring it up

```bash
devstack up
```

One idempotent command runs the saga: **preflight → network → generate →
shared (health-gated) → compose-up → hooks**. The shared services start once and
are gated healthy *before* your project starts. Re-running `up` is near-instant —
satisfied phases are skipped (resumable via the state ledger); a crash mid-run
resumes; a failure compensates (drops ref rows / downs the project) but **never
destroys data** (your DB volumes survive).

```bash
devstack up --json          # machine-readable phase records
devstack up api             # only the named project(s) + the shared services they use
devstack up --build         # rebuild images first
devstack up --no-hooks      # skip lifecycle hooks
```

## 3. Inspect and tear down

```bash
devstack status             # per-project service health + last saga outcome + shared ref graph
devstack shared status      # shared services, ref counts, consuming projects
devstack down               # stop this workspace's project stacks, release their refs
devstack shared gc          # report shared services at zero refs (--stop to actually stop them)
```

`down` leaves the shared services running by default (warm DBs are cheap). The
external network and volumes are never touched by `down`.

## 4. Recovery

The state ledger is a *cache of reality*, never the source of truth:

```bash
devstack shared doctor            # reconcile: prune ref rows for projects no longer live
devstack doctor --rebuild-state   # reconstruct the ledger from live container labels + config
```

## 5. Local HTTPS, DNS, and secrets (opt-in)

```bash
# Reverse proxy: set network.proxy.engine: caddy in workspace.yaml, then:
sudo devstack dns setup           # marker-fenced /etc/hosts for *.localhost
sudo devstack trust install       # install the local CA (mkcert)
devstack trust status             # diagnose CA / certutil / WSL2 readiness

# Offline secrets with SOPS+age:
devstack secrets keygen -o ~/.config/devstack/age.txt   # generate an age key
export SOPS_AGE_KEY_FILE=~/.config/devstack/age.txt
# reference secrets in devstack.yaml env as secret://<provider>/<file>#<key>
```

See [TROUBLESHOOTING.md](TROUBLESHOOTING.md) when something doesn't work, and
[ARCHITECTURE.md](ARCHITECTURE.md) for the design.
