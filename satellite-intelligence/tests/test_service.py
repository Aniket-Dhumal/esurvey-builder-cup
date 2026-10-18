"""The service, end to end over its own HTTP contract.

Two things carry most of the weight here.

A deployment that has not enabled radar must answer 404 on submit, because
the platform reads that as an unavailable module and assesses without radar,
saying so. Anything else -- a 500, a hang, a task that never completes --
turns an absent evidence source into a failed assessment.

And a synthetic reading must never be able to pass for an observation. It is
labelled at the source, the label survives the wire, and there is a test whose
only job is to fail if that label is ever dropped.
"""

from __future__ import annotations

import asyncio
import time

import pytest
from fastapi.testclient import TestClient

from esurvey_sar import main as service
from esurvey_sar.earth import (
    BadGeometryError,
    QuotaError,
    Synthetic,
    UnavailableError,
    Window,
    area_hectares,
    check,
    geometry_key,
)
from esurvey_sar.schemas import ErrorCode, Geometry, SARResult
from esurvey_sar.tasks import AtCapacityError, Runner

# A real Maharashtra parcel: roughly a hectare near Malegaon, Solapur.
PARCEL = {
    "type": "Polygon",
    "coordinates": [
        [
            [75.8130, 18.1700],
            [75.8140, 18.1700],
            [75.8140, 18.1709],
            [75.8130, 18.1709],
            [75.8130, 18.1700],
        ]
    ],
}


def submission(**over):
    body = {
        "geometry": PARCEL,
        "start_date": "2026-06-01",
        "end_date": "2026-08-01",
    }
    body.update(over)
    return body


def geometry(coords=None, kind="Polygon") -> Geometry:
    return Geometry(type=kind, coordinates=coords or PARCEL["coordinates"])


# ---------------------------------------------------------------------------
# The disabled deployment
# ---------------------------------------------------------------------------


def test_a_deployment_without_radar_answers_404_on_submit():
    # The load-bearing behaviour. The platform's client turns this into
    # ErrRadarUnavailable and proceeds without radar.
    service.runner = None
    with TestClient(service.app) as client:
        # TestClient runs the lifespan, which will set runner from settings;
        # force the disabled state after startup.
        service.runner = None
        r = client.post("/api/v1/sar/submit", json=submission())
    assert r.status_code == 404
    assert r.json()["error"]["code"] == "MODULE_NOT_ENABLED"


def test_a_disabled_deployment_is_alive_and_says_why_it_is_not_ready():
    service.runner = None
    with TestClient(service.app) as client:
        service.runner = None
        assert client.get("/healthz").json()["enabled"] is False

        ready = client.get("/readyz").json()
    assert ready["status"] == "disabled"
    # It has to say what the platform will do about it, not just that it is off.
    assert "without radar" in ready["effect"]


def test_liveness_does_not_depend_on_earth_engine():
    # A liveness probe that fails when a third party is down restarts a
    # healthy container in a loop.
    service.runner = None
    with TestClient(service.app) as client:
        r = client.get("/healthz")
    assert r.status_code == 200
    assert r.json()["status"] == "ok"


# ---------------------------------------------------------------------------
# The enabled deployment
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
        r = client.get(f"/api/v1/sar/status/{task_id}")
        assert r.status_code == 200, r.text
        body = r.json()
        if body["status"] != "PROCESSING":
            return body
        time.sleep(0.02)
    raise AssertionError("the task never left PROCESSING")


def test_a_submission_is_accepted_and_completes(client):
    r = client.post("/api/v1/sar/submit", json=submission())
    assert r.status_code == 202
    body = r.json()
    assert body["status"] == "PROCESSING"

    done = poll(client, body["task_id"])
    assert done["status"] == "COMPLETED"
    assert done["result"]["time_series"]


def test_every_pass_carries_its_orbit(client):
    # Backscatter differs by several decibels between ascending and descending
    # passes over an unchanged field. A series without the orbit lets a caller
    # compare across them and manufacture a change.
    r = client.post("/api/v1/sar/submit", json=submission())
    done = poll(client, r.json()["task_id"])

    for point in done["result"]["time_series"]:
        assert point["orbit"] in ("ASCENDING", "DESCENDING")
        assert "vv_db" in point and "vh_db" in point


def test_the_field_names_match_the_platforms_client(client):
    # These names are the wire contract with internal/satellite/radar.go.
    # A rename here leaves every field zero on the other side, and a zero
    # decibel reading is a plausible-looking number.
    r = client.post("/api/v1/sar/submit", json=submission())
    done = poll(client, r.json()["task_id"])

    result = done["result"]
    assert set(result) >= {"time_series", "missions"}
    point = result["time_series"][0]
    assert set(point) >= {"date", "vv_db", "vh_db", "orbit"}


def test_an_unknown_task_is_404_and_says_to_resubmit(client):
    r = client.get("/api/v1/sar/status/nothing")
    assert r.status_code == 404
    assert "resubmit" in r.json()["error"]["message"]


# ---------------------------------------------------------------------------
# Synthetic data must never pass for an observation
# ---------------------------------------------------------------------------


def test_synthetic_results_are_labelled_all_the_way_to_the_wire(client):
    # The only thing standing between a development convenience and a
    # fabricated finding on somebody's compensation record.
    r = client.post("/api/v1/sar/submit", json=submission())
    done = poll(client, r.json()["task_id"])

    assert done["result"]["missions"] == ["SYNTHETIC"]


def test_the_synthetic_backend_names_itself_in_readiness(client):
    body = client.get("/readyz").json()
    assert body["synthetic"] is True
    assert body["backend"] == "synthetic"
    assert "not an observation" in body["backend_detail"]


def test_synthetic_output_is_stable_for_the_same_parcel_and_window():
    # A developer chasing a bug should not also be chasing noise.
    s = Synthetic()
    w = Window("2026-06-01", "2026-08-01")
    a = s.series(geometry(), w)
    b = s.series(geometry(), w)
    assert [p.model_dump() for p in a.time_series] == [
        p.model_dump() for p in b.time_series
    ]


# ---------------------------------------------------------------------------
# What must not reach Earth Engine
# ---------------------------------------------------------------------------


def test_a_latitude_first_ring_is_refused_before_anything_is_billed():
    # A swapped pair puts a Maharashtra field in the Indian Ocean, where the
    # analysis succeeds and returns the backscatter of open water -- which is
    # exactly the signature this platform reads as flooding.
    with pytest.raises(ValueError, match="latitude-first"):
        Geometry(
            type="Polygon",
            coordinates=[
                [
                    [18.1700, 75.8130],
                    [18.1700, 75.8140],
                    [18.1709, 75.8140],
                    [18.1700, 75.8130],
                ]
            ],
        )


def test_a_taluka_sized_geometry_is_refused_with_a_named_code():
    big = [
        [
            [75.0, 18.0],
            [76.0, 18.0],
            [76.0, 19.0],
            [75.0, 19.0],
            [75.0, 18.0],
        ]
    ]
    with pytest.raises(BadGeometryError) as exc:
        check(geometry(big))
    assert exc.value.code == ErrorCode.GEOMETRY_TOO_LARGE
    assert "not a taluka" in str(exc.value)


def test_a_geometry_of_a_few_pixels_is_refused_as_speckle():
    tiny = [
        [
            [75.8130, 18.1700],
            [75.81302, 18.1700],
            [75.81302, 18.17002],
            [75.8130, 18.17002],
            [75.8130, 18.1700],
        ]
    ]
    with pytest.raises(BadGeometryError) as exc:
        check(geometry(tiny))
    assert "speckle" in str(exc.value)


def test_the_area_guard_agrees_with_the_parcel_it_is_given():
    # About one hectare: 0.001 degrees of latitude is roughly 111 m, and
    # 0.001 of longitude at 18N is about 106 m.
    area = area_hectares(geometry())
    assert 0.8 < area < 1.4, area


def test_a_window_that_ends_before_it_starts_is_refused(client):
    r = client.post(
        "/api/v1/sar/submit",
        json=submission(start_date="2026-08-01", end_date="2026-06-01"),
    )
    assert r.status_code == 400
    assert r.json()["error"]["code"] == ErrorCode.WINDOW_INVALID


def test_a_date_that_is_not_a_date_is_refused(client):
    r = client.post("/api/v1/sar/submit", json=submission(start_date="last June"))
    assert r.status_code == 422


# ---------------------------------------------------------------------------
# Scale
# ---------------------------------------------------------------------------


async def test_identical_work_is_done_once():
    # Two farmers in the same locality, and a parcel resubmitted after a poll
    # timeout, are the same question. Earth Engine should be asked once.
    calls = {"n": 0}

    class Counting(Synthetic):
        def series(self, g, w):
            calls["n"] += 1
            time.sleep(0.05)
            return super().series(g, w)

    r = Runner(Counting(), max_concurrent=4)
    w = Window("2026-06-01", "2026-08-01")

    ids = await asyncio.gather(*[r.submit(geometry(), w) for _ in range(5)])
    await asyncio.gather(*list(r._running))

    assert calls["n"] == 1, f"{calls['n']} analyses for one parcel and window"
    # Every caller gets an answer, whether or not theirs was the one that ran.
    assert all(task_id for task_id, _ in ids)


async def test_a_completed_result_answers_a_later_submission_without_recomputing():
    calls = {"n": 0}

    class Counting(Synthetic):
        def series(self, g, w):
            calls["n"] += 1
            return super().series(g, w)

    r = Runner(Counting(), max_concurrent=2)
    w = Window("2026-06-01", "2026-08-01")

    first, _ = await r.submit(geometry(), w)
    await asyncio.gather(*list(r._running))
    assert (await r.get(first)).status == "COMPLETED"

    second, fresh = await r.submit(geometry(), w)
    assert fresh is False
    assert (await r.get(second)).status == "COMPLETED"
    assert calls["n"] == 1


async def test_concurrency_is_bounded_so_earth_engine_is_not_throttled():
    # A district-wide event submits hundreds of parcels within minutes. Earth
    # Engine limits concurrency per project, and forwarding them all would
    # turn every one into a failure.
    peak = {"now": 0, "max": 0}

    class Watching(Synthetic):
        def series(self, g, w):
            peak["now"] += 1
            peak["max"] = max(peak["max"], peak["now"])
            time.sleep(0.05)
            peak["now"] -= 1
            return super().series(g, w)

    r = Runner(Watching(), max_concurrent=3, max_queued=100)

    # Distinct windows, so nothing is deduplicated and every one runs.
    subs = [
        r.submit(geometry(), Window("2026-06-01", f"2026-08-{d:02d}"))
        for d in range(1, 13)
    ]
    await asyncio.gather(*subs)
    await asyncio.gather(*list(r._running))

    assert peak["max"] <= 3, f"{peak['max']} analyses ran at once"


async def test_beyond_the_queue_limit_the_service_says_so_rather_than_accepting():
    # An unbounded queue is a slower way to fall over. A 503 with Retry-After
    # is a promise the caller can act on.
    class Slow(Synthetic):
        def series(self, g, w):
            time.sleep(0.3)
            return super().series(g, w)

    r = Runner(Slow(), max_concurrent=1, max_queued=2)

    await r.submit(geometry(), Window("2026-06-01", "2026-08-01"))
    await r.submit(geometry(), Window("2026-06-01", "2026-08-02"))

    with pytest.raises(AtCapacityError) as exc:
        await r.submit(geometry(), Window("2026-06-01", "2026-08-03"))
    assert exc.value.retry_after > 0

    await asyncio.gather(*list(r._running))


def test_at_capacity_is_503_with_retry_after(client):
    service.runner = Runner(_Blocking(), max_concurrent=1, max_queued=1)

    first = client.post(
        "/api/v1/sar/submit",
        json=submission(end_date="2026-08-05"),
    )
    assert first.status_code == 202

    second = client.post(
        "/api/v1/sar/submit",
        json=submission(end_date="2026-08-06"),
    )
    assert second.status_code == 503
    assert second.headers.get("Retry-After")
    assert second.json()["error"]["code"] == "AT_CAPACITY"


class _Blocking(Synthetic):
    def series(self, g, w):
        time.sleep(2)
        return super().series(g, w)


# ---------------------------------------------------------------------------
# Failures, each with a code a caller can branch on
# ---------------------------------------------------------------------------


async def test_a_quota_refusal_is_reported_as_quota_and_not_as_a_bug():
    class Throttled(Synthetic):
        def series(self, g, w):
            raise QuotaError("rate limit exceeded")

    r = Runner(Throttled())
    task_id, _ = await r.submit(geometry(), Window("2026-06-01", "2026-08-01"))
    await asyncio.gather(*list(r._running))

    task = await r.get(task_id)
    assert task.status == "FAILED"
    assert task.error_code == ErrorCode.EE_QUOTA


async def test_an_unreachable_earth_engine_is_reported_as_unavailable():
    class Down(Synthetic):
        def series(self, g, w):
            raise UnavailableError("Earth Engine would not initialise")

    r = Runner(Down())
    task_id, _ = await r.submit(geometry(), Window("2026-06-01", "2026-08-01"))
    await asyncio.gather(*list(r._running))

    task = await r.get(task_id)
    assert task.status == "FAILED"
    assert task.error_code == ErrorCode.EE_UNAVAILABLE
    assert "Earth Engine" in task.error


async def test_an_unexpected_failure_does_not_take_the_service_with_it():
    class Broken(Synthetic):
        def series(self, g, w):
            raise RuntimeError("something nobody anticipated")

    r = Runner(Broken())
    task_id, _ = await r.submit(geometry(), Window("2026-06-01", "2026-08-01"))
    await asyncio.gather(*list(r._running))

    task = await r.get(task_id)
    assert task.status == "FAILED"
    assert task.error_code == ErrorCode.INTERNAL

    # And the runner still works afterwards.
    r.backend = Synthetic()
    ok_id, _ = await r.submit(geometry(), Window("2026-06-02", "2026-08-01"))
    await asyncio.gather(*list(r._running))
    assert (await r.get(ok_id)).status == "COMPLETED"


async def test_a_failed_analysis_is_not_cached_as_an_answer():
    # Caching a failure would make one Earth Engine outage permanent for that
    # parcel and window.
    attempts = {"n": 0}

    class FlakyOnce(Synthetic):
        def series(self, g, w):
            attempts["n"] += 1
            if attempts["n"] == 1:
                raise UnavailableError("transient")
            return super().series(g, w)

    r = Runner(FlakyOnce())
    w = Window("2026-06-01", "2026-08-01")

    first, _ = await r.submit(geometry(), w)
    await asyncio.gather(*list(r._running))
    assert (await r.get(first)).status == "FAILED"

    second, _ = await r.submit(geometry(), w)
    await asyncio.gather(*list(r._running))
    assert (await r.get(second)).status == "COMPLETED"


# ---------------------------------------------------------------------------
# Housekeeping
# ---------------------------------------------------------------------------


async def test_finished_tasks_are_swept_but_their_answers_are_kept():
    # A platform that polls and then crashes should not leak memory for a day,
    # and a resubmission after a sweep should not cost another analysis.
    calls = {"n": 0}

    class Counting(Synthetic):
        def series(self, g, w):
            calls["n"] += 1
            return super().series(g, w)

    # A task answerable for a moment, an answer kept far longer: the two
    # lifetimes are deliberately different.
    r = Runner(Counting(), keep_completed_s=0.4, keep_results_s=60)
    w = Window("2026-06-01", "2026-08-01")

    first, _ = await r.submit(geometry(), w)
    await asyncio.gather(*list(r._running))
    assert (await r.get(first)).status == "COMPLETED"

    # Once the task has gone, the answer has not: a resubmission is served
    # from it rather than costing another analysis.
    await asyncio.sleep(0.5)
    assert await r.get(first) is None, "the old task should have been swept"

    second, fresh = await r.submit(geometry(), w)
    assert fresh is False
    assert (await r.get(second)).status == "COMPLETED"
    assert calls["n"] == 1


def test_the_cache_key_is_the_parcel_and_the_window():
    w1 = Window("2026-06-01", "2026-08-01")
    w2 = Window("2026-06-01", "2026-08-02")

    assert geometry_key(geometry(), w1) == geometry_key(geometry(), w1)
    assert geometry_key(geometry(), w1) != geometry_key(geometry(), w2)

    other = [
        [
            [75.9130, 18.1700],
            [75.9140, 18.1700],
            [75.9140, 18.1709],
            [75.9130, 18.1709],
            [75.9130, 18.1700],
        ]
    ]
    assert geometry_key(geometry(), w1) != geometry_key(geometry(other), w1)


def test_readiness_reports_what_the_service_is_actually_doing(client):
    body = client.get("/readyz").json()
    for key in ("tasks_held", "in_flight_or_queued", "cached_results", "queue_limit"):
        assert key in body, key


def test_an_empty_window_is_a_success_with_nothing_to_say():
    # The same convention the NDVI module uses for a wholly clouded window: a
    # parcel may genuinely have had no pass in a short window.
    class Nothing(Synthetic):
        def series(self, g, w):
            return SARResult(time_series=[], missions=["SENTINEL-1"])

    result = Nothing().series(geometry(), Window("2026-06-01", "2026-06-02"))
    assert result.time_series == []


# --- who may call this service ----------------------------------------------
#
# Nothing checked the caller until a security review asked what would happen on
# an orchestrated network, where "bound to loopback" stops being true.

def test_work_is_refused_without_the_token(monkeypatch):
    from esurvey_sar.config import settings

    monkeypatch.setattr(settings, "caller_token", "a-shared-token")

    from starlette.testclient import TestClient

    from esurvey_sar.main import app

    with TestClient(app) as c:
        assert c.post("/api/v1/sar/submit", json={}).status_code == 401
        assert c.get("/api/v1/sar/status/anything").status_code == 401


def test_the_right_token_is_let_through(monkeypatch):
    # Either header, because the platform's satellite client has always sent
    # X-API-Key and teaching it a second convention would buy nothing.
    from esurvey_sar.config import settings

    monkeypatch.setattr(settings, "caller_token", "a-shared-token")

    from starlette.testclient import TestClient

    from esurvey_sar.main import app

    with TestClient(app) as c:
        for headers in (
            {"Authorization": "Bearer a-shared-token"},
            {"X-API-Key": "a-shared-token"},
        ):
            # Not 401. Whatever it answers about the body is a different
            # question and not this test's.
            assert c.get(
                "/api/v1/sar/status/anything", headers=headers
            ).status_code != 401


def test_a_wrong_token_is_refused(monkeypatch):
    from esurvey_sar.config import settings

    monkeypatch.setattr(settings, "caller_token", "a-shared-token")

    from starlette.testclient import TestClient

    from esurvey_sar.main import app

    with TestClient(app) as c:
        r = c.get(
            "/api/v1/sar/status/anything",
            headers={"Authorization": "Bearer not-the-token"},
        )
        assert r.status_code == 401


def test_health_is_never_gated(monkeypatch):
    # An orchestrator polls these before it has any credentials, and a liveness
    # probe that fails authentication restarts the container it was checking.
    from esurvey_sar.config import settings

    monkeypatch.setattr(settings, "caller_token", "a-shared-token")

    from starlette.testclient import TestClient

    from esurvey_sar.main import app

    with TestClient(app) as c:
        assert c.get("/healthz").status_code == 200
        assert c.get("/readyz").status_code in (200, 503)


def test_an_unset_token_leaves_the_check_off(monkeypatch):
    # A laptop runs without any of this. A rule that stops a developer starting
    # the stack is a rule they delete rather than satisfy.
    from esurvey_sar.config import settings

    monkeypatch.setattr(settings, "caller_token", "")

    from starlette.testclient import TestClient

    from esurvey_sar.main import app

    with TestClient(app) as c:
        assert c.get("/api/v1/sar/status/anything").status_code != 401


# ---------------------------------------------------------------------------
# More than one replica
# ---------------------------------------------------------------------------
#
# The task table used to live in the process that took the submission, so a
# second replica answered "no such task" to the first replica's pollers and a
# restart lost the Earth Engine work in flight. State now lives in a store the
# replicas share (docs/PLATFORM_SCALE_REVIEW.md, A4); these are the properties
# that makes true.


async def test_another_replica_can_answer_a_poll():
    from esurvey_sar.store import MemoryStore

    shared = MemoryStore()
    one = Runner(Synthetic(), store=shared)
    two = Runner(Synthetic(), store=shared)

    task_id, fresh = await one.submit(geometry(), Window("2026-06-01", "2026-08-01"))
    assert fresh is True
    await asyncio.gather(*list(one._running))

    # The replica that never saw the submission answers for it.
    seen = await two.get(task_id)
    assert seen is not None, "a second replica could not find the first's task"
    assert seen.status == "COMPLETED"
    assert seen.result is not None


async def test_two_replicas_do_not_pay_twice_for_one_parcel():
    from esurvey_sar.store import MemoryStore

    calls = {"n": 0}

    class Counting(Synthetic):
        def series(self, g, w):
            calls["n"] += 1
            return super().series(g, w)

    shared = MemoryStore()
    one = Runner(Counting(), store=shared)
    two = Runner(Counting(), store=shared)
    w = Window("2026-06-01", "2026-08-01")

    first, fresh_one = await one.submit(geometry(), w)
    second, fresh_two = await two.submit(geometry(), w)
    await asyncio.gather(*list(one._running), *list(two._running))

    assert fresh_one is True
    assert fresh_two is False, "the second replica started a second analysis"
    assert first == second
    assert calls["n"] == 1


async def test_work_whose_replica_died_is_reported_rather_than_left_pending():
    # A poller waiting for something nobody is producing waits for ever. The
    # lease is what turns that into a failure the caller can act on -- the
    # platform's assessment queue reads it as retryable and asks again.
    from esurvey_sar.store import MemoryStore, TaskState

    shared = MemoryStore()
    runner = Runner(Synthetic(), store=shared)

    stranded = TaskState(id="abandoned-task", kind="sar")
    await shared.save(stranded, 3600)
    await shared.mark_pending(stranded.id)
    # No lease: whoever was producing this is gone.

    task = await runner.get(stranded.id)
    assert task.status == "FAILED"
    assert task.error_code == ErrorCode.EE_UNAVAILABLE
    assert "again" in task.error
