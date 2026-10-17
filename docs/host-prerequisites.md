# Host prerequisites

Here you find the Podman versions your host needs for `sandboxed-agents`, the reason for each floor, and what the preflight checks, so you can prepare a host before you install. How each floor was confirmed is recorded in the [maintainer notes](maintainers.md#how-the-version-floors-were-confirmed).

The executable never installs or configures host prerequisites and never runs with `sudo`. Beside Podman, the host needs OpenSSH: `ssh` and `ssh-keygen` on `PATH`.

## Supported minimum versions

| Platform | Minimum | Also required |
|---|---|---|
| Linux amd64 | Rootless Podman 4.4.0 | A usable `pasta` program on the engine side |
| Windows 11 x64 | Podman client 4.7.0 and Podman machine 4.7.0 | A running, rootless WSL2 machine, and a usable `pasta` program on the engine side |

The preflight checks these before `build`, `up`, and `update`, and `check` runs it on its own and reports each missing prerequisite ([command reference](command-reference.md#general-commands)).

## Linux amd64

The executable needs rootless Podman 4.4.0 or newer because every sandbox is created with `--network=pasta:--no-map-gw`. Podman 4.4.0 introduced pasta networking and the options passed through to pasta.

Every other Linux interface the executable uses is available at 4.4.0 or below: the user namespace option `keep-id` with `uid` and `gid` (4.3.0), `--user=0:0`, `--security-opt=no-new-privileges`, the resource limits, labeled named volumes and workspace binds, loopback port publishing, and the `--version` and info fields. None of them sets a higher floor.

The preflight runs `podman --version` and `podman info --format json`, requires `host.cgroupVersion` to be `v2` and `host.security.rootless` to be true, reads the cgroup controller list for the delegated `cpu`, `memory`, and `pids` controllers, looks up `newuidmap`, `newgidmap`, `pasta`, `ssh`, and `ssh-keygen` on `PATH`, and reads the subordinate UID and GID ranges. It calls the local Podman without a connection; a remote Podman setting such as `CONTAINER_HOST` is a documented limit of the check ([ADR 0006](adr/0006-podman-target-and-execution-identity.md)).

## Windows 11 x64

The executable needs Podman client 4.7.0 or newer and a Podman machine running 4.7.0 or newer, rootless, on WSL2. Podman 4.7.0 is the first version whose machine list and inspect output carry the fields the executable reads: the default machine, the WSL provider, whether the machine is rootful, and its state. The Podman-managed WSL machine, the global `--connection` option, and the container options sent through the remote client are available from 4.0.0; `keep-id` with `uid` and `gid` from 4.3.0; and pasta networking from 4.4.0.

Each command works on one Podman machine, the default one or the only one, and names it with `--connection` on every call that looks at or changes a sandbox or an image; no command starts a machine ([ADR 0006](adr/0006-podman-target-and-execution-identity.md)). The executable removes `CONTAINER_HOST`, `CONTAINER_CONNECTION`, and `CONTAINER_SSHKEY` from the environment of its Podman calls, so Podman's later guarantee that `--connection` takes precedence over `CONTAINER_HOST` (4.8.0) does not raise the floor. A machine name that starts with `-` or contains a line break or a NUL character is rejected by design.

The preflight reads the engine version with `podman version --format json` through the selected machine connection and requires its `Server.Version` to meet the machine floor, because `podman --version` reports the client only. It reads the delegated cgroup controllers inside the machine with `podman machine ssh NAME id -u` and `cat` on the user's `cgroup.controllers` and `cgroup.subtree_control` files, and reports a controller as delegated only when both list it. It also reads the machine's automount root: `podman machine ssh NAME test -e /etc/wsl.conf`, then `cat /etc/wsl.conf` when the file exists. A silent exit status 1 from the existence query means the default root `/mnt/`. An existing file without an `[automount] root` setting means the default root too. Other query failures and unreadable existing files fail the preflight. The preflight accepts and records a custom root. Directly after it, at step 2 of the [order of checks](command-reference.md#order-of-checks), `up NAME WORKSPACE` refuses every workspace bind while the machine's automount root is not the default (`/mnt/` or `/mnt`), naming the root and the machine, before the lifecycle lock and before any call that builds or creates. Host directories reach the machine only under the automount root, so a workspace must lie under the default one.

Resource limits on Windows depend on the machine's cgroup controllers being delegated; a machine whose user session lacks them fails the preflight, which reports the missing delegation and repairs nothing.
