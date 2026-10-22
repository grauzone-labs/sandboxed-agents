# Maintainer notes

This page is for the maintainers of `sandboxed-agents`. Here you find how the Podman version floors were confirmed, the live runs and source checks on record, the provenance and rotation of the NodeSource and Tailscale signing keys, how release builds and CI work, what the stable release publication relies on and how to recover an interrupted one, and how to build a development executable whose images work. The user pages link here wherever a statement rests on this evidence.

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
- **Info query.** The info fields for rootless mode and the cgroup version are confirmed from 3.2.0. Podman's info output has no field for delegated cgroup controllers, which is why the preflight reads the controller files itself ([host prerequisites](host-prerequisites.md)). Those reads passed in the live runs with Podman 5.4.2 and machine 5.8.9; nothing confirms them at the floors.
- **Engine version query.** The `Server.Version` query reported the machine's Podman 5.8.9 in the Windows live runs; nothing confirms it at 4.7.0.
- **Automount root query.** The Windows live runs exercised the read of an existing `wsl.conf` without an `[automount]` section, so the default root, not an absent file or a custom root; nothing confirms the two `podman machine ssh` calls at the 4.7.0 floor. `podman machine ssh` runs commands in the machine from 4.0.0, and the automount setting itself does not depend on the Podman version.
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
- **Earlier failures, superseded.** At `3104f28` the Windows `TestLiveImages` run failed in the forced-rollback step at the preflight with `podman machine ssh podman-machine-default id -u: exit status 255`. An offline fixture, `TestLiveUpdateFailurePreservesMachineSSH`, showed a harness defect with the same symptom: the failing `ssh` shim that the step prepends to `PATH` also caught the nested Podman's machine transport. The retained Windows log does not record which `ssh` the transport selected, so the live failure is consistent with that mechanism but not proven to share it. The shim now forwards every call but the readiness probe to the real `ssh`. The first Windows attempt at `95a9abb` failed in the build step with `server probably quit: unexpected EOF` and a stopped Podman machine, with a cause the logs do not establish; the rerun passed. Excerpt-only `TestLiveImages` passes before `f577a28` name no commit, date, or Podman version, and runs attempted on a development machine with Podman 4.3.1 failed at the preflight for lack of Podman 4.4, `pasta`, and delegated controllers.

### Not covered by any run

- The Microsoft signing key for the .NET feed: `packages-microsoft-prod.deb` is fetched over HTTPS and not pinned.
- Digests of the pulled Debian image and of built images, and pinned Playwright, .NET patch, or Debian package versions. The version manifest in every image records what a build installed.
- `scripts/release.sh` builds no Podman image.

## Repository signing keys

The base recipe pins the signing key of each package repository it adds by fingerprint ([images](images.md#signing-key-checks)), with one script, `image/context/base/check-signing-key.sh`, and one fingerprint file per repository. The offline test `bash image/test/signing-keys/test-check-signing-key.sh` checks the script against both fingerprint files and both key fixtures; it reads the expected fingerprints from the files, so a pin update needs no edit of the test.

### NodeSource signing key

The expected fingerprint is `6F71F525282841EEDAF851B42F59B5F99B1BE0B4`, stored in `image/context/base/nodesource-signing-key.fingerprint`.

#### Provenance

The key was fetched from deb.nodesource.com on 2026-10-11. It holds one primary key (`2F59B5F99B1BE0B4`, uid `NSolid <nsolid-gpg@nodesource.com>`) and one subkey. NodeSource's own repository (nodesource/distributions, at commit 9b431d8 of 2026-07-09) publishes no fingerprint of this key.

One source independent of deb.nodesource.com holds the same key: `files/repo/nodesource/NODESOURCE-GPG-SIGNING-KEY-EL` in voxpupuli/puppet-nodejs, at commit `7649b6b8a` of 2024-01-03. The 2015 version of that file (commit `9cf0f3035`) holds a different key, so this key was recorded there after 2015 and before 2026. That record shows the key stayed the same from 2024 to 2026; it does not prove that NodeSource published it, because that project's author also took it from NodeSource. Forum posts show the same fingerprint but are not an independent source. One unverified report says the hosted key once no longer matched the signature on an 18.x `InRelease` file; it has not been reproduced here.

The pin therefore records the key as fetched and reviewed when it was added, corroborated by a dated copy in another project.

#### SHA-1 binding signature

The key's subkey binding signature, created in 2016, uses SHA-1, verified with `gpg --list-packets` on the fetched key. Third-party reports say that Debian 13 refuses NodeSource repositories whose binding signatures use SHA-1, for example [nodesource/distributions#1920](https://github.com/nodesource/distributions/issues/1920). The risk was tested on 2026-10-11: the Containerfile was built with rootless Podman from `debian:trixie-slim`; apt fetched and verified the `node_24.x` repository (`InRelease`) with the pinned key, reported no key or signature warning, and installed `nodejs` 24.21.0, and the image ran `node --version` as v24.21.0. The refusal did not occur with this key on Debian 13. That build tested the key as fetched that day; a key rotation needs the same build again before the pin is updated.

### Tailscale signing key

The expected fingerprint is `2596A99EAAB33821893C0A79458CA832957F5868`, stored in `image/context/base/tailscale-signing-key.fingerprint`. The recipe fetches the binary keyring `https://pkgs.tailscale.com/stable/debian/trixie.noarmor.gpg`, the file Tailscale's own Debian 13 instructions install, checks it, and copies it unchanged into `/etc/apt/keyrings/tailscale.gpg`.

#### Provenance

The keyring was fetched from pkgs.tailscale.com on 2026-10-23 for decision D3 of Story #89 ([ADR 0010](adr/0010-t3-code-pairs-over-an-in-sandbox-tailnet-node.md)). It holds one primary key (`458CA832957F5868`, RSA 4096, created 2020-02-25, uid `Tailscale Inc. (Package repository signing key) <info@tailscale.com>`) and one encryption subkey (`B1547A3DDAAF03C6`). Tailscale publishes the same keyring for every Debian and Ubuntu release it supports, and the stable and unstable tracks share it; the fingerprint itself is not printed on Tailscale's installation pages. A web search for the fingerprint and for the key id on 2026-10-23 found no record of this key independent of pkgs.tailscale.com, so the pin records the key as fetched and reviewed when it was added, as the NodeSource pin does. The fixture `image/test/signing-keys/tailscale-archive-keyring.gpg` is that fetched keyring.

The Debian package `tailscale` depends on `iptables` and `iproute2`, which the image installs with it; the tailnet node of ADR 0010 runs in userspace networking mode and uses neither. The package's maintainer scripts enable a systemd unit only where systemd runs, so the image, which has no systemd, installs the package without starting a Tailscale process, and nothing in the recipe starts `tailscaled`.

### Key rotation

When a repository rotates its key, its check fails and `build` stops ([images](images.md#signing-key-checks)). To update a pin:

1. Fetch the new key.
2. Confirm its primary fingerprint with gpg, for example `gpg --show-keys --with-fingerprint`, in an isolated `GNUPGHOME`. When possible, confirm it from a source independent of the repository's host as well.
3. Write the new fingerprint into the repository's fingerprint file in `image/context/base/`.
4. Replace the repository's fixture in `image/test/signing-keys/` with the new key.
5. Update the provenance above: source, date, and key details.
6. Run the offline test, `bash image/test/signing-keys/test-check-signing-key.sh`.
7. Prepare the build context as under [Development builds with the manager](#development-builds-with-the-manager), build the base image with rootless Podman (`podman build -f <prepared-context>/context/base/Containerfile <prepared-context>/context/base`), and check that apt accepts the repository. A build from the checkout itself stops at the manager guard, because the `manager.bin` in the source tree is an empty placeholder.
8. Commit.

Changing a fingerprint file changes the asset hash, so the base image gets a new tag.

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

`.github/workflows/stable-release-gate.yml` runs when a tag matching `v*` is pushed. A preview tag passes without further checks; a stable tag is checked for the evidence under [Stable release check](releases.md#stable-release-check), and the workflow fails when evidence is missing. That check includes the provenance of the preview executable it runs ([Preview provenance before execution](#preview-provenance-before-execution)). `.github/workflows/release.yml` runs on the same tags, calls that gate for a stable tag, and publishes; see [Publishing a stable release](#publishing-a-stable-release).

## Publishing a stable release

This section records what the publication of a stable release relies on, what a maintainer sets up once by hand, and how to recover when a publication stops half-way. It states what is installed today and what is still absent. Where a fact comes from an external page, the page and the date it was read are named in [Sources](#publication-sources). Nothing here was observed on a real stable publication, because none has happened; the two preview publications on record ran the same workflow without the gate and without any registry step.

### State on record

Read on 2026-10-17, except a row that names a later date, which uses that date. A row says "absent" when the thing does not exist yet, and "installed" only where it was read back from GitHub.

| Item | State | Evidence |
|---|---|---|
| Tag ruleset `Stable tag creation (v*)` | installed, active | ID 25318428; read back through the rulesets API on 2026-10-17 ([rule page](https://github.com/grauzone-labs/sandboxed-agents/rules/25318428)) |
| Tag ruleset `Stable tag immutability (v*)` | installed, active | ID 25318430; read back the same way ([rule page](https://github.com/grauzone-labs/sandboxed-agents/rules/25318430)) |
| GitHub environment `release` | installed 2026-10-18T01:42:28Z, ID 24606547140; `protected_branches` false, `custom_branch_policies` true; exactly one deployment policy, ID 63427523, type `tag`, pattern `v*`, and no branch rule; no reviewers, wait timer, or secrets | read back through the environments and deployment-branch-policies APIs on 2026-10-18 and again on 2026-10-22, unchanged. The response reports `can_admins_bypass` true, GitHub's default, which the environment API does not let a request change, so an administrator override of the policy exists. The denial of an off-tag run was observed on 2026-10-18 with two real runs ([Environment `release`](#environment-release)) |
| Repository variable `NUGET_USER` | set to `grauzone` | read back through the Actions variables API on 2026-10-17 and on 2026-10-22 |
| Immutable releases repository setting | off | read through the repository API on 2026-10-17; keep it off, see [Immutable releases](#immutable-releases) |
| Branch protection of `main` | required checks `linux`, `windows`, and `compare`, strict, applied to administrators | read on 2026-10-17 |
| Publication workflow `.github/workflows/release.yml` | two preview runs on record, both green: [39115281621](https://github.com/grauzone-labs/sandboxed-agents/actions/runs/39115281621) for `v1.0.0-preview.20261018.1` and [39303250926](https://github.com/grauzone-labs/sandboxed-agents/actions/runs/39303250926) for `v1.0.0-preview.20261019.1`; in both, `stable_gate` was skipped and the `publish` job ran without the stable-only steps, as every preview does. No run for a stable tag | read back through the Actions runs API on 2026-10-22 |
| npm trusted publisher for `sandboxed-agents` | **absent**: the maintainer confirmed on 2026-10-17 that none is configured | none |
| NuGet trusted publishing policy for `SandboxedAgents` | **absent**: the same confirmation | none |
| `v*` tags, previews, and GitHub releases | two preview tags, each with its prerelease: [`v1.0.0-preview.20261018.1`](https://github.com/grauzone-labs/sandboxed-agents/releases/tag/v1.0.0-preview.20261018.1) on `f2fefd7cd431595e41d44674b7475a7ea6a4edfb` and [`v1.0.0-preview.20261019.1`](https://github.com/grauzone-labs/sandboxed-agents/releases/tag/v1.0.0-preview.20261019.1) on `a5c2c19409a7b038bd9600c2547eebc35efe596c`. No stable tag and no release that is not a prerelease | `gh release list` and `gh api repos/grauzone-labs/sandboxed-agents/commits/tags/TAG --jq .sha` on 2026-10-22 |
| Validation records and a manual-checklist run | on `v1.0.0-preview.20261018.1`: `live-suite-linux.json`, `live-suite-windows-11.json`, and `manual-checklist.json`, each naming `f2fefd7cd431595e41d44674b7475a7ea6a4edfb` with `result` `pass`; both live-suite records have `image_part_selected`, `image_part_ran`, and `image_coverage_complete` true and their five checks at `pass`; the manual-checklist record holds 23 checks, all `pass`, dated 2026-10-18 and 2026-10-19. On `v1.0.0-preview.20261019.1`: the two live-suite records, each naming `a5c2c19409a7b038bd9600c2547eebc35efe596c` with `result` `pass` and the image coverage run and complete, and no manual-checklist record | downloaded from the two prereleases and read on 2026-10-22. The stable gate has read none of them, because no stable tag exists |
| Registry OIDC publication | **never observed** | no stable tag has been pushed, so `release.yml` has published nothing to either registry (read 2026-10-22) |

The acceptance criteria of issue #36 that need a real publication, a real preview, or a manual run stay open until their evidence exists. This page is not that evidence.

### What the publication relies on

`.github/workflows/release.yml` runs when a tag matching `v*` is pushed. The workflow has two jobs.

- **`stable_gate`** runs only for a stable tag. It calls `stable-release-gate.yml` as a reusable workflow with the tag: the tag must point to the workflow commit, the release is rebuilt, and the [stable release check](releases.md#stable-release-check) runs against the preview and its records.
- **`publish`** runs on a GitHub-hosted runner, in the GitHub environment `release` for preview and stable tags alike ([Environment `release`](#environment-release)), after `stable_gate` succeeded, or for a preview tag, for which `stable_gate` is skipped. It rebuilds the tagged commit with `scripts/release.sh` and, for a stable tag, runs the `stable-gate` command again on that rebuild before anything else. It then attests the five release files in one step and runs `go run ./internal/release/cmd/publish-release`. That command checks again that the rebuilt executables match `SHA256SUMS` and print the tag's version, runs the `stable-gate` command a third time for a stable tag, verifies each file's attestation, and looks up the release for the tag. With none, it creates the release as a draft; a matching draft is resumed; a matching release that is already public is completed in place. In each case it uploads only the missing files. For a stable tag it also publishes the npm package and then the NuGet package, each only when its exact version is absent. Last, it makes a draft public: a preview as a prerelease that is explicitly not marked Latest, and a stable release with `make_latest=legacy`, so GitHub chooses the Latest release itself, by date and semantic version, and a stable release is not forced to be Latest. A release that is already public stays public throughout and is never made a draft again: the command adds its missing files and completes the missing registry packages, and skips that last step.

A preview tag takes the same path without the gate and without any registry step, and ends in a published GitHub prerelease. Concurrency is per tag and never cancels a running publication. The release text is the notes file; for a stable release the command appends, under "Validation gates (passed on this release commit)", one line for each of the three gates, `Offline suite`, `Live suite (Linux and Windows 11)`, and `Manual checklist`, each naming the commit of the release. The two live-suite records, one per platform, are one gate and have one line; the gate has already checked both records, the manual-checklist record, and the CI result against that commit.

| Prerequisite | Value | Why |
|---|---|---|
| Runner | GitHub-hosted `ubuntu-24.04` | npm trusted publishing supports cloud-hosted runners and not self-hosted ones. The CI workflows run on Blacksmith runners, so the publication does not reuse them. Whether a Blacksmith runner counts as self-hosted is not stated by npm; reports from other projects say it does, and nothing here depends on that. |
| Workflow file name | `release.yml` | Both registries bind the trust to the file name. Renaming the file breaks both policies until they are recreated. |
| Environment | the `publish` job declares `environment: release`; `stable_gate` declares none | Both registry policies name this environment, so a run that cannot deploy to it cannot obtain a registry token. See [Environment `release`](#environment-release). |
| Job permissions | `contents: write`, `actions: read`, `id-token: write`, `attestations: write` | `id-token: write` lets the job request the OIDC token that both registries and the attestation signing need; `attestations: write` stores the attestation; `contents: write` writes the release; `actions: read` reads the CI result. `artifact-metadata: write` is not granted and not needed: the attest step sets `push-to-registry: false` and `create-storage-record: false`, and the action creates a storage record only for a single OCI image pushed to a registry, which these five files are not ([`actions/attest` v4 source](https://github.com/actions/attest/blob/v4/src/attest.ts), [README on storage records](https://github.com/actions/attest/blob/v4/README.md#artifact-metadata-storage-records)). |
| Node.js and npm | Node.js 24; `npm install --global npm@11.5.1` | Trusted publishing needs npm 11.5.1 or newer and Node.js 22.14.0 or newer. The GitHub-hosted Ubuntu 24.04 image 20261012.334.1 carries npm 10.9.9, which is too old, so the job installs the floor version. That tarball is fetched by version and no digest for it is pinned in this repository: trust rests on the npm registry and its maintainers, which is also where the package is published. |
| .NET SDK | 8.0.x through `actions/setup-dotnet@v5` | `dotnet nuget push` and `dotnet nuget verify`. |
| NuGet login | `NuGet/login@v1` with `user: ${{ vars.NUGET_USER }}` | Exchanges the OIDC token for an API key that is valid for one hour. `NUGET_USER` holds the nuget.org profile name `grauzone`, not an email address, and is not a secret. |
| Attestation | `actions/attest@v4` over the five files in one step | A single attestation covers all five subjects. |
| `gh` | exactly 2.102.0, installed by the job | See [The `gh` version](#the-gh-version). |
| Release notes | `docs/release-notes/X.Y.Z.md` in the tagged commit | See [Release notes](#release-notes). |

The six external actions of the `publish` job (`actions/checkout`, `actions/setup-go`, `actions/setup-node`, `actions/setup-dotnet`, `actions/attest`, and `NuGet/login`) are referenced by full commit SHA, each with a comment naming the version it was resolved from. The versions in the table are those logical versions; what runs is the pinned commit, not a tag, and this page does not claim that any of those tags is immutable. To move a pin, resolve the new tag to its commit, then change the SHA and its version comment together.

The workflow reads no long-lived registry token, and the repository holds no registry secret (the repository's Actions secrets list was empty on 2026-10-17). The only registry credentials are short-lived: the identity of the run for npm and the one-hour key from `NuGet/login` for NuGet. For npm the publication command removes `NODE_AUTH_TOKEN`, `NPM_TOKEN`, and `NPM_CONFIG_*` from its environment, runs `npm publish` from an empty directory, and gives it empty user and global `.npmrc` files, so no token can be inherited. A stable publication refuses to start without the run's OIDC request variables and the NuGet key.

#### The `gh` version

`scripts/setup-release-gh.sh` downloads the upstream archive `gh_2.102.0_linux_amd64.tar.gz` from the `v2.102.0` release of `cli/cli`, checks it against the SHA-256 `bb766f710eef8ede859c18578c72c327597cd4c8a85b06001b1f3843c6019386` (the value in that release's `gh_2.102.0_checksums.txt`, compared on 2026-10-18), and puts it first on `PATH`. Both `release.yml` and `stable-release-gate.yml` run that script before any verification, so neither uses the `gh` of the runner image. The publication step relies on `gh attestation verify` with `--signer-workflow`, `--source-digest`, `--source-ref`, and `--deny-self-hosted-runners`, on `gh release create --draft --verify-tag`, and on `gh api`.

- `gh attestation` first shipped in 2.49.0. In releases from 2.49.0 to 2.66.1, `gh attestation verify` can exit with status zero, after printing a failure, when the artifact has an attestation of a different predicate type than the one requested (the default is `https://slsa.dev/provenance/v1`) and none of the requested type; a script that trusts only the exit status would accept it (CVE-2025-25204, [GHSA-fgw4-v983-mgp8](https://github.com/cli/cli/security/advisories/GHSA-fgw4-v983-mgp8), fixed in 2.67.0). 2.67.0 is therefore the minimum for manual attestation verification; the automated stable gate requires the checksum-pinned 2.102.0.
- For manual `gh attestation verify` on your own machine, use 2.67.0 or newer; `gh --version` shows yours. Running `stable-gate` directly requires 2.102.0, as in the pin-change bullet below. Version 2.23.0, as the Debian package on the maintainers' development machine, has no `attestation` command.
- **Why 2.102.0.** The gate and the publisher check attestations with `--signer-workflow`, `--source-digest`, `--source-ref`, and `--deny-self-hosted-runners`, and three published advisories affect that matching in older releases: [GHSA-wjmr-j3rp-mh2g](https://github.com/cli/cli/security/advisories/GHSA-wjmr-j3rp-mh2g) (`--signer-workflow` matched only as a prefix of the certificate identity, so another workflow of the repository whose path starts with it could pass; affects 2.51.0 to 2.101.0, fixed in 2.102.0), [GHSA-mm27-mwq9-fr5g](https://github.com/cli/cli/security/advisories/GHSA-mm27-mwq9-fr5g) (unescaped regular-expression characters in `--signer-repo` and `--signer-workflow` matching, so a lookalike name could pass; 2.96.0 and earlier, fixed in 2.97.0), and [GHSA-4mq3-hpgx-9cx8](https://github.com/cli/cli/security/advisories/GHSA-4mq3-hpgx-9cx8) (`--source-ref` compared without regard to case; 2.68.0 to 2.101.0, fixed in 2.102.0). Release [v2.102.0](https://github.com/cli/cli/releases/tag/v2.102.0) of 2026-09-30 contains the fixes, so the pin is that release and not an older one with a workaround. The advisories were read on 2026-10-18.
- Changing the pin means choosing a new upstream release, reading its checksum file, and changing both values in `scripts/setup-release-gh.sh` together. Change the accepted version `previewVerifierVersion` in `internal/release/cmd/stable-gate/github.go` in the same commit, with the tests that exercise it: the gate rejects any other `gh` version, so a new pin in the script alone makes every later gate fail. Update this section in that commit too.
- Two runs depend on it: the publisher, and the read-only stable gate ([Preview provenance before execution](#preview-provenance-before-execution)). The gate also rejects a `gh` whose version is not 2.102.0 before it verifies the preview, so running it directly with another `gh` does not bypass the check.

#### Preview provenance before execution

The stable gate compares the asset hash that the preview's Linux executable prints with `version` against the stable rebuild. To read it, the gate downloads that executable from the preview prerelease and runs it, on a runner that holds the workflow's credentials: in the read-only gate, its read-only GitHub token; in the `publish` job, the job's GitHub token with `contents` and `attestations` write permission and the OIDC request authority, and, in the publication step of a stable tag, the NuGet key. A prerelease asset is only as trustworthy as whoever last wrote it, so the requirement is:

- **Verify the exact downloaded bytes before they run.** The gate runs `gh attestation verify` on the downloaded file with `--repo grauzone-labs/sandboxed-agents`, `--signer-workflow` set to this repository's `.github/workflows/release.yml`, `--source-digest` set to the candidate commit, `--source-ref` set to `refs/tags/` and the preview tag, and `--deny-self-hosted-runners`. A missing, failed, or mismatching attestation fails the gate, and the file is never executed.
- **Run `version` with an empty environment.** The child process is started with no inherited environment variables, so none of the workflow's credentials is passed to it as a variable. That is the whole guarantee. The child runs as the same user as the gate, with no process, file system, or network isolation. On Linux, such a process may read the initial environment of its parent and ancestors from `/proc/<pid>/environ`, subject to the ptrace permission check ([proc_pid_environ(5)](https://man7.org/linux/man-pages/man5/proc_pid_environ.5.html)); those environments hold the read-only gate's GitHub token or, in the `publish` job, its GitHub token with `contents` and `attestations` write permission, the OIDC request variables, and, in the publication step of a stable tag, the NuGet key. Isolating the child has not been implemented or confirmed.
- **Apply it to every caller.** The rule covers the read-only `stable-release-gate.yml` (called by `stable_gate` and on its own tag push) and each `stable-gate` run inside the `publish` job and `publish-release`, which all share the same command.

The attestation authenticates where the file came from and which commit built it. It does not show that the code is benign. A verified preview was built from the candidate commit, which is trusted release code that the job already executes with the job's authority (`scripts/release.sh`, `go run`, and the rebuilt binaries), so a malicious attested preview would need malicious code at the candidate commit or control of the preview run, which already holds that authority. The verification keeps an unknown or altered file out; it does not make the child a sandbox. The exact-byte check and the pinned verifier fail closed.

Preview attestations exist since the first preview run on 2026-10-18 ([State on record](#state-on-record)); the manual checklist item `preview-attestation` verified them by hand on `v1.0.0-preview.20261018.1`. The gate has verified none, because it runs only for a stable tag; its first real verification is observed with the first stable tag and recorded as such. The offline tests of the gate use fakes and are not that evidence.
#### Release notes

Release notes live in `docs/release-notes/X.Y.Z.md`, one file per version, committed before the preview tag so that they belong to the validated commit. A preview `vX.Y.Z-preview.YYYYMMDD.N` uses the notes of its base version `X.Y.Z`. The publication job fails before it builds its release artifacts when the file is missing or empty at the tagged commit. The workflow publishes the file as the release text; for a stable release it appends, for each gate, the commit it passed on. The notes of 1.0.0 are [`docs/release-notes/1.0.0.md`](release-notes/1.0.0.md); they claim no gate result.

Nothing may be committed between the validated preview and the stable tag, so a correction to the notes after the preview means a new preview and new validation.

### Tag ruleset

The check that "a prerelease exists on the same commit" holds only while a published tag cannot be moved, or deleted and created again. Two active tag rulesets on `grauzone-labs/sandboxed-agents` target `refs/tags/v*` (GitHub's `fnmatch` syntax, so they cover every tag that starts with `v`: previews, stable tags, and a mistyped one alike). They were applied on 2026-10-17, before any tag existed, and are in place before the first publication beyond GitHub prereleases.

| Ruleset | ID | Rules | Bypass |
|---|---|---|---|
| `Stable tag creation (v*)` | 25318428 | Restrict creations | the repository Admin role (role ID 5), always |
| `Stable tag immutability (v*)` | 25318430 | Restrict updates, Restrict deletions | none |

GitHub describes the rules as: only users with bypass permission can create, push to (update), or delete a matching tag ([available rules](https://docs.github.com/en/repositories/configuring-branches-and-merges-in-your-repository/managing-rulesets/available-rules-for-rulesets), read 2026-10-17). With an empty bypass list nobody can move or delete a matching tag, administrators included, until a maintainer with the ruleset permission removes or changes the ruleset itself. Consequences that matter for a release:

- **A tag is permanent once pushed.** Check the tag name and the commit before `git push`. A preview tag that was pushed on a wrong commit stays; the next preview gets the next `N` or a new date.
- **A defect in a workflow or a notes file at the tagged commit cannot be fixed under the same tag.** The fix is a new commit, so a new preview, new validation, and a new version. A rerun of a run (see [Recovery](#recovery-from-an-interrupted-publication)) uses the workflow file of the tagged commit.
- **Only an administrator can create a `v*` tag.** The release workflow does not create tags; a maintainer with the Admin role pushes them.
- **Weakening either ruleset is a change to the release guarantee.** Record any change here, with its reason and date, in the same commit as a documentation change.

Settings to read back (they need administrator read access):

```sh
gh api repos/grauzone-labs/sandboxed-agents/rulesets --jq '[.[] | {id, name, target, enforcement}]'
gh api repos/grauzone-labs/sandboxed-agents/rulesets/25318428 --jq '{name, target, enforcement, bypass_actors, conditions, rules: [.rules[].type]}'
gh api repos/grauzone-labs/sandboxed-agents/rulesets/25318430 --jq '{name, target, enforcement, bypass_actors, conditions, rules: [.rules[].type]}'
```

To recreate them in the web interface, follow GitHub's [tag ruleset steps](https://docs.github.com/en/repositories/configuring-branches-and-merges-in-your-repository/managing-rulesets/creating-rulesets-for-a-repository) (Settings, Rules, Rulesets, New ruleset, New tag ruleset) with these values, or send the two bodies with `gh api --method POST repos/grauzone-labs/sandboxed-agents/rulesets --input FILE`:

```json
{
  "name": "Stable tag creation (v*)",
  "target": "tag",
  "enforcement": "active",
  "bypass_actors": [{ "actor_id": 5, "actor_type": "RepositoryRole", "bypass_mode": "always" }],
  "conditions": { "ref_name": { "include": ["refs/tags/v*"], "exclude": [] } },
  "rules": [{ "type": "creation" }]
}
```

```json
{
  "name": "Stable tag immutability (v*)",
  "target": "tag",
  "enforcement": "active",
  "bypass_actors": [],
  "conditions": { "ref_name": { "include": ["refs/tags/v*"], "exclude": [] } },
  "rules": [{ "type": "update" }, { "type": "deletion" }]
}
```

Release tags are not protected by the branch protection of `main`, which is a separate setting.

#### Immutable releases

GitHub's own "immutable releases" repository setting locks a published release's tag and assets. It is off, and the project does not depend on it. Leave it off: validation records are uploaded to a preview's prerelease after it is published ([manual checklist](manual-checklist.md#upload)), and the recovery of a public release adds a missing file. GitHub states that the protections start at publication ([immutable releases](https://docs.github.com/en/code-security/supply-chain-security/understanding-your-software-supply-chain/immutable-releases)), so turning the setting on would block both. That consequence is inferred from the page; it has not been tried here.

### Environment `release`

Both registry entries name the environment `release` in addition to the repository and the file name `release.yml`. A file name alone is weaker than the tag rules: another workflow run of the same file on a branch, or on a branch whose name starts with `v`, would otherwise carry the same repository and workflow claims. npm and NuGet both accept an optional environment in the policy ([npm](https://docs.npmjs.com/trusted-publishers/), [NuGet](https://learn.microsoft.com/en-us/nuget/nuget-org/trusted-publishing)), and GitHub can restrict an environment to named tags ([deployment branch and tag policies](https://docs.github.com/en/rest/deployments/branch-policies#create-a-deployment-branch-policy)). The `publish` job declares `environment: release` for preview and stable tags; the read-only `stable_gate` job declares none. Together with the tag rulesets, which let only the repository Admin role create `v*` tags and let nobody move or delete them, in normal operation only a pushed `v*` tag can deploy to the environment.

Settings of the environment, created by a repository administrator before the first `v*` tag is pushed:

- Name: `release`, in this repository.
- Deployment branches and tags: selected branches and tags only, with exactly one rule, of type **tag**, with the name pattern `v*`, and no branch rule. A plain `v*` branch rule would admit a branch whose name starts with `v`.
- No required reviewers and no wait timer. The environment adds no human approval step.

Create it first, and do not let a workflow create it: GitHub creates an environment that a running workflow names and that does not exist, and it has no protection ([managing environments](https://docs.github.com/en/actions/managing-workflow-runs-and-deployments/managing-deployments/managing-environments-for-deployment)). Anyone who can edit workflows can cause that, while only repository administrators can configure an environment.

Read it back (administrator access):

```sh
gh api repos/grauzone-labs/sandboxed-agents/environments/release --jq '{name, can_admins_bypass, deployment_branch_policy, protection_rules}'
gh api repos/grauzone-labs/sandboxed-agents/environments/release/deployment-branch-policies --jq '[.branch_policies[] | {name, type}]'
```

The first call should show `protected_branches` false and `custom_branch_policies` true under `deployment_branch_policy`, a `protection_rules` list with only the `branch_policy` rule (no reviewer or wait-timer rule), and `can_admins_bypass` true; the second exactly one policy, `v*` of type `tag`. A readback proves only the configured policy, and it reports `can_admins_bypass` true: GitHub's default allows an administrator override that the environment API cannot switch off, so the environment is not unconditionally non-bypassable. Repository administrators can already change the settings and create tags. It does not prove that GitHub denies a run, and it does not prove that either registry accepts or rejects a token. The name of the environment alone, without the tag rule, is not enough.

**Check before the registry entries are activated.** Prove the denial once with real runs, not with the readback. The maintained `release.yml` runs only for pushed `v*` tags, so a branch push does not start it; the check therefore uses two temporary, unmerged branches that carry a replacement test workflow with the same file name. `workflow_dispatch` is not used, because it needs the workflow file on the default branch, and a tag is never pushed ([events that trigger workflows](https://docs.github.com/en/actions/using-workflows/events-that-trigger-workflows)).

1. Create two temporary branches from the reviewed head: an ordinary one, `environment-denial-check`, and one whose name starts with `v`, `v-environment-denial-check`.
2. On each branch, replace the whole content of `.github/workflows/release.yml` with the test workflow below, so that no publication job or step remains, and commit it. Do not edit the maintained file on any other branch.
3. Push each branch. The push of a branch listed under `branches` starts the test workflow for it.

```yaml
name: Release environment denial check
'on':
  push:
    branches: [environment-denial-check, v-environment-denial-check]
permissions: {}
jobs:
  denied:
    runs-on: ubuntu-24.04
    environment: release
    permissions: {}
    steps:
      - name: Must never run
        run: echo "the release environment admitted this branch"
```

The workflow has a GitHub-hosted runner, no token permissions (no `id-token`, no `contents`), no secrets, and no publication; its single step only prints a marker line and is expected **not** to run. The expected result on each branch is a run for that push that GitHub refuses at the environment, because the ref is not a `v*` tag, before any step runs; the marker step must not run, and no override or bypass is used. If no run appears for a branch, nothing was proven: fix the trigger and push again.

Record the URL of both runs and, for each, that the environment refused the ref before any step started. If either run starts a step, the environment admitted a branch: the check has failed, the registry entries stay unconfigured, and the environment policy is corrected and checked again.

Afterwards cancel any run that is still pending, record it, and delete both temporary branches from the remote and locally. Never open a pull request from them or merge them, and never change the maintained, tag-only `release.yml` for this check.

**Result, 2026-10-18.** The check was run before the first `v*` tag was pushed, and the environment denied both runs:

| Branch | Commit | Run | What GitHub reported |
|---|---|---|---|
| `environment-denial-check` | `886fd3ffec55d289ce580afb26354682b084afcc` | [39115189654](https://github.com/grauzone-labs/sandboxed-agents/actions/runs/39115189654), created 2026-10-18T09:24:13Z | job `denied` ended with the conclusion `failure` after zero steps; its annotations read `Branch "environment-denial-check" is not allowed to deploy to release due to environment protection rules.` and `The deployment was rejected or didn't satisfy other protection rules.` |
| `v-environment-denial-check` | `c680cf4689d93e12124774bea71876543357625e` | [39115191120](https://github.com/grauzone-labs/sandboxed-agents/actions/runs/39115191120), created 2026-10-18T09:24:14Z | the same: conclusion `failure` after zero steps, with the same two annotations for `v-environment-denial-check` |

In both runs the environment refused the ref before any step started, so the marker step did not run, and the refusal itself shows that no override was applied. No run stayed pending, and both temporary branches are absent from the remote, read back on 2026-10-22 together with the two runs and their annotations through the Actions runs and check-run annotations APIs. The check proves the policy for a branch push; it does not prove that either registry accepts or rejects a token.

### One-time registry setup

Neither registry trusts the workflow yet. A maintainer sets this up by hand, once per registry, signed in as the account that owns the package. The values are exact; both registries compare them with the claims in the OIDC token of the run, and npm does not check the entries when you save them, so a mistake shows only at the first publication.

Timing matters. npm gives a new trusted publisher **two days** to complete its first successful publish, and then it expires and can no longer be used or edited; a NuGet policy for a private repository can also start with a temporary activation window. Create both entries after the preview is validated and just before the stable tag is pushed, not weeks ahead. The GitHub environment they name is different: it is created and read back earlier, before the first preview tag is pushed, because the preview's `publish` job references it too. Do not activate either registry entry until the off-tag check under [Environment `release`](#environment-release) has been run and has denied the run; it was run on 2026-10-18 and denied both runs, so that condition is met.

#### npm

Package `sandboxed-agents` on npmjs.com, as the maintainer account `grauzone` (which needs write access to the package and two-factor authentication). In the package settings, section Trusted Publisher, choose GitHub Actions:

| Field | Value |
|---|---|
| Organization or user | `grauzone-labs` |
| Repository | `sandboxed-agents` |
| Workflow filename | `release.yml` (the file name only, with its extension) |
| Environment name | `release` (exactly; the `publish` job declares it) |
| Allowed actions | `npm publish` selected. The workflow publishes directly; `npm stage publish` is always allowed and is not what the workflow runs. |

All fields are case-sensitive. The `repository.url` in the published `package.json` must match the GitHub repository; `packages/npm/package.json` carries `https://github.com/grauzone-labs/sandboxed-agents.git`, and npm records such a URL in the normalized form `git+https://github.com/OWNER/REPO.git`. npm generates provenance for a package published through OIDC from a public repository on its own; the repository is public.

After the first successful publish the entry is validated and bound to the repository's identity. If it expired before that, delete it, create it again, and rerun the publication.

#### NuGet

On nuget.org, signed in as `grauzone`, open the user menu, Trusted Publishing, and add a policy:

| Field | Value |
|---|---|
| Policy owner | the individual user `grauzone`, who owns `SandboxedAgents` |
| Repository Owner | `grauzone-labs` |
| Repository | `sandboxed-agents` |
| Workflow File | `release.yml` (the file name only, without `.github/workflows/`) |
| Environment | `release` |
| Scope | publishing new versions of an existing package, limited by the glob `SandboxedAgents` |

The values are case-insensitive. A policy applies to the packages the chosen owner owns, and the scope with its glob narrows it. The Microsoft page describes scopes and the glob but not the labels of the form, so choose the scope that matches the table when you see the form. A policy can start as temporarily active for seven days, which happens usually with private repositories; if no publish happens in that time it becomes inactive, and the window can be restarted. A successful publish makes it permanent.

The workflow gets the one-hour API key from `NuGet/login@v1` and `NUGET_USER`. The repository variable is set (see [State on record](#state-on-record)); it is a variable and not a secret because the name is not sensitive.

#### What exists before the first stable publication

Read on 2026-10-17, without publishing anything:

- **npm.** The package `sandboxed-agents` exists on the registry, with the maintainer `grauzone`. npm's `npm trust` page says the package must already exist before a trusted publisher is configured ([npm trust](https://docs.npmjs.com/cli/v11/commands/npm-trust), page edited 2026-06-02), and the trusted-publishers page does not describe configuring one for a name that does not exist. Because the package exists, no separate first-publish step is needed, and none is documented here.
- **NuGet.** The package `SandboxedAgents` exists on nuget.org, owned by `grauzone`. The Microsoft page does not describe how a policy behaves for an identifier that does not exist yet, whether a policy can reserve one, or what the first upload of a new identifier needs. That does not matter for this release, because the identifier exists under the owner the policy belongs to.

#### No token fallback

Issue #8 allowed a classic token held as a repository secret, `NPM_TOKEN` or `NUGET_API_KEY`, for a registry whose trusted publishing setup proved not feasible. Neither setup has been tried, so nothing shows that either is infeasible, and no fallback exists: the workflow reads no long-lived registry token and the repository holds no registry secret. If a setup proves infeasible, that is a decision for the maintainers: the workflow, the repository secret, and this page change together and name the registry, and nobody creates a token in advance. Do not add a token to get past a failed OIDC publication. A trusted publishing failure is fixed in the registry settings or the workflow, never with a token.

#### Limits of what can be tested

Previews are not published to the registries, so neither OIDC exchange can run before the first stable tag. Everything above about the registries comes from their documentation; the first real stable run is the first observation, and it is recorded as such. Because the tag cannot be moved, a bug in the workflow found at that point cannot be fixed under `v1.0.0`. The same holds for the environment: reading its policy back shows what is configured, not that GitHub denies an off-tag run; the denial was checked separately with real runs on 2026-10-18 ([Environment `release`](#environment-release)), before any registry entry is activated.

### Recovery from an interrupted publication

A run can stop between creating the GitHub release and finishing it: the runner is lost, a registry or the network fails, or someone cancels the run. Three states result, and the recovery for all of them is the same operation: **rerun the publication of the same tag**. A rerun of any tag rebuilds the tagged commit, attests, verifies, and resumes the GitHub release. Only a stable tag also repeats the stable gate and publishes to the registries; a preview has neither, so for a preview only the GitHub release applies. The command behind the `publish` job is built to resume, and it fails closed: on an API or network error, on metadata or file bytes it does not recognize, or on a mismatch, it stops and changes nothing further.

Rules that hold for every recovery:

- **Never move, delete, or recreate the tag**, and never create another tag for the same version. The rulesets forbid it, and it is the evidence the gates rest on.
- **Never overwrite a release file or a registry version.** The command uploads only files that are missing, never with `--clobber`, and publishes only a registry version that does not exist.
- **Never add a token**, and never publish by hand with a personal credential. A hand-made package is not the artifact the attestations describe.
- **Re-verify before trusting a recovery.** A rerun rebuilds the tagged commit with `scripts/release.sh`, which is reproducible, so the rebuilt files must have the same hashes as the ones already published; it attests again and checks each file's attestation against the repository, `release.yml`, the commit, and the tag. For a stable tag it also repeats the gate on the same evidence and compares the registries. A rerun that cannot match something does not repair it.
- **Registries have no rollback (stable releases).** An npm version cannot be reused after an unpublish ([npm unpublish policy](https://docs.npmjs.com/policies/unpublish)); nuget.org does not delete packages and offers unlisting only ([deleting packages](https://learn.microsoft.com/en-us/nuget/nuget-org/policies/deleting-packages)). A published registry version stays visible even when the GitHub release is incomplete.

To rerun:

```sh
gh run list --repo grauzone-labs/sandboxed-agents --workflow release.yml --limit 5
gh run view RUN_ID --repo grauzone-labs/sandboxed-agents --log-failed
gh run rerun RUN_ID --repo grauzone-labs/sandboxed-agents
```

Rerun all jobs, as above, and not only the failed one, so that the build runs again, and for a stable tag the gate too. If the failure came from something outside the workflow (for a stable tag: a registry outage, a missing record on the prerelease, or a missing registry policy), correct that first.

Look at the state before and after a rerun, read-only. Replace `vX.Y.Z` with the tag of the run: `vX.Y.Z-preview.YYYYMMDD.N` for a preview, `vX.Y.Z` for a stable release; in the `npm view` command use the version without the leading `v`. The first command applies to a preview and a stable release; the other two to a stable release only:

```sh
gh release view vX.Y.Z --repo grauzone-labs/sandboxed-agents --json tagName,isDraft,isPrerelease,assets
npm view sandboxed-agents@X.Y.Z version dist.integrity dist.tarball
curl -fsS https://api.nuget.org/v3-flatcontainer/sandboxedagents/index.json
```

Do not read the identity of a release from its `targetCommitish`. For a release of an existing tag, GitHub does not use that value to pick the commit and may report a branch name ([create a release](https://docs.github.com/en/rest/releases/releases#create-a-release)). The identity is the tag, resolved as in [Check a recovery by hand](#check-a-recovery-by-hand).

#### An abandoned draft

The run stopped after the draft release was created, with some or all files uploaded, and, for a stable tag, possibly after one registry; a preview publishes to GitHub only. `gh release view` shows `isDraft: true`. Only the GitHub release is private: a draft is not visible to users, but a stable registry version that was pushed before the run stopped is already public and permanent (see the rules above). For a stable release, check `npm view` and the nuget.org lookup before you decide anything. Rerun. The command finds the draft by its tag, rejects it if its release text or prerelease flag is not what the tagged commit and its notes imply (so do not edit the release text by hand), takes the commit from the existing tag, resolved locally and checked against the candidate commit, and not from the release's `target_commitish`, compares every file already on it with the rebuilt file, uploads the missing ones, and, for a stable tag, continues with the registries. Do not delete the draft first. Delete a draft only after you know why its files differ from the rebuilt ones: a difference means the build is not reproducible, which is a defect to file, not a nuisance to clear. Deleting a draft does not touch the tag, and the next rerun creates a new draft from the same commit.

#### A public release with missing files

The release is public and `gh release view` lists fewer than the five release files. Rerun. The command verifies the attestations, and for a stable tag checks the gate again, before it adds anything; it compares the files that are present with the rebuilt ones, and adds the missing files without replacing any. If a present file differs from the rebuilt bytes, it stops: a public file that differs is a security matter, not a recovery step. Treat it as an incident, report it under [SECURITY.md](../SECURITY.md), and do not replace the file; a corrected build is a new version.

#### A registry package without its GitHub release

This applies to stable releases only; a preview never publishes to a registry. A registry holds `X.Y.Z` and the GitHub release is absent, still a draft, or incomplete. The run stopped after a push, or the push succeeded and the report of it was lost. Rerun, and wait for the registry to index the version first if you pushed minutes ago, because a version that is not indexed yet looks absent, and the push would then be refused as a duplicate and fail the run. The command reads the exact version from each registry and compares what it downloads with the rebuilt package. An exact match counts as published and is skipped. The command publishes npm first, then NuGet, and makes a draft release public only when both hold their package. A release that is already public stays public while the missing package is added.

- **npm** is compared by exact bytes against `https://registry.npmjs.org/sandboxed-agents/-/sandboxed-agents-X.Y.Z.tgz`. `npm view` shows `dist.integrity` (SHA-512) and `dist.tarball`. For both registries a 404 counts as absent and any other status except 200 stops the run, because absence is then not proven.
- **NuGet** is compared by exact bytes first. nuget.org adds a repository signature to every package it hosts ([signed packages](https://learn.microsoft.com/en-us/nuget/reference/signed-packages-reference)), so a downloaded package normally differs from the one pushed. The command then accepts the package when every entry of the rebuilt archive is present in the download with the same name and content, no entry is duplicated or extra, and the only added entry is one non-empty `.signature.p7s`, and `dotnet nuget verify --all` ([reference](https://learn.microsoft.com/en-us/dotnet/core/tools/dotnet-nuget-verify)) succeeds on the download. The `.nupkg` on the GitHub release stays the exact attested build output.

If a registry holds a version that does not match, the command stops. That version cannot be removed or replaced (see the rules above). Do not push anything else under it; the release moves to a new version with a new validated preview, and the finding is filed as an issue.

#### Check a recovery by hand

A rerun that ends green is not the end of the recovery. Compare against the release itself, in empty scratch directories:

1. `gh release view vX.Y.Z --repo grauzone-labs/sandboxed-agents --json tagName,isDraft,isPrerelease,assets` shows the expected `tagName`, `isDraft` false, `isPrerelease` false for a stable release (true for a preview, with its preview tag in the command), and the five files. Then resolve the tag itself: `gh api repos/grauzone-labs/sandboxed-agents/commits/tags/vX.Y.Z --jq .sha` (with the actual tag, preview or stable) prints the commit the tag points to ([get a commit](https://docs.github.com/en/rest/commits/commits#get-a-commit)), and it must be the full SHA of the validated commit. A `targetCommitish` is not evidence of that.
2. Download the five files into one empty directory and run `gh attestation verify FILE --repo grauzone-labs/sandboxed-agents` on each. For this check, which names only the repository, 2.67.0 is the historical floor ([The `gh` version](#the-gh-version)); 2.102.0 matches the automated gate. Run `sha256sum --check --ignore-missing SHA256SUMS` for the two executables.
3. For a stable release, the release text has three gate lines under "Validation gates (passed on this release commit)": `Offline suite`, `Live suite (Linux and Windows 11)`, and `Manual checklist`. Each names a commit, and all three are the commit of the tag. The live suite has one line for its two platform records.
4. For a stable release, for npm, download the tarball named by `npm view sandboxed-agents@X.Y.Z dist.tarball` and compare its SHA-256 with the `.tgz` of the release.
5. For a stable release, for NuGet, download `https://api.nuget.org/v3-flatcontainer/sandboxedagents/X.Y.Z/sandboxedagents.X.Y.Z.nupkg`, run `dotnet nuget verify --all` on it, extract it and the release's `.nupkg` into two empty directories, delete the one added `.signature.p7s` from the extraction of the downloaded package, and run `diff -r` on the two directories. If the download is byte-identical to the release's `.nupkg`, nuget.org added no signature and nothing needs removing.
6. For a stable release, `sandboxed-agents version` from the release's executable prints the stable version and the same `asset-sha256` as the preview's.

Recovery proves nothing about the first publication itself. A recovery that happened is recorded with the run it recovered.

#### When a rerun cannot help

- **The stable gate failed.** Nothing was published. Upload the missing or corrected validation record to the preview's prerelease ([manual checklist](manual-checklist.md#upload)) when the evidence is what was wrong, and rerun.
- **The workflow or the notes at the tagged commit are wrong.** The tag cannot move. A fix needs a new commit, a new preview, new validation, and a new version.
- **A registry policy is missing or wrong (stable tag).** Correct it in the registry (see [One-time registry setup](#one-time-registry-setup)) and rerun. A policy whose two days have passed is deleted and created again.

### Publication sources

Read on 2026-10-17 unless another date is given; the three `gh` advisories, the `v2.102.0` release, and its checksum file were read on 2026-10-18.

- npm, [Trusted publishing](https://docs.npmjs.com/trusted-publishers): npm 11.5.1 and Node.js 22.14.0 minimums, GitHub-hosted runners only, field names and case sensitivity, `id-token: write`, allowed actions, two-day validation, `repository.url`; page last edited 2026-10-06.
- npm, [`npm trust`](https://docs.npmjs.com/cli/v11/commands/npm-trust): the package must exist; npm 11.15.0 or newer; page edited 2026-06-02. [Unpublish policy](https://docs.npmjs.com/policies/unpublish): a used `name@version` is never reusable.
- Microsoft Learn, [Trusted publishing on nuget.org](https://learn.microsoft.com/en-us/nuget/nuget-org/trusted-publishing): policy fields, one-hour key, `NuGet/login@v1`, policy owner, seven-day activation; page updated 2026-09-01. [NuGet/login](https://github.com/NuGet/login): `user` input and `NUGET_API_KEY` output. [Signed packages](https://learn.microsoft.com/en-us/nuget/reference/signed-packages-reference): every package uploaded to nuget.org is repository signed. [Deleting packages](https://learn.microsoft.com/en-us/nuget/nuget-org/policies/deleting-packages): no permanent deletion, unlisting only.
- GitHub Docs: [available rules for rulesets](https://docs.github.com/en/repositories/configuring-branches-and-merges-in-your-repository/managing-rulesets/available-rules-for-rulesets), [creating rulesets](https://docs.github.com/en/repositories/configuring-branches-and-merges-in-your-repository/managing-rulesets/creating-rulesets-for-a-repository), [immutable releases](https://docs.github.com/en/code-security/supply-chain-security/understanding-your-software-supply-chain/immutable-releases).
- GitHub CLI: [`gh attestation verify`](https://cli.github.com/manual/gh_attestation_verify), [`gh release create`](https://cli.github.com/manual/gh_release_create), [CVE-2025-25204 advisory](https://github.com/cli/cli/security/advisories/GHSA-fgw4-v983-mgp8), the pinned release [v2.102.0](https://github.com/cli/cli/releases/tag/v2.102.0), and the three advisories named under [The `gh` version](#the-gh-version). [actions/attest](https://github.com/actions/attest) for the `v4` action, its permissions, and its [source](https://github.com/actions/attest/blob/v4/src/attest.ts) for when it writes a storage record; [`dotnet nuget verify`](https://learn.microsoft.com/en-us/dotnet/core/tools/dotnet-nuget-verify). The Ubuntu 24.04 [runner image readme](https://github.com/actions/runner-images/blob/main/images/ubuntu/Ubuntu2404-Readme.md), image 20261012.334.1, for the preinstalled `gh`, Node.js, and npm.
- Registry state: `npm view sandboxed-agents` and the nuget.org flat-container and search endpoints, read without credentials.

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

## Offline tests behind the pages and the image

Three tests read the user pages: `TestSecurityPolicyCoversPodmanCreateOptions` checks that `SECURITY.md` names every option `up` passes to Podman create; `TestCommandReferenceMatchesCommandTree` checks the grammar block of the command reference against the parser; `TestQuickGuideCommandsParse` parses every command line of both quick guides. `image/test/signing-keys/test-check-signing-key.sh` checks the key check script against both fingerprint files, and `go test ./image` checks the embedded `sshd_config` policy with the host's `sshd -T`, the entrypoint's host-key startup under a real shell, and the recipe contents.
