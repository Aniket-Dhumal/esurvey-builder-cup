"""The ground vision service.

Takes a survey clip, returns the three measurements the specification's ground
score is computed from. Stateless: the platform posts the bytes, this answers,
nothing is stored.

# Why it is a separate service

The same reason satellite-intelligence is. Decoding video needs OpenCV and, in
time, a GPU and a pair of trained models; the assessment API needs none of
that and should not carry it. Keeping the boundary at HTTP means the model can
be replaced -- and the classical baseline in measure.py deleted -- without the
platform changing at all.

# Why a caller token

The endpoint decodes attacker-controlled video with a native library. That is
not something to leave open on a network, whatever else is true.
"""

from __future__ import annotations

import os
import secrets
import tempfile
from typing import Any

import cv2
from fastapi import (
    Depends,
    FastAPI,
    File,
    Form,
    Header,
    HTTPException,
    UploadFile,
    status,
)
from fastapi.responses import JSONResponse, Response

from . import agent
from .measure import MAX_KEYFRAMES, measure, sharpest_frame

app = FastAPI(title="eSurvey ground vision", version="0.1.0")

# Sixty megabytes, the same ceiling survey.MaxVideoBytes puts on one clip.
MAX_UPLOAD_BYTES = 60 << 20

CALLER_TOKEN = os.environ.get("ESURVEY_VISION_CALLER_TOKEN", "")

# The classical baseline: off unless a deployment says otherwise. It is
# unvalidated and the platform refuses it outside development anyway; this is
# the second lock, on the service's own side, so a misconfigured platform
# cannot obtain one either.
BASELINE_ENABLED = os.environ.get("ESURVEY_VISION_BASELINE", "0").lower() in (
    "1",
    "true",
    "yes",
)

# The model path, which is the one production uses: keyframes to the
# assessment service, which holds the model credential. See agent.py.
def model_enabled() -> bool:
    return agent.configured()


def enabled() -> bool:
    """Whether this service can measure anything at all."""
    return model_enabled() or BASELINE_ENABLED


def require_caller(
    authorization: str | None = Header(default=None),
    x_api_key: str | None = Header(default=None),
) -> None:
    """Reject a request that does not carry the shared token."""
    if not CALLER_TOKEN:
        # Unset disables the check, which is the same convention the radar
        # service uses; the platform refuses to start hardened without it.
        return

    offered = x_api_key or ""
    if not offered and authorization and authorization.startswith("Bearer "):
        offered = authorization[len("Bearer ") :]

    if not secrets.compare_digest(offered, CALLER_TOKEN):
        raise HTTPException(
            status_code=status.HTTP_401_UNAUTHORIZED,
            detail="this service is called by the eSurvey platform API only",
        )


@app.get("/healthz")
async def healthz() -> dict[str, Any]:
    """Alive. Says nothing about whether measuring is enabled, deliberately."""
    return {"status": "ok"}


@app.get("/readyz")
async def readyz() -> Response:
    if not enabled():
        return JSONResponse(
            status_code=status.HTTP_503_SERVICE_UNAVAILABLE,
            content={
                "status": "disabled",
                "reason": (
                    "no assessment service is configured and the classical "
                    "baseline is switched off"
                ),
                "effect": (
                    "the platform assesses without a ground vision score and "
                    "reports it as an absent evidence source"
                ),
            },
        )
    return JSONResponse(
        content={
            "status": "ready",
            "backend": "assessment-model" if model_enabled() else "classical-baseline",
            "backend_detail": (
                "keyframes read by the assessment service's model; area "
                "shares are estimates, not a segmentation mask"
                if model_enabled()
                else "colour thresholds and a structure tensor; not the "
                "specification's trained segmentation"
            ),
            "model_configured": model_enabled(),
            # What the platform keys on: a baseline measurement is refused
            # outside development, a model measurement is not.
            "baseline": not model_enabled(),
            "max_keyframes": MAX_KEYFRAMES,
        }
    )


@app.post("/api/v1/vision/frame", dependencies=[Depends(require_caller)])
async def clip_frame(video: UploadFile = File(...)) -> Response:
    """One still frame from a clip, for the assessment report to print.

    # Why this is separate from measuring

    The report prints one frame per point of the walk beside what was found
    there, and it must be able to do that on footage the model could not
    measure -- which is most footage, today. Folding the frame into the measure
    response would mean a clip that failed to score printed nothing, and an
    officer would be asked to confirm a figure with no picture of the field.

    It is also why this endpoint does not check `enabled()`: pulling a frame out
    of a video is decoding, not judgement, and it works on a deployment with no
    model at all.

    # Why the farmer no longer sends a photograph as well

    The walk used to record a still and then a clip at each point, and upload
    both -- twelve megabytes of clip and a few hundred kilobytes of photograph,
    over a village connection, eight times. The clip already contains that
    frame. Taking it here costs the farmer nothing and halves what their phone
    has to push.
    """
    data = await video.read(MAX_UPLOAD_BYTES + 1)
    if len(data) > MAX_UPLOAD_BYTES:
        return JSONResponse(
            status_code=status.HTTP_413_REQUEST_ENTITY_TOO_LARGE,
            content={"error": "TOO_LARGE", "detail": "the clip exceeds 60 MB"},
        )
    if not data:
        return JSONResponse(
            status_code=status.HTTP_400_BAD_REQUEST,
            content={"error": "EMPTY", "detail": "no bytes were uploaded"},
        )

    with tempfile.NamedTemporaryFile(suffix=".bin", delete=False) as fh:
        fh.write(data)
        path = fh.name
    try:
        frame = sharpest_frame(path)
    finally:
        try:
            os.unlink(path)
        except OSError:
            pass

    if frame is None:
        # Not an error on our side: a clip this decoder cannot open is a
        # finding about the file. The report prints the point without a
        # picture rather than failing to print.
        return JSONResponse(
            status_code=status.HTTP_422_UNPROCESSABLE_ENTITY,
            content={
                "error": "UNREADABLE",
                "detail": "no frame could be decoded from this clip",
            },
        )

    ok, buf = cv2.imencode(".jpg", frame, [int(cv2.IMWRITE_JPEG_QUALITY), 82])
    if not ok:
        return JSONResponse(
            status_code=status.HTTP_500_INTERNAL_SERVER_ERROR,
            content={"error": "ENCODE_FAILED", "detail": "the frame would not encode"},
        )
    return Response(content=buf.tobytes(), media_type="image/jpeg")


@app.post("/api/v1/vision/measure", dependencies=[Depends(require_caller)])
async def measure_clip(
    video: UploadFile = File(...),
    crop_name: str | None = Form(default=None),
    station_no: int | None = Form(default=None),
) -> Response:
    """Measure one survey clip.

    404 when the baseline is switched off. The load-bearing 404: the platform
    reads it as an unavailable evidence source and assesses without a ground
    score, which is the same path it takes when radar is off.
    """
    if not enabled():
        return JSONResponse(
            status_code=status.HTTP_404_NOT_FOUND,
            content={
                "error": "VISION_DISABLED",
                "detail": "no ground vision model is enabled on this deployment",
            },
        )

    data = await video.read(MAX_UPLOAD_BYTES + 1)
    if len(data) > MAX_UPLOAD_BYTES:
        return JSONResponse(
            status_code=status.HTTP_413_REQUEST_ENTITY_TOO_LARGE,
            content={"error": "TOO_LARGE", "detail": "the clip exceeds 60 MB"},
        )
    if not data:
        return JSONResponse(
            status_code=status.HTTP_400_BAD_REQUEST,
            content={"error": "EMPTY", "detail": "no bytes were uploaded"},
        )

    # OpenCV's decoder wants a path. A temporary file, removed straight after:
    # nothing here keeps evidence, and the platform's own store is the only
    # place a clip is meant to live.
    with tempfile.NamedTemporaryFile(suffix=".bin", delete=False) as fh:
        fh.write(data)
        path = fh.name
    unavailable: str | None = None
    try:
        if model_enabled():
            # The model reads the walk. A service that cannot answer leaves
            # the ground signal absent rather than falling back to a weaker
            # instrument that would be believed just as much.
            try:
                m = agent.measure_with_agent(path, crop_name, station_no)
            except agent.AgentUnavailable as exc:
                m, unavailable = None, str(exc)
        else:
            m = measure(path)
    finally:
        try:
            os.unlink(path)
        except OSError:
            pass

    if unavailable is not None:
        return JSONResponse(
            status_code=status.HTTP_503_SERVICE_UNAVAILABLE,
            content={"error": "MODEL_UNAVAILABLE", "detail": unavailable},
        )

    if m is None:
        return JSONResponse(
            status_code=status.HTTP_422_UNPROCESSABLE_ENTITY,
            content={
                "error": "UNUSABLE_FOOTAGE",
                "detail": (
                    "too few usable keyframes, or the camera was not pointed "
                    "at a crop"
                ),
            },
        )

    return JSONResponse(
        content={
            "stem_angle_degrees": m.stem_angle_degrees,
            "classes": {
                "healthy_foliage": m.classes.healthy_foliage,
                "submerged": m.classes.submerged,
                "necrotic": m.classes.necrotic,
                "background": m.classes.background,
            },
            "quality": m.quality,
            "keyframes": m.keyframes,
            "canopy_height_ratio": m.canopy_height_ratio,
            # The label the platform refuses on outside development.
            "baseline": m.baseline,
            "quality_notes": m.quality_notes,
            # Whether the field holds the crop that was claimed. Null means
            # the question could not be settled, which is not the same as no.
            "crop_declared": m.crop_declared,
            "crop_seen": m.crop_seen,
            "crop_matches": m.crop_matches,
            "crop_mismatch_confidence": m.crop_mismatch_confidence,
            "crop_mismatch_detail": m.crop_mismatch_detail,
        }
    )


def main() -> None:
    import uvicorn

    uvicorn.run(
        app,
        host=os.environ.get("ESURVEY_VISION_HOST", "0.0.0.0"),
        port=int(os.environ.get("ESURVEY_VISION_PORT", "8300")),
    )


if __name__ == "__main__":
    main()
