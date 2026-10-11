## Agent skills

### Model routing

At the start of every session, before starting or delegating any task, load and apply the `model-routing` skill when available. Follow its model selection, harness fallback, and session-reuse rules.

### Issue tracker

GitHub Issues for `grauzone-labs/sandboxed-agents` and linked organization Project #1 are the tracker. Before creating or reading tickets, publishing specifications, changing hierarchy, triaging, wayfinding, or completing work, use the operations and mappings in `docs/agents/issue-tracker.md`.

### Triage labels

Use the default seven-label vocabulary for GitHub Issues in this repo. Before applying triage roles, use `docs/agents/triage-labels.md`.

### Domain docs

This is a single-context repo. Before exploring domain behavior or naming domain concepts, use `docs/agents/domain.md`.

## Commit messages

Always use Conventional Commits: `type(scope): description`, with scope optional. When a commit includes multiple changes, describe each change as a bullet in the commit body, separated from the subject by a blank line.

Omit `Co-authored-by:` trailers and other co-author attribution from commit messages.

## Tests

Every Go test function (`func Test...`) has a doc comment directly above it, starting with the function name, that states the behaviour the test proves and the seam it exercises. Docstring coverage is complete when every test function in the repository has one.

## Pull request reviews

After pushing fixes for CodeRabbit findings on a pull request, comment on that pull request tagging `@coderabbitai`: name each finding fixed, give the fix commit's short SHA, and ask it to re-review the latest commit. Post the comment only once the fix is pushed and the pull request head matches it.
