---
name: live-validation
description: Gates live validation of a change on the project's Windows and Linux hosts behind the user's explicit consent to start, then collects both SSH aliases, starts one dedicated agent per host to run the repository's documented live checks at the target revision, reports with a timestamp which test is running on each host, and after every run removes the images the run built and left unused, with disk measurements before and after. Use when the agent concludes a change needs proof on a live host, when the user asks to validate, prove, or test a change live or on the real hosts, or when the user asks to clean up or free the disk a live run used on those hosts.
license: MIT
compatibility: Requires an SSH client with host aliases in the user's SSH configuration, the live test suite documented in docs/images.md on both hosts, a way to ask the user a question and wait for the answer, and a delegation tool that starts a subagent with a chosen model and returns a task handle.
metadata:
  author: "Sascha Grau"
  version: "1.2.2"
  category: "engineering"
---

# Live validation

Live validation runs the repository's documented live checks on a real Windows host and a real Linux host over SSH. The user owns the decision to start and the hosts it runs on, so the run passes a two-stage gate. A **live invocation** is one request to validate one target revision; the gate is per invocation. Cleaning up after a run belongs to that run's invocation and reuses its host agents and authorization. A standalone request to clean up what a run left authorizes the scope the user names: apply the [image cleanup](#image-cleanup) rules on that host without starting the suite, judge eligibility from the run's recorded inventory and asset hash, and ask only for an alias the request leaves missing.

## Stage 1: consent to start

The Yes/No checkpoint below is this project's workflow, set by its owner; it is not a permission the skill infers, and a request such as "run live validation" is the trigger, never the answer. For a new test invocation, ask it first, before any step on a live or remote host and before any alias step. Offline checks in the local checkout need no checkpoint. Use the host's question tool with Yes/No choices when it has one; otherwise ask in plain text with the two choices and end the turn.

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

Give each host agent, in its brief: the target revision, its confirmed alias, that consent and both aliases are already settled so it asks no gate again, the user's constraints for this invocation, the documented checks below, the progress duties in step 3, and the [image cleanup](#image-cleanup) rules. A host agent that reports blocked leaves the other running; the coordinator records that host as `pending` with the reason.

Each host agent runs the live checks the repository documents in [docs/images.md](../../../docs/images.md) under "Live image tests", with their own opt-in variable, on its host over its alias. That section is the default workflow; ordinary read-only diagnostics beside it need no further consent.

1. Confirm the host has a checkout at the target revision and record the SHA it reports. A missing revision is routine preparation inside the Yes: fetch it into a separate temporary checkout on the host and leave the host's existing work untouched. When the host cannot reach the revision, or access fails, report the host as `pending` with the reason; pushing source to a remote or changing the host's configuration is outside this authorization.
2. Before any build, record the **before** measurement and the **before** inventory defined under [Image cleanup](#image-cleanup). Judge headroom against a conservative bound on the peak additional space the suite needs before any cleanup: the initial build of the base image and every toolchain set, the no-cache rebuild of all of them while the first set still exists, the intermediate build layers, and the writable layers of the running sandboxes. Take the bound from the peak a documented or recorded run on that host reached, or, when none exists, from a reasoned conservative estimate of those listed components; never from net growth after cleanup and never from a fixed figure. On Windows the bound must fit both the host drive that holds the machine's virtual disk and the machine's own image filesystem. When the headroom is insufficient, first remove the images that an earlier run's record marks eligible under those rules; that removal needs no further permission, while any repair, prune, or compaction stays outside this invocation. Start the suite only when the free space on every measured filesystem exceeds the bound, with one exception: no bound exists and the user has decided to proceed without one after the coordinator stated the free space and the risk that the build exhausts the host. That decision is specific to this invocation, is recorded in the report, and is not covered by the Yes of stage 1. In every other case without a reliable bound, and whenever the host's own numbers cannot be read or the space is known to be insufficient, report the host as `pending (<free> free; <bound> expected, or no reliable estimate>)` without starting the suite.
3. Start the documented live suite in verbose mode as a background process with its own output buffer and a handle you can poll, never as one blocking foreground call. Send the coordinator an event at suite start, at every change of test or operation, and at least once a minute while an operation is still running. Name the test the suite is in and the image or sandbox it is working on, and classify the observed operation from the last line the suite printed, or from read-only evidence such as the host's existing logs or process metadata: **image build** (`up` of a missing image), **sandbox start or restart** (`up` of an existing image, `stop`, `start`), **SSH server check**, **smoke** (`shell`), **rebuild** (`build`), or **cleanup** (`remove`). A silent buffer keeps its last observed operation: send the last line and its age, and never read a pass, a finished check, or a next operation into silence; the suite's own `--- PASS` or `--- FAIL` line is the only result.
4. When a preflight or check fails, report the suite's own failure text as the finding. A repair the user has already authorized proceeds; a Podman, WSL, or SSH configuration change outside that authorization is the user's decision: propose it through the coordinator and wait for the answer.
5. After the suite has returned, pass or fail, and its own cleanup of sandboxes, volumes, and group state has run, remove the run's unused images as [Image cleanup](#image-cleanup) describes, then record the **after** measurement.
6. Return the suite's result line, the commit it ran on, its duration, the before and after measurements, and the cleanup state: what the suite removed, which image IDs this step removed, which it skipped and why, and anything left behind or unverified.

The coordinator turns the host agents' events into one status report, at least once a minute while either suite runs and at every event, in exactly this layout. The timestamp is the current UTC clock when the report is written, never a time copied from a log line. The Windows and Linux lines stay adjacent, each one compact line that names the alias, the test, the image or sandbox, the operation, and the age of the last output; the raw log line stays out of it. One blank line separates the header, the two-host group, and the footer, and one blank line follows the footer before anything else. The Windows line ends with a backslash so Markdown keeps the two hosts on separate lines. A host whose agent has not started, is blocked, or has finished says so in place of the operation.

```markdown
Live validation — <YYYY-MM-DD HH:MM:SS UTC>
| --- | --- |
| Windows:<br />**<alias>** | <test> on <image or sandbox>, <operation>, last output <n>s ago<br /><disk space left> |
| Linux:<br />**<alias>** | <test> on <image or sandbox>, <operation>, last output <n>s ago <br /><disk space left> |
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
| Disk | <free before → after; Podman storage before → after> | <free before → after; Podman storage before → after> |
| Cleanup | <sandboxes, volumes, state; images removed / skipped / unverified> | <sandboxes, volumes, state; images removed / skipped / unverified> |
```

Complete when both hosts have a recorded result, both measurements, and a cleanup state that accounts for every image the run created, or a recorded reason they have none.

## Image cleanup

The suite removes its sandboxes, volumes, and group state and leaves every image it built. Images are shared across controller groups and executables: the marker label and the asset hash say which executable built an image, not who owns it, so they never make an image disposable on their own. A rebuild moves a tag to the new image and leaves the previous one untagged, still backing any sandbox created from it. Repeated no-cache rebuilds therefore fill a host; the project's Windows host has a 100 GB drive.

**Measurement.** Record, as text in the host agent's report: the free space of the filesystem that holds Podman's storage (on Windows the host drive that holds the machine's virtual disk and, through the selected connection, the machine's own image filesystem), and `podman system df` for the same engine. Take it before any build and again after cleanup.

**Inventory.** Enumerate every image ID with `podman images --all --no-trunc` (a tag or digest is not an ID), read each image's labels and creation time with `podman image inspect`, and enumerate every container with `podman ps --all`, then read the image ID each one references from `podman container inspect` (the `ps` image column may show a name or tag), for the whole engine, not one group. Take it before any build. Keep this invocation's build output and record from it the exact full image ID of every image its build steps produced, with the step or result each ID came from; resolve any shortened ID against the engine's full IDs and keep it only when it resolves to exactly one image, then confirm it against `podman image inspect` and the before inventory. That record is the only proof that an image belongs to this invocation; the asset hash, the creation time, and the difference between inventories are not.

**Eligible for removal.** An image is eligible when all of these hold: it is absent from the before inventory; its full ID is in this invocation's build record; it carries this invocation's asset hash where the build labels one; no container on the engine, in any group, references it after the suite's own cleanup; and the user has not asked to keep it. That holds for every eligible image, an untagged image the rebuild superseded or an intermediate ancestor included: its full ID must be traced to this invocation's build output. The engine's verified parent chain only corroborates ownership or supersession of an ID already in the record; an ancestor seen only in a chain, or tied only by a matching hash or creation time, stays, even when that skips an image this invocation did create. An image present before the run, shared by other sandboxes, created by a concurrent workflow, matched by more than one candidate, or whose provenance the record cannot show stays, named as skipped. A query that fails leaves the image unverified, never absent.

**Removal.** Remove eligible images one by one by ID, toolchain images before the base image they depend on, with `podman rmi --no-prune <id>` and no `--force`; on Windows through the selected `--connection`. `--no-prune` keeps the dangling parents of the removed image, so an ancestor the build record does not prove stays in place instead of being removed by the engine's cascade; a proven parent is removed by its own ID after its children. Never run `podman image prune`, `podman system prune`, `podman system reset`, or any host-wide removal, and never delete, compact, or reconfigure a machine, its virtual disk, or WSL: those are host configuration changes with their own authorization. Keep the host's logs, checkouts, and evidence files. When the engine is unreachable, report `cleanup: pending (engine unreachable: <message>)` and stop.

**Reporting.** List removed IDs with their tags and sizes, skipped IDs with the reason, unverified items with the failing query, and the before and after measurements. A retention the user asked for is honoured and its disk cost reported. This cleanup is part of the authorized invocation and opens no new checkpoint.
