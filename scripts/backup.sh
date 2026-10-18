#!/usr/bin/env bash
#
# A backup of everything a claim rests on.
#
# # What it takes, and why both halves
#
# The database and the evidence volume. Either alone is useless: the database
# without the photographs is a set of decisions nobody can now justify, and the
# photographs without the database are a directory of unnamed images.
#
# The audit log's hash chain makes tampering detectable and does nothing at all
# about loss. That is what this is for.
#
# # Why a dump and not a filesystem snapshot
#
# A snapshot of a running Postgres data directory is a crash-consistent copy
# that usually restores and sometimes does not, and which cannot be restored
# into a different major version. pg_dump produces something that restores into
# an empty database of any supported version, which is what a restore drill and
# a migration both need.
#
# # Why it verifies before it reports success
#
# Because an untested backup is a belief, not a backup. The archive is listed
# back with pg_restore, which parses the whole custom-format file, so a dump
# truncated by a full disk fails here rather than during the emergency.
#
# Usage:
#   scripts/backup.sh [destination]
#
# Environment:
#   POSTGRES_USER, POSTGRES_PASSWORD, POSTGRES_DB   read from .env if present
#   BACKUP_KEEP_DAYS   how many days of archives to keep; default 14
#   PG_CONTAINER       compose service or container; default esurvey-postgres-1
#   EVIDENCE_DIR       what to archive alongside; default ./evidence

set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
dest="${1:-$here/backups}"

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
EVIDENCE_DIR="${EVIDENCE_DIR:-$here/evidence}"
BACKUP_KEEP_DAYS="${BACKUP_KEEP_DAYS:-14}"

if [[ -z "${POSTGRES_PASSWORD:-}" ]]; then
    echo "backup: POSTGRES_PASSWORD is not set" >&2
    exit 2
fi

stamp="$(date -u +%Y%m%dT%H%M%SZ)"
mkdir -p "$dest"

db="$dest/esurvey-db-$stamp.dump"
ev="$dest/esurvey-evidence-$stamp.tar.gz"

echo "backup: dumping the database"
# --format=custom so pg_restore can list it, restore it selectively, and
# restore it in parallel. -Z0 because the evidence is already compressed and
# the database dump is compressed by the archive format itself.
docker exec -e PGPASSWORD="$POSTGRES_PASSWORD" "$PG_CONTAINER" \
    pg_dump --username="$POSTGRES_USER" --dbname="$POSTGRES_DB" \
            --format=custom --no-owner --no-privileges \
    > "$db"

echo "backup: verifying the dump is readable"
# Not a restore. --list parses the whole custom-format archive, so a dump
# truncated by a full disk or a killed container fails now rather than in an
# emergency.
#
# Through a temporary file inside the container, not a pipe: a custom-format
# archive has a table of contents that pg_restore seeks to, and /dev/stdin is
# not seekable. Piping it appears to work and silently verifies nothing --
# which is how the first version of this script reported a perfectly good
# 3.4 MB dump as corrupt.
verify_dump() {
    if command -v pg_restore > /dev/null 2>&1; then
        pg_restore --list "$1" > /dev/null
        return
    fi
    docker exec -i "$PG_CONTAINER" sh -c '
        tmp=$(mktemp) || exit 1
        cat > "$tmp"
        pg_restore --list "$tmp" > /dev/null
        status=$?
        rm -f "$tmp"
        exit $status
    ' < "$1"
}

if ! verify_dump "$db"; then
    echo "backup: the dump did not verify; keeping it for inspection" >&2
    exit 1
fi

if [[ -d "$EVIDENCE_DIR" ]]; then
    echo "backup: archiving the evidence volume"
    tar -czf "$ev" -C "$(dirname "$EVIDENCE_DIR")" "$(basename "$EVIDENCE_DIR")"
else
    # Not a failure. A deployment storing evidence in a bucket has no local
    # directory, and its object storage is backed up by its own lifecycle
    # rules -- but say so, because a silent skip here is how somebody
    # discovers the photographs were never in the backup.
    echo "backup: no evidence directory at $EVIDENCE_DIR (object storage?), skipping"
    ev=""
fi

# A checksum beside each file, so a restore can tell a corrupted archive from
# a bad restore. sha256sum is not on every machine; shasum is the fallback.
sums() {
    if command -v sha256sum > /dev/null; then sha256sum "$@"; else shasum -a 256 "$@"; fi
}
(cd "$dest" && sums "$(basename "$db")" ${ev:+"$(basename "$ev")"} \
    > "esurvey-$stamp.sha256")

echo "backup: pruning archives older than $BACKUP_KEEP_DAYS days"
find "$dest" -maxdepth 1 -name 'esurvey-*' -type f \
     -mtime "+$BACKUP_KEEP_DAYS" -print -delete

echo "backup: done"
ls -lh "$dest"/esurvey-*"$stamp"* 2>/dev/null || true

cat <<'NOTE'

Restore, and prove it:

    scripts/restore-drill.sh backups/esurvey-db-<stamp>.dump

That restores into a throwaway database and reports the row counts. A backup
nobody has restored is a belief, not a backup.
NOTE
