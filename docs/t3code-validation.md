# T3 Code validation runbook

This page is for maintainers. It tells you how to run and record the live validation of the T3 Code Tool on a Linux host and a Windows 11 host, the work of [issue #97](https://github.com/grauzone-labs/sandboxed-agents/issues/97). What the Tool does is on the [Tools page](tools.md). This page says how to prove it on real hosts, and which proof counts.

The run list of #97 is covered by **recorded runs**, not by an extension of the live suite. The live suite, its records, and the release gate schema stay as they are ([releases](releases.md#live-suite)).

**No run is claimed here.** This page is a procedure and an empty record. Nothing in it, in an issue comment, or in an ADR narrative of an earlier manual run substitutes for a record made by this procedure.

## Three kinds of evidence

| Kind | Covers | Recorded in | Counts toward #97 |
|---|---|---|---|
| **The live suite**, `TestLiveSuite` | `preflight`, `lifecycle`, `update` (one `update NAME` and one forced rollback), `agent`, and `images`, all **without a Tool**. It never runs `tools`. | `live-suite-linux.json`, `live-suite-windows-11.json` | No. It shows the sandbox lifecycle that the Tool restore is built on, and nothing about T3 Code. Do not edit it, and do not read a pass of it as Tool coverage. |
| **Recorded Tool runs**, this page | Everything in the run list of #97 that a maintainer can run and observe on a host. | One [supplementary record](#supplementary-record) per host, posted on #97, with host-private evidence kept on the host | Yes, only when a record exists for the commit, platform, and versions. |
| **The human phone checks** | The T3 Connect phone link and the phone's QR scan over Tailscale. | The [manual checklist](manual-checklist.md) items `t3-connect-link` and `t3-qr-pair`, in the manual-checklist record | Yes, and only those. A recorded Tool run never claims them. |

A tailnet client's request to an origin in this runbook proves the origin reaches the service. It does not prove that the phone's app pairs and opens T3 Code; only the manual checklist does.

## Before you start

1. Follow [`.agents/skills/live-validation/SKILL.md`](../.agents/skills/live-validation/SKILL.md). It opens with the project's Yes/No start checkpoint, asks for both SSH aliases, and starts one dedicated agent per host. Take no action on a live host before the Yes. Its [image cleanup](../.agents/skills/live-validation/SKILL.md#image-cleanup) section owns the cleanup at the end.
2. **A dedicated controller group**, never `default`, and not the group of a running live suite, for example `t3-record`. It matches `^[a-z0-9][a-z0-9-]*$`. Export `SANDBOXED_AGENTS_GROUP` in every terminal. The group is a namespace, so the sandboxes below cannot collide with another group's, but images are shared across groups.
3. **A clean checkout at the exact revision**, with no uncommitted change, on the host or in a separate temporary checkout (the skill's step 1). Record the full 40-character SHA that the checkout reports. A dirty checkout, or a SHA that differs from the one under test, makes every result of the run invalid.

   ```sh
   git status --porcelain   # prints nothing
   git rev-parse HEAD       # the 40-character SHA of the record
   ```

4. **A prepared executable**: a plain `go build` embeds an empty manager and its images fail at the guard step. Build as [Development builds with the manager](maintainers.md#development-builds-with-the-manager) says, with the Go version of `go.mod`, from the repository root, into a directory outside the checkout:

   ```sh
   mkdir -p "$HOME/t3-record"
   image_build_context=$(mktemp -d)
   go run ./image/cmd/overlay "$image_build_context"
   go build -overlay "$image_build_context/overlay.json" -o "$HOME/t3-record/sandboxed-agents" ./cmd/sandboxed-agents
   ```

   ```powershell
   New-Item -ItemType Directory -Force "$env:USERPROFILE\t3-record" | Out-Null
   $context = Join-Path $env:TEMP 't3-record-context'; New-Item -ItemType Directory $context | Out-Null
   go run ./image/cmd/overlay $context
   go build -overlay "$context\overlay.json" -o "$env:USERPROFILE\t3-record\sandboxed-agents.exe" ./cmd/sandboxed-agents
   ```

   Run every `sandboxed-agents` command below with that executable, and record the output of `sandboxed-agents version`: the version and the `asset-sha256`.
5. **The host is ready and measured.** Rootless Podman as [host prerequisites](host-prerequisites.md) says, lingering on Linux, and `check` passing. Before any build, take what the skill's step 2 and [Measurement and Inventory](../.agents/skills/live-validation/SKILL.md#image-cleanup) require: the free space of the filesystem that holds Podman's storage (on Windows the host drive that holds the machine's disk, and the machine's own image filesystem), `podman system df`, the engine-wide `podman images --all --no-trunc` with each image's labels, and `podman ps --all` with the image each container references. Free space must exceed the bound the skill requires; this run builds the `native` toolchain and the base image once, if they are missing. Keep the build output, because it is the only proof which images this run created.
6. **A Tailscale account with MagicDNS and HTTPS certificates enabled**, a browser to approve devices, and a **tailnet client**: a second device in the tailnet with `curl`, or the phone's browser, that is not the host under test. Record which. Use a throwaway tailnet or one whose access policy you may read.
7. **An interactive terminal** on the host for `login`, `link`, and `pair`. If you drive the host over SSH, allocate a pseudo-terminal (`ssh -t`), or work in a local console, and record which you used.

Windows direct Podman calls need the machine the executable selects: `podman --connection NAME …`, where `NAME` is the default or sole machine from `podman machine list`. Every `podman` command below takes it there. In the commands, `CONTAINER` is `sandboxed-agents.GROUP.SANDBOX`.

**Two sandboxes**, both created with the `native` toolchain so that `update NAME --with none` is a real replacement:

```text
sandboxed-agents up t3-a --with native
sandboxed-agents up t3-b --with native
```

Take the [Podman configuration](#helpers) baseline of each sandbox now, before any `tools` command. Run `sandboxed-agents check` for the group and keep its output.

### Versions to record

At the start of the run, and again before any step that depends on a tag, record:

```sh
npm view t3 dist-tags --json
npm view t3 versions --json
```

Name the exact versions the run uses: `LATEST` and `NIGHTLY` (the tags' heads, which move), `PIN` (a published exact version, older than `LATEST`), and `PIN2` (another published exact version). Record the Podman version, the host OS build, and on Windows the WSL version. Record Node.js and `tailscale version` from `sandboxed-agents shell t3-a`. After every step that installs, `tools status` states the installed version; record that, never the tag.

## Helpers

**State of the service**, from `sandboxed-agents shell SANDBOX` (runs as `agent`):

```sh
cat ~/.local/share/sandboxed-agents/tools/t3code/selection.json
cat ~/.local/share/sandboxed-agents/tools/t3code/service/state.json
```

`state.json` holds `state`, the `supervisor_pid` and `service_pid`, `started`, `last_exit`, `last_failure`, and `restarts`. A **real restart** changes both process IDs and `started`. `restarts` counts crash restarts and must stay `0` in this run.

**Process identity**, from `sandboxed-agents shell SANDBOX`:

```sh
for p in /proc/[0-9]*; do
  printf '%s uid/gid %s | %s\n' "${p#/proc/}" "$(awk '/^(Uid|Gid):/{printf "%s ",$2}' "$p/status" 2>/dev/null)" "$(tr '\0' ' ' < "$p/cmdline" 2>/dev/null)"
done
```

Every process whose command line names `t3`, `t3code`, `cloudflared`, `tailscaled`, or `__supervise` must show real UID and GID `1000 1000`, and none `0`. The container's `sshd` and entrypoint are root and are not Tool processes. Cross-check from the host: `podman top CONTAINER user huser pid args`. Count the `t3 serve` processes: exactly one.

**Podman configuration**, on the host, saved to a host-private file per stage (`PHASE`, for example `after-enable`):

```sh
podman container inspect CONTAINER --format 'network={{.HostConfig.NetworkMode}}
publish={{json .HostConfig.PortBindings}}
mounts={{json .Mounts}}
capadd={{json .HostConfig.CapAdd}} capdrop={{json .HostConfig.CapDrop}}
security={{json .HostConfig.SecurityOpt}} privileged={{.HostConfig.Privileged}}
devices={{json .HostConfig.Devices}} userns={{.HostConfig.UsernsMode}} user={{.Config.User}}
limits={{.HostConfig.Memory}} {{.HostConfig.MemorySwap}} {{.HostConfig.NanoCpus}} {{.HostConfig.PidsLimit}} {{.HostConfig.ShmSize}}' > PHASE.config.txt
podman volume ls --format '{{.Name}}'
```

Take it once on the sandbox **before any Tool command** (the baseline), and again after each phase marked below. Compare each file with the baseline. They must be identical: one `127.0.0.1:PORT` binding of `22/tcp` and no other, the same mounts and volume names, no added capability or device, the same network mode, user, and limits. The container ID, the image ID, and, after `update NAME --with …`, the label `io.sandboxed-agents.toolchains` differ and are not compared. If a template field prints `<no value>` or errors on the host's Podman, compare the full `podman container inspect` JSON for that field instead and record that you did.

**A real replacement, not a no-op.** After `restart`, the container's `State.StartedAt` and `state.json`'s two PIDs and `started` change. After `stop` and `start`, the same. `update NAME` replaces the container only when the image or the toolchain set differs; with a current image it prints `already up to date` and replaces nothing ([images](images.md#updating-a-sandbox)). Therefore use `update NAME --with none` (on a sandbox created `--with native`) or `--with native` (on one on the base image), and read the container ID before and after: it must differ. A step whose container ID did not change did not exercise the replacement; record it as `fail` or `not-run`, never `pass`.

## The run

Run these phases in order on each host. Each phase names the checks of the [record](#supplementary-record) it feeds. A command that is expected to fail names what must stay unchanged; read it with `tools status` afterwards.

### 1. Selectors

1. **Refusals before anything changes** (check `enable-refusals`), on `t3-a`, which has no Tool yet. Each must exit 2 with a usage message, and `tools status t3-a t3code` must still say `not enabled`:

   ```text
   sandboxed-agents tools enable t3-a t3code --channel stable
   sandboxed-agents tools enable t3-a t3code --channel nightly --version 0.1.2
   sandboxed-agents tools enable t3-a t3code --version latest
   sandboxed-agents tools enable t3-a t3code --version 0.1.2+build.1
   ```

2. **An unpublished version** (`enable-unpublished`). Confirm `npm view -- t3@0.0.0-sandboxed-agents-none.1 version` fails with `E404`, then:

   ```text
   sandboxed-agents tools enable t3-a t3code --version 0.0.0-sandboxed-agents-none.1
   ```

   **Expected:** non-zero, a message that the version is not published, and `tools status` still `not enabled`.

3. **No selector** (`enable-none`), in a terminal T1 that you keep open until phase 2, then the Podman baseline comparison (`podman-unchanged`):

   ```text
   sandboxed-agents tools enable t3-a t3code
   sandboxed-agents tools status t3-a t3code
   ```

   **Expected:** `t3code enabled: version V (channel latest); service running`, where `V` equals `LATEST` recorded just before; `status` shows `channel latest; pin none`, `service running`, `T3 Connect not signed in`, and `tailnet node absent`.

4. **`--channel nightly`** (`enable-channel-nightly`) on the other sandbox: `sandboxed-agents tools enable t3-b t3code --channel nightly`. **Expected:** `(channel nightly)` and `V` equals `NIGHTLY`.

5. **`--channel latest` and `--version`** as `enable` selectors (`enable-channel-latest`, `enable-version-pin`) need a sandbox without a Tool, so they come after the deletes of phase 6. The `update` forms of every selector are in phases 3 and 4.

### 2. Survival and identity (UID/GID 1000)

On `t3-a`:

1. Read `state.json` and the identity listing and keep them (from any terminal). Then **close T1 entirely**, the terminal that ran the first `enable` in phase 1: end the shell, the terminal window, or the SSH session.
2. In a new terminal T2: `tools status` and the two helpers again (`identity-after-terminal-close`). **Expected:** `service running`, the **same** `supervisor_pid`, `service_pid`, and `started` as before T1 closed, `restarts` `0`, exactly one `t3 serve`, every Tool process at `1000 1000`.
3. **Repeated enable never leaves two** (`repeat-enable-keeps`): run `tools enable t3-a t3code` and `up t3-a` again. **Expected:** still one `t3 serve`, the same PIDs, `selection.json` unchanged.
4. **`stop` and `start`** (`identity-stop-start`): `stop t3-a`, then `start t3-a`. **Expected:** `start` prints `t3code service running`; `tools status` `running`; both PIDs and `started` **differ** from step 2; `restarts` `0`; identity at `1000 1000`; `selection.json` unchanged. Compare the Podman configuration (`podman-unchanged`).
5. **`restart`** (`identity-restart`): `restart t3-a`, same expectations, with `StartedAt` changed.
6. **`update NAME`** (`identity-update-name`): note the container ID, then `update t3-a --with none`. **Expected:** the container ID differs; the update prints `t3code service running`; PIDs and `started` differ; identity `1000 1000`; version, channel, and the toolchain label (`none`) as recorded; Podman configuration unchanged apart from the image, the container ID, and the label. A `restart` or `update` that exits non-zero because the service did not start, or that prints `already up to date`, is a failure of this check.
7. **`identity-cloudflared`**: `cloudflared` runs only after `link`. After the manual `t3-connect-link` item has linked a sandbox, list its processes: `cloudflared` must show `1000 1000`. When no sandbox was linked in this run, record `cloudflared` as `not-run (not linked)`; never infer it.

### 3. Update within a channel

On `t3-a` (channel `latest`):

1. `tools update t3-a t3code` (no selector). **Expected:** exit zero, the same `channel latest; pin none`, and the version equal to the tag's current head. **If the head equals the installed version, this step is a no-op and the record must say so**; it then proves only that an up-to-date channel selection is left alone, with no change to PIDs.
2. **Movement within a channel** (`update-channel`). Prefer the natural case: a channel whose head moved between the enable and this update, observed by comparing the recorded `tools status` version with the new one. When the head did not move, stage it, and mark the check `staged` in the record: run `tools update t3-a t3code --version PIN` (a real install of an older version), then in `sandboxed-agents shell` edit `~/.local/share/sandboxed-agents/tools/t3code/selection.json` so that it has `"channel": "latest"` and no `pin`, with `version` left at `PIN` (what the file holds for a channel whose head was `PIN` when it was enabled); then run `tools update` with no selector. **Expected:** the version becomes the head, `channel latest` stays recorded, no pin, and the service runs on the new version with new PIDs. The staged file is a state the executable itself writes; the edit is the only artificial step, and the record names it.
3. **Explicit channel** (`update-channel-explicit`): `tools update t3-a t3code --channel nightly`, then `tools update t3-a t3code --channel latest`. **Expected:** each switch installs that channel's head and records the channel with no pin.
4. `tools update t3-b t3code` (channel `nightly`, no selector): the same as step 1 for nightly.

### 4. Update within a pin

On `t3-b`:

1. `tools update t3-b t3code --version PIN` (`update-pin-set`). **Expected:** `(pin PIN)`, `channel none; pin PIN`, version `PIN`.
2. `tools update t3-b t3code` (`update-pin`). **Expected:** exit zero, still `pin PIN`, the version unchanged, **the PIDs unchanged** (nothing is reinstalled or restarted for a healthy pinned installation).
3. `tools update t3-b t3code --version PIN2`: the pin and the version become `PIN2`.
4. `tools update t3-b t3code --channel latest` (`update-channel-from-pin`): back to `channel latest; pin none` and the version `LATEST`.

Optionally, **staged damage** (`update-pin-repair`, mark `staged`): with the pin recorded and the service stopped, remove the pinned launcher prefix under `~/.local/share/sandboxed-agents/tools/t3code/npm/` as `agent`, then run `tools update` with no selector. **Expected:** the same pin and version are installed again and the service runs.

### Forced failed update, and what it proves

With a Tool enabled and running at version `V` and selection `S` (this is `update-failed-retained`), do it twice: once with a channel recorded, on `t3-a` after step 3 of phase 3, and once with a pin recorded, on `t3-b` after step 1 of phase 4. Record the check for each:

1. Record `selection.json`, `state.json` (the two PIDs and `started`), `tools status`, and the process list.
2. Confirm the nonexistent version: `npm view -- t3@0.0.0-sandboxed-agents-none.1 version` exits non-zero with `E404`. A published version of that name would invalidate the step.
3. Run it:

   ```text
   sandboxed-agents tools update SANDBOX t3code --version 0.0.0-sandboxed-agents-none.1
   ```

   with `SANDBOX` the sandbox of this round, `t3-a` or `t3-b`.

4. **Expected:** non-zero; the message names the failing step, that the version is not published; `tools status` shows the **same** version, channel or pin, and `service running`; `selection.json` is byte-identical; `state.json` shows the **same PIDs and `started`**, so the service never restarted; `~/.local/bin/t3` still points into the installed prefix; `npm/` holds the previous prefix only.

**What this proves.** The version is well-formed, so the usage check passes, and npm refuses it at the resolution step, before any install, any stop, or any recorded change. It proves that the previous installation, the selection, the version, and the running service stay when a switch fails **at resolution**. It does **not** prove that a later failure, an install, a runtime download, or a service that does not start on the new version, restores the previous service; those branches are covered by offline tests with injected processes only. Do not claim more, and do not record a service rollback from this run.

Another published version cannot be made to fail deterministically without a network fault or an edit of the sandbox, and neither is part of this procedure.

### 5. Podman publish and network arguments

At every point marked `podman-unchanged` above, and finally after the delete of phase 6, run the Podman configuration helper and compare it with the baseline taken before the first `tools` command. Also run, once, `podman port CONTAINER` (**expected:** `22/tcp -> 127.0.0.1:PORT` only) and `sandboxed-agents list` (**expected:** the same port). The check `podman-unchanged` passes only when every comparison was identical, and records how many comparisons were made.

### 6. Delete, delete again, re-enable

On `t3-a`, with at least one Git worktree inside T3 Code's home to prove the relocation. Create one as `agent` with `git worktree add` under `~/.local/share/sandboxed-agents/tools/t3code/home/worktrees/REPO/BRANCH`, from a repository outside the Tool directory such as one in `/workspace`, or have T3 Code create it:

1. `tools delete t3-a t3code` (`delete`). **Expected:** exit zero; the output names each moved path under `/home/agent/t3code-files/`; the worktree opens with `git worktree list` and `git rev-parse --git-dir` at its new path; `~/.local/share/sandboxed-agents/tools/t3code` and `~/.local/bin/t3` are gone; no `t3`, `cloudflared`, or `tailscaled` process runs; `~/.npm` and the agents (`agents status t3-a AGENT`) are untouched.
2. `tools delete t3-a t3code` again (`delete-again`). **Expected:** exit zero and `not enabled; nothing to delete`.
3. `tools enable t3-a t3code --channel latest` (`enable-channel-latest`, and `re-enable-clean`). **Expected:** `(channel latest)` and the version of `LATEST`; `tools status` shows `channel latest; pin none`, `T3 Connect not signed in, not linked`, `no paired device`, `tailnet node absent`: nothing from the earlier installation, not the earlier pin, login, or devices.
4. `tools delete t3-a t3code`, then `tools enable t3-a t3code --version PIN` (`enable-version-pin`). **Expected:** `(pin PIN)`, `channel none; pin PIN`, and the same clean state as in step 3.
5. Compare the Podman configuration with the baseline.

### 7. Tailnet: two sandboxes, correct service, through the lifecycle

Both sandboxes run the Tool. These are the Tailscale criteria of task `tailscale-pair`.

1. In an interactive terminal for each sandbox, `tools tailscale t3-a t3code login` and `tools tailscale t3-b t3code login`, approving each device in a browser. **Expected:** each prints `tailnet node signed in as t3-a.TAILNET.ts.net` or `t3-b.TAILNET.ts.net`, and each device is listed in the tailnet admin console. The two origins differ.
2. `tools pair t3-a t3code`, then `tools pair t3-b t3code`. Take the origin, `https://NAME.TAILNET.ts.net/`, from the printed pairing URL, the part before `/pair`, **without the token**. Do not scan; do not record the token or the QR code. Then `tools pair NAME t3code --revoke` for each. **Expected:** `tools status` shows `Serve maps the origin to the service` for each.
3. **From the tailnet client** (not the host), request each origin, for example `curl --silent --show-error --max-time 20 --output /dev/null --write-out '%{http_code}\n' ORIGIN`. **Expected:** each answers over HTTPS with a response from T3 Code. Record the status code; the page does not fix it. A TLS failure or a timeout is a failure.
4. **Each origin reaches only its own service** (`tailnet-correct-service`): `stop t3-b`. Request both origins: A answers, B does not. `start t3-b`: both answer. Then the same with `t3-a` stopped. The origins carry the sandbox's name, and only the running sandbox's origin answers.
5. **After `restart`** (`tailnet-after-restart`): `restart t3-a`. **Expected:** `t3code service running` and `t3code tailnet node running`; `tools status` shows the **same origin** as step 2 and `Serve maps the origin to the service`; no second `tools pair` was run; the tailnet client reaches both origins again. Repeat for `t3-b`.
6. **After `update NAME`** (`tailnet-after-update-name`): `update t3-a --with native` or `--with none`, whichever differs, with the container ID changing. The same expectations as step 5. Repeat for `t3-b`.
7. `podman-unchanged` once more on both sandboxes: the node and Serve add no publish, mount, device, capability, or network change.

The phone's pairing and its scan are the manual checklist's `t3-qr-pair`; do them there, on the same hosts if you want one set of devices.

## Cleanup

Follow the skill's [image cleanup](../.agents/skills/live-validation/SKILL.md#image-cleanup). In short:

1. `tools tailscale t3-a t3code logout` and the same for `t3-b` signs the nodes out; `tools delete` also does. Check the tailnet admin console and remove any device left over by hand.
2. `sandboxed-agents remove t3-a --volumes` and the same for `t3-b`. Then `sandboxed-agents list` shows no sandbox of the group, and `podman ps --all` and `podman volume ls` show no object of the group.
3. From the build output you kept, record the **exact full ID** of every image this run's build steps produced, with the step each came from, and resolve any shortened ID to exactly one full ID with `podman images --all --no-trunc`. An image is eligible only when the skill's rules say so: absent from the before inventory, in this run's build record, referenced by no container of any group, and not kept at the user's request. An image seen only in a parent chain, or tied only by a hash or a creation time, stays.
4. Remove each eligible image one by one by full ID, toolchain images before the base image they depend on: `podman rmi --no-prune ID`, with no `--force`, and on Windows with the selected `--connection`. **Never** run `podman image prune`, `podman system prune`, `podman system reset`, or any removal of the whole host, and never touch a machine, its disk, or WSL.
5. Take the after measurement, the same as the one before the build. Report removed IDs with tags and sizes, skipped IDs with the reason, unverified items with the failing query, and the before and after free space and `podman system df`, in the record's cleanup section. When the engine is unreachable, record `cleanup: pending (engine unreachable: MESSAGE)`.

## Supplementary record

Make one record per host and per run. Start it **before** the run, with every outcome `not-run`, so that an interrupted run leaves a record that shows it. Fill in an outcome only from what you observed. Keep command output, logs, process listings, and inspect files **on the host**, privately, and refer to them by file name; never paste them. The record never holds a credential, a login URL, a one-time code, a pairing token, a QR code, an account name, or a tailnet name. Post the completed record as a comment on [issue #97](https://github.com/grauzone-labs/sandboxed-agents/issues/97); no other file or gate reads it.

Outcomes: `not-run`, `pass`, `fail`, `staged` (passed through a staged state, which the note names), `blocked` (with the reason). A run counts toward #97 only when each check of the list in the issue has `pass`, or `staged` where this page allows it, on both hosts.

```markdown
## T3 Code recorded Tool run

Run result: not-run
Platform: <linux | windows-11>
Date range (UTC): <start> to <end>
Operator: <name or handle>

### Revision and executable
- Commit (40 hex): <sha>   Clean checkout: <yes | no>
- `sandboxed-agents version`: <version>, asset-sha256 <hash>
- Controller group: <group>

### Host
- OS and build: <…>   Podman: <version>   Windows only: WSL <version>, machine <name>
- Sandbox: Node.js <version>, tailscale <version>
- Tailnet client: <device kind>   Terminal for interactive steps: <console | ssh -t | other>

### T3 Code versions
- Tags at start: latest <v>, nightly <v>   Recorded again at: <time>
- Used: LATEST <v>, NIGHTLY <v>, PIN <v>, PIN2 <v>
- Installed after each enable/update: <step: version, in order>

### Checks
| Check | Outcome | Evidence (host-private file) | Note |
|---|---|---|---|
| enable-refusals | not-run | | |
| enable-unpublished | not-run | | |
| enable-none | not-run | | |
| enable-channel-nightly | not-run | | |
| enable-channel-latest | not-run | | |
| enable-version-pin | not-run | | |
| identity-after-terminal-close | not-run | | |
| repeat-enable-keeps | not-run | | |
| identity-stop-start | not-run | | |
| identity-restart | not-run | | |
| identity-update-name (container ID changed: yes/no) | not-run | | |
| identity-cloudflared | not-run | | |
| update-channel (natural or staged) | not-run | | |
| update-channel-explicit | not-run | | |
| update-pin-set | not-run | | |
| update-pin | not-run | | |
| update-channel-from-pin | not-run | | |
| update-pin-repair (optional) | not-run | | |
| update-failed-retained (channel) | not-run | | |
| update-failed-retained (pin) | not-run | | |
| podman-unchanged (comparisons made: n) | not-run | | |
| delete | not-run | | |
| delete-again | not-run | | |
| re-enable-clean | not-run | | |
| tailnet-two-origins | not-run | | |
| tailnet-correct-service | not-run | | |
| tailnet-after-restart | not-run | | |
| tailnet-after-update-name | not-run | | |
| cleanup | not-run | | |

### Cleanup
- Disk before → after: <free; `podman system df`>
- Images removed (full ID, tag, size): <…>   skipped (ID, reason): <…>   unverified: <…>
- Sandboxes and volumes of the group remaining: <none | …>   Tailnet devices left: <none | …>

### Not covered by this record
The phone checks (`t3-connect-link`, `t3-qr-pair`), service rollback after a failure past resolution, and anything not listed above.
```

## What a record may not say

- It may not say a check passed that was not run, nor merge a `staged` check into a `pass`.
- It may not cite the live suite's pass as evidence for a Tool check, nor an earlier manual run, an ADR narrative, or an issue comment.
- It may not claim a service rollback from the forced failed update.
- It may not claim the phone paired or opened T3 Code. The tailnet client's request proves the origin answers.
