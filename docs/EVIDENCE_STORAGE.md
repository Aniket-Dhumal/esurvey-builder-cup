# Evidence storage

Farmer-captured photographs and video are the most sensitive thing this
platform holds: images of identified people's land, tied to their name, their
Aadhaar reference and a GPS fix accurate to a few metres. They are also the
only thing here that cannot be recomputed. A satellite series can be
re-fetched, an assessment re-run, a report re-filed; a photograph of a
flattened field on the day it was flattened exists once.

Everything below follows from those two facts.

## What is built

Project `esurvey-501512`, created 30 August 2026.

| | |
|---|---|
| Bucket | `gs://esurvey-evidence-501512` |
| Region | `asia-south1` (Mumbai), single region |
| Storage class | STANDARD, → NEARLINE after 180 days |
| Uniform bucket-level access | on |
| Public access prevention | **enforced** |
| Object versioning | on |
| Soft delete | 30 days |
| Encryption | **CMEK**, `evidence-cmek`, rotating every 90 days |
| Service account | `esurvey-evidence@esurvey-501512.iam.gserviceaccount.com` |
| Its access | `roles/storage.objectAdmin`, **on the bucket only** |

### Why each of those

**Mumbai, single region.** Land records and farmer identity for an Indian
government scheme should not leave the country, and a single region is a
stronger statement about where the bytes are than a multi-region is. The cost
is that a regional outage makes evidence unreadable until it ends; the
alternative is not knowing which country a photograph is sitting in.

**Public access prevention, enforced.** Not "no public objects today" —
enforced means an object *cannot* be made public, by anyone, including a
future script with an over-broad role. Verified: an attempt to grant
`AllUsers:READER` is refused, and an anonymous GET returns 403.

**Uniform bucket-level access.** Per-object ACLs are how buckets leak. With
uniform access there is one policy to read and one place to get it wrong.

**Versioning and 30-day soft delete.** Evidence must not be silently
overwritten or quietly removed. Together these mean a deletion — accidental,
malicious, or from a bug — is recoverable for a month, and an overwrite never
destroys the original.

**Lifecycle.** Incomplete uploads are abandoned after 7 days, so a farmer
losing signal mid-upload does not leave billable debris. Non-current versions
are deleted after 365 days. Live objects are **never** auto-deleted: how long
evidence for a claim must be kept is a legal question, not a storage default.

**Bucket-scoped IAM, in both directions.** The service account can act on this
bucket and nothing else in the project, so a compromise of the application
reaches the evidence rather than the whole project.

And the reverse: the legacy `projectEditor` and `projectViewer` bindings a new
bucket carries have been removed. Those make every project editor an object
owner, which is how two unrelated Compute instances came to be able to read
every photograph here. What is left is project Owners and the application's
own account.

### Why there is no service-account key

Authentication is Application Default Credentials, and deliberately nothing
else:

```bash
# a developer machine
gcloud auth application-default login

# GKE or Cloud Run
# the workload's own identity, via Workload Identity
```

A downloaded key file for this service account would be a long-lived, silently
copyable credential to every photograph in the system. It would end up in a
`.env`, then in a backup, then in a chat message. There is no key to leak
because no key was created, and none should be.

## Encryption

Objects are encrypted with a customer-managed key that rotates every 90 days:

```
projects/esurvey-501512/locations/asia-south1/keyRings/esurvey-evidence/cryptoKeys/evidence-cmek
```

Applied 30 August 2026 and verified rather than assumed: a freshly written
object reports `cryptoKeyVersions/1` as its encryption key and reads back
intact.

What CMEK buys: the ability to revoke access to every object at once by
disabling one key, and an audit trail of every decrypt. What it does not buy:
protection from anyone who legitimately holds the key.

Objects written before that date keep the Google-managed key they were
written with — encryption is per object, not retroactive. The bucket held
nothing but smoke-test objects at the time, so in practice every object that
matters is CMEK-encrypted.

### What rotation does not do

A new key version is generated every 90 days and new objects use it. Existing
objects keep the version they were written under, which is why the older
versions must stay enabled: destroying one makes every object written under
it permanently unreadable. Rotation limits how much data a single key version
protects. It is not a way to re-encrypt the past.

## Who read what

Cloud Storage data access logging is on for the project:

| Log type | What it records |
|---|---|
| `DATA_READ` | Every object read — who looked at whose photographs |
| `DATA_WRITE` | Every object written or deleted |
| `ADMIN_READ` | Bucket and policy inspection |

Scoped to `storage.googleapis.com` alone rather than `allServices`. Turning
data access logs on everywhere would log every Postgres and KMS operation
too: a bill and a haystack, when the question this exists to answer is only
ever "who read whose photographs".

These reach Cloud Logging, not the platform's own audit table. The two answer
different questions — the audit table records what an *officer* did through
the API; these record what any identity did to the bucket, including one that
bypassed the API entirely. An investigation usually needs both.

## Turning it on

```bash
# 1. Credentials, on the host. Interactive, once.
gcloud auth application-default login

# 2. In .env
GOOGLE_ADC_FILE=$HOME/.config/gcloud/application_default_credentials.json
ESURVEY_EVIDENCE_BUCKET=esurvey-evidence-501512
ESURVEY_EVIDENCE_PREFIX=trial-2026-09   # optional, see below

# 3.
docker compose up -d api
```

`GOOGLE_ADC_FILE` is the host path compose mounts read-only at
`/var/run/gcp/adc.json`, where `GOOGLE_APPLICATION_CREDENTIALS` already points.
It defaults to `/dev/null`, so a deployment that does not use a bucket is
unaffected — Compose has no conditional volumes, and an unset source would
fail to start everything.

On GKE or Cloud Run, leave it unset. The workload's own identity comes from
the metadata server and no file is involved, which is the arrangement to
prefer wherever it is available.

Leave `ESURVEY_EVIDENCE_BUCKET` empty and evidence stays on the local volume,
which is the development default.

There is **no automatic fallback** from a configured bucket to local disk. If
the bucket is named and unreachable, the server refuses to start. Silently
writing evidence somewhere other than where an operator believes it is going
is worse than not starting.

`ESURVEY_EVIDENCE_PREFIX` keeps a trial's evidence separate from production's
inside one bucket, without depending on anyone remembering to. Set it per
trial.

## Verifying it

```bash
gcloud auth application-default login
ESURVEY_TEST_BUCKET=esurvey-evidence-501512 \
  go test ./internal/blob/ -run GCS -v
```

These are skipped without `ESURVEY_TEST_BUCKET`. They are opt-in rather than
absent because the failure modes that matter for an evidence store — wrong
region, no permission, public by accident — are all properties of the live
bucket, not of the code.

Without Application Default Credentials they fall back to a token borrowed
from the operator's `gcloud` session, so the Cloud Storage path can be
verified without anyone creating a service-account key. That is a test
convenience only: a downloaded key to this bucket would be a long-lived,
silently copyable credential to every photograph in the system, where a
gcloud token expires within the hour and is written nowhere.

Last verified 30 August 2026: all five pass against
`esurvey-evidence-501512`, including after the legacy bindings were removed.

`/readyz` reports where evidence is going and whether it is reachable, and
readiness fails if it is not:

```json
"evidence": { "status": "up", "where": "gs://esurvey-evidence-501512/trial-2026-09" }
```

## Backups

`gs://esurvey-backups-501512`, same region, same key, versioning on, public
access prevented. Lifecycle: STANDARD, NEARLINE at 30 days, COLDLINE at 120,
deleted at 400; superseded versions deleted after 30.

Four hundred days rather than a round year, so a restore during the same
season next year still has the previous one to compare against.

Nothing writes to it yet. The `pg_dump` command is in
[OPERATIONS.md](OPERATIONS.md); no schedule runs it.

## Why there is no bucket retention policy

A Cloud Storage retention policy prevents an object being deleted until it
reaches a given age. It is the obvious way to protect evidence, and it is
deliberately not used here. Three reasons, in order of weight.

**It would disable the lifecycle rules already in place.** Object Lifecycle
Management does not delete objects protected by a retention policy. This
bucket deletes superseded versions after 365 days; a seven-year retention
would silently stop that, and versions would accumulate for seven years while
the rule appeared to be configured and did nothing.

**It cannot express the rule that actually matters.** The application's
retention policy does not say "delete evidence at seven years". It says delete
it at seven years *unless the report is unsettled, or a grievance against it
is still unanswered* — because a farmer's appeal is about that claim and
answering it needs the evidence. A bucket policy knows only object age. It
cannot ask about an appeal.

**Two mechanisms for one rule is worse than one.** The application sweeper is
auditable (every run is recorded, including dry runs), reversible, refuses
without three separate confirmations, and reports what it would do before it
does anything. A bucket policy is none of those things.

Locking a retention policy is a further step again: a locked policy cannot be
shortened or removed by anyone, and prevents deleting the bucket. That is a
reasonable end state for settled claim evidence under a statutory retention
requirement. It is the wrong shape for a trial, and it is not reversible if
the period turns out to be wrong.

What protects evidence today, without any of those costs: versioning, a
30-day soft delete, public access prevention, uniform bucket-level access, a
customer-managed key, and bucket IAM limited to project Owners and the
application's own service account.

## What is still open

- **No bucket retention policy, and this is decided rather than pending.**
  See below.
- **No signed-URL delivery.** Officers stream evidence through the API, which
  authorises every read against the same jurisdiction rules as everything
  else. Signed URLs would take load off the API but move authorisation to a
  time-boxed token; not worth it until the API is actually the bottleneck.
- **Nothing writes backups yet.** The bucket exists and the command is
  written; no schedule runs it.
