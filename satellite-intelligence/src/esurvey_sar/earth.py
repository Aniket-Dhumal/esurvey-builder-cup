"""Sentinel-1 backscatter over a parcel.

# What is actually being measured

Sentinel-1 GRD, calibrated to sigma-nought and expressed in decibels. Two
polarisations, and both are needed because either alone is ambiguous:

  VV  co-polarised. Collapses over standing water, because a flat water
      surface reflects the pulse away from the satellite instead of back.
  VH  cross-polarised. Depends on volume scattering from a vertical stem
      canopy, so it falls when a crop goes flat.

The interpretation is not here. This returns decibels and the metadata needed
to read them; the platform's internal/sar package separates a flooded field
from a flattened one and admits that a harvested field looks identical to a
destroyed one.

# Why the orbit is carried

Backscatter differs by several decibels between ascending and descending
passes over an unchanged field, because the geometry differs. A series that
dropped the orbit would let a caller compare across them and manufacture a
change that did not happen.

# Why coverage is carried

A pass clipped by a scene edge is a mean over part of the parcel. Reporting it
as though it covered the field would make a corner of a field stand for all of
it, and nothing downstream could tell.
"""

from __future__ import annotations

import hashlib
import json
import logging
import math
from dataclasses import dataclass
from datetime import date, timedelta
from typing import Any, Protocol

from .schemas import ErrorCode, Geometry, SARPoint, SARResult

log = logging.getLogger(__name__)

# The collection, named once.
COLLECTION = "COPERNICUS/S1_GRD"

# A parcel larger than this is not a parcel. Maharashtra's largest holdings
# are a few tens of hectares; a thousand is somebody submitting a taluka, and
# Earth Engine would bill for it.
MAX_AREA_HA = 1000.0

# Sentinel-1 resolution is about 10 m, so a field below roughly a tenth of a
# hectare is a handful of pixels and its mean is mostly speckle.
MIN_AREA_HA = 0.05


class UnavailableError(Exception):
    """Earth Engine cannot be reached or is not authorised."""

    code = ErrorCode.EE_UNAVAILABLE


class QuotaError(Exception):
    """Earth Engine refused for quota reasons. Worth retrying later."""

    code = ErrorCode.EE_QUOTA


class BadGeometryError(Exception):
    def __init__(self, message: str, code: ErrorCode = ErrorCode.GEOMETRY_INVALID):
        super().__init__(message)
        self.code = code


@dataclass(frozen=True)
class Window:
    start: str
    end: str

    def widened(self, days: int = 0) -> Window:
        s = date.fromisoformat(self.start) - timedelta(days=days)
        e = date.fromisoformat(self.end) + timedelta(days=days)
        return Window(s.isoformat(), e.isoformat())


class Backend(Protocol):
    """Where measurements come from."""

    def series(self, geometry: Geometry, window: Window) -> SARResult: ...

    # Canopy water and soil moisture. Typed loosely here because moisture.py
    # imports from this module, and the reverse import would be circular.
    def moisture(self, req: Any) -> Any: ...

    def healthy(self) -> tuple[bool, str]: ...

    @property
    def name(self) -> str: ...


def geometry_key(geometry: Geometry, window: Window) -> str:
    """A stable cache key for a parcel and a window.

    Sorted JSON so that two structurally identical geometries submitted with
    different key order are one cache entry rather than two Earth Engine
    calls.
    """
    payload = json.dumps(
        {"t": geometry.type, "c": geometry.coordinates, "s": window.start,
         "e": window.end},
        sort_keys=True,
        separators=(",", ":"),
    )
    return hashlib.sha256(payload.encode()).hexdigest()[:32]


def area_hectares(geometry: Geometry) -> float:
    """The polygon's area, without Earth Engine.

    Checked before submitting, because refusing a taluka-sized geometry after
    paying for the analysis is the wrong order. A local flat-earth
    approximation: at field scale the error is far below the resolution of the
    imagery, and this is a guard rather than a measurement.
    """
    rings = _outer_rings(geometry)
    if not rings:
        raise BadGeometryError("no ring in the geometry")

    total = 0.0
    for ring in rings:
        if len(ring) < 4:
            raise BadGeometryError("a ring needs at least four positions")
        lat0 = math.radians(sum(p[1] for p in ring) / len(ring))
        m_lon = 111_320 * math.cos(lat0)
        m_lat = 111_132

        twice = 0.0
        for i in range(len(ring)):
            ax, ay = ring[i]
            bx, by = ring[(i + 1) % len(ring)]
            twice += (ax * m_lon) * (by * m_lat) - (bx * m_lon) * (ay * m_lat)
        total += abs(twice / 2)

    return total / 10_000


def _outer_rings(geometry: Geometry) -> list[list[tuple[float, float]]]:
    coords = geometry.coordinates
    # A Polygon is one ring set; a MultiPolygon is a list of them.
    polygons = [coords] if geometry.type == "Polygon" else coords
    out = []
    for poly in polygons:
        if poly and isinstance(poly[0], list):
            out.append([(float(p[0]), float(p[1])) for p in poly[0]])
    return out


def check(geometry: Geometry) -> float:
    """Refuse what should not reach Earth Engine, and return the area."""
    area = area_hectares(geometry)
    if area > MAX_AREA_HA:
        raise BadGeometryError(
            f"the geometry encloses {area:.0f} ha; a parcel is not a taluka",
            ErrorCode.GEOMETRY_TOO_LARGE,
        )
    if area < MIN_AREA_HA:
        raise BadGeometryError(
            f"the geometry encloses {area:.3f} ha, which is a handful of "
            f"Sentinel-1 pixels and would be mostly speckle",
            ErrorCode.GEOMETRY_INVALID,
        )
    return area


# ---------------------------------------------------------------------------
# Earth Engine
# ---------------------------------------------------------------------------


class EarthEngine:
    """The real thing."""

    def __init__(self, project: str) -> None:
        self.project = project
        self._ee: Any = None

    @property
    def name(self) -> str:
        return "earth-engine"

    def _client(self) -> Any:
        if self._ee is not None:
            return self._ee
        try:
            import ee
        except ImportError as exc:  # pragma: no cover - environment dependent
            raise UnavailableError(
                "the earthengine-api package is not installed"
            ) from exc

        try:
            # Application Default Credentials, the same as the platform's
            # evidence bucket and for the same reason: a downloaded
            # service-account key is a long-lived, silently copyable
            # credential.
            ee.Initialize(project=self.project)
        except Exception as exc:
            raise UnavailableError(f"Earth Engine would not initialise: {exc}") from exc

        self._ee = ee
        return ee

    def healthy(self) -> tuple[bool, str]:
        try:
            ee = self._client()
            # A trivial computation, to prove the round trip rather than the
            # import. An initialised client that cannot compute is the state
            # an unregistered project is in.
            ee.Number(1).getInfo()
            return True, "reachable"
        except UnavailableError as exc:
            return False, str(exc)
        except Exception as exc:  # pragma: no cover - network dependent
            return False, f"unreachable: {exc}"

    def series(self, geometry: Geometry, window: Window) -> SARResult:
        area = check(geometry)
        ee = self._client()

        region = ee.Geometry(json.loads(geometry.model_dump_json())["coordinates"]
                             and {"type": geometry.type,
                                  "coordinates": geometry.coordinates})

        try:
            collection = (
                ee.ImageCollection(COLLECTION)
                .filterBounds(region)
                .filterDate(window.start, window.end)
                .filter(ee.Filter.eq("instrumentMode", "IW"))
                .filter(ee.Filter.listContains("transmitterReceiverPolarisation", "VV"))
                .filter(ee.Filter.listContains("transmitterReceiverPolarisation", "VH"))
                .select(["VV", "VH"])
            )

            def reduce_one(image: Any) -> Any:
                stats = image.reduceRegion(
                    reducer=ee.Reducer.mean(),
                    geometry=region,
                    scale=10,
                    maxPixels=1e8,
                    bestEffort=True,
                )
                # The share of the parcel with data, so a pass clipped by a
                # scene edge is visible rather than silently standing for the
                # whole field.
                covered = (
                    image.select("VV")
                    .mask()
                    .reduceRegion(
                        reducer=ee.Reducer.mean(),
                        geometry=region,
                        scale=10,
                        maxPixels=1e8,
                        bestEffort=True,
                    )
                    .get("VV")
                )
                return ee.Feature(
                    None,
                    {
                        "date": image.date().format("YYYY-MM-dd"),
                        "vv": stats.get("VV"),
                        "vh": stats.get("VH"),
                        "orbit": image.get("orbitProperties_pass"),
                        "coverage": covered,
                    },
                )

            rows = collection.map(reduce_one).getInfo()
        except Exception as exc:
            text = str(exc).lower()
            if "quota" in text or "rate" in text or "429" in text:
                raise QuotaError(str(exc)) from exc
            raise UnavailableError(f"Earth Engine failed: {exc}") from exc

        return SARResult(
            time_series=_points(rows),
            missions=["SENTINEL-1"],
            area_ha=round(area, 4),
        )

    def moisture(self, req: Any) -> Any:
        """NDMI and the Sentinel-1 soil record, as one computation."""
        from .moisture import measure

        return measure(self._client(), req)


def _points(rows: Any) -> list[SARPoint]:
    """Turn a feature collection into a series, dropping what cannot be read.

    A pass whose reduction returned null for either polarisation covered none
    of the parcel -- the usual cause is a scene that clips it entirely -- and
    a null read as zero would be a -0 dB reading, which is a surface no field
    produces.
    """
    out: list[SARPoint] = []
    for feature in (rows or {}).get("features", []):
        p = feature.get("properties", {})
        vv, vh = p.get("vv"), p.get("vh")
        if vv is None or vh is None:
            continue
        out.append(
            SARPoint(
                date=p.get("date", ""),
                vv_db=round(float(vv), 3),
                vh_db=round(float(vh), 3),
                orbit=(p.get("orbit") or None),
                coverage=(
                    round(float(p["coverage"]), 3)
                    if p.get("coverage") is not None
                    else None
                ),
            )
        )
    out.sort(key=lambda x: x.date)
    return out


# ---------------------------------------------------------------------------
# Synthetic
# ---------------------------------------------------------------------------


class Synthetic:
    """Plausible backscatter without Earth Engine.

    For developing the platform's side of the contract before a project is
    registered. Off by default, and every result is labelled SYNTHETIC in the
    missions field, which the platform surfaces to the officer.

    That label is not decoration. A synthetic reading that reached somebody as
    though it were an observation would be a fabricated finding on a
    compensation record, which is worse than having no radar at all.
    """

    @property
    def name(self) -> str:
        return "synthetic"

    def healthy(self) -> tuple[bool, str]:
        return True, "synthetic backscatter; not an observation"

    def series(self, geometry: Geometry, window: Window) -> SARResult:
        area = check(geometry)

        # Deterministic in the parcel and window, so the same submission
        # answers the same way and a developer is not chasing noise.
        seed = int(geometry_key(geometry, window)[:8], 16)

        start = date.fromisoformat(window.start)
        end = date.fromisoformat(window.end)

        points: list[SARPoint] = []
        day = start
        i = 0
        while day <= end:
            r = _rand(seed + i)
            # A vegetated field sits around -9 dB VV and -15 dB VH, with a
            # decibel or so of soil-moisture wander between passes.
            vv = -9.0 + (r - 0.5) * 2.0
            vh = -15.0 + (_rand(seed + i + 977) - 0.5) * 2.0
            points.append(
                SARPoint(
                    date=day.isoformat(),
                    vv_db=round(vv, 3),
                    vh_db=round(vh, 3),
                    # Sentinel-1 alternates orbit direction between passes.
                    orbit="ASCENDING" if i % 2 == 0 else "DESCENDING",
                    coverage=1.0,
                )
            )
            day += timedelta(days=12)
            i += 1

        return SARResult(
            time_series=points,
            missions=["SYNTHETIC"],
            area_ha=round(area, 4),
        )

    def moisture(self, req: Any) -> Any:
        from .moisture import synthetic

        return synthetic(req)


def _rand(seed: int) -> float:
    """A stable pseudo-random in [0,1), without seeding a global generator."""
    h = hashlib.sha256(str(seed).encode()).digest()
    return int.from_bytes(h[:4], "big") / 0xFFFFFFFF
