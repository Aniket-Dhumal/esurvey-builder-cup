"""The HTTP contract the platform depends on.

The Go client branches on these exact statuses: 404 means no model on this
deployment and the assessment proceeds without a ground score, 422 means the
footage could not be measured, 200 carries a measurement that must be labelled.
Each of those is a different outcome on an officer's dossier, so each is tested.
"""

from __future__ import annotations

import importlib

import cv2
import numpy as np
import pytest
from fastapi.testclient import TestClient

from .test_measure import canopy, write_clip


def app_with(monkeypatch, *, enabled: bool, token: str = "") -> TestClient:
    """Reload the module so the environment is read fresh."""
    monkeypatch.setenv("ESURVEY_VISION_BASELINE", "1" if enabled else "0")
    monkeypatch.setenv("ESURVEY_VISION_CALLER_TOKEN", token)

    from esurvey_vision import main as mod

    importlib.reload(mod)
    return TestClient(mod.app)


@pytest.fixture
def clip(tmp_path) -> bytes:
    path = write_clip(tmp_path / "walk.mp4", [canopy(angle_deg=70)] * 30)
    with open(path, "rb") as fh:
        return fh.read()


def test_liveness_says_nothing_about_the_model(monkeypatch) -> None:
    """A probe that failed when measuring is off would restart a healthy box."""
    c = app_with(monkeypatch, enabled=False)
    assert c.get("/healthz").status_code == 200


def test_readiness_is_honest_about_being_switched_off(monkeypatch) -> None:
    c = app_with(monkeypatch, enabled=False)
    r = c.get("/readyz")

    assert r.status_code == 503
    body = r.json()
    assert body["status"] == "disabled"
    # And says what the platform will do about it, not just that it is off.
    assert "absent" in body["effect"]


def test_readiness_admits_it_is_a_baseline(monkeypatch) -> None:
    c = app_with(monkeypatch, enabled=True)
    body = c.get("/readyz").json()

    assert body["status"] == "ready"
    assert body["baseline"] is True
    assert "not the specification's trained segmentation" in body["backend_detail"]


def test_a_disabled_deployment_answers_404(monkeypatch, clip) -> None:
    """The load-bearing 404: read as an absent evidence source, not a failure."""
    c = app_with(monkeypatch, enabled=False)
    r = c.post("/api/v1/vision/measure", files={"video": ("clip.mp4", clip, "video/mp4")})

    assert r.status_code == 404
    assert r.json()["error"] == "VISION_DISABLED"


def test_a_measurement_is_returned_and_labelled(monkeypatch, clip) -> None:
    c = app_with(monkeypatch, enabled=True)
    r = c.post("/api/v1/vision/measure", files={"video": ("clip.mp4", clip, "video/mp4")})

    assert r.status_code == 200, r.text
    body = r.json()

    # The contract groundcv.Measurements is decoded from, field for field.
    for key in ("stem_angle_degrees", "classes", "quality", "keyframes", "baseline"):
        assert key in body, f"the response is missing {key}"
    for key in ("healthy_foliage", "submerged", "necrotic", "background"):
        assert key in body["classes"], f"the classes are missing {key}"

    assert body["baseline"] is True, "an unlabelled measurement would be believed"
    assert 0 <= body["quality"] <= 1
    assert body["stem_angle_degrees"] > 40, "a flattened canopy read as upright"


def test_unusable_footage_is_422_not_a_zero(monkeypatch, tmp_path) -> None:
    """A clip of the sky must refuse, not report a field with no damage."""
    sky = np.full((480, 640, 3), (230, 200, 160), dtype=np.uint8)
    path = write_clip(tmp_path / "sky.mp4", [sky] * 30)
    with open(path, "rb") as fh:
        data = fh.read()

    c = app_with(monkeypatch, enabled=True)
    r = c.post("/api/v1/vision/measure", files={"video": ("sky.mp4", data, "video/mp4")})

    assert r.status_code == 422
    assert r.json()["error"] == "UNUSABLE_FOOTAGE"


def test_an_empty_upload_is_refused(monkeypatch) -> None:
    c = app_with(monkeypatch, enabled=True)
    r = c.post("/api/v1/vision/measure", files={"video": ("empty.mp4", b"", "video/mp4")})

    assert r.status_code == 400
    assert r.json()["error"] == "EMPTY"


def test_the_caller_token_is_enforced(monkeypatch, clip) -> None:
    """This endpoint decodes attacker-controlled video with a native library."""
    c = app_with(monkeypatch, enabled=True, token="the-shared-secret")

    files = {"video": ("clip.mp4", clip, "video/mp4")}
    assert c.post("/api/v1/vision/measure", files=files).status_code == 401
    assert c.post(
        "/api/v1/vision/measure", files=files, headers={"X-API-Key": "wrong"}
    ).status_code == 401

    ok = c.post(
        "/api/v1/vision/measure",
        files={"video": ("clip.mp4", clip, "video/mp4")},
        headers={"X-API-Key": "the-shared-secret"},
    )
    assert ok.status_code == 200, ok.text


def test_a_bearer_token_is_accepted_too(monkeypatch, clip) -> None:
    c = app_with(monkeypatch, enabled=True, token="the-shared-secret")
    r = c.post(
        "/api/v1/vision/measure",
        files={"video": ("clip.mp4", clip, "video/mp4")},
        headers={"Authorization": "Bearer the-shared-secret"},
    )
    assert r.status_code == 200, r.text


def test_garbage_bytes_do_not_crash_the_service(monkeypatch) -> None:
    """Whatever arrives, the decoder must fail into a 422 rather than a 500."""
    c = app_with(monkeypatch, enabled=True)
    r = c.post(
        "/api/v1/vision/measure",
        files={"video": ("evil.mp4", b"\x00\x01\x02not a video at all", "video/mp4")},
    )
    assert r.status_code == 422
