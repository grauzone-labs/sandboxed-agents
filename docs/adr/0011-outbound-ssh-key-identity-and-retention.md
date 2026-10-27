---
status: accepted
---

# Outbound SSH key identity, retention, and confirmed host trust

The SSH integration of [#108](https://github.com/grauzone-labs/sandboxed-agents/issues/108) will use one dedicated Ed25519 key per remote machine, independent of the remote username and alias. The user confirmed in [#109](https://github.com/grauzone-labs/sandboxed-agents/issues/109) that valid keys will be reused and retained after decline, cancellation, or failure, and that explicitly confirmed remote host keys will remain trusted even if password authentication later fails. These are accepted design decisions that #108 will implement.

## Decisions

- Store the private key at `/home/agent/.ssh/id_ed25519_remote_<target-hash>`, with its public key at the same path plus `.pub`. Derive the hash from the normalized remote target: equivalent IPv6 spellings and hostname letter case identify the same key. The target may be an IPv4 address, IPv6 address, or machine name; username and alias do not enter the key identity.
- Reuse a valid existing dedicated key and print its public key again. Refuse damaged or incomplete keys; never automatically overwrite an existing key.
- Keep a generated key after decline, cancellation, or failure, and tell the user where it remains. Write the alias host entry only after the successful password test and alias selection required by #108; leave the existing SSH configuration unchanged on failure or cancellation.
- Record an explicitly confirmed remote host key in `/home/agent/.ssh/known_hosts`, even if the later password test fails. Refuse a changed host key. Preserve the connection approval and host-key confirmation required by #108 before connection and password transmission respectively.

## Reasons and alternatives

The machine boundary matches #108's dedicated-key scope and the confirmed decision: multiple usernames or aliases for the same normalized target will reuse one identity. Per-`user@host` keys would add identities beyond that chosen boundary.

Stable key identity and reuse let a retry present the same public key, including when the user has already copied it to the remote machine. Refusing every existing key would require manual recovery, and replacing or deleting a key after displaying it could invalidate that work. Retaining keys leaves unused credentials in home data, which is why the command reports their location.

Keeping explicitly confirmed host trust separates the user's trust decision from whether password authentication succeeds. Deferring persistence until a successful password test would repeat that confirmation after an unrelated authentication failure. A separate trust file would require additional configuration for later SSH use; the chosen location is the agent user's usual `known_hosts` file.

## Boundaries

The workflow will write keys, confirmed host trust, and aliases as `agent` in sandbox home data (ADR-0006). Every agent in the sandbox will share access to the dedicated key through that user and home data. No host SSH file or host-state file will be read or written; this outbound SSH integration is distinct from the host-to-sandbox SSH setup of ADR-0002.
