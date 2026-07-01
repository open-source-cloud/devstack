# Local HTTPS & DNS

[devstack](../../README.md) · [Guide](./README.md) › Local HTTPS & DNS

Give your services friendly, trusted URLs on your machine:

```
https://api.acme.localhost
https://web.acme.localhost
```

Three moving parts get you there:

1. **A reverse proxy** (`network.proxy` in `workspace.yaml`) that terminates TLS
   and routes `<service>.<project>.localhost` to the right container.
2. **DNS** — `dns setup` writes the `*.localhost` names into `/etc/hosts`.
3. **Trust** — `trust install` creates a local CA (mkcert) so the browser trusts
   the certs the proxy serves.

This page is about **local** reachability. To share a service on the public
internet, see [Public tunnels](tunnels.md).

## The proxy block

Enable the proxy in `workspace.yaml`:

```yaml
apiVersion: devstack/v1
kind: Workspace
name: acme
network:
  proxy:
    engine: caddy       # caddy | traefik | nginx
    httpsLocal: true    # terminate HTTPS locally with the mkcert CA
```

| Field | Values | Notes |
|---|---|---|
| `engine` | `caddy` \| `traefik` \| `nginx` | reverse-proxy engine devstack runs |
| `httpsLocal` | bool | serve HTTPS locally using the local CA (below) |

With the proxy on, `devstack up` routes each service that publishes an HTTP port
to `https://<service>.<project>.localhost`. The proxy joins the same
[`devstack_shared`](concepts.md) network and reaches containers by name — no host
ports required.

## DNS: `*.localhost` in `/etc/hosts`

`*.localhost` is **not** uniformly zero-config across OSes, so devstack writes the
names into `/etc/hosts` inside a marker-fenced block it owns.

```bash
devstack dns setup      # write the devstack-managed /etc/hosts block (needs sudo)
devstack dns status     # show which *.localhost entries are present/missing
devstack dns remove     # remove the devstack-managed block (needs sudo)
```

| Command | Needs sudo | Description |
|---|---|---|
| `dns setup` | yes | write/refresh the managed `/etc/hosts` block |
| `dns status` | no | report present vs missing entries |
| `dns remove` | yes | remove the managed block |

`setup` and `remove` edit a system file, so they need `sudo`; if the write is
denied you get a one-line sudo remediation. Only the fenced block is touched —
the rest of `/etc/hosts` is left alone.

## Trust: the local HTTPS CA (mkcert)

For the browser to trust local certs, install a local root CA into your system
and NSS (Firefox/Chrome) trust stores. devstack uses the external
[`mkcert`](https://github.com/FiloSottile/mkcert) binary.

```bash
devstack trust install     # create + trust the local root CA (needs sudo)
devstack trust status      # diagnose local-CA trust readiness
devstack trust uninstall   # remove the local root CA from trust stores (needs sudo)
```

| Command | Needs sudo | Description |
|---|---|---|
| `trust install` | yes | create the local root CA and add it to system/NSS trust stores |
| `trust status` | no | check whether the CA exists and is trusted |
| `trust uninstall` | yes | remove the local root CA from trust stores |

`trust status` is the first thing to run when a browser shows a certificate
warning on a `.localhost` URL.

## WSL2 caveats

On WSL2, the browser usually runs on **Windows** while the proxy runs in the
**Linux** distro — so trust and DNS have to be satisfied on *both* sides:

- **Trust:** `trust install` inside WSL2 trusts the CA for Linux tools. For a
  Windows browser to trust it too, install the same mkcert root CA into the
  Windows trust store (run mkcert on Windows against the same CA root, or import
  the root the Linux side generated).
- **DNS:** `dns setup` edits the Linux `/etc/hosts`. A Windows browser reads
  `C:\Windows\System32\drivers\etc\hosts` — add the `*.localhost` names there too
  if name resolution fails from Windows.
- Remember devstack keys its ledger by Docker context, so Docker Desktop vs an
  in-distro `dockerd` are tracked separately (see [Concepts](concepts.md)).

## Recipes

**Turn on local HTTPS end-to-end**

```bash
# 1. enable the proxy in workspace.yaml (engine: caddy, httpsLocal: true)
# 2. trust the local CA
devstack trust install
# 3. wire the hostnames
devstack dns setup
# 4. bring the workspace up — services now resolve at https://<svc>.<project>.localhost
devstack up
```

**Diagnose a cert / name failure**

```bash
devstack trust status     # is the CA present and trusted?
devstack dns status       # are the *.localhost entries in /etc/hosts?
```

**Tear the local wiring back down**

```bash
devstack dns remove
devstack trust uninstall
```

## See also

- [Public tunnels](tunnels.md) — expose a local service on the public internet (cloudflared).
- [Shared services & host access](shared-services.md) — host ports for GUI clients (`shared expose`).
- [Lifecycle](lifecycle.md) — `up`, `status`, `logs`, `dashboard`.
- [config-reference.md](config-reference.md) — the full `network` schema.

---

◀ [Secrets](secrets.md) · [Guide index](./README.md) · [Public tunnels ▶](tunnels.md)
