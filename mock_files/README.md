# Mock AgriStack data

Stand-in for AgriStack responses while the live API cannot supply plot geometry
or crop-sown data. Structured to match the **real** response byte-for-byte in
shape, so code written against these files runs unchanged once AgriStack starts
returning the missing fields.

## Why this exists

Verified against live MH and UP deployments on 2026-08-29:

| Field | Live result |
|---|---|
| `plot_geometry` | `null` on **7/7 parcels** (3 farmers, 2 states) |
| `ulpin` | `null` on 7/7 parcels |
| `crop_sown_data` | `[]` on 3/3 farmers, even unfiltered |

`i1004:o1007` returns demographic and RoR land attributes correctly, but does
not carry geometry or crops on any state deployment. The two APIs that declare
geometry as a **required** field — `/agristack/crop-survey-data` and
`/agristack/geo-referenced-maps` — return **403 Forbidden** for our participant
under both Basic and Bearer auth. (The participant id is part of the AgriStack
credentials and is kept with them, outside this repository.)

So everything here is real except `plot_geometry` (field-traced) and
`crop_sown_data` (synthetic — see below).

## Files

| File | What it is |
|---|---|
| `trial-*.i1004_o1007.json` | Registry records for a trial. Git-ignored: real farmers, real mobile numbers, real land. Not in this repository, and not to be put in it. |
| `parcel_453_malegaon.polygon.geojson` | Closed GeoJSON `Polygon` from the field trace of survey 453 — kept as the worked example the notes below are about. |
| `parcel_453_malegaon.source.geojson` | The raw traced `LineString`, exactly as supplied. Kept for provenance. |

**The single curated record that used to sit here has been removed.** It was
one farmer's captured on-seek response with a hand-added boundary, from when
no live response carried geometry. A real export now does: the records loaded
for the Malegaon trial carry `plot_geometry` on every parcel, so the platform
no longer needs a stand-in -- and keeping one meant two different answers for
the same farmer, with the loader picking whichever file sorted last.

## The parcel the notes below are about

Survey 453 in Malegaon (village 562111, Ta. Barshi), traced in the field and
recorded at 1.6 hectares of owner extent against a 4.82 ha polygon. The
identifiers are left out here for the same reason the trial records are: they
belong to a person.


## Two things to know before using this

**1. The source is a `LineString`, not a `Polygon`.** As supplied it is an open
path of 13 vertices whose first and last points differ:

```
first  [75.813582,  18.1727367]
last   [75.8136012, 18.1727167]
```

A LineString cannot be used for point-in-polygon. `parcel_453_malegaon.polygon.geojson`
and the `plot_geometry` in the JSON both close the ring (14 vertices, first == last)
to form a valid `Polygon`. Use those, not the source file.

**2. Traced area is ~3x the recorded owner extent.**

```
traced polygon  4.8202 Ha
owner_extent    1.6    Ha
```

The most likely reading: the trace covers **all of survey 453**, whereas
`owner_extent` is *this owner's share* of it — the record lists `joint_owners`
under khata 1454. That distinction matters for the platform:

* **geometry is per survey number** — use it for geofencing, DEM and satellite
  zonal statistics
* **`owner_extent` is per owner** — use it to apportion the assessed loss and
  compute the payout

Do not assume polygon area equals the insurable area. Confirm the intended
semantics before the actuarial engine relies on it.

## The synthetic crop record

`crop_sown_data` is **entirely invented** — AgriStack returned `[]` for every
farmer tested. Field names and types follow the `o17` schema, which is the
closest published definition of this array.

```
crop_name          Soybean
sown_area          1 + "6" HECTARE   (= 1.6 Ha, matching owner_extent)
season / year      Kharif / 2025-2026
sowing_date        24/06/2025
irrigation_type    Non Irrigated
```

Sowing date comes from the eSurvey spec's phenology table for soybean —
start-of-season Day 175 (mid-June), peak Day 235, harvest Day 285, peak NDVI
0.78 ± 0.06. Day 175 of 2025 is 24 June, so the date is agronomically coherent
with the NDVI baseline the assessment engine will fit.

**Three values you must not trust** (about the synthetic crop record that the
removed file carried; a live export supplies real ones):

1. **`crop_code` `0201001` is a guess.** The schema wants a real 7-digit code
   from the central crop registry. This one is a placeholder — replace it
   before anything keys off crop codes.

2. **`sown_area_decimal` encoding is ambiguous.** The spec gives
   `sown_area_integer: 2` with `sown_area_decimal: "30"`, while the CropSurvey
   schema elsewhere uses `"00006"`. Whether that reads as `2.30` or `2.00030`
   is not stated. This mock encodes 1.6 Ha as integer `1` + decimal `"6"`.
   Confirm the real convention before the actuarial engine parses areas — the
   two readings differ by orders of magnitude.

3. **`irrigation_type` is set to `Non Irrigated`.** The spec's own field is
   inconsistent: `crop_sown_data.irrigation_type` has example `Borewell` (an
   irrigation *source*), while `CropDetails` splits them into `irrigation_type`
   (`Irrigated` / `Non Irrigated`) and `irrigation_source` (the Borewell list).
   Kharif soybean in Barshi is typically rainfed, hence the choice here.

Note the crop is 1.6 Ha — the **owner's** extent, not the 4.82 Ha traced
polygon. That is deliberate and consistent with the area caveat above.

## Geometry format

`plot_geometry` follows the AgriStack schema — an object with `type` and
`coordinates`:

```json
"plot_geometry": {
  "type": "MultiPolygon",
  "coordinates": [[[ [lon, lat], ... ]]]
}
```

Note this is **correct GeoJSON nesting** (MultiPolygon -> polygons -> rings ->
positions). The AgriStack spec's own inline example is malformed — it nests one
level too shallow. Follow the file here, not that example.

Coordinates are `[longitude, latitude]`, WGS 84 / EPSG:4326.

## Swapping back to live

Nothing here is loaded automatically. When AgriStack starts returning geometry,
point the loader back at the live seek response — the JSON structure is
identical, so only the source changes. Re-verify with:

The AgriStack login is **not in this repository and must never be put in it**:
a credential committed once stays in the history after the line is deleted.
Export it from wherever your AgriStack credentials are kept, then run:

```
cd /opt/agristack-ref && sudo -u dh4wal env \
  CALLBACK_URL=https://agristack.agribid.ai/agristack/on-seek STATE=MH \
  ./venv/bin/python phase2_v1.py 56567763672 "" "" \
  "$AGRISTACK_USER" "$AGRISTACK_PASSWORD" i1004:o1007
```

If `plot_geometry` is non-null there, retire this mock.
