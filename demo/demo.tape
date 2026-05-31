# vtunnel demo — a VHS tape (https://github.com/charmbracelet/vhs)
#
# Records the real end-to-end flow against YOUR configured vtunnel:
#   expose a local port → public URL → real request → live dashboard.
#
# Prerequisites (on your machine):
#   - vtunnel set up (a tunnel + default domain) and `cloudflared` running
#   - vtunnel on PATH:  go build -o /usr/local/bin/vtunnel ./cmd/vtunnel
#   - VHS:  brew install vhs        (pulls ffmpeg + ttyd)
#   - python3 (for the throwaway local server) — swap for any local server you like
#
# BEFORE RECORDING: replace example.com below with your vtunnel domain.
#
# Render:  vhs demo/demo.tape      → writes site/demo.gif (+ demo.mp4)
# Refresh: just run it again — the tape is the source of truth.

Output site/demo.gif
Output demo.mp4

Require vtunnel

Set Shell "bash"
Set FontSize 18
Set Width 1100
Set Height 680
Set Padding 26
Set TypingSpeed 55ms
Set Theme "Catppuccin Mocha"

# Throwaway local app on :3000 so the tunnel has something to serve.
Hide
Type "python3 -m http.server 3000 >/dev/null 2>&1 &" Enter
Type "clear" Enter
Show

# 1. Expose a local port — prints the public URL and returns (--detach).
Type "vtunnel http 3000 demo --detach" Sleep 500ms Enter
Sleep 3s

# 2. The URL is public immediately — hit it from anywhere.
Type "curl -s https://demo.example.com | head -n 6" Sleep 500ms Enter
Sleep 3s

# 3. Open the dashboard — the request you just made shows up live.
Type "vtunnel http" Sleep 500ms Enter
Sleep 4.5s
Type "q"
Sleep 1s

# Cleanup (hidden): remove the route and stop the local server.
Hide
Type "vtunnel stop demo >/dev/null 2>&1; kill %1 2>/dev/null" Enter
Show
