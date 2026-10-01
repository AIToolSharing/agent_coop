#!/usr/bin/env bash
# Set up the coop server on one Linux host with systemd: nats-server and coop-hub on localhost,
# the broker password, and the command wrappers. Run it again after an upgrade; it changes only
# what is missing. TLS (Caddy) and tokens stay manual steps; it prints them.
#
#   sudo deploy/install.sh            from a clone at /opt/coop (see deploy/README.md)
#
# Environment:
#   COOP_HOME         the clone, default /opt/coop
#   COOP_HUB_LISTEN   where the hub listens on the first run, default 127.0.0.1:8080 (behind
#                     Caddy). On a LAN without TLS, use 0.0.0.0:<port> and give agents http://.
set -euo pipefail

home=${COOP_HOME:-/opt/coop}
listen=${COOP_HUB_LISTEN:-127.0.0.1:8080}
etc=/etc/coop

if [ "$(id -u)" -ne 0 ]; then
  echo "run as root: sudo $0" >&2
  exit 2
fi
for cmd in node nats-server systemctl openssl; do
  if ! command -v "$cmd" >/dev/null; then
    echo "missing: $cmd (see deploy/README.md, Prerequisites)" >&2
    exit 2
  fi
done
node_major=$(node --version | sed 's/^v\([0-9]*\).*/\1/')
if [ "$node_major" -lt 24 ]; then
  echo "node $(node --version) is too old; coop needs Node 24 (see deploy/README.md)" >&2
  exit 2
fi
if [ ! -f "$home/packages/hub/dist/main.js" ]; then
  echo "no build in $home: run (cd $home && npm ci && npm run build) first" >&2
  exit 2
fi

step() { printf '\n== %s\n' "$*"; }

step "user"
getent group coop >/dev/null || groupadd --system coop
id coop >/dev/null 2>&1 || useradd --system --gid coop --home "$home" --shell /usr/sbin/nologin coop

step "configuration in $etc"
install -d -m 755 "$etc"
install -m 644 "$home/deploy/nats.conf" "$etc/nats.conf"
if [ ! -f "$etc/nats.env" ]; then
  hub_pw=$(openssl rand -hex 32)
  printf 'COOP_HUB_NATS_PASSWORD=%s\n' "$hub_pw" >"$etc/nats.env"
  printf 'COOP_HUB_NATS_PASSWORD=%s\nCOOP_HUB_LISTEN=%s\n' "$hub_pw" "$listen" >"$etc/hub.env"
  unset hub_pw
  echo "wrote a new password to nats.env and hub.env"
else
  echo "nats.env exists; password kept"
fi
chown root:coop "$etc/nats.env" "$etc/hub.env"
chmod 640 "$etc/nats.env" "$etc/hub.env"
# Left over from before the TUI used the admin API; nothing reads it now.
rm -f "$etc/operator.env"

step "services"
install -m 644 "$home/deploy/nats.service" "$home/deploy/coop-hub.service" /etc/systemd/system/
install -m 755 "$home/deploy/coop-tui" "$home/deploy/coop-hub" /usr/local/bin/
chown -R root:root "$home"
systemctl daemon-reload
systemctl enable --now nats coop-hub
systemctl restart coop-hub
sleep 1
systemctl --no-pager --quiet is-active nats coop-hub && echo "nats and coop-hub are active"

step "left to do"
cat <<TEXT
1. TLS: install Caddy, then
     cp $home/deploy/Caddyfile /etc/caddy/Caddyfile
     systemctl edit caddy      # [Service] Environment=COOP_DOMAIN=coop.example.com
     systemctl restart caddy
   (On a LAN without TLS, the hub listens on $listen; agents use http://<this host>:<port>.)
2. One token per agent machine (shows one time only):
     coop-hub token add laptop
3. Your operator token, then the TUI from any machine (or here, without TLS):
     coop-hub token add --operator you
     coop-tui login https://coop.example.com you.<secret>     # or http://127.0.0.1:8080 here
     coop-tui
4. Optional: let the first agent create a session; add to $etc/hub.env:
     COOP_AUTO_CREATE_SESSIONS=1
   then: systemctl restart coop-hub
TEXT
