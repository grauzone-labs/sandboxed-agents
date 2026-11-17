# Issue 165: OpenCode `@opencode/cli` offline compatibility record

Supplementary record for [issue #165](https://github.com/grauzone-labs/sandboxed-agents/issues/165), which moves the `opencode` agent from the npm package `opencode-ai` to `@opencode/cli`. It is not a release manual-check record and no gate reads it. It holds what was found offline on 2026-11-17 UTC: which package and version were examined, what the repository's native contract test did against them, what a supplementary check showed about credentials, and what was read from OpenCode's source. It makes no claim about a live host, a Windows host, a real sandbox, or the manager's migration code; those belong to the live validation of the issue. Command output, caches, and databases stay with the operator's private evidence and are not in the repository.

## What was examined

| Item | Value |
| --- | --- |
| Package | `@opencode/cli` 2.3.0, npm `latest` on 2026-11-17, published 2026-11-13T08:54:17Z; its `bin` maps `opencode` and `opencode2` to `bin/opencode.exe`, and its `postinstall` script puts the native executable in place |
| Native package | `@opencode/cli-linux-x64` 2.3.0, tarball SHA-256 `408f854b5fac7857a3b4df4e783d3a8b70f5cc8c7c3187aac09c5be0576c14ae` |
| Executable | `package/bin/opencode` in that tarball, 250,685,616 bytes, SHA-256 `2e0e4656521ca56e5ba16a00d48429958b8105b3fa6466710a4350e68759381f`, prints `opencode v2.3.0` |
| Source | `anomalyco/opencode` tag `v2.3.0`, commit `a6b15d33daf6ab7d35569f6d711fdb630b9f04e7` |
| Contract test | `TestOpenCodeProxyNativeClientReachesOnlyFakeUpstream` in `internal/manager/opencode_upstream_test.go`, source SHA-256 `e2ab55a3707d19affab50d6c901a37ec1c0cb4b97f62d7540c5e54a13a791620`, repository HEAD `6feacbab35f41c14c26397cb94d15715fabd0448` with other work in progress in the tree; Go 1.27.2 |
| Platform | Linux x64 only |

The npm packument of `@opencode/cli` lists 2.3.0 and does not list 1.19.5 or 1.19.6, versions of `opencode-ai`. That is why `agents update` and `agents enable` check a pin against the published versions of `@opencode/cli` before installing ([agents](../../agents.md#move-opencode-to-its-new-package)).

The tarball integrity values equal the registry's SHA-512 values. The npm provenance of both packages names the workflow `.github/workflows/release.yml` of `anomalyco/opencode` and the dispatch commit `73abe63ac23224d88e739e7028882e547ef65a79`, which is one commit before the release tag: the workflow commits the version and changelog, tags that commit, and builds from the tag. GitHub's comparison of the two commits shows only version, changelog, and lock-file changes, and the runtime source files and build script read for this record are byte for byte the same at both. The attestation payload digests match the downloaded archives. The Sigstore signatures were not verified and OpenCode was not rebuilt.

## Reproduce

Work in a fresh, empty directory, keep scripts outside it, and run the test from the repository root.

```sh
work="$(mktemp -d)"
npm pack @opencode/cli-linux-x64@2.3.0 --pack-destination "$work"
sha256sum "$work"/*.tgz
mkdir "$work/cli" && tar -xzf "$work"/*.tgz -C "$work/cli"
sha256sum "$work/cli/package/bin/opencode"
```

Compare both checksums with the table. Then, from the repository root with the Go version of `go.mod`:

```sh
SANDBOXED_AGENTS_TEST_OPENCODE="$work/cli/package/bin/opencode" \
  go test ./internal/manager -run '^TestOpenCodeProxyNativeClientReachesOnlyFakeUpstream$' -count=1 -v
```

The recorded run used an empty environment with only `PATH`, an isolated `HOME`, `TMPDIR`, `GOCACHE`, and `GOMODCACHE`, `GOPROXY=https://proxy.golang.org,direct`, and this variable. It passed in 0.93 s and logged `OpenCode native contract: 2 authenticated chat requests; 0 denied registry checks; no provider-host requests`. That log line is fixed text plus two counters. The test itself fails on a request to any other address, a wrong bearer key or model, no request, or no `OK` in the output.

The recorded repeat that restricts the network ran the same test inside `unshare --user --map-root-user --net`, with loopback raised by an `ioctl` from a temporary Python script outside the repository and checked to be the only interface, after the Go module cache has been filled by a first run, with `GOPROXY=off` and `GOSUMDB=off`. It passed in 0.99 s with the same log line. A tool such as `ip link set lo up` would also raise loopback; it was not run for this record. The test does not set up that namespace; it is the repeat's own restriction, and without it only traffic that follows the test's proxy settings is observed.

Credential preservation was checked by a temporary script outside the repository, in the same loopback-only namespace, with an isolated home, and the three OpenCode flags of the test; the import step ran behind a proxy that refused every request:

1. `~/.config/opencode/opencode.json` held the configuration the proxy workflow writes: provider `cliproxyapi` with `npm` `@ai-sdk/openai-compatible`, `options.baseURL` `http://upstream.invalid/prefix/v1`, a dummy `apiKey`, the `_sandboxedAgentsProxy` entry, one model `chosen`, and top-level `model` `cliproxyapi/chosen`.
2. `~/.local/share/opencode/auth.json` held a dummy OpenAI API-key record and a dummy Anthropic OAuth record in OpenCode 1.x's format.
3. `opencode auth list --format json` exited 0 and listed both integrations. A read-only SQLite query of OpenCode's new database showed the key, the OAuth access value, and the OAuth refresh value imported into its credential table. The SHA-256 of both original files was the same before and after.
4. `opencode run` against a fake upstream then exited 0 with `OK`. Two POSTs reached `http://upstream.invalid/prefix/v1/chat/completions` with the dummy proxy key and the model `chosen`, nothing else was requested, neither body held the ownership entry, and neither request carried an unrelated dummy credential. Both files were unchanged.

**Clean up after every OpenCode run.** OpenCode 2.3.0 can leave a detached `opencode serve --service` process, with parent process 1, after the CLI that started it has exited. The credential run above left one, found by its executable path, working directory, and environment, and stopped with SIGTERM after its identity was checked; no other OpenCode process of the work remained. It kept the loopback-only namespace, so the network restriction and the recorded assertions stand. A finished CLI does not show that no process remains: the contract test waits for the CLI only and does not look for such a service. After a reproduction, list processes that run the extracted executable, stop only those you started, and then remove `$work`.

## Reading of the source

The first-use statement of [Agents](../../agents.md#opencode-proxy-workflow) rests on this chain at the release commit, read from the source and checked against the executable. The links are permalinks to `a6b15d33daf6ab7d35569f6d711fdb630b9f04e7`.

1. [`config/normalize.ts` lines 168-175 and 200-204](https://github.com/anomalyco/opencode/blob/a6b15d33daf6ab7d35569f6d711fdb630b9f04e7/packages/core/src/config/normalize.ts#L168) normalizes a legacy `provider` entry beside native providers and decodes the top-level `model`; the original file is not rewritten.
2. [`v1/config/provider.ts`](https://github.com/anomalyco/opencode/blob/a6b15d33daf6ab7d35569f6d711fdb630b9f04e7/packages/core/src/v1/config/provider.ts#L82) accepts `npm`, `models`, and `options` with extra keys, and [`v1/config/migrate.ts` lines 119-139 and 176-180](https://github.com/anomalyco/opencode/blob/a6b15d33daf6ab7d35569f6d711fdb630b9f04e7/packages/core/src/v1/config/migrate.ts#L119) turns `npm` into `package` and `options` into settings, keeping arbitrary keys, the ownership entry included.
3. [`aisdk-native.ts` lines 82-109, 127, and 205-218](https://github.com/anomalyco/opencode/blob/a6b15d33daf6ab7d35569f6d711fdb630b9f04e7/packages/core/src/aisdk-native.ts#L82) rewrites the adapter name `@ai-sdk/openai-compatible` to `@opencode/ai/providers/openai-compatible` and keeps `baseURL` and `apiKey`; its host-specific mappings do not apply to a custom `cliproxyapi` provider.
4. [`provider.ts` lines 132 and 198-226](https://github.com/anomalyco/opencode/blob/a6b15d33daf6ab7d35569f6d711fdb630b9f04e7/packages/core/src/provider.ts#L198) declares the native provider as a literal import and returns that built-in entry before the `npm.add` fallback; [lines 411-416 and 440-447](https://github.com/anomalyco/opencode/blob/a6b15d33daf6ab7d35569f6d711fdb630b9f04e7/packages/core/src/provider.ts#L411) apply the rewrite.
5. The [native provider](https://github.com/anomalyco/opencode/blob/a6b15d33daf6ab7d35569f6d711fdb630b9f04e7/packages/ai/src/providers/openai-compatible.ts#L39) uses the configured endpoint with bearer authentication, and its [protocol](https://github.com/anomalyco/opencode/blob/a6b15d33daf6ab7d35569f6d711fdb630b9f04e7/packages/ai/src/protocols/openai-compatible-chat.ts) selects `/chat/completions`.
6. [`cli/script/build.ts` lines 173-200](https://github.com/anomalyco/opencode/blob/a6b15d33daf6ab7d35569f6d711fdb630b9f04e7/packages/cli/script/build.ts#L173) compiles with Bun, `node-gyp` the only external. The executable holds the mapping `"@ai-sdk/openai-compatible":"@opencode/ai/providers/openai-compatible"` and the built-in entry for `@opencode/ai/providers/openai-compatible`.
7. The [legacy credential migration lines 33-108](https://github.com/anomalyco/opencode/blob/a6b15d33daf6ab7d35569f6d711fdb630b9f04e7/packages/core/src/database/migration/20260805200742_import_legacy_credentials.ts#L33) reads the old `auth.json` once into the credential table, skips malformed records and integrations that already hold a credential, and leaves the file alone. It is a database migration, not a continued synchronization.
8. The [login handler](https://github.com/anomalyco/opencode/blob/a6b15d33daf6ab7d35569f6d711fdb630b9f04e7/packages/cli/src/commands/handlers/auth/login.ts#L32) still implements `opencode auth login`.

## What this does not show

- It is Linux x64 only: not Windows, not a live host, not a real sandbox, not a real npm prefix migrated from `opencode-ai`, and not the manager's migration or pin handling, which the repository's own tests and the live validation cover.
- The credentials were dummies imported into a fresh OpenCode database. No real login, no credential's validity, and no database or session written by OpenCode 1.x was checked, and the import is not shown for every historical configuration.
- The configuration normalizer covers the fields the workflow writes. A user's own configuration may hold v1 fields it does not support or native v2 providers that interact with a legacy entry; no general equivalence of the two formats is claimed.
- The first-use statement covers the provider entry the workflow writes, without a model-level override, on 2.3.0. It says nothing of the model-list fetch, the updater, plugins, or any other provider package.
- The contract sets `OPENCODE_DISABLE_MODELS_FETCH`, `OPENCODE_DISABLE_AUTOUPDATE`, and `OPENCODE_DISABLE_DEFAULT_PLUGINS`. The first two names occur in the 2.3.0 executable and the third does not, so the test does not show that default plugins are disabled, and none of the three is shown to suppress all network activity of 2.3.0. The bounded evidence is the requests the test and the loopback-only repeat observed.
- The earlier [OpenCode 1.19.5 first-use record](../issue-146/first-use.md) is history for the previous package and is unchanged.
