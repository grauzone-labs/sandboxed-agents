# sandboxed-agents

`sandboxed-agents` runs coding agents in rootless Podman containers, one sandbox per workspace, on Linux and Windows 11. An agent's mistake, such as deleting the wrong directory, installing a bad package, or reading credentials it should not have, stays inside one sandbox: its workspace, its home data, and the credentials stored there.

It is one static executable, and the host needs only Podman and OpenSSH; [Host prerequisites](docs/host-prerequisites.md) states the Podman versions and the parts of a rootless Podman setup that the preflight checks. From here you can install it, create your first sandbox, and find the page for every command and topic.

## What you get

- One sandbox per workspace, created with `up`, with a workspace volume of its own or exactly one host directory bound at `/workspace`.
- The agents `copilot`, `claude`, `codex`, and `opencode`, installed into a sandbox on request, with login, one-shot runs, and persistent sessions you can detach from.
- Optional toolchains in the sandbox image: `dotnet`, `playwright`, `azure`, and `native`.
- Integrations for Git identity and credentials, GitHub, Azure, and Azure DevOps.
- SSH access for editors such as VS Code Remote SSH, as an opt-in setup.
- `update` to move a sandbox to a newer image with rollback, and `update --all` for every sandbox.
- The Tool T3 Code, installed into a sandbox on request, so that you can follow and steer its agents from the T3 mobile app, linked through your T3 account or by a QR code over Tailscale ([Tools](docs/tools.md)).

## Requirements

- Linux amd64 with rootless Podman 4.4.0 or newer, or Windows 11 x64 with Podman 4.7.0 or newer and a running rootless WSL2 machine.
- OpenSSH.

The executable never installs or configures these and never runs with `sudo`. [Host prerequisites](docs/host-prerequisites.md) gives the reason behind each floor.

## Get started

Follow a quick guide from installation to a signed-in agent session:

- [Linux quick guide](docs/quick-guide-linux.md)
- [Windows quick guide](docs/quick-guide-windows.md)

Install methods: the downloaded binary (both quick guides show download, checksum comparison, attestation verification, and placing the binary on `PATH`), the [npm package](docs/npm-package.md), and on Windows the [NuGet package](docs/nuget-package.md). [Releases](docs/releases.md) lists the release files, how to verify one, which releases exist, and what installing never touches.

## Documentation

| Page | Content |
|---|---|
| [Command reference](docs/command-reference.md) | Every command and option, the exit status rule, the order of checks, owner conflicts |
| [Host prerequisites](docs/host-prerequisites.md) | Podman version floors, their reasons, what the preflight checks |
| [Images](docs/images.md) | Base image, toolchains, builds, updates, rollback |
| [SSH access](docs/ssh.md) | Opt-in SSH setup, VS Code Remote SSH, fingerprints |
| [Agents](docs/agents.md) | Enable, pin, update, log in, run, and sessions |
| [Integrations](docs/integrations.md) | Git, GitHub, Azure, and Azure DevOps |
| [Tools](docs/tools.md) | T3 Code: enable, update, the service, T3 Connect and QR pairing, the network paths, delete |
| [Releases](docs/releases.md) | Release files, attestations, previews, gates, install methods |
| [npm package](docs/npm-package.md) and [NuGet package](docs/nuget-package.md) | Installing from a package |
| [Manual checklist](docs/manual-checklist.md) | Checks by hand before a stable release |
| [T3 Code validation](docs/t3code-validation.md) | For maintainers: the recorded live runs of the T3 Code Tool on both hosts |
| [Security policy](SECURITY.md) | Threat model, default Podman options, limits |
| [Maintainer notes](docs/maintainers.md) | For maintainers: how the version floors were confirmed, live runs and source checks on record, key rotation, release builds and CI |
| [Glossary](CONTEXT.md) and [decisions](docs/adr/0004-command-first-cli-grammar.md) | Terms and architecture decisions (ADR 0001 to 0010) |

## Limitations

[SECURITY.md](SECURITY.md) states the threat model and every limit. In short:

- **Not a defense against hostile code.** A sandbox shares a kernel with the host (on Windows, with the WSL2 machine). Use a dedicated virtual machine for hostile code.
- **Outbound networking is open.** An agent can send what it can read to any server it can reach.
- **Agents in one sandbox share everything:** the user, the home data, the credentials, and the workspace. Use separate sandboxes to separate them.
- **Only SSH is published**, on host loopback. No port is forwarded. A Tool's service, such as T3 Code's, listens on the sandbox's loopback. Once a Tool's tailnet node is signed in, peers that your tailnet's policy allows can reach the sandbox's `sshd` and that service through the node ([Tools](docs/tools.md#what-a-signed-in-tailnet-node-reaches), [command reference](docs/command-reference.md#the-tailnet-node)).
- **No command removes old images.** Remove unused ones yourself, for example with `podman image prune` ([images](docs/images.md#no-image-removal)).
- **Owner conflicts are reported and not repaired.** You resolve them with Podman ([command reference](docs/command-reference.md#ownership-and-names)).
- **Linux amd64 and Windows 11 x64 only.** ARM64, a self-update command, and install methods for agents other than npm are not part of the first version.
- **Windows workspaces** must lie under the Podman machine's default automount root ([host prerequisites](docs/host-prerequisites.md#windows-11-x64)).

## Security

See [SECURITY.md](SECURITY.md) to report a vulnerability and for the threat model.
