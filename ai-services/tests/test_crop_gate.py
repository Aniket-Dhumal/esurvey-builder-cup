"""Crop presence is settled before anything reasons about the crop.

The four specialists used to run together, so the phenology specialist
reasoned about pod-fill stage -- and returned "supports" -- for a parcel whose
photographs showed a bedroom. The synthesiser caught the contradiction
afterwards, which is late: a vote in favour of a claim whose crop was never
established is a vote that should not have been cast.

Two rounds now. What is on the ground first; what it means second.
"""

from __future__ import annotations

import pytest

from esurvey_ai.agents.findings import (
    SatelliteFinding,
    SpecialistReport,
    VisionFinding,
)
from esurvey_ai.agents.panel import crop_presence_from, run_panel
from esurvey_ai.schemas import Crop

from .test_guardrails import ground_photo, make_request
from .test_panel import BARE, HEALTHY, stub, with_position


def vision(matches: bool | None, identified: str = "") -> SpecialistReport:
    return SpecialistReport(
        agent="vision_specialist",
        ran=True,
        finding=VisionFinding(
            verdict="inconclusive",
            reasoning="test",
            confidence=1.0,
            crop_identified=identified,
            crop_matches_declaration=matches,
        ),
    )


class TestWhatRoundOneSettles:
    def test_a_photograph_of_something_else_means_no_crop(self):
        # The case this was written for: four indoor photographs against a
        # declared soybean crop.
        assert crop_presence_from([vision(matches=False)]) == "absent"

    def test_the_declared_crop_seen_means_a_crop(self):
        assert crop_presence_from([vision(matches=True)]) == "present"

    def test_a_crop_named_but_not_matched_still_counts_as_a_crop(self):
        # Something is growing; whether it is what was declared is a different
        # question, and one the synthesiser weighs.
        assert crop_presence_from([vision(None, identified="maize")]) == "present"

    def test_nothing_to_go_on_is_not_a_verdict(self):
        assert crop_presence_from([]) == "inconclusive"

    def test_a_specialist_that_did_not_run_settles_nothing(self):
        did_not_run = SpecialistReport(agent="vision_specialist", ran=False)
        assert crop_presence_from([did_not_run]) == "inconclusive"


def curve(present: bool | None) -> SpecialistReport:
    return SpecialistReport(
        agent="satellite_specialist",
        ran=True,
        finding=SatelliteFinding(
            verdict="inconclusive",
            reasoning="test",
            confidence=0.8,
            crop_present=present,
        ),
    )


class TestBothInstruments:
    """Where photographs and the curve both spoke, both are weighed."""

    def test_the_curve_alone_confirms_a_crop(self):
        # A screening from the console has no photographs, and a season that
        # rose from bare soil and fell again is real evidence.
        assert crop_presence_from([curve(True)]) == "present"

    def test_the_curve_alone_can_find_none(self):
        assert crop_presence_from([curve(False)]) == "absent"

    def test_agreement_settles_it(self):
        assert crop_presence_from([vision(True), curve(True)]) == "present"
        assert crop_presence_from([vision(False), curve(False)]) == "absent"

    def test_a_bad_photograph_does_not_prove_bare_land(self):
        # The mirror of the case the gate was written for: a farmer sends a
        # photograph of the wrong field, and the curve shows their own field
        # grew a crop. That is a conflict to report, not a verdict.
        assert crop_presence_from([vision(False), curve(True)]) == "contested"

    def test_a_good_photograph_does_not_overrule_bare_soil(self):
        assert crop_presence_from([vision(True), curve(False)]) == "contested"


class TestTheGate:
    """What the panel does with that answer."""

    @pytest.mark.asyncio
    async def test_no_crop_means_no_growth_stage(self, monkeypatch):
        async def saw_a_bedroom(*a, **k):
            return vision(matches=False)

        async def never(*a, **k):  # pragma: no cover - must not be reached
            raise AssertionError(
                "the phenology specialist reasoned about a crop that is not there"
            )

        stub(monkeypatch, "vision", saw_a_bedroom)
        stub(monkeypatch, "phenology", never)

        req = with_position(make_request(points=BARE))
        req.images = [ground_photo()]
        result = await run_panel(req)

        phenology = next(
            r for r in result.reports if r.agent == "phenology_specialist"
        )
        assert not phenology.ran
        # And it says why, rather than looking like a service that was down.
        assert "no crop was found" in (phenology.error or "")

    @pytest.mark.asyncio
    async def test_a_contested_crop_has_no_growth_stage(self, monkeypatch):
        async def saw_another_field(*a, **k):
            return vision(matches=False)

        async def curve_says_grown(*a, **k):
            return curve(True)

        async def never(*a, **k):  # pragma: no cover - must not be reached
            raise AssertionError("a contested crop was given a growth stage")

        stub(monkeypatch, "vision", saw_another_field)
        stub(monkeypatch, "satellite", curve_says_grown)
        stub(monkeypatch, "phenology", never)

        req = with_position(make_request(
            points=HEALTHY,
            crop=Crop(name="Soybean", sowing_date="2025-06-24"),
        ))
        req.images = [ground_photo()]
        result = await run_panel(req)

        phenology = next(
            r for r in result.reports if r.agent == "phenology_specialist"
        )
        assert not phenology.ran
        assert "disagree" in (phenology.error or "")

    @pytest.mark.asyncio
    async def test_a_crop_that_is_there_is_reasoned_about(self, monkeypatch):
        asked = []

        async def saw_the_crop(*a, **k):
            return vision(matches=True, identified="soybean")

        async def phenology(*a, **k):
            asked.append(True)
            return SpecialistReport(agent="phenology_specialist", ran=True)

        stub(monkeypatch, "vision", saw_the_crop)
        stub(monkeypatch, "phenology", phenology)

        # A crop and a sowing date, without which there is no stage to place
        # and the specialist is skipped for that reason instead.
        req = with_position(make_request(
            points=HEALTHY,
            crop=Crop(name="Soybean", sowing_date="2025-06-24"),
        ))
        req.images = [ground_photo()]
        await run_panel(req)

        assert asked, "a crop was found and nothing asked what stage it was at"
