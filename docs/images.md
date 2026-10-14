# Images

This document describes the base image recipe in `image/context/base/`, the four toolchain recipes in `image/context/toolchains/`, how `build` and `up` produce and refresh images, and how the base recipe checks the NodeSource signing key before it trusts the NodeSource package repository. The host executable embeds the image build context, as described in [ADR 0001](adr/0001-go-for-host-executable-and-in-container-manager.md).

`image/assets.go` owns the `go:embed` directive. Its `Context` API supplies a read-only context with CRLF text line endings normalized to LF. At build time, `.bin` files and NUL-containing files preserve the bytes already present in the tree. `.gitattributes` marks `.bin` files as binary and forces other context files to text, so binary assets must use `.bin` to preserve exact bytes through Git. The asset hash includes sorted relative paths and normalized contents with length prefixes; it excludes file modes and timestamps. `sandboxed-agents version` prints the version followed by `asset-sha256 HASH`.

Run `bash scripts/release.sh VERSION OUTPUT_DIRECTORY` with the Go version from `go.mod` on `PATH` to build static Linux and Windows AMD64 executables. The script runs `image/cmd/overlay` to prepare normalized files in a temporary directory and uses a Go build overlay, so the release executable embeds LF bytes even from a CRLF checkout. The checkout stays untouched and the temporary files are removed. A plain `go build` normalizes the context at runtime but embeds no manager, so its images cannot build; see [Development builds with the manager](#development-builds-with-the-manager). Use the release script for normalization before embedding and reproducible binary bytes. The script also emits `SHA256SUMS` and the `SandboxedAgents.VERSION.nupkg` [NuGet command package](nuget-package.md), which contains the Windows executable, its checksum manifest, and the PowerShell 7 install and remove scripts; npm packaging is not implemented. `VERSION` must be NuGet-compatible SemVer: `X.Y.Z` with optional prerelease and build metadata. The package filename omits any `+build` suffix while the nuspec keeps the full version, so `1.2.3+abc` produces `SandboxedAgents.1.2.3.nupkg` with version `1.2.3+abc` in its nuspec.

`.gitattributes` keeps `go.mod`, `scripts/*.sh`, `scripts/version-symbol.txt`, `packages/nuget/**`, all `*.go` files, and `.github/workflows/*.yml` and `*.yaml` checkouts at LF, so `gofmt` stays clean under `core.autocrlf=true`. The release tests still rewrite their inputs to CRLF to prove reproducibility.

Inside a Git worktree, `VERSION` requires the literal tag `vVERSION` to point at `HEAD`; annotated tags are peeled to their commit. For example, version `1.2.3` requires `v1.2.3`. The worktree must also be clean, including tracked edits and untracked files. Missing or mismatched tags and dirty worktrees stop the release before building. If the source root has a `.git` entry, Git must be available to verify it. Source directories outside Git remain buildable.

`go test ./scripts -count=1 -v` compares builds of the same tagged temporary commit with LF and CRLF assets, all Go sources, and module/linker metadata. On Windows, set `SANDBOXED_AGENTS_RELEASE_BASH` to the Git for Windows executable, such as `C:\Program Files\Git\bin\bash.exe`. `TestWindowsReleaseBuildsAreReproducible` and `TestWindowsNuGetCommandPackageLifecycle` skip with a reason when the setting is unset and fail when an explicitly configured path is not an existing file. The reproducibility test runs the Windows executable's `version` command. Linux tests run on Linux; the Windows tests skip other hosts.

`.github/workflows/release-reproducibility.yml` runs Linux and native Windows jobs on Blacksmith for pushes to `main`, pull requests, and manual runs. Each runner creates a local CI tag at its checkout's commit. The Windows job executes the Windows binary, compares its asset hash with Linux, and compares the SHA256 of both release executables with their Linux-built counterparts. Those runner tags are not pushed.

Release limitations:

- Local verification executes the Linux binary and checks cross-compiled Windows bytes and the PE AMD64 machine field; this Linux host cannot execute the Windows binary.
- CI runs the Windows binary on Blacksmith with configured Git for Windows Bash, comparing its asset hash and both executable SHA256 digests with Linux outputs. Native Windows verification belongs to CI.
- The clean-worktree check rejects tracked edits, untracked files, and ignored files under `image/context/` or other release inputs; ignored files elsewhere are allowed.
- A plain `go build` from a CRLF checkout embeds raw CRLF bytes and normalizes the context only at runtime. The release script normalizes text before embedding.
- [ADR 0001](adr/0001-go-for-host-executable-and-in-container-manager.md) requires building the manager for Linux before the host executable. The release script does this through `image/cmd/overlay`, which compiles `cmd/sandboxed-agents-manager` for Linux amd64 into the overlay context. Both host executables embed those same bytes. An offline test (`go test ./image`) checks that the embedded manager is a static Linux amd64 ELF file and is identical from LF and CRLF checkouts.
- NUL detection preserves tree bytes only during build normalization. Git's forced-text attributes can change line endings in non-`.bin` files, including NUL-containing files. Every binary asset must use `.bin` for exact bytes through Git; this naming convention has no further enforcement or content validation.

## Base image

The base image is Debian 13 (trixie) slim, which uses glibc, with Node.js 24 LTS installed on top. Debian 13 is the current stable release, while Debian 12 is only in its LTS period. Node.js 24 remains an LTS release.

Every toolchain's packages are available for Debian 13. The sources, checked on 2026-10-14, are in [Toolchains](#toolchains). Only a live build proves the images, and none of the toolchain images has been built live yet (see [Honest limits](#honest-limits)).

The base image contains:

- OpenSSH server and client, Git, GitHub CLI, tmux, `curl`, `jq`, `ripgrep`, `less`, `unzip`, `zip`, `xz-utils`, `file`, `procps`, Bash, coreutils, findutils, `gnupg`, and CA certificates.
- Node.js 24 and npm from the NodeSource repository `node_24.x`, installed after the [signing key check](#nodesource-signing-key-check).
- The user `agent` with UID and GID 1000, the directory `/workspace` owned by `agent`, and no SSH host keys.
- The in-container manager at `/usr/local/bin/sandboxed-agents-manager`.
- No agent and none of the toolchains below.

### Installed version manifest

Every image records the versions of what it installed in `/usr/local/share/sandboxed-agents/versions.txt`. The base image writes `/etc/os-release`, every Debian package with its version, the Node.js and npm versions, and the manager version. A toolchain image rewrites the same file with `image/context/toolchains/record-versions.sh`, which adds the output of `dotnet --list-sdks`, `playwright --version`, `az version`, and `cc --version` for the toolchains present.

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
7. Prepare the build context as in [Development builds with the manager](#development-builds-with-the-manager), build the base image with rootless Podman, `podman build -f TEMP/context/base/Containerfile TEMP/context/base`, and check that apt accepts the repository. The `manager.bin` in the source tree is an empty placeholder, so a build from the checkout itself stops at the manager guard.
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

Once the base image has been rebuilt, `build` prints that existing sandboxes keep their image until `update`, and that `list` marks them as outdated. It prints this also when a toolchain image failed. Because images are shared, the rebuild affects the sandboxes of every controller group. `build` followed by `update` brings a sandbox to fresh packages; the `update` command itself belongs to issue #28.

`up` builds only a missing image and may use the layer cache. A toolchain image counts as missing when its base image label differs from the ID of the current base image, so `up` never creates a sandbox from a toolchain image built on a superseded base. It never rebuilds an existing, current image.

### Recorded set and outdated sandboxes

`up` on an existing sandbox starts it when `--with` is absent or names the recorded set. A different set exits non-zero without starting the sandbox and names `update NAME --with SET`. A sandbox with no recorded set also fails.

`list` shows each sandbox's set. It marks a sandbox as outdated when its container was not created from the current image for that set, or when that toolchain image's `io.sandboxed-agents.base-image` label differs from the ID of the current base image. The second case arises when `build` rebuilt the base image and a toolchain build failed. A base rebuild therefore makes every older sandbox outdated, including its toolchain sets whose own rebuild failed.

### No image removal

No command removes an image, and the executable keeps no list of them. Old images accumulate in Podman's storage after rebuilds and new executable versions. Remove unused ones yourself, for example with `podman image prune`. Pruning cannot remove an image that a sandbox still uses.

## Development builds with the manager

The base recipe copies `manager.bin` into the image. In the source tree that file is an empty placeholder, because the manager is compiled at build time. A host executable built with a plain `go build` embeds the empty file, so an image build stops at the guard step with `Manager missing: build the host executable with image/cmd/overlay.` Its asset hash also differs from a prepared executable's, because the hash covers `manager.bin`.

To build a host executable whose images work, run these from the repository root with the Go version from `go.mod` on `PATH`:

```sh
go run ./image/cmd/overlay "$TEMP"
go build -overlay "$TEMP/overlay.json" -o sandboxed-agents ./cmd/sandboxed-agents
```

`TEMP` is an empty directory outside the checkout. The overlay command writes the normalized context into it, compiles `cmd/sandboxed-agents-manager` for Linux amd64 as `$TEMP/context/base/manager.bin`, and writes `overlay.json`. Keep the directory until the `go build` has finished. The checkout stays unchanged. `scripts/release.sh` does the same for release builds.

## Tests and limits

The offline test is run with `bash image/test/nodesource/test-check-nodesource-key.sh`. It needs no network. It checks that the key with the expected fingerprint is accepted, that a different fingerprint is refused with both fingerprints named, that a file with two primary keys is refused with the count and both fingerprints named, that an empty file is refused, that the fingerprint file holds 40 uppercase hex characters followed by one LF, and, statically, that the recipe runs the check before it writes the keyring and the source list.

`go test ./image ./internal/cli ./cmd/sandboxed-agents` runs offline with a fake Podman and builds no image. It covers tag derivation, the generated toolchain recipes, the base recipe contents, the manager overlay, toolchain validation, and the `build` and `up` behavior described above.

### Live image tests

The live image coverage creates one sandbox per toolchain and one without, runs each smoke check inside its sandbox as `agent` with `shell NAME`, calls `build` once, and then checks the labels, the shared base layers, that existing sandboxes keep their image, and that `list` marks them outdated. It compiles its own prepared executable and uses a new controller group. Because `build` renews every image of the current executable on the Podman host, the coverage needs its own opt-in; the generic live opt-in does not select it:

```sh
SANDBOXED_AGENTS_LIVE_TESTS=1 SANDBOXED_AGENTS_LIVE_IMAGES_TESTS=1 \
  go test ./cmd/sandboxed-agents -run '^TestLiveImages$' -timeout 90m -v
```

`SANDBOXED_AGENTS_LIVE_IMAGES_TESTS=1` is the variable that selects it.

### Honest limits

- The live image coverage has not run for this change. The development host has Podman 4.3.1, below the Linux minimum of 4.4.0 in [host prerequisites](host-prerequisites.md), so its preflight stops there. No toolchain image has been built, and no smoke check has run inside one.
- Nothing about this change has been run on Windows 11.
- The one live image build on record is the base-image build against the NodeSource key on 2026-10-11, described above. It predates the toolchain recipes and the manager.
- Package availability for Debian 13 rests on the vendor pages listed under [Toolchains](#toolchains), not on a build.
- Not covered: the Microsoft signing key for the .NET feed (`packages-microsoft-prod.deb` is fetched over HTTPS and not pinned), digests of the pulled Debian image or of built images, and pinned Playwright, .NET patch, or Debian package versions. The version manifest records what a build installed.
- Podman images in release preprocessing: `scripts/release.sh` builds none.
