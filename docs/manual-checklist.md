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
3. **Images are shared.** An image name contains no controller group, so every group of the same executable uses the same images. Run `build` only where an item calls for it (once, in [Persistence and update](#persistence-and-update)), and do not use the machine for other work that depends on `sandboxed-agents` images.
4. **Name sandboxes as the items do** (`chk-persist`, `chk-vs`, `chk-base`, `chk-native`, `chk-int`, and the guide's `agent01`). Each item says when its sandbox is removed with `remove NAME --volumes`. `chk-persist` lives through several items and is removed last.
5. **Use accounts you can revoke.** Revoke what you granted only at the end, in [Final cleanup](#final-cleanup), because the earlier items need the logins to still exist.
6. **Never write a token, one-time code, login URL, or account secret into notes or the record.** The record has no field for them, and nothing may be added.
7. **Create no release tag and publish nothing.** This checklist only reads a preview that already exists. Uploading the record is a separate maintainer step ([Upload](#upload)).

## Setup

Run the whole checklist for one **preview commit**: the full 40-character SHA of the commit that a preview tag (`vX.Y.Z-preview.YYYYMMDD.N`) points to, which is also the commit recorded in the record. Take the executables from that preview's prerelease and verify them as in the [quick guides](quick-guide-linux.md#3-verify). Check that `sandboxed-agents version` prints the preview's version and an `asset-sha256` equal to the one the other platform's build prints.

Platforms are `linux` (Linux amd64, rootless Podman 4.4.0 or newer) and `windows-11` (Windows 11 x64, Podman 4.7.0 or newer with a rootless WSL2 machine); see [host prerequisites](host-prerequisites.md). Each item says where it runs. For an item that allows either platform, the record names the one you used.

### Podman on Windows, and capturing state

Some items compare Podman and `sandboxed-agents` state before and after a command, and some call `podman` directly. On Windows every direct `podman` call in this document names the Podman machine the executable works on, with `--connection`. Find the machine whose `Default` column is `true` and use it through a helper, so that the calls cannot reach another machine; on Linux call `podman` as written:

```powershell
podman machine list --format '{{.Name}} {{.Default}}'
$Machine = 'MACHINE_NAME'   # the name of the machine with Default true, without a trailing *
function pm { podman --connection $Machine @args }
```

Wherever a block below writes `podman ...` for Linux, write `pm ...` on Windows. Capture the state an item needs to see unchanged with these helpers, which write one file each. The files go in a directory outside the install root (any scratch directory):

```sh
capture_state() {
  out="$1"; shift
  { sandboxed-agents list 2>&1
    podman ps --all --format '{{.Names}} {{.ID}} {{.Image}} {{.State}}' | sort
    podman volume ls --format '{{.Name}}' | sort
    for name in "$@"; do
      podman inspect "sandboxed-agents.manual-check.$name" 2>&1
      podman inspect "sandboxed-agents-backup.manual-check.$name" 2>&1
    done
  } > "$out"
}
```

```powershell
function Capture-State($File, [string[]]$Sandboxes = @()) {
  $Out = @(sandboxed-agents list 2>&1)
  $Out += pm ps --all --format '{{.Names}} {{.ID}} {{.Image}} {{.State}}' | Sort-Object
  $Out += pm volume ls --format '{{.Name}}' | Sort-Object
  foreach ($Name in $Sandboxes) {
    $Out += pm inspect "sandboxed-agents.manual-check.$Name" 2>&1
    $Out += pm inspect "sandboxed-agents-backup.manual-check.$Name" 2>&1
  }
  $Out | Set-Content $File
}
```

An item that expects "nothing changes" captures the state **before** the command, runs it, captures the state **after** it, and compares the two files with `diff before.txt after.txt` or `Compare-Object (Get-Content before.txt) (Get-Content after.txt)`. Any difference fails the item. A missing object makes `inspect` print an error; both captures print the same error, so the comparison still means something.

## Order of the run

Several items share one long-lived sandbox, `chk-persist`, so that a login done early is still there when the update is observed. Run them in this order:

1. [Create `chk-persist`](#the-shared-sandbox-chk-persist).
2. The logins, all in `chk-persist`: [agent logins](#agent-logins), [GitHub](#github-login), [Azure device code](#azure-login-device-code), [Azure browser](#azure-login-browser), [Azure DevOps](#azure-devops-token).
3. [Persistence and update](#persistence-and-update): the baseline round, then `stop` and `start`, then the one `build` and the `update`, with the same observations each time.
4. [No-terminal refusals](#toolchain-requirement-and-no-terminal-refusals), which also use `chk-persist`.
5. The independent items, each with a sandbox it removes itself: [VS Code Remote SSH](#vs-code-remote-ssh), [an interrupted update](#an-interrupted-update), the [toolchain requirement](#toolchain-requirement-and-no-terminal-refusals), the [quick guides](#quick-guides), and the [installs](#installers-issue-no-podman-call) on clean machines.
6. [Release files](#release-files-and-attestations).
7. [Final cleanup](#final-cleanup): the destructive removal of `chk-persist`, then the [image comparison](#no-command-removes-an-image), then every revocation.

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
| `installers-no-podman` | [Installers issue no Podman call](#installers-issue-no-podman-call) | `windows-11` |
| `preview-attestation` | [Release files and attestations](#release-files-and-attestations) | either |

## Items

### The shared sandbox `chk-persist`

Create it with the `azure` toolchain, which the Azure and Azure DevOps logins need, and with `opencode` enabled. Install no SSH setup yet; the Azure browser item needs the sandbox without one first:

```sh
sandboxed-agents up chk-persist --with azure --agents opencode
sandboxed-agents list
```

The first `up` builds the images, which takes a while.

**Every login, workflow, and observation below runs with the current executable**, the one built from the commit under test. A result observed with any other executable does not count for that commit.

The [update](#persistence-and-update) later gets an outdated image from one explicit `build`, so `chk-persist` is created with the current executable and nothing else is needed here.

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

1. In `shell chk-persist`, run `az logout`; `az account show` must report no login. Then, with no SSH setup installed, run `sandboxed-agents integrations login chk-persist azure browser`. **Expected:** it fails, names `ssh-config chk-persist --install`, and `az account show` still reports no login.
2. Run `sandboxed-agents ssh-config chk-persist --install`, then the browser workflow again. **Expected:** an `ssh -N -L ...` line and a URL appear before anything waits; the alias matches the host entry `chk-persist.manual-check`.
3. Open the URL but stop the tunnel before signing in, then press `Ctrl+C` in the first terminal. **Expected:** non-zero status, no account printed, and `az account show` reports no login (there was none to begin with).
4. Run the workflow once more, run the tunnel on the machine whose browser you use, open the URL, and sign in. **Expected:** exit status zero and the account is printed. Stop the tunnel; `az account show` still works in the shell.

The SSH setup and the Azure login stay until [Final cleanup](#final-cleanup).

### Azure DevOps token

Identifier `azdo-token`. Follow [Manual Azure DevOps login check](integrations.md#manual-azure-devops-login-check) in `chk-persist`, with a personal access token that has `Code (Read)` and `Project and Team (Read)` for a test organization with a private test repository.

1. Run `sandboxed-agents integrations login chk-persist azdo --organization https://dev.azure.com/ORGANIZATION` with standard input from a terminal. **Expected:** a prompt; nothing is shown while you paste the token; exit status zero.
2. In `shell chk-persist`, `GIT_TERMINAL_PROMPT=0 git clone https://dev.azure.com/ORGANIZATION/PROJECT/_git/REPO /workspace/azdo-0` succeeds, and `az devops project list --organization https://dev.azure.com/ORGANIZATION --output table` lists projects.
3. **Expected:** the token was typed only at the hidden prompt. The command line you ran, and so the host's shell history, holds `--organization` and the URL and no token.

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
5. Record the configuration the update must keep, redirecting both outputs to `config-before.txt` (on Windows write `pm inspect`, see [Podman on Windows](#podman-on-windows-and-capturing-state)):

   ```sh
   podman inspect --format '{{.Image}} {{.HostConfig.Memory}} {{.HostConfig.CpuQuota}} {{.HostConfig.CpuPeriod}} {{.HostConfig.PidsLimit}} {{.HostConfig.ShmSize}} {{index .Config.Labels "io.sandboxed-agents.toolchains"}} {{index .Config.Labels "io.sandboxed-agents.port"}}' sandboxed-agents.manual-check.chk-persist
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
3. Make the image outdated, then update, all with the current executable:
   1. Save the image inventory for the [image check](#no-command-removes-an-image) as `images-before.txt`: `podman images --all --format '{{.ID}} {{.Repository}}:{{.Tag}}'`. Note the container's image and the image its tag names now (`TAG` is the first output):

      ```sh
      podman inspect --format '{{.Config.Image}}' sandboxed-agents.manual-check.chk-persist
      podman inspect --format '{{.Image}}' sandboxed-agents.manual-check.chk-persist
      podman image inspect --format '{{.Id}}' TAG
      ```

      The last two print the same image ID.
   2. Run `sandboxed-agents build` **once**. It rebuilds the base image and every existing toolchain set of this executable, here the recorded `azure` set, anew, and marks the existing sandbox as outdated. This is documented behavior ([Build, refresh, and reuse](images.md#build-refresh-and-reuse)). It is the only `build` in the whole checklist, and the [image check](#no-command-removes-an-image) reuses it. Images are shared across controller groups, which is why the safety rules ask for a dedicated account.
   3. Check the target: `sandboxed-agents list` shows `chk-persist` as outdated, and `podman image inspect --format '{{.Id}}' TAG` now prints a **different** image ID from the container's `{{.Image}}`. If both are still the same, the update has nothing to replace, and the item cannot be observed.
   4. Run the plain `sandboxed-agents update chk-persist`, with no `--with`. The toolchain set stays the recorded `azure` set.
4. Round 2.
5. Repeat the `podman inspect` commands of the setup into `config-after.txt` and compare it with `config-before.txt`; only the image ID may differ, and it must equal the image ID that the tag names after the `build`.
6. Run `ssh chk-persist.manual-check true`.

**Expected after the update (`update-complete`):** the update ends with exit status zero; the image ID differs from the one recorded; the volumes (the mount names), the port, the memory, CPU, process, and shared-memory limits, the workspace, and the toolchain set (`azure`) are the same as recorded; the agent selection and the credentials are the same, as round 2 shows; and `ssh chk-persist.manual-check true` connects without a host key prompt.

**Identifiers:** `selection-pins-persist` passes on steps 1 and 2 of all three rounds; `git-identity-persist` on step 5 of all three; `credentials-persist` on steps 1, 3, 4, and 6 of all three; and, for the removal half of `credentials-persist`, on [Final cleanup](#final-cleanup); `update-complete` on the expected observations after the update.

### Toolchain requirement and no-terminal refusals

Identifiers `login-needs-terminal` and `azure-toolchain-required`. The first runs in `chk-persist` (which has the `azure` toolchain and an SSH setup), after the persistence rounds. The second uses two short-lived sandboxes.

For `login-needs-terminal`, first capture the state (`capture_state before-term.txt chk-persist`, or `Capture-State before-term.txt @('chk-persist')` on Windows), and note the output of `gh auth status` and `az account show` in `shell chk-persist`. Then take standard input from nothing for each login. None of the three may change the sandbox:

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

Record each exit status and message. Capture the state again as `after-term.txt`, and run `gh auth status` and `az account show` again.

**Expected:** each exits non-zero with a message that the login needs an interactive terminal. `before-term.txt` and `after-term.txt` are identical, and the two status commands print what they printed before, so the earlier logins are untouched. This item observes the refusal message, the non-zero status, and the unchanged state. It does not observe whether a workflow process started; the offline CLI tests prove separately that none does.

For `azure-toolchain-required`, create two sandboxes without the `azure` toolchain:

```sh
sandboxed-agents up chk-base
sandboxed-agents up chk-native --with native
```

For each, capture the state **before** (`capture_state before-SANDBOX.txt SANDBOX`, or `Capture-State` on Windows), then run the three commands below with an organization URL that does not exist (replace `SANDBOX` with `chk-base`, then `chk-native`), then capture the state **after** (`after-SANDBOX.txt`) and compare the two files:

```sh
sandboxed-agents integrations login SANDBOX azure device-code
sandboxed-agents integrations login SANDBOX azure browser
sandboxed-agents integrations login SANDBOX azdo --organization https://dev.azure.com/example-organization
```

**Expected:** each fails non-zero before anything is stored and before a token is read; the message names the `azure` toolchain and prints the complete command: `update chk-base --with azure` for `chk-base`, and `update chk-native --with azure,native` for `chk-native`. The printed set never contains `none`. Nothing changes: the `before` and `after` files, which hold `list`, `ps`, `volume ls`, and `inspect` of the sandbox, are identical, and `git config --global --get-regexp '^credential\.'` in `shell` prints nothing. Remove both when done:

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

Identifier `update-interrupted`. This needs a sandbox `chk-int` that an `update` replaces. Create it fresh with the current executable and replace it with `update chk-int --with native`, which changes the toolchain set and so always has something to replace (documented in [Updating a sandbox](images.md#updating-a-sandbox)). This does not depend on an older executable, and every command runs with the current one. The goal is to stop the `update` process after the old container was renamed to the backup and before the update finished, killing **only that process**.

1. Create it: `sandboxed-agents up chk-int`.
2. Start the update in the background and keep its process ID, poll for the backup for at most two minutes, and kill that one process ID:

   ```sh
   sandboxed-agents update chk-int --with native & UPDATE_PID=$!
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
   $Update = Start-Process sandboxed-agents -ArgumentList 'update', 'chk-int', '--with', 'native' -PassThru -NoNewWindow
   $Deadline = (Get-Date).AddSeconds(120)
   $Found = $false
   while ((Get-Date) -lt $Deadline -and -not $Update.HasExited) {
     $State = sandboxed-agents list 2>&1 | Out-String
     if ($State -match '(?m)chk-int.*update interrupted') { $Found = $true; break }
     Start-Sleep -Milliseconds 500
   }
   if ($Found -and -not $Update.HasExited) { Stop-Process -Id $Update.Id -Force } else { 'backup not seen; repeat with a fresh sandbox' }
   ```

   The commands name the process ID of the one `update` they started and nothing else. They never match processes by name. A `podman` call that was already running when the process was killed may still finish; that is part of a real interruption. If the update finished before the kill, or the backup never appears, the item cannot be observed that time: repeat with a fresh sandbox, and it does not pass until it is observed.
3. Capture the state **before** the commands that must change nothing: `capture_state before-int.txt chk-int` (`Capture-State before-int.txt @('chk-int')` on Windows). Run `sandboxed-agents check chk-int` and `sandboxed-agents list`, and record their output and exit status. Then run **every** command path that takes this sandbox's name from the list below. Save the list as `refusals.txt` in your scratch directory, one command per line (without the leading `sandboxed-agents`):

   ```text
   up chk-int
   start chk-int
   start chk-int --ssh-config
   stop chk-int
   restart chk-int
   remove chk-int
   shell chk-int
   ssh-config chk-int
   ssh-config chk-int --install
   ssh-config chk-int --remove
   fingerprint chk-int
   agents enable chk-int opencode
   agents disable chk-int opencode
   agents update chk-int opencode
   agents login chk-int opencode provider
   agents status chk-int opencode
   agents run chk-int opencode --version
   agents session chk-int opencode
   agents session chk-int opencode --stop
   integrations login chk-int github
   integrations login chk-int azure device-code
   integrations login chk-int azure browser
   integrations login chk-int azdo --organization https://dev.azure.com/example-organization
   integrations config chk-int git identity --name Manual-Check --email manual-check@example.invalid
   integrations config chk-int git credentials
   ```

   The list holds every command path of the grammar that takes a sandbox name, except `check chk-int`, `list`, `update chk-int`, and `update --all`, which recover or report. Commands that take no sandbox name (`version`, `build`, `check` without a name) are not part of it. Run them all with standard input from nothing, so that a missing terminal cannot be the reason for a refusal:

   ```sh
   while IFS= read -r line; do
     out=$(sh -c "sandboxed-agents $line" </dev/null 2>&1); code=$?
     case "$out" in *"update chk-int"*) names=yes ;; *) names=no ;; esac
     printf '%s | exit %s | names update chk-int: %s\n' "$line" "$code" "$names"
   done < refusals.txt
   ```

   ```powershell
   foreach ($Line in Get-Content refusals.txt) {
     $Out = cmd /c "sandboxed-agents $Line < NUL 2>&1" | Out-String
     $Code = $LASTEXITCODE
     '{0} | exit {1} | names update chk-int: {2}' -f $Line, $Code, ($Out -match 'update chk-int')
   }
   ```

   Record each line the loop prints. Then capture the state again as `after-int.txt` and compare it with `before-int.txt`.
4. Run `sandboxed-agents update chk-int`.

**Expected:**

- `check chk-int` reports the leftover backup container and exits non-zero. It and every refused command change nothing: `before-int.txt` and `after-int.txt` are identical, so `list`, `ps`, `volume ls`, and the `inspect` of the container and of the backup are the same.
- `list` shows the sandbox as `update interrupted`.
- Every line of `refusals.txt` shows a non-zero exit status and `names update chk-int: yes` (`True` on Windows). A line that exits zero, or whose output does not name `update chk-int`, fails the item; if a command such as `stop` or `remove` was not refused and changed the sandbox, discard the sandbox and repeat with a fresh one.
- `update chk-int` recovers instead of updating, builds no image, and exits as the state determines: zero when the replacement is ready and the update completes; non-zero when it restores the old sandbox ("restored" in its output). Either way the volumes are unchanged, no backup remains, and the next `update chk-int --with native` updates normally, or reports `already up to date` if the recovery had completed the update. See [Updating a sandbox](images.md#updating-a-sandbox) for the cases.

Clean up: `sandboxed-agents remove chk-int --volumes`.

### Quick guides

Identifiers `quick-guide-linux` (platform `linux`) and `quick-guide-windows` (platform `windows-11`). Follow the guide from its first step to a signed-in agent session on a clean machine, **using only that guide**. If you needed another document, a command did not work as written, or a step was unclear, the item fails.

- [Linux quick guide](quick-guide-linux.md) on a clean Linux machine with Podman and OpenSSH installed as the guide lists. The tag is the preview's.
- [Windows quick guide](quick-guide-windows.md) on a clean Windows 11 machine.

Export the dedicated group before the first `sandboxed-agents` command of the guide, as in the [safety rules](#safety-rules). **Expected:** every command works as written, including download, checksum comparison, attestation verification, placing the binary on `PATH`, `check`, `up`, `agents enable`, `agents login`, and `agents session`, and the final session shows an answered prompt without a sign-in prompt. At the end, remove the guide's sandbox: `sandboxed-agents remove agent01 --volumes`.

### npm install on a clean machine

Identifier `npm-install-clean`. Use a machine on which `sandboxed-agents` has never been installed, with Node.js 18 or newer, Podman, and OpenSSH. Download `sandboxed-agents-VERSION.tgz` from the preview prerelease. The npm registry holds no package from this repository, so the install uses the file ([npm package](npm-package.md#install)). Use the contained npm commands of [the install check](#installers-issue-no-podman-call) (Windows) or of its [Linux supplement](#optional-supplemental-linux-snapshots), so that npm writes its prefix, cache, temporary files, and logs only under the install root.

1. Install the tarball with the contained commands.
2. Run the installed command: `sandboxed-agents version`, `sandboxed-agents check`, and `sandboxed-agents list`.
3. Unpack a copy of the `.tgz`, change one byte of an executable in it, repack it as `tampered.tgz`, and install that into a second prefix under the install root with the same contained commands.
4. Remove the package with `npm uninstall -g` and the same `--prefix`.

**Expected:** step 2 prints the preview's version and `asset-sha256`, reports the host, and runs without a sandbox; step 3 fails the installation on the checksum; after step 4 the command is gone.

### NuGet install on a clean machine

Identifier `nuget-install-clean`, platform `windows-11`. Use a clean Windows 11 machine with PowerShell 7 and no administrator session. Take `SandboxedAgents.VERSION.nupkg` from the preview prerelease and follow [NuGet command package](nuget-package.md#install): extract it into the install root, unblock the scripts, and run `install-command.ps1`.

1. Record the user `Path` as stored: `(Get-Item HKCU:\Environment).GetValue('Path', '', 'DoNotExpandEnvironmentNames')`.
2. Install, open a new terminal, and run `sandboxed-agents version`.
3. Run the installer again (a same-version reinstall, not a distinct-version upgrade), then `remove-command.ps1`. Record the stored `Path` after each step.

**Expected:** the installer needs no administrator rights; `version` works in a new terminal; the stored `Path` differs only by the install directory entry, and existing entries, including unexpanded ones such as `%USERPROFILE%\bin`, are byte-for-byte unchanged; after removal the entry is gone and the other entries are unchanged. [The install check](#installers-issue-no-podman-call) adds the process-attributed write trace.

### Installers issue no Podman call

Identifier `installers-no-podman`, platform `windows-11`. Install, reinstall, and remove through **each** of three methods, and show two things: no Podman call happens, and the installers change no file and no registry value outside the install location and the user `PATH`. The methods are the downloaded binary, the npm package, and the NuGet command package. NuGet exists only on Windows, so the item is run on Windows 11, covers all three methods there, and is recorded with the platform `windows-11`. A Linux run of methods 1 and 2 is optional supplemental coverage ([below](#optional-supplemental-linux-snapshots)); it does **not** replace the Windows run, and the item does not pass on Linux alone.

**Why a trace and not a comparison of the whole profile.** A signed-in Windows profile always holds files that system processes keep open and rewrite (the registry hives, the browser and shell caches), so hashing the whole profile before and after cannot finish and would blame the installers for other programs' writes. The Windows run therefore records every file system and registry write of the **installer's own process tree** with Process Monitor from Microsoft Sysinternals, and attributes each write to a process. Unrelated background processes are excluded by that attribution, not by exempting paths: **no write by the item shell or its descendants outside the allowed locations is tolerated**, whoever the writing program is.

Official sources: the [Process Monitor page](https://learn.microsoft.com/en-us/sysinternals/downloads/procmon) of Microsoft Learn, and the Microsoft Learn blog post [Using Procmon in Command-line](https://learn.microsoft.com/en-us/archive/blogs/yash/using-procmon-in-command-line) for `/Quiet`, `/Minimized`, `/BackingFile`, and `/Terminate`. The Help that is installed with your version of Process Monitor is authoritative for its menus, filters, and command line; where it differs from the steps below, follow it and write down what you did.

**Prerequisites a human arranges before the run** (not something an agent session does):

- Process Monitor, downloaded by you from the official page above and unpacked into a directory of your choosing.
- A **separate elevated terminal, the observer**, opened by a human who consents to running Process Monitor with administrator rights on the test machine. Only the observer is elevated. The installers run in a normal, **non-elevated** terminal, the item shell, and the item fails if an installer needs more than that.
- The evidence directory below.
- A fresh disposable virtual machine, so that the run starts clean.

**The evidence directory.** A standard user cannot create folders in the root of the system drive. So arrange **before the run** an existing directory that you can write to and that lies **outside your user profile**: for example a folder on a test volume that was prepared for you, or a shared scratch location your account has write access to. Do not assume a particular drive or the ACLs of a public folder; if you have no such directory, the item cannot be run yet. The Process Monitor logs, the fake Podman, and the notes go beneath it. These log files can hold user names, paths, and environment details, so they are kept **locally only**, are never uploaded, and are deleted at the end of the item; the record has no field for them.

**Parent terminal setup.** In an ordinary, non-elevated terminal, the parent terminal, do all of this **before** you start the item shell. The item shell inherits this environment when it starts, and PowerShell reads some of these settings only at startup: the [about_Environment_Variables](https://learn.microsoft.com/en-us/powershell/module/microsoft.powershell.core/about/about_environment_variables?view=powershell-7.5) page of Microsoft Learn says that `PSModuleAnalysisCachePath` must name a full path including the file name and is set before starting PowerShell (the cache is written on a background thread some time after a module is imported), and that `POWERSHELL_TELEMETRY_OPTOUT` and a non-default `POWERSHELL_UPDATECHECK` must be set before starting the PowerShell process. All of these are process-only settings; the commands change no user or machine setting.

```powershell
$Evidence = Read-Host 'Existing writable evidence directory outside your user profile'
if (-not (Test-Path -LiteralPath $Evidence -PathType Container)) { throw 'the evidence directory does not exist' }
$Evidence = (Resolve-Path -LiteralPath $Evidence).ProviderPath.TrimEnd('\')
$ProfileRoot = (Resolve-Path -LiteralPath $env:USERPROFILE).ProviderPath.TrimEnd('\')
$Ignore = [StringComparison]::OrdinalIgnoreCase
if ($Evidence.Equals($ProfileRoot, $Ignore) -or $Evidence.StartsWith($ProfileRoot + '\', $Ignore) -or $ProfileRoot.StartsWith($Evidence + '\', $Ignore)) { throw 'the evidence directory must be outside the user profile and must not contain it' }
$Probe = Join-Path $Evidence "probe-$PID.tmp"
try { Set-Content -LiteralPath $Probe -Value 'x' -ErrorAction Stop; Remove-Item -LiteralPath $Probe -ErrorAction Stop } catch { throw "the evidence directory is not writable: $Evidence" }
$InstallRoot = Join-Path $HOME 'chk-install'
if (Test-Path -LiteralPath $InstallRoot) { throw "$InstallRoot already exists; start from a fresh machine, or remove it first" }
$Pml = Join-Path $Evidence "pml-$PID"
$Fake = Join-Path $Evidence "chk-fake-podman-$PID"
New-Item -ItemType Directory -ErrorAction Stop $Pml, $Fake | Out-Null
foreach ($Name in 'bin', 'prefix', 'cache', 'logs', 'tmp') { New-Item -ItemType Directory -ErrorAction Stop (Join-Path $InstallRoot $Name) | Out-Null }
Set-Content (Join-Path $Fake 'podman.cmd') "@echo off`r`necho %* >> `"$Fake\calls`"`r`nexit /b 1"
$env:Path = "$Fake;$env:Path"
$env:TEMP = Join-Path $InstallRoot 'tmp'; $env:TMP = $env:TEMP
$env:PSModuleAnalysisCachePath = Join-Path $InstallRoot 'cache\ModuleAnalysisCache'
$env:POWERSHELL_TELEMETRY_OPTOUT = '1'; $env:POWERSHELL_UPDATECHECK = 'Off'
$env:npm_config_update_notifier = 'false'
$env:CHK_EVIDENCE = $Evidence; $env:CHK_INSTALL_ROOT = $InstallRoot; $env:CHK_PML = $Pml; $env:CHK_FAKE = $Fake
$Pml
```

The last line prints the log directory; the observer uses it. The block creates the **install root** `$InstallRoot` as a new directory and stops if it already exists, so the run starts from a directory it made. Every file the methods write must land inside it, **including npm's prefix, cache, temporary files, and logs**; the commands below point them there, and `TEMP` and `TMP` point into it too. The default location of the NuGet command package in method 3b is the one other allowed location. The **fake Podman** is a `podman.cmd` in the evidence directory that appends its arguments to a `calls` file and exits with 1; the block puts it first on `PATH`.

**The item shell.** From the same parent terminal, start a fresh PowerShell 7 and use it for the whole item, so that its process ID and the processes it starts form one process tree. Start it only now, after the settings above:

```powershell
pwsh -NoProfile
```

In the item shell, set it up as follows. `$PID` is the process ID to write down as `SHELL_PID`. The commands stop if the shell was not started from the prepared parent terminal:

```powershell
Set-PSReadLineOption -HistorySaveStyle SaveNothing
$PID
$Evidence = $env:CHK_EVIDENCE; $InstallRoot = $env:CHK_INSTALL_ROOT; $Pml = $env:CHK_PML; $Fake = $env:CHK_FAKE
foreach ($Value in $Evidence, $InstallRoot, $Pml, $Fake) { if (-not $Value) { throw 'start this shell from the prepared parent terminal' } }
if ($env:PSModuleAnalysisCachePath -ne (Join-Path $InstallRoot 'cache\ModuleAnalysisCache')) { throw 'the module analysis cache path is not redirected' }
```

**The logical environment, recorded in memory.** The user `PATH` is a registry value of `HKCU:\Environment`. The helper records every value of that key with its kind and its raw, unexpanded content. It keeps the result in memory, so that the item shell writes no file while a trace runs:

```powershell
function Get-EnvSnapshot {
  $Key = Get-Item HKCU:\Environment
  $Key.GetValueNames() | Sort-Object | ForEach-Object { "$_ [$($Key.GetValueKind($_))] = $($Key.GetValue($_, '', 'DoNotExpandEnvironmentNames'))" }
}
```

**Prepare everything the captured steps will use, before any trace.** The captured steps run **inside this shell** and start no new PowerShell. To keep the shell's own startup and module caches out of the trace, load what the steps need now, extract the NuGet command package now, and let the background cache write finish:

```powershell
Get-EnvSnapshot | Out-Null
Get-Command Copy-Item, Remove-Item, Get-Item, Get-ChildItem, Set-Content, Unblock-File, Compare-Object, Expand-Archive | Out-Null
Copy-Item .\SandboxedAgents.VERSION.nupkg "$InstallRoot\package.zip"
Expand-Archive -LiteralPath "$InstallRoot\package.zip" -DestinationPath "$InstallRoot\package"
$Tools = Join-Path $InstallRoot 'package\tools'
Get-ChildItem -LiteralPath $Tools -Filter *.ps1 | Unblock-File
node --version
npm --version --cache "$InstallRoot\cache" --logs-dir "$InstallRoot\logs"
$Deadline = (Get-Date).AddSeconds(60)
while (-not (Test-Path -LiteralPath $env:PSModuleAnalysisCachePath) -and (Get-Date) -lt $Deadline) { Start-Sleep -Seconds 2 }
Start-Sleep -Seconds 10
Test-Path -LiteralPath $env:PSModuleAnalysisCachePath
```

`SandboxedAgents.VERSION.nupkg` is the verified `.nupkg` of the preview in the current directory. The last line prints whether the module analysis cache file exists. The node and npm start-ups run here, outside the capture, so their first-run state is not part of an installer's window. Keep this shell **alive past the end of the last capture**: PowerShell may write its startup profile data when a session ends, and that belongs to the end of the shell, not to an installer. None of this hides anything from the trace. If an installer or a child of an installer still writes outside the allowed locations while a capture runs, that write is a failure of the criterion and the item fails; do not remove the finding by preparing more, redirecting more, or exempting the path.

**The capture procedure, for each method.** Each method has its own log, `$Pml\METHOD.pml`, and three steps (install, reinstall, remove) inside it. Run the methods one after the other, in the order `binary`, `npm`, `nuget-root`, `nuget-default`. For each method, do these steps in order. The commands of a method are separate lines to run **one at a time**, never as one pasted block:

1. **Observer (elevated terminal), start the capture.** In the directory where Process Monitor is unpacked, with `PML_DIRECTORY` the log directory that the parent terminal printed:

   ```powershell
   .\Procmon64.exe /AcceptEula /Quiet /Minimized /BackingFile "PML_DIRECTORY\METHOD.pml"
   ```

   `/Quiet` suppresses the filter prompt at startup. It does **not** guarantee that no filter is active, because Process Monitor can load the filter it saved earlier.
2. **Observer, make the capture complete.** Open the Process Monitor window from the taskbar and do all of this **before** the item shell runs anything:
   - **Filter**, **Filter...**: press **Reset** and then **OK**, so that no saved filter remains. The filter list is empty apart from Process Monitor's own defaults. Reset it again if you see anything else.
   - On the **Filter** menu, make sure **Drop Filtered Events** is **not** checked, so that nothing is lost even if a filter is set by mistake.
   - On the toolbar, make sure the buttons for **file system activity**, **registry activity**, and **process and thread activity** are all enabled (pressed).
   - **Tools**, **Process Tree** lists the running processes (the shell with `SHELL_PID` among them), and the event counter in the status bar increases. If either does not hold, stop the capture with step 5, delete the log, and start again.
   - The window is the authority for its own controls. Follow the Help installed with your version of Process Monitor if a menu item is named differently, and write down what you did.
3. **Item shell, prepare.** Run the method's *before the capture* commands, if it has any, and start a fresh environment record for this method, with these two commands (the record of the previous method must not carry over):

   ```powershell
   $EnvSnap = [ordered]@{}
   $EnvSnap['start'] = Get-EnvSnapshot
   ```
4. **Item shell, the steps, one command at a time and in this order.** The method sections give `INSTALL`, `REINSTALL`, and `REMOVE`. Run nothing else in the item shell in between.
   1. Run `INSTALL`.
   2. `$EnvSnap['after-install'] = Get-EnvSnapshot`
   3. Run `REINSTALL`.
   4. `$EnvSnap['after-reinstall'] = Get-EnvSnapshot`
   5. Run `REMOVE`.
   6. `$EnvSnap['after-remove'] = Get-EnvSnapshot`
5. **Observer, stop the capture.**

   ```powershell
   .\Procmon64.exe /Terminate
   ```

   Wait until no `Procmon64.exe` process is left (Task Manager) before you open the log. A log that is read while Process Monitor still runs may be incomplete.
6. **Inspect the log** as described under [Inspecting a log](#inspecting-a-log), in the observer's Process Monitor, and compare the environment record of this method, which is still in the item shell: for example `Compare-Object $EnvSnap['start'] $EnvSnap['after-install']`, and the same for each pair of neighbors.

**Method 1, `binary` (downloaded binary).** The input is the verified Windows binary and a **second verified copy of the same preview binary** for the replacement. Verify both copies as in the [quick guides](quick-guide-windows.md#3-verify) before the capture. The replacement is a same-version overwrite. It exercises the overwrite and claims nothing about a distinct-version upgrade. (If a previous preview's verified executable exists, you may use it as the first copy and the current one as the replacement.) Before the capture: nothing.

- `INSTALL` = `Copy-Item .\sandboxed-agents-windows-amd64.exe "$InstallRoot\bin\sandboxed-agents.exe"`, then `& "$InstallRoot\bin\sandboxed-agents.exe" version`.
- `REINSTALL` = `Copy-Item .\second-copy.exe "$InstallRoot\bin\sandboxed-agents.exe" -Force`, then `& "$InstallRoot\bin\sandboxed-agents.exe" version`.
- `REMOVE` = `Remove-Item "$InstallRoot\bin\sandboxed-agents.exe"`.

**Method 2, `npm` (npm package).** The input is `sandboxed-agents-VERSION.tgz`. All paths are under the install root, and the update notifier is off. Before the capture: nothing.

- `INSTALL` = `npm install --offline --no-audit --no-fund -g --prefix "$InstallRoot\prefix" --cache "$InstallRoot\cache" --logs-dir "$InstallRoot\logs" .\sandboxed-agents-VERSION.tgz`, then `& "$InstallRoot\prefix\sandboxed-agents.cmd" version`.
- `REINSTALL` = the same `npm install` command again (a same-version reinstall).
- `REMOVE` = `npm uninstall -g --prefix "$InstallRoot\prefix" --cache "$InstallRoot\cache" --logs-dir "$InstallRoot\logs" sandboxed-agents`.

**Method 3a, `nuget-root` (NuGet command package into the install root).** The input is the `.nupkg`, extracted and unblocked during the preparation. Before the capture: nothing. The scripts run in the item shell itself with the call operator; no new PowerShell is started inside a capture.

- `INSTALL` = `& "$Tools\install-command.ps1" -InstallDirectory "$InstallRoot\nuget" -NoPathUpdate`
- `REINSTALL` = the same command again (a same-version reinstall).
- `REMOVE` = `& "$Tools\remove-command.ps1" -InstallDirectory "$InstallRoot\nuget" -NoPathUpdate`

**Method 3b, `nuget-default` (NuGet command package to its default location and `PATH`, the separate default-path test).** Uses the same extracted package and `$Tools`, with the default arguments, so the installer uses `%LOCALAPPDATA%\Programs\sandboxed-agents` and the user `PATH`. The allowed locations of this method are the install root, `%LOCALAPPDATA%\Programs\sandboxed-agents` (and its `Programs` parent if the installer has to create it), and the user `PATH` value. Before the capture: nothing. Run the scripts in the item shell with the call operator as in method 3a.

- `INSTALL` = `& "$Tools\install-command.ps1"`
- `REINSTALL` = the same command again (a same-version reinstall).
- `REMOVE` = `& "$Tools\remove-command.ps1"`

#### Inspecting a log

In the observer's Process Monitor, open the saved log (**File**, **Open**). The Process Monitor page describes the tools used here: filters can be set on any field without losing data, and the Process Tree tool shows the relationship of all processes in a trace.

1. **Select the process tree.** Open **Tools**, **Process Tree**. Find the process whose PID is `SHELL_PID` (`pwsh.exe`) and write down it and **every descendant**, including the ones that have already exited (they stay in the tree): `node.exe` and `cmd.exe` for npm, the installed executable for `version`. The NuGet scripts run inside the shell itself, so they add no child. This is the set of attributed processes. **Assert that no process in this set is `podman` or `podman.exe` (or has `podman` in its image path or command line), whatever the `PATH` was**: the fake Podman only catches calls that go through `PATH`, and a call by absolute path would otherwise pass unnoticed. Any such process fails the method. If `SHELL_PID` is not in the tree, you opened the wrong log or the capture did not run, and the item fails for that method. Note the total event count of the log; zero or very few events means the capture was not complete.
2. **Filter to the attributed events.** Open the filter dialog (**Filter**, **Filter...**, or Ctrl+L) and press **Reset** first. Add **Include** conditions `PID is N` for the shell and for each descendant. Conditions on the same field are alternatives, so the view shows the events of any of them. Add **Include** conditions `Event Class is File System` and `Event Class is Registry`. Set **no** `Result`, **no** `Operation`, and **no** `Detail` condition for the review below. The view now holds every file system and registry event of the attributed processes, whatever its result.
3. **Navigation aids, not a definition.** To find the likely changes first, you may temporarily add `Operation is X` conditions for `WriteFile`, `SetEndOfFileInformationFile`, `SetAllocationInformationFile`, `SetDispositionInformationFile`, `SetRenameInformationFile`, `RegCreateKey`, `RegSetValue`, `RegDeleteKey`, `RegDeleteValue`, `RegSetInfoKey`, or look at `CreateFile` rows with `Detail contains OpenResult: Created` and similar. **These lists are finite and incomplete.** They leave out, for example, `SetBasicInformationFile` (times and attributes), `SetSecurityFile`, `SetEAFile`, `SetLinkInformationFile` (hard links), reparse-point and other `FileSystemControl` requests, and the registry operations that load, restore, replace, rename, or secure a key. Remove the aids before step 4; they never replace it.
4. **Required review of ALL remaining events.** With the aids removed, list the distinct values of the **Operation** column in the view (sort by the column and scroll, or use **Tools**, **Count Occurrences...** on **Operation**). Classify **every** operation value that occurs, using the table below. An operation not in the table, or one you cannot classify, is **unknown**.

   | Class | Operations | What you do |
   |---|---|---|
   | Changes nothing | Reads, queries, enumerations and closes: `ReadFile`, `CloseFile`, `Query...` (`QueryInformationVolume`, `QueryDirectory`, `QueryOpen`, `QueryNameInformationFile`, `QueryStandardInformationFile`, `QueryBasicInformationFile`, `QuerySecurityFile`, `QueryEAFile`, ...), `SetPositionInformationFile`, `LockFile`, `UnlockFileSingle`, `FlushBuffersFile`, and for the registry `RegOpenKey`, `RegQueryKey`, `RegQueryValue`, `RegEnumKey`, `RegEnumValue`, `RegCloseKey`, `RegQueryKeySecurity`, `RegFlushKey` | Dismiss the class. Confirm once that its rows are what the name says. |
   | Changes content, metadata, names, security, links, or registry | `WriteFile`, `SetEndOfFileInformationFile`, `SetAllocationInformationFile`, `SetValidDataLengthInformationFile`, `SetDispositionInformationFile`, `SetRenameInformationFile`, `SetLinkInformationFile`, `SetShortNameInformation`, `SetBasicInformationFile`, `SetEAFile`, `SetSecurityFile`, `SetFileObjectIdInformation`, and for the registry `RegSetValue`, `RegSetInfoKey`, `RegDeleteKey`, `RegDeleteValue`, `RegRenameKey`, `RegLoadKey`, `RegUnLoadKey`, `RegRestoreKey`, `RegReplaceKey`, `RegSaveKey`, `RegSetKeySecurity` | **Every** row is a change. Apply the allowed locations (step 5). |
   | Depends on the details | `CreateFile`, `RegCreateKey`, `CreateFileMapping`, `FileSystemControl`, `DeviceIoControl` | Resolve every row from its **Detail** and the events that follow it, as below. |

   Resolve the third class as follows:
   - `CreateFile` with `OpenResult: Created`, `Overwritten`, or `Superseded` (a creation or overwrite disposition that took effect) is **a change**. A `CreateFile` that opened an existing file with write-capable rights, for example `Write Attributes` or `Generic Write`, is **not by itself a change**: it counts only if the same process then produces a changing event for that path (a row of the second class), so look at the events that follow it for the same path and the same process. An open with nothing changing after it is not a change.
   - `RegCreateKey` with a created-new-key disposition is a change; one that opened an existing key is not.
   - `CreateFileMapping` is classified by the rows around it: mapping an image or a file for reading changes nothing; a mapping that is followed by writes to the file is already covered by `WriteFile`.
   - `FileSystemControl` and `DeviceIoControl` are classified by the control code in **Detail**: a reparse point, hard link, sparse, compression, object-id, or encryption setting is a change; a query is not.
   - **Results, for every row.** Also list the distinct values of the **Result** column. For the first class any result is fine; queries end with codes such as `NO MORE ENTRIES`, `BUFFER OVERFLOW`, or `END OF FILE`. For the second class, `SUCCESS` means the change happened, a plain failure (`ACCESS DENIED`, `NAME NOT FOUND`, `PATH NOT FOUND`, `NAME COLLISION`, `SHARING VIOLATION`) means nothing changed, and any other result is unknown until you resolve it. For the third class the result is part of the resolution: a failed open changed nothing, and an open that ended in `REPARSE` continues on another path, which you check as its own row.
   - **An operation or a row that you cannot resolve prevents a pass.** Do not guess "probably harmless".
5. **Apply the allowed locations to every change.** For every row that is a change (the second class, and the rows of the third class that you resolved as changes), look at the **Path** column. A row is allowed only if its path is inside `$InstallRoot` (all methods) or, in method 3b, inside `%LOCALAPPDATA%\Programs\sandboxed-agents` (or is the `Programs` directory itself), or is the registry value `HKCU\Environment\Path` in method 3b. Rows for a named pipe (`\Device\NamedPipe\...`) are communication between the processes, not a change of a file; list them and accept that class only. **Every other row is a change outside the allowed locations, and the method fails.** Do not exclude a path in the filter to make a row go away.
6. **Write down the verdict.** For each method, note the number of events reviewed, the operations and results you saw and their class, the number of changes, why each accepted change is allowed, and every unknown. The `Path` value of method 3b must be written by the NuGet install script, which runs inside the item shell (`SHELL_PID`, `pwsh.exe`) as `install-command.ps1`, and removed by `remove-command.ps1`, and `HKCU\Environment\Path` must be the only registry value changed.

**Keep nothing but the verdict.** Keep the logs locally until you have the verdict for every method. Do not upload them or paste their content into the record or a ticket. Delete them at the end of the item.

**Expected:**

- The `calls` file in `$Fake` is empty or does not exist, so no Podman call was issued by any method.
- For methods `binary`, `npm`, and `nuget-root`, the required review finds no change outside `$InstallRoot` (apart from named pipes) and no unknown operation, and the environment records are identical at every step: `Compare-Object $EnvSnap['start'] $EnvSnap['after-install']`, and likewise for each pair of neighbors, print nothing.
- For `nuget-default`, the required review finds no change outside the allowed locations and no unknown operation, and the environment records show: `Path` gained the install directory entry between `start` and `after-install`, is unchanged between `after-install` and `after-reinstall`, and lost the entry again between `after-reinstall` and `after-remove`, so it equals `start`; its value kind is unchanged; no other value changed. Entries that were there before, including unexpanded ones such as `%USERPROFILE%\bin`, are byte-for-byte unchanged.
- A `Procmon64.exe` window that never counted events, a missing `SHELL_PID`, or a log that cannot be opened is no evidence: repeat the method. A write outside the allowed locations by the installer, an installer child process, or the item shell itself is a failure, and nothing is recorded as passed in its place. Sandboxes that exist keep running across the reinstall of any method.

When all captures have been stopped and you have the verdict for every method, close the item shell with `exit` and wait until its process has ended, so that it cannot write its module analysis cache into a tree you are about to delete. Do the cleanup from the original parent terminal, where `$InstallRoot`, `$Pml`, `$Fake`, and `$Evidence` are still defined. If the module analysis cache check in the preparation printed `False`, nothing needs to be done: any later write of that cache is redirected into the install root as well.

Then delete the install root, `$Pml`, `$Fake`, and the other files in the evidence directory that this item created. Leave the evidence directory itself as you found it.

#### Optional supplemental Linux snapshots

This is extra coverage for methods 1 and 2 on Linux, in Bash. It is **bounded and not exhaustive**: it hashes and lists only what `find` and `sha256sum` can read under the home directory, and it cannot see a write that was undone. It never replaces the Windows run, and a Linux result alone cannot pass `installers-no-podman`. The commands set up the install root and a fake Podman in `/tmp`, outside the home directory:

```sh
INSTALL_ROOT="$HOME/chk-install"; mkdir -p "$INSTALL_ROOT"/bin "$INSTALL_ROOT"/prefix "$INSTALL_ROOT"/cache "$INSTALL_ROOT"/logs "$INSTALL_ROOT"/tmp
SNAP="$(mktemp -d /tmp/chk-snapshot.XXXXXX)"
unset HISTFILE
mkdir -p /tmp/chk-fake-podman && printf '#!/bin/sh\necho "$@" >> /tmp/chk-fake-podman/calls\nexit 1\n' > /tmp/chk-fake-podman/podman && chmod +x /tmp/chk-fake-podman/podman
export PATH="/tmp/chk-fake-podman:$PATH"
```

```sh
snapshot() (
  set -o pipefail
  : > "$SNAP/$1.errors"
  find "$HOME" -xdev -path "$INSTALL_ROOT" -prune -o -type f -print0 2>>"$SNAP/$1.errors" | sort -z 2>>"$SNAP/$1.errors" | xargs -0 -r sha256sum > "$SNAP/$1.files" 2>>"$SNAP/$1.errors" || exit 1
  find "$HOME" -xdev -path "$INSTALL_ROOT" -prune -o -type l -printf '%p -> %l\n' 2>>"$SNAP/$1.errors" | sort > "$SNAP/$1.links" 2>>"$SNAP/$1.errors" || exit 1
  find "$HOME" -xdev -path "$INSTALL_ROOT" -prune -o -type d -print 2>>"$SNAP/$1.errors" | sort > "$SNAP/$1.dirs" 2>>"$SNAP/$1.errors" || exit 1
  test ! -s "$SNAP/$1.errors"
)
```

Take `snapshot before`, run a method, take `snapshot after`, and compare each pair with `diff "$SNAP/before.files" "$SNAP/after.files"` and the same for `.links` and `.dirs`. A failing `snapshot` or a non-empty `.errors` file means the snapshot is not usable. The function body is a subshell, so `set -o pipefail` does not stay on in your interactive shell. The npm commands of method 2 on Linux, with the prefix, cache, logs, and temporary directory under `$INSTALL_ROOT`:

```sh
export TMPDIR="$INSTALL_ROOT/tmp" npm_config_update_notifier=false
npm install --offline --no-audit --no-fund -g --prefix "$INSTALL_ROOT/prefix" --cache "$INSTALL_ROOT/cache" --logs-dir "$INSTALL_ROOT/logs" ./sandboxed-agents-VERSION.tgz
"$INSTALL_ROOT/prefix/bin/sandboxed-agents" version
npm uninstall -g --prefix "$INSTALL_ROOT/prefix" --cache "$INSTALL_ROOT/cache" --logs-dir "$INSTALL_ROOT/logs" sandboxed-agents
```

Method 1 on Linux is a copy to `$INSTALL_ROOT/bin/sandboxed-agents`, `version`, a copy of the second verified copy over it, `version` again, and a delete. Expect no difference and an empty `/tmp/chk-fake-podman/calls`, which supplements the Windows result without proving it.

### No command removes an image

Identifier `no-image-removal`. Compare the images before and after the one `build` and every other command of the run that could remove one.

1. `images-before.txt`, saved in [Persistence and update](#persistence-and-update) **before** its one `build`. This item runs no `build` of its own; that one is the shared step.
2. After the `build`, `update`, and the removals of the later items, and after [Final cleanup](#final-cleanup) has removed `chk-persist` with `--volumes`, list the images again into `images-after.txt` with the same command and compare the image IDs.

**Expected:** every image ID in `images-before.txt` is still present in `images-after.txt`. After the `build`, the replaced images may have lost their tags but still exist. No command of the run removed an image. The README names this as a limitation, and `podman image prune` removes unused images, never one a sandbox still uses.

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
4. Save `images-after.txt` and compare it with `images-before.txt` for the [image check](#no-command-removes-an-image).
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

Before you upload, check the file against the schema with `jq`. The check requires exactly the fields above, a `result` of `pass` or `fail` at both levels, only the 23 identifiers of this document, each at most once, the fixed platform of the platform-bound identifiers, and, for a record that says `pass`, all 23 identifiers present and every check passed. It also requires the top-level `result` to be `fail` when any check failed:

```sh
IDS='["agent-login-copilot","agent-login-claude","agent-login-codex","agent-login-opencode","github-login","azure-login-device-code","azure-login-browser","azdo-token","vscode-remote-ssh","npm-install-clean","nuget-install-clean","quick-guide-linux","quick-guide-windows","selection-pins-persist","git-identity-persist","credentials-persist","update-complete","update-interrupted","azure-toolchain-required","login-needs-terminal","no-image-removal","installers-no-podman","preview-attestation"]'
jq -e --argjson ids "$IDS" '
  (keys == ["checks","commit","kind","result","schema_version"])
  and .schema_version == 1 and .kind == "manual-checklist"
  and (.commit | test("^[0-9a-f]{40}$"))
  and (.result == "pass" or .result == "fail")
  and (.checks | type == "array" and length > 0)
  and all(.checks[];
        (keys == ["date","name","platform","result"])
        and (.result == "pass" or .result == "fail")
        and (.platform == "linux" or .platform == "windows-11")
        and (.date | test("^[0-9]{4}-[0-9]{2}-[0-9]{2}$"))
        and (.name | IN($ids[]))
        and (if (.name == "nuget-install-clean" or .name == "installers-no-podman" or .name == "quick-guide-windows") then .platform == "windows-11"
             elif .name == "quick-guide-linux" then .platform == "linux" else true end))
  and (([.checks[].name] | length) == ([.checks[].name] | unique | length))
  and (([.checks[] | select(.result == "fail")] | length) == 0 or .result == "fail")
  and (if .result == "pass" then (([.checks[].name] | sort) == ($ids | sort)) and all(.checks[]; .result == "pass") else true end)
' manual-checklist.json
```

`jq -e` exits non-zero when the check fails. The check validates the shape and the rules of this document; the date is checked for its form, and the stable release check parses it as a calendar date. A record that fails the check must be fixed before upload, or must not be uploaded.

Open the file and read it once before upload. It must contain nothing but the fields above.

### Upload

Uploading is a maintainer step on the preview's GitHub prerelease, where the stable release check reads the record. It is outward-facing, so do it only for a record you have read and only for a preview you ran against:

```sh
gh release upload TAG manual-checklist.json --repo grauzone-labs/sandboxed-agents
```

The stable release check accepts the record only when `schema_version` is `1`, `kind` is `manual-checklist`, `commit` is the commit of the release, `result` is `pass`, and every check holds `name`, `result`, `date`, and `platform`. See [Releases](releases.md#validation-records).
