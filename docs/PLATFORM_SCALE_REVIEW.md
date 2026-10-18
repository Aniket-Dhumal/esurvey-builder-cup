# Scale beyond sync: the rest of the platform

Review of 2026-09-16. The first review (SYNC_SCALE_REVIEW.md) followed one
path: a claim leaving a handset. This one follows everything else -- what an
officer's screen costs, what the audit trail costs, how a farmer is told
anything, how an assessment is produced, and what a season's evidence weighs.

Same method: read the path, then measure it against the local stack. Probes
are in the suites (`ESURVEY_SCALE=1`), so a regression shows up as a failing
test rather than as a slow morning.

Target, as before: **lakhs of claims a season**, peaking the morning after a
district-wide event.

---

## Measured

| Probe | Before | After |
|---|---|---|
| Officer worklist, first page, 200,000 reports in one district | **1.34 s** | **14 ms** |
| Worklist, page 20 | 1.01 s | 11 ms |
| Worklist, deepest allowed page (offset 19,950) | 1.11 s | 141 ms |
| Search by farmer name across the district | 291 ms | 31 ms |
| Notification outbox, 80 ms gateway | **12/s** (a lakh = 2 h 22 m) | **150/s** (a lakh = 11 m) |
| Audit log writes, 32 concurrent writers | 3,894/s | unchanged; now partitioned monthly (A2) |
| Audit log writes, 1 writer | 580/s | unchanged |
| Worklist page by cursor, 200,000 reports | **783 ms** | **3 ms** |
| Assessment requested | held the request for minutes | **queued, 202 in ms** |

---

Everything below was fixed on 2026-09-16 except where "still open" says
otherwise; the second half of this document records what each fix was.

## Fixed in this round

### The officer's worklist read the whole district (migration 0035)

The listing orders by *contested first, then oldest*, and no index matched
that order. Every page read every report in scope, computed each one's crop
totals through a lateral join, sorted the lot, and returned fifty rows. At
200,000 reports that was **1.3 s for the first page** -- the screen a taluka
office lives in, on the pool the write path shares, growing with the season.

Two indexes now carry the order: one district-wide, one keyed by village for
the scopes that are a single village (a talathi's list is a handful of rows
among a district's hundreds of thousands). The scan stops once it has the
page, so the lateral join runs for the rows returned rather than for the
district. **1.34 s -> 14 ms.**

Probe: `TestOfficerListingAtDistrictScale` (report package), which fails if any
of those pages takes more than a second.

### Farmers were told things one at a time

The outbox claimed a hundred messages, sent them **one after another**, and
waited for the next tick. With a gateway answering in 80 ms that is 12
messages a second: a district-wide event's acknowledgements took **2 h 22 m**,
and the decisions weeks later took as long again -- while the farmer who filed
first waited hours for "your claim has been received".

Now: a bounded pool of senders (16 by default, `ESURVEY_SMS_WORKERS`), and the
dispatcher drains rather than sampling -- it keeps claiming batches while the
queue is full instead of one batch per tick. **12/s -> 150/s**, a lakh in
eleven minutes.

The ceiling above this is contractual, not technical: Indian SMS aggregators
sell throughput per second and refuse above it. Raise the worker count to
match what the DLT-registered sender is actually given.

Probe: `TestOutboxDispatchRate` (notify package), which fails below 50/s.

---

## What the remaining hurdles were, and what was done

### A1. An assessment was produced inside one HTTP request -- fixed

`POST /api/v1/assessments` ran the whole pipeline inline: Earth Engine NDVI,
terrain, radar, the weather archive, ground vision, then the panel of models --
minutes of work on an officer's open connection. The satellite service admits
four Earth Engine jobs at once, so a lakh of assessments was roughly seventeen
days of engine time with nothing recording where it had got to; a restart lost
everything in flight; and back-pressure reached the officer as a failure.

Now (migration 0036, `internal/assessjob`, `internal/api/assessment_jobs.go`):

- **The request records intent and returns 202.** What can be checked at once
  still is -- the farmer exists, the parcel is theirs, the officer is posted
  where the land is -- so a mistyped farmer id is refused while the officer is
  still looking at the form.
- **Workers produce it**, four by default (`ESURVEY_ASSESSMENT_WORKERS`), to
  match the engine's own concurrency. Each claims one job at a time with
  `FOR UPDATE SKIP LOCKED`, so replicas divide the queue.
- **Failure is separated into two kinds.** A busy engine, a model provider's
  bad minute, a network fault: the job waits and is tried again, five times,
  backing off from a minute. A parcel with no boundary, a window with no clear
  observation: refused once, with a reason the officer can read.
- **A worker that dies gives its job back.** The claim is a lease; work whose
  lease expired is requeued by a sweep every minute.
- **One question is one piece of work.** Two officers asking about the same
  parcel and incident, or a console retrying after a lost reply, join the job
  that already exists rather than buying Earth Engine time twice.
- **The console polls** `/api/v1/assessment-jobs/{id}` and says "Queued" or
  "Being analysed"; an officer can leave the page and the work carries on.

Tests: `internal/assessjob` covers one-worker-per-job under concurrency, the
retry/refuse split, giving up after the attempts, a dead worker's job coming
back, and the backlog figures.

### A2. The audit trail grew without bound -- fixed

Measured ceiling unchanged and still worth knowing: **3,894 entries/s with 32
writers, 580/s with one**, because each entry is hash-chained to the one before
it. The expected average is ~7/s.

What changed is the growth (migration 0037): `audit_log` is now partitioned by
month, so a year can be archived and detached rather than deleted -- the table
stays append-only, the hash chain is preserved exactly (rows were copied with
their recorded hashes and `audit_log_verify()` still walks them), and the
indexes an officer's search uses are per month. Housekeeping creates the next
two months' partitions ahead of time, so the log never meets a month it has no
partition for.

Tests: `internal/audit/partition_test.go` -- entries land in the right month,
a future month can be made before it is needed, and the log still refuses to be
rewritten through the partitioned parent.

### A3. A season of evidence is tens of terabytes -- decided

Not a code fault; a decision that had not been made. Now:

- **Production refuses to start without `ESURVEY_EVIDENCE_BUCKET`.** A
  container volume cannot hold a season, and the symptom of trying is a
  farmer's video refused at its last chunk with the disk full.
- **`deploy/evidence-lifecycle.json`** carries the lifecycle rules: abandoned
  chunk parts deleted after a fortnight, evidence to Nearline at 90 days and
  Coldline at a year, and nothing deleted by the bucket -- what removes
  evidence is the platform's own retention policy, against a rule somebody
  signed.

### A4. One VM, one of everything -- fixed

Two halves, both now done.

**The satellite service no longer keeps its state in the process.** Tasks, the
results that make a resubmission free, and the claim that stops two replicas
paying twice for one parcel all live behind `store.Store`: a dict on a laptop,
Redis wherever `ESURVEY_SAR_REDIS_URL` is set (compose sets it). So a poller
reaching a different replica finds its task, and a restart no longer loses the
Earth Engine work in flight.

A replica that dies mid-analysis used to leave a task PROCESSING for ever. The
replica doing the work now renews a short lease, and a task found running
without one is reported failed -- which the assessment queue reads as retryable
and asks again. Earth Engine's limit is per project and each replica admits its
own `max_concurrent`, so the deployment multiplies: that is written down beside
the setting and in the topology note.

Tests: a second runner answering the first's poll, two runners not paying twice
for one parcel and window, and a stranded task reported rather than left
pending (`satellite-intelligence/tests/test_service.py`).

**The shape of production is written down**: docs/PRODUCTION_TOPOLOGY.md --
what each component has to be true for, what already coordinates through
Postgres or Redis, how to size assessment throughput backwards from the Earth
Engine quota, and the three things that are not yet true (the load balancer's
hop count, Postgres still a container on the app VM, and no autoscaling).

### A5. Deep paging -- fixed

Offset paging is still what the numbered pager uses, and still refused past
20,000 rows. Alongside it, listings now issue a cursor (`next_cursor`), which
continues at constant cost however deep it goes -- what a state-wide list
needs.

The sort key had to change for it to be index-friendly: the worklist orders by
contested first, and a cursor's "everything after this row" is a row comparison
that orders every column the same way. Written as `NOT contested` ascending --
the same order -- the whole comparison is answered from one index (migration
0038). Measured: **783 ms a page before, 3 ms after**, walking 1,500 rows over
thirty pages with no row repeated or skipped.

### A6. Smaller things -- fixed

- **Notifications are purged.** Delivered notices older than thirteen months
  are swept hourly (`ESURVEY_NOTIFICATION_RETENTION`); what was never
  delivered stays until somebody deals with it.
- **The console bell** polls once a minute and already stops when the tab is
  hidden. Left as it is: ~83 requests a second at five thousand officers,
  answered from a partial index.
- **The rollup queue** reads 20,000 marks a minute and recomputes up to 200
  villages -- comfortable at a lakh of filings an hour, and the queue to watch
  if filings ever outpace that.

---

## Still open

- The ten **handset checks** in docs/HANDSET_CHECKS.md, which need a phone.
  The logic under each is covered by automated tests -- the table at the top of
  that file says which -- so what the handset run is actually for is the
  platform underneath: Android stopping a process, a real radio dropping
  mid-chunk, two isolates on one keystore, and memory while the camera is open.
  `scripts/handset-check.sh` sets each one up.
- Acting on the topology note: moving Postgres off the app VM, and counting the
  proxy hops when a load balancer goes in front of more than one API replica.

---

## Deploying this round

- Migrations 0036, 0037 and 0038 run at startup. 0037 rewrites `audit_log`
  into a partitioned table and copies every row: quick at current volumes,
  but it is a table rewrite, so take the backup first.
- Set `ESURVEY_EVIDENCE_BUCKET` (production now refuses to start without it)
  and apply `deploy/evidence-lifecycle.json` to that bucket.
- New settings, all with defaults: `ESURVEY_ASSESSMENT_WORKERS`,
  `ESURVEY_SMS_WORKERS`, `ESURVEY_NOTIFICATION_RETENTION`.
- The console must be redeployed with the API: `POST /api/v1/assessments` now
  answers 202 with a job to poll.
- The satellite service now wants `ESURVEY_SAR_REDIS_URL` (compose sets it to
  the platform's own Redis, database 1). Without it the service still runs,
  single-replica, exactly as before.
