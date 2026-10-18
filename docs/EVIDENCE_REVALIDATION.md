# The farmer's on-site journey and the evidence it produces, revalidated

2026-09-16. The sync work made the claim reach the office; the scale work made
it do so at volume. Neither asked the older question: **when a farmer stands in
their field and does what the app tells them, does the platform end up holding
evidence that supports a damage figure?**

This is that check, read against the code as it stands today -- not against
what the design documents say was intended.

---

## What the farmer actually does

1. Home, or the New report tab -> Farmer ID -> the registry record.
2. The parcel is chosen; the app fetches that parcel's four assigned points
   while it still has signal, and carries them into the field.
3. The geofence bites before the form: outside the parcel plus 25 m, the form
   does not open (`parcel_location.dart`, `file_report_screen.dart`).
4. The form: crop, sown area, affected area, a damage estimate, the
   declaration. Saved as they type; **the draft enters the send queue the
   moment it is filed**.
5. The walk (`station_walk_screen.dart`): for each of four points -- spoken
   direction and a live distance, "arrived" within 12 m, then one still and a
   15-second 720p clip, audio off. A point that cannot be reached is moved to
   where the farmer stands, with the offset recorded.
6. Everything queues: photographs first, then the report, then the clips.

That flow is intact and works as described. The findings below are about what
happens to the evidence afterwards.

---

## Findings

### V1. Three of the four station clips are never measured (critical)

`vision.Client.findVideo` picks **one** clip per report -- the most recently
captured -- and measures that. The other three are recorded, uploaded over
whatever connection the farmer has (about 12 MB each, on 2G), stored, and
never read by anything.

The whole premise of directed sampling is that a figure rests on four points
across the field rather than on the corner the farmer chose. What the
specification calls the heaviest input in the fusion is, in practice, one clip
from one point -- and, because `findVideo` takes the latest, usually the last
station walked.

`internal/vision/vision.go:164`.

### V2. The document never says the farmer was directed (critical)

`dossier.Inputs.Sampling` and `dossier.SamplingCoverage` exist and render
properly. **Nothing populates them.** `assessmentDossier` fills in
denominators, thermal, moisture, radar, ground vision and the rest, and leaves
`Sampling` empty, so the section never appears.

Nor are the four points ever frozen against the report:
`report_sampling_stations` is written by `survey.Freeze`, which the app's flow
never calls -- the app fetches parcel-level points and walks them. So there is
no record that the points were fixed before the walk began.

Together, the sentence that answers a selective-evidence challenge at appeal --
"the farmer was directed to these four points, and here is what came back from
each" -- cannot be made from what is stored.

`internal/api/dossier_handlers.go`, `internal/survey/stations.go:178`.

### V3. Clips are sent to the model as photographs (critical)

`groundImagery` walks every capture on the report, calls `DataURI`, and appends
each as `ai.ImageFieldPhoto` with the caption "Farmer-captured field
photograph". It does not filter by media type, so a 12 MB clip becomes a
base64 data URI in the panel request; the AI service passes whatever mime it
finds straight through as an inline part.

Three consequences, in order of seriousness:

- `HasGroundImagery` treats it as ground imagery, and ground imagery is what
  unlocks a damage figure. A figure can therefore be unlocked by a clip the
  panel was never designed to read.
- The prompt carries tens of megabytes per assessment: cost, latency, and a
  provider that may simply refuse.
- The caption is wrong, which is exactly the kind of mislabelled evidence this
  codebase is otherwise careful about.

`internal/api/server.go:1523`.

### V4. A claim still reaches the office before any evidence (high)

The decision recorded on 2026-09-14 was that **all** evidence is required
before a claim can be sent. That is Phase 3 of FARMER_JOURNEY_PLAN.md and has
not been built. Today the draft is queued at "File", so:

- a farmer interrupted between the form and the walk has already filed a claim
  with nothing behind it;
- the office cannot tell "evidence still coming" from "evidence never came" --
  nothing marks a report evidence-incomplete, and `stations_captured` (below)
  is not shown anywhere.

### V5. Station clips skip the incident-window gates (high)

`beginUpload` builds its `survey.New` without `IncidentAt`, `WindowStart` or
`WindowEnd`, so `GateAfterIncident` and `GateWithinWindow` never run for video.
The photograph path (`capture_handlers.go:92`) sets all three.

So the strongest evidence in the system is the only evidence never checked
against the date of the calamity it is offered for.

### V6. "Cannot reach here" is unbounded on the phone (high)

One tap sets `arrived: true` wherever the farmer is standing, with no check
that they are inside the parcel. The server's geofence still refuses the
upload -- so the evidence is not accepted -- but the refusal arrives after the
walk, and the app records that clip as permanently failed. The farmer has
walked, filmed, and lost the station without being told at the moment it
mattered.

The offset is recorded, which is right; the missing half is the immediate
refusal on the handset.

### V7. A station counts as covered on video alone (medium)

Both the trigger (`stations_captured`) and the phone's own progress
(`stationsWithVideo`) count a station done when its clip exists. The still is
taken first, so the pair is usually complete -- but a still that failed to save
leaves a station recorded as covered with no photograph, and nothing says so.

### V8. Nobody can see the coverage that is recorded (medium)

`crop_loss_reports.stations_captured` is maintained by a trigger and read by no
Go code, no API and no console screen. An officer opening a claim cannot tell
whether it rests on four points or one.

### V9. A one-second clip counts as a station (medium)

The app records fifteen seconds, but nothing server-side requires it. The
upload accepts any clip inside the size limit, and `duration_ms` is recorded
but not checked. A truncated recording -- an interrupted walk, a handset that
stopped early -- counts as a covered station.

---

## What held up

- The geofence refuses before the form, on the handset and again at the server,
  and mocked positions are refused with it.
- Position, accuracy, heading and time are recorded live per capture, not read
  from file metadata.
- The walk resumes at the point that has no clip (phase 1), so an interrupted
  walk is not restarted from scratch.
- Photographs-first ordering holds: the claim reaches the office on the small
  payload while the clips follow.
- Duplicate evidence is de-duplicated by hash, per report and across reports.
- Every capture carries its gate results, and non-disqualifying failures travel
  to the model as caveats rather than being silently dropped.

---

## What to do, in order

1. **Measure every station clip** (V1), and let the score say how many points
   it rests on.
2. **Populate the sampling section and freeze the points against the report**
   (V2) -- the walk's whole evidentiary value is in that record.
3. **Stop sending clips as photographs** (V3): filter by media type, keep video
   for the vision pipeline, and decide deliberately whether a keyframe should
   go to the panel instead.
4. **Require the evidence before the claim is sent** (V4), which is Phase 3 as
   already decided.
5. **Gate clips against the incident window** (V5) and **refuse an out-of-parcel
   relocation on the handset** (V6).
6. The medium findings: a still and a clip per station (V7), coverage on the
   officer's screen (V8), and a minimum clip length (V9).

---

## Fixed (2026-09-16)

All nine findings are closed. What changed, in the order they were taken.

### V1 -- every station clip is measured

`vision.Measure` returned one clip, the newest, and the assembler scored it as
though it were the walk. It now returns one clip per station (`[]vision.Clip`,
newest per station, capped at `MaxClips = 6`), the assembler scores each of
them, and `groundcv.Combine` folds them into one measurement -- crop-pixel
weighted for the ratios, summed for the counts, so a clip that saw four pixels
of crop does not out-vote one that saw the field.

The score now carries what it rested on: `Stations` (per-point scores),
`StationsMeasured`, and `Spread` -- the range across the points. A field that
is flattened at one corner and untouched at the other no longer reports as its
average with nothing to say so. An unreadable clip is skipped rather than
failing the walk.

### V2 -- the points are frozen to the report, and the dossier says so

Two halves. The handset's directed points were computed for the walk but never
written against the report, so nothing afterwards could say where the farmer had
been sent: `survey.RecordDirection` now freezes them on the first station
capture (and only if not already frozen -- a later capture does not move them).
And the dossier's sampling section, which had existed empty since it was
written, is populated from `Captures.CoverageFor`.

### V3 -- clips are no longer sent as photographs

`api.groundImagery` sent every capture to the assessment panel, video included,
where a clip was handled as a still. It now skips anything whose content type
is video: the clips are the vision pipeline's input, and the panel is given
photographs. A deliberate keyframe hand-off remains available and was not
taken -- the vision score already carries what the keyframes found.

### V4 -- the claim waits for its evidence

The decision of 2026-09-14 was that everything is required before a claim is
sent. A draft is now filed as `holding` rather than `pending`: it is on the
phone, in the farmer's own list, and out of the send queue -- and its
photographs are out of it too. The walk ends at a new review screen rather than
at "done"; that screen lists what will go, names what is missing, and its send
button exists only when the evidence is whole. `releaseDraft` is what "send"
means.

Nothing is lost by waiting: the claim survives on the phone exactly as before.
Waiting changes when the office hears about it, not whether the farmer's work
survives.

### V5 -- clips are gated against the incident window

`finishUpload` did not know when the loss happened, so a clip was never tested
against it: a recording from before the event was evidence of the event. It now
reads `occurred_at` and the event's `incident_start`/`incident_end` alongside
the farm id, and the clip goes through the same gates a photograph does. A clip
dated before the incident fails `after_the_incident`, which is disqualifying,
and is not offered to the panel.

### V6 -- the handset refuses an out-of-parcel relocation

"Cannot reach this point" moved the point with no bound, so a farmer could
relocate a station into a neighbour's field. The journey now carries the
parcel's outline (`Journey.shape`) into the walk, and the relocation is refused
-- on screen and aloud -- when the farmer is standing outside their own parcel.

### V7 -- a point is a still and a clip, on both sides

The handset asks for a photograph and then a clip at each point and does not
call the point done until both are on the phone (`Store.stationsComplete`). The
server counted the point on the clip alone, so the two disagreed -- and with the
counter now on the officer's screen (V8) the generous number was the server's.
Migration 0039 recounts `stations_captured` with the handset's rule and
recounts existing rows.

### V8 -- coverage is on the officer's screen

`capture_count` and `stations_captured` had been maintained by a trigger since
the first walk and read by nothing. They now travel on both the report and the
worklist summary, and three places show them:

- the approval queue flags a claim whose walk is short, because a batch is
  approved without opening anything;
- the decide dialog says what the figure rests on, and notes a short walk as
  context rather than as a refusal -- a farmer whose phone died on the third
  point has a real claim with three points behind it;
- the evidence panel counts the points off the captures themselves, labels each
  tile with the point it came from, and shows the offset and clip length.

The panel also plays the clips, which it previously rendered as broken images
-- half the directed walk is video and an officer could not watch any of it.
Clips load when their play button is pressed, not with the grid: four clips of
several megabytes each would have spent the officer's connection on video
nobody asked for.

### V9 -- a minimum clip length

`survey.MinClipMs = 5000`, refused in `BeginUpload` when the handset declares a
shorter clip. A truncated recording is no longer a covered station.

---

## Verified

Full sweep, all green: 866 Go tests (21 skipped -- scale probes and cloud-only),
283 Flutter tests, `flutter analyze` clean, `tsc --noEmit` and `eslint` clean,
137 ai-services and 59 satellite-intelligence tests, `ruff` clean on both.

New tests worth naming: the combine weighting and its no-crop-pixels refusal;
the frozen points surviving later evidence; a pre-incident clip not reaching the
panel; held claims and their photographs staying out of the queue; and a point
counting only once it has both a still and a clip.
