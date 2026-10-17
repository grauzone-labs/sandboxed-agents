# Security policy

This policy tells you how to report a vulnerability, what `sandboxed-agents` protects against, which Podman options every sandbox is created with, and where its protection ends. The limits are stated as plainly as the protections.

## Reporting a vulnerability

GitHub private vulnerability reporting is not enabled on `grauzone-labs/sandboxed-agents`, so the repository has no private form. Do not put exploit details, account names, or credentials into a public issue. Open an issue that only asks for a private contact for a security report, with no description of the flaw, and report the details once a private channel exists.

When you report, include the version from `sandboxed-agents version`, your platform, and the steps to reproduce.

## Threat model

`sandboxed-agents` is built against an agent that does something wrong: it deletes the wrong directory, installs a bad package, or reads credentials it should not have. A mistake of that kind stays inside one sandbox: its workspace, its home data, and the credentials stored there.

It is **not** built against code that actively tries to escape. A sandbox shares the host's kernel, so a kernel or container-runtime vulnerability can break out of it. Use a dedicated virtual machine for code that you consider hostile.

## Default Podman options

`up` creates every sandbox with the following Podman options. The user cannot weaken them: the only options that change what Podman receives are the resource limits, the port, and the workspace.

| Option | Value | Purpose |
|---|---|---|
| `--name` | `sandboxed-agents.GROUP.NAME` | The container name carries the controller group. A name alone authorizes nothing. |
| `--label` | `io.sandboxed-agents.owner=GROUP`, `io.sandboxed-agents.name=NAME`, `io.sandboxed-agents.port=PORT`, `io.sandboxed-agents.toolchains=SET` | The owner label is the authoritative check: the executable changes only containers and volumes whose owner is its own controller group. The other labels record the sandbox name, its SSH port, and its toolchain set. The three volumes carry the owner and name labels as well. |
| `--userns=keep-id:uid=1000,gid=1000` | fixed | Maps the host user to UID and GID 1000 inside the container. |
| `--user=0:0` | fixed | Starts the container as container root, which initializes the container, runs sshd, and runs the in-container manager for administrative operations. |
| `--security-opt=no-new-privileges` | fixed | A process cannot gain privileges through setuid or similar mechanisms. |
| `--network=pasta:--no-map-gw` | fixed | Rootless `pasta` networking without mapping the gateway address to the host. |
| `--memory` | `8g`, at least `6m` | Memory limit; `up --memory SIZE` changes it. |
| `--cpus` | `4` | CPU limit; `up --cpus N` changes it. |
| `--pids-limit` | `2048` | Limit on processes; `up --pids-limit N` changes it. |
| `--shm-size` | `1g` | Size of `/dev/shm`; `up --shm-size SIZE` changes it. |
| `--publish` | `127.0.0.1:PORT:22/tcp` | The only published port: SSH, on host loopback. The default port is the first free one from 2222 upward. |
| `--mount` | one `/workspace` mount, one volume at `/home/agent`, one volume at `/etc/ssh` | The only mounts. See [Mounts](#mounts). |

The resource limits are enforced by cgroups v2 with delegated controllers, which the preflight checks. The [command reference](docs/command-reference.md#lifecycle-commands) lists the accepted value ranges.

### Users and root

Container root is an ID from the user's subordinate ID range, not root on the host. Container root initializes the container, runs sshd, and runs the manager for administrative requests. Shells, toolchains, agents, agent sessions, the login workflows of agents, and every integration workflow, login and config alike, run as `agent`, UID and GID 1000. The manager changes to that identity before it installs or starts an agent ([ADR 0006](docs/adr/0006-podman-target-and-execution-identity.md)). The executable never runs with `sudo` and never installs or configures host prerequisites.

### Mounts

Only `/workspace` may be a host bind ([ADR 0003](docs/adr/0003-workspace-is-the-only-host-bind.md)). By default it is a volume of the sandbox; `up NAME WORKSPACE` binds exactly one host directory instead. The home directory and the SSH server state are always volumes of the sandbox. Host credentials, SSH-agent sockets, container-engine sockets, and display sockets are never mounted. Requests for extra mounts are refused by design.

A workspace bind is refused before Podman is called when it contains, or lies inside, a protected host path, also through symlinks, junctions, and other aliases. The protected paths are the executable, its package launchers, the default per-user Windows install directory, host state, temporary build inputs, and your SSH directory. On Windows, a workspace under a custom WSL automount root is refused.

### Publishing

Only SSH is published, and only on `127.0.0.1`. Other computers on your network cannot reach it. Other users and other processes on the same host can connect to the port, but the sandbox accepts only the key dedicated to it, installed with `ssh-config NAME --install` or `--ssh-config` on `up` and `start`. The host entry that SSH setup writes sets `IdentitiesOnly yes`, `IdentityAgent none`, `ForwardAgent no`, and `StrictHostKeyChecking yes`, and pins the sandbox's host key. Without that opt-in, the executable does not touch your SSH files.

## Limits

- **Shared kernel.** A container shares the kernel of the system it runs on. On Linux that is the host's own kernel. On Windows 11 it is the kernel of the WSL2 machine that Podman runs in, which all sandboxes on that machine share; the WSL2 virtual machine sits between the sandbox and Windows, but it belongs to your account and is not a boundary between sandboxes. A kernel or container-runtime vulnerability can break out of a sandbox, so this is weaker isolation than a dedicated virtual machine.
- **Outbound networking is open.** There is no egress control and no network allowlist. An agent can send anything it can read to any server it can reach, including the files of its workspace and the credentials in its home data.
- **Agents in one sandbox share everything.** They share the user `agent`, the home data, the credentials, and the workspace. Separation means separate sandboxes. A login of one agent, a GitHub or Azure login, and the Git credential file are readable by every agent in the same sandbox.
- **Credentials are stored in plaintext in the home volume.** The Git `credential-store` helper writes `/home/agent/.git-credentials` as plaintext. Credentials survive `update` and are removed only by `remove NAME --volumes`.
- **The workspace is writable by the agent.** A host directory bound as workspace can be changed or deleted by an agent. Commit or back up work you care about.
- **Container root exists in the container.** A flaw in the manager or sshd could be reached as container root. It is an ID of the subordinate range, so it is not host root, but it is not harmless.
- **No managed services or port forwarding.** The sandbox publishes nothing beyond SSH. A login that needs a browser callback inside the sandbox needs an SSH port forward that you start.
- **Images are shared across the host.** Their names contain no controller group, and no command removes them. They hold no user data. Packages in an image are as current as its last build.
- **Ownership is not configuration.** The owner label authorizes a command on an existing container, but it does not attest that container's configuration ([ADR 0006](docs/adr/0006-podman-target-and-execution-identity.md)). `up` on an existing sandbox checks the owners and the resource limits it is given, then starts the owned container as it is. It does not compare the image, the mounts, the user, the network, or the security options with the defaults above, and it does not harden the container again after someone changed it directly with Podman. Creating or changing such a container needs access to your Podman, which is already more than a sandbox may reach. To get the defaults back, `remove NAME` and `up NAME` again; the volumes are kept.
- **Podman target binding is by name.** On Windows every call names the selected Podman machine with `--connection`, and the executable removes `CONTAINER_HOST`, `CONTAINER_CONNECTION`, and `CONTAINER_SSHKEY` from the environment of its Podman calls. A program that runs as you can still change what the connection name reaches. On Linux the executable calls the local Podman without a connection, and a remote Podman setting there is a documented limit of the preflight.
- **The lifecycle lock coordinates, it does not protect.** It serializes `up`, `start`, `stop`, `restart`, `remove`, `update`, and the SSH setup mutations of one sandbox, for the same host user and the same host state root ([ADR 0007](docs/adr/0007-serialize-sandbox-lifecycle-commands.md)). It does not hold back other host users, commands that use a different host state root, or your own direct `podman` calls and scripts. Anyone with your Podman authority can change a sandbox while a command runs.
- **Owner conflicts are reported, not repaired.** The executable never touches an object it cannot prove to be its own, but this protects against name collisions, not against another user of your Podman.
- **Podman is trusted.** The executable calls the Podman on your `PATH` (on Windows, the Podman machine chosen by the executable) and trusts what it reports.
- **Checksums prove the file, not the author.** The `SHA256SUMS` in a download or package detect corruption. [Build provenance attestations](docs/releases.md#verify-a-download) show where a release file was built.

## What proves these statements

The offline test suite checks that this page names every option `up` passes to Podman create, with Podman replaced by a fake; it does not show that Podman, the kernel, or the network enforce the options on a real host. That is the job of the [live suite and the manual checklist](docs/releases.md#gates), and a check counts only when a record exists for it.
