# The farmer app, reworked

Decisions taken 2026-08-31. This document is the agreed shape of the change;
it records what was chosen, and the consequences that come with each choice.

Five things change: the Farmer ID enters the workflow, the geofence starts
refusing, video joins the evidence set, the app gets a bottom navigation, and
-- the one that changes what an assessment is worth -- the app stops accepting
whatever evidence is offered and directs the farmer to four points across
their own field.

---

## 1. Sign-in stays exactly as it is

Mobile number, then a one-time code. Unchanged.

This matters more than it looks, because it is what makes everything in §2
safe. By the time a farmer types a Farmer ID, they have already proved they
hold the SIM registered against a farmer record. The Farmer ID is then a
*selector*, not a credential -- and it never has to be treated as a secret,
which is good, because it is printed on documents and known around the
village.

---

## 2. The Farmer ID, inside the workflow

The first step of filing a report becomes: enter your Farmer ID. The registry
record is fetched and the parcel, crop, extent and sowing date are filled in
from it, so the farmer confirms rather than types.

Backed by `agristack.FileSource.FarmerRecord(id)`, which already exists: it
reads every `.json` in `mock_files/`, re-reads the directory when it changes
so a record can be dropped in mid-trial without a restart, and isolates a
malformed file instead of failing the whole registry.

### The authorization rule

`GET /api/v1/farmers/{farmerID}` is officer-only today (`PermFarmerRead`), and
it must stay that way. A farmer needs a different route, because the obvious
implementation -- let a signed-in farmer fetch any ID -- would hand the
personal data of every farmer in the district to anyone who can read a
neighbour's paperwork.

The rule that closes it, using the mobile number the registry already carries:

> A farmer may load the record whose `mobile_no` matches the number they
> verified at sign-in. No other record, whatever ID they type.

There is nothing to guess and nothing to brute-force: you can only reach the
record that would have sent you the code you already answered. A farmer who
types a neighbour's ID gets the same answer as one who types nonsense.

`mobile_no` is normalised on both sides before comparison -- the registry has
a bare ten-digit number, the session may hold `+91`.

### What reaches the handset

A projection, not the record. The app needs:

    farmer_name, owner_name_ror (Marathi, for display)
    village / sub-district / district LGD codes
    farm_id, survey_number, sub_survey_number
    owner_extent + area_unit
    plot_geometry
    crop_name, season, year, sowing_date, irrigation_type

It does not need, and does not get: `aadhaar_hash`, `aadhaar`, `dob`,
`gender`, `caste_category`, `khata_number`, `joint_owners`. None of it is used
by any farmer-facing screen, and a borrowed handset is the wrong place for a
caste category to be cached.

---

## 3. The geofence refuses

**Decided: block outright.** Outside the parcel plus 25 m, the camera does not
open and no report can be filed.

`capture_geofence(farm_id, lon, lat)` already computes this, and the 25 m
tolerance is unchanged -- consumer GNSS is good to 5-15 m under open sky and
worse under canopy, so a farmer photographing from their own bund must not be
refused.

Three things follow from choosing to block.

**The check happens twice.** On the handset, so refusal is immediate and does
not cost an upload; and again on the server at submission, which is the one
that counts. A client-side fence alone is a suggestion.

**Mock locations are refused too.** A fence that blocks honest farmers and
waves through anyone who installed a location-spoofing app would be worse than
no fence, because it would look like a control. `Position.isMocked` is
recorded on every capture and refuses alongside the distance check. Nothing
records this today; it is new.

**A parcel with no boundary cannot be fenced, so it must be traced first.**
This is the consequence that needs handling rather than noting. Live AgriStack
returns `plot_geometry: null` for every farmer we can reach -- the mock record
has a polygon precisely because it was obtained out of band. Blocking on a
fence that does not exist would mean no real farmer could ever file.

So the flow is: no boundary → walk the boundary (`boundary_screen.dart`, which
exists) → the fence applies from then on. A self-traced boundary is weaker
evidence than a registry one and is already marked as such by the existing
provenance field; officers see which kind they are looking at.

### What the fence does and does not prove

Worth writing into the report rather than leaving implied. This parcel is a
strip roughly 100 m x 630 m. With +/-15 m GNSS and a 25 m buffer, the fenced
area necessarily overlaps the neighbouring survey number. The fence
establishes *"at this parcel or immediately adjacent to it"*. It does not
establish which side of a shared boundary somebody stood on, and the
assessment report should not imply that it does.

---

## 4. Video, required

**Decided: photographs and video are both mandatory.**

Vision models can read a walk-through in a way a still cannot: extent across
the plot, whether damage is uniform or patchy, lodging direction, standing
water. That is the reason for the choice and it is a good one.

Capture limits, chosen to keep the file as small as the evidence allows:

| | |
|---|---|
| Duration | 15 s maximum, hard stop |
| Resolution | 720p |
| Codec | H.264 / AAC in MP4 |
| Size per clip | ~12 MB at a typical 6 Mbps handset encode |
| Per report | 4 clips, one per station of §7 -- about 48 MB |

`survey_captures.content_type` has a CHECK constraint admitting only
`image/jpeg`, `image/png` and `image/webp`. A migration widens it to include
`video/mp4`, and `capture_kind` gains a video member.

### Resumable upload is now required, not backlog

This is the consequence of requiring video, and it is the largest piece of
work in the change.

Five photographs are about 2 MB. Four videos are about 48 MB -- roughly
twenty-five times the entire evidence payload as it stands, on 2G and 3G, from
a field. Around 26 minutes on rural 3G, and over two hours on 2G.

The upload path today sends a capture in one request: a connection that drops
at 80% starts again at zero. With 48 MB of required video that is not a slow
upload, it is a report that never completes, retried until the attempt budget
runs out.

So the change includes chunked, resumable upload with per-chunk
acknowledgement, and the queue records progress so a resumed pass continues
rather than restarts.

**Upload order: photographs first, then the report, then the videos.** The
farmer's claim reaches the office on the small payload; the video follows,
clip by clip, over however many hours it takes. The report is marked
evidence-incomplete until all four land, which is what the decision asks for,
but the claim itself is not sitting on a phone waiting for 48 MB to move.

---

## 5. Bottom navigation

Four tabs. The app has no navigation at all today -- every screen is pushed
onto a stack from Home.

| Tab | Marathi | Screen |
|---|---|---|
| Home | मुख्यपृष्ठ | `home_screen.dart` |
| New report | नवीन नोंद | `farmer_id_screen.dart` → `file_report_screen.dart` |
| My reports | माझ्या नोंदी | reports list |
| Help | मदत | `help_screen.dart` |

Appeals are not a tab. A farmer objects to a *decision*, so the objection
belongs on the report that carries it, where they are already looking at the
figure they disagree with -- not in a section they have to know to visit.

State is kept per tab, so a half-filled report survives a glance at Help.

---

## 6. Voice guidance

`Voice` and `VoiceButton` exist, offline Marathi TTS is verified on the test
handset, and the on/off preference already persists.

What changes: each screen speaks its purpose on first arrival, then falls
silent on subsequent visits and waits to be asked. Speaking every time would
train farmers to reach for the mute button, and a voice that has been muted
helps nobody. The existing preference still overrides everything.

Capture is the exception and speaks every time -- it is the step done standing
in a field, often one-handed, and it is where a farmer is most likely to be
looking at the crop instead of the screen.

---

## Consequences accepted

Two, recorded so they are not rediscovered later as surprises:

1. **Blocking on the geofence means a wrong registry boundary stops a real
   claim.** Mitigated by boundary tracing, not eliminated. A farmer whose
   traced boundary is also wrong has no route through the app.

2. **Requiring video means the slowest connection sets whether a claim
   completes.** Mitigated by resumable upload and by sending photographs
   first, not eliminated. On a bad enough connection the report is filed and
   stays evidence-incomplete.

---

## 7. Directed sampling, so the evidence is not chosen by the claimant

The problem this solves: a farmer who films only the waterlogged corner of a
parcel makes a 30% loss look like an 80% one. Not usually fraud -- the wet
patch is what feels worth showing -- but the assessment is only as
representative as the evidence, and evidence chosen by the person being
assessed is not representative of anything.

So the app stops accepting whatever is offered and starts saying where to
stand.

### Four stations, placed by the server

Computed when the report opens, from the parcel polygon and the terrain
already held on the assessment, and sent to the handset as part of the report.

Server-side rather than on the phone for one reason that matters more than the
others: the report can then state that the farmer was *directed* to these
points rather than choosing them. That sentence is what answers a selective-
evidence challenge at appeal, and it is only true if the points were fixed
before the walk began.

    station 1   the low end
    station 2   mid-parcel, one side
    station 3   mid-parcel, the other side
    station 4   the high end

Stations 2 and 3 sit on opposite sides of the parcel's long axis, which is the
"both sides of the farm" requirement: a strip 100 m wide sampled only down its
spine tells you nothing about either flank.

### How the order is decided

`Aspect.CardinalDirection` is already computed by the satellite service and
already persisted as `terrain_aspect`. Aspect is the direction the slope
faces, which is the downhill direction, which is where water collects. So the
low end is the downhill end and the high end is the opposite.

Where there is relief, stations run downhill-to-uphill, and station 4 is the
high ground a farmer would not otherwise have filmed.

Where there is not -- and around Barshi there often is not, a metre of relief
across a whole parcel is common -- the same four stations are spaced along the
parcel's long axis instead. No separate mode, no separate explanation: the
terrain reorders the stations when there is terrain to speak of, and the
geometry places them when there is not.

Every station is forced to a point inside the polygon rather than inside its
bounding box; on a strip parcel the two are very different places.

### Walking it

Guidance is cardinal direction plus a live distance countdown, spoken in
Marathi. Built in `station_guide.dart` (the geometry, unit-tested without a
handset) and `station_walk_screen.dart` (the walk itself).

    "उत्तरेकडे ३० मीटर चालत जा."
    "अजून १५ मीटर."
    "थांबा. इथे व्हिडिओ काढा."

Cardinal-plus-distance rather than turn-by-turn on purpose. Heading from a
handset is course-over-ground and needs the farmer to already be moving, and
the magnetometer is frequently uncalibrated -- a confident "turn left" that is
40 degrees wrong is worse than a direction and a number that the farmer
resolves by looking at their own field. The countdown is what does the real
work, and it is correct regardless of which way anybody is facing.

### At each station

15 s of video at 720p, plus one photograph. Four stations is 48 MB of video,
which is around 26 minutes on rural 3G and the reason §4's resumable upload is
not optional.

### When a station cannot be reached

Standing water too deep to wade, a bund, a fence, a neighbour's crop. One tap
-- "इथून पुढे जाता येत नाही" -- and the station moves to where the farmer is
actually standing. No reason is asked for; it would be one more prompt in a
field for something the numbers already show.

The relocated point still has to pass the geofence of §3. A station cannot be
relocated out of the parcel, which is what would otherwise turn this into the
way around the fence.

**The offset is recorded even though the reason is not.** The report shows
each station as captured, or captured *n* metres from where it was assigned.
An officer reading "station 4 captured 40 m short" knows exactly what they are
looking at and can weigh it, without the farmer having been made to justify
themselves while standing in water.

### What this gives the report

A sampling-coverage section in the dossier: the four assigned points, what was
captured at each, and how far from its assigned point. Where every station was
met, the report can say the parcel was sampled at four directed points across
its length and both flanks -- which is a materially stronger statement than
"the farmer supplied five photographs", and it is the statement that survives
an appeal.


---

## 8. What was built

Backend, migrations 0017-0019:

    survey_captures      + station_no, station_offset_m, duration_ms, is_mocked
                         content_type widened to video/mp4, webm, quicktime
    report_sampling_stations   the four frozen points, and the boundary
                               version they were frozen against
    capture_uploads      resumable upload state
    crop_loss_reports    + stations_captured

    parcel_sampling_stations(farm_id, downhill_deg)
        PostGIS. Oriented envelope for the long axis, worked in UTM so the
        flank offset is a real distance. Verified on the Malegaon reference
        parcel: four points inside the polygon, ~160 m apart down a 630 m
        strip, flanks ~51 m apart, and the low end following the aspect.

    GET  /api/v1/me/registry/{farmerID}          the mobile-match rule
    GET  /api/v1/reports/{reportID}/stations
    POST /api/v1/reports/{reportID}/uploads
    GET  /api/v1/reports/{reportID}/uploads/{uploadID}
    PUT  .../uploads/{uploadID}/chunks/{index}

App:

    ui/shell.dart                  four tabs, state kept per tab
    report/farmer_id_screen.dart   the ID step
    report/my_reports_screen.dart  the reports tab
    capture/station_guide.dart     bearing, compass, spoken distance
    capture/station_walk_screen.dart   the directed walk
    core/uploader.dart             resumable client

### Two behaviours worth knowing about

**The geofence now refuses rather than flags.** `survey.Submit` returns
`ErrOutsideParcel` or `ErrMockedPosition` *before* the media is stored, so a
refused capture leaves nothing behind. A parcel with no boundary is still not
fenced -- that gate is skipped rather than failed, and the distinction is
tested, because "there was nothing to check against" and "it was checked and
was fine" are different things to put in front of an officer.

**Video was already half-supported.** `survey.acceptedMedia` has carried
video/mp4, webm and quicktime all along; only the database CHECK constraint
refused them. Migration 0017 widened it to mp4 alone, which would have refused
every iPhone -- 0019 fixes that, and moves the station counter to
`content_type LIKE 'video/%'` so an iPhone walk is not counted as no coverage.

### Not built

- The station clips are handed to a callback; wiring them into the sync queue
  alongside the existing photograph queue is the remaining piece.
- `report_sampling_stations` holds one set per report. A report naming several
  survey numbers gets stations on the first, and the report says which.
