#!/bin/bash
# Put the rebuilt console and the evidence files in place.
#
# The console is a static export, so its API base is baked in at build time.
# The copy shipped with the repo was built against http://localhost:8090 and
# would have sent every officer's browser to their own machine; this one is
# built against the VM.
set -eu
cd /opt/esurvey

# --- console ---
rm -rf web-platform/out
tar xzf console.tgz -C web-platform
rm -f console.tgz
echo "console files: $(find web-platform/out -type f | wc -l)"

# --- evidence ---
# Written into the volume the API reads, through a throwaway container: the
# volume is the API's, and unpacking from the host would need to know where
# Docker keeps it.
docker run --rm -v esurvey_evidence:/e -v /opt/esurvey:/in alpine \
  sh -c 'cd /e && tar xzf /in/evidence.tgz && find /e -type f | wc -l'
rm -f evidence.tgz

docker compose restart caddy >/dev/null 2>&1
sleep 5
echo "--- serving ---"
curl -sk -o /dev/null -w "  console %{http_code}\n" --max-time 10 https://localhost:3100/
curl -sk -o /dev/null -w "  api     %{http_code}\n" --max-time 10 https://localhost:8090/healthz
