"""The panel: four specialists in parallel, then a synthesiser.

# Reliability, concretely

A multi-agent system is more parts and therefore more ways to fail. What makes
this one more reliable than the single agent it replaces is not the number of
agents — it is that every failure has a defined outcome:

- A specialist that times out or errors returns a report saying so. The panel
  continues with three, and the synthesiser is told which one is missing.
- A specialist whose evidence is absent is not run at all, and that is recorded
  as a reason rather than a silent skip. Running the vision specialist with no
  photographs would produce a confident opinion about nothing.
- If the synthesiser itself fails, the panel falls back to a deterministic
  assessment assembled from the specialist findings without a model. It refuses
  a damage figure and says why, which is the correct answer when the component
  that was meant to weigh the evidence is not available.
- If no credential is configured at all, the same deterministic path runs. The
  service degrades to "we could not assess this, here is what we had" instead
  of returning nothing.

That last property is what lets an assessment be attempted the moment an
offline-filed report syncs, rather than only when every upstream is healthy.
"""

from __future__ import annotations

import asyncio
import logging
import time

from ..failures import scrub
from ..schemas import (
    AssessRequest,
    CropPresence,
    FraudIndicator,
    FraudSeverity,
    RawAssessment,
)
from .base import PanelUnavailableError
from .findings import SatelliteFinding, SpecialistReport
from .specialists import (
    PANEL_VERSION,
    phenology_prompt,
    phenology_specialist,
    satellite_prompt,
    satellite_specialist,
    vision_prompt,
    vision_specialist,
    weather_prompt,
    weather_specialist,
)
from .synthesis import SYNTHESIS_VERSION, synthesis_prompt, synthesiser

log = logging.getLogger(__name__)

PROMPT_VERSION = f"{PANEL_VERSION}+{SYNTHESIS_VERSION}"


class PanelResult:
    """One panel run: what each specialist found, and what was made of it."""

    def __init__(
        self,
        assessment: RawAssessment,
        reports: list[SpecialistReport],
        duration_ms: int,
        degraded: bool,
        degraded_reason: str | None = None,
    ) -> None:
        self.assessment = assessment
        self.reports = reports
        self.duration_ms = duration_ms
        self.degraded = degraded
        self.degraded_reason = degraded_reason


def _ground_images(req: AssessRequest) -> int:
    return sum(1 for i in req.images if i.kind in ("field_photo", "survey_keyframe"))


async def run_panel(req: AssessRequest) -> PanelResult:
    """Run the whole panel and return a raw assessment for the guardrails."""
    started = time.monotonic()

    # Which specialists have anything to work with. A specialist run on absent
    # evidence does not abstain; it produces a confident opinion about nothing,
    # which is worse than a recorded gap.
    tasks: list[tuple[str, asyncio.Future[SpecialistReport]]] = []
    skipped: list[SpecialistReport] = []

    has_position = (
        req.parcel.centroid_lat is not None and req.parcel.centroid_lon is not None
    )
    if has_position:
        tasks.append(
            ("weather", asyncio.ensure_future(
                weather_specialist.run(weather_prompt(req))))
        )
    else:
        skipped.append(SpecialistReport(
            agent="weather_specialist", ran=False,
            error="the parcel has no centroid, so no weather could be looked up",
        ))

    ground = [i for i in req.images if i.kind in ("field_photo", "survey_keyframe")]
    if ground:
        tasks.append(
            ("vision", asyncio.ensure_future(
                vision_specialist.run(vision_prompt(req), ground)))
        )
    elif req.ground_expected:
        skipped.append(SpecialistReport(
            agent="vision_specialist", ran=False,
            error="no ground photographs were supplied",
        ))
    else:
        # A screening from the console, not a claim. Saying "no photographs
        # were supplied" of an assessment nobody filmed reads as a failure
        # and drags the confidence down for a gap that is the request's own
        # shape.
        skipped.append(SpecialistReport(
            agent="vision_specialist", ran=False,
            error=(
                "this assessment was requested without field imagery: it is "
                "a satellite screening of the parcel, so no damage figure is "
                "expected from it"
            ),
        ))

    if req.satellite.time_series:
        tasks.append(
            ("satellite", asyncio.ensure_future(
                satellite_specialist.run(satellite_prompt(req))))
        )
    else:
        skipped.append(SpecialistReport(
            agent="satellite_specialist", ran=False,
            error="the vegetation series is empty",
        ))

    reports: list[SpecialistReport] = list(skipped)

    if tasks:
        try:
            done = await asyncio.gather(*(t for _, t in tasks))
            reports.extend(done)
        except PanelUnavailableError as exc:
            for _, t in tasks:
                t.cancel()
            # Our own message, but scrubbed anyway: it becomes part of the
            # stored reasoning, and a future change that interpolated an
            # SDK error into it would otherwise leak silently.
            return _deterministic(req, reports, started, scrub(str(exc)) or "")

    # Round two. Growth stage and what the calamity would have done to the
    # crop are questions about a crop; asking them where none was established
    # produces a confident opinion about nothing, and it lands in the record
    # as a specialist supporting the claim.
    presence = crop_presence_from(reports)
    if presence == "absent":
        reports.append(SpecialistReport(
            agent="phenology_specialist", ran=False,
            error=(
                "no crop was found on this parcel, so there is no growth "
                "stage to place: the ground photographs and the vegetation "
                "record are what settle this, and both were asked first"
            ),
        ))
    elif presence == "contested":
        reports.append(SpecialistReport(
            agent="phenology_specialist", ran=False,
            error=(
                "the photographs and the vegetation record disagree about "
                "whether a crop is here, so there is no established crop to "
                "place a growth stage on: that disagreement is the finding"
            ),
        ))
    elif presence == "inconclusive":
        reports.append(SpecialistReport(
            agent="phenology_specialist", ran=False,
            error=(
                "no evidence settled whether a crop is here, and a growth "
                "stage derived from a declared sowing date alone is an "
                "assumption rather than an observation"
            ),
        ))
    elif not (req.crop or req.sowing_date):
        reports.append(SpecialistReport(
            agent="phenology_specialist", ran=False,
            error=(
                "no crop or sowing date was declared, so no growth stage "
                "could be derived"
            ),
        ))
    else:
        try:
            phenology = await phenology_specialist.run(phenology_prompt(req))
            # An inconclusive first round is not a licence to reason as
            # though the crop were established; the synthesiser is told what
            # round one found and weighs this accordingly.
            reports.append(phenology)
        except PanelUnavailableError as exc:
            reports.append(SpecialistReport(
                agent="phenology_specialist", ran=False,
                error=scrub(str(exc)) or "the model was unavailable",
            ))

    log.info(
        "%s panel: %s",
        req.assessment_id,
        ", ".join(
            f"{r.agent}={'ok' if r.ran else 'skipped'}({r.duration_ms}ms)"
            for r in reports
        ),
    )

    # If nothing ran there is nothing to synthesise, and asking a model to
    # reconcile four gaps would produce fluent emptiness.
    if not any(r.ran for r in reports):
        return _deterministic(
            req, reports, started, "no specialist had evidence to work with"
        )

    synthesis = await synthesiser.run(synthesis_prompt(req, reports))
    if not synthesis.ran or synthesis.finding is None:
        return _deterministic(
            req, reports, started,
            f"the synthesiser did not complete: {scrub(synthesis.error)}",
        )

    model = synthesis.finding
    raw = RawAssessment.model_validate(
        {
            **model.model_dump(mode="json"),
            "model": _model_name(),
            "prompt_version": PROMPT_VERSION,
            "guardrails_applied": [],
        }
    )

    # "Degraded" with no reason tells an officer that something was missing
    # and not what. Name the members that did not report.
    gaps = [f"{r.agent} ({scrub(r.error)})" for r in reports if not r.ran]

    return PanelResult(
        assessment=raw,
        reports=reports,
        duration_ms=int((time.monotonic() - started) * 1000),
        degraded=bool(gaps),
        degraded_reason=("did not report: " + "; ".join(gaps)) if gaps else None,
    )


def _model_name() -> str:
    from ..config import settings

    return settings.model


def _deterministic(
    req: AssessRequest,
    reports: list[SpecialistReport],
    started: float,
    reason: str,
) -> PanelResult:
    """An assessment assembled without a model.

    The floor the whole service falls back to. It never states a damage figure,
    because nothing here has looked at a photograph — but it is not empty: the
    one thing arithmetic can settle without a model is whether anything grew on
    this parcel, and that is also the strongest fraud signal the system has.

    This is what makes an assessment possible the moment an offline-filed
    report arrives, rather than only when every upstream is healthy.
    """
    presence = CropPresence.INCONCLUSIVE
    presence_reason = "No specialist finding was available to judge crop presence."
    fraud: list[FraudIndicator] = []
    cited: list[str] = []

    # Prefer a satellite specialist's finding; fall back to the series itself.
    sat_report = next(
        (r for r in reports
         if r.agent == "satellite_specialist" and r.ran
         and isinstance(r.finding, SatelliteFinding)),
        None,
    )
    series = req.satellite.time_series

    if sat_report is not None and sat_report.finding is not None:
        f = sat_report.finding
        assert isinstance(f, SatelliteFinding)
        if f.crop_present is True:
            presence = CropPresence.CONFIRMED
        elif f.crop_present is False:
            presence = CropPresence.ABSENT
        presence_reason = f.reasoning
        cited.append("satellite specialist")
    elif len(series) >= 2:
        peak = max(p.ndvi for p in series)
        if peak >= 0.30:
            presence = CropPresence.CONFIRMED
            presence_reason = (
                f"Assessed without a model: the vegetation series peaks at "
                f"{peak:.2f}, well above bare soil, so a crop grew here."
            )
        else:
            presence = CropPresence.ABSENT
            presence_reason = (
                f"Assessed without a model: the vegetation series never rises "
                f"above {peak:.2f}, at or near bare soil, across "
                f"{len(series)} observations."
            )
            fraud.append(
                FraudIndicator(
                    code="no_vegetation_detected",
                    severity=FraudSeverity.CRITICAL,
                    detail=(
                        f"A crop was declared on this parcel but the vegetation "
                        f"series peaks at {peak:.2f}, at or near bare soil."
                    ),
                )
            )
        cited.append("Sentinel-2 NDVI series")

    gaps = [f"{r.agent}: {scrub(r.error)}" for r in reports if not r.ran and r.error]
    gap_text = (
        " Not examined: " + "; ".join(gaps) + "."
        if gaps
        else " No specialist reported a gap."
    )

    raw = RawAssessment.model_validate(
        {
            "crop_presence": presence,
            "crop_presence_reasoning": presence_reason,
            "fraud_indicators": [f.model_dump(mode="json") for f in fraud],
            "insufficient_evidence": True,
            "confidence": 0.0,
            "reasoning": (
                "This assessment was produced without the reasoning panel: "
                f"{reason}. No damage figure is stated, because nothing here "
                "examined the crop. What could be settled arithmetically has "
                "been, and every gap is listed." + gap_text
            ),
            "damage_percent": None,
            "severity": None,
            "evidence_cited": cited,
            "contradictions": [],
            "model": "deterministic-fallback",
            "prompt_version": PROMPT_VERSION,
            "guardrails_applied": ["panel_unavailable_deterministic_fallback"],
        }
    )

    return PanelResult(
        assessment=raw,
        reports=reports,
        duration_ms=int((time.monotonic() - started) * 1000),
        degraded=True,
        degraded_reason=reason,
    )


def crop_presence_from(reports: list[SpecialistReport]) -> str:
    """What round one settled about a crop being here.

    Returns "present", "absent", "contested" or "inconclusive".

    # Two instruments, one question

    The vegetation curve is what confirms a crop where there are no
    photographs -- a screening from the console has nothing else, and a season
    that rises from bare soil and falls again is real evidence that something
    grew. Where photographs exist as well, both are asked and both must agree.

    Vision used to decide alone whenever it had anything to say, and the
    satellite was never consulted. That reads well on the case it was written
    for -- indoor photographs against a declared soybean crop -- and badly on
    its mirror image: a farmer who sends a photograph of the wrong field does
    not thereby prove their own land is bare, and the curve may well show a
    crop that is really there.

    So a disagreement is "contested" and not a verdict. Nothing downstream may
    treat the crop as established, and the synthesiser is told to report the
    conflict rather than resolve it -- which is the same rule this panel
    follows everywhere else: a contradiction surfaced to an officer is useful,
    and one settled by guesswork is not.
    """
    def ground() -> str | None:
        r = next(
            (r for r in reports if r.agent == "vision_specialist" and r.ran), None
        )
        if r is None or r.finding is None:
            return None
        matches = getattr(r.finding, "crop_matches_declaration", None)
        identified = getattr(r.finding, "crop_identified", "")
        if matches is False:
            return "absent"
        if matches is True or identified:
            return "present"
        return None

    def curve() -> str | None:
        r = next(
            (r for r in reports if r.agent == "satellite_specialist" and r.ran), None
        )
        if r is None or r.finding is None:
            return None
        present = getattr(r.finding, "crop_present", None)
        if present is True:
            return "present"
        if present is False:
            return "absent"
        return None

    seen, grown = ground(), curve()
    if seen is not None and grown is not None:
        return seen if seen == grown else "contested"
    return seen or grown or "inconclusive"
