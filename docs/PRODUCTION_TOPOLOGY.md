# What production looks like

Written 2026-09-16, after the scale work. The platform now runs on one VM with
one of everything, which is right for a pilot and wrong for a state. This is
what the shape should be when it serves districts, what each piece has to be
true for, and what is already true today.

Nothing here is a code change. Every component below can already be scaled --
the coordination it needs is in Postgres or Redis, not in a process's memory.

---

## The shape

    farmers' handsets ─┐
                       ├─ load balancer (TLS) ─ API x N (stateless)
    officers' console ─┘                          │
                                                  ├── Postgres (primary + replica)
                                                  ├── Redis (sessions, codes, limits)
                                                  ├── evidence bucket (GCS)
                                                  ├── sar service x N ── Earth Engine
                                                  └── ai service x N ── model provider

### The API

Stateless, and now genuinely so. Everything that used to be per process is
shared:

| What | Where it coordinates | Since |
|---|---|---|
| Idempotency keys | Postgres | migration 0032 |
| Sessions, one-time codes, throttles, rate limits | Redis | -- |
| The phone's sync lease | the phone's own SQLite | -- |
| Rollup sweeper | Postgres, `FOR UPDATE SKIP LOCKED` | migration 0030/0034 |
| Notification dispatcher | Postgres, same | -- |
| Hourly housekeeping | a Redis lock, one replica runs it | 2026-09-15 |
| Assessment workers | Postgres queue, claim with lease | migration 0036 |

Size it by memory, not by CPU: assembling a station clip streams now, so an
API replica's working set is small, but 512 MiB is tight for one that is also
rendering dossiers. **1 GiB per replica, two replicas minimum** -- two so that
a deploy is not an outage.

### Postgres

One writer. Everything above is designed around that, and nothing here needs
multi-master.

- **Managed** (Cloud SQL or equivalent), not a container on the app VM: the
  evidence of every claim in the state is in it.
- **Read replicas for reads, the primary for writes.** The API routes only
  staleness-tolerant reads to `ESURVEY_DATABASE_READ_URL`: the overview and
  state-wide views, audit search, registry listings, device health, coverage
  and the administrator's counts. Sign-in, permissions, a claim's own pages and
  every write stay on the primary, because a replica a second behind would show
  an officer the claim they just approved as still waiting. The API checks the
  replica every 3 s and reads from the primary while it is unreachable or more
  than `ESURVEY_DB_REPLICA_MAX_LAG` behind on two checks running
  (`internal/platform/db/router.go`); the Health page says which it is using
  and why. Several replicas sit behind one read endpoint (the managed
  service's, or a pooler database per replica).
- **Four pools per API instance** (`internal/platform/db/pools.go`):

  | Pool | Goes to | Used by | Size |
  |---|---|---|---|
  | requests | pooler -> primary | API requests | `ESURVEY_DB_MAX_CONNS` (20) |
  | workers | pooler -> primary | pipeline, assessments, outbox, sweeps | `ESURVEY_DB_WORKER_MAX_CONNS` (10) |
  | replica | pooler -> replica | the reads above | `ESURVEY_DB_READ_MAX_CONNS` (20) |
  | direct | primary, unpooled | migrations, the audit chain | 4 |

  The workers have their own pool so a backlog after a calamity cannot take
  the connections a farmer's request is waiting for.
- **PgBouncer in transaction mode** in front of the primary and the replicas
  (`deploy/pgbouncer/entrypoint.sh`), with `ESURVEY_DATABASE_POOLED=true`.
  Three things follow, all handled: session settings cannot travel on pooled
  connections, so the statement and idle-transaction timeouts are set on the
  database role at startup; migrations need a session advisory lock and the
  audit chain's seed is a session setting, so both use the direct pool -- the
  seed is never stored on the role, where anyone reading the catalog could see
  it; and pgx's prepared-statement cache needs PgBouncer 1.21 or later with
  `max_prepared_statements` set. Role timeouts apply to server connections the
  pooler opens after startup; a pooler already running keeps older connections
  until `server_lifetime` (1 h).
- **Connection budget**: behind the pooler, API pool sizes are client
  connections and cheap; what reaches Postgres is PgBouncer's
  `max_db_connections` per database (60) plus 4 direct per API instance. Keep
  that under `max_connections` with room for a migration and an operator.
- **Backups**: point-in-time recovery on, and the restore rehearsed (see
  OPERATIONS.md). `audit_log` is partitioned by month, so a year can be
  archived and detached rather than deleted.

### Redis

Sessions, one-time codes, login throttles, rate-limit counters, the
housekeeping lock, and the satellite service's task state. Not a cache in the
disposable sense: losing it signs every farmer out and drops the codes people
are typing.

- `appendonly yes`, `appendfsync everysec` -- already set.
- **`noeviction`**, and sized so it is never reached: roughly 400 B per live
  session plus 180 B per refresh in the last 72 hours. The API warns past 75%.
- Managed Redis with persistence, or a container with a real volume and a
  restore plan.

### The evidence bucket

Required in production -- the API refuses to start without
`ESURVEY_EVIDENCE_BUCKET`, because a season is terabytes and a container volume
is not. Apply `deploy/evidence-lifecycle.json`: chunk parts swept after a
fortnight, evidence cooled to Nearline at 90 days and Coldline at a year,
nothing deleted by the bucket.

### The satellite service

Now shares its task state through Redis (`ESURVEY_SAR_REDIS_URL`), so:

- a poller reaching a different replica still finds its task;
- a restart does not lose the Earth Engine work in flight;
- two replicas do not pay twice for the same parcel and window.

**The one thing to get right when adding replicas:** Earth Engine's limit is
per project, and each replica admits `ESURVEY_SAR_MAX_CONCURRENT` of its own.
Keep `replicas x max_concurrent` inside what the project actually allows --
two replicas at two each, not two at four, unless the quota was raised.

### The model service

Stateless. Scale it with the API's assessment workers, and remember the real
limit is the provider's quota per minute, not the container.

### Assessment throughput, end to end

The chain is: `ESURVEY_ASSESSMENT_WORKERS` per API replica -> the satellite
service's concurrency -> the Earth Engine quota. There is no point raising the
first past the third. Work it out from the quota backwards:

    Earth Engine concurrent requests allowed:   Q
    sar replicas x ESURVEY_SAR_MAX_CONCURRENT ≤ Q
    API replicas x ESURVEY_ASSESSMENT_WORKERS  ≈ Q

Everything beyond that queues in `assessment_jobs`, which is where it should
queue: durable, visible in `/api/v1/assessment-jobs`, and resumable after a
restart.

---

## What is not yet true

- **The load balancer.** Today Caddy terminates TLS in front of one API. With
  two or more replicas, `ESURVEY_TRUSTED_PROXY_HOPS` must count the hops from
  the outermost proxy, or the rate limiter will key every caller to the proxy's
  address -- which is the CGNAT failure (SYNC_SCALE_REVIEW.md, H1) inflicted on
  ourselves.
- **Postgres is a container on the app VM.** Move it before a district goes
  live, not after.
- **No autoscaling, and none wanted yet.** The peak is predictable -- the
  morning after an event -- and a fixed two or three replicas absorb it. What
  must not be autoscaled without thought is the satellite service, for the
  quota reason above.

## Checking it

    GET /readyz                      every dependency, and what is degraded
    GET /api/v1/assessment-jobs      queued, running, failed in the last day
    GET /api/v1/platform/devices     handsets with work waiting, and the stuck
    GET /readyz on the sar service   in flight, queue limit, per-replica concurrency

The numbers that say the shape is wrong: assessment `queued` climbing all day
(workers below quota, or the quota is the ceiling); `oldest_queued_at` hours
old; devices `stuck` rising while `with_pending` does not.
