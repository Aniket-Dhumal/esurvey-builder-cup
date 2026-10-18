#!/bin/bash
# Replace the VM's freshly migrated database with the local one.
#
# The dump carries the accounts with their argon2id password hashes, so every
# credential that works locally works here without anyone retyping a password
# -- which is the point. It also carries the 45,306 jurisdiction rows, the
# reports, the events and the assessments.
#
# The API is stopped first. A restore that drops the schema underneath a
# running service produces a stream of errors that look like corruption and
# are only a race.
set -eu
cd /opt/esurvey

docker compose stop api >/dev/null 2>&1

# Drop and recreate rather than restoring over the migrated schema: the dump
# creates every table it contains, and doing that on top of existing ones
# fails halfway and leaves a database that is neither.
docker compose exec -T postgres psql -U esurvey -d postgres -q \
  -c "DROP DATABASE IF EXISTS esurvey WITH (FORCE);" \
  -c "CREATE DATABASE esurvey OWNER esurvey;"

gunzip -c dbdump.sql.gz | docker compose exec -T postgres psql -U esurvey -d esurvey -q -v ON_ERROR_STOP=0 >/tmp/restore.log 2>&1 || true
echo "--- restore complete, errors (if any): ---"
grep -ci "^ERROR" /tmp/restore.log || echo 0

docker compose start api >/dev/null 2>&1
sleep 10

echo "--- what landed ---"
docker compose exec -T postgres psql -U esurvey -d esurvey -tAc \
  "select 'users '||(select count(*) from users)
   ||' | reports '||(select count(*) from crop_loss_reports)
   ||' | events '||(select count(*) from survey_events)
   ||' | jurisdictions '||(select count(*) from jurisdictions)
   ||' | assessments '||(select count(*) from assessments);"

echo "--- officers now on this deployment ---"
docker compose exec -T postgres psql -U esurvey -d esurvey -tAc \
  "select u.username||'  ['||ur.role||']' from users u
   join user_roles ur on ur.user_id=u.id and ur.revoked_at is null
   where u.kind='officer' order by ur.role limit 12;"
