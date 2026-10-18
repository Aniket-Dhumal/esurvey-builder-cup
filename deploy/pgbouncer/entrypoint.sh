#!/bin/sh
# PgBouncer in front of the primary and the replica, in transaction mode.
#
# The configuration is written here from the environment rather than baked
# into a file, so the password lives only in the environment the rest of the
# stack already reads it from.
#
#   esurvey       -> the primary      (writes, and reads that must see them)
#   esurvey_read  -> the read replica (dashboards, audit search, listings)
#
# Transaction mode shares a server connection between clients one transaction
# at a time, which is what lets hundreds of API connections sit on tens of
# database ones. It is also why the API sends no session settings on these
# connections (ESURVEY_DATABASE_POOLED) and keeps a small direct pool for
# migrations and the audit chain. max_prepared_statements lets pgx's statement
# cache work across that sharing (PgBouncer 1.21 and later).
set -eu

DIR=/tmp/pgbouncer
mkdir -p "$DIR"
umask 077

printf '"%s" "%s"
' "$DB_USER" "$DB_PASSWORD" > "$DIR/userlist.txt"

cat > "$DIR/pgbouncer.ini" <<EOF
[databases]
${DB_NAME} = host=${PRIMARY_HOST} port=5432 dbname=${DB_NAME}
${DB_NAME}_read = host=${REPLICA_HOST} port=5432 dbname=${DB_NAME}
; Any other database name goes to the primary under its own name -- the test
; database, for one, so the suites can be run through the pooler.
* = host=${PRIMARY_HOST} port=5432

[pgbouncer]
listen_addr = 0.0.0.0
listen_port = 6432
unix_socket_dir =
auth_type = scram-sha-256
auth_file = $DIR/userlist.txt

pool_mode = transaction
max_client_conn = ${MAX_CLIENT_CONN:-2000}
default_pool_size = ${DEFAULT_POOL_SIZE:-40}
reserve_pool_size = 5
reserve_pool_timeout = 3
max_db_connections = ${MAX_DB_CONNECTIONS:-60}
max_prepared_statements = 200

server_lifetime = 3600
server_idle_timeout = 600
query_wait_timeout = 30
client_idle_timeout = 0

log_connections = 0
log_disconnections = 0
stats_period = 300
EOF

exec pgbouncer "$DIR/pgbouncer.ini"
