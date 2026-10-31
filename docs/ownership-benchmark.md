# Isolated Linux ownership-mapping measurement

This diagnostic does **not** fix the unestablished storage-latency cause. The failing
Playwright create at `0eb167b` spent many minutes in ownership mapping/copy-up; that
observation does not identify a faulty driver, backing filesystem, or host setting.
Do not change the current store to test a guess.

The benchmark is the Go command `internal/ownershipbench/cmd/benchmark-ownership`; it needs
`findmnt`, rootless local Podman, a non-root Linux user, and an **already built, supplied
image archive**. It builds and pulls nothing. The previous live
invocation's Playwright images were cleaned up; an image/archive must be supplied
before this experiment can run. Running it on either project host requires the
live-validation skill's new-invocation checkpoint. No benchmark has been run live.

After consent, save the existing source image's full ID and `podman info --format
json` to `baseline-info.json`. Save that image with `podman save --format oci-archive
--output IMAGE.tar sha256:FULL_ID`. These preparation commands use the source store;
the benchmark itself only reads the supplied files and invokes Podman with an
explicit fresh `--root`, `--runroot`, `--storage-driver` and `--remote=false`.

Choose a conservative **additional-space** bound before execution, accounting for
the archive's expanded layers, load temporary data, the mapped writable copy and
workspace. Compressed archive size alone is insufficient. `--peak-bytes` is required
and free space must exceed it; a sampled reserve also stops a command as space runs
low. Record the estimate's basis beside the result. The archive already occupies
space and is never deleted by this command.

```sh
go run ./internal/ownershipbench/cmd/benchmark-ownership \
  --archive /path/to/IMAGE.tar --image sha256:FULL_ID \
  --baseline-info /path/to/baseline-info.json \
  --parent /path/on/current-filesystem/benchmarks --driver overlay \
  --peak-bytes CONSERVATIVE_ADDITIONAL_BYTES --timeout 900
```

The parent must already exist, be writable by the rootless user, and lie outside
the active graphroot/runroot. A random mode-0700 directory is created beneath it.
No existing storage/configuration file is edited. The command supplies a private
storage configuration and XDG directories, clears user configuration overrides,
and explicitly selects the volume path. Existing user storage configuration is
not reused; system container defaults may still apply. Inspect the recorded actual
store information and Podman command prefix, including the mount helper. If comparing an
existing fuse-overlayfs setup, add `--mount-program /absolute/path/to/fuse-overlayfs`;
omitting it tests native overlay and is a different configuration. The saved
baseline and measured store information let the reviewer see that difference.

Run once on the current backing filesystem in a fresh store, then once on an
already available, supported local filesystem with the same image, user, driver,
helper and bounds. Compare cold `create` times separately from `load`. A fresh
store on the current filesystem controls for accumulated store state; it does not
reproduce the old store's fragmentation or cache history. A `--driver vfs` run is a
separate diagnostic comparison, not a recommended performance fix. Never use NFS
or another filesystem unsupported by rootless Podman. If no supported candidate
exists, report that prerequisite rather than provisioning or reconfiguring one.

Each result directory retains `report.json`, command stdout/stderr, per-command
elapsed times and `/proc` samples (local process-group IDs, states, wait channels,
I/O counters and available space). Missing `/proc` facts are recorded, not inferred.
The report includes kernel, Podman version, baseline/actual storage information,
backing filesystem, archive hash and exact imported image/container IDs. It uses
`keep-id:uid=1000,gid=1000` with the root start identity, then verifies work as
`agent` and a workspace file owned by 1000:1000. Networking and image pulls are
disabled. It deliberately omits SSH, the home volume and application startup to
isolate the ownership-mapping/create path.

After successful proof, cleanup rechecks the invocation label and stopped state,
removes its container by exact ID, its labelled workspace volume, and only the
supplied/verified image with `rmi --no-prune`. Imported parent images whose individual
provenance is not recorded remain listed in the report. The store directory and
logs remain; the command never prunes, resets, recursively deletes a store, or touches
source images. Retained storage and rootless runtime state need separate inspection
before any later removal.

A timeout, SIGINT or SIGTERM kills the command's local process group and **retains the
store without further cleanup commands**. Detached mapping helpers or engine work
may still exist. Inspect the recorded `podman_prefix`, process samples, object IDs
and engine state before any cleanup; never interpret a killed client as proof that
mapping stopped. Do not remove a graphroot while an operation or mount is unverified.
Failure to start a process also retains the new store without cleanup commands.
SIGKILL or host failure cannot run that handler; reconcile the recorded state manually.
The command runs no automatic timeout retry or host repair.

References: [Podman storage and rootless options](https://docs.podman.io/en/latest/markdown/podman.1.html),
[archive loading](https://docs.podman.io/en/latest/markdown/podman-load.1.html),
[container creation](https://docs.podman.io/en/latest/markdown/podman-create.1.html).
