# Git integration

Integrations configure accounts and services inside a sandbox. Git currently has
two config workflows, `identity` and `credentials`; choose one explicitly.
`integrations login NAME git` fails and lists these config workflows. Login
integrations will become available as their implementations are delivered.

Every config command needs a running sandbox. A stopped sandbox fails and names
`start NAME`; a sandbox with only volumes names `up NAME`. If the manager does
not answer, the command names `check NAME` for diagnosis and `restart NAME` as
the next step. Nothing starts automatically and there is no fallback around the
manager. Invalid integration or workflow names list the available choices before
any Podman call.

## Commit identity

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

## Git credential helper

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
or host environment value is copied into the sandbox. The executable adds no
mount and passes only the requested identity values and a fixed agent home to
the manager. Git writes its global configuration in that home even if the
sandbox has ambient Git config file overrides.

Verification for this change is offline: fake Podman checks the CLI calls,
injected process functions check manager handlers, and real Git checks writes
in an isolated temporary home. Live Podman behavior and persistence across
`stop`, `start`, and `update` remain to be verified by #35.
