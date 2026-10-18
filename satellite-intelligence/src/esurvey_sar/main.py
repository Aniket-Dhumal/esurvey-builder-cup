"""The HTTP surface.

Deliberately the same shape as the Farm Analytics Engine's NDVI and terrain
modules -- submit returns a task id, the caller polls until the status leaves
PROCESSING -- because the platform's client already speaks that, and a second
convention would be a second thing to get wrong.

One behaviour is load-bearing and easy to lose in a refactor: a deployment
that has not enabled this must answer 404 on submit. The platform reads that
as ErrRadarUnavailable and proceeds without radar, saying so. Anything else --
a 500, a hang, a task that never completes -- turns an absent evidence source
into a failed assessment.
"""

from __future__ import annotations

import logging
from contextlib import asynccontextmanager
from typing import Any

from fastapi import Depends, FastAPI, Response
from fastapi.responses import JSONResponse

from .callers import require_caller
from .config import settings
from .earth import BadGeometryError, EarthEngine, Synthetic, Window
from .moisture import MoistureRequest, check_window, work_key
from .schemas import (
    ErrorCode,
    SARRequest,
    Status,
    StatusResponse,
    SubmitResponse,
)
from .store import RedisStore
from .tasks import AtCapacityError, Runner

log = logging.getLogger(__name__)

VERSION = "1.0.0"

runner: Runner | None = None


@asynccontextmanager
async def lifespan(app: FastAPI):
    global runner

    if not settings.configured:
        log.warning(
            "no Earth Engine project and synthetic mode is off: /api/v1/sar/submit "
            "will answer 404, and the platform will assess without radar and "
            "say so"
        )
    else:
        backend = (
            Synthetic() if settings.synthetic else EarthEngine(settings.ee_project)
        )
        # Shared state when a Redis is configured, so a second replica can
        # answer the first replica's pollers and a restart does not lose the
        # Earth Engine work in flight. A dict otherwise, which is what a
        # laptop and the tests run on. See store.py.
        store = None
        if settings.redis_url:
            import redis.asyncio as aioredis  # imported here: optional dependency

            store = RedisStore(aioredis.from_url(settings.redis_url))
            log.info("sar task state is shared through redis")

        runner = Runner(
            backend,
            max_concurrent=settings.max_concurrent,
            max_queued=settings.max_queued,
            keep_completed_s=settings.keep_completed_s,
            store=store,
        )
        log.info(
            "sar service ready backend=%s concurrency=%d queue_limit=%d",
            backend.name,
            settings.max_concurrent,
            settings.max_queued,
        )
        if settings.synthetic:
            log.warning(
                "SYNTHETIC MODE: backscatter is generated, not observed. Every "
                "result is labelled SYNTHETIC and the platform surfaces it."
            )

    yield


app = FastAPI(
    title="eSurvey satellite intelligence",
    version=VERSION,
    lifespan=lifespan,
)


def _error(status: int, code: ErrorCode | str, message: str, **extra: Any) -> Response:
    return JSONResponse(
        status_code=status,
        content={"error": {"code": str(code), "message": message}},
        headers=extra.pop("headers", None),
    )


@app.get("/healthz")
async def healthz() -> dict[str, Any]:
    """Alive. Says nothing about Earth Engine, deliberately.

    A liveness probe that fails when a third party is down restarts a healthy
    container in a loop, which is the failure mode this separation exists to
    avoid.
    """
    return {
        "status": "ok",
        "version": VERSION,
        "module": "sar",
        "enabled": runner is not None,
        # Which backend is behind the module, stated here as well as on
        # /readyz.
        #
        # "enabled": true reads as "this radar module works", and with no
        # Earth Engine project configured what it actually means is "a runner
        # object exists" -- one that returns generated backscatter. An
        # operator whose monitoring polls the liveness probe would have seen a
        # healthy radar module for as long as that was untrue.
        #
        # Saying it costs nothing and does not make liveness depend on Earth
        # Engine, which is what the probe must never do: a liveness check that
        # fails when a third party is down restarts a healthy container in a
        # loop.
        "backend": (
            runner.backend.name if runner is not None else "none"
        ),
    }


@app.get("/readyz")
async def readyz() -> Response:
    """Ready to do work, and honest about what is behind it."""
    if runner is None:
        return JSONResponse(
            status_code=200,
            content={
                "status": "disabled",
                "reason": "no Earth Engine project is configured and synthetic "
                "mode is off",
                "effect": "the platform assesses without radar and reports it "
                "as an absent evidence source",
            },
        )

    ok, detail = await _healthy(runner)
    body = {
        "status": "ready" if ok else "degraded",
        "backend": runner.backend.name,
        "backend_detail": detail,
        # What is actually running, not what configuration asked for. A
        # readiness endpoint that reports its own settings would keep saying
        # "real" after somebody swapped the backend, which is the one lie this
        # service must not tell.
        "synthetic": runner.backend.name == "synthetic",
        **(await runner.stats()),
    }
    # Degraded, not down: submissions are still accepted and will fail with a
    # named code, which the platform records as an absent source rather than
    # as an assessment failure.
    return JSONResponse(status_code=200, content=body)


async def _healthy(r: Runner) -> tuple[bool, str]:
    import asyncio

    try:
        return await asyncio.wait_for(asyncio.to_thread(r.backend.healthy), timeout=15)
    except TimeoutError:
        return False, "the backend did not answer within fifteen seconds"
    except Exception as exc:  # pragma: no cover - defensive
        return False, str(exc)


@app.post("/api/v1/sar/submit", dependencies=[Depends(require_caller)])
async def submit(req: SARRequest) -> Response:
    if runner is None:
        # The load-bearing 404. The platform reads it as an unavailable
        # module and proceeds without radar.
        return _error(
            404,
            "MODULE_NOT_ENABLED",
            "the radar module is not enabled on this deployment",
        )

    if req.end_date < req.start_date:
        return _error(
            400,
            ErrorCode.WINDOW_INVALID,
            f"{req.end_date} is before {req.start_date}",
        )

    window = Window(req.start_date, req.end_date)

    try:
        task_id, fresh = await runner.submit(req.geometry, window)
    except AtCapacityError as exc:
        return JSONResponse(
            status_code=503,
            content={
                "error": {
                    "code": "AT_CAPACITY",
                    "message": "the service is at capacity; this is a queue "
                    "limit rather than a failure",
                }
            },
            headers={"Retry-After": str(exc.retry_after)},
        )

    return JSONResponse(
        status_code=202,
        content=SubmitResponse(
            task_id=task_id,
            status=Status.PROCESSING,
            message=None
            if fresh
            else "answered from work already done for this parcel and window",
        ).model_dump(exclude_none=True),
    )


@app.get("/api/v1/sar/status/{task_id}", dependencies=[Depends(require_caller)])
async def status(task_id: str) -> Response:
    if runner is None:
        return _error(
            404,
            "MODULE_NOT_ENABLED",
            "the radar module is not enabled on this deployment",
        )

    task = await runner.get(task_id)
    if task is None or task.kind != "sar":
        # A task the service has swept, or one that never existed -- or one
        # that belongs to the moisture contract. All are 404: the caller
        # should resubmit, which is cheap because the result outlives the
        # task.
        return _error(404, "TASK_NOT_FOUND", "no such task; resubmit the parcel")

    return JSONResponse(
        status_code=200,
        content=StatusResponse(
            status=task.status,
            result=task.result,
            error=task.error,
            error_code=task.error_code,
        ).model_dump(exclude_none=True),
    )


# ---------------------------------------------------------------------------
# Moisture: canopy water (NDMI) and the Sentinel-1 soil record
# ---------------------------------------------------------------------------
#
# The same contract as radar, and the same runner, because it is the same
# Earth Engine project and the same concurrency limit. See moisture.py for
# what is measured and why the comparisons travel in the same call.
#
# And the same load-bearing 404 when the module is off: the platform reads it
# as ErrMoistureUnavailable and assesses without it, saying so.


@app.post("/api/v1/moisture/submit", dependencies=[Depends(require_caller)])
async def moisture_submit(req: MoistureRequest) -> Response:
    if runner is None:
        return _error(
            404,
            "MODULE_NOT_ENABLED",
            "the moisture module is not enabled on this deployment",
        )

    # Refused before anything is queued, let alone billed: an inverted or
    # season-long window is a caller's mistake, and finding it out from a
    # failed task a few seconds later is the wrong place.
    try:
        check_window(req.start_date, req.end_date)
    except BadGeometryError as exc:
        return _error(400, exc.code, str(exc))

    try:
        task_id, fresh = await runner.submit_work(
            "moisture", work_key(req), lambda: runner.backend.moisture(req)
        )
    except AtCapacityError as exc:
        return JSONResponse(
            status_code=503,
            content={
                "error": {
                    "code": "AT_CAPACITY",
                    "message": "the service is at capacity; this is a queue "
                    "limit rather than a failure",
                }
            },
            headers={"Retry-After": str(exc.retry_after)},
        )

    return JSONResponse(
        status_code=202,
        content=SubmitResponse(
            task_id=task_id,
            status=Status.PROCESSING,
            message=None
            if fresh
            else "answered from work already done for this parcel and window",
        ).model_dump(exclude_none=True),
    )


@app.get("/api/v1/moisture/status/{task_id}", dependencies=[Depends(require_caller)])
async def moisture_status(task_id: str) -> Response:
    if runner is None:
        return _error(
            404,
            "MODULE_NOT_ENABLED",
            "the moisture module is not enabled on this deployment",
        )

    task = await runner.get(task_id)
    # A radar task id asked for here is not found, rather than answered with
    # a radar series under a moisture status: a task belongs to one contract.
    if task is None or task.kind != "moisture":
        return _error(404, "TASK_NOT_FOUND", "no such task; resubmit the parcel")

    body: dict[str, Any] = {"status": str(task.status)}
    if task.result is not None:
        body["result"] = task.result
    if task.error is not None:
        body["error"] = task.error
    if task.error_code is not None:
        body["error_code"] = task.error_code
    return JSONResponse(status_code=200, content=body)


def main() -> None:  # pragma: no cover - entry point
    import uvicorn

    logging.basicConfig(level=logging.INFO)
    uvicorn.run(app, host=settings.host, port=settings.port)


if __name__ == "__main__":  # pragma: no cover
    main()
