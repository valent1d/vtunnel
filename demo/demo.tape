# vtunnel demo — a VHS tape (https://github.com/charmbracelet/vhs)
#
# One continuous dashboard session:
#   welcome → create a Classic tunnel (demo) → live requests → inspect a request
#   → add an OrbStack tunnel (dolibarr-v23) with Access OTP → two tunnels in the
#   list → remove both → end.
#
# It builds the CURRENT code (so the recording matches your working tree, not a
# stale brew install), starts the bundled demo server on :3000, and fires a few
# background requests once the tunnel is live — nothing manual to run.
#
# Prerequisites:
#   - vtunnel set up (a tunnel + default domain) and `cloudflared` running
#   - an HTTP-exposable OrbStack container (here: dolibarr-v23, subdomain doli23)
#   - go + brew's vtunnel on PATH; VHS:  brew install vhs
#
# TUNE BEFORE RECORDING: the domain (demo.valent1.xyz), the email, and the
# container selection (the `Down` in step 5 — adjust if your order differs).
#
# Render (from the repo root):  vhs demo/demo.tape   → writes site/demo.gif

Output site/demo.gif
# Output demo.mp4

Require vtunnel

Set Shell "bash"
Set FontSize 16
Set Width 1400
Set Height 800
Set Padding 24
Set TypingSpeed 110ms
Set Theme "Catppuccin Mocha"

# --- hidden setup: build current code, run the demo server, schedule requests ---
Hide
Type "go build -o /tmp/vtdemo/vtunnel ./cmd/vtunnel" Enter
Sleep 4s
Type 'export PATH="/tmp/vtdemo:$PATH"' Enter
Type "go build -o /tmp/vtdemo/demo-server ./demo/server && /tmp/vtdemo/demo-server &" Enter
Sleep 2.5s
# Fire 3 requests once demo.valent1.xyz is actually live (polls for HTTP 200).
Type '( until [ "$(curl -s -o /dev/null -w %{http_code} https://demo.valent1.xyz)" = "200" ]; do sleep 1; done; for p in /ping /webhook /healthz; do curl -s https://demo.valent1.xyz$p >/dev/null; sleep 1.2; done ) &' Enter
Type "clear" Enter
Show

# --- 1. welcome banner + useful commands ---
Type "vtunnel" Sleep 600ms Enter
Sleep 4s
Type "clear" Enter

# --- 2. open the dashboard (let OrbStack containers load) ---
Type "vtunnel http" Sleep 600ms Enter
Sleep 5s

# --- 3. create a Classic tunnel "demo" (Source is the first field; default Classic) ---
Type "n"
Sleep 1.5s
Tab
Type "3000"
Sleep 1s
Tab
Type "demo"
Sleep 1s
Tab 2
Sleep 800ms
Enter
Sleep 9s

# --- 4. inspect a request (focus is already on the log pane after create) ---
Up
Sleep 1.5s
Enter
Sleep 5s
Enter
Sleep 1.5s

# --- 5. add an OrbStack tunnel (2nd), protected with Access OTP ---
Type "n"
Sleep 1.5s
Right
Sleep 1.2s
Tab
Sleep 1s
# Container field: select dolibarr-v23 (the "doli23" container) — 2nd HTTP one here.
Down
Sleep 1.5s
Tab
Sleep 800ms
Backspace 16
Type "orbdoli23"
Sleep 1s
Tab 2
Right 2
Sleep 1s
Enter
Sleep 2s
# Access panel opens (OTP), focused on the allow field.
Type "valentin@valent1.dev"
Sleep 1.2s
Tab
Sleep 700ms
Enter
Sleep 6s

# --- 6. two tunnels in the list — dwell so the new one is readable ---
Tab
Sleep 2.5s
Up
Sleep 3.5s
Down
Sleep 4s

# --- 7. remove both tunnels (slowly), then end ---
Type "s"
Sleep 2s
Type "y"
Sleep 4.5s
Type "s"
Sleep 2s
Type "y"
Sleep 4.5s

# --- cleanup (hidden) ---
Hide
Type "kill %1 2>/dev/null" Enter
Show
