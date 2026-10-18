# satellite-intelligence

Sentinel-1 radar backscatter for the eSurvey assessment platform.

## What it is for

Monsoon calamity happens under cloud. Sentinel-2 is optical, so the NDVI
series the platform relies on goes blank at exactly the moment it matters —
the platform already records `largest_gap_days`, which means it has known
when it cannot see and has had nothing to fall back on. A two-month gap over
a kharif season is ordinary, not exceptional.

Radar does not care about cloud. Sentinel-1 passes every six to twelve days
over Maharashtra whatever the sky is doing, and it is free.

Two things the platform currently infers and can now observe:

- **Standing water** is near-black in radar, because a flat water surface
  reflects the pulse away from the satellite. Waterlogging risk today is
  derived from terrain slope, which says where water *would* collect rather
  than where it did.
- **Lodging** changes canopy geometry. A standing stem canopy depolarises the
  pulse and lifts the cross-polarised return; flatten it and the ratio between
  the two falls. That fall is the most direct measurement of lodging available
  from orbit.

## What it is not

Backscatter is not damage. It is a physical measurement whose relationship to
yield loss depends on crop, growth stage, soil moisture and incidence angle.
This service returns decibels and the metadata needed to interpret them. The
interpretation lives in the platform's `internal/sar` package, which already
knows how to separate a flooded field from a flattened one and to admit that
a harvested field looks identical to a destroyed one.

## The contract

Deliberately the same shape as the Farm Analytics Engine's NDVI and terrain
modules, because the platform's client already speaks it: submit returns a
task id, and the caller polls until the status leaves `PROCESSING`.

```
POST /api/v1/sar/submit          → 202 {"task_id": "...", "status": "PROCESSING"}
GET  /api/v1/sar/status/{taskID} → 200 {"status": "COMPLETED", "result": {...}}
GET  /healthz                    → 200
GET  /readyz                     → 200 when Earth Engine is reachable
```

A deployment that has not enabled this returns 404 on submit, which the
platform reads as `ErrRadarUnavailable` and treats as an absent evidence
source rather than a failure.

## Moisture

The same service also measures **canopy water** (Sentinel-2 NDMI) and a
**relative soil moisture index** (two years of Sentinel-1 VV), each against the
parcel's own prior seasons and the cropland within 1.5 km, in one Earth Engine
computation. See `src/esurvey_sar/moisture.py` for what is measured and why,
and the platform's `internal/moisture` for what it means.

```
POST /api/v1/moisture/submit          → 202 {"task_id": "...", "status": "PROCESSING"}
GET  /api/v1/moisture/status/{taskID} → 200 {"status": "COMPLETED", "result": {...}}
```

Request: `geometry`, `start_date`, `end_date` (at most 200 days apart) and
`baseline_years` (0–5, default 3). A task id belongs to one contract: a radar
id asked for on the moisture status endpoint is 404, and the reverse.

Radar and moisture run on **one** task runner — one semaphore, one queue limit,
one result cache — because they spend one Earth Engine project's quota. The
same 404-when-disabled behaviour applies, read by the platform as
`ErrMoistureUnavailable`. Synthetic mode generates a labelled drought, which
the platform refuses outside development.

Verified live on 14 September 2026 against `esurvey-501512`: a Malegaon parcel
of 131 Sentinel-2 pixels, 21,286 cropland pixels in the ring, two prior seasons
and 55 Sentinel-1 passes on relative orbit 63, in about 20 seconds.

## Running it

```
cp .env.example .env          # then set EE_PROJECT
docker compose up -d sar
```

Earth Engine authentication is Application Default Credentials, the same as
the platform's evidence bucket and for the same reason: a downloaded
service-account key is a long-lived, silently copyable credential. On a
developer machine:

```
gcloud auth application-default login
export GOOGLE_ADC_FILE=$HOME/.config/gcloud/application_default_credentials.json
```

The GCP project must be registered for Earth Engine. `esurvey-501512` already
has the API enabled and `farm-terrain@` holds `roles/earthengine.admin`.

## Synthetic mode

`ESURVEY_SAR_SYNTHETIC=1` serves plausible backscatter without Earth Engine,
for developing the platform's side of the contract.

It is off by default and every synthetic result is labelled `SYNTHETIC` in the
`missions` field, which the platform surfaces. A synthetic reading that
reached an officer as though it were an observation would be worse than no
radar at all, so the label is not decoration: it is the only thing standing
between a development convenience and a fabricated finding on somebody's
compensation record.

## Scale

The things that actually bind, and what is done about each:

**Earth Engine quotas.** EE limits concurrent requests per project. A
district-wide event submits hundreds of parcels within minutes, so requests
are bounded by a semaphore and queued rather than rejected — a slower answer
is worth more than a 429.

**Repeat work.** A closed window over an archive cannot change. Results are
cached by geometry and window, so a re-submitted parcel is answered from
memory, and the platform caches on its side too. Between them, the same
parcel analysed twice costs one Earth Engine call.

**Locality.** Neighbouring parcels in the same fortnight ask about overlapping
imagery. EE handles that internally; nothing here needs to.

**Task lifetime.** Tasks are kept for an hour after completion and swept. A
platform that polls and then crashes should not leak memory for a day.

**Backpressure.** Beyond the queue depth the service returns 503 with
`Retry-After` rather than accepting work it cannot start. The platform's
client already retries on 503 and treats a persistent one as an unavailable
module.
