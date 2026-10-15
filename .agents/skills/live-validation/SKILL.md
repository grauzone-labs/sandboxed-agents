---
name: live-validation
description: Gates live validation of a change on the project's Windows and Linux hosts behind the user's explicit consent to start, then collects both SSH aliases, starts one dedicated agent per host to run the repository's documented live checks at the target revision, and reports with a timestamp which test is running on each host. Use when the agent concludes a change needs proof on a live host, or when the user asks to validate, prove, or test a change live or on the real hosts.
license: MIT
compatibility: Requires an SSH client with host aliases in the user's SSH configuration, the live test suite documented in docs/images.md on both hosts, a way to ask the user a question and wait for the answer, and a delegation tool that starts a subagent with a chosen model and returns a task handle.
metadata:
  author: "Sascha Grau"
  version: "1.1.0"
  category: "engineering"
---

# Live validation

Live validation runs the repository's documented live checks on a real Windows host and a real Linux host over SSH. The user owns the decision to start and the hosts it runs on, so the run passes a two-stage gate. A **live invocation** is one request to validate one target revision; the gate is per invocation.

## Stage 1: consent to start

The Yes/No checkpoint below is this project's workflow, set by its owner; it is not a permission the skill infers, and a request such as "run live validation" is the trigger, never the answer. Ask it first, before any step on a live or remote host and before any alias step. Offline checks in the local checkout need no checkpoint. Use the host's question tool with Yes/No choices when it has one; otherwise ask in plain text with the two choices and end the turn.

> Live validation would run the documented live checks for `<change>` at revision `<sha>` on a Windows host and a Linux host over SSH. Should live validation start now?
> Choices: Yes / No

Complete when the user has answered. No answer is not consent: take no live-host or alias action while waiting.

- **Yes** authorizes this live invocation only. Continue with stage 2.
- **No, or deferred** ("later", "not now"): put `Live validation: pending (not started: <the user's words>)` in your next report, continue offline work that is still useful, and describe the change as unvalidated live, never as validated.

A Yes already given to this checkpoint in the same live invocation stays valid: resuming after an interruption, or the run's own image builds and sandbox starts, reopens nothing. A new request to validate, or a new target revision, is a new invocation and asks again.

## Stage 2: both SSH aliases

After Yes, ask for both aliases together in a prompt of its own:

> Which SSH host aliases should live validation use? Give both, as they appear in your SSH configuration.
> Windows SSH alias:
> Linux SSH alias:

- **Both given**: continue with stage 3.
- **One given**: keep it and ask for the missing one only: `Keeping the <platform> alias <alias>. Which SSH alias is the <other platform> host?`
- **Aliases named in the triggering request** belong to this invocation: after Yes, confirm them in the alias prompt (`Windows: <alias>, Linux: <alias>. Correct?`) or ask only for the missing one, never for the same text again.
- **An alias from an earlier task, session, memory, or the SSH configuration itself**, or "the same as last time": name it and ask the user to confirm it for this invocation before using it.

Complete when the user has named or confirmed both aliases in this invocation. The suite starts on neither host before then.

## Stage 3: one dedicated agent per host

After both aliases are confirmed, the session that passed the gates is the **coordinator**. It launches two **host agents** asynchronously through the host's delegation tool before waiting on either, one owning the Windows alias and one owning the Linux alias, and it runs no suite itself. Route each host agent's model with the `model-routing` skill. Keep each returned task handle: a second request for the same host resumes its running agent instead of starting another, and a session resumed mid-run reattaches to the handles it kept. A launch that fails marks its host `pending` with the tool's own message while any agent that did start keeps running and its result and cleanup are still collected; when neither agent can start, report `Live validation: blocked (no delegation available)` with the tool's own message. Running a host in the coordinator, or the two hosts one after the other, is not a fallback.

Give each host agent, in its brief: the target revision, its confirmed alias, that consent and both aliases are already settled so it asks no gate again, the user's constraints for this invocation, the documented checks below, and the progress duties in step 2. A host agent that reports blocked leaves the other running; the coordinator records that host as `pending` with the reason.

Each host agent runs the live checks the repository documents in [docs/images.md](../../../docs/images.md) under "Live image tests", with their own opt-in variable, on its host over its alias. That section is the default workflow; ordinary read-only diagnostics beside it need no further consent.

1. Confirm the host has a checkout at the target revision and record the SHA it reports. A missing revision is routine preparation inside the Yes: fetch it into a separate temporary checkout on the host and leave the host's existing work untouched. When the host cannot reach the revision, or access fails, report the host as `pending` with the reason; pushing source to a remote or changing the host's configuration is outside this authorization.
2. Start the documented live suite in verbose mode as a background process with its own output buffer and a handle you can poll, never as one blocking foreground call. Send the coordinator an event at suite start, at every change of test or operation, and at least once a minute while an operation is still running. Name the test the suite is in and the image or sandbox it is working on, and classify the observed operation from the last line the suite printed, or from read-only evidence such as the host's existing logs or process metadata: **image build** (`up` of a missing image), **sandbox start or restart** (`up` of an existing image, `stop`, `start`), **SSH server check**, **smoke** (`shell`), **rebuild** (`build`), or **cleanup** (`remove`). A silent buffer keeps its last observed operation: send the last line and its age, and never read a pass, a finished check, or a next operation into silence; the suite's own `--- PASS` or `--- FAIL` line is the only result.
3. When a preflight or check fails, report the suite's own failure text as the finding. A repair the user has already authorized proceeds; a Podman, WSL, or SSH configuration change outside that authorization is the user's decision: propose it through the coordinator and wait for the answer.
4. Return the suite's result line, the commit it ran on, its duration, and the cleanup state (what the suite removed and anything it left behind).

The coordinator turns the host agents' events into one status report, at least once a minute while either suite runs and at every event, in exactly this layout. The timestamp is the current UTC clock when the report is written, never a time copied from a log line. The Windows and Linux lines stay adjacent, each one compact line that names the alias, the test, the image or sandbox, the operation, and the age of the last output; the raw log line stays out of it. One blank line separates the header, the two-host group, and the footer, and one blank line follows the footer before anything else. The Windows line ends with a backslash so Markdown keeps the two hosts on separate lines. A host whose agent has not started, is blocked, or has finished says so in place of the operation.

```markdown
Live validation — <YYYY-MM-DD HH:MM:SS UTC>

Windows: <alias>, <test> on <image or sandbox>, <operation>, last output <n>s ago\
Linux: <alias>, <test> on <image or sandbox>, <operation>, last output <n>s ago

Next update: within 60 seconds

```

Report in this form when both host agents have returned, then stop:

```markdown
Live validation: <done | failed | pending>
Revision: <sha>

| | Windows (<alias>) | Linux (<alias>) |
| --- | --- | --- |
| Result | <suite result line> | <suite result line> |
| Duration | <mm:ss> | <mm:ss> |
| Cleanup | <state> | <state> |
```

Complete when both hosts have a recorded result and cleanup state, or a recorded reason they have none.
