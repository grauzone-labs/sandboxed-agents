# Install from npm

This page tells you how to install, upgrade, and remove the `sandboxed-agents` command from the npm package, what the package contains, and what its checks prove.

## Where the package comes from

A stable release is published to the npm registry as `sandboxed-agents`, and every release, preview or stable, carries the tarball `sandboxed-agents-X.Y.Z.tgz` among its [release files](releases.md#release-files). Previews are not published to the registry; install a preview from its tarball. Registry versions below 1.0.0 belong to the earlier implementation in `grauzone-git/sandboxed-ai-agents`, not to this repository: the shared package and command name alone does not identify a release of `grauzone-labs/sandboxed-agents`. If the command is already installed on your machine, identify its source before you replace it.

## Requirements

- Linux x64 or Windows 11 x64. Other platforms, including ARM64, are unsupported; on one of them, installation fails with a message that names the platform.
- Node.js 18 or newer, the `engines` range of the package.
- Podman and OpenSSH ([host prerequisites](host-prerequisites.md)), as for every install method. The package does not install or configure them.

## What the package contains

The package is named `sandboxed-agents` and provides the command `sandboxed-agents`, a small Node.js launcher. The archive holds:

- the Linux x64 and the Windows x64 executables;
- `SHA256SUMS`, the checksums of both executables;
- the launcher (`launcher.cjs`, `verify.cjs`) and `package.json`.

The launcher selects the executable for the current platform, checks it against `SHA256SUMS`, and runs it. It passes on the arguments, the standard streams, and the exit status. The package has no dependencies.

## Install

From a release tarball, with `X.Y.Z` replaced by its version:

```sh
npm install --offline --no-audit --no-fund -g ./sandboxed-agents-X.Y.Z.tgz
sandboxed-agents version
```

`version` prints `sandboxed-agents X.Y.Z` and `asset-sha256 <hash>`, the hash of the embedded build assets. This install needs no network access once the file is on the machine. A stable release can also be installed from the registry with `npm install -g sandboxed-agents@X.Y.Z`.

## Upgrade

Run the same install command with the newer release.

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

The checksums detect corruption and modification of an executable relative to the `SHA256SUMS` in the same package. They do not show who built the package: the manifest travels in the same archive, so someone who can change the package can change both files. Treat a passing check as "the files are as the package says", not as proof of origin; the [attestation](releases.md#verify-a-download) of the tarball proves where it was built.

## Workspace binds

Only `/workspace` may be a host bind. A bind that contains or lies inside one of the [protected host paths](../CONTEXT.md) is rejected before Podman is called. For the npm package, those paths include:

- the executables and the launcher;
- the launcher's `.cmd` and `.ps1` shims on Windows;
- the link that puts the command on your `PATH`.

Symlinks, junctions, and other aliases do not make these paths bindable. If `up` rejects your workspace, choose a directory outside the npm installation, for example your project directory.
