"""Guardrail behaviour.

These encode the rules that stop a fluent model producing an indefensible
number. They matter more than the prompt: a prompt is a request, a guardrail
is a guarantee.

The central rule under test: a damage figure requires ground imagery.
Sentinel-2 at 10 m over a smallholding establishes that a crop existed. It
does not measure how badly that crop was damaged.
"""

from __future__ import annotations

import pytest

from esurvey_ai import guardrails
from esurvey_ai.schemas import (
    AssessRequest,
    Crop,
    CropPresence,
    FraudIndicator,
    FraudSeverity,
    Image,
    NdviPoint,
    Parcel,
    RawAssessment,
    SatelliteEvidence,
    Severity,
    TerrainEvidence,
)

# A 1x1 PNG. Only the `kind` is load-bearing in these tests.
TINY_PNG = (
    "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAAAAAA6fptV"
    "AAAACklEQVR4nGMAAgAABQABDQottAAAAABJRU5ErkJggg=="
)


def make_request(
    *,
    points: list[tuple[str, float]] | None = None,
    gap_days: int = 0,
    crop: Crop | None = None,
    images: list[Image] | None = None,
    incident_date: str = "2025-10-12",
    declared_by: str | None = None,
) -> AssessRequest:
    series = [NdviPoint(date=d, ndvi=v) for d, v in (points or [])]
    return AssessRequest(
        assessment_id="test",
        incident_date=incident_date,
        parcel=Parcel(
            survey_number="453",
            farm_id="MH003078187222",
            village_lgd_code="562111",
            owner_extent_hectares=1.6,
            polygon_area_hectares=4.82,
        ),
        crop=crop,
        satellite=SatelliteEvidence(
            time_series=series,
            observation_count=len(series),
            largest_gap_days=gap_days,
            baseline_ndvi=max((v for _, v in points or []), default=None),
            incident_ndvi=series[-1].ndvi if series else None,
        ),
        terrain=TerrainEvidence(
            mean_slope_degrees=1.38,
            classification="Nearly Level / Flat",
            waterlogging_risk="High",
        ),
        images=images or [],
        calamity_declared_by=declared_by,
    )


def ground_photo() -> Image:
    return Image(kind="field_photo", data_uri=TINY_PNG)


def ndvi_overlay() -> Image:
    return Image(kind="ndvi_last", data_uri=TINY_PNG)


def model_said(
    damage: float | None,
    confidence: float,
    *,
    refused: bool = False,
    severity: Severity | None = None,
    presence: CropPresence = CropPresence.CONFIRMED,
    fraud: list[FraudIndicator] | None = None,
) -> RawAssessment:
    return RawAssessment(
        damage_percent=damage,
        severity=severity,
        confidence=confidence,
        insufficient_evidence=refused,
        reasoning="test",
        crop_presence=presence,
        crop_presence_reasoning="test",
        fraud_indicators=fraud or [],
        model="gemini-test",
        prompt_version="test",
    )


HEALTHY_SERIES = [("2025-08-01", 0.70), ("2025-10-20", 0.30)]


# --- the central rule -----------------------------------------------------


def test_a_certain_finding_keeps_its_confidence():
    """Photographs of a bedroom are a finding, not an absence of one.

    The guardrail zeroed confidence whenever crop presence was inconclusive,
    on the reasoning that an unsettled crop means nothing is known. That is
    usually true and was false here: the panel was certain the evidence was
    not of a field, said so at full confidence, and the assessment published
    0.000 beside it -- which an officer reads as "this knows nothing".
    """
    raw = RawAssessment(
        reasoning="the photographs show a bedroom",
        confidence=0.95,
        crop_presence=CropPresence.INCONCLUSIVE,
        damage_percent=None,
        insufficient_evidence=True,
        fraud_indicators=[
            FraudIndicator(
                code="evidence_not_of_field",
                severity=FraudSeverity.CRITICAL,
                detail="the submitted photographs are of a room",
            )
        ],
    )
    # Photographs were submitted -- that is the whole point -- so the
    # no-ground-truth cap does not apply. What they show is the finding.
    out = guardrails.postflight(raw, make_request(images=[ground_photo()]))
    assert out.confidence == 0.95
    assert "confidence_kept_for_critical_finding" in out.guardrails_applied
    # And the figure is still withheld: certainty about fraud is not a
    # licence to state a damage percentage.
    assert out.damage_percent is None


def test_an_unsettled_crop_with_nothing_damning_still_scores_zero():
    raw = RawAssessment(
        reasoning="the series is blind and no photographs were sent",
        confidence=0.4,
        crop_presence=CropPresence.INCONCLUSIVE,
        damage_percent=None,
        insufficient_evidence=True,
    )
    out = guardrails.postflight(raw, make_request())
    assert out.confidence == 0.0
    assert "no_confidence_on_inconclusive_presence" in out.guardrails_applied


def test_damage_figure_requires_ground_imagery() -> None:
    # The model may produce a number from satellite alone. It is discarded.
    req = make_request(points=HEALTHY_SERIES)
    out = guardrails.postflight(model_said(62, 0.8), req)

    assert out.damage_percent is None
    assert out.severity is None
    assert out.insufficient_evidence
    assert "damage_requires_ground_imagery" in out.guardrails_applied


def test_ndvi_overlays_do_not_count_as_ground_imagery() -> None:
    # A colourised satellite overlay is still satellite. Only a photograph of
    # the field is ground truth.
    req = make_request(points=HEALTHY_SERIES, images=[ndvi_overlay()])
    out = guardrails.postflight(model_said(55, 0.7), req)

    assert out.damage_percent is None
    assert "damage_requires_ground_imagery" in out.guardrails_applied


def test_ground_imagery_permits_a_damage_figure() -> None:
    req = make_request(points=HEALTHY_SERIES, images=[ground_photo()])
    out = guardrails.postflight(model_said(45, 0.7), req)

    assert out.damage_percent == pytest.approx(45)
    assert out.severity == Severity.SEVERE
    assert not out.insufficient_evidence


# --- pre-flight -----------------------------------------------------------


def test_refuses_only_when_there_is_nothing_at_all() -> None:
    result = guardrails.preflight(make_request(points=[]), min_observations=2)
    assert result is not None
    assert result.insufficient_evidence
    assert result.crop_presence == CropPresence.INCONCLUSIVE
    assert "no_evidence_at_all" in result.guardrails_applied


def test_sparse_series_still_proceeds() -> None:
    # Crop presence and fraud indicators remain worth having even when damage
    # cannot be quantified, so a thin series is no longer a blocker.
    req = make_request(points=[("2025-10-01", 0.45)])
    assert guardrails.preflight(req, min_observations=2) is None


def test_ground_imagery_alone_proceeds() -> None:
    req = make_request(points=[], images=[ground_photo()])
    assert guardrails.preflight(req, min_observations=2) is None


# --- coherence ------------------------------------------------------------


def test_damage_cleared_when_no_crop_was_ever_present() -> None:
    # Reporting damage to a crop that never existed is incoherent.
    req = make_request(points=HEALTHY_SERIES, images=[ground_photo()])
    out = guardrails.postflight(model_said(40, 0.7, presence=CropPresence.ABSENT), req)
    assert out.damage_percent is None
    assert out.insufficient_evidence
    assert "damage_cleared_no_crop_present" in out.guardrails_applied


def test_refusal_cannot_smuggle_a_figure() -> None:
    req = make_request(points=HEALTHY_SERIES, images=[ground_photo()])
    out = guardrails.postflight(
        model_said(72, 0.8, refused=True, severity=Severity.CATASTROPHIC), req
    )
    assert out.damage_percent is None
    assert out.severity is None
    assert "cleared_figure_on_refusal" in out.guardrails_applied


def test_severity_is_derived_not_trusted() -> None:
    req = make_request(points=HEALTHY_SERIES, images=[ground_photo()])
    out = guardrails.postflight(model_said(8, 0.7, severity=Severity.CATASTROPHIC), req)
    assert out.severity == Severity.NEGLIGIBLE
    assert "severity_derived_from_damage_percent" in out.guardrails_applied


def test_damage_percent_is_clamped() -> None:
    req = make_request(points=HEALTHY_SERIES, images=[ground_photo()])
    assert guardrails.postflight(model_said(180, 0.5), req).damage_percent == 100
    assert guardrails.postflight(model_said(-20, 0.5), req).damage_percent == 0


# --- confidence -----------------------------------------------------------


def test_confidence_capped_without_ground_truth() -> None:
    req = make_request(points=HEALTHY_SERIES)
    out = guardrails.postflight(model_said(None, 0.95, refused=True), req)
    assert out.confidence == pytest.approx(0.6)
    assert "confidence_capped_no_ground_truth" in out.guardrails_applied


def test_long_cloud_gap_reduces_confidence() -> None:
    req = make_request(points=[("2025-07-19", 0.14), ("2025-09-17", 0.36)], gap_days=60)
    out = guardrails.postflight(model_said(None, 0.9, refused=True), req)
    assert out.confidence <= 0.45
    assert "confidence_reduced_material_cloud_gap" in out.guardrails_applied


def test_inconclusive_presence_carries_no_confidence() -> None:
    req = make_request(points=[("2025-10-01", 0.4)])
    out = guardrails.postflight(
        model_said(None, 0.8, refused=True, presence=CropPresence.INCONCLUSIVE),
        req,
    )
    assert out.confidence == 0.0
    assert "no_confidence_on_inconclusive_presence" in out.guardrails_applied


# --- crop presence and fraud ---------------------------------------------


def test_fraud_indicators_survive_a_refusal() -> None:
    # The whole point of the redesign: a claim can be flagged even when no
    # damage figure is possible.
    req = make_request(points=[("2025-08-01", 0.13), ("2025-10-20", 0.12)])
    flag = FraudIndicator(
        code="no_vegetation_detected",
        severity=FraudSeverity.CRITICAL,
        detail="Soybean declared; NDVI never exceeded 0.13 against bare soil at 0.12.",
    )
    out = guardrails.postflight(
        model_said(None, 0.7, refused=True, presence=CropPresence.ABSENT, fraud=[flag]),
        req,
    )
    assert out.insufficient_evidence
    assert len(out.fraud_indicators) == 1
    assert out.fraud_indicators[0].code == "no_vegetation_detected"
    assert out.crop_presence == CropPresence.ABSENT


def test_vegetation_detected_on_a_real_crop_cycle() -> None:
    req = make_request(points=[("2025-08-01", 0.70), ("2025-09-01", 0.62)])
    assert guardrails.vegetation_ever_present(req) is True


def test_no_vegetation_on_a_flat_bare_series() -> None:
    req = make_request(points=[("2025-08-01", 0.13), ("2025-09-01", 0.12)])
    assert guardrails.vegetation_ever_present(req) is False


def test_vegetation_check_abstains_on_one_reading() -> None:
    req = make_request(points=[("2025-08-01", 0.70)])
    assert guardrails.vegetation_ever_present(req) is None


# --- deterministic arithmetic --------------------------------------------


def test_severity_tiers() -> None:
    assert guardrails.severity_for(5) == Severity.NEGLIGIBLE
    assert guardrails.severity_for(25) == Severity.MODERATE
    assert guardrails.severity_for(40) == Severity.SEVERE
    assert guardrails.severity_for(75) == Severity.CATASTROPHIC


def test_rrvl_matches_the_specification() -> None:
    # 0.80 -> 0.30, against bare soil at 0.12: 0.50 / 0.68 = 73.5%
    assert guardrails.rrvl_percent(0.80, 0.30) == pytest.approx(73.5, abs=0.1)


def test_rrvl_handles_baseline_at_bare_soil() -> None:
    assert guardrails.rrvl_percent(0.10, 0.05) == 0.0


# ---------------------------------------------------------------------------
# A district's declaration is not the farmer's claim
# ---------------------------------------------------------------------------


def _not_in_record() -> FraudIndicator:
    return FraudIndicator(
        code="calamity_not_in_record",
        severity=FraudSeverity.WARNING,
        detail="the rainfall record over this cell shows no drought",
    )


def test_no_fraud_indicator_stands_against_a_district_declaration():
    # Barshi, September 2026. The Collector declared drought over 1-20
    # September; the farmer filed under that declaration. The panel read an
    # ~11 km reanalysis grid, disagreed, and raised an indicator -- against
    # the farmer, for the district's statement.
    raw = model_said(
        None, 0.45, refused=True, fraud=[_not_in_record()]
    )
    out = guardrails.postflight(raw, make_request(declared_by="district"))

    codes = [f.code for f in out.fraud_indicators]
    assert "calamity_not_in_record" not in codes, (
        "a farmer is accused of the district's own declaration"
    )
    assert "no_fraud_indicator_against_a_district_declaration" in out.guardrails_applied


def test_the_same_indicator_stands_against_a_farmers_own_claim():
    # The rule is narrow. Where the farmer asserted the calamity, a record
    # that positively disagrees is exactly what an officer should see.
    raw = model_said(None, 0.45, refused=True, fraud=[_not_in_record()])
    out = guardrails.postflight(raw, make_request(declared_by="farmer"))

    assert [f.code for f in out.fraud_indicators] == ["calamity_not_in_record"]


def test_other_indicators_survive_a_declaration():
    # A declaration says drought happened in this area. It says nothing about
    # whether these photographs are of a field.
    other = FraudIndicator(
        code="evidence_not_of_field",
        severity=FraudSeverity.CRITICAL,
        detail="the images show a room",
    )
    raw = model_said(None, 0.9, refused=True, fraud=[_not_in_record(), other])
    out = guardrails.postflight(raw, make_request(declared_by="district"))

    assert [f.code for f in out.fraud_indicators] == ["evidence_not_of_field"]
