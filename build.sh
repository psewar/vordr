#!/bin/bash
# Tests, vet and static binaries for every agent target and the hub. Call from WSL or Linux:
#   ./build.sh [version]          default version: YYYY.MM.DD-HHMM
# Binaries land in dist/ (vordr-agent-<os>-<arch>[.exe], vordr-hub-linux-amd64).
set -euo pipefail
cd "$(dirname "$0")"
VERSION="${1:-$(date +%Y.%m.%d)-$(date +%H%M)}"
export CGO_ENABLED=0

# ADR index, numbers and supersede links (docs/adr/README.md, ADR-0072).
command -v python3 >/dev/null || { echo "python3 missing: build.sh needs it for the ADR check" >&2; exit 1; }
python3 docs/adr/check_index.py
# Site-neutral repository (ADR-0099): no host names, addresses, domains or paths of the site that runs it. The site's
# own terms stay outside the repository (~/.config/vordr/site-terms.txt or $VORDR_SITE_TERMS).
python3 -m unittest discover -q -s tools -p "check_*_test.py"
python3 tools/check_neutral.py
# Every document in English and German, same outline, links that resolve (ADR-0143).
python3 tools/check_docs.py

gofmt_out=$(gofmt -l .)
[ -z "$gofmt_out" ] || { echo "gofmt: $gofmt_out" >&2; exit 1; }
go vet ./...
go test ./...
# The race detector needs cgo; where a C compiler exists, run the concurrent parts with it.
if command -v gcc >/dev/null; then
  CGO_ENABLED=1 go test -race ./internal/agent ./internal/hub ./internal/protocol
else
  echo "race detector skipped: no gcc"
fi
GOOS=windows go vet ./...

mkdir -p dist
for t in linux/amd64 linux/arm64 linux/arm windows/amd64; do
  os=${t%/*}; arch=${t#*/}; ext=
  [ "$os" = windows ] && ext=.exe
  GOOS=$os GOARCH=$arch GOARM=7 go build -trimpath -ldflags "-s -w -X main.version=$VERSION" \
    -o "dist/vordr-agent-$os-$arch$ext" ./cmd/vordr-agent
done
GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "-s -w -X main.version=$VERSION" \
  -o dist/vordr-hub-linux-amd64 ./cmd/vordr-hub
# The release signer runs on the operator's machine, never on the hub (ADR-0139).
for t in linux/amd64 windows/amd64; do
  os=${t%/*}; arch=${t#*/}; ext=
  [ "$os" = windows ] && ext=.exe
  GOOS=$os GOARCH=$arch go build -trimpath -ldflags "-s -w -X main.version=$VERSION" \
    -o "dist/vordr-release-$os-$arch$ext" ./cmd/vordr-release
done
ls -l dist
echo "version $VERSION"
