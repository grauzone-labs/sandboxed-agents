---
status: accepted
---

# One Podman target per command, and explicit execution identities

On Windows, each command works on one Podman machine. The queries that select it, `podman machine list` and `podman machine inspect`, and the client's `podman --version` run in the local Podman client and name no connection. Every later call that looks at or changes a sandbox or an image names the selected machine with `--connection NAME`, and so do the preflight's version and info queries of the machine. `build`, `up`, and `update` use the machine their preflight checked. `start`, `stop`, `restart`, and `remove` run no preflight. Before their first lookup of the sandbox, they select the machine with the preflight's rule, using only `podman machine list` and `podman machine inspect`. The rule picks the one machine marked as default, or else the only machine. The command stops before it looks at or changes a sandbox in each of these cases:

- no machine exists;
- several machines exist and not exactly one of them is the default;
- the machine list or inspect answer cannot be read;
- the selected machine's name starts with `-` or contains a line break or a NUL character;
- the selected machine is stopped, does not use WSL2, is rootful, or does not report whether it is rootful.

The preflight, run by `check` and before `build`, `up`, and `update`, fails in the same cases.

No command starts a machine, and none falls back to another connection when a call fails. The executable removes `CONTAINER_HOST`, `CONTAINER_CONNECTION`, and `CONTAINER_SSHKEY` from the environment of its Podman calls on Windows. Podman's precedence (`--connection` before `--url`, these variables, and the default connection) is cited from [root.go, v5.0.0](https://raw.githubusercontent.com/containers/podman/v5.0.0/cmd/podman/root.go), as before; the guarantee that `--connection` beats `CONTAINER_HOST` was fixed upstream in Podman 4.8.0 (release notes, v4.8.0); the Windows floor is 4.7.0, so on that floor the removal of these variables is the guard that holds, and the precedence is a backstop only from 4.8.0 on. On Linux the executable calls the local Podman without a connection, and a remote setting there remains a documented limit of the check.

The binding is by connection name. The connection configuration belongs to the user's account, and a program running as that user can change what the name reaches. The binding keeps one command on the target it checked or selected, so that an ambient setting does not send it elsewhere. It does not attest the endpoint and does not defend against programs that have the user's authority.

Administrative control calls from the host to the in-container manager, such as the session query, run as container root. Each such call states this with `podman exec --user=0:0` ([podman-exec(1), `--user`](https://docs.podman.io/en/latest/markdown/podman-exec.1.html)) and does not inherit the user from the container's start user. Agent installation and agent work (agents, shells, toolchains used by agents, agent sessions, and integration workflows) must run as `agent`, UID and GID 1000, and the change to that identity must happen before an agent is installed or started and before an agent session is reached. The work of the Features follows this rule as described below.

- **Agent installation** (Agents Feature): the manager, called as container root, starts itself again as an unprivileged worker with UID and GID 1000, and only that worker reads the home volume and runs npm. The status probe of `agents status` runs in the same worker, so the agent's command never starts as container root.
- **Running an agent once** (Agents Feature): the host checks with the version query as container root that the manager answers, then starts the manager directly as `agent` with `podman exec --user=1000:1000 --env HOME=/home/agent`. The manager refuses to work under any other identity and starts the agent with an explicit request for UID and GID 1000.
- **Agent sessions** (Agents Feature): the host starts the manager for `agents session` directly as `agent` with `podman exec --user=1000:1000`, the manager refuses any other identity, and it runs the tmux server, the session, and the agent with UID and GID 1000. The session query is a root call, but the manager, called as container root, starts itself again as the trusted worker with UID and GID 1000 before tmux runs, so it reads the sessions of `agent`.
- **`shell`** (SSH access Feature): `podman exec --user=1000:1000`.
- **Git identity and Git credentials** (Integrations for Git, GitHub, Azure, and Azure DevOps Feature): the check whether the manager answers is the session query and runs as container root, while the host starts the manager that executes the workflow directly as `agent` with `podman exec --user=1000:1000 --env HOME=/home/agent`, so the identity is `agent` before Git starts and the global Git configuration it writes, including the credential helper, belongs to `agent`.
- **GitHub login** (same Feature): after the session query as container root, the host starts the manager directly as `agent` with `podman exec --user=1000:1000 --env HOME=/home/agent -it`, and the manager refuses any other identity and starts the GitHub CLI with an explicit request for UID and GID 1000, so its token and the Git credential helper it configures belong to `agent`.
- **Azure login** (same Feature): in the same way, the manager starts the Azure CLI with an explicit request for UID and GID 1000, so the login it stores belongs to `agent`. The `device-code` workflow needs a terminal. The `browser` workflow runs the same way, and its redirect port is reachable only through an SSH tunnel to the sandbox's sshd; the sandbox publishes nothing beyond SSH.
- **Azure DevOps login** (same Feature): `podman exec --user=1000:1000 --env HOME=/home/agent -i`, which passes the token on standard input; the manager refuses any other identity and starts the Azure CLI and Git with an explicit request for UID and GID 1000, so the stored token and the Git configuration belong to `agent`. The login also writes the token into the Git credential-store file of the home volume through `git credential approve`.

This way no agent process runs as container root, and agent sessions are reached under the agents' own identity.

The owner label authorizes a command on an existing container (ADR-0005), but it does not attest that container's configuration. `up` checks the owners and the resource limits it is given. It then starts an existing owned container as it is and does not compare the image, mounts, user, network, or security options with the defaults. Creating or changing such a container needs access to the user's Podman, which is already more than a sandbox may reach.

Offline tests check the arguments that the executable passes to Podman, but they cannot show isolation. Only runs against real Podman can show that the user namespace, start and exec users, `no-new-privileges`, network, mounts, and limits isolate a sandbox as intended. That evidence belongs to the live suite (Distribution and releases Feature) and counts only from recorded live runs.

## Considered options

- **Refuse to run when `CONTAINER_HOST` or `CONTAINER_CONNECTION` is set.** Rejected: the executable removes both from its Podman calls, and an explicit `--connection` takes priority over them anyway, so refusing would add a failure that protects nothing further.
- **Pick a running machine when several exist without a default.** Rejected: the choice would be a guess, and the command could act on a machine nobody chose.
- **Run the full preflight in `start`, `stop`, `restart`, and `remove`.** Rejected: `start`, `stop`, and `restart` change only whether an existing sandbox runs, and `remove` deletes a container and, when asked, volumes that already exist. None of them builds an image or creates a sandbox, which is what the host prerequisites are checked for. They need the target and the facts that make it acceptable, not every host prerequisite.
- **Let `podman exec` inherit the container's start user.** Rejected: the manager's identity would depend on a create option and could change silently with it.
- **Compare the full configuration of a reused container.** Not part of this decision. It would be new scope beyond the first version.

## Consequences

- On Windows, commands without a preflight issue read-only machine queries before their first lookup.
- On Windows, the preflight's commands inside the selected machine run the host's `ssh` with the identity file, port, and user that `podman machine inspect NAME` reports, not `podman machine ssh` (ADR-0008). The machine is still selected and inspected only through Podman, and the inspected SSH configuration is validated before any client starts; the lifecycle commands that only select the target never use it. Linux is unchanged.
- A Windows user with several Podman machines has to mark one as the default before the commands act on any of them.
- Agent installation, the status probe, running an agent once, agent sessions with their session query, `shell`, and every integration workflow run as UID and GID 1000, and their offline tests check the requested identity. Any further agent work must run as UID and GID 1000 as well, and its tests must check it. Only administrative control calls use container root.
- The user documentation states the target binding, its name-based limit, the two identities, and the unattested reuse of owned containers as plainly as the protections.
