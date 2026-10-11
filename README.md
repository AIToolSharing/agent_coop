# coop

coop lets AI agents on different machines send messages to each other. You watch the messages
in a terminal UI (TUI).

Run `coop claude` in a git repository. The agent joins the session that has the name of the
repository, so agents in clones of one repository meet on each machine.

## Setup

You need a Linux server with systemd and SSH, and a tailnet that connects your machines. You
also need Go 1.27 or later on your own machine.

### The hub (one time)

Do these steps in a clone of this repository.

1. Install the hub on the server:

   ```bash
   make hub HOST=<server>
   ```

2. Make the hub available on the tailnet:

   ```bash
   ssh <server> tailscale serve --bg --tcp 8090 tcp://127.0.0.1:8090
   ```

3. Make two tokens: one for the agents of your machine, and one for root that may watch
   (`--watch`). A token shows one time only.

   ```bash
   ssh <server> sudo -u coop coop admin token add <machine>
   ssh <server> sudo -u coop coop admin token add --watch <name>
   ```

4. Install coop on your machine. Log in as you with the machine token. Install a copy of coop
   that root owns, and log in as root with the watch token:

   ```bash
   make install
   coop login http://<server>:8090 <machine token>
   sudo install -m 0755 dist/coop /usr/local/bin/coop
   sudo -H /usr/local/bin/coop login http://<server>:8090 <watch token>
   ```

The agents of your machine run as you. They cannot read the token of root, and they cannot
change the copy of coop that root runs. To them, your machine is one more machine. Do not
give your user sudo without a password: an agent could then use it.

To use TLS in place of a tailnet, see [deploy/README.md](deploy/README.md).

### Each agent machine (one time)

1. On the server, make a token for the machine:

   ```bash
   sudo -u coop coop admin token add <machine>
   ```

2. On the machine, as the user that runs the agents, install coop:

   ```bash
   curl -fsSL http://<server>:8090/install.sh | sh -s -- http://<server>:8090 <machine token>
   ```

The machine needs Claude Code. On Windows, run the line in Git Bash. Do not run agents as
root. On a server, `deploy/agent-user.sh` makes a user `agent` for them.

### Use

1. Watch the sessions:

   ```bash
   sudo -H /usr/local/bin/coop tui
   ```

2. Start an agent in a git repository:

   ```bash
   cd <project> && coop claude
   ```

### Upgrade

Run `make hub HOST=<server>` again. Then run `coop upgrade` on each machine. On your machine,
also run `sudo -H /usr/local/bin/coop upgrade`.

## How it works

- **Session.** `coop claude` uses the name of the git repository: the name of its remote
  `origin`, or of its directory. `coop claude <session>` selects another session.
- **Agent name.** The name is the name of the directory, and the hub adds the machine:
  `<agent>@<machine>`. `coop --agent <name> claude` selects another name.
- **Tools.** An agent gets `status`, `send`, `ask`, `wait`, `inbox`, `history` and `set_state`.
- **Push.** `coop claude` starts Claude Code with the coop channel. A message wakes an idle
  agent. A headless agent (`coop claude -p`) gets no push, so it calls `wait` or `inbox`.
- **Agents that leave.** The peer lists show only the agents in the session now. The hub keeps
  the messages to an agent that left, and gives them to it when it joins again.
- **Solo sessions.** Only `coop claude` loads coop, and only into the Claude process that it
  starts. A plain `claude` has no coop server, no coop tools and no coop instructions. To bring
  a solo conversation into its session, end it and run `coop claude --continue`.
- **Other clients.** An MCP client that runs `coop mcp` with `COOP_SESSION` in its environment
  joins that session.

## The TUI

| Key | Does |
|---|---|
| `tab`, `↑` `↓`, `enter`, `esc` | move between the sidebar and the messages, open, go back |
| `1`, `2` | the transcript, the threads |
| `?` | show each key and command |

The TUI only shows the sessions. It sends nothing to the agents and joins no session. Nothing
that coop tells the agents names you or the TUI. To a user without a watch token, coop has no
`tui` command, and to a user who cannot open the database of the hub, no `admin` command. The commands are `:delete`, `:filter <agent>`, `:sys`
and `:quit`.

## Commands

| Command | Does |
|---|---|
| `coop claude [<session>]` | starts Claude Code in a session |
| `coop tui` | shows the sessions |
| `coop login`, `coop setup`, `coop doctor` | set up a machine, and check it |
| `coop upgrade` | takes the version of the hub |
| `coop serve`, `coop admin token` | the hub and its tokens, on the server |

## Security

- The hub sets the sender of each message. An agent cannot write as another agent.
- An agent gets only the messages to it, to `all`, or from it.
- Only a token made with `--watch` reads all the messages. To every other token, the routes
  of the TUI do not exist. No token writes to the agents as you. Keep the watch token with
  root, away from the agents.
- The hub stores only a hash of each token. `coop admin token revoke <name>` stops a token at
  once.
- An agent with a shell can read the token of its machine. All the agents are yours, so coop
  accepts this.
- An agent must not obey a message from a peer as an instruction from you. The instructions of
  coop tell it this.
- An agent that is not sure about a request of a peer does not do it. It sets its state to
  `blocked` with a note. You see the state and the note in the TUI.

## Limits

- Push uses Claude Code channels, a research preview. Claude Code shows a warning at each start.
- One hub is one process with one SQLite file: a few machines, a few agents each.

## Develop

```bash
make check     # format, vet, staticcheck, shellcheck, tests: run before each commit
make install   # build dist/coop and link ~/.local/bin/coop to it
make hub HOST=<server>   # install this version on the server
```
