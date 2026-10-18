#!/bin/bash
# Serve a certificate the client can actually verify against an IP address.
#
# TLS SNI may not carry an IP literal (RFC 6066), so a browser or curl
# connecting to https://34.14.221.80 sends no server name at all. Caddy's
# `tls internal` then has no named site to match and fails the handshake --
# which is exactly why this worked from localhost inside the VM, where the
# name "localhost" is a valid SNI, and from nowhere else.
#
# An explicit certificate with the IP in its subjectAltName is served for
# every connection regardless of SNI, which is what an address-only
# deployment needs. It is self-signed, so a browser warns once; pointing a
# real domain at this VM would let Caddy get a publicly trusted certificate
# on its own and the warning would go away.
set -eu
cd /opt/esurvey
mkdir -p tls

openssl req -x509 -newkey rsa:2048 -nodes -days 825 \
  -keyout tls/key.pem -out tls/cert.pem \
  -subj "/CN=34.14.221.80/O=eSurvey" \
  -addext "subjectAltName=IP:34.14.221.80,DNS:localhost" 2>/dev/null

cat > Caddyfile <<'CADDY'
{
	auto_https off
}

:8443 {
	tls /etc/caddy/tls/cert.pem /etc/caddy/tls/key.pem
	reverse_proxy api:8090
}

:3443 {
	tls /etc/caddy/tls/cert.pem /etc/caddy/tls/key.pem
	root * /srv/console
	try_files {path} {path}/index.html {path}.html /index.html
	file_server
}
CADDY

python3 - <<'PY'
import io
p = "docker-compose.override.yml"
s = io.open(p, encoding="utf-8").read()
if "tls:/etc/caddy/tls" not in s:
    s = s.replace(
        "      - ./Caddyfile:/etc/caddy/Caddyfile:ro",
        "      - ./Caddyfile:/etc/caddy/Caddyfile:ro\n      - ./tls:/etc/caddy/tls:ro",
    )
io.open(p, "w", encoding="utf-8", newline="\n").write(s)
print("caddy now mounts the certificate")
PY

docker compose up -d caddy 2>&1 | tail -3
