# Sandbox management prototype: concept summary

- **Status:** Discussion input for a later `/grilling` session. Not ratified architecture and not a production specification. The root product docs stay authoritative: [README](../../README.md), [CONTEXT.md](../../CONTEXT.md), and [ADRs](../../docs/adr/).
- **Date:** 2026-10-17
- **Baseline:** commit `53dbdf0` on `feature/154b6b62-prototype-sandbox-management-tui`. How to run the prototype: [README](./README.md).
- **Historical verification at `53dbdf0`:** 26 Go tests and 77 executable checks passed, with vet, build, format, and tidy. A two-process browser flow passed for isolation, start, attach, revoke, reconnect, and disconnect, and the secret was cleared on logout and on a 401. The race detector could not run (no GCC). These results were recorded at that commit and were not re-run for this document. They are prototype evidence only, not proof of real Podman, SSH, production service, or live-host behaviour.

## 1. Problem and intended experience

Sandbox management should be simpler. The prototype asks whether the web "Control room" works as a keyboard-first terminal UI (TUI). The web UI is the reference, and the Go TUI is derived from it. All visible web and TUI text is English. Sandboxes, images, agents, integrations, and sessions should be reachable from one layout without deep navigation.

## 2. Terms and scope

Vocabulary follows [CONTEXT.md](../../CONTEXT.md). Terms as used here:

- **Sandbox:** the unit the user creates, starts, stops, and deletes. It has an image, an agent selection, and sessions. The glossary avoids "environment" and "instance" as synonyms for a sandbox. Those words may still name a backend connection target, as described below.
- **Image:** the container filesystem a sandbox is created from, including its toolchains. Sandboxes with the same toolchain set share one image.
- **Toolchain:** an optional set of SDKs or system packages built into an image. Not a "tool" (the glossary reserves that word for installable programs and managed services).
- **Agent:** a coding CLI from the agent catalog, installed and enabled per sandbox. Its version and status are shown.
- **Integration:** a connection to an external account or service, such as Git identity. It offers login and config workflows.
- **Agent session:** a persistent terminal running one agent in a sandbox.
- **Management backend:** the service that holds sandbox, image, and session state and serves the UI.
- **Remote environment:** the label the prototype uses for another management backend that this one connects to. The term applies only to that connection target, never to a sandbox or a controller group.
- **Controller group:** the product namespace an executable manages (`SANDBOXED_AGENTS_GROUP`). The prototype shows a fixed `default` group for its fake data.
- **Sandbox manager:** the trusted Go program inside a sandbox's image (glossary "Manager"). It is distinct from the future host management background service.

Scope: the prototype uses fake data only. It starts no Podman container, opens no SSH connection, and calls no model provider. Its visible text uses "instance" for a backend connection target (for example "g Instance" and "Switch instance") alongside "remote environment". Whether that wording should be made consistent with "management backend" is an optional naming question (section 8). The distinction itself stays clear: a sandbox is managed by a backend, and a backend is not a sandbox.

## 3. Confirmed user requirements

These were stated by the user in the conversation. They describe the intended concept, not the state of the prototype.

### 3.1 Navigation and layout

- One control-room layout. Option A is preferred. The tree navigation of option B is merged into A without per-sandbox child nodes. Options B and C are removed.
- Main areas are **Sandboxes** and **Images**. **Application Configuration** is separate and covers tool and service settings, including future connections to several sandboxed-agents backends.
- The sidebar shows a hierarchical group and its objects only. There are no redundant sandbox subpages. The selected sandbox shows its details and context actions.
- `Tab` cycles only the functions available for the current sandbox. Actions sit inside the relevant detail tab and can be used directly there.

### 3.2 Sandboxes

- The list shows lifecycle status.
- The selected sandbox's details show its image and toolchains, with versions.
- Create, start, stop, and delete are available.
- From a sandbox the user manages its agents, integrations, configuration, and sessions.
- Toolchains are changed through the **Image**, not the sandbox detail, because a shared image change affects other sandboxes.

### 3.3 Agents

- All catalog agents are listed vertically with version and status.
- Enable or Disable is offered per sandbox where it applies.

### 3.4 Integrations

- Git name and email can be edited.
- Each integration offers Connect and Disconnect.
- A real interactive login is unresolved (section 8). The prototype's Connect and Disconnect are fake. Do not treat real login as implemented.

### 3.5 Sessions

- Open sessions are listed with their agent.
- Start, End, and Attach are available in the relevant agent or session details.
- Attach uses a live WebSocket terminal with input, output, resize, detach, reattach, and history.

### 3.6 Images

- One combined view shows image metadata and tool versions. Linked sandboxes navigate to the corresponding sandbox.
- **Build image** is a global action and is not repeated in image detail.
- Image detail offers **Configure toolchains**. Changing a shared image's toolchains shows an impact review, and after explicit confirmation the linked sandboxes are updated and restarted. The user requires that warning. The mechanics behind it are in section 5.4.

### 3.7 New sandbox wizard

The wizard replaces the earlier task-oriented variant. Its steps are:

1. **Image:** pick an existing image (browse its details in a full-height scrolling list), or build a new image with the selected toolchains.
2. **Sandbox:** review the details and choose at least one agent.
3. **Integrations:** optional, and can be skipped. No integration is mandatory.
4. **Review:** only the final confirmation applies changes. Cancelling changes nothing.

### 3.8 Keyboard, web, and safety

- The web UI is the first reference. The TUI is a Go program derived from it and is driven from the keyboard.
- Both front ends share the backend state.
- Stale and offline state must be visible, and the target of each action must be identifiable.
- Existing lifecycle and session protections are preserved. The UI does not bypass the product core.

## 4. Management backend and remote concept

- A backend can connect to another backend by label, origin, and token. It can disconnect, and reconnect with a replacement token.
- Selecting a remote environment with `g` makes the local backend proxy that backend's inventory, actions, forms, reviews, and terminal stream.
- Remote connections do not chain. The remote backend's own connections are not followed, and a self-connection or a duplicate connection is rejected.
- Connection and token administration always acts on the backend that hosts the local TUI, even when a remote environment is selected. Tokens for a remote backend are created in that backend's own TUI or web UI.
- When the remote backend is unreachable, its cached inventory is shown read-only. Writes are rejected and not queued.
- Disconnecting leaves the remote backend running. Reconnecting uses the same connection record and its state.
- The user's token workflow: generate a token on the target system, then enter the target address and token locally to connect.

## 5. Current prototype

### 5.1 Real behaviour in the prototype

- Two independent OS processes can run on ports 4317 and 4318. Each has its own fake inventory, and the local one proxies the remote one over HTTP.
- The API and the WebSocket terminal require authentication.
- The terminal WebSocket uses a single-use ticket with a 15-second lifetime. The ticket travels in the WebSocket subprotocol, not in the URL ([auth.go](./auth.go)).
- The TUI uses Bubble Tea and Lip Gloss. The browser terminal uses xterm.
- The browser polls state about every 300 ms ([index.html](./index.html), [tui_service.go](./tui_service.go)). The prototype exposes an authenticated HTTP API, and its terminal runs over WebSocket. It has no dedicated chat protocol or finalized production API contract.
- Listeners bind to loopback only. Standalone `-serve` and `-browser` require a private `-token-file` (0600) ([token_bootstrap.go](./token_bootstrap.go)).

### 5.2 Simulated behaviour

- Sandboxes, images, toolchains, agent versions, integrations, and Git identity are seeded fake data ([model.go](./model.go)).
- Agent sessions run on a simulated in-memory terminal transport ([terminal.go](./terminal.go)). There is no real shell, agent, or SSH provider. Ctrl+C interrupts a demo agent.
- Create, build, start, and stop are simulated delays (default 650 ms). Connect and Disconnect for integrations are fake.

### 5.3 State, lifetime, and authentication choices

- Fake inventories, named tokens, and remote connections are held in RAM. A full process exit resets them.
- The one exception is the setup token in the private token file. It is a bootstrap credential that survives restarts with the same file. It does not restore named tokens or state.
- A named token has a name, a lifetime, and a secret that is shown once. The secret is 32 random bytes, opaque, and only its hash is stored ([auth.go](./auth.go)). The token list shows metadata. Revoking a token closes its terminal streams.
- Every token has full administrative scope. A role or permission model is not finalized.
- Local TUI access is private owner access. Loopback alone is not trusted.
- The browser keeps its bearer token in page memory only, with no cookies and no `localStorage`. A reload requires logging in again.
- A remote credential lives only in the client backend's memory and never appears in snapshots.
- A remote origin must use HTTPS with a verified certificate. Plain HTTP is accepted only on loopback. Redirects and insecure TLS are not followed or allowed.
- Cross-machine access uses SSH port forwarding that the user sets up. The prototype opens no SSH connection itself and does not manage the forwarding.
- If a remote backend fails, the local backend stays logged in and its configuration stays usable.

### 5.4 Image rollout as prototyped (needs scrutiny)

**User requirement:** when a shared image's toolchains change, the impact review warns that all linked sandboxes will be updated and restarted, and the change applies only after explicit confirmation. Cancelling changes nothing.

**Prototype choices (not requirements):**

- builds or reuses an immutable image for the new set and keeps the previous image,
- points every linked sandbox at the new image, ends its open sessions, and records a restart,
- sets stopped linked sandboxes to running, and warns about it in the review,
- keeps the workspace, home data, and other data.

**Open decisions:** whether ending open sessions and starting previously stopped sandboxes are the intended behaviour, and what the exact failure and recovery behaviour is if a rollout fails partway.

The accepted bulk update and restart must be reconciled with the product's per-sandbox update semantics. `update NAME --with SET` replaces one sandbox's toolchain set and keeps its volumes ([CONTEXT.md](../../CONTEXT.md)). This is a question about how the two fit together, not a decision and not a change to the core contract.

## 6. Future feature boundaries (not built)

- A background host management service, so remote management works without an open TUI. The prototype starts its backend only with the owning TUI, and `-serve` is standalone. There is no OS service installer and no autostart.
- Real interactive integration login, possibly through SSH port forwarding or another mechanism.
- Real agent terminals with reconnection, a multiplexer, and real failure handling.
- Production remote transport: TLS, certificate trust, listen policy, and any multi-hop rules.
- A role or permission model beyond full-administrative tokens.
- Real Podman, SSH, and lifecycle operations behind the UI.

## 7. Confirmed prototype-only facts for grilling

- "Disable service" and "Enable service" in Configuration keep in-memory state. Quitting the owning TUI with `q` stops the service.
- The prototype's image rollout refuses to run while a linked sandbox has an action in progress, and it checks an impact fingerprint against the state the user reviewed. It has no lifecycle lock like [ADR-0007](../../docs/adr/0007-serialize-sandbox-lifecycle-commands.md).
- The prototype opens no SSH connection itself. Its cross-machine access depends on a user-managed SSH port forward to a backend port. That is different from the product's SSH access into a sandbox ([ADR-0002](../../docs/adr/0002-ssh-access-with-opt-in-host-setup.md)).

## 8. Unresolved questions for `/grilling`

These questions are open. They do not answer anything and add no commitment.

### Scope and users

- Who uses this: one operator, or several people with different rights?
- When must token scopes become roles, if ever?

### Backends, Podman identity, and controller groups

- Is one management backend bound to one Podman target and one controller group, or can it hold several? See [ADR-0005](../../docs/adr/0005-podman-names-carry-controller-group.md) and [ADR-0006](../../docs/adr/0006-podman-target-and-execution-identity.md).
- How does the visible "Controller group" relate to a backend connection?
- Is the visible "instance" wording for backends worth aligning with "management backend" and "remote environment", or is it acceptable as is? The glossary's avoid-list concerns sandboxes only.

### Image rollout

- How does the accepted bulk behaviour (confirmed update and restart of all linked sandboxes) reconcile with per-sandbox `update NAME --with SET` and its volume-keeping semantics?
- Should open sessions end and stopped sandboxes start as part of a shared change, or should those cases be handled differently?
- What is the exact failure and recovery behaviour if a rollout fails partway?

### Sessions and agent terminals

- Is ending a session the same as stopping its agent? The glossary says a session ends when its agent exits.
- What does Ctrl+C mean for a real agent?
- Which reconnection mechanism does a real attach need, and what happens to history on failure?

### Background host service

- What installation, lifecycle, persistence, and permission model fits it?
- Which state, if any, survives a restart?

### Authentication

- Where do long-lived token secrets live?
- What revocation and expiry rules apply beyond closing streams?
- How is bootstrap done without a token file?

### Remote transport

- Which transport, TLS, trust model, and listen policy apply to a real remote backend?
- Is multi-hop ever permitted?
- How is the user-managed SSH forwarding to a backend set up, kept alive, and secured? Consider who trusts which host key, and what the forwarded port exposes.

### Integrations

- How does a real interactive login work, and who holds the resulting credentials?

### Concurrency and dangerous actions

- How are concurrent writes from two UIs resolved?
- How is a stale review rejected?
- Which actions need explicit confirmation beyond the ones shown?

### MVP versus later

- Which parts of the prototype belong to an MVP, and which stay future work?

## 9. Suggested future invocation

```
/grilling prototypes/sandbox-management/CONCEPT.md
```

This form was not validated against the CLI. A conversational request that names this file also works.
