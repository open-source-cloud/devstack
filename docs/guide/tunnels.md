# Public tunnels

[devstack](../../README.md) · [Guide](./README.md) › Public tunnels

Sometimes local isn't enough — you need a **public URL** for a webhook, a demo,
or a phone on another network. devstack can expose a local service over a
[Cloudflare Tunnel](https://developers.cloudflare.com/cloudflare-one/connections/connect-networks/)
using the `cloudflared` daemon.

This is an **optional, account-gated, default-down** feature. Nothing is exposed
until you explicitly log in, create a tunnel, route a hostname, and bring it up.
For local-only HTTPS (no public exposure), use [Local HTTPS & DNS](networking.md)
instead.

```bash
devstack tunnel login                       # authenticate cloudflared (once)
devstack tunnel create demo                 # create a named tunnel + creds file
devstack tunnel route demo app.example.com  # map a hostname to it
devstack tunnel up                          # bring it up (detached by default)
# ...share https://app.example.com...
devstack tunnel down                        # stop it (creds + routes preserved)
```

## The five verbs

| Command | Args | Notes |
|---|---|---|
| `tunnel login` | none | authenticate `cloudflared` with your Cloudflare account |
| `tunnel create <name>` | tunnel name | create a named tunnel; **writes its credentials file** |
| `tunnel route <name> <hostname>` | name + hostname | route a **non-wildcard** hostname to the tunnel |
| `tunnel up [name]` | optional name | bring the managed tunnel up (default is **down**) |
| `tunnel down` | none | stop the tunnel; **credentials and routes are preserved** |

`tunnel up [name]` flags:

| Flag | Default | Description |
|---|---|---|
| `--detach` | **true** | run the tunnel container detached (background) |
| `--allow-secrets` | false | override the refusal to tunnel services carrying non-local `secret://` values |

## The `network.tunnel` block

Declare the tunnel's provider and default hostname in `workspace.yaml`:

```yaml
apiVersion: devstack/v1
kind: Workspace
name: acme
network:
  tunnel:
    provider: cloudflared
    hostname: app.example.com
```

| Field | Notes |
|---|---|
| `provider` | the tunnel provider (`cloudflared`) |
| `hostname` | the public hostname routed to the tunnel |

## Safety: what a tunnel refuses to do

A public tunnel is a sharp tool, so devstack adds two guardrails:

- **Refuses secret-bearing services by default.** If a service you would expose
  carries a **non-local** `secret://` value, `tunnel up` refuses — publishing it
  could leak a real secret to the public internet. Pass `--allow-secrets` to
  override deliberately, once you're sure the exposure is intended. See
  [Secrets](secrets.md) for what counts as a secret.
- **Non-wildcard hostnames only.** `tunnel route` accepts a concrete hostname
  (`app.example.com`), not a wildcard (`*.example.com`). Each route is explicit.

`tunnel down` stops the daemon but **keeps the credentials file and the routes**,
so bringing it back up later is a single `tunnel up`.

## End-to-end: share a local app publicly

Starting from a running workspace with an `api` service on port 8080:

```bash
# 1. Authenticate cloudflared with your Cloudflare account (opens a browser).
devstack tunnel login

# 2. Create a named tunnel. This writes the tunnel's credentials file.
devstack tunnel create demo

# 3. Route a concrete (non-wildcard) hostname to the tunnel.
#    The hostname must be one you control in Cloudflare DNS.
devstack tunnel route demo api.example.com

# 4. Bring the tunnel up. It runs detached by default.
#    If `api` carries non-local secret:// values, add --allow-secrets.
devstack tunnel up demo

# 5. Your app is now reachable at https://api.example.com from anywhere.
#    Test a webhook, share the demo link, etc.

# 6. When done, stop the tunnel. Credentials + routes are preserved,
#    so `tunnel up` later reuses them.
devstack tunnel down
```

You can also set the hostname once in `workspace.yaml` under `network.tunnel`
and skip repeating it — `tunnel up` (no name) uses the configured tunnel.

## Recipes

**Expose a service that carries a secret (deliberate)**

```bash
devstack tunnel up demo --allow-secrets
```

**Run the tunnel in the foreground (watch its logs live)**

```bash
devstack tunnel up demo --detach=false
```

**Pause exposure without losing the setup**

```bash
devstack tunnel down       # stop; creds + routes stay on disk
devstack tunnel up demo    # resume later — no re-create, no re-route
```

## Troubleshooting

| Symptom | Likely cause / fix |
|---|---|
| `tunnel up` refuses | a target service carries a non-local `secret://` value — confirm it's safe, then re-run with `--allow-secrets` |
| `tunnel route` rejected | you passed a wildcard hostname — use a concrete host like `api.example.com` |
| No public URL after `up` | check the tunnel is actually up (it's detached by default); re-run with `--detach=false` to watch logs |
| `login` didn't stick | re-run `tunnel login`; `create`/`route`/`up` all require an authenticated `cloudflared` |
| Hostname doesn't resolve | the hostname must exist in your Cloudflare DNS and point at the tunnel |

## See also

- [Local HTTPS & DNS](networking.md) — local `*.localhost` URLs (no public exposure).
- [Secrets](secrets.md) — what makes `tunnel up` refuse a service, and `--allow-secrets`.
- [Lifecycle](lifecycle.md) — bring services up before tunnelling them.
- [command-reference.md](command-reference.md) — every `tunnel` verb, terse.

---

◀ [Local HTTPS & DNS](networking.md) · [Guide index](./README.md) · [Recovery, teardown & housekeeping ▶](recovery.md)
