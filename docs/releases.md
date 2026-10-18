# Releases

This page tells you what a release consists of, how to verify a downloaded file, how previews and stable releases differ, which gates a stable release passes, and which install methods exist.

## Status

The [releases list](https://github.com/grauzone-labs/sandboxed-agents/releases) of `grauzone-labs/sandboxed-agents` is the record of what has been published: a stable release appears as a GitHub release, a preview as a prerelease. A version that is not listed there has not been published, and the npm and NuGet registries hold a package from this repository only for stable releases ([Versions](#versions)). The quick guides write the tag as a placeholder; take the tag of the release you install from that list.

## Release files

| File | Content |
|---|---|
| `sandboxed-agents-linux-amd64` | The Linux executable. |
| `sandboxed-agents-windows-amd64.exe` | The Windows executable. |
| `SHA256SUMS` | The SHA-256 checksums of both executables. |
| `sandboxed-agents-X.Y.Z.tgz` | The [npm package](npm-package.md), with both executables and `SHA256SUMS`. |
| `SandboxedAgents.X.Y.Z.nupkg` | The [NuGet package](nuget-package.md), with the Windows executable and `SHA256SUMS`. |

**Every release file is attested.** Each of the five files carries a build provenance attestation. A release file without a verifiable attestation does not belong to a release; checking this on a preview is item `preview-attestation` of the [manual checklist](manual-checklist.md).

Builds are reproducible: the same commit and tag yield byte-identical executables and packages on Linux and Windows. `sandboxed-agents version` prints the version and `asset-sha256 HASH`, the hash of the embedded build assets, which is the same on every platform for one commit.

## Verify a download

Verification uses two checks that answer different questions.

**Checksum.** `SHA256SUMS` lists the expected digest of each executable. A match shows that the file is the one the checksum file describes and was not damaged in transit. It does not show who built it, because the checksum file travels with the download.

**Attestation.** A build provenance attestation is signed evidence of which repository, workflow, and commit built a file. The GitHub CLI checks it, once per file you use:

```text
gh attestation verify FILE --repo grauzone-labs/sandboxed-agents
```

`gh attestation verify --help` shows whether your `gh` offers the command. `gh` is optional: the executable itself needs only Podman and OpenSSH ([host prerequisites](host-prerequisites.md)). Without `gh` you can compare checksums but cannot verify provenance. The [Linux](quick-guide-linux.md) and [Windows](quick-guide-windows.md) quick guides show both checks with copyable commands.

## Versions

- A **preview** is a maintainer test build, tagged `vX.Y.Z-preview.YYYYMMDD.N` (for example `v1.0.0-preview.20261101.1`) and published as a GitHub prerelease. It is **not** published to the npm or NuGet registry; install it from its release files.
- A **stable release** is tagged `vX.Y.Z` on the same commit as a validated preview and published as a GitHub release, as `sandboxed-agents` on npm, and as `SandboxedAgents` on NuGet. Version 1.0.0 is the first one.
- The stable release is rebuilt from the validated commit. Only the version string differs from the preview binaries; the release checks that `version` prints the same asset hash for both.
- Release notes are committed per version in `docs/release-notes/X.Y.Z.md`, before the preview tag, and belong to the validated commit. A preview uses the notes of its base version `X.Y.Z`. The notes of version 1.0.0 are [`release-notes/1.0.0.md`](release-notes/1.0.0.md); they state what the version contains and no gate result.
- Previews and stable releases are made by one workflow, `.github/workflows/release.yml`, which runs when a tag matching `v*` is pushed. [Publishing a stable release](maintainers.md#publishing-a-stable-release) in the maintainer notes records what it relies on and how a maintainer recovers an interrupted publication.

## Gates

A preview is validated when three gates passed on its commit:

1. **Offline suite.** No network, Podman, or credentials. It runs in CI on every commit, on Linux and Windows.
2. **Live suite.** An opt-in suite against real Podman on Linux and Windows 11, in a dedicated controller group so it cannot touch a sandbox somebody works in. A live run counts as passed only when it included the image coverage. See [Live suite](#live-suite).
3. **Manual checklist.** The [manual checklist](manual-checklist.md), run by hand on a real host.

The three gates are separate. An offline result is never presented as a live or manual result, and an offline test does not prove that isolation works on a real host. The runs on record are listed in the [maintainer notes](maintainers.md#live-runs-on-record).

### Live suite

The live suite is the test `TestLiveSuite` in `cmd/sandboxed-agents`. It runs only with `SANDBOXED_AGENTS_LIVE_TESTS=1`; without that exact value the offline suite skips it and calls no real Podman. It takes its controller group from `SANDBOXED_AGENTS_GROUP`, which must name a dedicated group other than `default` that matches `^[a-z0-9][a-z0-9-]*$`, and the directory for its record from `SANDBOXED_AGENTS_LIVE_RECORD_DIR`. With the `default` group, an unset or invalid group, or no record directory, the suite exits non-zero before it calls Podman and writes no record. Its sandboxes are `live-lifecycle`, `live-update`, `live-agent`, and, in the image coverage, `image-none`, `image-dotnet`, `image-playwright`, `image-azure`, and `image-native`; because the group is a namespace of its own, these names cannot collide with sandboxes of another group.

```sh
SANDBOXED_AGENTS_LIVE_TESTS=1 \
SANDBOXED_AGENTS_GROUP=live \
SANDBOXED_AGENTS_LIVE_RECORD_DIR="$HOME/live-suite-records" \
  go test ./cmd/sandboxed-agents -run '^TestLiveSuite$' -timeout 120m -v
```

Run it from the repository root of a clean checkout at the commit under test, with the Go version from `go.mod` on `PATH`; the record names that commit. Add `SANDBOXED_AGENTS_LIVE_IMAGES_TESTS=1` to select the image coverage, which a release run must include. The suite's cases, each a subtest and a check of the record:

| Check | What it does |
|---|---|
| `preflight` | Runs `check` with the development executable, then compiles the prepared executable with the embedded manager ([maintainer notes](maintainers.md#development-builds-with-the-manager)) that every later case uses, and reads its asset hash. When it fails, no later case runs. |
| `lifecycle` | `up` on the existing base image (or lets `up` build it), `list`, `check NAME`, `stop`, `start`, and `remove NAME --volumes`, with the SSH server check of the image coverage after the creation and after the restart. |
| `update` | Creates a sandbox with `--with native`, authorizes a test key, runs one successful `update NAME --with none` to the existing base image, then one forced rollback: `update NAME --with native --force` with only the readiness probe's `ssh` replaced by a failing one, which must restore the sandbox, its host key, and its authorization. |
| `agent` | `agents enable NAME opencode` through a real npm install, `agents status` with the installed version, `agents session` started through a pseudo-terminal the suite provides and detached with the tmux keys, then `agents status` must report the running session; `agents session --stop`, `agents disable`, and the corresponding status reports. It signs in nowhere, uses no credential, and needs the network for the npm install only. |
| `images` | The image coverage described under [Live image tests](images.md#live-image-tests): one sandbox per toolchain set and one without, the smoke checks, the suite's only `build`, and the outdated marks. It runs only when selected; otherwise the record says `not-run`. |

The suite writes its record before it calls any program, as a failing record with every check `not-run`, and replaces it atomically with the final one when it ends, so an interrupted run leaves a failing record. The record is `live-suite-linux.json` or `live-suite-windows-11.json` in the record directory, in the format under [Validation records](#validation-records): `image_part_selected` says whether the image coverage was selected, `image_part_ran` whether its `build` exited with status 0, and `image_coverage_complete` whether the whole `images` case passed, its cleanup included. The record holds the fixed check names and nothing from the host: no names of the group, sandboxes, containers, volumes, or images, no paths, output, or error messages. The suite's log, which `go test -v` prints, holds those details.

Every case removes its sandbox and volumes at its end; at the end of the run the suite checks that no container, backup container, or volume of its controller group remains, and it removes the group's host state. It leaves every image it built; removing them afterwards is a maintainer step ([maintainer notes](maintainers.md#running-the-live-suite)).

### Validation records

A validation of a preview consists of three records, all for the commit of the preview, uploaded as files to its GitHub prerelease:

| Record | File name |
|---|---|
| Live suite on Linux | `live-suite-linux.json` |
| Live suite on Windows 11 | `live-suite-windows-11.json` |
| Manual checklist | `manual-checklist.json` |

Each record is one JSON object encoded in UTF-8. The exact fields of the manual-checklist record, and what each check contains, are in the [manual checklist](manual-checklist.md#the-record). A live-suite record has `schema_version` 2 and the fields `kind`, `commit`, `platform`, `result`, `image_part_selected`, `image_part_ran`, `image_coverage_complete`, and `checks`. A record with a missing or an additional field is rejected.

### Stable release check

On a stable tag, the release workflow checks that:

- a prerelease exists on the same commit, and its tag has the same version;
- the three validation records are present on it, under their file names;
- each record names that commit, has the right schema and kind, and reports a pass;
- each live-suite record says the image coverage ran and completed;
- the offline suite is green on the commit;
- the asset hash printed by `version` is the same for the preview and the stable binaries. The preview executable that prints it is run only after the exact downloaded file passes `gh attestation verify` for this repository, `release.yml`, the candidate commit, and the preview tag, and it runs without the workflow's credentials ([maintainer notes](maintainers.md#preview-provenance-before-execution)).

When a check fails, the workflow fails with a message that names the missing preview, file, or gate, and publishes nothing, neither a GitHub release nor a registry package. The decision logic is covered by offline tests without network or credentials; publication itself is observed on a real stable release and recorded as such, never as an offline result.

### Stable release publication

When the check passes, the workflow rebuilds the tagged commit, attests all five release files, and publishes them as a GitHub release that is not a prerelease. The same run publishes the npm package as `sandboxed-agents` and the NuGet package as `SandboxedAgents` through trusted publishing: the run identifies itself to each registry by OIDC and receives a short-lived token, so the repository holds no long-lived registry token. The text of the GitHub release states, for each of the three gates (the offline suite; the live suite on Linux and Windows 11; the manual checklist), the commit it passed on. The live suite is one gate with two records, so it has one line, and the stable release check has already required both records to name the release commit. A preview tag runs the same build and attestation and ends in a GitHub prerelease; it publishes to neither registry.

The publish job runs in the GitHub environment `release`, which must admit only `v*` tags, and both registry policies name that environment. The release files, the trusted publisher settings, the tag ruleset that keeps `v*` tags from being created by others, moved, or deleted, and the recovery from an interrupted publication are described for maintainers in [Publishing a stable release](maintainers.md#publishing-a-stable-release).

## Installing

- Downloaded binary: the [Linux](quick-guide-linux.md) and [Windows](quick-guide-windows.md) quick guides.
- [npm package](npm-package.md), on Linux x64 and Windows 11 x64.
- [NuGet package](nuget-package.md), on Windows 11 x64.

Installing, upgrading, or removing the executable by any method never calls Podman and never touches sandboxes, SSH configuration, or sandbox data. Sandboxes keep running across an upgrade and keep their image until you run `update`. No command removes old images ([images](images.md#no-image-removal)).
