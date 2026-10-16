# Quick guide: Linux

This guide takes you from a clean Linux machine to a signed-in agent running in a sandbox. It uses the downloaded binary, which needs only Podman and OpenSSH on the host. For another install method see the [npm package](npm-package.md).

It was written from the project's design and tests. Whether a new user can follow it on a clean machine without other documentation is item `quick-guide-linux` of the [manual checklist](manual-checklist.md), and no recorded run exists yet.

## Before you start

You need:

- Linux on amd64 and a user account without `sudo` for everything below. `sandboxed-agents` never runs with `sudo` and never installs or configures Podman for you.
- Rootless Podman 4.4.0 or newer, with cgroups v2, subordinate UID and GID ranges (`newuidmap`, `newgidmap`), and `pasta`. [Host prerequisites](host-prerequisites.md) gives the reasons and the exact floors.
- OpenSSH: `ssh` and `ssh-keygen`.
- `curl` and `sha256sum`, to download and check the file.
- Optional: the GitHub CLI, `gh`, to verify the build attestation. See [Releases](releases.md#verify-a-download). Without it, step 3 compares checksums only.

Check Podman first:

```text
podman --version
```

## 1. Choose a release tag

A release is identified by its tag. The tag names below are placeholders: open the [releases page](https://github.com/grauzone-labs/sandboxed-agents/releases) of `grauzone-labs/sandboxed-agents` and use the tag of a release that is there. A preview tag looks like `v1.0.0-preview.YYYYMMDD.N`, and a stable tag like `v1.0.0`. This guide does not claim that any particular release exists; see [Releases](releases.md#status) for the current state. Put the tag you chose in a variable, replacing the placeholder:

```text
TAG=v1.0.0-preview.YYYYMMDD.N
```

## 2. Download

Download the Linux binary and the checksum file into a directory that does not exist yet. The command below stops at the first problem: it refuses a directory that is already there, and it does not start the next download after a failed one.

```text
test -n "$TAG" && \
mkdir ~/sandboxed-agents-download && \
cd ~/sandboxed-agents-download && \
curl -fLO "https://github.com/grauzone-labs/sandboxed-agents/releases/download/$TAG/sandboxed-agents-linux-amd64" && \
curl -fLO "https://github.com/grauzone-labs/sandboxed-agents/releases/download/$TAG/SHA256SUMS"
```

If any part fails (the tag is not set, the directory already exists, or either download fails), **stop**. Do not go on to step 3, and do not use the files that are there. Open a new terminal, set `TAG` again, and run the command again with a different, unused directory name, for example `~/sandboxed-agents-download-2` in both places. Never reuse a directory from a failed attempt, because the checksum check in step 3 could then look at old files.

## 3. Verify

Compare the checksum. `SHA256SUMS` also lists the Windows binary, so the command ignores lines for files you did not download:

```text
sha256sum --check --ignore-missing SHA256SUMS
```

It must print `sandboxed-agents-linux-amd64: OK`. A checksum shows that the file matches the checksum file. It does not show who built it.

If you have `gh`, also verify the build attestation of both files. The command must report a successful verification:

```text
gh attestation verify sandboxed-agents-linux-amd64 --repo grauzone-labs/sandboxed-agents
gh attestation verify SHA256SUMS --repo grauzone-labs/sandboxed-agents
```

Stop here if either check fails, and do not run the file.

## 4. Install

Put the binary in a directory on your `PATH`. The `export` below changes `PATH` for the current terminal only. If `~/.local/bin` is not on your `PATH` in new terminals, add the same `export` line to your shell profile (for example `~/.profile` or `~/.bashrc`) yourself; the executable never edits your profile.

```text
mkdir -p ~/.local/bin
install -m 0755 sandboxed-agents-linux-amd64 ~/.local/bin/sandboxed-agents
export PATH="$HOME/.local/bin:$PATH"
```

Do not install it into a directory you plan to use as a sandbox workspace: a workspace that contains the executable is refused.

## 5. Check the host

```text
sandboxed-agents version
sandboxed-agents check
```

`version` prints the version and an `asset-sha256` line. `check` reports every missing host prerequisite and exits non-zero when one is missing. Fix those with your distribution's tools and run it again; the executable only reads your host.

## 6. Create a sandbox

```text
sandboxed-agents up agent01
```

The first `up` builds the base image, which takes a while and needs network access. It creates the sandbox `agent01` with a workspace volume of its own, starts it, and publishes SSH on `127.0.0.1` only. Choose toolchains with `--with`, for example `--with dotnet`; the [command reference](command-reference.md) lists them. To use an existing host directory as the workspace instead, give `up` that directory as a second argument.

```text
sandboxed-agents list
```

## 7. Enable an agent

The agents are `copilot`, `claude`, `codex`, and `opencode`. This guide uses `claude`:

```text
sandboxed-agents agents enable agent01 claude
sandboxed-agents agents status agent01 claude
```

`enable` installs the agent into the sandbox with npm, so the sandbox needs network access. You can also pass `--agents claude` to `up`.

## 8. Sign in

```text
sandboxed-agents agents login agent01 claude
```

The command needs an interactive terminal. It prints a URL; open it in a browser on your machine, sign in, and follow the agent's prompts. The sandbox has no browser and publishes nothing but SSH. The login is stored in the sandbox's home volume and survives `stop`, `start`, and `update`. [Agents](agents.md#sign-in-to-an-agent) lists the login workflows of the other agents.

## 9. Start an agent session

```text
sandboxed-agents agents session agent01 claude
```

This attaches to a persistent terminal session running the agent in `/workspace`. Detach with `Ctrl+b` then `d`; the agent keeps running. Run the same command to reattach. The session ends when the agent exits.

You have now reached a signed-in agent session.

## Afterwards

Set your Git identity for commits in the sandbox, with your own name and email:

```text
sandboxed-agents integrations config agent01 git identity --name "A Person" --email "a@example.com"
```

For VS Code Remote SSH, install the opt-in SSH setup and follow [SSH access](ssh.md):

```text
sandboxed-agents ssh-config agent01 --install
```

Detaching leaves the agent running, and a running agent session makes `stop` refuse. End the session first with `--stop`, which needs no terminal. Then stop and start the sandbox; the login and the agent selection survive:

```text
sandboxed-agents agents session agent01 claude --stop
sandboxed-agents stop agent01
sandboxed-agents start agent01
```

Alternatively `stop agent01 --force` ends the sessions itself and names them. After `start`, no session runs; `agents session agent01 claude` starts a fresh one.

When a newer executable is installed, `update` moves the sandbox to its image and keeps the volumes, the port, the limits, the toolchain set, the agent selection, and the credentials. It refuses while an agent session runs, in the same way as `stop`, unless you pass `--force`:

```text
sandboxed-agents update agent01
```

Old images stay in Podman's storage; no command removes them. See the [README](../README.md#limitations).

### Clean up

This step is optional and last. `remove` deletes the container and keeps the volumes, so a later `up agent01` adopts them. It also refuses while an agent session runs. Add `--volumes` only when you want to delete the home data and the credentials stored there for good:

```text
sandboxed-agents remove agent01
sandboxed-agents remove agent01 --volumes
```

Run the first, the second, or both in this order. After the first, only the volumes remain, and the second deletes them; run alone, `remove agent01 --volumes` removes the container and the volumes together.

## More

[Command reference](command-reference.md), [Images](images.md), [SSH access](ssh.md), [Agents](agents.md), [Integrations](integrations.md), [Security policy](../SECURITY.md).
