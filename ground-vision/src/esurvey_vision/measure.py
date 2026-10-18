"""Measuring crop damage in a survey video.

# What this is

A classical computer-vision baseline for the three ground signals the
specification asks for: the stem lodging angle, the submerged fraction and the
necrotic fraction. It decodes keyframes, separates crop from everything else in
HSV, splits the crop into healthy, submerged and necrotic by colour, and
estimates the dominant stem orientation from the image structure tensor.

# What this is not, and why that is stated so loudly

The specification asks for SegFormer-B5 semantic segmentation and YOLOv10-X
stem detection over a Structure-from-Motion point cloud. This is none of those.
It is colour thresholds and a gradient orientation, which is a different and
much weaker instrument: it cannot tell a submerged panicle from a shadowed one,
it reads silt and dry soil alike, and its lodging angle is the orientation of
whatever texture dominates the frame rather than of stems it has identified.

That matters more here than it usually would, because the specification gives
this signal half the weight of a compensation decision in clear weather and
nearly two thirds under cloud. An unvalidated estimate carrying that much
weight is worse than no estimate at all, because the absence is visible and the
bad number is not.

So every result is labelled `baseline: true`, exactly as the radar service
labels a generated series SYNTHETIC, and the platform refuses to use it outside
development. It exists so the whole path can be built, tested and argued with
before a trained model arrives to replace it -- at which point this module is
deleted and nothing else changes, because the contract is the boundary.
"""

from __future__ import annotations

import math
from dataclasses import dataclass, field

import cv2
import numpy as np

# Sampling. A survey walk is a minute or two; scoring every frame would spend
# most of the time re-measuring the same plants from a slightly different angle.
MAX_KEYFRAMES = 24
MIN_KEYFRAMES = 3

# Below this the frame is too dark to say anything about colour, and colour is
# what both loss ratios are made of.
MIN_MEAN_V = 35

# A frame this blurred has no usable gradient orientation, so the lodging angle
# would be the orientation of the blur.
MIN_FOCUS = 40.0


@dataclass
class Classes:
    """Pixel counts, matching the specification's four semantic classes."""

    healthy_foliage: int = 0  # C1
    submerged: int = 0  # C2
    necrotic: int = 0  # C3
    background: int = 0  # C4

    def add(self, other: "Classes") -> None:
        self.healthy_foliage += other.healthy_foliage
        self.submerged += other.submerged
        self.necrotic += other.necrotic
        self.background += other.background

    @property
    def crop(self) -> int:
        return self.healthy_foliage + self.submerged + self.necrotic


@dataclass
class Measurements:
    """The contract the platform scores. Mirrors groundcv.Measurements in Go."""

    stem_angle_degrees: float
    classes: Classes
    quality: float
    keyframes: int
    canopy_height_ratio: float | None = None

    # Always true from this module. The platform reads it and refuses the
    # measurement outside development.
    baseline: bool = True

    # Why the quality score came out where it did, so a low one can be argued
    # with rather than merely disbelieved.
    quality_notes: list[str] = field(default_factory=list)

    # Whether the field holds the crop the farmer declared.
    #
    # None everywhere from the classical baseline, which cannot tell one crop
    # from another and must not appear to. The model path fills them, and a
    # mismatch becomes a fraud indicator on the assessment -- which is why
    # None and False are kept apart here: "could not tell" is not "it is
    # something else".
    crop_declared: str | None = None
    crop_seen: str | None = None
    crop_matches: bool | None = None
    crop_mismatch_confidence: float = 0.0
    crop_mismatch_detail: str | None = None


def keyframes(video_path: str, want: int = MAX_KEYFRAMES) -> list[np.ndarray]:
    """Evenly spaced frames across the clip.

    Evenly spaced rather than the first N: a farmer starts recording while
    still walking to the field, so the opening seconds are frequently a path,
    a bund or their own feet.
    """
    cap = cv2.VideoCapture(video_path)
    if not cap.isOpened():
        return []
    try:
        total = int(cap.get(cv2.CAP_PROP_FRAME_COUNT))
        if total <= 0:
            # Some containers do not report a frame count. Read what comes.
            frames = []
            while len(frames) < want:
                ok, frame = cap.read()
                if not ok:
                    break
                frames.append(frame)
            return frames

        step = max(1, total // want)
        frames = []
        for i in range(0, total, step):
            cap.set(cv2.CAP_PROP_POS_FRAMES, i)
            ok, frame = cap.read()
            if ok:
                frames.append(frame)
            if len(frames) >= want:
                break
        return frames
    finally:
        cap.release()


def _focus(gray: np.ndarray) -> float:
    """Variance of the Laplacian: the standard cheap sharpness measure."""
    return float(cv2.Laplacian(gray, cv2.CV_64F).var())


def classify(frame: np.ndarray) -> Classes:
    """Split one frame into the four semantic classes by colour.

    Hue separates living foliage from everything else far more stably than RGB
    does, because it is largely invariant to how bright the day is -- and a
    survey video is shot in whatever light the farmer had.

    The bands are deliberately conservative. Anything not clearly one of the
    three crop classes falls to background, so an uncertain pixel weakens the
    denominator rather than inventing damage.
    """
    hsv = cv2.cvtColor(frame, cv2.COLOR_BGR2HSV)
    h, s, v = hsv[..., 0], hsv[..., 1], hsv[..., 2]

    # OpenCV hue is 0-179. Green foliage sits around 35-85.
    saturated = s > 60
    lit = v > 40

    healthy = (h >= 35) & (h <= 85) & saturated & lit

    # Necrosis and blight: yellow through brown, still saturated. This is the
    # weakest of the three bands, because ripening cereal is the same colour as
    # dying cereal and nothing here can tell them apart.
    necrotic = (((h >= 10) & (h < 35)) | (h >= 170)) & saturated & lit

    # Submerged and silt-coated: dark and desaturated, or the blue-grey of
    # standing water over a canopy.
    water = ((h >= 90) & (h <= 130) & (s > 40)) | ((s <= 60) & (v < 110))
    submerged = water & lit

    # Mutually exclusive, in the specification's own order of precedence: a
    # pixel under water is submerged whatever colour the leaf beneath it is.
    necrotic = necrotic & ~submerged
    healthy = healthy & ~submerged & ~necrotic

    total = frame.shape[0] * frame.shape[1]
    c1 = int(np.count_nonzero(healthy))
    c2 = int(np.count_nonzero(submerged))
    c3 = int(np.count_nonzero(necrotic))
    return Classes(
        healthy_foliage=c1,
        submerged=c2,
        necrotic=c3,
        background=total - c1 - c2 - c3,
    )


def stem_angle(frame: np.ndarray) -> tuple[float, float]:
    """Dominant texture orientation from the structure tensor, in degrees.

    Returns the angle from vertical and a coherence in [0,1] saying how
    strongly the frame has one orientation at all.

    A standing cereal canopy is a field of near-vertical lines. Flatten it and
    the lines go horizontal. The structure tensor's principal eigenvector is
    the cheapest honest summary of that, and its eigenvalue ratio says whether
    there was a dominant direction to find -- a frame of shapeless mush returns
    a low coherence, which is what stops the angle being believed.
    """
    gray = cv2.cvtColor(frame, cv2.COLOR_BGR2GRAY)
    gray = cv2.GaussianBlur(gray, (5, 5), 0)

    gx = cv2.Sobel(gray, cv2.CV_64F, 1, 0, ksize=5)
    gy = cv2.Sobel(gray, cv2.CV_64F, 0, 1, ksize=5)

    jxx = float(np.mean(gx * gx))
    jyy = float(np.mean(gy * gy))
    jxy = float(np.mean(gx * gy))

    trace = jxx + jyy
    if trace <= 1e-9:
        return 0.0, 0.0

    # Eigen-decomposition of the 2x2 symmetric tensor, in closed form.
    diff = jxx - jyy
    root = math.sqrt(diff * diff + 4.0 * jxy * jxy)
    lam1 = 0.5 * (trace + root)
    lam2 = 0.5 * (trace - root)

    coherence = 0.0 if lam1 <= 1e-9 else float((lam1 - lam2) / (lam1 + lam2))

    # The dominant gradient direction is perpendicular to the dominant
    # structure, so the stems run along the minor eigenvector.
    gradient_angle = 0.5 * math.atan2(2.0 * jxy, diff)
    structure_angle = gradient_angle + math.pi / 2.0

    # Fold into an angle from vertical in [0, 90].
    #
    # The structure orientation is measured from the x-axis, so upright stems
    # come out at 90 degrees and a flattened canopy at 0. The angle the
    # specification wants is the complement of that, and taking it the other
    # way round reports a standing crop as prostrate -- which is the most
    # expensive single error this module could make, since lodging carries the
    # heaviest of the three ground weights.
    deg = math.degrees(structure_angle) % 180.0
    from_vertical = abs(90.0 - deg)
    return min(90.0, max(0.0, from_vertical)), coherence


def measure(video_path: str) -> Measurements | None:
    """Measure one survey clip. None when nothing usable could be read."""
    frames = keyframes(video_path)
    if len(frames) < MIN_KEYFRAMES:
        return None

    classes = Classes()
    angles: list[float] = []
    coherences: list[float] = []
    focuses: list[float] = []
    brightness: list[float] = []
    used = 0
    notes: list[str] = []

    for frame in frames:
        gray = cv2.cvtColor(frame, cv2.COLOR_BGR2GRAY)
        mean_v = float(np.mean(gray))
        focus = _focus(gray)

        brightness.append(mean_v)
        focuses.append(focus)

        if mean_v < MIN_MEAN_V or focus < MIN_FOCUS:
            # Too dark or too blurred to contribute colour or orientation.
            continue

        classes.add(classify(frame))
        angle, coherence = stem_angle(frame)
        angles.append(angle)
        coherences.append(coherence)
        used += 1

    if used < MIN_KEYFRAMES or classes.crop < 100:
        return None

    if used < len(frames):
        notes.append(
            f"{len(frames) - used} of {len(frames)} keyframes were too dark or "
            "too blurred to score"
        )

    # The median rather than the mean: one frame of the sky should not drag the
    # lodging angle of a whole walk.
    angle = float(np.median(angles))
    coherence = float(np.median(coherences))

    quality, quality_notes = _quality(
        used=used,
        coherence=coherence,
        crop_share=classes.crop / max(1, classes.crop + classes.background),
        focus=float(np.median(focuses)),
        brightness=float(np.median(brightness)),
    )
    notes.extend(quality_notes)

    return Measurements(
        stem_angle_degrees=angle,
        classes=classes,
        quality=quality,
        keyframes=used,
        quality_notes=notes,
    )


def _quality(
    *,
    used: int,
    coherence: float,
    crop_share: float,
    focus: float,
    brightness: float,
) -> tuple[float, list[str]]:
    """The specification's q_cv: how far this measurement should be trusted.

    Four things move it, and each is a way the footage can fail to answer the
    question rather than a way the crop can be damaged. Keeping them out of the
    score itself is deliberate: a blurry video of a flattened field must read
    as less certain, never as less damaged.
    """
    notes: list[str] = []

    # Enough frames to be a walk rather than a glance.
    frame_term = min(1.0, used / 12.0)
    if used < 8:
        notes.append(f"only {used} usable keyframes")

    # Did the frames have a dominant orientation at all?
    coherence_term = min(1.0, coherence / 0.35)
    if coherence < 0.15:
        notes.append("no dominant stem orientation; the lodging angle is weak")

    # Was the camera pointed at the crop?
    crop_term = min(1.0, crop_share / 0.35)
    if crop_share < 0.2:
        notes.append(f"only {crop_share:.0%} of the frame was crop")

    # Sharp enough to segment.
    focus_term = min(1.0, focus / 150.0)
    if focus < 80:
        notes.append("the footage is soft")

    if brightness < 60:
        notes.append("the footage is dark")

    q = frame_term * 0.25 + coherence_term * 0.30 + crop_term * 0.30 + focus_term * 0.15

    # A classical baseline must never present as a confident instrument. The
    # ceiling keeps it below the specification's 0.85 "good video" branch, so
    # it can never claim the 0.50/0.65 weight a trained model would earn.
    ceiling = 0.55
    if q > ceiling:
        notes.append(
            "capped: this is an unvalidated classical baseline, not the "
            "specification's trained segmentation"
        )
    return min(ceiling, round(q, 4)), notes


def sharpest_frame(video_path: str, want: int = MAX_KEYFRAMES) -> np.ndarray | None:
    """The clearest of the evenly spaced frames, or None if the clip is unreadable.

    # Why the sharpest rather than the first, or the middle

    The report prints one frame per point and an officer reads the crop off it.
    A farmer panning a phone across a field produces motion blur on most frames
    and a usable image on a few, and the first frame is frequently their own
    feet or the path in. The variance of the Laplacian is the standard cheap
    measure of how much edge detail an image holds, and on a set of frames of
    the same scene it picks the one that is in focus.

    Deterministic for a given clip, which matters more here than being clever:
    the assessment report has to print the same page at an appeal next year as
    it printed today, and a frame chosen at random would break that.
    """
    frames = keyframes(video_path, want=want)
    if not frames:
        return None
    return max(
        frames,
        key=lambda f: float(
            cv2.Laplacian(cv2.cvtColor(f, cv2.COLOR_BGR2GRAY), cv2.CV_64F).var()
        ),
    )
