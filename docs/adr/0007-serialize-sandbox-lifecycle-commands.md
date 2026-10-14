---
status: accepted
---

# Serialize lifecycle and SSH setup mutations on one sandbox

The lifecycle commands `up`, `start`, `stop`, `restart`, `remove`, and `update`, and the SSH setup mutations `ssh-config --install` and `ssh-config --remove`, never act on the same sandbox at the same time. A sandbox here means one sandbox name in one controller group, within the Podman authority of one host user. Each of these commands takes an exclusive, nonblocking OS advisory lock for its sandbox. A second command that finds the lock taken does not wait. It exits non-zero, names the sandbox, says that another lifecycle or SSH setup command is in progress for it, and tells the user to retry. `update --all` takes the same lock for each sandbox it acts on; a sandbox whose lock is taken is passed over and counted as failed, and the command continues with the others ([Name inventory of `update --all`](#name-inventory-of-update---all)). The read-only preview `ssh-config NAME` does not take a lock or create host state.

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

## Name inventory of `update --all`

`update --all` must know the sandbox names before it can take their locks, and it cannot lock a name it has not found. After its preflight, it therefore runs one name inventory: `podman ps --all --format json` and `podman volume ls --format json`. From these lists it takes only the names of the current controller group's sandboxes, as they follow from the Podman names of containers, backup containers, and volumes (ADR-0005). A sandbox of which only volumes or only a backup container remain is found as well. Names of other controller groups are not taken.

The inventory yields names and nothing else. Its records are never used as the state of a sandbox: not for existence, owner, running state, labels, mounts, or configuration. For each found name, `update --all` takes the name's nonblocking lock and only then inspects the container, the volumes, and the backup container, as `update NAME` does after its lock. Every decision about a sandbox comes from that inspection. A name whose lock is taken is passed over, reported, and counted as failed, and the other names continue.

The window between the inventory and the locks is harmless for that reason. A sandbox that another command changed in between is judged by what the inspection under the lock finds. One that disappeared in between is reported with the same `does not exist in this controller group` message as `update NAME` gives, and counted as failed. A sandbox created after the inventory is not part of this run. The inventory itself changes nothing.

Every lock that `update --all` takes stays held from its acquisition until the command exits: through the inspection of every name, all image builds, and the replacements and rollbacks, which run one sandbox after another. No sandbox is changed before all builds have succeeded, so holding the locks across the builds keeps every inspected sandbox as it was inspected, as far as the contract reaches. `update NAME` keeps its single-name order.

## Lock files

- Each sandbox has one lock file at a fixed path in the `locks` directory of the controller group's host state, `group-GROUP/locks/`.
- The file name is derived from a SHA-256 hash of the sandbox's Podman container name `sandboxed-agents.GROUP.NAME` (ADR-0005), written as lowercase hexadecimal. Two sandbox names that differ only in case therefore get different files on case-insensitive file systems, and no file name equals a name that Windows reserves for devices.
- The lock file is a regular file. A command fails without acting on the sandbox when something other than a regular file is at that path.
- Lock files are never deleted. A process that opens a lock file which another process is removing at that moment would lock a file that no other command will open, and both would act on the same sandbox. A lock file is therefore left behind for every sandbox name that a lifecycle command has ever been run for.
- A lock file stays empty. It records no update progress, no previous running state, and no configuration. The progress of an update and the running state before it remain only in Podman: in the label on the new container and in the backup container's name (ADR-0005, Update with rollback Feature).
- On Linux the lock is `flock` with `LOCK_EX | LOCK_NB`. On Windows it is `LockFileEx` with `LOCKFILE_EXCLUSIVE_LOCK | LOCKFILE_FAIL_IMMEDIATELY`. The manager lock in the container uses the same primitives.

## What the contract covers

Commands on different sandbox names, or on the same name in different controller groups, take different locks and run independently. The contract covers only the executable's own lifecycle commands run by the same host user. It does not cover:

- commands run by other host users, which use their own Podman and host state;
- commands that resolve a different host state root, for example with another `XDG_STATE_HOME` on Linux or another `LOCALAPPDATA` on Windows. They lock a different file, so commands are coordinated only when they share one host state root;
- writers that use Podman directly, such as the user's own `podman` calls or scripts. They hold the same Podman authority as the executable. Agents in a sandbox get no container-engine socket (ADR-0003);
- image builds and loopback port allocation, which are shared across sandboxes and controller groups (ADR-0005). Two lifecycle commands on different sandboxes may build the same image or read the same recorded ports at the same time. This decision does not serialize those.

The lock coordinates the executable's own commands. It is not a security boundary: any program that holds the user's Podman authority can ignore it, delete the lock file, or change the sandbox's objects directly.

## When inspected objects change

While a command holds the lock, no other lifecycle command of the executable can change the objects the command has inspected. A writer outside the contract still can. Rereading the objects before each action cannot rule this out, because the writer can act between the reread and the action. The command therefore does not reread its objects to detect changes and does not retry automatically on the basis of what it observed before. When a Podman call fails unexpectedly, for example because a container is missing or its name is taken, the command stops, exits non-zero, and does not report success. What `update` does after such a failure, and how an interrupted update is recovered, belongs to the Update with rollback Feature. This decision itself promises no transactional rollback.

## Readiness wait of `update`

After `update` starts the new container, the sandbox counts as ready when both checks have succeeded once:

- **Manager.** The manager answers its existing version query, `podman exec --user=0:0 CONTAINER /usr/local/bin/sandboxed-agents-manager version`, run as container root (ADR-0006). The readiness wait adds no manager functionality.
- **SSH.** The host's `ssh` completes an SSH key exchange with sshd through the loopback port recorded on the container and receives an Ed25519 host key for `[127.0.0.1]:PORT`. Each attempt is one unauthenticated SSH connection that ends before sign-in; ADR-0008 describes how `ssh` is run and how the completed key exchange is recognized. sshd sends its host key only during the SSH key exchange, so the check shows that sshd answers through the published loopback port. It needs no authorization, authenticates with no key, and writes no file in the user's SSH directory or in host state. An open TCP port is not enough: rootless Podman's port forwarding can accept a connection before sshd listens in the container.

The total wait is 60 seconds and includes the time the probe processes run. Both checks run right after the start. After that, every 250 milliseconds, only the checks that have not succeeded yet run again. Each probe has a timeout of 3 seconds, cut to the time left before the deadline. When the wait ends before both checks have succeeded, because the deadline passed or the command was cancelled, `update` exits non-zero and reports that the readiness wait failed, with the cause. The failure handling belongs to the Update with rollback Feature.

On a cold start, the Podman machine, the container's initialization, the manager, and sshd can take several seconds to answer. 60 seconds leaves room for that and still bounds how long a failed update leaves the user waiting. The value is set from these considerations only. Offline tests check the deadline, the interval, and the probes through injected process functions, and the CLI tests use fake Podman and a fake `ssh`. No live run has measured the value, so no live verification is claimed for it.

The readiness wait needs no host prerequisite beyond those the preflight already checks: `ssh` is one of the OpenSSH prerequisites `ssh` and `ssh-keygen` (ADR-0002). The preflight is one check of the complete prerequisite set: `check` reports it, and `build`, `up`, and `update` each require all of it. This is deliberate. A preflight that varied by command would let `check`, `build`, and `up` pass on a host where the next `update` fails, and only after its build. The list of prerequisites is defined in the Sandbox lifecycle and storage Feature.

## Considered options

- **Waiting for the lock.** Rejected: the second command would block with no visible reason, and once it got the lock it would act on a sandbox that the first command had just changed, without the user seeing that change first.
- **A lock record in Podman**, such as a label or a marker container. Rejected: Podman cannot change the labels of an existing container (ADR-0005), and a marker is not released when the process dies, so a crashed command would leave the sandbox locked until someone repaired it.
- **One lock per controller group.** Rejected: it would serialize commands on unrelated sandboxes.
- **Using the inventory of `update --all` as sandbox state.** Rejected: it is read before any lock is held, so another lifecycle command can change a sandbox after the inventory and before its lock. Only the inspection under the lock is current.
- **Releasing each lock of `update --all` after its sandbox was inspected.** Rejected: another command could change the sandbox during the builds, and the update would then act on facts read before that change.
- **Optimistic checks without a lock**: reread the objects before each action and stop when they changed. Rejected: the executable's own commands would still interleave between the reread and the action, and the extra Podman calls are slow through a WSL2 machine.
- **Lock files named after the sandbox name.** Rejected: names that differ only in case would share a file on case-insensitive file systems, and names such as `con` or `nul` are reserved on Windows.
- **Deleting a lock file after use.** Rejected because of the race described under [Lock files](#lock-files).
- **Keeping update progress in the lock file.** Rejected: ADR-0005 already rejected state about an update outside Podman, because it can be missing or stale after an interruption.
- **TCP connect as the SSH readiness check.** Rejected because port forwarding can accept connections before sshd does.

## Consequences

- Each lifecycle command and SSH setup mutation opens and locks one file in host state before its first sandbox lookup, and `update --all` one file per found name after its name inventory, so host state and its `locks` directory are created when they are missing, also for commands that never install an SSH setup. The read-only SSH preview creates no host state.
- Two lifecycle commands on the same sandbox, for example from two terminals or from a script and a user, fail fast instead of interleaving. Scripts that run them in parallel must retry.
- While `update --all` runs, which can take minutes because of builds and readiness waits, every other lifecycle command on the sandboxes it holds is refused. A second `update --all` passes those sandboxes over and exits non-zero.
- Lock files accumulate in `group-GROUP/locks/`, one per sandbox name ever used, including unknown names. Each is empty.
- The user documentation states that writers that use Podman directly and commands with another host state root are outside the contract, and that the lock is coordination, not protection.
