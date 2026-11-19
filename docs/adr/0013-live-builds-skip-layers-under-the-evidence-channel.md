---
status: accepted
---

# Live builds skip layers under the evidence channel

The live-validation cleanup (#180) may remove only images that a private, primary record proves a live run's builds produced. Under Podman's default layer policy a build also commits an intermediate image for every instruction, or reuses an existing one as a cache hit, and no interface that the executable can control reports those per-step decisions. The final image of a build is already recorded from the builder's `--iidfile` and confirmed by inspection under the image-tag lock; the intermediates are not, so they stay as unknown images and every run that leaves them ends `partial`. On 2026-11-19 the user chose "Use layer-free live builds (Recommended)": while the live supervisor's private build-evidence channel is active and confirmed for the selected engine, the executable builds without layers, so that a build commits one image, its final, and that image is the whole record of the build.

## Decision

This ADR records two decisions, and only these two.

**Layer-free builds for live evidence.** While the live supervisor's private build-evidence channel is active and confirmed for the selected engine, the executable adds `--layers=false` to every image build it runs, beside `--iidfile`, and keeps the flag when the image ID file is unavailable after the engine was confirmed; the build is then layer-free without a receipt and its evidence is unverified. Builds outside that channel keep Podman's default layer policy: ordinary `build`, `up`, and `update` are unchanged, including their layer cache. Under this policy each build of the project's single-stage recipes commits exactly one image, its final, and the build record names that image's full ID with the step `COMMIT <tag>`, from `--iidfile` confirmed by inspection under the image-tag lock. A build attempt that ends without such a confirmed result stays open, and an image that is absent from the before inventory and that no valid record names makes the cleanup `partial`, whether or not every surviving attempt is closed. How a channel that cannot be confirmed, and every other evidence failure, is recorded and reported is an operator detail of `docs/images.md`; it grants no deletion authority and leaves the decision unchanged.

**Registry base images pulled by `FROM`.** A base image that a `FROM` instruction pulls from a registry during a run is outside that run's build record: it is not counted as an image the run's build steps produced. Cleanup never removes it. It stays under the existing rules, and when it is absent from the before inventory and the collector has revalidated it as a known literal input, the report lists it with its tags, its size, and the reason "not committed by this run's builds". A pulled base without such an input is an unexplained image and keeps the cleanup `partial`. The tags are the sanitized ones: canonical project tags are shown and any other tag is redacted, because the word never authorized raw host detail. A known input does not by itself make the cleanup `partial`; a user's retention, foreign tags and a failed inspection keep their own skipped reasons.

## Considered options

- **Keep the default layer policy and retain unknown intermediates.** This is the behaviour before this decision. It is safe, and it stays the fallback, but it leaves #180 open: every run that leaves intermediates ends `partial`, they persist across runs, and they reduce the free space of later runs.
- **Layer-free builds product-wide.** Not chosen. It would make live and product builds the same, but it changes a product cache policy: after an upgrade, `up` would lose instruction-level cache reuse and rebuild the base with its downloads, and the product's images would carry one layer per stage. That is a larger and separate decision.
- **A repository-owned instrumented builder, or an upstream or patched Podman with a typed per-step record.** Not chosen. Either would give a per-step receipt under the default policy, but they need a new builder backend and its maintenance, or a host software upgrade or a change to the product floors.
- **A trusted logger side channel from the builder's debug output.** Rejected: it needs debug output and host log configuration, and it does not exist on the Windows machine.
- **Deriving intermediates from the parent chain of a proven final.** Rejected: the image cleanup rules forbid it, and the rules are unchanged.
- **A dedicated storage root or engine for live builds.** Not chosen: it is a host policy that was not accepted, and a remote Windows engine cannot select one per request.

## Consequences

- Once the engine is confirmed, builds in a live run read no layer cache and write no per-instruction intermediate images; a build whose engine could not be confirmed keeps the default layers. Their images carry one new layer per stage. Their durations are not like for like with the earlier baselines of the story, so every report names the layer policy beside its seconds.
- A live run started through `live-run` no longer exercises Podman's default layer policy, which ordinary `build`, `up`, and `update` keep. The direct `go test` commands of the release runbook set no evidence channel, so they keep the default layers; the schema 2 record does not say which policy a run used.
- How a registry base is recognised (only from a known literal `FROM` input that the selected engine confirms by inspection, never from the look of a tag; it retains the image and attributes no pull or deletion) and how the timestamp variable is detected per platform are operator details of `docs/images.md`, not further decisions.
- The live check for baked-in SSH host keys can no longer see a file that one instruction creates and a later instruction deletes, because a build now keeps one layer per stage. An offline check of the recipe, that the SSH host keys are removed in the instruction that installs the SSH server, stands in for that case.
- Intermediate images that earlier runs left stay, as the cleanup rules require, until a separately authorized action removes them.
- The optional layer-label telemetry that an earlier change added has no intermediate image to label under this policy, and the implementation removes it; this record fixes no interface. Receipts of the earlier, iid-only form stay readable but cannot authorize deleting a cacheable image or complete accounting.

Unchanged: every live case and assertion, the one no-cache `build` and its pull flags and labels, the schema 2 record, the stable release check, the product floors, host configuration, and the image cleanup rules word for word.

## Status

On 2026-11-19 the user accepted both decisions above, with the caveats listed under Consequences, in answer to a question that stated each of them. The acceptance authorizes no live run, no host, machine, connection, or configuration change, no product-wide cache change, no numeric bound or no-bound decision, and no change to the cleanup eligibility rules. Later the same day the accounting was made stricter within #180: an unexplained new image keeps the cleanup `partial` whether or not every surviving attempt is closed, and a pulled base without a known input counts as unexplained. That edit changed reporting only, left the scope of both decisions as accepted, and was not put to the user again, so the user's acceptance covers the decisions as first stated and not that wording. The decision rests on reading the source of Podman and Buildah at the product floors and in later releases, not on a live run, and the offline implementation exists and was verified against fake engines when this record was updated; runtime proof is pending.

## Open

- Runtime behaviour on real engines, none of it observed live: whether the Windows machine applies the layer policy from a request that omits `layers`, the real format of `--iidfile`, the real count of untagged images per build under each policy, server-side `SOURCE_DATE_EPOCH`, disk use and timing on both hosts, and the reconciled inventory. `docs/images.md` describes the policy only as offline-verified design.
- Default-timestamp uniqueness, that a build without timestamp controls produces an image new to the storage, remains a source-based assumption, and the remote server's own environment stays unobserved.
- An offline recipe guard stands in for the one live assertion that loses sensitivity (a baked-in SSH host key created and deleted across instructions); it covers that pattern only.
