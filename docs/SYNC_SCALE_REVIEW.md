# Sync at production scale: test results and gaps

Review of 2026-09-15. Target: lakhs of claims in a season, with the morning
after a district-wide event as the peak -- every handset in a district
regaining signal within the same hour.

Method: read the whole write path (phone queue -> HTTP -> middleware -> stores
-> Postgres/Redis/blob), then prove or measure each suspected problem against
the local stack (Postgres 16 + PostGIS, Redis 7, blob dir) with probes that are
now part of the test suites.

    ESURVEY_SCALE=1 go test ./internal/report ./internal/api ./internal/survey
    ESURVEY_TEST_REDIS_ADDR=... go test ./internal/auth     (rotation)
    flutter test test/scale_gaps_test.dart

Every gap below is fixed (2026-09-15 and 2026-09-16); see "Fixes" at the end.
The tests that proved them now run as ordinary regression tests.

---

## What was measured

| Probe | Result |
|---|---|
| 16 different farmers filing at once, one district | **10 of 16 refused** (`crop_loss_reports_reference_key` duplicate) |
| One filing, district with 20 vs 200,000 reports | 15.7 ms -> 35.9 ms, growing linearly |
| Idempotency: 1,000,000 stored keys, 400 concurrent callers, pool of 20 | 2,508 claim+complete/s, 0 failures; Begin p99 158 ms, Complete p99 173 ms (pool wait) |
| Idempotency table size at 1M keys (300-byte bodies) | 503 MB |
| Idempotency sweep, 333,330 expired keys | 3.1 s, batched, no long lock |
| 4 full-size (59 MiB) videos assembling at once | **956 MiB heap above baseline**; API container limit is 512 MiB |
| Redis memory per refresh rotation | live token 352 B; tombstone 137 B kept **31 days**; maxmemory 256 MiB |
| Upload started 8 days ago, chunk received today, then sweep | **swept mid-upload** |
| App isolate + background worker both refreshing | **session family revoked** |
| Refresh reply lost, client retries | **session family revoked** |
| Photograph of a report the office refused | **counted as waiting for ever** |

---

## Critical -- will fail at scale

### C1. Concurrent filings in a district are refused as invalid (proven)

`report.nextReference` is `SELECT count(*) + 1 FROM crop_loss_reports WHERE
district = $1 AND left(crop_year,4) = $2`, inside the insert transaction with no
lock. Two filings in the same district at the same moment read the same count
and build the same reference; the unique constraint refuses the second.

Why it is critical: the error is wrapped as `ErrBadInput` and answered **400
INVALID_REPORT**. The phone's retry policy reads a 400 as the office refusing
the claim, marks the draft `failed` and stops. On the peak morning most claims
in a busy district would be refused this way, and every photograph behind them
then waits for ever (H3).

Also: the cost grows with the district (count over every report ever filed
there), and it runs inside the transaction, lengthening lock hold time. Any
future deletion of reports would make the numbering collide permanently.

Fix: a counter row per (district, year) -- `INSERT ... ON CONFLICT DO UPDATE SET
n = n + 1 RETURNING n` -- or a sequence; retry the insert on a reference
collision; and map any unique-violation or serialization failure that is not
the client's fault to a retryable 503, never 400.

### C2. Assembling videos can OOM-kill the API (proven)

`finishUpload` reads every part into one `[]byte` (up to 60 MiB), then
`Submit` does `io.ReadAll` over it again (buffer doubling), hashes, and hands
the bytes to the blob store. Four concurrent assemblies (the `AssemblyLimit`)
measured **956 MiB** of heap. The API container limit is **512 MiB**.

When it trips, the kernel kills the whole API process: every in-flight request
on that instance fails, every phone backs off, and they return together.

Fix: stream. Assemble as an `io.MultiReader` over the parts, hash while
streaming, stream into the blob store (on GCS use object compose, no bytes in
the API at all), and let `Submit` accept a pre-computed hash and a reader.
Until then: `AssemblyLimit = 1` and `GOMEMLIMIT` set below the container
limit, or raise the limit to 2 GiB.

### C3. Redis fills with refresh-token tombstones, then evicts one-time codes

Every rotation writes a `rtu:` tombstone kept for `ttl + 24h` = **31 days**
(137 B) plus the new live token (352 B). Access tokens last 15 minutes, so an
active phone rotates many times a day. Redis is capped at 256 MiB with
`volatile-ttl` -- which evicts the keys **closest to expiry first**: OTP codes
(5 min) and rate-limit counters (1 min), before any refresh token.

Estimate: 100,000 farmers averaging one rotation a day hold ~425 MiB of
tombstones alone. Past ~60,000 the store is full; from then on a farmer's OTP
can be evicted between SMS and entry, so sign-in fails with "invalid code",
and the rate limiter silently stops limiting.

Fix: keep tombstones only as long as reuse detection is useful (hours, or
until the successor is itself rotated), give farmers a longer access-token TTL
(hours, not 15 min -- the refresh token remains the revocation point), put OTPs
and throttles where they cannot be evicted (`noeviction` instance, or
Postgres), and size Redis from the expected session count.

### C4. Refresh rotation signs farmers out in ordinary conditions (proven)

Reuse detection revokes the whole session family when a spent refresh token is
presented. Two ordinary situations present one:

1. **Two isolates.** The background worker refreshes and rotates; the app's
   `Session` still holds the old token in memory (`load()` reads once) and
   presents it on its next refresh. Family revoked.
2. **Lost reply.** On 2G the server rotates but the response never arrives;
   the phone retries with the only token it has. Family revoked. The server
   comment accepts this as "one sign-in" -- at lakhs of farmers on rural
   networks that is thousands of forced OTP sign-ins a day, each needing
   signal, an SMS and its cost. After Phase 1 nothing is deleted, but nothing
   is sent until the farmer signs in again.

`Rotate` is also Get-then-pipeline, not atomic: two simultaneous refreshes with
one live token can both mint, leaving two live tokens in a family.

Fix: a short grace window -- a token rotated within the last N seconds,
presented again from the same family, gets the same successor back instead of
revocation; make rotation atomic (Lua or `GETDEL`); on the phone, re-read the
keystore before refreshing and hold a cross-isolate lock (the SQLite lease
pattern) around refresh; longer farmer access TTL (see C3).

---

## High

### H1. Anonymous rate limit by IP throttles whole villages

Before a token is presented the budget is **60 requests/minute per IP**. Token
refresh and OTP are anonymous. Rural mobile carriers put thousands of
subscribers behind one CGNAT address. On the peak morning refreshes from one
carrier gateway exceed 60/min, get 429, and the app's refresh treats any
non-401 failure as "offline" -- the pass ends and the village's queues stall
and retry together.

Fix: exempt `/auth/refresh` from the IP bucket and limit it per refresh-token
hash (the token is unguessable); keep OTP limited per mobile number (already
throttled) with a much higher per-IP ceiling. Also re-check
`ESURVEY_TRUSTED_PROXY_HOPS` whenever a load balancer is added in front of
Caddy: with the wrong hop count every caller shares one bucket.

### H2. Slow uploads are swept mid-flight, and the app then gives up (proven)

`capture_uploads.expires_at` is set once at begin (+7 days) and never moved
by activity. A farmer whose 48 MB of video trickles over more than a week
loses the upload while chunks are still arriving. The next chunk answers 404
`UPLOAD_NOT_FOUND`, which the phone classifies as permanent: the video is
marked failed and never sent.

Fix: extend `expires_at` on every chunk and on resume; on the phone, treat
`UPLOAD_NOT_FOUND` as "start this upload again" (begin by the same key), not a
refusal.

### H3. Photographs of a refused report wait for ever (proven)

`_sendCapture` returns `waiting` while its draft has no server id. A draft the
office refused never gets one, so its photographs stay in `outstanding`
indefinitely: the home queue card never clears, the device heartbeat reports
them pending for ever (operator noise at scale), and a departed farmer's data
is never purged from a shared phone.

Fix: when a draft is `failed`, mark its captures blocked with the draft's
reason; count them as refused, not waiting.

### H4. The phone reads every video fully into memory to hash it

`station_walk_screen._saveStation` does `File(dest).readAsBytes()` and
`sha256.convert(bytes)` straight after recording, with the camera still open.
A 15 s 720p clip is 12--30 MB depending on the handset's encoder; on a 2--3 GB
phone this is where Android kills the app mid-walk. (The uploader already
hashes by streaming; the capture screens do not.)

Fix: stream the hash (`sha256.bind(file.openRead())`), as the uploader does.

### H5. The sync lease expires during a long upload

The device-wide lease has a 15-minute TTL and is renewed once per queued item.
One video over 2G/3G takes 20--40 minutes, so the lease lapses mid-upload and
the other isolate starts a second pass that uploads the same file in parallel
(the server copes; the farmer's data and battery do not). WorkManager also
stops a worker after about 10 minutes; the killed worker's lease then blocks
the app's own passes until it expires.

Fix: renew the lease per chunk; give background passes a time budget (stop
cleanly before 9 minutes, resumable anyway); shorter TTL once renewal is per
chunk.

---

## Medium

- **M1. Every resume re-hashes and re-begins.** The uploader hashes the whole
  file on every pass and always calls `POST /uploads`, which runs the PostGIS
  geofence and an open-upload count before discovering it is a resume. Use the
  capture row's stored sha256, and short-circuit begin for a known key.
- **M2. Filing cost grows with the district** (C1, 16 -> 36 ms at 200k), inside
  the transaction.
- **M3. Parked-account clients leak.** `Sync._apiFor` builds a new `Api` (and
  `http.Client`) per parked farmer per pass and never closes it; with the app
  open that is one leaked client every 30 s.
- **M4. Builds already in the field cannot be forced to update.** The
  minimum-version gate skips requests with no `X-App-Version`, and builds
  before 1.1.0 do not send one.
- **M5. Database error text reaches the phone.** `INVALID_REPORT` carries the
  raw Postgres message ("duplicate key value violates unique constraint ...").
- **M6. Idempotency storage.** ~500 MB per million keys with bodies; a season
  at ~10 keys per claim is several GB. Store bodies only for creates, or trim
  the body to the fields a replay needs.
- **M7. Device health scans the whole table** on every console load; the
  "stuck" query's OR defeats the index. Fine to ~10^6 devices; materialise
  after that.
- **M8. Sweeps run on every replica.** The hourly housekeeping runs in each API
  process; with several replicas they duplicate work and contend. Take an
  advisory lock so one runs.
- **M9. Filings in one village serialise briefly** on the rollup dirty-mark
  (`ON CONFLICT DO NOTHING` waits for the other transaction). Harmless at
  current transaction lengths; worse while C1/M2 lengthen them.

---

## What held up

- Idempotency record under concurrency and volume (above): exactly-once with
  no failures; sweep bounded.
- Report and photograph de-duplication under concurrent resends (existing
  tests: 16 and 12 concurrent copies -> one row).
- Upload resume, duplicate chunks, corrupt-assembly restart (existing tests).
- Phone queue: never abandons transient work, backs off with jitter, honours
  Retry-After, stops on 426, breaker after 3 server errors (existing tests).

## Suggested order

1. C1 reference counter + retryable conflicts (small, removes mass refusals)
2. C4 + C3 refresh grace window, longer farmer access TTL, tombstone TTL, OTP
   out of the evictable store (auth is the gate to everything else)
3. C2 streaming assembly (or `AssemblyLimit=1` + `GOMEMLIMIT` today)
4. H1 refresh out of the per-IP bucket
5. H2, H3, H4, H5 (phone and upload lifecycle)
6. Medium items

---

## Fixes (2026-09-15)

| Item | Change | Evidence after |
|---|---|---|
| C1 | `report_reference_counters` (migration 0033), advanced by one upsert inside the filing transaction just before the insert; seeded from the highest number already issued. Collisions, deadlocks, lock and statement timeouts are `ErrRetry` -> **503 TRY_AGAIN** with Retry-After, never 400. Constraint text no longer reaches the phone (M5). A first attempt that took the number on a second connection deadlocked the pool under load and was replaced. | 32 simultaneous filings in one district: all land, unique references. One filing: 10.9 ms empty district, 8.9 ms at 200,000 reports. |
| C2 | Assembly streams: parts are read one at a time to hash, then streamed into the store through `blob.Streamer` (`Dir.PutStream`, `GCS.PutStream` with a 4 MiB writer buffer). `Submit` keeps the in-memory path for photographs (8 MiB bounded); clips go through `record`. | 8 x 59 MiB clips finishing together: heap growth **18 MiB** (was 956 MiB). Test asserts < 128 MiB. |
| C3 | Tombstones kept 72 h (was 31 days) in a compact `family|next|unix` form; farmers get a 6 h access token (`ESURVEY_FARMER_ACCESS_TTL`), so at most ~4 rotations a day; Redis `noeviction` with a 1 GB default and a sizing note; the API logs a warning past 75% of Redis memory. | Tombstone TTL and size asserted in `auth/session_test.go`. |
| C4 | Rotation is atomic (WATCH/MULTI, jittered retries). The immediately previous token, within 72 h, whose successor has never been used, is answered with a fresh successor and the unused one destroyed; anything else is still reuse and revokes the family. The theft verdict is itself committed, so a stale read retries instead of revoking. The phone re-reads the keystore before refreshing, so a token another process already rotated is picked up rather than re-spent. | Redis-backed: lost reply forgiven; two processes forgiven; old token after its successor was used revokes; grace expires; 20 simultaneous refreshes leave exactly one live token. App: worker-then-app refresh and lost-reply refresh no longer revoke. |
| H1 | Farmer sign-in, OTP, refresh and logout get their own per-address bucket of 1,200/min (was the shared 60/min anonymous bucket). | `ratelimit_budget_test.go`. |
| H2 | `expires_at` extended by every chunk and every resume; the phone treats 404 `UPLOAD_NOT_FOUND` as "begin again", not a refusal. | `TestAnUploadStillMovingIsNotSwept`; `retry_test`, `uploader_test`. |
| H3 | A photograph whose report was refused (or is gone) is marked refused with the report's reason; "try again" restores both. | `scale_gaps_test.dart`. |
| H4 | Capture and station screens hash by streaming the file. | -- |
| H5 | The uploader renews the sync lease after every chunk; background passes have an 8-minute budget and stop between chunks or items. | `uploader_test` (renewal per chunk, stop between chunks), `sync_test` (budget). |
| M1 | Resuming a known upload key skips the geofence and open-upload count; the uploader sends the hash already on the row instead of re-hashing the file. | `uploader_test`. |
| M3 | Parked-farmer clients are closed after their pass. | -- |
| M4 | A farmer request with no `X-App-Version` counts as older than any minimum; officers' browsers are unaffected. | `device_handlers_test.go`. |
| M6 | Recorded idempotent bodies capped at 64 KiB (was 512). | -- |
| M8 | Hourly housekeeping takes a Redis lock, so one replica sweeps. | -- |

### Still open, then closed (2026-09-16)

| Item | Change | Evidence after |
|---|---|---|
| M7 | The health page is answered from partial indexes over the phones that have work, refusals or a recent report (migration 0034 adds a stored `pending_total` and three indexes); the two-index "stuck" list replaces the `OR`; the total is taken from the planner's statistics above fifty thousand rows; the whole payload is cached in Redis for 30 s, so viewers refreshing share one computation. | 300,000 devices: **57--73 ms** for the uncached page (`TestDeviceHealthAtFleetScale`, asserts under 500 ms). |
| M9 | The rollup queue is append-only: no unique key, so a filing never waits on another filing's mark. The sweeper reads up to 20,000 rows, recomputes up to 200 distinct pairs, and deletes every claimed row belonging to a pair it recomputed -- so duplicates drain at the rate they arrive. It also fixes a lost update: deleting by (event, village) used to remove marks made *during* a sweep, leaving a village's rollup quietly stale. | `TestOneVillagesFilingsDoNotWaitOnEachOther`: a second filing's mark with `lock_timeout = 500ms` no longer blocks; the sweep clears both marks. |
| Event references | `event_reference_counters` (migration 0034), advanced in the creating transaction; seeded from the highest number already issued. The last `count(*)+1` in the platform is gone. | `TestEventsCreatedAtOnceInOneDistrictGetDistinctReferences`: 8 simultaneous declarations, distinct references. |
| Handset checks | What can be automated now is: the whole upload lifecycle over real HTTP through routing, the token and the app-version gate, including housekeeping sweeping an upload mid-flight and the app beginning it again (`internal/api/upload_e2e_test.go`). What cannot is written up as a run sheet: **docs/HANDSET_CHECKS.md**, ten checks with how to force each condition and where to look. | `TestAClipSurvivesEverythingBetweenTheHandsetAndTheStore`. |

Also fixed while here: an unmapped upload failure now logs its reason instead
of answering 500 with nothing in the log.

### Still open

- The ten handset checks in **docs/HANDSET_CHECKS.md** have not been run; the
  results table is empty. Nothing else on this list is outstanding.

### Deploying these

Migrations 0033 and 0034 run at startup. 0034 rewrites two trigger functions
and drops a primary key on `report_rollup_dirty`; both are quick, and the
sweeper tolerates either shape. Set `REDIS_MAXMEMORY` from the sizing note in
docker-compose.yml and recreate Redis so `noeviction` takes effect.
