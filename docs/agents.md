# Agents

Agents are the coding CLIs of the agent catalog. The catalog names four: `copilot`, `claude`, `codex`, and `opencode`. A sandbox installs an agent into its home data and enables it there. The agents enabled in a sandbox, each with its pin if it has one, are the sandbox's agent selection.

This page covers installing, pinning, updating, and removing agents. Agent sessions, agent login, and running an agent once are separate commands and are not described here.

## Requirements

Every `agents` command needs a running sandbox. A stopped sandbox fails and names `start NAME`; a sandbox with only volumes names `up NAME`. If the manager does not answer, the command names `check NAME` for diagnosis and `restart NAME` as the next step. Nothing starts automatically and there is no fallback around the manager.

The manager installs and works on agents as `agent` (UID and GID 1000), never as container root ([ADR 0006](adr/0006-podman-target-and-execution-identity.md)).

## Concurrent changes

Installation changes (`enable`, `update`, `disable`) take the manager lock, so only one runs at a time in a sandbox. The lock is exclusive and nonblocking, the same rule [ADR 0007](adr/0007-serialize-sandbox-lifecycle-commands.md) sets for lifecycle commands. A change that finds another agent change in progress does not wait. It exits non-zero, says that another agent change is in progress, and tells you to retry. Nothing retries automatically: run the command again once the first one has finished, and scripts that change agents in parallel must retry themselves.

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
- If a different version is installed, the command replaces it with `X` and records the pin.

If an enabled agent's command is missing or does not match its selection entry, for example after an interrupted change, `enable` fails and names `--force` instead of reporting success or recording a new pin. See [Recover from an interrupted change](#recover-from-an-interrupted-change). On a healthy agent, a repeated `enable` without a new version is the same no-op with or without `--force`.

## Check an agent

```text
sandboxed-agents agents status agent01 claude
```

This reports whether the agent is enabled, its installed version, its pin, and, for agents whose catalog entry has a status probe, whether it is signed in. The probe runs as `agent` in the same worker as installation, so the agent's command never starts as container root. The probe runs only when the agent's command link is the one the selection records; if the command is missing, mismatched, or not a managed link, `status` still reports the selection and shows signed in as `unknown`. See [Recover from an interrupted change](#recover-from-an-interrupted-change).

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

If an `update` failure includes a `retry:` line, it repeats the command you ran, including `--unpin` and `--force` if you passed them. That line is an ordinary update. If the failure message names `--force` for a mismatched or interrupted managed installation, add `--force` to the retry; without it the retry refuses again. Add it only when the message names it. `--force` never bypasses the check for an unsafe command.

## Disable an agent

```text
sandboxed-agents agents disable agent01 claude
```

This removes the agent's managed command, its entry in the agent selection, and its pin. When it removed a pin, the output names it. It leaves the agent's credentials, cached data, and all other contents of the home data alone. Enabling the agent again without `--version` installs the newest version with no pin.

A missing command does not stop `disable`. If the command link points to a different installation than the selection entry, `disable` fails and names `--force`; see [Recover from an interrupted change](#recover-from-an-interrupted-change). For an agent that is not enabled, `disable` reports that there is nothing to disable.

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

`--force` does not bypass the manager lock, so a second agent change still fails with "retry". It does not stop, wait for, or check agent sessions, because sessions are not delivered yet. It also does not make a change crash-consistent: the manager has no recovery protocol for every point at which a change can be interrupted. When `--force` repairs a mismatched or unrecorded command link, the manager attempts, after the selection change is committed, to remove the installation directory that link pointed to, only if no committed selection entry references it. The removal can fail and leave that directory behind. When the selection change fails, the manager attempts no cleanup of that directory and makes a best-effort attempt to restore the previous link. The manager does not sweep home data for other installation directories that no selection entry records, so an interrupted change can still leave one behind.

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

Agent sessions, agent login workflows, and running an agent once are not delivered by these commands.

Installation changes are not crash-consistent. `--force` repairs a missing or mismatched managed command link as described under [Recover from an interrupted change](#recover-from-an-interrupted-change), but the manager has no complete recovery protocol for interrupted changes.

The `agents` commands cannot work against the base image the project supplies today. That image still holds a placeholder keepalive entrypoint ([#49](https://github.com/grauzone-labs/sandboxed-agents/issues/49)), contains no manager, and has no `agent` account. Until the image integration is completed, every `agents` command and `up --agents` fails at the manager check described under [Requirements](#requirements), and no agent can be installed. This page describes the intended behaviour of the commands; it has not been verified against a live sandbox.
