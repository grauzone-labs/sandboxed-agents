# Sandbox management prototype

A throwaway prototype that asks: does the web Control room work well as a
keyboard-first terminal UI (TUI)? Sandboxes, images and versions are fake, and
no Podman, SSH or model provider is touched. Both the TUI and the web UI are in
English. Everything lives in memory.

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

`go run .` starts the TUI and its web/API service on `127.0.0.1:4317`. The
listener is loopback only. If the port is taken, start fails; pick another one
with `-port`.

## Keys

Arrows pick object or tab, `Tab` selects an action, `Enter` runs it, `n` opens
the new-sandbox wizard, `g` switches instance, `?` shows help, `q` quits. In a
session, `Esc` detaches and keeps the history.

## Two backends, one TUI

This walks through the real multi-backend flow with two independent prototype
backends on the same machine. Their sandbox and image runtime is still fake.

1. **Remote backend.** In terminal B run `go run . -port 4318`.
2. **Remote token.** In B, press `3` (Configuration), `Tab` to **Create named
   token**, `Enter`, then enter a name and a lifetime in hours. The opaque token
   is shown once; copy it before dismissing.
3. **Local backend.** In terminal A run `go run .` (port 4317).
4. **Connect.** In A, open Configuration (`3`) → **Connect remote environment**.
   Enter a label, URL `http://127.0.0.1:4318` and the token (the field is
   hidden).
5. **Use it.** Press `g` and select the new environment.
   - **Disconnect remote environment** drops the connection and keeps its
     label and URL.
   - **Reconnect with replacement token** needs a new token from B.
   - Revoking a token in B closes the streams that use it.

Token and connection management in Configuration always acts on the backend
that hosts this TUI (A), even while B is selected with `g`. To create or revoke
a token for B, use B's own TUI or web UI.

## Web UI and tokens

Open <http://127.0.0.1:4317/> on the same machine. The page is a public login
gate: it needs a named token created in the owning TUI. The token stays in page
memory only, so a reload asks you to log in again. There are no cookies or
`localStorage`.

Every token grants full control, including creating tokens and managing remote
connections. Tokens expire or are revoked, which also closes their streams.
Named tokens, remote connections and fake data are RAM-only and are lost when
the process fully exits. **Disable service** / **Enable service**
(Configuration) keeps them. `q` stops the owning TUI and its server.

## Standalone backend

```sh
go run . -serve -port 4318 -token-file /tmp/sandbox-4318.token
```

`-token-file` is required with `-serve`. The file is created as a private
(0600) regular file, or read if it exists. It holds a setup token, which is the
only credential that survives a restart with the same file; named tokens and
data do not. Never pass a secret as a command-line argument. To log in to the
web UI with it, view the file in a private terminal and paste it into the login
field. Stop the service with `Ctrl+C`.

Connect a TUI to it with a private token file, or omit the file for a hidden
prompt:

```sh
go run . -connect http://127.0.0.1:4318 -token-file /path/to/private.token
```

`SANDBOX_PROTOTYPE_TOKEN` is also read if you already have a token. Use only
tokens you created.

## Across machines

Backends listen on loopback only and there is no service installer. To reach a
remote backend, forward its port over SSH, then connect to the forwarded port
using a token created on the target:

```sh
ssh -N -L 5318:127.0.0.1:4318 user@remote-host
# then, on this machine: Connect remote environment → http://127.0.0.1:5318
```

A non-loopback origin must use HTTPS with a valid certificate. Plain HTTP is
accepted only on loopback.

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
