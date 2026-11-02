# Agent status probes

This page records why `agents status` reports `signed in` for Claude Code and `unknown` for the other three delivered agents, and which command, documentation, and version each decision rests on. It is the evidence behind [Check an agent](agents.md#check-an-agent).

## What the probe reports

A probe reports whether the agent's own tooling has authentication configured in the sandbox. It does not validate the credential: a revoked token, an expired subscription, or an account without access can still show `signed in yes`. Only a successful prompt shows that an agent works.

The sandbox may use a probe only if the agent documents a command that:

- reads the stored authentication state and nothing else,
- never starts a login, refreshes a token, or contacts a provider to check one,
- gives a result that does not depend on guessing from credential files or environment variables.

When no command meets all three, the status stays `unknown`. A wrong `yes` or `no` is worse than `unknown`.

## Decisions

Checked on 2026-11-01 against the newest npm release of each package. An unpinned `agents enable` installs the npm default tag, so these are the versions a new sandbox receives today; the tags move.

| Agent | Version checked | Status | Command and documentation |
| --- | --- | --- | --- |
| `claude` | 2.1.316 | Probe: `yes`, `no`, or `unknown` | `claude auth status --json` ([CLI reference](https://code.claude.com/docs/en/cli-reference#cli-commands)) |
| `codex` | 0.166.0 | Always `unknown` | `codex login status` ([CLI reference](https://developers.openai.com/codex/cli/reference/#codex-login)) |
| `copilot` | 1.0.105 | Always `unknown` | None ([command reference](https://docs.github.com/en/copilot/reference/copilot-cli-reference/cli-command-reference#command-line-commands)) |
| `opencode` | 1.19.5 | Always `unknown` | `opencode auth list` ([CLI docs](https://opencode.ai/docs/cli/#list)) |

### Claude Code

`claude auth status --json` prints a JSON object with a `loggedIn` boolean. The documentation defines the exit status: 0 when logged in, 1 when logged out. The sandbox accepts only a pair that agrees:

| Exit status | `loggedIn` | `signed in` |
| --- | --- | --- |
| 0 | `true` | `yes` |
| 1 | `false` | `no` |
| anything else | any | `unknown` |

Output that is not one JSON object, a missing or non-boolean `loggedIn`, a repeated key, more than 64 KiB of output, a command that cannot start, and a command that has not finished after one second are all `unknown`. After the timeout the sandbox ends the command's whole process group and waits at most another 100 ms for it to exit: the timeout is one second, the total at most about 1.1 seconds. The probe runs as `agent`, and its output and errors are never printed or stored, because the signed-in output names the account.

The version 2.1.316 handler reads the stored credential sources and exits; it starts no login and refreshes nothing. On a home without a login the command exits 1 with `loggedIn` false, which is what the `no` row relies on. A sandbox on Linux has reported `signed in yes` after a human login; see [Live acceptance](#live-acceptance).

### Codex

`codex login status` exits 0 when authentication is configured. Exit 1 means either "not logged in" or a configuration, storage, or credential-check error, so the exit status alone cannot separate signed out from broken. The command also cannot be called read-only for every sign-in mode of the newest release: with a saved agent identity it can register a task with the provider, and with a workload identity it triggers an external credential exchange ([agent_identity.rs](https://github.com/openai/codex/blob/rust-v0.166.0/codex-rs/login/src/auth/agent_identity.rs#L133-L162), [workload_identity.rs](https://github.com/openai/codex/blob/rust-v0.166.0/codex-rs/login/src/auth/workload_identity.rs), [login.rs](https://github.com/openai/codex/blob/rust-v0.166.0/codex-rs/cli/src/login.rs#L488-L554)). The sandbox does not run it, and the status stays `unknown` until Codex documents a status command that never contacts a provider.

### Copilot CLI

Copilot CLI 1.0.105 has no noninteractive status command: its help and the [command reference](https://docs.github.com/en/copilot/reference/copilot-cli-reference/cli-command-reference#command-line-commands) list `login` and no `status`, `auth`, or `whoami`. `/user` works only inside an interactive session, and `gh auth status` reports the GitHub CLI's login, not Copilot's. The status stays `unknown`.

### OpenCode

`opencode auth list` lists the stored credentials and the provider environment variables that are set ([source of 1.19.5](https://github.com/anomalyco/opencode/blob/v1.19.5/packages/opencode/src/cli/cmd/providers.ts#L248-L297)). It exits 0 even with no credentials, offers no JSON output, and validates nothing, so reading `yes` from a listed credential would infer a sign-in from the presence of a credential. The status stays `unknown`.

## Pinned and older versions

A pin can select a version older than the one checked. If its command does not exist, rejects the flag, or prints something other than the documented JSON, the status is `unknown`, never `no`.

## Live acceptance

The decisions above come from the vendors' documentation, the published source of the versions named, and runs of those versions in disposable directories without any real account. They were then checked on the two supported hosts with the executable built from commit `c675982101bdf302963ae86954a41e1b00f29184`, on 2026-11-02:

- **Live suite:** all six checks passed on both hosts, including full image coverage and the T3 Code records.
- **Signed out, both hosts:** a fresh sandbox with Claude Code 2.1.316 enabled and no login showed `signed in no`.
- **Signed in, Linux:** after a person logged in to Claude Code 2.1.316 in a browser, `agents status` showed `signed in yes` at 17:24:02 UTC, exit status zero in 824 ms, session false. The same sandbox had shown `signed in no` before the login.
- **Signed in, Windows:** `signed in yes` has not been observed.

No agent prompt was run, and no revoked or expired token was tried, so `yes` still shows only that authentication is configured. The evidence is also posted as comments on [issue 88](https://github.com/grauzone-labs/sandboxed-agents/issues/88). The same steps repeat on every preview in the Claude status check of the [manual checklist](manual-checklist.md#claude-signed-in-status).
