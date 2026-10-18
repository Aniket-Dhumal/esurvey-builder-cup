"""The optical index at the spots the farmer filmed.

An addition to the parcel-wide series, not a replacement for it. What is
load-bearing here is the instruction: the two specialists who judge the
farmer's own evidence must be told to weigh the reading at the point above the
field mean *for that purpose*, and must be told not to restate either as the
other. A panel shown two figures with no rule for which governs what will
average them, and the average is exactly the number that hides fraud.
"""

from esurvey_ai.agents.specialists import satellite_prompt, vision_prompt
from esurvey_ai.schemas import AssessRequest, NdviPoint, Parcel, SatelliteEvidence

JUDGES_THE_FOOTAGE = (satellite_prompt, vision_prompt)


def request(**kw) -> AssessRequest:
    base = dict(
        assessment_id="a-1",
        incident_date="2026-08-14",
        sowing_date="2026-06-15",
        calamity_type="hailstorm",
        parcel=Parcel(
            survey_number="42",
            farm_id="FARM-1",
            village_lgd_code="562100",
            owner_extent_hectares=2.0,
        ),
        satellite=SatelliteEvidence(
            time_series=[
                NdviPoint(date="2026-06-20", ndvi=0.20),
                NdviPoint(date="2026-08-10", ndvi=0.62),
            ],
            observation_count=2,
            largest_gap_days=51,
        ),
    )
    base.update(kw)
    return AssessRequest(**base)


def packet_with_points():
    return {
        "point_ndvi": {
            "radius_m": 20,
            "field_rrvl_percent": 12,
            "readings": [
                {
                    "station_no": 2,
                    "area_m2": 1180,
                    "observations": 5,
                    "rrvl_percent": 9,
                    "against_field_points": -3,
                    "footage_scv": 84,
                },
                {
                    "station_no": 4,
                    "area_m2": 310,
                    "observations": 4,
                    "rrvl_percent": 71,
                    "against_field_points": 59,
                    "weak": True,
                    "weak_because": "small_patch",
                },
            ],
            "clouded_points": [3],
            "contradictions": [
                {
                    "station_no": 2,
                    "footage_scv": 84,
                    "point_rrvl_percent": 9,
                    "area_m2": 1180,
                    "observations": 5,
                }
            ],
        }
    }


def test_the_readings_reach_the_specialists_that_judge_the_footage():
    req = request(evidence=packet_with_points())
    for prompt in JUDGES_THE_FOOTAGE:
        text = prompt(req)
        assert "station_no" in text, f"{prompt.__name__} was not given the points"
        assert "1180" in text, f"{prompt.__name__} was not given the sampled area"
        assert "84" in text, f"{prompt.__name__} was not given the footage score"


def test_the_absence_is_said_rather_than_left_out():
    # A specialist shown nothing about the capture points will assume the
    # comparison was made and came back clean.
    for prompt in JUDGES_THE_FOOTAGE:
        text = prompt(request(evidence={}))
        assert "not read at the individual capture points" in text or (
            "compared only against the field mean" in text
        ), f"{prompt.__name__} does not say the per-point check was not made"


def test_the_point_reading_governs_the_footage_and_not_the_field():
    # Both halves of the rule matter. Without the first the panel keeps
    # explaining a mismatch away as satellite averaging; without the second it
    # starts reporting a figure from a few hundred square metres as the
    # parcel's loss.
    for prompt in JUDGES_THE_FOOTAGE:
        text = prompt(request(evidence=packet_with_points())).lower()
        assert "above the field mean" in text or "more weight than the" in text, (
            f"{prompt.__name__} is not told to prefer the point readings when "
            f"judging the footage"
        )


def test_the_satellite_specialist_is_told_not_to_restate_a_point_as_the_parcel():
    text = satellite_prompt(request(evidence=packet_with_points()))
    assert "Never restate a point reading as the parcel's figure." in text


def test_a_weak_reading_is_to_be_ignored_rather_than_weighed_gently():
    # "Weigh it less" invites a model to weigh it anyway. A patch of a few
    # hundred square metres cannot support a finding in either direction.
    for prompt in JUDGES_THE_FOOTAGE:
        text = prompt(request(evidence=packet_with_points())).lower()
        assert "weak" in text, f"{prompt.__name__} never mentions weak readings"
        assert "cannot carry an allegation" in text or "cannot support a finding" in (
            text
        ), f"{prompt.__name__} does not say a weak reading supports nothing"


def test_a_disagreement_is_to_be_reported_not_averaged():
    for prompt in JUDGES_THE_FOOTAGE:
        text = prompt(request(evidence=packet_with_points())).lower()
        assert "contradiction" in text, (
            f"{prompt.__name__} is not told to report a disagreement as one"
        )
        assert "averag" in text, (
            f"{prompt.__name__} is not told what not to do with it"
        )
