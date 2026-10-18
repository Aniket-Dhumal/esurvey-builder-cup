"""Deterministic checks around the model.

The single largest risk in this design is that Gemini produces a confident,
well-written assessment for almost any input, including bad ones. Fluency is
not accuracy, and a plausible paragraph is more dangerous than an obvious
error because nobody questions it.

So the model's judgement is bounded on both sides:

  pre-flight   refuse before spending a call when there is nothing to say
  post-flight  clamp, cross-check and downgrade what comes back

The hardest rule here: **a damage figure requires ground imagery.** Sentinel-2
at 10 m over a one-hectare smallholding can establish that a crop existed and
roughly when it grew. It cannot measure how badly that crop was damaged, and a
number derived from it alone would be a guess wearing a decimal point.

The Go backend enforces its own copy of these. That duplication is deliberate:
this service is one deployable and the backend must not trust it.
"""

from __future__ import annotations

from .schemas import (
    AssessRequest,
    AssessResult,
    CropPresence,
    FraudSeverity,
    RawAssessment,
    Severity,
)

# Bare soil NDVI for this agro-ecological zone, per the specification.
NDVI_BARE_SOIL = 0.12

# Above this, green biomass is unambiguously present.
NDVI_VEGETATION_PRESENT = 0.30

# Ceiling on any conclusion drawn without ground imagery.
CONFIDENCE_CEILING_NO_GROUND_TRUTH = 0.6

# A gap this long may hide the canopy peak, the damage event, or both.
MATERIAL_GAP_DAYS = 30


def has_ground_imagery(req: AssessRequest) -> bool:
    """Whether direct field evidence is present.

    This gates the damage figure entirely. Everything else is corroboration.
    """
    return any(img.kind in ("field_photo", "survey_keyframe") for img in req.images)


def preflight(req: AssessRequest, min_observations: int) -> AssessResult | None:
    """Refuse before calling the model, or return None to proceed.

    Refuses only when there is nothing at all to say. A sparse NDVI series is
    no longer a blocker: crop presence and fraud indicators remain valuable
    even when damage cannot be quantified, and are often the most useful thing
    an officer receives.
    """
    if not req.satellite.time_series and not has_ground_imagery(req):
        return _refusal(
            "No cloud-free Sentinel-2 observation fell within the window and "
            "no ground imagery was submitted. There is nothing to assess — "
            "neither whether a crop was grown, nor what condition it was in.",
            ["no_evidence_at_all"],
        )
    return None


def _refusal(reason: str, rules: list[str]) -> AssessResult:
    return AssessResult(
        damage_percent=None,
        severity=None,
        confidence=0.0,
        insufficient_evidence=True,
        reasoning=reason,
        crop_presence=CropPresence.INCONCLUSIVE,
        crop_presence_reasoning="",
        fraud_indicators=[],
        evidence_cited=[],
        contradictions=[],
        model="none",
        prompt_version="none",
        guardrails_applied=rules,
    )


def postflight(result: RawAssessment, req: AssessRequest) -> AssessResult:
    """Bound what came back and convert it to the validated contract."""
    applied = list(result.guardrails_applied)
    r = result.model_copy(deep=True)
    ground = has_ground_imagery(req)

    # A farmer is not accused of the district's own declaration.
    #
    # calamity_not_in_record says the weather record shows nothing like the
    # reported calamity. Against a farmer's claim that is a discrepancy worth
    # an officer's attention. Against a calamity the Collector declared for
    # this area it is an accusation about somebody else's statement, made on
    # an ~11 km reanalysis grid, and the named person it lands on is the
    # farmer.
    #
    # The synthesis prompt already says this. It is enforced here as well
    # because the rule is about a real person's record and depending on a
    # prompt for it is depending on the model having read carefully -- and
    # because this failed once in exactly that way: the backend sent
    # "declared_by: farmer" for a claim filed under a declared drought, so the
    # prompt rule was correct, applicable, and never reached.
    if (req.calamity_declared_by or "").strip() == "district":
        kept = [
            f for f in (r.fraud_indicators or [])
            if f.code != "calamity_not_in_record"
        ]
        if len(kept) != len(r.fraud_indicators or []):
            r.fraud_indicators = kept
            applied.append("no_fraud_indicator_against_a_district_declaration")

    # The hard rule. Satellite cannot measure damage, so a figure produced
    # without ground imagery is discarded however confident it sounded.
    if not ground and r.damage_percent is not None:
        r.damage_percent = None
        r.severity = None
        r.insufficient_evidence = True
        applied.append("damage_requires_ground_imagery")

    if r.insufficient_evidence:
        # A refusal must not smuggle a figure out with it.
        if r.damage_percent is not None or r.severity is not None:
            r.damage_percent = None
            r.severity = None
            applied.append("cleared_figure_on_refusal")
        # Confidence still describes the crop-presence finding, so it is not
        # forced to zero — but an inconclusive presence usually means no
        # confidence in anything.
        #
        # Usually, and not always. An assessment can be certain of something
        # damning while remaining unable to settle whether a crop is there:
        # photographs of a bedroom submitted for a soybean claim are a
        # finding held at full confidence, and the satellite being blind
        # under cloud does not weaken it. Zeroing confidence there published
        # "0.000" against a claim the panel had just described as supported
        # by evidence that is not of a field — which an officer reads as
        # "this assessment knows nothing".
        #
        # So a critical fraud indicator keeps its confidence. The damage
        # figure is still withheld: that is the rule above, and it is
        # untouched by this.
        critical = any(
            f.severity == FraudSeverity.CRITICAL
            for f in (r.fraud_indicators or [])
        )
        if r.crop_presence == CropPresence.INCONCLUSIVE and not critical:
            r.confidence = 0.0
            applied.append("no_confidence_on_inconclusive_presence")
        elif r.crop_presence == CropPresence.INCONCLUSIVE:
            applied.append("confidence_kept_for_critical_finding")
    else:
        if r.damage_percent is None:
            r.insufficient_evidence = True
            r.severity = None
            applied.append("no_figure_treated_as_refusal")
        else:
            r.damage_percent = max(0.0, min(100.0, r.damage_percent))
            # Severity must agree with the figure it accompanies. A model
            # saying "Catastrophic" beside 8% is contradicting itself, and the
            # number is what downstream code reads.
            derived = severity_for(r.damage_percent)
            if r.severity != derived:
                r.severity = derived
                applied.append("severity_derived_from_damage_percent")

    if not ground and r.confidence > CONFIDENCE_CEILING_NO_GROUND_TRUTH:
        r.confidence = CONFIDENCE_CEILING_NO_GROUND_TRUTH
        applied.append("confidence_capped_no_ground_truth")

    if req.satellite.largest_gap_days > MATERIAL_GAP_DAYS:
        reduced = min(r.confidence, 0.45)
        if reduced < r.confidence:
            r.confidence = reduced
            applied.append("confidence_reduced_material_cloud_gap")

    # A model that finds no crop at all, yet reports damage, is incoherent.
    if r.crop_presence == CropPresence.ABSENT and r.damage_percent:
        r.damage_percent = None
        r.severity = None
        r.insufficient_evidence = True
        applied.append("damage_cleared_no_crop_present")

    r.confidence = max(0.0, min(1.0, r.confidence))
    r.guardrails_applied = applied
    return _finalise(r)


def _finalise(r: RawAssessment) -> AssessResult:
    """Convert a bounded RawAssessment into the validated contract."""
    return AssessResult(
        damage_percent=r.damage_percent,
        severity=r.severity,
        confidence=r.confidence,
        insufficient_evidence=r.insufficient_evidence,
        reasoning=r.reasoning,
        crop_presence=r.crop_presence,
        crop_presence_reasoning=r.crop_presence_reasoning,
        fraud_indicators=r.fraud_indicators,
        evidence_cited=r.evidence_cited,
        contradictions=r.contradictions,
        model=r.model,
        prompt_version=r.prompt_version,
        guardrails_applied=r.guardrails_applied,
    )


def severity_for(damage_percent: float) -> Severity:
    """Tier a damage figure, per the specification's classification."""
    if damage_percent >= 50:
        return Severity.CATASTROPHIC
    if damage_percent >= 33:
        return Severity.SEVERE
    if damage_percent >= 20:
        return Severity.MODERATE
    return Severity.NEGLIGIBLE


def rrvl_percent(baseline_ndvi: float, incident_ndvi: float) -> float:
    """Relative Rate of Vegetation Loss, per the specification.

    Retained for the report, not as a damage measure. It quantifies how far
    the canopy fell toward bare soil, which is a useful thing to plot and a
    poor thing to pay out on.
    """
    denom = baseline_ndvi - NDVI_BARE_SOIL
    if denom <= 0:
        return 0.0
    loss = (baseline_ndvi - incident_ndvi) / denom * 100
    return max(0.0, min(100.0, loss))


def peak_ndvi(req: AssessRequest) -> float | None:
    """Highest observed reading in the window."""
    if not req.satellite.time_series:
        return None
    return max(p.ndvi for p in req.satellite.time_series)


def vegetation_ever_present(req: AssessRequest) -> bool | None:
    """Deterministic crop-presence check.

    Cross-checks the model rather than replacing it: a series that never rises
    meaningfully above bare soil is strong evidence nothing was grown, and
    that finding should not depend on a language model noticing it.

    None when there are too few observations to say.
    """
    peak = peak_ndvi(req)
    if peak is None or req.satellite.observation_count < 2:
        return None
    return peak >= NDVI_VEGETATION_PRESENT
