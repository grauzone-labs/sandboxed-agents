# Paired live-suite durations

On 2026-11-08, both platforms passed the full live suite at
`90ad6ba8eba88077126fb2fb0990f64850acfdd5`. Windows took 4,021.94 seconds and
Linux 1,638.13 seconds: **2.46 times** as long for this pair. This records elapsed
time, not a cause or a speedup.

## Run and evidence

The user answered the live-validation Yes/No checkpoint **Yes** for this revision,
then supplied both SSH aliases before either host was contacted. Each host ran the
suite once, with image coverage selected. Windows preparation included failed
command construction and a detached launch that ended before the suite executed;
neither is counted as a suite run. No host repair or tuning was performed.

Both schema 2 records report all six checks passing and
`image_part_selected`, `image_part_ran`, and `image_coverage_complete` true:
[Linux](live-suite-linux.json) and [Windows](live-suite-windows-11.json).
The [Linux T3 Code supplement](t3code-linux.json) and
[Windows T3 Code supplement](t3code-windows-11.json) both pass, including service
restore after replacement and rollback. Their recorded stable, pinned, and nightly
Tool versions match between platforms.

The verbose logs remain private on their respective hosts. Each invocation's
collector succeeded with exit status 0 and printed the destination of its current
durations file. The six JSON files here are byte-for-byte copies of the records;
they retain no private host identifiers, resource names, host paths, account
details, or raw program output. Collection did not change the schema 2 records,
the release gate, or cancellation behavior.

## Comparison context

The platforms share the source revision and selected and completed coverage.
They are not otherwise a controlled comparison:

| Observation | Linux | Windows |
| --- | --- | --- |
| Host/runtime | Linux amd64, rootless overlay storage | Windows 11 amd64, rootless WSL machine |
| Effective Go toolchain | 1.27.2 | 1.27.3 |
| Podman | 5.4.2 | Client and server 6.1.4 |
| Host load at start | Unknown | Unknown |
| Matching base image at start | Absent | Absent |
| Build-cache context | Unknown; pre-existing untagged images | Cold by the inventory proxy |
| Engine images before the run | 7 | 1 |
| Containers and volumes before the run | 0 / 0 | 0 / 0 |

The context values come from the before-run inventories, classified against each
run's logged asset hash. They are retained in the
[Linux durations](durations-linux.json) and
[Windows durations](durations-windows-11.json). Unknown load on both hosts does
not establish equal load. The cache classification is an inventory proxy, not a
measurement of cache hits. Hardware and download conditions were not controlled.

The image asset hashes also differed: Linux
`e038aec8781095cc43b9c9a52439cec312851a6d0fc96ed4b8e305f484732f67`
and Windows
`b0cdd3c08d3038fe14794075c57b68d4863d09f45acc82ae32770675adb56de6`.
Their cause was not investigated. Equal source revisions therefore do not imply
identical image-build inputs in this pair.

## Case durations

These are the inclusive case durations emitted by `go test -v` and retained by
the collector. Every case passed. Ratios are Windows divided by Linux, rounded to
two decimal places. Nested subtests are not added to their parents.

| Case | Linux seconds | Windows seconds | Ratio |
| --- | ---: | ---: | ---: |
| preflight | 2.60 | 18.88 | 7.26× |
| lifecycle | 35.65 | 517.67 | 14.52× |
| update | 111.37 | 347.13 | 3.12× |
| agent | 25.66 | 120.97 | 4.71× |
| t3code | 441.97 | 867.23 | 1.96× |
| images | 1,020.75 | 2,149.28 | 2.11× |
| Suite total | 1,638.13 | 4,021.94 | 2.46× |

The case sums are 1,638.00 and 4,021.16 seconds. The suite totals are the suite's
own reported durations, not reconstructed sums; rounding and work outside the
cases leave differences of 0.13 and 0.78 seconds. Package and wall-clock durations
are not substituted for the suite total.

`images` is the largest case on both platforms: 62.3% of Linux's suite time and
53.4% of Windows's. Its absolute difference is 1,128.53 seconds, 47.3% of the total
2,383.81-second difference. The largest ratio is instead `lifecycle`, at 14.52×.
Neither identifies the responsible operation: a case includes multiple CLI,
Podman, build, readiness, and smoke operations. A slow `up` is not evidence of
ownership mapping. The preflight ratio also does not establish preflight as the
dominant contributor to the overall difference.

## Disk and cleanup

Before any build, each host agent recorded the complete engine inventory,
container image references, storage usage, and free space. The conservative
additional-space estimates were 87 GiB for Linux and 53.6 GB for Windows, including
two image generations, intermediate/transient layers, writable sandboxes, and
build/download staging. Both hosts exceeded their estimates; Windows passed the
check on both the host drive and the VM filesystem. These estimates are not
measurements of actual peak usage.

| Measurement | Linux before → after | Windows before → after |
| --- | --- | --- |
| Host storage filesystem free, bytes | 118,723,440,640 → 118,678,310,912 | 81,733,386,240 → 81,685,741,568 |
| VM image filesystem free, bytes | Not applicable | 1,024,976,949,248 → 1,024,960,045,056 |
| Podman image storage, reported size | 516.1 MB → 516.1 MB | 81.31 MB → 81.31 MB |
| Proven run-built images removed | 62 | 68 |
| Pre-existing images retained | 7 | 1 |
| Containers and volumes after cleanup | 0 / 0 | 0 / 0 |

Every removed full image ID was traced to this invocation's build output, absent
from its before inventory, checked for the applicable asset hash, and unreferenced
by any container. Images were removed individually with `podman rmi --no-prune`,
without force or host-wide pruning. All remaining images were present before the
run. No unresolved build IDs or cleanup queries remain. Full ID, tag, size, and
removal accounting stays in the private run evidence rather than these sanitized
records. Logs, checkouts, and evidence were retained; no disk compaction was done.

## Historical evidence and conclusion

The ~6× figure in [story #152](https://github.com/grauzone-labs/sandboxed-agents/issues/152)
remains an unrecorded observation. The preceding retained pair at `dbd5237` was
1,944.72 seconds on Linux and 4,266.48 seconds on Windows, or 2.19×, as recorded in
the [maintainer run history](../../maintainers.md#live-runs-on-record). Historical
revisions, suites, and host state differ. The current 2.46× ratio neither
reproduces the ~6× observation nor establishes a performance change from that
older pair. Two comparable runs are a starting sample, not a proof threshold.

**Attribution is insufficient.** The case totals locate elapsed time at case
granularity, but the differing cache context, engines, toolchains, image asset
hashes, and unknown host load prevent attributing the difference to a specific
operation worth changing. No supplemental operation timings were added, no
optimization or host tuning was performed, and no speedup is promised. This
retains the insufficient-attribution conclusion allowed by
[task #155](https://github.com/grauzone-labs/sandboxed-agents/issues/155).
