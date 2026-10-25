# Tools

This page tells you how to install T3 Code into a sandbox as a Tool, how its version moves, what keeps its service running, how to sign in to T3 Connect, how to pair a phone over a Tailscale tailnet, what `tools delete` removes and keeps, and where the limits are. [Command reference](command-reference.md#tool-commands) lists every option and every failure; the reasons behind the design are in [ADR 0009](adr/0009-tools-run-as-manager-supervised-services.md) and [ADR 0010](adr/0010-t3-code-pairs-over-an-in-sandbox-tailnet-node.md).

A **Tool** is a program or service that runs beside agents in a sandbox. It is installed into the sandbox's home data after the sandbox exists and runs as the user `agent`, UID and GID 1000, like an agent does. The first Tool is [T3 Code](https://github.com/pingdotgg/t3code), tool name `t3code`. It gives you T3 Code's web and mobile interface for the agents in a sandbox. Terms such as Tool directory, Tool selection, channel, and tailnet node are defined in [`CONTEXT.md`](../CONTEXT.md).

## Three sign-ins that are not the same

T3 Code involves three different sign-ins. Keep them apart:

| What | Command | What it is |
|---|---|---|
| **T3 Connect login** | `tools connect NAME t3code login`, then `link` | A login to your **T3 account**. It authenticates no coding agent. After `link`, a phone signed in to the same T3 account sees the sandbox as an environment without any QR code, over an outbound relay. |
| **QR pairing** | `tools tailscale NAME t3code login`, then `tools pair NAME t3code` | A one-time **device pairing**. A phone in your tailnet scans a QR code and talks to the sandbox's T3 Code directly over the tailnet. It uses no T3 account login, and it needs the sandbox's own Tailscale device. |
| **Agent provider login** | `agents login NAME AGENT` | The sign-in of a coding agent such as `claude` or `codex` to its provider. T3 Connect and QR pairing neither perform it nor replace it. |

An agent that T3 Code starts in a sandbox is one of the sandbox's [agents](agents.md) and needs its own provider login there.

## Enable T3 Code

```text
sandboxed-agents tools enable agent01 t3code
```

The sandbox must be running. The command installs the npm package `t3` at an exact version into the Tool directory in the sandbox's home data, makes the launcher download and verify T3 Code's own runtime, starts the T3 Code service, and prints `t3code enabled: version V (channel latest); service running`. The base image carries what T3 Code relies on (Node.js, `libatomic1`, and the `tailscale` package); no toolchain is needed. A sandbox created from an older image needs `build`, then `update NAME` ([images](images.md#base-image)).

Choose what the Tool follows:

| You run | The Tool selection records |
|---|---|
| `tools enable NAME t3code` | channel `latest`, upstream's stable line |
| `tools enable NAME t3code --channel nightly` | channel `nightly` |
| `tools enable NAME t3code --version 1.2.3` | a **Tool pin**, exactly that version |

A channel is resolved to one exact version when it is installed, and the installation always uses that exact version. `--channel` accepts only `latest` and `nightly`; upstream's `preview` line is no channel. `--version` takes an exact `MAJOR.MINOR.PATCH` with an optional prerelease. A tag, a range, or build metadata is a usage error, and a well-formed version that npm does not publish fails before anything is installed or recorded. `--channel` together with `--version` is a usage error.

The selection is stored in the Tool directory, `~/.local/share/sandboxed-agents/tools/t3code`, inside the sandbox's home data. It survives `stop`, `start`, `restart`, and `update`; host state keeps nothing about a Tool.

Running `tools enable` again without a selector keeps the installation and the selection, reports them, and starts the service only if it is not running. A damaged installation is repaired at the recorded version, without asking npm for a newer one.

## Update, repair, and roll back

T3 Code never updates itself, and upstream's update command is never called. The installed version changes only through `tools enable` and `tools update`, and only to the exact version the Tool selection names.

| You run | What happens |
|---|---|
| `tools update NAME t3code` on a **channel** | Moves to the newest release of the same channel and never to another line. When the newest release is the installed one and the installation is intact, nothing is reinstalled. |
| `tools update NAME t3code` on a **pin** | Keeps the pinned version. It reinstalls that version only when the installation is missing or damaged. |
| `tools update NAME t3code --channel CHANNEL` | Switches to the newest release of that channel. This is the way back from a pin: there is no unpin option. |
| `tools update NAME t3code --version X` | Switches to that exact version and records it as the pin. The executable does not compare versions, so `X` may be older than the installed one. |
| `tools enable NAME t3code --channel CHANNEL` or `--version X` on an enabled Tool | The same switch as `update` with that selector. |

An update without a selector never changes the selection. A switch installs the new version beside the active one, runs it once so that its runtime is downloaded and verified, and only then stops the service and starts it on the new version. The new selection is recorded only after the service runs on the new version.

**A failed update keeps the old version.** When the install, the runtime download, or the service start on the new version fails, the command removes what it installed, starts the previous version again if it had stopped it, leaves the selection as it was, exits non-zero, and names the failing step. A switch that was interrupted can leave an extra installation behind; the next `tools enable` or `tools update` removes every installation that the selection does not name.

**Rollback** is therefore automatic for a failed update. To go back to an older version after a successful update, pin it: `tools update NAME t3code --version OLDER`.

`tools enable`, `tools update`, and `tools delete` take the sandbox's manager lock, which agent installations share. A second change at the same time is refused with "another agent or tool change is in progress; retry".

`tools update`, `tools delete`, `stop`, `restart`, and `update NAME` stop the T3 Code service without asking. No `tools` command takes `--force`. The lifecycle commands `stop`, `restart`, `update NAME`, and `remove` keep their `--force`, whose one meaning is ending running agent sessions; it is not a guard for the T3 Code service, which these commands stop whether or not you pass it. Work that T3 Code is driving at that moment is interrupted ([ADR 0009, D2](adr/0009-tools-run-as-manager-supervised-services.md#d2-the-service-guard)).

## The service, its supervision, and its status

While T3 Code is enabled, one T3 Code service (`t3 serve`) runs in the background as `agent` and listens on the sandbox's loopback, `127.0.0.1:3773`. Nothing about the sandbox's Podman options changes for it, and no port is published for it.

A **service supervisor** that the manager starts keeps the service running. It is detached from the terminal and from the SSH connection that ran the command, so the service keeps running after the terminal that enabled it closes. It restarts a crashed service after one second, doubling the wait up to thirty seconds, and gives up after five consecutive starts that did not stay up for a minute. The service is then `failed`. Its output goes to `service/service.log` in the Tool directory, with the previous run in `service.log.1`. The supervision needs no systemd, no `t3 service install`, and no change to the image's `ENTRYPOINT` or PID 1.

Lifecycle commands treat the service as follows:

- `stop` ends the service together with the container. Once the sandbox is ready, `up`, `start`, `restart`, `update NAME`, and `update --all` start the service of every enabled Tool again, and `update NAME` does so also when it rolls back to the previous sandbox. If a tailnet node exists, it starts again with the service.
- When a service cannot start, the sandbox stays running, the command reports the failure, names `tools status NAME t3code`, and exits non-zero. `update --all` still counts that sandbox as updated and exits non-zero.
- `remove NAME` keeps the home volume and with it the Tool directory; a later `up NAME` adopts it and starts the service again. `remove NAME --volumes` removes the Tool with the home data.
- On Linux, keep lingering enabled for your user, or every sandbox and its Tool services stop when your last login session ends ([host prerequisites](host-prerequisites.md#linux-amd64)).

```text
sandboxed-agents tools status agent01 t3code
```

`tools status` reports whether the Tool is enabled, its installed version, its channel or pin, and the service state (`running`, `stopped`, or `failed`, with the last failure reason and the log location). On a healthy installation it also reports the T3 Connect state, the tailnet node, and the paired devices. It takes no lock and, for the T3 Connect, tailnet, and device parts, runs upstream's status queries as `agent`. Those can take several seconds right after an enable or a start; the command waits up to 30 seconds and then reports that upstream's status queries did not answer, not that the manager is unreachable. `check NAME` lists every enabled Tool and counts a `failed` service or node as a problem.

A paired phone appears in the device list under the label upstream's session gives it (observed as `t3 pair`), not the phone's name.

## T3 Connect: log in and link

T3 Connect is T3 Code's account and managed-connection service. It runs over an outbound relay and needs a T3 account and the T3 mobile app signed in to the same account.

```text
sandboxed-agents tools connect agent01 t3code login
sandboxed-agents tools connect agent01 t3code link
```

- `login` shows upstream's URL, device code, and expiry and waits while you approve in a browser on any device. It needs an interactive terminal. Denial, expiry, a request failure, and `Ctrl+C` each exit non-zero. A failed or interrupted login stores no partial new credential, and a login that existed before stays as it was, so a later `login` starts clean.
- `link` authorizes the sandbox's T3 Code service as an environment of your account and restarts the service so that the link takes effect. It needs an interactive terminal. On the phone, signed in to the same account, the environment appears without a QR code.
- `unlink` removes the environment and keeps the login. `logout` clears the login. Neither touches any agent provider login.

The credentials and the link state live only in the Tool directory inside the sandbox, readable by `agent`. The relay needs **outbound** connections only: to TCP and UDP port 7844 and over HTTPS. Block 7844 and `tools status` shows `relay failure`. No port is published.

**What the account reaches.** A device signed in to the linked T3 account can open the sandbox's T3 Code through the relay, and T3 Code runs agents as `agent`. Treat the account as having the reach of an agent in that sandbox: the workspace, the home data, and the credentials stored there ([SECURITY.md](../SECURITY.md#t3-code-network-paths)). `unlink` ends that reach.

## Reach T3 Code over Tailscale and pair a phone

QR pairing needs an address a phone can reach, and the sandbox has none of its own. The Tool therefore runs its own Tailscale device per sandbox, a **tailnet node**. This is the topology ([ADR 0010](adr/0010-t3-code-pairs-over-an-in-sandbox-tailnet-node.md)):

- Inside the sandbox, a `tailscaled` in userspace networking mode runs as `agent` under its own service supervisor, with its state in the Tool directory. It needs no TUN device, no capability, no host Tailscale, and no published port; the executable installs nothing on the host.
- Each sandbox signs in as a device of its own, named after the sandbox, so two sandboxes have two origins: `https://NAME.<your tailnet>.ts.net/`.
- Tailscale Serve maps that origin's HTTPS port 443 to the service on `http://127.0.0.1:3773`. Tailscale Funnel is never used, so nothing is reachable from the public internet.

### Prerequisites

- A Tailscale account and a phone with the Tailscale app and the T3 mobile app, both in the **same tailnet** as the nodes.
- **MagicDNS** and **HTTPS certificates** enabled for the tailnet at `https://login.tailscale.com/admin/dns`. `tools pair` names whichever is missing.
- Your tailnet's access policy must allow the phone to reach the node on port 443.
- A browser on any device to approve the node, and an interactive terminal for `login` and for `pair`. The login accepts no auth key.
- A running T3 Code service. The sandbox needs outbound access to Tailscale's control plane.

### Sign the node in and pair

```text
sandboxed-agents tools tailscale agent01 t3code login
sandboxed-agents tools pair agent01 t3code
```

`tools tailscale … login` creates the node, prints a Tailscale login URL that carries a one-time code (keep it to yourself), and waits until you approve the device. The node's hostname is the sandbox's name as a DNS label; Tailscale appends a counter if the name is taken in your tailnet. A login that fails or that you interrupt on a node it just created removes that node again. On a node that already existed, the failure keeps the node as it was, and `tools tailscale … logout` is what removes it. A node that is already signed in is reported and changed nothing.

`tools pair` creates the Serve mapping on the first pairing, then prints upstream's terminal QR code, a copyable URL, a token, and the expiry (five minutes by default; `--ttl 1m`, `--ttl 2h`, and so on change it). Scan it with the T3 mobile app within that time. The command exits without learning whether the code was scanned. A code works once; an expired or used code fails on the phone, and `tools pair` issues a new one. Upstream may note that the endpoint has not answered yet; that is expected, because the sandbox itself has no route to the tailnet.

`tools pair NAME t3code --revoke` revokes every unused code and works with the service stopped. It does not revoke a device that is already paired.

The mapping is kept in the node's state. After `restart` and `update NAME`, `tools status` shows the same origin and `Serve maps the origin to the service`, and the phone reaches the sandbox again without a second pairing.

`tools tailscale … logout` signs the node out as a best effort, stops it, and removes its state. If the sign-out fails the command warns, and the device stays in your tailnet admin console until it expires or you remove it there.

Pairing refuses before anything changes, with one message each, when the service is not running, the node is absent, stopped, failed, or not signed in, the tailnet has no MagicDNS or no HTTPS certificates, the node is offline, or the node's port 443 already fronts something else.

### What a signed-in node exposes

While the node is signed in, a device of your tailnet that your tailnet's access policy allows reaches more than the HTTPS origin on port 443. Through the node's tailnet address it also reaches the sandbox's **loopback listeners**:

- `sshd` on **port 22**, which accepts only the key dedicated to the sandbox ([SSH access](ssh.md)); and
- the T3 Code service on **port 3773** over plain HTTP, which T3 Code's own pairing and session tokens protect.

The host and the public internet gain nothing, and nothing is reachable while no node is signed in. The reach is governed by your tailnet's access policy, which you administer; restrict it there. Do not set `tailscale set --shields-up` on the node to narrow it: it blocks the HTTPS origin as well, and the phone loses the sandbox until it is unset.

## Delete T3 Code

```text
sandboxed-agents tools delete agent01 t3code
```

`tools delete` removes the Tool completely: it stops the service and its supervision, signs out of T3 Connect and signs the tailnet node out of your tailnet (both as a best effort; a failure is a warning), then removes the command link `~/.local/bin/t3` and the whole Tool directory. That includes the installed launchers, the downloaded runtimes and relay client, T3 Code's database, settings, secrets, logs, caches, pairing codes and device sessions, the node's state and Serve mapping, and the Tool selection.

**Your project files are kept.** T3 Code creates Git repositories and worktrees inside the Tool directory (`home/projects/` and `home/worktrees/`). Before it removes anything, the delete moves each one to `/home/agent/t3code-files/` with Git's own commands (`git worktree move`, `git worktree repair`), keeps the relative path, and prints every moved path. They stay usable with Git, and the delete never deletes or damages a worktree. A worktree whose location you set outside the Tool directory is not touched.

The delete **refuses before changing anything** when its checks, made before it stops the service, find a problem: a T3 Connect login in progress, a target path that already exists, a worktree whose repository cannot be found, a Git repository among the Tool's own files, or a command link that the Tool does not own. After those checks it begins to change things: it stops the service, signs out of T3 Connect and the tailnet, takes the inventory of project files again, and moves them. A failure at that point, such as a service or node that does not stop, a failed move, or a Tool directory that cannot be removed, exits non-zero with the service possibly stopped and the Tool partly intact, and names what remains. Run the delete again to complete it; it never removes the Tool directory while a project file has not been moved. A second delete finds nothing and exits zero. A later `tools enable` starts from scratch: channel `latest`, no pin, no login, no paired device.

It keeps the workspace, every agent and integration with their installations and credentials, the shared npm cache `~/.npm`, and `/home/agent/t3code-files/`. To remove the moved files too, delete that directory yourself in `shell`.

`remove NAME --volumes` also removes the Tool, together with everything else in the home data, but it makes no sign-out call: run `tools delete` first, or remove the sandbox's device from your tailnet admin console and revoke the T3 Connect link by hand.

## Limits

- **One Tool.** T3 Code is the only Tool. The service port is fixed at 3773 on the sandbox's loopback; every sandbox has its own network namespace, so two sandboxes do not collide.
- **No guard on the service.** Stopping, restarting, updating, or deleting interrupts what T3 Code is driving. What a phone shows for a run cut off that way has not been observed.
- **Only what you run changes the version.** There is no automatic update, no unpin option (`--channel` is the way back), and no `--force` for `tools` commands.
- **T3 Connect needs outbound TCP and UDP 7844.** It also needs a T3 account and the T3 mobile app.
- **QR pairing needs your tailnet.** It needs MagicDNS, HTTPS certificates, a signed-in node per sandbox, and an access policy that lets the phone reach it. Each sandbox is a device in your tailnet. The sandbox cannot check from inside whether the phone reaches the origin; `tools pair` does not confirm a scan.
- **A signed-in node widens the sandbox's inbound reach** to `sshd` on port 22 and the service on port 3773 for allowed tailnet devices ([above](#what-a-signed-in-node-exposes)).
- **The supervisor gives up after five failed starts.** The service is then `failed` until `tools enable`, `restart`, or another start request begins again.
- **A stopped sandbox runs no Tool.** On Linux, a host without lingering stops every sandbox when your last login session ends ([host prerequisites](host-prerequisites.md#linux-amd64)).
- **Linux amd64 sandbox image only.** Both host platforms run the Linux amd64 sandbox.
- **Moved project files stay.** `tools delete` leaves `/home/agent/t3code-files/` for you to keep or remove.

## What has been verified

This page describes the behaviour the offline tests cover and the commands implement. Live evidence is narrower: the Tailscale topology and phone QR pairing were run on a Linux and a Windows 11 host, and the checklist item for QR pairing passed by hand on a Linux preview; the T3 Connect phone link has not been run by hand. The runs on record, with their commits and the gaps, are listed in the [maintainer notes](maintainers.md#t3-code-tool-evidence-and-gaps); the steps are in the [manual checklist](manual-checklist.md#t3-connect-login-and-link).
