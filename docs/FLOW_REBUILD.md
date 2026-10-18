# The survey flow, rebuilt

Raised 2026-09-01, after using the app on a handset 600 km from the parcel and
finding that none of the directed-sampling work was reachable.

## What was wrong

The backend was built and tested. The screens were built and their logic was
tested. Nothing connected them.

    station_walk_screen.dart   never referenced   dead code
    uploader.dart              never referenced   dead code
    GET /reports/{id}/stations never called by the app
    report flow                file_report_screen.dart:238 -> CaptureScreen

So: no video, no walking guidance, no terrain — because none of it was
reachable from any screen a farmer can get to. The report flow still ended in
the old photographs-only capture.

Worse, this was reported as "verified on real hardware". What had been verified
was the Farmer ID screen and the bottom navigation. The flow behind them had
never been run.

### The ±9 m

That is the handset's GNSS accuracy — how tightly it knows where *it* is. It
says nothing about where the field is, and the capture screen never compared
the two. There was no distance-to-parcel check anywhere in the app.

The geofence existed only at `survey/capture.go`, server-side, at submission.
Six hundred kilometres away the app would let a farmer fill the form, take five
photographs, and only then have the server refuse them — the most expensive
possible place to say no.

## Decisions

**Testing away from the field: a debug-only override.** The fence makes the
survey untestable from anywhere but the parcel. `kDebugMode` is a compile-time
constant, so in a release build the branch is dropped: the override is not
disabled in production, it is absent from it. A banner says the position is
simulated, because a pretend position that looks real is how a demo becomes a
false claim.

**No terrain yet: place geometrically and say so.** The first report on a
parcel has no assessment, so no aspect. Four points along the long axis,
flanks on opposite sides, `placement: geometric`, and the report makes no claim
that either end is lower. The farmer walks now rather than waiting in a field
for an Earth Engine round trip that may not answer.

**The fence bites before the form opens.** Tap "Report damage", the app
compares position to the parcel, and says so immediately with the distance.
Nothing typed, nothing photographed, nothing wasted.

## The architectural correction

`StationWalkScreen` fetched stations by report id. The report does not exist on
the server yet — this app is offline-first, and a draft lives on the phone
until there is signal. A farmer standing in a field with no bars could never
have been told where to stand.

Stations are a property of the **parcel**, not the report: placement is
deterministic given a boundary and an aspect. So:

    GET /api/v1/parcels/{farmID}/stations

The app asks when it fetches the parcel — a step that already needs a network —
and carries the four points into the walk. The server still placed them, still
before the walk began, so the report's claim that the farmer was directed
rather than choosing is unchanged. The report-scoped route stays for freezing a
set against a report and recording which boundary version built it.

## Built so far

    core/parcel_location.dart      distance to the parcel, from the polygon the
                                   registry already sent; 17 tests including
                                   the Pune-to-Malegaon case
    GET /parcels/{farmID}/stations verified live: four points across survey
                                   no. 453, placement "geometric"
    survey.PlaceStations           placement without a report

## Still to wire

    1. Gate "Report damage" on ParcelFix.atParcel, with the debug banner
    2. Fetch stations at parcel selection, store them with the draft
    3. Route the report flow into StationWalkScreen instead of CaptureScreen
    4. Queue the station video and photo through Uploader
    5. Send station_no and the offset with each capture

Nothing in this list is designed-but-unreachable: each step is what makes the
previous one visible to a farmer.
