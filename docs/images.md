# Images

This document describes the base image recipe in `image/context/base/` and how it checks the NodeSource signing key before it trusts the NodeSource package repository. The host executable embeds the image build context, as described in [ADR 0001](adr/0001-go-for-host-executable-and-in-container-manager.md).

`image/assets.go` owns the `go:embed` directive. Its `Context` API supplies a read-only context with CRLF text line endings normalized to LF. At build time, `.bin` files and NUL-containing files preserve the bytes already present in the tree. `.gitattributes` marks `.bin` files as binary and forces other context files to text, so binary assets must use `.bin` to preserve exact bytes through Git. The asset hash includes sorted relative paths and normalized contents with length prefixes; it excludes file modes and timestamps. `sandboxed-agents version` prints the version followed by `asset-sha256 HASH`.

Run `bash scripts/release.sh VERSION OUTPUT_DIRECTORY` with the Go version from `go.mod` on `PATH` to build static Linux and Windows AMD64 executables. The script runs `image/cmd/overlay` to prepare normalized files in a temporary directory and uses a Go build overlay, so the release executable embeds LF bytes even from a CRLF checkout. The checkout stays untouched and the temporary files are removed. A plain `go build` exposes the same normalized context and asset hash at runtime; use the release script for normalization before embedding and reproducible binary bytes. The script produces executables only; package distribution is not implemented.

`.gitattributes` keeps `go.mod`, `scripts/*.sh`, and `scripts/version-symbol.txt` checkouts at LF.

Inside a Git worktree, `VERSION` requires the literal tag `vVERSION` to point at `HEAD`; annotated tags are peeled to their commit. For example, version `1.2.3` requires `v1.2.3`. The worktree must also be clean, including tracked edits and untracked files. Missing or mismatched tags and dirty worktrees stop the release before building. If the source root has a `.git` entry, Git must be available to verify it. Source directories outside Git remain buildable.

`go test ./scripts -count=1 -v` compares builds of the same tagged temporary commit with LF and CRLF assets, all Go sources, and module/linker metadata. On Windows, set `SANDBOXED_AGENTS_RELEASE_BASH` to the Git for Windows executable, such as `C:\Program Files\Git\bin\bash.exe`. The Windows test requires that setting and runs the Windows executable's `version` command. Linux tests run on Linux; the Windows test skips other hosts.

`.github/workflows/release-reproducibility.yml` runs Linux and native Windows jobs on Blacksmith for pushes to `main`, pull requests, and manual runs. Each runner creates a local CI tag at its checkout's commit. The Windows job executes the Windows binary, compares its asset hash with Linux, and compares the SHA256 of both release executables with their Linux-built counterparts. Those runner tags are not pushed.

Release limitations:

- Local verification executes the Linux binary and checks cross-compiled Windows bytes and the PE AMD64 machine field; this Linux host cannot execute the Windows binary.
- CI runs the Windows binary on Blacksmith with configured Git for Windows Bash, comparing its asset hash and both executable SHA256 digests with Linux outputs. Native Windows verification belongs to CI.
- The clean-worktree check rejects tracked edits, untracked files, and ignored files under `image/context/` or other release inputs; ignored files elsewhere are allowed.
- A plain `go build` from a CRLF checkout embeds raw CRLF bytes and normalizes the context only at runtime. The release script normalizes text before embedding.
- [ADR 0001](adr/0001-go-for-host-executable-and-in-container-manager.md) requires building the manager for Linux before the host executable. The manager does not exist yet, so this release script builds and embeds no manager.
- NUL detection preserves tree bytes only during build normalization. Git's forced-text attributes can change line endings in non-`.bin` files, including NUL-containing files. Every binary asset must use `.bin` for exact bytes through Git; this naming convention has no further enforcement or content validation.
- npm and NuGet packages do not exist and are not produced or tested by this script.

## Build pipeline

The public `image.Build` API writes the complete normalized context into a fresh system temporary directory, hashes the written files, invokes a supplied Podman runner, and removes the directory before returning. It removes it after a successful build, a failed build, cancellation, and a failure while preparing the context. Build and cleanup errors are returned; a failed operation returns no image tag. A nil asset source uses the embedded `image.Context`.

`BuildOptions.RecipeDir` selects a directory within the context and defaults to `base`. The build passes that directory and its `Containerfile` to `podman build`, along with the image tag. The caller selects a recipe for the requested toolchain set and supplies a runner bound to the target checked by its preflight. The runner also owns build policy flags and labels. The API does not select a machine or run a preflight itself.

`image.Tag` names images as `localhost/sandboxed-agents:SET-HASH`, using the full SHA256 asset hash. `SET` is `none` for the base image or the sorted, distinct toolchain names joined with `-`, for example `dotnet-playwright`. Its input must have passed catalog validation. Toolchain order and duplicates do not change the tag, and controller groups do not enter it. Changing any embedded asset, including the fingerprint file, changes the hash and tag. LF and CRLF assets produce the same tags.

This pipeline is available for the command implementations. The CLI's `build` command still reports that it is not implemented: issue #13 supplies its prerequisite preflight and Podman target binding, and #18 supplies its rebuild policy, labels, and toolchain recipes. Tag derivation does not enable toolchains in the delivered catalog.

Offline tests exercise the API through a supplied process runner and through fixture executables compiled with LF and CRLF embedded assets against fake Podman. They cover identical hashes and tags, the normalized fingerprint in the temporary context, changed fingerprint tags, and cleanup on success, failure, and cancellation. They do not build an image with real Podman.

## Base image

The base image is Debian 13 (trixie) slim, which uses glibc, with Node.js 24 LTS installed on top. Debian 13 is the current stable release, while Debian 12 is only in its LTS period. Node.js 24 remains an LTS release.

Every toolchain's packages are available for Debian 13. This choice rests on that statement. This repository does not verify it; the per-toolchain builds and smoke checks (the acceptance criteria of Feature #3, and the live suite) do.

The contents of the base image are set by Feature #3. This recipe installs Node.js only, plus the packages it needs to fetch and check the key: `ca-certificates`, `curl`, and `gnupg`.

### NodeSource signing key check

The recipe in `image/context/base/Containerfile` downloads the key from https://deb.nodesource.com/gpgkey/nodesource-repo.gpg.key into a temporary file. It then runs `check-nodesource-key.sh`, which compares the key in that file with the expected fingerprint in `nodesource-signing-key.fingerprint`. Only after the check passes does the recipe dearmor the key into `/etc/apt/keyrings/nodesource.gpg` and write the apt source line with `signed-by` pointing at that file.

The check requires exactly one primary key in the downloaded file. If the primary fingerprint differs from the expected one, the script fails and prints the expected and the found fingerprint. If the file holds other than one primary key, the script fails and prints the count, the expected fingerprint, and every primary fingerprint it found. Nothing from the NodeSource repository is installed after a failed check, because the keyring and the source line are never written. A failed check fails the image build.

## The expected fingerprint

The expected fingerprint is `6F71F525282841EEDAF851B42F59B5F99B1BE0B4`, stored in `image/context/base/nodesource-signing-key.fingerprint`. It is the fingerprint of the primary key. The key file holds one primary key (`2F59B5F99B1BE0B4`, uid `NSolid <nsolid-gpg@nodesource.com>`) and one subkey.

The key was fetched from deb.nodesource.com on 2026-10-11.

NodeSource's own repository (nodesource/distributions, at commit 9b431d8 of 2026-07-09) publishes no fingerprint of this key.

One source independent of deb.nodesource.com holds the same key. The file `files/repo/nodesource/NODESOURCE-GPG-SIGNING-KEY-EL` in voxpupuli/puppet-nodejs, at commit `7649b6b8a` of 2024-01-03, contains a key with this primary fingerprint. The version of that file from 2015 (commit `9cf0f3035`) holds a different key, so this key was recorded there after 2015 and before 2026. That record is dated and comes from another project. It shows that the key has stayed the same from 2024 to 2026. It does not prove that NodeSource published the key, because that project's author also took the key from NodeSource.

Forum posts also show the same fingerprint, but they are not an independent source. One unverified report says the hosted key once no longer matched the signature on an 18.x `InRelease` file. This report has not been reproduced here.

The expected fingerprint therefore records the key as fetched and reviewed when it was added, and corroborated by a dated copy in another project. It detects a later change of the key. It does not detect a key that was already substituted before its first recorded copy.

## Known limit: SHA-1 signature

The key's subkey binding signature, created in 2016, uses SHA-1. This was verified with `gpg --list-packets` on the fetched key. Third-party reports say that Debian 13 refuses NodeSource repositories whose binding signatures use SHA-1. One such report is in https://github.com/nodesource/distributions/issues/1920.

The risk was tested on 2026-10-11. The Containerfile was built with rootless Podman from `debian:trixie-slim`. apt fetched and verified the `node_24.x` repository (`InRelease`) with the pinned key, reported no key or signature warning, and installed `nodejs` 24.21.0 from it. The image ran `node --version` as v24.21.0, and the check files were removed from it. So the refusal reported elsewhere did not occur with this key on Debian 13.

That build tested the key as fetched on that day. It does not cover a later key. A key rotation needs the same build again before the pin is updated.

## Key rotation

If NodeSource rotates its signing key, the downloaded key no longer matches the expected fingerprint and the check fails. `build` then stops and builds no image for any toolchain set. The previous images stay usable under their tags. `up` cannot build a missing base image until the recipe is updated.

To update the expected fingerprint, the maintainer takes these steps:

1. Fetch the new key.
2. Confirm its primary fingerprint with gpg, for example `gpg --show-keys` with `--with-fingerprint`, in an isolated `GNUPGHOME`. When possible, also confirm it from a source independent of deb.nodesource.com.
3. Write the new fingerprint into `image/context/base/nodesource-signing-key.fingerprint`.
4. Replace the fixture `image/test/nodesource/nodesource-repo.gpg.key` with the new key.
5. Update the source, date, and key details in the section above.
6. Run the offline test. It reads the expected fingerprint from the fingerprint file, so the test needs no edit.
7. Build the base image with rootless Podman, `podman build -f image/context/base/Containerfile image/context/base`, and check that apt accepts the repository.
8. Commit.

Changing the fingerprint file changes the asset hash and the image tags derived by the build pipeline.

## Tests and limits

The offline test is run with `bash image/test/nodesource/test-check-nodesource-key.sh`. It needs no network. It checks that the key with the expected fingerprint is accepted, that a different fingerprint is refused with both fingerprints named, that a file with two primary keys is refused with the count and both fingerprints named, that an empty file is refused, that the fingerprint file holds 40 uppercase hex characters followed by one LF, and, statically, that the recipe runs the check before it writes the keyring and the source list.

Several things are not covered here:

- CLI build execution, rebuild policy, image labels, and toolchain recipes (issues #13 and #18). The image build API supplies tags and temporary context cleanup; release preprocessing does not build a Podman image.
- The Microsoft signing key.
- Digests of the pulled Debian image or of built images.
- The live build against the real NodeSource key (issue #18 and the live suite).
