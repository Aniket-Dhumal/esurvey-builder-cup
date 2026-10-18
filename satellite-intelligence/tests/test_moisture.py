"""The moisture module: its contract, and the parts of it that run without
Earth Engine.

The Earth Engine computation itself cannot be exercised here -- it needs a
registered project -- and was verified against the live service instead. What
is tested is everything around it that decides whether a number can be
trusted: which passes are dropped, how overlapping tiles are merged, that a
generated record is labelled all the way to the wire, and that the wire names
match the platform's Go structs.
"""

from __future__ import annotations

import time

import pytest
from fastapi.testclient import TestClient

from esurvey_sar import main as service
from esurvey_sar.earth import BadGeometryError, Synthetic
from esurvey_sar.moisture import (
    MAX_WINDOW_DAYS,
    MoistureRequest,
    MoistureResult,
    OpticalPass,
    SoilPass,
    _offset_of,
    _shift_years,
    check_window,
    optical_passes,
    soil_passes,
    synthetic,
    work_key,
)
from esurvey_sar.tasks import Runner

from .test_service import PARCEL


def req(**over) -> MoistureRequest:
    body = {
        "geometry": PARCEL,
        "start_date": "2026-07-05",
        "end_date": "2026-10-03",
        "baseline_years": 3,
    }
    body.update(over)
    return MoistureRequest(**body)


# ---------------------------------------------------------------------------
# The wire
# ---------------------------------------------------------------------------


def test_the_wire_names_match_the_platforms_go_structs():
    # Mirrored in internal/satellite/moisture_test.go. A renamed field fails
    # quietly on the other side: an unmarshalled Go field is a zero, and a
    # zero NDMI is a plausible value for a stressed crop.
    assert sorted(MoistureRequest.model_fields) == [
        "baseline_years", "end_date", "farmer_id", "field_id", "geometry",
        "start_date",
    ]
    assert sorted(MoistureResult.model_fields) == [
        "area_ha", "baseline_years", "clear_threshold", "missions", "optical",
        "parcel_pixels", "ring_cropland_pixels", "soil",
    ]
    assert sorted(OpticalPass.model_fields) == [
        "clear_fraction", "clear_pixels", "date", "ndmi", "ndvi",
        "ring_clear_pixels", "ring_ndmi", "ring_ndvi", "season_offset",
    ]
    assert sorted(SoilPass.model_fields) == [
        "coverage", "date", "orbit", "relative_orbit", "vv_db",
    ]


# ---------------------------------------------------------------------------
# Turning Earth Engine rows into passes
# ---------------------------------------------------------------------------


def row(**props):
    return {"properties": props}


def test_a_pass_with_no_clear_pixel_is_dropped_not_read_as_zero():
    # A null mean read as zero is an NDMI of 0.0 -- a plausible value, for a
    # stressed crop. A cloud would become a drought.
    rows = {"features": [
        row(date="2026-08-08", season_offset=0, ndmi=None, ndvi=None,
            clear=0, total=30),
        row(date="2026-08-13", season_offset=0, ndmi=0.31, ndvi=0.7,
            clear=30, total=30),
    ]}
    out = optical_passes(rows)
    assert [p.date for p in out] == ["2026-08-13"]


def test_overlapping_tiles_on_one_day_are_one_observation():
    # A parcel near a tile edge appears in two granules on the same day. Left
    # as two, one clear day would count as two in every average.
    rows = {"features": [
        row(date="2026-08-13", season_offset=0, ndmi=0.30, ndvi=0.70,
            clear=20, total=30, ring_ndmi=0.28, ring_ndvi=0.6, ring_clear=4000),
        row(date="2026-08-13", season_offset=0, ndmi=0.36, ndvi=0.76,
            clear=10, total=30, ring_ndmi=0.34, ring_ndvi=0.7, ring_clear=2000),
    ]}
    out = optical_passes(rows)
    assert len(out) == 1
    # Weighted by clear pixels: (0.30*20 + 0.36*10) / 30.
    assert out[0].ndmi == pytest.approx(0.32, abs=1e-4)
    # And the clear count cannot exceed the parcel.
    assert out[0].clear_pixels <= 30
    assert out[0].clear_fraction <= 1.0


def test_an_impossible_index_is_dropped():
    rows = {"features": [
        row(date="2026-08-13", season_offset=0, ndmi=1.7, ndvi=0.7,
            clear=30, total=30),
    ]}
    assert optical_passes(rows) == []


def test_a_neighbourhood_with_no_cropland_stays_missing():
    rows = {"features": [
        row(date="2026-08-13", season_offset=0, ndmi=0.31, ndvi=0.7,
            clear=30, total=30, ring_ndmi=None, ring_clear=0),
    ]}
    out = optical_passes(rows)
    assert out[0].ring_ndmi is None
    assert out[0].ring_clear_pixels == 0


def test_seasons_are_kept_apart_and_ordered():
    rows = {"features": [
        row(date="2026-08-13", season_offset=0, ndmi=0.2, clear=30, total=30),
        row(date="2024-08-13", season_offset=2, ndmi=0.3, clear=30, total=30),
        row(date="2026-07-13", season_offset=0, ndmi=0.3, clear=30, total=30),
    ]}
    out = optical_passes(rows)
    assert [(p.season_offset, p.date) for p in out] == [
        (0, "2026-07-13"), (0, "2026-08-13"), (2, "2024-08-13"),
    ]


def test_a_pass_is_assigned_to_the_season_it_fell_in():
    from datetime import date

    start = date(2026, 7, 5)
    assert _offset_of("2026-08-01", start, 3) == 0
    assert _offset_of("2025-08-01", start, 3) == 1
    assert _offset_of("2023-07-20", start, 3) == 3


def test_29_february_shifts_to_a_real_date():
    assert _shift_years("2028-02-29", 1) == "2027-02-28"


def test_radar_rows_with_impossible_backscatter_are_dropped():
    # -0 dB is a surface no field produces.
    rows = {"features": [
        row(date="2026-08-03", vv=None, orbit="DESCENDING", relative_orbit=63),
        row(date="2026-08-15", vv=12.0, orbit="DESCENDING", relative_orbit=63),
        row(date="2026-08-27", vv=-10.25, orbit="DESCENDING", relative_orbit=63,
            coverage=1.0),
    ]}
    out = soil_passes(rows)
    assert [p.date for p in out] == ["2026-08-27"]
    assert out[0].relative_orbit == 63


def test_same_day_radar_frames_are_merged():
    rows = {"features": [
        row(date="2026-08-27", vv=-10.0, relative_orbit=63, coverage=0.6),
        row(date="2026-08-27", vv=-11.0, relative_orbit=63, coverage=0.4),
    ]}
    out = soil_passes(rows)
    assert len(out) == 1
    assert out[0].vv_db == pytest.approx(-10.5)


# ---------------------------------------------------------------------------
# Refusing what should not be billed
# ---------------------------------------------------------------------------


def test_a_season_long_window_is_refused_before_earth_engine():
    with pytest.raises(BadGeometryError) as exc:
        check_window("2026-01-01", "2026-12-31")
    assert str(MAX_WINDOW_DAYS) in str(exc.value)


def test_an_inverted_window_is_refused():
    with pytest.raises(BadGeometryError):
        check_window("2026-10-03", "2026-07-05")


def test_baseline_depth_is_bounded():
    with pytest.raises(ValueError):
        req(baseline_years=11)


def test_the_cache_key_includes_the_baseline_depth():
    # Three prior seasons and five are different questions.
    assert work_key(req(baseline_years=3)) != work_key(req(baseline_years=5))
    assert work_key(req()) == work_key(req())


# ---------------------------------------------------------------------------
# Synthetic
# ---------------------------------------------------------------------------


def test_a_synthetic_record_is_labelled():
    out = synthetic(req())
    assert out.missions == ["SYNTHETIC"]
    # Every prior season is present, so the platform's comparison is exercised.
    assert {p.season_offset for p in out.optical} == {0, 1, 2, 3}


def test_synthetic_output_is_stable():
    a = synthetic(req()).model_dump()
    b = synthetic(req()).model_dump()
    assert a == b


# ---------------------------------------------------------------------------
# Over HTTP
# ---------------------------------------------------------------------------


@pytest.fixture
def client():
    service.runner = Runner(Synthetic(), max_concurrent=2, max_queued=5)
    with TestClient(service.app) as c:
        service.runner = Runner(Synthetic(), max_concurrent=2, max_queued=5)
        yield c
    service.runner = None


def poll(client, task_id, timeout=5.0):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        r = client.get(f"/api/v1/moisture/status/{task_id}")
        assert r.status_code == 200, r.text
        body = r.json()
        if body["status"] != "PROCESSING":
            return body
        time.sleep(0.02)
    raise AssertionError("the task never left PROCESSING")


def submission(**over):
    body = {
        "geometry": PARCEL,
        "start_date": "2026-07-05",
        "end_date": "2026-10-03",
        "baseline_years": 3,
    }
    body.update(over)
    return body


def test_a_deployment_without_the_module_answers_404():
    # The load-bearing 404: the platform reads it as unavailable and assesses
    # without moisture, saying so.
    service.runner = None
    with TestClient(service.app) as c:
        service.runner = None
        r = c.post("/api/v1/moisture/submit", json=submission())
    assert r.status_code == 404
    assert r.json()["error"]["code"] == "MODULE_NOT_ENABLED"


def test_a_submission_completes_and_the_label_survives_the_wire(client):
    r = client.post("/api/v1/moisture/submit", json=submission())
    assert r.status_code == 202, r.text
    body = poll(client, r.json()["task_id"])
    assert body["status"] == "COMPLETED"
    assert body["result"]["missions"] == ["SYNTHETIC"]
    assert body["result"]["optical"], "no optical passes"
    assert body["result"]["soil"], "no soil passes"


def test_a_bad_window_is_400_and_nothing_is_queued(client):
    r = client.post("/api/v1/moisture/submit",
                    json=submission(start_date="2026-01-01", end_date="2026-12-31"))
    assert r.status_code == 400
    assert r.json()["error"]["code"] == "WINDOW_INVALID"
    # Read through the service's own readiness rather than its internals:
    # what matters is that nothing was queued, and that is what /readyz says.
    assert client.get("/readyz").json()["in_flight_or_queued"] == 0


def test_identical_requests_share_one_measurement(client):
    first = client.post("/api/v1/moisture/submit", json=submission()).json()
    poll(client, first["task_id"])
    second = client.post("/api/v1/moisture/submit", json=submission())
    assert second.status_code == 202
    assert "already done" in second.json().get("message", "")


# A task id belongs to one contract. A radar id asked for as moisture -- or the
# reverse -- is not found, rather than answered with the other kind's result.
def test_a_task_is_only_answerable_on_its_own_contract(client):
    m = client.post("/api/v1/moisture/submit", json=submission()).json()
    poll(client, m["task_id"])
    assert client.get(f"/api/v1/sar/status/{m['task_id']}").status_code == 404

    s = client.post("/api/v1/sar/submit", json={
        "geometry": PARCEL, "start_date": "2026-06-01", "end_date": "2026-08-01",
    }).json()
    assert client.get(f"/api/v1/moisture/status/{s['task_id']}").status_code == 404


# Radar and moisture share one concurrency budget, because they share one
# Earth Engine project quota. Two budgets would double the requests against a
# limit sized for one.
async def test_radar_and_moisture_share_one_concurrency_budget():
    import asyncio

    from esurvey_sar.earth import Window

    peak = {"now": 0, "max": 0}

    class Watching(Synthetic):
        def _enter(self):
            peak["now"] += 1
            peak["max"] = max(peak["max"], peak["now"])
            time.sleep(0.05)
            peak["now"] -= 1

        def series(self, g, w):
            self._enter()
            return super().series(g, w)

        def moisture(self, r):
            self._enter()
            return super().moisture(r)

    runner = Runner(Watching(), max_concurrent=2, max_queued=100)
    from esurvey_sar.schemas import Geometry

    g = Geometry(**PARCEL)
    subs = []
    for d in range(1, 6):
        w = Window("2026-06-01", f"2026-08-{d:02d}")
        subs.append(runner.submit(g, w))
        mreq = req(end_date=f"2026-09-{d:02d}")
        subs.append(runner.submit_work(
            "moisture", work_key(mreq), lambda r=mreq: runner.backend.moisture(r)))
    await asyncio.gather(*subs)
    await asyncio.gather(*list(runner._running))

    assert peak["max"] <= 2, f"{peak['max']} Earth Engine calls ran at once"
