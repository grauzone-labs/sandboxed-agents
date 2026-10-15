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
your terminal to the manager; it still copies nothing from the host.

## GitHub

GitHub has one login workflow, `device-code`. It is the only login workflow
delivered so far: other integrations either have no login workflow yet or, like
Git, only config workflows.

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
