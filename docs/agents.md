# Agents

This page shows you how to enable, pin, update, and disable an agent in a sandbox, sign in to it, run it once, and keep it running in an agent session you can detach from. Agents are the coding CLIs of the agent catalog: `copilot`, `claude`, `codex`, and `opencode`. A sandbox installs an agent into its home data and enables it there; the agents enabled in a sandbox, each with its pin if it has one, are the sandbox's agent selection.

## Requirements

Every `agents` command needs a running sandbox. A stopped sandbox fails and names `start NAME`; a sandbox with only volumes names `up NAME`. If the manager does not answer, the command exits non-zero, says that the manager does not answer, names `check NAME` for diagnosis and `restart NAME` as the next step, and changes nothing. This failure comes before the check that the agent is enabled and before the check for a terminal, so a `run`, `login`, or `session` command on a sandbox whose manager is broken reports the manager, not the agent. Nothing starts automatically and there is no fallback around the manager.

The manager installs and works on agents as `agent` (UID and GID 1000), never as container root ([ADR 0006](adr/0006-podman-target-and-execution-identity.md)). `run`, `login`, and `session` start the manager directly as `agent`, and the manager refuses to work under any other identity.

## Concurrent changes

Installation changes (`enable`, `update`, `disable`) and session changes (starting or stopping an agent session) take the manager lock, so only one runs at a time in a sandbox. The lock is exclusive and nonblocking, the same rule [ADR 0007](adr/0007-serialize-sandbox-lifecycle-commands.md) sets for lifecycle commands. A change that finds another agent or Tool change in progress does not wait. It exits non-zero, says that another agent or tool change is in progress, and tells you to retry. Nothing retries automatically: run the command again once the first one has finished, and scripts that change agents in parallel must retry themselves. Attaching to a running session, `agents run`, and `agents login` do not take the lock.

`agents status` and `list` do not take the lock. They read the agent selection as one consistent snapshot and stay responsive while an npm install runs (`status` may also run the status probe). The snapshot only contains committed installations. While a first `enable` is still running, the agent is not in the snapshot yet: `status` reports it as not enabled and `list` omits it until the installation finishes. While an `update` or other replacement is running, the snapshot still shows the previously committed installation and version, not an absence, until the replacement commits.

The agent selection and its pins live in the sandbox's home data. They survive `stop` and `start`, and the host keeps no copy.

## Enable an agent

```text
sandboxed-agents agents enable agent01 claude
sandboxed-agents agents enable agent01 claude --version 1.2.3
```

Without `--version`, the command installs the agent's package at its newest version and records no pin. If the agent is already enabled, the command changes nothing, reports the installed version and the pin (or that there is none), and exits zero.

With `--version X`, the command installs exactly that package version and pins the agent to it. `X` must be an exact npm package version, `MAJOR.MINOR.PATCH` with an optional prerelease such as `1.2.3-beta.1`. Tags, ranges, and versions with `+build` metadata are rejected: npm strips build metadata, so it cannot provide an installation that matches such a literal pin.

- If that version is already installed, the command records the pin and installs nothing.
- If a different version is installed, the command replaces it with `X` and records the pin. This replacement refuses while a session of the agent is running; see [Sessions and installation changes](#sessions-and-installation-changes).

If an enabled agent's command is missing or does not match its selection entry, for example after an interrupted change, `enable` fails and names `--force` instead of reporting success or recording a new pin. See [Recover from an interrupted change](#recover-from-an-interrupted-change). On a healthy agent, a repeated `enable` without a new version is the same no-op with or without `--force`.

## Check an agent

```text
sandboxed-agents agents status agent01 claude
```

This reports whether the agent is enabled, its installed version, its pin, whether an [agent session](#keep-an-agent-running-in-a-session) of the agent is running, and, for agents whose catalog entry has a status probe, whether it is signed in. None of the four delivered agents declares a status probe today, so `signed in` shows `unknown` for all of them; use the agent's own status command in `shell NAME` to confirm a login. The probe runs as `agent` in the same worker as installation, so the agent's command never starts as container root. The probe runs only when the agent's command link is the one the selection records; if the command is missing, mismatched, or not a managed link, `status` still reports the selection and shows signed in as `unknown`. See [Recover from an interrupted change](#recover-from-an-interrupted-change).

## Update an agent

```text
sandboxed-agents agents update agent01 claude
sandboxed-agents agents update agent01 claude --unpin
```

- A pinned agent is reinstalled at its pinned version.
- An unpinned agent is updated to the newest version.
- `--unpin` installs the newest version and clears the pin. On an unpinned agent it behaves like an ordinary update.

A pin lasts until `agents update --unpin`, `agents disable`, or a new `--version` on `agents enable` removes or replaces it.

`update` needs an enabled agent. If the command is missing, `update` installs it again. If the command link points to a different installation than the selection entry, `update` fails and names `--force`; see [Recover from an interrupted change](#recover-from-an-interrupted-change).

`update` refuses while a session of the same agent is running; see [Sessions and installation changes](#sessions-and-installation-changes).

If an `update` failure includes a `retry:` line, it repeats the command you ran, including `--unpin` and `--force` if you passed them. That line is an ordinary update. If the failure message names `--force` for a mismatched or interrupted managed installation, add `--force` to the retry; without it the retry refuses again. Add it only when the message names it. `--force` never bypasses the check for an unsafe command.

## Disable an agent

```text
sandboxed-agents agents disable agent01 claude
```

This removes the agent's managed command, its entry in the agent selection, and its pin. When it removed a pin, the output names it. It leaves the agent's credentials, cached data, and all other contents of the home data alone. Enabling the agent again without `--version` installs the newest version with no pin.

A missing command does not stop `disable`. If the command link points to a different installation than the selection entry, `disable` fails and names `--force`; see [Recover from an interrupted change](#recover-from-an-interrupted-change). For an agent that is not enabled, `disable` reports that there is nothing to disable. `disable` refuses while a session of the agent is running; see [Sessions and installation changes](#sessions-and-installation-changes).

## Recover from an interrupted change

An `enable` or `update` that is killed at the wrong moment, or cut off by a container stop, can leave the agent's command link and its selection entry disagreeing. The command may be missing, may point to an installation the selection does not record, or may exist for an agent that has no selection entry.

| State | Without `--force` | With `--force` |
| --- | --- | --- |
| Healthy: the link matches the selection entry | `enable` is a no-op; `update` and `disable` work as usual | The same. `enable --version X` with a different `X` still installs `X` and replaces the pin, as it does without `--force`. |
| Enabled, command missing | `enable` fails and names `--force`; `update` installs again; `disable` removes the entry | `enable` installs again and restores the command |
| Enabled, link points to a different installation | All three fail, name `--force`, and change nothing | `enable` and `update` install again and replace the link and the entry; `disable` removes the link and the entry |
| Not enabled, link points to an installation | `enable` fails and names `--force`; `update` and `disable` report the agent as not enabled | `enable` installs and creates the entry, with no pin unless `--version` is given; `disable` removes the link; `update` still needs `enable --force` first |

Installing again keeps the pin recorded in the selection. `enable --version X` replaces it with `X`, and `update --unpin` clears it. A change that fails never reports the agent as enabled.

A change that inspects an agent's command checks it before it runs npm or changes anything, and refuses an unsafe command, with or without `--force`. `--force` applies only to the managed installation of the same catalog agent. It never bypasses this check: a regular file, a link outside the manager's installations, a link into another agent's installation or package, and a link that escapes its installation are all refused. Read-only commands do not change the command and do not refuse because of a command-link mismatch. `list` reads only the selection. `status` reads the selection and the command link, and runs the status probe only when the link is the selection's managed link; otherwise it shows signed in as `unknown`. A normal `disable` of an agent that is not enabled still reports that there is nothing to disable.

`--force` does not bypass the manager lock, so a second agent change still fails with "retry". When the change would replace or remove the installation of an agent whose session is running, `--force` also stops that session; see [Sessions and installation changes](#sessions-and-installation-changes). It does not make a change crash-consistent: the manager has no recovery protocol for every point at which a change can be interrupted. When `--force` repairs a mismatched or unrecorded command link, the manager attempts, after the selection change is committed, to remove the installation directory that link pointed to, only if no committed selection entry references it. The removal can fail and leave that directory behind. When the selection change fails, the manager attempts no cleanup of that directory and makes a best-effort attempt to restore the previous link. The manager does not sweep home data for other installation directories that no selection entry records, so an interrupted change can still leave one behind.

## Sign in to an agent

```text
sandboxed-agents agents login agent01 claude
sandboxed-agents agents login agent01 codex device-code
```

`login` runs one of the agent's login workflows from the catalog, in `/workspace`, as `agent`. Each workflow is the agent's own login command with fixed arguments; the sandbox adds nothing and stores nothing itself. Whatever the agent writes, it writes into the sandbox's home data, so a login survives `stop` and `start` and is gone with `remove --volumes`.

| Agent | Workflows | What the sandbox runs | Primary reference |
| --- | --- | --- | --- |
| `copilot` | `device-code` | `copilot login --device-code` | [Copilot CLI command reference](https://docs.github.com/en/copilot/reference/copilot-cli-reference/cli-command-reference) |
| `claude` | `browser` | `claude auth login` | [Claude Code CLI reference](https://code.claude.com/docs/en/cli-reference) |
| `codex` | `browser`, `device-code` | `codex login`, `codex login --device-auth` | [Codex CLI reference](https://developers.openai.com/codex/cli/reference/) |
| `opencode` | `provider` | `opencode auth login` | [OpenCode CLI docs](https://opencode.ai/docs/cli/) |

The workflow name is optional only when the agent has exactly one workflow: `agents login agent01 claude` runs `browser`, and `agents login agent01 codex` fails and lists `browser` and `device-code`. An unknown workflow fails the same way, before Podman is called.

The command needs an interactive terminal on both standard input and standard output, because every workflow shows a URL or code and waits for input. Without one it fails and starts nothing. Before the workflow starts, the command prints the agent's login message from the catalog, which says what to do on your host.

The sandbox has no browser and publishes nothing beyond SSH. Open the URL the agent shows in a browser on your host or on any other machine. A device-code workflow (`copilot`, `codex device-code`) needs no connection back into the sandbox: enter the code in the browser and the agent polls the provider itself. A browser workflow that waits for a callback on a local port inside the sandbox (`codex browser`) only completes when that port is reachable from your browser, which takes a port forward over SSH to the sandbox (see [SSH](ssh.md)); the catalog's login message for `codex` says so. `claude browser` and `opencode provider` show their URL or provider prompt and read the result in the terminal according to each agent's own documentation; follow the agent's instructions.

The exit status is zero when the agent's login command exited zero and non-zero otherwise, including when you cancel with Ctrl+C. A non-zero status prints `agents login failed` after the agent's own output. `login` needs the agent to be enabled; otherwise it names `agents enable NAME AGENT`.

Agents in one sandbox share the home data, so a login belongs to the sandbox, not to one session or run. `agents disable` keeps the credentials; `agents enable` afterwards finds them again.

## Run an agent once

```text
sandboxed-agents agents run agent01 claude --help
sandboxed-agents agents run agent01 codex exec "list the TODO comments"
sandboxed-agents agents run agent01 opencode run "summarize README.md" < /dev/null
```

`run` starts the agent's command once in `/workspace` as `agent`, with every argument after the agent name passed on unchanged and in order, including arguments that start with a dash. The agent's standard input, output, and error are your terminal's streams, and the exit status of `agents run` is the agent's exit status. Only `run` passes another program's exit status through; every other `agents` command uses its own.

A terminal is not required. When both standard input and standard output are a terminal, the agent gets a pseudo-terminal and runs interactively; otherwise it runs with plain pipes, so you can script it, redirect its output, or feed it input from a file. The agent has to be enabled; otherwise `run` names `agents enable NAME AGENT`.

A run is synchronous and not persistent: `agents run` waits for the agent and returns its exit status, and nothing in the sandbox keeps the run or reattaches to it. Interactively, the agent runs on a pseudo-terminal and normally stops when that terminal closes. With plain pipes, the agent is a `podman exec` process in the sandbox and the manager does not guarantee that it stops when the host command or the SSH connection goes away. Do not rely on either case to end the agent in time, and do not use `run` for an agent that should keep working: for that, use a [session](#keep-an-agent-running-in-a-session), which has a recorded lifetime and `--stop`.

## Keep an agent running in a session

```text
sandboxed-agents agents session agent01 opencode
sandboxed-agents agents session agent01 opencode --stop
```

An agent session is a persistent terminal session running one agent in `/workspace`, which you can detach from and reattach to. The manager keeps it in a tmux server of its own, run as `agent`, with tmux defaults and no user configuration. There is at most one session per agent in a sandbox, and the session's name is the agent's name.

`agents session NAME AGENT` starts the agent's session when none runs and attaches to it, or attaches to the existing one. Starting and attaching need an interactive terminal; without one the command fails and starts nothing. Only starting and stopping take the manager lock; attaching does not, so a second terminal can attach while a change on another agent runs.

- **Detach** with the tmux default, `Ctrl+b` then `d`. The agent keeps running. Closing the terminal, dropping an SSH connection, or killing the host command detaches too and leaves the agent alive.
- **Reattach** from any terminal, including a second one while the first is still attached, with the same `agents session NAME AGENT`. All attached terminals show the same session.
- **The session ends with its agent.** When the agent process exits, for example when you quit it from its own prompt, the session closes. A running session therefore always means a running agent, and `agents status` shows `session true` for it. The next `agents session NAME AGENT` starts a fresh one.
- **Stop** with `--stop`. It ends the session and with it the agent, like closing the agent's terminal, and prints the session it stopped. `--stop` needs no terminal. It exits zero when no session was running and reports that there is nothing to stop; it also exits zero on an agent that is not enabled. Stopping does not wait for the agent to save anything: detach and quit the agent from its prompt when you want an orderly exit.

A session lives in the running sandbox only. `stop NAME`, `restart NAME`, `remove NAME`, and `update NAME` end every session of the sandbox, which is why they refuse while sessions are running and name them unless you pass `--force`. Starting a sandbox again starts no session. The `session` command needs the agent to be enabled; otherwise it names `agents enable NAME AGENT`.

Sessions are not isolated from each other: the agents in a sandbox share its user, files, and credentials, and so do their sessions and runs.

## Sessions and installation changes

A change that would replace or remove the installation of an agent while that agent's session runs is refused, because the session's agent would keep running from a deleted installation. The guard runs under the manager lock, directly before the change, and it looks only at the session of the same agent: a session of another agent never blocks a change.

| Command on an agent with a running session | Without `--force` | With `--force` |
| --- | --- | --- |
| `agents disable` | Refuses, names the running session | Stops the session, names it, and disables |
| `agents update`, with or without `--unpin` | Refuses, names the running session | Stops the session, names it, and updates |
| `agents enable --version X` with `X` different from the installed version | Refuses, names the running session | Stops the session, names it, and installs `X` |
| `agents enable --version X` with the installed version `X` | Records the pin, installs nothing, no guard | The same |
| `agents enable` without `--version` | No-op, no guard | The same no-op |

`--force` here means "stop the session and proceed"; the command names the session it stopped. An `agents enable --force` on an agent that is not enabled, or without a `--version` that differs from the installed version, behaves as without `--force` and is not an error. The session guard and the command-link repair under [Recover from an interrupted change](#recover-from-an-interrupted-change) are independent: `--force` covers both, but a healthy agent with a running session is refused only for the session, and a mismatched command link without a session is refused only for the link.

## Choose agents at creation

```text
sandboxed-agents up agent01 --agents claude,codex
```

`up` enables the listed agents after the sandbox is running, for a new sandbox and for an existing one.

If an agent fails to install, the sandbox keeps running. `up` still attempts the remaining agents, exits non-zero, reports each failure, and names `agents enable NAME AGENT` to retry that agent.

The retry commands that `up` lists are ordinary `agents enable` commands without `--force`. If the failure message for an agent names `--force` because of an interrupted managed installation, run the listed retry with `--force` added, for example `sandboxed-agents agents enable agent01 claude --force`. `up` never adds `--force` itself. See [Recover from an interrupted change](#recover-from-an-interrupted-change).

If the manager does not answer, the sandbox also keeps running and `up` reports no agent as installed. If the manager stopped answering partway through, agents installed earlier in the same request are not rolled back. `up` reports that once, with one diagnosis that names `check NAME` and `restart NAME`, and lists the `agents enable NAME AGENT` command to retry for every agent you listed.

## See the selection

```text
sandboxed-agents list
```

`list` shows each sandbox's agent selection. For a sandbox that is stopped or whose manager does not answer, it shows the selection as unavailable instead of guessing.

## Limits

- `agents login` runs only the workflows the catalog records; it cannot pass options to an agent's login command. Use `agents run NAME AGENT ...` to start any other command of the agent, including other authentication commands, but `agents run` does not guard a running session or take the manager lock.
- No delivered agent has a status probe, so `agents status` cannot tell whether an agent is signed in.
- The sandbox publishes no port beyond SSH, so a login that waits for a browser callback inside the sandbox needs a port forward over SSH ([SSH access](ssh.md)).
- Installation changes are not crash-consistent. `--force` repairs a missing or mismatched managed command link as described under [Recover from an interrupted change](#recover-from-an-interrupted-change), but the manager has no complete recovery protocol for interrupted changes.
- Sign-ins are checked by hand only: the live suite enables an agent, starts, detaches, and stops its session, but signs in nowhere ([releases](releases.md#live-suite)), and the logins are items of the [manual checklist](manual-checklist.md).
