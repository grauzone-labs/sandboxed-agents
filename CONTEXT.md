# Sandboxed agents

`sandboxed-agents` runs coding agents in rootless Podman containers so that an agent's mistake stays inside one sandbox: its workspace, home data, and the credentials stored there. This glossary defines the language used to describe the product.

## Language

### Product

**Sandbox**:
One isolated rootless container with its own workspace, home data, SSH server state, agent selection, and toolchain set. It is the unit users create, update, stop, and remove.
_Avoid_: Container (as a product term), environment, instance

**Workspace**:
The project files available to agents in a sandbox, mounted at `/workspace`. It is a sandbox-owned volume by default; the user may instead bind exactly one host directory.
_Avoid_: Project directory, mount

**Agent**:
A coding CLI from the agent catalog that is installed into a sandbox's home data and enabled there. Agents in one sandbox share its user, files, and credentials.
_Avoid_: Assistant, bot, model

**Agent session**:
A persistent terminal session running one agent in a sandbox, which the user can detach from and reattach to. It ends when its agent exits, so a running agent session always means a running agent.
_Avoid_: Agent run (a run is a one-shot command that ends with its terminal)

**Agent catalog**:
The agent data embedded in the executable: per agent its install method, command, login workflows, and an optional status probe that reports whether the agent is signed in. During development it may hold entries not delivered yet; those count as unknown, and only delivered entries can be enabled.
_Avoid_: Agent registry, agent list

**Agent selection**:
The set of agents enabled in a sandbox, each with its pin if it has one. It is stored in the sandbox's home data, so it and its pins survive `stop` and `start`; host state keeps no copy.
_Avoid_: Enabled agents list, agent config

**Pin**:
The exact version of an agent's package that the user requested with `agents enable --version`, recorded with the agent in the agent selection. `agents update` reinstalls that version until `agents update --unpin` or `agents disable` removes the pin or a new `--version` replaces it.
_Avoid_: Version lock, version constraint

**Manager**:
The trusted Go program in a sandbox's image that handles the executable's administrative requests and agent work. It handles administrative requests as container root and changes to the user `agent` before it reads home data or does work for an agent.
_Avoid_: Daemon, agent (for the manager itself)

**Manager lock**:
The one lock with which a sandbox's manager serializes installation changes (enabling, updating, or disabling an agent) and session changes (starting or ending an agent session). Attaching to a running agent session does not take it.
_Avoid_: Install lock, session lock

**Integration**:
A connection between a sandbox and an external account or service, such as Git, GitHub, Azure, or Azure DevOps. An integration offers login and config workflows and has no installable program of its own.
_Avoid_: Tool, built-in workflow target, plugin

**Workflow**:
A named login or config operation that an agent or integration exposes. A login workflow authenticates or links an account; a config workflow changes a setting that is not authentication.

**Toolchain**:
An optional set of SDKs or system packages built into a sandbox's image, such as .NET, Playwright with browsers, Azure CLI, or native build tools. Each sandbox's toolchain set is chosen when it is created; `update NAME --with SET` replaces it with another set and keeps the sandbox's volumes.
_Avoid_: Tool, image option, capability

**Tool**:
Reserved for installable programs and managed services that run beside agents. The first version ships none.

**Image**:
The container filesystem a sandbox is created from: the base contents every sandbox needs plus the sandbox's toolchains. Sandboxes with the same toolchain set share one image.

**Base image**:
The image with the base contents and no toolchains, produced by `build` and selected by `--with none`. Toolchain images are layered on it, so toolchain sets share its layers.
_Avoid_: Default image, root image

**Asset hash**:
The SHA-256 hash of sorted paths and normalized bytes in the embedded image build context.
_Avoid_: Asset identity, context identity, context hash

### Ownership

**Controller group**:
The namespace an installed executable manages. The default group is `default`; the environment variable `SANDBOXED_AGENTS_GROUP` selects another. It does not depend on where the executable is installed. Two controller groups can each hold a sandbox of the same name.
_Avoid_: Controller, owner path, installation

**Owner**:
The controller group recorded on a sandbox and its volumes. The executable changes only objects whose owner is its own controller group. An object under one of its sandbox names with a missing or different owner is an owner conflict: the executable reports it and leaves the repair to the user.

**Backup container**:
The previous container of a sandbox, kept under its own name while `update` replaces it and removed once the new container is ready. It is not a sandbox and never gets a row of its own in `list`, which shows its sandbox as "update interrupted".

### Host boundaries

**Host state**:
Mutable data the executable keeps in the operating system's state directory, in one `group-GROUP` directory per controller group. It holds the managed SSH keys and configuration of SSH setups and an empty lifecycle lock for each sandbox name a lifecycle command has run for. It records nothing about an update.

**Lifecycle lock**:
The host-side lock with which the executable's lifecycle commands (`up`, `start`, `stop`, `restart`, `remove`, `update`) serialize on one sandbox: a second command refuses at once instead of waiting. That refusal is step 3 of the ten-step order of checks, after the preflight and before an unknown sandbox name. It is an empty file in host state, so it coordinates only commands that use the same host state root, and it does not hold back writers that use Podman directly. It is distinct from the manager lock inside the sandbox.
_Avoid_: Sandbox lock, update lock

**Protected host paths**:
Host locations that must never be exposed through a workspace bind: the executable, its package launchers, the default per-user Windows executable install directory `%LOCALAPPDATA%\Programs\sandboxed-agents`, host state, temporary build inputs, and the user's SSH directory. Symlinks, junctions, and other aliases do not make a protected path bindable.

**SSH setup**:
The opt-in host configuration that lets editors and desktop UIs reach a sandbox over SSH, using a key dedicated to that sandbox and a pinned host key. Without it, the executable leaves host SSH files untouched.

**Preflight**:
The read-only check of host prerequisites that runs before a sandbox or image is built. It reports each missing prerequisite. The executable only reads the host and never changes it: every program it runs is a query. On Linux these are `podman --version`; `podman info`, for rootless mode and the cgroup version; reads of the cgroup controller list, for the delegated CPU, memory, and process controllers; lookups on `PATH` of `newuidmap`, `newgidmap`, `pasta`, `ssh`, and `ssh-keygen`; and reads of the subordinate UID and GID ranges. On Windows, read-only queries of the Podman client and the Podman machine and read-only commands inside a running machine. None of them installs, configures, starts, or repairs anything. The one permitted exception is Podman's own initialization during the informational `podman --version` and `podman info` queries, which may create or adjust Podman's per-user configuration and runtime directories.

### Distribution

**NuGet command package**:
The versioned NuGet archive, `SandboxedAgents.VERSION.nupkg`, that distributes the Windows executable, its checksum manifest, and PowerShell 7 install and remove scripts with their shared helper. It is not a project library: nothing references it as a dependency.
_Avoid_: NuGet library, NuGet dependency
