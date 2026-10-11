#!/usr/bin/env bash
# Install the coop hub on one Linux host with systemd: the binary, the `coop` user, the data
# directory, one unit, and the release binaries that the hub gives to its devices. Run it
# again for an upgrade. TLS and tokens stay manual steps; it prints them.
#
#   curl -fsSL https://github.com/AIToolSharing/agent_coop/releases/latest/download/install.sh | sudo bash
#   deploy/install.sh [<coop binary>]
#
# <coop binary> is the release binary for this host (default: coop-linux-<arch> next to this
# script). Each coop-<os>-<arch> file in the directory of that binary goes to
# /var/lib/coop/dist. A device downloads the one for its platform from the hub: with the
# install line that this script prints, and later with `coop upgrade`.
#
# With no binary (the curl line), the script first downloads the files of a release from
# GitHub: the latest one, or the version in COOP_VERSION (for example v0.15.0).
# COOP_RELEASE_URL names another place that has the same files.
#
# `make hub HOST=<ssh host>` copies the files of a build to a host and runs this script there.
set -euo pipefail

unit_dir=/etc/systemd/system
dist=/var/lib/coop/dist
here=$(cd "$(dirname "$0")" && pwd)

if [ "$(id -u)" -ne 0 ]; then
  # From a pipe there is no file to run again with sudo.
  if [ ! -f "$0" ] || ! command -v sudo >/dev/null; then
    echo "run as root: curl ... | sudo bash, or sudo deploy/install.sh [<coop binary>]" >&2
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
release=""
if [ $# -eq 0 ] && [ ! -f "$bin" ]; then
  if [ -n "${COOP_VERSION:-}" ]; then
    release=https://github.com/AIToolSharing/agent_coop/releases/download/$COOP_VERSION
  else
    release=https://github.com/AIToolSharing/agent_coop/releases/latest/download
  fi
  release=${COOP_RELEASE_URL:-$release}
  command -v curl >/dev/null || { echo "missing: curl" >&2; exit 2; }
  here=$(mktemp -d)
  trap 'rm -rf "$here"' EXIT
  echo "download the release from $release"
  for f in coop-linux-amd64 coop-linux-arm64 coop-darwin-arm64 coop-windows-amd64.exe coop.service tls-selfsigned.sh nginx-coop.conf; do
    curl -fsSL -o "$here/$f" "$release/$f"
  done
  bin=$here/coop-linux-$arch
fi
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
# The TLS script stays: the downloaded files go when this script ends.
tlsdir=$here
if [ -n "$release" ]; then
  tlsdir=/usr/local/lib/coop
  install -d -m 755 "$tlsdir"
  install -m 755 "$here/tls-selfsigned.sh" "$tlsdir/"
  install -m 644 "$here/nginx-coop.conf" "$tlsdir/"
fi

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
1. Make the hub reachable. On a tailnet, one line, and the hub address is http://<this host>:8090:
     tailscale serve --bg --tcp 8090 tcp://127.0.0.1:8090
   Else TLS in front of the hub, with a self-signed certificate on this host's nginx:
     sudo $tlsdir/tls-selfsigned.sh          # port 8443; prints the certificate fingerprint
   (Any other reverse proxy works too: proxy to 127.0.0.1:8090 with buffering off.)
2. A token for root on your own machine; --watch lets it show all sessions (it shows one time):
     sudo -u coop coop admin token add --watch console
     on your machine: sudo -H /usr/local/libexec/coop login https://<this host>:8443 console.<secret>
     on your machine: sudo -H /usr/local/libexec/coop tui
3. One token per agent machine:
     sudo -u coop coop admin token add laptop
   On that machine, as the user that runs the agents, one time:
     curl -fsSL <hub address>/install.sh | sh -s -- <hub address> laptop.<secret>
   A later version of coop comes with: coop upgrade
   (With a self-signed certificate, curl refuses the hub: see deploy/README.md, Tokens.)
Run the admin commands as the coop user, so that the database files keep that owner.
TEXT
