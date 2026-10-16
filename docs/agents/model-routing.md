# Model routing

Category overrides for the `model-routing` skill. A category without a row uses the skill's built-in default.

Model access: provider-native
Subscription burner: disabled

| Category | Model | Effort | Escalation model | Escalation effort |
| --- | --- | --- | --- | --- |

## Execution speed

Use the built-in models, reasoning efforts, and escalation routes.
Prefer fast execution wherever the selected model and account support it.
This preference applies to initial and escalation routes.

Before launching or resuming work, check the current harness catalog.
Keep the model ID unchanged and select its supported fast option.

Confirmed T3 options as of 2026-10-16:

- Codex `gpt-6.1-sol` and `gpt-6-luna`: set
  `target.options.serviceTier` to `priority`.
- Claude `claude-opus-5-5`: set `target.options.fastMode` to `true`.
- Claude `claude-sonnet-5-5` and `claude-haiku-5-5`: retain standard speed.

Use an execution path that can select the required option explicitly.
In other harnesses, resolve the equivalent supported fast control.
Do not invent model IDs with a fast suffix, change models, or lower
reasoning effort to obtain faster execution.

If fast execution is unavailable, retain the selected model at standard
speed and report the fallback. Report requested and effective speed
separately when runtime verification is available; otherwise record
the verification gap.

Sources:
- https://developers.openai.com/api/docs/guides/fast-mode
- https://code.claude.com/docs/en/fast-mode
