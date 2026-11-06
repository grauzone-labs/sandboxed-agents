# Agents

This page shows you how to enable, pin, update, and disable an agent in a sandbox, sign in to it, run it once, and keep it running in an agent session you can detach from. Agents are the coding CLIs of the agent catalog: `copilot`, `claude`, `codex`, and `opencode`. A sandbox installs an agent into its home data and enables it there; the agents enabled in a sandbox, each with its pin if it has one, are the sandbox's agent selection.

## Requirements

Every `agents` command needs a running sandbox. A stopped sandbox fails and names `start NAME`; a sandbox with only volumes names `up NAME`. If the manager does not answer, the command exits non-zero, says that the manager does not answer, names `check NAME` for diagnosis and `restart NAME` as the next step, and changes nothing. This failure comes before the check that the agent is enabled and before the check for a terminal, so a `run`, `login`, or `session` command on a sandbox whose manager is broken reports the manager, not the agent. Nothing starts automatically and there is no fallback around the manager.

The manager installs and works on agents as `agent` (UID and GID 1000), never as container root ([ADR 0006](adr/0006-podman-target-and-execution-identity.md)). `run`, `login`, and `session` start the manager directly as `agent`, and the manager refuses to work under any other identity.

## Concurrent changes

Installation changes (`enable`, `update`, `disable`) and session changes (starting or stopping an agent session) take the manager lock, so only one runs at a time in a sandbox. The lock is exclusive and nonblocking, the same rule [ADR 0007](adr/0007-serialize-sandbox-lifecycle-commands.md) sets for lifecycle commands. A change that finds another agent or Tool change in progress does not wait. It exits non-zero, says that another agent or tool change is in progress, and tells you to retry. Nothing retries automatically: run the command again once the first one has finished, and scripts that change agents in parallel must retry themselves. Attaching to a running session, `agents run`, and `agents login` do not take the lock, with one exception: the [`claude proxy` workflow](#sign-claude-code-in-to-a-cliproxyapi-server) writes a file in the home data and holds the lock for that write, so it refuses with the same retry message while another change runs.

`agents status` and `list` do not take the lock. They read the agent selection as one consistent snapshot and stay responsive while an npm install runs (`status` may also run the status probe, which gives the agent's command one second to finish and then waits at most another 100 ms to end it, so it adds at most about 1.1 seconds). The snapshot only contains committed installations. While a first `enable` is still running, the agent is not in the snapshot yet: `status` reports it as not enabled and `list` omits it until the installation finishes. While an `update` or other replacement is running, the snapshot still shows the previously committed installation and version, not an absence, until the replacement commits.

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

This reports whether the agent is enabled, its installed version, its pin, whether an [agent session](#keep-an-agent-running-in-a-session) of the agent is running, and whether it is signed in:

| Agent | `signed in` |
| --- | --- |
| `claude` | `yes` when Claude Code reports a login, `no` when it reports that it is signed out, `unknown` otherwise |
| `codex`, `copilot`, `opencode` | Always `unknown`; use the agent's own command in `shell NAME` to confirm a login |

`signed in` says that authentication is configured in the sandbox, not that the credential still works: a revoked or expired token can still show `yes`. Claude's answer comes from `claude auth status --json`, run as `agent` in the same worker as installation, so the agent's command never starts as container root. The check reads stored state only. It starts no login and refreshes no token, and it never prints the account details it reads. It also never contacts a [CLIProxyAPI server](#sign-claude-code-in-to-a-cliproxyapi-server): after `claude proxy`, the answer reflects what Claude Code counts as configured authentication and says nothing about whether the server accepts the key. If the command has not finished after one second, it is ended and the status is `unknown`; ending it can take up to another 100 ms, so `status` can wait about 1.1 seconds for this check. A pin that selects a Claude Code version without the command or with a different output also gives `unknown`, never `no`. The other three agents offer no command that is both documented and read-only, which [Agent status probes](agent-status-probes.md) shows with the version and documentation of each.

The probe runs only when the agent's command link is the one the selection records; if the command is missing, mismatched, or not a managed link, `status` still reports the selection and shows signed in as `unknown`. See [Recover from an interrupted change](#recover-from-an-interrupted-change).

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
| `claude` | `browser`, `proxy` | `claude auth login`; `proxy` runs no login command and writes Claude Code's user settings instead ([below](#sign-claude-code-in-to-a-cliproxyapi-server)) | [Claude Code CLI reference](https://code.claude.com/docs/en/cli-reference) |
| `codex` | `browser`, `device-code` | `codex login`, `codex login --device-auth` | [Codex CLI reference](https://developers.openai.com/codex/cli/reference/) |
| `opencode` | `provider` | `opencode auth login` | [OpenCode CLI docs](https://opencode.ai/docs/cli/) |

The workflow name is optional only when the agent has exactly one workflow: `agents login agent01 copilot` runs `device-code`, and `agents login agent01 opencode` runs `provider`. `agents login agent01 claude` fails and lists `browser` and `proxy`, and `agents login agent01 codex` fails and lists `browser` and `device-code`. An unknown workflow fails the same way, before Podman is called. Earlier releases ran `browser` for a bare `agents login agent01 claude`; since `claude` gained `proxy`, name the workflow.

Only the `proxy` workflow of `claude` takes options: `--url URL`, `--model ID`, and `--remove`. Any of them on another agent or workflow is a usage error that exits 2 before Podman is called.

Every workflow except `claude proxy` needs an interactive terminal on both standard input and standard output, because it shows a URL or code and waits for input. Without one it fails and starts nothing. Before such a workflow starts, the command prints the agent's login message from the catalog, which says what to do on your host. `claude proxy` prints no login message: it reads a key and writes a file, as described [below](#sign-claude-code-in-to-a-cliproxyapi-server).

The sandbox has no browser and publishes nothing beyond SSH. Open the URL the agent shows in a browser on your host or on any other machine. A device-code workflow (`copilot`, `codex device-code`) needs no connection back into the sandbox: enter the code in the browser and the agent polls the provider itself. A browser workflow that waits for a callback on a local port inside the sandbox (`codex browser`) only completes when that port is reachable from your browser, which takes a port forward over SSH to the sandbox (see [SSH](ssh.md)); the catalog's login message for `codex` says so. `claude browser` and `opencode provider` show their URL or provider prompt and read the result in the terminal according to each agent's own documentation; follow the agent's instructions.

The exit status is zero when the agent's login command exited zero and non-zero otherwise, including when you cancel with Ctrl+C. A non-zero status prints `agents login failed` after the agent's own output. `login` needs the agent to be enabled; otherwise it names `agents enable NAME AGENT`.

Agents in one sandbox share the home data, so a login belongs to the sandbox, not to one session or run. `agents disable` keeps the credentials; `agents enable` afterwards finds them again.

### Sign Claude Code in to a CLIProxyAPI server

```text
sandboxed-agents agents login agent01 claude proxy --url http://proxy.example:8317
sandboxed-agents agents login agent01 claude proxy --url https://proxy.example --model claude-fable-5-1 < key.txt
sandboxed-agents agents login agent01 claude proxy --remove
```

The `proxy` workflow points Claude Code at a [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) server that you already run and that the sandbox can reach, and gives it one of the server's client API keys. It is the client half only: it installs, hosts, and configures no server, and it signs in to no upstream account. Claude Code then sends its requests to `/v1/messages` under the URL, with the key as a bearer token, instead of to Anthropic. The workflow follows the [Claude Code CLIProxyAPI guide](https://help.router-for.me/agent-client/claude-code.html) for the settings it writes and copies none of the guide's permission-bypass or model-tuning examples.

**What it writes.** The configuration lives in Claude Code's own user settings file in the home data, `/home/agent/.claude/settings.json`, which Claude Code reads on every start ([Claude Code authentication](https://code.claude.com/docs/en/authentication)): `env.ANTHROPIC_BASE_URL` holds the URL, `env.ANTHROPIC_AUTH_TOKEN` holds the key, and `model` is set to `ID` when you give `--model ID`. Beside them the workflow records a small ownership entry, `_sandboxedAgentsProxy`, which holds no secret: the SHA-256 of the URL value and of the key value it wrote, the model selection it set, and the one it replaced, the two models as plain JSON values. A later `--remove` uses it to tell its own entries from yours without a second copy of the key. The write merges into the existing file and keeps every other setting; it goes to a temporary file in the same directory, which is then renamed over the settings file, so a failure at any point leaves the previous file byte for byte as it was. The file is owned by `agent` and readable by `agent` only (mode `0600`). Because Claude Code reads it on every start, `agents run`, `agents session`, `shell NAME`, and an editor connected over SSH all see the configuration. Claude Code may put the two values into its own process environment; that is its documented client mechanism, not something the sandbox injects.

**`--url`.** The base address of the server as seen from inside the sandbox: `http` or `https`, a host, an optional port, and an optional path prefix. It takes no user information, query, fragment, or whitespace, and it must not end in `/v1`, which Claude Code appends itself; trailing slashes are removed. `127.0.0.1` and `localhost` are the sandbox's own loopback, not your host's: the sandbox adds no port forward and no route, so the server must be reachable from the container as written. For `http` to any host other than loopback, the command warns on standard error that the key travels unencrypted. A missing or invalid `--url` is a usage error that exits 2 before Podman is called.

**The key.** It is read the same way as the Azure DevOps token ([Integrations](integrations.md#azure-devops)) and is never accepted as an option or argument. When standard input is a terminal, a `Key:` prompt appears on standard error and nothing is echoed while you paste; Enter ends the input. Otherwise the first line of standard input is used, so a pipe or a file works and the workflow needs no terminal. Ordinary spaces around the key are removed; a carriage return, NUL, or any other control character anywhere in the line is rejected before that trimming, unlike the Azure DevOps token, so piped input must end its line with LF alone, and a CRLF file or `echo` from a Windows shell fails. On Windows, the terminal prompt is the easiest way. An empty line, `</dev/null`, a key with whitespace inside, or a line with a control character exits 1 and changes nothing: by then the sandbox checks, the manager's version and status answers, and the check that `claude` is enabled have run, and the configuration write has not started. Those checks come first, so a stopped sandbox or a silent manager is reported before any key is consumed. The key reaches the manager on its standard input only. It appears in no process argument on the host or in the sandbox, in no Podman argument, in no host environment variable, in no shell startup file, and in no manager log or command output.

**Refusals.** The workflow changes nothing and exits non-zero when the settings file is not a JSON object, when its `env` is not an object, when `env.ANTHROPIC_API_KEY` is already set, or when `apiKeyHelper` is set: each message names the cause. The same refusal applies to `--remove`. Those two settings are another way of giving Claude Code a credential, and the workflow never deletes such a credential and never sends it anywhere, in either direction: resolve the conflict yourself, by removing or moving the setting, before the proxy can be written or removed. A missing settings file is not an error: the workflow creates it.

**Success.** The command prints that the configuration was written and that the key was not checked against the server. It then suggests the noninteractive check, which is the only check there is:

```text
sandboxed-agents agents run agent01 claude -p 'Reply OK'
```

A reply means that Claude Code reached the server and the server accepted the key and the model. An authentication error in Claude Code's output means that the server rejected the key; a connection error means that the URL is not reachable from the sandbox. The sandbox interprets neither; `agents run` passes Claude Code's output and exit status through. Sessions and runs that were already running keep their old configuration until you start them again; the command restarts nothing.

The request path itself is covered offline by a contract test that runs a real `claude` command against a fake upstream server and asserts that every request goes to `/v1/messages` under the configured URL with `Authorization: Bearer` and the configured model, and that no request leaves for an Anthropic host. It needs a Claude Code executable on the machine that runs it:

```text
SANDBOXED_AGENTS_TEST_CLAUDE=/path/to/claude go test ./internal/manager -run TestClaudeProxyNativeClientReachesOnlyFakeUpstream -count=1
```

It has passed against Claude Code 2.1.301, which accepted the settings file with the ownership entry and sent the bearer key, the model, and the URL prefix to the fake upstream only. It does not replace the live checks against a real server, which the [manual checklist](manual-checklist.md#claude-proxy-workflow) still holds as not yet run.

**Running it again, and `--remove`.** A second `proxy` replaces the URL and the key, and `--model` replaces the model selection again. `--remove` reads no key and deletes only what the workflow owns: the two `env` entries when their values are still the ones it wrote, and the `model` entry when it is still the managed one, which is then set back to the selection it replaced or dropped if there was none. A URL, key, or model you edited by hand afterwards is yours and stays. `--remove` with `--url` or `--model` is a usage error. When nothing is managed, `--remove` changes nothing, still reports the removal, and exits zero. The workflow takes the manager lock for its write, so it refuses with the same retry message as an installation change while another agent or Tool change runs.

**`browser` and `proxy` together.** The workflow never touches the OAuth login of `browser`, in either direction. While the proxy configuration exists, Claude Code uses it; after `--remove`, a browser login that was there before applies again, and a browser login made while the proxy was configured applies once the proxy is removed.

**`agents status`.** The probe still runs `claude auth status --json`, which reads what Claude Code finds configured and never contacts the server. With a proxy key and no browser login, the answer depends on whether the installed Claude Code version counts an auth token from its settings as a login, so `yes`, `no`, and `unknown` are all possible, and none of them says that the server accepts the key. The [manual checklist](manual-checklist.md#claude-proxy-workflow) records the value observed at the first live validation of the workflow. Use the `agents run` check above as the evidence.

**Where the key lives, and what can read it.** The key is stored as plaintext in the home volume. Every agent in the sandbox runs as `agent` and can read it, and so can anyone in `shell NAME` or connected over SSH. There is no encryption at rest; the keyring and encrypted-store work for other credentials ([#142](https://github.com/grauzone-labs/sandboxed-agents/issues/142), [#143](https://github.com/grauzone-labs/sandboxed-agents/issues/143)) does not cover it, and this workflow does not depend on either. The host keeps no copy. To take the key out of use, revoke it on the server; `--remove` only stops Claude Code from sending it. `remove NAME --volumes` deletes the home volume and the key with it.

**Persistence.** The URL, the key, and the managed model selection are home data, so they survive `stop` and `start`, `restart`, `update NAME`, `agents update`, and an `agents enable --force` reinstall of `claude`. `agents disable` keeps them, as it keeps every credential; `remove NAME --volumes` removes them.

## Run an agent once

```text
sandboxed-agents agents run agent01 claude --help
sandboxed-agents agents run agent01 codex exec "list the TODO comments"
sandboxed-agents agents run agent01 opencode run "summarize README.md" < /dev/null
```

`run` starts the agent's command once in `/workspace` as `agent`, with every argument after the agent name passed on unchanged and in order, including arguments that start with a dash. So `--help` after the agent name shows the agent's own help; `agents run --help` without a sandbox name shows the help of `agents run` itself. The agent's standard input, output, and error are your terminal's streams, and the exit status of `agents run` is the agent's exit status. Only `run` passes another program's exit status through; every other `agents` command uses its own.

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

Attach from a terminal that supports UTF-8 and 256 colours, as current terminal emulators do. The manager attaches with the tmux options `-u` and `-2`, which make tmux treat your terminal as a UTF-8 terminal with 256 colours whatever `TERM` and locale the terminal inside the sandbox reports. Without them, a `TERM` with eight colours, such as `xterm`, and no UTF-8 locale would make tmux draw the agent's logo, borders, and other non-ASCII characters as underscores and show the agent in eight colours. tmux can show an agent's 24-bit colours as the nearest of the 256. A terminal without UTF-8 or 256 colours shows those characters or colours wrongly.

The two options are automatic: the manager adds them, and `agents session` has no option of its own for them. A confirmed case: attaching OpenCode 1.19.4 without them drew only isolated fragments of its start screen, because tmux saw the client as a non-UTF-8 `xterm` with eight colours; with them the same session drew completely. To compare by hand, run tmux inside the sandbox as `agent`, for example in `shell NAME`, and give the options to tmux as global options before the subcommand, as in `tmux -L sandboxed-agents -f /dev/null -u -2 attach-session -t '=opencode'`; the options belong to tmux itself, not to `attach-session`. The [maintainer evidence](maintainers.md#cause-of-the-fragmented-session-2026-11-03) has the details and its limits.

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

- `agents login` runs only the workflows the catalog records; it cannot pass options to an agent's login command. The only options it takes, `--url`, `--model`, and `--remove`, belong to the `claude proxy` workflow and go to the sandbox's own settings write, never to Claude Code. Use `agents run NAME AGENT ...` to start any other command of the agent, including other authentication commands, but `agents run` does not guard a running session or take the manager lock.
- Only `claude` has a status probe. `agents status` shows `signed in unknown` for `codex`, `copilot`, and `opencode`, and for `claude` when its command is unavailable, slow, or older than the documented output ([why](agent-status-probes.md)).
- `signed in yes` means that the agent reports configured authentication; it does not show that the token is still valid or that the account can use the agent. After `claude proxy`, it does not show that the server accepts the key either; the documented check is `agents run NAME claude -p ...`.
- `claude proxy` stores the server key as plaintext in the home volume, readable by every agent in the sandbox, and validates it against nothing. Revoke it on the server to take it out of use.
- The sandbox publishes no port beyond SSH, so a login that waits for a browser callback inside the sandbox needs a port forward over SSH ([SSH access](ssh.md)).
- Installation changes are not crash-consistent. `--force` repairs a missing or mismatched managed command link as described under [Recover from an interrupted change](#recover-from-an-interrupted-change), but the manager has no complete recovery protocol for interrupted changes.
- Sign-ins are checked by hand only: the live suite enables an agent, starts, detaches, and stops its session, but signs in nowhere ([releases](releases.md#live-suite)), and the logins are items of the [manual checklist](manual-checklist.md), which also holds the Claude `signed in` check and the [Claude proxy workflow checks](manual-checklist.md#claude-proxy-workflow), the latter not yet run against a live server. [Agent status probes](agent-status-probes.md#live-acceptance) records where it has been observed.
