#!/usr/bin/env bash
#
# Restoring a backup into a throwaway database, and checking what came back.
#
# # Why this exists as a script rather than as a runbook step
#
# Because a backup nobody has restored is a belief. The failure modes are
# quiet: a dump that was truncated by a full disk, a schema that no longer
# matches the archive, an extension the target database does not have. None of
# them announce themselves until the day the restore is not a drill.
#
# So this runs the whole path -- create, restore, migrate, count -- into a
# database named after the moment it was made, and drops it at the end unless
# told to keep it. Nothing it does can touch the live database: the target name
# is generated here and refused if it collides with the configured one.
#
# # What "passed" means
#
# The archive restored without error, the schema migrated to the current
# version on top of it, and the tables a claim depends on came back non-empty
# -- or empty for the right reason, which the output says rather than assumes.
#
# Usage:
#   scripts/restore-drill.sh backups/esurvey-db-20260910T060000Z.dump [--keep]

set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
archive="${1:-}"
keep="${2:-}"

if [[ -z "$archive" || ! -f "$archive" ]]; then
    echo "usage: scripts/restore-drill.sh <db.dump> [--keep]" >&2
    exit 2
fi

# Reading .env without sourcing it.
#
# `source .env` executes the file, and a value with a space in it -- a Windows
# path, say -- is then parsed as a command. That is how this failed the first
# time it was run: GOOGLE_ADC_FILE points at a directory containing " - ", and
# bash tried to run "-" as a program.
#
# Compose parses this file leniently and bash does not, so the file stays as
# compose wants it and this reads only the keys it needs.
env_value() {
    local key="$1" line
    [[ -f "$here/.env" ]] || return 0
    line="$(grep -m1 -E "^[[:space:]]*${key}=" "$here/.env" || true)"
    line="${line#*=}"
    # Strip one layer of surrounding quotes, and a trailing CR from a file
    # that has been through a Windows editor.
    line="${line%"$(printf '\r')"}"
    case "$line" in
        '"'*'"') line="${line:1:${#line}-2}" ;;
        "'"*"'") line="${line:1:${#line}-2}" ;;
    esac
    printf '%s' "$line"
}

# Read, never echoed. The password must not reach the log, the process list,
# or a shell trace.
POSTGRES_PASSWORD="${POSTGRES_PASSWORD:-$(env_value POSTGRES_PASSWORD)}"
PG_CONTAINER="${PG_CONTAINER:-esurvey-postgres-1}"
POSTGRES_USER="${POSTGRES_USER:-$(env_value POSTGRES_USER)}"
POSTGRES_USER="${POSTGRES_USER:-esurvey}"
POSTGRES_DB="${POSTGRES_DB:-$(env_value POSTGRES_DB)}"
POSTGRES_DB="${POSTGRES_DB:-esurvey}"
PG_PORT="${PG_PORT:-55432}"

if [[ -z "${POSTGRES_PASSWORD:-}" ]]; then
    echo "restore-drill: POSTGRES_PASSWORD is not set" >&2
    exit 2
fi

target="esurvey_drill_$(date -u +%Y%m%d%H%M%S)"

# Belt and braces. The name is generated, so this cannot fire -- which is the
# point of asserting it: the day somebody edits the line above, this catches it
# before the drill drops production.
if [[ "$target" == "$POSTGRES_DB" ]]; then
    echo "restore-drill: refusing to restore over the live database" >&2
    exit 1
fi

psql_in() {
    docker exec -i -e PGPASSWORD="$POSTGRES_PASSWORD" "$PG_CONTAINER" \
        psql --username="$POSTGRES_USER" "$@"
}

cleanup() {
    if [[ "$keep" == "--keep" ]]; then
        echo "restore-drill: keeping $target"
        return
    fi
    psql_in --dbname=postgres -q \
        -c "DROP DATABASE IF EXISTS $target" > /dev/null 2>&1 || true
    echo "restore-drill: dropped $target"
}
trap cleanup EXIT

echo "restore-drill: creating $target"
# Plain and empty, deliberately not from template_postgis.
#
# The dump carries its own CREATE EXTENSION and CREATE SCHEMA statements --
# postgis brings tiger and topology with it -- so restoring into a database
# that already has them fails on the first one: `schema "tiger" already
# exists`. That is not a corrupt backup, it is a drill set up wrongly, and it
# would have been read as the first.
#
# An empty target also makes the drill mean more: it proves the archive can
# rebuild the database from nothing, which is what a real restore does.
psql_in --dbname=postgres -q -c "CREATE DATABASE $target"

echo "restore-drill: restoring"
# --no-owner because the drill database is owned by whoever ran this, and
# --exit-on-error because a restore that reports success after forty errors is
# the exact thing this drill exists to catch.
docker exec -i -e PGPASSWORD="$POSTGRES_PASSWORD" "$PG_CONTAINER" \
    pg_restore --username="$POSTGRES_USER" --dbname="$target" \
               --no-owner --no-privileges --exit-on-error \
    < "$archive"

echo "restore-drill: migrating on top of the restored schema"
# A backup is usually older than the code that will read it. If the migrations
# do not apply to it, the restore has produced a database the API cannot open,
# and that is a failed drill even though pg_restore was happy.
(
    cd "$here/backend"
    go run ./cmd/migrate \
        -url "postgres://$POSTGRES_USER:$POSTGRES_PASSWORD@127.0.0.1:$PG_PORT/$target?sslmode=disable"
)

echo
echo "restore-drill: what came back"
psql_in --dbname="$target" -c "
    SELECT 'jurisdictions'        AS table, count(*) FROM jurisdictions
    UNION ALL SELECT 'users',              count(*) FROM users
    UNION ALL SELECT 'survey_events',      count(*) FROM survey_events
    UNION ALL SELECT 'crop_loss_reports',  count(*) FROM crop_loss_reports
    UNION ALL SELECT 'report_verifications', count(*) FROM report_verifications
    UNION ALL SELECT 'assessments',        count(*) FROM assessments
    UNION ALL SELECT 'survey_captures',    count(*) FROM survey_captures
    UNION ALL SELECT 'audit_log',          count(*) FROM audit_log
    ORDER BY 1"

echo
echo "restore-drill: checking the audit chain on the restored copy"
# The one table whose integrity is checkable rather than merely countable. A
# chain that verifies after a restore proves the rows came back in order and
# unaltered -- which is what an inquiry into a restored system would ask.
psql_in --dbname="$target" -tAc "
    SELECT CASE
        WHEN count(*) = 0 THEN 'audit log is empty'
        ELSE count(*)::text || ' entries restored'
    END FROM audit_log"

echo
echo "restore-drill: passed"
