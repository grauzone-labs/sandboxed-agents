---
status: accepted
---

# SSH as the user connection path, with opt-in host setup

Every sandbox runs an SSH server published only on host loopback, and editors and desktop UIs connect through it. The executable itself controls the in-container manager through `podman exec`. Writing host SSH files is opt-in: without `--ssh-config`, creating or starting a sandbox leaves the user's SSH configuration untouched. Each sandbox gets a dedicated key, and its host key is pinned.

SSH was chosen because VS Code Remote SSH, T3 Code, and similar UIs all work over it in the same way for local sandboxes and remote workers, and because it adds no mounts or credential forwarding of its own.

## Considered options

- **`podman exec` only**, with no SSH server, SSH state volume, or host SSH files. This removes key management, Windows ACL handling, port allocation, and fingerprint checks, but editors would have to attach through Dev Containers style mechanisms, whose extensions can add mounts and credential forwarding that conflict with the single-bind rule in ADR-0003.
- **SSH setup on every `up`**. Rejected because the executable must not change host SSH files without being asked.

## Consequences

- Each sandbox needs a third named volume for SSH server state and a loopback port.
- OpenSSH (`ssh` and `ssh-keygen`) is a host prerequisite.
- `remove` deletes a sandbox's SSH setup, so `update` must not go through `remove`.
