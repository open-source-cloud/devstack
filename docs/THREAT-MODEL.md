# devstack — Threat model & security posture

This document states what `devstack` defends against, what it explicitly does
**not**, and the invariants the implementation upholds. It is a companion to
[ARCHITECTURE.md](ARCHITECTURE.md) (§7.3 teardown, §7.5 secrets) and
[DECISIONS.md](DECISIONS.md).

devstack is a **local developer tool**: it runs on a trusted workstation, drives a
Docker daemon the user already controls, and shares infrastructure across that
user's own repos. It is **not** a multi-tenant service and does not isolate
mutually-distrusting users on one machine.

## Assets

| Asset | Where it lives | Sensitivity |
|---|---|---|
| Secret values (`secret://…`) | provider (SOPS/age, AWS, Infisical…), resolved in-memory at `up`, injected via the child process env | **high** — never written to a generated file or the ledger |
| Provider credentials | OS keyring (`secrets login`) or `DEVSTACK_<PROVIDER>_TOKEN` env | high |
| Local root CA private key | mkcert's `CAROOT` (mkcert-owned) | high — can mint trusted certs |
| The state ledger (SQLite) | `$XDG_DATA_HOME/devstack/state.db`, keyed by Docker context | low — ref counts, port allocations, provisioning records; a cache of reality, rebuildable |
| Shared data volumes | Docker named volumes (Postgres/Redis/MinIO) | medium — dev data |
| Committed config | `workspace.yaml` + `devstack.yaml` in the repo | **must contain zero secrets** |

## Trust boundaries

- **The developer + their shell** — fully trusted. devstack inherits the user's
  Docker context, git SSH/credential helpers, and sudo.
- **The Docker daemon** — trusted. devstack drives it; a compromised daemon is
  out of scope (it already runs the user's containers).
- **Project containers on `devstack_shared`** — semi-trusted. They share one
  flat bridge network and can reach each other and the shared services by DNS
  alias. devstack does **not** firewall containers from one another (see
  Non-goals).
- **Secret providers & the network to them** — trusted to return correct values;
  devstack authenticates with the user's own credentials.

## Security invariants (asserted by tests where marked ✓)

1. **No secret value ever touches disk.** Generated compose files reference each
   secret as a *valueless* `environment: [NAME]` key; the resolved value is
   passed only through the compose-up child process env (`exec.Cmd.Env`). A CI
   leak test asserts no secret value appears in any generated artifact. ✓
   ([spec 04](specs/04-secrets.md), ARCHITECTURE §7.5)
2. **Secrets are not logged.** Resolution is batched in-memory; `secrets.Redact`
   scrubs known values from diagnostics. Provider credentials are never echoed.
3. **The committed config is secret-free.** Secrets are references, not literals;
   provider auth lives in the keyring/env, never in `workspace.yaml`.
4. **Credential resolution order is explicit:** `DEVSTACK_<PROVIDER>_TOKEN` env →
   the managed OS-keyring entry → the provider's native default (AWS shared
   config, `SOPS_AGE_KEY_FILE`, …). On a host without a Secret Service (common on
   WSL2), `secrets login` **degrades to env-var mode with a warning** rather than
   silently storing nothing. ✓ ([spec 04](specs/04-secrets.md))
5. **The keyring entry is namespaced** under the `devstack` service and addressed
   per provider; `secrets logout` / `uninstall` remove it.
6. **Single-writer mutation.** Every change to the ledger or the shared stack is
   serialized by a coarse `flock` advisory lock (`internal/lock`); reads are
   lock-free snapshots. This prevents TOCTOU races on port allocation,
   network-ensure, ref counts, and `CREATE ROLE`. ✓ ([spec 08](specs/08-state-locking-and-lifecycle.md))
7. **Teardown is complete and confirmed.** `uninstall` removes the external
   network, all stacks **and their volumes**, the marker-fenced `/etc/hosts`
   block, alias symlinks, the XDG ledger/cache/config, **and the root CA from
   every trust store** (host + Firefox/NSS + Windows via `certutil.exe` on WSL2).
   Any store it cannot clear is reported loudly — *a CA left in a trust store is a
   security defect*. Data loss requires explicit confirmation (`--yes`).
   ([spec 13](specs/13-doctor-diagnostics-and-teardown.md))
8. **Generation is deterministic** — byte-identical output for identical input
   (CI-asserted), so a generated artifact can be reviewed and trusted; nothing
   nondeterministic (timestamps, random ports) is baked into a committed-looking
   file.

## Specific risks & mitigations

- **Port exposure.** Default posture is **no published host ports** — shared
  services are reachable only over the tool-owned Docker network by DNS alias.
  Host ports are allocated from the ledger, inside the lock, only on an explicit
  host-reachability request. This minimizes the surface bound to `0.0.0.0`/
  `127.0.0.1`.
- **The local CA.** mkcert installs a root CA trusted by the OS/browsers — a real
  power. devstack treats `trust install` as opt-in and sudo-gated, and makes
  `uninstall` remove the CA from every store. Compromise of the CA private key
  (mkcert-owned) lets an attacker mint trusted certs; protect `CAROOT` as you
  would an SSH key.
- **`/etc/hosts` edits** are marker-fenced and idempotent; devstack only ever
  edits inside its own fence, never the rest of the file.
- **Tunnels (`cloudflared`)** can publish a local service to the public internet.
  devstack refuses wildcard ingress, refuses to route a service that carries a
  non-local secret, and never stores the Cloudflare account credential itself.
- **External binaries** (docker, git, mkcert, sops, cloudflared) are invoked by
  name and inherit the user's `PATH`; devstack does not pin their hashes. A
  compromised `PATH` is out of scope (it compromises the whole shell).
- **Supply chain.** Every dependency is pure-Go (`CGO_ENABLED=0` static binary),
  pinned in `go.mod`, and scanned by `govulncheck` in CI. Risky/fast-moving deps
  sit behind `internal/` interfaces so they can be swapped or vendored.

## Non-goals (explicitly out of scope)

- **Multi-tenant / mutually-distrusting users on one machine.** devstack assumes
  one trusted developer.
- **Container-to-container isolation.** All stacks share one flat bridge network
  by design (that *is* the shared-infra feature); a malicious container can reach
  its neighbors. Do not run untrusted images on `devstack_shared`.
- **Secret encryption at rest beyond the provider.** devstack resolves secrets;
  it does not re-encrypt or vault them. The provider (SOPS/age, AWS SM, Infisical)
  owns at-rest protection.
- **Hardening the Docker daemon or the host OS.**
- **Network egress filtering.**

## Reporting

This is pre-1.0 software. Report security issues privately to the maintainers
rather than via public issues until a disclosure policy is published.
