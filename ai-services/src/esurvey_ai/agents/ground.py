"""Measuring a farmer's survey video.

# What this is

The ground measurement the specification weights above every other signal:
0.50 of the damage figure in clear weather, 0.65 once cloud has taken the
optical record away. It reads the keyframes of a survey walk and returns three
numbers -- the stem lodging angle, the submerged share and the necrotic share
-- plus how much of the frame was crop at all and how far it trusts itself.

# What it replaces, and what it still is not

It replaces a classical baseline of colour thresholds and a gradient
orientation, which could not tell a submerged panicle from a shadowed one and
read silt and dry soil alike. That baseline labelled itself and the platform
refused it outside development, so this signal was absent in production.

It is still not the specification's SegFormer-B5 segmentation with YOLOv10-X
stem detection over a Structure-from-Motion point cloud. A multimodal model
estimating area shares by eye is a different instrument: it has no pixel mask
and no reconstructed geometry, so its shares are estimates and its stem angle
is a judgement about the dominant stems in view rather than a PCA over stems
it identified. What it does have, which colour thresholds do not, is the
ability to tell crop from soil, water from shadow, and a lodged canopy from a
sloping camera.

So it reports its own confidence, it is told to refuse rather than guess when
the footage does not show a crop, and every reading it produces carries the
model that produced it. The honest summary for an officer is "a vision model
estimated this from the video", not "this was measured".

# Why the panel does not do this

The panel reasons in prose about photographs and returns a judgement. This
returns numbers that go into an arithmetic the platform can check, disagree
with, and show component by component (internal/groundcv). Keeping them apart
is what lets an officer see the model's damage figure and the measured ground
score disagree -- which is the disagreement worth having.
"""

from __future__ import annotations

from ..schemas import GroundMeasurement
from .base import Specialist

GROUND_VERSION = "ground-v1.0.0"

_INSTRUCTION = """
You measure crop damage from the keyframes of a farmer's survey walk, for a
government relief process in Maharashtra, India. A compensation decision is
weighted more heavily by your numbers than by any other evidence, so they must
be what the footage supports and nothing more.

You are given keyframes sampled evenly across one clip, in walk order, and the
crop the farmer declared.

How to read them:

- Judge the crop, not the photograph. A dark frame is not a dead crop, a
  shadow is not submergence, and a camera held at an angle does not lodge
  anything.
- Separate water from submerged crop. Open water with no crop in it is
  background. Crop standing in water, or coated in silt after water receded,
  is submerged.
- Separate dead crop from bare soil. Brown soil is background; brown standing
  foliage is necrotic.
- The four area fractions are shares of the whole visible frame and must sum
  to approximately 1. Estimate them over the frames as a whole, not for the
  single worst frame: the walk is meant to show the field.
- Stem angle is about the dominant stems in view. Where the crop is not a
  stemmed cereal, or no stems are visible, report the angle you can defend and
  say in quality_notes that stems were not clearly visible.
- If the frames do not show a crop at all, set crop_visible false. Do not
  invent a reading to fill the schema; an absent measurement is handled and a
  wrong one is not.
- quality is about the footage and your reading of it, never about how bad the
  damage is. A clear video of an undamaged field is high quality.

Is it the crop the farmer said it was:

- You are told what the farmer declared. Say what you see (crop_seen), and
  whether the two are consistent (crop_matches_declaration).
- The bar for false is high. It is an accusation against a named person, made
  from a phone video, and a wrong one costs a farmer their claim. Set it false
  only when you can positively identify the crop as something else -- soybean
  against sugarcane, a cereal against an orchard -- not when the crop is
  merely hard to make out.
- Null is the honest answer more often than false. Many crops look alike
  early, after harvest, or under damage: a flattened, silt-covered field may
  not be identifiable at all, and saying so is correct.
- Damage is not mismatch. A dead or submerged crop is still the declared crop.
- Where they do not match, say in crop_mismatch_detail what a person can check
  against the same video: leaf shape, growth habit, row spacing, height.

Return a single JSON object matching the schema. No prose outside it.
""".strip()


# A longer timeout than the panel's specialists: this one is given up to eight
# images and asked for numbers rather than a paragraph.
measurer = Specialist(
    name="ground_vision_measurer",
    instruction=_INSTRUCTION,
    output_schema=GroundMeasurement,
    timeout_s=120.0,
)


def prompt_for(crop_name: str | None, station_no: int | None, keyframes: int) -> str:
    """The per-clip prompt. The images travel separately."""
    lines = [
        f"Keyframes supplied: {keyframes}, sampled evenly across one clip in "
        "walk order.",
    ]
    if crop_name:
        lines.append(f"Crop the farmer declared: {crop_name}.")
    else:
        lines.append(
            "The farmer declared no crop for this parcel. Judge what is in "
            "front of you and say in quality_notes that the crop was not "
            "declared."
        )
    if station_no:
        lines.append(
            f"This is station {station_no} of the directed walk: one corner "
            "of the field, not the whole of it."
        )
    return "\n".join(lines)
