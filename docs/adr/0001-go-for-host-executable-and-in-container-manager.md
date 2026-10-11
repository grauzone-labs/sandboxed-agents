---
status: accepted
---

# Go for the host executable and the in-container manager

The host side is one static Go executable that embeds the image build context, so a host needs only Podman and OpenSSH. The in-container manager, which installs agents, runs workflows, and coordinates agent sessions, is a second command in the same Go module. It is compiled for Linux, and each host executable embeds the manager binary that matches its container architecture.

## Considered options

- **Node.js manager without a build step.** Node is in the image anyway because the agents are npm packages, and a script manager is architecture independent. Rejected in favor of one language for the whole project, with catalog types shared between host and manager.
- **No manager, host drives everything through `podman exec`**. Rejected because locks, session coordination, and install state would move to the host, and every operation would become many Podman calls, which is slow through a WSL2 machine.
- **Script launchers on the host** (Bash and PowerShell). Rejected because two launchers diverge over time and require runtimes on the host.

## Consequences

- The release build compiles the manager for Linux before it compiles the host executable, and the Windows executable carries a Linux binary.
- The agent and integration catalogs are data read by both sides through the same Go types.
- Supporting a new container architecture means building and embedding another manager binary.
