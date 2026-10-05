#!/usr/bin/env bash
# Install the coop hub on one Linux host with systemd: the binary, the `coop` user, the data
# directory, one unit, and the release binaries that the hub gives to its devices. Run it
# again with the new binaries for an upgrade. TLS and tokens stay manual steps; it prints them.
#
#   deploy/install.sh [<coop binary>]
#
# <coop binary> is the release binary for this host (default: coop-linux-<arch> next to this
# script). Each coop-<os>-<arch> file in the directory of that binary goes to
# /var/lib/coop/dist. A device downloads the one for its platform from the hub: with the
# install line that this script prints, and later with `coop upgrade`.
#
# `make hub HOST=<ssh host>` copies the files to a host and runs this script there.
set -euo pipefail

unit_dir=/etc/systemd/system
dist=/var/lib/coop/dist
here=$(cd "$(dirname "$0")" && pwd)

if [ "$(id -u)" -ne 0 ]; then
  if ! command -v sudo >/dev/null; then
    echo "run as root: $0 [<coop binary>]" >&2
    exit 2
  fi
  exec sudo "$0" "$@"
fi
case $(uname -m) in
  x86_64) arch=amd64 ;;
  aarch64 | arm64) arch=arm64 ;;
  *) arch=$(uname -m) ;;
esac
bin=${1:-$here/coop-linux-$arch}
if [ ! -f "$bin" ]; then
  echo "no such file: $bin" >&2
  echo "usage: $0 [<coop binary>]   (for example dist/coop-linux-amd64 from 'make release')" >&2
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
# A hub from before the unit set UMask=0077 left the database files readable by every user.
find /var/lib/coop -maxdepth 1 -type f -name 'coop.db*' -exec chmod 600 {} +

step "binaries for the devices in $dist"
# Only the binaries of this version stay: a device must not get an older one.
install -d -o coop -g coop -m 755 "$dist"
rm -f "$dist"/coop-*
for f in "$(dirname "$bin")"/coop-*-*; do
  if [ -f "$f" ]; then
    install -o coop -g coop -m 755 "$f" "$dist/"
    basename "$f"
  fi
done

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
   On that machine, as the user that runs the agents, one time:
     curl -fsSL <hub address>/install.sh | sh -s -- <hub address> laptop.<secret>
   A later version of coop comes with: coop upgrade
   (With a self-signed certificate, curl refuses the hub: see deploy/README.md, Tokens.)
Run the admin commands as the coop user, so that the database files keep that owner.
TEXT
