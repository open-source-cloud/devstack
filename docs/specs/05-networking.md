# Spec 05 — Local HTTPS, reverse proxy & Cloudflare Tunnel

**Modules:** `internal/proxy`, `internal/trust`, `internal/dns`, `internal/tunnel` · **Milestone:** M5 · **Effort:** ~7w

## Purpose
Give every project service a stable local URL with working HTTPS at `https://<service>.<project>.localhost`, routed by a shared reverse proxy that needs no central config as projects come and go — and an *optional* public URL via Cloudflare Tunnel. **Local HTTPS is opt-in** so a broken trust path never blocks `up`.

## Decisions
- **Proxy = Caddy 2** via `lucaslorentz/caddy-docker-proxy` (pin image, e.g. `:2.12-alpine`), run as **one shared container** in the shared stack on `devstack_shared`. Routing is **label-driven**: project service templates emit `caddy=<svc>.<proj>.localhost`, `caddy.reverse_proxy={{upstreams <port>}}`, `caddy.tls=internal`. Adding/removing a project never touches central proxy config — caddy-docker-proxy reloads on the Docker event. Traefik/nginx stay pluggable behind a `Proxy` interface.
- **Single source of truth = a `[]Route{host, upstream, port, tls}`** that renders **both** the Caddy labels **and** the cloudflared ingress block, so local and public routing can never drift.
- **Local CA via `mkcert` ([Q-CA](../OPEN-QUESTIONS.md)):** shell out to the maintained `mkcert` binary to install the root CA into host + Firefox/NSS stores — **not** `smallstep/truststore` (unmaintained). `trust install|uninstall|status` commands. (If full pure-Go self-ownership is later required, generate the CA in `crypto/x509`, mount it as Caddy's internal-CA root, and vendor a trust-store installer — higher maintenance.)
- **Default domain `<service>.<project>.localhost`;** `.test` is opt-in behind a privileged `dns setup` (resolver/dnsmasq drop-in). For OS-resolver clients, generate idempotent **marker-fenced `/etc/hosts`** entries (sudo) — owned by `internal/dns`, removed on uninstall.
- **Tunnel = cloudflared** as an **optional** managed container, **locally-managed named tunnel** (credentials-file, config-as-code), ingress → the **Caddy** container (so public reuses local routing). `tunnel login|create|route|up|down`. **Default DOWN**, loud confirm, and **refuse to tunnel a service whose env carries non-local `secret://` values** without an override.
- **Orchestrate Caddy + cloudflared as containers** via the Docker SDK / shared compose — do **not** vendor them as Go libraries.

## Verified constraints / gotchas — the cross-platform reality
- **`*.localhost` is NOT uniformly zero-config:**
  - Linux: works only if **systemd-resolved stub is active** (not guaranteed on WSL2/minimal Ubuntu).
  - macOS: the OS resolver resolves `*.localhost` **only on macOS 26+**; on ≤15, non-browser clients (Go's net stack, curl, Docker healthchecks, the Caddy/tunnel origin lookups) and Safari **fail** without `/etc/hosts`.
  - Chrome/Firefox bypass the OS resolver (work everywhere) — but **Firefox ignores `/etc/hosts`** for `*.localhost`.
  - → **`/etc/hosts` is the only consistently reliable mechanism** for OS-resolver clients; treat macOS version + systemd-resolved presence as runtime-detected branches.
- **Caddy trust is NOT automatic in a container:** Caddy generates the cert fine, but installing its root into the host store fails from inside a container/unprivileged. Hence the explicit host-side `mkcert`/trust step. **Persist Caddy's `/data` volume** or the CA regenerates and trust breaks on every recreate. Internal-CA leaf certs are short-lived (fine for browsers; can surprise strict non-browser clients).
- **WSL2 dual trust store:** the CA must be imported into the **Windows** store (`certutil.exe -addstore -user Root`) via interop because browsers run on Windows, not in the distro. Windows-side Firefox has its own NSS. Corporate-managed Windows may block user-root CAs — document the manual fallback. Detect WSL via `uname -r` contains `microsoft` / WSLInterop.
- **`mkcert` needs platform tools at runtime** (`security` on macOS + sudo; `update-ca-certificates`/`trust` on Linux + sudo; `certutil` from `libnss3-tools` for Firefox — absent on clean Ubuntu/WSL2). `trust status` must diagnose and give the exact remediation.
- **Cloudflare Tunnel:** the **wildcard CNAME (`*.project`) must be created manually** in DNS (dashboard/API) — `cloudflared tunnel route dns` and the dashboard reject `*`. Only a single leading label is wildcardable. Free tier ≈ 1000 tunnels. Generate creds via `cloudflared tunnel create` (writes `<UUID>.json`) to avoid the 2025.2–2025.4 `tunnel login` regression. WSL2: runs as a normal Linux binary but QUIC/clock-skew can bite — offer the **http2 protocol fallback**.

## Acceptance criteria
- [ ] `up` with `httpsLocal: true` → `https://api.shop.localhost` loads in Chrome with a trusted cert after `trust install`.
- [ ] Adding a new project service → it gets a working URL with **no edit** to any central proxy config (label-driven reload).
- [ ] `trust status` on a clean Ubuntu correctly reports missing `certutil` and prints `apt install libnss3-tools`.
- [ ] On WSL2, `trust install` imports the CA into the Windows store via `certutil.exe` and Chrome-on-Windows trusts it.
- [ ] `tunnel up` is refused (with override hint) for a service whose env contains a non-local `secret://`; default state is DOWN.
- [ ] Local routing and tunnel ingress are generated from the same `[]Route` (no drift).
- [ ] `uninstall` removes the CA from **all** stores and the `/etc/hosts` marker block.

## Dependencies / consumers
The proxy/cloudflared containers live in the shared stack ([spec 03](03-workspaces-and-shared-services.md)); routes are emitted by project templates ([spec 02](02-templating-and-generation.md)); tunnel creds stored via secrets ([spec 04](04-secrets.md)). State (CA key/cert, tunnel JSON, cert.pem) lives `0600` under the XDG state dir, gitignored.

## Open questions
[Q-PROXY](../OPEN-QUESTIONS.md) (Caddy default — confirm), [Q-CA](../OPEN-QUESTIONS.md) (mkcert dependency acceptable?).
