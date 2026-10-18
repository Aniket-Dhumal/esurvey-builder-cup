# Running eSurvey

What to do when something is wrong, and what to have done before it is.

## Backups

There are two scripts, and a drill that has been run:

```bash
scripts/backup.sh                                  # database + evidence, verified
scripts/restore-drill.sh backups/esurvey-db-<stamp>.dump   # restore it, and prove it
```

`backup.sh` dumps Postgres in custom format, verifies the archive is readable
before reporting success, tars the evidence volume, writes a SHA-256 beside
both, and prunes anything older than `BACKUP_KEEP_DAYS` (default 14).

`restore-drill.sh` restores into a throwaway database created empty, runs the
migrations on top of the restored schema, and prints the row counts. It refuses
to touch the configured database. Last run 10 September 2026 against the local
stack: 45,306 jurisdictions, 20 reports, 41 captures and 1,073 audit entries
restored, migrations 27 to 30 applied on top, passed.

Everything below is procedure, and procedure without a backup is a plan for how
to lose things carefully — so schedule the first script and run the second
often enough to believe it.

### Scheduling it

Nothing schedules these yet; they are run by hand. On the VM:

```cron
# Nightly at 01:30 IST, off-peak for a district that files during the day.
30 20 * * * cd /opt/esurvey && ./scripts/backup.sh >> /var/log/esurvey-backup.log 2>&1
```

A dump on the same disk as the database survives a dropped table and nothing
else, so follow it with the upload below.

### What has to be backed up, and what does not

| | Why |
|---|---|
| **Postgres** | Everything that cannot be recomputed: reports, verifications, adjudications, grievances, the audit trail, and the jurisdiction tree. |
| **Evidence** | Photographs and video. The one thing in the system that exists once — a photograph of a flattened field on the day it was flattened cannot be re-taken. |
| ~~Assessments~~ | Recomputable from the registry, the satellite series and the panel. Restore them if it is cheap; do not delay a restore for them. |
| ~~Satellite overlays~~ | Derived from public data. |
| ~~Container images~~ | Rebuilt from the repository, which is what the repository is for. |

### Postgres

```bash
# Nightly, retained 30 days. --format=custom so pg_restore can be selective;
# a plain SQL dump restores all-or-nothing, which is the wrong shape during
# an incident.
docker compose exec -T postgres pg_dump \
  --username=esurvey --dbname=esurvey \
  --format=custom --compress=9 \
  > "esurvey-$(date -u +%Y%m%dT%H%M%SZ).dump"
```

Two things about that command matter more than the command:

**It must be tested.** A backup nobody has restored is a file, not a backup.
That is what `scripts/restore-drill.sh` is for, and it does more than restore:
it applies the current migrations on top of the restored schema, because a
backup is nearly always older than the code that will read it. A dump that
restores cleanly into a database the API then cannot open is a failed restore
even though `pg_restore` was happy.

**It must live somewhere else.** A dump on the same disk as the database
survives a dropped table and nothing else. `gs://esurvey-backups-501512`
exists for this: same region as the evidence, same customer-managed key,
versioning on, public access prevented, ageing to COLDLINE and deleted at
400 days.

```bash
gcloud storage cp "esurvey-$(date -u +%Y%m%dT%H%M%SZ).dump" \
  gs://esurvey-backups-501512/postgres/
```

### Evidence

Cloud Storage already holds this: object versioning is on and soft delete
retains a deleted object for 30 days, so an accidental delete or overwrite is
recoverable for a month without any additional copy. See
[EVIDENCE_STORAGE.md](EVIDENCE_STORAGE.md).

While `ESURVEY_EVIDENCE_BUCKET` is empty, evidence is on a local Docker
volume with **none of that**. That is the single largest data risk in the
current deployment, and turning the bucket on is the fix.

### Restoring

```bash
# Into a fresh database, never over a live one. Restoring over a running
# system is how a bad backup becomes a bad database.
docker compose exec -T postgres createdb -U esurvey esurvey_restore
docker compose exec -T postgres pg_restore \
  --username=esurvey --dbname=esurvey_restore --no-owner \
  < esurvey-20260830T000000Z.dump
```

Then point a spare API container at it, check `/readyz`, and only afterwards
consider swapping. The audit table is append-only by trigger, so a restore
that loses rows loses them silently — check its count against what you expect
before trusting anything else.

## When something is wrong

`/readyz` answers most of it. Every dependency reports itself:

```json
{
  "checks": {
    "postgres":      { "status": "up" },
    "redis":         { "status": "up" },
    "ai":            { "status": "ok", "prompt_version": "…" },
    "satellite":     { "status": "up" },
    "evidence":      { "status": "up", "where": "gs://…" },
    "registry":      { "status": "up", "farmers": 1, "files": 1 },
    "notifications": { "status": "undelivered", "queued": 412 }
  }
}
```

### "Assessments are all failing"

Look at `checks.satellite`. The analytics engine is a single deployment on a
fixed address and it is called *before* the assessment panel, so when it is
down nothing else gets a chance to degrade gracefully.

- `status: unreachable` means the circuit breaker is open: it failed five
  times consecutively and requests are being refused in milliseconds rather
  than waiting out a 60-second timeout. It probes again every 30 seconds and
  closes itself when the engine answers.
- Officers see "the satellite analysis service is not responding, try
  shortly", not a connection error.
- Existing assessments, reports and the whole console are unaffected.

`esurvey_satellite_consecutive_failures` on `/metrics` is the same number
over time.

### "Nobody is getting messages"

`checks.notifications.status: undelivered` with a non-zero `queued` means
exactly what it says: messages are being written and no provider is
configured to send them. Nothing is lost — connect a provider and the backlog
goes out. The count is the honest measure of how much the platform is failing
to tell people.

### "A farmer says they cannot see their reports"

Almost always jurisdiction, not a bug. Every listing is scoped by the
officer's posting or the farmer's own id, and the audit log records a denial
with the reason:

```
GET /api/v1/admin/audit?action=authz.denied&resource_id=<their id>
```

### "Docker will not start"

Seen on 30 August 2026 after the host disk filled. The symptom is
`Docker Desktop is unable to start`; the cause is one layer down.

```powershell
Get-Service com.docker.service     # Stopped
wsl.exe -l -v                      # times out
```

Docker Desktop reports `running wslexec: ... CommandTimedOut` in
`%LOCALAPPDATA%\Docker\log\host\com.docker.backend.exe.log`. WSL itself is
wedged, not Docker.

None of the unprivileged recoveries work: `docker desktop restart` cannot stop
the wedged processes, `wsl --shutdown` times out, and killing `wslservice` or
`vmmemWSL` is access-denied.

The sequence that fixed it, all elevated:

```powershell
Stop-Process -Name wslservice -Force   # the service sits in StopPending until this
Start-Service WSLService
```

`wsl -l -v` then answers, and reveals the real fault — the `docker-desktop`
distro stuck in `Uninstalling`, which is what Docker could not start from.
Clear it and let Docker rebuild it:

```powershell
wsl --unregister docker-desktop        # unprivileged; see below
# kill any stale "Docker Desktop" / com.docker.backend processes, then relaunch
```

**Unregistering `docker-desktop` does not destroy your volumes.** Its
`BasePath` is `AppData\Local\Docker\wsl\main`, a ~100 MB disk holding only
the engine OS. The data lives beside it in
`AppData\Local\Docker\wsl\disk\docker_data.vhdx` — 43 GB here — which WSL
does not own and Docker re-attaches. Confirm the registration before
unregistering anything:

```powershell
Get-ChildItem HKCU:\SOFTWARE\Microsoft\Windows\CurrentVersion\Lxss |
  ForEach-Object { (Get-ItemProperty $_.PSPath) | Select DistributionName, BasePath }
```

Every container came back, across all projects. Free disk space first — this
began when C: reached 100%, and Go's build cache and npm's cache are the two
largest reclaimable things on a development machine. That 43 GB Docker disk is
the other.

### "The console is slow"

Check `esurvey_http_request_duration_seconds` by route on `/metrics`. If it
is a search box, confirm the trigram indexes exist —
`TestTheSearchIndexesExistAfterMigrating` covers this, and their absence
turns every search into a sequential scan that gets worse weekly rather than
suddenly.

## Deploying

Migrations run at startup, under an advisory lock, one replica at a time, one
transaction each. A rolling deploy of five containers runs them once. A
migration that has already been applied is checksummed, so editing one after
it has run is caught at startup rather than becoming a difference between two
environments nobody can explain.

## The irreversible things

Three, and all of them are deliberately awkward:

**Retention sweeps.** `POST /api/v1/admin/retention/sweep` needs three
things to agree before it writes: the policy enabled in the database,
`confirm: true` in the request, and `retention:run` on the caller. Any one
missing produces a count and no change. Start with the count — it is the
number worth looking at before anyone makes it real.

**The migration tests.** They `DROP SCHEMA public`, and refuse to run unless
the database name contains `test`.

**Locking bucket retention.** Not done, and not pending either — there is no
bucket retention policy at all, by decision. It would disable the lifecycle
rules already configured, and it cannot express the rule that matters, which
is "delete at seven years unless an appeal is unanswered". See
[EVIDENCE_STORAGE.md](EVIDENCE_STORAGE.md).

## The default compute account

The default Compute Engine service account,
`21977092096-compute@developer.gserviceaccount.com`, holds **`roles/editor`**
on the project. That is Google's default and a well-known over-privilege.

Two Compute instances run under it, `aviral` (asia-south1-a) and `esurvey-vm`
(asia-south1-c), both with the default access scopes:

```
devstorage.read_only  logging.write  monitoring.write  pubsub
service.management.readonly
```

Access scopes are a ceiling applied on top of IAM, so the practical reach is
narrower than Editor suggests — those VMs could **read** storage but not
write or delete it. Read was the part that mattered: it included every farmer
photograph.

### What was done about it

The exposure came through the *bucket*, not the project. A bucket created in
the console carries legacy convenience bindings — `projectEditor` as
`legacyObjectOwner`, `projectViewer` as `legacyObjectReader` — which make
every project editor an object owner. Those have been removed from both
`esurvey-evidence-501512` and `esurvey-backups-501512`.

What remains on each bucket:

| Principal | Role |
|---|---|
| `projectOwner:esurvey-501512` | `legacyBucketOwner`, `legacyObjectOwner` |
| `esurvey-evidence@` (evidence bucket only) | `roles/storage.objectAdmin` |

The application was unaffected — it authenticates as `esurvey-evidence@`,
which holds its own explicit binding — and this was confirmed by re-running
`go test ./internal/blob/ -run GCS` against the live bucket afterwards.

### Removing the Editor binding — determined, not done

This was recorded as needing investigation. It has been investigated, and the
answer is that it is safe. What the two VMs can actually reach, given their
scopes:

| Scope | What it reaches in this project |
|---|---|
| `devstorage.read_only` | **Nothing.** Only two buckets exist and both now exclude `projectEditor`. |
| `pubsub` | **Nothing.** No topics and no subscriptions exist. |
| `service.management.readonly` | Service configuration, read-only. |
| `logging.write`, `monitoring.write` | The only live use. |

And nothing else uses that service account: no Cloud Run services, no
Artifact Registry repositories, no Container Registry buckets, and the
Functions, GKE and App Engine APIs are not enabled.

So `roles/editor` grants those VMs exactly what `logging.logWriter` and
`monitoring.metricWriter` would, and a great deal they cannot use — including
read access to any bucket created in future, which is the actual risk.

```bash
SA=21977092096-compute@developer.gserviceaccount.com

# Grant the replacements first, so there is no window without logging.
gcloud projects add-iam-policy-binding esurvey-501512 \
  --member="serviceAccount:$SA" --role=roles/logging.logWriter --condition=None
gcloud projects add-iam-policy-binding esurvey-501512 \
  --member="serviceAccount:$SA" --role=roles/monitoring.metricWriter --condition=None

# Then remove the over-privilege.
gcloud projects remove-iam-policy-binding esurvey-501512 \
  --member="serviceAccount:$SA" --role=roles/editor
```

Reversible with one `add-iam-policy-binding` if anything surfaces. Not run
from this session: modifying project IAM is refused by the tooling here, and
that is the right default for a change to shared infrastructure.

Worth doing before a new bucket is created, because a new bucket carries the
`projectEditor` bindings by default and the exposure returns.

## What is still open

- **No evidence bucket in use.** `ESURVEY_EVIDENCE_BUCKET` is empty, so
  evidence is on a local volume with no versioning and no soft delete.
- **No notification provider.** Messages queue.
- **No backup schedule.** The bucket and the commands exist; nothing runs them.
- **Circles are 1-of-358 mapped**, and the one is marked `PROVISIONAL`. The
  Tehsildar absorbs arbitration everywhere else by fallback rather than by
  design.
