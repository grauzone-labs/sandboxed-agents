# OpenCode proxy workflow: first use inside a sandbox

Supplementary record for the eighth of the ten acceptance criteria of [issue #146](https://github.com/grauzone-labs/sandboxed-agents/issues/146): "any network fetch the provider package needs on first use inside the sandbox is verified and documented". It is not a release manual-check record and no gate reads it. It records the parts of checks 1 and 2 of the [OpenCode proxy workflow item](../../manual-checklist.md#opencode-proxy-workflow) of the manual checklist that were run, the full live suites run at the same commit, and, in [What the provider package needs](#what-the-provider-package-needs-on-first-use), the reading of OpenCode's source and published build that answers the criterion for OpenCode 1.19.5. Command output, inventories, and logs stay with the operator's private evidence and are not in the repository.

## Scope

| | |
| --- | --- |
| Commit | `dbd5237a694064e7f0c64f64263ad64cac387610` |
| Date | 2026-11-06 (UTC) |
| Platforms | Linux and Windows 11, one host each, a dedicated controller group each |
| Sandbox | fresh, `--with none`, `opencode` enabled, OpenCode 1.19.5, no pin |
| Server | a CLIProxyAPI server the operator runs, reachable from the sandbox, with the model `gpt-6-luna` configured |
| Settings | the sandbox's defaults: no `OPENCODE_DISABLE_*` variable, no fake upstream, no synthetic `HOME`, model-list fetch, updater, and default plugins left enabled |

The operator ran `agents login NAME opencode proxy --url URL --model gpt-6-luna` and typed the key at the native `Key:` prompt; the operator reported that the command succeeded, and its output was not captured. No provider login was made before it, so check 4 of the item, which restores one, had nothing to restore and was not run. Check 3, the rejected key, was not run either. On both platforms an in-sandbox verifier then read `opencode.json` and reported booleans only: a `cliproxyapi` provider present, `model` equal to `cliproxyapi/gpt-6-luna`, the base URL equal to the URL as entered with `/v1` appended, and a nonempty key. The two verifiers were not the same: on Windows the verifier also reported, as booleans, a regular file, mode `600`, owner and group ID 1000, and the expected adapter `@ai-sdk/openai-compatible`; on Linux the mode `600`, the owner `agent`, and the size came from file metadata instead, and the adapter was not checked.

No secret value was captured by any step: the verifiers emitted booleans only, no configuration content was read into any record, and the captured run output was redacted before it was kept and held no credential.

## Result

The first `agents run NAME opencode run 'Reply OK' < /dev/null` after `proxy` replied `OK` under the `build · gpt-6-luna` banner and exited zero on both platforms. Its output held no install, download, or package message. The configuration file kept mode `600` and owner `agent` through the run and grew by 48 bytes; its content after the run was not read on either platform. On Windows the engine became unavailable after the run, so the boolean verifier could not run a second time there; the mode and owner had been read before that.

The configuration as verified before the run and the successful reply support that the request went through the configured provider with the configured model. No server-side log, request capture, or other attestation of which server or model served it was available, so this record claims no more than that.

What the run added outside `~/.config/opencode`, by metadata only (no file content was read):

| Path under `/home/agent` | Linux | Windows |
| --- | --- | --- |
| `.cache/opencode/models.json` | added, about 5.9 MB | added, about 5.9 MB |
| `.local/share/opencode/opencode.db` | added | added |
| `.local/share/opencode/log/opencode.log` | added | added |
| `.local/state/opencode/locks/*/meta.json` and `heartbeat` | added | added |
| `.npm/_cacache/` | six content objects and six index entries added | one temporary file added, about 1.2 MB |

Inside `~/.config/opencode`, both runs added a `.gitignore` and changed `opencode.json`. No preexisting file outside that directory changed. The two platforms were observed independently and their counts are not comparable: Linux had 17 new files outside the configuration directory and Windows 6.

## Not verified

- Whether the run opened a connection to `registry.npmjs.org` or any other registry, and what the `npm` cache entries hold. No proxy, firewall, or server log was available, and no capture was installed. A cache file's name identifies no package, so no package identity, download, or fetch is inferred from the files above.
- The request as sent on the wire: its path and headers, the server and model that served it, and the absence of traffic to any other provider. The `/v1` suffix comes from the implementation and the verified configuration, not from a captured request.
- The content of `opencode.json` after the run on both platforms, and on Windows the booleans the verifier reports after the run.
- The output of the `proxy` command itself, which the operator reported but did not capture.
- The rejected-key check and the `--remove` restoration check, which were not run.
- Any OpenCode release other than 1.19.5.

This observation shows that the first use completes inside a sandbox with the sandbox's defaults. By itself it does not verify whether any fetch happened, or the identity or the destination of one. The criterion asks a narrower question, which the next section answers from OpenCode's source and published build: whether the provider package needs a fetch.

## What the provider package needs on first use

**The question.** The criterion asks whether the provider package, the `npm` package the workflow names in the `cliproxyapi` provider, needs a network fetch on its first use inside the sandbox, and that the answer be verified and documented. It does not ask for an account of every connection the run opens. An earlier reading of this record treated the criterion as unanswerable until the identity and destination of every file the run left in the `npm` cache were known; that reading was broader than the criterion and is withdrawn. The observation above keeps its limits as recorded, and the answer below rests on the sources it names, not on the observation alone. The analysis was done offline on 2026-11-07.

**The answer, for OpenCode 1.19.5.** The provider package needs no fetch. The adapter is a literal import bundled into the `opencode` executable at build time, and OpenCode loads it from there. The reading is of the source at the tag `v1.19.5`, which resolves to the commit `810c0eba1a7348036f8bdf61e6632d0c36c6876c`; every link below is a permalink to that commit.

| Source | What it shows |
| --- | --- |
| [`provider.ts` line 158](https://github.com/anomalyco/opencode/blob/810c0eba1a7348036f8bdf61e6632d0c36c6876c/packages/opencode/src/provider/provider.ts#L158) | The table of bundled providers has an entry for `@ai-sdk/openai-compatible`: a literal `import()` of the adapter, resolved and bundled at build time, that yields `createOpenAICompatible`. The adapter is part of the build, not a package resolved at run time. |
| [`provider.ts` lines 1547-1555](https://github.com/anomalyco/opencode/blob/810c0eba1a7348036f8bdf61e6632d0c36c6876c/packages/opencode/src/provider/provider.ts#L1547-L1555) | The adapter name for a configured model comes from the model's own `provider.npm` override first, then from the provider's `npm` entry, and only after that from the remaining sources ending in the catalog default. The workflow writes `@ai-sdk/openai-compatible` as the `cliproxyapi` provider's `npm` entry and no model-level override, and the repository's workflow test asserts that exact value, so the configured provider `npm` takes precedence over the catalog defaults here. |
| [`provider.ts` lines 1856-1874](https://github.com/anomalyco/opencode/blob/810c0eba1a7348036f8bdf61e6632d0c36c6876c/packages/opencode/src/provider/provider.ts#L1856-L1874) | The loader looks the adapter name up in the bundled table first; when an entry exists it builds the SDK from the bundled import and returns. Only a name with no entry reaches the install fallback, which fetches the package through OpenCode's npm service. There is no second fallback: a failure anywhere in the loader is raised as a provider initialization error ([line 1889](https://github.com/anomalyco/opencode/blob/810c0eba1a7348036f8bdf61e6632d0c36c6876c/packages/opencode/src/provider/provider.ts#L1889)). |
| [`llm.ts` lines 94-101](https://github.com/anomalyco/opencode/blob/810c0eba1a7348036f8bdf61e6632d0c36c6876c/packages/opencode/src/session/llm.ts#L94-L101) | A chat request obtains its language model through `getLanguage`, which calls the loader above ([`provider.ts` line 1930](https://github.com/anomalyco/opencode/blob/810c0eba1a7348036f8bdf61e6632d0c36c6876c/packages/opencode/src/provider/provider.ts#L1930)); the `run` command's request takes this path. |
| [`npm.ts` lines 88-144](https://github.com/anomalyco/opencode/blob/810c0eba1a7348036f8bdf61e6632d0c36c6876c/packages/core/src/npm.ts#L88-L144) | The install fallback the bundled path skips: it would resolve the package with `@npmcli/arborist` into OpenCode's own package cache. It is not reached for a bundled adapter. |
| [`package.json` line 71](https://github.com/anomalyco/opencode/blob/810c0eba1a7348036f8bdf61e6632d0c36c6876c/packages/opencode/package.json#L71) | The release pins `@ai-sdk/openai-compatible` at `2.0.41`; the lock file records the same version with its integrity for the `opencode` package. The only patch the repository applies to the adapter changes the error object of a streamed error chunk, nothing about loading. |
| [`build.ts` lines 164-200](https://github.com/anomalyco/opencode/blob/810c0eba1a7348036f8bdf61e6632d0c36c6876c/packages/opencode/script/build.ts#L164-L200) | The release build compiles the sources into one executable with Bun, with `node-gyp` as the only external module, so the literal import above is resolved at build time and lands inside the executable. |

**The published build.** The reading was checked against a locally inspected copy of the published Linux archive, by a static inspection on a workstation on 2026-11-07:

| | |
| --- | --- |
| Package | `opencode-linux-x64` 1.19.5, the official archive from the npm registry |
| Archive integrity | `sha512-i1qh7VooLGML9xX6ud93y0JA8j/1aPeDYRKjsmYXrJX1TjIWHgBBMkEbEJPZthpWbsJg//XfBgR6Kb7+NPaJiQ==`, equal to the registry's metadata and to the lock file of a local npm installation of OpenCode 1.19.5 outside the sandbox, whose executable is byte for byte the archive's |
| Executable | SHA-256 `7df4e4136301f16589b0f30bf91a61380fca5f8ef22dd36570f5ebe1d4ec802c`, 214,386,864 bytes |
| Adapter inside it | The executable's module table maps the string `@ai-sdk/openai-compatible` to an embedded chunk whose `createOpenAICompatible` factory carries the version `2.0.41` |
| Loader inside it | The compiled loader in the executable builds the SDK from the bundled entry and returns before the install fallback, as the source does |
| Not done | No hash of the executable on either live host was taken, so the host executables are not compared to this archive; on both hosts `agents status` reported the version 1.19.5 only. The inspection was static and ran no OpenCode. |

**Corroboration.** The repository's contract test ran OpenCode 1.19.5 at commit `dbd5237a694064e7f0c64f64263ad64cac387610` outside a sandbox from a fresh home, configuration, and cache, with the model-list fetch, the updater, and the default plugins disabled. It saw two authenticated chat requests at the fake upstream and refused one connection to the registry, and the chat completed. That supports the reading, since a refused registry did not stop the adapter from loading, and it is no more than support: the test did not identify what the refused connection was for, and its settings are not the sandbox's defaults.

**What this does not show.** It is no attestation that the run opens no connection other than the chat request. OpenCode has network paths that are independent of the adapter and that the sandbox leaves enabled: the model-list fetch, the registry query for the version behind the `opencode-ai` tag that finds the latest release ([`installation/index.ts` lines 222-228](https://github.com/anomalyco/opencode/blob/810c0eba1a7348036f8bdf61e6632d0c36c6876c/packages/opencode/src/installation/index.ts#L222-L228)), a background npm install of `@opencode-ai/plugin` at OpenCode's own version while the configuration loads ([`config.ts` lines 452-470](https://github.com/anomalyco/opencode/blob/810c0eba1a7348036f8bdf61e6632d0c36c6876c/packages/opencode/src/config/config.ts#L452-L470)), and plugin loading. What the files the runs above left in the `npm` cache hold, and what wrote them, is unknown; none of these paths is identified as the cause, and the first bullet under [Not verified](#not-verified) stands. The in-sandbox runs had no loader trace, so the bundled path is read from the source and the artifact, not observed on a host. On Linux the verifier did not read the adapter name from the configuration; the Windows verifier did, and the workflow test asserts the value the workflow writes on every platform. The answer holds for 1.19.5 only: a later release may require a package fetch on first use, so recheck its source and build. Check 2 of the manual checklist item stays at every live checkpoint for that reason.

## Full live suites at the same commit

Before the observation, `TestLiveSuite` with image coverage ran at the same commit on each host, in its own controller group, with all six checks (`preflight`, `lifecycle`, `update`, `agent`, `t3code`, `images`) `pass` and the `t3code` supplemental record `pass` with its thirteen checkpoints. The suites signed in nowhere and ran no proxy workflow. The records the suites wrote are copied beside this page:

| Platform | Suite | Package | Validation record | Supplemental record |
| --- | --- | --- | --- | --- |
| Linux | `PASS (1944.72s)` | `ok 1946.046s` | [`live-suite-linux.json`](live-suite-linux.json) | [`t3code-linux.json`](t3code-linux.json) |
| Windows 11 | `PASS (4266.48s)` | `ok 4277.454s` | [`live-suite-windows-11.json`](live-suite-windows-11.json) | [`t3code-windows-11.json`](t3code-windows-11.json) |

## Cleanup

- **Full suites, complete.** Each suite removed its own sandboxes and volumes. The images the suite's build produced that the operator's inventory recorded as eligible, absent before the run and referenced by no container, were then removed one at a time by full ID without pruning or forcing, and every image that existed before the run was kept: 68 removed and 1 kept on Windows, 62 removed and 7 kept on Linux. The checkouts, logs, and evidence were retained.
- **Linux observation, complete.** `remove NAME --volumes` removed the sandbox and its three volumes; the seven images the sandbox build produced were recorded as eligible and removed the same way, and the seven preexisting images were kept. The image set after cleanup equals the set before the invocation, and no container or volume remained. The evidence, the temporary checkout, and the controller's lock state were retained.
- **Windows observation, complete after authorized recovery.** After the run the engine's socket refused connections and the machine reported not running; the cause is unknown. One `remove NAME --volumes` attempt failed because a running machine is required, and no image removal was attempted. The initial cleanup attempt ran no start, restart, repair, or prune, as none was authorized at the time. The host owner then authorized starting the existing machine if stopped and finishing the removal. The machine stopped again after the first start; a remove attempt after the first start failed the same way, so a second start was needed; no other change was made. With the machine running, `remove NAME --volumes` against the exact target exited zero, and fresh snapshots verified the sandbox and its three volumes absent. The four images the sandbox build produced were recorded as eligible and removed one by one, without prune or force, after a fresh reference check before each; the other 69 images are the same exact set before and after, and no cleanup item is unverified. A later inspect reported the machine stopped although no stop was issued; that cause is unverified, and no cleanup is outstanding. Report completed: 2026-11-06 23:51:37 UTC, at `dbd5237`.
