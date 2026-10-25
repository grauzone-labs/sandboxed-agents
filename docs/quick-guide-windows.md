# Quick guide: Windows

**Goal:** a signed-in agent running in a sandbox on your Windows 11 machine, installed from the downloaded binary. **Expected duration:** 30 to 60 minutes, most of it the image build in step 6, which runs unattended. For a stable release, the [NuGet package](nuget-package.md) and the [npm package](npm-package.md) are the other install methods; what a sandbox protects and what it does not is in the [security policy](../SECURITY.md).

## Prerequisites

Check each item in PowerShell 7 or Windows PowerShell as your normal user, which is where every command below runs. `sandboxed-agents` never needs administrator rights and never installs or configures Podman, WSL2, or OpenSSH for you.

- [ ] **Windows 11 x64.** Check: `Get-CimInstance Win32_OperatingSystem | Select-Object Caption, OSArchitecture` prints `Microsoft Windows 11` and `64-bit`.
- [ ] **Podman 4.7.0 or newer as client and as machine** ([host prerequisites](host-prerequisites.md#windows-11-x64)). Check: `podman version` prints a `Client` and a `Server` version of `4.7.0` or higher. Missing: follow the [Podman for Windows tutorial](https://github.com/podman-container-tools/podman/blob/main/docs/tutorials/podman-for-windows.md) with the WSL provider.
- [ ] **A running rootless WSL2 Podman machine**, marked as default or the only one. Check: `podman machine list` prints one running machine with `*` after its name, or exactly one running machine.
- [ ] **`pasta` inside the Podman machine** ([host prerequisites](host-prerequisites.md#windows-11-x64)). Check: `podman machine ssh command -v pasta` prints a path; when your machine is not `podman-machine-default`, put its name after `ssh`.
- [ ] **OpenSSH client.** Check: `Get-Command ssh, ssh-keygen` prints two commands. Missing: enable the optional Windows feature **OpenSSH Client**.
- [ ] **Optional: the GitHub CLI**, to verify the build attestation ([releases](releases.md#verify-a-download)). Check: `gh attestation verify --help` prints the command's help.

## 1. Choose a release tag

Open the [releases page](https://github.com/grauzone-labs/sandboxed-agents/releases) of `grauzone-labs/sandboxed-agents` and take the tag of a release that is listed there; the tag below is a placeholder ([releases](releases.md#status)). Put it in a variable:

```text
$Tag = 'v1.0.0-preview.YYYYMMDD.N'
```

**Expected:** entering `$Tag` on a line of its own prints the tag you chose.

**If it fails:** the variable is empty in a new terminal; set it again there.

## 2. Download

Download the Windows binary and the checksum file into a new directory. The block stops at the first problem and refuses a directory that already exists; paste all of it at once.

```text
& {
  $ErrorActionPreference = 'Stop'
  if (-not $Tag) { throw 'set $Tag first (step 1)' }
  $Dir = "$HOME\sandboxed-agents-download"
  if (Test-Path -LiteralPath $Dir) { throw "$Dir already exists; choose a directory that is not in use" }
  New-Item -ItemType Directory -Path $Dir | Out-Null
  $Base = "https://github.com/grauzone-labs/sandboxed-agents/releases/download/$Tag"
  Invoke-WebRequest "$Base/sandboxed-agents-windows-amd64.exe" -OutFile "$Dir\sandboxed-agents-windows-amd64.exe" -ErrorAction Stop
  Invoke-WebRequest "$Base/SHA256SUMS" -OutFile "$Dir\SHA256SUMS" -ErrorAction Stop
  Set-Location -LiteralPath $Dir
}
```

**Expected:** the block prints no error, and `Get-ChildItem` shows `sandboxed-agents-windows-amd64.exe` and `SHA256SUMS`.

**If it fails:** do not use the files that are there. Open a new terminal, set `$Tag` again, and run the block with an unused directory name, for example `sandboxed-agents-download-2` in the `$Dir` line.

## 3. Verify

Compare the checksum, then verify the build attestation of both files. `SHA256SUMS` also lists the Linux binary, so the first lines pick the line of the Windows file; without `gh`, run the first three lines only ([releases](releases.md#verify-a-download)).

```text
$Expected = ((Get-Content SHA256SUMS | Where-Object { $_ -like '*sandboxed-agents-windows-amd64.exe' }) -split '\s+')[0].ToLower()
$Actual = (Get-FileHash sandboxed-agents-windows-amd64.exe -Algorithm SHA256).Hash.ToLower()
if ($Actual -eq $Expected) { 'checksum matches' } else { throw 'checksum differs: do not run this file' }
gh attestation verify sandboxed-agents-windows-amd64.exe --repo grauzone-labs/sandboxed-agents
gh attestation verify SHA256SUMS --repo grauzone-labs/sandboxed-agents
```

**Expected:** `checksum matches`, and, when you ran the `gh` commands, each reports a successful verification.

> **Warning:** if the checksum or an attestation check fails, do not run the file. Delete the download directory and start again at step 2 with a new directory name.

## 4. Install

Copy the binary to the per-user program directory that the NuGet installer uses as well, and put it on your `PATH`. The `$env:Path` line applies to the current terminal only; for new terminals, add `%LOCALAPPDATA%\Programs\sandboxed-agents` to your user `Path` in the Environment Variables dialog yourself and keep its existing entries, or let the [NuGet package](nuget-package.md) do it. The executable treats this directory as a protected host path, so a workspace that contains it is refused.

```text
$Dir = Join-Path $env:LOCALAPPDATA 'Programs\sandboxed-agents'
New-Item -ItemType Directory -Force $Dir | Out-Null
Copy-Item sandboxed-agents-windows-amd64.exe (Join-Path $Dir 'sandboxed-agents.exe')
$env:Path = "$Dir;$env:Path"
```

**Expected:** `Get-Command sandboxed-agents` prints the path `...\Programs\sandboxed-agents\sandboxed-agents.exe`.

**If it fails:** an error names the command when the `$env:Path` line did not run in this terminal; run it again.

## 5. Check the host

Both commands only read your host.

```text
sandboxed-agents version
sandboxed-agents check
```

**Expected:** `version` prints the version and an `asset-sha256` line; `check` prints `host prerequisites satisfied`.

**If it fails:** `check` names each missing prerequisite, including a Podman machine that is not running. Fix it with the Podman and Windows tools ([host prerequisites](host-prerequisites.md#windows-11-x64)) and run `sandboxed-agents check` again.

## 6. Create a sandbox

The first `up` builds the base image, which takes a while and needs network access. It creates the sandbox `agent01` with a workspace volume of its own, starts it, and publishes SSH on `127.0.0.1` only. Toolchains (`--with`) and a host directory as the workspace are options described in [Images](images.md#toolchains) and the [command reference](command-reference.md#lifecycle-commands); a host directory must lie under the Podman machine's default automount root ([host prerequisites](host-prerequisites.md#windows-11-x64)).

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

The command needs an interactive terminal. It prints a URL; open it in a browser on Windows, sign in, and follow the agent's prompts, because the sandbox has no browser. The login is stored in the sandbox's home data and survives `stop`, `start`, and `update` ([agents](agents.md#sign-in-to-an-agent) lists the workflows of the other agents).

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
- **T3 Code.** `sandboxed-agents tools enable agent01 t3code` installs T3 Code and keeps its service running in the sandbox. The [Tools page](tools.md) covers updates, T3 Connect, pairing a phone over Tailscale, delete, and the limits.
- **Update.** After you install a newer executable, `sandboxed-agents update agent01` moves the sandbox to the new image and keeps its volumes, port, limits, toolchain set, agent selection, and credentials ([images](images.md#updating-a-sandbox)). Close every running `sandboxed-agents.exe` before you replace the file, because Windows cannot replace an executable in use.
- **Clean up.** `sandboxed-agents remove agent01` removes the sandbox's container and keeps the volumes, so a later `up agent01` adopts them ([command reference](command-reference.md#lifecycle-commands)).

> **Warning:** `sandboxed-agents remove agent01 --volumes` also deletes the sandbox's three volumes for good: the home data with the credentials stored there, the SSH server state, and the workspace volume with the files in it.
