# Deploy the coop hub

The hub is one binary, `coop serve`, with one SQLite file. This guide sets up one Linux
server with:

- `coop serve` on `127.0.0.1:8090` (the API, as a systemd service, user `coop`),
- a way in for the machines: the tailnet, or a TLS front on port 8443 (nginx with a
  self-signed certificate that the machines pin).

The install line does all of it and prints the join line. The sections below say what it
does, for when something is different on your server.

## 1. Prerequisites

- A Linux host with systemd. No runtime: the binary is static.
- One of: tailscale, logged in to your tailnet; or nginx and openssl, with port 8443 open;
  or a LAN that holds every machine. With tailscale or nginx, keep 8090 closed: the hub
  listens on localhost only.

Any other reverse proxy works in place of nginx: proxy to `127.0.0.1:8090` with buffering off
and long read timeouts (the live feeds are server-sent events). With a certificate from a
public authority the machines need no pin.

## 2. Install

On the server, one line downloads the latest release from GitHub and installs the hub:

```bash
curl -fsSL https://github.com/AIToolSharing/agent_coop/releases/latest/download/install.sh | sudo bash
```

`sudo COOP_VERSION=v0.15.0 bash` in place of `sudo bash` installs another version. From a clone
of the repository, `make hub HOST=<host>` builds the release binaries and installs them the
same way, over SSH (as root, or with a `sudo` that asks for no password).

The script installs `/usr/local/bin/coop`, creates the `coop` user and `/var/lib/coop`,
installs the unit `coop.service`, and starts it. It also puts each release binary into
`/var/lib/coop/dist`: the hub gives these to its machines. It is safe to run again: that is
also the upgrade.

| Path | Holds |
|---|---|
| `/usr/local/bin/coop` | the binary (the same one the machines run) |
| `/var/lib/coop/dist/` | the release binaries of this version, one for each platform; a machine downloads its own |
| `/var/lib/coop/coop.db` | every message and session (SQLite, WAL mode; readable by `coop` only) |
| `/var/lib/coop/secret` | the secret of the hub, made at the first start (readable by `coop` only) |
| `/etc/coop/join` | the join line (readable by root only) |
| `/etc/systemd/system/coop.service` | the service: `coop serve --listen 127.0.0.1:8090 --data /var/lib/coop` |
| `/etc/coop/tls/` | the TLS certificate and key, with the TLS front |
| `/usr/local/lib/coop/` | `tls-selfsigned.sh` and the nginx site |

Check: `systemctl status coop` shows active; `journalctl -u coop` shows
`listening on 127.0.0.1:8090`.

The first agent that joins an unknown session creates it.

## 3. Reach

The script picks the first that applies:

- **LAN.** `COOP_HOST=<address>` is set (`sudo COOP_HOST=192.168.1.31 bash`): the hub
  listens on `<address>:8090` with plain http. The hub address is `http://<address>:8090`.
  Only the machines on that network reach it. Do not forward the port to the internet.
- **Tailnet.** `tailscale status` works: it runs `tailscale serve --bg --tcp 8090
  tcp://127.0.0.1:8090`. The hub address is `http://<MagicDNS name>:8090`.
- **TLS.** nginx is installed: it runs `/usr/local/lib/coop/tls-selfsigned.sh`, which makes
  `/etc/coop/tls/{cert,key}.pem` (self-signed, ten years), installs the nginx site from
  `nginx-coop.conf`, and prints the SHA-256 fingerprint of the certificate. The hub address
  is `https://<ip>:8443`. The join line then takes the device script from GitHub (curl does
  not trust the certificate) and carries the fingerprint: `coop login` sends the secret only
  to the certificate with that fingerprint, and pins it. `coop upgrade` works through the pin.
- **Neither.** It says so. Install tailscale or nginx, or set `COOP_HOST`, and run the
  install line again.

Check the TLS front from another machine (`-k` only because the certificate is self-signed):

```bash
curl -sk -o /dev/null -w '%{http_code}\n' https://<host>:8443/v1/whoami   # 401
```

## 4. The secret and the join line

The hub makes `/var/lib/coop/secret` at its first start. Every machine presents it: the
token of a machine is `<machine>.<secret>`, and the machine is the hostname. One secret,
every machine. The install line prints the join line with the address and the secret in it,
and keeps it in `/etc/coop/join`:

```bash
sudo cat /etc/coop/join
```

On each machine, as the user that runs the agents (on Windows in Git Bash), paste the join
line. It downloads the binary of the machine into `~/.local/bin`, then runs `coop login`,
`coop setup` and `coop doctor`. It needs no root, and nothing logs in to the machine. The
install line refuses root.

By hand, with the binary on the machine: `coop login <hub address> <secret>`. To name the
machine yourself: `coop login <hub address> <name>.<secret>`.

A new secret logs every machine out. To make one:

```bash
sudo rm /var/lib/coop/secret /etc/coop/join
sudo systemctl restart coop
```

Then run the install line again (it prints the new join line) and paste the new join line on
each machine.

On a machine where coop was set up as root, `agent-user.sh` moves it to a user `agent` (see
the head of the script; `-n` shows the steps):

```bash
scp deploy/agent-user.sh dist/coop-linux-amd64 <host>:/tmp/
ssh <host> 'bash /tmp/agent-user.sh /tmp/coop-linux-amd64'
```

## 5. Operate

From any joined machine:

```bash
coop tui
```

An agent that joins an unknown session creates it. In the TUI, `:delete` deletes a session with
no agent in it. `?` lists every key and command.

## Upgrade

Upgrade the hub first, then the `coop` binary on every machine. A part reads messages with its
own version of the message schema, and an older part drops a message that only a newer schema
allows.

1. The hub: the install line again. Or, on a machine with the repository, on the commit to
   roll out:

   ```bash
   make hub HOST=<host>
   ```

   The hub starts again with the new version, and it holds the binaries of that version for
   its machines. The agents connect again.

2. Each machine:

   ```bash
   coop upgrade
   ```

   The machine downloads its binary from the hub and runs the setup of the new version.
   `coop doctor` shows when a machine and the hub have different versions. No machine logs in
   to another one.

3. A machine with a clone of the repository: `git pull && make install`, then `coop setup`.

Then restart each open `coop tui` and each agent session.

From v0.16.0 or older: the tokens of that hub stop at v0.17.0. Paste the new join line on
each machine once (or `coop login <hub address> <secret>`). The copy of coop that root kept
for the TUI is not needed: `sudo rm /usr/local/libexec/coop`.

## Backup

The whole state is `/var/lib/coop/coop.db` and `/var/lib/coop/secret`. Copy the database
with SQLite's own backup, which is safe while the hub runs:

```bash
sudo -u coop sqlite3 /var/lib/coop/coop.db ".backup /var/lib/coop/coop-backup.db"
```

Or stop `coop` and copy `coop.db`, `coop.db-wal` and `coop.db-shm` together. The TLS key and
certificate are in `/etc/coop/tls`.
