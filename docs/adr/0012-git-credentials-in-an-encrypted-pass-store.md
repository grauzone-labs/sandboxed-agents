---
status: accepted
---

# Git credentials in an encrypted pass store

[#143](https://github.com/grauzone-labs/sandboxed-agents/issues/143) replaces the plaintext `credential-store` file with Git Credential Manager (GCM) and its `gpg` credential store. During triage of #143 the maintainer approved how GCM 3.0.2 is admitted into the image, which workflow sets up and unlocks the store, which key protects it, how long an unlock must last, and which mechanism holds it. This ADR records those five decisions. They are accepted design; #143 now implements them.

## Decisions

### GCM provenance

GCM 3.0.2 is not packaged by Debian, and its [GitHub release](https://api.github.com/repos/git-ecosystem/git-credential-manager/releases/tags/v3.0.2) has no checksum or signature file among its assets. The maintainer approved a narrow exception like #113's:

- A maintainer downloads the selected GCM 3.0.2 x64 release asset, computes its SHA-256, and compares it with the `digest` that GitHub's release metadata reports for that asset. The reviewed hash is pinned in the recipe only when both agree.
- Every image build checks the downloaded asset against the pinned hash and fails on a mismatch, before anything from the asset is installed.
- The maintainer notes record the source, the date, the pinned hash, and how it was reviewed, and state that the pin fixes reviewed bytes from a GitHub release; it does not independently authenticate the publisher.
- Only x64 is pinned. The product supports Linux amd64 and Windows x64 hosts only ([host prerequisites](../host-prerequisites.md)).

The considered alternative was to wait for stronger upstream verification, such as signatures or attestations, which would leave #143 blocked until upstream publishes them. The chosen exception follows the precedent of the Azure DevOps keyring wheels in #113 ([maintainer notes](../maintainers.md#microsoft-azure-cli-signing-key-and-extension)): a reviewed hash detects any later change of the bytes, though not a substitution before the review. Each GCM upgrade needs the same review and a new pin.

### Setup and unlock surface

- `integrations config NAME git credentials` creates the encrypted store on first use and unlocks it on every later run.
- It requires an interactive terminal and, without one, fails before any write or passphrase read.
- No managed workflow configures the plaintext `credential-store` any more.
- It keeps the per-host helper entries that the GitHub login workflow writes for `github.com` and `gist.github.com`, whichever of the two runs first.
- A successful first run leaves the store unlocked.
- A run with a wrong passphrase clears any earlier unlock before it fails, so it always leaves the store locked.

The considered alternatives were a separate workflow name for unlocking, and keeping the plaintext store as an explicit opt-in. This decision breaks the documented contract that `git credentials` needs no terminal ([integrations](../integrations.md#git-credential-helper)) and the test that enforces it.

### Store key

- One dedicated GPG key protects the `pass` store and is used for nothing else: a primary key with an encryption subkey, protected by the user's passphrase, with no expiry.
- The store uses a dedicated GnuPG home and gpg-agent. The manager-owned, non-prompting pinentry is configured only for that agent; the user's default GnuPG home and its other keys keep their existing pinentry behavior.
- No managed workflow exports the key.
- A lost passphrase cannot be recovered: the user resets the store and stores the credentials again by signing in.

The considered alternative was the same key with an expiry, after which new credentials could not be encrypted to it until the user extended it. The key algorithm is an implementation detail; how the passphrase is entered is set by the unlock mechanism below.

### Unlock lifetime

- After an unlock, the store stays unlocked while the sandbox runs, with no time limit and no idle timeout. A `SIGHUP` to gpg-agent flushes the cached passphrase, so the store locks and needs another unlock even if the agent keeps running ([GnuPG 2.4.7 source](https://github.com/gpg/gnupg/blob/gnupg-2.4.7/agent/gpg-agent.c)).
- `stop`, `restart`, and `update` lock it again; the next `git credentials` run unlocks it.
- If gpg-agent ends, for example by a crash or an out-of-memory kill, the unlock is lost and the store is locked; no availability beyond gpg-agent's life is promised.

The considered alternative was a finite absolute limit after which the user unlocks again. Plain gpg-agent settings do not meet the requirement: in GnuPG 2.4.7, `max-cache-ttl` also expires preset passphrases, and `0` expires them rather than lifting the bound ([`agent/cache.c`](https://github.com/gpg/gnupg/blob/gnupg-2.4.7/agent/cache.c#L197-L245), [`agent/command.c`](https://github.com/gpg/gnupg/blob/gnupg-2.4.7/agent/command.c#L2551-L2656)).

### Unlock mechanism

The store is unlocked through gpg-agent's generic memory and a pinentry adapter that never prompts:

- The `git credentials` workflow reads the passphrase on the terminal without echo and verifies it against the store key, even when a cache is warm. Only a verified passphrase is stored. A run that fails verification has already cleared any earlier unlock.
- It stores the passphrase with `PUT_SECRET` and TTL `-1` under a name that only this product uses, never as a cache entry under the key's keygrip. gpg-agent exempts these named data entries from the idle and `max-cache-ttl` expiry ([`agent/cache.c`](https://github.com/gpg/gnupg/blob/gnupg-2.4.7/agent/cache.c#L197-L245), [`agent/command.c`](https://github.com/gpg/gnupg/blob/gnupg-2.4.7/agent/command.c#L3406-L3587)).
- The process that verified the passphrase sends it inline in the `PUT_SECRET` command over gpg-agent's private Unix socket, never in a process argument, environment variable, file, or log. In the proof, the confidential-inquiry form was acknowledged but stored no data; its cause was not established.
- A pinentry adapter owned by the manager is gpg-agent's only pinentry. It reads the passphrase with `GET_SECRET`, never prompts, and fails when the passphrase is absent or gpg-agent does not answer. It replaces the `pinentry-tty` and `allow-preset-passphrase` proposal of #143.
- The Git credential helper runs GCM with the nonsecret `GPG_TTY=/dev/null`, because GCM refuses GPG operations when neither `GPG_TTY` nor `SSH_TTY` is set ([`CredentialStore.cs`](https://github.com/git-ecosystem/git-credential-manager/blob/0d95999b8fb39bd1045770fe1d69f6a2b07101d7/src/Core/CredentialStore.cs#L269-L291)).
- The whole GCM helper process runs under a documented, finite deadline with a kill grace period. The adapter's own limit cannot bound a gpg-agent stall that happens before gpg-agent starts the pinentry.
- The passphrase travels only over private pipes and sockets between these processes and gpg-agent, and never appears in user-visible output, diagnostics, or logs. gpg-agent runs without IPC debugging or logging.
- No manager-supervised process is added; a separate process holding the passphrase is not approved.

A [local proof](https://github.com/grauzone-labs/sandboxed-agents/issues/143#issuecomment-6909531269) of this mechanism passed in a minimal Debian 13 Podman image with GnuPG 2.4.7, Git 2.47.3, and GCM 3.0.2. It showed headless lookups from a separate `exec` against the same gpg-agent; during the finite observation, no expiry under short `default-cache-ttl` and `max-cache-ttl` values; rejection of a wrong passphrase with the ordinary cache warm, leaving the store locked; fast failure with gpg-agent gone, and failure at the helper deadline with gpg-agent stalled; no deadlock under concurrent lookups; a locked store after container stop and start, restart, and replacement, each unlocked again by a rerun; and no test passphrase in 18 home files, the container log, or 220 sampled process argument and environment snapshots. The inline `PUT_SECRET`, `GPG_TTY`, and whole-helper deadline conditions above came from that proof. It did not use the repository base image or the manager, so it does not validate the product.

## Status

The design is implemented in #143. The [maintainer notes](../maintainers.md#repeatable-local-git-credential-check) record validation evidence and remaining proof limits.

## Implementation choices

- Whole GCM invocation deadline: 10 seconds, followed by one second of kill grace.
- Key algorithm: Ed25519 certification primary plus Curve25519 encryption subkey, both protected by the passphrase, without expiry.
- Asset: `gcm-linux-x64-3.0.2.tar.gz`, with the reviewed SHA-256 recorded in the recipe and maintainer notes. Comparable release-overlay builds measured growth of 122,307,668 bytes (116.64 MiB).
