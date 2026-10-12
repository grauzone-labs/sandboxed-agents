#!/usr/bin/env bash
set -euo pipefail

version_number='(0|[1-9][0-9]*)'
version_identifier='(0|[1-9][0-9]*|[0-9A-Za-z-]*[A-Za-z-][0-9A-Za-z-]*)'
version_pattern="^$version_number\.$version_number\.$version_number(-$version_identifier(\.$version_identifier)*)?(\+[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$"
if [[ $# -ne 2 || ! $1 =~ $version_pattern ]]; then
    printf 'usage: %s VERSION OUTPUT_DIRECTORY\n' "$0" >&2
    exit 2
fi
version=$1
mkdir -p -- "$2"
output=$(cd -- "$2" && pwd)
root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
cd -- "$root"

root_has_git_entry() {
    [[ -e "$root/.git" || -L "$root/.git" ]]
}

inside_worktree=false
if command -v git >/dev/null 2>&1; then
    if ! inside_worktree=$(git rev-parse --is-inside-work-tree 2>/dev/null); then
        if root_has_git_entry; then
            printf 'release cannot inspect its Git worktree\n' >&2
            exit 1
        fi
        inside_worktree=false
    fi
elif root_has_git_entry; then
    printf 'release requires git to verify its worktree\n' >&2
    exit 1
fi

if [[ $inside_worktree == true ]]; then
    tag="refs/tags/v$version"
    if ! tag_commit=$(git rev-parse --verify "$tag^{commit}" 2>/dev/null); then
        printf 'release requires tag %s at HEAD\n' "$tag" >&2
        exit 1
    fi
    if [[ $tag_commit != "$(git rev-parse HEAD)" ]]; then
        printf 'release tag %s does not point to HEAD\n' "$tag" >&2
        exit 1
    fi
    worktree_status=$(git status --porcelain --untracked-files=all)
    if [[ -n $worktree_status ]]; then
        printf 'release requires a clean Git worktree (tracked and untracked files)\n' >&2
        exit 1
    fi
    ignored_inputs=$(git ls-files --others --ignored --exclude-standard -- \
        ':(glob)image/context/**' 'go.mod' 'go.work' '.gitattributes' ':(glob)**/*.go' \
        'scripts/release.sh' 'scripts/version-symbol.txt' 'packages/nuget/SandboxedAgents.nuspec' \
        ':(glob)packages/nuget/tools/**')
    if [[ -n $ignored_inputs ]]; then
        printf 'release refuses ignored release inputs:\n%s\n' "$ignored_inputs" >&2
        exit 1
    fi
fi

export GOTOOLCHAIN=local CGO_ENABLED=0 GOFLAGS= GOEXPERIMENT= GOAMD64=v1 GOWORK=off GOENV=off GOFIPS140=off
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

GOOS=$host_os GOARCH=$host_arch go run ./internal/nuget/cmd/package "$version" "$root" "$output"
