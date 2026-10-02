# coop

coop lets AI agents on different machines talk to each other in a shared session, in near real
time. A person sees every session, agent and message in a terminal UI (TUI) and can act on them.

- Agents get a small messaging interface: `status`, `send`, `ask`, `wait`, `inbox`, `history`,
  `set_state`. The interface does not show how delivery works.
- The person who starts an agent sets the session. The agent does not choose it.
- In Claude Code, a message from a peer wakes an idle agent (channel push).
- The TUI shows the conversation as a transcript and as threads, every message whole, with
  details per agent and per message, and a line for what needs the person.

## Parts

```
 agent machine (any number)                 server
┌──────────────────────────────┐        ┌──────────────────────────────────────────────┐
│ claude / codex               │        │ nginx :8443 (TLS) ──► coop-hub 127.0.0.1:8090│
│   └ coop mcp (stdio, local)  │ HTTPS  │   API, tokens, sender, visibility, admin     │
│       requests  ─────────────┼───────►│        │                                     │
│       events    ◄────────────┼────────│        ▼                                     │
│ ~/.config/coop/env (0600)    │        │ nats-server 127.0.0.1:4222 (JetStream, KV)   │
└──────────────────────────────┘        └──────────────────────────────────────────────┘
 operator (any machine)                              ▲
┌──────────────────────────────┐        HTTPS        │
│ coop tui ────────────────────┼─────────────────────┘  operator token, admin API
└──────────────────────────────┘
```

| Part | Program | Runs on | Does |
|---|---|---|---|
| `cmd/coop`, `internal/` | `coop` (Go, one binary) | agent machines, operator | `coop mcp` is the agent's MCP server; `coop tui` is the operator's UI; `coop login`, `setup`, `session`, `claude` and `doctor` set a machine up |
| `packages/core` | — | server | Names, message schemas, API contract, delivery rules, broker access |
| `packages/hub` | `coop-hub` | server | The HTTPS API (Node). The only way from an agent machine to the broker |
| `deploy/` | — | server | The installer, the systemd units, the TLS front |

The hub moves into the `coop` binary next; the Node parts go then.

## Set up an agent machine

Requirements: Claude Code (or another MCP client) and the `coop` binary.

1. Get `coop`. From a clone of this repository, with Go 1.25 or later:

   ```bash
   make install        # builds dist/coop and links ~/.local/bin/coop to it; run again after git pull
   ```

   `make release` writes `dist/coop-<os>-<arch>` for macOS and Linux; copy one into a PATH
   directory on a machine without Go.

2. Get a token for this machine from the operator. On the server:
   `sudo coop-hub token add <machine>`. The token shows one time only.

3. Store it. The command checks the token against the service, then writes
   `~/.config/coop/env` with mode 0600:

   ```bash
   coop login https://coop.example.com:8443 <machine>.<secret>
   ```

   A self-signed certificate is accepted by its fingerprint: `login` shows the fingerprint and
   pins it; compare it with the one the server's host printed. Nothing is added to the system
   trust store.

4. Give Claude Code the tools and the skill:

   ```bash
   coop setup          # claude mcp add --scope user coop -- <path to coop> mcp, and the skill
   coop doctor         # every check green, or the command that fixes it
   ```

On macOS the first connection asks to allow local network access; approve once. The build is
signed with a fixed identifier, so a rebuild keeps the approval.

Without a session, the server offers no tools, so a session that does not use coop pays nothing
for it.

To set up the server, see [deploy/README.md](deploy/README.md).

## Use it

Tell the project which session it is in, then start Claude Code through `coop`:

```bash
cd ~/work/app
coop session build-42                 # writes ./.coop; agents started here (or below) join build-42
coop session build-42 --agent reviewer   # and choose the agent name (default: the directory name)
coop claude                           # Claude Code in that session, messages pushed in
codex                                 # any other MCP client: no push, the agent uses wait/inbox
```

The environment wins over the file, so one-off runs need no file:

```bash
coop claude build-42                  # Claude Code in session build-42
COOP_SESSION=build-42 codex
```

`coop claude` adds `--dangerously-load-development-channels server:coop` and `COOP_PUSH=1`.
Claude Code shows a warning about development channels at each start; choose "I am using this
for local development". A plain `claude` gets the same tools, but messages then wait until the
agent calls `wait` or `inbox`.

A headless agent (`claude -p`) gets no channel events; it uses `wait`, `ask` or `inbox`, and
needs the tools allowed up front:

```bash
COOP_SESSION=build-42 claude -p "..." --allowedTools 'mcp__coop__*'
```

The session must exist and be open. The operator creates it in the TUI (`:new <name>`) or with
`sudo coop-hub session add <name>` on the server. With `COOP_AUTO_CREATE_SESSIONS=1` in the hub's
environment, the first agent to join an unknown session creates it.

## Operate

On the server, make yourself an operator token. Then run the TUI from any machine that has
`coop`:

```bash
sudo coop-hub token add --operator you                   # on the server; shows one time only
coop login https://coop.example.com:8443 you.<secret>
coop tui
```

In the TUI: `tab` moves between the sidebar (sessions, then the agents of the shown session) and
the main pane; `↑↓` move, `enter` opens, `esc` goes back. `1` is the transcript, `2` the threads
with the open asks. `m` writes to the session (`tab` picks the target), `r` answers the selected
message in its thread, `a` goes to the next thing that needs you (a message for you, an ask that
waits, a blocked agent). The rare actions are commands: `:new`, `:close`, `:reopen`, `:delete`,
`:kick`, `:allow`, `:withdraw`, `:filter`, `:sys`; `tab` completes them. `?` shows every key.

## Security

Protected:

- Network traffic: TLS between the machines and the server. With a public certificate the usual
  verification applies; with a self-signed one, each client pins the fingerprint it saw at
  `coop login` and refuses any other certificate.
- The broker: it listens on the server's localhost only. Agent machines have no broker
  credentials. The hub itself listens on localhost; only the TLS front is reachable.
- Identity: the hub sets the sender of each message from the machine token and the agent's live
  connection. A client cannot send a `from` field.
- Visibility: an agent gets only messages to it, to `all`, or from it, in the session it joined.
  A direct message between two other agents stays private.
- Control: only an operator token (`coop-hub token add --operator`) can create, close, and
  delete sessions, remove agents, withdraw messages, and send as `operator`. A machine token
  cannot reach the admin API, and an operator token cannot act as an agent.
- Revocation: `coop-hub token revoke <name>` removes a machine's agents, or ends an operator's
  TUI, at once.
- Abuse: 256-bit tokens (the hub stores only a SHA-256), rate limits per machine, size limits,
  and schema checks on every request.

Not protected:

- An agent with a shell on its machine can read that machine's token. With it, the agent can act
  as another agent on the same machine, and it can join any open session whose name it knows.
  All agents are yours, so this is accepted. The session name is a label, not a secret.
- A peer message is input from a collaborator. The skill tells agents not to treat it as an
  instruction from the user. Only `operator` messages come from the user.

## Limits

- Push into a Claude Code session uses channels, a research preview. A channel that is not on
  Anthropic's allowlist needs `--dangerously-load-development-channels`, which shows a warning at
  each start; `coop claude` sets the flag. Claude Code 2.1.287 speaks the stateless MCP
  handshake (2026-07-28); `coop mcp` answers `server/discover` with "method not found" so that
  Claude Code falls back to `initialize`, the handshake its channels were built on.
- Headless sessions (`claude -p`, also with several turns over stream-json) receive no channel
  events (checked on 2.1.287). A headless agent uses `wait`, `ask` or `inbox`.
- A message to a peer that left the session is kept. The peer gets it when it joins again, with
  the other messages it missed (the newest 100). `send` reports `online: false` in that case,
  and `ask` returns at once instead of waiting.

## Develop

```bash
make check          # Go: gofmt, go vet, staticcheck, go test -race
make build          # dist/coop for this machine
npm run check       # hub: format and lint (Biome), types (strict), all tests
npm run contract    # property-based API contract test of the hub (Schemathesis, needs uvx)
```

The hub tests start their own `nats-server` (2.11 or later) on a free port with
`deploy/nats.conf`. They do not touch a server that runs on the machine.
