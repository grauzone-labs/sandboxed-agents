---
status: accepted
---

# Command-first CLI grammar

Commands come first and the sandbox name is the first argument after the command path: `sandboxed-agents up agent01`, `sandboxed-agents agents login agent01 codex`. The parser is an ordinary subcommand tree, and sandbox names need no reserved words.

## Considered options

- **Name first** (`sandboxed-agents agent01 up`). Rejected: every command word would have to be reserved as a sandbox name, `update` would have two shapes because `update --all` names no sandbox, and the parser would need special handling to detect and explain misplaced arguments.
- **Name as an option with a default** from an environment variable or the current directory. Rejected because it hides which sandbox a command acts on, which is risky for `remove` and `update`.

## Consequences

- No sandbox name is reserved, which ADR-0005 relies on.
- Commands that act on every sandbox take `--all` in place of the name.
