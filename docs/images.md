# Images

This page tells you what the image a sandbox runs in contains, which toolchains you can add, how `build` and `up` create and refresh images, how a sandbox records its toolchain set, and how `update` moves a sandbox to a newer image and rolls back when that fails. The executable embeds the image build context ([ADR 0001](adr/0001-go-for-host-executable-and-in-container-manager.md)); `sandboxed-agents version` prints its asset hash, and every image built from it carries that hash in its tag.

## Base image

The base image is Debian 13 (trixie) slim with Node.js 24 LTS. It contains:

- OpenSSH server and client, Git, GitHub CLI, tmux, `curl`, `jq`, `ripgrep`, `less`, `unzip`, `zip`, `xz-utils`, `file`, `procps`, Bash, coreutils, findutils, `gnupg`, CA certificates, and `libatomic1`, the GCC atomics runtime library that the T3 Code runtime loads.
- Node.js 24 from the NodeSource repository `node_24.x`, installed after its [signing key check](#signing-key-checks), and then npm at `npm@latest`, which replaces the npm that the NodeSource package carries. Node.js stays on major version 24. `build` resolves the npm release when it rebuilds the base image. A build that reuses cached layers keeps their npm ([Build, refresh, and reuse](#build-refresh-and-reuse)), and nothing updates npm at sandbox startup. A latest npm whose declared Node.js range excludes the installed Node.js 24 release fails the build ([npm engine requirements](#npm-engine-requirements)). Toolchain images are built on this base image and inherit its npm. Image builds do not affect the host's npm or the npm that the release workflow pins for publishing.
- Tailscale (`tailscale` and `tailscaled`) from Tailscale's Debian 13 repository, installed after its [signing key check](#signing-key-checks), for the T3 Code Tool's tailnet node ([ADR 0010](adr/0010-t3-code-pairs-over-an-in-sandbox-tailnet-node.md)). The image starts no Tailscale process: the manager starts `tailscaled` as `agent` in userspace networking mode, inside the Tool directory, only after `tools tailscale NAME t3code login`. A sandbox without that login runs no Tailscale process and creates no Tailscale state.
- The user `agent` with UID and GID 1000, the directory `/workspace` owned by `agent`, and no baked SSH host keys.
- The in-container manager at `/usr/local/bin/sandboxed-agents-manager`.
- No agent, no Tool, and none of the toolchains below.

### Installed version manifest

Every image records the versions of what it installed in `/usr/local/share/sandboxed-agents/versions.txt`: `/etc/os-release`, every Debian package with its version, the Node.js and npm versions, and the manager version. A toolchain image rewrites the same file and adds the output of `dotnet --list-sdks`, `playwright --version`, `az version`, and `cc --version` for the toolchains present.

### npm engine requirements

The npm install step passes `--engine-strict`, so npm refuses a latest release whose declared Node.js range excludes the installed Node.js 24 with `EBADENGINE`, and the image build fails. Only the declared range is checked. The build neither falls back to an earlier npm nor moves to another Node.js major version, and no option overrides the floating `npm@latest` choice.

An `EBADENGINE` failure ends the Podman build before Podman tags an image, so the existing tag stays and existing images and sandboxes stay usable ([Build, refresh, and reuse](#build-refresh-and-reuse) covers a failure after tagging), but a new executable with no image yet cannot create a sandbox until an image build succeeds. Rerun `build` later. Each `build` installs the newest `node_24.x` package that NodeSource offers and `npm@latest`; the engine check passes once npm's declared range includes that Node.js 24 release, which can follow either a new npm release or a new NodeSource Node.js 24 release. Passing the check does not guarantee that the rest of the build succeeds.

### SSH host keys

The recipe removes the SSH host keys that the Debian package creates, so no key is shared between sandboxes or recorded in an image. The entrypoint runs `ssh-keygen -A` at every container start, before it starts sshd. The sandbox's SSH volume is mounted at `/etc/ssh` by then, so only the keys that volume lacks are created: the keys are generated on first start and survive `stop`, `start`, and `update`, which keep the volumes. If key generation fails, the container fails to start rather than run without host keys.

Podman can accept `exec` before the entrypoint has finished. The manager's `ssh-host-key` command, which SSH setup uses to pin the host key, therefore polls every 25 ms for up to 6 seconds while `/etc/ssh/ssh_host_ed25519_key.pub` is missing or empty, inside the host's 15-second total budget for ordinary administrative requests. That budget includes process completion, up to one second of pipe draining per attempt, and at most one retry for an explicitly read-only request after a transport, deadline, or pipe-completion failure. The first read attempt has at most ten seconds; its retry uses only the remaining budget. A manager operation failure is not retried, failed output is never accepted as a response, and SSH authorization/deauthorization writes are never retried. Readiness retains its separate 3-second probes and 60-second overall deadline. Any other failure, such as a permission error or a malformed non-empty key, fails at once, and the command prints no key on failure.

### SSH server

After the host keys exist, the entrypoint replaces itself with `/usr/sbin/sshd -D -e -f /usr/local/etc/sandboxed-agents/sshd_config`, so sshd runs as root in the foreground as the container's main process, logs to stderr, and the container stops when sshd exits. The configuration file lives in the image, outside the persistent `/etc/ssh` volume, so the volume holds only server state and cannot change the policy. The policy accepts only the user `agent` with public-key authentication against the single file `/etc/ssh/authorized_keys`, where the manager places the key that [SSH setup](ssh.md) installs. Root login, passwords, keyboard-interactive authentication, and agent forwarding are off, and SFTP uses the internal subsystem.

### Signing key checks

The recipe adds two package repositories beyond Debian's, NodeSource for Node.js and Tailscale's for the `tailscale` package, and pins each repository's signing key by fingerprint. For each one it downloads the key into a temporary file and runs `check-signing-key.sh`, which compares the fingerprint of the one primary key the file must contain with the expected fingerprint: `6F71F525282841EEDAF851B42F59B5F99B1BE0B4` for NodeSource and `2596A99EAAB33821893C0A79458CA832957F5868` for Tailscale. Only after the check passes does it write the keyring, dearmored for NodeSource into `/etc/apt/keyrings/nodesource.gpg` and copied as the binary keyring Tailscale publishes into `/etc/apt/keyrings/tailscale.gpg`, and the apt source line with `signed-by` pointing at that file. A differing fingerprint fails the check and prints the repository's name with the expected and the found fingerprint; a file with other than one primary key fails it and prints the count, the expected fingerprint, and every primary fingerprint found. A failed check fails the image build, and nothing from that repository is installed, because the keyring and the source line are never written.

The pins detect a later change of a key, not a key that was substituted before its first recorded copy. If NodeSource or Tailscale rotates its key, `build` stops and builds no image for any toolchain set, the previous images stay usable under their tags, and `up` cannot build a missing base image until the recipe is updated. The provenance of the expected fingerprints and the rotation procedure are in the [maintainer notes](maintainers.md#repository-signing-keys).

Neither pin covers the replacement npm. The build fetches `npm@latest` from the public npm registry, `registry.npmjs.org`, over HTTPS, and npm checks the downloaded archive against the integrity value in the registry's metadata for that release. No separately pinned signing key, version, or hash applies to this download, and that check does not authenticate the publisher independently of the registry. The NodeSource key covers the NodeSource package, not the npm that replaces its copy.

## Toolchains

The toolchains are `dotnet`, `playwright`, `azure`, and `native`. All are off by default. `--with none`, accepted by `build`, `up`, and `update NAME`, selects the base image alone and stands alone: `--with none,dotnet` fails. An unknown name fails before Podman is called and lists the valid values: `dotnet`, `playwright`, `azure`, `native`, and `none`.

| Toolchain | Installs |
|---|---|
| `dotnet` | .NET 8 and .NET 10 SDKs (`dotnet-sdk-8.0`, `dotnet-sdk-10.0`) from the Microsoft package feed for Debian 13, with `DOTNET_CLI_TELEMETRY_OPTOUT=1` and `DOTNET_NOLOGO=1`. .NET 9 is not installed. |
| `playwright` | The npm packages `playwright` and `@playwright/test` installed globally under `/usr/local`, so `require('playwright')` resolves through `NODE_PATH=/usr/local/lib/node_modules`. The Chromium, Firefox, and WebKit browsers and their system packages come from `playwright install --with-deps chromium firefox webkit`. Browser binaries live in `/opt/playwright-browsers` (`PLAYWRIGHT_BROWSERS_PATH`), outside the home volume and readable by every user. The Playwright version is not pinned: a build installs the current npm release. |
| `azure` | Azure CLI from Microsoft’s signed `bookworm` apt repository, following its current release, and Microsoft’s Azure DevOps extension 1.0.10, pinned by SHA-256. The extension is installed at build time in Azure CLI’s default system-extension directory under `/opt/az`, outside the home volume and readable by `agent`; dynamic extension installation is disabled. Pinned, hash-verified PyPI credential dependencies are installed alongside it at build time. The manifest records the CLI, extension and dependency versions. |
| `native` | `build-essential`, `cmake`, `ninja-build`, `pkg-config`, and `gdb`. |

The Azure CLI remains Python-based. On 2026-10-27, Microsoft's [apt guidance](https://learn.microsoft.com/en-us/cli/azure/install-azure-cli-linux?pivots=apt#no-package-for-your-distribution) documents using `bookworm` when a newer Debian release has no package; the image keeps Debian 13 and follows that fallback. Debian 13 is not in Microsoft's tested-distribution table. The Azure recipe checks `microsoft.asc` against the embedded primary fingerprint `BC528686B50D79E339D3721CEB3E94ADBE1229CF` before creating its dedicated keyring and `signed-by` source; apt verifies the package through that key. It downloads the DevOps wheel over HTTPS from Microsoft's distribution and checks the recorded SHA-256 before installing it with no dependency downloads. Azure CLI supplies the main extension runtime. A narrow exception approved on 2026-10-27 permits PyPI wheels for keyring and its transitive runtime dependencies: every version and SHA-256 is pinned in `azure-devops-requirements.txt`, and pip requires hashes and binary wheels at build time. Microsoft artifact verification is unchanged. The build checks `az devops --help` and exercises the real credential store as `agent` with downloads forbidden, using disposable configuration and a dummy nonsecret token. It records the CLI, extension and dependency versions and removes `/root/.azure`; no credentials are seeded. `az` and `az devops` need no first-use installation or network request for local help and version commands.

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

A build materializes the complete context in a temporary directory, with text normalized to LF while `.bin` and NUL-containing files keep their bytes, hashes the files it wrote, and runs `podman build` on them: for the base image with `base/Containerfile`, for a toolchain set with a generated `toolchains/Containerfile` that starts `FROM` the base tag, adds the fragments of the selected toolchains in sorted order, and ends by recording the versions. The temporary directory is removed whether the build succeeds or fails, and the tag counts as built only when both the build and the cleanup succeed. The one exception is the rebuild with `--jobs` above 1, which reports an image that the engine built but whose cleanup or output failed apart from the failed sets ([below](#rebuilding-with---jobs)).

## Build, refresh, and reuse

`build [--with ...] [--jobs N]` always builds anew, so a new build installs current packages, including the current `npm@latest`. With or without `--with`:


1. It lists the images that carry the marker label and the asset hash of the running executable and reads each image's toolchain set. The result is every existing toolchain set of this executable, in any controller group. `--with a,b` adds that set when no image exists for it. Images from other executables (other asset hashes) are neither found nor rebuilt.
2. It rebuilds the base image with `--no-cache --pull=always`, so Podman pulls `debian:trixie-slim` again.
3. It rebuilds each toolchain set in sorted order on the new base image with `--no-cache --pull=never`, even when the tag already exists. With `--jobs N` and N above 1, up to N toolchain sets are rebuilt at the same time ([below](#rebuilding-with---jobs)).

When the base image fails to build, including a failed key check, `build` stops and builds no toolchain image. When a toolchain image fails to build, `build` continues with the remaining sets, then names the failed sets and exits non-zero. A build that fails inside Podman does not move its tag, so an image that was not rebuilt stays usable under its tag. Once Podman has built and tagged an image, removing the temporary build context can still fail: `build` then reports the failure and does not roll the tag back. With `--jobs 1` it counts the set as failed although the new tag may already have moved; with `--jobs` above `1` it reports `image TAG was rebuilt;` and not a failed set ([below](#rebuilding-with---jobs)). Existing sandboxes stay on their old image until `update`. A successful rebuild moves the tag to the new image; the previous image loses the tag but still backs the sandboxes created from it.

Once the base image has been rebuilt, `build` prints that existing sandboxes keep their image until `update` and that `list` marks them as outdated, also when a toolchain image failed. Because images are shared, the rebuild affects the sandboxes of every controller group. `build` followed by `update NAME` brings a sandbox to the packages that `build` installed, including the npm that was `latest` when `build` ran ([Updating a sandbox](#updating-a-sandbox)); `build` alone leaves every existing sandbox, running or stopped, as it is.

`up` builds only a missing image and may use the layer cache; `update NAME` and `update --all` build a missing image the same way. None of them promises a fresh `npm@latest` resolution: an existing image or cached layers keep the npm they already hold, and only `build` guarantees a fresh resolution, so run it first when you want the current npm. A toolchain image counts as missing when its base image label differs from the ID of the current base image, so `up` never creates a sandbox from a toolchain image built on a superseded base. It never rebuilds an existing, current image.

### Rebuilding with `--jobs`

`--jobs N` takes a positive integer. Without it the value is `1`, and a rebuild with `--jobs 1`, given or not, is the sequential rebuild above, with the same output, flags, and error text.

- **A conservative, unmeasured default.** `1` is not sized from the host's CPU or memory, no timing has been measured, and no speedup is claimed. A larger value is your choice and an opt-in.
- **Base first and alone.** The base image is rebuilt before any toolchain image starts, and while it runs this command builds nothing else; a different process, such as an `up` for another tag, can build at the same time. Its immutable image ID is read while the base tag's lock is still held, the lock is then released, and every toolchain build uses that ID, so all of them are built on the same new base. The lock of a toolchain tag covers that tag's build; `build` still gives no atomicity over several tags, and it holds no port lock.
- **Bounded workers.** After the base, at most N toolchain sets are built at the same time. A set that fails frees its slot, and the other sets go on.
- **Output.** With N above 1, the complete output of each image, standard output and standard error together, is printed as one block when that image finishes, in the order the images finish, headed `build output for base image:` or `build output for toolchain set SET:` and followed by `built image TAG` when it succeeded. Nothing is truncated, and a block is never interleaved with another block or with a progress or lock message, also when both streams go to the same terminal. Within one block the output keeps the order it arrived in; standard output and standard error are not ordered against each other.
- **Progress.** With N above 1, while an image builds, standard error shows `build progress SET: N bytes received`, `none` standing for the base image. The lines report bytes that actually arrived: the first bytes are reported at once, then an image updates at most once a second. A build that is silent prints nothing and no percentage is invented. Lock waits keep their existing line. The progress lines are supplemental and never change a build.
- **Failures.** Only a set whose build genuinely failed, including a failure before the build ran, is named in `failed toolchain sets` in sorted order, as before, with exit status 1; the full output of a failed image is in its block, not repeated in the error. A build that fails inside Podman never replaces its tag, and `build` removes no image and retries no build that ran.
- **A rebuilt image whose output could not be written (N above 1).** With N above 1, when the engine built an image successfully, a later problem with its block (the heading, the copy, the result line), with reading its output, or with removing its temporary log or the build context never turns it into a failed set. The error says `image TAG was rebuilt;` and names the operation that failed, and the command exits with status 1 for that problem. A rebuilt base whose output could not be written still has its ID read and its toolchain sets built, and the final error keeps the guidance that existing sandboxes keep their image until `update`. When real failures and output problems occur together, only the genuine failures are in the sorted list and the output problems are reported apart. Progress lines that cannot be written change nothing. With `--jobs 1`, given or not, none of this applies: the output, the treatment of a caller's write error, and the classification of a failure are as they always were, so an error in the build context cleanup or in the build evidence after the engine succeeded still makes that set a failed set.
- **Temporary logs.** Each image's output is held in its own private temporary file while it builds. A normal end closes and removes it, and a removal that fails is reported. If the process is killed, such a file (`sandboxed-agents-build-output-*`) can stay, as the temporary build context can. The command removes only the exact files of its own run and sweeps no others; the files can hold build output, which is not guaranteed to be free of secrets.
- **Unchanged.** Every rebuild still uses `--no-cache`; the base keeps `--pull=always` and each toolchain `--pull=never`; the labels and the guidance that existing sandboxes keep their image until `update` stay as above.

Fake-engine tests prove this behaviour. No native Windows run and no live timing exist yet: whether `--jobs` above `1` is faster, and what it costs in memory, is unmeasured, and the build times of earlier runs (roughly 300 to 800 seconds on Linux and 869 to 930 on Windows for the one no-cache `build`) are not compared with it.

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

Recovery builds no image and does not compare the replacement with the current image tag, so a replacement that it completes may come from an older image. After a recovery, check `list` and run `update NAME` again if the sandbox is still marked outdated.

### Updating every sandbox

`update --all [--force]` takes no `NAME` and no `--with`; `update` with neither or both of `NAME` and `--all`, or `--all` with `--with`, fails with a usage message before any Podman call. After the preflight it reads one name inventory, `podman ps --all` and `podman volume ls`, and takes only the names of the current controller group's sandboxes, including those of which only volumes or only a backup remain ([ADR 0007](adr/0007-serialize-sandbox-lifecycle-commands.md#name-inventory-of-update---all)). The inventory supplies names and no state. For each name the command takes the sandbox's lifecycle lock and only then inspects its objects, so every decision rests on facts read under the lock. Every lifecycle lock stays held until the command exits; the image-tag locks it takes while it ensures an image are released after each ensure.

Per sandbox, after the lock and a fresh inspection:

- A taken lock, an owner conflict, or an unreadable state is reported with its own diagnostic and counted as not updated. A name that vanished after the inventory gets the `does not exist in this controller group` message of `update NAME` and is counted as not updated too. No image is built for an owner-conflicted or locked sandbox.
- A sandbox of which only volumes remain is passed over with a note naming `up NAME`, is counted separately as volumes only, and is not a failure.
- A sandbox with a leftover backup is recovered as `update NAME` recovers it, without the session guard. It needs no image.
- A sandbox whose image is current is reported as already up to date, with no build and no session check.
- Every other sandbox needs the image for its recorded toolchain set.

The command builds every needed image once per set before it changes any sandbox, recoveries included. The build barrier is all-or-nothing ([ADR 0007](adr/0007-serialize-sandbox-lifecycle-commands.md)): if a build fails, no sandbox is changed and every pending sandbox is reported as not updated, even one whose own image built. After the builds it replaces or recovers the sandboxes one after another. For an ordinary replacement the session guard runs per sandbox directly before its change: without `--force` a sandbox with a running agent session, or whose sessions cannot be determined, is left unchanged, reported with its sessions, and counted as not updated while the others continue; with `--force` each sandbox behaves as in `update NAME --force`, naming the ended sessions or saying that they cannot be named. A recovery runs no session guard and so can end sessions it cannot name. A failed replacement is rolled back as in `update NAME`, a failed rollback step reports the diagnostics described there, a recovery that restores the old sandbox counts as not updated, and the command continues with the next sandbox. It prints a summary such as `2 updated, 1 current, 1 volumes only, 1 not updated` and exits non-zero when any sandbox was not updated; volumes-only sandboxes do not count as not updated. While it runs, other lifecycle commands on the sandboxes it holds are refused, and a second `update --all` passes them over.

## Live image tests

The live harness bounds each operation by its class ([Operation bounds and early stop](#operation-bounds-and-early-stop)) and supervises its owned
host process tree (a Linux process group or Windows job). Cancellation ends that
local tree and reports elapsed time; it does not prove that a remote engine or a
detached mapping helper stopped. Reconcile recorded objects and engine state before
cleanup after an ambiguous timeout. Successful completion preserves intentionally
detached work; a descendant holding an output pipe beyond the one-second drain
limit instead fails with incomplete output and terminates the remaining owned tree.
For an unestablished Linux ownership-mapping delay, the
[isolated benchmark](ownership-benchmark.md) prepares a bounded comparison without
changing the active store or claiming a storage fix.

The image coverage is the `images` case of the live suite ([releases](releases.md#live-suite)). It creates one sandbox per toolchain and one without. Right after each `up` it runs the manager's `ssh-host-key` query as root, then checks the SSH server: the container is running with the user `0:0`, PID 1 is `sshd` with UID 0, port 22 is published once on `127.0.0.1` at the port recorded in the container's `io.sandboxed-agents.port` label, and an unauthenticated `ssh -v` from the host to that port completes the key exchange and records in a fresh `known_hosts` the same Ed25519 key that the manager query returned. For the base sandbox it repeats the key comparison and the server check after `stop` and `start`. The exchange proves a reachable sshd with the volume's host key, not a login, because every authentication method is off on the client side. The case runs each smoke check inside its sandbox as `agent` with `shell NAME`, which is a Podman `exec` and not an SSH session, calls `build` once, checks the labels, the shared base layers, that existing sandboxes keep their image, and that `list` marks them outdated, and removes its sandboxes and volumes.

The live suite also checks the swap limit: a new sandbox's `podman inspect` shows `MemorySwap` equal to `Memory`, and its cgroup `memory.swap.max` is `0`. The lifecycle case checks this, the `images` case checks it for each image, and the `update` case checks it at the initial sandbox, after replacement, and after rollback. Legacy swap allowances are covered by the offline CLI tests.

The suite's `t3code` case also checks service restore on a real successful replacement from the base image to `native` and on a forced readiness rollback to that replacement. It verifies the Tool's pin, live HTTP service, and UID/GID 1000 after both, and compares the sandbox's publish, network, hardening, resource limits, and three volumes with their pre-enable values. This adds no Tailscale login or phone reachability coverage; those remain manually recorded ([live suite](releases.md#live-suite)).

**Use a new record directory for each run.** Running by hand, set `SANDBOXED_AGENTS_LIVE_RECORD_DIR` to a new, empty directory for every invocation, and never reuse one: a directory left by an earlier run can keep that run's files, including `engine-failures-<platform>.json`, which then are not this run's evidence ([Whose file it is](#engine-failure-snapshots)). The block that runs the suite below sets a new `RUN_ID` (`$runId` in PowerShell) itself and builds the directory `$HOME/live-suite-records/$RUN_ID` (`live-suite-records\$runId` under `$env:USERPROFILE`) from it. The collector block that follows uses that same value and directory from the same shell session, so paste it into the session that ran the suite and never set a second ID there: its `-record` path has to name the record that this run wrote, and the block stops when no ID is set.

Because `build` renews every image of the current executable on the Podman host, the coverage has its own opt-in beside the suite's variables, `SANDBOXED_AGENTS_LIVE_IMAGES_TESTS=1`:

```sh
RUN_ID="$(date -u +%Y%m%dT%H%M%SZ)"
SANDBOXED_AGENTS_LIVE_TESTS=1 \
SANDBOXED_AGENTS_LIVE_OVERLAP=1 \
SANDBOXED_AGENTS_LIVE_BUILD_JOBS=1 \
SANDBOXED_AGENTS_GROUP=live \
SANDBOXED_AGENTS_LIVE_RECORD_DIR="$HOME/live-suite-records/$RUN_ID" \
SANDBOXED_AGENTS_LIVE_IMAGES_TESTS=1 \
  go test ./cmd/sandboxed-agents -run '^TestLiveSuite$' -timeout 120m -v
```

The two settings are written out as `1` in the manual blocks, so that a value left in your shell cannot change a manual run; raise one only by choice, as described under Overlap and parallel rebuild. The six cases and the stop policy are unchanged.

Without the image opt-in the suite runs its other cases and records the image coverage as `not-run`. The suite removes its sandboxes, volumes, and group state but none of the images it builds; removing them is a maintainer step, and the runs on record are listed in the [maintainer notes](maintainers.md#live-runs-on-record).

**Overlap and parallel rebuild.** Two optional positive integers select how much of the suite runs at the same time. `SANDBOXED_AGENTS_LIVE_OVERLAP` bounds how many independent pieces of the suite run together, and `SANDBOXED_AGENTS_LIVE_BUILD_JOBS` is the `--jobs N` of the suite's one no-cache rebuild ([`--jobs`](#rebuilding-with---jobs)). Each defaults to `1`, unset or empty, on Linux and on Windows alike. Zero, a negative number, a value that is not a number, or one that overflows is refused before any engine call. No larger default exists: none is calibrated, paired measurements with the engine's CPU and memory are pending, and none of this claims a faster run.

- **`1`.** The suite runs its former sequence exactly: preflight, lifecycle, update, agent, t3code, images, with the same commands in the same order and no extra `up`. With `BUILD_JOBS` at `1` the rebuild is the exact former `build` invocation.
- **More than `1` for the cases.** Preflight still runs first and alone. The existing first `up` of the `lifecycle` case ensures the base image, and no other case starts before it has finished; no extra `up` and no prewarming is added. After it, the rest of `lifecycle`, the `update`, `agent`, and `t3code` cases, and the five initial toolchain units of `images` share one budget of that many units. One initial toolchain unit is one sandbox's creation and its SSH, restart, smoke, swap, and image checks. The useful maximum is the number of independent cases plus the five toolchain units (nine); a larger value admits all of them and adds no work.
- **More than `1` for the rebuild.** `--jobs N` is added to the existing single rebuild and to no other command. There is still exactly one no-cache rebuild, with the same exclusivity below, and no extra build.
- **Join.** The one no-cache `build` and every later check of `images` (the replaced, kept, shared-layer, outdated, and removal checks) wait until every independent case, its cleanup included, has finished. The image cleanup also joins them when `images` fails early, and the check that no object of the group remains runs after the whole suite. The suite's own `remove` calls share one gate, because each product `remove` reaches the group's nonblocking SSH configuration lock; a removal that has to wait logs `live operation cleanup: waiting for another suite remove` and stays inside its cleanup operation bound, and the product lock itself stays nonblocking.
- **Records.** The schema 2 record keeps its six fixed checks, their order, and one result each; `images` stays one result with one duration, and no field is added. The initial toolchain subtests inside `images` are not release checks and add no duration case: the collector ignores their completion lines and still refuses a fixed case that completes twice, with its parser unchanged. An interrupted run keeps its initial failing record.
- **After a failure.** With `SANDBOXED_AGENTS_LIVE_STOP_AFTER_FAILURE=1`, every queued piece of work checks the outcome of the running tests before it is admitted, so a failure that did not stop its test, or that happened during a cleanup, also refuses it. Refused work stays `not-run`. Work that is already running, and its cleanup, finishes: nothing is cancelled. An `images` case whose initial work was skipped, or that follows a failure, is reported failing and incomplete, never passed. Which of `fail` and `not-run` it gets depends on how far it had come: if the case had already been entered or its initial work admitted when a sibling failed, it can end `fail` even before its own first operation, and if it was still queued and had not been entered, it stays `not-run`. That is the conservative result for incomplete coverage, and the timing of the failure decides which boundary was reached; no operation failure of its own is invented. A removal that waits its turn at the suite's remove gate spends the same operation bound while it waits, so a stalled removal can make a queued one fail at its bound; it stays a `fail`, and the final check that no object of the group remains still runs.
- **If `lifecycle` fails before the base is ensured.** With the overlap above `1`, a failing first `up` of `lifecycle` ends that check as `fail`. With the stop option off, the suite then joins the cleanup of `lifecycle` and runs `update`, `agent`, `t3code`, and the selected `images` one after another in their original order, with all their cleanups and assertions; the initial image work is then serial and the one rebuild still receives the configured jobs. With the stop option on, those checks stay `not-run`. No extra `up` and no retry is added.
- **Reading the log and the status.** More than one `live operation CLASS: started` and `finished` message can be in flight on one host at the same time, each from its own case; simultaneous initial image operations all belong to `images`. The most recent line therefore does not show that only one operation is active. `live-run status` reports only the latest sanitized observation, never a list of what is active: when the observed case changes, the earlier operation is cleared and the operation is `unknown` until the new case supplies a known one, and an unknown or root context clears both. A completion marker can become the latest case but never inherits another case's operation. Operation timings stay individual inclusive durations, so the sum of the cases need not equal the suite's wall time.
- **What the evidence records.** `operations-<platform>.json` keeps the configured `case_overlap` and `build_jobs` as numbers, from its first write, beside the engine facts and the per-operation times; a value above the work that exists is capped when it runs and is not rewritten in the file. They are supplemental schema 1 facts. Older operation files without them stay readable and no setting is invented for them; a negative value in the file is refused. `live-run status` also shows the two numbers. Schema 2 is unchanged.
- **Resources.** A larger value runs more builds, sandboxes, and temporary output files at the same time, and two different things have to be checked for it. First, the engine needs CPU and memory for the work that runs together; check the engine's available CPU and memory against the concurrency you chose. This is a check of the engine's capacity, not part of the headroom bound, and no CPU or memory gate or figure is set here. Second, the headroom bound, which is a number of disk bytes, has to cover the disk that the work uses at the same time, including the temporary output logs that `--jobs` above `1` keeps in the host's temporary directory. Observations from serial runs alone do not establish either requirement for concurrent work. On Windows a value above `1` is not advised until the capacity decision of #181 permits it, and a failed capacity read stays unverified as before. No number is given here, and nothing here authorizes a run.
- **Limits.** Fake-process tests prove the scheduling, the join, the cap, the cleanup, and the records. No live run, native Windows run, measured speedup, or calibration backs it.

### Operation bounds and early stop

Every operation the suite runs ends at a bound set by its operation class, the same classes that [Operation timings](#operation-timings) records. The bound covers each CLI invocation, each direct Podman call, and each command the suite runs in a terminal, such as an agent session. The bounds replace the earlier limit of 60 minutes per CLI invocation and are all shorter than the suite's `-timeout` of 120 minutes.

| Class | Bound (seconds) | What the number rests on |
|---|---|---|
| `preflight` | 300 | Room beyond six 30-second machine reads. Not a measured maximum. |
| `inspection` | 120 | The two-minute budget that the harness already gave direct Podman reads. |
| `SSH server check` | 120 | The same two-minute read budget, set equal to the other read classes. |
| `Tool probe` | 120 | The same two-minute read budget. |
| `image archive` | 600 | The 10-minute budget that the save of an image archive already had, now a class of its own. Not a measured maximum. |
| `image build or sandbox start` | 1800 | Every `up`, whether it builds an image or only starts a sandbox. The first builds of the three toolchain images took about 750 to 870 seconds combined on Windows ([#173](https://github.com/grauzone-labs/sandboxed-agents/issues/173) evidence table, approximate, n = 3). The bound is a whole-stage proxy of about 2.1 times the upper figure, for one `up`. |
| `rebuild` | 2100 | The one no-cache `build` of all five images took 869 to 930 seconds on Windows (#173 evidence table, approximate, n = 3). The bound is about 2.3 times the upper figure. |
| `sandbox start or restart` | 600 | The longest Windows `lifecycle` case on record took 517.67 seconds as a whole (`90ad6ba`, n = 4; [#171 time analysis](validation/issue-171/feedback-time-analysis.md)). The bound is a whole-case proxy of about 1.16 times that, for one command: a narrow margin. |
| `smoke` | 300 | A Windows `shell` took 30 to 69 seconds, an approximate figure from the text of issue [#182](https://github.com/grauzone-labs/sandboxed-agents/issues/182), not a new measurement. The bound is about 4.3 times the upper figure. |
| `cleanup` | 180 | A Windows `remove` took 40 to 55 seconds, an approximate figure from the text of issue #182, not a new measurement. The bound is about 3.3 times the upper figure. |
| `Tool install` | 1200 | The longest Windows `t3code` case on record took 867.23 seconds as a whole (`90ad6ba`, n = 4; #171 time analysis). The bound is a whole-case proxy of about 1.38 times that, for one command: a narrow margin. |
| `Tool update` | 1200 | The same whole-case proxy as `Tool install`. |
| `update or rollback` | 1200 | No per-invocation measurement exists. For scale only, the longest Windows `update` case on record took 347.13 seconds as a whole (#171 time analysis); the bound is not derived from it. |
| `agent install` | 1200 | No per-invocation measurement exists. For scale only, the longest Windows `agent` case on record took 133.37 seconds as a whole (#171 time analysis); the bound is not derived from it. |
| `agent session` | 300 | No recorded maximum names this class. |
| `Tool delete` | 300 | No recorded maximum names this class. |

The harness has no default bound: an operation of a class that has none is a defect of the harness and stops the suite with an explicit failure, never with a silent default.

**These bounds are provisional.** The sources above are whole-case times of four Windows runs at earlier revisions (`90ad6ba`, `ae4526e`, `941caa6`, `0a6ac3f`, [#171 time analysis](validation/issue-171/feedback-time-analysis.md)) and approximate figures that the #173 and #182 texts reconstructed from polled log tails and transcripts, accurate to about 40 seconds. None is the largest time of one operation of the current suite, and the table says where a bound rests on a whole case instead of an operation: the margins it states are against those whole-case figures, and the two narrow ones (about 1.16 and 1.38 times) leave a healthy but slower run only about 16 % and 38 % of room beyond the longest case on record. A bound with no recorded time is a general ceiling, not a measurement. All of them await calibration, class by class, against the per-operation seconds that [`operations-<platform>.json`](#operation-timings) records from live runs; no such run has been used yet. Until then, read a bound as the point where a stalled operation stops holding the run, not as an expected duration, and make no claim from it that a healthy run is faster or that the bound is safe for a slower host.

The finer bounds that existed before stay as they are, and a bound of a class never lengthens one of them. The manager's `ssh-host-key` query keeps its 9 seconds plus one second to drain its output. The harness's own SSH key-exchange check from the host keeps its 15-second overall limit and 3-second attempts. The suite's terminal waits keep their own smaller limits. The production commands' own timeouts, deadlines, and messages are untouched: the bounds govern the harness only.

**The image archive.** The `images` case saves each base image as an archive to look for baked-in SSH host keys. That save is its own class, `image archive`, with the 600 seconds it already had, and a stall ends the owned local process tree and says that engine operations may still be running, as for any bound. The suite reads the archive's binary tar bytes through a pipe for its check and never logs them: the private log holds only the `live operation image archive: started` and `finished after DURATION` markers and the save's standard error, framed as `live output: ` records as under Streaming.

**When a bound is hit.** The suite ends only its owned host process tree, as for any cancellation (on Windows, a terminal command's process starts suspended inside a private job that ends with it, and closing the job ends its owned children; the test of that path compiles for Windows but has not run natively), and the failure message depends on the operation. A CLI invocation or a terminal command names the operation class, the time elapsed, and the bound, and asks that recorded objects be reconciled before cleanup. A direct Podman call and the image archive name the class and the elapsed time and say that engine operations may still be running; they name neither the bound nor the next step, so read that class's bound from the table above and, as for every bound, reconcile the recorded objects and the engine state before cleanup. The case fails and the parent test returns; the lines of the cases that finished, the failing schema 2 record, and `operations-<platform>.json` are written as for any failed case, and the suite's own cleanup of sandboxes, volumes, and group state runs. The bound shows in the operations file as a `fail` entry of its class.

**Streaming.** While an operation runs, the suite forwards its output to the private `go test -v` log as it arrives, so a stall shows as a last line with an age instead of silence. A CLI invocation and a direct Podman call stream through the same capture that keeps their combined output, and a terminal command streams the new part of its transcript each time the suite polls it. Each streamed record is `live output: ` followed by the fragment quoted as an ASCII Go string (`strconv.QuoteToASCII`), so a newline or control byte in the output appears as an escape inside the quotes and cannot start a record of its own. A line of more than 4096 raw bytes is split into several records, each holding at most 4096 raw bytes before quoting; the quoted record can be longer. A final line without a newline is flushed before the operation's finished marker. The output that the command returns to its caller is unchanged, byte for byte, in the order it was observed, and a successful command no longer logs the same output a second time. Around every operation inside a case the suite also logs two marker lines, `live operation CLASS: started` and `live operation CLASS: finished after DURATION`, where `CLASS` is one of the fixed classes. Every class uses this format, the image archive included. The markers hold no argument, name, or path, and no streamed output can produce one, because it always carries the `live output: ` prefix. The one stream that is not text is the image archive of the `images` case: its binary tar bytes are checked and discarded, not logged, and only its markers and standard error reach the log. The raw fragments stay in the private log on the host, and the sanitized files are unchanged.

**Stop after the first failed case.** Set `SANDBOXED_AGENTS_LIVE_STOP_AFTER_FAILURE=1` for a run that only a pass can serve. After a case fails, the suite starts no further case, and the schema 2 record keeps each case it did not reach as `not-run`. The failed case still fails the record, and the cleanup still runs. Any other value, including an unset variable, leaves the order and results of the cases as they are. The opt-in fabricates no coverage and no pass: a record from a stopped run cannot pass the stable release check, which requires a pass and completed image coverage ([Stable release check](releases.md#stable-release-check)). The preflight already ends the suite when it fails, with or without the opt-in.

### Retaining case durations

The schema 2 record holds results without timings. `go test -v` prints each case's duration and the suite's total, and that log is private: it holds group, sandbox, container, volume, and image names, paths, and program output. A run that should leave duration evidence keeps that log on the host where the suite ran, outside the record directory, and derives from it a sanitized file, `durations-linux.json` or `durations-windows-11.json`, written beside the schema 2 record. Only that file is shared with the run's evidence. Collecting it reads the record and never changes it, so the schema 2 record, the release gate, and the failing record that an interrupted run leaves are the same with or without it. The suite itself writes a second sanitized file beside them, `operations-linux.json` or `operations-windows-11.json`, which shows where each case spent its time; [Operation timings](#operation-timings) describes it. The release gate reads neither file, and neither can stand in for the schema 2 record.

`go test -v` prints a top-level case's completion line only once the parent test ends, so the log of a run that the parent did not outlive may hold none of its case durations, finished cases included. The file records what the log emitted and nothing more.

**Run the suite with its log kept.** The [host commands](#running-a-live-run-with-the-host-commands) run this same suite and collection from one repository-owned helper; the commands below are what they replace by hand. Both commands send the verbose output to a log file and keep the exit status of `go test`, the suite's verdict. Add `SANDBOXED_AGENTS_LIVE_IMAGES_TESTS=1` as above whenever the image coverage is selected, which a release run requires.

On Linux, in Bash:

```sh
PRIVATE="$HOME/live-suite-private"
mkdir -p "$PRIVATE" && chmod 700 "$PRIVATE"
RUN_ID="$(date -u +%Y%m%dT%H%M%SZ)"
SANDBOXED_AGENTS_LIVE_TESTS=1 \
SANDBOXED_AGENTS_LIVE_OVERLAP=1 \
SANDBOXED_AGENTS_LIVE_BUILD_JOBS=1 \
SANDBOXED_AGENTS_GROUP=live \
SANDBOXED_AGENTS_LIVE_RECORD_DIR="$HOME/live-suite-records/$RUN_ID" \
SANDBOXED_AGENTS_LIVE_IMAGES_TESTS=1 \
  go test ./cmd/sandboxed-agents -run '^TestLiveSuite$' -timeout 120m -v \
  > "$PRIVATE/live-suite-linux.log" 2>&1
status=$?
```

On Windows, in PowerShell:

```powershell
$private = Join-Path $env:USERPROFILE 'live-suite-private'
New-Item -ItemType Directory -Force $private | Out-Null
$env:SANDBOXED_AGENTS_LIVE_TESTS = '1'
$env:SANDBOXED_AGENTS_LIVE_OVERLAP = '1'
$env:SANDBOXED_AGENTS_LIVE_BUILD_JOBS = '1'
$env:SANDBOXED_AGENTS_GROUP = 'live'
$runId = (Get-Date).ToUniversalTime().ToString('yyyyMMddTHHmmssZ')
$env:SANDBOXED_AGENTS_LIVE_RECORD_DIR = Join-Path $env:USERPROFILE "live-suite-records\$runId"
$env:SANDBOXED_AGENTS_LIVE_IMAGES_TESTS = '1'
go test ./cmd/sandboxed-agents -run '^TestLiveSuite$' -timeout 120m -v 2>&1 |
  Out-File -Encoding utf8 -FilePath (Join-Path $private 'live-suite-windows-11.log')
$status = $LASTEXITCODE
```

Watch a run from a second session with `tail -f` on Linux or `Get-Content -Wait -Tail 20` on Windows; to run in the background, start the same command from a subshell or job and have it write `$status` to a file when `go test` returns. Three habits keep the exit status honest:

- **A pipeline reports its last command.** `go test ... | tee LOG` exits 0 when `tee` does, whether or not the suite passed or was interrupted. In Bash set `set -o pipefail` first and read `${PIPESTATUS[0]}`; in PowerShell read `$LASTEXITCODE`, which only a native program sets, never `$?`, which reports the pipeline.
- **Keep PowerShell's `$ErrorActionPreference` at `Continue`.** With `Stop`, Windows PowerShell 5.1 turns a line that `go` writes to standard error, such as `go: downloading`, into a terminating error that can end the run early.
- **Write the Windows log as UTF-8.** `>` and `Tee-Object` in Windows PowerShell 5.1 write UTF-16, which the collector reads as a log with no cases.

The suite's exit status is the result to report, and an interrupted or failed run is reported as it ended. Do not rerun or edit anything to make collection succeed.

**Observe the comparison context before the suite starts.** Save a snapshot of the engine's inventory (`podman images --all --no-trunc` with each image's labels and `podman ps --all`; on Windows with the `--connection` the preflight selects) and a note of what else runs on the host. Classify the three values from that snapshot alone, and for `base_image` only after the run, because the asset hash it needs appears first in the log's preflight `version` output; the engine after the run has been changed by the suite and shows nothing about its start. The collector takes three categorical values and nothing else about the host. `unknown` is the answer whenever a value cannot be shown from the snapshot, and an elapsed time never shows it.

| Value | Choices | Meaning |
|---|---|---|
| `host_load` | `idle`, `shared`, `unknown` | `idle` when the snapshot and the host show nothing else running, `shared` when something else was (containers of another group, another build or suite, the maintainer's own work), `unknown` when it was not looked at. |
| `base_image` | `present`, `absent`, `unknown` | `present` when the snapshot holds a base image (toolchain set `none`) whose `io.sandboxed-agents.asset-sha256` label equals the asset hash that the log's preflight `version` output prints, `absent` when a snapshot that was read completely holds none, `unknown` when there is no snapshot, it is incomplete, or the log does not print the hash. |
| `build_cache` | `warm`, `cold`, `unknown` | A proxy from the snapshot, not a cache reading: no query reports the build cache before a run, and a build's own `Using cache` lines are not read. `warm` when the snapshot holds images labelled `io.sandboxed-agents.image` that carry this run's asset hash, `cold` when a complete `--all` snapshot holds no image with that label and no untagged image, `unknown` otherwise, including images of other asset hashes only. `warm` does not mean the run hit the cache, and neither value predicts a faster or slower run. |

The revision, the platform (`linux` or `windows-11`), and the selected coverage (`image_part_selected`, `image_part_ran`, `image_coverage_complete`) are categorical facts too, and the collector copies them from the schema 2 record rather than taking them as input.

**Collect the durations** after the suite has returned, pass or fail, from the repository root of the checkout that ran it. The collector reads the log of one run on standard input and the run's own schema 2 record named by `-record`, takes the three context values from the flags (each defaults to `unknown`), and prints the path of the file it wrote. Feed it one run's log: a log with a case twice is refused, and an existing durations file is replaced.

On Linux:

```sh
: "${RUN_ID:?use the RUN_ID that the suite block of this same run set}" && go run ./internal/validationduration/cmd/retain-durations \
  -record "$HOME/live-suite-records/$RUN_ID/live-suite-linux.json" \
  -host-load idle -base-image absent -build-cache cold \
  < "$PRIVATE/live-suite-linux.log"
```

On Windows, where PowerShell has no `<` redirection and the log is piped in:

```powershell
if (-not $env:SANDBOXED_AGENTS_LIVE_RECORD_DIR) { throw 'use the session that ran the suite block of this same run' }
Get-Content -Encoding utf8 (Join-Path $private 'live-suite-windows-11.log') |
  go run ./internal/validationduration/cmd/retain-durations `
    -record (Join-Path $env:SANDBOXED_AGENTS_LIVE_RECORD_DIR 'live-suite-windows-11.json') `
    -host-load idle -base-image absent -build-cache cold
```

Use the values you observed in place of `idle`, `absent`, and `cold`. The collector exits non-zero with a one-line message when the record is not a schema 2 live-suite record, a context value is outside its choices, the log repeats a case, or a phase timestamp is malformed, unpaired, or reversed; it then writes no durations file and leaves an earlier one as it was, and the suite's status and record are unaffected. The optional `operations-<platform>.json` in the record directory is judged apart: when it is invalid, unreadable, partial, or names another revision or platform, the collector still writes the durations file and prints its path, leaves that operations file and the schema 2 record byte for byte as they were, does not update the operations file, and exits non-zero with `case durations retained; optional operation evidence: ` and the reason. Read that as retained case durations beside operation evidence that is unverified.

**Preparation and post-suite seconds** are optional. The suite cannot see the steps before it starts or after it returns, so the collector takes their boundaries: `-preparation-start` and `-preparation-end` for the host preparation, `-post-suite-start` and `-post-suite-end` for the steps after the suite, such as image cleanup (the collector call itself needs the end timestamp, so it falls outside the interval). Each value is an RFC3339 timestamp such as `2026-11-18T14:03:07Z` (`date -u +%Y-%m-%dT%H:%M:%SZ` on Linux, `(Get-Date).ToUniversalTime().ToString('o')` in PowerShell), taken by whoever runs the steps at the moment each boundary passes; the collector cannot check that they match what happened. Each phase takes both its timestamps or neither, and the end may not precede the start. The collector keeps only the difference, as `preparation_seconds` and `post_suite_seconds` in `durations-<platform>.json` and, when an operation file of the same revision and platform exists, in that file; it keeps no timestamp. A phase left out stays absent: not zero, and not a measured instant. Take the post-suite end after the last step you want counted, then collect once with every pair you have; a later collection replaces the durations file, so a call without a pair leaves that phase out of it.

On Linux:

```sh
: "${RUN_ID:?use the RUN_ID that the suite block of this same run set}" && go run ./internal/validationduration/cmd/retain-durations \
  -record "$HOME/live-suite-records/$RUN_ID/live-suite-linux.json" \
  -host-load idle -base-image absent -build-cache cold \
  -preparation-start "$PREP_START" -preparation-end "$PREP_END" \
  -post-suite-start "$POST_START" -post-suite-end "$POST_END" \
  < "$PRIVATE/live-suite-linux.log"
```

On Windows, add the same four flags to the `retain-durations` call above, each with the timestamp you captured.

**A file is current evidence only when this invocation's collector succeeded.** Success is exit status 0 and the printed path of the file just written; the one exception is the optional-evidence message above, where the path of the file just written is also printed and only the operations file is unverified. After any other failure, the earlier `durations-<platform>.json` that may sit in the record directory stays exactly as it was and belongs to an earlier run, even when it names the same revision. Do not read, quote, or report its numbers as this run's: the run's durations are `unverified` with the collector's own message, and the earlier file is left in place.

An interrupted run is collected from its failing record and the log as it ended. The file holds only the completion lines that log emitted. A signal or `-timeout` that ends the test binary before the parent returns yields a successful collection with an empty `cases` list: valid, no observations, not zero and not proof that no case finished. A case that fails while the suite still returns (status 1) leaves its parent's completion lines in the log, and they are collected.

**What the file holds.** The revision, the platform, the three coverage flags, the three context values, and for each top-level case of the suite that appears in the log its `name`, `result` (`pass`, `fail`, or `skip`), and `seconds`. It holds no group, sandbox, container, volume, or image names, no paths, no account details, and no program output: the collector keeps nothing from the log but the fixed case names and their numbers. Read three things from it correctly:

- **A case's `seconds` is inclusive.** It is the time `go test -v` printed for that case, with its nested subtests and its cleanup inside it. The collector discards nested lines; never add them, and never add cases to rebuild the suite's total, because the total also counts what runs outside the cases.
- **A missing case did not report.** A case the log has no completion line for, because the run was interrupted before the parent printed it or because the revision's suite lacks the case, is left out of the file; it is not zero, and the file does not show whether the case finished. A `skip` carries the time the log printed.
- **The coverage flags say what ran.** Compare only runs whose coverage matches; a run without the image coverage omits the case that dominates the total.

**Comparing runs.** Put the differences first: revision, coverage, and each context value, with `unknown` named as unknown, never treated as equal to a known value or to another `unknown`. Then compare the same case in two files of one platform, or in a Linux and a Windows file of a paired run, which has the same revision and the same coverage, so that the platform is the difference under study; the ratio of one case between them is a comparison only on those terms. Two comparable runs are a starting sample, not proof, and a duration shows how long a case took, not why; a cause or a speedup needs its own evidence.

#### Operation timings

Besides the schema 2 record, the live suite writes `operations-<platform>.json` to the record directory, with the platform spelled as in `durations-<platform>.json`. Where `durations-<platform>.json` says how long a case took, this file says which commands it spent that time in. It is supplemental evidence like the durations file: it holds no results of the checks, the schema 2 record stays the only evidence of what passed, and the file is not a replacement for it.

**What it holds.** The file has `schema_version` 1, `kind` `live-suite-operations`, the revision and the platform, and four kinds of numbers and labels:

- **`operations`** holds one entry for each CLI invocation the suite makes and each direct Podman call it makes, in the order they finished. An entry has the `case` it ran in, its `source` (`cli` or `podman`), its `operation` class, its `result`, and its `seconds`. A command run in a terminal, such as an agent session, is one entry, recorded when the command ends; a terminal closed before its command ended is recorded as `fail`.
- **`cases`** holds, for each case that ran, its `name`, `result` (`pass`, `fail`, or `skip`), `seconds`, and `unaccounted_seconds`.
- **`engine`** holds `cpus` and `memory_bytes`, the capacity numbers that the selected Podman engine reports. On Windows that is the engine of the connection the preflight selects, not the host that runs the CLI.
- **`preparation_seconds`** and **`post_suite_seconds`** appear only when the collector was given their timestamps ([above](#retaining-case-durations)).

**The vocabulary is fixed.** An entry holds no arguments, group, sandbox, container, volume, or image names, no paths, no timestamps, and no program output: the suite keeps only the labels below and the numbers, and the collector refuses a file with anything else. `case` is one of the suite's checks, `preflight`, `lifecycle`, `update`, `agent`, `t3code`, or `images`, or `suite` for a command that runs outside every case, such as the cleanup the suite registers for its whole run. `operation` is one of the classes the [live-validation skill](../.agents/skills/live-validation/SKILL.md) names for its status reports (image build or sandbox start, image archive, sandbox start or restart, SSH server check, smoke, update or rollback, agent install, agent session, rebuild, cleanup, Tool install, Tool update, Tool delete, Tool probe) or one of two more, `preflight` and `inspection`; no other label exists, and the collector rejects any other. The CLI subcommand picks the class: `up` is `image build or sandbox start`; `stop`, `start`, and `restart` are `sandbox start or restart`; `shell` is `smoke`; `update` is `update or rollback`; `build` is `rebuild`; `remove` is `cleanup`; `agents enable` is `agent install` and every other `agents` command is `agent session`; `tools enable`, `tools update`, and `tools delete` are `Tool install`, `Tool update`, and `Tool delete`, and every other `tools` command is `Tool probe`; `check` and `version`, each run with no further argument, are `preflight`, while `check NAME` and any other use of the two are `inspection`; anything else is `inspection`. The suite names the class of a direct Podman call where it makes the call, and never by comparing command text: the manager's `ssh-host-key` query and the check of PID 1 in a container are `SSH server check`, the `t3code` case's read-only service checks (HTTP response, listener, and process identity) are `Tool probe`, the save of an image archive is `image archive`, the reads that confirm a sandbox is gone and inventory the group at the end are `cleanup`, and every other direct Podman call is `inspection`. Not every Podman call in the `t3code` case is a `Tool probe`: the fixture steps that break and repair a Tool installation change it, so they are `Tool update`, never read-only probes, and so is a failed `tools update`.

**Read each number as follows.**

- **`seconds` of an operation is inclusive and is the whole process.** It runs from the start of the command until it ended and its output was drained, so a cancelled or timed-out command carries its elapsed time. `result` is the process outcome, `pass` for success and `fail` for any error, and not the check's verdict: a query that is expected to find nothing, or the rollback the `update` case forces, is a `fail` entry inside a passing case.
- **An `up` is one `image build or sandbox start` entry.** It covers any image build, the container start, and the wait for the sandbox, and the file neither splits them nor shows whether the image already existed. The suite makes no trusted observation of a build inside one invocation, and no program output decides the class, so the seconds are an inclusive duration of an ambiguous operation, never the measured time of an image build. A speedup of the build alone cannot be read from it.
- **`unaccounted_seconds` is the case's `seconds` minus the sum of the `seconds` of its operations.** The case's `seconds` includes the case's own cleanup and is measured by the suite, so it has finer precision than the two-decimal line of `go test -v`. What remains is time outside the measured commands: the case's assertions, SSH client checks on the host, waits, and the harness's own work. It is not a measured operation, and it is negative when operations overlap and add up to more than the case's wall time; the file keeps the negative number. The collector recomputes it from the entries each time it writes the file. The `suite` case has no entry in `cases`, and its commands have no remainder.
- **`engine.cpus` and `engine.memory_bytes` are numbers, not a statement of the host's configuration.** They come from one read-only `podman info` call of the preflight after the connection is selected, and the read is supplemental and best effort: when it fails, answers malformed data, or lacks a positive CPU count or memory, the file has no `engine`, the suite logs `engine capacity unavailable` privately, and neither the preflight check nor any other schema 2 verdict changes. A failed read still counts as an `inspection` entry. A run whose preflight stopped before that read also has no `engine`; an absent `engine` is unverified, never zero.
- **`preparation_seconds` and `post_suite_seconds` are what the collector was told.** The suite measures neither.

Do not add up the operations of every case to get a suite total, or add a remainder to anything: the total also counts what runs outside the cases, and operations can overlap. A time says how long a command took on that run, not why, so a cause or a speedup needs its own evidence, as for the case durations.

**Incomplete runs.** The suite writes the file empty (no operations, no cases, no `engine`) before any command runs, and replaces it once the suite's cleanups have finished, whether the suite passed or failed. A failing case keeps every operation that ran; a case that did not run, because the preflight failed or the image coverage was not selected, has no entry in `cases`, and an unselected `images` case is reported as `not-run` by the schema 2 record, not here. A signal or `-timeout` that ends the test binary before those cleanups leaves the empty file: valid, no observations, neither zero nor proof that no command ran, and a command that was still running then leaves no entry. The failing schema 2 record that such a run leaves is written first and is unchanged. An `operations-<platform>.json` is this run's evidence only when it names this run's revision and the suite started in this invocation; after a run that failed before the suite started, a file in the record directory belongs to an earlier run, and its numbers are not reported as this run's.

#### Engine failure snapshots

When an unexpected operation fails, the suite can record what the engine reports immediately afterward in `engine-failures-<platform>.json`, with the platform spelled as in `operations-<platform>.json`. Each run admits at most four snapshots, including snapshots whose queries fail. The file is supplemental evidence like the two files above: it never passes or fails a case, the schema 2 record and the release gate neither read it nor change with it, and it cannot stand in for the record.

**What it holds.** The file has `schema_version` 1, `kind` `live-engine-failures`, the revision and the platform, a list of `samples`, and the boolean `samples_truncated`. A sample holds fixed vocabulary only: the `case` (one of the suite's checks, or `suite`), the `source` (`cli` or `podman`), the `operation` class, a `result` of `fail`, the `engine_query` and `machine_query` status (`verified`, `unverified`, and for the machine also `not-applicable`), and a `machine_state` (`running`, `stopped`, `starting`, `stopping`, or `unknown`). Numbers appear only when read: `cpus`, `memory_bytes`, `free_memory_bytes`, `swap_bytes`, and `free_swap_bytes`. It holds no host, account, path, machine, or object name, no command output, and no error text.

`samples_truncated` starts as `false` and becomes `true` when an eligible unexpected failure cannot obtain one of the run's four snapshot slots. It marks omitted coverage; it does not count the omitted failures. A slot is reserved before the queries begin, so concurrent failures cannot exceed the cap, and a snapshot whose facts are unverified still uses a slot. An absent file, an empty list, or `samples_truncated: false` is not proof that no engine failure occurred, especially after an interruption or a failed write: a later failed write can leave a file that holds only the samples before it, so such a file is never proof that the run's coverage was complete.

**Whose file it is.** The file is this invocation's evidence only when it was written in a fresh, empty record directory that this invocation alone used (by hand, a new directory for every invocation as described under [Live image tests](#live-image-tests); the [host commands](#running-a-live-run-with-the-host-commands) use a new one for each run, and `prepare` refuses a directory that already holds a suite record), and the suite's log has no line `live engine failure evidence could not be initialized`. Its schema, revision, and platform fields do not show which invocation wrote it. The suite writes the empty file before its first command, but it can stop earlier (for example when it cannot resolve the commit or the record directory), and the first write can fail with only that log line; then a file that an earlier run left in a reused directory stays in place, also at the same revision. The suite writes no marker of a successful initialization, so a file in a reused directory, or from a run whose initialization failed or never ran, is unverified and is not reported as this run's.

**When it is written.** The suite writes the file empty before its first command and atomically replaces it when each admitted snapshot completes, before that failing invocation returns to its case and before the case's cleanup. The first failure that the cap suppresses also writes `samples_truncated: true`; later suppressed failures run no diagnostic query and write nothing more, so a run makes at most six writes of the file: the empty start, four completed samples, and that first suppression. A later interruption keeps the completed writes; an observation still in flight may be absent. The observation points include a failed CLI invocation, a failed command in a terminal, a failed direct Podman call (the SSH host-key query included), and a failed image archive. The normal negative probes, `container exists` and `volume exists` with exit status 1, are excluded. The suite also suppresses automatic sampling around the calls it expects to fail: the forced readiness rollback of the `update` and `t3code` cases and the deterministic staging failure of `tools update`. Each runs on a host copy without automatic sampling, while the invocation timings and the original assertions stay active. A result that satisfies its expected-failure assertion takes no snapshot. A wrong failure, such as an allocator error, or an unexpected success is observed through the original host before that same assertion fails the case and the cleanup runs, subject to the same four-snapshot cap. When the invocation succeeded unexpectedly, a fixed mismatch error triggers the observation only: the assertion still receives its original nil error and output, the case still fails, and the invocation's timing still records `pass`. The observation reports an assertion mismatch; it does not claim that the successful process returned an error. Other calls, the Tool break and repair steps included, keep their snapshots. A sample is not by itself a product defect or a failing case.

**How it reads the engine.** A snapshot runs read-only Podman queries only: `podman info` on the selected engine and, on Windows, `podman machine inspect` for the selected machine's own name, run locally without `--connection` as [ADR 0006](adr/0006-podman-target-and-execution-identity.md) requires. Until the Windows connection is selected, no query runs and the snapshot stays `unverified`: the suite never asks an ambient engine or another machine. No query starts or changes a machine, sandbox, image, host setting, or memory allocation. Each query has a three-second execution timeout plus a 250 ms drain, within its snapshot's shared eight-second context. Four admitted snapshots therefore allow at most eight diagnostic Podman invocations and 32 seconds of snapshot budgets in a run; snapshots can overlap, so 32 seconds is a fixed limit and not a measured or expected delay, and it is no measured improvement or calibration. Its output is kept private and capped at 64 KiB, and its error text is discarded. A query that fails, times out, or answers malformed or oversized data leaves its facts absent and its status `unverified`, never zero, and it neither replaces the original error nor causes another snapshot. The preflight's capacity read and the failure snapshots decode the same typed `podman info` answer: a `cpus` or `memory_bytes` of zero reads as absent, while a measured zero for free memory or swap stays zero; a partial answer keeps the facts it holds, and `engine_query` is `verified` only when all five facts were read. Every query counts as an `inspection` entry in `operations-<platform>.json`. A failed write of the file prints a fixed message and does not change the case's result.

**What it does not show.** A snapshot is taken after a failed invocation has returned, not continuously: it is no peak measurement, it cannot recover a peak or a kernel event that happened before, and it names no cause. Offline tests with fake processes cover the path, the privacy, the bounds, and the target selection; they do not reproduce a real out-of-memory failure, a `cannot allocate memory` error, or a WSL failure, and a Windows cross-compile does not show how the path behaves on Windows.

### Running a live run with the host commands

`./internal/liverun/cmd/live-run` is a repository-owned helper with the phases `prepare`, `run`, `status`, `collect`, and `cleanup`. It replaces the scripts a host agent would otherwise write anew for each run. Run it on the host, from the checkout under test, on Linux or Windows 11, after the decision to start a live run has been made: the [live-validation skill](../.agents/skills/live-validation/SKILL.md) gates that decision, and the helper does not. It fetches no source, pushes nothing, changes no host configuration, and starts or repairs no Podman machine, and a stopped machine is reported with a fixed kind and reason, the engine's own message staying in the private diagnostic. Only `cleanup` removes anything, and only the images that a private primary record proves this run built: its final images. Each phase prints one sanitized JSON result with no group, sandbox, container, volume, or image name and no path, except where `cleanup` lists immutable image IDs. Raw output, paths, and object identities stay in the private run directory.

**What is verified and what is pending.**

- **Offline.** Tests with fake `podman`, `go`, and filesystem answers cover every check below, the exclusion between runs, the detached run, the sanitized outputs, and the cleanup rules, and the helper and its tests compile for Windows.
- **Linux fixture.** A fixture kills the launching session and its process group with a hangup signal, and the suite still ends with its own exit status.
- **Not run.** Nothing here has run against real Podman output, and no timing is measured: the seconds that `prepare` and `run` report, which images the build proof covers, and the final inventory await authorized live runs on both hosts. Do not read a cross-compile as that proof, do not read a Linux fixture as proof for Windows, and do not read any of this as a speedup.

**Linux: survival of the SSH session is not shown.** The fixture shows that the supervisor, in a new session, outlives a hangup. It does not show that a run outlives a real SSH disconnect under systemd-logind. The systemd documentation says that when `KillUserProcesses=` applies to a user, "the scope unit corresponding to the session and all processes inside that scope will be terminated" at logout, and that `KillExcludeUsers=` can override it per user. Lingering (`loginctl enable-linger`) keeps the user's manager after logouts; the documentation does not say that it moves a process that was started inside an SSH session out of that session's scope, and this page does not claim it does. `prepare` does not read this policy. Read the host's logind policy without changing it, keep the SSH session connected until `status` shows `ended`, and report a run that the end of the session stopped as the status `137` with `interrupted`, never as a suite result. Changing the host's session or linger configuration is the user's decision, outside this invocation. Proving survival needs a real disconnect on that host under that policy.

**Windows: session detachment.**

- **Console and group.** `DETACHED_PROCESS` and `CREATE_NEW_PROCESS_GROUP` detach the supervisor from the launching console and process group, but not from a job object that the host's SSH session may own.
- **Job facts.** The helper therefore reads the launching process's immediate job, as sanitized facts `InJob`, `KillOnClose`, `Breakaway` and `SilentBreakaway`. `prepare` does so before any engine or build query and refuses a job that allows neither explicit nor silent breakaway with `check session-detachment failed`.
- **Launch.** `run` repeats the read and adds `CREATE_BREAKAWAY_FROM_JOB` only for a job with explicit breakaway (silent breakaway needs no flag); a denial when the process is created is the same named failure.
- **Supervisor.** The supervisor probes itself before any build and refuses to continue if it remains in any inherited job or if the query is unverified, because the immediate job does not reveal outer nested jobs and a breakaway can leave the process inside a restrictive outer one. `run` reports that failure by name.
- **No host change.** Nothing is elevated, scheduled, or changed in the host's job policy.
- **Fixture.** A fixture that closes real kill-on-close jobs around a suspended launcher (cases without a job, with explicit and silent breakaway, with a restrictive job, and with a restrictive outer job) compiles for Windows and is in the Windows pull-request test list. It has not run natively here, and it skips with a named prerequisite where the CI parent itself forbids isolation.
- **Not shown.** Neither the compile, the test list, nor a skip shows that a run survives the end of an SSH session. A native host disconnect, in Windows PowerShell 5.1 as a non-administrator, must prove that against the host's actual job policy; until it does, survival is pending.
- **Suite process.** The suite's own process runs in a job that ends with it; that path compiles for Windows and has not run there.

**Build the helper in the checkout.** Build it from the checkout at the target revision so that its code matches the suite it starts, and write the binary into the private directory, outside the checkout.

On Linux, in Bash:

```sh
PRIVATE="$HOME/live-suite-private"
mkdir -p "$PRIVATE" && chmod 700 "$PRIVATE"
go build -o "$PRIVATE/live-run" ./internal/liverun/cmd/live-run
```

On Windows, in PowerShell:

```powershell
$private = Join-Path $env:USERPROFILE 'live-suite-private'
New-Item -ItemType Directory -Force $private | Out-Null
go build -o (Join-Path $private 'live-run.exe') ./internal/liverun/cmd/live-run
```

**Use two fresh directories for every run.** The setup block below sets a new unique `RUN_ID` (`$runId` in PowerShell) once for the run, and the phase blocks use it in the directory names. The private run directory is new for each run and single use: `prepare` creates it with mode `0700`, it holds the suite log, the snapshots, and every diagnostic, and a run directory whose suite has started is refused. After any failed `run`, including a startup refusal, use a new `RUN_ID` and new private and record directories: a `run` that was attempted leaves `launching.json` in its run directory, and a retry there is refused with `run directory already launched`. Run `prepare` and `run` from the same shell, with the same `PATH` and Go environment (`GOTOOLCHAIN` included), because `run` repeats the Go and engine checks in the environment it starts from and refuses a toolchain that differs. The record directory is a second fresh directory that receives the schema 2 record and the sanitized files, and `prepare` refuses one that already holds `live-suite-linux.json` or `live-suite-windows-11.json`, so an earlier invocation's record cannot pass for this one. Neither directory may lie inside the other, and neither may lie inside the checkout, so that the run itself leaves no untracked file there. Keep the private directory out of anything that is shared.

**Prepare.** `prepare` takes the exact 40-character revision, the run directory, the record directory, and the peak record described below:

Run each phase from its own block, in order, in one shell session, and continue only after the check that follows the block. A native command that fails does not stop Windows PowerShell 5.1, and a pasted run of several blocks would not stop either, so a block is never pasted together with the next one. First set the variables once per run. Replace the placeholder revision with the real one; nothing else sets it, and `RUN_ID` and `$runId` must be new for every run.

On Linux, in Bash:

```sh
RUN_ID="$(date -u +%Y%m%dT%H%M%SZ)"
SHA="<the 40-character revision under test>"
```

On Windows, in PowerShell 5.1, with `$private` as defined above:

```powershell
$runId = (Get-Date).ToUniversalTime().ToString('yyyyMMddTHHmmssZ')
$sha = '<the 40-character revision under test>'
$helper = Join-Path $private 'live-run.exe'
$runDir = Join-Path $private "run-$runId"
$recordDir = Join-Path $env:USERPROFILE "live-suite-records\$runId"
```

Then run `prepare`. In Bash:

```sh
"$PRIVATE/live-run" prepare -revision "$SHA" \
  -run-dir "$PRIVATE/run-$RUN_ID" -record-dir "$HOME/live-suite-records/$RUN_ID" \
  -peak-record "$PRIVATE/peak-bound.json"
echo "prepare exit status: $?"
```

In PowerShell:

```powershell
& $helper prepare -revision $sha -run-dir $runDir -record-dir $recordDir -peak-record (Join-Path $private 'peak-bound.json')
"prepare exit status: $LASTEXITCODE"
```

**Gate after `prepare`.** Go on to `run` only when the printed exit status is `0` and `prepare` printed `{"Prepared":true}`. Any other result is a refusal: read its printed check name, and start no `run`.

`prepare` also takes `-overlap N` and `-build-jobs N`, positive integers that default to `1`; a value that is not positive is refused before any host or engine query. They are saved with the run, and `run` and its supervisor use the saved values and set `SANDBOXED_AGENTS_LIVE_OVERLAP` and `SANDBOXED_AGENTS_LIVE_BUILD_JOBS` themselves, so settings in your shell do not select the concurrency of a run. Saved settings of an older run that lack them read as `1`, and a negative saved value stops the run before any process starts. Add them only when you chose them: no larger value is calibrated, and neither changes the headroom, retention, target, or cleanup rules; check the engine's available CPU and memory against the concurrency you chose, and make the disk bound you supply cover the disk that work uses at the same time, as described under Overlap and parallel rebuild. Report the values you used; `status` shows them as `case_overlap` and `build_jobs`. The example omits `-host-load`, so the record says `unknown`, which is the default. Add `-host-load idle` or `-host-load shared` only when you looked at what else runs on the host; copying `idle` without that look would record a value that nothing shows.

`-checkout` defaults to the current directory, `-group` to `live`, and `-images` to `true`, which selects the image coverage that a release run requires; `-images=false` leaves it out. `-host-load` takes `idle`, `shared`, or `unknown` as [observed before the suite starts](#retaining-case-durations), and defaults to `unknown`; the helper infers no value from the host. `prepare` checks, in this order, and stops at the first check that fails, before any build or test starts:

- **`revision`** and **`checkout-clean`**: `HEAD` equals the revision, and `git status` with all untracked and all ignored entries shows nothing: a modified tracked file, an untracked file, and an ignored file each refuse the claimed revision, because each could change what the suite builds or runs. The helper deletes none of them, and `cleanup` repeats this check before it removes anything. After such a refusal never clean the user's checkout: make a new temporary checkout of the revision, build the helper there, and run every phase from it.
- **`go-pin`** and **`go-version`**: the effective `go env GOVERSION` equals the exact patch version that `go.mod` pins, so the asset hashes of two hosts can match.
- **`record-directory`**: the record directory is usable and holds no suite record.
- **`engine`**: one rootless engine answers. On Windows that is the default WSL2 machine, which must be running and rootless.
- **`engine-exclusive`**: no other live run owned by this helper holds the same engine. The lock is per engine, takes its files from one root in the host user's cache directory (`sandboxed-agents/live-run`) that no option selects, spans checkouts and controller groups there, and its files are never deleted. A run and its `cleanup` that find a different stored root refuse with `coordination root changed`. The coordination is cooperative: another OS user, or the same user with a different cache environment, is a separate coordination domain, and the lock is neither a security barrier against other processes of the same user nor a lock against writers that use the engine directly.
- **`filesystem`**, **`storage-measurement`**, and **`inventory`**: the free space of every measured filesystem, `podman system df`, and the whole-engine inventory of images and containers in one pass, with full image IDs. On Linux the filesystem is the one that holds Podman's storage. On Windows there are two: the host drive that holds the machine's virtual disk, found from the user's own WSL registry entry and read without administrator rights, and the machine's storage filesystem, read with a bounded `df` over the SSH endpoint that `podman machine inspect` reports, with a private `known_hosts` file.
- **`peak-bound`** and **`headroom`**: a reasoned peak record that admits a bound, as described below, exists and names this engine, and every measured filesystem has strictly more free bytes than that bound. A run without such a record proceeds only on the invocation-specific decision below.

A failure prints `check NAME failed; private diagnostic retained` and keeps the engine's own message in `diagnostic.txt` of the run directory. The `engine` check adds a fixed kind and a fixed reason, `check engine failed; KIND: REASON; private diagnostic retained`. The kind is `engine unreachable`, `machine stopped`, `malformed target`, `ambiguous target`, or `unsafe target`; the reason is one of a short list (connection refused, connection timed out, permission denied, machine stopped with a note to start it outside this command, engine address unavailable, or that the selected engine did not answer and its connection is to be checked), and an unrecognized message gets the last one. Paths, host names, and tokens of the raw message stay in the private diagnostic. A rejection by `peak-bound` or `headroom` also prints the free bytes of each filesystem role and the expected bytes, or that there is no reliable bound; the host is then `pending`, and the suite is not started. A failed `prepare` can be repeated with the same run directory until a `run` has been attempted in it; after any failed `run`, use new directories as described above. When the headroom was insufficient and you removed, in one pass, the images that an earlier run's record marks eligible, run `prepare` again after that removal: its fresh whole-engine inventory and free-space measurement replace the earlier ones as the before records, the bound is selected again for that state, and there is no second removal pass. The inventory, the `system df` text, and the free space are kept in `preparation.json` in the run directory, also after a headroom rejection, so a rejected preparation still leaves the before measurement.

**The peak record.** The headroom rule needs a bound on the peak additional space the run needs before any cleanup. The helper measures; it does not decide the bound.

- **What it samples.** During the suite the supervisor samples the free bytes of every measured filesystem about every ten seconds. It writes this run's facts to `peak.json` in the run directory and keeps the engine's per-filesystem high water in `KEY-observed-peak.json` in the coordination root, where `KEY` is the hexadecimal SHA-256 of the engine identity.
- **High water.** The observed file keeps the highest sampled growth of each filesystem across lower, incomplete, and killed runs, and each kept value carries the revision, interval, completeness, and context of the sample that produced it. A later lower or failed run cannot erase it; `peak.json` still holds only that run's facts.
- **What the facts are.** The revision, the engine, each filesystem's role and path, its free bytes at the start and at its lowest sample, the growth between them, the number of samples, the nominal interval, and the start and end times. A record whose sampling stopped early or failed is marked incomplete, and a sampling failure never changes the suite's exit.
- **Never a bound.** The helper always writes `PeakAdditionalBytes` as 0. A sampled lowest value is an observation, not an upper bound, because it misses space taken and freed between two samples, other writers on the same filesystem, and the variation of a later run. The helper never derives a bound from the net growth after cleanup, from a multiplier, or from a fixed figure, and it never writes or replaces the admitted bound `KEY-peak.json` or a file passed as `-peak-record`.

A bound exists only when a maintainer reasons one and writes it into a record of their own. `prepare` reads `-peak-record`, or `KEY-peak.json` in the coordination root when the option is absent, and admits the original format of three fields:

| Field | Condition |
|---|---|
| `EngineID` | The engine host name and storage root joined by a colon, as `podman info` reports them, equal to this engine's; `preparation.json` of a rejected run holds the value. |
| `PeakAdditionalBytes` | Greater than zero: the peak additional space the run needs before any cleanup, not the net growth after it. |
| `Basis` | Non-empty text that says how the number was reached. The bound applies only to a run whose cache state, selected toolchain sets, and dependency versions (a floating release such as `npm@latest` included) the basis covers, as the headroom rule requires. |

No other field is required, and no component model exists. A record may carry the sampled `Filesystems` facts of the helper's own record; they only constrain the bound: an entry whose sampled growth exceeds `PeakAdditionalBytes` makes the record inadmissible. Independently of the record's content, a bound is contradicted, and so unreliable, when the engine's latest observed record, for the same engine, filesystem name and path, shows a sampled growth above it; the record is kept and never rewritten. A missing, mismatched, inadmissible, or contradicted record refuses preparation with `peak-bound`.

The amount still has to be the maintainer's own numeric judgement under the skill's headroom rule: it has to cover the initial builds of the base image and every toolchain set, their simultaneous no-cache replacements and intermediates, and the writable layers of the running sandboxes, on both filesystems on Windows. The sampled facts do not establish it. No bound has been established for either host by this documentation.

A run may proceed without a bound only on a decision that the user has made for that invocation after the free space and the risk were stated: pass `-allow-unbounded` together with `-unbounded-decision` and the text of that decision. The decision can replace a missing, mismatched, inadmissible, or contradicted bound, and nothing else:

- **Still refused.** An existing bound or observed record that cannot be read; a filesystem with no free space; a bound for this engine that is at least as large as the free space; and a sampled growth, for the same filesystem on this engine, that is at least as large as the free space. These are known insufficiency or unreadable facts, and no decision waives them. `run` repeats the same refusal before it starts the suite.
- **Still checked.** An admitted bound still has to pass `headroom`, and free space that cannot be read is never accepted.
- **Never invented.** No bound, multiplier, or host figure replaces the missing one.

**Run.** `live-run run -run-dir RUN` starts the suite detached from the session that launched it and returns once the supervisor acknowledges the start, within five minutes, printing `{"started":true,"preparation_seconds":N}`. On Linux the supervisor is a new session; on Windows it is a process detached from the console and in a new process group, and it owns the suite in a job object that ends with it (see the pending note above for what that does and does not show about surviving an SSH session). The supervisor first probes its own session on Windows (see the pending note above) and then repeats every `prepare` check under the held engine lock, because time can pass between the two commands, and replaces `before.json` with that final snapshot, which later comparison and cleanup use. It then runs the suite as documented above, `go test ./cmd/sandboxed-agents -run '^TestLiveSuite$' -timeout 120m -v`, with the live opt-in, the group, the record directory, the image coverage as `prepare` selected it, and `SANDBOXED_AGENTS_LIVE_STOP_AFTER_FAILURE=1` ([early stop](#operation-bounds-and-early-stop)). Its output goes as UTF-8 bytes to `suite.log` in the run directory, the private log, so no shell redirection and no PowerShell text encoding is involved. When the suite returns, or when the supervisor is found dead, the run directory holds `exit.json` and `exit-status`, a plain integer: the exit status of `go test`, or 137 with `interrupted` for a supervisor that died. The suite's end time is taken when `go test` returns, before the supervisor finishes its sampling. The supervisor and `status` publish the exit evidence under one lock of the run directory: `status` that finds the supervisor gone first re-reads `exit.json`, keeps an exit that the supervisor published in the meantime, and repairs `exit-status` from it, and it writes 137 with `interrupted` only when no exit evidence exists. An `exit.json` that is malformed or incomplete is reported as unverified, never as a success. A failure to save progress, or to write the private log, never stops the suite's output: the suite's output keeps draining, progress is saved at most once a second unless the case or operation changed, and a lost write to the private log sets `log_verified` false in the exit evidence, after which `collect` and `cleanup` refuse that log. A second `run` is refused while a suite of this helper holds the engine, and accepted once that suite has ended, also after a killed one.

`preparation_seconds` runs from the start of the `prepare` invocation to the start of the suite. It therefore includes the time between the two commands and any repeated `prepare`, and it is the number the collector later receives. It is not a measurement of the checks alone.

Start the suite from its own block. In Bash:

```sh
"$PRIVATE/live-run" run -run-dir "$PRIVATE/run-$RUN_ID"
echo "run exit status: $?"
```

In PowerShell:

```powershell
& $helper run -run-dir $runDir
"run exit status: $LASTEXITCODE"
```

**Gate after `run`.** Go on to `status` only when the printed exit status is `0` and `run` printed `{"started":true,...}`. Any other result is a startup refusal or a failure: report the text that `run` printed, and after any failed `run` start over with a new `RUN_ID` and new directories as described above.

**Status.** `live-run status -run-dir RUN` reads the private files and prints one JSON object with these fields, and no raw log line:

| Field | Meaning |
|---|---|
| `case`, `operation` | The most recently observed suite case and operation class, each from a fixed list, or `unknown`; a change of case clears the earlier operation until the new case supplies one. |
| `case_overlap`, `build_jobs` | The saved numeric overlap and rebuild jobs of this run, `1` when the run saved none. |
| `last_output_age_seconds` | Seconds since the suite last wrote output, or `-1` before any output. |
| `ended`, `running` | Whether the suite has ended, and whether its supervisor or suite still lives. |
| `free_space` | Free bytes now, one entry per measured filesystem, named `engine` or `host`. |
| `exit_code`, `interrupted` | Present once the suite has ended. |
| `post_suite_total_seconds` | Present once the suite has ended and a `cleanup` completed its publication: the seconds from the suite's end to the end of that cleanup's publication, below. Absent otherwise; never zero. |
| `log_verified`, `peak_recorded`, `peak_bound_available` | Present once the suite has ended: whether the private log is complete, whether this run's sampled peak record was written, and whether a reasoned bound exists for this engine: the `-peak-record` file or `KEY-peak.json` is admissible and the latest observed record does not contradict it. The sampled record never makes the last one true; the two values refer to different files. |

The case and operation come from the marker lines and `-v` case lines that [the suite streams](#operation-bounds-and-early-stop), and the classes are the fixed vocabulary of the [operation timings](#operation-timings). A `live output: ` record is quoted program output and never classifies an operation, and a command line is recognized only when `sandboxed-agents` leads it after the log's own prefix. An `up` shows as `image build or sandbox start`: the helper does not guess from a raw line whether an image was built. Asset-hash lines in the output can supply a comparison-context hint, never ownership of an image. A silent log keeps its last observation while the age grows; a silence is not a pass, not a finished operation, and not the next one. The values describe the latest observation and do not say that other cases are idle. Before the suite has started, `running` is false even while the supervisor is still preparing, which can take more than five minutes on Windows. A `status` that shows `ended` with exit status 1, no case, operation `unknown`, and an output age of `-1` right after a failed `run` is the supervisor's startup refusal, not a suite result; the failure that `run` printed names it.

Poll from its own block, and run it again and again; it returns at once and does not wait. In Bash:

```sh
"$PRIVATE/live-run" status -run-dir "$PRIVATE/run-$RUN_ID"
```

In PowerShell:

```powershell
& $helper status -run-dir $runDir
```

**Gate before `collect`.** Repeat the block until its output shows `"ended":true`. One call that shows `"ended":false`, or one that you did not read, is not the end of the suite: run no `collect` and no `cleanup` while the suite is running.

**Collect.** After `status` shows `ended`, `live-run collect -run-dir RUN` runs the durations collector on `suite.log` with the record, and passes the preparation start and end from the run. It takes the three comparison values from the final `before.json` alone and the asset hash from the log's preflight output: `host_load` is the value given to `prepare`, and `base_image` and `build_cache` follow the [rules above](#retaining-case-durations), with `unknown` when the snapshot or the hash is missing. It refuses a private log that the run marked unverified. It prints `durations_file` (the file name only), `collected`, `optional_operation_evidence_unverified`, and the three values. Only exit status 0 with `collected` true and that file name confirm this invocation's collection. When `optional_operation_evidence_unverified` is true, the collector retained this run's case durations but could not update the optional `operations-<platform>.json`, which stays as it was: the durations file is current and the operations evidence is unverified, and both are reported as that. After any other failure the collector's message is in `diagnostic.txt`, the command exits non-zero, and an earlier `durations-<platform>.json` in the record directory is not this run's. `collect` takes no post-suite interval; the next phase does.

After `status` has shown `"ended":true`, collect from its own block. In Bash:

```sh
"$PRIVATE/live-run" collect -run-dir "$PRIVATE/run-$RUN_ID"
echo "collect exit status: $?"
```

In PowerShell:

```powershell
& $helper collect -run-dir $runDir
"collect exit status: $LASTEXITCODE"
```

**Gate after `collect`.** The collection is confirmed only by exit status `0` with `collected` true and the file name, as above; report anything else as unverified. A failed `collect` leaves the suite's result unchanged, and `cleanup` runs the final collection again.

**Cleanup.** `live-run cleanup -run-dir RUN` removes the images this invocation built and left unused, by the rules of the skill's [image cleanup](../.agents/skills/live-validation/SKILL.md#image-cleanup), which this command applies and does not change.

- **When and for which images.** Run it with the run directory prepared and run for this invocation, after the suite has ended, whether it passed, failed, or was interrupted. Repeat `-retain sha256:FULL_IMAGE_ID`, with a full 64-hexadecimal image ID, for each image the user asked to keep; a tag, a prefix, or any other argument is refused.
- **Before it inventories.** The command starts no suite and no machine, takes the engine lock, and refuses while an owned suite is still running. It checks the saved artifacts, the revision of the checkout, and that the engine is the one of the snapshot.
- **Before each removal.** It rechecks the engine, every container reference of the whole engine, and the checkout, with the same strict clean check as `prepare`, before each `podman rmi --no-prune ID`.
- **Never.** It never runs a prune, a reset, or a forced removal, and it removes no container, volume, group state, log, or checkout.
- **An earlier run.** To clean the images that an earlier run left, pass that run's own private directory; the command needs that run's checkout at its original revision and strictly clean, and ends with `cleanup: pending` otherwise.
- **A log that is not verified.** When `log_verified` is false the command stops with `cleanup: pending (private suite log unverified)`, writes no report and removes nothing. The images stay as left behind; report that line and remove none of them by hand.
- **After a pending stop.** A `cleanup` that stops as pending before its report leaves no sanitized before or after measurement: `prepare` printed only that it succeeded and kept the before measurement in the private `preparation.json`, and `status` shows only current free space. Report the missing side as unverified.
- **Leftovers.** The report counts only the containers left on the engine. The product's read-only `list`, run with `SANDBOXED_AGENTS_GROUP` set to the run's group, shows leftover sandboxes of that group, and a sandbox of which only volumes remain shows as `volumes only`. The group's host state has no supported read, so what no such command shows is reported as unverified.

Clean up from its own block, only after `status` has shown `"ended":true`; add one `-retain sha256:FULL_IMAGE_ID` for each image the user asked to keep. In Bash:

```sh
"$PRIVATE/live-run" cleanup -run-dir "$PRIVATE/run-$RUN_ID"
echo "cleanup exit status: $?"
```

In PowerShell:

```powershell
& $helper cleanup -run-dir $runDir
"cleanup exit status: $LASTEXITCODE"
```

**Gate after `cleanup`.** Exit status `0` means `complete`. A non-zero status is `partial`, `unverified`, or `pending`: report the printed line and the sanitized report as they are, and remove no image by hand.

**Build proof.** An image is proven only by a private primary record, never by what the suite prints.

- **Channel.** The supervisor creates the private `build-evidence` directory in the run directory, with a manifest that binds the revision, the engine, and an unpredictable invocation identity, and hands its location to the suite in the environment variable `SANDBOXED_AGENTS_LIVE_BUILD_EVIDENCE_DIR`. It is an internal channel, not a command option of the product, and it is never copied into a workspace or sandbox.
- **Final images.** When that variable is set, the executable's own build adds `--iidfile` to its `podman build`, and then, still under the image-tag lock, reads the engine identity and the built tag's ID and asset-hash label with read-only queries. Only when the ID from the file, the ID from the tag, the asset hash, and the engine of the manifest all agree does it publish one event under a random file name, so concurrent builds never overwrite each other.
- **Ordinary builds.** Without the variable a build runs with exactly its former flags, cache, and queries, and a failure of any evidence step changes neither the build nor the suite's result.
- **Reading.** Events are read as strict typed JSON of at most 64 KiB each, at most 16 MiB and 10,000 files together. A file that is malformed, foreign to the invocation, or incomplete is not admitted and is reported as an unverified item, while the other valid events are still read; an invalid manifest admits nothing.

Output proves nothing. A `STEP` line, a result line, a `COMMIT` line, an operation marker, or a whole forged build window in the suite's output, whether it comes from the suite's own commands, a smoke check, an agent, or a Tool, is data and grants no ownership; `cleanup` no longer parses it for proof. An image that carries a tag outside the project's canonical tags, or a mix of such tags and project tags, is skipped as foreign. Each recorded ID is resolved against the full inventory of the engine, matches exactly one image, and is confirmed by `podman image inspect`, in batches of at most 64 IDs with a per-ID query only for a batch that failed or came back incomplete; a failed query leaves the image unverified, never absent. The full ID, step, and source stay in the private `build-record.json`.

**Intermediate images are not removed.** Only an image with its own independent primary record is eligible: a full ID from the builder-controlled `--iidfile`, inspected for the matching full ID and the expected asset under the image-tag lock after the selected engine was confirmed. A tagged image without its own primary result stays unverified. An intermediate or ancestor image has no such record, so `cleanup` retains it as skipped or unverified with its byte cost, and does not claim complete cleanup or coverage of the step that produced each image.

**Optional layer telemetry is audit only.** When the executable supports it, the opted-in build also publishes a typed attempt event and adds `--layer-label io.sandboxed-agents.live-build-invocation=INVOCATION:ATTEMPT`, where `INVOCATION` is the manifest's random identity and `ATTEMPT` a separate random 256-bit identity of that build attempt; the event keeps its publication and end times. The label correlates an image with an attempt for the audit and grants no ownership, neither alone nor with the attempt's time window: an explicit `FROM` can inherit a label into another workflow's image, so a label cannot tell this run's intermediate from someone else's. Primary full IDs are recorded once, and a layer candidate is never attributed to every tag. No parent chain, creation time, hash, difference of inventories, or output stands in for a primary record.

**Support gate.** The label is requested only when the client and the server (for a remote target, the server side must be known) are known releases of at least 4.7 and a bounded `podman build --help` shows the `--layer-label` option. An older, unknown, or malformed version, a missing option, or a failed probe leaves the build with its `--iidfile` evidence only. This gate decides whether optional telemetry is requested; it is not a new minimum Podman version and changes no product floor. A build that has run is never run again because of what it printed, and a genuine build error stays an error.

**When the producer fails.** A build that fails removes its own pending marker and returns its own error unchanged. A pending, image-ID, or event file that a killed or interrupted producer left behind, and a malformed or foreign event, are reported as unverified items; they do not stop the audit. `cleanup` still removes the images that independent, valid evidence proves, reports every other image as skipped with its size, takes the after measurement, and runs the final collection, with the report `unverified`. Contradictory asset identities in the events admit no ownership at all, but the audit is still published. No unknown image becomes eligible by any of this.

What the channel covers is not shown. Safe removal of intermediates would need a per-step, immutable commit-result record from the builder, separate from `RUN` output and distinguishing a newly committed image from a reused one; none exists. Whether the installed Podman and its build engine apply `--layer-label`, how many IDs stay unknown, their total size, the real `--iidfile` output, and the final inventories are all pending authorized live runs on both hosts. The report must show the skipped sizes, and no complete provenance is claimed.

Each attempt writes a new `cleanup-RANDOM` directory in the run directory, mode `0700`, with the queries, inventories, build record, removal plan, final inventory, and raw measurements, all private. The command prints the sanitized report and writes the same report to `cleanup-linux.json` or `cleanup-windows-11.json` in the record directory.

- **What the report holds.** The suite's exit code and whether it was interrupted; the removed, skipped, and unverified images, with immutable IDs, canonical project tags, byte sizes (with `size_verified`), and a fixed reason or query category; the before and after free space by filesystem role and a four-row allowlist of `podman system df`; the number of containers left on the engine; `post_suite_seconds`; and `collected` with `durations_file`.
- **`complete`.** Every query and the reconciliation succeeded and every image of the run is accounted for.
- **`partial`.** An image remains only because no primary record proves it. The command exits non-zero with `cleanup: partial (unproven images retained; see sanitized cleanup report)`, the report lists each retained image with its size, and a successful final collection still counts. An intermediate image has no primary record, so if the run's builds left such images, `partial` with a non-zero exit is the expected outcome of the run, not an exception. Report the exact count and total bytes of the retained images from the report. This invocation has no supported way to remove them without primary evidence: they persist across runs, they reduce the free space that the next run starts with, and the before measurement of the next run counts them. A `partial` cleanup is therefore not a complete image cleanup, and it does not complete the acceptance of the cleanup task; if the builds left no such image, nothing is retained for this reason.
- **`unverified`.** Incomplete, malformed, or failed producer evidence, or an inconsistent asset identity, gives this status while the independently proven images are still removed; so does any other query or reconciliation that did not succeed. The command exits non-zero and the report says what is unverified, independent of the suite's result. A `collected` true with `optional_operation_evidence_unverified` shows as a fixed unverified item, `case durations retained; optional operation evidence unverified`, and never certifies an earlier file.
- **`pending`.** An engine that does not answer ends the command with `cleanup: pending (engine unreachable: REASON; private diagnostic retained)` and no removal, where `REASON` is one of the fixed reasons of the `engine` check. A target that is stopped, ambiguous, unsafe, or malformed is reported by its own kind instead of as unreachability, and other saved evidence that is missing, incomplete, or changed ends it with a `cleanup: pending (...)` that names the cause.
- **Limits.** Private JSON files are capped at 16 MiB, the sanitized report at 4 MiB, and `suite.log` at 64 MiB; a plan that does not fit is refused before any removal.

`cleanup` also runs the final durations collection, with both the preparation interval and the post-suite interval, and replaces the file that `collect` wrote.

- **`post_suite_seconds`.** The interval starts at the `EndedAt` time in `exit.json`, so it includes an earlier `collect`, and it ends once the engine is reconciled, the after measurements are taken, and the preliminary cleanup report is saved. It ends before the final collector call and the publication of the report, which cannot count their own duration, so it is a lower bound on the time after the suite and never a total that includes everything.
- **Confirmation.** Only a true `collected` and the named durations file confirm this invocation's final collection; a failed final collection is reported as unverified and never confirms an earlier file. When `cleanup` stops before that collection, for example because the engine is unreachable, the file of the earlier successful `collect` has no post-suite interval and is the only collection of the run.

**Post-suite total.** A separate number gives a total, described here.

- **What is kept.** A cleanup that completes keeps a private `cleanup-completion.json` in the run directory, and a `completion.json` in its own attempt directory. They record the suite's end, the completion time, the revision, the attempt, a fixed description of the endpoint, and the seconds between the two.
- **The boundary.** The endpoint is taken after the final collector call has returned, after the sanitized report file and the output of the command have been written, and after the return path was torn down. It excludes the writes of its own timing files and the last overhead of the process's return.
- **Reading it.** `status` prints the number as `post_suite_total_seconds` only when the record matches this run's revision and the authoritative suite end, so call `status` after `cleanup`. It is the last successfully retained completed interval: an early refusal, a failed publication, or a lost timing write creates no new total and never changes the verdict, and an earlier total is not upgraded after a later attempt failed.
- **Unchanged.** The report value `post_suite_seconds` stays the lower bound, and the schema 2 record and the report layouts are unchanged.

**Comparing the total with the earlier baselines.** The total is not like for like with the historical post-suite figures of the story (Linux 62 to 315 seconds, 1,190 once; Windows 270 to 494).

- **Scope of removal.** The historical figures come from a manual removal of 62 images on Linux and 68 on Windows. This command removes only the images that a primary record proves, which can be fewer, so it can do less removal work; report the number and total bytes of the images it retained beside the total.
- **Steps left out.** The total does not include the host agent's report step, which the historical figures did.
- **A killed run.** When the supervisor was found dead, `EndedAt` is the time at which `status` detected it, not the time of the kill, so the total of such a run starts late.
- **Pending.** No authorized run has produced a total yet; a comparison needs runs on both hosts.

Cleanup has been exercised against fake engines only. The seconds it reports are to be compared with the post-suite seconds of earlier runs only after authorized runs on both hosts, and none of this documents a faster run.
