# Command reference

This page gives you every command and option of `sandboxed-agents`, the exit status rule, the order in which a command checks its preconditions, and the way the executable treats objects it does not own. Each area links to the topic page with the detail behind it.

Terms such as sandbox, workspace, agent, integration, toolchain, image, controller group, and owner are defined in [`CONTEXT.md`](../CONTEXT.md). Architecture decisions are recorded in [`docs/adr/`](adr/0004-command-first-cli-grammar.md).

## Grammar

The command is `sandboxed-agents`. Commands come first, and the sandbox name is the first argument after the command path ([ADR 0004](adr/0004-command-first-cli-grammar.md)). The executable has no official short alias and is never installed as `sandbox`.

In the grammar, square brackets mark what is optional, an uppercase word stands for a value you choose, and `|` separates alternatives. An option with an uppercase word after it takes a value; an option without one is a switch. Give a value as `--option VALUE` or `--option=VALUE`.

```text
sandboxed-agents version
sandboxed-agents list
sandboxed-agents check [NAME]
sandboxed-agents build [--with SET]
sandboxed-agents up NAME [WORKSPACE] [--with SET] [--agents LIST] [--port N] [--ssh-config] [--memory SIZE] [--cpus N] [--pids-limit N] [--shm-size SIZE]
sandboxed-agents start NAME [--ssh-config]
sandboxed-agents stop NAME [--force]
sandboxed-agents restart NAME [--force]
sandboxed-agents remove NAME [--volumes] [--force]
sandboxed-agents update NAME [--with SET] [--force]
sandboxed-agents update --all [--force]
sandboxed-agents shell NAME
sandboxed-agents ssh-config NAME [--install|--remove]
sandboxed-agents fingerprint NAME
sandboxed-agents agents enable NAME AGENT [--version X] [--force]
sandboxed-agents agents disable NAME AGENT [--force]
sandboxed-agents agents update NAME AGENT [--unpin] [--force]
sandboxed-agents agents login NAME AGENT [WORKFLOW]
sandboxed-agents agents status NAME AGENT
sandboxed-agents agents run NAME AGENT [ARG...]
sandboxed-agents agents session NAME AGENT [--stop]
sandboxed-agents integrations login NAME github
sandboxed-agents integrations login NAME azure device-code
sandboxed-agents integrations login NAME azure browser
sandboxed-agents integrations login NAME azdo --organization URL
sandboxed-agents integrations config NAME git identity [--name PERSON] [--email EMAIL]
sandboxed-agents integrations config NAME git credentials
```

Names:

- **Sandbox `NAME`.** It starts with a letter or digit and continues with letters, digits, `_`, `.`, and `-`. No name is reserved.
- **Controller group.** The environment variable `SANDBOXED_AGENTS_GROUP` selects the controller group. It defaults to `default` and must match `^[a-z0-9][a-z0-9-]*$`. Each controller group is its own namespace; see [Ownership and names](#ownership-and-names).
- **`SET` and `LIST`.** Both are comma-separated names. `--with SET` names toolchains: `dotnet`, `playwright`, `azure`, and `native`, or `none` alone for the base image without toolchains. `--agents LIST` names agents of the catalog: `copilot`, `claude`, `codex`, and `opencode`. An unknown name fails before Podman is called and lists the valid names.
- **Parsing.** An unknown option, a duplicate option, a missing value, and a value on a switch are usage errors. After `--`, every argument is a positional argument.

## General commands

| Command | What it does |
|---|---|
| `version` | Prints the version and `asset-sha256 HASH`, the hash of the embedded build assets. It never calls Podman. |
| `list` | Lists the sandboxes of the current controller group with state, port, toolchain set, agents, and problems. States include "volumes only", "update interrupted", and "owner conflict". A sandbox whose image is older than the current one is marked as outdated. |
| `check` | Without a name, runs the preflight on its own and reports each missing host prerequisite. See [Host prerequisites](host-prerequisites.md). |
| `check NAME` | Diagnoses one sandbox. It reports an owner conflict, a leftover backup container of an interrupted update, and other problems, exits non-zero when it finds one, and changes nothing. |
| `build [--with SET]` | Builds the base image and every existing toolchain set of this executable anew, plus `SET` when given. See [Images](images.md#build-refresh-and-reuse). |

## Lifecycle commands

`up`, `start`, `stop`, `restart`, `remove`, and `update`, and the SSH setup mutations `ssh-config NAME --install` and `ssh-config NAME --remove`, never act on the same sandbox at the same time ([ADR 0007](adr/0007-serialize-sandbox-lifecycle-commands.md)). Each takes an exclusive, nonblocking lock for its sandbox in host state. A second command that finds the sandbox's lock taken exits non-zero, names the sandbox, says that another lifecycle or SSH setup command is in progress, and tells you to retry. The preview `ssh-config NAME` takes no lock. `update --all` takes the lock of each sandbox it acts on, and passes over a sandbox whose lock is taken and counts it as not updated.

**The lock coordinates, it does not protect.** It covers only these commands, run by the same host user with the same host state root. It does not cover commands of other host users, which have their own Podman and host state. It does not cover commands that resolve a different host state root (for example another `XDG_STATE_HOME` on Linux or another `LOCALAPPDATA` on Windows), because they lock a different file. It does not cover writers that use Podman directly, such as your own `podman` calls or scripts, which hold the same authority over Podman as the executable and are not held back by the lock. Image builds and loopback port allocation are shared across sandboxes and controller groups and are not serialized by it either.

| Command | What it does |
|---|---|
| `up NAME [WORKSPACE]` | Creates and starts a sandbox, or starts an existing one. Without `WORKSPACE`, the workspace is a volume of the sandbox. With one, exactly that host directory is bound at `/workspace`. A directory that contains or lies inside a protected host path is refused. |
| `start NAME` | Starts a stopped sandbox. |
| `stop NAME` | Stops a running sandbox. |
| `restart NAME` | Stops and starts a sandbox. |
| `remove NAME` | Removes the container and keeps the volumes, so a later `up NAME` adopts them. It cleans up the host side of an SSH setup. |
| `update NAME` | Moves the sandbox to the image of the installed executable, with the same volumes, port, limits, toolchain set, workspace, agent selection, and credentials. See [Updating a sandbox](images.md#updating-a-sandbox). |
| `update --all` | Updates every sandbox of the current controller group. It builds every needed image before it changes any sandbox, continues after a failure, and exits non-zero when any sandbox was not updated. See [Updating every sandbox](images.md#updating-every-sandbox). |

Options:

| Option | Applies to | Meaning |
|---|---|---|
| `--with SET` | `build`, `up`, `update NAME` | The toolchain set. `none` selects the base image. `update NAME --with SET` replaces the recorded set. `update --all` takes no `--with`. |
| `--agents LIST` | `up` | Agents to enable after the sandbox runs. A failed install leaves the sandbox running, is reported, and makes `up` exit non-zero. See [Agents](agents.md#choose-agents-at-creation). |
| `--port N` | `up` | The host loopback port for SSH. Without it, the first free port from 2222 upward that no sandbox of any controller group has recorded. |
| `--ssh-config` | `up`, `start` | Installs the opt-in SSH setup after the sandbox runs. See [SSH access](ssh.md). |
| `--memory SIZE` | `up` | Memory limit. Default `8g`, at least `6m`. |
| `--cpus N` | `up` | CPU limit. Default `4`. |
| `--pids-limit N` | `up` | Limit on processes. Default `2048`. |
| `--shm-size SIZE` | `up` | Size of `/dev/shm`. Default `1g`. |
| `--force` | `stop`, `restart`, `remove`, `update` | Proceed although agent sessions are running, and end them. The command names the sessions it ends. |
| `--volumes` | `remove` | Also remove the sandbox's three volumes, and with them the home data, the credentials stored there, and the SSH server state. |

Every limit is greater than zero and has no sign and no exponent. `SIZE` is a whole number of bytes, optionally followed by `k`, `m`, `g`, or `t` in either case (powers of 1024), and is at most 9223372036854775807 bytes. `--cpus` takes a whole number, optionally with a point and one to three decimals, at most `9223372036.854`. `--pids-limit` takes a whole number, at most 9223372036854775807.

Options that describe a sandbox's configuration apply when the sandbox is created. `up` on an existing sandbox with an option that differs from the recorded configuration fails and names the difference and the way to change it.

A command that would end an agent session, or replace the installation of an agent whose session is running, refuses while one runs, names the running sessions, and proceeds only with `--force`. This covers `stop`, `restart`, `remove`, and `update`, and for the agent concerned `agents disable`, `agents update`, and `agents enable` on an enabled agent. When the manager does not answer, `stop`, `restart`, `remove`, and `update` cannot rule out running sessions and refuse in the same way.

## Access commands

| Command | What it does |
|---|---|
| `shell NAME` | Opens a shell as `agent` in `/workspace` through Podman. It needs no SSH setup. The exit status is the shell's. |
| `ssh-config NAME` | Prints the host entry for the sandbox and creates nothing. |
| `ssh-config NAME --install` | Creates a key dedicated to the sandbox, authorizes it, pins the host key, writes the host entry, and adds the controller group's `Include` line to your SSH configuration. |
| `ssh-config NAME --remove` | Removes the host side of the SSH setup and, on a running sandbox, the authorization in the sandbox. |
| `fingerprint NAME` | Prints the SSH host key fingerprints of the sandbox, one line per key type, for comparison in a desktop UI. |

`--install` and `--remove` together are a usage error. See [SSH access](ssh.md) for VS Code Remote SSH, host entry names, and recovery.

## Agent commands

| Command | What it does |
|---|---|
| `agents enable NAME AGENT` | Installs the agent into the sandbox's home data and enables it. `--version X` installs exactly that npm package version and pins the agent to it. `--force` repairs an interrupted managed installation. |
| `agents disable NAME AGENT` | Removes the agent's command, selection entry, and pin. It leaves credentials alone. |
| `agents update NAME AGENT` | Reinstalls a pinned agent at its pin, or updates an unpinned one. `--unpin` installs the newest version and clears the pin. |
| `agents login NAME AGENT [WORKFLOW]` | Runs one of the agent's login workflows. It needs an interactive terminal. |
| `agents status NAME AGENT` | Reports whether the agent is enabled, its version, its pin, whether a session runs, and whether it is signed in when a status probe exists. |
| `agents run NAME AGENT [ARG...]` | Runs the agent once in `/workspace`. Every argument after the agent name is passed on unchanged. The exit status is the agent's. |
| `agents session NAME AGENT` | Starts or attaches to the agent's persistent terminal session. `--stop` ends the session and needs no terminal. |

All `agents` commands need a running sandbox with an answering manager; see [Agents](agents.md).

## Integration commands

| Command | What it does |
|---|---|
| `integrations login NAME github` | Signs in to GitHub with the device flow and sets up the Git credential helper. |
| `integrations login NAME azure device-code` | Signs in to Azure with a device code. |
| `integrations login NAME azure browser` | Signs in to Azure with a browser on your machine, through an SSH tunnel. It needs the SSH setup. |
| `integrations login NAME azdo --organization URL` | Stores an Azure DevOps personal access token for the organization. The token is read from standard input only. |
| `integrations config NAME git identity` | Sets `user.name` and `user.email` of `agent` for every repository in the sandbox. `--name PERSON` and `--email EMAIL` supply your name and email address (not the sandbox name); an omitted one is prompted for and needs a terminal. |
| `integrations config NAME git credentials` | Configures Git's `credential-store` helper with a file in the home volume. |

The options belong to their workflow. `--name` and `--email` are options of the `git identity` config workflow, and `--organization` is an option of the `azdo` login workflow. A workflow that takes no options refuses any. `azure` and `azdo` need the `azure` toolchain. On a sandbox without it, the command fails, names the toolchain, prints the complete `update NAME --with ...` command for the recorded toolchain set plus `azure` (the printed set never contains `none`), and changes nothing. `integrations login github`, `integrations login azure`, and the Azure browser and device-code workflows need an interactive terminal and fail without one. See [Integrations](integrations.md).

Credentials live in the sandbox's home volume. They survive `update` and are removed by `remove NAME --volumes`.

## Exit status

Exit status zero means the command did what was asked, or the requested state already held. Non-zero means the command could not do it, it refused, or a checking command (`check`, `check NAME`) found a problem. Only `agents run` and `shell` pass through another program's exit status. The first version defines no distinct codes per kind of failure; a usage error happens to exit with 2.

## Order of checks

Checks run in a fixed order and the first failure is reported. The order is binding for every command:

1. Usage, invalid names, unknown agent, integration, workflow, and toolchain names, and path errors (a `WORKSPACE` that does not exist or is not a directory, and a workspace bind that contains or lies inside a protected host path, also through an alias). This step calls no Podman and comes before the preflight.
2. The preflight, for the commands that run it (`build`, `up`, `update`). On Windows it also reads the machine's automount root, and a workspace under a custom root is refused here, before any call that builds or creates.
3. A taken lifecycle lock, for the lifecycle commands `up`, `start`, `stop`, `restart`, `remove`, and `update`, and for `ssh-config NAME --install` and `ssh-config NAME --remove`.
4. An unknown sandbox name, or a sandbox of which only volumes remain when the command needs a container. When one of those volumes has a missing or different owner, that owner conflict is reported here instead.
5. A missing or different owner of the container or of one of the sandbox's volumes.
6. An interrupted update, which every command except `update NAME`, `check NAME`, and `list` refuses, naming `update NAME`. `update --all` handles such a sandbox as `update NAME` does.
7. A stopped sandbox, for commands that need a running container; the message names `start NAME`. Nothing starts on its own.
8. Preconditions: first a manager that does not answer, then a missing toolchain, an agent that is not enabled, or conflicting options on `up`. `update` asks the manager only in its session guard, after the builds.
9. A missing terminal.
10. The session guard, directly before the change.

`update` builds a missing image only after the lifecycle lock of step 3 and the sandbox-state checks of steps 4 to 7, and before its session guard. A failing check of steps 1 to 7 therefore never leaves a build behind.

A sandbox of which only volumes remain counts as known. Commands that need a container fail for it and name `up NAME`, which adopts the volumes. When the manager of a running sandbox does not answer, every `agents` and `integrations` command and `ssh-config NAME --install` fail and name `check NAME` and `restart NAME`.

## Ownership and names

The sandbox `NAME` in controller group `GROUP` is the Podman container `sandboxed-agents.GROUP.NAME`. Its volumes carry the same prefix, with the suffixes `.workspace`, `.home`, and `.ssh`. The backup container of an update is `sandboxed-agents-backup.GROUP.NAME` ([ADR 0005](adr/0005-podman-names-carry-controller-group.md)). The owner label `io.sandboxed-agents.owner` is the authoritative check, and a matching name alone authorizes nothing.

Owner conflicts are reported and not repaired: `list` shows the state "owner conflict", `check NAME` describes the conflict with the Podman names of the objects concerned, every other command refuses except `remove NAME --volumes` when the sandbox's own container exists, `update --all` passes over such a sandbox and exits non-zero, and for everything else you resolve the conflict with Podman by removing or renaming the foreign object.

Loopback ports belong to the host, not to a controller group. Port allocation reads the ports recorded on the `sandboxed-agents` containers of every group and changes none of them. Images are shared across the host: their names contain no controller group.

## Limitation: old images stay

No command of the first version removes an image; remove unused ones yourself with Podman ([images](images.md#no-image-removal)).

## More documentation

The [README](../README.md#documentation) lists every page: the quick guides, the topic pages behind each command area, the release and package pages, the manual checklist, and the security policy.
