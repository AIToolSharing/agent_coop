#!/usr/bin/env bash
# Install the coop hub on one Linux host with systemd: the binary, the `coop` user, the data
# directory, one unit, and the release binaries that the hub gives to its devices. Then it
# makes the hub reachable (tailscale serve on a tailnet, else TLS on nginx) and prints the
# join line: the one line that each machine runs. Run it again for an upgrade.
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
# On a LAN with neither tailscale nor nginx, COOP_HOST=<LAN address of this host> makes the
# hub listen on that address with plain http: `curl ... | sudo COOP_HOST=192.168.1.31 bash`.
#
# `make hub HOST=<ssh host>` copies the files of a build to a host and runs this script there.
set -euo pipefail

unit_dir=/etc/systemd/system
data=/var/lib/coop
dist=$data/dist
tlsdir=/usr/local/lib/coop
release_dl=https://github.com/AIToolSharing/agent_coop/releases/latest/download
here=$(cd "$(dirname "$0")" && pwd)
listen=${COOP_HOST:+$COOP_HOST:8090}
listen=${listen:-127.0.0.1:8090}

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
# The TLS files stay in $tlsdir: the files next to this script can go when it ends.
install -d -m 755 "$tlsdir"
install -m 755 "$here/tls-selfsigned.sh" "$tlsdir/"
install -m 644 "$here/nginx-coop.conf" "$tlsdir/"

step "data in $data"
install -d -o coop -g coop -m 750 "$data"
# A hub from before the unit set UMask=0077 left the database files readable by every user.
find "$data" -maxdepth 1 -type f -name 'coop.db*' -exec chmod 600 {} +

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
sed "s|--listen 127.0.0.1:8090|--listen $listen|" "$here/coop.service" > "$unit_dir/coop.service"
chmod 644 "$unit_dir/coop.service"
systemctl daemon-reload
systemctl enable --now coop
systemctl restart coop
sleep 1
systemctl --no-pager --quiet is-active coop && echo "coop is active on $listen"

if systemctl --quiet is-enabled coop-hub 2>/dev/null || systemctl --quiet is-enabled nats 2>/dev/null; then
  step "the Node hub is still installed"
  echo "coop-hub and nats are the old server. See deploy/README.md, Migrate, to stop them."
fi

step "the secret"
# The hub makes $data/secret at its first start. Every machine presents it.
secret=
for _ in 1 2 3 4 5 6 7 8 9 10; do
  if [ -s "$data/secret" ]; then
    secret=$(tr -d '\n' < "$data/secret")
    break
  fi
  sleep 0.5
done
if [ -z "$secret" ]; then
  echo "the hub made no secret in $data/secret: see journalctl -u coop" >&2
  exit 1
fi
echo "the secret is in $data/secret"

step "reach"
# A tailnet needs no TLS: tailscale serve puts the hub on it. Else nginx with a self-signed
# certificate, which the clients pin by its fingerprint. Any other reverse proxy works too:
# proxy to 127.0.0.1:8090 with buffering off.
if [ -n "${COOP_HOST:-}" ]; then
  url=http://$listen
  join="curl -fsSL $url/install.sh | sh -s -- $url $secret"
  echo "the hub is on the LAN: $url"
elif command -v tailscale >/dev/null && tailscale status >/dev/null 2>&1; then
  tailscale serve --bg --tcp 8090 tcp://127.0.0.1:8090 >/dev/null
  host=$(tailscale status --json | sed -n 's/^ *"DNSName": *"\([^"]*\)\.",*$/\1/p' | head -1)
  [ -n "$host" ] || host=$(tailscale ip -4 | head -1)
  url=http://$host:8090
  join="curl -fsSL $url/install.sh | sh -s -- $url $secret"
  echo "the hub is on the tailnet: $url"
elif command -v nginx >/dev/null; then
  fp=$("$tlsdir/tls-selfsigned.sh")
  ip=$(hostname -I | awk '{print $1}')
  url=https://$ip:8443
  # curl does not trust the certificate: the script and the binary come from GitHub, and coop
  # login sends the secret only to the certificate with this fingerprint.
  join="curl -fsSL $release_dl/device.sh | COOP_CERT_SHA256=$fp sh -s -- $url $secret"
  echo "the hub is behind TLS: $url, certificate fingerprint $fp"
else
  join="curl -fsSL <hub>/install.sh | sh -s -- <hub> $secret"
  cat <<TEXT
Neither tailscale nor nginx is on this host, so the hub is on 127.0.0.1:8090 only. Do one:
  a tailnet: install tailscale, log in, and run this install line again
  TLS:       install nginx, and run this install line again
  a LAN:     run this install line again with COOP_HOST=<the LAN address of this host>
Then put the hub address into the join line in place of <hub>.
TEXT
fi
install -d -m 755 /etc/coop
printf '%s\n' "$join" > /etc/coop/join
chmod 600 /etc/coop/join

step "the join line"
cat <<TEXT
On each machine, as the user that runs the agents (on Windows in Git Bash), one time:

  $join

Then, on that machine:  cd <project> && coop claude     (the agent)
                        coop tui                        (the sessions)
                        coop upgrade                    (a later version)
The line again:  sudo cat /etc/coop/join
A new secret:    sudo rm $data/secret /etc/coop/join; sudo systemctl restart coop; run this install line again
TEXT
