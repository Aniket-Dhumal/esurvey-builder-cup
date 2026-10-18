# eSurvey — post-disaster crop loss assessment

Assesses crop damage for a land parcel by combining AgriStack registry records,
Sentinel-2 vegetation indices and Copernicus DEM terrain, and presents the
result for officer review under the revenue hierarchy that actually decides
these claims.

```
mock_files/    AgriStack records (real, with traced geometry)
reference_data/ the Maharashtra administrative hierarchy, from LGD
backend/       Go API — identity, authorization, registry, satellite, storage
ai-services/   Gemini assessment agent (Google ADK)
web-platform/  Next.js officer console (static export, client-only)
docs/          the platform specification and development plan
```

## Run it

```bash
cp .env.example .env
./scripts/gen-secrets.sh >> .env    # then delete the blank originals
# fill in ESURVEY_AI_GOOGLE_API_KEY and SATELLITE_API_KEY

docker compose up --build
```

Four containers: Postgres (PostGIS), Redis, the assessment agent, the API.
Postgres and Redis are published on loopback only — they hold the officer roll
and every live session.

The console is separate, because it compiles to static files:

```bash
cd web-platform
npm install && npm run dev          # http://localhost:3000
```

Sign in with `ESURVEY_BOOTSTRAP_ADMIN_USERNAME` and the generated password.
That account can do exactly one thing until it sets a password of its own.

Then load the administrative hierarchy — 45,305 rows, about two seconds, and
safe to re-run:

```bash
docker compose run --rm --no-deps api lgd-load /app/reference_data/lgd_maharashtra.csv
docker compose run --rm --no-deps api lgd-load /app/reference_data/circles_maharashtra.csv
```

See `reference_data/README.md` — in particular for why circles are almost
entirely missing and what it takes to add them.

### Without Docker

```bash
cd ai-services && uv run uvicorn esurvey_ai.main:app --port 8100
cd backend && go run ./cmd/server
```

The backend still needs a reachable Postgres and Redis; everything it needs is
in `.env.example`.

## Who can do what

Authorization has two axes, and both must pass on every request.

**What** — the permissions a role carries. The matrix lives in
`backend/internal/rbac`, in Go rather than in the database: it is a policy
artifact that should change as a reviewed diff with a test beside it, and
keeping it out of SQL removes a standing privilege-escalation path.

**Where** — the jurisdiction the role was granted in. A Tehsildar holds
`assessment:approve` inside their own taluka and nowhere else. Scoping is
pushed into the SQL (`jurisdictions.ancestor_ids && $1`, GIN-indexed) rather
than applied to a loaded result set, because filtering afterwards is one
forgotten early return away from a leak and does not paginate either.

| Role | Carries |
| --- | --- |
| `system_admin` | accounts, role grants, audit — and **nothing** about a claim |
| `state_officer` | read and escalate, statewide |
| `district_magistrate` | review, approve, escalate, audit |
| `tehsildar` | review, approve, escalate |
| `mandal_officer` | create, review, escalate |
| `talathi` | create, review, field survey |
| `krushi_sahayak` | create, review, field survey |
| `farmer` | submit a claim, read their own records |

Two of those are load-bearing and easy to undo by accident. `system_admin`
cannot touch a claim: whoever can mint an officer account must not also be able
to approve a payout with it. And Talathi and Krushi Sahayak both hold
`assessment:review`, because the joint panchnama is signed by the revenue side
and the agriculture side together.

A circle (mandal / saza) has no LGD code — LGD stops at sub-district and
resumes at village — so circles carry a locally assigned code and villages hang
off them. A Circle Officer's jurisdiction is otherwise inexpressible.

## Survey events

A calamity is notified by a **District Magistrate**, for their own district
and no other. The notification names the talukas or circles it covers, and a
farmer may be surveyed against it only if their land sits under one of those
areas and the incident date falls inside the event window.

```
SE-2026-496-001   Unseasonal hailstorm, Barshi and Solapur North
                  hailstorm · 2026-08-20 to 2026-08-22 · 192 villages
                  draft → declared → open → closed → assessment_complete
```

The district is taken from the declaring officer's posting rather than the
request body, and the database refuses an area outside it — a rule with that
consequence should not depend on every future write path remembering it.
Declaring is separate from drafting, because a notification is something a
Collector signs.

Village codes are not accepted as areas. A hailstorm across a taluka is not a
hundred-row form, and a village-by-village notification is not how a calamity
is declared.

## The console

```
Officers
  /                Dashboard
  /worklist        what I must verify, oldest deadline first
  /verify?id=      the report, the blind, record a finding, adjudicate
  /events          survey events; a Collector declares from here
  /reports         loss reports, filed and tracked
  /assessments     satellite + agent assessments
  /farmers  /parcels
  /admin           officer accounts and postings
  /audit           the trail

Farmers
  /my              the whole journey on one screen
```

The sidebar renders only what the signed-in officer can use — a Talathi sees
*My worklist* and not *Officer accounts*. That is a courtesy, not a control:
the server checks every request regardless. What it buys is a navigation an
officer can trust, rather than one that teaches them which links give 403.

## The farmer app

One screen, `/my`, installable to a home screen. **Both journeys are the same
journey**: with a network the report reaches the office as it is made, without
one it is written to the handset and sent when signal returns. There is no
offline mode to find and turn on — the farmer is told which happened and
nothing else changes.

### Guided capture, in four stages

One photograph of the middle distance answers nothing. Three answer different
questions and an assessor needs all of them.

```
1  पूर्ण शेत      wide shot     is this a field, what state is it in
2  नुकसान         the damage    lodging, shattering, submersion look nothing alike
3  पीक            the crop      identifies the crop and its growth stage
4  आणखी काही      anything else optional
```

Wide first, because it is taken from the edge where the farmer already is —
walk into the middle and that shot is gone.

Each stage speaks its instruction, shows what makes the photograph usable, and
reads the handset while it is being taken:

- **Position** — `watchPosition`, so accuracy tightens from 60 m to 6 m while
  the farmer stands still, and they can see it happening.
- **Tilt** — "tilt the phone down a little" before the shot, rather than a
  rejected photograph of the sky a week later. Guidance only, never evidence.
- **Heading** — recorded, so two photographs from one spot facing opposite
  ways are distinguishable to the officer reading them later.
- **Battery and storage** — warned about before the walk, not during it.

### Voice

Marathi, spoken and on screen. Many farmers read Marathi slowly and some not at
all; an instruction that arrives only as a paragraph does not arrive. Where a
device has no Marathi voice it falls back to Hindi, then English, then silence —
and the text is always there regardless. An English voice reading Devanagari
produces noise, so it stays quiet instead.

### Nothing waits for a network

A photograph is written to IndexedDB before anything else happens. If the app
is killed on the next line it still exists. `navigator.storage.persist()` asks
the browser not to evict it.

Sync is two passes — drafts, then the photographs of the drafts that landed —
and is safe to interrupt at any point. A draft records its server id the moment
it has one, so a crash cannot file twice; a photograph whose acknowledgement is
lost is re-sent and the server recognises the identical bytes.

**Connectivity is probed, not assumed.** `navigator.onLine` reports whether the
device has a network interface, not whether anything is reachable — a handset
on a village tower with no working backhaul says "online" while every request
fails, and that is the common case here. So a HEAD to `/healthz` decides, with
Next.js's own technique from its offline guide: a probe still pending at 200 ms
counts as reachable, because a genuinely dead host fails in milliseconds.

**A network failure does not count against the retry budget.** Only a refusal
does. The office never saw the request, so there is nothing to give up on, and
a farmer walking between two fields would otherwise burn six attempts on a
signal that was about to come back.

## Self-survey

The farmer photographs their own damage. A village pair cannot walk every
parcel in a taluka after a hailstorm, and the farmer is already standing in the
field; the only question is whether what they send back can be trusted.

**In the browser, not an app.** A farmer files one claim a year. Asking them to
install an app, from a store they may not have an account for, on a handset
with 2 GB free, is where most of them stop. A link works on every Android in
the village. What that costs is a weaker attestation than a signed native app
could give, and the gates below carry the weight instead.

**The position is read live.** Coordinates come from the handset at the moment
of capture, not from the image file — browsers strip EXIF and EXIF is trivially
editable anyway. A live reading has to be lied about deliberately.

**Six gates, each named rather than scored.** A gate that says "78% confident"
cannot be put in front of a farmer; "this photograph was taken 340 m outside
the plot" can.

```
inside_parcel           PostGIS containment, 25 m buffer for GNSS drift
position_fix            the handset fix was better than 100 m
within_incident_window  taken inside the notified window
after_the_incident      taken after the loss occurred
captured_near_upload    taken within a week of being sent
distinct_image          these exact bytes have not been sent before
```

**Only two of them disqualify.** A photograph taken outside the plot, or dated
before the loss, is not evidence of this loss on this parcel and never reaches
the model. Everything else is a provenance caveat: it travels to the officer,
and it travels to the model *in the caption*, so a ten-day-old image is
described as ten days old rather than shown as though it were fresh.

Nothing is refused at the door. A farmer standing on the bund of their own
field is outside the polygon and is not a fraudster; the officer sees the
distance and decides.

The 25 m buffer is deliberate: consumer GNSS is good to 5–15 m under open sky
and worse under a canopy, so it absorbs honest drift while leaving the
neighbouring survey number outside.

## Sessions

Officers sign in with a username and an argon2id-hashed password. Farmers get a
one-time code to the handset already in the registry; there is no password to
reset at an office they would have to travel to.

Access tokens are Ed25519 JWTs carrying the caller's grants, so an
authorization decision costs no database round trip. The cost of that — a
revoked grant staying usable until the token expires — is bounded by a
15-minute lifetime and by revoking every session the moment a grant changes.

Refresh tokens are opaque, stored hashed in Redis, single-use, and grouped into
families. Presenting one twice revokes the whole family: it cannot be
distinguished from a stolen token being used alongside the real one, and the
cost of being wrong is one sign-in versus indefinite access.

Rotation without signing anyone out: move `ESURVEY_JWT_SEED` into
`ESURVEY_JWT_PREVIOUS_SEEDS`, set a new one, drop the old entry after one
access-token lifetime.

## Audit

Every sign-in, refusal, grant and record read is written to `audit_log`, which
refuses `UPDATE` and `DELETE` at the database. An audit log the application can
rewrite is not evidence. Read it at `GET /api/v1/admin/audit` — an audit trail
that needs a database console is one nobody checks.

## API

```
GET  /healthz                          liveness
GET  /readyz                           readiness: postgres, redis, agent, registry
GET  /.well-known/esurvey-keys         token verification keys

POST /api/v1/auth/officer/login
POST /api/v1/auth/farmer/otp           request a code
POST /api/v1/auth/farmer/verify        exchange it for tokens
POST /api/v1/auth/refresh
POST /api/v1/auth/logout
GET  /api/v1/auth/me                   identity, grants, permissions
POST /api/v1/auth/password

GET  /api/v1/farmers                   farmer:read
GET  /api/v1/farmers/{farmerID}        farmer:read   + jurisdiction
POST /api/v1/assessments               assessment:create + jurisdiction
GET  /api/v1/assessments               assessment:read, scoped
GET  /api/v1/assessments/{id}          assessment:read, scoped
POST /api/v1/farmers/{id}/enrol        farmer:enrol — at the counter
POST /api/v1/reports/{id}/captures     survey:capture — the photograph
GET  /api/v1/reports/{id}/captures     the evidence and its gate results
GET  /api/v1/parcels/{farmID}/boundary the geometry a capture is checked against

GET  /api/v1/worklist                  what I must verify
GET  /api/v1/villages/{code}/authorities   who is allocated to a village
POST /api/v1/reports                   report:create
GET  /api/v1/reports                   report:read, scoped
GET  /api/v1/reports/{id}              report:read
GET  /api/v1/reports/{id}/print        the printable bilingual form
POST /api/v1/reports/{id}/allocate     assign to the village pair
GET  /api/v1/reports/{id}/brief        pre-visit — never the peer figure
POST /api/v1/reports/{id}/verify       record a finding (allocated only)
GET  /api/v1/reports/{id}/findings     blind until yours is in
POST /api/v1/reports/{id}/adjudicate   the figure that leaves the system

POST /api/v1/events                    event:create   — Collector only
GET  /api/v1/events                    event:read, scoped
GET  /api/v1/events/{id}               event:read
POST /api/v1/events/{id}/state         event:declare / event:close
GET  /api/v1/events/{id}/eligibility   may this parcel be surveyed?

GET  /api/v1/jurisdictions             reference:read
GET  /api/v1/calamity-types            reference:read
GET  /api/v1/roles                     reference:read — the matrix itself

POST /api/v1/admin/users               user:create
GET  /api/v1/admin/users               user:read
POST /api/v1/admin/users/{id}/roles    role:grant
POST /api/v1/admin/users/{id}/status   user:update
GET  /api/v1/admin/audit               audit:read
```

An assessment, once signed in:

```bash
curl -X POST localhost:8090/api/v1/assessments \
  -H "Authorization: Bearer $ACCESS_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"farmer_id":"<farmer id>","farm_id":"<farm id>","incident_date":"2025-10-12"}'
```

The two ids come from the registry this deployment is loaded with: any row of
`GET /api/v1/farmers` carries both. There is deliberately no example pair here
-- the records are real farmers and their land.

`sowing_date` is optional — it defaults to the parcel's crop declaration.

## How an assessment is produced

The parcel boundary goes to the analytics engine twice: once for a Sentinel-2
NDVI series over the crop cycle, once for Copernicus DEM terrain. Those figures
and the registry's own record of what was declared are handed to a Gemini agent,
which returns a judgement with reasoning an officer can read and argue with.

**NDVI establishes that something grew, and roughly when.** It is not the
damage signal. Sentinel-2 at 10 m over a one- or two-hectare smallholding
cannot measure how badly a crop was hurt, and a number derived from it alone
would be a guess wearing a decimal point. What it is good for is the fraud
cross-check: a declared crop on a parcel that never rose above bare soil, or a
curve that peaks two months away from the declared sowing date.

**A damage percentage requires ground imagery.** No field photograph, no
figure — enforced in the agent and again in the backend, because the agent is a
separate deployable reached over HTTP and a rollback or a swapped model could
change its behaviour without this service knowing. That duplication is
deliberate.

**Terrain contributes susceptibility, not damage.** Flat ground under a
thriving crop is not a loss, however prone to ponding it is.

**Nothing fast-tracks.** Confidence is capped at 0.60 without ground truth, and
every claim reaches at least standard audit.

On the reference parcel this produces `crop_presence: confirmed`, no damage
figure, and a `crop_type_mismatch` warning — the agent independently spotted
that an October NDVI peak does not match a June-sown soybean.

## What is not real yet

**Geometry and crops are mocked.** AgriStack returns `plot_geometry: null` and
an empty `crop_sown_data` for every farmer reachable on both the Maharashtra
and Uttar Pradesh deployments — verified across 7 parcels and 3 farmers. The
two APIs that do guarantee geometry, `/agristack/crop-survey-data` and
`/agristack/geo-referenced-maps`, return 403 for our participant. See
`mock_files/README.md`.

**Offline capture is not yet proven on a handset.** The queue, the sync
engine and the request contract are verified against the live API; IndexedDB,
the camera, the sensors and the speech synthesis need a real phone.

**Ground capture works; a real photograph is still needed.** The chain is
proven end to end — capture, geofence, into the agent, confidence cap lifted.
What it has not yet seen is an actual photograph of a damaged field. Shown a
synthetic test image the agent says so and refuses, which is the guardrail
working rather than a gap: a model that produced a confident percentage from a
solid square would be exactly the failure this design exists to prevent.

**No SAR.** Optical NDVI cannot see through cloud. On the reference parcel a
110-day kharif window yielded four usable passes with a 60-day gap — if a
cyclone had landed inside it, the assessment would have had nothing to say.

**No SMS gateway.** Farmer one-time codes are written to the service log.
`ESURVEY_ENV=production` refuses to start while that is true.

**Circles are almost entirely unmapped.** The LGD hierarchy is loaded in full
— 35 districts, 358 talukas, 44,911 villages — but circles have no LGD code
and exist only in district revenue records. 357 of 358 talukas have none, so
no Circle Officer can be posted in them and claims there fall through to the
Tehsildar. See `reference_data/README.md`.

## Tests

```bash
cd backend && go test ./...
cd ai-services && uv run pytest
cd web-platform && npx tsc --noEmit && npm run build
```

The Go suite covers the token parser against algorithm confusion and tampering,
the permission matrix against the separation-of-duties rules above, and the
assessment guardrails against the "damage without ground imagery" case. The
backend image runs `go vet ./... && go test ./...` during the build: a
container that cannot pass its own authorization tests should not reach a
registry.
