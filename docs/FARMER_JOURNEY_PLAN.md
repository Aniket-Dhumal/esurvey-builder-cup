# The farmer's journey, made reliable

Planning note, 2026-09-14. Follows the sync work: the queue now never loses
work to a network or a failing office. This plan is about everything *before*
the queue and *after* it -- the path a farmer walks through the app, from
opening it to hearing the decision -- so that no step can strand them, lose
what they did, or leave them not knowing where they stand.

---

## 1. The journey, end to end

    0  Open app            onboarding (once) -> sign in (mobile + code)
    1  Prepare             land records, open events, field outline, stations
                           -- everything the field needs, fetched while there
                           is signal, kept on the phone
    2  Start a claim       pick event + parcel; duplicate check
    3  Reach the field     geofence: "you are 340 m away, walk north"
    4  Fill the claim      crop, sown/affected area, damage %, declaration
    5  Evidence            directed station walk (video + photo x4), or the
                           photograph sequence where no stations exist
    6  Review & confirm    what will be sent, what is missing, sign
    7  Sending             queue -> receipt with the office's reference
    8  Follow the claim    timeline: received -> evidence complete ->
                           examined -> decided -> (appeal) -> closed
    9  Act on the outcome  appeal, add evidence if asked, withdraw

A step is reliable when: it works with no signal, it survives the app being
killed, the phone restarting or the battery dying, and the farmer can always
see where they are and what happens next.

---

## 2. What is broken or fragile today (found in the code)

Ordered by harm.

| # | Where | What happens | Harm |
|---|---|---|---|
| G1 | `core/api.dart:240` -> `main.dart _handleSignedOut` | A refused or expired refresh token (30 days, or a revoked session) calls the same path as "Sign out": `clearPersonalData()` deletes the queue. | **An unsent claim and its video are deleted** because a token expired while the farmer was offline for a month. |
| G2 | `home_screen.dart:354 _resume` | Resuming an interrupted claim always opens `CaptureScreen` (photographs), even when the draft was on the station walk. | Directed-sampling evidence is silently lost on any interruption; stations already captured are ignored. |
| G3 | `file_report_screen.dart:358` | The draft enters the send queue the moment "File" is pressed, before any evidence. | A claim with zero evidence reaches the office if the farmer is interrupted or gives up; the officer sees an empty claim. |
| G4 | `file_report_screen.dart:218` | Stations are fetched on the form screen, i.e. usually **in the field**, where there is no signal. Failure -> `_stations = null` -> photograph path, silently. | Most real claims fall back to the weakest evidence without anyone knowing why. |
| G5 | `file_report_screen.dart:254` | No outline -> boundary walk -> `sync.run()` -> re-fetch stations. Needs signal twice mid-journey. | Same as G4; the farmer walks the edge and still gets photographs. |
| G6 | Two entry points | Home "Report damage" uses the session's farmer and `land_data`; the New-report tab asks for a Farmer ID, needs the network, and uses `events.first`. | Different behaviour for the same task; the tab fails offline and ignores all but one event. |
| G7 | Nowhere | No duplicate check: same farmer, parcel, event can be filed twice. | Two claims for one loss; officer rework; looks like fraud. |
| G8 | Form state | Typed values live only in widget state until "File". | App killed mid-form = start again (minor, but it's the step done standing in a field). |
| G9 | After sending | No receipt screen; the reference appears only after sync, only on the list. No change notification; status refreshes only when Home is pulled. | Farmer does not know it arrived, and does not learn the decision until they happen to look. |
| G10 | Pre-flight | No check of storage (~60 MB for a walk), battery, camera/mic/location permission, GPS quality before the walk starts. | Walk fails at station 3 on a full phone. |

---

## 3. Design

### 3.1 One journey, persisted as a state machine

A `journeys` table (one row per claim in progress), written on every step
transition, is the single source of truth for "where is this farmer":

    journey_id (= draft local_id), farmer_id, event_id, farm_id
    stage       preparing | at_field | form | evidence | review | queued
                | sent | abandoned
    form_json   the typed values, saved on every change (debounced)
    stations_json, station_progress  (which stations captured, offsets)
    evidence_mode   stations | photographs, and WHY (no_outline,
                    stations_unavailable_offline, farmer_declined)
    created_at, updated_at

On launch, Home reads it and offers exactly one action: "Continue your claim
for survey no. 453 -- station 3 of 4". Resume goes to the stage recorded, not
to a guessed screen (fixes G2, G8).

Screens become views of the journey row rather than owners of the state.

### 3.2 A claim is sent when it is ready, not when it is started

The draft is **not** queued at "File". It is queued at step 6, when the
farmer confirms. Evidence rules at confirmation:

- complete (all stations, or all photographs) -> send
- incomplete -> **cannot be sent**. The review screen lists exactly what is
  missing ("station 3: video") with one button to go and capture it
  (decided 2026-09-14: all evidence required) (fixes G3)

What keeps "all required" from trapping a claim:
- a station that cannot be reached is relocated to where the farmer stands
  (existing, still inside the geofence), so every station is always
  capturable
- a parcel with no stations uses the photograph sequence, and all of its
  photographs are required instead

Photos-first-then-report-then-video ordering from the redesign is kept once
queued.

### 3.3 Prepare while there is signal

A "Ready for the field" check per parcel, run on Home whenever online:

    land record        cached
    open events        cached
    field outline      registry | traced | missing
    stations           fetched and cached per parcel (+ boundary version)
    storage / battery  enough for a walk

Shown as a small checklist on the parcel card. The claim flow reads only from
the cache, so the field needs no network at all (fixes G4, G6). If the
outline is missing, tracing it is offered **here**, at home, not mid-claim;
after it syncs the stations are fetched in the background (fixes G5).

Where the farmer is already in the field with nothing cached, the flow still
works (photographs), and records `evidence_mode_reason =
stations_unavailable_offline` so the officer knows why.

### 3.4 One entry point: Farmer ID, then every parcel

Decided 2026-09-14. "Report damage" on Home and the New-report tab start the
same flow:

    Farmer ID  ->  all of this farmer's land parcels  ->  pick the one being
                                                         surveyed  ->  event

- The parcel list shows every parcel on the record, each with survey number,
  village, crop, area, readiness (§3.3) and its claim state for the open
  event: *not claimed* / *in progress -- continue* / *sent -- view*.
- A farmer with several parcels files one claim per parcel; each is its own
  journey and they can be in progress side by side.
- The registry answer is cached after the first fetch, so the Farmer ID step
  works offline on a phone that has seen it before.
- Event choice is explicit when more than one is open (fixes G6).

### 3.5 Signing out is a decision; a token expiring is not

- **Farmer taps Sign out**: warn if anything is unsent ("2 claims not yet
  sent will be deleted from this phone"), require confirmation, then clear.
- **Refresh token refused/expired**: keep the queue and the journeys; show
  "Sign in again to send your claims". The queue is paused (not wiped) until
  a sign-in succeeds.
- **A different farmer signs in** on a handset holding another farmer's
  unsent work (decided 2026-09-14): that work **keeps sending in the
  background until the server has all of it, and is deleted from the phone
  only once it has**. The new farmer never sees it -- every screen and query
  is scoped to the signed-in farmer.

  What this requires:
  - every queue row and journey carries its owner's `farmer_id`
  - credentials are kept **per farmer** in secure storage, not one session:
    the queue sends each row with its owner's token, so farmer A's claim is
    never sent as farmer B (the server would refuse it, and should)
  - sign-in of B does not revoke A's refresh token on the phone
  - once all of A's rows are sent, A's photographs, rows and tokens are
    removed from the handset
  - if A's token expires before the work is sent, it is held (still hidden)
    until A signs in on this phone again; the Sign-in screen says "this phone
    has claims waiting for another farmer" without naming them

(fixes G1)

### 3.6 Duplicate guard, per parcel

One claim per (farmer, event, parcel); different parcels are separate claims.
Client: the parcel list (§3.4) already shows the claim state, so a claimed
parcel offers "Continue" or "View", never "Start".
Server: unique open report per (farmer, event, farm_id); a second create
returns the existing reference rather than a second report. Changes go
through the existing revision route (fixes G7).

### 3.7 Pre-flight before the walk

Before station 1: location permission + GPS accuracy under ~20 m, camera and
microphone permission, free storage >= 150 MB, battery >= 15% (warn, not
block). Each failure is one screen with one action (fixes G10).

### 3.8 After sending: receipt and timeline

- Receipt screen at confirmation: "Saved on this phone. It will go by
  itself." -> turns into the office's reference when it lands.
- Claim detail: a timeline from the server's status history plus local
  events (queued, photographs sent 3/4, video 40%).
- Status changes reach the farmer: SMS from the server on the key
  transitions (received, decided), and/or push. *(Decision needed -- §5.)*
  Background sync also refreshes report status, so the app is current when
  opened (fixes G9).

---

## 4. Build order

Each phase is shippable on its own and closes the harms in order.

**Phase 1 -- nothing is lost** (G1, G2, G8)
- Token expiry keeps the queue; sign-in-again banner; guarded Sign out
- Per-farmer credentials; queue rows scoped and sent by owner; another
  farmer's work drains hidden and is deleted once fully on the server
- `journeys` table (migration v5), form autosave, resume to recorded stage
  including the station reached
- Tests: expired refresh keeps queue; kill mid-form/mid-walk -> resume exact

**Phase 2 -- the field needs no signal** (G4, G5, G6)
- Farmer ID -> all parcels list with readiness and claim state; cached
  registry answer
- Per-parcel station cache; outline tracing from the parcel list
- Explicit event choice
- Tests: whole claim filed in airplane mode from a prepared parcel

**Phase 3 -- a claim is sent on purpose** (G3, G7, G10)
- Review & confirm step; sending blocked until all evidence is captured
- Pre-flight checks; one claim per parcel (client + server rule)
- Tests: abandon before confirm sends nothing; duplicate returns existing

**Phase 4 -- the farmer knows where it stands** (G9)
- Receipt, claim timeline, upload progress per claim
- Status refresh in background sync; in-app only (no SMS/push for now)
- Tests: status change reflected after background pass

**Phase 5 -- proof on a handset**
- Scripted field run on the test handset: airplane-mode claim, battery pull
  mid-walk, token expiry, reinstall, borrowed handset. Recorded results in
  this document -- not "verified" until each has run (see FLOW_REBUILD.md
  for why that word is now reserved).

---

## 5. Decisions (2026-09-14)

1. **Borrowed handset**: another farmer's unsent work keeps sending, hidden,
   until the server has all of it; deleted from the phone only then.
2. **Evidence**: everything required before a claim can be sent.
3. **Multiple land**: Farmer ID lists every parcel; the farmer picks the one
   being surveyed; one claim per (farmer, event, parcel).
4. **Status updates**: in-app only for now.

---

## 6. Phase 1 -- built (2026-09-14)

Nothing is lost: G1, G2 and G8 closed.

### An expired login deletes nothing
- `Session.lapse()` (core/session.dart): a refused refresh token clears the
  tokens and keeps who the farmer was. `Api` calls it instead of `end()`.
- `main.dart _handleLapsed`: stops the foreground timer, shows sign-in with
  "your claim has not been sent yet ... sign in again to send it". The queue,
  the claims in progress and the files all stay.

### Every farmer's work is sent under their own name
- Schema v5: `owner_id` on drafts, captures, boundaries, appeals, journeys.
  Rows from before v5 are adopted by the signed-in farmer at startup
  (`Store.adoptUnowned`) -- until now only one farmer's work could be on a
  phone.
- `Store.owner`: every screen query reads only the signed-in farmer's rows;
  `Store.everyone` reads across owners (background wake-up, "is anything
  waiting at all").
- `core/accounts.dart`: signing out with work unsent parks that farmer's
  credentials in the keystore under `parked.<owner>.`. `Session.end()` now
  deletes only its own keys, so parked credentials survive the next farmer.
- `Sync`: the signed-in farmer's rows go with their token, then each parked
  farmer's rows with that farmer's token. When a farmer other than the
  signed-in one has nothing left unsent, their rows are removed
  (`purgeOwnerIfDone`), their credentials forgotten, and orphaned files swept.
  A refused item is kept 30 days so the farmer can see why if they return.
- A parked token the server refuses: credentials forgotten, rows held hidden
  until that farmer signs in on the phone again. Nothing is sent without a
  token that owns it.
- Sign-in of a different farmer clears only cached personal answers (land,
  reports), keeping phone preferences and the engine's own keys.
- The sign-out dialog now says unsent work keeps sending, hidden, and is
  removed once it arrives.
- Background worker drains parked farmers even when nobody is signed in.

### A claim in progress resumes at the exact step
- `journeys` table + `report/journey.dart`.
- The report form saves as the farmer types (debounced), starting from the
  first change; reopening the same event + parcel continues that claim.
- Filing moves the journey to `evidence` and records whether it is the
  station walk or the photograph sequence, with the four points.
- `StationWalkScreen` opens at the first point without its clip; a point is
  done only once its video is saved (the still is written first).
- Home shows one card per unfinished claim: survey number and "points 2/4" /
  "photographs 3/5" / "form not finished", with Continue, and Discard for an
  unfiled form only. A walk found complete is marked done.
- Unsent drafts from before v5 become evidence-stage journeys in the
  migration, so they are offered for completion.

### Tests
`test/owners_test.dart` (lapse keeps data; scoping; parked farmer sent with
own token and purged after; refused parked token holds rows; nobody signed in
sends nothing; purge waits for unsent and recent refusals; v4 -> v5 upgrade),
`test/journey_test.dart` (form saved and restored; created_at stable; same
parcel continues; walk resume point; point done only with its clip;
photograph completeness per stage). Full suite: 269 passing, analyze clean,
debug APK builds.

### Not yet verified on a handset
Kill mid-form, kill mid-walk, token expiry, and a second farmer signing in --
listed for Phase 5.

---

## The offline journey, revalidated (2026-09-16)

The journey was walked end to end against the implementation rather than the
plan. What follows is what it does, and the three things that changed as a
result.

### What needs a signal, and what does not

| Step | Signal |
|---|---|
| Sign in (OTP over SMS) | **Needed, once.** The screen says so. A refresh token then keeps the farmer signed in; an access token expiring offline is not a logout. |
| Home, open events | No. Cache-first, with the age of the answer in words. |
| Farmer ID, pick the parcel | No, once the record has been cached. |
| The form, including "are you in this field?" | No. The parcel polygon came down with the record. |
| The four walk points | No, **as of this revalidation**. See A below. |
| The walk: geofence, arrival, still + clip per point | No. |
| Review and send | No. The claim is released into the queue. |
| Delivery | Whenever a network appears -- app closed, phone rebooted. |

### A. The walk no longer needs a signal at form time

The four points are placed by PostGIS from the parcel boundary, and were
fetched at the moment the farmer picked the parcel -- on the form screen,
standing in the field. No signal there meant no points, and the claim fell
back to the older photograph sequence with nobody told that the heaviest
evidence in the specification had been skipped.

They are now pulled for every parcel on the farmer's record whenever the
record refreshes, and read from the phone when there is no network
(`FarmerData.stations` / `prefetch`). A stale set can send a farmer to
slightly the wrong spot if the boundary was corrected; it cannot put wrong
points into the record, because the server freezes its own placement against
the report on the first capture.

### B. The window is measured on when the farmer filed

`filed_late` was computed from `now()` at insert -- the moment the claim
reached the office. For a counter filing those are the same instant. For this
app they are not, and the gap is the whole point of it: a farmer who filled
the form on the first morning and found a signal on the third day was recorded
as having filed late, on the one flag an officer reads when weighing whether
to believe a claim.

The handset now sends `filed_at`, both instants are kept (migration 0040), and
the window is measured on the farmer's. The clock is recorded rather than
trusted: an instant this server has not reached, or one before the loss
itself, is discarded and the claim is filed on arrival as before.

**The close is measured the same way** (decided 2026-09-16). A claim completed
before the event stopped taking claims is taken however long the queue then
held it. Refusing on arrival meant this system's own delay -- a queue waiting
for a network, or for the rest of the evidence it now insists on -- destroyed
a claim that was made in time, after the app had told the farmer it was safe
and would send itself.

Two deadlines have to be cleared, not one: `survey_closes_at`, which is what
the farmer was shown, and `closed_at`, when the district actually stopped,
which may be earlier. Whichever came first is the one that was real.

Such a claim is recorded as `arrived_after_close` (migration 0041) and shown
as such in the approval queue and on the verify screen. The district closed
its books on a count that did not include it, and this is the one place where
a handset's own clock decides whether a claim exists at all rather than merely
how it is flagged -- so it is said out loud rather than folded in silently.

A claim with no filing instant, or one completed after the close, is still
refused. The deadline did not stop meaning anything; what moved is which
instant it is measured against.

### Also fixed: a paused event no longer destroys a queued claim

A district pausing its own event answered a filing with the same error class
as a refusal, which the handset reads as final: it marked the claim failed,
stopped sending it, and told the farmer the office had said no -- permanently,
over a state the district meant to undo that afternoon. A pause now answers
`EVENT_PAUSED`, which the queue treats as "come back later" (`ErrNotYet`,
`retry.dart`).

### C. The phone reminds the farmer about an unfinished claim

A claim waits for its evidence, so a farmer interrupted between the form and
the walk has a whole claim on the handset that the office will never hear
about. The only thing that would say so was opening the app -- and the
background worker exists precisely because that does not happen.

A local notification now does, a day after a claim was last touched
(`core/reminders.dart`, scheduled through WorkManager with no network
constraint). Nothing leaves the handset, so this stays inside the in-app-only
decision of 2026-09-14. It names no crop, survey number or figure: a lock
screen is read by whoever is holding the phone, which on a shared handset is
often not the farmer whose claim it is.

### Also found

The home screen read the farmer's parcels from `land_data`, which is
AgriStack's own wire format and internal to the backend's registry adapter;
the API returns `parcels`. So the getter was always empty and "Report damage"
on the home screen always answered "your land records have not arrived yet" to
a farmer whose land records were on the screen above it. Filing worked only
through the Farmer ID screen, which reads the same record under the right key.

---

## One number, several farmers (2026-09-16)

### What was already true

Sign-in was already a mobile number and an OTP; the Farmer ID was a separate
step afterwards. What did not exist was more than one farmer behind a number.

### What was in the way

`users.agristack_farmer_id` is a single UNIQUE column, and a farmer's scope was
`Scope{FarmerID: ...}` — one account, one registry identity. The filing path
overwrote whatever `farmer_id` arrived with the account's own, which was safe
while that was the only possibility and quietly wrong the moment it was not: a
second farmer's claim would have been filed against the first, with the
evidence attached to the wrong person's land.

### The rule now

A session may act for a farmer two ways: it is the record whose registered
mobile signed in, or the session holds a **grant** for it (`actingFarmer`).
Naming any other farmer is refused rather than silently rewritten.

A grant is issued by `POST /api/v1/me/registry/lookup`, which takes a Farmer ID
and the date of birth the registry holds. It was decided on 2026-09-16 that the
date of birth is the question, because the strict mobile rule locks out every
farmer whose AgriStack number is one they no longer hold — and those are the
people most likely to need the app.

**What that costs, stated where it is implemented.** A Farmer ID is printed on
documents and known around a village, so the date of birth carries the whole
weight. It is one low-entropy fact a relative or neighbour may simply know, and
in this registry's own sample data it is 1 January. So the route is built as
though it will be guessed at: five wrong answers lock the account out for
fifteen minutes; the counter **fails closed**, unlike the general rate limiter,
because a cache outage that opens an unlimited guessing window is worse than a
farmer waiting a minute; a wrong id, an unknown id and a wrong date answer
identically; and both outcomes are audited.

A grant is an Ed25519 token on the same keyring as the access token, bound to
the account and the farmer, valid ninety days. It is a distinct `typ`, so
neither token can ever be presented as the other. It is never a credential on
its own: it says *which* of an account's farmers is being asked about.

### Why the link is not on the server

It was decided that the office holds no register of which households share a
handset. The list of farmers lives in the phone's own `farmers` table with the
grant beside each one, and a reinstall starts empty. The cost is that a grant
cannot be withdrawn before it expires, which is why ninety days and not longer.

### On the handset

Signing in no longer lands on Home. It lands on the picker: the people this
phone can act for, and **Add another farmer** below them. Picking one scopes
every screen to them; the switcher in the Home app bar comes back here without
costing another SMS.

`Store.owner` says whose credentials send a row. `Store.farmer` says whose
claim it is. They are different questions and the separation is the whole
design: the **screens** follow the farmer, and the **queue does not**. Work is
sent under the account that queued it whoever is on screen, because a claim
that stops sending when somebody switches farmer is a claim that never arrives.

### Also fixed

- `FarmerData.record` called `/api/v1/farmers/{id}`, the officer listing, which
  is guarded by a permission no farmer holds. Every refresh from the app
  answered 403 and fell back to the cache — a record that could be fetched once
  and then never updated again. It now calls the farmer-facing route.
- The record and report caches were keyed per handset, so a second farmer's
  record overwrote the first. Keyed per farmer now.
- A district pausing its own event answered with the same error class as a
  refusal, which the handset reads as final: it marked the claim failed and
  stopped sending it, permanently, over a state meant to be undone that
  afternoon. A pause now answers `EVENT_PAUSED`, which the queue retries.

### Tested end to end (2026-09-16)

`test/offline_journey_test.dart` (19) and `test/farmers_test.dart` (13) walk
the whole offline story farmer by farmer: the session and the farmer choice
outliving the app; each farmer's record, reports and walk points kept apart;
claims, photographs and boundaries invisible to the next farmer while still
sending; and a season's worth of rows (12 farmers, 240 claims) staying
correctly apart. Backend: `lookup_lockout_test.go` against real Redis, and
`acting_farmer_test.go` for who may act for whom.

What the tests found, and what was fixed:

- **Events were not narrowed to the farmer at all.** A farmer holds no
  jurisdiction, so `eventVisible` matches everything and the office sends every
  open event in the state. The home screen said "notified for your area", the
  New report tab took `events.first` without asking, and a claim filed against
  another district's event was filled in, walked, photographed and *then*
  refused for a village it never covered. `eventsForFarmer` now filters by the
  farmer's district, on the phone, so it works with no signal; where more than
  one calamity is open the farmer is asked which.
- **A boundary belonged to nobody.** The column existed and `putBoundary` never
  set it, so every farmer counted every boundary as their own. Worse, sync
  rewrites the row on each status change from the background isolate, where no
  farmer is on screen — which would have erased the attribution on the first
  retry. Both fixed.
- **Offline read as "wrong details".** `lookup` returned null for every
  failure, so adding a farmer with no signal told the farmer their Farmer ID
  and date of birth did not match — sending them to check a document that was
  never the problem. `LookupResult` now separates "could not ask" from "no".
- **An expired grant was invisible.** The office refuses a claim for a farmer
  whose grant has lapsed, so working as them meant gathering a whole claim and
  being refused on sending. The picker now says so on the card and asks for the
  date of birth again first.
- **No index stood behind the farmer scope.** `drafts`, `journeys` and
  `boundaries` are now indexed on (owner, farmer, status); the capture scope
  reaches its farmer through `captures_draft`, which turns that subquery from a
  scan into a lookup.

---

## The claim advances itself (2026-09-16)

Three manual steps stood between a claim arriving and an officer seeing it:
acknowledgement, requesting an assessment, and an allocation the console could
not even ask for — `api.allocateReport` existed and no screen called it. On a
quiet week that is a delay; on the evening a district takes ten thousand
claims it is the system not working.

### The pipeline

    complete receipt → acknowledged (system; the reference is the tracker)
                     → assessment queued, asked for by nobody
                     → assessment ends, either way
                     → allocated to the village pair → assigned

From `assigned` nothing changed: the pair record blind, `Reconcile` compares
them, and the ladder runs from the Circle up.

The claim's own `state` drives the worker rather than a second status column —
two columns that must agree about the same thing eventually disagree, and the
one an officer reads would be the wrong one. The shape is the one this
platform uses for everything that must not be lost: `FOR UPDATE SKIP LOCKED`,
one step at a time, backoff in a column, jittered so a district's claims do not
retry in lockstep.

### What "complete" means

The handset declares `expected_captures` with the claim. It already holds the
claim until the walk is whole (V4), so it knows the full set before it sends
anything. Completion is stamped inside the trigger that already counts
captures: the row is being written anyway, so it is exactly-once with no second
writer racing it.

### The ways it could stall, and what each does instead

- Evidence stops arriving → released after 48 h, flagged `evidence_incomplete`,
  so an officer is told the claim is short rather than counting photographs.
- The assessment fails → the claim goes on flagged. A satellite service having
  a bad week is not charged to a farmer, and the two officers were always the
  ones deciding it.
- The claim names no registry parcel → assessment skipped, not waited for.
- **No Talathi posted** → the claim stops, and the reason naming that village
  is written on it. Posting an officer is enough to unstick it, and there is a
  test that says so.

Two constraints had to be relaxed, both encoding the design that changed.
`acknowledged_has_officer` required a name beside the receipt timestamp; null
now means the system received it. `assessment_jobs.requested_by` was NOT NULL;
null now means the pipeline queued it, rather than putting a name in the audit
trail that decided nothing.

### Progress

`GET /api/v1/reports/{id}/progress` computes where a claim stands. Nothing is
stored: every fact is already recorded somewhere, and a `progress` column would
be a sixth copy — the one that goes stale, on the screen a farmer reads to find
out whether anybody is doing anything.

It returns **keys, not sentences**. The handset renders them in Marathi from
strings it already carries, so the progress screen works with no signal, which
is where it is most likely to be read.

Five stages: received, checked, field, decision, closed. Each carries `done` /
`current` / `pending` / **`attention`** — the last meaning current and *not
moving on its own*, so a stuck claim does not look like one in progress.

**The release gate.** The farmer sees the officers' remarks and the loss
percentage only once **both** village officers have recorded. That is not
presentation: the pair record blind to each other deliberately, and releasing
the first officer's remark early would make the farmer the channel between
them — the blind gone without anybody deciding to remove it.

The farmer's view also drops the officers' names, which rung the claim sits at,
whether the two disagreed, and what is stopping it. All true, none of it
something a farmer can act on, and the first two name people to go and press.

### The farmer's own view (2026-09-16)

`ProgressScreen`, reached by tapping a claim on Home or My reports. Three
things on it.

**The claim number, first and large.** It is the one thing a farmer needs when
they ring the office or walk into it, and it was a line of small type on a card
among several. Selectable, and the voice button reads it out digit by digit.

**The five stages**, in words a farmer recognises rather than the office's own:
"two officers are examining it", not "field verification". Each carries a line
saying what it is waiting for where there is something useful to say — "one of
the two has recorded, the other is still to".

**The officers' remarks and the figure**, once both village officers have
filed. Before that the screen says so rather than showing nothing: an empty
space is read as an empty claim.

The app holds every sentence. The office sends `received`, `field`,
`awaiting_second_officer`; the screen turns them into Marathi. So a farmer
standing in their own field with no signal reads exactly what they would read
in an office, from the cached copy of the last answer — which is where this
screen is most likely to be opened.

The figure is never shown without the line beside it: *this is not an amount of
money, and it can still change*. A percentage next to a claim reads as money to
anybody who has been waiting for money.
