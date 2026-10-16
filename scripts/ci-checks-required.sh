#!/usr/bin/env bash
set -euo pipefail

# Main pushes supply the exact-commit, two-OS evidence consumed by the stable
# release gate. Manual runs also request full validation, regardless of paths.
if [[ ${GITHUB_EVENT_NAME:-} != pull_request ]]; then
  printf 'run_checks=true\n'
  exit 0
fi

if [[ ! ${CI_BASE_SHA:-} =~ ^[0-9a-f]{40}$ || ! ${CI_HEAD_SHA:-} =~ ^[0-9a-f]{40}$ ]]; then
  echo 'CI comparison commits missing or invalid; running all checks.' >&2
  printf 'run_checks=true\n'
  exit 0
fi

changed_paths=$(mktemp)
trap 'rm -f "$changed_paths"' EXIT
if ! git diff --name-only --no-renames -z "$CI_BASE_SHA...$CI_HEAD_SHA" -- > "$changed_paths"; then
  echo 'CI comparison unavailable; running all checks.' >&2
  printf 'run_checks=true\n'
  exit 0
fi

if [[ ! -s $changed_paths ]]; then
  echo 'CI comparison has no changed paths; running all checks.' >&2
  printf 'run_checks=true\n'
  exit 0
fi

while IFS= read -r -d '' path; do
  case "$path" in
    # Go packages can live below documentation directories too.
    *.go) printf 'run_checks=true\n'; exit 0 ;;
    docs/*|.agents/*|AGENTS.md) ;;
    *) printf 'run_checks=true\n'; exit 0 ;;
  esac
done < "$changed_paths"

echo 'Only documentation or skill paths changed; skipping expensive PR checks.' >&2
printf 'run_checks=false\n'
