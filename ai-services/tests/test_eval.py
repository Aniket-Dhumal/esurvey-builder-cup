"""The harness has to be right before its scores mean anything.

A suite that cannot fail is a suite that reports success. These check the
checks: that every rule can distinguish a good result from a bad one, and
that no case quietly asserts nothing.
"""

from __future__ import annotations

from esurvey_ai.eval.cases import (
    CASES,
    confidence_below,
    crop_presence_is,
    gives_reasons,
    raises,
    raises_no_fraud,
    refuses_a_damage_figure,
    states_it_is_unsure,
)
from esurvey_ai.schemas import (
    AssessResult,
    CropPresence,
    FraudIndicator,
    FraudSeverity,
)


def result(**kw) -> AssessResult:
    base = dict(
        damage_percent=None,
        severity=None,
        confidence=0.4,
        insufficient_evidence=True,
        reasoning="A sentence long enough to be worth an officer reading it here.",
        crop_presence=CropPresence.CONFIRMED,
        crop_presence_reasoning="the curve is a crop",
        fraud_indicators=[],
        evidence_cited=[],
        contradictions=[],
        model="test",
        prompt_version="test",
        guardrails_applied=[],
    )
    base.update(kw)
    return AssessResult(**base)


def test_every_case_asserts_something():
    # A case with no rules passes unconditionally and inflates the score.
    for c in CASES:
        assert c.checks, f"{c.name} asserts nothing"
        assert c.why.strip(), f"{c.name} does not say why it exists"


def test_case_names_are_unique():
    names = [c.name for c in CASES]
    assert len(names) == len(set(names))


def test_damage_figure_rule_distinguishes():
    assert refuses_a_damage_figure(result()).passed
    assert not refuses_a_damage_figure(result(damage_percent=62.0)).passed
    # Zero is a stated figure, not an absence. A model that says "0% damage"
    # has made a claim, and the rule has to see it.
    assert not refuses_a_damage_figure(result(damage_percent=0.0)).passed


def test_insufficient_evidence_rule_distinguishes():
    assert states_it_is_unsure(result()).passed
    assert not states_it_is_unsure(result(insufficient_evidence=False)).passed


def test_fraud_rules_distinguish():
    none = result()
    flagged = result(fraud_indicators=[
        FraudIndicator(
            code="no_vegetation_detected",
            severity=FraudSeverity.CRITICAL,
            detail="nothing grew",
        )
    ])

    assert raises_no_fraud(none).passed
    assert not raises_no_fraud(flagged).passed

    assert raises("no_vegetation_detected")(flagged).passed
    assert not raises("no_vegetation_detected")(none).passed
    # The wrong code must not satisfy the rule.
    assert not raises("crop_type_mismatch")(flagged).passed


def test_crop_presence_rule_distinguishes():
    assert crop_presence_is("confirmed")(result()).passed
    assert not crop_presence_is("absent")(result()).passed
    assert crop_presence_is("absent")(
        result(crop_presence=CropPresence.ABSENT)
    ).passed


def test_confidence_ceiling_distinguishes():
    assert confidence_below(0.75)(result(confidence=0.4)).passed
    assert confidence_below(0.75)(result(confidence=0.75)).passed
    assert not confidence_below(0.75)(result(confidence=0.9)).passed


def test_reasoning_rule_rejects_a_stub():
    assert gives_reasons(result()).passed
    assert not gives_reasons(result(reasoning="ok")).passed
    assert not gives_reasons(result(reasoning="   ")).passed


def test_no_case_expects_a_damage_percentage():
    # There is no ground truth: nobody has photographed a damaged field, had
    # it assessed, and recorded both. A case asserting a specific figure would
    # be measuring against a number somebody invented.
    for c in CASES:
        for name in c.checks:
            assert "%" not in name or "figure" in name.lower(), (
                f"{c.name}/{name} looks like it expects a specific figure"
            )
