# Deploy the coop server

This guide sets up one Linux server (a VPS) with:

- `nats-server` on `127.0.0.1:4222` (message store, localhost only),
- `coop-hub` on `127.0.0.1:8080` (the API for agent machines),
- Caddy on port 443 (TLS in front of the hub),
- `coop-tui` for the operator, over the operator's own SSH login.

Agent machines connect only to port 443.

## 1. Prerequisites

- A DNS name for the server. Your own domain works, or `<ip-with-dashes>.sslip.io`
  (for example `203-0-113-7.sslip.io`).
- Node.js 24, Caddy 2, and `nats-server` 2.11 or later (release binary in `/usr/local/bin`).
- Open ports: 22 (SSH), 80 (certificate challenge), 443. Keep 4222 and 8080 closed; both
  services listen on localhost only.

## 2. Install the code

```bash
sudo useradd --system --home /opt/coop --shell /usr/sbin/nologin coop
sudo git clone <this repository> /opt/coop
cd /opt/coop && sudo npm ci && sudo npm run build
sudo chown -R root:root /opt/coop
```

## 3. Configuration and secrets

```bash
sudo mkdir -p /etc/coop
sudo cp /opt/coop/deploy/nats.conf /etc/coop/nats.conf
HUB_PW=$(openssl rand -hex 32)
OP_PW=$(openssl rand -hex 32)

# nats-server gets both passwords.
printf 'COOP_HUB_NATS_PASSWORD=%s\nCOOP_OPERATOR_NATS_PASSWORD=%s\n' "$HUB_PW" "$OP_PW" \
  | sudo tee /etc/coop/nats.env >/dev/null
# The hub gets its own password only.
printf 'COOP_HUB_NATS_PASSWORD=%s\nCOOP_HUB_LISTEN=127.0.0.1:8080\n' "$HUB_PW" \
  | sudo tee /etc/coop/hub.env >/dev/null
# The TUI gets the operator password only.
printf 'COOP_OPERATOR_NATS_PASSWORD=%s\n' "$OP_PW" | sudo tee /etc/coop/operator.env >/dev/null

sudo groupadd --force coop-operators
sudo usermod -aG coop-operators "$USER"
sudo chown root:coop /etc/coop/nats.env /etc/coop/hub.env
sudo chmod 640 /etc/coop/nats.env /etc/coop/hub.env
sudo chown root:coop-operators /etc/coop/operator.env
sudo chmod 640 /etc/coop/operator.env
unset HUB_PW OP_PW
```

## 4. Services

```bash
sudo cp /opt/coop/deploy/nats.service /opt/coop/deploy/coop-hub.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now nats coop-hub
sudo install -m 755 /opt/coop/deploy/coop-tui /opt/coop/deploy/coop-hub /usr/local/bin/
```

Check: `systemctl status nats coop-hub` shows both active. The hub creates the stream and the
buckets when it starts.

## 5. TLS

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

## 6. Add an agent machine

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

## 7. Operate

```bash
ssh -t you@server coop-tui
```

Create a session with `n`, then start agents with `COOP_SESSION=<name>`. Press `1`–`6` for the
views, `tab` to move between panes, and `q` to quit. The key line at the bottom lists all keys.

## Backup

The whole state is in `/var/lib/nats`. Stop `nats` (or take a filesystem snapshot) and copy the
directory.
