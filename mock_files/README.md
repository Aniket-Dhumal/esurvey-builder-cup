# Registry data — supplied at run time, not committed

This directory is where the platform reads AgriStack registry records and
parcel geometry from. **It is empty in this repository on purpose.** Point
`ESURVEY_MOCK_DIR` at a directory outside the tree that holds the real export,
or leave it empty — every test that needs registry records skips when it finds
none, and the stack starts either way.

## Why it is empty

A registry record is a name, a date of birth, a mobile number, a khata number
and a list of land holdings. A parcel file is the traced boundary of somebody's
field: a survey number plus its coordinates identifies a household as surely as
the name does. This repository is public. Neither belongs in it, and a file
committed once stays in the history after the line is deleted.

## What the loader expects

| File | What it is |
| --- | --- |
| `*.i1004_o1007.json` | AgriStack `on-seek` responses — the registry record for one farmer, in the live response's own shape |
| `*.polygon.geojson` | A closed GeoJSON `Polygon` per survey number, used for geofencing, DEM and satellite zonal statistics |
| `*.source.geojson` | The raw traced `LineString` as supplied, kept for provenance |

Coordinates are `[longitude, latitude]`, WGS 84 / EPSG:4326. Files sort by name
and the loader takes the last match, so do not keep two records for the same
farmer in the directory.

## Why a stand-in was needed at all

Verified against live MH and UP AgriStack deployments on 2026-08-29:

| Field | Live result |
| --- | --- |
| `plot_geometry` | `null` on **7/7 parcels** (3 farmers, 2 states) |
| `ulpin` | `null` on 7/7 parcels |
| `crop_sown_data` | `[]` on 3/3 farmers, even unfiltered |

`i1004:o1007` returns demographic and RoR land attributes correctly but carries
no geometry or crops on any state deployment. The two APIs that declare
geometry as a **required** field — `/agristack/crop-survey-data` and
`/agristack/geo-referenced-maps` — return **403 Forbidden** for our participant
under both Basic and Bearer auth. So boundaries are field-traced and the crop
record is reconstructed until that changes.

## Four things to know before relying on this data

**1. A traced source is a `LineString`, not a `Polygon`.** As supplied it is an
open path whose first and last points differ, and a LineString cannot be used
for point-in-polygon. The `.polygon.geojson` closes the ring (first == last) to
form a valid `Polygon`. Use that, not the source file.

**2. Traced area is not insurable area.** On the reference parcel the trace came
to 4.82 ha against a recorded owner extent of 1.6 ha. The most likely reading is
that the trace covers the whole survey number while `owner_extent` is this
owner's share of it — the record lists joint owners under one khata. That
distinction is load-bearing:

- **geometry is per survey number** — use it for geofencing, DEM and satellite
  zonal statistics
- **`owner_extent` is per owner** — use it to apportion the assessed loss

Confirm the intended semantics before the actuarial engine relies on either.

**3. `sown_area_decimal` encoding is ambiguous.** The spec gives
`sown_area_integer: 2` with `sown_area_decimal: "30"`, while the CropSurvey
schema elsewhere uses `"00006"`. Whether that reads as `2.30` or `2.00030` is
not stated anywhere, and the two readings differ by orders of magnitude.

**4. `crop_code` must be a real 7-digit code** from the central crop registry.
Anything reconstructed is a placeholder and must be replaced before code keys
off crop codes.

## Geometry format

`plot_geometry` follows the AgriStack schema — an object with `type` and
`coordinates`:

```json
"plot_geometry": {
  "type": "MultiPolygon",
  "coordinates": [[[ [lon, lat], ... ]]]
}
```

That is **correct GeoJSON nesting** (MultiPolygon, polygons, rings, positions).
The AgriStack spec's own inline example is malformed — it nests one level too
shallow. Follow this, not that example.

## Swapping back to live

Nothing here is loaded automatically. When AgriStack starts returning geometry,
point the loader at the live seek response — the structure is identical, so only
the source changes.

The AgriStack login is **not in this repository and must never be put in it**.
Export it from wherever those credentials are kept, then re-verify with the
`i1004:o1007` seek for the farmer in question. If `plot_geometry` comes back
non-null, the stand-in is retired.
