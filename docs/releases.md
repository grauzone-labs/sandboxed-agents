# Releases

This page describes the files of a release, how to verify them, how previews and stable releases differ, and the three gates a stable release passes. It also says what the repository's workflows do today and what they do not do yet.

## Status

No release of `grauzone-labs/sandboxed-agents` has been published yet. Not a stable release such as 1.0.0, and no preview either. What exists today:

- **`release-reproducibility.yml`** runs on every push to `main`, on pull requests, and on request. It runs the offline suite, builds the release files with `scripts/release.sh` under a throwaway version (`0.0.0-ci-` and the commit), and compares the asset hash and the executable and npm package digests between a Linux and a Windows runner. It publishes and attests nothing.
- **`stable-release-gate.yml`** runs when a tag matching `v*` is pushed. A preview tag passes it without further checks. A stable tag is checked for the evidence listed under [Stable release check](#stable-release-check), and the workflow fails when the evidence is missing. It publishes nothing either.

Nothing in the repository yet creates a GitHub prerelease, uploads release files, creates attestations, or publishes to npm or NuGet. That work is tracked in [#33](https://github.com/grauzone-labs/sandboxed-agents/issues/33) (gate decision and validation records), [#34](https://github.com/grauzone-labs/sandboxed-agents/issues/34) (live suite), and [#36](https://github.com/grauzone-labs/sandboxed-agents/issues/36) (stable release and trusted publishing). This page describes the release as designed; the sections below that depend on a published release say so. The [quick guides](quick-guide-linux.md) use a placeholder for the tag for that reason.

## Release files

A release consists of these files:

| File | Content |
|---|---|
| `sandboxed-agents-linux-amd64` | The Linux executable. |
| `sandboxed-agents-windows-amd64.exe` | The Windows executable. |
| `SHA256SUMS` | The SHA-256 checksums of both executables. |
| `sandboxed-agents-X.Y.Z.tgz` | The [npm package](npm-package.md), with both executables and `SHA256SUMS`. |
| `SandboxedAgents.X.Y.Z.nupkg` | The [NuGet package](nuget-package.md), with the Windows executable and `SHA256SUMS`. |

**Every release file is attested.** Each of the five files above carries a build provenance attestation: both executables, `SHA256SUMS`, the `.tgz`, and the `.nupkg`. A release file without a verifiable attestation does not belong to a release. Since no release exists yet, no attestation exists yet either; the first preview shows whether the release process delivers them, and checking that is item `preview-attestation` of the [manual checklist](manual-checklist.md).

Builds are reproducible: the same commit and tag yield byte-identical executables and packages. Text files of the build assets are normalized to Unix line endings when the executable is built, before they are embedded and hashed, so one commit yields the same asset hash and image tags on every platform. `sandboxed-agents version` prints the version and `asset-sha256 HASH`, the hash of the embedded build assets.

## Verify a download

Install Podman and OpenSSH first ([host prerequisites](host-prerequisites.md)); the executable needs nothing else. Verification uses two checks that answer different questions.

**Checksum.** `SHA256SUMS` lists the expected digest of each executable. A match shows that the file is the one the checksum file describes, and that it was not damaged in transit. It does not show who built it, because the checksum file travels with the download.

**Attestation.** A build provenance attestation is signed evidence of which repository, workflow, and commit built a file. The GitHub CLI checks it:

```text
gh attestation verify FILE --repo grauzone-labs/sandboxed-agents
```

Use a version of `gh` that offers the command: `gh attestation verify --help` shows whether yours does. `gh` is optional: the downloaded executable needs only Podman and OpenSSH. Without `gh` you can compare checksums but cannot verify provenance. Run the command once per file you use.

The [Linux](quick-guide-linux.md) and [Windows](quick-guide-windows.md) quick guides show both checks with copyable commands.

## Versions

- A **preview** is a maintainer test build. It is tagged `vX.Y.Z-preview.YYYYMMDD.N`, for example `v1.0.0-preview.20261101.1`, and is published as a GitHub prerelease. It is **not** published to the npm or NuGet registry.
- A **stable release** is tagged `vX.Y.Z` on the same commit as a validated preview. Version 1.0.0 is the first one. It is published as a GitHub release that is not a prerelease, and as `sandboxed-agents` on npm and `SandboxedAgents` on NuGet. Until it exists, the registries hold no package from this repository, and you install from the files of a release ([npm package](npm-package.md), [NuGet package](nuget-package.md)).
- The stable release is rebuilt from the validated commit. Only the version string differs from the preview binaries, and the release checks that `version` prints the same asset hash for both.
- Release notes are committed per version, before the preview tag, and belong to the validated commit.

## Gates

A preview is validated when three gates passed on its commit:

1. **Offline suite.** No network, Podman, or credentials. It runs in CI on every commit, on Linux and Windows.
2. **Live suite.** An opt-in suite against real Podman on Linux and Windows 11, in a dedicated controller group so it cannot touch a sandbox somebody works in. A live run counts as passed only when it included the image coverage. See [Live suite](#live-suite).
3. **Manual checklist.** The maintained [manual checklist](manual-checklist.md), run by hand on a real host.

The three gates are separate. An offline result is never presented as a live or manual result, and an offline test does not prove that isolation works on a real host.

### Live suite

The live suite is the test `TestLiveSuite` in `cmd/sandboxed-agents`. It runs only with `SANDBOXED_AGENTS_LIVE_TESTS=1`; without that exact value the offline suite skips it and calls no real Podman. It takes its controller group from `SANDBOXED_AGENTS_GROUP`, which must name a dedicated group other than `default` that matches `^[a-z0-9][a-z0-9-]*$`, and the directory for its record from `SANDBOXED_AGENTS_LIVE_RECORD_DIR`. With the `default` group, an unset or invalid group, or no record directory, the suite exits non-zero before it calls Podman, and writes no record. The suite's sandboxes are `live-lifecycle`, `live-update`, `live-agent`, and, in the image coverage, `image-none`, `image-dotnet`, `image-playwright`, `image-azure`, and `image-native`; because the group is a namespace of its own, these names cannot collide with sandboxes of another group.

```sh
SANDBOXED_AGENTS_LIVE_TESTS=1 \
SANDBOXED_AGENTS_GROUP=live \
SANDBOXED_AGENTS_LIVE_RECORD_DIR="$HOME/live-suite-records" \
  go test ./cmd/sandboxed-agents -run '^TestLiveSuite$' -timeout 120m -v
```

Run it from the repository root of a clean checkout at the commit under test, with the Go version from `go.mod` on `PATH`; the record names that commit. Add `SANDBOXED_AGENTS_LIVE_IMAGES_TESTS=1` to select the image coverage, which a release run must include. The suite's cases, each a subtest and a check of the record:

| Check | What it does |
|---|---|
| `preflight` | Runs `check` with the development executable, then compiles the prepared executable with the embedded manager ([images](images.md#development-builds-with-the-manager)) that every later case uses, and reads its asset hash. When it fails, no later case runs. |
| `lifecycle` | `up` on the existing base image (or lets `up` build it), `list`, `check NAME`, `stop`, `start`, and `remove NAME --volumes`, with the SSH server check of the image coverage after the creation and after the restart. |
| `update` | Creates a sandbox with `--with native`, authorizes a test key, runs one successful `update NAME --with none` to the existing base image, then one forced rollback: `update NAME --with native --force` with only the readiness probe's `ssh` replaced by a failing one, which must restore the sandbox, its host key, and its authorization. |
| `agent` | `agents enable NAME opencode` through a real npm install, `agents status` with the installed version, `agents session` started through a pseudo-terminal the suite provides and detached with the tmux keys, then `agents status` must report the running session; `agents session --stop`, `agents disable`, and the corresponding status reports. It signs in nowhere, uses no credential, and needs the network for the npm install only. |
| `images` | The image coverage described under [live image tests](images.md#live-image-tests): one sandbox per toolchain set and one without, the smoke checks, the suite's only `build`, and the outdated marks. It runs only when selected; otherwise the record says `not-run`. |

The suite writes its record before it calls any program, as a failing record with every check `not-run`, and replaces it atomically with the final one when it ends, so an interrupted run leaves a failing record. The record is `live-suite-linux.json` or `live-suite-windows-11.json` in the record directory, in the format under [Validation records](#validation-records): `image_part_selected` says whether the image coverage was selected, `image_part_ran` whether its `build` exited with status 0, and `image_coverage_complete` whether the whole `images` case passed, its cleanup included. The record holds the fixed check names above and nothing from the host: no names of the group, sandboxes, containers, volumes, or images, no paths, output, or error messages. The suite's log, which `go test -v` prints, holds those details.

Every case removes its sandbox and volumes at its end; at the end of the run the suite checks that no container, backup container, or volume of its controller group remains, and it removes the group's host state. It leaves every image it built; the host agent of the `live-validation` skill removes the run's unused images afterwards ([Image cleanup](../.agents/skills/live-validation/SKILL.md#image-cleanup)).

The offline suite covers the suite's own rules without real Podman. Against fake Podman, whose preflight answers fail `check`: the refusal of the `default` group and of invalid group names before any Podman call, the record written before the first Podman use with the checkout's commit, the host's platform, every later check `not-run`, and no host detail, and the image selection recorded as given. Against a scripted executable, fake Podman, and fake `ssh`, on Linux: the whole suite runs through every case with and without the image selection, writes a passing record only with `image_part_ran` and `image_coverage_complete` true when the image coverage was selected, issues `build` exactly once with the selection and never without it, and removes every sandbox it created. The pseudo-terminal it provides counts as an interactive terminal for `agents session` on Linux and Windows. None of this is live evidence: no run of the full suite is recorded yet, and the live runs recorded under [live proof](images.md#live-proof) ran the image coverage of the earlier test alone.

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
- the asset hash printed by `version` is the same for the preview and the stable binaries.

When a check fails, the workflow fails with a message that names the missing preview, file, or gate, and publishes nothing. The decision logic is covered by offline tests without network or credentials. Publication itself can only be observed on the first real stable release, and that observation is recorded as such, not as an offline result.

## Installing

- Downloaded binary: the [Linux](quick-guide-linux.md) and [Windows](quick-guide-windows.md) quick guides.
- [npm package](npm-package.md), on Linux x64 and Windows 11 x64.
- [NuGet package](nuget-package.md), on Windows 11 x64.

Installing, upgrading, or removing the executable by any method never calls Podman and never touches sandboxes, SSH configuration, or sandbox data. Sandboxes keep running across an upgrade. A sandbox keeps its image until you run `update`.

Old images are not removed by any command, including after an upgrade. See the [README](../README.md#limitations).
