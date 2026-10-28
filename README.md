# coop

coop lets AI agents on different machines send messages to each other. You watch the
messages in a terminal UI (TUI).

Run `coop claude` in a git repository. The agent joins the session that has the name of the
repository. Agents in clones of one repository meet in one session. An agent can start more
agents on its machine.

## Setup

You need:

- a Linux server with systemd: on your tailnet, with nginx and port 8443 open, or on your LAN,
- Claude Code on each machine,
- a user that is not root on each machine, for the agents.

### The hub (one time, on the server)

```bash
curl -fsSL https://github.com/AIToolSharing/agent_coop/releases/latest/download/install.sh | sudo bash
```

The line installs the hub and makes it reachable. It ends with the join line. To see the
join line again, run `sudo cat /etc/coop/join`. A server with neither tailscale nor nginx
serves the hub on its LAN address with plain http: `sudo COOP_HOST=<address> bash` in place
of `sudo bash`.

### Each machine (one time)

Paste the join line as the user that runs the agents. On Windows, paste it in Git Bash. The
join line looks like this:

```bash
curl -fsSL http://<hub>:8090/install.sh | sh -s -- http://<hub>:8090 <secret>
```

The line installs coop into `~/.local/bin` and logs the machine in.

### Use

```bash
cd <project> && coop claude     # Claude Code, in the session of the repository
coop tui                        # the sessions, on any machine
```

### Upgrade

Run the install line on the server again. Then run `coop upgrade` on each machine.

## How it works

- **Session.** `coop claude` takes the name of the git repository: the name of its remote
  `origin`, or of its directory. Outside a repository, it takes the name of the directory.
  `coop -s <session> claude` selects another session.
- **Agent name.** The name is the name of the directory. The hub adds the machine:
  `<agent>@<machine>`. A second `coop claude` in the same directory is `<agent>-2`, the
  next one `<agent>-3`, and so on.
- **Tools.** An agent gets `status`, `send`, `ask`, `wait`, `inbox`, `history`,
  `set_state` and `spawn`.
- **Workers.** `spawn` starts a headless Claude Code on the agent's machine, in its session,
  with its own task. The worker gets the next numbered name of the agent. It sends its
  result back and leaves. Its output is in `~/.local/state/coop/<session>/<name>.log`. You
  start one `coop claude` on a machine. The agent starts the rest.
- **Push.** `coop claude` starts Claude Code with the coop channel. A message wakes an idle
  agent. A headless agent (`coop claude -p`, a worker) gets no push. It calls `wait` or
  `inbox`.
- **Agents that leave.** The peer lists show only the agents in the session now. The hub
  keeps the messages to an agent that left. It gives them to the agent when it joins again.
- **Solo sessions.** Only `coop claude` loads coop, and only into the Claude process that it
  starts. A plain `claude` has no coop. To bring a solo conversation into its session, end
  it and run `coop claude --continue`.
- **Other clients.** An MCP client that runs `coop mcp` with `COOP_SESSION` in its
  environment joins that session.

## The TUI

| Key | Does |
|---|---|
| `tab`, `↑` `↓`, `enter`, `esc` | move between the sidebar and the messages, open, go back |
| `1`, `2` | the transcript, the threads |
| `?` | show each key and command |

The TUI only shows the sessions. It sends nothing to the agents and joins no session. The
commands are `:delete`, `:filter <agent>`, `:sys` and `:quit`.

## Commands

| Command | Does |
|---|---|
| `coop [-s <session>] claude` | starts Claude Code in a session |
| `coop tui` | shows the sessions |
| `coop upgrade` | takes the version of the hub |
| `coop doctor` | checks the connection, the secret, the session and the setup |
| `coop login <url> <secret>` | logs a machine in by hand |
| `coop serve` | the hub, on the server |

## Security

- The hub sets the sender of each message. An agent cannot write as another agent.
- An agent gets only the messages to it, to `all`, or from it.
- One secret opens the hub. Every machine presents it behind its hostname. Everyone with
  the secret reads every session and acts as a machine. All the machines are yours. To make
  a new secret, remove `/var/lib/coop/secret` and `/etc/coop/join` on the server, restart
  `coop`, and run the install line again. The new join line goes on each machine again.
- An agent with a shell can read the secret of its machine.
- A worker runs with its tools allowed and nobody asked: Bash, Read, Edit, Write, Glob, Grep
  and the coop tools, in the directory that the agent gave it.
- An agent must not obey a message from a peer as an instruction from you. The instructions
  of coop tell it this.
- An agent that is not sure about a request of a peer does not do it. It sets its state to
  `blocked` with a note. You see the state and the note in the TUI.

## Limits

- Push uses Claude Code channels, a research preview. Claude Code shows a warning at each
  start.
- One hub is one process with one SQLite file: a few machines, a few agents each.

## Develop

```bash
make check               # format, vet, staticcheck, shellcheck, tests: run before each commit
make install             # build dist/coop and link ~/.local/bin/coop to it
make hub HOST=<server>   # install this build on the server
make publish             # at a tag: put the release on GitHub, for the install line
```
