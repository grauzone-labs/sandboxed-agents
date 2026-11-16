#!/usr/bin/env bash
# Run offline real-Git/GnuPG tests and the real manager mechanism against a locally built repository image.
set -euo pipefail
image=${1:?usage: scripts/check-git-credentials.sh LOCAL_REPOSITORY_IMAGE}
check_directory=$(mktemp -d)
check_container="wi143-tests-$$"
cleanup() {
    podman rm --force "$check_container" >/dev/null 2>&1 || true
    rm -rf "$check_directory"
}
trap cleanup EXIT
go test -c -o "$check_directory/store.test" ./internal/gitcredentials
go test -c -o "$check_directory/manager.test" ./internal/manager
go build -o "$check_directory/fixture" ./internal/gitcredentials/testdata/manager
podman run -d --name "$check_container" --network=none --entrypoint sleep "$image" infinity >/dev/null
for artifact in store.test manager.test fixture; do
    podman cp "$check_directory/$artifact" "$check_container:/tmp/$artifact"
done
podman exec --user=1000:1000 --env SA_GIT_CREDENTIAL_TEST_MANAGER=/tmp/fixture "$check_container" /tmp/store.test -test.v
podman exec --user=1000:1000 --env SA_GIT_CREDENTIAL_TEST_MANAGER=/tmp/fixture "$check_container" /tmp/manager.test -test.run '^TestAzureDevOps' -test.v
python3 scripts/check-git-credentials.py "$image"
