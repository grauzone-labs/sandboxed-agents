---
status: accepted
---

# The workspace is the only host bind

A sandbox has three named volumes: workspace, home, and SSH server state. Only the workspace may be replaced by a host directory, and by exactly one. Home and SSH server state are always named volumes. Host credentials, SSH-agent sockets, container-engine sockets, and display sockets are never mounted. A workspace bind is rejected when it contains, or lies inside, a protected host path, after symlinks, junctions, and other aliases are resolved.

This rule is the security statement the product makes: an agent can reach the files in its workspace and nothing else on the host. One rule with one exception is easy to explain and to test.

## Considered options

- **Additional configurable binds or shared volumes**, such as a package cache or several project directories. Rejected because every extra bind needs the same guards, and a shared cache connects sandboxes that are meant to be separate.
- **No host bind at all**. This is the safest option and would remove the WSL path translation on Windows, but it rules out editing an existing project directory from the host.

## Consequences

- Requests for extra mounts are refused by design. Files reach a sandbox through the workspace, Git, or SSH.
- Windows needs path translation between Windows paths and WSL mount paths, and custom WSL automount roots are refused.
- Installing the executable inside a directory that is later bound as a workspace makes that bind fail.
