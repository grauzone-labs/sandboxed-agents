# Host prerequisites

This document states the Podman versions that the sandboxed-agents executable needs on the host. For each floor, it gives the reason and how the floor was confirmed.

The versions were confirmed from versioned upstream Podman documentation and source code, not on a live host. Only the live suite checks live behavior. Nothing here shows that a given Podman installation behaves as its documentation says.

The behavior described here follows Epic #1, its Features, and the ADRs. The executable does not implement all of it yet.

## Supported minimum versions

| Platform | Minimum | Also required |
|---|---|---|
| Linux amd64 | Rootless Podman 4.4.0 | A usable pasta program on the engine side |
| Windows 11 x64 | Podman client 4.7.0 and Podman machine 4.7.0 | A running, rootless WSL2 machine, and a usable pasta program on the engine side |

The preflight is planned to check the host prerequisites automatically before `build`, `up`, and `update`. The `check` command is planned to run it on its own too.

## Linux amd64

The executable needs rootless Podman 4.4.0 or newer.

The reason is the network option `--network=pasta:--no-map-gw`. It uses pasta networking, which Podman 4.4.0 introduced. The v4.4.0 release notes list pasta as new. The v4.4.0 network documentation describes `pasta[:OPTIONS,…]`, rootless operation, and options passed through to pasta. The v4.4.0 engine source adds the supplied options and defaults to `--no-map-gw`.

Every other Linux interface that the executable uses is available at 4.4.0 or below. These are the user namespace option `keep-id` with `uid` and `gid` (4.3.0), `--user=0:0`, `--security-opt=no-new-privileges`, the resource limits, labeled named volumes and workspace binds, loopback port publishing, and the `--version` and info fields. No other interface sets a higher floor.

## Windows 11 x64

The supported floor is Podman client 4.7.0 or newer and Podman machine 4.7.0 or newer, with a running rootless WSL2 machine. Epic #1 sets this floor as the supported minimum. The confirmation below shows that 4.7.0 is the lowest version that supports the confirmed interfaces the executable uses.

The following Podman interfaces were confirmed for Windows from versioned sources:

- The WSL machine provider that Podman manages is available from 4.0.0.
- The machine list and inspect fields that the executable reads are available from 4.7.0. These are the default machine, the WSL provider, whether the machine is rootful, and its state.
- The global `--connection` option is available with a Podman-managed WSL machine from 4.0.0.
- The container options that the executable sends through the remote client are available from 4.0.0 with a Podman-managed WSL machine: user, security options, volumes, labels, port publishing, and resource limits. Resource limits also depend on the machine's cgroup controllers being delegated. The `keep-id` option with `uid` and `gid` is available from 4.3.0, and `pasta` networking from 4.4.0.
- The guarantee that `--connection` takes precedence over `CONTAINER_HOST` is available from 4.8.0. [ADR 0006](adr/0006-podman-target-and-execution-identity.md) has the executable remove `CONTAINER_HOST`, `CONTAINER_CONNECTION`, and `CONTAINER_SSHKEY` from the environment of its Podman calls, and cites the 5.0.0 precedence rules as a second guard. The 4.8.0 version would be needed only if the executable stopped removing those variables.

[ADR 0006](adr/0006-podman-target-and-execution-identity.md) also rejects a machine name that starts with `-` or contains a line break or a NUL character. That is a design rule, not a Podman version requirement.

While those variables are removed, the 4.8.0 guarantee does not raise the floor above 4.7.0.

## Open points

These are known limits of what this page confirms.

- **Automount root query.** The Windows preflight reads the automount root of the machine. Its exact query is not confirmed, because the command is not implemented yet. Only a transport for such a query is confirmed: `podman machine ssh` runs a command in the machine from 4.0.0. The automount setting itself does not depend on the Podman version.
- **Exact introduction versions on Linux.** For some options, availability below 4.4.0 is confirmed, but the release that introduced them is not. These are `--user=0:0`, `--security-opt=no-new-privileges`, and the resource limits `--memory`, `--cpus`, `--pids-limit`, and `--shm-size`. The first release that offers the label readback that the executable needs is also not established. Since all of them are available below 4.4.0, they do not raise the Linux floor.
- **Info query.** The info fields for rootless mode and cgroup controllers are confirmed from 3.2.0. The exact command and the rule the preflight applies to them are not confirmed. Podman's info output has no field that shows whether cgroup controllers are delegated to the user, and the Podman version alone does not show it either.
- **Engine version query.** `podman --version` reports the version of the client only. The call that reads the engine version is not confirmed.
- **Remote Podman on Linux.** On Linux the executable calls the local Podman without a connection. A remote setting there, such as `CONTAINER_HOST`, remains a documented limit of the check ([ADR 0006](adr/0006-podman-target-and-execution-identity.md)).

## How this was confirmed

The sources are the upstream Podman repository (containers/podman) at the tags named below, and the Podman documentation site. Each is cited with its version.

### Linux amd64

- Network option `--network=pasta`: network documentation, engine source (`libpod/networking_pasta_linux.go`), and release notes, all at v4.4.0.
- User namespace `keep-id` with `uid` and `gid`: userns documentation and `pkg/namespaces/namespaces.go` at v4.3.0. The v4.2.1 parser recognizes only plain `keep-id`.
- `--user` for `run` and `exec`: run and exec manuals at v1.0.0, and the exec manual at v4.0.0.
- `--security-opt=no-new-privileges`: `cmd/podman/create.go` at v0.12.1, and the `security-opt` documentation at v4.4.0 and v5.0.0.
- Resource limits: run manual at v1.0.0, release notes and `pkg/spec/spec.go` at v1.5.0 (rootless limits with cgroup v2), and the run manual at v5.0.0.
- Labeled named volumes, workspace binds, and label readback: volume-create manual, `libpod/runtime_ctr.go`, and inspect sources at v0.12.1, and the inspect sources at v4.4.0.
- Loopback port publishing: release notes and `libpod/networking_linux.go` at v1.1.0, and `libpod/networking_pasta_linux.go` at v4.4.0.
- Info fields: `libpod/define/info.go` at v3.2.0, v4.4.0, and v5.0.0. The v3.1.2 schema lacks the cgroup controllers.
- Cgroup controller discovery: the vendored `containers/common` cgroups source at v4.4.0.

### Windows 11 x64

- WSL machine provider: v4.0.0 release notes.
- Machine list and inspect fields: `cmd/podman/machine/list.go` and `pkg/machine/wsl/machine.go` at v4.7.0, `cmd/podman/machine/inspect.go` at v5.0.0. The v4.6.2 inspect schema lacks the rootful field.
- Global `--connection`: v2.0.5 release notes, and `cmd/podman/root.go` at v4.0.0 and v5.0.0.
- Container options through the remote client: the run and exec manuals at v4.0.0 (`--volume` covers named volumes, directory binds, and remote-server paths), the Linux `keep-id` sources above, the Linux pasta sources at v4.4.0 above, and `cmd/podman/common/netflags.go` and `pkg/domain/infra/tunnel/containers.go` at v5.0.0 for pasta through the remote client.
- Connection precedence: v4.8.0 release notes (the fix for `CONTAINER_HOST` overriding `--connection`), and `cmd/podman/root.go` at v5.0.0.
- Machine command execution: `pkg/machine/wsl/machine.go` at v4.0.0 and the `podman machine ssh` documentation at v5.0.0.
