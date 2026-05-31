#!/bin/sh
# vtunnel installer — downloads the latest release binary for your OS/arch,
# verifies its checksum, and installs it. Mirrors the Homebrew flow for Linux
# and for macOS without Homebrew.
#
#   curl -fsSL https://raw.githubusercontent.com/valent1d/vtunnel/main/install.sh | sh
#
# Environment overrides:
#   VTUNNEL_VERSION       install a specific tag (default: latest release)
#   VTUNNEL_INSTALL_DIR   install directory (default: /usr/local/bin if writable, else ~/.local/bin)

set -eu

REPO="valent1d/vtunnel"

# --- colors (only when stdout is a TTY and NO_COLOR is unset) ---
if [ -t 1 ] && [ -z "${NO_COLOR:-}" ]; then
	GREEN="$(printf '\033[1;38;5;48m')"
	CYAN="$(printf '\033[38;5;81m')"
	GREY="$(printf '\033[38;5;245m')"
	RED="$(printf '\033[38;5;203m')"
	BOLD="$(printf '\033[1m')"
	RESET="$(printf '\033[0m')"
else
	GREEN=""; CYAN=""; GREY=""; RED=""; BOLD=""; RESET=""
fi

step() { printf '%s✓%s %s\n' "$GREEN" "$RESET" "$1"; }
info() { printf '%s%s%s\n' "$GREY" "$1" "$RESET"; }
fail() { printf '%s✗ %s%s\n' "$RED" "$1" "$RESET" >&2; exit 1; }

logo() {
	printf '%s' "$GREEN"
	cat <<'ART'

  _   __________  ___  ___  ________
 | | / /_  __/ / / / |/ / |/ / __/ /
 | |/ / / / / /_/ /    /    / _// /__
 |___/ /_/  \____/_/|_/_/|_/___/____/

ART
	printf '%s' "$RESET"
	printf '%sPleasant local tunnels powered by Cloudflare Tunnel.%s\n\n' "$GREY" "$RESET"
}

have() { command -v "$1" >/dev/null 2>&1; }

download() {
	# download <url> <out>
	if have curl; then
		curl -fsSL "$1" -o "$2"
	elif have wget; then
		wget -qO "$2" "$1"
	else
		fail "need curl or wget to download"
	fi
}

fetch() {
	# fetch <url> -> stdout
	if have curl; then
		curl -fsSL "$1"
	else
		wget -qO- "$1"
	fi
}

detect_target() {
	os="$(uname -s)"
	case "$os" in
		Linux) os="linux" ;;
		Darwin) os="darwin" ;;
		*) fail "unsupported OS: $os (vtunnel supports Linux and macOS)" ;;
	esac
	arch="$(uname -m)"
	case "$arch" in
		x86_64 | amd64) arch="amd64" ;;
		arm64 | aarch64) arch="arm64" ;;
		*) fail "unsupported architecture: $arch" ;;
	esac
	printf '%s_%s' "$os" "$arch"
}

resolve_version() {
	if [ -n "${VTUNNEL_VERSION:-}" ]; then
		printf '%s' "$VTUNNEL_VERSION"
		return
	fi
	# The releases list isn't reliably ordered, so collect every tag and pick the
	# highest by version sort. Prefer the latest stable release; if there are only
	# prereleases (e.g. during beta), fall back to the highest prerelease.
	tags="$(fetch "https://api.github.com/repos/${REPO}/releases?per_page=100" \
		| grep '"tag_name"' \
		| sed -E 's/.*"tag_name"[[:space:]]*:[[:space:]]*"([^"]+)".*/\1/')"
	[ -n "$tags" ] || return
	stable="$(printf '%s\n' "$tags" | grep -v -- '-' | sort -V | tail -n1)"
	if [ -n "$stable" ]; then
		printf '%s' "$stable"
	else
		printf '%s\n' "$tags" | sort -V | tail -n1
	fi
}

verify_checksum() {
	# verify_checksum <file> <checksums_file> <basename>
	expected="$(grep " $3\$" "$2" | awk '{print $1}' | head -n1)"
	[ -n "$expected" ] || fail "no checksum found for $3"
	if have sha256sum; then
		actual="$(sha256sum "$1" | awk '{print $1}')"
	elif have shasum; then
		actual="$(shasum -a 256 "$1" | awk '{print $1}')"
	else
		info "no sha256 tool found; skipping checksum verification"
		return
	fi
	[ "$actual" = "$expected" ] || fail "checksum mismatch for $3"
}

choose_dir() {
	if [ -n "${VTUNNEL_INSTALL_DIR:-}" ]; then
		printf '%s' "$VTUNNEL_INSTALL_DIR"
	elif [ -w /usr/local/bin ] 2>/dev/null; then
		printf '/usr/local/bin'
	elif [ "$(id -u)" = "0" ]; then
		printf '/usr/local/bin'
	else
		printf '%s/.local/bin' "$HOME"
	fi
}

main() {
	logo

	target="$(detect_target)"
	step "Detected ${BOLD}${target}${RESET}"

	version="$(resolve_version)"
	[ -n "$version" ] || fail "could not resolve the latest version"
	step "Latest version ${BOLD}${version}${RESET}"

	base="https://github.com/${REPO}/releases/download/${version}"
	tarball="vtunnel_${version}_${target}.tar.gz"
	tmp="$(mktemp -d)"
	trap 'rm -rf "$tmp"' EXIT

	info "Downloading ${tarball}…"
	download "${base}/${tarball}" "${tmp}/${tarball}" || fail "download failed (is ${version} published for ${target}?)"
	download "${base}/checksums.txt" "${tmp}/checksums.txt" || fail "could not download checksums"
	verify_checksum "${tmp}/${tarball}" "${tmp}/checksums.txt" "${tarball}"
	step "Downloaded and verified"

	tar -xzf "${tmp}/${tarball}" -C "${tmp}" || fail "could not extract archive"
	[ -f "${tmp}/vtunnel" ] || fail "archive did not contain a vtunnel binary"
	chmod +x "${tmp}/vtunnel"

	dir="$(choose_dir)"
	mkdir -p "$dir" 2>/dev/null || fail "cannot create install directory $dir"
	if mv "${tmp}/vtunnel" "${dir}/vtunnel" 2>/dev/null; then
		:
	elif have sudo; then
		info "Elevating with sudo to install into ${dir}…"
		sudo mv "${tmp}/vtunnel" "${dir}/vtunnel" || fail "could not install into ${dir}"
	else
		fail "cannot write to ${dir} (set VTUNNEL_INSTALL_DIR to a writable path)"
	fi
	step "Installed ${BOLD}vtunnel${RESET} to ${dir}/vtunnel"

	# Warn if the install dir is not on PATH.
	case ":${PATH}:" in
		*":${dir}:"*) ;;
		*) printf '\n%s⚠ %s is not on your PATH.%s Add this to your shell profile:\n    %sexport PATH="%s:$PATH"%s\n' "$RED" "$dir" "$RESET" "$CYAN" "$dir" "$RESET" ;;
	esac

	if ! have cloudflared; then
		printf '\n%scloudflared is required and was not found.%s Install it:\n' "$GREY" "$RESET"
		if [ "${target%_*}" = "darwin" ]; then
			printf '    %sbrew install cloudflared%s\n' "$CYAN" "$RESET"
		else
			printf '    %shttps://developers.cloudflare.com/cloudflare-one/connections/connect-networks/downloads/%s\n' "$CYAN" "$RESET"
		fi
	fi

	# Final nudge — mirrors the Homebrew caveat.
	printf '\n%s%s┌─────────────────────────────────────────────┐%s\n' "$BOLD" "$GREEN" "$RESET"
	printf '%s%s│  vtunnel is installed — one step left!        │%s\n' "$BOLD" "$GREEN" "$RESET"
	printf '%s%s│                                               │%s\n' "$BOLD" "$GREEN" "$RESET"
	printf '%s%s│  Run the guided setup:                        │%s\n' "$BOLD" "$GREEN" "$RESET"
	printf '%s%s│      vtunnel onboarding                       │%s\n' "$BOLD" "$GREEN" "$RESET"
	printf '%s%s└─────────────────────────────────────────────┘%s\n\n' "$BOLD" "$GREEN" "$RESET"
}

main "$@"
