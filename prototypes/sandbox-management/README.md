# Sandbox management prototype

A throwaway prototype that asks: does the web Control room work well as a
keyboard-first terminal UI (TUI)? All sandboxes, images and versions are fake.
Data lives only in memory. No Podman, SSH or model provider is needed.

## Prerequisites

- Git and access to the repository
- Go 1.27.2 or newer (see `go.mod`)
- A terminal of at least 60x20 characters (tmux is not required)

## Start

The prototype is a nested Go module. Run it from its own directory, not the
repository root.

```sh
git clone --branch feature/154b6b62-prototype-sandbox-management-tui \
  https://github.com/grauzone-labs/sandboxed-agents.git
cd sandboxed-agents/prototypes/sandbox-management
go run .
```

`go run .` starts the TUI and a web/API service on `127.0.0.1:4317`. Open
<http://127.0.0.1:4317/> in a browser on the same machine to see the same
state. The listener is loopback only, so another machine cannot reach it.

## Try it

- Keys: arrows pick object or tab, `Tab` selects an action, `Enter` runs it,
  `n` opens the new-sandbox wizard, `g` shows the instance, `?` shows help,
  `q` quits. In a session, `Esc` detaches and keeps the history.
- Sharing: press `3` (Configuration), `Tab` to **Disable service** or
  **Enable service**, then `Enter`. State is kept while sharing is disabled.
- `q` stops the TUI that owns the service, and the server with it. A fresh
  start resets the in-memory data.
- If another process already uses the port, a second TUI connects to the
  existing compatible backend. Only the owning TUI can toggle availability.

## Options

```sh
go run . -port 4318                       # use another port
go run . -service=false                   # TUI with sharing initially off
go run . -serve                           # backend + web only; Ctrl+C stops
go run . -connect http://127.0.0.1:4317   # TUI for a running backend
```

`-serve` and `-connect` can run in two terminals.

## Check

```sh
go test ./...
go vet ./...
go run . -check
```

## Optional build

```sh
go build -o sandbox-prototype .
./sandbox-prototype
```

The prototype is not installed as an operating system service. Stop it with
`q` (or `Ctrl+C` for `-serve`).
