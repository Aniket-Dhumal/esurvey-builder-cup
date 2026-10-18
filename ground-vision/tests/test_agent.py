"""What the platform is told when the model reads a clip, and when it cannot.

The model call itself is stubbed. What is tested here is the part that decides
what a farmer's claim is measured on: how estimated area shares become the
contract's counts, and -- the distinction that matters most -- whether the
platform is told "the footage showed no crop" or "nobody looked".
"""

from __future__ import annotations

import io
import json

import pytest

from esurvey_vision import agent


class _Response:
    """Minimal stand-in for the object urlopen returns."""

    def __init__(self, payload: dict) -> None:
        self._body = json.dumps(payload).encode()

    def read(self) -> bytes:
        return self._body

    def __enter__(self) -> "_Response":
        return self

    def __exit__(self, *_: object) -> None:
        return None


def _answer(monkeypatch, payload: dict) -> None:
    monkeypatch.setattr(agent, "AGENT_URL", "http://agent.invalid")
    monkeypatch.setattr(
        agent.urllib.request, "urlopen", lambda *a, **k: _Response(payload)
    )


def _frames(monkeypatch, n: int = 4) -> None:
    import numpy as np

    frame = np.full((64, 64, 3), 120, dtype=np.uint8)
    monkeypatch.setattr(agent, "keyframes", lambda *a, **k: [frame] * n)


MEASURED = {
    "measured": True,
    "model": "gemini-3.1-pro-preview",
    "measurement": {
        "crop_visible": True,
        "stem_angle_degrees": 62.0,
        "healthy_fraction": 0.20,
        "submerged_fraction": 0.30,
        "necrotic_fraction": 0.10,
        "background_fraction": 0.40,
        "quality": 0.72,
        "quality_notes": ["even light, steady walk"],
        "observations": ["water standing between rows"],
        "canopy_height_ratio": 0.55,
    },
}


def test_area_shares_become_the_contracts_counts(monkeypatch):
    _frames(monkeypatch, 4)
    _answer(monkeypatch, MEASURED)

    m = agent.measure_with_agent("/tmp/clip", crop_name="Soybean", station_no=2)

    assert m is not None
    crop = m.classes.crop
    # The ratios the score is built from survive the conversion exactly: of
    # the 60% that is crop, half is submerged and a sixth is necrotic.
    assert m.classes.submerged / crop == pytest.approx(0.5, abs=0.001)
    assert m.classes.necrotic / crop == pytest.approx(1 / 6, abs=0.001)
    # And the crop share of the frame, which is what shows a reading taken
    # mostly of sky.
    total = crop + m.classes.background
    assert crop / total == pytest.approx(0.6, abs=0.001)
    assert m.stem_angle_degrees == 62.0
    assert m.quality == pytest.approx(0.72)
    assert m.keyframes == 4
    assert m.canopy_height_ratio == pytest.approx(0.55)


def test_a_model_measurement_is_not_labelled_a_baseline(monkeypatch):
    # The label the platform refuses on outside development. A baseline here
    # would mean the heaviest evidence source stays absent in production,
    # which is the state this work exists to end.
    _frames(monkeypatch)
    _answer(monkeypatch, MEASURED)

    m = agent.measure_with_agent("/tmp/clip")

    assert m is not None
    assert m.baseline is False
    # And the instrument is named where an officer reads the quality notes,
    # rather than hidden behind the boolean.
    assert any("gemini" in n.lower() for n in m.quality_notes)
    assert any("not from a segmentation mask" in n for n in m.quality_notes)


def test_no_crop_in_the_footage_is_a_finding_not_a_failure(monkeypatch):
    _frames(monkeypatch)
    _answer(
        monkeypatch,
        {
            "measured": False,
            "failed": False,
            "reason": "no crop is visible in the footage",
            "model": "gemini-3.1-pro-preview",
        },
    )

    assert agent.measure_with_agent("/tmp/clip") is None


def test_a_model_failure_is_never_reported_as_footage_that_showed_no_crop(monkeypatch):
    # Seen on the first live run: a credential the provider refused came back
    # to the platform as "unusable footage", which tells an officer the farmer
    # filmed a path when in fact nobody looked at the video at all.
    _frames(monkeypatch)
    _answer(
        monkeypatch,
        {
            "measured": False,
            "failed": True,
            "reason": "the model service refused this deployment's credential",
            "model": "gemini-3.1-pro-preview",
        },
    )

    with pytest.raises(agent.AgentUnavailable) as exc:
        agent.measure_with_agent("/tmp/clip")
    assert "credential" in str(exc.value)


def test_a_service_that_does_not_answer_leaves_the_signal_absent(monkeypatch):
    _frames(monkeypatch)
    monkeypatch.setattr(agent, "AGENT_URL", "http://agent.invalid")

    def boom(*_a, **_k):
        raise agent.urllib.error.URLError("no route to host")

    monkeypatch.setattr(agent.urllib.request, "urlopen", boom)

    with pytest.raises(agent.AgentUnavailable):
        agent.measure_with_agent("/tmp/clip")


def test_a_clip_with_too_few_usable_frames_is_not_sent_to_the_model(monkeypatch):
    called = False

    def urlopen(*_a, **_k):
        nonlocal called
        called = True
        return _Response(MEASURED)

    monkeypatch.setattr(agent, "AGENT_URL", "http://agent.invalid")
    monkeypatch.setattr(agent.urllib.request, "urlopen", urlopen)
    _frames(monkeypatch, 1)

    assert agent.measure_with_agent("/tmp/clip") is None
    assert not called, "a single frame is not a walk; it must not cost a model call"


def test_shares_that_do_not_sum_to_one_are_normalised(monkeypatch):
    _frames(monkeypatch, 2)
    payload = json.loads(json.dumps(MEASURED))
    payload["measurement"].update(
        healthy_fraction=0.4, submerged_fraction=0.4, necrotic_fraction=0.0,
        background_fraction=0.4,
    )
    _answer(monkeypatch, payload)

    m = agent.measure_with_agent("/tmp/clip")

    assert m is not None
    assert m.classes.crop + m.classes.background == agent.AREA_UNITS * 2
    assert m.classes.submerged / m.classes.crop == pytest.approx(0.5, abs=0.001)


def test_the_model_cannot_award_itself_the_specifications_top_weight(monkeypatch):
    # 0.85 and above is the branch that gives the ground signal 0.65 of a
    # compensation figure under cloud. The specification wrote it for a
    # segmentation model validated against ground truth; this instrument has
    # been validated against nothing, so its own confidence is capped below it.
    _frames(monkeypatch, 3)
    payload = json.loads(json.dumps(MEASURED))
    payload["measurement"]["quality"] = 0.97
    _answer(monkeypatch, payload)

    m = agent.measure_with_agent("/tmp/clip")

    assert m is not None
    assert m.quality == pytest.approx(agent.MODEL_QUALITY_CAP)
    assert m.quality < 0.85
    assert any("capped" in n for n in m.quality_notes)
