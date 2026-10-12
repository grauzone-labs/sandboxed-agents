#!/usr/bin/env bash
set -euo pipefail

if [[ "$IS_REUSABLE_CALL" == false && "$GITHUB_EVENT_NAME" == push && "$STABLE_TAG" =~ ^v[0-9]+\.[0-9]+\.[0-9]+-preview\.[0-9]{8}\.[0-9]+$ ]]; then
  echo 'preview=true' >> "$GITHUB_OUTPUT"
  exit 0
fi
[[ "$STABLE_TAG" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || { echo 'stable release gate: invalid stable tag' >&2; exit 1; }
tag_commit=$(git rev-parse --verify "refs/tags/${STABLE_TAG}^{commit}") || { echo 'stable release gate: stable tag does not resolve to a commit' >&2; exit 1; }
[[ "$tag_commit" == "$GITHUB_SHA" ]] || { echo 'stable release gate: stable tag commit differs from workflow commit' >&2; exit 1; }
