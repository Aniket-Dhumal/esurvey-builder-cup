"""The wire contract.

Field names match the Go structs in `internal/satellite/radar.go` exactly.
Changing one without the other is the most likely way to break this
integration, and the failure is quiet: an unmarshalled field is a zero, and a
zero decibel reading is a plausible-looking number.

The shape deliberately mirrors the Farm Analytics Engine's NDVI and terrain
modules -- submit returns a task id, the caller polls until the status leaves
PROCESSING -- because the platform's client already speaks that and a second
convention would be a second thing to get wrong.
"""

from __future__ import annotations

from enum import StrEnum
from typing import Any, Literal

from pydantic import BaseModel, Field, field_validator


class Status(StrEnum):
    PROCESSING = "PROCESSING"
    COMPLETED = "COMPLETED"
    FAILED = "FAILED"


class ErrorCode(StrEnum):
    """Stable codes, so a caller branches on the code and not on prose."""

    GEOMETRY_INVALID = "GEOMETRY_INVALID"
    GEOMETRY_TOO_LARGE = "GEOMETRY_TOO_LARGE"
    LATLON_SWAPPED = "LATLON_SWAPPED"
    WINDOW_INVALID = "WINDOW_INVALID"
    EE_UNAVAILABLE = "EE_UNAVAILABLE"
    EE_QUOTA = "EE_QUOTA"
    INTERNAL = "INTERNAL"


class Geometry(BaseModel):
    """A GeoJSON Polygon or MultiPolygon, longitude first."""

    type: Literal["Polygon", "MultiPolygon"]
    coordinates: Any

    @field_validator("coordinates")
    @classmethod
    def _positions_look_like_india(cls, v: Any) -> Any:
        """Catch a latitude-first ring before Earth Engine bills for it.

        A swapped pair puts a Maharashtra field in the Indian Ocean, where the
        analysis succeeds and returns the backscatter of open water -- which
        is exactly the signature this platform reads as flooding. Refusing it
        here with a named code is far better than a confident wrong answer.
        """
        for lon, lat in _positions(v):
            if not (-180 <= lon <= 180 and -90 <= lat <= 90):
                raise ValueError("position out of range")
            # India spans roughly 68-98E and 6-38N. A pair that fits the
            # second range in its first slot is almost certainly reversed.
            if 6 <= lon <= 38 and 68 <= lat <= 98:
                raise ValueError(
                    "positions appear to be latitude-first; GeoJSON is "
                    "longitude-first and a swapped ring analyses the sea"
                )
        return v


def _positions(coords: Any) -> list[tuple[float, float]]:
    """Every [lon, lat] pair in a nested coordinate structure."""
    out: list[tuple[float, float]] = []

    def walk(node: Any) -> None:
        if (
            isinstance(node, (list, tuple))
            and len(node) == 2
            and all(isinstance(x, (int, float)) for x in node)
        ):
            out.append((float(node[0]), float(node[1])))
            return
        if isinstance(node, (list, tuple)):
            for child in node:
                walk(child)

    walk(coords)
    return out


class SARRequest(BaseModel):
    """A parcel and a window."""

    geometry: Geometry
    start_date: str
    end_date: str
    farmer_id: str | None = None
    field_id: str | None = None

    @field_validator("start_date", "end_date")
    @classmethod
    def _is_a_date(cls, v: str) -> str:
        from datetime import date

        date.fromisoformat(v)
        return v


class SARPoint(BaseModel):
    """The parcel-mean backscatter for one pass, in decibels."""

    date: str
    vv_db: float
    vh_db: float

    # ASCENDING or DESCENDING. Required for a meaningful comparison: the same
    # unchanged field returns several decibels differently between the two, so
    # a series without it can only be compared loosely.
    orbit: str | None = None

    # How much of the parcel the pass actually covered. A pass clipped by a
    # scene edge is a mean over part of the field, and a caller comparing it
    # with a full one is comparing two different fields.
    coverage: float | None = Field(default=None, ge=0, le=1)


class SARResult(BaseModel):
    """The completed analysis.

    An empty series is a success with nothing to say -- the same convention
    the NDVI module uses for a wholly clouded window -- because a parcel may
    genuinely have had no pass in a short window.
    """

    time_series: list[SARPoint] = Field(default_factory=list)

    # What was actually queried, so a figure can be traced to a collection
    # rather than to "satellite". SYNTHETIC appears here in synthetic mode and
    # the platform surfaces it.
    missions: list[str] = Field(default_factory=list)

    # The area analysed, so a caller can see it matches the parcel they sent.
    area_ha: float | None = None


class SubmitResponse(BaseModel):
    task_id: str
    status: Status
    message: str | None = None


class StatusResponse(BaseModel):
    status: Status
    result: SARResult | None = None
    error: str | None = None
    error_code: str | None = None


class APIError(BaseModel):
    """The engine's error envelope, matching the analytics engine's."""

    class Detail(BaseModel):
        code: str
        message: str

    error: Detail
