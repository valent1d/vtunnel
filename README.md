# vtunnel

`vtunnel` is a local developer tool that aims to provide an ngrok-like workflow on top of Cloudflare Tunnel.

The core idea is simple:

1. Configure Cloudflare and `cloudflared` once with wildcard ingress rules.
2. Let `cloudflared` forward wildcard traffic to a local `vtunnel` proxy.
3. Let `vtunnel` dynamically route each public hostname to a local port.

```txt
Internet
  -> Cloudflare wildcard DNS
  -> Cloudflare Tunnel
  -> cloudflared
  -> vtunnel local proxy
  -> localhost:<project-port>
```

The goal is not to replace `cloudflared`. The goal is to make the daily local-development workflow pleasant while still relying on Cloudflare Tunnel for traffic.

## Current Status

This is an early MVP. It currently supports:

- local config under `~/.config/vtunnel/`
- a local proxy on `127.0.0.1:8787`
- a local API on `127.0.0.1:8788`
- dynamic hostname-to-local-port routes
- request logs
- a basic TUI dashboard
- a guided `vtunnel onboarding` first-run wizard
- `cloudflared` config diagnostics and local ingress repair
- optional Cloudflare API discovery via a token stored in macOS Keychain
- wildcard DNS repair through the Cloudflare API or `cloudflared`
- release metadata through `vtunnel --version`

Homebrew packaging is being prepared. Service installation is still future work.

## Development

Requirements:

- Go
- optionally `cloudflared`

Run tests:

```bash
go test ./...
```

Build:

```bash
make build VERSION=v0.1.0
./bin/vtunnel --version
```

Run from source:

```bash
go run ./cmd/vtunnel --help
```

Create local release artifacts:

```bash
scripts/release.sh 0.1.0
```

This generates macOS archives and checksums under `dist/`. Tagged GitHub releases are handled by `.github/workflows/release.yml`.

See `docs/release.md` for the Homebrew formula flow, including private repository caveats.

## Setup

For guided first-run setup:

```bash
vtunnel onboarding
```

The wizard checks local dependencies, Cloudflare auth, domains, tunnels, wildcard DNS, local `cloudflared` config, and runtime processes before ending with a ready summary.

Add one or more domains to the local vtunnel config:

```bash
vtunnel setup --domain example.com
```

By default, `setup` runs diagnostics and prints a dry-run plan for local `cloudflared` config changes.

Connect native `cloudflared` account credentials:

```bash
vtunnel cloudflared login
vtunnel cloudflared status
```

This uses `cloudflared tunnel login` and stores Cloudflare's `cert.pem` in `~/.cloudflared/`.

To write missing or incorrect local `cloudflared` ingress rules:

```bash
vtunnel setup --domain example.com --write-cloudflared
```

When writing, vtunnel creates a timestamped backup of the existing `cloudflared` config first.

To create and configure a replacement tunnel when the configured tunnel no longer exists:

```bash
vtunnel setup --fix-tunnel
```

To repair wildcard DNS records so they point at the configured tunnel:

```bash
vtunnel setup --fix-dns
```

When possible, vtunnel uses the Cloudflare API token stored in macOS Keychain. If no token is available, it can fall back to `cloudflared tunnel route dns --overwrite-dns`.

To start `cloudflared` with the configured tunnel config:

```bash
vtunnel setup --start-cloudflared
```

The process is started in the background and logs to `~/.config/vtunnel/logs/cloudflared.log`.

Expected `cloudflared` ingress shape:

```yaml
ingress:
  - hostname: "*.example.com"
    service: http://127.0.0.1:8787
  - service: http_status:404
```

## Usage

Expose a local service:

```bash
vtunnel http 3000 dev
```

This starts the local vtunnel daemon and, when the setup is ready, starts `cloudflared` automatically if it is not already running with the vtunnel config.

With a specific domain:

```bash
vtunnel http 5173 app --domain example.dev
```

Run detached instead of following request logs:

```bash
vtunnel http 3000 dev --detach
```

List routes:

```bash
vtunnel list
```

Stop a route:

```bash
vtunnel stop dev
```

Show request logs:

```bash
vtunnel logs
vtunnel logs dev
vtunnel logs dev --follow
```

Open the TUI dashboard:

```bash
vtunnel
```

Daemon controls:

```bash
vtunnel daemon stop
vtunnel daemon restart
```

Optional Cloudflare API discovery:

```bash
vtunnel cloudflare auth
vtunnel cloudflare status
```

`cloudflare auth` opens a Cloudflare token template URL with vtunnel permissions pre-filled, then stores the token in macOS Keychain.

This verifies the token and lists accessible accounts, zones, and wildcard DNS records. Tunnels are discovered through `cloudflared` and `cert.pem`, not the Cloudflare API token.

## Config

Default paths:

```txt
~/.config/vtunnel/config.yml
~/.config/vtunnel/routes.json
~/.config/vtunnel/logs/requests.jsonl
```

Example config:

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

The API must stay bound to `127.0.0.1`.
