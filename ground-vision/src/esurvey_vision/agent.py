"""Measuring a clip with the assessment model.

# Why this service still exists when the model does the reading

Video decoding does not belong in the assessment service, and the model
credential does not belong here. So this service does what it is good at --
open the container, sample the walk evenly, drop the frames that are too dark
or too blurred to read -- and sends keyframes to the agent service, which
holds the credential and returns the measurement.

That split also means one clip costs one model call rather than one per frame,
and the platform's contract does not change: it still posts a clip here and
gets the same JSON back.

# What is different about the numbers

The specification's pipeline segments every pixel, so its class shares are
counts. A model estimating area shares by eye has no mask to count. The shares
it returns are converted to the contract's counts over a fixed area basis per
keyframe, which is exact for everything the score actually uses: the ratios
(submerged and necrotic over crop) and the crop share of the frame are the
same number either way. What is lost is any pretence that a count means a
pixel -- see AREA_UNITS.

# What happens when the model cannot answer

Nothing is invented. A clip the model refuses (no crop in view) is an
unusable-footage answer, and a model that fails or times out leaves the
ground signal absent, which the platform already handles: an assessment says
the heaviest evidence source was missing rather than scoring a guess.
"""

from __future__ import annotations

import base64
import logging
import os

import cv2
import numpy as np
import urllib.error
import urllib.request
import json

from .measure import Classes, Measurements, keyframes

log = logging.getLogger("esurvey_vision.agent")

#: Where the assessment service is. Empty turns the model path off, and the
#: service falls back to the classical baseline if that is enabled.
AGENT_URL = os.environ.get("ESURVEY_VISION_AGENT_URL", "").rstrip("/")

#: The token the agent service demands of its callers -- the same internal
#: token the platform uses to reach both services.
AGENT_TOKEN = os.environ.get("ESURVEY_VISION_AGENT_TOKEN", "")

#: One model call over eight images. Generous, because the alternative to
#: waiting is an assessment with no ground evidence at all.
AGENT_TIMEOUT_S = float(os.environ.get("ESURVEY_VISION_AGENT_TIMEOUT", "180"))

#: How many keyframes are sent. The agent service caps this too.
AGENT_KEYFRAMES = int(os.environ.get("ESURVEY_VISION_AGENT_KEYFRAMES", "8"))

#: The area basis one keyframe contributes to the class counts.
#:
#: The contract is pixel counts and the model returns shares, so a basis has to
#: be chosen. Ten thousand units per frame keeps four decimal places of the
#: share, sums exactly, and is small enough that nobody reading a dossier will
#: mistake the number for a pixel count from a real mask.
AREA_UNITS = 10_000

#: JPEG quality for the frames sent. 80 is visually lossless for this purpose
#: and keeps eight frames inside a few megabytes.
JPEG_QUALITY = 80

#: The ceiling on how much the model may say it trusts itself.
#:
#: The fusion gives the ground signal 0.50 of a compensation figure in clear
#: weather and 0.65 under cloud, but only where quality is 0.85 or better --
#: a branch the specification wrote for a segmentation model validated against
#: ground truth. This instrument has not been validated against anything: it
#: is a multimodal model's estimate of area shares, and its confidence is its
#: own opinion of its own work.
#:
#: So it is capped below that branch until somebody has measured it against
#: labelled field footage. At 0.80 it still lands in the mixed branch and
#: carries 0.35 of the figure, which is far more than the classical baseline's
#: 0.20 and less than a validated model's 0.65. Raise it -- to 1.0, which is
#: no cap -- when there is a validation to point at.
MODEL_QUALITY_CAP = float(os.environ.get("ESURVEY_VISION_QUALITY_CAP", "0.80"))


def configured() -> bool:
    """Whether the model path can be used at all."""
    return bool(AGENT_URL)


def _data_uri(frame: np.ndarray) -> str | None:
    ok, buf = cv2.imencode(".jpg", frame, [int(cv2.IMWRITE_JPEG_QUALITY), JPEG_QUALITY])
    if not ok:
        return None
    return "data:image/jpeg;base64," + base64.b64encode(buf.tobytes()).decode("ascii")


def _counts(m: dict, frames: int) -> Classes:
    """Turn the model's area shares into the contract's counts.

    The shares are normalised first: a model asked for four numbers that sum
    to one will occasionally return 0.99 or 1.02, and the ratios the score is
    built from must not move because of that.
    """
    healthy = max(0.0, float(m.get("healthy_fraction") or 0.0))
    submerged = max(0.0, float(m.get("submerged_fraction") or 0.0))
    necrotic = max(0.0, float(m.get("necrotic_fraction") or 0.0))
    background = max(0.0, float(m.get("background_fraction") or 0.0))

    total = healthy + submerged + necrotic + background
    if total <= 0:
        return Classes()

    basis = AREA_UNITS * max(1, frames)
    healthy_n = round(basis * healthy / total)
    submerged_n = round(basis * submerged / total)
    necrotic_n = round(basis * necrotic / total)
    # The remainder lands on background, which is the one class no ratio is
    # taken over, so rounding cannot move a loss figure.
    background_n = basis - healthy_n - submerged_n - necrotic_n
    return Classes(
        healthy_foliage=int(healthy_n),
        submerged=int(submerged_n),
        necrotic=int(necrotic_n),
        background=int(max(0, background_n)),
    )


class AgentUnavailable(RuntimeError):
    """The agent service could not be reached or did not answer."""


def measure_with_agent(
    video_path: str, crop_name: str | None = None, station_no: int | None = None
) -> Measurements | None:
    """Measure one clip through the assessment model.

    Returns None when the footage cannot be measured -- too few usable frames,
    or a model that reports no crop in view. Raises AgentUnavailable when the
    service itself could not answer, which the caller reports as an absent
    evidence source rather than as a finding about the field.
    """
    if not configured():
        raise AgentUnavailable("no agent service is configured")

    frames = keyframes(video_path, want=AGENT_KEYFRAMES)
    if len(frames) < 2:
        log.info("clip yielded %d usable keyframes", len(frames))
        return None

    images = []
    for frame in frames:
        uri = _data_uri(frame)
        if uri is not None:
            images.append({"kind": "survey_keyframe", "data_uri": uri})
    if len(images) < 2:
        return None

    body = json.dumps(
        {
            "keyframes": images,
            "crop_name": crop_name,
            "station_no": station_no,
        }
    ).encode("utf-8")

    req = urllib.request.Request(
        AGENT_URL + "/api/v1/ground-measure",
        data=body,
        headers={"Content-Type": "application/json"},
        method="POST",
    )
    if AGENT_TOKEN:
        req.add_header("Authorization", "Bearer " + AGENT_TOKEN)

    try:
        with urllib.request.urlopen(req, timeout=AGENT_TIMEOUT_S) as resp:
            payload = json.loads(resp.read().decode("utf-8"))
    except urllib.error.HTTPError as exc:
        raise AgentUnavailable(
            f"the assessment service answered {exc.code}"
        ) from None
    except (urllib.error.URLError, TimeoutError, ValueError) as exc:
        raise AgentUnavailable("the assessment service did not answer") from exc

    if not payload.get("measured"):
        reason = payload.get("reason") or "the model returned no measurement"
        # "No crop in view" is a finding about the footage; a model that
        # failed is not. Reading the two the same way would tell an officer
        # the farmer filmed a path when in fact nobody looked -- seen on the
        # first live run, where a rejected credential came back as unusable
        # footage.
        if payload.get("failed"):
            log.warning("the model could not measure the clip: %s", reason)
            raise AgentUnavailable(reason)
        log.info("clip not measured: %s", reason)
        return None

    m = payload.get("measurement") or {}
    notes = list(m.get("quality_notes") or [])
    notes.append(
        "estimated by "
        + str(payload.get("model") or "the assessment model")
        + " from "
        + str(len(images))
        + " keyframes, not from a segmentation mask"
    )
    for observation in (m.get("observations") or [])[:4]:
        notes.append("observed: " + str(observation))

    quality = min(1.0, max(0.0, float(m.get("quality") or 0.0)))
    if quality > MODEL_QUALITY_CAP:
        notes.append(
            f"confidence capped at {MODEL_QUALITY_CAP:.2f}: this instrument "
            "has not been validated against labelled field footage"
        )
        quality = MODEL_QUALITY_CAP

    matches = m.get("crop_matches_declaration")
    if not crop_name:
        # Nothing was declared, so there is nothing to disagree with. Said
        # explicitly rather than left to a null that could read as "no
        # mismatch found".
        matches = None
    mismatch_detail = m.get("crop_mismatch_detail") if matches is False else None
    if matches is False:
        notes.append(
            "the crop in the footage does not look like the declared "
            + str(crop_name)
            + (f": {mismatch_detail}" if mismatch_detail else "")
        )

    height = m.get("canopy_height_ratio")
    return Measurements(
        stem_angle_degrees=float(m.get("stem_angle_degrees") or 0.0),
        classes=_counts(m, len(images)),
        quality=quality,
        keyframes=len(images),
        canopy_height_ratio=float(height) if height is not None else None,
        # Not a baseline: the platform may use this. The instrument is named
        # in the notes rather than hidden behind a boolean.
        baseline=False,
        quality_notes=notes,
        crop_declared=crop_name or None,
        crop_seen=(m.get("crop_seen") or None),
        crop_matches=matches,
        crop_mismatch_confidence=(
            float(m.get("crop_mismatch_confidence") or 0.0) if matches is False else 0.0
        ),
        crop_mismatch_detail=mismatch_detail,
    )
