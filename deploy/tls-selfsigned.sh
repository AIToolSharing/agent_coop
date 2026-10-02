#!/usr/bin/env bash
# Put TLS in front of the hub with a self-signed certificate, on the nginx of this host
# (Debian layout: sites-available, sites-enabled). Run as root on the hub host:
#   sudo deploy/tls-selfsigned.sh [port]      (default port 8443)
# After it, the hub listens on 127.0.0.1:8090 only, and every client comes through TLS.
# Clients trust the certificate by its fingerprint: `coop login https://<host>:<port> <token>`
# shows the fingerprint and pins it. Compare it with the one this script prints.
# Safe to run again: it keeps an existing certificate.
set -euo pipefail
port=${1:-8443}
tls=/etc/coop/tls
here=$(cd "$(dirname "$0")" && pwd)
command -v nginx >/dev/null || { echo "nginx is not installed" >&2; exit 1; }
[ -f /etc/coop/hub.env ] || { echo "/etc/coop/hub.env is missing: run deploy/install.sh first" >&2; exit 1; }
ip=$(hostname -I | awk '{print $1}')
install -d -m 700 "$tls"
if [ ! -f "$tls/cert.pem" ]; then
  openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -nodes -days 3650 \
    -subj "/CN=coop hub" -addext "subjectAltName=IP:$ip,DNS:$(hostname)" \
    -keyout "$tls/key.pem" -out "$tls/cert.pem" 2>/dev/null
  chmod 600 "$tls/key.pem"
  echo "made $tls/cert.pem (valid 10 years)"
fi
sed "s/__PORT__/$port/g" "$here/nginx-coop.conf" > /etc/nginx/sites-available/coop
ln -sf /etc/nginx/sites-available/coop /etc/nginx/sites-enabled/coop
nginx -t
systemctl reload nginx
# The hub listens on the loopback only from now on.
if grep -q '^COOP_HUB_LISTEN=' /etc/coop/hub.env; then
  sed -i 's/^COOP_HUB_LISTEN=.*/COOP_HUB_LISTEN=127.0.0.1:8090/' /etc/coop/hub.env
else
  echo 'COOP_HUB_LISTEN=127.0.0.1:8090' >> /etc/coop/hub.env
fi
systemctl restart coop-hub
echo "hub: https://$ip:$port"
echo "certificate fingerprint (coop login shows the same one on each client):"
openssl x509 -in "$tls/cert.pem" -noout -fingerprint -sha256
