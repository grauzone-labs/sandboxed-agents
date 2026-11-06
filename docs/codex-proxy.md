# Codex proxy notes

Maintainer notes for the `proxy` workflow of `codex` ([Agents](agents.md#codex-proxy-workflow), [#145](https://github.com/grauzone-labs/sandboxed-agents/issues/145)). The user-facing behavior lives on the Agents page; this page records the decisions behind it, the evidence each one rests on, and what the offline test does and does not show, so that a later Codex or CLIProxyAPI release can be checked against the same points.

## Key mechanism

The workflow stores the key through Codex's own API-key login, `codex login --with-api-key` with the key on standard input ([Codex authentication](https://learn.chatgpt.com/docs/auth)), and selects the stored login for the provider with `requires_openai_auth = true` in `[model_providers.cliproxyapi]`. The key lands in `auth.json`, the file Codex documents for the `file` credential store, and nowhere else.

The alternatives the story ruled out, and why:

- **An environment variable** (`env_key` on the provider, or `OPENAI_API_KEY` in the environment) would have to be present in every process that starts Codex. `agents run`, `agents session`, `shell NAME`, and an editor over SSH each start Codex differently, and none of them injects environment, so the key would be missing on some path. Codex also ignores `env_key` once `requires_openai_auth` is set.
- **The key in `config.toml`** has no documented field in API-key mode. The only place the guide puts a key into `config.toml` is the `X-Api-Key` header of its ChatGPT login mode, which is the mode below that the workflow does not use. A secret in `config.toml` would also sit beside settings the user edits and shares.
- **The operating-system credential store** (`cli_auth_credentials_store = "keyring"` or `auto`) is not available in a sandbox, which runs no secret service. The workflow passes `-c cli_auth_credentials_store="file"` to the staged login so that the login lands in a file, and refuses a `config.toml` that sets the store to anything other than `file`, because Codex would then look for the login where the workflow did not put it. An unset store is accepted: Codex's default falls back to the file when no store is available.

The staged login runs with `CODEX_HOME` pointed at a temporary directory under the home data. That keeps a failed or unexpected login away from the active `auth.json` and lets the workflow inspect the result before publishing it: it accepts only a file whose `OPENAI_API_KEY` is exactly the key and that holds no `tokens`.

## Which CLIProxyAPI mode

The [CLIProxyAPI Codex guide](https://help.router-for.me/agent-client/codex) describes two client setups. The workflow follows the **API mode**: `model_provider = "cliproxyapi"`, a provider table with `base_url`, `wire_api = "responses"`, and `requires_openai_auth = true`, and the key in `auth.json`.

The guide's **OAuth mode**, which it recommends for interactive use, is excluded on purpose. It needs a ChatGPT login to exist, because Codex sends that login's token, and it carries the CLIProxyAPI key as an `X-Api-Key` header in `config.toml`. The story requires that no ChatGPT login is touched silently and that the key is not written into `config.toml` without a documented field, and the mode fails both. It also ties the proxy to an upstream account the sandbox has no business holding.

## The actor header

The guide's API-mode snippet includes `http_headers = { "X-OpenAI-Actor-Authorization" = "local-proxy" }`. The workflow does not write it. The story allowed the header only if CLIProxyAPI's API-key mode is shown to need it, and the evidence says it is a client-side signal, not a server requirement:

- Codex treats `X-OpenAI-Actor-Authorization` as a feature-eligibility signal: with the header present, or with `requires_openai_auth = true`, Codex offers tools such as image generation for the provider. Projects that added the header to a CLIProxyAPI setup did so to get image generation back, not to authenticate ([CLIProxyAPI fork PR #29](https://github.com/Automancer-Ltd/CLIProxyAPI/pull/29), [Paseo v0.9.905 release notes](https://github.com/iamriajul/paseo/releases/tag/v0.9.905)). The header's value is arbitrary, and the [codex-proxy-rs](https://github.com/zyycn/codex-proxy-rs/issues/373) configuration comment says it is not a secret.
- The workflow already sets `requires_openai_auth = true`, which covers the same eligibility gate, so the header would add nothing.
- The native contract test below passed without it: CLIProxyAPI-shaped requests reach `/v1/responses` with the bearer key, and the server authenticates by that key.

Revisit this if a CLIProxyAPI release starts to require the header, which would show up as a rejected request in the manual checks with a key the server accepts.

## The version floor

`codex proxy` refuses Codex older than 0.166.0, on both the host, from the installed version `agents status` reports, and in the manager, from the agent selection. 0.166.0 is the version the native contract test passed against, so it is the oldest version with evidence; older versions are untested, not known to be broken. The flags and settings the workflow depends on, `login --with-api-key`, `cli_auth_credentials_store`, and `requires_openai_auth`, are all documented without a version on the Codex authentication page. Raise the floor only with a reason, and lower it only after the native test has passed against the older version.

## Ownership record

`[_sandboxedAgentsProxy]` in `config.toml` holds `Provider`, a copy of the provider table the workflow wrote; `TokenHash`, the SHA-256 of the key; `ProvidersExisted`, whether `config.toml` already had a `model_providers` table before the first setup; and `PreviousProvider` and `PreviousModel`, the top-level `model_provider` and `model` values the first setup replaced, each omitted when there was none. The key itself appears only in `auth.json`.

The record is what makes `--remove` safe without a second copy of the key: the provider table is compared to `Provider` as a whole, so an edited table is refused rather than deleted; the stored login is deleted only when its `OPENAI_API_KEY` hashes to `TokenHash` and no `tokens` are present; `model_provider` and `model` are restored only while `model_provider` still names `cliproxyapi`; and an emptied `model_providers` table is dropped when `ProvidersExisted` is false, because the workflow created it, and kept when the table was there before. A ChatGPT login that `--replace-login` replaced is not recorded and not restored: Codex keeps one login, and keeping a copy of its tokens would put a second credential into the home data.

## TOML round trip

The workflow decodes `config.toml` into a map and encodes it again. Values, tables, and profiles survive; comments, key order, and formatting do not. The Agents page tells users so. A format-preserving edit would need a different TOML library and was not worth it for a file Codex itself rewrites.

## What the native contract test isolates

`TestCodexProxyNativeClient` in `internal/manager/codex_proxy_test.go` runs only with `SANDBOXED_AGENTS_TEST_CODEX` set to a Codex executable. It has passed against Codex 0.166.0: setup with `--replace-login` over a stored ChatGPT login, a `codex exec` that sent a POST to `/prefix/v1/responses` with `Authorization: Bearer` and the configured model to the fake upstream only, and a `--remove` that restored the original `model`, kept an unrelated profile, and deleted the owned `auth.json`.

The test isolates Codex in two ways that the workflow does not:

- It routes all of Codex's traffic to the fake upstream with `HTTP_PROXY`, `HTTPS_PROXY`, and `ALL_PROXY`, so any request to another host is refused there instead of leaving the machine. That is how the test can assert that no request reached `api.openai.com` or `chatgpt.com`.
- Its own `codex exec` passes `-c analytics.enabled=false`, `-c features.plugins=false`, `-c features.apps=false`, `-c features.api_key_model_discovery=false`, and `-c features.api_key_cyber_access_programs=false`. Without them, a first run of the test saw Codex 0.166.0 attempt startup traffic to GitHub, `chatgpt.com`, and `ab.chatgpt.com`, unrelated to the model request, which the proxy refused and which made the run fail.

The workflow writes none of those settings and enforces no network isolation. In a sandbox, Codex keeps the user's analytics and feature settings, and whatever ancillary traffic those settings cause continues to go where Codex sends it; only the model requests go to the CLIProxyAPI server. The test shows that the model request path is routed and authenticated as documented, nothing more. The [manual checklist](manual-checklist.md#codex-proxy-workflow) holds the live checks against a real server, not yet run.
