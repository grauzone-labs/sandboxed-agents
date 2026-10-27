---
status: accepted
---

# Outbound SSH key identity and retention

The SSH integration of [#108](https://github.com/grauzone-labs/sandboxed-agents/issues/108) uses one dedicated Ed25519 key per remote machine, independent of the remote username and alias. The user confirmed in [#109](https://github.com/grauzone-labs/sandboxed-agents/issues/109) that valid keys are reused and retained after decline, cancellation, or failure, and that explicitly confirmed remote host keys remain trusted even if password authentication later fails. These are accepted design decisions; #108 implements them separately.

## Decisions

- The private key is `/home/agent/.ssh/id_ed25519_remote_<target-hash>`, with its public key at the same path plus `.pub`. The hash is derived from the normalized remote target: equivalent IPv6 spellings and hostname letter case identify the same key. The target may be an IPv4 address, IPv6 address, or machine name; username and alias do not enter the key identity.
- A valid existing dedicated key is reused and its public key printed again. Damaged or incomplete keys cause refusal; an existing key is never automatically overwritten.
- A generated key is kept after decline, cancellation, or failure, and the command tells the user where it remains. The alias host entry is written only after the successful password test and alias selection required by #108; a failed or cancelled workflow leaves the existing SSH configuration unchanged.
- An explicitly confirmed remote host key is recorded in `/home/agent/.ssh/known_hosts`, even if the later password test fails. A changed host key causes refusal. The connection approval and host-key confirmation required by #108 remain separate gates before connection and password transmission respectively.

## Reasons and alternatives

Stable key identity and reuse let a retry present the same public key, including when the user has already copied it to the remote machine. Refusing every existing key would require manual recovery, and replacing or deleting a key after displaying it could invalidate that work. Retaining keys leaves unused credentials in home data, which is why the command reports their location.

Keeping explicitly confirmed host trust separates the user's trust decision from whether password authentication succeeds. Deferring persistence until a successful password test would repeat that confirmation after an unrelated authentication failure. A separate trust file would require additional configuration for later SSH use; the chosen location is the agent user's usual `known_hosts` file.

These choices qualify for an ADR: changing persistent key identity or replacing a retained key can require migration and remote authorization changes; retained keys and trust after an unsuccessful workflow are surprising without context; and refusal, replacement, deletion, and deferred trust persistence were real alternatives.

## Boundaries

The confirmed command is `sandboxed-agents integrations config NAME ssh --host HOST --user USER`. It follows the existing integration pattern and command-first grammar (ADR-0004), so the command shape alone needs no new ADR. The manager executes the workflow as `agent`, UID and GID 1000, following ADR-0006 and the existing integration check order. No host SSH file or host-state file is read or written.

This outbound SSH integration is distinct from the host-to-sandbox SSH setup of ADR-0002. There is no conflict with ADR-0002, ADR-0004, or ADR-0006. Every agent in the sandbox shares access to the key through the shared user and home data.

Installing the public key on the remote machine and verifying key-based authentication remain outside #108. The user wants a separate future story for that work; #109 records the intent only.
