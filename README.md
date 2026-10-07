# coop

coop lets AI agents on different machines talk to each other in a shared session. A person,
the operator, sees each session, agent and message in a terminal UI (TUI). The operator decides
when each agent works.

- Agents get a small set of message tools: `status`, `send`, `ask`, `wait`, `inbox`, `history`,
  `set_state`. The tools do not show how delivery works.
- The person who starts an agent sets the session. The agent does not select it.
- In Claude Code, a message from a peer wakes an idle agent (channel push).
- The operator holds, releases, pauses and stops each agent from the TUI.
- The TUI shows the conversation as a transcript and as threads, with each message whole.
- The TUI shows what each agent does at its terminal: its tool calls, its words, and the files
  that it changed.

One Go binary, `coop`, is every part: the hub (the server program), the agent's MCP server, the
TUI, and the setup commands.

## Quick start

### 1. Deploy the hub (one time)

Do these steps on your own machine, in a clone of this repository. You need Go 1.25 or later,
and a Linux server with systemd that you can log in to with SSH.

1. Build the binaries, copy them to the server, install the hub and start it.

   ```bash
   make hub HOST=<server>
   ```

2. Make your operator token. The token shows one time only.

   ```bash
   ssh <server> 'sudo -u coop coop admin token add --operator <your name>'
   ```

3. Install `coop` on your own machine and store the token.

   ```bash
   make install
   coop login <hub address> <operator token>
   ```

The hub listens on `127.0.0.1:8090` of the server. Put a TLS front or a private network between
the hub and the other machines. [deploy/README.md](deploy/README.md) gives the steps. To
upgrade the hub, do step 1 again.

### 2. Add an agent machine (one time for each machine)

1. Make a token for the machine. Do this on the server. The token shows one time only.

   ```bash
   sudo -u coop coop admin token add <machine>
   ```

2. Install coop. Do this on the machine, as the user that runs the agents.

   ```bash
   curl -fsSL <hub address>/install.sh | sh -s -- <hub address> <machine token>
   ```

   The machine downloads its binary from the hub into `~/.local/bin`. Then the line stores the
   hub address and the token, connects coop to Claude Code, and checks the result. The machine
   needs Claude Code. The line needs no root.

3. If the machine is a server, do not run agents as root. Make a user that has no privileges,
   and do step 2 as that user. On a machine where coop ran as root, `deploy/agent-user.sh`
   moves coop to a user `agent`.

On Windows, run the line in Git Bash. It installs `coop.exe`.

`curl` must trust the hub address: plain `http` in a private network, or a public certificate.
For a hub with a self-signed certificate, copy `dist/coop-<os>-<arch>` to the machine as
`coop`. Then run `coop login <hub address> <machine token>`, `coop setup` and `coop doctor`.

### 3. Use it

1. Start the TUI on your own machine.

   ```bash
   coop tui
   ```

2. Start an agent in a project directory, in a session.

   ```bash
   cd <project> && coop claude <session>
   ```

   To start the agent on another machine, use `coop start`. The directory needs no `.coop` file.

   ```bash
   coop start <machine> <project> <session>
   ```

3. Release the agent. A new agent is held and does no work. In the TUI, press `tab`, go to the
   agent, and press `g`. Type a task, or press `enter` for none.
4. Steer the agent. `p` pauses or resumes it, `x` stops it, and `m` writes a message. `?` shows
   each key.

To upgrade to a new version, run `make hub HOST=<server>` on your own machine. Then run
`coop upgrade` on each agent machine: the machine takes the version of its hub. In a clone, run
`make install`. See [deploy/README.md](deploy/README.md), Upgrade.

The sections below give the details.

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

The diagram shows a hub behind a TLS front. A private network in place of the TLS front works
the same way.

| Command | Runs on | Does |
|---|---|---|
| `coop mcp` | agent machines | the agent's MCP server (stdio). Claude Code starts it. |
| `coop hook pretool` | agent machines | the gate check before a tool call. Claude Code runs it. |
| `coop hook posttool`, `prompt`, `stop` | agent machines | report what the agent does to the hub. Claude Code runs them. |
| `coop tui` | the operator's machine | shows each session and steers each agent |
| `coop login`, `setup`, `session`, `claude`, `doctor` | agent machines, operator | set a machine up and check it |
| `coop start` | any machine | starts a named agent in a session on another machine |
| `coop serve` | the server | the hub: the API over one SQLite file |
| `coop admin token` | the server | makes, lists and revokes tokens |

| Directory | Holds |
|---|---|
| `cmd/coop` | the commands |
| `internal/hub`, `internal/store` | the rules of the hub and its SQLite store |
| `internal/api` | the HTTP routes and the OpenAPI document |
| `internal/shim` | the agent's MCP server |
| `internal/tui` | the TUI |
| `internal/wire` | names, events and records |
| `deploy/` | the installer of the hub, the unit, the TLS front, and `agent-user.sh` for a server that ran coop as root |

## Agent machines

A machine needs Claude Code, or another MCP client, and the `coop` binary.

- **The binary.** A machine gets the binary from its hub. The install line puts it in
  `~/.local/bin`, and `coop upgrade` takes each later version of the hub. In a clone with Go
  1.25 or later, `make install` builds `dist/coop` and links `~/.local/bin/coop` to it. Run it
  again after each `git pull`. `make release` writes `dist/coop-<os>-<arch>` for macOS, Linux
  and Windows.
- **The token.** `coop login` checks the token against the hub. Then it writes
  `~/.config/coop/env` with mode 0600.
- **A self-signed certificate.** `coop login` shows the fingerprint of the certificate and pins
  it. Compare it with the fingerprint that the server showed. `coop login` adds nothing to the
  system trust store.
- **Claude Code.** `coop setup` registers `coop mcp` with Claude Code and writes the skill. It
  also puts the gate hook and the activity hooks into `~/.claude/settings.json`. It keeps a
  copy of that file as `settings.json.before-coop`. Run `coop setup` again after an upgrade of
  `coop`. `coop upgrade` does this for you.
- **The check.** `coop doctor` shows each check as ok, or gives the command that repairs it.
  It also compares the version of the machine with the version of the hub.

Do not run agents as root. On a server, run `deploy/agent-user.sh` as root. The script makes a
user `agent` that has no privileges. It gives that user coop, a copy of root's Claude Code, and
root's coop credentials. Then it runs `coop setup` for that user. With `-n`, the script shows
each step and changes nothing.

After the script, log the user `agent` in to Claude Code one time. Then start the agents as
that user.

On macOS, the first connection asks you to allow local network access. Approve it one time. The
build has a fixed signing identifier, so a new build keeps the approval.

On Windows, an agent machine and the TUI work; the hub (`coop serve`, `deploy/`) is for Linux.
Use Git Bash for the install line of the hub. In a clone, `make install` runs in PowerShell,
in cmd and in Git Bash: it builds `~/.local/bin/coop.exe`, also while a coop runs. Run it again
after each change. The other targets of the Makefile need Git Bash. Claude Code runs the hooks
of coop in Git Bash. The credential file has no mode there: the user's profile directory keeps
it private. `coop claude` and the SSH of `coop start` run as a child process of coop.

A Claude Code session that is in no coop session gets no tools from `coop mcp`. Such a session
pays nothing for coop.

## Sessions and agents

A project directory joins a session through a `.coop` file. `coop session <name>` writes the
file. Each agent that starts in that directory, or below it, joins the session.

```bash
cd ~/work/app
coop session build-42                    # writes ./.coop
coop session build-42 --agent reviewer   # and sets the agent name
coop claude                              # Claude Code in that session, with push
coop --agent reviewer claude             # the same, as the agent "reviewer"
codex                                    # another MCP client: no push
```

The default agent name is the name of the project directory. In the home directory, the default
name is `agent`. The name of the unix user is never the agent name.

The environment wins over the `.coop` file. Thus a run for one time needs no file.

```bash
coop claude build-42                  # Claude Code in session build-42
COOP_SESSION=build-42 codex
```

To start an agent on another machine, as the user `agent` there, use `coop start`. The session
comes from the command line, so the directory needs no `.coop` file.

```bash
coop start basedmatrix git/app build-42              # machine, project, session
coop start -a reviewer basedmatrix git/app build-42  # with an agent name
```

The agent starts over SSH in this terminal. Arguments after the session go to `claude`. `-n`
shows the command and does not run it.

One session on a machine holds a name. The hub does not let in a second session with the same
name. The second agent gets the reason, and joins when the name is free. To run two agents in
one directory, start one of them with `coop --agent <name> claude`.

`coop claude` adds the flag `--dangerously-load-development-channels server:coop` and sets
`COOP_PUSH=1`. With these, a message from a peer wakes an idle agent. Claude Code shows a
warning about development channels at each start. Select "I am using this for local
development". A plain `claude` gets the same tools, but no push. Its messages wait until the
agent calls `wait` or `inbox`.

Each agent in a session is behind the gate of the operator (see Operate). Before each tool
call, the hook `coop hook pretool` asks the hub if the operator lets the agent work.
`coop setup` puts the hook into `~/.claude/settings.json`. Thus each Claude Code session in a
coop session has the hook, however you start it.

On a machine where `coop setup` did not run, `coop claude` gives the hook with `--settings`. In
that case, do not pass a `--settings` of your own. To start one session with no gate, set
`COOP_GATE=off` in its environment. Another MCP client has no hook. The TUI marks such an agent
`soft`, and a hold or a pause is only advice to it.

A headless agent (`claude -p`) gets no push. It uses `wait`, `ask` or `inbox`. Allow the coop
tools when you start it.

```bash
COOP_SESSION=build-42 claude -p "..." --allowedTools 'mcp__coop__*'
```

The first agent that joins an unknown session creates the session. A closed session stays
closed until the operator opens it again. With `coop serve --auto-create=false`, only the
operator creates sessions (`:new <name>` in the TUI).

## Operate

The TUI runs on each machine that has `coop` and your operator token.

```bash
coop tui
```

| Key | Does |
|---|---|
| `tab` | moves between the sidebar and the main pane |
| `↑` `↓`, `enter`, `esc` | move, open, go back |
| `1`, `2` | the transcript, and the threads with the open asks |
| `3` | the activity: what the agents do at their terminals |
| `m` | writes to the session (`tab` selects the target) |
| `r` | answers the selected message in its thread |
| `a` | goes to the next thing that needs you |
| `:` | starts a command (`tab` completes it) |
| `?` | shows each key and each command |

The sidebar lists the sessions, then the agents of the shown session. The commands are `:new`,
`:close`, `:reopen`, `:delete`, `:kick`, `:allow`, `:forget`, `:go`, `:pause`, `:resume`,
`:hold`, `:withdraw`, `:filter`, `:sys` and `:brief`.

### See what the agents do

The hooks of Claude Code report to the hub what each agent does at its terminal. The TUI shows
it in three places.

- **The activity view (`3`).** Each tool call is one line: the tool, what the call does, and
  its result. The result is `✓`, `✗`, or `▸` while the call runs. The words of an agent are
  there whole. A prompt that a person typed at the terminal of an agent has the mark
  `»`. `space` follows the newest line, `/` searches, and `:filter <agent>` shows one agent.
- **The sidebar.** Under an agent, one line shows the tool call that it runs now.
- **The agent details (`enter` on an agent).** They show the git branch, the running calls,
  the files that the agent changed, and its newest activity. A file that a second agent of the
  session changed too has the mark `also <agent>`.

The limits:

- The hub keeps the newest 300 items of each agent in memory. The activity is not in the
  record of the session. After a restart of the hub, the activity is empty.
- A text has at most 2000 characters. A longer text ends with `[…]`.
- Only an agent with the hooks reports: one that `coop claude` started, or each Claude Code
  session on a machine where `coop setup` ran. For another agent, the details say so.
- The agent's own coop tools (`send`, `wait`, and the others) are not in the activity. The
  messages are in the transcript.
- No agent gets the activity of another agent. Only an operator token can read it.

### A brief of the session

`:brief` runs Claude Code on your machine over the shown session: the agents with their states,
notes and gates, and the newest 200 messages. It shows a summary of at most 12 lines in the
main pane; `esc` closes it. `:brief every 10m` repeats it until `:brief off`, or until you show
another session. The run is apart from coop: no MCP server, no settings, no tools, a neutral
directory. Nothing goes to the hub, and no agent joins a session for it.

### Let an agent run the session: the orchestrator

An orchestrator is an agent that may also do what you do in the TUI. It releases, pauses and
stops agents, and it creates, closes and reopens sessions. It reads each message of a session.
Use it for a workflow in which one agent starts and directs the others, for example
[agent-pipeline](https://github.com/map588/agents).

1. Make an orchestrator token on the server, and store it on the machine of the orchestrator.

   ```bash
   ssh <server> 'sudo -u coop coop admin token add --role orchestrator <name>'
   coop login <hub address> <orchestrator token>
   ```

2. Start the orchestrator in its session.

   ```bash
   coop --orchestrator claude <session>
   ```

The orchestrator has three more tools:

| Tool | Does |
|---|---|
| `steer` | releases (with a task), pauses, resumes, stops, allows and forgets agents; sets the hold; creates, closes and reopens sessions |
| `read` | gives each message of a session, also the messages between two other agents |
| `sessions` | lists each session with its agents, their states, notes and gates |

It starts agents on other machines with `coop start`. It is never held when it joins.

Messages do not interrupt the orchestrator one by one, also not your messages. They queue, and
one short notice says that items wait. The next notice comes after it reads them with `inbox`
or `wait`. Agents follow a message of the orchestrator as your instruction,
and they ask the orchestrator, not you. Ask the orchestrator for the status: it answers from
the states and the messages of each agent. You can still pause or stop it. It sends as
itself, never as `operator`. So it asks you each question that is yours. The sidebar marks it
with `★`, and the timeline names it: "released by the orchestrator <name>".

An agent that the orchestrator starts with `coop claude` is a plain agent. The flag does not
pass to it.

### Give work to a free agent: `any`

A `send` or an `ask` to `any` goes to one agent. The hub picks the agent, and the result names
it. In the TUI, `any` is a target of `m`.

- **Free.** The hub picks an agent that is in the session now, that you do not hold or pause,
  and whose state is not `working` or `blocked`. It never picks the sender, and never an
  orchestrator.
- **Least load.** Of the free agents, the one on the machine with the fewest working agents
  gets the message. The count includes the agents of the other sessions. Machines with the
  same load take turns.
- **No free agent.** The send fails with a conflict (409), and the message lists each agent
  with the reason. The hub keeps no such message for later.
- **The state is a report.** An agent says that it works with the tool `set_state`. An agent
  that does not report stays `idle`, and the hub gives it each task. The hub counts a task as
  load only after the agent reports `working`.
- **The name.** `any` is a reserved name, as `all` and `operator`. No agent can have it.

### Workers: agents that wait for work as a service

`deploy/coop-worker.sh` runs an agent as a service on an agent machine. In each round it
starts the CLI of the agent headless. The agent waits for a message for up to five minutes,
does what the message asks, answers the sender, and ends. Then the next round starts.
A worker that waits is `idle`, so a send to `any` can pick it.

1. On the machine, as the user `agent`, make the directory of the worker and name its session.

   ```bash
   mkdir -p ~/coop-workers/claude && cd ~/coop-workers/claude
   coop session <session> --agent <name>
   ```

2. Copy `deploy/coop-worker.sh` and `deploy/coop-worker@.service` to the machine. As root,
   install them and start the worker. The instance name is the kind of the agent.

   ```bash
   install -m 755 coop-worker.sh /usr/local/bin/coop-worker
   install -m 644 coop-worker@.service /etc/systemd/system/
   systemctl enable --now coop-worker@claude
   ```

3. Release the worker one time (`g` in the TUI), or set the hold of the session off (`H`). A
   held worker cannot call its tools. One release is sufficient: the worker joins again in each
   round under the same name.

`deploy/coop-worker.run` is the same service for runit. `deploy/coop-worker.ps1` and
`deploy/coop-worker-tasks.ps1` are the worker for Windows, as a task at logon.

The kinds are `claude`, `codex`, `copilot` and `gemini`. Only the `claude` worker is behind
your gate, because only `coop claude` gives the gate hook. The other kinds run with their tools
allowed and no question, and a hold, a pause or a stop does not refuse their tool calls. Each
peer of the session can make such a worker run commands. Use these kinds only in a session
where you trust each agent.

### Hold, pause, stop

You decide when an agent works. The keys act on the agent under the sidebar cursor, or on the
agent whose details are open.

| Key | Command | Does |
|---|---|---|
| `g` | `:go [agent] [task]` | releases a held or paused agent. The text that you type is its task. |
| `p` | `:pause [agent]`, `:resume [agent]` | stops the agent at its next tool call, or lets it continue |
| `x` | `:kick <agent>` | stops the agent: the hook refuses each tool call until `:allow` |
| `P`, `R` | `:pause`, `:resume` | pauses each working agent of the session, resumes each paused agent |
| `H` | `:hold on\|off` | new agents of the session wait for your release, or start at once |

- **Held.** A new session holds each agent that joins it for the first time. The hook refuses
  the first tool call of the agent, and the agent waits. The attention line counts the held
  agents, and `a` goes to the next one.
- **A pipeline.** If another agent starts the agents of a session, release the first agent and
  press `H`.
- **Paused.** A pause stops the agent at its next tool call. It does not interrupt a command
  that runs. A held or paused agent can still read and write messages.
- **No answer from the hub.** If the hook gets no answer from the hub, the last answer of the
  hub decides. An agent that you hold, paused or stopped stays refused. Thus a restart of the
  hub releases no agent. Each other agent works on, and the hook writes one line on stderr.
  Thus a hub that is down does not stop your agents. An agent that the hub never answered has
  no last answer: it works, also in a session that holds new agents. The hook keeps the last
  answer in `~/.config/coop/gate/`.
- **The default.** `coop serve --hold-new=false` makes each new session start its agents at
  once.

### Agents that left, and agents that you removed

An agent that left stays in the agent list for five minutes. `:forget <name>` drops it at once,
also from the peers of the other agents. `:forget` with no name drops each agent that left. A
forgotten agent can join again, as a new agent.

`:kick` removes an agent, and the agent leaves the agent list. If it tries to join, the list
shows it again for five minutes as `refused 2m ago`. Thus you see that the agent waits for
`:allow <name>`.

A second session that asks for a name in use shows as a line `duplicate refused 2m ago`. The
line is under the agent that holds the name.

## Security

Protected:

- **Network traffic.** TLS protects the traffic between the machines and the hub. With a public
  certificate, the usual verification applies. With a self-signed certificate, each client pins
  the fingerprint that it saw at `coop login`. The client refuses each other certificate.
- **The store.** The store is one SQLite file on the server. Only the `coop` user can read it.
  The hub listens on the localhost of the server, so only the TLS front is reachable.
- **Identity.** The hub sets the sender of each message from the machine token and the live
  connection of the agent. A client cannot send a `from` field.
- **Visibility.** An agent gets only the messages to it, to `all`, or from it, in the session
  that it joined. A direct message between two other agents stays private.
- **Activity.** The hub gives the activity of the agents (tool calls, words, prompts, changed
  files) only to an operator token. The hub keeps it in memory only.
- **Roles.** Each token has one role. The hub checks the role at each request.

  | Role | Agent tools | Reads all sessions | Changes sessions and agents | Sends as `operator` |
  |---|---|---|---|---|
  | `machine` | yes | no | no | no |
  | `operator` | no | yes | yes | yes |
  | `orchestrator` | yes | yes | yes | no |

  Only the person who starts a process selects its role (`coop --orchestrator claude`). A
  credential file or a `.coop` file cannot.
- **Revocation.** `coop admin token revoke <name>` makes the hub refuse each later request of
  that token at once. The open streams of the token end in 15 seconds.
- **Abuse.** Tokens have 256 bits, and the hub stores only a SHA-256 of each token. The hub has
  rate limits for each machine, and size limits. It checks each request against the API
  contract.

Not protected:

- An agent with a shell on its machine can read the token of that machine. With the token, the
  agent can act as another agent on the same machine. It can also join each open session whose
  name it knows, or create a session. All agents are yours, so coop accepts this. The session
  name is a label, not a secret.
- The gate stops an agent that does not obey instructions. It does not stop a hostile agent:
  such an agent can start a process that has no hook.
- The activity can hold a secret: a command line, or words of an agent. Each person with an
  operator token can read it while the hub keeps it.
- An orchestrator token on a machine gives its power to each process there that can read the
  credential file. Put it only on a machine that you trust as you trust your operator token.
  The orchestrator's agent can be told what to do by a message of a peer. The skill tells it
  not to obey a peer as the user.
- A message from a peer is input from a collaborator. The skill tells agents not to obey it as
  an instruction from the user. Only `operator` messages come from the user.

## Limits

- Push into a Claude Code session uses channels, a research preview. A channel that is not on
  the allowlist of Anthropic needs `--dangerously-load-development-channels`. The flag shows a
  warning at each start. `coop claude` sets the flag.
- Claude Code 2.1.287 speaks the stateless MCP handshake (2026-07-28). `coop mcp` answers
  `server/discover` with "method not found". Then Claude Code uses `initialize`, the handshake
  that its channels need.
- Headless sessions (`claude -p`) get no channel events. Tests on 2.1.287 showed this, also with
  more than one turn over stream-json. A headless agent uses `wait`, `ask` or `inbox`.
- The hub keeps a message to a peer that left the session. The peer gets it when it joins
  again, with the other messages that it missed (the newest 100). In that case, `send` reports
  `online: false` and `state: away`, and `ask` returns at once.
- The hub is one process over one SQLite file. That is the size of the tool: a few machines, a
  few agents on each machine, one operator.

## Develop

The rule of the scope: coop is the hub, the shim and the TUI. It knows sessions, agents,
messages, states, the gate and the trace. It does not know the terminal an agent runs in, the
launcher that started it, or the workflow it is part of. An integration uses coop's public
surface, the API and the environment, and puts nothing into coop's protocol.

```bash
make check          # gofmt, go vet, staticcheck, shellcheck, go test -race: the gate for each commit
make contract       # the API against its own /openapi.json with Schemathesis (needs uvx)
make build          # dist/coop for this machine
make release        # dist/coop-darwin-arm64, -linux-amd64, -linux-arm64, -windows-amd64.exe
```

The API contract is `internal/api/openapi.json`. The hub serves it at `/openapi.json`. The
handlers check each request by hand against the same rules. `make contract` checks that the
handlers and the document agree. Run it after each change below `internal/api`.

The hub tests in `internal/api/hub_test.go` start a real hub with a new store on a free port.
They touch no hub on the machine.
