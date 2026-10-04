# Deploy the coop hub

The hub is one binary, `coop serve`, with one SQLite file. This guide sets up one Linux
server with:

- `coop serve` on `127.0.0.1:8090` (the API, as a systemd service, user `coop`),
- a TLS front on port 8443: nginx with a self-signed certificate that the clients pin.

Agent machines and the operator's `coop tui` connect only to port 8443.

## 1. Prerequisites

- A Linux host with systemd, nginx and openssl. No runtime: the binary is static.
- Open port: 8443 (and SSH). Keep 8090 closed; the hub listens on localhost only.

Any other reverse proxy works in place of nginx: proxy to `127.0.0.1:8090` with buffering off
and long read timeouts (the live feeds are server-sent events). With a certificate from a
public authority the clients need no pin.

## 2. Install

On a machine with the repository and Go, build the release binaries and copy the one for the
server's platform to the server:

```bash
make release                                   # dist/coop-linux-amd64, -linux-arm64, -darwin-arm64
scp dist/coop-linux-amd64 deploy/install.sh deploy/coop.service <host>:/tmp/
```

On the server:

```bash
cd /tmp && sudo ./install.sh ./coop-linux-amd64
```

The script installs `/usr/local/bin/coop`, creates the `coop` user and `/var/lib/coop`,
installs the unit `coop.service`, and starts it. It is safe to run again: that is also the
upgrade. It ends with the steps that are left: TLS and tokens.

| Path | Holds |
|---|---|
| `/usr/local/bin/coop` | the binary (the same one the clients run) |
| `/var/lib/coop/coop.db` | every event, session, kick and token (SQLite, WAL mode; readable by `coop` only) |
| `/etc/systemd/system/coop.service` | the service: `coop serve --listen 127.0.0.1:8090 --data /var/lib/coop` |
| `/etc/coop/tls/` | the TLS certificate and key (step 3) |

Check: `systemctl status coop` shows active; `journalctl -u coop` shows
`listening on 127.0.0.1:8090`.

To keep the Node hub's rule that only the operator creates sessions, add
`--auto-create=false` to `ExecStart` in the unit (`systemctl edit coop`). By default the first
agent that joins an unknown session creates it.

A new session holds each agent that joins it for the first time, until the operator releases
it in the TUI. `--hold-new=false` makes new sessions start their agents at once. The setting
of one session changes in the TUI (`H`). After an upgrade from a version before the gate,
each existing session holds new agents; agents that the hub knows already may work.

## 3. TLS

```bash
sudo deploy/tls-selfsigned.sh          # or: sudo deploy/tls-selfsigned.sh 9443
```

It makes `/etc/coop/tls/{cert,key}.pem` (self-signed, ten years), installs the nginx site from
`deploy/nginx-coop.conf`, and prints the certificate's SHA-256 fingerprint. Each client shows
the same fingerprint at `coop login` and pins it. It is safe to run again; it keeps the
certificate. Copy `tls-selfsigned.sh` and `nginx-coop.conf` to the server next to each other.

Check from another machine (`-k` only because the certificate is self-signed):

```bash
curl -sk -o /dev/null -w '%{http_code}\n' https://<host>:8443/v1/admin/sessions   # 401
```

## 4. Tokens

Run the token commands on the server as the `coop` user, so that the database files keep
that owner. A token shows one time only.

Your operator token, for `coop tui` and the admin API:

```bash
sudo -u coop coop admin token add --operator you
```

One token per agent machine:

```bash
sudo -u coop coop admin token add laptop
```

Give a token to the machine's owner over a private channel. On the machine:

```bash
coop login https://<host>:8443 laptop.<secret>     # shows and pins the fingerprint
coop setup
coop doctor
```

To remove a machine and its agents at once:

```bash
sudo -u coop coop admin token revoke laptop
sudo -u coop coop admin token list
```

A revoked token fails at once on every request; an open stream of that token ends within
15 seconds. A new token of the same name replaces the old one.

## 5. Operate

From any machine with `coop`:

```bash
coop login https://<host>:8443 you.<secret>
coop tui
```

Sessions are managed in the TUI: `:new <name>`, `:close`, `:reopen`, `:delete`. An agent that
joins an unknown session creates it, unless the unit says `--auto-create=false`. In the TUI,
`?` lists every key and command.

## Upgrade

Upgrade the hub and the `coop` binary on every machine together. A part reads messages with its
own version of the message schema, and an older part drops a message that only a newer schema
allows.

```bash
make release
scp dist/coop-linux-amd64 deploy/install.sh deploy/coop.service <host>:/tmp/
ssh <host> 'cd /tmp && sudo ./install.sh ./coop-linux-amd64'
```

Then restart each open `coop tui` and each agent session.

## Backup

The whole state is `/var/lib/coop/coop.db`. Copy it with SQLite's own backup, which is safe
while the hub runs:

```bash
sudo -u coop sqlite3 /var/lib/coop/coop.db ".backup /var/lib/coop/coop-backup.db"
```

Or stop `coop` and copy `coop.db`, `coop.db-wal` and `coop.db-shm` together. The TLS key and
certificate are in `/etc/coop/tls`.

## Migrate from the Node hub

The Node hub (`coop-hub` with `nats`) and the Go hub keep different stores. Tokens cannot move:
the hub stores only their hashes. History does not move either. The steps:

1. Install the Go hub (section 2) while the Node hub still runs. The unit listens on 8090,
   which the Node hub holds; stop the Node hub first:
   `sudo systemctl disable --now coop-hub nats`.
2. Run `sudo ./install.sh ./coop-linux-amd64` (or `systemctl restart coop` if it is installed).
3. Make new tokens (section 4) and run `coop login` again on every machine.
4. Remove what is left: `sudo rm -rf /opt/coop /etc/coop/hub.env /etc/coop/nats.env /etc/coop/nats.conf /var/lib/nats /usr/local/bin/coop-hub /etc/systemd/system/coop-hub.service /etc/systemd/system/nats.service && sudo systemctl daemon-reload`.
   The nginx site and the certificate stay.
5. On each machine: `coop doctor`, then restart the open Claude sessions and `coop tui`.
