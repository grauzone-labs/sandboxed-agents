#!/usr/bin/env bash
# Install the checksum-pinned GitHub CLI used by release publication and preview verification.
set -euo pipefail

: "${RUNNER_TEMP:?RUNNER_TEMP is required}" "${GITHUB_PATH:?GITHUB_PATH is required}"
directory="$RUNNER_TEMP/release-tools"
mkdir -p "$directory"
curl --fail --silent --show-error --location \
    https://github.com/cli/cli/releases/download/v2.102.0/gh_2.102.0_linux_amd64.tar.gz \
    --output "$directory/gh_2.102.0_linux_amd64.tar.gz"
(
    cd "$directory"
    printf '%s\n' 'bb766f710eef8ede859c18578c72c327597cd4c8a85b06001b1f3843c6019386  gh_2.102.0_linux_amd64.tar.gz' | sha256sum --check --strict
    tar -xzf gh_2.102.0_linux_amd64.tar.gz
)
"$directory/gh_2.102.0_linux_amd64/bin/gh" --version
printf '%s\n' "$directory/gh_2.102.0_linux_amd64/bin" >> "$GITHUB_PATH"
