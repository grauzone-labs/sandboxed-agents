---
status: accepted
---

# Serialize lifecycle and SSH setup mutations on one sandbox

The lifecycle commands `up`, `start`, `stop`, `restart`, `remove`, and `update`, and the SSH setup mutations `ssh-config --install` and `ssh-config --remove`, never act on the same sandbox at the same time. A sandbox here means one sandbox name in one controller group, within the Podman authority of one host user. Each of these commands takes an exclusive, nonblocking OS advisory lock for its sandbox. A second command that finds the lock taken does not wait. It exits non-zero, names the sandbox, says that another lifecycle or SSH setup command is in progress for it, and tells the user to retry. `update --all` takes the same lock for each sandbox it acts on; a sandbox whose lock is taken is passed over and counted as failed, and the command continues with the others ([Name inventory of `update --all`](#name-inventory-of-update---all)). The read-only preview `ssh-config NAME` does not take a lock or create host state.

Two further locks serve resources that sandboxes share: the image-tag lock, one per image tag, and the port-allocation lock, one for loopback port allocation. Like the lifecycle lock they are exclusive, but unlike it they are blocking, and they are common to all controller groups of one host state root ([Common image-tag and port-allocation locks](#common-image-tag-and-port-allocation-locks)). A second command that finds the lock of a shared resource taken says so once on standard error, waits for it, and then rechecks current Podman facts; it is never refused. The ten-step order of checks and the refusal of a taken lifecycle lock do not change.

`update` makes this necessary: it renames and replaces a container in several steps. Without a lock, a `start` or a second `update` could act on a sandbox between the rename and the creation, or between the stop of the old container and the start of the new one.

## When the lock is held

A command takes the lock after its usage checks, the controller group and sandbox name checks, and, depending on the command, the preflight (`up`, `update`) or the selection of the Windows Podman target (`start`, `stop`, `restart`, `remove`, `ssh-config --install`, `ssh-config --remove`, ADR-0006). It takes the lock before its first lookup of a sandbox object. The only exception is the name inventory of `update --all`, described below, which reads names and no sandbox's state; `update --all` takes one lock per found name after it. Every lifecycle command, `ssh-config --install`, `ssh-config --remove`, and `update NAME` or `update --all`, holds each lock it has taken through every later check, including the owner check and the session guard, and through every action, including image builds that the command runs and the installation or removal of the SSH setup. Each lock is released when the command exits, and the operating system releases it if the process dies.

Because no command inspects a sandbox's objects before it holds that sandbox's lock, and the name inventory of `update --all` is used for names only, the next command starts from the current Podman facts, not from facts read while another command was still changing them.

## Place in the order of checks

The refusal of a taken lock is step 3 of the ten-step order of checks that the Epic makes binding for every command:

1. usage, invalid names, unknown agent, integration, workflow, and toolchain names, and path errors, without calling Podman;
2. the preflight, for the commands that run it (`build`, `up`, `update`);
3. a taken lifecycle lock, for `up`, `start`, `stop`, `restart`, `remove`, `update`, `ssh-config --install`, and `ssh-config --remove`;
4. an unknown sandbox name, or a sandbox of which only volumes remain when the command needs a container; when one of those volumes has a missing or different owner, that owner conflict is reported here instead (Feature #2);
5. a missing or different owner of the container or of one of the sandbox's volumes;
6. an interrupted update;
7. a stopped sandbox, for commands that need a running container;
8. preconditions;
9. a missing terminal;
10. the session guard, directly before the change.

A lock refusal therefore comes after usage errors and preflight failures and before an unknown sandbox name. Steps 4 to 10 run under the lock, and so do the image builds of `update`, which come after step 7 and before the session guard.

The common image-tag and port-allocation locks add no step. They are waited for only by work that comes after step 3: image ensuring and the port claim of `up`, after its early port check, and the image builds of `update`, which run inside the lifecycle lock, and `build`, which takes no lifecycle lock and waits for an image-tag lock after its steps 1 and 2. A command that waits for a common lock keeps its lifecycle lock, so another command on the same sandbox is still refused at step 3 at once.

## Name inventory of `update --all`

`update --all` must know the sandbox names before it can take their locks, and it cannot lock a name it has not found. After its preflight, it therefore runs one name inventory: `podman ps --all --format json` and `podman volume ls --format json`. From these lists it takes only the names of the current controller group's sandboxes, as they follow from the Podman names of containers, backup containers, and volumes (ADR-0005). A sandbox of which only volumes or only a backup container remain is found as well. Names of other controller groups are not taken.

The inventory yields names and nothing else. Its records are never used as the state of a sandbox: not for existence, owner, running state, labels, mounts, or configuration. For each found name, `update --all` takes the name's nonblocking lock and only then inspects the container, the volumes, and the backup container, as `update NAME` does after its lock. Every decision about a sandbox comes from that inspection. A name whose lock is taken is passed over, reported, and counted as failed, and the other names continue.

The window between the inventory and the locks is harmless for that reason. A sandbox that another command changed in between is judged by what the inspection under the lock finds. One that disappeared in between is reported with the same `does not exist in this controller group` message as `update NAME` gives, and counted as failed. A sandbox created after the inventory is not part of this run. The inventory itself changes nothing.

Every lock that `update --all` takes stays held from its acquisition until the command exits: through the inspection of every name, all image builds, and the replacements and rollbacks, which run one sandbox after another. No sandbox is changed before all builds have succeeded, so holding the locks across the builds keeps every inspected sandbox as it was inspected, as far as the contract reaches. `update NAME` keeps its single-name order.

## Lock files

- Each sandbox has one lock file at a fixed path in the `locks` directory of the controller group's host state, `group-GROUP/locks/`. The common image-tag and port-allocation locks have their own files, directly below the common `sandboxed-agents` host state root and outside every `group-GROUP` directory ([Common image-tag and port-allocation locks](#common-image-tag-and-port-allocation-locks)). The rules below hold for all lock files.
- The file name is derived from a SHA-256 hash of the sandbox's Podman container name `sandboxed-agents.GROUP.NAME` (ADR-0005), written as lowercase hexadecimal. Two sandbox names that differ only in case therefore get different files on case-insensitive file systems, and no file name equals a name that Windows reserves for devices.
- The lock file is a regular file. A command fails without acting on the sandbox when something other than a regular file is at that path.
- Lock files are never deleted. A process that opens a lock file which another process is removing at that moment would lock a file that no other command will open, and both would act on the same sandbox. A lock file is therefore left behind for every sandbox name that a lifecycle command or SSH setup mutation has ever been run for, and for every image tag and the port-allocation lock once a command has taken them, whether or not it had to wait.
- A lock file stays empty. It records no update progress, no previous running state, and no configuration. The progress of an update and the running state before it remain only in Podman: in the label on the new container and in the backup container's name (ADR-0005, Update with rollback Feature).
- On Linux the lifecycle and SSH configuration locks are `flock` with `LOCK_EX | LOCK_NB`. On Windows they are `LockFileEx` with `LOCKFILE_EXCLUSIVE_LOCK | LOCKFILE_FAIL_IMMEDIATELY`. The manager lock in the container uses the same primitives. The image-tag and port-allocation locks first make the same nonblocking attempt and, only when it finds the lock taken, wait for it: `flock` with `LOCK_EX` on Linux and `LockFileEx` with `LOCKFILE_EXCLUSIVE_LOCK` alone on Windows.

## What the contract covers

Commands on different sandbox names, or on the same name in different controller groups, take different lifecycle locks and do not refuse each other. They still wait for each other where they need the same image tag or, when creating a sandbox, the port-allocation lock; creations for different image tags do not wait for each other's builds. The contract covers only the executable's own lifecycle commands and SSH setup mutations run by the same host user. It does not cover:

- commands run by other host users, which use their own Podman and host state;
- commands that resolve a different host state root, for example with another `XDG_STATE_HOME` on Linux or another `LOCALAPPDATA` on Windows. They lock a different file, so commands are coordinated only when they share one host state root;
- writers that use Podman directly, such as the user's own `podman` calls or scripts. They hold the same Podman authority as the executable. Agents in a sandbox get no container-engine socket (ADR-0003);
- the complete work of a multi-tag rebuild, of a sandbox start after creation, and of anything that spans several tags, as [Common image-tag and port-allocation locks](#common-image-tag-and-port-allocation-locks) states: each common lock covers one resource over one step, not a transaction;
- an unrelated process that takes a loopback port: the port check is a probe, not a reservation.

Image builds and loopback port allocation, which are shared across sandboxes and controller groups (ADR-0005), are covered by the common locks for the executable's own commands, and only for them.

The lock coordinates the executable's own commands. It is not a security boundary: any program that holds the user's Podman authority can ignore it, delete the lock file, or change the sandbox's objects directly.

## When inspected objects change

While a command holds the lock, no other lifecycle command of the executable can change the objects the command has inspected. A writer outside the contract still can. Rereading the objects before each action cannot rule this out, because the writer can act between the reread and the action. The command therefore does not reread its objects to detect changes and does not retry automatically on the basis of what it observed before. When a Podman call fails unexpectedly, for example because a container is missing or its name is taken, the command stops, exits non-zero, and does not report success. What `update` does after such a failure, and how an interrupted update is recovered, belongs to the Update with rollback Feature. This decision itself promises no transactional rollback.

## Common image-tag and port-allocation locks

Image tags and loopback ports belong to the host, not to a controller group (ADR-0005). Two commands on different sandboxes, in the same or in different groups, can therefore need the same tag or read the same recorded ports. Two locks serialize this work for the executable's own commands. Both are blocking: a command that finds one taken waits until its holder releases it, with no timeout, and then continues from current Podman facts. The first, nonblocking attempt decides whether the wait happens: when it finds the lock taken, the command writes one line to standard error that names the kind of lock, `image-tag` or `port-allocation`, and its lock file, and then waits. A lock that is free produces no line, so the output and behaviour of a command that meets no contention are unchanged. A command that waits for two locks, a toolchain ensure that finds both tags taken, writes one line for each. A failure of the operating system's lock call itself names the kind of lock and keeps the cause, for example `lock image-tag: CAUSE`; a lock path that is not a regular file is refused as for the other locks.

**Image-tag lock.** One lock covers one complete image tag. Its file is `locks/images/HASH` below the common `sandboxed-agents` host state root, where `HASH` is the lowercase hexadecimal SHA-256 of the tag. Image tags carry no controller group, so every group at the same host state root waits on the same file. `up`, `update NAME`, `update --all`, and `build` take it.

- **Ensuring** (`up`, `update`): the command takes the tag's lock before it checks whether the tag exists and, for a toolchain tag, whether its base image label matches the current base image. A command that waited therefore reuses a completed build when the facts it reads after the wait show one, and builds only when they show none. A holder whose build failed releases the lock; a waiter then makes its own attempt and reports its own result, not the holder's.
- **Toolchain tag.** The ensure of a toolchain tag takes the toolchain tag's lock first and the base tag's lock second, held only while it checks or builds the base tag. It reads the base image ID after its waits, so it never compares a completed toolchain build with a base ID captured before it waited. It holds the toolchain lock through the toolchain build.
- **Explicit rebuild** (`build`): the command still builds with `--no-cache` and, for the base image, `--pull=always`, also when it waited behind another command. It takes the lock of one tag at a time for that tag's build and never holds the base tag's lock while it waits for a toolchain tag's lock. With the lock order below, the waits cannot form a cycle.

**Port-allocation lock.** One file, `locks/ports` at the same common root, covers loopback port allocation. A command that creates a new sandbox with `up` first checks the port without any lock, from the ports recorded on the containers of every group and a probe of the requested or chosen port, so a port-availability failure still comes before an image error. The lock does not cover image ensuring: a build can take minutes, and holding the lock through it would make `up` calls for different image tags build one after another. After image ensuring has finished and released its image-tag locks, the command takes the port-allocation lock and rechecks the port from a fresh inventory of the recorded ports: it checks the requested port again, or selects a port again when none was requested, so a claim that another `up` recorded in the meantime is seen and an automatic choice moves to another port. It does this immediately before it creates the volumes and the container that carries the port label, and releases the lock right after that container is created, or on any failure. A requested port that the recheck finds recorded or unavailable fails the command like the first check. Starting the container, reading it back, and restoring its tools run without the lock. The label on the created container is what other creations then read. `up` on an existing sandbox, `start`, and `update` do not take it, because they keep the recorded port.

**Lock order.** A command takes its locks in this order, and every command takes a subset of them: the lifecycle lock, the toolchain tag's lock, the base tag's lock. The port-allocation lock is taken only after image ensuring has released its image-tag locks, so no command holds an image-tag lock and the port-allocation lock at once and the two kinds cannot form a wait cycle. The group's SSH configuration lock follows the lifecycle lock. `up` and `update` release their image-tag and port-allocation locks before any SSH setup runs, so no command waits for a common lock while it holds the SSH configuration lock. `build` starts at the image-tag locks because it takes no lifecycle lock. The lifecycle locks are nonblocking, so they never take part in a wait cycle. A holder that dies releases its lock, because the operating system releases it when the process ends.

**What this does not promise.**

- The executable's commands are coordinated for the same host user and the same host state root, across controller groups. Direct Podman writers, other host users, and commands with another state root are not held back ([What the contract covers](#what-the-contract-covers)).
- A lock covers one tag or one port claim, not a sequence. `build` releases each lock after that tag's build, so another `build` can replace a base image between this `build`'s base build and one of its toolchain builds. The toolchain image then records the base ID that `build` read, and a later ensure rebuilds it as built on a superseded base. A rebuild and the sandbox creations or updates that follow it are not one transaction.
- The port probe is not an operating-system reservation. An unrelated host process can take the port between the probe and Podman's start of the container, and the start then fails like any failed start.
- The wait has no timeout. A holder that stays alive without finishing keeps its waiters waiting, and the line on standard error is what names the lock file they are blocked on.
- No lock is a security boundary.

## Readiness wait of `update`

After `update` starts the new container, the sandbox counts as ready when both checks have succeeded once:

- **Manager.** The manager answers its existing version query, `podman exec --user=0:0 CONTAINER /usr/local/bin/sandboxed-agents-manager version`, run as container root (ADR-0006). The readiness wait adds no manager functionality.
- **SSH.** The host's `ssh` completes an SSH key exchange with sshd through the loopback port recorded on the container and receives an Ed25519 host key for `[127.0.0.1]:PORT`. Each attempt is one unauthenticated SSH connection that ends before sign-in; ADR-0008 describes how `ssh` is run and how the completed key exchange is recognized. sshd sends its host key only during the SSH key exchange, so the check shows that sshd answers through the published loopback port. It needs no authorization, authenticates with no key, and writes no file in the user's SSH directory or in host state. An open TCP port is not enough: rootless Podman's port forwarding can accept a connection before sshd listens in the container.

The total wait is 60 seconds and includes the time the probe processes run. Both checks run right after the start. After that, every 250 milliseconds, only the checks that have not succeeded yet run again. Each probe has a timeout of 3 seconds, cut to the time left before the deadline. When the wait ends before both checks have succeeded, because the deadline passed or the command was cancelled, `update` exits non-zero and reports that the readiness wait failed, with the cause. The failure handling belongs to the Update with rollback Feature.

On a cold start, the Podman machine, the container's initialization, the manager, and sshd can take several seconds to answer. 60 seconds leaves room for that and still bounds how long a failed update leaves the user waiting. The value is set from these considerations only. Offline tests check the deadline, the interval, and the probes through injected process functions, and the CLI tests use fake Podman and a fake `ssh`. No live run has measured the value, so no live verification is claimed for it.

The readiness wait needs no host prerequisite beyond those the preflight already checks: `ssh` is one of the OpenSSH prerequisites `ssh` and `ssh-keygen` (ADR-0002). The preflight is one check of the complete prerequisite set: `check` reports it, and `build`, `up`, and `update` each require all of it. This is deliberate. A preflight that varied by command would let `check`, `build`, and `up` pass on a host where the next `update` fails, and only after its build. The list of prerequisites is defined in the Sandbox lifecycle and storage Feature.

## Considered options

- **Waiting for a taken lifecycle lock.** Rejected: the second command would block with no visible reason, and once it got the lock it would act on a sandbox that the first command had just changed, without the user seeing that change first.
- **A lock record in Podman**, such as a label or a marker container. Rejected: Podman cannot change the labels of an existing container (ADR-0005), and a marker is not released when the process dies, so a crashed command would leave the sandbox locked until someone repaired it.
- **One lock per controller group.** Rejected for lifecycle commands: it would serialize commands on unrelated sandboxes. The narrower SSH configuration lock in the consequences above covers only SSH setup changes, which share one managed configuration file per group.
- **Using the inventory of `update --all` as sandbox state.** Rejected: it is read before any lock is held, so another lifecycle command can change a sandbox after the inventory and before its lock. Only the inspection under the lock is current.
- **Releasing each lock of `update --all` after its sandbox was inspected.** Rejected: another command could change the sandbox during the builds, and the update would then act on facts read before that change.
- **A nonblocking refusal for shared resources.** Rejected: a command that needs a tag another command is building would fail for a reason that goes away on its own, and the second command would find a completed build to reuse. The lifecycle locks stay nonblocking because a sandbox that another command is changing is not worth waiting for (see *Waiting for a taken lifecycle lock*).
- **One lock per controller group for images and ports.** Rejected: image tags and recorded ports are shared by every group, so a group lock would not serialize the commands that collide.
- **Holding the port-allocation lock through image ensuring.** Rejected: `up` calls that need different image tags would build one after another, and an `up` of a cached image would wait behind a `build` of an unrelated toolchain tag. The command checks the port early without the lock and rechecks it under the lock after the image work, which costs one more read of the recorded ports.
- **One lock for all of a command's image work, or a base lock held while taking a toolchain lock.** Rejected: an ensure takes the toolchain tag's lock before the base tag's, so a rebuild that held the base lock and then waited for a toolchain lock could wait on an ensure that waits on it.
- **Optimistic checks without a lock**: reread the objects before each action and stop when they changed. Rejected: the executable's own commands would still interleave between the reread and the action, and the extra Podman calls are slow through a WSL2 machine.
- **Lock files named after the sandbox name.** Rejected: names that differ only in case would share a file on case-insensitive file systems, and names such as `con` or `nul` are reserved on Windows.
- **Deleting a lock file after use.** Rejected because of the race described under [Lock files](#lock-files).
- **Keeping update progress in the lock file.** Rejected: ADR-0005 already rejected state about an update outside Podman, because it can be missing or stale after an interruption.
- **TCP connect as the SSH readiness check.** Rejected because port forwarding can accept connections before sshd does.

## Consequences

- Each lifecycle command and SSH setup mutation opens and locks one file in host state before its first sandbox lookup, and `update --all` one file per found name after its name inventory, so host state and its `locks` directory are created when they are missing, also for commands that never install an SSH setup. `build` creates host state as well when it reaches an image rebuild. The read-only SSH preview creates no host state.
- Two commands that ensure the same image tag, `up` or `update` on different sandboxes or in different groups, build it at most once: the second waits and then reuses the completed build when the facts it reads after the wait show one. `build` is not an ensure: it always rebuilds with `--no-cache`, so a `build` that waited behind another command still rebuilds the tag, while an `up` or `update` that waited behind a `build` reuses its result only when its recheck shows the tag current. Two `up` calls that create sandboxes claim different loopback ports, and their builds of different image tags are not serialized. A command that waits for a long build says so once on standard error, has no timeout, and, when it is a sandbox command, keeps its lifecycle lock, so the same sandbox stays refused for that time.
- Lifecycle commands and SSH setup mutations on the same sandbox, for example from two terminals or from a script and a user, fail fast instead of interleaving. Scripts that run them in parallel must retry.
- While `update --all` runs, which can take minutes because of builds and readiness waits, every other lifecycle command on the sandboxes it holds is refused. A second `update --all` passes those sandboxes over and exits non-zero.
- Lock files accumulate in `group-GROUP/locks/`, one per sandbox name ever used, including unknown names, and in `locks/images/`, one per image tag ever ensured or rebuilt. Each is empty.
- SSH configuration changes of one controller group also serialise on a second lock, `group-GROUP/locks/ssh-config`, because every sandbox of the group shares its managed SSH configuration and `Include` line. It is exclusive and nonblocking like the lifecycle lock, and a command that finds it taken exits non-zero, writes nothing, and says that another SSH setup command is in progress. Every command takes the sandbox's lock first and the group lock second, so they cannot deadlock. `remove` removes the container before its SSH cleanup, so a refused group lock leaves the sandbox's SSH files in place until `remove` runs again.
- The user documentation states that writers that use Podman directly and commands with another host state root are outside the contract, and that the lock is coordination, not protection. It does not describe the common locks as a transaction over a multi-tag rebuild or a sandbox start.
