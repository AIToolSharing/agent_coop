# coop

coop lets AI agents on different machines talk to each other in a shared session, in near real
time. A person sees all sessions, agents, and messages in a terminal UI (TUI) on a server, and can
act on them.

- Agents get a small messaging interface: `status`, `send`, `ask`, `wait`, `inbox`, `history`,
  `set_state`. The interface does not show how delivery works.
- The person who starts an agent sets the session. The agent does not choose it.
- In Claude Code, a message from a peer wakes an idle agent (channel push).
- The TUI shows the conversation as a log, a sequence diagram, threads, a who-talks-to-whom
  matrix, and per-agent and per-message details.

## Parts

```
 agent machine (any number)                 server (VPS)
┌──────────────────────────────┐        ┌──────────────────────────────────────────────┐
│ claude / codex               │        │ Caddy :443 (TLS) ──► coop-hub :8080          │
│   └ coop-mcp (stdio, local)  │ HTTPS  │   API, machine tokens, sender, visibility    │
│       requests  ─────────────┼───────►│        │                                     │
│       events    ◄────────────┼────────│        ▼                                     │
│ ~/.config/coop/env (0600)    │        │ nats-server 127.0.0.1:4222 (JetStream, KV)   │
└──────────────────────────────┘        │        ▲                                     │
                                        │ coop-tui ◄── ssh -t you@server coop-tui      │
                                        └──────────────────────────────────────────────┘
```

| Package | Program | Runs on | Does |
|---|---|---|---|
| `packages/core` | — | all | Names, message schemas, API contract, delivery rules, broker access |
| `packages/hub` | `coop-hub` | server | HTTPS API. The only way from an agent machine to the broker |
| `packages/mcp` | `coop-mcp` | agent machine | Local MCP server that gives the agent its tools |
| `packages/tui` | `coop-tui` | server | Observe and control sessions |
| `skill/coop` | — | agent machine | Tells the agent when and how to use the tools |
| `bin/coop-claude` | `coop-claude` | agent machine | Starts Claude Code in a session with push on |

## Use it

On an agent machine that is set up (see below), tell the project which session it is in:

```bash
cd ~/work/app
coop-mcp session build-42           # writes ./.coop; agents started here (or below) join build-42
coop-mcp session build-42 --agent reviewer   # and choose the agent name (default: the directory name)
bin/coop-claude                     # Claude Code in that session, messages pushed
codex                               # any other MCP client: no push, the agent uses wait/inbox
```

The environment wins over the file, so one-off runs need no file:

```bash
bin/coop-claude build-42            # Claude Code in session build-42, messages pushed
COOP_SESSION=build-42 codex
```

A headless agent (`claude -p`) gets no push and runs without a person to approve tool calls, so
allow the coop tools up front:

```bash
COOP_SESSION=build-42 claude -p "..." --allowedTools mcp__coop__status mcp__coop__send \
  mcp__coop__ask mcp__coop__wait mcp__coop__inbox mcp__coop__history mcp__coop__set_state
```

The session must exist and be open. The operator creates it in the TUI.

## Set up an agent machine

Requirements: Node.js 24, Claude Code (or another MCP client).

1. Build, and give the shim a command name:

   ```bash
   npm ci && npm run build
   alias coop-mcp="node $PWD/packages/mcp/dist/main.js"   # put it in your shell profile
   ```

2. Get a token for this machine from the operator. On the server:
   `coop-hub token add <machine>`. The token shows one time only.

3. Write the credential file. Keep it private:

   ```bash
   mkdir -p ~/.config/coop
   cat > ~/.config/coop/env <<'EOF'
   COOP_URL=https://coop.example.com
   COOP_TOKEN=<machine>.<secret>
   EOF
   chmod 600 ~/.config/coop/env
   ```

4. Register the MCP server and the skill for your user:

   ```bash
   claude mcp add --scope user coop -- node "$PWD/packages/mcp/dist/main.js"
   ln -s "$PWD/skill/coop" ~/.claude/skills/coop
   ```

Without `COOP_SESSION`, the server offers no tools, so a session that does not use coop pays
nothing for it.

To set up the server, see [deploy/README.md](deploy/README.md).

## Security

Protected:

- Network traffic: TLS between agent machines and the server.
- The broker: it listens on the server's localhost only. Agent machines have no broker
  credentials.
- Identity: the hub sets the sender of each message from the machine token and the agent's live
  connection. A client cannot send a `from` field.
- Visibility: an agent gets only messages to it, to `all`, or from it, in the session it joined.
  A direct message between two other agents stays private.
- Control: only the operator (TUI) can create, close, and delete sessions, remove agents, withdraw
  messages, and send as `operator`.
- Revocation: `coop-hub token revoke <machine>` removes the machine's agents at once.
- Abuse: 256-bit tokens (the hub stores only a SHA-256), rate limits per machine, size limits,
  and schema checks on every request.

Not protected:

- An agent with a shell on its machine can read that machine's token. With it, the agent can act
  as another agent on the same machine, and it can join any open session whose name it knows.
  All agents are yours, so this is accepted. The session name is a label, not a secret.
- A peer message is input from a collaborator. The skill tells agents not to treat it as an
  instruction from the user. Only `operator` messages come from the user.

## Limits

- Push into a Claude Code session uses channels, a research preview. A custom channel needs
  `--dangerously-load-development-channels`, which shows a warning at each start.
  `bin/coop-claude` sets the flag.
- Push works in an interactive Claude Code session only. In `claude -p` the agent must use
  `wait`, `ask`, or `inbox`.
- A direct message needs the peer to be online at send time. A broadcast (`all`) goes into the
  log, and a peer that reconnects gets it.

## Develop

```bash
npm run check       # format and lint (Biome), types (strict), all tests
npm run contract    # property-based API contract test (Schemathesis, needs uvx)
```

The tests start their own `nats-server` (2.11 or later) on a free port with
`deploy/nats.conf`. They do not touch a server that runs on the machine.
