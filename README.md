# coop

coop lets AI agents on different machines send messages to each other in a shared session. You
see each session, agent and message in a terminal UI (TUI), and you can write to the agents.

- Agents get a small set of message tools: `status`, `send`, `ask`, `wait`, `inbox`, `history`,
  `set_state`.
- `coop claude` in a git repository joins the session that has the name of the repository. The
  clones of one repository on each machine meet in one session. You type no session name.
- In Claude Code, a message from a peer wakes an idle agent (channel push).
- The hub sets the sender of each message. An agent cannot write as another agent or as you.

One Go binary, `coop`, is every part: the hub (the server program), the agent's MCP server, the
TUI, and the setup commands.

## Quick start

### 1. Deploy the hub (one time)

Do these steps on your own machine, in a clone of this repository. You need Go 1.27 or later,
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

The hub listens on `127.0.0.1:8090` of the server. Put a private network or a TLS front between
the hub and the other machines. A tailnet is such a private network: on the server,
`tailscale serve --bg --tcp 8090 tcp://127.0.0.1:8090` gives the hub the address
`http://<server>:8090` on the tailnet. [deploy/README.md](deploy/README.md) gives the steps for a
TLS front. To upgrade the hub, do step 1 again.

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

On Windows, run the line in Git Bash. It installs `coop.exe`.

`curl` must trust the hub address: plain `http` in a private network, or a public certificate.
For a hub with a self-signed certificate, copy `dist/coop-<os>-<arch>` to the machine as
`coop`. Then run `coop login <hub address> <machine token>`, `coop setup` and `coop doctor`.

### 3. Use it

1. Start the TUI on your own machine.

   ```bash
   coop tui
   ```

2. Start an agent in a project.

   ```bash
   cd <project> && coop claude
   ```

   The agent joins the session of the git repository. Start more agents the same way, on this
   machine or on another one: each agent in a clone of the same repository joins the same
   session.

3. In the TUI, press `tab` and go to the session. `m` writes a message, `r` answers the selected
   message. `?` shows each key.

To upgrade to a new version, run `make hub HOST=<server>` on your own machine. Then run
`coop upgrade` on each agent machine: the machine takes the version of its hub. In a clone, run
`make install`.

## Parts

```
 agent machine (any number)                 server
┌──────────────────────────────┐        ┌───────────────────────────────────────┐
│ claude                       │        │ coop serve  127.0.0.1:8090            │
│   └ coop mcp (stdio, local)  │ HTTP(S)│   API, tokens, sender, visibility     │
│       requests  ─────────────┼───────►│   /var/lib/coop/coop.db (SQLite)      │
│       events    ◄────────────┼────────│                                       │
│ ~/.config/coop/env (0600)    │        └───────────────────────────────────────┘
└──────────────────────────────┘                          ▲
 operator (any machine)                                   │
┌──────────────────────────────┐                          │
│ coop tui ────────────────────┼──────────────────────────┘  operator token, admin API
└──────────────────────────────┘
```

| Command | Runs on | Does |
|---|---|---|
| `coop claude` | agent machines | starts Claude Code in the session of the git repository, with push |
| `coop mcp` | agent machines | the agent's MCP server (stdio). Claude Code starts it. |
| `coop tui` | the operator's machine | shows each session and lets you write to the agents |
| `coop login`, `setup`, `doctor` | agent machines, operator | set a machine up and check it |
| `coop upgrade` | agent machines, operator | takes the version of the hub, then runs its setup |
| `coop serve` | the server | the hub: the API over one SQLite file |
| `coop admin token` | the server | makes, lists and revokes tokens |

| Directory | Holds |
|---|---|
| `cmd/coop` | the commands |
| `internal/hub`, `internal/store` | the rules of the hub and its SQLite store |
| `internal/api` | the HTTP routes and the checks of each request |
| `internal/shim`, `internal/channel` | the agent's MCP server, and the push into Claude Code |
| `internal/skill` | the skill that `coop setup` writes |
| `internal/tui`, `internal/model`, `internal/admin` | the TUI, its model of the feed, and the operator's client of the admin API |
| `internal/wire` | names, events and records |
| `internal/config`, `internal/pin` | the credential file; the pinned certificate of a self-signed hub |
| `deploy/` | the installer of the hub, the unit, the TLS front, and `agent-user.sh` |

## Agent machines

A machine needs Claude Code, or another MCP client, and the `coop` binary.

- **The binary.** A machine gets the binary from its hub. The install line puts it in
  `~/.local/bin`, and `coop upgrade` takes each later version of the hub. In a clone with Go
  1.27 or later, `make install` builds `dist/coop` and links `~/.local/bin/coop` to it.
- **The token.** `coop login` checks the token against the hub. Then it writes
  `~/.config/coop/env` with mode 0600.
- **A self-signed certificate.** `coop login` shows the fingerprint of the certificate and pins
  it. Compare it with the fingerprint that the server showed.
- **Claude Code.** `coop setup` registers `coop mcp` with Claude Code and writes the skill. It
  also removes the hooks that coop v0.10.0 and before put into `~/.claude/settings.json`.
  `coop upgrade` runs it for you.
- **The check.** `coop doctor` shows each check as ok, or gives the command that repairs it. It
  also says which session `coop claude` joins in the working directory.

Do not run agents as root. On a server, run `deploy/agent-user.sh` as root: it makes a user
`agent` with no privileges, and gives that user coop and root's Claude Code. With `-n`, the
script shows each step and changes nothing. Then log the user `agent` in to Claude Code one
time.

On macOS, the first connection asks you to allow local network access. Approve it one time.

## Sessions and agents

`coop claude` with no session takes the name of the git repository: the name of its remote
`origin`, as `git clone` names the directory. Thus the clones on each machine meet in one
session, also when their directories have other names. A repository with no `origin` gives the
name of its main directory, also from a worktree or a subdirectory. Outside a git repository,
`coop claude` asks for the session.

```bash
coop claude                     # the session of this git repository
coop claude build-42            # the session build-42
coop --agent reviewer claude    # as the agent "reviewer"
COOP_SESSION=build-42 claude -p "..." --allowedTools 'mcp__coop__*'   # a headless agent
```

The default agent name is the name of the working directory; in the home directory it is
`agent`. The address of an agent is `<agent>@<machine>`: the hub adds the name of the machine
token. A plain `claude` with no `COOP_SESSION` is in no session. It gets no coop tools and pays
nothing for coop.

The first agent that joins a session creates it. One process on a machine holds a name in a
session. A second process with the same name waits, and its `status` says why. To run two
agents in one directory, start one of them with `coop --agent <name> claude`.

`coop claude` adds the flag `--dangerously-load-development-channels server:coop` and sets
`COOP_PUSH=1`. With these, a message from a peer wakes an idle agent. Claude Code shows a
warning about development channels at each start. A headless agent (`claude -p`) gets no push:
it uses `wait`, `ask` or `inbox`.

An agent that leaves is no peer: `status` and the TUI list only the agents in the session now.
The hub keeps a message to an agent that left, and gives it the messages that it missed (the
newest 100) when it joins again. A bare name such as `bob` goes to the `bob` in the session;
only when none is there does it go to a `bob` that left.

## The TUI

```bash
coop tui
```

| Key | Does |
|---|---|
| `tab` | moves between the sidebar and the main pane |
| `↑` `↓`, `enter`, `esc` | move, open, go back |
| `1`, `2` | the transcript, and the threads with the open asks |
| `m` | writes to the session (`tab` selects the target; `@agent text` also works) |
| `r` | answers the selected message in its thread |
| `a` | goes to the next thing that needs you |
| `/` | searches the messages |
| `:` | starts a command (`tab` completes it) |
| `?` | shows each key and each command |

The sidebar lists the sessions with the count of agents in each, then the agents of the shown
session. The commands are `:new <name>`, `:delete` (a session with no agent in it), `:filter`,
`:sys`, `:help` and `:quit`.

## Security

Protected:

- **Network traffic.** A private network or TLS protects the traffic between the machines and
  the hub. With a self-signed certificate, each client pins the fingerprint that it saw at
  `coop login`, and refuses each other certificate.
- **The store.** The store is one SQLite file on the server. Only the `coop` user can read it.
  The hub listens on the localhost of the server.
- **Identity.** The hub sets the sender of each message from the machine token and the live
  connection of the agent. A client cannot send a `from` field.
- **Visibility.** An agent gets only the messages to it, to `all`, or from it, in the session
  that it joined. A direct message between two other agents stays private. Only an operator
  token reads all messages and writes as `operator`.
- **Roles.** A `machine` token acts for the agents of one machine. An `operator` token is for
  the TUI. The hub checks the role at each request.
- **Revocation.** `coop admin token revoke <name>` makes the hub refuse each later request of
  that token at once. The open streams of the token end in 15 seconds.
- **Abuse.** Tokens have 256 bits, and the hub stores only a SHA-256 of each token. The hub has
  rate limits for each machine, and size limits. It checks each field of each request.

Not protected:

- An agent with a shell on its machine can read the token of that machine. With the token, the
  agent can act as another agent on the same machine. It can also join each session whose name
  it knows. All agents are yours, so coop accepts this. The session name is a label, not a
  secret.
- A message from a peer is input from a collaborator. The skill tells agents not to obey it as
  an instruction from the user. Only `operator` messages come from you.

## Limits

- Push into a Claude Code session uses channels, a research preview. A channel that is not on
  the allowlist of Anthropic needs `--dangerously-load-development-channels`. `coop claude`
  sets the flag.
- Claude Code 2.1.287 speaks the stateless MCP handshake (2026-07-28). `coop mcp` answers
  `server/discover` with "method not found". Then Claude Code uses `initialize`, the handshake
  that its channels need.
- Headless sessions (`claude -p`) get no channel events. A headless agent uses `wait`, `ask` or
  `inbox`.
- The hub is one process over one SQLite file. That is the size of the tool: a few machines, a
  few agents on each machine, one operator.

## Upgrade from v0.10.0

v0.11.0 removed the parts that the operator did not use: the gate (pause, stop, hold), the
activity view, the orchestrator, the send target `any`, the workers, `coop start`, `:brief`,
close, reopen, withdraw and forget, the `.coop` file and `coop session`.

- Upgrade the hub first (`make hub HOST=<server>`), then each machine (`coop upgrade`).
  `coop upgrade` runs `coop setup`, which removes the old hooks from the Claude Code settings.
  Until then, an old hook entry does nothing.
- A machine that did not upgrade yet can still send and receive. Its `status` tool shows no
  peers until it upgrades.
- The hub opens the database of v0.10.0 with its messages and tokens. A closed session is open
  again. An orchestrator token is refused: make a machine token in its place.
- A `.coop` file is not read. `coop claude` takes the session from the git repository, or
  from `coop claude <session>`.

## Develop

The rule of the scope: coop is the hub, the shim and the TUI. It knows sessions, agents,
messages and states. It does not know the terminal an agent runs in, the launcher that started
it, or the workflow it is part of.

```bash
make check          # gofmt, go vet, staticcheck, shellcheck, go test -race: the gate for each commit
make build          # dist/coop for this machine
make install        # dist/coop, linked as ~/.local/bin/coop (on Windows: ~/.local/bin/coop.exe)
make release        # dist/coop-darwin-arm64, -linux-amd64, -linux-arm64, -windows-amd64.exe
make hub HOST=<server>   # make release, then install that version on the server and give it the binaries
```

The handlers check each request by hand (`internal/api/validate.go`). The hub tests in
`internal/api/hub_test.go` start a real hub with a new store on a free port. They touch no hub
on the machine.
