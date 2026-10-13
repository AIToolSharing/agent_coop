#!/usr/bin/env bash
# Put TLS in front of the hub with a self-signed certificate, on the nginx of this host
# (Debian layout: sites-available, sites-enabled). install.sh runs it; by hand, as root:
#   sudo /usr/local/lib/coop/tls-selfsigned.sh [port]      (default port 8443)
# The hub listens on 127.0.0.1:8090 (deploy/coop.service); every client comes through TLS.
# Clients trust the certificate by its fingerprint: `coop login https://<host>:<port> <secret>`
# shows the fingerprint and pins it. Safe to run again: it keeps an existing certificate.
# Stdout is the fingerprint, and nothing else: install.sh puts it into the join line.
set -euo pipefail
port=${1:-8443}
tls=/etc/coop/tls
here=$(cd "$(dirname "$0")" && pwd)
command -v nginx >/dev/null || { echo "nginx is not installed" >&2; exit 1; }
ip=$(hostname -I | awk '{print $1}')
install -d -m 755 /etc/coop
install -d -m 700 "$tls"
if [ ! -f "$tls/cert.pem" ]; then
  openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -nodes -days 3650 \
    -subj "/CN=coop hub" -addext "subjectAltName=IP:$ip,DNS:$(hostname)" \
    -keyout "$tls/key.pem" -out "$tls/cert.pem" 2>/dev/null
  chmod 600 "$tls/key.pem"
  echo "made $tls/cert.pem (valid 10 years)" >&2
fi
sed "s/__PORT__/$port/g" "$here/nginx-coop.conf" > /etc/nginx/sites-available/coop
ln -sf /etc/nginx/sites-available/coop /etc/nginx/sites-enabled/coop
nginx -t >&2
systemctl reload nginx
echo "TLS in front of the hub: https://$ip:$port" >&2
openssl x509 -in "$tls/cert.pem" -noout -fingerprint -sha256 | cut -d= -f2
