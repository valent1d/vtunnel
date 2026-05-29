#!/usr/bin/env bash
set -euo pipefail

version="${1:-}"
if [[ -z "${version}" || "${version}" == "dev" ]]; then
  echo "usage: scripts/release.sh <version>"
  echo "example: scripts/release.sh 0.1.0"
  exit 1
fi

version="${version#v}"
tag="v${version}"
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
dist="${root}/dist"
commit="$(git -C "${root}" rev-parse --short HEAD 2>/dev/null || echo unknown)"
date="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
ldflags="-s -w -X vtunnel/internal/cli.version=${tag} -X vtunnel/internal/cli.commit=${commit} -X vtunnel/internal/cli.date=${date}"

mkdir -p "${dist}"
rm -f "${dist}/vtunnel_${tag}_"*.tar.gz "${dist}/checksums.txt"

build() {
  local goos="$1"
  local goarch="$2"
  local tmp
  tmp="$(mktemp -d)"

  echo "building ${goos}/${goarch}"
  (
    cd "${root}"
    GOOS="${goos}" GOARCH="${goarch}" go build -trimpath -ldflags "${ldflags}" -o "${tmp}/vtunnel" ./cmd/vtunnel
  )
  tar -C "${tmp}" -czf "${dist}/vtunnel_${tag}_${goos}_${goarch}.tar.gz" vtunnel
  rm -rf "${tmp}"
}

build darwin arm64
build darwin amd64

(
  cd "${dist}"
  shasum -a 256 "vtunnel_${tag}_"*.tar.gz > checksums.txt
)

echo
echo "release artifacts:"
ls -lh "${dist}/vtunnel_${tag}_"*.tar.gz "${dist}/checksums.txt"
echo
echo "next:"
echo "  1. Upload dist artifacts to GitHub release ${tag}"
echo "  2. Replace URLs and sha256 values in Formula/vtunnel.rb"
echo "  3. Test with: brew install --build-from-source ./Formula/vtunnel.rb"
