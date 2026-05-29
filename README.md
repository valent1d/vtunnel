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
| `vtunnel status` | Show daemon and config status |
| `vtunnel service install` | Start vtunnel automatically at login (macOS) |
| `vtunnel --help` | Show every command and flag |

### Exposing apps

```bash
vtunnel http 3000 dev                  # → https://dev.<default-domain>
vtunnel http 5173 app --domain example.dev   # pick a specific domain
vtunnel http 3000 dev --detach         # create the route and return (no log tail)
```

### The dashboard

```bash
vtunnel http        # no port → open the TUI dashboard
```

The dashboard lists your active routes and streams incoming requests so you can inspect traffic without leaving the terminal.

### Logs

```bash
vtunnel logs              # all routes
vtunnel logs dev          # one route
vtunnel logs dev --follow # live tail
vtunnel logs --limit 100  # more history
```

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
