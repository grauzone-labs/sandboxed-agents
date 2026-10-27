# Images

This page tells you what the image a sandbox runs in contains, which toolchains you can add, how `build` and `up` create and refresh images, how a sandbox records its toolchain set, and how `update` moves a sandbox to a newer image and rolls back when that fails. The executable embeds the image build context ([ADR 0001](adr/0001-go-for-host-executable-and-in-container-manager.md)); `sandboxed-agents version` prints its asset hash, and every image built from it carries that hash in its tag.

## Base image

The base image is Debian 13 (trixie) slim with Node.js 24 LTS. It contains:

- OpenSSH server and client, Git, GitHub CLI, tmux, `curl`, `jq`, `ripgrep`, `less`, `unzip`, `zip`, `xz-utils`, `file`, `procps`, Bash, coreutils, findutils, `gnupg`, CA certificates, and `libatomic1`, the GCC atomics runtime library that the T3 Code runtime loads.
- Node.js 24 and npm from the NodeSource repository `node_24.x`, installed after its [signing key check](#signing-key-checks).
- Tailscale (`tailscale` and `tailscaled`) from Tailscale's Debian 13 repository, installed after its [signing key check](#signing-key-checks), for the T3 Code Tool's tailnet node ([ADR 0010](adr/0010-t3-code-pairs-over-an-in-sandbox-tailnet-node.md)). The image starts no Tailscale process: the manager starts `tailscaled` as `agent` in userspace networking mode, inside the Tool directory, only after `tools tailscale NAME t3code login`. A sandbox without that login runs no Tailscale process and creates no Tailscale state.
- The user `agent` with UID and GID 1000, the directory `/workspace` owned by `agent`, and no baked SSH host keys.
- The in-container manager at `/usr/local/bin/sandboxed-agents-manager`.
- No agent, no Tool, and none of the toolchains below.

### Installed version manifest

Every image records the versions of what it installed in `/usr/local/share/sandboxed-agents/versions.txt`: `/etc/os-release`, every Debian package with its version, the Node.js and npm versions, and the manager version. A toolchain image rewrites the same file and adds the output of `dotnet --list-sdks`, `playwright --version`, `az version`, and `cc --version` for the toolchains present.

### SSH host keys

The recipe removes the SSH host keys that the Debian package creates, so no key is shared between sandboxes or recorded in an image. The entrypoint runs `ssh-keygen -A` at every container start, before it starts sshd. The sandbox's SSH volume is mounted at `/etc/ssh` by then, so only the keys that volume lacks are created: the keys are generated on first start and survive `stop`, `start`, and `update`, which keep the volumes. If key generation fails, the container fails to start rather than run without host keys.

Podman can accept `exec` before the entrypoint has finished. The manager's `ssh-host-key` command, which SSH setup uses to pin the host key, therefore polls every 25 ms for up to 2 seconds while `/etc/ssh/ssh_host_ed25519_key.pub` is missing or empty, inside the host's 3-second limit for administrative queries. Any other failure, such as a permission error or a malformed non-empty key, fails at once, and the command prints no key on failure.

### SSH server

After the host keys exist, the entrypoint replaces itself with `/usr/sbin/sshd -D -e -f /usr/local/etc/sandboxed-agents/sshd_config`, so sshd runs as root in the foreground as the container's main process, logs to stderr, and the container stops when sshd exits. The configuration file lives in the image, outside the persistent `/etc/ssh` volume, so the volume holds only server state and cannot change the policy. The policy accepts only the user `agent` with public-key authentication against the single file `/etc/ssh/authorized_keys`, where the manager places the key that [SSH setup](ssh.md) installs. Root login, passwords, keyboard-interactive authentication, and agent forwarding are off, and SFTP uses the internal subsystem.

### Signing key checks

The recipe adds two package repositories beyond Debian's, NodeSource for Node.js and Tailscale's for the `tailscale` package, and pins each repository's signing key by fingerprint. For each one it downloads the key into a temporary file and runs `check-signing-key.sh`, which compares the fingerprint of the one primary key the file must contain with the expected fingerprint: `6F71F525282841EEDAF851B42F59B5F99B1BE0B4` for NodeSource and `2596A99EAAB33821893C0A79458CA832957F5868` for Tailscale. Only after the check passes does it write the keyring, dearmored for NodeSource into `/etc/apt/keyrings/nodesource.gpg` and copied as the binary keyring Tailscale publishes into `/etc/apt/keyrings/tailscale.gpg`, and the apt source line with `signed-by` pointing at that file. A differing fingerprint fails the check and prints the repository's name with the expected and the found fingerprint; a file with other than one primary key fails it and prints the count, the expected fingerprint, and every primary fingerprint found. A failed check fails the image build, and nothing from that repository is installed, because the keyring and the source line are never written.

The pins detect a later change of a key, not a key that was substituted before its first recorded copy. If NodeSource or Tailscale rotates its key, `build` stops and builds no image for any toolchain set, the previous images stay usable under their tags, and `up` cannot build a missing base image until the recipe is updated. The provenance of the expected fingerprints and the rotation procedure are in the [maintainer notes](maintainers.md#repository-signing-keys).

## Toolchains

The toolchains are `dotnet`, `playwright`, `azure`, and `native`. All are off by default. `--with none`, accepted by `build`, `up`, and `update NAME`, selects the base image alone and stands alone: `--with none,dotnet` fails. An unknown name fails before Podman is called and lists the valid values: `dotnet`, `playwright`, `azure`, `native`, and `none`.

| Toolchain | Installs |
|---|---|
| `dotnet` | .NET 8 and .NET 10 SDKs (`dotnet-sdk-8.0`, `dotnet-sdk-10.0`) from the Microsoft package feed for Debian 13, with `DOTNET_CLI_TELEMETRY_OPTOUT=1` and `DOTNET_NOLOGO=1`. .NET 9 is not installed. |
| `playwright` | The npm packages `playwright` and `@playwright/test` installed globally under `/usr/local`, so `require('playwright')` resolves through `NODE_PATH=/usr/local/lib/node_modules`. The Chromium, Firefox, and WebKit browsers and their system packages come from `playwright install --with-deps chromium firefox webkit`. Browser binaries live in `/opt/playwright-browsers` (`PLAYWRIGHT_BROWSERS_PATH`), outside the home volume and readable by every user. The Playwright version is not pinned: a build installs the current npm release. |
| `azure` | Azure CLI from Microsoft’s signed `bookworm` apt repository, following its current release, and Microsoft’s Azure DevOps extension 1.0.10, pinned by SHA-256. The extension is installed at build time in Azure CLI’s default system-extension directory under `/opt/az`, outside the home volume and readable by `agent`; dynamic extension installation is disabled. Both versions are recorded in the image manifest. |
| `native` | `build-essential`, `cmake`, `ninja-build`, `pkg-config`, and `gdb`. |

The Azure CLI remains Python-based. On 2026-10-27, Microsoft's [apt guidance](https://learn.microsoft.com/en-us/cli/azure/install-azure-cli-linux?pivots=apt#no-package-for-your-distribution) documents using `bookworm` when a newer Debian release has no package; the image keeps Debian 13 and follows that fallback. Debian 13 is not in Microsoft's tested-distribution table. The Azure recipe checks `microsoft.asc` against the embedded primary fingerprint `BC528686B50D79E339D3721CEB3E94ADBE1229CF` before creating its dedicated keyring and `signed-by` source; apt verifies the package through that key. It downloads the DevOps wheel over HTTPS from Microsoft's distribution and checks the recorded SHA-256 before installing it with no dependency downloads. Azure CLI supplies the extension's runtime dependencies. The build checks `az devops --help`, records both versions and removes `/root/.azure`; no credentials are seeded. `az` and `az devops` need no first-use installation or network request for local help and version commands.

A changed Microsoft key fails the Azure toolchain build before installation; other toolchain sets can still build and the previous Azure image stays usable. The [maintainer notes](maintainers.md#microsoft-azure-cli-signing-key-and-extension) record the key and wheel provenance, the version policy, and how to update them. The .NET recipe separately downloads `packages-microsoft-prod.deb` for Debian 13 over HTTPS and installs it; its key is still not pinned by that recipe.

## Tags and labels

An image is tagged `localhost/sandboxed-agents:SET-HASH`: `HASH` is the asset hash of the executable that built it, and `SET` is the canonical toolchain set, the names sorted, deduplicated, and joined with `-`, or `none` for the base image. The tag contains no controller group, so every group shares the images of one executable, and every toolchain image is built `FROM` the base image, so toolchain sets share the base layers. Every image carries these labels, by which `build` finds images:

| Label | Value |
|---|---|
| `io.sandboxed-agents.image` | `sandboxed-agents` |
| `io.sandboxed-agents.asset-sha256` | The asset hash of the executable that built it |
| `io.sandboxed-agents.toolchains` | The set, comma-separated and sorted, or `none` |
| `io.sandboxed-agents.base-image` | Toolchain images only: the ID of the base image they were built on |

A sandbox container records its set in `io.sandboxed-agents.toolchains` as well. Nothing about images is stored in host state.

A build materializes the complete context in a temporary directory, with text normalized to LF while `.bin` and NUL-containing files keep their bytes, hashes the files it wrote, and runs `podman build` on them: for the base image with `base/Containerfile`, for a toolchain set with a generated `toolchains/Containerfile` that starts `FROM` the base tag, adds the fragments of the selected toolchains in sorted order, and ends by recording the versions. The temporary directory is removed whether the build succeeds or fails, and the tag counts as built only when both the build and the cleanup succeed.

## Build, refresh, and reuse

`build [--with ...]` always builds anew, so a new build installs current packages. With or without `--with`:

1. It lists the images that carry the marker label and the asset hash of the running executable and reads each image's toolchain set. The result is every existing toolchain set of this executable, in any controller group. `--with a,b` adds that set when no image exists for it. Images from other executables (other asset hashes) are neither found nor rebuilt.
2. It rebuilds the base image with `--no-cache --pull=always`, so Podman pulls `debian:trixie-slim` again.
3. It rebuilds each toolchain set in sorted order on the new base image with `--no-cache --pull=never`, even when the tag already exists.

When the base image fails to build, including a failed key check, `build` stops and builds no toolchain image. When a toolchain image fails to build, `build` continues with the remaining sets, then names the failed sets and exits non-zero. A failed build never replaces its tag, so an image that was not rebuilt stays usable under its tag. A successful rebuild moves the tag to the new image; the previous image loses the tag but still backs the sandboxes created from it.

Once the base image has been rebuilt, `build` prints that existing sandboxes keep their image until `update` and that `list` marks them as outdated, also when a toolchain image failed. Because images are shared, the rebuild affects the sandboxes of every controller group. `build` followed by `update` brings a sandbox to fresh packages ([Updating a sandbox](#updating-a-sandbox)).

`up` builds only a missing image and may use the layer cache. A toolchain image counts as missing when its base image label differs from the ID of the current base image, so `up` never creates a sandbox from a toolchain image built on a superseded base. It never rebuilds an existing, current image.

### Recorded set and outdated sandboxes

`up` on an existing sandbox starts it when `--with` is absent or names the recorded set. A different set exits non-zero without starting the sandbox and names `update NAME --with SET`. The recorded set is the container's `io.sandboxed-agents.toolchains` label; a sandbox created before that label existed falls back to the set in the tag of its `Config.Image` name. Only an explicit `--with` needs a recorded set: when neither source yields a valid set, `up --with` fails, while `up` without `--with` still starts the sandbox.

`list` shows each sandbox's set, read the same way. It marks a sandbox as outdated when the immutable image ID of its container differs from the ID of the current image for that set, or when that toolchain image's `io.sandboxed-agents.base-image` label differs from the ID of the current base image. The second case arises when `build` rebuilt the base image and a toolchain build failed, so a base rebuild makes every older sandbox outdated.

### No image removal

No command removes an image, and the executable keeps no list of them. Old images accumulate in Podman's storage after rebuilds and new executable versions. Remove unused ones yourself, for example with `podman image prune`, which cannot remove an image that a sandbox still uses.

## Updating a sandbox

`update NAME` moves one sandbox to the image of the installed executable. It keeps the workspace, home data, SSH server state, loopback port, resource limits (the recorded `memory-swap` included, unchanged), and toolchain set. `update NAME --with dotnet,native` replaces the set; `--with none` selects the base image. An explicit `--with` also replaces a recorded set that cannot be read; without `--with`, an unreadable recorded set refuses the update, and an invalid `--with` set fails before Podman is called. A current image prints `already up to date` without asking about agent sessions or replacing the sandbox.

The command checks prerequisites, takes the sandbox's lifecycle lock, checks ownership and state, and builds any missing image before changing the sandbox; a toolchain image based on an older base image is rebuilt first. A running agent session blocks replacement unless `--force` is given. When the manager cannot answer, the command also requires `--force`, since it cannot rule out sessions.

During replacement, the old container becomes a backup. The replacement uses the same volumes and is created before the old container stops; it starts only after that stop succeeds. Readiness requires the manager's version response and a completed Ed25519 SSH key exchange through the recorded port. Pending probes retry every 250 milliseconds, with a 3-second limit per probe and a 60-second total deadline. The SSH probe uses temporary known_hosts data and does not change the installed SSH setup.

A create, start, or readiness failure rolls the update back: the replacement is removed, and the old container gets its name and its prior running or stopped state back. When a rollback step fails, the output names that step and the retained backup, and the next `update NAME` judges the objects afresh ([Recovering an interrupted update](#recovering-an-interrupted-update)). No update or rollback path removes a named volume. Sessions ended by stopping the old container cannot be restarted by rollback; the output names them, or says their names were unavailable. A stopped sandbox is started for readiness and stopped again after successful backup removal.

If backup removal fails, the update succeeded but the sandbox is `update interrupted`: other commands refuse it, and the output says that `update NAME` cleans up. Because a sandbox that was stopped before the update is stopped again only after the backup is removed, that failure also leaves the new container running, and the output says so. The output also names the manual alternative, `podman rm sandboxed-agents-backup.GROUP.NAME` on Linux or `podman --connection 'CONNECTION' rm sandboxed-agents-backup.GROUP.NAME` in PowerShell on Windows with the connection the preflight selected; it applies only to this case, where the backup is the superseded container. If the final stop fails, the update succeeded but the sandbox is still running; `stop NAME` stops it.

One window remains in every update: an interruption after the backup is removed and before the final stop leaves a sandbox that was stopped before the update running under its name with no backup. Such a sandbox counts as updated and running; `stop NAME` restores the stopped state.

### Recovering an interrupted update

`update NAME` on a sandbox with a leftover backup recovers the interrupted update instead of updating, without building an image. Under the lifecycle lock it first checks the owner of the container, the backup, and the volumes. An owner conflict is reported first, naming the foreign objects and pointing to Podman, and nothing is restored or removed. The command then judges the objects present, because Podman does not record how an interrupted state arose:

- **Running backup.** The old container was never stopped, so the replacement never started. The replacement is removed without being started, the backup is renamed back to the sandbox's name, and the old sandbox keeps running. The command exits non-zero without updating.
- **Stopped backup and a supported replacement.** The replacement is started when it is stopped, and the command waits for readiness. When the replacement is ready, the update counts as done: the backup is removed, the replacement is stopped again when its recorded prior state label says the sandbox was stopped before, and the command exits zero.
- **Replacement that cannot be used.** A replacement that cannot start or fails readiness is not kept. The same holds for one the command must not start or probe: its running state is missing, its recorded SSH port is invalid, or its recorded storage is unsupported, meaning not exactly one mount each at `/workspace`, `/home/agent`, and `/etc/ssh` and no other, where home and SSH server state are the sandbox's own named volumes among the owner-checked volumes, and the workspace is either that named volume or a bind of a non-empty host path. Replacement and recovery share that storage check, which reads no resource limit, toolchain set, or image, so a replacement attached to another sandbox's volume is never started or probed. In each case the replacement is removed, the backup is renamed back, and the old container is started when its prior state was running. The command says that the sandbox was restored and exits non-zero without updating; the next `update NAME` updates normally.
- **Unknown prior state.** A stopped backup whose replacement is absent or carries no valid prior state label cannot be classified. The sandbox is restored and stays stopped, and the output says that the previous state could not be determined and names `start NAME`.

Recovery restores the sandbox's volumes, configuration, and prior running or stopped state, and changes no volume. It cannot resume a process that the interrupted update already ended. It does not run the agent-session guard that an ordinary replacement runs: it takes no `--force` decision, and removing or stopping a replacement can end sessions in it without the command naming them. Starting a restored sandbox ends none.

Only two conditions retain the backup and refuse without restoring: a backup without a recorded running state, and an owner conflict, which is reported before anything else. Both change nothing and point to Podman. A backup that stays retained may be the only good container and must not be removed by hand.

A failure during recovery leaves one of two states, and the output tells them apart. If removing the replacement or renaming the backup fails, the output names that step and the retained backup, and the next `update NAME` judges the objects afresh. If the rename succeeded and only starting the restored sandbox fails, the old container is already back under the sandbox's name and stopped; the output says that the sandbox was restored but could not be started and names `start NAME`. A failed removal of the backup after a ready replacement is the `update interrupted` case above, and `update NAME` retries that cleanup.

### Updating every sandbox

`update --all [--force]` takes no `NAME` and no `--with`; `update` with neither or both of `NAME` and `--all`, or `--all` with `--with`, fails with a usage message before any Podman call. After the preflight it reads one name inventory, `podman ps --all` and `podman volume ls`, and takes only the names of the current controller group's sandboxes, including those of which only volumes or only a backup remain ([ADR 0007](adr/0007-serialize-sandbox-lifecycle-commands.md#name-inventory-of-update---all)). The inventory supplies names and no state. For each name the command takes the sandbox's lifecycle lock and only then inspects its objects, so every decision rests on facts read under the lock. Every lock stays held until the command exits.

Per sandbox, after the lock and a fresh inspection:

- A taken lock, an owner conflict, or an unreadable state is reported with its own diagnostic and counted as not updated. A name that vanished after the inventory gets the `does not exist in this controller group` message of `update NAME` and is counted as not updated too. No image is built for an owner-conflicted or locked sandbox.
- A sandbox of which only volumes remain is passed over with a note naming `up NAME`, is counted separately as volumes only, and is not a failure.
- A sandbox with a leftover backup is recovered as `update NAME` recovers it, without the session guard. It needs no image.
- A sandbox whose image is current is reported as already up to date, with no build and no session check.
- Every other sandbox needs the image for its recorded toolchain set.

The command builds every needed image once per set before it changes any sandbox, recoveries included. The build barrier is all-or-nothing ([ADR 0007](adr/0007-serialize-sandbox-lifecycle-commands.md)): if a build fails, no sandbox is changed and every pending sandbox is reported as not updated, even one whose own image built. After the builds it replaces or recovers the sandboxes one after another. For an ordinary replacement the session guard runs per sandbox directly before its change: without `--force` a sandbox with a running agent session, or whose sessions cannot be determined, is left unchanged, reported with its sessions, and counted as not updated while the others continue; with `--force` each sandbox behaves as in `update NAME --force`, naming the ended sessions or saying that they cannot be named. A recovery runs no session guard and so can end sessions it cannot name. A failed replacement is rolled back as in `update NAME`, a failed rollback step reports the diagnostics described there, a recovery that restores the old sandbox counts as not updated, and the command continues with the next sandbox. It prints a summary such as `2 updated, 1 current, 1 volumes only, 1 not updated` and exits non-zero when any sandbox was not updated; volumes-only sandboxes do not count as not updated. While it runs, other lifecycle commands on the sandboxes it holds are refused, and a second `update --all` passes them over.

## Live image tests

The image coverage is the `images` case of the live suite ([releases](releases.md#live-suite)). It creates one sandbox per toolchain and one without. Right after each `up` it runs the manager's `ssh-host-key` query as root, then checks the SSH server: the container is running with the user `0:0`, PID 1 is `sshd` with UID 0, port 22 is published once on `127.0.0.1` at the port recorded in the container's `io.sandboxed-agents.port` label, and an unauthenticated `ssh -v` from the host to that port completes the key exchange and records in a fresh `known_hosts` the same Ed25519 key that the manager query returned. For the base sandbox it repeats the key comparison and the server check after `stop` and `start`. The exchange proves a reachable sshd with the volume's host key, not a login, because every authentication method is off on the client side. The case runs each smoke check inside its sandbox as `agent` with `shell NAME`, which is a Podman `exec` and not an SSH session, calls `build` once, checks the labels, the shared base layers, that existing sandboxes keep their image, and that `list` marks them outdated, and removes its sandboxes and volumes.

The live suite also checks the swap limit: a new sandbox's `podman inspect` shows `MemorySwap` equal to `Memory`, and its cgroup `memory.swap.max` is `0`. The lifecycle case checks this, the `images` case checks it for each image, and the `update` case checks it at the initial sandbox, after replacement, and after rollback. Legacy swap allowances are covered by the offline CLI tests.

The suite's `t3code` case also checks service restore on a real successful replacement from the base image to `native` and on a forced readiness rollback to that replacement. It verifies the Tool's pin, live HTTP service, and UID/GID 1000 after both, and compares the sandbox's publish, network, hardening, resource limits, and three volumes with their pre-enable values. This adds no Tailscale login or phone reachability coverage; those remain manually recorded ([live suite](releases.md#live-suite)).

Because `build` renews every image of the current executable on the Podman host, the coverage has its own opt-in beside the suite's variables, `SANDBOXED_AGENTS_LIVE_IMAGES_TESTS=1`:

```sh
SANDBOXED_AGENTS_LIVE_TESTS=1 \
SANDBOXED_AGENTS_GROUP=live \
SANDBOXED_AGENTS_LIVE_RECORD_DIR="$HOME/live-suite-records" \
SANDBOXED_AGENTS_LIVE_IMAGES_TESTS=1 \
  go test ./cmd/sandboxed-agents -run '^TestLiveSuite$' -timeout 120m -v
```

Without the image opt-in the suite runs its other cases and records the image coverage as `not-run`. The suite removes its sandboxes, volumes, and group state but none of the images it builds; removing them is a maintainer step, and the runs on record are listed in the [maintainer notes](maintainers.md#live-runs-on-record).
