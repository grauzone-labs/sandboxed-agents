# Quick guide: Windows

This guide takes you from a clean Windows 11 machine to a signed-in agent running in a sandbox. It uses the downloaded binary, which needs only Podman and OpenSSH on the host. For other install methods see the [NuGet package](nuget-package.md), which installs the same executable and adds it to your `PATH` for you, and the [npm package](npm-package.md).

It was written from the project's design and tests. Whether a new user can follow it on a clean Windows 11 machine without other documentation is item `quick-guide-windows` of the [manual checklist](manual-checklist.md), and no recorded run exists yet.

All commands run in PowerShell 7 (`pwsh`) or Windows PowerShell, as your normal user. Only the prerequisites in the next section may need administrator rights; `sandboxed-agents` itself never does, and never installs or configures Podman, WSL2, or OpenSSH for you.

## Before you start

You need Windows 11 x64 and:

- **Podman 4.7.0 or newer**, as client and as machine, with a running, rootless **WSL2** Podman machine. [Host prerequisites](host-prerequisites.md) gives the floors and the reasons. The executable works on the machine marked as default, or else the only one, and never starts a machine.
- **OpenSSH client** (`ssh` and `ssh-keygen`).
- **Optional:** the GitHub CLI, `gh`, to verify the build attestation. See [Releases](releases.md#verify-a-download). Without it, step 3 compares checksums only.

If one of them is missing, install it yourself; the executable does not. For Podman, follow the official [Podman for Windows tutorial](https://github.com/podman-container-tools/podman/blob/main/docs/tutorials/podman-for-windows.md): install the amd64 MSI from the official Podman releases, choose the WSL provider, run `wsl --install` from an administrator terminal if WSL is not present (it may need a restart), then create and start the machine in a normal terminal with `podman machine init` and `podman machine start`. The tutorial describes a rootless machine as the default. The version floor of this project is 4.7.0 ([host prerequisites](host-prerequisites.md)), whatever version the tutorial's text assumes. If `ssh` is missing, enable the optional Windows feature **OpenSSH Client**.

Check the result:

```text
podman --version
podman machine list
ssh -V
```

`podman machine list` must show a running machine marked as the default (a `*` after its name), or exactly one machine, which must be running even if it has no default mark.

## 1. Choose a release tag

A release is identified by its tag. The tag names below are placeholders: open the [releases page](https://github.com/grauzone-labs/sandboxed-agents/releases) of `grauzone-labs/sandboxed-agents` and use the tag of a release that is there. A preview tag looks like `v1.0.0-preview.YYYYMMDD.N`, and a stable tag like `v1.0.0`. This guide does not claim that any particular release exists; see [Releases](releases.md#status) for the current state. Put the tag you chose in a variable, replacing the placeholder:

```text
$Tag = 'v1.0.0-preview.YYYYMMDD.N'
```

## 2. Download

Download the Windows binary and the checksum file into a directory that does not exist yet. The block below stops at the first problem: it refuses a directory that is already there, and it does not start the next download after a failed one. Paste all of it at once.

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

If the block prints an error, **stop**. Do not go on to step 3, and do not use the files that are there. Open a new terminal, set `$Tag` again, and run the block again with a different, unused directory name, for example `sandboxed-agents-download-2` in the `$Dir` line. Never reuse a directory from a failed attempt, because the checksum check in step 3 could then look at old files.

## 3. Verify

Compare the checksum. `SHA256SUMS` lists both executables, so the command picks the line of the Windows file:

```text
$Expected = ((Get-Content SHA256SUMS | Where-Object { $_ -like '*sandboxed-agents-windows-amd64.exe' }) -split '\s+')[0].ToLower()
$Actual = (Get-FileHash sandboxed-agents-windows-amd64.exe -Algorithm SHA256).Hash.ToLower()
if ($Actual -eq $Expected) { 'checksum matches' } else { throw 'checksum differs: do not run this file' }
```

It must print `checksum matches`. A checksum shows that the file matches the checksum file. It does not show who built it.

If you have `gh`, also verify the build attestation of both files. The command must report a successful verification:

```text
gh attestation verify sandboxed-agents-windows-amd64.exe --repo grauzone-labs/sandboxed-agents
gh attestation verify SHA256SUMS --repo grauzone-labs/sandboxed-agents
```

Stop here if either check fails, and do not run the file.

## 4. Install

Copy the binary to the per-user program directory that the NuGet installer uses as well. The executable treats this directory as a protected host path, so a workspace that contains it is refused:

```text
$Dir = Join-Path $env:LOCALAPPDATA 'Programs\sandboxed-agents'
New-Item -ItemType Directory -Force $Dir | Out-Null
Copy-Item sandboxed-agents-windows-amd64.exe (Join-Path $Dir 'sandboxed-agents.exe')
$env:Path = "$Dir;$env:Path"
```

The last line changes `PATH` for the current terminal only. To keep it in new terminals, add `%LOCALAPPDATA%\Programs\sandboxed-agents` to the user `Path` variable yourself in the Environment Variables dialog, which this command opens:

```text
rundll32 sysdm.cpl,EditEnvironmentVariables
```

Edit the user `Path` there and keep its existing entries as they are. Scripts that rewrite `Path` can expand or retype existing entries; the [NuGet installer](nuget-package.md) avoids that, so use it if you prefer an automated change.

## 5. Check the host

```text
sandboxed-agents version
sandboxed-agents check
```

`version` prints the version and an `asset-sha256` line. `check` reports every missing host prerequisite, including the Podman machine, and exits non-zero when one is missing. The executable only reads your host. Fix the problem with the Podman and Windows tools and run `check` again.

## 6. Create a sandbox

```text
sandboxed-agents up agent01
```

The first `up` builds the base image, which takes a while and needs network access. It creates the sandbox `agent01` with a workspace volume of its own, starts it, and publishes SSH on `127.0.0.1` only. Choose toolchains with `--with`, for example `--with dotnet`; the [command reference](command-reference.md) lists them. To use an existing host directory as the workspace instead, give `up` that directory as a second argument. It must lie under the machine's default WSL automount root; a custom root is refused.

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

The command needs an interactive terminal. It prints a URL; open it in a browser on Windows, sign in, and follow the agent's prompts. The sandbox has no browser and publishes nothing but SSH. The login is stored in the sandbox's home volume and survives `stop`, `start`, and `update`. [Agents](agents.md#sign-in-to-an-agent) lists the login workflows of the other agents.

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

When a newer executable is installed, `update` moves the sandbox to its image and keeps the volumes, the port, the limits, the toolchain set, the agent selection, and the credentials. It refuses while an agent session runs, in the same way as `stop`, unless you pass `--force`. Close any running `sandboxed-agents.exe` before you replace the file, because Windows cannot replace an executable in use:

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
