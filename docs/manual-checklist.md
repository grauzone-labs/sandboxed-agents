# Manual checklist

This checklist is what you run by hand on a preview before a stable release: real logins with real accounts, a real editor, installs on clean machines, the quick guides followed by a person, and behavior that spans several features on a real host. It is the third release gate ([Releases](releases.md#gates)). Each item states what to do and what to observe, and the run ends in one record uploaded to the preview.

An item counts as passed only when a record of a run on the commit under test says so. The steps and expected observations come from the documented behavior of the executable. If an observation differs, the item fails and the defect is filed as its own issue.

## Manual checks and release evidence are different things

- A **manual check** is what a person observed on a real machine while following an item below. The offline suite replaces Podman, SSH, and the agents with fakes, so an offline result is never a manual check, and it does not show that the user namespace, the `no-new-privileges` option, the network, the mounts, or the limits isolate a sandbox. Only runs against real Podman can show that.
- The **live suite** is a separate gate with its own records. This checklist does not repeat what the live suite covers for agents: installing an agent through npm, the installed version in `agents status`, a session that keeps running after detaching, `--stop`, and disabling. The live suite signs in nowhere, so the logins are here.
- **Release evidence** is the record described under [The record](#the-record): one file for one run on one preview commit, uploaded to that preview's GitHub prerelease. A run that has not happened produces no record, and a record is never edited to show a pass that was not observed.
- The **runner** `manual-check` ([below](#the-runner)) performs the mechanical steps of the items, records what each command printed and how it exited, and stops at every step only a person can perform. Its output is an observation, never an entry of the record: a pass is recorded only by a person, with `manual-check record add --human-observed`.

## Safety rules

These steps create, update, and delete sandboxes, install software, and edit SSH and `PATH` settings. Follow these rules for every item:

1. **Never run on a host whose state you care about.** Use a disposable virtual machine, or a dedicated operating-system account on a test machine. Rootless Podman keeps its images and containers per user account, and the SSH setup edits `~/.ssh/config` of the account it runs in. The clean-machine items need a machine that has never had `sandboxed-agents`.
2. **Use a dedicated controller group, never `default`.** The runner takes the group from `preflight --group` (default `manual-check`, which matches `^[a-z0-9][a-z0-9-]*$`), exports it to every command it runs, and proves at the preflight that the variable reaches a child process and that `sandboxed-agents list` shows no sandbox this checklist did not create. In a terminal of your own, export it before the first `sandboxed-agents` command and prove it the same way: the second line of each block starts a child process and must print `manual-check`, and `sandboxed-agents list` must then show no sandbox you did not create for this run. If the child prints nothing, or the list shows another sandbox, stop.

   ```sh
   export SANDBOXED_AGENTS_GROUP=manual-check
   sh -c 'echo "group: $SANDBOXED_AGENTS_GROUP"'
   sandboxed-agents list
   ```

   ```powershell
   $env:SANDBOXED_AGENTS_GROUP = 'manual-check'
   pwsh -NoProfile -Command '$env:SANDBOXED_AGENTS_GROUP'
   sandboxed-agents list
   ```

   The group's containers are named `sandboxed-agents.manual-check.NAME`, and its SSH host entries `NAME.manual-check`.
3. **Images are shared.** An image name contains no controller group, so every group of the same executable uses the same images. Run `build` only where an item calls for it (once, in [Persistence and update](#persistence-and-update)), and do not use the machine for other work that depends on `sandboxed-agents` images.
4. **Name sandboxes as the items do** (`chk-persist`, `chk-vs`, `chk-base`, `chk-native`, `chk-int`, and the guide's `agent01`). Each item says when its sandbox is removed with `remove NAME --volumes`. `chk-persist` lives through several items and is removed last.
5. **Use accounts you can revoke.** Revoke what you granted only at the end, in [Final cleanup](#final-cleanup), because the earlier items need the logins to still exist.
6. **Never write a token, one-time code, login URL, or account secret into notes or the record.** The record has no field for them, and nothing may be added. The runner's files hold command output and exit statuses, readable by the operator only; they never leave the host.
7. **Create no release tag and publish nothing.** This checklist only reads a preview that already exists. Uploading the record is a separate maintainer step ([Upload](#upload)).

## Setup

Run the whole checklist for one **preview commit**: the full 40-character SHA of the commit that a preview tag (`vX.Y.Z-preview.YYYYMMDD.N`) points to, which is also the commit recorded in the record. Take the executables from that preview's prerelease and verify them as in the [quick guides](quick-guide-linux.md#3-verify).

Platforms are `linux` (Linux amd64, rootless Podman 4.4.0 or newer) and `windows-11` (Windows 11 x64, Podman 4.7.0 or newer with a rootless WSL2 machine); see [host prerequisites](host-prerequisites.md). Each item says where it runs. For an item that allows either platform, the record names the one you used.

### The runner

The runner is the Go command `manual-check` in `internal/manualcheck/cmd/manual-check`. Build it from the repository at any commit and copy it to the test machine; it is a tool of this checklist, not a release file, and its version does not need to be the preview's:

```sh
go build -o manual-check ./internal/manualcheck/cmd/manual-check
```

Every command of the runner belongs to one **run**, named by `--run ID`, with a private run directory below `$XDG_STATE_HOME/sandboxed-agents-manual` (`~/.local/state/sandboxed-agents-manual` when the variable is unset; `%LOCALAPPDATA%\sandboxed-agents-manual` on Windows), or below `--parent DIR`. A fresh retest gets a new run ID; a run directory is never reused. The runner records, for every step it runs (each command of the product, the installers, and the tools that an item executes), the command line, the process ID, the UTC start and end, the exit status, and the output, as files of the item's directory, readable by the operator only. The state an item compares (the Podman image, container, and volume queries, and the preflight's host diagnostics) is saved as the output files the item names, without a step record of its own. Each item directory holds a `verdict.txt` to which every runner stage of the item adds one block: the UTC time, the stage, `result=ok` or `result=deviation`, and what it observed. Its first line is the aggregate, `aggregate=deviation` as soon as any stage of the run deviated, and no later stage clears it: a stage run again adds a block, and a clean aggregate needs a new run. Where an item allows a repeated attempt, such as a new capture ID in `installers-no-podman`, each attempt keeps its block, and the item's rules say which attempt is evidence. Where a person has to act, a `human-steps.txt` holds the exact steps. Nothing in these files is a pass of the record.

```text
manual-check preflight --run ID --commit SHA --version VERSION --asset-sha256 HASH [--executable PATH] [--executable-sha256 HASH]
```

The preflight is the first command of a run and the precondition of every other one: it runs `version` and checks that the executable prints `sandboxed-agents VERSION` and the `asset-sha256` of the preview (equal on both platforms), optionally compares the executable's SHA-256 with the `SHA256SUMS` entry of the prerelease, proves the controller group, records safe host diagnostics (Podman version, rootless mode, graph root, the user-namespace entries, `/dev/net/tun`, lingering, `check`, `podman system df`), takes the whole-engine inventory of images (full IDs with references), containers (with the image each references), and volumes, and checks the free space against `--min-free-bytes`: on Linux that of the filesystem that holds Podman's storage, on Windows that of the drive that holds the user profile, where the machine's disk lives. It writes `run.json`, which every item reads, so no item runs against an executable the preflight did not verify. On Windows it names the Podman machine the executable works on (`--podman-connection`, or the default machine) and uses it for every direct `podman` call.

**State an item must see unchanged.** Where an item says the state is captured before and after a command, the runner writes `list`, `podman ps --all`, `podman volume ls`, and `podman inspect` of the named sandbox and of its backup container into one file each and compares the two byte for byte; any difference fails the item. A missing object makes `inspect` print an error; both captures hold the same error, so the comparison still means something.

**The record.** `manual-check record` is the only writer of the record ([The record](#the-record)): `record init FILE --commit SHA` creates it, `record add FILE --name ID --result pass|fail --date YYYY-MM-DD --platform linux|windows-11` adds one check a person ran (a pass needs `--human-observed`; a rerun needs `--replace`), `record status FILE` lists the identifiers that are still not run, and `record validate FILE` applies the documented check. The top-level result becomes `pass` only when all 23 identifiers are present and passed.

## Order of the run

Several items share one long-lived sandbox, `chk-persist`, so that a login done early is still there when the update is observed. Run them in this order:

1. The [preflight](#the-runner), then [create `chk-persist`](#the-shared-sandbox-chk-persist).
2. The logins, all in `chk-persist`: [agent logins](#agent-logins), [GitHub](#github-login), [Azure device code](#azure-login-device-code), [Azure browser](#azure-login-browser), [Azure DevOps](#azure-devops-token), [T3 Connect](#t3-connect-login-and-link), [QR pairing over the tailnet](#qr-pairing-over-the-tailnet).
3. [Persistence and update](#persistence-and-update): the setup, the baseline round, then `stop` and `start`, then the one `build` and the `update`, with the same observations each time.
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
| `t3-connect-link` | [T3 Connect login and link](#t3-connect-login-and-link) | either |
| `t3-qr-pair` | [QR pairing over the tailnet](#qr-pairing-over-the-tailnet) | either |
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


### What the runner does and what a person does

Every identifier maps to one runner command, or to none, and to the steps a person performs. The runner never performs a login, answers a session prompt, operates an editor or a browser, opens an elevated terminal, or writes a pass.

| Identifier | Runner | A person |
|---|---|---|
| `agent-login-*` | none | the cancelled and the completed login, the answered prompt in a session |
| `github-login`, `azure-login-device-code`, `azure-login-browser`, `azdo-token` | none; `persistence setup` records the Azure account by hash afterwards | the logins and the tunnel |
| `t3-connect-link`, `t3-qr-pair` | none | the T3 Connect login, link, unlink, and logout with the phone; the tailnet login, the QR pairing, and the second sandbox |
| `vscode-remote-ssh` | `vscode prepare`, `vscode verify --file NAME`, `vscode cleanup` | the editor connection, the file, `whoami` |
| `npm-install-clean` | `npm-clean --tarball FILE` | provides the clean machine |
| `nuget-install-clean` | `nuget-clean prepare`, then `after-install`, `after-reinstall`, `after-remove` | `version` in a new terminal |
| `quick-guide-linux`, `quick-guide-windows` | none | the guide, start to end |
| `selection-pins-persist`, `git-identity-persist`, `credentials-persist` | `persistence setup`, `persistence observe --round N`, `final-cleanup` | the four sessions of each round |
| `update-complete` | `persistence make-outdated`, `persistence update` | the sessions of round 2 |
| `update-interrupted` | `interrupted-update` | nothing |
| `azure-toolchain-required` | `refusals toolchain` | nothing |
| `login-needs-terminal` | `refusals no-terminal` | nothing |
| `no-image-removal` | `persistence make-outdated` (before), `final-cleanup` (after and compare) | nothing |
| `installers-no-podman` | `installers configure`, `installers prepare --method M`, `installers summarize` | the elevated observer, the Process Monitor window, the four steps in the item shell, the review of the evidence |
| `preview-attestation` | `attestation --tag TAG` | nothing |

## Items

### The shared sandbox `chk-persist`

Create it with the `azure` toolchain, which the Azure and Azure DevOps logins need, and with `opencode` enabled. Install no SSH setup yet; the Azure browser item needs the sandbox without one first:

```sh
sandboxed-agents up chk-persist --with azure --agents opencode
sandboxed-agents list
```

The first `up` builds the images, which takes a while.

**Every login, workflow, and observation below runs with the current executable**, the one built from the commit under test and verified by the preflight. A result observed with any other executable does not count for that commit.

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

### T3 Connect login and link

Identifier `t3-connect-link`. One T3 account login for the T3 Code Tool, ending in the sandbox's environment visible in the T3 mobile app, then unlinked and signed out again. This is a T3 account, not an agent provider login; the agent logins above stay as they are. You need a T3 account signed in on a phone with the T3 mobile app, and the sandbox needs outbound access to port 7844.

1. `sandboxed-agents tools enable chk-persist t3code`. **Expected:** `t3code enabled: version V (channel latest); service running`, and `sandboxed-agents tools status chk-persist t3code` shows `service running` and `T3 Connect not signed in, not linked`.
2. Start `sandboxed-agents tools connect chk-persist t3code login` in an interactive terminal, wait for the URL and the code, and press `Ctrl+C`. **Expected:** non-zero status, `t3code connect login was interrupted`, and `tools status` still shows `not signed in`.
3. Run it again, open the URL in a browser on your machine, enter the code, and approve. **Expected:** upstream's success message and exit status zero; `tools status` shows `T3 Connect signed in`.
4. `sandboxed-agents tools connect chk-persist t3code link`, approving in the browser when asked. **Expected:** exit status zero, `t3code service restarted; the link takes effect`, and `tools status` shows `linked` with `relay client available` and, once the relay has connected, `relay reachable`.
5. On the phone, in the T3 mobile app signed in to the same account, the sandbox's environment appears; open it and send a short prompt to an agent in `chk-persist`. **Expected:** the environment lists the sandbox and the prompt is answered.
6. `sandboxed-agents tools connect chk-persist t3code unlink`. **Expected:** exit status zero; `tools status` shows `signed in` and `not linked`; the environment disappears from the app or shows as unavailable.
7. `sandboxed-agents tools connect chk-persist t3code logout`. **Expected:** exit status zero; `tools status` shows `not signed in`.
8. In `shell chk-persist`, `gh auth status` and `az account show` report the same logins as before this item. **Expected:** unchanged; T3 Connect touched no agent or integration login.

Record `t3-connect-link`. T3 Code stays enabled in `chk-persist` for the later items.

### QR pairing over the tailnet

Identifier `t3-qr-pair`. A phone in your tailnet scans the QR code of `chk-persist` and opens the sandbox's T3 Code over the tailnet origin, which the T3 Connect item above does not exercise. You need a Tailscale account with MagicDNS and HTTPS certificates enabled at `https://login.tailscale.com/admin/dns`, a phone in that tailnet with the T3 mobile app, and a browser to approve the node. Each sandbox becomes its own device in your tailnet ([ADR 0010](adr/0010-t3-code-pairs-over-an-in-sandbox-tailnet-node.md)).

1. `sandboxed-agents tools status chk-persist t3code`. **Expected:** `tailnet node absent; run tools tailscale chk-persist t3code login`.
2. `sandboxed-agents tools pair chk-persist t3code`. **Expected:** non-zero status, `t3code tailnet node not signed in; run tools tailscale NAME t3code login`, and no code: `tools pair chk-persist t3code --revoke` prints `no unused pairing code`.
3. Start `sandboxed-agents tools tailscale chk-persist t3code login` in an interactive terminal, wait for `To authenticate, visit:` with the URL, and press `Ctrl+C`. **Expected:** non-zero status, `t3code tailscale login was interrupted`, and `tools status` shows `tailnet node absent` again.
4. Run it again, open the URL in a browser, and approve the device. **Expected:** `t3code tailnet node signed in as chk-persist.<your tailnet>.ts.net; origin https://chk-persist.<your tailnet>.ts.net/`, exit status zero, and the device `chk-persist` appears in your tailnet admin console.
5. `sandboxed-agents tools pair chk-persist t3code`. **Expected:** upstream's QR code, `Pairing URL: https://chk-persist.<your tailnet>.ts.net/pair#token=…`, `Expires:` five minutes ahead, and the note that the code is single-use and that the command does not confirm the scan. Upstream's note that the HTTPS endpoint has not answered yet may appear; it is expected.
6. Scan the QR code with the T3 mobile app on the phone within five minutes. **Expected:** the phone pairs and opens the sandbox's T3 Code; send a short prompt to an agent in `chk-persist` and get an answer. `tools status chk-persist t3code` shows `Serve maps the origin to the service` and the phone under `paired devices`.
7. Scan the same code again, or open its URL again on the phone. **Expected:** the phone reports the code as invalid; `tools pair chk-persist t3code --ttl 1m` issues a new one, which you leave unscanned.
8. `sandboxed-agents tools pair chk-persist t3code --revoke`. **Expected:** `revoked 1 unused pairing code`; the paired phone stays connected.
9. `sandboxed-agents restart chk-persist`. **Expected:** `t3code service running` and `t3code tailnet node running`; on the phone the sandbox's T3 Code is reachable again without a new pairing.
10. `sandboxed-agents up chk-pair2 --with none`, `tools enable chk-pair2 t3code`, `tools tailscale chk-pair2 t3code login` with approval, and `tools pair chk-pair2 t3code`. **Expected:** the origin is `https://chk-pair2.<your tailnet>.ts.net/`, distinct from the first; the phone scans it and sees `chk-pair2`'s T3 Code, not `chk-persist`'s. Then `remove chk-pair2 --volumes`, and remove the device `chk-pair2` from the tailnet admin console if it is still listed.

Record `t3-qr-pair`. The node of `chk-persist` stays signed in for the later items.

### Persistence and update

Identifiers `selection-pins-persist`, `git-identity-persist`, `credentials-persist`, and `update-complete`. They run in `chk-persist`, after all seven login items, so that the real agent, GitHub, Azure, Azure DevOps, T3 Connect, and tailnet logins are present when the lifecycle commands run. A fake Git credential supplements the real ones but does not prove that they survived; the real logins do.

**Setup, once:**

```text
manual-check persistence setup --run ID --github-repo https://github.com/OWNER/REPO.git --azdo-organization https://dev.azure.com/ORGANIZATION --azdo-repo https://dev.azure.com/ORGANIZATION/PROJECT/_git/REPO
```

It reads the installed version `V` of `opencode` from `agents status` and pins it with `agents enable chk-persist opencode --version V`; runs `integrations config chk-persist git identity --name "Manual Check" --email "manual-check@example.invalid"` and `integrations config chk-persist git credentials`; stores a fake credential for `example.invalid` with `git credential approve` in `shell chk-persist`; records the configuration the update must keep (`podman inspect` of the image, the memory, CPU, process, and shared-memory limits, the toolchain and port labels, and the mount names and destinations) as `config-before.txt`; and notes the Azure account of `az account show` by its SHA-256 only, so that every round can compare it without the name being in any file.

**The observation set.** Run it three times, as **round 0** (now), **round 1** (after `stop` and `start`), and **round 2** (after `update`). `N` is the round number. Every commit is made fresh in that round, in a fresh repository, so no old commit is read:

```text
manual-check persistence observe --run ID --round N
```

1. For each of `copilot`, `claude`, `codex`, and `opencode`, the runner checks that `agents status chk-persist AGENT` shows the agent enabled and no running session, and prints the person's step: `agents session chk-persist AGENT` answers a prompt such as `say hello` with no sign-in prompt; detach, then `agents session chk-persist AGENT --stop`. A running session would make `stop` and `update` refuse, so none may be left running.
2. `agents status chk-persist opencode` shows version `V` and the pin.
3. In `shell chk-persist`: `gh auth status` shows `github.com` logged in, and `GIT_TERMINAL_PROMPT=0 git clone https://github.com/OWNER/REPO.git /workspace/gh-N` succeeds without a prompt (round 0 reuses `/workspace/gh-0`).
4. In `shell chk-persist`: `az account show` prints the account noted at login (compared by hash), `GIT_TERMINAL_PROMPT=0 git clone https://dev.azure.com/ORGANIZATION/PROJECT/_git/REPO /workspace/azdo-N` succeeds without a prompt (round 0 reuses `/workspace/azdo-0`), and `az devops project list --organization https://dev.azure.com/ORGANIZATION --output table` lists projects.
5. In `shell chk-persist`, two new commits of this round, one in the new repository `/workspace/repo-N` and one in `/workspace/gh-N`, each made with `git commit --allow-empty -m round-N`, print their author with `git log -1 --format='%an <%ae> %s'`.
6. `grep -c example.invalid /home/agent/.git-credentials` prints `1`.

**Expected, in every round:** steps 1 to 4 show the agents and the three integrations still signed in, with no sign-in prompt; step 2 shows version `V` pinned; step 5 prints `Manual Check <manual-check@example.invalid> round-N` for both commits; step 6 prints `1`. The runner's verdict covers steps 2 to 6 and the statuses of step 1; the answered prompts of step 1 are what you observed.

**The sequence:**

1. Round 0: the observation set, as the baseline.
2. `sandboxed-agents stop chk-persist`, then `sandboxed-agents start chk-persist`. Round 1.
3. Make the image outdated, then update, all with the current executable:

   ```text
   manual-check persistence make-outdated --run ID
   manual-check persistence update --run ID
   ```

   `make-outdated` saves the image inventory for the [image check](#no-command-removes-an-image) as `images-before`, notes the container's image and the image its tag names (`TAG` is the container's `Config.Image`; both IDs are the same), runs `sandboxed-agents build` **once**, and checks that `list` shows `chk-persist` as outdated and that the tag now names a **different** image. The `build` rebuilds the base image and every existing toolchain set of this executable, here the recorded `azure` set, anew, and marks the existing sandbox as outdated. This is documented behavior ([Build, refresh, and reuse](images.md#build-refresh-and-reuse)). It is the only `build` in the whole checklist, and the [image check](#no-command-removes-an-image) reuses it. If the tag still names the container's image, the update has nothing to replace, the item cannot be observed, and the runner stops. `update` runs the plain `sandboxed-agents update chk-persist` once, with no `--with`, so the toolchain set stays the recorded `azure` set; records the configuration again as `config-after.txt`; runs `ssh chk-persist.manual-check true` without an interactive terminal; and evaluates the evidence, comparing the mount names as a set. `persistence verify-update` repeats the evaluation of the recorded evidence without running the update again.
4. Round 2.

**Expected after the update (`update-complete`):** the update ends with exit status zero; the image ID differs from the one recorded and equals the image the tag names after the `build`; the volumes (the mount names), the port, the memory, CPU, process, and shared-memory limits, the workspace, and the toolchain set (`azure`) are the same as recorded; the agent selection and the credentials are the same, as round 2 shows; and `ssh chk-persist.manual-check true` connects without a host key prompt.

**Identifiers:** `selection-pins-persist` passes on steps 1 and 2 of all three rounds; `git-identity-persist` on step 5 of all three; `credentials-persist` on steps 1, 3, 4, and 6 of all three; and, for the removal half of `credentials-persist`, on [Final cleanup](#final-cleanup); `update-complete` on the expected observations after the update.

### Toolchain requirement and no-terminal refusals

Identifiers `login-needs-terminal` and `azure-toolchain-required`. The first runs in `chk-persist` (which has the `azure` toolchain and an SSH setup), after the persistence rounds. The second uses two short-lived sandboxes.

```text
manual-check refusals no-terminal --run ID
manual-check refusals toolchain --run ID
```

For `login-needs-terminal`, the runner captures the state of `chk-persist` and what `gh auth status` and `az account show` report in `shell chk-persist` (the GitHub login line, the exit statuses, and the SHA-256 of the account name, never the name), then runs `integrations login chk-persist github`, `integrations login chk-persist azure device-code`, and `integrations login chk-persist azure browser`, each with standard input from nothing, then captures the state and the two outputs again.

**Expected:** each exits non-zero with a message that the login needs an interactive terminal. The two state captures are identical, and the two status commands print what they printed before, so the earlier logins are untouched. This item observes the refusal message, the non-zero status, and the unchanged state. It does not observe whether a workflow process started; the offline CLI tests prove separately that none does.

For `azure-toolchain-required`, the runner creates `chk-base` with `up chk-base` and `chk-native` with `up chk-native --with native`, both without the `azure` toolchain. For each, it captures the state, runs `integrations login SANDBOX azure device-code`, `integrations login SANDBOX azure browser`, and `integrations login SANDBOX azdo --organization https://dev.azure.com/example-organization` (an organization that does not exist), captures the state again, and checks `git config --global --get-regexp '^credential\.'` in `shell`. The toolchain check precedes the terminal check, so no terminal is needed to observe it. It removes both sandboxes with `--volumes` unless `--keep` is given.

**Expected:** each fails non-zero before anything is stored and before a token is read; the message names the `azure` toolchain and prints the complete command: `update chk-base --with azure` for `chk-base`, and `update chk-native --with azure,native` for `chk-native`. The printed set never contains `none`. Nothing changes: the `before` and `after` captures, which hold `list`, `ps`, `volume ls`, and `inspect` of the sandbox, are identical, and the credential query prints nothing.

### VS Code Remote SSH

Identifier `vscode-remote-ssh`. Follow [SSH access](ssh.md#connect-with-vs-code-remote-ssh) with VS Code and its Remote - SSH extension installed, in a sandbox of its own.

1. `manual-check vscode prepare --run ID` runs `sandboxed-agents up chk-vs --ssh-config`, checks that `ssh-config chk-vs` prints the host entry `chk-vs.manual-check`, and runs `ssh chk-vs.manual-check whoami` without an interactive terminal. **Expected:** `agent`, with no host key prompt.
2. In VS Code, run **Remote-SSH: Connect to Host...** and choose `chk-vs.manual-check`. Open the folder `/workspace`.
3. Create a file in the editor and run `whoami` in its integrated terminal.
4. `manual-check vscode verify --run ID --file NAME` checks that the file is present under `/workspace` in `sandboxed-agents shell chk-vs`.

**Expected:** the connection opens with no host key prompt; `whoami` prints `agent`; the file created in the editor appears in the sandbox. Then close the VS Code window and run `manual-check vscode cleanup --run ID`, which runs `ssh-config chk-vs --remove` and `remove chk-vs --volumes`.

### An interrupted update

Identifier `update-interrupted`. This needs a sandbox `chk-int` that an `update` replaces. The runner creates it fresh with the current executable and replaces it with `update chk-int --with native`, which changes the toolchain set and so always has something to replace (documented in [Updating a sandbox](images.md#updating-a-sandbox)). This does not depend on an older executable, and every command runs with the current one. The goal is to stop the `update` process after the old container was renamed to the backup and before the update finished, killing **only that process**.

```text
manual-check interrupted-update --run ID
```

1. The runner creates the sandbox with `sandboxed-agents up chk-int`.
2. It starts `sandboxed-agents update chk-int --with native` as a child process it holds a handle to, polls for the backup container for at most two minutes, and kills that one process through its handle; it never matches a process by name, and it records the process ID and its identity. A `podman` call that was already running when the process was killed may still finish; that is part of a real interruption. If the update finished before the kill, or the backup never appeared, the item cannot be observed that time: the runner stops with `not observed`; repeat with a fresh sandbox, and the item does not pass until it is observed.
3. It captures the state **before** the commands that must change nothing, runs `sandboxed-agents check chk-int` and `sandboxed-agents list`, and then **every** command path that takes this sandbox's name, each with standard input from nothing, so that a missing terminal cannot be the reason for a refusal. The list, written as `refusals.txt` in the item's directory, holds every command path of the grammar that takes a sandbox name, except `check chk-int`, `list`, `update chk-int`, and `update --all`, which recover or report; commands that take no sandbox name (`version`, `build`, `check` without a name) are not part of it:

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

   It records each exit status and whether the output names `update chk-int` as `refusals-results.txt`, then captures the state again and compares it.
4. It runs `sandboxed-agents update chk-int`, then `sandboxed-agents update chk-int --with native`, and removes the sandbox with `--volumes` unless `--keep` is given.

**Expected:**

- `check chk-int` reports the leftover backup container and exits non-zero. It and every refused command change nothing: the two captures are identical, so `list`, `ps`, `volume ls`, and the `inspect` of the container and of the backup are the same.
- `list` shows the sandbox as `update interrupted`.
- Every line of `refusals.txt` shows a non-zero exit status and `names update chk-int: true`. A line that exits zero, or whose output does not name `update chk-int`, fails the item; if a command such as `stop` or `remove` was not refused and changed the sandbox, discard the sandbox and repeat with a fresh one.
- `update chk-int` recovers instead of updating, builds no image, and exits as the state determines: zero when the replacement is ready and the update completes; non-zero when it restores the old sandbox ("restored" in its output). Either way the volumes are unchanged, no backup remains, and the next `update chk-int --with native` updates normally, or reports `already up to date` if the recovery had completed the update. See [Updating a sandbox](images.md#updating-a-sandbox) for the cases.

### Quick guides

Identifiers `quick-guide-linux` (platform `linux`) and `quick-guide-windows` (platform `windows-11`). Follow the guide from its first step to a signed-in agent session on a clean machine, **using only that guide**. If you needed another document, a command did not work as written, or a step was unclear, the item fails. The runner takes no part in this item.

- [Linux quick guide](quick-guide-linux.md) on a clean Linux machine with Podman and OpenSSH installed as the guide lists. The tag is the preview's.
- [Windows quick guide](quick-guide-windows.md) on a clean Windows 11 machine.

Export the dedicated group before the first `sandboxed-agents` command of the guide, as in the [safety rules](#safety-rules). **Expected:** every command works as written, including download, checksum comparison, attestation verification, placing the binary on `PATH`, `check`, `up`, `agents enable`, `agents login`, and `agents session`, and the final session shows an answered prompt without a sign-in prompt. At the end, remove the guide's sandbox: `sandboxed-agents remove agent01 --volumes`.

### npm install on a clean machine

Identifier `npm-install-clean`. Use a machine on which `sandboxed-agents` has never been installed, with Node.js 18 or newer, Podman, and OpenSSH. Download `sandboxed-agents-VERSION.tgz` from the preview prerelease; a preview is never published to the registry, so the install uses the file ([npm package](npm-package.md#install)).

```text
manual-check npm-clean --run ID --tarball sandboxed-agents-VERSION.tgz
```

The runner refuses an existing install root (`~/chk-install`, or `--install-root`) or an installed `sandboxed-agents` command, so the clean-machine precondition is yours. It installs the tarball with `npm install --offline --no-audit --no-fund -g` and a prefix, cache, logs directory, and temporary directory under the install root, so that npm writes only there; runs the installed command's `version`, `check`, and `list`; unpacks a copy of the tarball, changes one byte of the executable of the host's platform, repacks it as `tampered.tgz`, and installs that into a second prefix under the install root; and removes the package with `npm uninstall -g` and the same prefix.

**Expected:** `version` prints the preview's version and `asset-sha256`, `check` reports the host, and `list` runs without a sandbox; the tampered install fails on the checksum and leaves no command; after the uninstall the command is gone.

### NuGet install on a clean machine

Identifier `nuget-install-clean`, platform `windows-11`. Use a clean Windows 11 machine with PowerShell 7 and no administrator session. Take `SandboxedAgents.VERSION.nupkg` from the preview prerelease ([NuGet command package](nuget-package.md#install)).

```text
manual-check nuget-clean prepare --run ID --package SandboxedAgents.VERSION.nupkg
manual-check nuget-clean after-install --run ID
manual-check nuget-clean after-reinstall --run ID
manual-check nuget-clean after-remove --run ID
```

`prepare` extracts the package into the install root (`%USERPROFILE%\chk-nuget`, or `--install-root`), unblocks the scripts, and records the user `Path` as stored in `HKCU\Environment`, with its registry kind and without expanding environment names. Each later stage runs `install-command.ps1` (twice: the install, then a same-version reinstall, not a distinct-version upgrade) or `remove-command.ps1` without administrator rights and records the stored `Path` again. Every stage first reads the elevation of its own process token and refuses, before it changes anything, when the terminal is elevated or the elevation cannot be read. After the install, open a new terminal and run `sandboxed-agents version`; that is your step.

**Expected:** the installer needs no administrator rights; `version` works in a new terminal; the stored `Path` differs only by the install directory entry, and existing entries, including unexpanded ones such as `%USERPROFILE%\bin`, are byte-for-byte unchanged, as is the value's kind; the reinstall changes nothing; after the removal the entry is gone and the stored `Path` equals the one recorded before the install. [The install check](#installers-issue-no-podman-call) adds the process-attributed write trace.

### Installers issue no Podman call

Identifier `installers-no-podman`, platform `windows-11`. Install, reinstall, and remove through **each** of three methods, and show two things: no Podman call happens, and the installers change no file and no registry value outside the install location and the user `PATH`. The only exception is the narrow [Windows bookkeeping](#windows-bookkeeping) that is proven row by row. The methods are the downloaded binary, the npm package, and the NuGet command package. NuGet exists only on Windows, so the item is run on Windows 11, covers all three methods there, and is recorded with the platform `windows-11`. A Linux run of the npm method is optional supplemental coverage ([below](#optional-supplemental-linux-coverage)); it does **not** replace the Windows run, and the item does not pass on Linux alone.

**Why a trace and not a comparison of the whole profile.** A signed-in Windows profile always holds files that system processes keep open and rewrite (the registry hives, the browser and shell caches), so hashing the whole profile before and after cannot finish and would blame the installers for other programs' writes. The Windows run therefore records every file system and registry write of the **installer's own process tree** with Process Monitor from Microsoft Sysinternals, and attributes each write to a process. Unrelated background processes are excluded by that attribution, not by exempting paths: **no write by the item shell or its descendants outside the allowed locations is tolerated**, whoever the writing program is. The only exception is a row that the review proves to be one of the kinds of [Windows bookkeeping](#windows-bookkeeping), with that kind's evidence. No path, key, or operation is exempt as a whole.

Official sources: the [Process Monitor page](https://learn.microsoft.com/en-us/sysinternals/downloads/procmon) of Microsoft Learn, and the Microsoft Learn blog post [Using Procmon in Command-line](https://learn.microsoft.com/en-us/archive/blogs/yash/using-procmon-in-command-line) for `/Quiet`, `/Minimized`, `/BackingFile`, and `/Terminate`. The Help that is installed with your version of Process Monitor is authoritative for its menus, filters, and command line; where it differs from the steps below, follow it and write down what you did.

**The evidence package.** The scripts of `internal/manualcheck/windows/installers` perform the capture and the review: a normal **item shell** in which the installers run, an elevated **observer** that starts a Windows kernel trace and then Process Monitor, confirms the window with you, waits for the method, stops both recorders, exports the logs, and reviews them with the rules of [Inspecting a log](#inspecting-a-log), [Windows bookkeeping](#windows-bookkeeping), and [Kernel trace stacks for the NTFS log](#kernel-trace-stacks-for-the-ntfs-log). The review's classification is automatic and its counts are written down; it is a local triage, and a method passes only on your review of its evidence. The package's own tests, in its `tests` directory, exercise the reviewer, the readiness guards, the kernel-trace reader, and the process identity guard with synthetic exports; the Go test `TestInstallerHelperSuite` runs them.

**Prerequisites a person arranges before the run** (not something an agent session does):

- Process Monitor, downloaded by you from the official page above and unpacked into a directory of your choosing, and the `wpr.exe` and `tracerpt.exe` of the Windows installation.
- The **frozen inputs** on the test machine, with a manifest that names them: the verified Windows binary and a **second verified copy of the same preview binary** for the replacement, the npm tarball, the `.nupkg`, and `SHA256SUMS` of the preview, each with its SHA-256, and a portable Node.js ZIP for the npm method with its SHA-256 from the Node.js project's checksum file. The manifest is a JSON object with `schema_version` 1, the preview `commit`, `files` with `binary`, `second_binary`, `npm_package`, `nuget_package`, and `sums` (each `path` and `sha256`), and `node` with the `directory` the ZIP unpacks to and its `zip_sha256`.
- A **runs directory outside your user profile** that you can write to, for example on a test volume prepared for you. The logs and exports go beneath it. These files can hold user names, paths, and environment details, so they are kept **locally only**, are never uploaded, and are deleted at the end of the item; the record has no field for them.
- A fresh disposable virtual machine, so that the run starts clean, with the preflight done and this repository's package directory copied to it.
- A **separate elevated terminal, the observer**, opened by a person who consents to running Process Monitor and the kernel trace with administrator rights on the test machine. Only the observer is elevated. The installers run in a normal, **non-elevated** terminal, the item shell, and the item fails if an installer needs more than that.

**Configure once per candidate**, in a normal terminal. The configuration pins, before any capture, everything the evidence depends on: the candidate, the revision of this document, every script, test, and profile of the package, the frozen inputs against their manifest, the Node ZIP, and the three native tools, each by SHA-256. The scripts refuse any file whose hash changed.

```text
manual-check installers configure --candidate SHA --criterion-file docs\manual-checklist.md --package-directory PACKAGE --inputs-manifest MANIFEST --node-zip NODE.zip --procmon PROCMON\Procmon64.exe --runs-root RUNS --min-free-bytes 21474836480 --out installers.json
```

**The capture, for each method**, in the order `binary`, `npm`, `nuget-root`, `nuget-default`. Each method has its own one-shot capture run; a directory that exists is never reused.

1. **Normal terminal, prepare.** `manual-check installers prepare --run ID --configuration installers.json --method METHOD` (with `--capture-id METHOD-NAME` for a repeated attempt; a capture run directory is never reused) verifies the configuration again, creates the capture run directory under the runs root and a fresh install root `%USERPROFILE%\chk-install-METHOD-ID` with `bin`, `prefix`, `cache`, `logs`, and `tmp`, puts a **fake Podman** (`podman.cmd`, which appends its arguments to a `calls` file and exits with 1) first on `PATH`, unpacks the pinned Node runtime into the install root and, for the NuGet methods, the package, points `TEMP`, `TMP`, and `PSModuleAnalysisCachePath` into the install root, sets `POWERSHELL_TELEMETRY_OPTOUT`, `POWERSHELL_UPDATECHECK=Off`, and `npm_config_update_notifier=false` (process-only settings that PowerShell reads at startup, per [about_Environment_Variables](https://learn.microsoft.com/en-us/powershell/module/microsoft.powershell.core/about/about_environment_variables?view=powershell-7.5)), and opens the **item shell**, a fresh PowerShell 7 that stays open for the whole method. The item shell warms up what the steps need (the environment snapshot, Node and npm, the cmdlets, the NuGet scripts unblocked) before any trace, waits for the module analysis cache, records its own process identity, and prints `ITEM_SHELL_READY`. Every file the methods write must land inside the install root, **including npm's prefix, cache, temporary files, and logs**; the default location of the NuGet command package in method `nuget-default` is the one other allowed location.
2. **Observer (elevated terminal), start the capture.** `& 'PACKAGE\Start-InstallerCapture.ps1' -RunDirectory RUN`, with the run directory the preparation printed. The observer verifies the configuration and the Microsoft signatures of the native tools, proves the item shell's identity (process ID, executable, start time, command line), writes `pins.json` with every pinned file, records the volume mapping, starts the kernel trace under a named instance and checks that its file-write stacks are active, and only then starts Process Monitor with `/AcceptEula /Quiet /NoConnect /BackingFile RUN\procmon.pml`. `/Quiet` suppresses the filter prompt at startup. It does **not** guarantee that no filter is active, because Process Monitor can load the filter it saved earlier.
3. **Observer, make the capture complete.** In the Process Monitor window, **before** the item shell runs anything: **Filter**, **Filter...**: press **Reset** and then **OK**, so that no saved filter remains. On the **Filter** menu, make sure **Drop Filtered Events** is **not** checked, so that nothing is lost even if a filter is set by mistake, and **Enable Advanced Output** is off. On the toolbar, make sure the buttons for **file system activity**, **registry activity**, and **process and thread activity** are all enabled, and that the **Date & Time**, **Thread ID**, and **Duration** columns are shown. The event counter in the status bar increases. Press Enter in the observer; it records your confirmation and prints `CAPTURE_READY`. Escape cancels before anything ran. The window is the authority for its own controls; follow the Help installed with your version of Process Monitor if a menu item is named differently, and write down what you did. The capture has a five-minute budget.
4. **Item shell, the steps, one command at a time and in this order:** `Begin-InstallerMethod`, which checks the observer's ready record and takes the first environment snapshot; `Invoke-InstallerStep install`; `Invoke-InstallerStep reinstall`; `Invoke-InstallerStep remove`. Run nothing else in the item shell in between, and never paste them as one block. Each step records its exit statuses and a snapshot of every value of `HKCU:\Environment` with its kind and raw, unexpanded content, kept in memory so that the shell writes no file while a trace runs. The steps are:
   - `binary`: copy the verified binary to `bin\sandboxed-agents.exe` and run `version`; copy the second verified copy over it (a same-version overwrite, which claims nothing about a distinct-version upgrade) and run `version`; delete it.
   - `npm`: `npm install --offline --no-audit --no-fund -g` of the tarball with the prefix, cache, and logs directory under the install root, then `version` through the installed command; the same install again (a same-version reinstall); `npm uninstall -g` with the same prefix.
   - `nuget-root`: `install-command.ps1 -InstallDirectory INSTALL_ROOT\nuget -NoPathUpdate` with the call operator inside the item shell, so no new PowerShell starts in a capture; the same again; `remove-command.ps1` with the same arguments.
   - `nuget-default`: `install-command.ps1` with its default arguments, so the installer uses `%LOCALAPPDATA%\Programs\sandboxed-agents` and the user `PATH`; the same again; `remove-command.ps1`. The allowed locations of this method are the install root, `%LOCALAPPDATA%\Programs\sandboxed-agents` (and its `Programs` parent if the installer has to create it), and the user `PATH` value.
5. **Observer, stop and export.** When the method's completion marker appears, the observer proves the identity of the Process Monitor process it started once more, terminates it with `/Terminate`, waits until it has exited, stops its own kernel-trace instance, records the volume mapping again, and prints `CAPTURE_STOPPED`. Run `Save-InstallerMethod` in the item shell, which writes the step results and the environment snapshots only after the stop record names both recorders as stopped. The observer then exports the unfiltered CSV and the XML with stacks from the saved log, the raw-time and summary XML of the kernel trace, the saved-stack and extended-attribute evidence, reviews the trace and the environment snapshots, hashes every file, writes `result.json`, and prints `REVIEW_COMPLETE`. If the observer timed out waiting for the save, save first, then run `& 'PACKAGE\Export-InstallerCapture.ps1' -RunDirectory RUN` once in the elevated terminal; an export is never repeated or overwritten.
6. **Normal terminal, summarize.** `manual-check installers summarize --run ID --run-directory RUN` records the reviewer's counts under the item: the verdict of the automatic triage, the verdict of the environment review, the rows exported, the bookkeeping rows, the unknown rows, the changes outside the allowed locations, whether the kernel trace was complete, and the size of the fake Podman's `calls` file. A run whose capture or export failed is reported as such.
7. **Review the evidence** under [Inspecting a log](#inspecting-a-log). The exported CSV, the XML with stacks, the kernel exports, and the review's private report are in the run directory; the review's summary names every accepted bookkeeping row with its evidence, every incomplete row, and every unknown. Then exit the item shell and wait until its process has ended, so that it cannot write its module analysis cache into a tree you are about to delete.

**Expected:**

- The `calls` file of the fake Podman is empty or does not exist, and no process of the attributed tree is `podman` or `podman.exe`, so no Podman call was issued by any method.
- For methods `binary`, `npm`, and `nuget-root`, the required review finds no change outside the install root and no unknown or incomplete row. Accepted named-pipe and bookkeeping rows do not count as changes. The environment snapshots are identical at every step.
- For `nuget-default`, the required review finds no change outside the allowed locations and no unknown or incomplete row, and the environment snapshots show: `Path` gained the install directory entry between `start` and `after-install`, is unchanged between `after-install` and `after-reinstall`, and lost the entry again between `after-reinstall` and `after-remove`, so it equals `start`; its value kind is unchanged; no other value changed. Entries that were there before, including unexpanded ones such as `%USERPROFILE%\bin`, are byte-for-byte unchanged.
- A capture that never counted events, an item shell that is not in the process tree, a log that cannot be opened, a kernel trace that lost events or buffers, or a pin that changed is no evidence: the scripts refuse to export it, and you repeat the method. A write outside the allowed locations by the installer, an installer child process, or the item shell itself is a failure, and nothing is recorded as passed in its place. Accepted named-pipe rows and proven [Windows bookkeeping](#windows-bookkeeping) are the only exceptions, and approval by a person never replaces their evidence. Sandboxes that exist keep running across the reinstall of any method.

**Keep nothing but the verdict.** Keep the logs locally until you have the verdict for every method, including the call stacks that [Windows bookkeeping](#windows-bookkeeping) takes from them. Do not upload them or paste their content into the record or a ticket. When all methods have their verdict, delete the install roots and the capture run directories. Leave the runs directory itself as you found it.

#### Inspecting a log

In the observer's Process Monitor, open the saved log (**File**, **Open**). The Process Monitor page describes the tools used here: filters can be set on any field without losing data, and the Process Tree tool shows the relationship of all processes in a trace.

1. **Select the process tree.** Open **Tools**, **Process Tree**. Find the item shell, the `pwsh.exe` whose PID the run directory's `item-context.private.json` records as `pid`, and write down it and **every descendant**, including the ones that have already exited (they stay in the tree): `node.exe` and `cmd.exe` for npm, the installed executable for `version`. The NuGet scripts run inside the shell itself, so they add no child. This is the set of attributed processes. **Assert that no process in this set is `podman` or `podman.exe` (or has `podman` in its image path or command line), whatever the `PATH` was**: the fake Podman only catches calls that go through `PATH`, and a call by absolute path would otherwise pass unnoticed. Any such process fails the method. If the item shell is not in the tree, you opened the wrong log or the capture did not run, and the item fails for that method. Note the total event count of the log; zero or very few events means the capture was not complete.
2. **Filter to the attributed events.** Open the filter dialog (**Filter**, **Filter...**, or Ctrl+L) and press **Reset** first. Add **Include** conditions `PID is N` for the shell and for each descendant. Conditions on the same field are alternatives, so the view shows the events of any of them. Add **Include** conditions `Event Class is File System` and `Event Class is Registry`. Set **no** `Result`, **no** `Operation`, and **no** `Detail` condition for the review below. The view now holds every file system and registry event of the attributed processes, whatever its result.
3. **Navigation aids, not a definition.** To find the likely changes first, you may temporarily add `Operation is X` conditions for `WriteFile`, `SetEndOfFileInformationFile`, `SetAllocationInformationFile`, `SetDispositionInformationFile`, `SetRenameInformationFile`, `RegCreateKey`, `RegSetValue`, `RegDeleteKey`, `RegDeleteValue`, `RegSetInfoKey`, or look at `CreateFile` rows with `Detail contains OpenResult: Created` and similar. **These lists are finite and incomplete.** They leave out, for example, `SetBasicInformationFile` (times and attributes), `SetSecurityFile`, `SetEAFile`, `SetLinkInformationFile` (hard links), reparse-point and other `FileSystemControl` requests, and the registry operations that load, restore, replace, rename, or secure a key. Remove the aids before step 4; they never replace it.
4. **Required review of ALL remaining events.** With the aids removed, list the distinct values of the **Operation** column in the view (sort by the column and scroll, or use **Tools**, **Count Occurrences...** on **Operation**). Classify **every** operation value that occurs, using the table below. An operation not in the table, or one you cannot classify, is **unknown**.

   | Class | Operations | What you do |
   |---|---|---|
   | Changes nothing | Reads, queries, enumerations and closes: `ReadFile`, `CloseFile`, `Query...` (`QueryInformationVolume`, `QueryDirectory`, `QueryOpen`, `QueryNameInformationFile`, `QueryStandardInformationFile`, `QueryBasicInformationFile`, `QuerySecurityFile`, `QueryEAFile`, ...), `SetPositionInformationFile`, `LockFile`, `UnlockFileSingle`, `FlushBuffersFile`. Also these exact file system notifications: `IRP_MJ_CLOSE` (the last handle of a file object was closed), `FASTIO_ACQUIRE_FOR_CC_FLUSH` and `FASTIO_RELEASE_FOR_CC_FLUSH` (a file system resource is acquired or released around a cache flush; any data the flush writes appears as its own `WriteFile` row), `FASTIO_RELEASE_FOR_SECTION_SYNCHRONIZATION`, and `FASTIO_MDL_READ_COMPLETE` (completion of a read). For the registry `RegOpenKey`, `RegQueryKey`, `RegQueryValue`, `RegEnumKey`, `RegEnumValue`, `RegCloseKey`, `RegQueryKeySecurity`, `RegFlushKey` | Dismiss the class. Confirm once that its rows are what the name says. |
   | Changes content, metadata, names, security, links, or registry | `WriteFile`, `SetEndOfFileInformationFile`, `SetAllocationInformationFile`, `SetValidDataLengthInformationFile`, `SetDispositionInformationFile`, `SetDispositionInformationEx`, `SetRenameInformationFile`, `SetRenameInformationEx`, `SetLinkInformationFile`, `SetShortNameInformation`, `SetBasicInformationFile`, `SetEAFile`, `SetSecurityFile`, `SetFileObjectIdInformation`, and for the registry `RegSetValue`, `RegSetInfoKey`, `RegDeleteKey`, `RegDeleteValue`, `RegRenameKey`, `RegLoadKey`, `RegUnLoadKey`, `RegRestoreKey`, `RegReplaceKey`, `RegSaveKey`, `RegSetKeySecurity` | **Every** row is a change. Apply the allowed locations (step 5). |
   | Depends on the details | `CreateFile`, `RegCreateKey`, `CreateFileMapping`, `FileSystemControl`, `DeviceIoControl` | Resolve every row from its **Detail** and the events that follow it, as below. |

   Resolve the third class as follows:
   - `CreateFile` with `OpenResult: Created`, `Overwritten`, or `Superseded` (a creation or overwrite disposition that took effect) is **a change**. A `CreateFile` that opened an existing file with write-capable rights, for example `Write Attributes` or `Generic Write`, is **not by itself a change**: it counts only if the same process then produces a changing event for that path (a row of the second class), so look at the events that follow it for the same path and the same process. An open with nothing changing after it is not a change.
   - `RegCreateKey` with a created-new-key disposition is a change; one that opened an existing key is not.
   - `CreateFileMapping` is classified by its **Detail**. Process Monitor shows the section synchronization request there with its `SyncType` and `PageProtection`.
     - `SyncType: SyncTypeOther` creates no section. Its page protection is absent or zero, and it changes nothing.
     - `SyncType: SyncTypeCreateSection` with `PAGE_READONLY`, `PAGE_EXECUTE`, or `PAGE_EXECUTE_READ` changes nothing, because a write to such pages is an access violation.
     - Any other protection on a path outside the allowed locations is **unknown** until proven otherwise. This covers `PAGE_READWRITE` and `PAGE_EXECUTE_READWRITE`, and also `PAGE_WRITECOPY` and `PAGE_EXECUTE_WRITECOPY`. A mapped view written as read/write writes the file. Only a view mapped copy-on-write never writes the file, so the copy-on-write case needs positive evidence that every view of the section was mapped copy-on-write. The absence of an attributed `WriteFile` row is no such proof.
   - `FileSystemControl` and `DeviceIoControl` are classified by the control **name** in **Detail**, using the documented meaning of that control. A reparse point, hard link, sparse, compression, object-id, or encryption setting is a change; a query is not. A control shown only as a number, or one with no documented meaning, is **unknown**, whatever its result. `FSCTL_QUERY_FILE_REGIONS` (`0x90284`) is a query: it returns a list of the file's regions. Exactly one numeric control is resolved by its published value: `0x90390`, which is `FSCTL_QUERY_VOLUME_CONTAINER_STATE`. A row with that control and the result `INVALID DEVICE REQUEST` changed nothing, because the request is not a valid operation for the target and was rejected. Any other result for that control stays unknown, and so does every other numeric control. `FSCTL_WRITE_USN_CLOSE_RECORD` writes a record into the volume's change journal for the target file. It is therefore a change of that file, and the allowed locations apply to its target path.
   - **Results, for every row.** Also list the distinct values of the **Result** column. For the first class any result is fine; queries end with codes such as `NO MORE ENTRIES`, `BUFFER OVERFLOW`, or `END OF FILE`. For the second class, `SUCCESS` means the change happened, a plain failure (`ACCESS DENIED`, `NAME NOT FOUND`, `PATH NOT FOUND`, `NAME COLLISION`, `SHARING VIOLATION`) means nothing changed, and any other result is unknown until you resolve it. For the third class the result is part of the resolution: a failed open changed nothing, and an open that ended in `REPARSE` continues on another path, which you check as its own row. These exact pairs are also failures that changed nothing:
     - `CreateFile` with `NAME INVALID`: the object name is invalid.
     - `CreateFile` with `DELETE PENDING`: the file already has a delete pending.
     - `SetDispositionInformationFile` or `SetDispositionInformationEx` with `NOT EMPTY`: the directory to be deleted is not empty, so it was not deleted.

     A row of any class whose result is `FAST IO DISALLOWED` changed nothing. The request was not passed to the file system on the fast path, and the I/O manager may reissue it as an equivalent IRP. That IRP appears as its own row and is classified on its own. No other failure result counts as "changed nothing" for an operation whose meaning is unknown.
   - **An operation or a row that you cannot resolve prevents a pass.** Do not guess "probably harmless".
5. **Apply the allowed locations to every change.** For every row that is a change (the second class, and the rows of the third class that you resolved as changes), look at the **Path** column. A row is allowed only if its path is inside the install root (all methods) or, in `nuget-default`, inside `%LOCALAPPDATA%\Programs\sandboxed-agents` (or is the `Programs` directory itself), or is the registry value `HKCU\Environment\Path` in `nuget-default`.
   - **Named pipes.** Accept a row as communication between processes, not as a change of a file, only when both of these hold:
     - its path names a pipe below `\Device\NamedPipe\`;
     - its operation is `CreatePipe`, `CreateFile`, `ReadFile`, `WriteFile`, `SetPipeInformation`, `FlushBuffersFile`, `CloseFile`, `IRP_MJ_CLOSE`, or a `Query...` operation.

     `CreatePipe` creates the server end of a pipe instance, and the instance is deleted when its last handle is closed. `SetPipeInformation` sets a pipe's read mode and completion mode.

     A row on the bare `\Device\NamedPipe`, which names no pipe, is accepted as communication only when all of these hold:
     - it is a `WriteFile` or `SetPipeInformation` row;
     - its result is `SUCCESS`;
     - the same PID has a successful `CreatePipe` row earlier in the log.

     Every other operation on a pipe path, including `<Unknown>` operations, is resolved like any other row, or stays unknown.
   - **Windows bookkeeping.** A row outside the allowed locations is accepted only if it matches one of the kinds under [Windows bookkeeping](#windows-bookkeeping) exactly and carries that kind's evidence. A row that matches a kind but lacks its evidence is **incomplete**, not accepted.

   **Every other row is a change outside the allowed locations, and the method fails.** Do not exclude a path in the filter to make a row go away.
6. **Write down the verdict.** For each method, note the number of events reviewed, the operations and results you saw and their class, the number of changes, and why each accepted change is allowed. Note every accepted bookkeeping row with its kind and evidence, every incomplete row, and every unknown. A method with an unknown or incomplete row does not pass. A method with a change outside the allowed locations fails. The `Path` value of `nuget-default` must be written by the NuGet install script, which runs inside the item shell (the `pwsh.exe` of `item-context.private.json`) as `install-command.ps1`, and removed by `remove-command.ps1`, and `HKCU\Environment\Path` must be the only registry value changed.

#### Windows bookkeeping

Windows itself does some bookkeeping in the thread of an attributed process: the NTFS log, the code integrity signature cache, and the state of a registry handle. Such a row is accepted only when all of the following hold:

- It matches one kind below exactly: its operation, its result, and the exact form of its **Detail**.
- Its evidence is complete.
- The same path has no other change by an attributed process unless that change is itself accepted bookkeeping of the same family. Each row must qualify on its own evidence. The families are:
  - the NTFS log: repeated `ntfs-logfile-write` rows;
  - the signature cache: `ci-signature-cache-ea` and `ci-signature-cache-usn-close` rows on the same file;
  - handle tags: repeated `registry-handle-tags` rows.

  Any other change on that path is a change outside the allowed locations, and the method fails. That includes content, size, times or attributes, security, a rename, a link, a reparse point, a deletion, and an extended attribute without this evidence.

The rules for each accepted row:

- **Missing evidence.** A row that matches a kind but whose evidence is missing is **incomplete**. Examples are a call stack without frames, or a stack that does not show the named component. An incomplete row prevents a pass. It is never accepted on the grounds that it is probable.
- **Timing is not cause.** A change in an allowed location on the same volume at about the same time does not prove what caused the row.
- **Unmatched rows.** Any row that matches no kind, such as a different information class, protection, or control, is classified by steps 4 and 5 like any other row.
- **The verdict note.** Write down each accepted row with:
  - its row number;
  - its PID and process name;
  - its operation, path, result, and **Detail**;
  - its kind;
  - its evidence.

The call stack comes from the saved log (**Event Properties**, **Stack**, or an export that keeps stacks). Only for an `ntfs-logfile-write` row whose saved stack has no frames may the stack evidence come from elsewhere: a kernel trace of the same capture, under [Kernel trace stacks for the NTFS log](#kernel-trace-stacks-for-the-ntfs-log). It is recorded by module name and path. A frame counts as `Ntfs.sys` only when its module path is the system file `System32\drivers\Ntfs.sys` in the Windows directory of the captured host. A frame counts as `CI.dll` only when its module path is `System32\CI.dll` in that directory. A module of the same name anywhere else does not count.

| Kind | Rows it covers | Evidence required |
|---|---|---|
| `ntfs-logfile-write` | `WriteFile`, `SUCCESS`, path exactly `X:\$LogFile` of a local NTFS volume, **Detail** with both `Non-cached` and `Paging I/O`. `$LogFile` is the transactional logging file of NTFS. | No attributed process opened that path. This means no `CreateFile` row for it in the log. The row's call stack has frames and contains `Ntfs.sys`. A row whose saved stack has no frames may instead have the evidence under [Kernel trace stacks for the NTFS log](#kernel-trace-stacks-for-the-ntfs-log). |
| `ci-signature-cache-ea` | `SetEAFile`, `SUCCESS`, on a file outside the allowed locations. Windows caches the result of an image signature check in a kernel extended attribute, which only kernel mode can set. | Every `SetEAFile` row for that file has a call stack with frames that contains `CI.dll`, the Windows code integrity engine. After the capture, a read-only query of the file's extended attributes lists exactly one name, `$KERNEL.PURGE.ESBCACHE`, compared without regard to case. Example: `fsutil file queryEA`, with its exit code `0` and its time recorded. Any other name, `$KERNEL.PURGE.` names included, needs a primary source and a revision of this item that names it. |
| `ci-signature-cache-usn-close` | `FileSystemControl` with `Control: FSCTL_WRITE_USN_CLOSE_RECORD`, `SUCCESS`, on a file outside the allowed locations. The documented procedure for setting a kernel extended attribute first issues this control, to commit the file's pending journal records. | The row's call stack has frames and contains `CI.dll`. The same file has the extended-attribute query evidence of `ci-signature-cache-ea`. |
| `registry-handle-tags` | `RegSetInfoKey`, `SUCCESS`, **Detail** exactly `KeySetInformationClass: KeySetHandleTagsInformation, Length: 0`. The tags are stored in the key body, the kernel object of one open handle, not in the key. | The exact **Detail** above. Any other information class or length is a change. Microsoft documents this class only as reserved for system use. Acceptance rests on the published kernel research in [Sources for the review](#sources-for-the-review). |

Process Monitor does not show the name or the value of an extended attribute in a `SetEAFile` row. For `ci-signature-cache-ea`, the evidence therefore shows which component made the request and which names the file carries after the run. It does not show the content of each request or the attributes the file had before. Write this limit into the verdict note. A future capture can close the gap by querying the extended attributes of the affected files before the capture as well.

##### Kernel trace stacks for the NTFS log

Process Monitor can save a `WriteFile` row with `Paging I/O` without any stack frames. In every capture examined for this item, it did so for every paging write. Under the rule above, such an `ntfs-logfile-write` row is **incomplete**. This section gives the only other source for its stack evidence: a Windows kernel trace recorded in the same capture as the log. It applies only to `ntfs-logfile-write` rows whose saved stack has no frames. A row whose saved stack has frames is judged on those frames alone, and every other kind takes its stack from the saved log. The row still has to match the kind exactly, no attributed process may have opened the path, and the same-path family rule still holds.

This evidence does not say which trace event is the row. It shows two things. First, every write that the row's thread made to the same `$LogFile` anywhere in the trace has `Ntfs.sys` on its stack. Second, the trace saw at least as many such writes as the log has rows. The times recorded by the two tools are never compared, and their events are never matched by order.

A kernel trace counts as the stack source for a method only when **all** of the following hold. If one fails, every `ntfs-logfile-write` row of that method without frames stays **incomplete**, and the method is captured again. A kernel trace from another capture never counts, and neither does one added to a saved log later.

1. **Same capture.** The kernel trace started before Process Monitor started for the method, and stopped after Process Monitor had ended. The trace itself shows this: it contains the process start and the process end of the Process Monitor instance that wrote the method's log, identified by its command line. The trace records process and thread events, image events, file I/O events with their file names, and a stack walk for each file write.
2. **Nothing dropped.** The trace reports zero lost events and zero lost buffers. The log was captured as described above, with **Drop Filtered Events** off, and its exports report the same total event count.
3. **Fixed before the capture.** The SHA-256 of the recording profile was written down before the capture started. So was the SHA-256 of every tool that reads the trace or compares it with the log, and of each tool's tests. The verdict note gives these hashes. It also gives the SHA-256 of the log, of each export, of the trace file, and of each export of the trace.
4. **One process, one thread.** The row's PID and TID each name a single instance for the whole trace. The trace has no end of that process or thread followed by a new start with the same ID, and its thread events tie the TID to that PID. Every trace event used below names that TID in both its header and its payload. For the process, it names that PID or leaves the process unset.
5. **File and volume.** The trace's file name events give the write's file key exactly one name, `\Device\HarddiskVolumeN\$LogFile`. A read-only query of the drive letter's NT device name ties `\Device\HarddiskVolumeN` to the drive `X:` of the row (for example `QueryDosDevice`). It is made before and after the capture, with the same result both times. The query also shows a local, fixed NTFS volume.
6. **Module.** The trace's image events give `Ntfs.sys` at `\SystemRoot\System32\Drivers\Ntfs.sys` one base address and one size for the whole trace. `Ntfs.sys` is not loaded or unloaded during the trace, and no other image overlaps that range. A read-only query resolves `\SystemRoot` to the Windows directory of the captured host. A frame counts as `Ntfs.sys` only when its address lies in that range.
7. **Every write of the thread has the stack.** The row's **group** is every file write event in the trace by the row's thread on the file key of item 5, from anywhere in the trace. The group is not empty. Each event in the group has exactly one stack walk event linked to it, through the timestamp, process, and thread that the stack walk records. No other event of that thread has the same timestamp. The stack has frames, and at least one of them is in `Ntfs.sys`.
8. **The counts agree, without time.** A row and a trace event have the same **fingerprint** when they agree on PID, TID, byte offset, and length. They must also agree on each of the flags `Non-cached`, `Paging I/O`, and `Synchronous Paging I/O`: each flag is present in both or absent from both. For each fingerprint of the row's thread, the group has at least as many events as the log has `WriteFile` rows on `X:\$LogFile`. The log may contain both the creation and the exit of that thread. In that case, the trace must contain the thread's start and end too, and the two counts must be equal for every fingerprint of that thread. A count outside these rules leaves every `ntfs-logfile-write` row of the method without frames **incomplete**.

For each row accepted this way, the verdict note gives the following. Rows of one thread may share one entry for their group.

- the row's number, its thread, and its fingerprint;
- each trace event of the group, with its stack walk event, its frame count, and its `Ntfs.sys` frames;
- the image range of `Ntfs.sys`;
- the two volume queries;
- the start and end events of the Process Monitor instance in the trace;
- the counts of item 8.

The note also contains this limit, word for word: *"This evidence does not identify the trace event of a row. It shows that the kernel trace recorded every write of the row's thread to `$LogFile` with `Ntfs.sys` on its stack. It assumes that, while both tools run, the kernel trace records every write request that Process Monitor records, as a file write of the same thread with the same offset, length, and flags. Neither tool documents this; the counts in this capture are consistent with it."* The approval of a person never replaces any of these conditions. Delete the kernel trace and its exports together with the log, as described under **Keep nothing but the verdict**.

#### Reviewing a saved log again

A log may be reviewed again under a later revision of this item, without repeating the capture, only when all of these hold:

- the capture was complete as described above;
- the log and every export used are the unchanged files of that capture (their SHA-256 hashes match the ones recorded when they were made);
- the review concerns the commit that was captured.

When evidence needs a state that neither the saved log nor a read-only query can provide, the method is captured again.

A review under a new revision is a **new** verdict. It never changes an earlier verdict or an earlier record, and an earlier record stays as it was made. Its verdict note names all of the following:

- the captured commit;
- the revision of this item that was applied: the commit of this document, or the SHA-256 of the file before it is committed;
- the SHA-256 of any reviewer tool used;
- the SHA-256 of each source log and each export;
- every accepted bookkeeping row with its evidence;
- every incomplete and unknown row that remains.

A check is recorded as passed only under the rules of [The record](#the-record). The approval of a person is no evidence for any row.

#### Sources for the review

| Source | Used for |
|---|---|
| [IRP_MJ_CLOSE](https://learn.microsoft.com/en-us/windows-hardware/drivers/kernel/irp-mj-close) | `IRP_MJ_CLOSE` is sent when the last handle of a file object is closed. |
| [FAST_IO_DISPATCH](https://learn.microsoft.com/en-us/windows-hardware/drivers/ddi/wdm/ns-wdm-_fast_io_dispatch) | The cache-flush acquire and release callbacks only acquire and release a file system resource. MDL read completion completes a read. |
| [Disallowing a fast I/O operation](https://learn.microsoft.com/en-us/windows-hardware/drivers/ifs/disallowing-a-fast-i-o-operation-in-a-preoperation-callback-routine) | A disallowed fast I/O request is not sent to the file system, and an equivalent IRP may be reissued. |
| [FLT_PARAMETERS for IRP_MJ_ACQUIRE_FOR_SECTION_SYNCHRONIZATION](https://learn.microsoft.com/en-us/windows-hardware/drivers/ifs/flt-parameters-for-irp-mj-acquire-for-section-synchronization) | `SyncTypeOther` creates no section, and its page protection is zero. |
| [Memory protection constants](https://learn.microsoft.com/en-us/windows/win32/memory/memory-protection-constants) | A write to `PAGE_READONLY`, `PAGE_EXECUTE`, or `PAGE_EXECUTE_READ` pages is an access violation. |
| [File-backed and page-file-backed sections](https://learn.microsoft.com/en-us/windows-hardware/drivers/kernel/file-backed-and-page-file-backed-sections) | A read/write view writes the file. A copy-on-write view never does. |
| [Named pipes](https://learn.microsoft.com/en-us/windows/win32/ipc/named-pipes), [Named pipe operations](https://learn.microsoft.com/en-us/windows/win32/ipc/named-pipe-operations) | A pipe is a conduit with its own buffers, and its unread data is discarded when the connection is closed. |
| [NtCreateNamedPipeFile](https://learn.microsoft.com/en-us/windows/win32/devnotes/nt-create-named-pipe-file) | Creating a pipe creates the server end of a pipe instance. The instance is deleted when its last handle is closed. |
| [MS-FSCC: FilePipeInformation](https://learn.microsoft.com/en-us/openspecs/windows_protocols/ms-fscc/cd805dd2-9248-4024-ac0f-b87a702dd366) | Setting pipe information sets the pipe's read mode and completion mode. |
| [MS-ERREF: NTSTATUS values](https://learn.microsoft.com/en-us/openspecs/windows_protocols/ms-erref/596a1078-e883-4972-9bbc-49e60bebca55) | `STATUS_OBJECT_NAME_INVALID`, `STATUS_DELETE_PENDING`, and `STATUS_DIRECTORY_NOT_EMPTY` report a request that failed. `STATUS_INVALID_DEVICE_REQUEST` means the request is not a valid operation for the target device. |
| [MS-FSCC: FSCTL_QUERY_FILE_REGIONS request](https://learn.microsoft.com/en-us/openspecs/windows_protocols/ms-fscc/4630b33f-a955-4ce0-91b6-fd4ba4aac1ce), [MS-FSCC: FSCTL structures](https://learn.microsoft.com/en-us/openspecs/windows_protocols/ms-fscc/4dc02779-9d95-43f8-bba4-8d4ce4961458) | The control asks for a list of the file's regions. Its function number is `0x90284`. |
| [FSCTL_QUERY_VOLUME_CONTAINER_STATE](https://microsoft.github.io/windows-docs-rs/doc/windows/Win32/System/Ioctl/constant.FSCTL_QUERY_VOLUME_CONTAINER_STATE.html) (Microsoft `windows` crate for Rust) | The constant's value is `590736`, which is `0x90390`. |
| [MS-FSCC: NTFS reserved file names](https://learn.microsoft.com/en-us/openspecs/windows_protocols/ms-fscc/b04c3bd0-79dc-4e58-b8ed-74f19fc2ea0a) | `$LogFile` is the transactional logging file of NTFS. |
| [Kernel extended attributes](https://learn.microsoft.com/en-us/windows-hardware/drivers/ifs/kernel-extended-attributes) | The signature cache lives in `$Kernel` attributes, which only kernel mode can set. `FSCTL_WRITE_USN_CLOSE_RECORD` comes before the attribute is set, and `$Kernel.Purge.` attributes are deleted when the file's data or reparse point changes. |
| [FSCTL_WRITE_USN_CLOSE_RECORD](https://learn.microsoft.com/en-us/windows/win32/api/winioctl/ni-winioctl-fsctl_write_usn_close_record) | The control writes a close record into the change journal. |
| [Kernel Data Protection](https://www.microsoft.com/en-us/security/blog/2020/07/08/introducing-kernel-data-protection-a-new-platform-security-technology-for-preventing-data-corruption/) | `CI.dll` is the code integrity engine of Windows. |
| [KEY_SET_INFORMATION_CLASS](https://learn.microsoft.com/en-us/windows-hardware/drivers/ddi/wdm/ne-wdm-_key_set_information_class) | Microsoft documents `KeySetHandleTagsInformation` only as reserved for system use. |
| [The Windows Registry Adventure #6](https://projectzero.google/2025/04/the-windows-registry-adventure-6-kernel.html) (Google Project Zero; original research, not Microsoft documentation) | Handle tags are a field of the per-handle key body. They are set with `KeySetHandleTagsInformation`. |

#### Optional supplemental Linux coverage

A Linux run of the npm method is extra coverage: `manual-check npm-clean` ([npm install on a clean machine](#npm-install-on-a-clean-machine)) is a contained install whose prefix, cache, logs, and temporary directory lie under its install root, followed by `version`, `check`, and `list` of the installed command, the tampered reinstall, and `npm uninstall -g`. No runner command covers the binary method or a fake Podman on Linux. The coverage is bounded and not exhaustive, it cannot see a write that was undone, and it never replaces the Windows run; a Linux result alone cannot pass `installers-no-podman`.

### No command removes an image

Identifier `no-image-removal`. Compare the images before and after the one `build` and every other command of the run that could remove one.

1. `images-before`, saved by `manual-check persistence make-outdated` in [Persistence and update](#persistence-and-update) **before** its one `build`, holds the full ID of every image on the engine with its references. This item runs no `build` of its own; that one is the shared step.
2. After the `build`, `update`, and the removals of the later items, and after [Final cleanup](#final-cleanup) has removed `chk-persist` with `--volumes`, `manual-check final-cleanup` lists the images again as `images-after` and compares the IDs. `manual-check images inventory --run ID --name NAME` and `manual-check images compare --run ID --before NAME --after NAME` do the same at any other point.

**Expected:** every image ID in `images-before` is still present in `images-after`. After the `build`, the replaced images may have lost their tags but still exist. No command of the run removed an image. The README names this as a limitation, and `podman image prune` removes unused images, never one a sandbox still uses.

### Release files and attestations

Identifier `preview-attestation`. This item needs a **tagged preview** and fails while none exists, or while the release process does not produce attestations ([Releases](releases.md#release-files)).

```text
manual-check attestation --run ID --tag TAG
```

1. `gh release view TAG --repo grauzone-labs/sandboxed-agents --json assets --jq '.assets[].name'` for the preview tag `TAG`.
2. Every file of the list is downloaded into a fresh directory and verified with `gh attestation verify FILE --repo grauzone-labs/sandboxed-agents`. This check names only the repository; for it, 2.67.0 is the historical floor, which the runner requires, and 2.102.0 is the version the automated gate requires: `gh attestation` first shipped in 2.49.0, and releases up to 2.66.1 can exit with status zero, after printing a failure, when the artifact has an attestation of a different predicate type than the requested one ([maintainer notes](maintainers.md#the-gh-version)).
3. The runner computes the SHA-256 of the two downloaded executables and compares each with its entry in the downloaded `SHA256SUMS`, the same way on both platforms; a missing entry or a different hash fails the item.
4. The commit of `TAG` (`git ls-remote` of the tag and its peeled object) is compared with the commit of the run.

**Expected:**

- Step 1 lists `sandboxed-agents-linux-amd64`, `sandboxed-agents-windows-amd64.exe`, `SHA256SUMS`, `sandboxed-agents-VERSION.tgz`, and `SandboxedAgents.VERSION.nupkg`. Validation records already uploaded to the prerelease (`live-suite-linux.json`, `live-suite-windows-11.json`, `manual-checklist.json`) may be listed beside them; they carry no build attestation and are not verified in step 2. Any other file fails the item.
- Step 2 reports a successful verification for **each** of the five files.
- Step 3 prints `OK` for both executables.
- Step 4 prints the full commit SHA that goes into the record.

### Final cleanup

Do this last, after every observation above. It is the only place that removes `chk-persist` and the only place that revokes anything.

```text
manual-check final-cleanup --run ID
```

1. The runner removes the SSH setup: `sandboxed-agents ssh-config chk-persist --remove`.
2. It removes the sandbox and its volumes: `sandboxed-agents remove chk-persist --volumes`. **Expected:** the three volumes are removed (`podman volume ls` shows none for `chk-persist`).
3. This is the removal half of `credentials-persist`. It creates the sandbox again with `sandboxed-agents up chk-persist --with azure` and checks, in `sandboxed-agents shell chk-persist`, that `/home/agent/.git-credentials` is absent, that `git config --global --get user.email` prints nothing, and that `gh auth status` and `az account show` report no login; then, from the host, that `sandboxed-agents agents status chk-persist opencode` shows the agent not enabled. Then it removes the sandbox for good: `sandboxed-agents remove chk-persist --volumes`.
4. It saves `images-after` and compares it with `images-before` for the [image check](#no-command-removes-an-image).
5. Revoke what you granted: remove the GitHub device authorization in your GitHub settings, revoke the Azure sign-in sessions for the test account, delete the Azure DevOps personal access token, sign out of the four agents' accounts or revoke their sessions at each provider, remove the sandbox's environment and the CLI authorization from the T3 account if they still exist, remove the paired phone's session in the T3 mobile app, and remove the devices `chk-persist` and `chk-pair2` from your tailnet admin console if they are still listed (the `remove chk-persist --volumes` above took the node's state without a sign-out).
6. `sandboxed-agents list` must show no sandbox of the group, and `podman ps --all` no `sandboxed-agents.manual-check.` container; the runner checks both.

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
- The top-level `result` is `pass` only when the record has one check for each of the 25 identifiers and every check passed. In every other case it is `fail`. (The stable release check reads the top-level `result`, the shape of the record, and the commit; the completeness of the list is this checklist's rule, not something the check enforces.)
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

Before you upload, check the file against the schema with `jq`. The check requires exactly the fields above, a `result` of `pass` or `fail` at both levels, only the 25 identifiers of this document, each at most once, the fixed platform of the platform-bound identifiers, and, for a record that says `pass`, all 25 identifiers present and every check passed. It also requires the top-level `result` to be `fail` when any check failed:

```sh
IDS='["agent-login-copilot","agent-login-claude","agent-login-codex","agent-login-opencode","github-login","azure-login-device-code","azure-login-browser","azdo-token","t3-connect-link","t3-qr-pair","vscode-remote-ssh","npm-install-clean","nuget-install-clean","quick-guide-linux","quick-guide-windows","selection-pins-persist","git-identity-persist","credentials-persist","update-complete","update-interrupted","azure-toolchain-required","login-needs-terminal","no-image-removal","installers-no-podman","preview-attestation"]'
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
