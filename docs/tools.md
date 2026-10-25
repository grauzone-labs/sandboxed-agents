# Tools

A Tool is a program or managed service that runs beside agents in a sandbox, installed into the sandbox's home data after the sandbox exists ([`CONTEXT.md`](../CONTEXT.md), [ADR 0009](adr/0009-tools-run-as-manager-supervised-services.md)). The first version ships one Tool, **T3 Code**, tool name `t3code`. It runs there as a background service, so that you can follow and steer the sandbox's agents from the T3 mobile app on your phone, without a change to the sandbox's containment. This page tells you how to enable it, how it updates and what a failed update keeps, what runs and what it reaches, how to link your phone, and how to remove it. The commands and their options are in the [command reference](command-reference.md#tool-commands).

Read [Logins and pairing](#logins-and-pairing) before you link a phone. The **T3 Connect login is a T3 account login**, **QR pairing is a device pairing**, and **agent provider sign-in stays `agents login NAME AGENT`**; a T3 account, a Tailscale tailnet, and your agents' providers are three unrelated accounts.

## What is verified and how

This page describes what the executable does, as its offline tests check it against fake Podman and fake SSH. An offline test does not show that a real host behaves that way. Nothing on this page is a claim that a live run or a manual check passed.

- The repository's live suite, `TestLiveSuite`, never enables a Tool ([releases](releases.md#live-suite)). Its records say nothing about T3 Code.
- Earlier observations on live hosts, such as those the ADRs 0009 and 0010 cite, informed the design. They are not acceptance evidence. What counts as live evidence for the Tool is a **recorded Tool run** that the [T3 Code validation runbook](t3code-validation.md) defines, with its commit, platform, and T3 Code versions. A run without such a record does not count.
- The phone checks, the T3 Connect link and the QR scan over Tailscale, are items of the [manual checklist](manual-checklist.md#t3-connect-login-and-link). A person observes them on a real phone, and they have a record of their own.

## Topology and prerequisites

```text
phone ── T3 mobile app ──┬── T3 Connect account ── relay ──(outbound, TCP/UDP 7844 and HTTPS)──┐
                         │                                                                    │
                         └── tailnet ── Serve origin https://NAME.TAILNET.ts.net/ (443) ──┐   │
                                         node address, ports 22 and 3773 ──────────────┤   │
                                                                                       ▼   ▼
   host ── Podman ── sandbox ── sshd (22) ── T3 Code service 127.0.0.1:3773 ◄── tailnet node, relay client
            └─ published: 127.0.0.1:PORT → 22/tcp, and nothing else
```

The T3 Code service listens on `127.0.0.1:3773` inside the sandbox and nowhere else ([ADR 0009](adr/0009-tools-run-as-manager-supervised-services.md#network-boundary)). Three paths lead to it:

| Path | For | Network |
|---|---|---|
| **SSH** | A T3 desktop client on the host | The existing SSH path. You start a port forward through the sandbox's `sshd`, as for a browser login ([SSH access](ssh.md)). The executable adds no publish and no forward. |
| **T3 Connect relay** | The phone, through your T3 account | The service's relay client, `cloudflared`, opens **outbound** connections only: TCP and UDP port 7844, and HTTPS. No inbound port exists. A firewall that blocks outbound port 7844 shows as `relay failure` in `tools status`. |
| **Tailnet origin** | The phone, by QR pairing | The Tool's own Tailscale device, described below. The phone must be in the same tailnet. |

**Tailscale is part of the Tool, not a prerequisite you install.** `tools tailscale NAME t3code login` creates a tailnet node in the sandbox, a `tailscaled` in userspace networking mode that runs as `agent` ([ADR 0010](adr/0010-t3-code-pairs-over-an-in-sandbox-tailnet-node.md)). It needs no `/dev/net/tun`, no capability, no host Tailscale, no published port, and no Funnel. The sandbox's image carries the `tailscale` package and starts no Tailscale process; a sandbox whose Tool never signed in runs none and keeps no Tailscale state. Each sandbox is its own device in your tailnet, named after the sandbox, with its own origin `https://NAME.TAILNET.ts.net/`; two sandboxes on one host get two origins, and each origin reaches only its own service. Tailscale appends a counter to the name when your tailnet already has a device of that name.

What you need:

- A sandbox that runs and has an image with `libatomic1` and the `tailscale` package. A sandbox created from an older image needs `build` and then `update NAME`; the commands say so when they find it missing.
- Outbound network access from the sandbox: for the npm install, for T3 Code's own runtime download, for the relay (TCP/UDP 7844 and HTTPS), and for the tailnet node.
- An interactive terminal for `tools connect … login`, `link`, `tools tailscale … login`, and `tools pair`. They fail without one before they change anything.
- For the QR path: a Tailscale account with **MagicDNS** and **HTTPS certificates** enabled at `https://login.tailscale.com/admin/dns`, and a phone in the same tailnet with the T3 mobile app. A sandbox's node does not need the admin console beyond that; you approve it in a browser on any device.
- For the T3 Connect path: a T3 account, and the T3 mobile app signed in to it.
- On Linux, lingering for your user, or systemd stops every running sandbox with its Tool services when your last login session ends ([host prerequisites](host-prerequisites.md#linux-amd64)).

### What a signed-in tailnet node reaches

While the node is signed in, `tailscaled` forwards a connection that a tailnet peer makes to the node's address to the **same port on the sandbox's loopback**. A device of your tailnet that your tailnet's access policy allows can therefore reach, besides the HTTPS origin on port 443:

- **port 22**, `sshd`, which accepts only the key dedicated to the sandbox;
- **port 3773**, the T3 Code service over plain HTTP, which T3 Code's own pairing and session tokens protect.

Through the node, nothing is reachable from outside your tailnet, and nothing while no node is signed in; the host and the public internet gain nothing through it. The T3 Connect relay and the host's SSH path are separate paths and are not affected by the node. [SECURITY.md](../SECURITY.md#tools-and-their-network-paths) is the canonical statement of the three paths. Your tailnet's access policy decides which peers get this reach, and the sandbox cannot see that policy. Do not set `--shields-up` on the node to narrow it: it blocks the HTTPS origin as well, and your phone loses the sandbox until it is unset ([ADR 0010](adr/0010-t3-code-pairs-over-an-in-sandbox-tailnet-node.md#what-this-changes-about-the-sandboxs-reach)).

### What does not change

A Tool changes no Podman option. The container and its volumes keep their names and labels, and the publish stays `127.0.0.1:PORT:22/tcp`, the only published port. There is no new mount, device, or capability. The network mode stays `--network=pasta:--no-map-gw`, the user, `--userns`, and `--security-opt=no-new-privileges` are as before, and the resource limits are unchanged. Host state records nothing about a Tool. See the table in [SECURITY.md](../SECURITY.md#default-podman-options) and [Tools and their network paths](../SECURITY.md#tools-and-their-network-paths).

## Logins and pairing

Five different things carry a login or a pairing name in T3 Code's setup. Only the first, the fourth, and the fifth are logins, and each to a different account.

| Action | Command | What it is | What it touches |
|---|---|---|---|
| **T3 Connect login** | `tools connect NAME t3code login` | A **T3 account** login. T3 Code shows a URL and a code, and you approve them in a browser. It stores the account authorization. It does not link anything yet. | Only the Tool directory in the sandbox. |
| **T3 Connect link** | `tools connect NAME t3code link` | Authorizes this sandbox's service as an **environment of your T3 account**. On the phone, you sign in to the same account and pick the sandbox. It shows **no QR code**. It restarts the service so the link takes effect. | The account's list of environments. |
| **QR pairing** | `tools pair NAME t3code` | A **device pairing**: one-time code for one device, shown as a QR code and a URL for the tailnet origin. It is not a login to any account. | A device session on the service. |
| **Tailscale node login** | `tools tailscale NAME t3code login` | Signs the sandbox's own tailnet node in to **your tailnet**. | Your tailnet's device list. |
| **Agent provider sign-in** | `agents login NAME AGENT` | Codex, Claude, Copilot, or OpenCode sign-in. | The agent's credentials in the sandbox. |

T3 Connect, QR pairing, and the Tailscale node authenticate **no agent provider**: sign in to an agent with `agents login NAME AGENT` ([agents](agents.md#sign-in-to-an-agent)), whether or not T3 Code is enabled. `tools connect … unlink` and `logout` never touch an agent or integration login.

## Enable T3 Code

```text
sandboxed-agents tools enable agent01 t3code
sandboxed-agents tools enable agent01 t3code --channel nightly
sandboxed-agents tools enable agent01 t3code --version 0.1.2
```

A first `enable` installs T3 Code's npm package `t3`, makes its launcher download and verify its own runtime, records the selection, starts the service, and prints `t3code enabled: version V (channel latest); service running`, or `(pin V)` for a pin.

- **Without a selector** it records the channel `latest`, upstream's stable line, and installs the version the npm tag `latest` names at that moment.
- **`--channel latest|nightly`** records a channel. `nightly` is upstream's nightly line. Upstream's `preview` line is no channel, and every other value is a usage error that lists both valid ones.
- **`--version X`** records a **Tool pin**: an exact `MAJOR.MINOR.PATCH` with an optional prerelease, such as `0.1.2-nightly.20261022.3526`. A tag, a range, or build metadata is a usage error. A well-formed version that npm does not publish fails before anything is installed or recorded.
- `--channel` together with `--version` is a usage error, on `enable` and on `update`.

A channel resolves to **one exact version at install time**, and the installation always uses that exact version. The tags move: `latest` and `nightly` name different versions tomorrow. The installed version is whatever the selection resolved to when you ran `enable` or `update`, shown by `tools status`.

A repeated `enable` without a selector on an enabled Tool keeps the installation and the recorded channel or pin, reports both, and starts the service only when it is not running. A damaged installation, a missing launcher or runtime, is repaired at its recorded version.

The installation runs as `agent` under the manager lock. A concurrent agent or Tool change is refused at once with "another agent or tool change is in progress; retry", and nothing waits.

## Update rules and what a failed update keeps

The recorded selection decides the version, and nothing else does. T3 Code's own update command is never called, and T3 Code's CLI and server do not update themselves. The channel or pin lives in the sandbox's home data and survives `stop`, `start`, `restart`, and `update NAME`; the host keeps no copy.

| Recorded | `tools update NAME t3code` | `--channel latest\|nightly` | `--version X` |
|---|---|---|---|
| channel | Moves to the newest release of that channel. When that is the installed version, nothing is reinstalled and the service is started only if it is not running. The channel stays recorded. | Records and installs that channel's newest release. | Pins `X`. |
| pin | Reinstalls the pinned version only when its installation is missing or damaged. It never changes the recorded pin. | Returns to a channel. There is no unpin option. | Pins `X`. |

An explicit selector that differs from the recorded one, on `enable` or `update`, installs that release, and the new channel or pin is recorded **only after** the install, the verified runtime download, and the service start on the new version have all succeeded.

**A failed switch keeps the previous installation, selection, and running service, and exits non-zero naming the failing step.** In order:

1. *Resolution fails* (npm does not publish the version, or cannot resolve the channel): nothing changes. The service is not stopped.
2. *Install or runtime download fails*, or the runtime reports another version: the staging prefix and the new runtime directory are removed. The previous installation and its service are untouched, because the service is stopped only after the install succeeded.
3. *The service does not start on the new version*: the new service is stopped, the new prefix and runtime are removed, and the previous version's service is started again. The message says whether the previous version is running again.
4. *The selection or the command link cannot be written*: the new service is stopped, the new version is removed, and the previous version is started again.

A switch that is interrupted before it reaches either end can leave an extra prefix and runtime directory. The next `tools enable` or `tools update` removes every prefix and runtime the selection does not name before it does anything else; the service keeps running from the named one meanwhile.

Limits of these statements: they are what the code does and what its offline tests prove with injected process functions. The runbook's forced failed update would exercise the first case only, with a version that npm does not publish ([T3 Code validation runbook](t3code-validation.md#forced-failed-update-and-what-it-proves)); no procedure here exercises the others live.

## The service

While T3 Code is enabled, exactly one T3 Code service, `t3 serve`, runs in the background as `agent`, on `127.0.0.1:3773`. A **service supervisor** that the manager starts, with no systemd and no init system in the image, keeps it running ([ADR 0009](adr/0009-tools-run-as-manager-supervised-services.md#d1-supervision-without-systemd)):

- It is detached from the terminal and the SSH connection that ran `tools enable`, so the service keeps running when they close.
- After a crash it restarts the service after one second, doubling up to thirty seconds, and gives up after five consecutive starts that did not stay up for a minute. The service is then `failed`; the next start request begins again. These values are design values, no live run measured them.
- A repeated `enable`, `start`, `restart`, or `up` never leaves two services.
- Every process of the Tool, the launcher, the downloaded runtime, `cloudflared`, and the tailnet node's `tailscaled`, runs as `agent`, UID and GID 1000, never as container root.
- The log is `service.log` in the Tool directory's `service/` directory, with the previous run in `service.log.1`.

**The lifecycle restores the service as agent work, not as a guard.** `up`, `start`, `restart`, `update NAME`, and `update --all` start the service again once the sandbox runs, and the tailnet node too when one exists; `update NAME` does so also on the backup container after a rollback. When the service cannot start, the sandbox stays running, the command reports the failure, names `tools status NAME t3code`, and exits non-zero. `stop` does not stop the service separately: Podman ends every process when it ends the container.

**There is no service guard.** `stop`, `restart`, `update NAME`, `tools update`, and `tools delete` stop the T3 Code service **without asking**, and `--force` keeps its one meaning, ending agent sessions. Anything T3 Code is driving at that moment is **interrupted**: T3 Code reports activity only per project, no stable query across projects exists, and T3 Code records an interrupted run itself. Check the phone's runs before you stop, restart, update, or delete a sandbox that T3 Code is steering ([ADR 0009, D2](adr/0009-tools-run-as-manager-supervised-services.md#d2-the-service-guard)). The guard for agent sessions applies as before, also while T3 Code is enabled.

## Status

```text
sandboxed-agents tools status agent01 t3code
sandboxed-agents check agent01
```

`tools status` reports, in one line:

- whether the Tool is enabled, the installed version, and the channel or pin;
- the **service**: `running`, `stopped`, or `failed`, with the last failure reason and the log location. A crashed service never shows as `running`;
- **T3 Connect**: signed in (with the account) or not, linked or not, the relay client, and `relay reachable` or `relay failure` with upstream's reason, for example a blocked outbound port 7844;
- the **tailnet node**: `absent`, or `running`, `stopped`, or `failed`, and for a running node whether it is signed in, its MagicDNS name and origin, whether Serve maps the origin to the service, whether the tailnet grants HTTPS certificates, and whether the node is online;
- the **paired devices**, each with its name and whether it is connected now, or `no paired device`.

The status of a Tool reads its records as one snapshot and takes no lock. The T3 Connect, tailnet, and device parts run upstream's and Tailscale's own queries as `agent`, and each part reports its **own** failure: a part that cannot be read prints its reason (for example `T3 Connect status unavailable: …`, `tailnet status unavailable: …`, or `paired devices unavailable: …`) while the other parts still print, and the command exits zero. A damaged installation says so and names `tools enable`. Those queries can take several seconds right after an enable or a start; the command waits up to 30 seconds for them and then reports that upstream's status queries did not answer, which does not mean the manager is unreachable (`check NAME` tells). `check NAME` lists every enabled Tool with its service and node state, counts a `failed` one as a problem, and starts no upstream program.

## Link the T3 mobile app

### Through your T3 account

```text
sandboxed-agents tools connect agent01 t3code login
sandboxed-agents tools connect agent01 t3code link
```

`login` prints upstream's URL, code, and expiry; open the URL in a browser on your computer or phone and approve. Denial, expiry, a request failure, and `Ctrl+C` each exit non-zero with their own message, and none leaves a credential behind. `link` restarts the service. Then sign in to **the same T3 account** in the T3 mobile app, pick the sandbox's environment, and open it. `unlink` unlinks the environment and keeps the login; `logout` clears the login and disables T3 Connect. The credentials live only in the sandbox's home data, readable by `agent`.

### By QR code over Tailscale

```text
sandboxed-agents tools tailscale agent01 t3code login
sandboxed-agents tools pair agent01 t3code
```

`tools tailscale … login` creates the node, prints Tailscale's login URL (it carries a one-time code; treat it as sensitive), and waits until you approve the device in a browser on any device. It accepts no auth key. `tools pair` then prints a QR code, a copyable URL, a token, and the expiry. Scan it with the T3 mobile app on a phone in the same tailnet.

- The code is **single use** and valid until the printed expiry, five minutes by default (`--ttl`). The command does **not** confirm the scan. A used or expired code fails on the phone; run `tools pair` again. `tools pair NAME t3code --revoke` revokes every unused code.
- It issues a code only for the tailnet HTTPS origin, never for a loopback or container address.
- It refuses before it changes anything, with its own message, when the service is not running, the node is absent, stopped, failed, or not signed in, the tailnet lacks MagicDNS or HTTPS certificates, the node is offline, or Serve on port 443 already fronts something else.
- The origin works again after `stop`/`start`, `restart`, and `update NAME` without a second pairing; `tools status` says `Serve maps the origin to the service`, or names `tools pair` when no mapping exists.

`tools tailscale … logout` signs the node out as a best effort and removes it. A failed sign-out is a warning; the device then stays in your tailnet admin console until it expires or you remove it there.

## Delete

```text
sandboxed-agents tools delete agent01 t3code
```

`tools delete` stops the service and its supervision record, signs out of T3 Connect and signs the tailnet node out (each a best effort, a failure is a warning), and removes the **Tool directory** `~/.local/share/sandboxed-agents/tools/t3code` entirely: the launchers, runtimes, `cloudflared`, T3 Code's user data, settings, credentials, `service.env`, logs, caches, pairing codes and device sessions, the node's state and its Serve mapping, and the recorded channel or pin. It also removes the command link `~/.local/bin/t3`.

**User worktrees are kept.** T3 Code may create Git worktrees and repositories inside its own home. Before it removes anything, the delete finds every Git repository and linked worktree inside the Tool directory and **moves** it to `/home/agent/t3code-files/`, keeping its path relative to T3 Code's home, with `git worktree move` and `git worktree repair`. It names each moved path. The projects stay usable with Git, and no worktree is deleted or damaged. A delete that cannot do this, because a target path exists, a worktree's repository cannot be found, a `.git` entry lies among the Tool's own files, or the command link is not the Tool's, refuses before it removes anything, and there is no option to skip the relocation.

**Kept:** the workspace, every agent with its installation, pin, and credentials, every integration, the shared runtimes of the image such as Node.js, **the shared npm cache** `~/.npm`, and the moved projects. T3 Code's own `uninstall` is not used, because it keeps user data.

A second `tools delete` finds nothing, says so, and exits zero. An interrupted delete reports what remains and exits non-zero; a rerun completes it. A later `tools enable` starts from scratch: channel `latest`, no pin, no T3 Connect login, no paired device. A worktree that T3 Code had moved outside the Tool directory is never touched.

## Limits

- T3 Code is the only Tool. Other Tools, a plugin mechanism, and upstream's `preview` line are not part of this version.
- The sandbox is Linux amd64; the host is Linux amd64 or Windows 11 x64.
- There is no unpin option and no `--force` on any `tools` command, and no service guard (see [The service](#the-service)).
- The installed version never moves by itself. A channel moves only when you run `tools update`.
- T3 Code's runtime and `cloudflared` come from upstream's releases, which T3 Code downloads and checks against upstream's checksums. A checksum proves the file, not the author. The executable adds no download of its own beyond the npm package.
- Anyone who can sign in to the linked T3 account, or holds a paired device, steers the agents in the sandbox, and agents reach everything the sandbox holds. This project has not audited T3 Connect's relay or account service.
- Reach through a signed-in tailnet node is described under [What a signed-in tailnet node reaches](#what-a-signed-in-tailnet-node-reaches). Whether your tailnet's access policy lets a phone reach the origin is outside what the sandbox can see.
- The sandbox itself has no route to its own tailnet address (userspace networking), so upstream's note that the endpoint has not answered yet is expected during `tools pair`.
- `tools tailscale … login` holds the manager lock while you approve the device; sign in before you start other agent or Tool changes in that sandbox.

See [T3 Code validation runbook](t3code-validation.md) for how the live behavior is recorded, and [SECURITY.md](../SECURITY.md#tools-and-their-network-paths) for the security statement.
