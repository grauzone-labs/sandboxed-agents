# Install from npm

This page describes npm tarballs built from `grauzone-labs/sandboxed-agents`: what they contain, how to install, upgrade, and remove them, and what their checks prove.

## Publication

The [registry metadata for `sandboxed-agents@0.2.0`](https://registry.npmjs.org/sandboxed-agents/0.2.0) points to the prior repository, `grauzone-git/sandboxed-ai-agents`. The shared package and command name alone does not identify a release of `grauzone-labs/sandboxed-agents`.

This repository has not published a release yet, so no `sandboxed-agents` tarball or registry version from it exists. Publication and migration from the earlier package are tracked in [#36](https://github.com/grauzone-labs/sandboxed-agents/issues/36).

The examples below install an explicit tarball from this repository once one is released. They make no claim about registry availability or compatibility with the earlier implementation.

## Requirements

- Linux x64 or Windows 11 x64. Other platforms, including ARM64, are unsupported. On an unsupported platform, installation fails with a message that names the platform.
- Node.js 18 or newer, the `engines` range of the package.
- Podman ([host prerequisites](host-prerequisites.md)) and OpenSSH, as for every install method. The package does not install or configure them.

## What the package contains

The package is named `sandboxed-agents` and provides the command `sandboxed-agents`. The command is a small Node.js launcher. The package archive `sandboxed-agents-X.Y.Z.tgz` holds:

- the Linux x64 and the Windows x64 executables;
- `SHA256SUMS`, the checksums of both executables;
- the launcher (`launcher.cjs`, `verify.cjs`) and `package.json`.

The launcher selects the executable for the current platform, checks it against `SHA256SUMS`, and runs it. It passes on the arguments, the standard streams, and the exit status. The package has no dependencies.

## Install

Once this repository publishes a release tarball, download `sandboxed-agents-X.Y.Z.tgz` and install that file. Replace `X.Y.Z` with its version. If the shared command is already installed, identify its source before replacing it; migration from the earlier implementation is tracked in [#36](https://github.com/grauzone-labs/sandboxed-agents/issues/36).

```sh
npm install --offline --no-audit --no-fund -g ./sandboxed-agents-X.Y.Z.tgz
sandboxed-agents version
```

`version` prints `sandboxed-agents X.Y.Z` and `asset-sha256 <hash>`, the hash of the embedded build assets. This install needs no network access once the file is on the machine.

## Upgrade

To upgrade an installation from this repository, run the same install command with the newer release tarball.

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
