# SSH access

Every sandbox runs an SSH server that is published only on host loopback. Editors and desktop UIs connect to it over SSH ([ADR 0002](adr/0002-ssh-access-with-opt-in-host-setup.md)). This page shows how to set up that access, connect with VS Code Remote SSH and other desktop UIs, and recover when something changes.

## You may not need SSH

`shell NAME` opens a shell as `agent` in `/workspace` through Podman and needs no SSH setup. Use the SSH setup when an editor or desktop UI has to connect.

## SSH setup is opt-in

Without the opt-in, `up` and `start` do not read or write any file in your SSH directory. Opt in with one of these commands:

```text
sandboxed-agents ssh-config agent01 --install
sandboxed-agents up agent01 --ssh-config
sandboxed-agents start agent01 --ssh-config
```

Each of them does the following:

- creates a key dedicated to this sandbox;
- authorizes that key in the sandbox as the only key for `agent`;
- pins the sandbox's host key, so the first connection shows no host key prompt;
- writes a host entry for the sandbox;
- makes sure your SSH configuration contains the `Include` line of the current controller group.

The sandbox must be running. `ssh-config NAME --install` on a stopped sandbox fails and names `start NAME`; it starts nothing. `up NAME --ssh-config` on an existing sandbox starts it and installs the SSH setup.

Running `ssh-config NAME --install` again with an unchanged host key changes nothing, says so, and exits zero.

If the installation fails during `up` or `start`, the sandbox keeps running. The command exits non-zero, names the reason, and names `ssh-config NAME --install` to retry.

The host entry allows key authentication with the dedicated key only: it sets `IdentitiesOnly yes`, `IdentityAgent none`, `ForwardAgent no`, and `StrictHostKeyChecking yes`.

## Preview the host entry

```text
sandboxed-agents ssh-config agent01
```

This prints the host entry and creates nothing. If the SSH setup is not installed, it prints the entry that `--install` would write and says on standard error that the entry works only after `ssh-config NAME --install`. It also works on a stopped sandbox.

## Host entry names and controller groups

The host entry name depends on the controller group:

| Controller group | Sandbox | Host entry |
|---|---|---|
| `default` | `agent01` | `agent01` |
| `live` | `agent01` | `agent01.live` |

After installation in the `default` group, `ssh agent01` connects as `agent` using only the dedicated key.

Each controller group that has at least one installed SSH setup has its own managed configuration and exactly one `Include` line in your SSH configuration:

- The first installation in a group adds that group's `Include` line and changes nothing else in your SSH configuration. If you have no SSH configuration file, the installation creates it with that one line.
- A second SSH setup in the same group adds no line.
- An SSH setup in another group adds that group's line.
- Removing a group's last host entry removes that group's line and no other.

## Name conflicts

Sandbox names may contain dots, so two sandboxes can lead to the same host entry name. Before it writes an entry for a sandbox that has no SSH setup yet, `--install` asks OpenSSH whether your own SSH configuration already resolves that name to a configured host. This also catches an entry that you wrote by hand and an entry of a sandbox in another controller group.

If it does, `--install` exits non-zero, names the host entry, and changes no file. Rename or remove the conflicting host in your SSH configuration, or choose a different sandbox name, and run `--install` again. Settings that the system-wide SSH configuration applies to every host do not count as a conflict, and a host that is configured only there is not detected. If OpenSSH itself reports an error, for example because your SSH configuration is invalid, `--install` reports the failure and changes no file.

## Changed host key

The host key is pinned when you install the SSH setup. If the sandbox's host key later differs from the pinned one:

- `ssh` fails the connection instead of re-pinning silently;
- `ssh-config NAME --install` fails and leaves the pin unchanged.

If you expect the change, replace the SSH setup:

```text
sandboxed-agents ssh-config agent01 --remove
sandboxed-agents ssh-config agent01 --install
```

If you do not expect the change, compare the key first with `fingerprint NAME` (see below).

## Remove the SSH setup

```text
sandboxed-agents ssh-config agent01 --remove
```

This deletes the sandbox's host entry, key pair, and pinned host key, and the group's `Include` line when it was the group's last host entry. Other sandboxes' entries stay. `remove NAME` also deletes this host side when it removes the sandbox.

The authorized key lives in the sandbox:

- On a running sandbox, `--remove` also removes the public key from the sandbox's authorized keys.
- On a stopped sandbox, `--remove` cleans up the host side, says that the authorization remains in the sandbox, and exits zero. A later `--install` replaces it, so exactly one key is authorized.
- If the sandbox runs but its manager does not answer, `--remove` still cleans up the host side, says that the authorization in the sandbox could not be removed, and exits non-zero.
- When `remove NAME` keeps the volumes, it says that the authorization remains in the volume and is replaced by the next `--install`.

A refused `remove` leaves the SSH setup untouched.

## Connect with VS Code Remote SSH

1. Install the SSH setup: `sandboxed-agents ssh-config agent01 --install`.
2. In VS Code, install the Remote - SSH extension.
3. Run **Remote-SSH: Connect to Host...** from the Command Palette, then select or enter the sandbox's host entry: `agent01`, or `agent01.live` in the `live` group.
4. When the remote window opens, open the folder `/workspace`.

VS Code connects through the same host entry as `ssh`, so the dedicated key and the pinned host key apply. If the host entry is not in the list, enter its name. To check that the entry is installed, run `sandboxed-agents ssh-config agent01`; without an installed SSH setup it says so on standard error.

For more help with the extension, see the official guide, [Remote Development using SSH](https://code.visualstudio.com/docs/remote/ssh).

## Connect with other desktop UIs

Other desktop UIs that connect over SSH use the same host entry. Select or enter the host entry name (`agent01` or `agent01.live`) wherever the UI asks for an SSH host, so it uses the dedicated key and the pinned host key from your SSH configuration.

If a UI does not read your SSH configuration and asks for the host, port, user, and key itself, it shows the host key fingerprint of the server on its first connection. Compare it as follows.

### Compare a fingerprint

```text
sandboxed-agents fingerprint agent01
```

It prints three lines, always in this order, one per host key:

```text
ssh-ed25519 SHA256:...
ecdsa-sha2-nistp256 SHA256:...
ssh-rsa SHA256:...
```

Find the line with the key type that the UI shows and compare the `SHA256:` digest. The digest is written in the format that OpenSSH shows by default, so it has no `=` padding. Accept the connection in the UI only when it matches. `fingerprint NAME` needs a running sandbox, needs no SSH setup, and changes nothing. If it cannot read one of the three keys, it exits non-zero, names the file, and prints no fingerprint at all.

## Files and permissions

The SSH setup needs no administrator rights.

- **Linux:** the installation creates the directories and files of the controller group's host state with private permissions. If it creates `~/.ssh` or `~/.ssh/config`, they are private too. An existing `~/.ssh` keeps its mode, and a rewritten `~/.ssh/config` keeps the mode of the file it replaces.
- **Windows:** the key files and the managed configuration get permissions that OpenSSH for Windows accepts, under a standard account. A `.ssh` directory or SSH configuration file that `--install` creates gets them as well; an existing file keeps its permissions.

## When a command refuses

Every command of the SSH setup checks the sandbox in the fixed order of the product, and the first failure is reported:

- **Owner conflict:** if the container or one of the volumes has a missing or different owner, every form of `ssh-config`, `fingerprint`, `shell`, and `up` or `start` with `--ssh-config` refuse and name the Podman objects. Remove or rename the foreign object with Podman. Nothing is changed on the host.
- **Interrupted update:** the commands name `update NAME`.
- **Only volumes remain:** `shell`, `fingerprint`, `ssh-config NAME`, `ssh-config NAME --install`, and `start NAME --ssh-config` name `up NAME`, which adopts the volumes. `ssh-config NAME --remove` and `remove NAME` still clean up the host side of an SSH setup.
- **Manager does not answer:** `ssh-config NAME --install` names `check NAME` and `restart NAME` and creates nothing on the host.
- **Stopped sandbox:** `ssh-config NAME --install` and `fingerprint NAME` name `start NAME`.
