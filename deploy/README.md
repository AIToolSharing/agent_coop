# Deploy the coop server

This guide sets up one Linux server with:

- `nats-server` on `127.0.0.1:4222` (message store, localhost only),
- `coop-hub` on `127.0.0.1:8090` (the API, Node.js, until the Go hub replaces it),
- a TLS front on port 8443: nginx with a self-signed certificate that the clients pin.

Agent machines and the operator's `coop tui` connect only to port 8443.

## 1. Prerequisites

- Node.js 24 (`node` on the PATH; a tarball from nodejs.org under `/usr/local` works),
  `nats-server` 2.11 or later (release binary in `/usr/local/bin`), nginx, openssl.
- Open port: 8443 (and SSH). Keep 4222 and 8090 closed; both services listen on localhost only.

Any other reverse proxy works in place of nginx: proxy to `127.0.0.1:8090` with buffering off
and long read timeouts (the live feeds are server-sent events). With a certificate from a
public authority the clients need no pin.

## 2. Install

```bash
sudo git clone <this repository> /opt/coop      # or rsync a clone to /opt/coop
cd /opt/coop && sudo npm ci && sudo npm run build
sudo deploy/install.sh
```

The script creates the `coop` user, the configuration in `/etc/coop` with a new broker
password, the systemd units, and the `coop-hub` command. It is safe to run again: it keeps the
password and changes only what is missing. It ends with the steps that are left: TLS and tokens.

What it writes, for reference:

| Path | Mode | Holds |
|---|---|---|
| `/etc/coop/nats.conf` | 644 | the broker configuration (no secrets) |
| `/etc/coop/nats.env` | 640 root:coop | the broker password |
| `/etc/coop/hub.env` | 640 root:coop | the same password for the hub, and `COOP_HUB_LISTEN`; add `COOP_AUTO_CREATE_SESSIONS=1` here to let the first agent create a session |

Check: `systemctl status nats coop-hub` shows both active. The hub creates the stream and the
buckets when it starts.

## 3. TLS

```bash
sudo deploy/tls-selfsigned.sh          # or: sudo deploy/tls-selfsigned.sh 9443
```

It makes `/etc/coop/tls/{cert,key}.pem` (self-signed, ten years), installs the nginx site from
`deploy/nginx-coop.conf`, moves the hub to `127.0.0.1:8090`, and prints the certificate's
SHA-256 fingerprint. Each client shows the same fingerprint at `coop login` and pins it. It is
safe to run again; it keeps the certificate.

Check from another machine (`-k` only because the certificate is self-signed):

```bash
curl -sk -o /dev/null -w '%{http_code}\n' https://<host>:8443/v1/admin/sessions   # 401
```

## 4. Add an agent machine

On the server, make a token for the machine. The token shows one time only:

```bash
sudo coop-hub token add laptop
```

Give the token to the machine's owner over a private channel. On the machine:

```bash
coop login https://<host>:8443 laptop.<secret>     # shows and pins the fingerprint
coop setup
coop doctor
```

To remove a machine and its agents at once:

```bash
sudo coop-hub token revoke laptop
sudo coop-hub token list
```

## 5. Operate

Make yourself an operator token on the server, then run the TUI from any machine with `coop`:

```bash
sudo coop-hub token add --operator you          # shows one time only
coop login https://<host>:8443 you.<secret>
coop tui
```

Create a session with `:new <name>` in the TUI, or from a shell:

```bash
sudo coop-hub session add build-42
sudo coop-hub session list
```

Then start agents in that session (see the main README). In the TUI, `?` lists every key and
command; the hint line at the bottom shows the ones that apply.

## Upgrade

Upgrade the hub and the `coop` binary on every machine together. A part reads messages with its
own version of the message schema, and an older part drops a message that only a newer schema
allows.

```bash
cd /opt/coop && sudo git pull && sudo npm ci && sudo npm run build
sudo deploy/install.sh
```

Then restart each open `coop tui` and each agent session.

## Backup

The whole state is in `/var/lib/nats`. Stop `nats` (or take a filesystem snapshot) and copy the
directory. The TLS key and certificate are in `/etc/coop/tls`.
