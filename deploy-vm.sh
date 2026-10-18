set -eux
cd /opt/esurvey
tar xzf esurvey.tgz && rm -f esurvey.tgz

# --- Caddy in front -----------------------------------------------------
# The API speaks plaintext HTTP and hands out bearer tokens and identified
# farmers' land and photographs. It is never published directly. Caddy
# terminates TLS and the API stays on loopback, which is also what lets the
# stack run as `staging` rather than in development mode: a hardened
# environment refuses to start without either a certificate or a declared
# proxy in front of it.
#
# `tls internal` issues a self-signed certificate, because the VM has an IP
# and no domain and a public CA will not sign for an address. Encrypted, and
# a browser will warn once. A real deployment points a name at this and Caddy
# gets a public certificate on its own.
cat > Caddyfile <<'CADDY'
{
	auto_https disable_redirects
}

:443 {
	tls internal

	handle /api/* {
		reverse_proxy api:8090
	}
	handle /healthz {
		reverse_proxy api:8090
	}
	handle {
		root * /srv/console
		try_files {path} {path}/ {path}.html /index.html
		file_server
	}
}
CADDY

cat > docker-compose.override.yml <<'OVR'
services:
  caddy:
    image: caddy:2-alpine
    restart: unless-stopped
    depends_on: [api]
    ports:
      - "8090:443"
      - "3100:443"
    volumes:
      - ./Caddyfile:/etc/caddy/Caddyfile:ro
      - ./web-platform/out:/srv/console:ro
      - caddy-data:/data
      - caddy-config:/config
volumes:
  caddy-data:
  caddy-config:
OVR

# --- secrets, generated here and never in the repository ----------------
rnd() { head -c "$1" /dev/urandom | base64 -w0 | tr -d '=+/' | cut -c1-"$2"; }
cat > .env <<ENV
ESURVEY_ENV=staging
ESURVEY_TRUSTED_PROXY_HOPS=1

POSTGRES_PASSWORD=$(rnd 48 40)
REDIS_PASSWORD=$(rnd 48 40)
ESURVEY_JWT_SEED=$(head -c 32 /dev/urandom | base64 -w0)
ESURVEY_AUDIT_CHAIN_SEED=$(rnd 48 40)
ESURVEY_INTERNAL_TOKEN=$(rnd 48 40)

ESURVEY_BOOTSTRAP_ADMIN_USERNAME=admin
ESURVEY_BOOTSTRAP_ADMIN_PASSWORD=$(rnd 32 24)!Aa1

ESURVEY_CORS_ORIGINS=https://34.14.221.80:3100,https://34.14.221.80:8090
ESURVEY_CONSOLE_URL=https://34.14.221.80:3100

ESURVEY_SAR_EE_PROJECT=esurvey-501512
ESURVEY_SAR_SYNTHETIC=0
ESURVEY_VISION_BASELINE=0
ENV
chmod 600 .env
echo "--- generated, admin password ---"
grep BOOTSTRAP_ADMIN_PASSWORD .env
