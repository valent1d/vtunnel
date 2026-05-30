<div align="center">

<img src=".github/vtunnel-logo.svg" alt="vtunnel" width="440">

**Expose your local apps on your own Cloudflare domain — with a polished CLI & TUI.**

[![Go](https://img.shields.io/badge/Go-1.26-00ADD8?logo=go&logoColor=white)](https://go.dev)
[![Built with Charm](https://img.shields.io/badge/Built_with-Charm-FF1493)](https://charm.sh)
[![Release](https://img.shields.io/github/v/release/valent1d/vtunnel?include_prereleases&label=release&color=42A5F5)](https://github.com/valent1d/vtunnel/releases)

```bash
vtunnel http 3000 dev   →   https://dev.example.com
```

</div>

---

`vtunnel` turns Cloudflare Tunnel into a pleasant daily local-dev experience. Point a wildcard domain at a tunnel once, then expose any local port at a clean public URL in one command — no random subdomains, no per-seat pricing, no re-configuring Cloudflare every time.

A guided wizard sets everything up, and a built-in TUI dashboard lets you watch requests as they come in.

> **Status: beta — actively developed.** The daily workflow (`onboarding`, `http`, `list`, `logs`) is solid. Expect rough edges in less-traveled paths, and a few features (services, Keychain) are macOS-only for now.

## How it works

`vtunnel` doesn't replace `cloudflared` — it sits in front of it and makes the local side enjoyable.

```txt
Internet
  → Cloudflare wildcard DNS   (*.example.com)
  → Cloudflare Tunnel
  → cloudflared
  → vtunnel local proxy       (127.0.0.1:8787)
  → localhost:<your app port>
```

Cloudflare and `cloudflared` are configured **once** with a wildcard ingress rule. After that, exposing an app is just a local route change — `vtunnel` dynamically maps each public hostname to a local port. Your Cloudflare setup never gets touched again on a daily basis.

## Quick start

**1. Install** (macOS, via the Homebrew tap):

```bash
brew install valent1d/vtunnel/vtunnel
```

**2. Run the guided setup:**

```bash
vtunnel onboarding
```

The wizard walks through 11 steps — local dependencies, Cloudflare auth, domains, tunnel, wildcard DNS, local `cloudflared` config, and runtime processes — fixing what it can along the way and ending with a ready summary.

**3. Expose a local app:**

```bash
vtunnel http 3000 dev
```

That starts the local daemon, ensures `cloudflared` is running, and serves `localhost:3000` at `https://dev.<your-domain>` while following its request logs live.

## Daily commands

| Command | What it does |
| --- | --- |
| `vtunnel http 3000 dev` | Expose `localhost:3000` at `dev.<domain>` |
| `vtunnel http` | Open the request dashboard (TUI) |
| `vtunnel list` | List active routes |
| `vtunnel logs dev` | Show request logs (`-f` to follow) |
| `vtunnel stop dev` | Remove a route |
| `vtunnel orbstack` | Expose an OrbStack container (interactive picker) |
| `vtunnel status` | Show daemon and config status |
| `vtunnel service install` | Start vtunnel automatically at login (macOS) |
| `vtunnel uninstall` | Remove services, config, and (optionally) Cloudflare resources |
| `vtunnel --help` | Show every command and flag |

### Exposing apps

```bash
vtunnel http 3000 dev                  # → https://dev.<default-domain>
vtunnel http 5173 app --domain example.dev   # pick a specific domain
vtunnel http 3000 dev --detach         # create the route and return (no log tail)
```

The first argument can also be a full URL or `host:port`, so you can forward to any HTTP upstream — another machine, a VM, a NAS — not just a local port:

```bash
vtunnel http http://192.168.1.10:8080 nas    # forward to any host
vtunnel http 3000 dev --target http://web.local:8080   # or via --target
```

### OrbStack containers

If you use [OrbStack](https://orbstack.dev), `vtunnel` exposes Docker containers directly. Each container is reachable on the host at its `<name>.orb.local` domain, and `vtunnel` simply forwards to it.

```bash
vtunnel orbstack                       # interactive picker → choose, name, expose
vtunnel orbstack list                  # list running containers
vtunnel orbstack expose dolibarr-v23   # expose a container
vtunnel orbstack expose dolibarr-v23 app1   # …with your own subdomain
vtunnel orbstack watch                 # auto-expose containers as they come and go
```

The subdomain defaults to the container's custom domain (the `dev.orbstack.domains` label) or its name. Exposing opens the dashboard focused on the new route, where OrbStack-backed tunnels carry a `⬡` badge and a compact OrbStack detail card.

`vtunnel orbstack watch` keeps your routes in sync with running containers: every HTTP container gets a route, and routes are removed when their container stops. It only manages routes it created — manual ones are left untouched.

### The dashboard

```bash
vtunnel http        # no port → open the TUI dashboard
```

The dashboard lists your active routes and streams incoming requests so you can inspect traffic without leaving the terminal.

Press `enter` on a request to open its detail: full request and response **headers and body** (captured up to 64 KiB each). From there, press `r` to **replay** the request against your local app — handy for iterating on webhooks (Stripe, GitHub, …) without re-triggering the sender.

### Logs

```bash
vtunnel logs              # all routes
vtunnel logs dev          # one route
vtunnel logs dev --follow # live tail
vtunnel logs --limit 100  # more history
```

## Protect routes with Cloudflare Access

Exposed routes are public by default. [Cloudflare Access](https://www.cloudflare.com/zero-trust/products/access/) (Zero Trust) puts a login page at the **edge** — before traffic reaches your local app — so only the people you allow get in. It's free for up to 50 users.

```bash
vtunnel access setup                 # guided one-time Zero Trust enablement
vtunnel cloudflare auth --access     # grant vtunnel an Access-write token
vtunnel access status                # Zero Trust state, identity providers, protected routes

vtunnel http 8080 admin --protect --allow you@example.com        # email one-time PIN
vtunnel http 8080 admin --protect=sso --idp "Authentik"          # SSO (allow optional)
vtunnel access protect admin --allow @example.com                # protect an existing route
vtunnel access pause admin           # temporarily public (keeps config) · resume to re-enable
vtunnel access unprotect admin                                   # back to public

vtunnel access idp list                                          # identity providers
vtunnel access idp add authentik --issuer https://auth.example.com/application/o/cf/ \
  --client-id … --client-secret …                                # OIDC (endpoints auto-discovered)
vtunnel access idp add google --apps-domain example.com --client-id … --client-secret …
```

You can also manage all of this **from the HTTP dashboard**: press `a` on a route to open the Access panel (choose Public / SSO / OTP, pick the identity provider, set who's allowed, pause/resume), and the "new tunnel" form has a Protect step so you can secure a route as you create it.

- **`--protect` / `--protect=otp`** — email one-time PIN (no identity provider needed). `--allow` is required (an email or `@domain`). For a single-person route, just allow one email.
- **`--protect=sso`** — use an identity provider (`--idp <name>`); Authentik (OIDC) and Google Workspace are supported. `--allow` is optional — leave it out to allow anyone who signs in through that provider, or narrow with `@domain`.

Protection is created **before** the route is published (the hostname is never briefly public), and rolled back if publishing fails. Protected routes show a 🔒 badge in the dashboard. If you must enable Zero Trust first, `vtunnel access status` walks you through the one-time dashboard step.

> First-time Zero Trust activation (choosing a team name and plan) is done once in the Cloudflare dashboard — Cloudflare requires a card even on the free plan, and you are not charged.

## TCP tunnels (databases, SSH, …)

vtunnel can also expose **TCP** services (Postgres, MySQL/MariaDB, SSH, Redis…) through the same Cloudflare Tunnel.

> **Important — TCP is not zero-install like HTTP.** A browser speaks HTTP, so HTTP routes work for anyone with the link. A database/SSH client speaks a raw protocol, and Cloudflare's edge only serves HTTP on its web ports — there is **no public `db.example.com:5432` to dial** on the free plan. So the **connecting machine** must wrap the raw TCP into the tunnel with `cloudflared` (that's what `vtunnel tcp connect` does). Use TCP tunnels to reach your own service from another of your machines, or to give a teammate who can install `cloudflared` access — not for anonymous, install-free access.

**On the machine that has the service** (and runs the tunnel):

```bash
vtunnel tcp 5432 db                   # expose localhost:5432 at db.<domain>
vtunnel tcp 192.168.1.10:22 ssh       # …or any host:port
vtunnel orbstack expose doli-db --tcp # an OrbStack container's port (e.g. MariaDB)
vtunnel tcp list                      # list TCP tunnels
vtunnel tcp rm db                     # remove one
```

Adding or removing a TCP tunnel **restarts cloudflared** to apply the change, which briefly reconnects all tunnels (HTTP included).

**On the machine that wants to connect** (needs `cloudflared` installed):

```bash
vtunnel tcp connect db --port 5432    # opens 127.0.0.1:5432, keep it running
psql -h 127.0.0.1 -p 5432 …           # then point your client at the local port
```

(`vtunnel tcp connect` wraps `cloudflared access tcp`; `--port` defaults to the service's port.)

## One-time setup

Most people just run `vtunnel onboarding` and never touch this. If you'd rather drive setup yourself, `vtunnel setup` runs the same diagnostics and applies fixes with explicit flags (it prints a dry-run plan by default).

Add one or more domains:

```bash
vtunnel setup --domain example.com
```

Connect native `cloudflared` credentials (creates `~/.cloudflared/cert.pem`):

```bash
vtunnel cloudflared login
vtunnel cloudflared status
```

Apply the local `cloudflared` ingress rules (a timestamped backup is created first):

```bash
vtunnel setup --domain example.com --write-cloudflared
```

Repair a missing tunnel or wildcard DNS:

```bash
vtunnel setup --fix-tunnel    # recreate the tunnel and update local config
vtunnel setup --fix-dns       # point wildcard DNS at the tunnel
```

Start `cloudflared` with the configured tunnel:

```bash
vtunnel setup --start-cloudflared
```

The expected `cloudflared` ingress shape `vtunnel` manages:

```yaml
ingress:
  - hostname: "*.example.com"
    service: http://127.0.0.1:8787
  - service: http_status:404
```

### Optional: Cloudflare API access

For safer DNS verification and repair, `vtunnel` can use a Cloudflare API token:

```bash
vtunnel cloudflare auth     # opens a pre-filled token template, then stores it
vtunnel cloudflare status   # read-only discovery of accounts, zones, DNS
```

The token is stored in the macOS Keychain and is entirely optional — tunnels are discovered through `cloudflared` and `cert.pem`, not the API token. DNS repair falls back to `cloudflared tunnel route dns` when no token is present.

## Run at login (macOS)

Install `vtunnel` and `cloudflared` as user LaunchAgents so they start automatically:

```bash
vtunnel service install
vtunnel service status
```

This writes LaunchAgents (`sh.vltn.vtunnel.daemon`, `sh.vltn.vtunnel.cloudflared`) under `~/Library/LaunchAgents/` and manages them with `launchctl`. They restart when you log in.

```bash
vtunnel service start
vtunnel service stop
vtunnel service uninstall
```

You can also drive the daemon directly without services:

```bash
vtunnel daemon stop
vtunnel daemon restart
```

## Uninstall

`vtunnel uninstall` tears down everything it installed — LaunchAgents, the local config directory, and the Keychain token — showing a plan and asking before it removes anything:

```bash
vtunnel uninstall              # interactive; prints a plan, then confirms
vtunnel uninstall --dry-run    # preview what would be removed, change nothing
vtunnel uninstall --keep-config   # remove services but keep your config + token
vtunnel uninstall --cloudflare    # also delete the tunnel and wildcard DNS records
```

Your Cloudflare account is left untouched by default — pass `--cloudflare` to also delete the tunnel, the wildcard DNS, and any Access apps vtunnel created. The binary itself is removed separately with `brew uninstall vtunnel`. (Stopping a protected route with `vtunnel stop` also removes its Access app.)

## Configuration

Default paths:

```txt
~/.config/vtunnel/config.yml
~/.config/vtunnel/routes.json
~/.config/vtunnel/logs/requests.jsonl
```

Example `config.yml`:

```yaml
default_domain: example.com
domains:
  - example.com

proxy:
  listen: 127.0.0.1:8787

api:
  listen: 127.0.0.1:8788

cloudflared:
  tunnel_name: example
  config_path: ~/.cloudflared/config.yml
```

For safety, the local proxy and API must stay bound to `127.0.0.1`.

## Platform support

`vtunnel` is built and tested on **macOS**: installation (Homebrew tap), background services (LaunchAgents), and token storage (Keychain) target macOS first. The core — the proxy, daemon, routing, and request logs — is portable, so Linux works for the daily flow, but service installation and Keychain integration are macOS-only for now.

## Built with

- **[Go](https://go.dev)** — a single static binary, no runtime to install
- **[Cobra](https://github.com/spf13/cobra)** — command-line framework
- **[Bubble Tea](https://github.com/charmbracelet/bubbletea)**, **[Lip Gloss](https://github.com/charmbracelet/lipgloss)** & **[Bubbles](https://github.com/charmbracelet/bubbles)** — the [Charm](https://charm.sh) stack behind the onboarding wizard and TUI dashboard

---

<div align="center">
<sub>vtunnel by <a href="https://vltn.sh">vltn.sh</a></sub>
</div>
