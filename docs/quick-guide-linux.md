# Quick guide: Linux

**Goal:** a signed-in agent running in a sandbox on your Linux machine, installed from the downloaded binary. **Expected duration:** 30 to 60 minutes, most of it the image build in step 6, which runs unattended. For a stable release, the [npm package](npm-package.md) is the other install method; what a sandbox protects and what it does not is in the [security policy](../SECURITY.md).

## Prerequisites

Check each item in a terminal before you start. `sandboxed-agents` never runs with `sudo` and never installs or configures Podman or OpenSSH for you.

- [ ] **Linux on amd64**, with a user account that needs no `sudo` for anything below. Check: `uname -m` prints `x86_64`.
- [ ] **Rootless Podman 4.4.0 or newer**, with cgroups v2, subordinate ID ranges, and `pasta` ([host prerequisites](host-prerequisites.md#linux-amd64)). Check: `podman --version` prints `4.4.0` or higher.
- [ ] **OpenSSH client.** Check: `command -v ssh ssh-keygen` prints two paths.
- [ ] **curl and sha256sum.** Check: `command -v curl sha256sum` prints two paths.
- [ ] **Optional: the GitHub CLI**, to verify the build attestation ([releases](releases.md#verify-a-download)). Check: `gh attestation verify --help` prints the command's help.

## 1. Choose a release tag

Open the [releases page](https://github.com/grauzone-labs/sandboxed-agents/releases) of `grauzone-labs/sandboxed-agents` and take the tag of a release that is listed there; the tag below is a placeholder ([releases](releases.md#status)). Put it in a variable:

```text
TAG=v1.0.0-preview.YYYYMMDD.N
```

**Expected:** `echo "$TAG"` prints the tag you chose.

**If it fails:** the variable is empty in a new terminal; set it again there.

## 2. Download

Download the Linux binary and the checksum file into a new directory. The command stops at the first problem and refuses a directory that already exists.

```text
test -n "$TAG" && \
mkdir ~/sandboxed-agents-download && \
cd ~/sandboxed-agents-download && \
curl -fLO "https://github.com/grauzone-labs/sandboxed-agents/releases/download/$TAG/sandboxed-agents-linux-amd64" && \
curl -fLO "https://github.com/grauzone-labs/sandboxed-agents/releases/download/$TAG/SHA256SUMS"
```

**Expected:** both downloads finish without an error, and `ls` shows `sandboxed-agents-linux-amd64` and `SHA256SUMS`.

**If it fails:** do not use the files that are there. Open a new terminal, set `TAG` again, and run the command with an unused directory name, for example `~/sandboxed-agents-download-2` in both places.

## 3. Verify

Compare the checksum, then verify the build attestation of both files. `SHA256SUMS` also lists the Windows binary, so the first command ignores files you did not download; without `gh`, run the first command only ([releases](releases.md#verify-a-download)).

```text
sha256sum --check --ignore-missing SHA256SUMS
gh attestation verify sandboxed-agents-linux-amd64 --repo grauzone-labs/sandboxed-agents
gh attestation verify SHA256SUMS --repo grauzone-labs/sandboxed-agents
```

**Expected:** `sandboxed-agents-linux-amd64: OK`, and, when you ran the `gh` commands, each reports a successful verification.

> **Warning:** if the checksum or an attestation check fails, do not run the file. Delete the download directory and start again at step 2 with a new directory name.

## 4. Install

Put the binary on your `PATH`. The `export` line applies to the current terminal only; for new terminals, add the same line to your shell profile (`~/.profile` or `~/.bashrc`) yourself, because the executable never edits it. Do not use a directory you plan to bind as a workspace: a workspace that contains the executable is refused.

```text
mkdir -p ~/.local/bin
install -m 0755 sandboxed-agents-linux-amd64 ~/.local/bin/sandboxed-agents
export PATH="$HOME/.local/bin:$PATH"
```

**Expected:** `command -v sandboxed-agents` prints `$HOME/.local/bin/sandboxed-agents` with your home directory filled in.

**If it fails:** nothing is printed when the `export` line did not run in this terminal; run it again.

## 5. Check the host

Both commands only read your host.

```text
sandboxed-agents version
sandboxed-agents check
```

**Expected:** `version` prints the version and an `asset-sha256` line; `check` prints `host prerequisites satisfied`.

**If it fails:** `check` names each missing prerequisite. Fix it with your distribution's tools ([host prerequisites](host-prerequisites.md#linux-amd64)) and run `sandboxed-agents check` again.

## 6. Create a sandbox

The first `up` builds the base image, which takes a while and needs network access. It creates the sandbox `agent01` with a workspace volume of its own, starts it, and publishes SSH on `127.0.0.1` only. Toolchains (`--with`) and a host directory as the workspace are options described in [Images](images.md#toolchains) and the [command reference](command-reference.md#lifecycle-commands).

```text
sandboxed-agents up agent01
sandboxed-agents list
```

**Expected:** `up` ends with a line `sandbox agent01 running; image ...; --publish 127.0.0.1:PORT:22/tcp`, and `list` shows `agent01` in state `running`.

**If it fails:** the output names the cause. Fix it and run `sandboxed-agents up agent01` again.

## 7. Enable an agent

The agents are `copilot`, `claude`, `codex`, and `opencode`; this guide uses `claude`. `enable` installs the agent into the sandbox with npm, so the sandbox needs network access ([agents](agents.md#enable-an-agent)).

```text
sandboxed-agents agents enable agent01 claude
sandboxed-agents agents status agent01 claude
```

**Expected:** `enable` prints `claude enabled: version ...`, and `status` prints `claude: enabled; version ...; pin none; session false; signed in unknown`.

**If it fails:** the output names the cause and, when the manager does not answer, `check agent01` for diagnosis; then run `restart agent01 --force`, because `restart` cannot rule out running sessions while the manager is silent. Fix the cause, for example missing network access in the sandbox, and run the `enable` command again.

## 8. Sign in

The command needs an interactive terminal. It prints a URL; open it in a browser on your machine, sign in, and follow the agent's prompts, because the sandbox has no browser. The login is stored in the sandbox's home data and survives `stop`, `start`, and `update` ([agents](agents.md#sign-in-to-an-agent) lists the workflows of the other agents).

```text
sandboxed-agents agents login agent01 claude
```

**Expected:** the agent confirms the sign-in, and the command exits without `agents login failed`.

**If it fails:** the command prints `agents login failed` after the agent's own output. Read that output, then run the command again.

## 9. Start an agent session

This attaches to a persistent terminal session running the agent in `/workspace`. Detach with `Ctrl+b` then `d`; the agent keeps running, and the same command reattaches. The session ends when the agent exits ([agents](agents.md#keep-an-agent-running-in-a-session)).

```text
sandboxed-agents agents session agent01 claude
```

**Expected:** the agent's prompt appears and answers without asking you to sign in. You have reached a signed-in agent session.

**If it fails:** the output names what is missing: `start agent01` for a stopped sandbox, `agents enable agent01 claude` for an agent that is not enabled, or an interactive terminal.

## Next steps

- **Git identity.** Set your name and email for commits in the sandbox: `sandboxed-agents integrations config agent01 git identity --name "A Person" --email "a@example.com"` ([integrations](integrations.md#commit-identity)).
- **SSH setup for an editor.** `sandboxed-agents ssh-config agent01 --install` creates a dedicated key and a host entry, which VS Code Remote SSH and other desktop UIs use ([SSH access](ssh.md#connect-with-vs-code-remote-ssh)).
- **Stop and start.** `sandboxed-agents stop agent01` and `sandboxed-agents start agent01` stop and start the sandbox; the login and the agent selection survive. A running agent session makes `stop`, `update`, and `remove` refuse: end it first with `sandboxed-agents agents session agent01 claude --stop`, or add `--force` to the refusing command, which ends the sessions and names them ([agents](agents.md#keep-an-agent-running-in-a-session), [command reference](command-reference.md#lifecycle-commands)).
- **T3 Code on your phone.** `sandboxed-agents tools enable agent01 t3code` installs T3 Code into the sandbox as a Tool, so that you can follow and steer its agents from the T3 mobile app. T3 Connect is a T3 account login and QR pairing is a device pairing; neither is the agent sign-in of step 8. The Tool, its network paths, and its limits are on the [Tools page](tools.md).
- **Update.** After you install a newer executable, `sandboxed-agents update agent01` moves the sandbox to the new image and keeps its volumes, port, limits, toolchain set, agent selection, and credentials ([images](images.md#updating-a-sandbox)).
- **Clean up.** `sandboxed-agents remove agent01` removes the sandbox's container and keeps the volumes, so a later `up agent01` adopts them ([command reference](command-reference.md#lifecycle-commands)).

> **Warning:** `sandboxed-agents remove agent01 --volumes` also deletes the sandbox's three volumes for good: the home data with the credentials stored there, the SSH server state, and the workspace volume with the files in it.
