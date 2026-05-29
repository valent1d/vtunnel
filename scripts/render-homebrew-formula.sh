#!/usr/bin/env bash
set -euo pipefail

version="${1:-}"
output="${2:-Formula/vtunnel.rb}"
repo="${VTUNNEL_GITHUB_REPO:-valent1d/vtunnel}"
checksums="${VTUNNEL_CHECKSUMS_FILE:-dist/checksums.txt}"

if [[ -z "${version}" ]]; then
  echo "usage: scripts/render-homebrew-formula.sh <version> [output]" >&2
  exit 1
fi
if [[ ! -f "${checksums}" ]]; then
  echo "checksums file not found: ${checksums}" >&2
  exit 1
fi

tag="${version}"
if [[ "${tag}" != v* ]]; then
  tag="v${tag}"
fi
formula_version="${tag#v}"

arm64_asset="vtunnel_${tag}_darwin_arm64.tar.gz"
amd64_asset="vtunnel_${tag}_darwin_amd64.tar.gz"
arm64_sha="$(awk -v asset="${arm64_asset}" '$2 == asset { print $1 }' "${checksums}")"
amd64_sha="$(awk -v asset="${amd64_asset}" '$2 == asset { print $1 }' "${checksums}")"

if [[ -z "${arm64_sha}" ]]; then
  echo "missing checksum for ${arm64_asset}" >&2
  exit 1
fi
if [[ -z "${amd64_sha}" ]]; then
  echo "missing checksum for ${amd64_asset}" >&2
  exit 1
fi

mkdir -p "$(dirname "${output}")"
cat > "${output}" <<FORMULA
class Vtunnel < Formula
  desc "Pleasant local tunnels powered by Cloudflare Tunnel"
  homepage "https://github.com/${repo}"
  version "${formula_version}"

  on_macos do
    if Hardware::CPU.arm?
      url "https://github.com/${repo}/releases/download/${tag}/${arm64_asset}"
      sha256 "${arm64_sha}"
    else
      url "https://github.com/${repo}/releases/download/${tag}/${amd64_asset}"
      sha256 "${amd64_sha}"
    end
  end

  depends_on "cloudflared"

  def install
    bin.install "vtunnel"
  end

  def caveats
    <<~EOS
      To start using vtunnel, run:
        vtunnel onboarding
    EOS
  end

  test do
    assert_match "vtunnel", shell_output("#{bin}/vtunnel --version")
  end
end
FORMULA

echo "wrote ${output}"
