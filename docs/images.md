# Images

This document describes the base image recipe in `image/context/base/` and how it checks the NodeSource signing key before it trusts the NodeSource package repository. The image build context is embedded in the host executable, as described in [ADR 0001](adr/0001-go-for-host-executable-and-in-container-manager.md).

## Base image

The base image is Debian 13 (trixie) slim, which uses glibc, with Node.js 24 LTS installed on top. Debian 13 is the current stable release, while Debian 12 is only in its LTS period. Node.js 24 remains an LTS release.

Every toolchain's packages are available for Debian 13. This choice rests on that statement. This repository does not verify it; the per-toolchain builds and smoke checks (the acceptance criteria of Feature #3, and the live suite) do.

The contents of the base image are set by Feature #3. This recipe installs Node.js only, plus the packages it needs to fetch and check the key: `ca-certificates`, `curl`, and `gnupg`.

### NodeSource signing key check

The recipe in `image/context/base/Containerfile` downloads the key from https://deb.nodesource.com/gpgkey/nodesource-repo.gpg.key into a temporary file. It then runs `check-nodesource-key.sh`, which compares the key in that file with the expected fingerprint in `nodesource-signing-key.fingerprint`. Only after the check passes does the recipe dearmor the key into `/etc/apt/keyrings/nodesource.gpg` and write the apt source line with `signed-by` pointing at that file.

The check requires exactly one primary key in the downloaded file. If the primary fingerprint differs from the expected one, the script fails and prints the expected and the found fingerprint. If the file holds other than one primary key, the script fails and prints the count, the expected fingerprint, and every primary fingerprint it found. Nothing from the NodeSource repository is installed after a failed check, because the keyring and the source line are never written. A failed check fails the image build.

## The expected fingerprint

The expected fingerprint is `6F71F525282841EEDAF851B42F59B5F99B1BE0B4`, stored in `image/context/base/nodesource-signing-key.fingerprint`. It is the fingerprint of the primary key. The key file holds one primary key (`2F59B5F99B1BE0B4`, uid `NSolid <nsolid-gpg@nodesource.com>`) and one subkey.

The key was fetched from deb.nodesource.com on 2026-10-11.

There is no independent confirmation of this fingerprint. NodeSource's own repository (nodesource/distributions, at commit 9b431d8 of 2026-07-09) publishes no fingerprint of this key. A web search found the same fingerprint only in third-party install logs, such as forum posts from users running NodeSource setup scripts. Those logs are not a source independent of deb.nodesource.com, so the expected fingerprint does not rest on them.

The fingerprint therefore records the key as fetched and reviewed when it was added. It detects a later change of the key. It does not detect a key that was already substituted at the time of fetching.

## Known limit: SHA-1 signature

The key's subkey binding signature, created in 2016, uses SHA-1. This was verified with `gpg --list-packets` on the fetched key. Third-party reports say that Debian 13 refuses NodeSource repositories whose binding signatures use SHA-1. One such report is in https://github.com/nodesource/distributions/issues/1920. These reports were not verified here.

This repository has not reproduced that refusal. The fingerprint check does not address it, since it compares the primary key's fingerprint and says nothing about which hash the signatures use. The live base build decides whether apt accepts this key.

## Key rotation

If NodeSource rotates its signing key, the downloaded key no longer matches the expected fingerprint and the check fails. `build` then stops and builds no image for any toolchain set. The previous images stay usable under their tags. `up` cannot build a missing base image until the recipe is updated.

To update the expected fingerprint, the maintainer takes these steps:

1. Fetch the new key.
2. Confirm its primary fingerprint with gpg, for example `gpg --show-keys` with `--with-fingerprint`, in an isolated `GNUPGHOME`. When possible, also confirm it from a source independent of deb.nodesource.com.
3. Write the new fingerprint into `image/context/base/nodesource-signing-key.fingerprint`.
4. Replace the fixture `image/test/nodesource/nodesource-repo.gpg.key` with the new key.
5. Update the source, date, and key details in the section above.
6. Run the offline test. It reads the expected fingerprint from the fingerprint file, so the test needs no edit.
7. Commit.

Changing the fingerprint file changes the asset hash and therefore the image tags.

## Tests and limits

The offline test is run with `bash image/test/nodesource/test-check-nodesource-key.sh`. It needs no network. It checks that the key with the expected fingerprint is accepted, that a different fingerprint is refused with both fingerprints named, that a file with two primary keys is refused with the count and both fingerprints named, that an empty file is refused, that the fingerprint file holds 40 uppercase hex characters followed by one LF, and, statically, that the recipe runs the check before it writes the keyring and the source list.

Several things are not covered here:

- The embedding of the fingerprint file in the build context, and the line-ending normalization at build time (issue #12). This change sets LF line endings for `image/context/**` and `image/test/**` in `.gitattributes`; issue #12 checks that the build uses them.
- The Microsoft signing key.
- Digests of the pulled Debian image or of built images.
- The live build against the real NodeSource key (issue #18 and the live suite).
