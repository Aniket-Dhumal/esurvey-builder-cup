"""What each specialist returns.

# Why every finding carries its own limitations

A specialist that cannot say what it could not see is a specialist whose
confidence is uninterpretable. The synthesiser's hardest job is deciding what
to do when two of them disagree, and that is only tractable if each one has
already said how far its own evidence actually reaches.

# Why none of them returns a damage percentage except vision

Only ground imagery can measure how badly a crop was hurt. Weather says whether
the claimed calamity happened in the area; phenology says what damage at this
growth stage would look like and whether the crop could recover; satellite says
whether a crop grew at all and whether the curve matches what was declared.
None of those is a measurement of loss, and a schema that let them return one
would invite exactly the confident-but-baseless figure this system exists to
prevent.
"""

from __future__ import annotations

from enum import StrEnum

from pydantic import BaseModel, Field

from ..schemas import GroundMeasurement, ModelAssessment


class Verdict(StrEnum):
    """How a specialist's evidence bears on the claim."""

    SUPPORTS = "supports"
    CONTRADICTS = "contradicts"
    INCONCLUSIVE = "inconclusive"
    NOT_ASSESSED = "not_assessed"


class SpecialistBase(BaseModel):
    """Fields every specialist must fill in."""

    verdict: Verdict = Field(
        description=(
            "Whether your evidence supports the reported loss, contradicts it, "
            "or cannot settle it. Use not_assessed only if you were given "
            "nothing to work with."
        )
    )
    confidence: float = Field(
        ge=0,
        le=1,
        description=(
            "How far your own evidence takes you. Low confidence with a clear "
            "reason is more useful than a confident guess."
        ),
    )
    reasoning: str = Field(
        description="What you concluded and why, in two or three sentences."
    )
    limitations: list[str] = Field(
        default_factory=list,
        description=(
            "What your evidence could not tell you. State these plainly; the "
            "synthesiser weighs your finding by them."
        ),
    )


class WeatherFinding(SpecialistBase):
    """Did the region experience what the farmer says happened."""

    calamity_consistent: bool | None = Field(
        default=None,
        description=(
            "Whether the reported calamity is consistent with the weather in "
            "the fourteen days up to the incident. Null if the source was "
            "unavailable."
        ),
    )
    total_rain_mm: float | None = None
    max_daily_rain_mm: float | None = None
    max_wind_gust_kmh: float | None = None
    hail_days: list[str] = Field(
        default_factory=list,
        description="Dates in the window on which the record shows hail.",
    )
    antecedent_conditions: str = Field(
        default="",
        description=(
            "What the run-up looked like — a week of saturation before a storm "
            "is often why the storm lodged the crop."
        ),
    )


class PhenologyFinding(SpecialistBase):
    """What the crop's growth stage means for this loss."""

    days_after_sowing: int | None = None
    growth_stage: str = Field(
        default="",
        description=(
            "The stage the crop would have been at on the incident date, "
            "e.g. vegetative, flowering, pod fill, maturity."
        ),
    )
    stage_plausible: bool | None = Field(
        default=None,
        description=(
            "Whether the declared sowing date and the incident date are "
            "consistent with a crop being in the ground at all."
        ),
    )
    plausible_damage_modes: list[str] = Field(
        default_factory=list,
        description=(
            "What this calamity does to this crop at this stage — lodging, "
            "shattering, defoliation, submersion, sterility."
        ),
    )
    recoverability: str = Field(
        default="",
        description=(
            "Whether the crop could recover from damage at this stage. Hail at "
            "vegetative stage often regrows; at pod fill it does not."
        ),
    )


class VisionFinding(SpecialistBase):
    """What the ground photographs actually show.

    The only specialist that may state a damage percentage, because it is the
    only one looking at the crop.
    """

    usable_images: int = 0
    crop_identified: str = Field(
        default="",
        description="What crop the photographs show, or empty if unclear.",
    )
    crop_matches_declaration: bool | None = None
    damage_modes_observed: list[str] = Field(
        default_factory=list,
        description="What the damage looks like in the images.",
    )
    damage_percent: float | None = Field(
        default=None,
        ge=0,
        le=100,
        description=(
            "Proportion of the crop in view that is lost. Null unless the "
            "images genuinely show enough of the field to support a figure. A "
            "photograph of one plant does not."
        ),
    )
    image_quality: str = Field(
        default="",
        description=(
            "Whether the photographs are usable: framing, focus, light, and "
            "whether they show the field or something else."
        ),
    )


class SatelliteFinding(SpecialistBase):
    """What the vegetation record says about this parcel."""

    crop_present: bool | None = Field(
        default=None,
        description=(
            "Whether a crop demonstrably grew here. This is what a 10 m "
            "optical series can settle; damage is not."
        ),
    )
    curve_matches_declaration: bool | None = Field(
        default=None,
        description=(
            "Whether the shape and timing of the NDVI curve match the declared "
            "crop and sowing date."
        ),
    )
    drop_at_incident: bool | None = Field(
        default=None,
        description=(
            "Whether the series shows a fall consistent with the incident. "
            "Null when cloud left the window blind around that date."
        ),
    )
    observation_quality: str = Field(
        default="",
        description="How much the cloud gaps limit what the series can show.",
    )


class SpecialistReport(BaseModel):
    """One specialist's run, whether or not it produced a finding."""

    agent: str
    ran: bool
    duration_ms: int = 0
    error: str | None = None
    # The synthesiser is run through the same Specialist machinery, so its
    # ModelAssessment has to be admissible here too. Leaving it out made every
    # live run fall back to the deterministic path with a validation error --
    # a panel that always degraded, and said so only in a log line.
    finding: (
        WeatherFinding
        | PhenologyFinding
        | VisionFinding
        | SatelliteFinding
        | ModelAssessment
        | GroundMeasurement
        | None
    ) = None
