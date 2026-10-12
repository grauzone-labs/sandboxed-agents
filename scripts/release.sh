#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 2 || ! $1 =~ ^[a-zA-Z0-9][a-zA-Z0-9._+-]*$ ]]; then
    printf 'usage: %s VERSION OUTPUT_DIRECTORY\n' "$0" >&2
    exit 2
fi
version=$1
mkdir -p -- "$2"
output=$(cd -- "$2" && pwd)
root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
cd -- "$root"

export GOTOOLCHAIN=local CGO_ENABLED=0 GOFLAGS= GOEXPERIMENT= GOAMD64=v1
required_go=$(awk '$1 == "go" { print "go" $2 }' go.mod)
if [[ $(go env GOVERSION) != "$required_go" ]]; then
    printf 'release requires %s on PATH\n' "$required_go" >&2
    exit 1
fi

overlay_directory=$(mktemp -d)
trap 'rm -rf -- "$overlay_directory"' EXIT
GOOS=$(go env GOHOSTOS) GOARCH=$(go env GOHOSTARCH) go run ./image/cmd/overlay "$overlay_directory"
version_symbol=$(GOOS=$(go env GOHOSTOS) GOARCH=$(go env GOHOSTARCH) go run ./internal/release/cmd/version-symbol)

for target in linux windows; do
    suffix=
    if [[ $target == windows ]]; then suffix=.exe; fi
    GOOS=$target GOARCH=amd64 go build -overlay "$overlay_directory/overlay.json" \
        -trimpath -buildvcs=false \
        -ldflags "-buildid= -X $version_symbol=$version" \
        -o "$output/sandboxed-agents-$target-amd64$suffix" ./cmd/sandboxed-agents
done
