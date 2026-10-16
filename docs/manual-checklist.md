# Manual checklist

This checklist covers what no automated suite can reach: real logins with real accounts, a real editor, installs on clean machines, the quick guides followed by a person, and behavior that spans several features on a real host. It is the third release gate ([Releases](releases.md#gates)). Each item states what to do and what to observe.

**Nothing in this document has been run.** No manual run is recorded in the repository, no preview exists to run it on, and so no item counts as passed. The steps and expected observations come from the documented behavior of the executable and its offline tests. If an observation differs, the item fails and the defect is filed as its own issue.

## Manual checks and release evidence are different things

- A **manual check** is what a person observed on a real machine while following an item below. The offline suite replaces Podman, SSH, and the agents with fakes, so an offline result is never a manual check, and it does not show that the user namespace, the `no-new-privileges` option, the network, the mounts, or the limits isolate a sandbox. Only runs against real Podman can show that.
- The **live suite** is a separate gate with its own records. This checklist does not repeat what the live suite covers for agents: installing an agent through npm, the installed version in `agents status`, a session that keeps running after detaching, `--stop`, and disabling. The live suite signs in nowhere, so the logins are here.
- **Release evidence** is the record described under [The record](#the-record): one file for one run on one preview commit, uploaded to that preview's GitHub prerelease. A run that has not happened produces no record, and a record is never edited to show a pass that was not observed.

## Safety rules

These steps create, update, and delete sandboxes, install software, and edit SSH and `PATH` settings. Follow these rules for every item:

1. **Never run on a host whose state you care about.** Use a disposable virtual machine, or a dedicated operating-system account on a test machine. Rootless Podman keeps its images and containers per user account, and the SSH setup edits `~/.ssh/config` of the account it runs in. The clean-machine items need a machine that has never had `sandboxed-agents`.
2. **Use a dedicated controller group, never `default`.** Export `SANDBOXED_AGENTS_GROUP=manual-check` before the first command, in every terminal you use. `manual-check` matches the group pattern `^[a-z0-9][a-z0-9-]*$`. Its containers are named `sandboxed-agents.manual-check.NAME`, and its SSH host entries `NAME.manual-check`.

   ```sh
   export SANDBOXED_AGENTS_GROUP=manual-check
   sh -c 'echo "group: $SANDBOXED_AGENTS_GROUP"'
   ```

   ```powershell
   $env:SANDBOXED_AGENTS_GROUP = 'manual-check'
   pwsh -NoProfile -Command '$env:SANDBOXED_AGENTS_GROUP'
   ```

   The second line of each block starts a child process and must print `manual-check`; that proves the variable is exported and reaches `sandboxed-agents`. `sandboxed-agents list` must then show no sandbox you did not create for this run. If it does, or the child prints nothing, stop.
3. **Images are shared.** An image name contains no controller group, so every group of the same executable uses the same images. Run `build` only where an item calls for it, and do not use the machine for other work that depends on `sandboxed-agents` images.
4. **Name sandboxes as the items do** (`chk-persist`, `chk-vs`, `chk-base`, `chk-native`, `chk-int`, and the guide's `agent01`). Each item says when its sandbox is removed with `remove NAME --volumes`. `chk-persist` lives through several items and is removed last.
5. **Use accounts you can revoke.** Revoke what you granted only at the end, in [Final cleanup](#final-cleanup), because the earlier items need the logins to still exist.
6. **Never write a token, one-time code, login URL, or account secret into notes or the record.** The record has no field for them, and nothing may be added.
7. **Create no release tag and publish nothing.** This checklist only reads a preview that already exists. Uploading the record is a separate maintainer step ([Upload](#upload)).

## Setup

Run the whole checklist for one **preview commit**: the full 40-character SHA of the commit that a preview tag (`vX.Y.Z-preview.YYYYMMDD.N`) points to, which is also the commit recorded in the record. Take the executables from that preview's prerelease and verify them as in the [quick guides](quick-guide-linux.md#3-verify). Check that `sandboxed-agents version` prints the preview's version and an `asset-sha256` equal to the one the other platform's build prints.

Platforms are `linux` (Linux amd64, rootless Podman 4.4.0 or newer) and `windows-11` (Windows 11 x64, Podman 4.7.0 or newer with a rootless WSL2 machine); see [host prerequisites](host-prerequisites.md). Each item says where it runs. For an item that allows either platform, the record names the one you used.

## Order of the run

Several items share one long-lived sandbox, `chk-persist`, so that a login done early is still there when the update is observed. Run them in this order:

1. [Create `chk-persist`](#the-shared-sandbox-chk-persist).
2. The logins, all in `chk-persist`: [agent logins](#agent-logins), [GitHub](#github-login), [Azure device code](#azure-login-device-code), [Azure browser](#azure-login-browser), [Azure DevOps](#azure-devops-token).
3. [Persistence and update](#persistence-and-update): the baseline round, then `stop` and `start`, then `update`, with the same observations each time.
4. [No-terminal refusals](#toolchain-requirement-and-no-terminal-refusals), which also use `chk-persist`.
5. The independent items, each with a sandbox it removes itself: [VS Code Remote SSH](#vs-code-remote-ssh), [an interrupted update](#an-interrupted-update), the [toolchain requirement](#toolchain-requirement-and-no-terminal-refusals), the [quick guides](#quick-guides), and the [installs](#installers-issue-no-podman-call) on clean machines.
6. [No image removal](#no-command-removes-an-image) and [release files](#release-files-and-attestations).
7. [Final cleanup](#final-cleanup): the destructive removal of `chk-persist` and every revocation.

## Check identifiers

Each item has a fixed identifier. The identifier is the `name` of its check in the record. Use these identifiers exactly and no others; they never contain a value taken from the host or from user input.

| Identifier | Item | Platform |
|---|---|---|
| `agent-login-copilot` | [Agent login: copilot](#agent-logins) | either |
| `agent-login-claude` | [Agent login: claude](#agent-logins) | either |
| `agent-login-codex` | [Agent login: codex](#agent-logins) | either |
| `agent-login-opencode` | [Agent login: opencode](#agent-logins) | either |
| `github-login` | [GitHub login](#github-login) | either |
| `azure-login-device-code` | [Azure login, device code](#azure-login-device-code) | either |
| `azure-login-browser` | [Azure login, browser](#azure-login-browser) | either |
| `azdo-token` | [Azure DevOps token](#azure-devops-token) | either |
| `vscode-remote-ssh` | [VS Code Remote SSH](#vs-code-remote-ssh) | either |
| `npm-install-clean` | [npm install on a clean machine](#npm-install-on-a-clean-machine) | either |
| `nuget-install-clean` | [NuGet install on a clean machine](#nuget-install-on-a-clean-machine) | `windows-11` |
| `quick-guide-linux` | [Linux quick guide](#quick-guides) | `linux` |
| `quick-guide-windows` | [Windows quick guide](#quick-guides) | `windows-11` |
| `selection-pins-persist` | [Selection and pins survive](#persistence-and-update) | either |
| `git-identity-persist` | [Git identity survives](#persistence-and-update) | either |
| `credentials-persist` | [Credentials survive](#persistence-and-update) | either |
| `update-complete` | [A complete update](#persistence-and-update) | either |
| `update-interrupted` | [An interrupted update](#an-interrupted-update) | either |
| `azure-toolchain-required` | [Toolchain requirement](#toolchain-requirement-and-no-terminal-refusals) | either |
| `login-needs-terminal` | [No-terminal refusal](#toolchain-requirement-and-no-terminal-refusals) | either |
| `no-image-removal` | [No command removes an image](#no-command-removes-an-image) | either |
| `installers-no-podman` | [Installers issue no Podman call](#installers-issue-no-podman-call) | either |
| `preview-attestation` | [Release files and attestations](#release-files-and-attestations) | either |

## Items

### The shared sandbox `chk-persist`

Create it with the `azure` toolchain, which the Azure and Azure DevOps logins need, and with `opencode` enabled. Install no SSH setup yet; the Azure browser item needs the sandbox without one first:

```sh
sandboxed-agents up chk-persist --with azure --agents opencode
sandboxed-agents list
```

The first `up` builds the images, which takes a while. For the update observations later, create `chk-persist` with the **previous** preview's executable if one exists, and use the current one from [Persistence and update](#persistence-and-update) on. The logins below work with either.

### Agent logins

One login per agent of the catalog, with a real account, ending in a signed-in agent. Four items, one per identifier. The live suite signs in nowhere, so this is the only login check. All four agents are enabled in `chk-persist` and stay signed in until the final cleanup.

| Agent | Workflow | Account you need |
|---|---|---|
| `copilot` | `device-code` | GitHub account with Copilot access |
| `claude` | `browser` | Anthropic account |
| `codex` | `device-code` | OpenAI account |
| `opencode` | `provider` | An account or key for one model provider |

For each `AGENT` and its workflow (`opencode` is already enabled):

1. `sandboxed-agents agents enable chk-persist AGENT`
2. Start `sandboxed-agents agents login chk-persist AGENT WORKFLOW` in an interactive terminal, and cancel it once with `Ctrl+C`. **Expected:** non-zero status and `agents login failed`.
3. Run the same command again, open the URL or enter the code that the agent shows in a browser on your machine, and sign in. **Expected:** the agent's own success message and exit status zero.
4. Run `sandboxed-agents agents session chk-persist AGENT`, send a short prompt such as `say hello`, and detach with `Ctrl+b` then `d`. **Expected:** an answer to the prompt and no sign-in prompt. `agents status chk-persist AGENT` shows `signed in unknown`, because no delivered agent has a status probe; the answer is the evidence of the login.
5. End the session: `sandboxed-agents agents session chk-persist AGENT --stop`.

Record the result per agent. If one agent passes and another fails, record the failure for the failing identifier only.

### GitHub login

Identifier `github-login`. Use a test account and a private test repository. This item follows [Manual GitHub login check](integrations.md#manual-github-login-check) in `chk-persist`.

1. Run `sandboxed-agents integrations login chk-persist github`, wait for the code, and press `Ctrl+C`. **Expected:** non-zero status, and `gh auth status` in `sandboxed-agents shell chk-persist` shows no login.
2. Run the command again and complete the device flow in your browser. **Expected:** exit status zero, no error.
3. In `shell chk-persist`, `gh auth status` shows `github.com` logged in, and `GIT_TERMINAL_PROMPT=0 git clone https://github.com/OWNER/REPO.git /workspace/gh-0` clones the private repository without a prompt.
4. In `/workspace/gh-0`, commit on a temporary branch and run `GIT_TERMINAL_PROMPT=0 git push -u origin HEAD`. **Expected:** the push succeeds without a prompt. Delete the remote branch.

The login stays until [Final cleanup](#final-cleanup).

### Azure login, device code

Identifier `azure-login-device-code`. Follow [Manual Azure login check](integrations.md#manual-azure-login-check), steps 1 to 4, in `chk-persist`, which has the `azure` toolchain.

1. Make sure no Azure login exists: in `shell chk-persist`, `az account show` reports no login. Run `sandboxed-agents integrations login chk-persist azure device-code`, wait for the code, and press `Ctrl+C`. **Expected:** non-zero status, no account printed, and `az account show` still reports no login.
2. Run it again and complete the device flow. **Expected:** exit status zero, and the output ends with the account and subscription that the Azure CLI selected. Note the account name.
3. In `shell chk-persist`, `az account show` prints the same account.

Do not run `az logout` here; the login must still exist for the persistence observations. The next item logs out first on purpose.

### Azure login, browser

Identifier `azure-login-browser`. Follow steps 5 to 7 of [Manual Azure login check](integrations.md#manual-azure-login-check). The earlier device-code login is a valid login, so start by removing it, and keep the sandbox without an SSH setup until step 2.

1. In `shell chk-persist`, run `az logout`; `az account show` must report no login. Then, with no SSH setup installed, run `sandboxed-agents integrations login chk-persist azure browser`. **Expected:** it fails, names `ssh-config chk-persist --install`, and starts no `az`.
2. Run `sandboxed-agents ssh-config chk-persist --install`, then the browser workflow again. **Expected:** an `ssh -N -L ...` line and a URL appear before anything waits; the alias matches the host entry `chk-persist.manual-check`.
3. Open the URL but stop the tunnel before signing in, then press `Ctrl+C` in the first terminal. **Expected:** non-zero status, no account printed, and `az account show` reports no login (there was none to begin with).
4. Run the workflow once more, run the tunnel on the machine whose browser you use, open the URL, and sign in. **Expected:** exit status zero and the account is printed. Stop the tunnel; `az account show` still works in the shell.

The SSH setup and the Azure login stay until [Final cleanup](#final-cleanup).

### Azure DevOps token

Identifier `azdo-token`. Follow [Manual Azure DevOps login check](integrations.md#manual-azure-devops-login-check) in `chk-persist`, with a personal access token that has `Code (Read)` and `Project and Team (Read)` for a test organization with a private test repository.

1. Run `sandboxed-agents integrations login chk-persist azdo --organization https://dev.azure.com/ORGANIZATION` with standard input from a terminal. **Expected:** a prompt; nothing is shown while you paste the token; exit status zero.
2. In `shell chk-persist`, `GIT_TERMINAL_PROMPT=0 git clone https://dev.azure.com/ORGANIZATION/PROJECT/_git/REPO /workspace/azdo-0` succeeds, and `az devops project list --organization https://dev.azure.com/ORGANIZATION --output table` lists projects.
3. **Expected:** the token is in no shell history of the host, and not in the arguments of the `podman` process while the command ran.

The token stays until [Final cleanup](#final-cleanup).

### Persistence and update

Identifiers `selection-pins-persist`, `git-identity-persist`, `credentials-persist`, and `update-complete`. They run in `chk-persist`, after all five login items, so that the real agent, GitHub, Azure, and Azure DevOps logins are present when the lifecycle commands run. A fake Git credential supplements the real ones but does not prove that they survived; the real logins do.

**Setup, once:**

1. Note the installed version `V` of `opencode` from `sandboxed-agents agents status chk-persist opencode`, and pin it: `sandboxed-agents agents enable chk-persist opencode --version V`.
2. `sandboxed-agents integrations config chk-persist git identity --name "Manual Check" --email "manual-check@example.invalid"`
3. `sandboxed-agents integrations config chk-persist git credentials`
4. In `sandboxed-agents shell chk-persist`, store a fake credential:

   ```sh
   printf 'protocol=https\nhost=example.invalid\nusername=chk\npassword=not-a-secret\n' | git credential approve
   ```
5. Record the configuration the update must keep (add `--connection MACHINE` before `inspect` on Windows, with the name of the default Podman machine):

   ```sh
   podman inspect --format '{{.Image}} {{.HostConfig.Memory}} {{.HostConfig.NanoCpus}} {{.HostConfig.PidsLimit}} {{.HostConfig.ShmSize}} {{index .Config.Labels "io.sandboxed-agents.toolchains"}} {{index .Config.Labels "io.sandboxed-agents.port"}}' sandboxed-agents.manual-check.chk-persist
   podman inspect --format '{{range .Mounts}}{{.Name}} {{.Destination}};{{end}}' sandboxed-agents.manual-check.chk-persist
   ```

**The observation set.** Run it three times, as **round 0** (now), **round 1** (after `stop` and `start`), and **round 2** (after `update`). `N` is the round number. Every commit is made fresh in that round, in a fresh repository, so no old commit is read:

1. For each of `copilot`, `claude`, `codex`, and `opencode`: `agents status chk-persist AGENT` shows the agent enabled; `agents session chk-persist AGENT` answers a prompt such as `say hello` with no sign-in prompt; detach, then `agents session chk-persist AGENT --stop`. A running session would make `stop` and `update` refuse, so none may be left running.
2. `agents status chk-persist opencode` shows version `V` and the pin.
3. In `shell chk-persist`: `gh auth status` shows `github.com` logged in, and `GIT_TERMINAL_PROMPT=0 git clone https://github.com/OWNER/REPO.git /workspace/gh-N` succeeds without a prompt (round 0 reuses `/workspace/gh-0`).
4. In `shell chk-persist`: `az account show` prints the account noted at login, and `GIT_TERMINAL_PROMPT=0 git clone https://dev.azure.com/ORGANIZATION/PROJECT/_git/REPO /workspace/azdo-N` succeeds without a prompt, and `az devops project list --organization https://dev.azure.com/ORGANIZATION --output table` lists projects.
5. In `shell chk-persist`, make two new commits in this round, one in a new repository and one in the GitHub clone, and print each author:

   ```sh
   git init /workspace/repo-N && git -C /workspace/repo-N commit --allow-empty -m round-N
   git -C /workspace/repo-N log -1 --format='%an <%ae> %s'
   git -C /workspace/gh-N commit --allow-empty -m round-N
   git -C /workspace/gh-N log -1 --format='%an <%ae> %s'
   ```
6. `grep -c example.invalid /home/agent/.git-credentials` prints `1`.

**Expected, in every round:** step 1 to 4 show the agents and the three integrations still signed in, with no sign-in prompt; step 2 shows version `V` pinned; step 5 prints `Manual Check <manual-check@example.invalid> round-N` for both commits; step 6 prints `1`.

**The sequence:**

1. Round 0: the observation set, as the baseline.
2. `sandboxed-agents stop chk-persist`, then `sandboxed-agents start chk-persist`. Round 1.
3. Update. The sandbox needs an older image than the installed executable. Prefer to have created `chk-persist` with the previous preview's executable, then install the current one and run `sandboxed-agents update chk-persist`. If no earlier executable exists, run `sandboxed-agents update chk-persist --with azure,native` instead. That changes the toolchain set on purpose, keeps `azure`, is slower, and makes the toolchain observation below expect `azure,native`. Before the update, record the image IDs for the [image check](#no-command-removes-an-image): `podman images --all --format '{{.ID}} {{.Repository}}:{{.Tag}}'`.
4. Round 2.
5. Repeat the `podman inspect` commands of the setup.
6. Run `ssh chk-persist.manual-check true`.

**Expected after the update (`update-complete`):** the update ends with exit status zero; the image ID differs from the one recorded; the volumes (the mount names), the port, the memory, CPU, process, and shared-memory limits, the workspace, and the toolchain set (or the one `--with` named) are the same as recorded; the agent selection and the credentials are the same, as round 2 shows; and `ssh chk-persist.manual-check true` connects without a host key prompt.

**Identifiers:** `selection-pins-persist` passes on steps 1 and 2 of all three rounds; `git-identity-persist` on step 5 of all three; `credentials-persist` on steps 1, 3, 4, and 6 of all three; and, for the removal half of `credentials-persist`, on [Final cleanup](#final-cleanup); `update-complete` on the expected observations after the update.

### Toolchain requirement and no-terminal refusals

Identifiers `login-needs-terminal` and `azure-toolchain-required`. The first runs in `chk-persist` (which has the `azure` toolchain and an SSH setup), after the persistence rounds. The second uses two short-lived sandboxes.

For `login-needs-terminal`, take standard input from nothing. None of the three may change the sandbox:

```sh
sandboxed-agents integrations login chk-persist github </dev/null
sandboxed-agents integrations login chk-persist azure device-code </dev/null
sandboxed-agents integrations login chk-persist azure browser </dev/null
```

```powershell
cmd /c "sandboxed-agents integrations login chk-persist github < NUL"
cmd /c "sandboxed-agents integrations login chk-persist azure device-code < NUL"
cmd /c "sandboxed-agents integrations login chk-persist azure browser < NUL"
```

**Expected:** each exits non-zero with a message that the login needs an interactive terminal, and starts no `gh` or `az` process in the sandbox. Afterwards `gh auth status` and `az account show` in `shell chk-persist` still show the earlier logins.

For `azure-toolchain-required`, create two sandboxes without the `azure` toolchain:

```sh
sandboxed-agents up chk-base
sandboxed-agents up chk-native --with native
```

For each, run the three commands below with an organization URL that does not exist (replace `SANDBOX` with `chk-base`, then `chk-native`):

```sh
sandboxed-agents integrations login SANDBOX azure device-code
sandboxed-agents integrations login SANDBOX azure browser
sandboxed-agents integrations login SANDBOX azdo --organization https://dev.azure.com/example-organization
```

**Expected:** each fails non-zero before anything is stored and before a token is read; the message names the `azure` toolchain and prints the complete command: `update chk-base --with azure` for `chk-base`, and `update chk-native --with azure,native` for `chk-native`. The printed set never contains `none`. Nothing changes: `sandboxed-agents list` and `podman inspect` of the sandbox are the same before and after, and `git config --global --get-regexp '^credential\.'` in `shell` prints nothing. Remove both when done:

```sh
sandboxed-agents remove chk-base --volumes
sandboxed-agents remove chk-native --volumes
```

### VS Code Remote SSH

Identifier `vscode-remote-ssh`. Follow [SSH access](ssh.md#connect-with-vs-code-remote-ssh) with VS Code and its Remote - SSH extension installed, in a sandbox of its own.

1. `sandboxed-agents up chk-vs --ssh-config`.
2. In VS Code, run **Remote-SSH: Connect to Host...** and choose `chk-vs.manual-check`. Open the folder `/workspace`.
3. Create a file in the editor and run `whoami` in its integrated terminal.

**Expected:** the connection opens with no host key prompt; `whoami` prints `agent`; the file created in the editor appears in `sandboxed-agents shell chk-vs` under `/workspace`. Then close the VS Code window and clean up:

```sh
sandboxed-agents ssh-config chk-vs --remove
sandboxed-agents remove chk-vs --volumes
```

### An interrupted update

Identifier `update-interrupted`. This needs a sandbox `chk-int` that an `update` can replace: create it with the previous preview's executable and install the current one, or use `update chk-int --with native` as the replacement in the steps below. The goal is to stop the `update` process after the old container was renamed to the backup and before the update finished, killing **only that process**.

1. Create it: `sandboxed-agents up chk-int`.
2. Start the update in the background and keep its process ID, poll for the backup for at most two minutes, and kill that one process ID:

   ```sh
   sandboxed-agents update chk-int & UPDATE_PID=$!
   FOUND=no
   for i in $(seq 1 600); do
     if podman container exists sandboxed-agents-backup.manual-check.chk-int; then FOUND=yes; break; fi
     kill -0 "$UPDATE_PID" 2>/dev/null || break
     sleep 0.2
   done
   if [ "$FOUND" = yes ]; then kill -KILL "$UPDATE_PID"; else echo 'backup not seen; repeat with a fresh sandbox'; fi
   wait "$UPDATE_PID"
   ```

   ```powershell
   $Update = Start-Process sandboxed-agents -ArgumentList 'update', 'chk-int' -PassThru -NoNewWindow
   $Deadline = (Get-Date).AddSeconds(120)
   $Found = $false
   while ((Get-Date) -lt $Deadline -and -not $Update.HasExited) {
     $State = sandboxed-agents list 2>&1 | Out-String
     if ($State -match '(?m)chk-int.*update interrupted') { $Found = $true; break }
     Start-Sleep -Milliseconds 500
   }
   if ($Found -and -not $Update.HasExited) { Stop-Process -Id $Update.Id -Force } else { 'backup not seen; repeat with a fresh sandbox' }
   ```

   The commands name the process ID of the one `update` they started and nothing else. They never match processes by name. Use `--with native` after `update chk-int` in both blocks if you take that route. A `podman` call that was already running when the process was killed may still finish; that is part of a real interruption. If the update finished before the kill, or the backup never appears, the item cannot be observed that time: repeat with a fresh sandbox, and it does not pass until it is observed.
3. Run `sandboxed-agents check chk-int`, `sandboxed-agents list`, `sandboxed-agents start chk-int`, and `sandboxed-agents shell chk-int`.
4. Run `sandboxed-agents update chk-int`.

**Expected:**

- `check chk-int` reports the leftover backup container, exits non-zero, and changes nothing (`podman ps --all` is the same before and after).
- `list` shows the sandbox as `update interrupted`.
- Every other command except `update chk-int` refuses and names `update chk-int`.
- `update chk-int` recovers instead of updating, builds no image, and exits as the state determines: zero when the replacement is ready and the update completes; non-zero when it restores the old sandbox ("restored" in its output). Either way the volumes are unchanged, no backup remains, and the next `update chk-int` updates normally. See [Updating a sandbox](images.md#updating-a-sandbox) for the cases.

Clean up: `sandboxed-agents remove chk-int --volumes`.

### Quick guides

Identifiers `quick-guide-linux` (platform `linux`) and `quick-guide-windows` (platform `windows-11`). Follow the guide from its first step to a signed-in agent session on a clean machine, **using only that guide**. If you needed another document, a command did not work as written, or a step was unclear, the item fails.

- [Linux quick guide](quick-guide-linux.md) on a clean Linux machine with Podman and OpenSSH installed as the guide lists. The tag is the preview's.
- [Windows quick guide](quick-guide-windows.md) on a clean Windows 11 machine.

Export the dedicated group before the first `sandboxed-agents` command of the guide, as in the [safety rules](#safety-rules). **Expected:** every command works as written, including download, checksum comparison, attestation verification, placing the binary on `PATH`, `check`, `up`, `agents enable`, `agents login`, and `agents session`, and the final session shows an answered prompt without a sign-in prompt. At the end, remove the guide's sandbox: `sandboxed-agents remove agent01 --volumes`.

### npm install on a clean machine

Identifier `npm-install-clean`. Use a machine on which `sandboxed-agents` has never been installed, with Node.js 18 or newer, Podman, and OpenSSH. Download `sandboxed-agents-VERSION.tgz` from the preview prerelease. The npm registry holds no package from this repository, so the install uses the file ([npm package](npm-package.md#install)). Use the contained install commands of [the install check](#installers-issue-no-podman-call), so that npm writes its prefix, cache, temporary files, and logs only under the install root.

1. Install the tarball with the contained commands.
2. Run the installed command: `sandboxed-agents version`, `sandboxed-agents check`, and `sandboxed-agents list`.
3. Unpack a copy of the `.tgz`, change one byte of an executable in it, repack it as `tampered.tgz`, and install that into a second prefix under the install root with the same contained commands.
4. Remove the package with `npm uninstall -g` and the same `--prefix`.

**Expected:** step 2 prints the preview's version and `asset-sha256`, reports the host, and runs without a sandbox; step 3 fails the installation on the checksum; after step 4 the command is gone.

### NuGet install on a clean machine

Identifier `nuget-install-clean`, platform `windows-11`. Use a clean Windows 11 machine with PowerShell 7 and no administrator session. Take `SandboxedAgents.VERSION.nupkg` from the preview prerelease and follow [NuGet command package](nuget-package.md#install): extract it into the install root, unblock the scripts, and run `install-command.ps1`.

1. Record the user `Path` as stored: `(Get-Item HKCU:\Environment).GetValue('Path', '', 'DoNotExpandEnvironmentNames')`.
2. Install, open a new terminal, and run `sandboxed-agents version`.
3. Run the installer again (upgrade), then `remove-command.ps1`. Record the stored `Path` after each step.

**Expected:** the installer needs no administrator rights; `version` works in a new terminal; the stored `Path` differs only by the install directory entry, and existing entries, including unexpanded ones such as `%USERPROFILE%\bin`, are byte-for-byte unchanged; after removal the entry is gone and the other entries are unchanged. [The install check](#installers-issue-no-podman-call) adds the file comparison.

### Installers issue no Podman call

Identifier `installers-no-podman`. Install, upgrade, and remove through **each** of three methods, and show that no Podman call happens and that nothing outside the install location and the user `PATH` changes: the downloaded binary, the npm package, and on Windows the NuGet package. Use a machine where a snapshot is meaningful: a fresh disposable virtual machine, with shell history off.

**The install root.** Choose a new, empty directory as the install root, and keep every file the methods write inside it, **including npm's prefix, cache, temporary files, and logs**. Do not exempt any other location. The snapshots live outside the inspected tree, so taking them causes no drift:

```sh
INSTALL_ROOT="$HOME/chk-install"; mkdir -p "$INSTALL_ROOT"/{bin,prefix,cache,logs,tmp}
SNAP="$(mktemp -d /tmp/chk-snapshot.XXXXXX)"
unset HISTFILE
```

```powershell
$InstallRoot = Join-Path $HOME 'chk-install'
New-Item -ItemType Directory -Force $InstallRoot | Out-Null
$Snap = Join-Path $env:SystemDrive "chk-snapshot-$PID"
New-Item -ItemType Directory -Force $Snap | Out-Null
Set-PSReadLineOption -HistorySaveStyle SaveNothing
```

On Windows the snapshot directory lies outside the user profile, so it cannot appear in the comparison.

**A fake Podman that records its calls**, outside the inspected tree, first on `PATH` for the whole item:

```sh
mkdir -p /tmp/chk-fake-podman && printf '#!/bin/sh\necho "$@" >> /tmp/chk-fake-podman/calls\nexit 1\n' > /tmp/chk-fake-podman/podman && chmod +x /tmp/chk-fake-podman/podman
export PATH="/tmp/chk-fake-podman:$PATH"
```

On Windows, create `podman.cmd` in `$env:SystemDrive\chk-fake-podman` that appends `%*` to a `calls` file there and exits with 1, and put that directory first in `$env:Path` for the session.

**The snapshot.** It compares paths and content hashes of everything under the home directory except the install root, plus symbolic links and directories, so a changed, added, or removed file fails the comparison:

```sh
snapshot() {
  find "$HOME" -xdev -path "$INSTALL_ROOT" -prune -o -type f -print0 | sort -z | xargs -0 -r sha256sum > "$SNAP/$1.files"
  find "$HOME" -xdev -path "$INSTALL_ROOT" -prune -o -type l -printf '%p -> %l\n' | sort > "$SNAP/$1.links"
  find "$HOME" -xdev -path "$INSTALL_ROOT" -prune -o -type d -print | sort > "$SNAP/$1.dirs"
}
```

```powershell
function Take-Snapshot($Label) {
  Get-ChildItem $env:USERPROFILE -Recurse -Force -File -ErrorAction SilentlyContinue |
    Where-Object { $_.FullName -notlike "$InstallRoot*" -and $_.FullName -notlike "$env:LOCALAPPDATA\Programs\sandboxed-agents*" } |
    Get-FileHash -Algorithm SHA256 | Sort-Object Path | ForEach-Object { "$($_.Hash)  $($_.Path)" } | Set-Content (Join-Path $Snap "$Label.files")
  (Get-Item HKCU:\Environment).GetValueNames() | Sort-Object | ForEach-Object { "$_ = $((Get-Item HKCU:\Environment).GetValue($_, '', 'DoNotExpandEnvironmentNames'))" } | Set-Content (Join-Path $Snap "$Label.environment")
}
```

**Take a snapshot** (`snapshot before` on Linux, `Take-Snapshot before` on Windows), then run each method, then take `after` snapshots and compare with `diff "$SNAP/before.files" "$SNAP/after.files"` (and `.links`, `.dirs`), or `Compare-Object (Get-Content "$Snap\before.files") (Get-Content "$Snap\after.files")` on Windows.

**Method 1, downloaded binary:** copy the verified binary to `$INSTALL_ROOT/bin/sandboxed-agents` (`$InstallRoot\bin\sandboxed-agents.exe` on Windows), run it by its full path with `version`, copy the next build over it as an upgrade, and delete it.

**Method 2, npm package:** install the tarball with the prefix, cache, logs, and temporary directory all under the install root, and with the update notifier off:

```sh
export TMPDIR="$INSTALL_ROOT/tmp" npm_config_update_notifier=false
npm install --offline --no-audit --no-fund -g --prefix "$INSTALL_ROOT/prefix" --cache "$INSTALL_ROOT/cache" --logs-dir "$INSTALL_ROOT/logs" ./sandboxed-agents-VERSION.tgz
"$INSTALL_ROOT/prefix/bin/sandboxed-agents" version
npm install --offline --no-audit --no-fund -g --prefix "$INSTALL_ROOT/prefix" --cache "$INSTALL_ROOT/cache" --logs-dir "$INSTALL_ROOT/logs" ./sandboxed-agents-VERSION.tgz
npm uninstall -g --prefix "$INSTALL_ROOT/prefix" --cache "$INSTALL_ROOT/cache" --logs-dir "$INSTALL_ROOT/logs" sandboxed-agents
```

```powershell
$env:TEMP = Join-Path $InstallRoot 'tmp'; $env:TMP = $env:TEMP; $env:npm_config_update_notifier = 'false'
npm install --offline --no-audit --no-fund -g --prefix "$InstallRoot\prefix" --cache "$InstallRoot\cache" --logs-dir "$InstallRoot\logs" .\sandboxed-agents-VERSION.tgz
& "$InstallRoot\prefix\sandboxed-agents.cmd" version
npm install --offline --no-audit --no-fund -g --prefix "$InstallRoot\prefix" --cache "$InstallRoot\cache" --logs-dir "$InstallRoot\logs" .\sandboxed-agents-VERSION.tgz
npm uninstall -g --prefix "$InstallRoot\prefix" --cache "$InstallRoot\cache" --logs-dir "$InstallRoot\logs" sandboxed-agents
```

**Method 3, NuGet package (Windows only):** extract the `.nupkg` under `$InstallRoot\package`, then run `install-command.ps1` from its `tools` directory (installing to `%LOCALAPPDATA%\Programs\sandboxed-agents`, the install location of this method, which the snapshot excludes, together with the `Programs` parent directory if the installer has to create it), run it again as an upgrade, and then `remove-command.ps1`. The user `Path` may change by the one install directory entry and nothing else; the `.environment` snapshots show it.

**Expected:** `$env:SystemDrive\chk-fake-podman\calls` or `/tmp/chk-fake-podman/calls` is empty or does not exist, so no Podman call was issued. The `files`, `links`, `dirs`, and `environment` comparisons show **no difference** outside the install root and the install location, except the one user `Path` entry for NuGet. Any other difference fails the item, whatever wrote it. If background activity of the machine itself writes a file during the run, repeat the run on a quiescent machine; do not exempt the file. Sandboxes that exist keep running across the upgrade of any method.

After the item, delete the install root and the snapshot directories.

### No command removes an image

Identifier `no-image-removal`. Compare the images before and after the commands that could remove one.

1. The list recorded just before the `update` in [Persistence and update](#persistence-and-update): `podman images --all --format '{{.ID}} {{.Repository}}:{{.Tag}}'`.
2. After the update, run `sandboxed-agents build` (it rebuilds the shared images and marks sandboxes outdated, which is why it runs only here). All `chk-` sandboxes still existing at that point are `chk-persist` only, since the others were removed after their items.
3. After [Final cleanup](#final-cleanup) has removed `chk-persist` with `--volumes`, list the images again and compare the image IDs.

**Expected:** every image ID from step 1 is still present after step 3; after `build`, the replaced images may have lost their tags but still exist. The README names this as a limitation, and `podman image prune` removes unused images, never one a sandbox still uses.

### Release files and attestations

Identifier `preview-attestation`. This item needs a **tagged preview** and fails while none exists, or while the release process does not produce attestations. No workflow in the repository publishes releases or attestations yet ([Releases](releases.md#status)).

1. `gh release view TAG --repo grauzone-labs/sandboxed-agents --json assets --jq '.assets[].name'` for the preview tag `TAG`.
2. Download every file of the list and, for each, run `gh attestation verify FILE --repo grauzone-labs/sandboxed-agents`.
3. `sha256sum --check --ignore-missing SHA256SUMS` next to the two executables.
4. `git rev-parse "TAG^{commit}"` and compare it with the commit of your run.

**Expected:**

- Step 1 lists `sandboxed-agents-linux-amd64`, `sandboxed-agents-windows-amd64.exe`, `SHA256SUMS`, `sandboxed-agents-VERSION.tgz`, and `SandboxedAgents.VERSION.nupkg`.
- Step 2 reports a successful verification for **each** of the five files.
- Step 3 prints `OK` for both executables.
- Step 4 prints the full commit SHA that goes into the record.

### Final cleanup

Do this last, after every observation above. It is the only place that removes `chk-persist` and the only place that revokes anything.

1. Remove the SSH setup: `sandboxed-agents ssh-config chk-persist --remove`.
2. Remove the sandbox and its volumes: `sandboxed-agents remove chk-persist --volumes`. **Expected:** the three volumes are removed (`podman volume ls` shows none for `chk-persist`).
3. This is the removal half of `credentials-persist`. Create it again, `sandboxed-agents up chk-persist --with azure`, and in `shell chk-persist` check that `/home/agent/.git-credentials` is absent, `git config --global --get user.email` prints nothing, `gh auth status` and `az account show` report no login, and `sandboxed-agents agents status chk-persist opencode` shows the agent not enabled. Then remove it for good: `sandboxed-agents remove chk-persist --volumes`.
4. Compare the images for the [image check](#no-command-removes-an-image).
5. Revoke what you granted: remove the GitHub device authorization in your GitHub settings, revoke the Azure sign-in sessions for the test account, delete the Azure DevOps personal access token, and sign out of the four agents' accounts or revoke their sessions at each provider.
6. `sandboxed-agents list` must show no sandbox of the group, and `podman ps --all` no `sandboxed-agents.manual-check.` container.

## The record

One full run on one preview commit is recorded as one file named `manual-checklist.json`. It is one JSON object encoded in UTF-8 with exactly these fields, and no others:

| Field | Type | Value |
|---|---|---|
| `schema_version` | number | `1` |
| `kind` | string | `manual-checklist` |
| `commit` | string | The full commit SHA of the preview: 40 lowercase hexadecimal characters. |
| `result` | string | `pass` or `fail`. |
| `checks` | array | One object per check. |

Each object of `checks` has exactly these fields, and no others:

| Field | Type | Value |
|---|---|---|
| `name` | string | One of the identifiers in [Check identifiers](#check-identifiers). |
| `result` | string | `pass` or `fail`. |
| `date` | string | The date the check was run, as `YYYY-MM-DD`. |
| `platform` | string | The platform the check was run on: `linux` or `windows-11`. |

Rules:

- **A missing or an additional field is rejected**, in the record and in a check. No field can hold a note, a name, a host, a URL, or a token.
- The top-level `result` is `pass` only when the record has one check for each of the 23 identifiers and every check passed. In every other case it is `fail`. (The stable release check reads the top-level `result`, the shape of the record, and the commit; the completeness of the list is this checklist's rule, not something the check enforces.)
- A check is recorded only after you ran it. A check you have not run has no entry. A record in progress, or one with any failed check, has the top-level `result` `fail`. An item counts as passed only after a rerun on the commit under test shows it passing; a defect found during the run is filed as its own issue.
- The record marks every item as checked by hand. Nothing in it comes from the offline suite or the live suite.
- **The repository holds no passing record, and none may be committed.** A record exists only as an upload to a preview prerelease.

The shape, with placeholders that are not values to copy (`FULL_COMMIT_SHA` stands for 40 lowercase hexadecimal characters, `YYYY-MM-DD` for the day you ran the check), and an unfinished run:

```json
{
  "schema_version": 1,
  "kind": "manual-checklist",
  "commit": "FULL_COMMIT_SHA",
  "result": "fail",
  "checks": [
    { "name": "github-login", "result": "fail", "date": "YYYY-MM-DD", "platform": "linux" }
  ]
}
```

Before you upload, check that the file has exactly these fields and holds no credential. With `jq`:

```sh
jq -e '(keys == ["checks","commit","kind","result","schema_version"]) and .schema_version == 1 and .kind == "manual-checklist" and (.commit | test("^[0-9a-f]{40}$")) and (.checks | length > 0) and all(.checks[]; (keys == ["date","name","platform","result"]) and (.result == "pass" or .result == "fail") and (.platform == "linux" or .platform == "windows-11") and (.date | test("^[0-9]{4}-[0-9]{2}-[0-9]{2}$")))' manual-checklist.json
```

Open the file and read it once before upload. It must contain nothing but the fields above.

### Upload

Uploading is a maintainer step on the preview's GitHub prerelease, where the stable release check reads the record. It is outward-facing, so do it only for a record you have read and only for a preview you ran against:

```sh
gh release upload TAG manual-checklist.json --repo grauzone-labs/sandboxed-agents
```

The stable release check accepts the record only when `schema_version` is `1`, `kind` is `manual-checklist`, `commit` is the commit of the release, `result` is `pass`, and every check holds `name`, `result`, `date`, and `platform`. See [Releases](releases.md#validation-records).
