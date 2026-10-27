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
The trusted Go program in a sandbox's image that handles the executable's administrative requests, agent work, and Tool work. It handles administrative requests as container root and changes to the user `agent` before it reads home data or does work for an agent or a Tool.
_Avoid_: Daemon, agent (for the manager itself)

**Manager lock**:
The one lock with which a sandbox's manager serializes installation changes (enabling, updating, or disabling an agent; enabling, updating, or deleting a Tool), session changes (starting or ending an agent session), and Tool service, tailnet node, and T3 Connect changes (starting or stopping a service or node, `tools tailscale … login` and `logout`, `link`, `unlink`, `logout`). Attaching to a running agent session, `tools status`, `tools pair`, and `tools connect … login` do not take it; the login holds the Tool's connect lock instead (ADR-0009, ADR-0010).
_Avoid_: Install lock, session lock

**Integration**:
A connection between a sandbox and an external account or service, such as Git, GitHub, Azure, or Azure DevOps. An integration offers login and config workflows and has no installable program of its own.
_Avoid_: Tool, built-in workflow target, plugin

**SSH integration**:
The `ssh` integration for configuring outbound SSH access from a sandbox to a remote machine using a dedicated key and an alias in the `agent` user's home data.
_Avoid_: SSH setup (the host-to-sandbox connection)

**Workflow**:
A named login or config operation that an agent or integration exposes. A login workflow authenticates or links an account; a config workflow changes a setting that is not authentication.

**Toolchain**:
An optional set of SDKs or system packages built into a sandbox's image, such as .NET, Playwright with browsers, Azure CLI, or native build tools. Each sandbox's toolchain set is chosen when it is created; `update NAME --with SET` replaces it with another set and keeps the sandbox's volumes.
_Avoid_: Tool, image option, capability

**Tool**:
An installable program or managed service from the Tool catalog that runs beside agents in a sandbox, installed into the home data after the sandbox exists and run as `agent`. T3 Code, tool name `t3code`, is the first Tool, and `tools delete` removes a Tool completely (ADR-0009).
_Avoid_: Plugin, extension, add-on

**Tool catalog**:
The tool data embedded in the executable: per Tool its npm package, command, service and its loopback origin, connect actions, and the pairing and device-session commands. During development it may hold entries not delivered yet; those count as unknown, and only delivered entries can be enabled.
_Avoid_: Tool registry, tool list

**Tool directory**:
The one directory in the sandbox's home data that holds everything a Tool installs and stores, `~/.local/share/sandboxed-agents/tools/TOOL`: the Tool selection, the npm prefixes, the Tool's own home (`T3CODE_HOME` for T3 Code), the service state and logs, and the tailnet node's state under `tailscale/`. `tools delete` removes it entirely after relocating the user project files found inside; host state keeps nothing about a Tool.
_Avoid_: Tool home (T3 Code's own home is one subdirectory of it), install prefix

**Tool selection**:
The record of an enabled Tool in its Tool directory: its channel or Tool pin and the installed version. It survives `stop`, `start`, `restart`, and `update`; host state keeps no copy.
_Avoid_: Tool config, enabled tools list

**Channel**:
The release line a Tool is installed from: `latest`, upstream's stable line, or `nightly`; the upstream `preview` line is no channel. A channel selection moves to the newest release of its line on `tools update` and never to another line.
_Avoid_: Train, release track, dist-tag (the npm mechanism behind a channel)

**Tool pin**:
The exact version of a Tool requested with `--version` on `tools enable` or `tools update`, recorded in the Tool selection and kept until another `--version` or a `--channel` replaces it. There is no unpin option: `tools update --channel` returns to a channel.
_Avoid_: Version lock, pinned channel

**T3 Code service**:
The T3 Code server, `t3 serve`, kept running in the background on the sandbox's loopback by its service supervisor while T3 Code is enabled. It runs as `agent` and is `running`, `stopped`, or `failed`; a crashed service never counts as running.
_Avoid_: Daemon, systemd service, T3 server

**Service supervisor**:
The detached manager process, one per supervised process of a Tool (its service and its tailnet node), that starts the process as `agent`, restarts it after a crash within a bound, stops it on request, and records its state and logs in the Tool directory. `up`, `start`, `restart`, and `update` start it again after the sandbox is ready (ADR-0009).
_Avoid_: Init system, daemon manager, watchdog

**T3 Connect**:
T3 Code's account and managed-connection service, driven with `tools connect NAME t3code login|link|unlink|logout`, whose login is a T3 account login with a device code, and whose connections then run over an outbound relay. It is not an agent provider login: provider sign-in stays `agents login NAME AGENT`, and T3 Connect authenticates no provider.
_Avoid_: Provider login, cloud login, remote access (ambiguous with QR pairing)

**T3 Connect link**:
Authorizing a sandbox's T3 Code service as an environment of the T3 Connect account with `tools connect NAME t3code link`, after which the phone, signed in to the same account, picks the environment without any QR code. `unlink` keeps the login, `logout` clears it.
_Avoid_: Pairing, binding, registering

**QR pairing**:
A one-time pairing of a phone directly with the sandbox's T3 Code service over the tailnet origin, started with `tools pair NAME t3code`: a terminal QR code, a copyable URL, and an expiry, after which the command exits without learning whether the code was scanned. It needs a signed-in tailnet node and is separate from the T3 Connect account login (ADR-0010).
_Avoid_: Linking, connecting, device login

**Tailnet node**:
The T3 Code Tool's own Tailscale device: a `tailscaled` in userspace networking mode, run as `agent` by a service supervisor with its state in the Tool directory, that exists from `tools tailscale NAME t3code login` until `logout` or `tools delete`. It needs no TUN device, no capability, and no host Tailscale, and the base image carries its binaries (ADR-0010).
_Avoid_: Tailscale daemon, host Tailscale, VPN

**Tailnet origin**:
The HTTPS address of a sandbox's T3 Code service in the user's tailnet, `https://<MagicDNS name of the tailnet node>/`, which Tailscale Serve maps to the service's loopback port and which the QR code carries. Each sandbox has its own origin; a phone in the same tailnet reaches it, the public internet does not.
_Avoid_: Public URL, Funnel URL, pairing URL (the pairing URL is the origin plus the one-time token)

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
Mutable data the executable keeps in the operating system's state directory, in one `group-GROUP` directory per controller group. It holds the managed SSH keys and configuration of SSH setups and an empty lifecycle lock for each sandbox name a lifecycle or SSH setup mutation has run for. It records nothing about an update.

**Lifecycle lock**:
The host-side lock with which the executable's lifecycle commands (`up`, `start`, `stop`, `restart`, `remove`, `update`) and SSH setup mutations (`ssh-config --install`, `ssh-config --remove`) serialize on one sandbox: a second command refuses at once instead of waiting. That refusal is step 3 of the ten-step order of checks, after the preflight or Podman target selection and before an unknown sandbox name. It is an empty file in host state, so it coordinates only commands that use the same host state root, and it does not hold back writers that use Podman directly. Previewing with `ssh-config NAME` does not take a lock or create host state. The lifecycle lock is distinct from the manager lock inside the sandbox.
_Avoid_: Sandbox lock, update lock

**Protected host paths**:
Host locations that must never be exposed through a workspace bind: the executable, its package launchers, the default per-user Windows executable install directory `%LOCALAPPDATA%\Programs\sandboxed-agents`, host state, temporary build inputs, and the user's SSH directory. Symlinks, junctions, and other aliases do not make a protected path bindable.

**SSH setup**:
The opt-in host configuration that lets editors and desktop UIs reach a sandbox over SSH, using a key dedicated to that sandbox and a pinned host key. Without it, the executable leaves host SSH files untouched.

**Preflight**:
The read-only check of host prerequisites that runs before a sandbox or image is built. It reports each missing prerequisite. The executable only reads the host and never changes it: every program it runs is a query. On Linux these are `podman --version`; `podman info`, for rootless mode and the cgroup version; reads of the cgroup controller list, for the delegated CPU, memory, and process controllers; lookups on `PATH` of `newuidmap`, `newgidmap`, `pasta`, `ssh`, and `ssh-keygen`; and reads of the subordinate UID and GID ranges. On Windows, read-only queries of the Podman client and the Podman machine, and read-only commands inside the running machine that the host's `ssh` runs with the identity, port, and user `podman machine inspect` reports, checking that the machine's host key stays the same within the preflight (ADR-0008). None of them installs, configures, starts, or repairs anything. The one permitted exception is Podman's own initialization during the informational `podman --version` and `podman info` queries, which may create or adjust Podman's per-user configuration and runtime directories.

### Distribution

**NuGet command package**:
The versioned NuGet archive, `SandboxedAgents.VERSION.nupkg`, that distributes the Windows executable, its checksum manifest, and PowerShell 7 install and remove scripts with their shared helper. It is not a project library: nothing references it as a dependency.
_Avoid_: NuGet library, NuGet dependency
