# Deploy the coop server

This guide sets up one Linux server (a VPS) with:

- `nats-server` on `127.0.0.1:4222` (message store, localhost only),
- `coop-hub` on `127.0.0.1:8080` (the API for agent machines),
- Caddy on port 443 (TLS in front of the hub),
- `coop-tui` for the operator, from any machine, with an operator token.

Agent machines and the TUI connect only to port 443.

## 1. Prerequisites

- A DNS name for the server. Your own domain works, or `<ip-with-dashes>.sslip.io`
  (for example `203-0-113-7.sslip.io`).
- Node.js 24, Caddy 2, and `nats-server` 2.11 or later (release binary in `/usr/local/bin`).
- Open ports: 22 (SSH), 80 (certificate challenge), 443. Keep 4222 and 8080 closed; both
  services listen on localhost only.

## 2. Install

```bash
sudo git clone <this repository> /opt/coop
cd /opt/coop && sudo npm ci && sudo npm run build
sudo deploy/install.sh
```

The script creates the `coop` user, the configuration in `/etc/coop` with a new broker
password, the systemd units, and the `coop-hub` and `coop-tui` commands. It is safe to run
again: it keeps the password and changes only what is missing. It ends with the steps that are
left: TLS and tokens.

What it writes, for reference:

| Path | Mode | Holds |
|---|---|---|
| `/etc/coop/nats.conf` | 644 | the broker configuration (no secrets) |
| `/etc/coop/nats.env` | 640 root:coop | the broker password |
| `/etc/coop/hub.env` | 640 root:coop | the same password for the hub; add `COOP_AUTO_CREATE_SESSIONS=1` here to let the first agent create a session |

Check: `systemctl status nats coop-hub` shows both active. The hub creates the stream and the
buckets when it starts.

## 3. TLS

```bash
sudo cp /opt/coop/deploy/Caddyfile /etc/caddy/Caddyfile
sudo systemctl edit caddy        # add: [Service]  Environment=COOP_DOMAIN=coop.example.com
sudo systemctl restart caddy
```

Check from another machine:

```bash
curl -s https://coop.example.com/openapi.json | head -c 80          # the API document
curl -s -o /dev/null -w '%{http_code}\n' https://coop.example.com/v1/sessions/x   # 401
```

## 4. Add an agent machine

On the server, make a token for the machine. The token shows one time only:

```bash
sudo coop-hub token add laptop
```

Give the token to the machine's owner over a private channel. On the machine, follow
"Set up an agent machine" in the main README. To remove a machine and its agents at once:

```bash
sudo coop-hub token revoke laptop
sudo coop-hub token list
```

## 5. Operate

Make yourself an operator token on the server, then run the TUI from any machine that reaches
the hub (on the server itself, `http://127.0.0.1:8080` works without TLS):

```bash
sudo coop-hub token add --operator you          # shows one time only
coop-tui login https://coop.example.com you.<secret>
coop-tui
```

Create a session with `n` in the TUI, or from a shell:

```bash
sudo coop-hub session add build-42
sudo coop-hub session list
```

Then start agents in that session (see the main README). In the TUI, press `1`–`6` for the
views, `tab` to move between panes, and `q` to quit. The key line at the bottom lists all keys.

## Upgrade

Upgrade all parts together: the hub, the TUI, and the MCP server on every agent machine. A part
reads messages with its own version of the message schema, and an older part drops a message
that only a newer schema allows.

```bash
cd /opt/coop && sudo git pull && sudo npm ci && sudo npm run build
sudo deploy/install.sh
```

Then restart each open TUI and each agent session.

## Backup

The whole state is in `/var/lib/nats`. Stop `nats` (or take a filesystem snapshot) and copy the
directory.
