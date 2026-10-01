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
| `packages/mcp` | `coop-mcp` | agent machine | Local MCP server that gives the agent its tools, and the setup commands |
| `packages/tui` | `coop-tui` | server | Observe and control sessions |
| `plugin/` | — | agent machine | The Claude Code plugin: the MCP server as one file, and the skill |

## Use it

On an agent machine that is set up (see below), tell the project which session it is in:

```bash
cd ~/work/app
coop-mcp session build-42           # writes ./.coop; agents started here (or below) join build-42
coop-mcp session build-42 --agent reviewer   # and choose the agent name (default: the directory name)
coop-mcp claude                     # Claude Code in that session, messages pushed in
codex                               # any other MCP client: no push, the agent uses wait/inbox
```

The environment wins over the file, so one-off runs need no file:

```bash
coop-mcp claude build-42            # Claude Code in session build-42, messages pushed in
COOP_SESSION=build-42 codex
```

A headless agent (`claude -p`) runs without a person to approve tool calls, so allow the coop
tools up front. The tool prefix is `mcp__coop__` for a server added with `claude mcp add`, and
`mcp__plugin_coop_coop__` for the plugin:

```bash
COOP_SESSION=build-42 claude -p "..." --allowedTools 'mcp__coop__*'
```

The session must exist and be open. The operator creates it in the TUI or with
`coop-hub session add <name>` on the server. With `COOP_AUTO_CREATE_SESSIONS=1` in the hub's
environment, the first agent to join an unknown session creates it.

## Set up an agent machine

Requirements: Node.js 24, and Claude Code or another MCP client.

1. Get the `coop-mcp` command. It is one file:

   ```bash
   mkdir -p ~/.local/bin
   curl -fsSL https://raw.githubusercontent.com/AIToolSharing/agent_coop/main/plugin/coop-mcp.mjs \
     -o ~/.local/bin/coop-mcp && chmod +x ~/.local/bin/coop-mcp
   ```

   (In a clone of this repository, `npm ci && npm run bundle` builds the same file at
   `plugin/coop-mcp.mjs`.)

2. Get a token for this machine from the operator. On the server:
   `coop-hub token add <machine>`. The token shows one time only.

3. Store it. The command checks the token against the service, then writes
   `~/.config/coop/env` with mode 0600 and prints the next steps:

   ```bash
   coop-mcp login https://coop.example.com <machine>.<secret>
   ```

4. Give your agent the tools. In Claude Code, install the plugin; it brings the MCP server and the
   skill that tells the agent when to use it:

   ```
   /plugin marketplace add AIToolSharing/agent_coop
   /plugin install coop@coop
   ```

   Then start sessions with `COOP_CHANNEL=plugin:coop@coop coop-mcp claude`. For any other MCP
   client, register the server yourself:

   ```bash
   claude mcp add --scope user coop -- coop-mcp
   ```

Without a session, the server offers no tools, so a session that does not use coop pays nothing
for it.

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

- Push into a Claude Code session uses channels, a research preview. A channel that is not on
  Anthropic's allowlist needs `--dangerously-load-development-channels`, which shows a warning at
  each start. `coop-mcp claude` sets the flag. On a Team or Enterprise plan, an admin can list
  the plugin in `allowedChannelPlugins` instead, and `claude --channels plugin:coop@coop` runs
  without the warning.
- Claude Code documents that channels also work in `claude -p`. coop has not verified this yet;
  a headless agent can always use `wait`, `ask`, or `inbox`.
- A message to a peer that left the session is kept. The peer gets it when it joins again, with
  the other messages it missed (the newest 100). `send` reports `online: false` in that case,
  and `ask` returns at once instead of waiting.

## Roadmap

Agreed, not yet built:

- **Operator over HTTPS.** An operator token role and `/v1/admin/*` routes on the hub, so that
  `coop-tui` runs on any machine over HTTPS and the server keeps no second credential path
  (`operator.env`, the `coop-operators` group, the `coop-tui` wrapper, the operator NATS user).
- **TUI redesign.** A sidebar (sessions, then the agents of the selected session), a chat-style
  transcript with wrapped text and threads, a composer with an explicit target, an attention
  strip (messages for you, blocked agents, stale asks), details as overlays, and a `:` command
  line for the rare operator actions. The model and the view renderers stay; the shell changes.

## Develop

```bash
npm run check       # format and lint (Biome), types (strict), all tests, bundle up to date
npm run contract    # property-based API contract test (Schemathesis, needs uvx)
npm run bundle      # rebuild plugin/coop-mcp.mjs after a change to packages/core or packages/mcp
```

`plugin/coop-mcp.mjs` is committed, so a plugin install and the `curl` step need no build.
`npm run check` fails when it is stale.

The tests start their own `nats-server` (2.11 or later) on a free port with
`deploy/nats.conf`. They do not touch a server that runs on the machine.
