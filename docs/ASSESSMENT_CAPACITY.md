# Assessment capacity for a lakh of claims a day

How many assessments this platform can produce is decided by the outside
services each one calls, not by the API. This note is how to size those
limits, what to ask each provider for, and where to read whether it is
working.

## What one assessment calls

| Stage | Upstream | Gate | Held for |
|---|---|---|---|
| Vegetation series (NDVI) | Farm Analytics Engine (Earth Engine) | `satellite` | submit to result |
| Terrain | Farm Analytics Engine | `satellite` | submit to result |
| Radar backscatter | radar service (Earth Engine, Sentinel-1) | `radar` | submit to result |
| Soil and canopy moisture | radar service | `radar` | submit to result |
| Ground video, per clip (up to 6) | ground-vision service | `vision` | one clip |
| Assessment panel | agent service -> model provider | `ai` | the whole panel |

The panel runs four specialists at once and then a synthesiser, so one slot of
the `ai` gate is up to **4 model calls at once and 5 in total**.

Repeat analyses of the same parcel and window are served from caches (the
engine's own, and the shared Postgres remote-sensing cache), so re-assessments
cost far less than first assessments.

## The arithmetic

A gate with `L` slots, on a stage that takes `t` seconds, finishes at most

    L x 86,400 / t   per day

and the platform's daily capacity is the smallest of those across the stages.
To finish `N` assessments a day a stage needs

    L = N x t / 86,400   slots, running all day

A lakh a day is 1.16 a second on average. Claims do not arrive evenly -- they
arrive in the days after a calamity -- so size for the backlog you are willing
to carry, not the average: a gate sized for the average never catches up
after a hailstorm.

| Stage time `t` | Slots for 1 lakh/day | Slots to clear 1 lakh in 12 h |
|---|---|---|
| 30 s | 35 | 70 |
| 60 s | 70 | 139 |
| 90 s | 105 | 209 |
| 180 s | 209 | 417 |

For the `ai` gate multiply by 4 for peak concurrent model calls, and by 5 for
model calls per assessment: a lakh a day is **5 lakh model calls a day**.

**The stage times above are placeholders.** They have not been measured
against the production engine, radar service or model at volume. Measure them
first (below), then size.

## Measuring

The product administrator's Health page (`/platform/health/`) shows:

- **Upstream capacity** -- each gate's slots in use across every instance
  against its limit, and the average wait for a slot. The gate that is always
  full with a growing wait is the one limiting throughput. Raising any other
  does nothing.
- **Claim flow** -- assessments finished in the last hour, median and 95th
  percentile run time, and *per day at this rate*. Read that last figure only
  while there is a backlog; an idle queue finishes what it is given.
- **Oldest receipt owed** -- how long the oldest complete claim has waited for
  its acknowledgement. Minutes at most when healthy, whatever the assessment
  backlog.

`assessment_jobs.run_ms` holds each job's run time for anything deeper.

## Setting the limits

All are deployment-wide, counted in Redis across every API instance. Set each
to the upstream's own limit, never per instance.

| Variable | Default | Set to |
|---|---|---|
| `ESURVEY_GATE_SATELLITE` | 4 | the Farm Analytics Engine's Earth Engine project concurrency |
| `ESURVEY_GATE_RADAR` | 4 | radar service replicas x `ESURVEY_SAR_MAX_CONCURRENT` |
| `ESURVEY_GATE_AI` | 8 | model quota (concurrent requests) / 4, and check requests-per-minute: `ai x 5 x 60 / t` |
| `ESURVEY_GATE_VISION` | 4 | ground-vision replicas x their capacity |
| `ESURVEY_ASSESSMENT_WORKERS` | 24 | per instance; at least the largest gate divided by instances |

If Redis is unavailable each instance counts on its own at the same limit, so
the deployment can briefly exceed a quota; the upstream's 429 is retried. The
Health page says so ("counting this instance only").

A job still waiting for a slot after 20 minutes is put back without spending
one of its five attempts, so a long backlog delays claims but does not fail
them.

## What to ask for

1. **Earth Engine (satellite and radar).** Concurrent-request quota on the
   project for the season's peak, with the arithmetic above. Earth Engine
   limits per project, so the analytics engine and the radar service compete
   if they share one; separate projects double the headroom.
2. **Model provider.** Requests per minute and tokens per minute for 5 lakh
   calls a day with 4 at once per assessment, including images. Ask for the
   peak, not the average.
3. **Radar and vision services.** Replicas: each replica admits
   `max_concurrent`, and the gate should equal replicas times that.

## What this does not change

The officers. Every claim still goes to a Talathi and a Krushi Sahayak; an
assessment that is late or absent sends the claim on without one rather than
holding it (`report.AdvanceOne`). Capacity here decides how many claims reach
the officers *with* evidence, not whether they reach them.
