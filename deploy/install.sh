#!/usr/bin/env bash
# Install the coop hub on one Linux host with systemd: the binary, the `coop` user, the data
# directory and one unit. Run it again after an upgrade with the new binary. TLS and tokens
# stay manual steps; it prints them.
#
#   sudo deploy/install.sh dist/coop-linux-amd64      (the release binary for this host)
set -euo pipefail

bin=${1:-}
unit_dir=/etc/systemd/system
here=$(cd "$(dirname "$0")" && pwd)

if [ "$(id -u)" -ne 0 ]; then
  echo "run as root: sudo $0 <coop binary>" >&2
  exit 2
fi
if [ -z "$bin" ] || [ ! -f "$bin" ]; then
  echo "usage: sudo $0 <coop binary>   (for example dist/coop-linux-amd64 from 'make release')" >&2
  exit 2
fi
for cmd in systemctl openssl; do
  if ! command -v "$cmd" >/dev/null; then
    echo "missing: $cmd" >&2
    exit 2
  fi
done

step() { printf '\n== %s\n' "$*"; }

step "user"
getent group coop >/dev/null || groupadd --system coop
id coop >/dev/null 2>&1 || useradd --system --gid coop --home /var/lib/coop --shell /usr/sbin/nologin coop

step "binary"
install -m 755 "$bin" /usr/local/bin/coop
/usr/local/bin/coop version

step "data in /var/lib/coop"
install -d -o coop -g coop -m 750 /var/lib/coop

step "service"
install -m 644 "$here/coop.service" "$unit_dir/coop.service"
systemctl daemon-reload
systemctl enable --now coop
systemctl restart coop
sleep 1
systemctl --no-pager --quiet is-active coop && echo "coop is active on 127.0.0.1:8090"

if systemctl --quiet is-enabled coop-hub 2>/dev/null || systemctl --quiet is-enabled nats 2>/dev/null; then
  step "the Node hub is still installed"
  echo "coop-hub and nats are the old server. See deploy/README.md, Migrate, to stop them."
fi

step "left to do"
cat <<TEXT
1. TLS in front of the hub, with a self-signed certificate on this host's nginx:
     sudo $here/tls-selfsigned.sh          # port 8443; prints the certificate fingerprint
   (Any other reverse proxy works too: proxy to 127.0.0.1:8090 with buffering off.)
2. Your operator token, then the TUI from any machine (the token shows one time only):
     sudo -u coop coop admin token add --operator you
     coop login https://<this host>:8443 you.<secret>
     coop tui
3. One token per agent machine:
     sudo -u coop coop admin token add laptop
   On that machine: coop login https://<this host>:8443 laptop.<secret>; coop setup; coop doctor
Run the admin commands as the coop user, so that the database files keep that owner.
TEXT
