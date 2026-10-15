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
overrides. The [GitHub login](#github) is the one workflow that also connects
your terminal to the manager, and the [Azure DevOps login](#azure-devops) is
the one that passes a token on standard input; both still copy nothing from the
host.

## GitHub

GitHub has one login workflow, `device-code`. The delivered login integrations
are `github` and [`azdo`](#azure-devops); `azure` has no delivered login
workflow yet, and Git has only config workflows.

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
