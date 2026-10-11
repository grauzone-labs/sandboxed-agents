# Host prerequisites

This document states the Podman versions that the sandboxed-agents executable needs on the host. For each floor, it gives the reason and how the floor was confirmed.

The versions were confirmed from versioned upstream Podman documentation and source code. They were not confirmed on a live host. Only the live suite checks live behavior. Nothing here shows that a given Podman installation behaves as its documentation says.

## Supported minimum versions

| Platform | Minimum | Also required |
|---|---|---|
| Linux amd64 | Rootless Podman 4.4.0 | None beyond Podman itself |
| Windows 11 x64 | Podman client 5.0.0 and Podman machine 5.0.0 | A running, rootless WSL2 machine |

The preflight checks the host prerequisites before `build`, `up`, and `update`. The `check` command runs it too.

## Linux amd64

The executable needs rootless Podman 4.4.0 or newer.

The reason is the network option `--network=pasta:--no-map-gw`. It uses pasta networking, which Podman 4.4.0 introduced. The v4.4.0 release notes list pasta as new. The v4.4.0 network documentation describes `pasta[:OPTIONS,…]`, rootless operation, and options passed through to pasta. The v4.4.0 engine source adds the supplied options and defaults to `--no-map-gw`.

Every other Linux interface that the executable uses is available at 4.4.0 or below, according to the research. These are the user namespace option `keep-id` with `uid` and `gid` (4.3.0), `--user=0:0`, `--security-opt=no-new-privileges`, the resource limits, labeled named volumes and workspace binds, loopback port publishing, and the `--version` and info fields. No other interface sets a higher floor.

A usable `pasta` executable remains a prerequisite on the engine side.

## Windows 11 x64

The supported floor is Podman client 5.0.0 or newer and Podman machine 5.0.0 or newer, with a running rootless WSL2 machine. The project sets this floor.

The research confirmed the following about the Podman interfaces that the executable uses on Windows:

- The WSL machine provider that Podman manages is available from 4.0.0.
- The machine list and inspect fields that the executable reads are available from 4.7.0. These are the default machine, the WSL provider, whether the machine is rootful, and its state.
- The global `--connection` option is available with a Podman-managed WSL machine from 4.0.0.
- The guarantee that `--connection` takes precedence over `CONTAINER_HOST` is available from 4.8.0. The executable does not rely on it. On Windows it removes `CONTAINER_HOST`, `CONTAINER_CONNECTION`, and `CONTAINER_SSHKEY` from the environment of its Podman calls, and it names one machine with `--connection` in every call that looks at or changes a sandbox or an image. [ADR 0006](adr/0006-podman-target-and-execution-identity.md) records this.

5.0.0 is the version that the analysis in ADR 0006 was made against. That analysis read `root.go` at v5.0.0.

None of the confirmed interfaces first requires 5.0.0. The research found no requirement for a higher version, and it did not establish 5.0.0 as the lowest upstream version that works. This page does not adopt a lower floor than 5.0.0. Whether to lower it is an open decision.

## Open points

These are known limits of what this page confirms.

- **Automount root query.** The Windows preflight reads the automount root of the machine. The research did not confirm this query, because its exact command is not yet implemented. The research confirmed only a transport for such a query: `podman machine ssh` runs a command in the machine from 4.0.0. The automount setting itself does not depend on the Podman version.
- **Exact introduction versions on Linux.** For some options, the research confirmed that they are available below 4.4.0 but did not find the release that introduced them. These are `--user=0:0`, `--security-opt=no-new-privileges`, and the resource limits `--memory`, `--cpus`, `--pids-limit`, and `--shm-size`. The first release that offers the label readback that the executable needs is also not established. Since all of them are available below 4.4.0, they do not raise the Linux floor.
- **Info query.** The research confirmed the info fields for rootless mode and cgroup controllers, which are available from 3.2.0. It did not confirm the exact command and acceptance rule of the executable's check. Podman's info output has no field that shows whether cgroup controllers are delegated to the user. The Podman version alone does not show it either.
- **Client and engine versions.** `podman --version` reports the version of the client. A query of the engine version is a separate call.
- **Live behavior.** None of the above shows how a real host behaves. The live suite is the only place that checks it.

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
- Connection precedence: v4.8.0 release notes (the fix for `CONTAINER_HOST` overriding `--connection`), and `cmd/podman/root.go` at v5.0.0.
- Pasta through the remote client: `cmd/podman/common/netflags.go` and `pkg/domain/infra/tunnel/containers.go` at v5.0.0.
- Machine command execution: `pkg/machine/wsl/machine.go` at v4.0.0 and the `podman machine ssh` documentation at v5.0.0.
