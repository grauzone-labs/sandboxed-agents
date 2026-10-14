# Install from npm

This page describes npm tarballs built from `grauzone-labs/sandboxed-agents`: what they contain, how to install, upgrade, and remove them, and what their checks prove.

## Publication

The [registry metadata for `sandboxed-agents@0.2.0`](https://registry.npmjs.org/sandboxed-agents/0.2.0) points to the prior repository, `grauzone-git/sandboxed-ai-agents`. The shared package and command name alone does not identify a release of `grauzone-labs/sandboxed-agents`.

Publishing this repository's packages, including trusted publishing, belongs to the release workflow (#36). Before recommending registry installs or upgrades from the earlier implementation, maintainers need to confirm publishing rights to the existing name, identify the versions and repository metadata belonging to this implementation, and document the migration steps.

The examples below select an explicit tarball from this repository; they make no claim about registry availability or compatibility with the earlier implementation.

## Requirements

- Linux x64 or Windows x64. Other platforms are unsupported, ARM64 included. On an unsupported platform, installation fails with a message that names the platform.
- Node.js 18 or newer, the `engines` range of the package.
- Podman and OpenSSH, as for every install method. The package does not install or configure them; see [host prerequisites](host-prerequisites.md).

## What the package contains

The package is named `sandboxed-agents` and provides the command `sandboxed-agents`. The command is a small Node.js launcher. The release archive `sandboxed-agents-X.Y.Z.tgz` holds:

- the Linux x64 and the Windows x64 executables;
- `SHA256SUMS`, the checksums of both executables;
- the launcher.

The launcher selects the executable for the current platform, checks it against `SHA256SUMS`, and runs it. It passes on the arguments, the standard streams, and the exit status. The package has no dependencies.

## Install

When a release tarball from `grauzone-labs/sandboxed-agents` is available, download `sandboxed-agents-X.Y.Z.tgz` and install that file. Replace `X.Y.Z` with its version. If the shared command is already installed, identify its source before replacing it; a transition from the earlier implementation needs migration steps documented under #36.

```sh
npm install --offline --no-audit --no-fund -g ./sandboxed-agents-X.Y.Z.tgz
sandboxed-agents version
```

`version` prints the version and the hash of the embedded build assets. This install needs no network access once the file is on the machine.

## Upgrade

For an installation from this repository, download its newer release tarball and install that explicit file over the old version:

```sh
npm install --offline --no-audit --no-fund -g ./sandboxed-agents-X.Y.Z.tgz
```

## Remove

```sh
npm uninstall -g sandboxed-agents
```

## What installing, upgrading, and removing touch

Installing, upgrading, or removing the package never calls Podman. It does not touch sandboxes, SSH configuration, or sandbox data. Sandboxes keep running and keep their data across an upgrade or a removal.

## Checksums

The package is checked in two places:

- **At install**, the package's install step compares both executables with `SHA256SUMS`. A mismatch fails the installation.
- **On every run**, the launcher compares the executable it is about to start with `SHA256SUMS`. If it does not match, the launcher refuses to run it and exits non-zero. Installing with `npm install --ignore-scripts` skips the install check, but not this one.

The checksums detect corruption and modification of an executable relative to the `SHA256SUMS` in the same package. They do not show who built the package. The manifest travels in the same archive, so someone who can change the package can change both files. Treat a passing check as "the files are as the package says", not as proof of origin.

## Workspace binds

Only `/workspace` may be a host bind. A bind that contains or lies inside one of the [protected host paths](../CONTEXT.md) is rejected, before Podman is called. For the npm package, those paths include:

- the executables and the launcher;
- the launcher's `.cmd` and `.ps1` shims on Windows;
- the link that puts the command on your `PATH`.

Symlinks, junctions, and other aliases do not make these paths bindable: a workspace that reaches one of them through an alias is rejected too. If `up` rejects your workspace, choose a directory outside the npm installation, for example your project directory.
