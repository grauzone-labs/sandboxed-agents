# OpenCode proxy workflow: first use inside a sandbox

Supplementary record for the open acceptance criterion of [issue #146](https://github.com/grauzone-labs/sandboxed-agents/issues/146), the eighth of its ten: "any network fetch the provider package needs on first use inside the sandbox is verified and documented". It is not a release manual-check record and no gate reads it. It records the parts of checks 1 and 2 of the [OpenCode proxy workflow item](../../manual-checklist.md#opencode-proxy-workflow) of the manual checklist that were run, and the full live suites run at the same commit. Command output, inventories, and logs stay with the operator's private evidence and are not in the repository.

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

This observation shows that the first use completes inside a sandbox with the sandbox's defaults. It does not verify whether a package fetch happened, or the identity or the destination of one, so the acceptance criterion stays open.

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
