# Maintainer notes

This page is for the maintainers of `sandboxed-agents`. Here you find how the Podman version floors were confirmed, the live runs and source checks on record, the provenance and rotation of the NodeSource signing key, how release builds and CI work, and how to build a development executable whose images work. The user pages link here wherever a statement rests on this evidence.

## How the version floors were confirmed

[Host prerequisites](host-prerequisites.md) states the floors: rootless Podman 4.4.0 on Linux amd64, and Podman client and machine 4.7.0 with a running rootless WSL2 machine on Windows 11 x64. They were confirmed from versioned upstream Podman documentation and source, not on a live host. The [live runs](#live-runs-on-record) ran on Podman 5.4.2 and on client 5.8.3 with machine 5.8.9, so they exercise the preflight at those versions and confirm nothing at the floors.

The sources are the upstream Podman repository (containers/podman) at the tags named below, and the Podman documentation site.

### Linux amd64

The floor follows from `--network=pasta:--no-map-gw`, which Podman 4.4.0 introduced. Every other Linux interface the executable uses is available at 4.4.0 or below.

- Network option `--network=pasta`: [network documentation](https://raw.githubusercontent.com/containers/podman/v4.4.0/docs/source/markdown/options/network.md), engine source ([`libpod/networking_pasta_linux.go`](https://raw.githubusercontent.com/containers/podman/v4.4.0/libpod/networking_pasta_linux.go)), and [release notes](https://raw.githubusercontent.com/containers/podman/v4.4.0/RELEASE_NOTES.md), all at v4.4.0. The engine source adds the supplied options and defaults to `--no-map-gw`.
- User namespace `keep-id` with `uid` and `gid`: [userns documentation](https://raw.githubusercontent.com/containers/podman/v4.3.0/docs/source/markdown/options/userns.container.md) and [`pkg/namespaces/namespaces.go`](https://raw.githubusercontent.com/containers/podman/v4.3.0/pkg/namespaces/namespaces.go) at v4.3.0. The [v4.2.1 parser](https://raw.githubusercontent.com/containers/podman/v4.2.1/pkg/namespaces/namespaces.go) recognizes only plain `keep-id`.
- `--user` for `run` and `exec`: [run](https://raw.githubusercontent.com/containers/podman/v1.0.0/docs/podman-run.1.md) and [exec](https://raw.githubusercontent.com/containers/podman/v1.0.0/docs/podman-exec.1.md) manuals at v1.0.0, and the [exec manual](https://raw.githubusercontent.com/containers/podman/v4.0.0/docs/source/markdown/podman-exec.1.md) at v4.0.0.
- `--security-opt=no-new-privileges`: [`cmd/podman/create.go`](https://raw.githubusercontent.com/containers/podman/v0.12.1/cmd/podman/create.go) at v0.12.1, and the `security-opt` documentation at [v4.4.0](https://raw.githubusercontent.com/containers/podman/v4.4.0/docs/source/markdown/options/security-opt.md) and [v5.0.0](https://raw.githubusercontent.com/containers/podman/v5.0.0/docs/source/markdown/options/security-opt.md).
- Resource limits: [run manual](https://raw.githubusercontent.com/containers/podman/v1.0.0/docs/podman-run.1.md) at v1.0.0, [release notes](https://raw.githubusercontent.com/containers/podman/v1.5.0/RELEASE_NOTES.md) and [`pkg/spec/spec.go`](https://raw.githubusercontent.com/containers/podman/v1.5.0/pkg/spec/spec.go) at v1.5.0 (rootless limits with cgroup v2), and the [run manual](https://docs.podman.io/en/v5.0.0/markdown/podman-run.1.html) at v5.0.0.
- Labeled named volumes, workspace binds, and label readback: [volume-create manual](https://raw.githubusercontent.com/containers/podman/v0.12.1/docs/podman-volume-create.1.md), [`libpod/runtime_ctr.go`](https://raw.githubusercontent.com/containers/podman/v0.12.1/libpod/runtime_ctr.go), and [inspect sources](https://raw.githubusercontent.com/containers/podman/v0.12.1/cmd/podman/shared/container.go) at v0.12.1, and the [inspect sources](https://raw.githubusercontent.com/containers/podman/v4.4.0/libpod/container_inspect.go) at v4.4.0.
- Loopback port publishing: [release notes](https://raw.githubusercontent.com/containers/podman/v1.1.0/RELEASE_NOTES.md) and [`libpod/networking_linux.go`](https://raw.githubusercontent.com/containers/podman/v1.1.0/libpod/networking_linux.go) at v1.1.0, and [`libpod/networking_pasta_linux.go`](https://raw.githubusercontent.com/containers/podman/v4.4.0/libpod/networking_pasta_linux.go) at v4.4.0.
- Info fields: `libpod/define/info.go` at [v3.2.0](https://raw.githubusercontent.com/containers/podman/v3.2.0/libpod/define/info.go), [v4.4.0](https://raw.githubusercontent.com/containers/podman/v4.4.0/libpod/define/info.go), and [v5.0.0](https://raw.githubusercontent.com/containers/podman/v5.0.0/libpod/define/info.go). The [v3.1.2 schema](https://raw.githubusercontent.com/containers/podman/v3.1.2/libpod/define/info.go) lacks the cgroup controllers.
- Cgroup controller discovery: the vendored [`containers/common` cgroups source](https://raw.githubusercontent.com/containers/podman/v4.4.0/vendor/github.com/containers/common/pkg/cgroups/cgroups_linux.go) at v4.4.0.

### Windows 11 x64

Epic #1 sets client and machine 4.7.0 as the supported minimum. The confirmation shows that 4.7.0 is the lowest version that supports every interface the executable uses on Windows.

- The WSL machine provider that Podman manages is available from 4.0.0: [v4.0.0 release notes](https://raw.githubusercontent.com/containers/podman/v4.0.0/RELEASE_NOTES.md).
- The machine list and inspect fields the executable reads (the default machine, the WSL provider, whether the machine is rootful, and its state) are available from 4.7.0: [`cmd/podman/machine/list.go`](https://raw.githubusercontent.com/containers/podman/v4.7.0/cmd/podman/machine/list.go) and [`pkg/machine/wsl/machine.go`](https://raw.githubusercontent.com/containers/podman/v4.7.0/pkg/machine/wsl/machine.go) at v4.7.0, [`cmd/podman/machine/inspect.go`](https://raw.githubusercontent.com/containers/podman/v5.0.0/cmd/podman/machine/inspect.go) at v5.0.0. The [v4.6.2 inspect schema](https://raw.githubusercontent.com/containers/podman/v4.6.2/pkg/machine/config.go) lacks the rootful field.
- The global `--connection` option with a Podman-managed WSL machine is available from 4.0.0: [v2.0.5 release notes](https://raw.githubusercontent.com/containers/podman/v2.0.5/RELEASE_NOTES.md), and `cmd/podman/root.go` at [v4.0.0](https://raw.githubusercontent.com/containers/podman/v4.0.0/cmd/podman/root.go) and [v5.0.0](https://raw.githubusercontent.com/containers/podman/v5.0.0/cmd/podman/root.go).
- The container options sent through the remote client (user, security options, volumes, labels, port publishing, and resource limits) are available from 4.0.0 with a Podman-managed WSL machine: the [run](https://raw.githubusercontent.com/containers/podman/v4.0.0/docs/source/markdown/podman-run.1.md) and [exec](https://raw.githubusercontent.com/containers/podman/v4.0.0/docs/source/markdown/podman-exec.1.md) manuals at v4.0.0 (`--volume` covers named volumes, directory binds, and remote-server paths), the Linux `keep-id` sources above (4.3.0), the Linux pasta sources above (4.4.0), and [`cmd/podman/common/netflags.go`](https://raw.githubusercontent.com/containers/podman/v5.0.0/cmd/podman/common/netflags.go) and [`pkg/domain/infra/tunnel/containers.go`](https://raw.githubusercontent.com/containers/podman/v5.0.0/pkg/domain/infra/tunnel/containers.go) at v5.0.0 for pasta through the remote client. Resource limits also depend on the machine's cgroup controllers being delegated.
- The guarantee that `--connection` takes precedence over `CONTAINER_HOST` is available from 4.8.0: [v4.8.0 release notes](https://raw.githubusercontent.com/containers/podman/v4.8.0/RELEASE_NOTES.md), and [`cmd/podman/root.go`](https://raw.githubusercontent.com/containers/podman/v5.0.0/cmd/podman/root.go) at v5.0.0. [ADR 0006](adr/0006-podman-target-and-execution-identity.md) has the executable remove `CONTAINER_HOST`, `CONTAINER_CONNECTION`, and `CONTAINER_SSHKEY` from the environment of its Podman calls, so the 4.8.0 guarantee does not raise the floor; it would be needed only if the executable stopped removing those variables.
- Machine command execution with `podman machine ssh` is available from 4.0.0: [`pkg/machine/wsl/machine.go`](https://raw.githubusercontent.com/containers/podman/v4.0.0/pkg/machine/wsl/machine.go) at v4.0.0 and the [`podman machine ssh` documentation](https://docs.podman.io/en/v5.0.0/markdown/podman-machine-ssh.1.html) at v5.0.0.

### Limits of the confirmation

- **Exact introduction versions on Linux.** For the user, security, and resource-limit options, availability below 4.4.0 is confirmed, but the release that introduced them is not. The first release that offers the label readback the executable needs is also not established. All of them are available below 4.4.0, so they do not raise the Linux floor.
- **Info query.** The info fields for rootless mode and the cgroup version are confirmed from 3.2.0. Podman's info output has no field for delegated cgroup controllers, and the version alone does not show it, so the preflight reads delegation itself from the user's `cgroup.controllers` and `cgroup.subtree_control` files (on Windows through `podman machine ssh NAME id -u` and `cat`). These queries passed in the live runs with Podman 5.4.2 and machine 5.8.9; nothing confirms them at the floors.
- **Engine version query.** `podman --version` reports the client only; the preflight reads `Server.Version` from `podman version --format json` through the selected machine connection. The query reported the machine's Podman 5.8.9 in the Windows live runs; nothing confirms it at 4.7.0.
- **Automount root query.** The Windows preflight runs `podman machine ssh NAME test -e /etc/wsl.conf`, then reads an existing file with `podman machine ssh NAME cat /etc/wsl.conf`. The Windows live runs exercised the read of an existing `wsl.conf` without an `[automount]` section, so the default root `/mnt/`, not an absent file or a custom root; nothing confirms the transport at the 4.7.0 floor. `podman machine ssh` runs commands in the machine from 4.0.0, and the automount setting itself does not depend on the Podman version.
- **Remote Podman on Linux.** On Linux the executable calls the local Podman without a connection. A remote setting there, such as `CONTAINER_HOST`, is a documented limit of the preflight ([ADR 0006](adr/0006-podman-target-and-execution-identity.md)).

## Live runs on record

The live suite is `TestLiveSuite` ([releases](releases.md#live-suite)). Before the suite existed, the live coverage was the test `TestLiveImages`, which the suite's `images` and `update` cases replaced. Every run below ran on the project's two hosts:

| | Linux host | Windows host |
|---|---|---|
| System | Debian 13.6 (trixie), kernel 7.0.14-20-pve, amd64, 8 vCPU, 16 GiB | Windows 11 (build 10.0.26300.9457), x64, 8 vCPU, 16 GiB; WSL 2.7.12.0, kernel 6.18.33.2-2 |
| Podman | 5.4.2, rootless, cgroup v2, `systemd` cgroup manager | client 5.8.3; rootless WSL2 machine with Podman 5.8.9, cgroup v2, `cgroupfs` manager |
| Go, OpenSSH | 1.27.2; OpenSSH 10.0p2 (Debian-7+deb13u4) | 1.27.2; OpenSSH for Windows 9.5p2 |

| Date (UTC) | Commit | Test | Linux | Windows | Record |
|---|---|---|---|---|---|
| 2026-10-15 | `f577a28` | `TestLiveImages`, image coverage; asset hash `04de2cbb…bdef` on both hosts | `PASS (1254.04s)` | `PASS (2787.61s)` | this page: [what the runs proved](#what-the-runs-proved) and the observations below |
| 2026-10-16 | `95a9abb` | `TestLiveImages`, with the successful `image-none` update and the forced `image-native` rollback | `PASS (1601.03s)` | `PASS (2842.61s)` | [pull request #66](https://github.com/grauzone-labs/sandboxed-agents/pull/66#issuecomment-6216134884) |
| 2026-10-16 | `4831f91` | `TestLiveSuite` with image coverage, every case `pass` | `PASS (2357.21s)` | `PASS (3308.43s)` | [pull request #74](https://github.com/grauzone-labs/sandboxed-agents/pull/74), the live suite records comment, with both JSON records |
| 2026-10-16/17 | `da065a2` | `TestLiveSuite` with image coverage, every case `pass` | `PASS (2218.65s)` | `PASS (3114.23s)` | [issue #71](https://github.com/grauzone-labs/sandboxed-agents/issues/71#issuecomment-6227867029), the baseline comment |

The `TestLiveSuite` runs wrote `live-suite-linux.json` and `live-suite-windows-11.json` with `image_part_selected`, `image_part_ran`, and `image_coverage_complete` true and the five checks `preflight`, `lifecycle`, `update`, `agent`, and `images` at `pass`. In the `agent` case `opencode` 1.19.3 was installed through npm, its session started in a Linux pseudo-terminal and a Windows pseudo console, detached with `Ctrl+b d`, reported by `agents status` as `session true`, stopped with `--stop`, and disabled. Each run left no container, volume, or host state of its controller group, and the images it built were removed afterwards by ID ([Image cleanup](../.agents/skills/live-validation/SKILL.md#image-cleanup)).

### What the runs proved

- `check` passed its preflight on both hosts, and `up` built the base image and the four toolchain images and created one running sandbox per toolchain set.
- Right after each `up`, the manager's `ssh-host-key` query returned a key; the container ran as user `0:0`; PID 1 was `sshd` with UID 0; port 22 was published once on `127.0.0.1` at the labelled port; and an unauthenticated `ssh -v` from the host completed the key exchange (`SSH2_MSG_NEWKEYS received`) and recorded the same Ed25519 key in a fresh `known_hosts`. The base sandbox kept its host key across `stop` and `start`.
- Each sandbox's smoke check ran as `agent` through `shell`, a Podman `exec`: the base image's programs, Debian 13, Node.js 24, and the manager; `dotnet --list-sdks`; Chromium, Firefox, and WebKit launched through Playwright; `az version` and `az devops --help`; and a C program compiled with gcc.
- Every image carried its labels, every toolchain image shared the base image's layers, and the base image held no baked SSH host keys. One `build` rebuilt all images, existing sandboxes kept their previous image, and `list` marked them outdated.
- One successful `update` and one forced rollback, with the readiness probe's `ssh` replaced by a failing one, restored the sandbox, its host key, and its authorized key, on both hosts.
- **Resource limits.** Inside the base sandbox, `cpu.max`, `memory.max`, and `pids.max` under `/sys/fs/cgroup` read `400000 100000`, `8589934592`, and `2048`, the defaults of 4 CPUs, 8 GiB, and 2048 processes; Podman's inspect output recorded the same. The kernel enforced the limits on both hosts, which the preflight's controller list alone does not show. Podman also applied its default swap allowance of twice the memory limit (`MemorySwap 17179869184`), which the executable does not set ([#76](https://github.com/grauzone-labs/sandboxed-agents/issues/76)).

### What the runs did not prove

- No authenticated SSH login: every client authentication method is off in the key-exchange check, and no SSH setup was installed. The SSH setup, VS Code Remote SSH, and the Azure browser tunnel are items of the [manual checklist](manual-checklist.md).
- No sign-in: the suite uses no credential and runs no login or integration workflow. Logins are manual checklist items.
- Nothing at the version floors: both hosts ran Podman well above them.
- The recovery of a leftover backup and `update --all` are proven offline only.

### Observations beside the runs

- **WSL version on the Windows host.** Before the first passing run, the host had WSL 3.0.3.0 (kernel 6.18.54.1-1) and `check` failed: the Podman machine's user session sat under a `/non-systemd/` cgroup hierarchy in which the session scope had no `cgroup.controllers` file and the user manager's was empty, so the preflight reported the delegated `cpu`, `memory`, and `pids` controllers as required. Adding `[boot] systemd=true` to the machine's `wsl.conf` was tried and reverted, because the machine no longer started with it. With the user's authorization the host was downgraded to WSL [2.7.12](https://github.com/microsoft/WSL/releases/tag/2.7.12) from Microsoft's signed MSI; afterwards both controller files listed `cpu io memory pids`, and the runs passed with the original `wsl.conf` and the machine's `cgroup_manager = "cgroupfs"`. This is an observation on one host, not a recommendation of a WSL version; the preflight only reports the missing delegation. Related upstream reports: [podman #29749](https://github.com/podman-container-tools/podman/issues/29749), [WSL #41492](https://github.com/microsoft/WSL/issues/41492).
- **Azure CLI warnings.** `az version` in the `azure` image printed `Error loading command module 'monitor'` and `Error loading command module 'rdbms'` beside Python `FutureWarning` lines, exited zero with a non-empty `azure-cli` version, and `az devops --help` printed the DevOps command groups. The `monitor` and `rdbms` groups of the Debian-packaged Azure CLI 2.74.0 are covered by no test.
- **Earlier failures, superseded.** At `3104f28` the Windows `TestLiveImages` run failed in the forced-rollback step at the preflight with `podman machine ssh podman-machine-default id -u: exit status 255`. An offline fixture, `TestLiveUpdateFailurePreservesMachineSSH`, reproduced the mechanism: the failing `ssh` shim that the step prepends to `PATH` also caught the nested Podman's machine transport. The shim now forwards every call but the readiness probe to the real `ssh`. The first Windows attempt at `95a9abb` failed in the build step with `server probably quit: unexpected EOF` and a stopped Podman machine, with a cause the logs do not establish; the rerun passed. Excerpt-only `TestLiveImages` passes before `f577a28` name no commit, date, or Podman version, and runs attempted on a development machine with Podman 4.3.1 failed at the preflight for lack of Podman 4.4, `pasta`, and delegated controllers.

### Not covered by any run

- The Microsoft signing key for the .NET feed: `packages-microsoft-prod.deb` is fetched over HTTPS and not pinned.
- Digests of the pulled Debian image and of built images, and pinned Playwright, .NET patch, or Debian package versions. The version manifest in every image records what a build installed.
- `scripts/release.sh` builds no Podman image.

## NodeSource signing key

The base recipe pins the NodeSource signing key by fingerprint ([images](images.md#nodesource-signing-key-check)). The expected fingerprint is `6F71F525282841EEDAF851B42F59B5F99B1BE0B4`, stored in `image/context/base/nodesource-signing-key.fingerprint`.

### Provenance

The key was fetched from deb.nodesource.com on 2026-10-11. It holds one primary key (`2F59B5F99B1BE0B4`, uid `NSolid <nsolid-gpg@nodesource.com>`) and one subkey. NodeSource's own repository (nodesource/distributions, at commit 9b431d8 of 2026-07-09) publishes no fingerprint of this key.

One source independent of deb.nodesource.com holds the same key: `files/repo/nodesource/NODESOURCE-GPG-SIGNING-KEY-EL` in voxpupuli/puppet-nodejs, at commit `7649b6b8a` of 2024-01-03. The 2015 version of that file (commit `9cf0f3035`) holds a different key, so this key was recorded there after 2015 and before 2026. That record shows the key stayed the same from 2024 to 2026; it does not prove that NodeSource published it, because that project's author also took it from NodeSource. Forum posts show the same fingerprint but are not an independent source. One unverified report says the hosted key once no longer matched the signature on an 18.x `InRelease` file; it has not been reproduced here.

The pin therefore records the key as fetched and reviewed when it was added, corroborated by a dated copy in another project. It detects a later change of the key, not a key substituted before its first recorded copy.

### SHA-1 binding signature

The key's subkey binding signature, created in 2016, uses SHA-1, verified with `gpg --list-packets` on the fetched key. Third-party reports say that Debian 13 refuses NodeSource repositories whose binding signatures use SHA-1, for example [nodesource/distributions#1920](https://github.com/nodesource/distributions/issues/1920). The risk was tested on 2026-10-11: the Containerfile was built with rootless Podman from `debian:trixie-slim`; apt fetched and verified the `node_24.x` repository (`InRelease`) with the pinned key, reported no key or signature warning, and installed `nodejs` 24.21.0, and the image ran `node --version` as v24.21.0. The refusal did not occur with this key on Debian 13. That build tested the key as fetched that day; a key rotation needs the same build again before the pin is updated.

### Key rotation

When NodeSource rotates its key, the check fails and `build` stops ([images](images.md#nodesource-signing-key-check)). To update the pin:

1. Fetch the new key.
2. Confirm its primary fingerprint with gpg, for example `gpg --show-keys --with-fingerprint`, in an isolated `GNUPGHOME`. When possible, confirm it from a source independent of deb.nodesource.com as well.
3. Write the new fingerprint into `image/context/base/nodesource-signing-key.fingerprint`.
4. Replace the fixture `image/test/nodesource/nodesource-repo.gpg.key` with the new key.
5. Update the provenance above: source, date, and key details.
6. Run the offline test, `bash image/test/nodesource/test-check-nodesource-key.sh`. It reads the expected fingerprint from the fingerprint file, so it needs no edit.
7. Prepare the build context as under [Development builds with the manager](#development-builds-with-the-manager), build the base image with rootless Podman (`podman build -f <prepared-context>/context/base/Containerfile <prepared-context>/context/base`), and check that apt accepts the repository. A build from the checkout itself stops at the manager guard, because the `manager.bin` in the source tree is an empty placeholder.
8. Commit.

Changing the fingerprint file changes the asset hash, so the base image gets a new tag.

## Toolchain package sources

Checked on 2026-10-14; every toolchain's packages are available for Debian 13:

- [`azure-cli`](https://packages.debian.org/trixie/azure-cli) and [`python3-azext-devops`](https://packages.debian.org/trixie/python3-azext-devops) in Debian 13, both architecture-independent, at 2.74.0-1 and 1.0.1-1. Microsoft's [Azure CLI installation page](https://learn.microsoft.com/en-us/cli/azure/install-azure-cli-linux) (apt tab) lists Debian 11 and 12 as the tested releases for Microsoft's feed and no Debian 13 entry, and named 2.91.0 as current, so the packaged Azure CLI trails upstream. The trade is a supported Debian 13 install at an older version, with no Microsoft key in the image.
- [Microsoft's .NET installation page](https://learn.microsoft.com/en-us/dotnet/core/install/linux-debian) lists .NET 8, 9, and 10 for Debian 13 and publishes `packages-microsoft-prod.deb` for Debian 13 at `packages.microsoft.com/config/debian/13/`.
- [Playwright's system requirements](https://playwright.dev/docs/intro#system-requirements) list Debian 12 and 13 on x86-64 and arm64.

## Release builds

`bash scripts/release.sh VERSION OUTPUT_DIRECTORY`, with the Go version from `go.mod` on `PATH`, builds the static Linux and Windows AMD64 executables, `SHA256SUMS`, the [npm package](npm-package.md) `sandboxed-agents-VERSION.tgz`, and the [NuGet command package](nuget-package.md) `SandboxedAgents.VERSION.nupkg`. Both executables and the embedded manager are built with `-trimpath -buildvcs=false -buildid=` and `-s -w`.

- `VERSION` must be NuGet-compatible SemVer: `X.Y.Z` with optional prerelease and build metadata. The package filename omits a `+build` suffix while the nuspec keeps the full version, so `1.2.3+abc` produces `SandboxedAgents.1.2.3.nupkg` with version `1.2.3+abc` in its nuspec.
- Inside a Git worktree, the literal tag `vVERSION` must point at `HEAD` (annotated tags are peeled), and the worktree must be clean: tracked edits, untracked files, and ignored files under `image/context/` or other release inputs stop the build. Source directories outside Git remain buildable; if the source root has a `.git` entry, Git must be available.
- The script runs `image/cmd/overlay`, which normalizes the image build context to LF in a temporary directory, compiles `cmd/sandboxed-agents-manager` for Linux amd64 into it as `context/base/manager.bin`, and writes a Go build overlay. Both host executables embed those same bytes; the checkout stays untouched and the temporary files are removed. A plain `go build` normalizes the context only at runtime and embeds the empty manager placeholder.
- `.gitattributes` keeps `go.mod`, `scripts/*.sh`, `scripts/version-symbol.txt`, `packages/nuget/**`, all `*.go` files, and the workflow files at LF, so `gofmt` stays clean under `core.autocrlf=true`, marks `.bin` files as binary, and forces the other context files to text. Every binary asset must use `.bin` to keep exact bytes through Git; this naming convention has no further enforcement.
- Local verification executes the Linux binary and checks the cross-compiled Windows bytes and the PE AMD64 machine field; a Linux host cannot execute the Windows binary. Native Windows verification is CI's job.

`go test ./scripts -count=1 -v` compares builds of the same tagged temporary commit with LF and CRLF assets, all Go sources, and module and linker metadata. On Windows, set `SANDBOXED_AGENTS_RELEASE_BASH` to the Git for Windows executable, such as `C:\Program Files\Git\bin\bash.exe`; `TestWindowsReleaseBuildsAreReproducible` and `TestWindowsNuGetCommandPackageLifecycle` skip with a reason when it is unset and fail when a configured path is not an existing file. `go test ./image` checks that the embedded manager is a static Linux amd64 ELF file and identical from LF and CRLF checkouts.

### Continuous integration

`.github/workflows/release-reproducibility.yml` runs on Blacksmith runners for pushes to `main`, pull requests, and manual runs; feature-branch pushes start no run, so a pull request runs once. A newer pull request run cancels a superseded in-progress run of the same pull request; pushes to `main` and manual runs always run to completion, because the stable release gate requires a successful push run with both the `linux` and `windows` jobs on the exact commit.

- The `changes` job runs `scripts/ci-checks-required.sh`. On a pull request that changes only `docs/`, `.agents/`, or `AGENTS.md`, with no `.go` file and none of the pages the offline consistency tests read (`docs/command-reference.md` and the two quick guides), the `linux` job skips the Go toolchain, the offline suite, and the release build, and the `windows` and `compare` jobs are skipped. Pushes to `main` and manual runs always run the full checks.
- In a full run, the `linux` and `windows` jobs run side by side. Each restores the Go caches, creates a local CI tag at its checkout's commit (never pushed), runs the offline suite, builds the release files under a throwaway version (`0.0.0-ci-` and the commit), and prints the asset hash of its executable. The Windows job runs the Windows binary with Git for Windows Bash.
- The `compare` job checks that the asset hash and the SHA256 digests of both executables and the npm tarball are identical on both runners.

`.github/workflows/stable-release-gate.yml` runs when a tag matching `v*` is pushed. A preview tag passes without further checks; a stable tag is checked for the evidence under [Stable release check](releases.md#stable-release-check), and the workflow fails when evidence is missing.

## Development builds with the manager

The base recipe copies `manager.bin` into the image. In the source tree that file is an empty placeholder, because the manager is compiled at build time. A host executable from a plain `go build` embeds the empty file, so an image build stops at the guard step with `Manager missing: build the host executable with image/cmd/overlay.`, and its asset hash differs from a prepared executable's. To build a host executable whose images work, run these from the repository root with the Go version from `go.mod` on `PATH`:

```sh
image_build_context=$(mktemp -d)
trap 'rm -rf "$image_build_context"' EXIT
go run ./image/cmd/overlay "$image_build_context"
go build -overlay "$image_build_context/overlay.json" -o sandboxed-agents ./cmd/sandboxed-agents
```

`image_build_context` is an empty directory outside the checkout, removed when the shell exits. Keep it until the `go build` has finished. The checkout stays unchanged.

## Running the live suite

The live suite runs on the project's Linux and Windows hosts over their SSH connections, following the `live-validation` skill, which opens with the project's start checkpoint and owns the [image cleanup](../.agents/skills/live-validation/SKILL.md#image-cleanup) after a run. The command, its variables, and the record it writes are described under [Live suite](releases.md#live-suite); the image coverage and its opt-in under [Live image tests](images.md#live-image-tests). Run it from a clean checkout at the commit under test, because the record names that commit.

## Offline test coverage of the documentation

Three tests read the user pages: `TestSecurityPolicyCoversPodmanCreateOptions` checks that `SECURITY.md` names every option `up` passes to Podman create; `TestCommandReferenceMatchesCommandTree` checks the grammar block of the command reference against the parser; `TestQuickGuideCommandsParse` parses every command line of both quick guides. `image/test/nodesource/test-check-nodesource-key.sh` checks the key check script against the fingerprint file, and `go test ./image` checks the embedded `sshd_config` policy with the host's `sshd -T`, the entrypoint's host-key startup under a real shell, and the recipe contents.
