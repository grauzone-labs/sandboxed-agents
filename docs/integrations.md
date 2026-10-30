# Integrations

This page shows you how to set up Git identity and credentials in a sandbox, how to sign in to GitHub, Azure, and Azure DevOps from it, and how to connect it to a remote machine over SSH. Integrations configure accounts and services inside a sandbox through config and login workflows.

Every integration command needs a running sandbox. A stopped sandbox fails and names `start NAME`; a sandbox with only volumes names `up NAME`. If the manager does not answer, the command names `check NAME` for diagnosis and `restart NAME` as the next step. Nothing starts automatically and there is no fallback around the manager. Invalid integration or workflow names list the available choices before any Podman call.

Every workflow runs as `agent` (UID and GID 1000) in the sandbox. No host credential, host configuration, or host environment value is copied into the sandbox. The executable adds no mount for a workflow and passes only the values you give it. Whatever a workflow stores lives in the home volume. It survives `stop`, `start`, and `update`. Every agent in the sandbox can read it. Only `sandboxed-agents remove NAME --volumes` deletes it; removing the sandbox alone keeps its volumes. This is why separate sandboxes, not separate agents, separate accounts.

## Git

Git has two config workflows, `identity` and `credentials`; choose one explicitly. Git has no login workflow: `integrations login NAME git` fails and lists the config workflows.

### Commit identity

```sh
sandboxed-agents integrations config agent01 git identity --name 'A Person' --email 'a@example.com'
```

This sets `user.name` and `user.email` in the global Git configuration of `agent`, `/home/agent/.gitconfig`, in the home volume. It applies to every repository and agent in the sandbox unless a repository overrides it. Both options together need no terminal. When either is omitted, terminal input and output are required and the command prompts only for missing values; without a terminal it fails before writing either value. Both values are collected before Git starts; an empty response or input failure changes nothing. The manager passes Git a fixed home, `/home/agent`, so Git writes its global configuration there even if the sandbox has ambient Git configuration overrides.

### Git credential helper

```sh
sandboxed-agents integrations config agent01 git credentials
```

The helper is Git's built-in `credential-store`. It needs no additional program, daemon, or desktop keyring, and its explicit file keeps credentials in the home volume, as plaintext. The manager runs this one call as `agent`:

```sh
git config --global --replace-all -- credential.helper 'store --file=/home/agent/.git-credentials'
```

This replaces every global `credential.helper` entry in `/home/agent/.gitconfig` and leaves system and repository Git configuration unchanged. The command takes no options, prompts for nothing, needs no terminal, and behaves the same with or without one. It stores no credentials itself and creates no credential file. A later Git operation on an HTTPS remote asks for credentials through an askpass program or terminal when no helper supplies them, then stores accepted credentials in `/home/agent/.git-credentials`.

## GitHub

GitHub has one login workflow, `device-code`. The workflow name is optional because it is the only choice; any other name fails and lists `device-code`. The workflow takes no options.

```sh
sandboxed-agents integrations login agent01 github
sandboxed-agents integrations login agent01 github device-code
```

### What the command needs

Standard input and standard output must both be terminals. Without them the command fails before `gh` starts, so redirecting or piping either stream does not work. The terminal is connected through to the sandbox, so you answer the GitHub CLI's prompts yourself. It may ask whether to re-authenticate an existing login and whether to "Authenticate Git with your GitHub credentials?" (either answer works, because the second command configures the helper afterward). It then shows a one-time code and a URL and waits for you to press Enter. The sandbox has no browser, so `gh` may report that it cannot open one; that is expected. Open the URL in a browser on any machine, enter the code, and approve. The browser needs no connection back into the sandbox.

### What it runs

The manager verifies it is running as `agent`, then runs two commands as `agent`, in this order, each attached to your terminal:

```sh
gh auth login --hostname github.com --git-protocol https --web
gh auth setup-git --hostname github.com
```

The first stores a login for `github.com` and chooses HTTPS as the Git protocol. The second configures Git to use `gh` as the credential helper for `github.com` over HTTPS, so Git can clone and push private repositories your account can reach without a prompt. The GitHub CLI requests its default token scopes; an organization that enforces SAML single sign-on may need to authorize the token before its private repositories work. See the GitHub CLI manual for [`gh auth login`](https://cli.github.com/manual/gh_auth_login) and [`gh auth setup-git`](https://cli.github.com/manual/gh_auth_setup-git).

Both commands run with the same controlled environment: `GH_TOKEN`, `GITHUB_TOKEN`, `GH_ENTERPRISE_TOKEN`, `GITHUB_ENTERPRISE_TOKEN`, and any `GH_CONFIG_DIR` are removed, and `GH_CONFIG_DIR`, `GIT_CONFIG_GLOBAL`, and `XDG_CONFIG_HOME` are set to fixed paths in `/home/agent`. The workflow therefore never uses an ambient token; it may still reuse a login already stored in the home volume, as `gh`'s prompts decide. This applies only to these two commands: a token variable set elsewhere in the sandbox's environment still takes precedence when you later run `gh` yourself.

### Result and failure

The command exits zero only when both commands succeed. If `gh auth login` fails or is cancelled (for example with Ctrl+C, or when the code expires), the separate `setup-git` step does not run. `gh auth login` may already have written configuration, and may have configured Git, before it failed, so a partial login or helper entry can remain. If `setup-git` fails, the command fails even though the login is stored.

A reported workflow or runtime failure, including a missing terminal, exits with status 1 and a short message; the exit code of `gh` is not passed through. A usage error (such as an unknown workflow name) exits non-zero, and a process stopped by an outside signal may report a different non-zero status. After a partial failure, re-run the command, or run `gh auth setup-git --hostname github.com` in a `shell NAME` session if the login already succeeded.

### Where the login lives

The GitHub CLI keeps its configuration and the stored token under `/home/agent/.config/gh`, and `gh auth setup-git` adds its helper entries for `github.com` to `/home/agent/.gitconfig`. Both are in the home volume. Treat the token as a plaintext credential that every agent in the sandbox can read and use for any repository your account can reach. To revoke access on GitHub's side, revoke the token in your GitHub account settings.

`setup-git` writes an empty `github.com` helper entry followed by the `gh` helper (and the same for `gist.github.com`). The empty entry resets any earlier helper for those hosts, so the generic store helper from [`integrations config NAME git credentials`](#git-credential-helper) no longer applies to `github.com`, but still serves other hosts. `gh auth logout` removes the stored login, not these Git entries.

### Manual GitHub login check

The offline tests run no real `gh` and contact no GitHub server, so a working device login, a private clone, and a push are proven only by this check. Run it once for each change to the workflow, on a real sandbox with a browser and an account that can push to a private test repository; the [manual checklist](manual-checklist.md#github-login) runs it before a stable release.

1. Use a new, clean test sandbox; `gh auth logout` does not undo Git helper entries, so an earlier login leaves it unclean. Run `sandboxed-agents integrations login agent01 github`, wait for the code, and press Ctrl+C. Confirm the status is non-zero and that, in `shell agent01`, `gh auth status` reports no login. Note any `git config --global --get-regexp '^credential\.'` entries; a cancelled login may leave some.
2. Run the command again and complete the device flow in your browser. Confirm it exits zero and prints no error.
3. In `shell agent01`, run `gh auth status`; it must show `github.com` logged in. Then clone your private test repository without a prompt: `GIT_TERMINAL_PROMPT=0 git clone https://github.com/OWNER/REPO.git`.
4. In the clone, make a test commit on a temporary branch (set the author first with [`integrations config NAME git identity`](#commit-identity) if needed) and run `GIT_TERMINAL_PROMPT=0 git push -u origin HEAD`. Confirm the push succeeds without a prompt, then delete the remote branch.
5. Run `stop` and `start`, open a new shell, and repeat the clone to a fresh directory. Confirm it still works: the login persisted in the home volume.
6. Run the command without a terminal, for example `sandboxed-agents integrations login agent01 github </dev/null`. Confirm it fails with a terminal error and a non-zero status, and starts no `gh`.

Record the sandbox image, host platform, tested commit, `gh --version` from the sandbox, and each step's result. Never record a token or one-time code.

## Azure

Azure has two login workflows, `device-code` and `browser`. Name one explicitly: omitting the workflow fails and lists both, and neither takes options.

```sh
sandboxed-agents integrations login agent01 azure device-code
sandboxed-agents integrations login agent01 azure browser
```

Both store an Azure CLI login for `agent` in the home volume. `device-code` works from any terminal and needs no connection back into the sandbox. `browser` completes the login in a browser on your machine and needs an SSH tunnel to the sandbox while it runs, so it works only with an installed [SSH setup](ssh.md). When in doubt, use `device-code`.

### What both workflows need

Beyond the running sandbox and answering manager that every integration command needs, the checks run in this order, and each failure happens before the Azure login starts; only the manager probe has touched the sandbox by then:

- The sandbox's image must include the `azure` [toolchain](images.md#toolchains), which provides the Azure CLI. Otherwise the command fails and names the fix: `update NAME --with SET`, where `SET` is the sandbox's recorded toolchain set plus `azure` in canonical sorted order, for example `--with azure` for a base-only sandbox or `--with azure,dotnet` for a sandbox built with `dotnet`.
- For `browser`, the sandbox's SSH setup must be installed. Otherwise the command fails and names `ssh-config NAME --install`. The device-code workflow does not need it.
- Standard input and standard output must both be terminals. Redirecting or piping either stream fails before `az` starts. The terminal is connected through to the sandbox, so you see the Azure CLI's output and answer its prompts yourself.

### What they run

The manager verifies it is running as `agent`, then runs two commands as `agent`, in this order, each attached to your terminal:

| Workflow | Login command | Account check |
| --- | --- | --- |
| `device-code` | `az login --use-device-code` | `az account show` |
| `browser` | `az login` | `az account show` |

The account check runs only after the login command succeeded. It prints the account and subscription the Azure CLI selected as its default, so you can see at once whether you signed in to the right tenant and subscription. See the Azure CLI documentation for [`az login`](https://learn.microsoft.com/en-us/cli/azure/authenticate-azure-cli-interactively) and [`az account show`](https://learn.microsoft.com/en-us/cli/azure/account#az-account-show).

The `azure` image installs Microsoft's Azure CLI from its signed `bookworm` repository using the documented fallback for newer Debian releases, checked on 2026-10-27. The signing key is pinned by fingerprint; Azure CLI follows the current repository release, while Microsoft's Azure DevOps extension is pinned to 1.0.10 and verified by SHA-256. Pinned, SHA-256-verified PyPI keyring dependencies are installed alongside the extension at build time under the narrow provenance exception approved on 2026-10-27; credential storage therefore needs no first-use package installation. The manifest records the CLI, extension and dependency versions. The extension is already installed system-wide in Azure CLI’s default system-extension directory under `/opt/az`, readable by `agent` and outside the home volume, so local help needs no first-use download. The [maintainer notes](maintainers.md#microsoft-azure-cli-signing-key-and-extension) record the sources, verification and validation results; [images](images.md#toolchains) describes the installation policy. These package changes leave the device-code, browser callback and PAT-on-standard-input workflows below unchanged. The Azure workflows were validated live on Linux and Windows and manually with real accounts on Windows; see the maintainer notes for revision-specific results.

Both commands run with `HOME=/home/agent` and `AZURE_CONFIG_DIR=/home/agent/.azure`, replacing any ambient value, so the Azure CLI reads and writes its configuration and token cache only in the home volume. Any ambient `BROWSER` value is removed; the `browser` workflow sets its own, as described below.

### Device-code workflow

`az login --use-device-code` prints a one-time code and a URL and waits. Open the URL in a browser on any machine, enter the code, and sign in. When the sign-in completes, the Azure CLI stores the login and lists the subscriptions it found, and the account check prints the selected one.

### Browser workflow

`az login` without options is the Azure CLI's interactive login: it opens a browser and starts a listener inside the sandbox on a port chosen when the login starts, waiting for the redirect that completes the sign-in. The redirect URL addresses `localhost` on that port. The listener stays inside the sandbox's network, the sandbox has no browser, and it publishes no port beyond SSH, so the manager stands in for the browser: it sets `BROWSER` so that the Azure CLI, once it knows the redirect port and before it waits for the reply, runs the manager's callback instead of a browser. The callback prints instructions in this form, with the port filled in:

```text
On the machine with your browser, run this in another terminal and leave it running:
ssh -N -L 127.0.0.1:PORT:127.0.0.1:PORT -o ExitOnForwardFailure=yes agent01
Then open this URL in that browser:
https://login.microsoftonline.com/...
```

The alias in the `ssh` command is the sandbox's installed [host entry](ssh.md#host-entry-names-and-controller-groups): `agent01` in the `default` controller group, `agent01.GROUP` in any other. This is why the workflow requires an installed SSH setup. To complete the login:

1. On the machine with your browser, run the printed `ssh` command in a second terminal and leave it running. It binds that one port on your machine's loopback address only, forwards it to the same port on the sandbox's loopback address, and runs no remote command. With `ExitOnForwardFailure=yes`, it exits at once if the port is already in use on your machine; free the port or cancel and retry.
2. Open the printed URL in that browser and sign in. Microsoft then redirects the browser to `http://localhost:PORT/...`, and the tunnel carries that request to the listener waiting in the sandbox.
3. Keep the tunnel open until the command in the first terminal prints the account check, then stop the tunnel with Ctrl+C. The login is stored; the tunnel is not needed afterwards.

The printed `ssh` command assumes the alias resolves on the machine where you run it. That is the case when your browser runs on the sandbox host, where `ssh-config NAME --install` wrote the entry. When the sandbox runs on a remote host, your browser machine needs an SSH configuration of its own under which that alias reaches the sandbox's SSH server through that host, for example a `ProxyJump` through the host with the sandbox's dedicated key and pinned host key. The executable installs the SSH setup only on the sandbox host and provisions nothing on other machines; if no such configuration exists, use the `device-code` workflow instead.

The tunnel is the browser's only path to the listener: the executable forwards no host credential, configuration, or environment value through it, and the sandbox still publishes nothing beyond SSH. The callback prints the login URL once; it contains no password or token, but it is tied to this login attempt, so do not record it. If the callback cannot find a loopback redirect port in the URL the Azure CLI hands it, it reports that and asks you to cancel; press Ctrl+C in the first terminal and use `device-code`.

### Result and failure

The command exits zero only when both commands succeed. If `az login` fails or is cancelled, for example with Ctrl+C, when a device code expires, or when the tunnel is closed before the redirect arrives, the account check does not run. `az login` may already have written configuration under `/home/agent/.azure` before it failed. If `az account show` fails, for example because the account has no subscription the CLI can select, the command fails even though the login is stored.

A reported workflow or runtime failure, including a missing terminal, exits with status 1 and a short message; the exit code of `az` is not passed through. A usage error (such as an unknown workflow name) exits non-zero, and a process stopped by an outside signal may report a different non-zero status. After a failure, re-run the command, or inspect the stored state with `az account show` in a `shell NAME` session.

### Where the login lives

The Azure CLI keeps its configuration and its token cache under `/home/agent/.azure` in the home volume. Treat that directory as a plaintext credential that every agent in the sandbox can read and use for anything your account can reach. `az logout` in a `shell NAME` session removes the stored login. To revoke access on Microsoft's side, revoke the sessions in your account's security settings or have a tenant administrator do so.

### Manual Azure login check

The offline tests run no real `az`, open no tunnel, and contact no Microsoft server, so a working login is proven only by this check. Run it once for each change to the workflows, on a real sandbox built with the `azure` toolchain, with a browser and an Azure account that can select a subscription; steps 6 and 7 need an installed SSH setup. The [manual checklist](manual-checklist.md#azure-login-device-code) runs it before a stable release.

1. Use a new, clean test sandbox. Run `sandboxed-agents integrations login agent01 azure device-code`, wait for the code, and press Ctrl+C. Confirm the status is non-zero, that no account was printed, and that, in `shell agent01`, `az account show` reports no login.
2. Run the command again and complete the device flow in your browser. Confirm it exits zero and ends by printing the selected account and subscription.
3. In `shell agent01`, run `az account show`; it must print the same account.
4. Run `stop` and `start`, open a new shell, and run `az account show` again. Confirm the login persisted in the home volume. Then run `az logout`.
5. On a sandbox without an installed SSH setup, run `sandboxed-agents integrations login agent01 azure browser`. Confirm it fails naming `ssh-config agent01 --install` and starts no `az`.
6. Run `ssh-config agent01 --install` and run the browser workflow again. Confirm the `ssh -N -L ...` line and the URL appear before anything waits on you, that the alias matches the installed host entry, and that `az` is still waiting. Run the tunnel on your browser machine, open the URL, sign in, and confirm the command exits zero and prints the account. Stop the tunnel and confirm `az account show` still works in `shell agent01`.
7. Run the browser workflow once more, open the URL, but stop the tunnel before signing in, then press Ctrl+C in the first terminal. Confirm the status is non-zero and no account was printed.
8. On a sandbox built without the `azure` toolchain, run either workflow. Confirm it fails naming `update agent01 --with ...` with the sandbox's recorded set plus `azure`, and runs nothing in the sandbox.
9. Run a workflow without a terminal, for example `sandboxed-agents integrations login agent01 azure device-code </dev/null`. Confirm it fails with a terminal error and a non-zero status, and starts no `az`.

Record the sandbox image, host platform, tested commit, `az --version` from the sandbox, whether the browser ran on the sandbox host or elsewhere, and each step's result. Never record a device code, login URL, or token.

## Azure DevOps

Azure DevOps has one login workflow, which takes no workflow name. It stores a personal access token (PAT) for the Azure DevOps CLI extension and for Git remotes of one organization.

```sh
sandboxed-agents integrations login agent01 azdo --organization https://dev.azure.com/ORGANIZATION
```

`--organization URL` is required. A workflow name, a missing or invalid `--organization`, or `--organization` on another integration is a usage error that exits with status 2 before Podman is called. The token is never accepted as an option or argument: it is read from standard input only.

The URL must be HTTPS with a host and without user information, a query, or a fragment; trailing slashes are removed. For `dev.azure.com` the path must be exactly the organization name, which also becomes the Git username (Azure DevOps accepts any username with a PAT). Other HTTPS hosts, such as the older `https://ORGANIZATION.visualstudio.com`, pass validation with the first label of the host as the username, but only the `dev.azure.com` form is tested.

The sandbox needs the `azure` [toolchain](images.md#toolchains) (`az` with the `azure-devops` extension). The command checks for it after the manager answers and before it reads the token. Without it, the command fails, names the `azure` toolchain, prints the complete `update NAME --with SET` command with the sandbox's recorded set plus `azure` in canonical sorted order (never `none`), reads no token, and changes nothing. Azure login does the same.

### How the token is read

Standard input decides, and standard output plays no part:

- When standard input is a terminal, the command prompts with `Token:` on standard error and reads the token without echo. Enter ends the input; Ctrl+C or Ctrl+D cancels, and the command fails without storing anything.
- Otherwise (a pipe or a redirected file) the command reads the token from it, whether standard output is a terminal or not. This is how scripts and secret managers pass the token:

```sh
some-secret-tool read azdo-pat | sandboxed-agents integrations login agent01 azdo --organization https://dev.azure.com/contoso
```

Only the first line is used, with surrounding whitespace removed; further lines are ignored, not rejected. An empty first line, an input that ends before any text, or a token with a carriage return or NUL inside fails with status 1 before anything runs in the sandbox. The token is read only after the sandbox checks above have passed, so an unknown name, owner conflict, stopped sandbox, or unanswering manager is reported first and no token is consumed.

### What it runs

The executable starts the manager once as `agent`, without a terminal, and writes the token to its standard input. The manager verifies it is running as `agent` and runs four programs as `agent`, in this order, stopping at the first failure:

```sh
az devops login --organization https://dev.azure.com/ORGANIZATION --only-show-errors
git config --global --replace-all -- credential.helper 'store --file=/home/agent/.git-credentials'
git config --global --replace-all -- credential.https://dev.azure.com/ORGANIZATION.username ORGANIZATION
git -c credential.helper= -c 'credential.helper=store --file=/home/agent/.git-credentials' -c credential.useHttpPath=false credential approve
```

The first stores the token for the Azure DevOps CLI extension, from standard input. The second configures the same store helper as [`integrations config NAME git credentials`](#git-credential-helper), replacing every global `credential.helper` entry. The third sets the username Git sends for the organization URL. The fourth hands URL, username, and token to the helper on standard input; its `-c` options make this one call use only the store helper, so no helper from repository or system Git configuration sees the PAT, and the entry covers the host and username rather than one repository path.

The token appears in no process argument on the host or in the sandbox, and the output of all four programs is discarded because their diagnostics could contain it. The programs run with a controlled environment that keeps Azure CLI and Git state under `/home/agent`, drops ambient `AZURE_*` values (including any ambient PAT) and Git trace and askpass variables, and disables Azure CLI telemetry, log files, and Git terminal prompts. Later `az` or `git` calls in the sandbox use its own environment.

### Result and failure

On success the command writes nothing to standard output and exits zero; with a terminal, the `Token:` prompt on standard error is its only output. Any failure, including a missing or empty token, exits with status 1 and a short message; the exit codes and messages of `az` and `git` are not passed through.

Because the programs run in order, a failure after the first can leave a partial state: the Azure DevOps extension may hold the token while the Git helper or stored credential is missing. The message says credentials may be partially configured, not which step failed. Re-run the command with the same token; each step replaces what an earlier run wrote.

### Where the token lives

The only credential in play is the PAT you supply on standard input. It is stored twice in the home volume: by the Azure DevOps CLI extension under `/home/agent/.azure`, and by Git's store helper as a plaintext line for `dev.azure.com` and the organization username in `/home/agent/.git-credentials`; the helper and username entries live in `/home/agent/.gitconfig`. Every agent in the sandbox can use the token for everything the PAT allows. To revoke access, revoke the PAT in Azure DevOps; `az devops logout` removes only the extension's copy. A later [GitHub login](#github) resets the helper for `github.com` only; the Azure DevOps entry keeps serving its organization.

### Manual Azure DevOps login check

The offline tests run real Git against a stub `az` and contact no Azure DevOps server: after the login, `git credential fill` resolves the stored token for `https://dev.azure.com/ORGANIZATION/PROJECT/_git/REPO` with and without an `ORGANIZATION@` user prefix, and a repository-level helper is never called. A live private clone and an `az devops` query are proven only by this check. Run it once for each change to the workflow, on a real sandbox built with `--with azure`, with a PAT that has `Code (Read)` and `Project and Team (Read)` for an organization holding a private test repository; the [manual checklist](manual-checklist.md#azure-devops-token) runs it before a stable release.

1. In a new sandbox, confirm in `shell agent01` that `az devops --help` works and `git config --global --get-regexp '^credential\.'` prints nothing.
2. Run the command with `</dev/null`. Confirm it fails with a token message, exits non-zero, and left no `credential.` entries.
3. Run it with standard input on the terminal: it prompts, shows nothing while you paste, and exits zero. Repeat with the token piped from a file.
4. In `shell agent01`, clone a private repository without a prompt: `GIT_TERMINAL_PROMPT=0 git clone https://dev.azure.com/ORGANIZATION/PROJECT/_git/REPO`, then run `az devops project list --organization https://dev.azure.com/ORGANIZATION --output table`.
5. Confirm the token is not in the host shell history, and that the `podman` process of step 3 had no token in its arguments (`ps` on the host while it runs).
6. Run `stop` and `start`, then repeat the clone to a fresh directory.
7. On a sandbox without the `azure` toolchain, confirm the command fails non-zero and writes no `credential.` entry.

Record the sandbox image, host platform, tested commit, `az version`, and each step's result. Never record the token.

## SSH to a remote machine

The `ssh` integration has one config workflow, which takes no workflow name. It connects the sandbox to a remote machine over SSH: the sandbox gets a key dedicated to that machine and, after a successful password test, a short alias that agents use with `ssh ALIAS`.

```sh
sandboxed-agents integrations config agent01 ssh --host build.example.com --user alice
```

The connection runs **from the sandbox to the remote machine**. Everything the command creates lives in the home data of `agent`, in `/home/agent/.ssh/`. It is not the [SSH setup](ssh.md), which connects your host to the sandbox, and it reads and writes no SSH file on your host and no file in host state ([ADR 0011](adr/0011-outbound-ssh-key-identity-and-retention.md)).

### What you give

- `--host HOST` names the remote machine: a bare IPv4 address, an IPv6 address (also with a zone, such as `fe80::1%eth0`), or a machine name. URLs, `user@` prefixes, and ports are refused; the test connects to port 22.
- `--user USER` is the remote username and is required. It starts with a letter, digit, or `_` and continues with letters, digits, `_`, `.`, `@`, backslash, and `-`.

Both are checked before Podman is called. The command then runs the usual integration checks, and it needs an interactive terminal: without one it fails before a key is generated. See the [command reference](command-reference.md#ssh-integration) for the exact rules and the order of checks.

### The steps

1. **Dedicated key.** The manager generates an Ed25519 key for the remote machine at `/home/agent/.ssh/id_ed25519_remote_<target-hash>`, or reuses a valid one from an earlier run, and prints its public key, one `authorized_keys` line, on standard output. Its location is printed on standard error. The private key is never printed.
2. **Approval.** The command asks whether to test the connection now. Answer `yes` (or `y`) to go on. Any other answer declines; Ctrl+D or Ctrl+C cancels. Nothing connects to the remote machine before you answer `yes`.
3. **Host key.** For a remote machine the sandbox does not trust yet, `ssh` shows the host key's fingerprint and asks whether to continue connecting. Compare the fingerprint with one you got from the machine's administrator, or from `ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub` on the machine itself, and answer `yes` only when it matches. `no` ends the test as a host key rejection, before any password is sent.
4. **Password.** `ssh` asks for the password of `USER` on your terminal and shows nothing while you type. The password goes to `ssh` only: the command takes it from no option, no environment variable, and no file, and stores and prints it nowhere.
5. **Alias.** After a successful test, a machine name is the alias. For an IP address the command asks for an alias name. The host entry is appended to `/home/agent/.ssh/config`:

   ```text
   Host build.example.com
       HostName build.example.com
       User alice
       IdentityFile /home/agent/.ssh/id_ed25519_remote_<target-hash>
       IdentitiesOnly yes
       UserKnownHostsFile /home/agent/.ssh/known_hosts
   ```

   Existing entries and comments stay as they are. An alias that another entry already claims is not overwritten: the command names the conflict and asks for another alias name. A `Host *` setting that would give the alias another user, key, or host fails the command and names that option.

### What it runs

The manager verifies it runs as `agent` and runs the image's `ssh` once as `agent`, with a fixed environment:

```sh
/usr/bin/ssh -n -T -F /dev/null \
  -o BatchMode=no -o ConnectTimeout=15 -o ConnectionAttempts=1 \
  -o PreferredAuthentications=password -o PasswordAuthentication=yes \
  -o PubkeyAuthentication=no -o KbdInteractiveAuthentication=no \
  -o HostbasedAuthentication=no -o GSSAPIAuthentication=no -o IdentityAgent=none \
  -o StrictHostKeyChecking=ask -o UserKnownHostsFile=/home/agent/.ssh/known_hosts \
  -o GlobalKnownHostsFile=/dev/null -o UpdateHostKeys=no -o CheckHostIP=no \
  -o VerifyHostKeyDNS=no -o HashKnownHosts=no -o ClearAllForwardings=yes \
  -l USER -- HOST 'exit 0'
```

`-F /dev/null` keeps every SSH configuration file out of the test, so an existing entry cannot redirect it. Password authentication is the only method: `ssh` offers no key, uses no SSH agent, and never tries a key login. A server that accepts passwords only through keyboard-interactive prompts therefore reports an authentication failure. `exit 0` is the remote command that ends the session once the password was accepted; when it reports its own non-zero status, the test still counts as successful, because the remote machine ran it.

### Host key trust

The sandbox trusts remote host keys only through `/home/agent/.ssh/known_hosts`. A host key you confirmed at the prompt is recorded there and stays there even when the password is then refused, so a retry does not ask again. A host key that differs from the trusted one is refused, and no password is sent. If the remote machine's key really changed, check the new fingerprint, remove the old entry in `shell NAME` with `ssh-keygen -R HOST -f /home/agent/.ssh/known_hosts`, and run the command again.

### Result and failure

The command exits zero only when the password test succeeded and the alias host entry was written. Every other outcome exits non-zero, writes no host entry, leaves `/home/agent/.ssh/config` unchanged, and names where the dedicated key remains. `ssh`'s own messages appear above the command's message, which names the cause:

| Message contains | Cause |
|---|---|
| `SSH connection test declined` or `SSH connection test cancelled` | You did not answer `yes`, ended the input, or pressed Ctrl+C. No connection was made. |
| `name not resolvable` | The machine name has no address in the sandbox's name resolution. |
| `unreachable or timed out` | The machine refused the connection on port 22, has no route, did not answer within 15 seconds, or a firewall or security policy in the sandbox's network denied the connection. |
| `host key rejected` | You did not confirm the host key, or it differs from the one already trusted. |
| `authentication failed` | The machine refused the username and password, or does not offer password authentication. |

Another `ssh` failure is reported as `SSH connection test failed` with `ssh`'s own message. The key stays for a later run, which reuses it and prints the same public key again.

### What the password test proves

A successful test proves that the sandbox reaches the remote machine and that the machine accepts the username and password. It does **not** prove that the dedicated key is authorized there. The command never installs the public key on the remote machine and never tries a key login. To let agents log in with the key, add the printed public key line to `~/.ssh/authorized_keys` of `USER` on the remote machine yourself, then check it in `shell NAME` with `ssh ALIAS true`.

### Where the key and the alias live

The dedicated key, `known_hosts`, and the host entry are in `/home/agent/.ssh/` in the home volume. Agents in a sandbox share the `agent` user and its files, so **every agent in the sandbox can use the dedicated key** for whatever the remote machine allows it, and can read the alias. They survive `stop`, `start`, and `update`, and `remove NAME --volumes` deletes them. Remove the public key from the remote machine's `authorized_keys` to revoke the sandbox's access. No SSH file on your host is read or written: your `~/.ssh/config`, keys, and `known_hosts` stay untouched.

### Manual SSH connection check

The offline tests run no real connection: a fake process seam answers for `ssh`, and only `ssh -G` resolves configuration locally. A real host key prompt, password prompt, and connection are proven only by this check. Run it once for each change to the workflow, on a real sandbox, against a remote machine you control that runs an SSH server with password authentication on port 22 and a test account; the [manual checklist](manual-checklist.md#ssh-to-a-remote-machine) runs it before a stable release.

1. In a new sandbox, run `sandboxed-agents integrations config agent01 ssh --host HOST --user USER` with the remote machine's name, answer `no` at the approval question, and confirm it exits non-zero, says that no connection was made, and names the key's location. On the remote machine, confirm its SSH log shows no connection from the sandbox.
2. Run it again and confirm the same public key is printed. Answer `yes`, confirm that `ssh` shows the host key fingerprint, compare it with the machine's own, and answer `no`. Confirm the command reports `host key rejected` and that `/home/agent/.ssh/config` does not exist in `shell agent01`.
3. Run it again, confirm the host key, and type a wrong password at each prompt. Confirm it reports `authentication failed`, that the typed text was never shown, and that `/home/agent/.ssh/known_hosts` now holds the host key while `/home/agent/.ssh/config` is still absent.
4. Run it again with the right password. Confirm that no host key prompt appears, that it exits zero, and that `ssh -G HOST` in `shell agent01` resolves the remote machine, the username, the dedicated key, and `identitiesonly yes`.
5. Run it with `--host` set to a name that does not resolve, and with the IP address of a machine that does not answer. Confirm the messages name `name not resolvable` and `unreachable or timed out`.
6. On the remote machine, change or replace the SSH host key, run the command once more with the machine name, and confirm it reports `host key rejected` with `ssh`'s changed-key warning and asks for no password. Restore the original host key.
7. Confirm the remote machine's `authorized_keys` of `USER` is unchanged and that its log shows only password logins. On the host, confirm `~/.ssh/config`, `~/.ssh/known_hosts`, and the keys in `~/.ssh` are unchanged.

Record the sandbox image, host platform, tested commit, the remote machine's OpenSSH version, and each step's result. Never record the password.
