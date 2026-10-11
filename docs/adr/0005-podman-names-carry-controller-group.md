---
status: accepted
---

# Podman names carry a product prefix and the controller group

The sandbox `NAME` in controller group `GROUP` is the Podman container `sandboxed-agents.GROUP.NAME`, and its volumes carry the same prefix. While `update` replaces a container, the old one is kept as the backup container `sandboxed-agents-backup.GROUP.NAME`. Controller group names match `^[a-z0-9][a-z0-9-]*$`, so they contain no dot and the Podman name splits unambiguously into prefix, group, and sandbox name. The sandbox name is also recorded in a label, and the owner label stays the authoritative check: a matching name alone authorizes nothing.

Two requirements led here. `update` needs a backup name that cannot collide with a sandbox, while no sandbox name may be reserved (ADR-0004). And a controller group is meant to be a namespace: the live suite and a second installation must be able to use a sandbox name that already exists in another group on the same host.

## Considered options

- **Sandbox name as the container name, with a reserved backup suffix** such as `NAME.backup`. Rejected because it reserves sandbox names, which ADR-0004 rules out.
- **A random backup name, with the mapping kept in host state.** Rejected because recovering an interrupted update would depend on a file outside Podman that can be missing or stale after the interruption.
- **Marking the backup with a label.** Not possible: Podman can rename an existing container but cannot change its labels.
- **Controller group only in the owner label.** The names would be shorter, but sandbox names would be unique across all groups on a host, and `up` in one group could fail because of a sandbox in another.

## Consequences

- `podman ps` shows the long names. Users address a sandbox by its sandbox name; the executable derives the Podman name.
- `list` and `update --all` tell a backup container from a sandbox by its name prefix and never treat it as a sandbox.
- An invalid controller group name fails before Podman is called.
- The pattern admits names that Windows reserves for devices, such as `con` and `nul`, so a group name alone is not a safe directory name. In host state the directory of a group is therefore named `group-GROUP`, for example `group-con`, on every platform: the prefix is part of the same path component, so no component ever equals a reserved device name. The Podman names already carry the `sandboxed-agents` prefix.
- Loopback ports belong to the host and not to a controller group, so port allocation reads the ports recorded on the containers of every group.
- Images are the exception: their names contain no controller group, and every group with the same executable and toolchain set shares one image. An image holds no user data, and sharing saves build time and disk space. A `build` in one group therefore marks the sandboxes of other groups as outdated.
- The SSH host entry of a sandbox lives in the user's single SSH configuration and so needs a name that is unique across groups: the sandbox name in the `default` group, `NAME.GROUP` in any other. Because sandbox names may contain dots, installing an SSH setup refuses when the entry name already resolves to a configured host. Each group keeps its own managed configuration in host state and its own `Include` line, so no group writes another group's files.
