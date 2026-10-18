#!/usr/bin/env bash
# Does the radar module actually observe anything?
#
# Run this after `gcloud auth application-default login`, and again whenever
# radar findings look wrong. It walks the whole chain rather than checking one
# link, because every link has failed at least once in development and each
# failure looks the same from the console: a report with no radar evidence.
#
#   1. Application Default Credentials exist on the host
#   2. the container can read them at the path the library is told to use
#   3. Earth Engine initialises for the registered project
#   4. it will do a round trip -- an initialised client that cannot compute is
#      exactly the state an unregistered project is in, and it is silent
#   5. a real Sentinel-1 series comes back for a real parcel, unlabelled
#
# Exits non-zero on the first step that fails, naming what to do about it.
set -uo pipefail

cd "$(dirname "$0")/.."

ADC="${GOOGLE_ADC_FILE:-}"
[ -z "$ADC" ] && ADC=$(grep -E '^GOOGLE_ADC_FILE=' .env 2>/dev/null | cut -d= -f2-)
PROJECT=$(grep -E '^ESURVEY_SAR_EE_PROJECT=' .env 2>/dev/null | cut -d= -f2-)
TOKEN=$(grep -E '^ESURVEY_INTERNAL_TOKEN=' .env 2>/dev/null | cut -d= -f2-)
PORT="${SAR_PORT:-8200}"

fail() { printf '\n  FAILED: %s\n\n  %s\n\n' "$1" "$2"; exit 1; }
ok()   { printf '  ok    %s\n' "$1"; }

printf '\nEarth Engine check — project %s\n\n' "${PROJECT:-<unset>}"

# --- 1. credentials on the host ---------------------------------------------
[ -n "$PROJECT" ] || fail "no project configured" \
  "Set ESURVEY_SAR_EE_PROJECT in .env"
[ -n "$ADC" ] && [ -f "$ADC" ] || fail "no Application Default Credentials" \
  "Run:  gcloud auth application-default login
  Then: gcloud auth application-default set-quota-project $PROJECT

  The file lands at %APPDATA%\\gcloud\\application_default_credentials.json and
  GOOGLE_ADC_FILE in .env must point at it. Do not restart the radar service
  before it exists: Docker creates a directory where it expects a file, and
  the mount then never works until that directory is removed."
ok "credentials present on the host"

# --- 2. the container can read them -----------------------------------------
# A directory here is the footgun above having already happened.
kind=$(docker compose exec -T sar python -c "
import os,sys
p='/var/run/gcp/adc.json'
print('missing' if not os.path.exists(p) else 'dir' if os.path.isdir(p) else 'file')
" 2>/dev/null | tr -d '\r')
case "$kind" in
  file) ok "the container reads them at /var/run/gcp/adc.json" ;;
  dir)  fail "/var/run/gcp/adc.json is a directory inside the container" \
        "Docker created it because the host file did not exist when the
  service started. Remove it and bring the service back up:
      docker compose down sar && docker compose up -d sar" ;;
  *)    fail "the container cannot see the credentials" \
        "Check GOOGLE_ADC_FILE in .env, then: docker compose up -d sar" ;;
esac

# --- 3, 4. initialise, and prove the round trip ------------------------------
ready=$(curl -s --max-time 30 "http://localhost:$PORT/readyz")
echo "$ready" | grep -q '"backend":"earth-engine"' || fail \
  "the radar module is not on Earth Engine" \
  "readyz says: $ready
  Set ESURVEY_SAR_SYNTHETIC=0 and ESURVEY_SAR_EE_PROJECT=$PROJECT, then:
      docker compose up -d sar"
ok "Earth Engine initialised and answering"

# --- 5. a real series for a real parcel --------------------------------------
# Barshi, Solapur. Roughly one Sentinel-1 pixel neighbourhood.
body='{"geometry":{"type":"Polygon","coordinates":[[[75.808,18.166],[75.818,18.166],[75.818,18.174],[75.808,18.174],[75.808,18.166]]]},"start_date":"2026-07-01","end_date":"2026-08-25"}'
task=$(curl -s --max-time 30 -X POST "http://localhost:$PORT/api/v1/sar/submit" \
  -H "authorization: Bearer $TOKEN" -H 'content-type: application/json' \
  -d "$body" | sed -n 's/.*"task_id":"\([^"]*\)".*/\1/p')
[ -n "$task" ] || fail "the radar service refused a submission" \
  "Check the token in .env matches ESURVEY_SAR_CALLER_TOKEN"

for _ in $(seq 1 40); do
  res=$(curl -s --max-time 30 "http://localhost:$PORT/api/v1/sar/status/$task" \
    -H "authorization: Bearer $TOKEN")
  case "$res" in
    *COMPLETED*) break ;;
    *FAILED*)    fail "the series could not be built" "$res" ;;
  esac
  sleep 5
done

echo "$res" | grep -q 'SYNTHETIC' && fail \
  "the series came back labelled SYNTHETIC" \
  "Earth Engine is configured but generated data is still being returned.
  Set ESURVEY_SAR_SYNTHETIC=0 and restart:  docker compose up -d sar"
echo "$res" | grep -q '"vv_db"' || fail "the series is empty" "$res"

n=$(echo "$res" | grep -o '"date"' | wc -l | tr -d ' ')
ok "a real Sentinel-1 series came back: $n passes"
printf '\n  Radar is observing. Every reading from here is an observation, not a\n  generated series, and the dossier will stop marking it as one.\n\n'
