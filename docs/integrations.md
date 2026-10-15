# Integrations

Integrations configure accounts and services inside a sandbox through config
and login workflows.

Every integration command needs a running sandbox. A stopped sandbox fails and
names `start NAME`; a sandbox with only volumes names `up NAME`. If the manager does
not answer, the command names `check NAME` for diagnosis and `restart NAME` as
the next step. Nothing starts automatically and there is no fallback around the
manager. Invalid integration or workflow names list the available choices before
any Podman call.

## Git

Git has two config workflows, `identity` and `credentials`; choose one
explicitly. Git has no login workflow: `integrations login NAME git` fails and
lists the config workflows.

### Commit identity

```sh
sandboxed-agents integrations config agent01 git identity --name 'A Person' --email 'a@example.com'
```

This sets `user.name` and `user.email` in the global Git configuration of `agent`,
`/home/agent/.gitconfig`, in the home volume. It applies to every repository and
agent in the sandbox unless a repository overrides it. Both options together
need no terminal. When either is omitted, terminal input and output are required
and the command prompts only for missing values. Without a terminal it fails
before writing either value. Both values are collected before Git starts; an
empty response or input failure changes nothing.

### Git credential helper

```sh
sandboxed-agents integrations config agent01 git credentials
```

The helper is Git's built-in `credential-store`. It needs no additional program,
daemon, or desktop keyring, and its explicit file keeps credentials in the home
volume. It stores credentials as plaintext, so separate sandboxes provide the
separation between accounts.

The manager runs this one call as `agent` (UID and GID 1000):

```sh
git config --global --replace-all -- credential.helper 'store --file=/home/agent/.git-credentials'
```

This replaces every global `credential.helper` entry in `/home/agent/.gitconfig`.
It leaves system and repository Git configuration unchanged. The command takes
no options, prompts for nothing, and needs no terminal; it behaves the same with
or without one. It stores no credentials itself and creates no credential file.
A later Git operation on an HTTPS remote asks for credentials through an askpass
program or terminal when no helper supplies them, then stores accepted
credentials in `/home/agent/.git-credentials`.

Credentials are stored in the home volume, are readable by every agent in the
sandbox, and are removed with `sandboxed-agents remove NAME --volumes`. Removing
only the sandbox keeps its volumes. No host credential, host Git configuration,
or host environment value is copied into the sandbox. For these config
workflows the executable adds no mount and passes only the requested identity
values and a fixed agent home to the manager. Git writes its global
configuration in that home even if the sandbox has ambient Git config file
overrides. The login workflows for [GitHub](#github) and [Azure](#azure) also
connect your terminal to the manager. [Azure DevOps](#azure-devops) passes a
token on standard input; these workflows copy no ambient host credentials.

## GitHub

GitHub has one login workflow, `device-code`. [Azure](#azure) delivers two
login workflows, `device-code` and `browser`. [Azure DevOps](#azure-devops)
accepts a token without a workflow name, and Git has only config workflows.

```sh
sandboxed-agents integrations login agent01 github
sandboxed-agents integrations login agent01 github device-code
```

The workflow name is optional because `device-code` is the only choice; any
other name fails and lists `device-code`. The workflow takes no options.

### What the command needs

Standard input and standard output must both be terminals. Without them the
command fails before `gh` starts, so redirecting or piping either stream does not
work. The terminal is connected through to the sandbox, so you answer the GitHub
CLI's prompts yourself. It may ask whether to re-authenticate an existing login
and whether to "Authenticate Git with your GitHub credentials?" (either answer
works, because the second command configures the helper afterward). It then
shows a one-time code and a URL and waits for you to press Enter. The sandbox
has no browser, so `gh` may report that it cannot open one. That is expected:
open the URL in a browser on any machine, enter the code, and approve. The
browser needs no connection back into the sandbox.

### What it runs

The manager verifies it is running as `agent`, then runs two commands as `agent`
(UID and GID 1000), in this order, each attached to your terminal:

```sh
gh auth login --hostname github.com --git-protocol https --web
gh auth setup-git --hostname github.com
```

The first stores a login for `github.com` and chooses HTTPS as the Git protocol.
The second configures Git to use `gh` as the credential helper for `github.com`
over HTTPS, so Git can clone and push private repositories your account can
reach without a prompt. The GitHub CLI requests its default token scopes; an
organization that enforces SAML single sign-on may need to authorize the token
before its private repositories work. See the GitHub CLI manual for
[`gh auth login`](https://cli.github.com/manual/gh_auth_login) and
[`gh auth setup-git`](https://cli.github.com/manual/gh_auth_setup-git).

Both commands run with the same controlled environment: `GH_TOKEN`,
`GITHUB_TOKEN`, `GH_ENTERPRISE_TOKEN`, `GITHUB_ENTERPRISE_TOKEN`, and any
`GH_CONFIG_DIR` are removed, and `GH_CONFIG_DIR`, `GIT_CONFIG_GLOBAL`, and
`XDG_CONFIG_HOME` are set to fixed paths in `/home/agent`. The workflow therefore
never uses an ambient token; it may still reuse a login already stored in the
home volume, as `gh`'s prompts decide. This applies only to these two commands: a
token variable set elsewhere in the sandbox's environment still takes precedence
when you later run `gh` yourself.

### Result and failure

The command exits zero only when both commands succeed. If `gh auth login` fails
or is cancelled (for example with Ctrl+C, or when the code expires), the separate
`setup-git` step does not run. `gh auth login` may already have written
configuration, and may have configured Git, before it failed, so a partial login
or helper entry can remain. If `setup-git` fails, the command fails even though
the login is stored.

A reported workflow or runtime failure, including a missing terminal, exits with
status 1 and a short message; the exit code of `gh` is not passed through. A
usage error (such as an unknown workflow name) exits non-zero, and a process
stopped by an outside signal may report a different non-zero status. After a
partial failure, re-run the command, or run
`gh auth setup-git --hostname github.com` in a `shell NAME` session if the login
already succeeded.

### Where the login lives

Nothing is copied from the host: no host token, `gh` configuration, Git
configuration, or environment value reaches the sandbox, and the executable adds
no mount. The login exists only because you complete the device flow.

The GitHub CLI keeps its configuration and the stored token under
`/home/agent/.config/gh`, and `gh auth setup-git` adds its helper entries for
`github.com` to `/home/agent/.gitconfig`. Both are in the home volume, so they
survive `stop`, `start`, and `update`. Treat the token as a plaintext credential:
every agent in the sandbox runs as `agent`, shares the home volume, and can read
it and use it for any repository your account can reach. This is why separate
sandboxes, not separate agents, separate accounts. Only
`sandboxed-agents remove NAME --volumes` deletes the login; removing the sandbox
alone keeps its volumes. To revoke access on GitHub's side, revoke the token in
your GitHub account settings.

`setup-git` writes an empty `github.com` helper entry followed by the `gh`
helper (and the same for `gist.github.com`). The empty entry resets any earlier
helper for those hosts, so the generic store helper from
[`integrations config NAME git credentials`](#git-credential-helper) no longer
applies to `github.com`, but still serves other hosts. `gh auth logout` removes
the stored login, not these Git entries.

### Manual GitHub login check

Offline tests cover command dispatch, process identities and environments, the
terminal check, helper setup order, and failure statuses. They run no real `gh`
and contact no GitHub server, so they are **not proof** of a working device login,
a private clone, or a push. Run this checklist once for each change to the
workflow, on a real sandbox, with a browser and an account that can push to a
private test repository.

1. Use a new, clean test sandbox; `gh auth logout` does not undo Git helper
   entries, so an earlier login leaves it unclean. Run
   `sandboxed-agents integrations login agent01 github`, wait for the code, and
   press Ctrl+C. Confirm the status is non-zero and that, in `shell agent01`,
   `gh auth status` reports no login. Note any
   `git config --global --get-regexp '^credential\.'` entries; a cancelled
   login may leave some.
2. Run the command again and complete the device flow in your browser. Confirm
   it exits zero and prints no error.
3. In `shell agent01`, run `gh auth status`; it must show `github.com` logged in.
   Then clone your private test repository without a prompt:
   `GIT_TERMINAL_PROMPT=0 git clone https://github.com/OWNER/REPO.git`.
4. In the clone, make a test commit on a temporary branch (set the author first
   with [`integrations config NAME git identity`](#commit-identity) if needed)
   and run `GIT_TERMINAL_PROMPT=0 git push -u origin HEAD`. Confirm the push
   succeeds without a prompt, then delete the remote branch.
5. Run `stop` and `start`, open a new shell, and repeat the clone to a fresh
   directory. Confirm it still works: the login persisted in the home volume.
6. Run the command without a terminal, for example
   `sandboxed-agents integrations login agent01 github </dev/null`. Confirm it
   fails with a terminal error and a non-zero status, and starts no `gh`.

Record the sandbox image, host platform, tested commit, `gh --version` from the
sandbox, and each step's result. Never record a token or one-time code.

## Azure

Azure has two login workflows, `device-code` and `browser`. Name one
explicitly: omitting the workflow fails and lists both, and neither takes
options.

```sh
sandboxed-agents integrations login agent01 azure device-code
sandboxed-agents integrations login agent01 azure browser
```

Both store an Azure CLI login for `agent` in the home volume. `device-code`
works from any terminal and needs no connection back into the sandbox.
`browser` completes the login in a browser on your machine and needs an SSH
tunnel to the sandbox while it runs, so it works only with an installed [SSH
setup](ssh.md). When in doubt, use `device-code`.

### What both workflows need

Beyond the running sandbox and answering manager that every integration command
needs, the checks run in this order, and each failure happens before the Azure
login starts; only the manager probe that every integration command runs has
touched the sandbox by then:

- The sandbox's image must include the `azure` [toolchain](images.md), which
  provides the Azure CLI. Otherwise the command fails and names the fix:
  `update NAME --with SET`, where `SET` is the sandbox's recorded toolchain set
  plus `azure` in canonical sorted order, for example `--with azure` for a
  base-only sandbox or `--with azure,dotnet` for a sandbox built with `dotnet`.
- For `browser`, the sandbox's SSH setup must be installed. Otherwise the
  command fails and names `ssh-config NAME --install`. The device-code workflow
  does not need it.
- Standard input and standard output must both be terminals. Redirecting or
  piping either stream fails before `az` starts. The terminal is connected
  through to the sandbox, so you see the Azure CLI's output and answer its
  prompts yourself.

### What they run

The manager verifies it is running as `agent`, then runs two commands as `agent`
(UID and GID 1000), in this order, each attached to your terminal:

| Workflow | Login command | Account check |
| --- | --- | --- |
| `device-code` | `az login --use-device-code` | `az account show` |
| `browser` | `az login` | `az account show` |

The account check runs only after the login command succeeded. It prints the
account and subscription the Azure CLI selected as its default, so you can see
at once whether you signed in to the right tenant and subscription. See the
Azure CLI documentation for
[`az login`](https://learn.microsoft.com/en-us/cli/azure/authenticate-azure-cli-interactively)
and
[`az account show`](https://learn.microsoft.com/en-us/cli/azure/account#az-account-show).

Both commands run with `HOME=/home/agent` and `AZURE_CONFIG_DIR=/home/agent/.azure`,
replacing any ambient value, so the Azure CLI reads and writes its configuration
and token cache only in the home volume. Any ambient `BROWSER` value is removed;
the `browser` workflow sets its own, as described below. No host credential,
Azure CLI configuration, or environment value is copied into the sandbox, and
the executable adds no mount. The login exists only because you complete the
sign-in.

### Device-code workflow

`az login --use-device-code` prints a one-time code and a URL and waits. Open
the URL in a browser on any machine, enter the code, and sign in. The browser
needs no connection back into the sandbox. When the sign-in completes, the
Azure CLI stores the login and lists the subscriptions it found, and the account
check prints the selected one.

### Browser workflow

`az login` without options is the Azure CLI's interactive login: it opens a
browser and starts a listener inside the sandbox on a port chosen when the
login starts, waiting for the redirect that completes the sign-in. The redirect
URL addresses `localhost` on that port. The listener stays inside the sandbox's
network, the sandbox has no browser, and it publishes no port beyond SSH, so
the manager stands in for the browser. It sets `BROWSER` so that the Azure
CLI, once it knows the redirect port and before it waits for the reply, runs
the manager's callback instead of a browser. The callback prints instructions
in this form, with the port filled in:

```text
On the machine with your browser, run this in another terminal and leave it running:
ssh -N -L 127.0.0.1:PORT:127.0.0.1:PORT -o ExitOnForwardFailure=yes agent01
Then open this URL in that browser:
https://login.microsoftonline.com/...
```

The alias in the `ssh` command is the sandbox's installed
[host entry](ssh.md#host-entry-names-and-controller-groups): `agent01` in the
`default` controller group, `agent01.GROUP` in any other.
This is why the workflow requires an installed SSH setup.

**Current limitation.** The tunnel needs the sandbox's SSH server to be
running, and the default image does not start it yet: its entrypoint generates
the host keys and then only keeps the sandbox running, pending
[issue #49](https://github.com/grauzone-labs/sandboxed-agents/issues/49),
which makes sshd the sandbox's long-running process. Until that lands, the
browser workflow starts and prints its instructions, but the tunnel cannot
connect and the login cannot complete on a sandbox built from the default
image. It can complete only on a separately prepared sandbox that already runs
sshd. Use `device-code` on the default image. This limitation predates the
Azure workflows and is outside their scope. To complete the login where sshd
runs:

1. On the machine with your browser, run the printed `ssh` command in a second
   terminal and leave it running. It binds that one port on your machine's
   loopback address only, forwards it to the same port on the sandbox's
   loopback address, and runs no remote command. With
   `ExitOnForwardFailure=yes`, it exits at once if the port is already in use
   on your machine; free the port or cancel and retry.
2. Open the printed URL in that browser and sign in. Microsoft then redirects
   the browser to `http://localhost:PORT/...`, and the tunnel carries that
   request to the listener waiting in the sandbox.
3. Keep the tunnel open until the command in the first terminal prints the
   account check, then stop the tunnel with Ctrl+C. The login is stored; the
   tunnel is not needed afterwards.

The printed `ssh` command assumes the alias resolves on the machine where you
run it. That is the case when your browser runs on the sandbox host, where
`ssh-config NAME --install` wrote the entry. When the sandbox runs on a remote
host, your browser machine needs an SSH configuration of its own under which
that alias reaches the sandbox's SSH server through that host, for example a
`ProxyJump` through the host with the sandbox's dedicated key and pinned host
key. The executable installs the SSH setup only on the sandbox host and
provisions nothing on other machines; if no such configuration exists, use the
`device-code` workflow instead.

The tunnel is the browser's only path to the listener: the executable forwards
no host credential, configuration, or environment value through it, and the
sandbox still publishes nothing beyond SSH. The callback prints the login URL once;
it contains no password or token, but it is tied to this login attempt, so do
not record it.

If the callback cannot find a loopback redirect port in the URL the Azure CLI
hands it, it reports that and asks you to cancel; press Ctrl+C in the first
terminal and use `device-code`.

### Result and failure

The command exits zero only when both commands succeed. If `az login` fails or
is cancelled, for example with Ctrl+C, when a device code expires, or when the
tunnel is closed before the redirect arrives, the account check does not run.
`az login` may already have written configuration under `/home/agent/.azure`
before it failed. If `az account show` fails, for example because the account
has no subscription the CLI can select, the command fails even though the login
is stored.

A reported workflow or runtime failure, including a missing terminal, exits
with status 1 and a short message; the exit code of `az` is not passed through.
A usage error (such as an unknown workflow name) exits non-zero, and a process
stopped by an outside signal may report a different non-zero status. After a
failure, re-run the command, or inspect the stored state with `az account show`
in a `shell NAME` session.

### Where the login lives

The Azure CLI keeps its configuration and its token cache under
`/home/agent/.azure` in the home volume, so the login survives `stop`, `start`,
and `update`. Treat that directory as a plaintext credential: every agent in the
sandbox runs as `agent`, shares the home volume, and can read it and use it for
anything your account can reach. This is why separate sandboxes, not separate
agents, separate accounts. `az logout` in a `shell NAME` session removes the
stored login, and `sandboxed-agents remove NAME --volumes` deletes the home
volume; removing the sandbox alone keeps its volumes. To revoke access on
Microsoft's side, revoke the sessions in your account's security settings or
have a tenant administrator do so.

### Manual Azure login check

Offline tests cover command dispatch, the toolchain and SSH setup checks, the
terminal check, process identities and environments, the order of the login
and account check, the browser callback's output, and failure statuses. They
run no real `az`, open no tunnel, and contact no Microsoft server, so they are
**not proof** of a working login. Run this checklist once for each change to
the workflows, on a real sandbox built with the `azure` toolchain, with a
browser and an Azure account that can select a subscription. Steps 6 and 7
need a sandbox whose SSH server runs; on the default image they stay pending
until [issue #49](https://github.com/grauzone-labs/sandboxed-agents/issues/49)
lands, and no browser login has been verified live yet.

1. Use a new, clean test sandbox. Run
   `sandboxed-agents integrations login agent01 azure device-code`, wait for
   the code, and press Ctrl+C. Confirm the status is non-zero, that no account
   was printed, and that, in `shell agent01`, `az account show` reports no
   login.
2. Run the command again and complete the device flow in your browser. Confirm
   it exits zero and ends by printing the selected account and subscription.
3. In `shell agent01`, run `az account show`; it must print the same account.
4. Run `stop` and `start`, open a new shell, and run `az account show` again.
   Confirm the login persisted in the home volume. Then run `az logout`.
5. On a sandbox without an installed SSH setup, run
   `sandboxed-agents integrations login agent01 azure browser`. Confirm it fails
   naming `ssh-config agent01 --install` and starts no `az`.
6. Run `ssh-config agent01 --install` and run the browser workflow again.
   Confirm the `ssh -N -L ...` line and the URL appear before anything waits on
   you, that the alias matches the installed host entry, and that `az` is still
   waiting. Run the tunnel on your browser machine, open the URL, sign in, and
   confirm the command exits zero and prints the account. Stop the tunnel and
   confirm `az account show` still works in `shell agent01`.
7. Run the browser workflow once more, open the URL, but stop the tunnel before
   signing in, then press Ctrl+C in the first terminal. Confirm the status is
   non-zero and no account was printed.
8. On a sandbox built without the `azure` toolchain, run either workflow.
   Confirm it fails naming `update agent01 --with ...` with the sandbox's
   recorded set plus `azure`, and runs nothing in the sandbox.
9. Run a workflow without a terminal, for example
   `sandboxed-agents integrations login agent01 azure device-code </dev/null`.
   Confirm it fails with a terminal error and a non-zero status, and starts no
   `az`.

Record the sandbox image, host platform, tested commit, `az --version` from the
sandbox, whether the browser ran on the sandbox host or elsewhere, and each
step's result. Never record a device code, login URL, or token.

## Azure DevOps

Azure DevOps has one login workflow, which takes no workflow name. It stores a
personal access token (PAT) for the Azure DevOps CLI extension and for Git
remotes of one organization.

```sh
sandboxed-agents integrations login agent01 azdo --organization https://dev.azure.com/ORGANIZATION
```

`--organization URL` is required. A workflow name, a missing or invalid
`--organization`, or `--organization` on another integration is a usage error
that exits with status 2 before Podman is called. The token is never accepted
as an option or argument: it is read from standard input only.

The URL must be HTTPS with a host and without user information, a query, or a
fragment; trailing slashes are removed. For `dev.azure.com` the path must be
exactly the organization name, which also becomes the Git username (Azure
DevOps accepts any username with a PAT). Other HTTPS hosts, such as the older
`https://ORGANIZATION.visualstudio.com`, pass validation with the first label
of the host as the username, but only the `dev.azure.com` form is tested.

The sandbox needs the `azure` toolchain (`az` with the `azure-devops`
extension, see [`images.md`](images.md)). The command does not check for it yet
(issue #35): without it, the `az` step fails and nothing is stored. Add it with
`update NAME --with SET`, where `SET` is the sandbox's toolchain set plus `azure`.

### How the token is read

Standard input decides, and standard output plays no part:

- When standard input is a terminal, the command prompts with `Token:` on
  standard error and reads the token without echo. Enter ends the input;
  Ctrl+C or Ctrl+D cancels, and the command fails without storing anything.
- Otherwise (a pipe or a redirected file) the command reads the token from it,
  whether standard output is a terminal or not. This is how scripts and secret
  managers pass the token:

```sh
some-secret-tool read azdo-pat | sandboxed-agents integrations login agent01 azdo --organization https://dev.azure.com/contoso
```

Only the first line is used, with surrounding whitespace removed; further lines
are ignored, not rejected. An empty first line, an input that ends before any
text, or a token with a carriage return or NUL inside fails with status 1
before anything runs in the sandbox. The token is read only after the sandbox
checks above have passed, so an unknown name, owner conflict, stopped sandbox,
or unanswering manager is reported first and no token is consumed.

### What it runs

The executable starts the manager once as `agent`, without a terminal, and
writes the token to its standard input. The manager verifies it is running as
`agent` and runs four programs as `agent` (UID and GID 1000), in this order,
stopping at the first failure:

```sh
az devops login --organization https://dev.azure.com/ORGANIZATION --only-show-errors
git config --global --replace-all -- credential.helper 'store --file=/home/agent/.git-credentials'
git config --global --replace-all -- credential.https://dev.azure.com/ORGANIZATION.username ORGANIZATION
git -c credential.helper= -c 'credential.helper=store --file=/home/agent/.git-credentials' -c credential.useHttpPath=false credential approve
```

The first stores the token for the Azure DevOps CLI extension, from standard
input. The second configures the same store helper as
[`integrations config NAME git credentials`](#git-credential-helper), replacing
every global `credential.helper` entry. The third sets the username Git sends
for the organization URL. The fourth hands URL, username, and token to the
helper on standard input; its `-c` options make this one call use only the
store helper, so no helper from repository or system Git configuration sees the
PAT, and the entry covers the host and username rather than one repository path.

The token appears in no process argument on the host or in the sandbox, and
the output of all four programs is discarded because their diagnostics could
contain it. The programs run with a controlled environment that keeps Azure CLI
and Git state under `/home/agent`, drops ambient `AZURE_*` values (including
any ambient PAT) and Git trace and askpass variables, and disables Azure CLI
telemetry, log files, and Git terminal prompts. Later `az` or `git` calls in
the sandbox use its own environment.

### Result and failure

On success the command writes nothing to standard output and exits zero; with a
terminal, the `Token:` prompt on standard error is its only output. Any
failure, including a missing or empty token, exits with status 1 and a short
message; the exit codes and messages of `az` and `git` are not passed through.

Because the programs run in order, a failure after the first can leave a
partial state: the Azure DevOps extension may hold the token while the Git
helper or stored credential is missing. The message says credentials may be
partially configured, not which step failed. Re-run the command with the same
token; each step replaces what an earlier run wrote.

### Where the token lives

No ambient host credential, Azure CLI or Git configuration, or environment
value is imported, and the executable adds no mount. The only credential in
play is the PAT you supply on standard input. It is stored twice in the home
volume: by the Azure DevOps CLI extension under `/home/agent/.azure`, and by
Git's store helper as a plaintext line for `dev.azure.com` and the organization
username in `/home/agent/.git-credentials`; the helper and username entries live
in `/home/agent/.gitconfig`. All survive `stop`, `start`, and `update`; only
`sandboxed-agents remove NAME --volumes` deletes them. Every agent in the
sandbox runs as `agent`, shares the home volume, and can use the token for
everything the PAT allows, which is why separate sandboxes, not separate agents,
separate accounts. To revoke access, revoke the PAT in Azure DevOps;
`az devops logout` removes only the extension's copy. A later
[GitHub login](#github) resets the helper for `github.com` only; the Azure
DevOps entry keeps serving its organization.

### Manual Azure DevOps login check

Offline tests cover command dispatch, the sandbox checks that precede reading
the token, hidden terminal input on Linux, empty-token rejection, the identity,
environment, and standard input of each program, and that the token appears in
no Podman argument or output. They run real Git: after the login,
`git credential fill` resolves the stored token for
`https://dev.azure.com/ORGANIZATION/PROJECT/_git/REPO` with and without an
`ORGANIZATION@` user prefix, and a repository-level helper is never called.
`az` is replaced by a stub and no Azure DevOps server is contacted, so a live
private clone and an `az devops` query have **not** been verified: no
organization or PAT was available during development. Run this checklist once
per change on a real sandbox built with `--with azure`, with a PAT that has
`Code (Read)` and `Project and Team (Read)` for an organization holding a
private test repository.

1. In a new sandbox, confirm in `shell agent01` that `az devops --help` works
   and `git config --global --get-regexp '^credential\.'` prints nothing.
2. Run the command with `</dev/null`. Confirm it fails with a token message,
   exits non-zero, and left no `credential.` entries.
3. Run it with standard input on the terminal: it prompts, shows nothing while
   you paste, and exits zero. Repeat with the token piped from a file.
4. In `shell agent01`, clone a private repository without a prompt:
   `GIT_TERMINAL_PROMPT=0 git clone https://dev.azure.com/ORGANIZATION/PROJECT/_git/REPO`,
   then run
   `az devops project list --organization https://dev.azure.com/ORGANIZATION --output table`.
5. Confirm the token is not in the host shell history, and that the `podman`
   process of step 3 had no token in its arguments (`ps` on the host while it
   runs).
6. Run `stop` and `start`, then repeat the clone to a fresh directory.
7. On a sandbox without the `azure` toolchain, confirm the command fails
   non-zero and writes no `credential.` entry.

Record the sandbox image, host platform, tested commit, `az version`, and each
step's result. Never record the token.
