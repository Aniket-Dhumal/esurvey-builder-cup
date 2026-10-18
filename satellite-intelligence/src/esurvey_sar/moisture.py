"""Canopy water and surface soil moisture over a parcel.

# Two measurements, because either alone is ambiguous

  NDMI   (B8A - B11) / (B8A + B11), Sentinel-2 at 20 m. Leaf water content.
         It falls when a crop is short of water, and it falls days to weeks
         before NDVI does -- a plant closes its stomata and loses turgor
         before it loses chlorophyll. That lead is why it is worth having.

  Soil   Sentinel-1 VV backscatter at 10 m, over two years of history. Wet
         soil has a higher dielectric constant and returns more of the pulse,
         so a field's VV moves between a dry floor and a wet ceiling that are
         properties of that field and that orbit. Where the day's reading sits
         between them is a relative surface moisture index, and it keeps
         working under the monsoon cloud that blinds NDMI.

Neither is damage. NDMI is leaf water, not yield; the soil index is the top
few centimetres of a bare-ish surface, and a dense canopy all but hides it. The
interpretation -- what an anomaly means for this crop at this stage, and when
the soil index cannot be trusted -- lives in the platform's internal/moisture
package, as radar's does in internal/sar.

# Why the comparisons are gathered here, in the same call

An NDMI value has no meaning on its own. 0.2 is a stressed maize canopy and a
healthy sorghum one. What carries meaning is the field against itself in other
years, and against the cropland around it on the same day. Both are gathered
in the one Earth Engine computation as the current season, because a parcel,
its prior seasons and its neighbourhood are one question -- asking three times
would triple the quota spent during exactly the event that exhausts it.

# Why cloud is masked twice

Cloud Score+ is the better mask and catches thin cloud and haze that the scene
classification misses. The scene classification catches cloud shadow, which
Cloud Score+ is weaker on, and shadow drags NDMI down in a way that reads as
drought. A pixel survives only if both agree it is clear.

# Why the neighbourhood is cropland only

A ring around a field in Maharashtra holds roads, a village, a tank and scrub.
None of them is a crop under the same weather, and a tank full of water would
make every neighbour look wetter than the claimant. ESA WorldCover's cropland
class keeps the comparison to fields.
"""

from __future__ import annotations

import hashlib
import logging
from datetime import date, timedelta
from typing import Any

from pydantic import BaseModel, Field, field_validator

from .earth import (
    BadGeometryError,
    QuotaError,
    UnavailableError,
    Window,
    _rand,
    check,
    geometry_key,
)
from .schemas import ErrorCode, Geometry

log = logging.getLogger(__name__)

S2 = "COPERNICUS/S2_SR_HARMONIZED"
CLOUD_SCORE = "GOOGLE/CLOUD_SCORE_PLUS/V1/S2_HARMONIZED"
S1 = "COPERNICUS/S1_GRD"
WORLDCOVER = "ESA/WorldCover/v200"

# Cloud Score+ cs_cdf at or above this is clear. 0.6 is Google's own
# recommendation for balanced masking; higher throws away usable monsoon
# passes, lower lets haze through.
CLEAR_THRESHOLD = 0.60

# Scene classification classes that are never ground: saturated, cloud shadow,
# medium and high cloud, cirrus, snow.
SCL_REJECT = [1, 3, 8, 9, 10, 11]

# ESA WorldCover cropland.
CROPLAND = 40

# The neighbourhood: cropland between these distances from the parcel edge.
# The inner margin keeps the claimant's own bund and edge pixels out of the
# comparison; 1.5 km is far enough to hold a sample of fields and near enough
# to have had the same weather.
RING_INNER_M = 30
RING_OUTER_M = 1500

# How far back the soil moisture references look. Two years holds at least one
# full dry season and one full monsoon on every relative orbit, which is what
# a dry floor and a wet ceiling need.
SOIL_HISTORY_DAYS = 730

# Bounds on what a caller may ask for. Earth Engine bills by computation, and
# a request with ten baseline years over a whole season is a taluka's worth of
# work for one parcel.
MAX_BASELINE_YEARS = 5
MAX_WINDOW_DAYS = 200


# ---------------------------------------------------------------------------
# The wire contract
# ---------------------------------------------------------------------------
#
# Field names match the Go structs in internal/satellite/moisture.go exactly.
# An unmarshalled field is a zero in Go, and a zero NDMI is a plausible value,
# so a renamed field fails quietly. There is a test that pins them.


class MoistureRequest(BaseModel):
    """A parcel, a window, and how many prior seasons to compare it with."""

    geometry: Geometry
    start_date: str
    end_date: str
    baseline_years: int = Field(default=3, ge=0, le=MAX_BASELINE_YEARS)
    farmer_id: str | None = None
    field_id: str | None = None

    @field_validator("start_date", "end_date")
    @classmethod
    def _is_a_date(cls, v: str) -> str:
        date.fromisoformat(v)
        return v


class OpticalPass(BaseModel):
    """One Sentinel-2 date over the parcel and its neighbourhood.

    Values are means over clear pixels only. clear_fraction is the share of
    the parcel that was clear, so a pass that saw a corner of the field is
    visible as one rather than standing for the whole of it.
    """

    date: str
    # 0 for the season being assessed, 1 for the same window a year earlier,
    # and so on. The comparison is always like-for-like in the calendar.
    season_offset: int = Field(ge=0)

    ndmi: float | None = None
    ndvi: float | None = None
    clear_fraction: float = Field(ge=0, le=1)
    clear_pixels: int = Field(ge=0)

    # Cropland in the ring around the parcel, same date, same masks.
    ring_ndmi: float | None = None
    ring_ndvi: float | None = None
    ring_clear_pixels: int = Field(default=0, ge=0)


class SoilPass(BaseModel):
    """One Sentinel-1 pass, VV only, over the parcel.

    relative_orbit is carried because the dry floor and wet ceiling belong to
    a viewing geometry. The same unchanged field reads several decibels apart
    from two relative orbits, so references mixed across them would call a
    change of orbit a change of moisture.
    """

    date: str
    vv_db: float
    orbit: str | None = None
    relative_orbit: int | None = None
    coverage: float | None = Field(default=None, ge=0, le=1)


class MoistureResult(BaseModel):
    """The completed measurement.

    Empty lists are a success with nothing to say -- a wholly clouded window,
    or a parcel no pass covered -- the same convention as the radar module.
    """

    optical: list[OpticalPass] = Field(default_factory=list)
    soil: list[SoilPass] = Field(default_factory=list)

    # How many 20 m pixels the parcel holds, and how many cropland pixels the
    # ring holds. A mean over a handful of pixels is mostly edge, and the
    # platform needs the count to say so.
    parcel_pixels: int = 0
    ring_cropland_pixels: int = 0

    baseline_years: int = 0
    clear_threshold: float = CLEAR_THRESHOLD

    # What was actually queried. SYNTHETIC appears here in synthetic mode and
    # the platform refuses it outside development.
    missions: list[str] = Field(default_factory=list)
    area_ha: float | None = None


def work_key(req: MoistureRequest) -> str:
    """The cache key: parcel, window and baseline depth."""
    base = geometry_key(req.geometry, Window(req.start_date, req.end_date))
    return hashlib.sha256(
        f"{base}:{req.baseline_years}".encode()
    ).hexdigest()[:32]


def check_window(start: str, end: str) -> None:
    """Refuse a window that is inverted or would cost a season's compute."""
    s, e = date.fromisoformat(start), date.fromisoformat(end)
    if e < s:
        raise BadGeometryError(f"{end} is before {start}", ErrorCode.WINDOW_INVALID)
    if (e - s).days > MAX_WINDOW_DAYS:
        raise BadGeometryError(
            f"a {(e - s).days}-day window is more than the {MAX_WINDOW_DAYS} days "
            "a moisture comparison needs",
            ErrorCode.WINDOW_INVALID,
        )


# ---------------------------------------------------------------------------
# Earth Engine
# ---------------------------------------------------------------------------


def _shift_years(day: str, years: int) -> str:
    d = date.fromisoformat(day)
    try:
        return d.replace(year=d.year - years).isoformat()
    except ValueError:
        # 29 February in a year that has none.
        return d.replace(year=d.year - years, day=28).isoformat()


def measure(ee: Any, req: MoistureRequest) -> MoistureResult:
    """Run the whole measurement as one Earth Engine computation."""
    area = check(req.geometry)
    check_window(req.start_date, req.end_date)

    region = ee.Geometry({"type": req.geometry.type,
                          "coordinates": req.geometry.coordinates})
    # maxError of a metre: the default is proportional to the geometry and on a
    # hectare-sized field can round the inner margin away entirely.
    ring = region.buffer(RING_OUTER_M, 1).difference(
        region.buffer(RING_INNER_M, 1), 1
    )
    cropland = ee.ImageCollection(WORLDCOVER).first().select("Map").eq(CROPLAND)

    try:
        optical_rows = _optical(ee, req, region, ring, cropland)
        soil_rows = _soil(ee, req, region)
        counts = _pixel_counts(ee, region, ring, cropland)
    except Exception as exc:
        text = str(exc).lower()
        if "quota" in text or "rate" in text or "429" in text or "too many" in text:
            raise QuotaError(str(exc)) from exc
        raise UnavailableError(f"Earth Engine failed: {exc}") from exc

    return MoistureResult(
        optical=optical_passes(optical_rows),
        soil=soil_passes(soil_rows),
        parcel_pixels=int(counts.get("parcel") or 0),
        ring_cropland_pixels=int(counts.get("ring") or 0),
        baseline_years=req.baseline_years,
        missions=["SENTINEL-2", "SENTINEL-1", "ESA-WORLDCOVER"],
        area_ha=round(area, 4),
    )


def _optical(ee: Any, req: MoistureRequest, region: Any, ring: Any,
             cropland: Any) -> Any:
    # The season, and the same calendar window in each prior year, as one
    # collection. One filter, one map, one getInfo -- not one call per year.
    ranges = []
    for offset in range(req.baseline_years + 1):
        start = _shift_years(req.start_date, offset)
        # filterDate's end is exclusive.
        end = (date.fromisoformat(_shift_years(req.end_date, offset))
               + timedelta(days=1)).isoformat()
        ranges.append(ee.Filter.date(start, end))
    when = ranges[0] if len(ranges) == 1 else ee.Filter.Or(*ranges)

    scores = ee.ImageCollection(CLOUD_SCORE)
    collection = (
        ee.ImageCollection(S2)
        .filterBounds(ring.bounds())
        .filter(when)
        .linkCollection(scores, ["cs_cdf"])
    )

    season_start = date.fromisoformat(req.start_date)

    def reduce_one(image: Any) -> Any:
        clear = image.select("cs_cdf").gte(CLEAR_THRESHOLD).And(
            image.select("SCL").remap(SCL_REJECT, [0] * len(SCL_REJECT), 1).eq(1)
        )
        idx = ee.Image.cat(
            image.normalizedDifference(["B8A", "B11"]).rename("ndmi"),
            image.normalizedDifference(["B8A", "B4"]).rename("ndvi"),
        ).updateMask(clear)

        reducer = ee.Reducer.mean().combine(ee.Reducer.count(), sharedInputs=True)
        parcel = idx.reduceRegion(
            reducer=reducer, geometry=region, scale=20, maxPixels=1e8,
        )
        neighbours = idx.updateMask(cropland).reduceRegion(
            reducer=reducer, geometry=ring, scale=20, maxPixels=1e8,
        )
        # Every pixel of the parcel, clear or not, so the clear share is a
        # share of the field rather than of whatever the scene happened to
        # cover.
        total = ee.Image.constant(1).clip(region).reduceRegion(
            reducer=ee.Reducer.count(), geometry=region, scale=20, maxPixels=1e8,
        ).get("constant")

        return ee.Feature(None, {
            "date": image.date().format("YYYY-MM-dd"),
            "ndmi": parcel.get("ndmi_mean"),
            "ndvi": parcel.get("ndvi_mean"),
            "clear": parcel.get("ndmi_count"),
            "total": total,
            "ring_ndmi": neighbours.get("ndmi_mean"),
            "ring_ndvi": neighbours.get("ndvi_mean"),
            "ring_clear": neighbours.get("ndmi_count"),
        })

    rows = collection.map(reduce_one).getInfo()
    # The offset is derived here rather than in Earth Engine: a date that
    # falls in the window shifted N years back belongs to season N.
    for feature in (rows or {}).get("features", []):
        props = feature.get("properties", {})
        props["season_offset"] = _offset_of(props.get("date"), season_start,
                                            req.baseline_years)
    return rows


def _offset_of(day: str | None, season_start: date, max_years: int) -> int:
    if not day:
        return 0
    d = date.fromisoformat(day)
    for offset in range(max_years + 1):
        start = date.fromisoformat(_shift_years(season_start.isoformat(), offset))
        if d >= start:
            return offset
    return max_years


def _soil(ee: Any, req: MoistureRequest, region: Any) -> Any:
    end = date.fromisoformat(req.end_date) + timedelta(days=1)
    start = end - timedelta(days=SOIL_HISTORY_DAYS)
    collection = (
        ee.ImageCollection(S1)
        .filterBounds(region)
        .filterDate(start.isoformat(), end.isoformat())
        .filter(ee.Filter.eq("instrumentMode", "IW"))
        .filter(ee.Filter.listContains("transmitterReceiverPolarisation", "VV"))
        .select(["VV"])
    )

    def reduce_one(image: Any) -> Any:
        vv = image.select("VV")
        mean = vv.reduceRegion(
            reducer=ee.Reducer.mean(), geometry=region, scale=10, maxPixels=1e8,
        ).get("VV")
        covered = vv.mask().reduceRegion(
            reducer=ee.Reducer.mean(), geometry=region, scale=10, maxPixels=1e8,
        ).get("VV")
        return ee.Feature(None, {
            "date": image.date().format("YYYY-MM-dd"),
            "vv": mean,
            "orbit": image.get("orbitProperties_pass"),
            "relative_orbit": image.get("relativeOrbitNumber_start"),
            "coverage": covered,
        })

    return collection.map(reduce_one).getInfo()


def _pixel_counts(ee: Any, region: Any, ring: Any, cropland: Any) -> dict[str, Any]:
    parcel = ee.Image.constant(1).reduceRegion(
        reducer=ee.Reducer.count(), geometry=region, scale=20, maxPixels=1e8,
    ).get("constant")
    neighbours = cropland.selfMask().reduceRegion(
        reducer=ee.Reducer.count(), geometry=ring, scale=20, maxPixels=1e8,
    ).get("Map")
    return ee.Dictionary({"parcel": parcel, "ring": neighbours}).getInfo()


# ---------------------------------------------------------------------------
# Turning Earth Engine's rows into passes
# ---------------------------------------------------------------------------


def optical_passes(rows: Any) -> list[OpticalPass]:
    """Merge, clean and order the optical rows.

    # Why same-date rows are merged

    Sentinel-2 tiles overlap. A parcel near a tile edge appears in two
    granules on the same day, which is one observation reported twice. Left
    as two, it would count double in every average and make a single clear
    day look like a well-observed week. The two are merged, weighted by how
    many clear pixels each saw.

    # Why a row with no clear pixel is dropped

    Its mean is null, and a null read as zero is an NDMI of 0.0 -- a plausible
    value, for a stressed crop. Dropping it keeps a cloud out of the drought
    record.
    """
    merged: dict[tuple[int, str], dict[str, float]] = {}
    for feature in (rows or {}).get("features", []):
        p = feature.get("properties", {})
        day = p.get("date")
        total = _num(p.get("total")) or 0.0
        clear = _num(p.get("clear")) or 0.0
        ndmi, ndvi = _num(p.get("ndmi")), _num(p.get("ndvi"))
        if not day or total <= 0 or clear <= 0 or ndmi is None:
            continue
        if not -1.0 <= ndmi <= 1.0 or (ndvi is not None and not -1.0 <= ndvi <= 1.0):
            continue

        key = (int(p.get("season_offset") or 0), day)
        m = merged.setdefault(key, {
            "clear": 0.0, "total": 0.0, "ndmi": 0.0, "ndvi": 0.0, "ndvi_w": 0.0,
            "ring_clear": 0.0, "ring_ndmi": 0.0, "ring_ndvi": 0.0, "ring_ndvi_w": 0.0,
        })
        m["ndmi"] += ndmi * clear
        if ndvi is not None:
            m["ndvi"] += ndvi * clear
            m["ndvi_w"] += clear
        m["clear"] += clear
        m["total"] = max(m["total"], total)

        rc = _num(p.get("ring_clear")) or 0.0
        rn, rv = _num(p.get("ring_ndmi")), _num(p.get("ring_ndvi"))
        if rc > 0 and rn is not None and -1.0 <= rn <= 1.0:
            m["ring_ndmi"] += rn * rc
            if rv is not None and -1.0 <= rv <= 1.0:
                m["ring_ndvi"] += rv * rc
                m["ring_ndvi_w"] += rc
            m["ring_clear"] += rc

    out: list[OpticalPass] = []
    for (offset, day), m in merged.items():
        # Overlapping granules each count the same pixels, so the clear count
        # is capped at the parcel's size rather than summed past it.
        clear = min(m["clear"], m["total"])
        out.append(OpticalPass(
            date=day,
            season_offset=offset,
            ndmi=round(m["ndmi"] / m["clear"], 4),
            ndvi=round(m["ndvi"] / m["ndvi_w"], 4) if m["ndvi_w"] > 0 else None,
            clear_fraction=round(min(1.0, clear / m["total"]), 3),
            clear_pixels=round(clear),
            ring_ndmi=(round(m["ring_ndmi"] / m["ring_clear"], 4)
                       if m["ring_clear"] > 0 else None),
            ring_ndvi=(round(m["ring_ndvi"] / m["ring_ndvi_w"], 4)
                       if m["ring_ndvi_w"] > 0 else None),
            ring_clear_pixels=round(m["ring_clear"]),
        ))
    out.sort(key=lambda x: (x.season_offset, x.date))
    return out


def soil_passes(rows: Any) -> list[SoilPass]:
    """Clean and order the radar rows.

    Same-day duplicates from overlapping frames are merged by mean; a null or
    physically impossible backscatter is dropped rather than read as zero,
    because -0 dB is a surface no field produces.
    """
    merged: dict[tuple[str, int | None], list[SoilPass]] = {}
    for feature in (rows or {}).get("features", []):
        p = feature.get("properties", {})
        vv = _num(p.get("vv"))
        if not p.get("date") or vv is None or not -35.0 <= vv <= 5.0:
            continue
        rel = p.get("relative_orbit")
        rel_int = int(rel) if isinstance(rel, (int, float)) else None
        cov = _num(p.get("coverage"))
        merged.setdefault((p["date"], rel_int), []).append(SoilPass(
            date=p["date"],
            vv_db=vv,
            orbit=p.get("orbit") or None,
            relative_orbit=rel_int,
            coverage=round(min(1.0, max(0.0, cov)), 3) if cov is not None else None,
        ))

    out = []
    for group in merged.values():
        first = group[0]
        out.append(SoilPass(
            date=first.date,
            vv_db=round(sum(g.vv_db for g in group) / len(group), 3),
            orbit=first.orbit,
            relative_orbit=first.relative_orbit,
            coverage=max((g.coverage or 0.0) for g in group) if any(
                g.coverage is not None for g in group) else None,
        ))
    out.sort(key=lambda x: x.date)
    return out


def _num(v: Any) -> float | None:
    if isinstance(v, bool) or v is None:
        return None
    try:
        f = float(v)
    except (TypeError, ValueError):
        return None
    return None if f != f else f  # NaN


# ---------------------------------------------------------------------------
# Synthetic
# ---------------------------------------------------------------------------


def synthetic(req: MoistureRequest) -> MoistureResult:
    """Plausible measurements without Earth Engine, labelled SYNTHETIC.

    Deterministic in the request, so the same submission answers the same way.
    The current season dries toward the end of the window and the prior
    seasons do not, which is the shape the platform's analysis most needs to
    be exercised against -- and the reason the label matters: this is a
    drought that did not happen.
    """
    area = check(req.geometry)
    check_window(req.start_date, req.end_date)
    seed = int(work_key(req)[:8], 16)

    start = date.fromisoformat(req.start_date)
    end = date.fromisoformat(req.end_date)
    span = max(1, (end - start).days)

    optical: list[OpticalPass] = []
    for offset in range(req.baseline_years + 1):
        day, i = start, 0
        while day <= end:
            r = _rand(seed + offset * 1000 + i)
            progress = (day - start).days / span
            dry = 0.18 * progress if offset == 0 else 0.0
            ndmi = 0.32 - dry + (r - 0.5) * 0.04
            ndvi = 0.68 - dry * 0.6 + (r - 0.5) * 0.04
            optical.append(OpticalPass(
                date=_shift_years(day.isoformat(), offset),
                season_offset=offset,
                ndmi=round(ndmi, 4),
                ndvi=round(ndvi, 4),
                clear_fraction=1.0,
                clear_pixels=max(1, int(area * 25)),
                ring_ndmi=round(0.30 + (r - 0.5) * 0.03, 4),
                ring_ndvi=round(0.66 + (r - 0.5) * 0.03, 4),
                ring_clear_pixels=12000,
            ))
            day += timedelta(days=5)
            i += 1

    soil: list[SoilPass] = []
    history_start = end - timedelta(days=SOIL_HISTORY_DAYS)
    day, i = history_start, 0
    while day <= end:
        r = _rand(seed + 77_000 + i)
        # A seasonal swing between a dry floor and a monsoon ceiling.
        month = day.month
        wet = 1.0 if 6 <= month <= 9 else 0.2
        vv = -16.0 + 5.0 * wet + (r - 0.5) * 1.5
        soil.append(SoilPass(
            date=day.isoformat(), vv_db=round(vv, 3),
            orbit="ASCENDING" if i % 2 == 0 else "DESCENDING",
            relative_orbit=34 if i % 2 == 0 else 136,
            coverage=1.0,
        ))
        day += timedelta(days=6)
        i += 1

    return MoistureResult(
        optical=optical,
        soil=soil,
        parcel_pixels=max(1, int(area * 25)),
        ring_cropland_pixels=12000,
        baseline_years=req.baseline_years,
        missions=["SYNTHETIC"],
        area_ha=round(area, 4),
    )
