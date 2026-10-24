## Agent skills

### Model routing

At the start of every session, before starting or delegating any task, load and apply the `model-routing` skill when available. Follow its model selection, harness fallback, and session-reuse rules.

### Issue tracker

GitHub Issues for `grauzone-labs/sandboxed-agents` and linked organization Project #1 are the tracker. Before creating or reading tickets, publishing specifications, changing hierarchy, triaging, wayfinding, or completing work, use the operations and mappings in `docs/agents/issue-tracker.md`.

### Triage labels

Use the default seven-label vocabulary for GitHub Issues in this repo. Before applying triage roles, use `docs/agents/triage-labels.md`.

### Live validation

Before validating a change on a live Windows or Linux host, whether the user asks for live validation or the work needs live proof, use `.agents/skills/live-validation/SKILL.md`; it opens with the project's Yes/No start checkpoint. Its "Image cleanup" section holds the rules for removing the images a live run leaves.

### Domain docs

This is a single-context repo. Before exploring domain behavior or naming domain concepts, use `docs/agents/domain.md`.

## Commit messages

Always use Conventional Commits: `type(scope): description`, with scope optional. When a commit includes multiple changes, describe each change as a bullet in the commit body, separated from the subject by a blank line.

Omit `Co-authored-by:` trailers and other co-author attribution from commit messages.

## Go documentation

Across every Go package in this repository, including platform-specific source, test helpers, fixtures, and entrypoints, each named function and method, exported or unexported, has a doc comment directly above it that describes its behaviour. Each exported package-level type, constant, and variable has one too; a group of related declarations may share a single comment.

Every test function (`func Test...`) has a comment that starts with the function name and states the behaviour the test proves and the seam it exercises. Documentation is complete when every Go file in the repository meets these rules.

## Pull request reviews

Codex reviews every pull request: its summary comment lists a Code Review and a Security Review per head commit. A pull request merges only after both reviews have completed on its current head with no open suggestion. After pushing fixes for Codex findings, comment on that pull request with `@codex review`, and also `@codex security review` when a security finding was fixed: name each finding fixed and give the fix commit's short SHA. Post the comment only once the fix is pushed and the pull request head matches it.
