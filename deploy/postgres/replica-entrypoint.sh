#!/bin/sh
# A streaming read replica of the compose primary.
#
# On an empty volume: wait for the primary, copy it with pg_basebackup, and
# write the standby configuration (-R). A replication slot keeps the primary
# from discarding WAL the replica has not received yet; the primary caps how
# much it will keep for a slot (max_slot_wal_keep_size), so a replica left
# stopped for days is invalidated rather than filling the primary's disk.
#
# On a volume that already holds a replica, this does nothing but start it.
set -eu

SLOT="${REPLICA_SLOT:-esurvey_replica_1}"

if [ ! -s "$PGDATA/PG_VERSION" ]; then
  echo "replica: no data directory; cloning the primary"
  until pg_isready -h "$PRIMARY_HOST" -U "$POSTGRES_USER" >/dev/null 2>&1; do
    sleep 1
  done

  export PGPASSWORD="$POSTGRES_PASSWORD"
  # A slot left behind by an earlier replica volume would make -C fail.
  psql -h "$PRIMARY_HOST" -U "$POSTGRES_USER" -d postgres -v ON_ERROR_STOP=1 -qc     "SELECT pg_drop_replication_slot(slot_name) FROM pg_replication_slots
      WHERE slot_name = '$SLOT' AND NOT active" >/dev/null

  mkdir -p "$PGDATA"
  chown -R postgres:postgres "$PGDATA"
  chmod 700 "$PGDATA"
  gosu postgres env PGPASSWORD="$POSTGRES_PASSWORD" pg_basebackup     -h "$PRIMARY_HOST" -U "$POSTGRES_USER" -D "$PGDATA"     -X stream -R -C -S "$SLOT" --checkpoint=fast --progress
  echo "replica: cloned; starting as a standby"
fi

exec docker-entrypoint.sh "$@"
