"""Weather for a parcel, fetched from Open-Meteo.

# Why the agent fetches this itself

Everything else the panel reasons over is handed to it by the backend. Weather
is not: it depends on where the parcel is and when the loss happened, and the
combination is only known at assessment time. Making it a tool the agent calls
is what lets the weather specialist decide the window it needs rather than
being given a fixed one.

# Why Open-Meteo

It is free, needs no key, and covers the whole of India at ~11 km ERA5/ECMWF
resolution. That resolution is the honest limitation: it is a regional picture,
not a reading from this field. A hailstorm can flatten one village and miss the
next, and this data cannot tell those apart. What it can do is answer whether
the claimed calamity is consistent with what the region experienced — which is
a fraud cross-check, not a measurement.
"""

from __future__ import annotations

import logging
from datetime import date, timedelta
from typing import Any

import httpx

log = logging.getLogger(__name__)

# Two endpoints, because neither one covers the whole range this system needs.
#
# The forecast endpoint serves past dates too, roughly 92 days back, and unlike
# the archive it has no publication lag -- a loss reported within 48 hours is
# exactly the case that lag would break. The archive reaches back to 1940 but
# trails real time by about five days.
#
# Both are needed. An assessment is not always run soon after the incident: a
# report filed offline can sync weeks later, a rejected assessment can be
# reprocessed, and a district can reopen an event. So the source is chosen by
# how old the incident is, and if the preferred one refuses, the other is tried
# before giving up.
_FORECAST = "https://api.open-meteo.com/v1/forecast"
_ARCHIVE = "https://archive-api.open-meteo.com/v1/archive"

# Well inside the forecast endpoint's ~92-day reach, so a window that starts
# fourteen days before the incident still lands in range.
_FORECAST_REACH_DAYS = 60

_DAILY = ",".join(
    [
        "weather_code",
        "precipitation_sum",
        "rain_sum",
        "precipitation_hours",
        "wind_speed_10m_max",
        "wind_gusts_10m_max",
        "temperature_2m_max",
        "temperature_2m_min",
    ]
)

# WMO codes. 96 and 99 are thunderstorm with hail — the only direct hail signal
# this source carries, and worth having because hail is the calamity most often
# claimed and least visible in a rainfall total.
_HAIL_CODES = {96, 99}
_THUNDERSTORM_CODES = {95, 96, 99}

_TIMEOUT_S = 12.0


class WeatherUnavailableError(RuntimeError):
    """No weather source could answer for this parcel and window."""


def _refusal(resp: httpx.Response) -> str:
    """Open-Meteo's own explanation for a rejected request, if it gave one."""
    try:
        reason = resp.json().get("reason")
    except ValueError:
        reason = None
    return str(reason) if reason else f"HTTP {resp.status_code}"


async def fetch_weather_window(
    latitude: float,
    longitude: float,
    incident_date: str,
    days_before: int = 14,
) -> dict[str, Any]:
    """Daily weather for the days leading up to and including an incident.

    Args:
        latitude: Parcel centroid latitude.
        longitude: Parcel centroid longitude.
        incident_date: The date of the loss, YYYY-MM-DD.
        days_before: How many days before the incident to include. Fourteen is
            the default because it spans the run-up a crop actually responds
            to — a week of saturation before a storm is why the storm lodged
            it — without reaching back into a different weather regime.

    Returns:
        A summary and the daily series, ready for a model to reason over.
    """
    try:
        end = date.fromisoformat(incident_date)
    except ValueError as exc:
        raise WeatherUnavailableError(f"bad incident date {incident_date!r}") from exc

    start = end - timedelta(days=days_before)

    params = {
        "latitude": f"{latitude:.4f}",
        "longitude": f"{longitude:.4f}",
        "start_date": start.isoformat(),
        "end_date": end.isoformat(),
        "daily": _DAILY,
        "timezone": "Asia/Kolkata",
    }

    # Recent incidents go to the forecast endpoint, older ones to the archive.
    age_days = (date.today() - end).days
    order = (
        [(_ARCHIVE, "archive"), (_FORECAST, "forecast")]
        if age_days > _FORECAST_REACH_DAYS
        else [(_FORECAST, "forecast"), (_ARCHIVE, "archive")]
    )

    body: dict[str, Any] | None = None
    which = ""
    failures: list[str] = []

    async with httpx.AsyncClient(timeout=_TIMEOUT_S) as client:
        for url, label in order:
            try:
                resp = await client.get(url, params=params)
                if resp.status_code >= 400:
                    # Open-Meteo puts the actual problem in the body. Reporting
                    # "could not reach Open-Meteo" for an out-of-range date
                    # sends everyone downstream looking at the network.
                    failures.append(f"{label}: {_refusal(resp)}")
                    continue
                body = resp.json()
                which = label
                break
            except httpx.HTTPError as exc:
                failures.append(f"{label}: could not be reached ({exc})")
            except ValueError as exc:
                failures.append(f"{label}: returned unusable data ({exc})")

    if body is None:
        raise WeatherUnavailableError(
            "; ".join(failures) or "no weather source answered"
        )

    daily = body.get("daily") or {}
    dates: list[str] = daily.get("time") or []
    if not dates:
        raise WeatherUnavailableError("Open-Meteo returned no daily rows")

    def col(name: str) -> list[float | None]:
        values = daily.get(name) or []
        return list(values) + [None] * (len(dates) - len(values))

    codes = [int(c) if c is not None else None for c in col("weather_code")]
    rain = col("precipitation_sum")
    gusts = col("wind_gusts_10m_max")
    tmax = col("temperature_2m_max")
    tmin = col("temperature_2m_min")
    hours = col("precipitation_hours")

    rows = [
        {
            "date": d,
            "weather_code": codes[i],
            "precipitation_mm": rain[i],
            "precipitation_hours": hours[i],
            "wind_gust_max_kmh": gusts[i],
            "temp_max_c": tmax[i],
            "temp_min_c": tmin[i],
            "hail_reported": codes[i] in _HAIL_CODES if codes[i] is not None else None,
        }
        for i, d in enumerate(dates)
    ]

    incident_row = next((r for r in rows if r["date"] == end.isoformat()), None)
    rain_values = [r for r in rain if r is not None]
    gust_values = [g for g in gusts if g is not None]

    return {
        "source": f"Open-Meteo {which} (ERA5/ECMWF, ~11 km grid)",
        "resolution_note": (
            "Regional, not a reading from this field. A hailstorm can hit one "
            "village and miss the next; this says what the area experienced, "
            "which is a cross-check on the claim rather than a measurement of "
            "the parcel."
        ),
        "latitude": latitude,
        "longitude": longitude,
        "window_start": start.isoformat(),
        "window_end": end.isoformat(),
        "days": len(rows),
        "total_rain_mm": round(sum(rain_values), 1) if rain_values else None,
        "max_daily_rain_mm": round(max(rain_values), 1) if rain_values else None,
        "max_wind_gust_kmh": round(max(gust_values), 1) if gust_values else None,
        "hail_days": [r["date"] for r in rows if r["hail_reported"]],
        "thunderstorm_days": [
            r["date"] for r in rows
            if r["weather_code"] is not None
            and r["weather_code"] in _THUNDERSTORM_CODES
        ],
        "on_the_incident_day": incident_row,
        "daily": rows,
    }


async def get_recent_weather(
    latitude: float,
    longitude: float,
    incident_date: str,
) -> dict[str, Any]:
    """Look up the weather at a parcel for the fourteen days up to an incident.

    Use this to check whether the calamity a farmer reports is consistent with
    what the area actually experienced. Returns daily rainfall, wind gusts,
    temperatures and WMO weather codes, plus which days carried hail or
    thunderstorms.

    Args:
        latitude: Parcel centroid latitude, in decimal degrees.
        longitude: Parcel centroid longitude, in decimal degrees.
        incident_date: The date the loss occurred, as YYYY-MM-DD.

    Returns:
        The weather window, or ``{"available": false, "error": ...}`` if the
        source could not be reached.
    """
    try:
        window = await fetch_weather_window(latitude, longitude, incident_date)
        window["available"] = True
        return window
    except WeatherUnavailableError as exc:
        # Returned, not raised. A tool that raises into the model produces an
        # opaque failure and usually a hallucinated recovery; one that returns
        # "unavailable" lets the specialist say so in its finding, which is
        # the difference between a degraded assessment and no assessment.
        log.warning("weather unavailable: %s", exc)
        return {"available": False, "error": str(exc)}
    except Exception as exc:
        log.exception("weather tool failed")
        return {"available": False, "error": f"unexpected failure: {exc}"}
