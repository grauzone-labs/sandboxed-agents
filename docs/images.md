# Images

This document describes the base image recipe in `image/context/base/`, the four toolchain recipes in `image/context/toolchains/`, how `build` and `up` produce and refresh images, and how the base recipe checks the NodeSource signing key before it trusts the NodeSource package repository. The host executable embeds the image build context, as described in [ADR 0001](adr/0001-go-for-host-executable-and-in-container-manager.md).

`image/assets.go` owns the `go:embed` directive. Its `Context` API supplies a read-only context with CRLF text line endings normalized to LF. At build time, `.bin` files and NUL-containing files preserve the bytes already present in the tree. `.gitattributes` marks `.bin` files as binary and forces other context files to text, so binary assets must use `.bin` to preserve exact bytes through Git. The asset hash includes sorted relative paths and normalized contents with length prefixes; it excludes file modes and timestamps. `sandboxed-agents version` prints the version followed by `asset-sha256 HASH`.

Run `bash scripts/release.sh VERSION OUTPUT_DIRECTORY` with the Go version from `go.mod` on `PATH` to build static Linux and Windows AMD64 executables. The script runs `image/cmd/overlay` to prepare normalized files in a temporary directory and uses a Go build overlay, so the release executable embeds LF bytes even from a CRLF checkout. The checkout stays untouched and the temporary files are removed. A plain `go build` normalizes the context at runtime but embeds no manager, so its images cannot build; see [Development builds with the manager](#development-builds-with-the-manager). Use the release script for normalization before embedding and reproducible binary bytes. The script also emits `SHA256SUMS` and the `SandboxedAgents.VERSION.nupkg` [NuGet command package](nuget-package.md), which contains the Windows executable, its checksum manifest, and the PowerShell 7 install and remove scripts. It also emits the `sandboxed-agents-VERSION.tgz` [npm package](npm-package.md), which holds both executables, their checksum manifest, and a Node.js launcher. `VERSION` must be NuGet-compatible SemVer: `X.Y.Z` with optional prerelease and build metadata. The package filename omits any `+build` suffix while the nuspec keeps the full version, so `1.2.3+abc` produces `SandboxedAgents.1.2.3.nupkg` with version `1.2.3+abc` in its nuspec.

`.gitattributes` keeps `go.mod`, `scripts/*.sh`, `scripts/version-symbol.txt`, `packages/nuget/**`, all `*.go` files, and `.github/workflows/*.yml` and `*.yaml` checkouts at LF, so `gofmt` stays clean under `core.autocrlf=true`. The release tests still rewrite their inputs to CRLF to prove reproducibility.

Inside a Git worktree, `VERSION` requires the literal tag `vVERSION` to point at `HEAD`; annotated tags are peeled to their commit. For example, version `1.2.3` requires `v1.2.3`. The worktree must also be clean, including tracked edits and untracked files. Missing or mismatched tags and dirty worktrees stop the release before building. If the source root has a `.git` entry, Git must be available to verify it. Source directories outside Git remain buildable.

`go test ./scripts -count=1 -v` compares builds of the same tagged temporary commit with LF and CRLF assets, all Go sources, and module/linker metadata. On Windows, set `SANDBOXED_AGENTS_RELEASE_BASH` to the Git for Windows executable, such as `C:\Program Files\Git\bin\bash.exe`. `TestWindowsReleaseBuildsAreReproducible` and `TestWindowsNuGetCommandPackageLifecycle` skip with a reason when the setting is unset and fail when an explicitly configured path is not an existing file. The reproducibility test runs the Windows executable's `version` command. Linux tests run on Linux; the Windows tests skip other hosts.

`.github/workflows/release-reproducibility.yml` runs on Blacksmith for pushes to `main`, pull requests, and manual runs; feature-branch pushes start no run, so a pull request runs once rather than twice. A newer pull request run cancels a superseded in-progress run of the same pull request, while pushes to `main` and manual runs always run to completion so the exact-commit stable release evidence survives. The Linux job starts by running `scripts/ci-checks-required.sh`. On a pull request that changes only `docs/`, `.agents/`, or `AGENTS.md`, with no `.go` file among them, that decision step succeeds and the Linux job skips the Go toolchain, the offline suite, and the release build, while the Windows job is skipped entirely. Pushes to `main` and manual runs always run the full checks, because the stable release gate requires a successful push run with both the Linux and Windows jobs on the exact commit.

In a full run, both jobs restore the Go build and module caches, and each runner creates a local CI tag at its checkout's commit. The Windows job executes the Windows binary, compares its asset hash with Linux, and compares the SHA256 of both release executables and the npm tarball with their Linux-built counterparts. Those runner tags are not pushed.

Release limitations:

- Local verification executes the Linux binary and checks cross-compiled Windows bytes and the PE AMD64 machine field; this Linux host cannot execute the Windows binary.
- CI runs the Windows binary on Blacksmith with configured Git for Windows Bash, comparing its asset hash and both executable SHA256 digests with Linux outputs. Native Windows verification belongs to CI.
- The clean-worktree check rejects tracked edits, untracked files, and ignored files under `image/context/` or other release inputs; ignored files elsewhere are allowed.
- A plain `go build` from a CRLF checkout embeds raw CRLF bytes and normalizes the context only at runtime. The release script normalizes text before embedding.
- [ADR 0001](adr/0001-go-for-host-executable-and-in-container-manager.md) requires building the manager for Linux before the host executable. The release script does this through `image/cmd/overlay`, which compiles `cmd/sandboxed-agents-manager` for Linux amd64 into the overlay context. Both host executables embed those same bytes. An offline test (`go test ./image`) checks that the embedded manager is a static Linux amd64 ELF file and is identical from LF and CRLF checkouts.
- NUL detection preserves tree bytes only during build normalization. Git's forced-text attributes can change line endings in non-`.bin` files, including NUL-containing files. Every binary asset must use `.bin` for exact bytes through Git; this naming convention has no further enforcement or content validation.

## Base image

The base image is Debian 13 (trixie) slim, which uses glibc, with Node.js 24 LTS installed on top. Debian 13 is the current stable release, while Debian 12 is only in its LTS period. Node.js 24 remains an LTS release.

Every toolchain's packages are available for Debian 13. The sources, checked on 2026-10-14, are in [Toolchains](#toolchains). Only a live build proves the images; the recorded live runs are in [Live proof](#live-proof) and [Honest limits](#honest-limits).

The base image contains:

- OpenSSH server and client, Git, GitHub CLI, tmux, `curl`, `jq`, `ripgrep`, `less`, `unzip`, `zip`, `xz-utils`, `file`, `procps`, Bash, coreutils, findutils, `gnupg`, and CA certificates.
- Node.js 24 and npm from the NodeSource repository `node_24.x`, installed after the [signing key check](#nodesource-signing-key-check).
- The user `agent` with UID and GID 1000, the directory `/workspace` owned by `agent`, and no baked SSH host keys. See [SSH host keys](#ssh-host-keys).
- The in-container manager at `/usr/local/bin/sandboxed-agents-manager`.
- No agent and none of the toolchains below.

### Installed version manifest

Every image records the versions of what it installed in `/usr/local/share/sandboxed-agents/versions.txt`. The base image writes `/etc/os-release`, every Debian package with its version, the Node.js and npm versions, and the manager version. A toolchain image rewrites the same file with `image/context/toolchains/record-versions.sh`, which adds the output of `dotnet --list-sdks`, `playwright --version`, `az version`, and `cc --version` for the toolchains present.

### SSH host keys

The recipe removes the SSH host keys that the Debian package creates, so no key is shared between sandboxes or recorded in an image. The entrypoint instead runs `ssh-keygen -A` under `set -e` at every container start, before it starts sshd. The sandbox's SSH volume is mounted at `/etc/ssh` by then, so `ssh-keygen -A` creates only the keys that volume lacks. The keys are therefore generated on first start and survive `stop`, `start`, and `update`, which keep the volumes. If key generation fails, the container fails to start rather than run without host keys.

Podman can accept `exec` before the entrypoint has finished. The manager's `ssh-host-key` command, which SSH setup uses to pin the host key, therefore polls every 25 ms for up to 2 seconds while `/etc/ssh/ssh_host_ed25519_key.pub` is missing or empty, inside the host's 3-second limit for administrative queries. Any other failure, such as a permission error or a malformed non-empty key, fails at once. The command prints no key on failure.

### SSH server

After the host keys exist, the entrypoint replaces its shell with `/usr/sbin/sshd -D -e -f /usr/local/etc/sandboxed-agents/sshd_config` through `exec`, so sshd runs as root in the foreground as the container's main process, logs to stderr, and the container stops when sshd exits. The configuration file is copied into the image at that path, outside the persistent `/etc/ssh` volume, so the volume holds only server state and cannot change the policy. The policy accepts only the user `agent` with public-key authentication against the single file `/etc/ssh/authorized_keys`: root login, passwords, keyboard-interactive authentication, and agent forwarding are off, and SFTP uses the internal subsystem.

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
7. Prepare the build context (`<prepared-context>`) as in [Development builds with the manager](#development-builds-with-the-manager), build the base image with rootless Podman, `podman build -f <prepared-context>/context/base/Containerfile <prepared-context>/context/base`, and check that apt accepts the repository. The `manager.bin` in the source tree is an empty placeholder, so a build from the checkout itself stops at the manager guard.
8. Commit.

Changing the fingerprint file changes the asset hash, so the base image gets a new tag (see [Build and tags](#build-and-tags)).

## Toolchains

The toolchains are `dotnet`, `playwright`, `azure`, and `native`. All are off by default. `--with none`, accepted by `up` and `build`, selects the base image alone and stands alone: `--with none,dotnet` fails. An unknown name fails before Podman is called and lists the valid values: `dotnet`, `playwright`, `azure`, `native`, and `none`.

Each toolchain is one recipe fragment in `image/context/toolchains/NAME.Containerfile`:

| Toolchain | Installs |
|---|---|
| `dotnet` | .NET 8 and .NET 10 SDKs (`dotnet-sdk-8.0`, `dotnet-sdk-10.0`) from the Microsoft package feed for Debian 13, with `DOTNET_CLI_TELEMETRY_OPTOUT=1` and `DOTNET_NOLOGO=1`. .NET 9 is not installed. |
| `playwright` | The npm packages `playwright` and `@playwright/test` installed globally under `/usr/local`, so `require('playwright')` resolves through `NODE_PATH=/usr/local/lib/node_modules`. The Chromium, Firefox, and WebKit browsers and their system packages come from `playwright install --with-deps chromium firefox webkit`. Browser binaries live in `/opt/playwright-browsers` (`PLAYWRIGHT_BROWSERS_PATH`), outside the home volume and readable by every user. The Playwright version is not pinned: a build installs the current npm release. |
| `azure` | The Debian packages `azure-cli` and `python3-azext-devops`, so `az devops` is available system-wide. The build runs `az devops --help` to check it. |
| `native` | `build-essential`, `cmake`, `ninja-build`, `pkg-config`, and `gdb`. |

The Azure CLI comes from Debian rather than from Microsoft's Azure CLI package feed. Microsoft's installation page lists Debian 11 and 12 as the tested Debian releases for that feed and no Debian 13 entry, while Debian 13 packages `azure-cli` 2.74.0-1 and `python3-azext-devops` 1.0.1-1 (both architecture-independent). Microsoft's page named version 2.91.0 as current, so the packaged Azure CLI trails upstream. The trade is a supported Debian 13 install at an older version. Debian's own repositories deliver the package and its signature chain, so the Azure toolchain adds no Microsoft key.

The .NET SDKs come from Microsoft's feed. Microsoft's .NET installation page lists .NET 8, 9, and 10 for Debian 13 and publishes `packages-microsoft-prod.deb` for Debian 13 at `packages.microsoft.com/config/debian/13/`. The recipe downloads that file over HTTPS and installs it. The Microsoft signing key is not pinned (see [Honest limits](#honest-limits)).

Playwright's system requirements list Debian 12 and 13 on x86-64 and arm64.

Sources, checked on 2026-10-14:

- https://packages.debian.org/trixie/azure-cli and https://packages.debian.org/trixie/python3-azext-devops
- https://learn.microsoft.com/en-us/cli/azure/install-azure-cli-linux (apt tab)
- https://learn.microsoft.com/en-us/dotnet/core/install/linux-debian
- https://playwright.dev/docs/intro#system-requirements

## Build and tags

`image.Build` and `image.BuildWithToolchains` in `image/build.go` take a context, an optional source file system (the embedded context when nil), and a Podman runner that the caller binds to the target its preflight checked. They do the following:

1. They materialize the complete context in a temporary directory, with text normalized to LF while `.bin` and NUL-containing files keep their bytes.
2. They hash the files they wrote, not the source, so the tag matches what Podman receives even if the source changes during the build.
3. They check that `base/Containerfile` is a regular file.
4. For the base image they run `podman build` with `base/Containerfile` as the file, the base tag, and `base` in the temporary directory as the build context. For a toolchain set they first write `toolchains/Containerfile` after hashing: `ARG BASE_IMAGE=` with the base tag, `FROM ${BASE_IMAGE}`, the fragments of the selected toolchains in sorted order, and a final step that runs `record-versions.sh`. The build context is the whole temporary directory.
5. They remove the temporary directory whether the build succeeds or fails.

They return the tag only when both the build and the cleanup succeed. A cleanup failure is joined to any build error and clears the tag.

`image.Tag` derives `localhost/sandboxed-agents:SET-HASH` from the asset hash and a toolchain set. `SET` is the canonical set: the toolchain names sorted, deduplicated, and joined with `-`. The base image has no toolchains and uses `SET=none`. `Tag` does not validate names; the caller checks the set against the supported toolchain names first. The tag does not include the controller group, so every group shares the images with the same asset hash and set. Because every toolchain image is built `FROM` the base image, different sets share the base layers.

### Labels

The CLI adds labels and Podman flags to each build; the Go API leaves them to its caller. Every image carries:

| Label | Value |
|---|---|
| `io.sandboxed-agents.image` | `sandboxed-agents` |
| `io.sandboxed-agents.asset-sha256` | The asset hash of the executable that built it |
| `io.sandboxed-agents.toolchains` | The set, comma-separated and sorted, or `none` |
| `io.sandboxed-agents.base-image` | Toolchain images only: the ID of the base image they were built on |

A sandbox container records its set in `io.sandboxed-agents.toolchains` as well. Nothing about images is stored in host state. `build` finds images by these labels.

## Build, refresh, and reuse

`build [--with ...]` always builds anew, so a new build installs current packages. With or without `--with`:

1. It lists the images that carry the marker label and the asset hash of the running executable, and reads each image's toolchain set. The result is every existing toolchain set of this executable, in any controller group. `--with a,b` adds that set when no image exists for it. Images from other executables (other asset hashes) are neither found nor rebuilt.
2. It rebuilds the base image with `--no-cache --pull=always`, so Podman pulls `debian:trixie-slim` again.
3. It rebuilds each toolchain set in sorted order on the new base image with `--no-cache --pull=never`, even when the tag already exists.

Failures follow two rules. When the base image fails to build, including a failed key check, `build` stops and builds no toolchain image. When a toolchain image fails to build, `build` continues with the remaining sets, then names the failed sets and exits non-zero. A failed build never replaces its tag, so an image that was not rebuilt stays usable under its tag. A successful rebuild moves the tag to the new image; the previous image loses the tag but still backs the sandboxes created from it.

Once the base image has been rebuilt, `build` prints that existing sandboxes keep their image until `update`, and that `list` marks them as outdated. It prints this also when a toolchain image failed. Because images are shared, the rebuild affects the sandboxes of every controller group. `build` followed by `update` brings a sandbox to fresh packages; see [Updating a sandbox](#updating-a-sandbox).

`up` builds only a missing image and may use the layer cache. A toolchain image counts as missing when its base image label differs from the ID of the current base image, so `up` never creates a sandbox from a toolchain image built on a superseded base. It never rebuilds an existing, current image.

### Recorded set and outdated sandboxes

`up` on an existing sandbox starts it when `--with` is absent or names the recorded set. A different set exits non-zero without starting the sandbox and names `update NAME --with SET`. The recorded set is the container's `io.sandboxed-agents.toolchains` label. A sandbox created before that label existed falls back to the set in the tag of its `Config.Image` name. Only an explicit `--with` needs a recorded set: when neither source yields a valid set, `up --with` fails, while `up` without `--with` still starts the sandbox.

`list` shows each sandbox's set, read the same way. It marks a sandbox as outdated when the immutable image ID of its container differs from the ID of the current image for that set, or when that toolchain image's `io.sandboxed-agents.base-image` label differs from the ID of the current base image. The second case arises when `build` rebuilt the base image and a toolchain build failed. A base rebuild therefore makes every older sandbox outdated, including its toolchain sets whose own rebuild failed.

### No image removal

No command removes an image, and the executable keeps no list of them. Old images accumulate in Podman's storage after rebuilds and new executable versions. Remove unused ones yourself, for example with `podman image prune`. Pruning cannot remove an image that a sandbox still uses.

## Development builds with the manager

The base recipe copies `manager.bin` into the image. In the source tree that file is an empty placeholder, because the manager is compiled at build time. A host executable built with a plain `go build` embeds the empty file, so an image build stops at the guard step with `Manager missing: build the host executable with image/cmd/overlay.` Its asset hash also differs from a prepared executable's, because the hash covers `manager.bin`.

To build a host executable whose images work, run these from the repository root with the Go version from `go.mod` on `PATH`:

```sh
image_build_context=$(mktemp -d)
trap 'rm -rf "$image_build_context"' EXIT
go run ./image/cmd/overlay "$image_build_context"
go build -overlay "$image_build_context/overlay.json" -o sandboxed-agents ./cmd/sandboxed-agents
```

`image_build_context` is an empty directory outside the checkout, removed when the shell exits. The overlay command writes the normalized context into it, compiles `cmd/sandboxed-agents-manager` for Linux amd64 as `$image_build_context/context/base/manager.bin`, and writes `overlay.json`. Keep the directory until the `go build` has finished. The checkout stays unchanged. `scripts/release.sh` does the same for release builds.

## Tests and limits

The offline test is run with `bash image/test/nodesource/test-check-nodesource-key.sh`. It needs no network. It checks that the key with the expected fingerprint is accepted, that a different fingerprint is refused with both fingerprints named, that a file with two primary keys is refused with the count and both fingerprints named, that an empty file is refused, that the fingerprint file holds 40 uppercase hex characters followed by one LF, and, statically, that the recipe runs the check before it writes the keyring and the source list.

`go test ./image ./internal/cli ./cmd/sandboxed-agents` runs offline with a fake Podman and builds no image. It covers tag derivation, the generated toolchain recipes, the base recipe contents, the manager overlay, toolchain validation, and the `build` and `up` behavior described above.

`go test ./image ./internal/manager` also runs offline. `TestImageStartupInitializesSSHHostKeys` executes the embedded entrypoint under a real shell and OpenSSH, with `ssh-keygen -f` redirected to a temporary prefix. With `sshd` replaced by a recording script, it checks that the key exists before sshd starts, that a second start keeps it, that a failing `ssh-keygen` stops the startup before sshd, and that the startup shell `exec`s sshd with `-D -e -f /usr/local/etc/sandboxed-agents/sshd_config` under the startup process ID. `TestImageBuildDeclaresSSHD` checks that the materialized recipe declares that entrypoint and copies `sshd_config` to that path, and that the materialized file holds the three host-key lines and the policy settings. `TestImageSSHPolicy` parses the embedded `sshd_config` with the host's real `/usr/sbin/sshd -T` against a temporary host key and checks the effective settings: only `agent`, public-key authentication alone against `/etc/ssh/authorized_keys`, no `AuthorizedKeysCommand`, root login, passwords, keyboard-interactive authentication, and agent forwarding off, PAM on, and the internal SFTP subsystem. It skips where `/usr/sbin/sshd` or `ssh-keygen` is missing. `TestSSHHostKeyWaitsForStartup` and `TestSSHHostKeyFailsClosed` check the bounded wait for a missing or empty public key, and that a permission error or malformed key fails at once with no key printed. These tests run no Podman and no container, so they do not prove the volume mount, a real sshd start, or an SSH login. The volume mount and the sshd start are proven by the live runs in [Live proof](#live-proof); an SSH login is not.

### Live image tests

The live image coverage creates one sandbox per toolchain and one without. Right after each `up` it runs the manager's `ssh-host-key` query as root, then checks the SSH server: the container is running with the user `0:0`, PID 1 is `sshd` with UID 0, port 22 is published once, on `127.0.0.1` at the port recorded in the container's `io.sandboxed-agents.port` label, and an unauthenticated `ssh -v` from the host to that port completes the key exchange (`SSH2_MSG_NEWKEYS received`) and records in a fresh `known_hosts` the same Ed25519 key that the manager query returned. For the base sandbox it repeats the key comparison and the server check after `stop` and `start`. The exchange proves a reachable sshd with the volume's host key, not a login, because every authentication method is off on the client side. It runs each smoke check inside its sandbox as `agent` with `shell NAME`, which is a Podman `exec` and not an SSH session, calls `build` once, and then checks the labels, the shared base layers, that existing sandboxes keep their image, and that `list` marks them outdated. It compiles its own prepared executable and uses a new controller group. Because `build` renews every image of the current executable on the Podman host, the coverage has its own opt-in, `SANDBOXED_AGENTS_LIVE_IMAGES_TESTS=1`, and needs no other variable:

```sh
SANDBOXED_AGENTS_LIVE_IMAGES_TESTS=1 \
  go test ./cmd/sandboxed-agents -run '^TestLiveImages$' -timeout 90m -v
```

The suite removes its sandboxes, volumes, and group state but none of the images it builds. After every invocation, pass or fail, the host agent of the `live-validation` skill measures the disk, removes the run's unused images by ID, and reports what it kept ([Image cleanup](../.agents/skills/live-validation/SKILL.md#image-cleanup)).

### Live proof

The current proof for the image, its foreground sshd ([#49](https://github.com/grauzone-labs/sandboxed-agents/issues/49)), and the host-key startup is one run of `TestLiveImages` on each supported platform. Both ran on 2026-10-15 on commit `f577a28` (`f577a2885026b2ed46c399874f16029013b84957`), both passed with exit status 0, and both compiled the same executable asset hash, `04de2cbb88dec2d7894ce2076f4bfb54098fad9ae442f4b0168012c4bb48bdef`.

| | Linux | Windows |
|---|---|---|
| Host | Debian 13.6 (trixie), kernel 7.0.14-20-pve, amd64 | Windows 11 (build 10.0.26300.9457), x64 |
| Podman | 5.4.2, rootless, cgroup v2, `systemd` manager | client 5.8.3; rootless WSL2 machine with Podman 5.8.9, cgroup v2, `cgroupfs` manager; WSL 2.7.12.0, kernel 6.18.33.2-2 |
| Go | 1.27.2 | 1.27.2 |
| OpenSSH client | 10.0p2 (Debian-7+deb13u4) | OpenSSH for Windows 9.5p2 |
| Run (UTC) | 21:52 to 22:13 | 21:18 to 22:05 |
| Result | `--- PASS: TestLiveImages (1254.04s)`, package `1254.968s` | `--- PASS: TestLiveImages (2787.61s)`, package `2793.365s` |

Both runs proved, on their hosts:

- `check` passed its preflight, and `up` built the base image and the four toolchain images and created five running sandboxes, one per set.
- Right after each `up`, the manager's `ssh-host-key` query returned a key; the container ran as user `0:0`; PID 1 was `sshd` with UID 0; port 22 was published once on `127.0.0.1` at the labelled port; and an unauthenticated `ssh -v` from the host completed the key exchange (`SSH2_MSG_NEWKEYS received`) and recorded the same Ed25519 key in a fresh `known_hosts`.
- The base sandbox kept its host key across `stop` and `start`, and the server checks passed again after the restart.
- Each sandbox's smoke check ran as `agent` through `shell`, a Podman `exec`: the base image's programs, Debian 13, Node.js 24, and the manager; `dotnet --list-sdks`; Chromium, Firefox, and WebKit launched through Playwright; `az version` and `az devops --help`; and a C program compiled with gcc.
- Every image carried the labels, every toolchain image shared the base image's layers, and the base image held no baked SSH host keys.
- One `build` rebuilt all five images, the existing sandboxes kept their previous image, and `list` marked them outdated.
- An inspection after each run, separate from the test's own cleanup, found no container, volume, or host state of the run's controller group, no other container on the machine, and a responsive rootless Podman API.

The runs did not prove an authenticated SSH login: every client authentication method is off in the key-exchange check, and no SSH setup was installed. They also ran no `agents` command, installed no agent, and ran no integration workflow. Those stay unverified live; see [agents](agents.md#limits) and [integrations](integrations.md). Both hosts ran Podman well above the floors in [host prerequisites](host-prerequisites.md); the floors themselves remain untested live.

**Resource limits.** During each run, the base sandbox's container was inspected separately. On both hosts the files `cpu.max`, `memory.max`, and `pids.max` under `/sys/fs/cgroup` inside the container read `400000 100000`, `8589934592`, and `2048`, which are the executable's defaults of 4 CPUs, 8 GiB memory, and 2048 processes; on Windows, Podman's inspect output recorded the same defaults. The kernel therefore enforced the limits on both machines, which the preflight's controller list alone does not show.

**WSL version on the Windows host.** Before the passing run, the same host had WSL 3.0.3.0 (kernel 6.18.54.1-1), and `check` failed: the Podman machine's user session sat under a `/non-systemd/` cgroup hierarchy in which the session scope had no `cgroup.controllers` file and the user manager's was empty, so the preflight reported the delegated `cpu`, `memory`, and `pids` controllers as required. The machine's `wsl.conf` held only its default user, and systemd reported `Delegate=yes` with `cpu io memory pids` for the user manager. Adding `[boot] systemd=true` to `wsl.conf` was tried and then reverted, because the machine no longer started with it; after the revert, `wsl.conf` matched its backup byte for byte. With the user's authorization the host was then downgraded to WSL [2.7.12](https://github.com/microsoft/WSL/releases/tag/2.7.12) from Microsoft's signed MSI, after exporting the machine distribution. Afterwards the user manager's `cgroup.controllers` and `cgroup.subtree_control` both listed `cpu io memory pids`, and the run above passed. The passing run therefore used the original `wsl.conf` and the machine's unchanged `cgroup_manager = "cgroupfs"` setting. This is an observation on one host, not a recommendation of a WSL version, and the executable does not repair it: the preflight only reports the missing delegation. Related upstream reports are [podman #29749](https://github.com/podman-container-tools/podman/issues/29749) and [WSL #41492](https://github.com/microsoft/WSL/issues/41492).

**Azure CLI warnings.** The `azure` smoke passed on both hosts, but `az version` printed two errors beside Python `FutureWarning` lines and an update notice: `Error loading command module 'monitor': ord() expected string of length 1, but int found` and `Error loading command module 'rdbms': No module named 'azure.mgmt.rdbms.mysql_flexibleservers'`. `az version` still exited zero with a non-empty `azure-cli` version, and `az devops --help` printed the DevOps command groups, which is what the smoke checks. The `monitor` and `rdbms` command groups failed to load in the Debian-packaged Azure CLI 2.74.0 and are not covered by any test.

### Honest limits

- Before the runs above, `TestLiveImages` had passed on one Linux host and one Windows host, as reported from output excerpts only: Linux `--- PASS: TestLiveImages (1107.23s)` with package `1107.779s`, and Windows `--- PASS: TestLiveImages (2710.16s)` with package `2716.322s`. The excerpts name no commit, host Podman version, or date, and those runs predate the SSH host-key startup change and the foreground sshd entrypoint of #49, so they prove neither. The runs in [Live proof](#live-proof) supersede both excerpts.
- An attempt to run `TestLiveImages` in the development workspace on 2026-10-15 failed at the preflight before any build: Podman is 4.3.1, below the Linux minimum of 4.4.0 in [host prerequisites](host-prerequisites.md); `pasta` is not on `PATH`; and the cgroup `cpu`, `memory`, and `pids` controllers exist only at the host root, not delegated to the user, so the delegation check fails. That host was not upgraded to clear these.
- The base-image build against the NodeSource key on 2026-10-11, described above, predates the toolchain recipes and the manager. It is no longer the only live build on record.
- Not covered: the Microsoft signing key for the .NET feed (`packages-microsoft-prod.deb` is fetched over HTTPS and not pinned), digests of the pulled Debian image or of built images, and pinned Playwright, .NET patch, or Debian package versions. The version manifest records what a build installed.
- Podman images in release preprocessing: `scripts/release.sh` builds none.

## Updating a sandbox

`update NAME` moves one sandbox to the image of the installed executable. It keeps the workspace, home data, SSH server state, loopback port, resource limits, and toolchain set. `update NAME --with dotnet,native` replaces the set; `--with none` selects the base image. An explicit `--with` also replaces a recorded set that cannot be read; without `--with`, an unreadable recorded set refuses the update, and an invalid `--with` set fails before Podman is called. A current image prints `already up to date` without asking about agent sessions or replacing the sandbox.

The command checks prerequisites, takes the sandbox's lifecycle lock, checks ownership and state, and builds any missing image before changing the sandbox. A toolchain image based on an older base image is rebuilt first. A running agent session blocks replacement unless `--force` is given. When the manager cannot answer, the command also requires `--force`, since it cannot rule out sessions.

During replacement, the old container becomes a backup. The replacement uses the same volumes and is created before the old container stops; it starts only after that stop succeeds. Readiness requires the manager's version response and a completed Ed25519 SSH key exchange through the recorded port. Pending probes retry every 250 milliseconds, with a 3-second limit per probe and a 60-second total deadline. The SSH probe uses temporary known_hosts data and does not change the installed SSH setup.

A create, start, or readiness failure removes the replacement and restores the old container's name and prior running/stopped state. No update or rollback path removes a named volume. Sessions ended by stopping the old container cannot be restarted by rollback; the output names them, or says their names were unavailable. A stopped sandbox is started for readiness and stopped again after successful backup removal.

If backup removal fails, the update succeeded but the sandbox is `update interrupted`; other commands refuse it, and the output says that `update NAME` cleans up. A sandbox that was stopped before the update is stopped again only after the backup is removed, as issue #28 requires, so that failure also leaves the new container running, and the output says so. The output also names the manual alternative, `podman rm sandboxed-agents-backup.GROUP.NAME` on Linux or `podman --connection 'CONNECTION' rm sandboxed-agents-backup.GROUP.NAME` in PowerShell on Windows with the connection the preflight selected; it applies only to this case, where the backup is the superseded container. If the final stop fails, the update succeeded but the sandbox is still running; `stop NAME` stops it.

`update NAME` on a sandbox with a leftover backup recovers the interrupted update instead of updating, without building an image. Under the lifecycle lock it first checks the owner of the container, the backup, and the volumes. An owner conflict is reported first, naming the foreign objects and pointing to Podman, and nothing is restored or removed. The command then judges the objects present, because Podman does not record how an interrupted state arose:

- **Running backup.** The old container was never stopped, so the replacement never started. The replacement is removed without being started, the backup is renamed back to the sandbox's name, and the old sandbox keeps running. The command exits non-zero without updating.
- **Stopped backup and a supported replacement.** The replacement is started when it is stopped, and the command waits for readiness. When the replacement is ready, the update counts as done: the backup is removed, the replacement is stopped again when its recorded prior state label says the sandbox was stopped before, and the command exits zero.
- **Replacement that cannot start or fails readiness.** The replacement is removed, the backup is renamed back, and the old container is started when its prior state was running. The command says that the sandbox was restored and exits non-zero without updating; the next `update NAME` updates normally.
- **Unknown prior state.** A stopped backup whose replacement is absent or carries no valid prior state label cannot be classified. The sandbox is restored and stays stopped, and the output says that the previous state could not be determined and names `start NAME`.

Some conditions still retain the backup and refuse. A backup without a recorded running state always does. A replacement that the command would have to start or probe is refused when its running state is missing, its recorded SSH port is invalid, or its recorded storage is unsupported: exactly one mount each at `/workspace`, `/home/agent`, and `/etc/ssh` and no other, where home and SSH server state are the sandbox's own named volumes among the owner-checked volumes, and the workspace is either that named volume or a bind of a non-empty host path. Replacement and cleanup share that check, which reads no resource limit, toolchain set, or image, so a replacement attached to another sandbox's volume is never started or probed. These refusals, like the owner conflict, change nothing and point to Podman. If a restore step itself fails, the output names the step and the retained backup, and the next `update NAME` judges the objects afresh. Recovery changes no volume. A backup that stays retained may be the only good container and must not be removed by hand.

One window remains in every update: an interruption after the backup is removed and before the final stop leaves a sandbox that was stopped before the update running under its name with no backup. Issue #28 fixes that order, and a failed final stop counts the update as done, so this version treats such a sandbox as updated and running; `stop NAME` restores the stopped state.

Offline tests cover replacement, failures, session guards, lifecycle locking, toolchain changes, the readiness deadline, and the completion of a leftover backup when the replacement is ready: all four combinations of prior and current running state, unsafe states and owner conflicts, start and removal failures with the retry that completes them, a final-stop failure that reports the update as done, a failed readiness check, the Windows connection, and the lifecycle lock. The recovery and `update --all` paths of issue #29 have offline tests through the executable CLI seam with a fake Podman: a running backup restored without starting the replacement or building, a backup without a replacement, a replacement that cannot start, an unknown prior state that stays stopped, owner conflicts that change nothing, and, for `update --all`, continuing after a failure, building every needed image before any sandbox changes, passing over volumes-only sandboxes and owner conflicts without a build, and the per-sandbox session guard with and without `--force`. These are offline results only; #29 has no live run. An explicit `--with` over an unknown, missing, or invalid recorded set is tested for `none` and `native` with success and with a start failure whose rollback restores the original container unchanged, and the override is shown to bypass neither usage, owner, nor mount validation. Cleanup storage validation is tested with a replacement on another owned sandbox's home volume, with wrong, missing, duplicate, and extra mounts and bind mounts at each destination for running and stopped replacements, none of which starts, probes, or changes anything, with a valid workspace bind that has no workspace volume, an unreadable toolchain set, and no memory limit, which cleans up, and with a foreign backup beside invalid storage, where the owner conflict is reported first. The real host SSH client is tested against a test peer with valid, corrupt, and absent exchange signatures. The SSH probe exchanges keys with the sshd that the image runs as its main process since issue #49 ([SSH server](#ssh-server)). On Linux hosts with `/usr/sbin/sshd`, `ssh`, and `ssh-keygen`, `TestReadinessWithImageSSHPolicy` runs the readiness wait against a real sshd on loopback, started with the embedded `sshd_config` and temporary host keys, with only the Podman manager response substituted. As root it runs the exact embedded policy; as another user it adds the test-only `-o UsePAM=no`, because `UsePAM yes` needs root, and says so in its log. Only the unprivileged variant has been run so far. The exchange ends before authentication, so the test proves the key exchange against the image's policy, not PAM authentication, and it is no live proof.

The opt-in `TestLiveImages` suite also checks one successful update and one Podman rollback caused by a deliberately failing SSH probe, plus host-key and authorization persistence. User-approved runs at commit `95a9abb` on 2026-10-16 passed on both project hosts: Linux `--- PASS: TestLiveImages (1601.03s)`, exit 0 (Podman 5.4.2 rootless on cgroup v2, OpenSSH 10.0), and Windows `--- PASS: TestLiveImages (2842.61s)`, exit 0 (Podman client 5.8.3 against a WSL machine, OpenSSH for Windows 9.5), each including the successful `image-none` update, the forced `image-native` rollback with its readiness-failure, restored-image, host-key, and authorized-key assertions, and the suite's removal of its containers, volumes, and host state. That proves issue #7's criterion of one successful update and one forced rollback on both hosts; the recovery of a leftover backup and `update --all` remain proven offline only. The run records are summarised in [pull request #66](https://github.com/grauzone-labs/sandboxed-agents/pull/66#issuecomment-6216134884). Two earlier Windows failures are history, not current limits: at commit `3104f28` the suite failed (`--- FAIL: TestLiveImages (2149.56s)`) after `update image-none` had succeeded, when the forced-rollback step stopped in the preflight with `podman machine ssh podman-machine-default id -u: exit status 255` before any readiness probe, consistent with the harness defect reproduced offline below though the live cause is unconfirmed; and the first Windows attempt at `95a9abb` failed in the build step with `server probably quit: unexpected EOF` and a stopped Podman machine, before the update path, with a cause the logs do not establish. Linux had passed at `3104f28` as well (`--- PASS: TestLiveImages (1596.95s)`). Earlier attempts on the implementation host, last at commit `52deafd`, failed at the preflight for lack of Podman 4.4, `pasta`, and delegated cgroup controllers. Later commits that change only documentation carry no live run of their own. The #49 [live proof](#live-proof) above comes from a different commit and covers `up`, `stop`, and `start`, not `update`.

The forced-rollback step prepends a fake `ssh` to `PATH` with `TEST_SSH_EXIT=255` so that the readiness probe fails. An offline process fixture that models the nested transport, `TestLiveUpdateFailurePreservesMachineSSH`, drives the suite's own command helper through a nested fake Podman, that `ssh` shim, and a saved fake standing in for the real client. With the shim as it was, the fixture failed in 0.14 seconds with the same `exit status 255` at preflight step 2, because the nested Podman's `machine ssh` ran through the shim; restoring only `PATH`, or only the shim's exit status, let the fixture's transport through. That is a harness defect observed in the fixture. The retained Windows log holds no record of which `ssh` the machine transport selected, so the live failure is consistent with this mechanism but not proven to have the same cause, and the run cannot rule out other factors. Production code is unchanged by the fix. The suite now records the absolute path of the real `ssh` before it prepends the shim, and the shim forwards every call to it as a direct process with the original arguments, standard streams, and exit status, except the readiness probe itself, identified by the selected sandbox's port, `agent@127.0.0.1`, and the temporary known_hosts option. The fixture passes for three machine queries on Windows and the direct probe on Linux, forwards nearby calls with another port, user, or policy, and preserves a path with spaces, the argument vector, standard input and error, and an exit status of 23. Its driver is built once beside the other test binaries at package start and reused from that directory by nested runs, with no new environment flag; the full offline suite passes with that change.

### Updating every sandbox

`update --all [--force]` takes no `NAME` and no `--with`; `update` with neither or both of `NAME` and `--all`, or `--all` with `--with`, fails with a usage message before any Podman call. After the preflight it reads one name inventory, `podman ps --all` and `podman volume ls`, and takes only the names of the current controller group's sandboxes, including those of which only volumes or only a backup remain ([ADR 0007](adr/0007-serialize-sandbox-lifecycle-commands.md#name-inventory-of-update---all)). The inventory supplies names and no state. For each name the command takes the sandbox's lifecycle lock and only then inspects its objects, so every decision rests on facts read under the lock. Every lock stays held until the command exits, across the inspection of all names, all builds, and every replacement or recovery.

Per sandbox, after the lock and a fresh inspection:

- A taken lock, an owner conflict, an unreadable state, or an unknown name is reported and counted as not updated. No image is built for an owner-conflicted or locked sandbox.
- A sandbox of which only volumes remain is passed over with a note naming `up NAME` and is not a failure.
- A sandbox with a leftover backup is recovered as `update NAME` recovers it. It needs no image.
- A sandbox whose image is current is reported as already up to date, with no build and no session check.
- Every other sandbox needs the image for its recorded toolchain set.

The command builds every needed image once per set before it changes any sandbox, recoveries included. If a build fails, no sandbox is changed and every pending sandbox is reported as not updated. After the builds it replaces or recovers the sandboxes one after another. The session guard runs per sandbox directly before its change: without `--force` a sandbox with a running agent session, or whose sessions cannot be determined, is skipped, reported with its sessions, and counted as not updated while the others continue; with `--force` each sandbox behaves as in `update NAME --force`, naming the ended sessions or saying that they cannot be named. A failed replacement is rolled back as in `update NAME`, a recovery that restores the old sandbox counts as not updated, and the command continues. It prints a summary of updated, current, skipped, and not updated sandboxes and exits non-zero when any sandbox was not updated; skipped volumes-only sandboxes do not count. While it runs, other lifecycle commands on the sandboxes it holds are refused, and a second `update --all` passes them over.
