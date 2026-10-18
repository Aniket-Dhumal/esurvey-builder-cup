set -eux
cd /opt/esurvey
# On GCE the metadata server provides Application Default Credentials for the
# instance's service account. The SAR image hardcodes
# GOOGLE_APPLICATION_CREDENTIALS to a mounted file, which would point the
# library at a path that does not exist here; emptied so it falls back to the
# metadata server instead.
cat >> docker-compose.override.yml <<'OVR2'

  sar:
    environment:
      GOOGLE_APPLICATION_CREDENTIALS: ""
    volumes: !reset []
OVR2
docker compose build --parallel 2>&1 | tail -3
docker compose up -d 2>&1 | tail -20
