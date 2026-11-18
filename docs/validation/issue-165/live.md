# Issue 165: live validation of the OpenCode package change

Supplementary record for [issue #165](https://github.com/grauzone-labs/sandboxed-agents/issues/165), not a release validation record; no gate reads it. It covers live runs of `sandboxed-agents` commit `941caa696a9e8e01e764ffbd2c231d8165beba03` on Linux and Windows 11, with fake upstreams and dummy settings and credentials, never a real provider account or server. The [offline record](offline.md) holds the source and build reading of OpenCode 2.3.0, which is the only version that reading covers. Verbose logs, full image ID ledgers, and host paths stay with the operator.

**Outcome.** The targeted cutover supplement passed on both hosts. The Linux full suite passed. The Windows full suite failed, so there is no paired full-suite pass and no release gate success here.

## Build inputs

Both hosts ran this source commit, but the embedded manager came from different Go toolchains, so the builds are not identical and neither platform's result stands for the other.

| | Linux | Windows 11 |
| --- | --- | --- |
| Go toolchain of the embedded manager | 1.27.2 | 1.27.3 |
| Asset hash | `1210e5695ccccec2b804413a150f91fd54417822f18f98f3763674c44dbbab67` | `123973b0e4405285c5d0ac2b9ee2edd16df6cbc0a41d84cf6ef5f185ef1a9632` |

Both duration records give the context `host_load` shared, `base_image` absent, `build_cache` unknown. No timings are compared across platforms.

## Linux: pass

- **Suite.** `TestLiveSuite` with the image coverage exited 0 in 2005.158 s, Go's suite total; the per-case seconds leave out overhead and need not sum to it. All six checks pass with the image coverage complete ([`live-suite-linux.json`](2026-11-18/live-suite-linux.json), [`durations-linux.json`](2026-11-18/durations-linux.json), [`t3code-linux.json`](2026-11-18/t3code-linux.json)).
- **Cutover supplement** ([`opencode-cutover-linux.json`](2026-11-18/opencode-cutover-linux.json), `@opencode/cli` 2.3.0). An installation of the published `opencode-ai` 1.19.6 package, with dummy settings and credentials, moved to `@opencode/cli` by an unpinned `agents update` and by a forced `agents enable` that also repaired an interrupted link, each with the configuration and `auth.json` preserved. A recorded pin to 1.19.6 (unpublished) and a requested unavailable version exited 1 before any install and changed nothing. `update --unpin` then installed 2.3.0. A first install of an unavailable version created nothing and named `enable --version` only. A proxy to a fake loopback server ran non-terminally with exit 0, `OK`, and the expected request, and `--remove` restored the model, the theme, and the existing provider.
- **Cleanup.** Five supplemental sandboxes (15 volumes) and the group state were removed; 0 containers and 0 volumes remain. Free space went from 108 GiB to 102 GiB and images from 18 (967.6 MB) to 62 (10.18 GB). The run removed 62 images of its own build output with `podman rmi --no-prune`, all exit 0. It kept 46 new images it could not prove it created: 32 of a concurrent run, 13 unlabeled of unverified origin, and one failed "unoverlaid" build intermediate (598,804,104 bytes), whose ownership is unverified. [`inventory-reconciliation.json`](2026-11-18/inventory-reconciliation.json) shows the 18 starting IDs, the 62 removed, and the 46 kept are pairwise disjoint, so this run targeted no baseline ID. The final count of 62 does not equal 18 + 46 = 64; no final ID inventory was kept, so the 2-image difference is unexplained.

## Windows 11: suite fail, supplement pass

- **Suite, fail.** [`live-suite-windows-11.json`](2026-11-18/live-suite-windows-11.json) exited 1 (`TestLiveSuite` 2194.48 s; Go package 2204.867 s). `preflight`, `lifecycle`, `update`, `agent`, and `t3code` passed. `images` failed at the Azure smoke (`shell image-azure`, after the initial images had been built), during a Podman/WSL preflight query: a machine inspection exited 125 when a WSL call returned `0xffffffff`. The suite's later no-cache rebuild was not reached, so `image_part_ran` and `image_coverage_complete` stay false as recorded, and the image coverage stays incomplete. `image_part_ran` does not mean that no image was built. The run does not count as passed ([releases](../../releases.md#live-suite)). See also [`durations-windows-11.json`](2026-11-18/durations-windows-11.json) and [`t3code-windows-11.json`](2026-11-18/t3code-windows-11.json).
- **Cutover supplement, pass** ([`opencode-cutover-windows-11.json`](2026-11-18/opencode-cutover-windows-11.json), result `pass`, closure follow-up `pass`). It uses the same executable as the suite and keeps the per-case versions and registry lookups, which moved from 2.3.1 to 2.3.2 during the work. A genuine `opencode-ai` 1.19.6 fixture with dummy settings moved to `@opencode/cli` 2.3.1 by `agents update`, and `update --unpin` recovered with 2.3.1. The proxy to a fake loopback server passed with 2.3.1 after the fake reply was corrected to OpenCode's streaming contract: exit 0, `OK`, the request assertions, and `--remove` preserved the model, theme, and user instruction. A targeted closure in a fresh sandbox, with an in-container npm audit shim and exact before and after state snapshots, found that a first install, a recorded pin, and a requested pin that are unavailable each exit 1 with the right guidance, leave the state unchanged, and make zero install calls. It then repaired a valid mismatched managed link by a forced `enable` to `@opencode/cli` 2.3.2 with an empty pin, a working managed command, both old prefixes removed, and the configuration and `auth.json` byte hashes unchanged; a dangling link was refused. The proxy was not repeated with 2.3.2. Earlier partial attempts stay in the JSON history and are closed by the follow-up.
- **Cleanup.** Five output images of the first run and one closure base image were removed by full ID, each absent before its build, label-checked, unreferenced, with `rmi --no-prune` exit 0. 17 target-labeled intermediates were kept, because their IDs were not traced to build output. The four supplemental sandboxes and their 12 volumes were removed; 0 containers and 0 volumes remain, and the Podman machine was left running. Measured on a shared host, without reconciliation: C: free 81,334,743,168 bytes before and 72,871,833,600 after; the Podman store had 1,066,349,199,360 bytes free before and 1,061,809,729,536 after (19,291,447,296 used of 1,081,101,176,832); the final count is 65 images (10.08 GB).

## Limits

- The source and build reading is for 2.3.0 only, not for 2.3.1 or 2.3.2.
- This is targeted evidence of the feature, not a passing Windows suite, a manual-checklist record, or a preview validation.
- It shows nothing about a real provider login, a real server, or a valid credential.
