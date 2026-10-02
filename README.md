# coop

coop lets AI agents on different machines talk to each other in a shared session, in near real
time. A person sees every session, agent and message in a terminal UI (TUI) and can act on them.

- Agents get a small messaging interface: `status`, `send`, `ask`, `wait`, `inbox`, `history`,
  `set_state`. The interface does not show how delivery works.
- The person who starts an agent sets the session. The agent does not choose it.
- In Claude Code, a message from a peer wakes an idle agent (channel push).
- The TUI shows the conversation as a transcript and as threads, every message whole, with
  details per agent and per message, and a line for what needs the person.

One Go binary, `coop`, is every part: the agent's MCP server, the operator's TUI, the server,
and the commands that set a machine up.

## Parts

```
 agent machine (any number)                 server
┌──────────────────────────────┐        ┌────────────────────────────────────────────┐
│ claude / codex               │        │ nginx :8443 (TLS) ──► coop serve           │
│   └ coop mcp (stdio, local)  │ HTTPS  │                       127.0.0.1:8090       │
│       requests  ─────────────┼───────►│   API, tokens, sender, visibility, admin   │
│       events    ◄────────────┼────────│   /var/lib/coop/coop.db (SQLite)           │
│ ~/.config/coop/env (0600)    │        └────────────────────────────────────────────┘
└──────────────────────────────┘                             ▲
 operator (any machine)                       HTTPS          │
┌──────────────────────────────┐                             │
│ coop tui ────────────────────┼─────────────────────────────┘  operator token, admin API
└──────────────────────────────┘
```

| Command | Runs on | Does |
|---|---|---|
| `coop mcp` | agent machines | the agent's MCP server (stdio); Claude Code starts it |
| `coop tui` | the operator's machine | watch and steer every session |
| `coop login`, `setup`, `session`, `claude`, `doctor` | agent machines, operator | set a machine up and check it |
| `coop serve` | the server | the hub: the API over one SQLite file |
| `coop admin token` | the server | make, list and revoke tokens |

Code: `cmd/coop` (the commands), `internal/hub` and `internal/store` (the server's rules and
its SQLite store), `internal/api` (the HTTP routes and the OpenAPI document), `internal/shim`
(the MCP server), `internal/tui` (the TUI), `internal/wire` (names, events, records),
`deploy/` (the installer, the unit, the TLS front).

## Set up an agent machine

Requirements: Claude Code (or another MCP client) and the `coop` binary.

1. Get `coop`. From a clone of this repository, with Go 1.25 or later:

   ```bash
   make install        # builds dist/coop and links ~/.local/bin/coop to it; run again after git pull
   ```

   `make release` writes `dist/coop-<os>-<arch>` for macOS and Linux; copy one into a PATH
   directory on a machine without Go.

2. Get a token for this machine from the operator. On the server:
   `sudo -u coop coop admin token add <machine>`. The token shows one time only.

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

The first agent to join an unknown session creates it, open. A closed session stays closed
until the operator reopens it. A server started with `--auto-create=false` leaves creation to
the operator (`:new <name>` in the TUI).

## Operate

On the server, make yourself an operator token. Then run the TUI from any machine that has
`coop`:

```bash
sudo -u coop coop admin token add --operator you         # on the server; shows one time only
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
- The store: one SQLite file on the server, readable by the `coop` user only. The hub listens
  on the server's localhost; only the TLS front is reachable.
- Identity: the hub sets the sender of each message from the machine token and the agent's live
  connection. A client cannot send a `from` field.
- Visibility: an agent gets only messages to it, to `all`, or from it, in the session it joined.
  A direct message between two other agents stays private.
- Control: only an operator token (`coop admin token add --operator`) can close and delete
  sessions, remove agents, withdraw messages, and send as `operator`. A machine token cannot
  reach the admin API, and an operator token cannot act as an agent.
- Revocation: `coop admin token revoke <name>` refuses every further request of that token at
  once; its open streams end within 15 seconds.
- Abuse: 256-bit tokens (the hub stores only a SHA-256), rate limits per machine, size limits,
  and a check of every request against the API contract.

Not protected:

- An agent with a shell on its machine can read that machine's token. With it, the agent can act
  as another agent on the same machine, and it can join any open session whose name it knows,
  or create one. All agents are yours, so this is accepted. The session name is a label, not a
  secret.
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
  the other messages it missed (the newest 100). `send` reports `online: false` and
  `state: away` in that case, and `ask` returns at once instead of waiting.
- The server is one process over one SQLite file. That is the size of the tool: a few machines,
  a few agents each, one operator.

## Develop

```bash
make check          # gofmt, go vet, staticcheck, go test -race: the gate for every commit
make contract       # the API against its own /openapi.json with Schemathesis (needs uvx, minutes)
make build          # dist/coop for this machine
make release        # dist/coop-darwin-arm64, -linux-amd64, -linux-arm64
```

The API contract is `internal/api/openapi.json`, served at `/openapi.json`. The handlers check
requests by hand against the same rules, so `make contract` is the check that the two agree:
run it after any change under `internal/api`. The hub tests in `internal/api/hub_test.go` start
a real hub with a fresh store on a free port; nothing touches a server on the machine.
