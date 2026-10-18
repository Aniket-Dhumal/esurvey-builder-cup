"""What the baseline can and cannot see.

The footage here is generated, so the answer is known before the measurement
runs. That is the only way to test a vision module without a labelled field
dataset, and it is honest about what it proves: it shows the module responds in
the right direction to the thing being varied, not that it is accurate on real
video of a real field. Accuracy against ground truth is what the trained model
this replaces will have to demonstrate.
"""

from __future__ import annotations

import cv2
import numpy as np
import pytest

from esurvey_vision.measure import classify, measure, stem_angle

GREEN = (60, 180, 60)  # BGR, a healthy canopy
BROWN = (40, 110, 160)  # dried and necrotic
WATER = (120, 95, 70)  # dark blue-grey standing water


def canopy(
    *,
    angle_deg: float = 0.0,
    colour: tuple[int, int, int] = GREEN,
    size: tuple[int, int] = (480, 640),
    spacing: int = 14,
) -> np.ndarray:
    """A field of parallel stems at a given angle from vertical."""
    h, w = size
    img = np.full((h, w, 3), (35, 45, 30), dtype=np.uint8)  # soil

    theta = np.deg2rad(angle_deg)
    dx, dy = np.sin(theta), np.cos(theta)
    length = int(np.hypot(h, w))

    for x0 in range(-length, w + length, spacing):
        p0 = (int(x0), h)
        p1 = (int(x0 + dx * length), int(h - dy * length))
        cv2.line(img, p0, p1, colour, 3)
    return img


def write_clip(path, frames: list[np.ndarray], fps: int = 10) -> str:
    h, w = frames[0].shape[:2]
    out = str(path)
    writer = cv2.VideoWriter(out, cv2.VideoWriter_fourcc(*"mp4v"), fps, (w, h))
    assert writer.isOpened(), "no mp4v encoder available in this environment"
    for f in frames:
        writer.write(f)
    writer.release()
    return out


# --- the lodging angle ------------------------------------------------------


@pytest.mark.parametrize("truth", [0, 20, 45, 70, 85])
def test_the_lodging_angle_tracks_the_canopy(truth: int) -> None:
    """An erect canopy reads near zero and a flat one reads near ninety."""
    angle, coherence = stem_angle(canopy(angle_deg=truth))

    assert coherence > 0.2, "parallel lines should have a dominant orientation"
    assert abs(angle - truth) < 15, f"read {angle:.1f}deg for a {truth}deg canopy"


def test_the_angle_is_monotonic_in_the_truth() -> None:
    """Flatter footage must not read as more upright."""
    read = [stem_angle(canopy(angle_deg=t))[0] for t in (0, 15, 30, 45, 60, 75)]
    for earlier, later in zip(read, read[1:]):
        assert later >= earlier - 8, f"angle went backwards: {read}"


def test_shapeless_footage_has_no_dominant_orientation() -> None:
    """Noise must report low coherence, which is what stops it being believed."""
    rng = np.random.default_rng(20260901)
    noise = rng.integers(0, 255, (480, 640, 3), dtype=np.uint8)

    _, coherence = stem_angle(noise)
    assert coherence < 0.2, "noise reported a dominant stem direction"


# --- the loss ratios --------------------------------------------------------


def test_a_healthy_canopy_is_mostly_healthy_pixels() -> None:
    c = classify(canopy(colour=GREEN))
    assert c.crop > 0
    assert c.healthy_foliage > c.submerged + c.necrotic


def test_a_flooded_canopy_reads_as_submerged() -> None:
    c = classify(canopy(colour=WATER, spacing=6))
    assert c.submerged > c.healthy_foliage, "standing water did not read as submerged"


def test_a_dried_canopy_reads_as_necrotic() -> None:
    c = classify(canopy(colour=BROWN, spacing=6))
    assert c.necrotic > c.healthy_foliage, "dried foliage did not read as necrotic"


def test_the_classes_are_mutually_exclusive_and_exhaustive() -> None:
    """Every pixel lands in exactly one class, or the ratios are meaningless."""
    frame = canopy(colour=GREEN)
    c = classify(frame)
    assert c.crop + c.background == frame.shape[0] * frame.shape[1]


def test_a_pixel_under_water_is_submerged_not_healthy() -> None:
    """The precedence the specification implies: water wins over leaf colour."""
    frame = canopy(colour=GREEN)
    flooded = classify(canopy(colour=WATER, spacing=6))
    dry = classify(frame)
    assert flooded.submerged > dry.submerged


# --- the whole clip ---------------------------------------------------------


def test_measuring_a_clip_of_a_flattened_field(tmp_path) -> None:
    path = write_clip(tmp_path / "flat.mp4", [canopy(angle_deg=80)] * 30)
    m = measure(path)

    assert m is not None, "a clear clip of a flat canopy could not be measured"
    assert m.keyframes >= 3
    assert m.stem_angle_degrees > 55, f"read {m.stem_angle_degrees:.1f}deg for a flat field"
    assert m.classes.crop > 100


def test_measuring_a_clip_of_a_standing_field(tmp_path) -> None:
    path = write_clip(tmp_path / "erect.mp4", [canopy(angle_deg=2)] * 30)
    m = measure(path)

    assert m is not None
    assert m.stem_angle_degrees < 25, f"read {m.stem_angle_degrees:.1f}deg for a standing field"


def test_a_clip_of_the_sky_is_refused(tmp_path) -> None:
    """No crop pixels means no measurement, rather than a measurement of zero."""
    sky = np.full((480, 640, 3), (230, 200, 160), dtype=np.uint8)
    path = write_clip(tmp_path / "sky.mp4", [sky] * 30)

    assert measure(path) is None


def test_a_dark_clip_is_refused(tmp_path) -> None:
    """Colour is what both ratios are made of, and darkness has none."""
    night = np.full((480, 640, 3), (8, 8, 8), dtype=np.uint8)
    path = write_clip(tmp_path / "night.mp4", [night] * 30)

    assert measure(path) is None


def test_too_short_a_clip_is_refused(tmp_path) -> None:
    path = write_clip(tmp_path / "blink.mp4", [canopy()] * 2)
    assert measure(path) is None


def test_an_unreadable_file_is_refused(tmp_path) -> None:
    junk = tmp_path / "notavideo.mp4"
    junk.write_bytes(b"this is not a video")
    assert measure(str(junk)) is None


# --- the safety property ----------------------------------------------------


def test_quality_can_never_reach_the_trained_model_branch(tmp_path) -> None:
    """The whole reason this baseline is allowed to exist.

    The specification hands the ground signal 0.50 of a compensation decision
    at q_cv >= 0.85, and 0.65 under cloud. This module is colour thresholds and
    a gradient; it must never be able to claim that weight. The cap holds it
    below 0.60, which puts it in the specification's own "poor video" branch at
    0.20 -- the lowest weight the fusion can give a ground score.
    """
    # The most favourable footage this module will ever see: sharp, bright,
    # perfectly parallel, filling the frame.
    path = write_clip(tmp_path / "ideal.mp4", [canopy(angle_deg=45, spacing=8)] * 40)
    m = measure(path)

    assert m is not None
    assert m.quality < 0.60, (
        f"quality reached {m.quality}, which would let an unvalidated baseline "
        "take weight the specification reserves for a trained model"
    )
    assert m.baseline is True, "the measurement did not label itself a baseline"


def test_every_measurement_labels_itself(tmp_path) -> None:
    path = write_clip(tmp_path / "clip.mp4", [canopy(angle_deg=30)] * 20)
    m = measure(path)
    assert m is not None and m.baseline is True
