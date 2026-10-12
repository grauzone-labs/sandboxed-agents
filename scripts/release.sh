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

if [[ $(git rev-parse --is-inside-work-tree 2>/dev/null || true) == true ]]; then
    tag="refs/tags/v$version"
    if ! tag_commit=$(git rev-parse --verify "$tag^{commit}" 2>/dev/null); then
        printf 'release requires tag %s at HEAD\n' "$tag" >&2
        exit 1
    fi
    if [[ $tag_commit != "$(git rev-parse HEAD)" ]]; then
        printf 'release tag %s does not point to HEAD\n' "$tag" >&2
        exit 1
    fi
fi

export GOTOOLCHAIN=local CGO_ENABLED=0 GOFLAGS= GOEXPERIMENT= GOAMD64=v1
required_go=$(awk '$1 == "go" { sub(/\r$/, "", $2); print "go" $2 }' go.mod)
if [[ $(go env GOVERSION) != "$required_go" ]]; then
    printf 'release requires %s on PATH\n' "$required_go" >&2
    exit 1
fi

host_os=$(go env GOHOSTOS)
host_arch=$(go env GOHOSTARCH)

overlay_directory=$(mktemp -d)
trap 'rm -rf -- "$overlay_directory"' EXIT
GOOS=$host_os GOARCH=$host_arch go run ./image/cmd/overlay "$overlay_directory"
version_symbol=$(GOOS=$host_os GOARCH=$host_arch go run ./internal/release/cmd/version-symbol)

for target in linux windows; do
    suffix=
    if [[ $target == windows ]]; then suffix=.exe; fi
    GOOS=$target GOARCH=amd64 go build -overlay "$overlay_directory/overlay.json" \
        -trimpath -buildvcs=false \
        -ldflags "-buildid= -X $version_symbol=$version" \
        -o "$output/sandboxed-agents-$target-amd64$suffix" ./cmd/sandboxed-agents
done
