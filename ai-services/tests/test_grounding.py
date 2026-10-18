"""The grounding packet reaching the panel.

The Go service assembles what a damage figure can stand on. The load-bearing
claim on this side is not that the figures are rendered nicely -- it is that
every specialist is told what could *not* be gathered, because a specialist
shown only what arrived cannot tell a well-grounded assessment from an
ungrounded one and will write with the same confidence either way.
"""

from esurvey_ai.agents.specialists import (
    phenology_prompt,
    satellite_prompt,
    vision_prompt,
    weather_prompt,
)
from esurvey_ai.schemas import AssessRequest, NdviPoint, Parcel, SatelliteEvidence

ALL_PROMPTS = (weather_prompt, phenology_prompt, vision_prompt, satellite_prompt)


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
            centroid_lat=18.5,
            centroid_lon=73.8,
        ),
        satellite=SatelliteEvidence(
            time_series=[
                NdviPoint(date="2026-06-20", ndvi=0.2),
                NdviPoint(date="2026-08-10", ndvi=0.31),
            ],
            observation_count=2,
            largest_gap_days=51,
        ),
    )
    base.update(kw)
    return AssessRequest(**base)


def test_no_packet_at_all_is_said_to_every_specialist():
    # The state this platform was in before any of these sources existed. A
    # specialist that is not told will write as though the figure were
    # grounded.
    req = request()
    for prompt in ALL_PROMPTS:
        text = prompt(req)
        assert "No grounding evidence was assembled" in text, prompt.__name__


def test_what_could_not_be_gathered_reaches_every_specialist():
    req = request(
        evidence={
            "absent": [
                "radar: the analytics engine has no radar module",
                "threshold yield: none has been issued for this unit",
            ]
        }
    )
    for prompt in ALL_PROMPTS:
        text = prompt(req)
        assert "Could NOT be gathered" in text, prompt.__name__
        assert "no radar module" in text, prompt.__name__
        assert "threshold yield" in text, prompt.__name__


def test_a_gap_is_framed_as_a_gap_and_not_as_a_finding():
    # "No radar reading" must not become "radar found nothing wrong".
    req = request(evidence={"absent": ["radar: no pass follows the incident yet"]})
    text = satellite_prompt(req)
    assert "gap in the evidence, not a finding of absence" in text


def test_a_complete_packet_says_so_rather_than_saying_nothing():
    req = request(evidence={"absent": []})
    text = weather_prompt(req)
    assert "Everything the service tried to gather was available" in text


def test_prior_seasons_reach_the_satellite_specialist():
    req = request(
        evidence={
            "denominators": {
                "parcel_history": {
                    "verdict": "below_normal",
                    "mean_prior_peak": 0.72,
                    "this_season_peak": 0.31,
                }
            }
        }
    )
    text = satellite_prompt(req)
    assert "below_normal" in text
    assert "0.72" in text
    assert "what this land normally" in text


def test_the_neighbourhood_verdict_reaches_the_satellite_specialist():
    req = request(
        evidence={
            "denominators": {
                "neighbourhood": {
                    "verdict": "regional_event",
                    "neighbourhood_rrvl_percent": 48.0,
                }
            }
        }
    )
    text = satellite_prompt(req)
    assert "regional_event" in text
    assert "corroborated by" in text


def test_radar_reaches_the_satellite_specialist_with_the_gap_it_covered():
    # The question worth asking when NDVI comes back with a two-month hole.
    req = request(
        evidence={
            "radar": {"findings": [{"code": "inundation", "confidence": "high"}]},
            "radar_passes_in_optical_gap": 4,
        }
    )
    text = satellite_prompt(req)
    assert "inundation" in text
    assert "observed this field 4 time(s)" in text
    assert "sees through cloud" in text


def test_the_thermal_stage_reaches_phenology_and_displaces_day_counting():
    req = request(
        evidence={
            "thermal": {"stage": "seed_fill", "accumulated_gdd": 1420.0},
            "susceptibility": {"level": "severe", "recoverable": False},
        }
    )
    text = phenology_prompt(req)
    assert "seed_fill" in text
    assert "1420" in text
    assert "rather than counting days" in text
    assert "severe" in text


def test_a_disputed_sowing_date_is_named_as_the_most_important_thing():
    req = request(
        evidence={
            "sowing_check": {
                "agrees": False,
                "difference_days": -34,
                "note": "the field was already greening before the declared sowing",
            }
        }
    )
    text = phenology_prompt(req)
    assert "already greening" in text
    assert "is built on the date" in text


def test_officer_counts_reach_the_vision_specialist_as_measurements():
    # Photographs are an impression; a quadrat count is a measurement, and
    # averaging the two would lose that.
    req = request(
        evidence={
            "field_measurements": {
                "quadrats": 3,
                "mean_stand_loss_percent": 62.0,
                "supports_a_figure": True,
            }
        }
    )
    text = vision_prompt(req)
    assert "62" in text
    assert "are measurements and your reading of the" in text


def test_the_clip_measurements_reach_the_vision_specialist():
    # The ground-vision service measures the same footage the specialist is
    # looking at -- stem angle, lodged and necrotic fractions -- and the
    # fusion weights it heavily. It was not shown to the specialist at all,
    # so the model's impression of the clips and the measurement of them sat
    # side by side and could never disagree out loud.
    req = request(
        evidence={
            "ground_cv": {
                "s_cv": 58.4,
                "stem_angle_degrees": 47.2,
                "lodging_loss": 0.61,
                "crop_pixel_share": 0.72,
                "stations_measured": 4,
            }
        }
    )
    text = vision_prompt(req)
    assert "47.2" in text
    assert "58.4" in text
    # And it is named as a measurement rather than another opinion.
    assert "computed from the" in text


def test_a_specialist_told_nothing_was_measured_says_so():
    text = vision_prompt(request(evidence={}))
    assert "The clips were not measured." in text


def test_the_summarised_weather_displaces_the_tool_call():
    # The season-long record is computed deterministically and is what gets
    # kept with the assessment; a tool call the model summarises is not.
    req = request(
        evidence={
            "weather": {
                "season": {"longest_dry_spell_days": 38, "rain_mm": 210.5},
            }
        }
    )
    text = weather_prompt(req)
    assert "38" in text
    assert "deterministically" in text
    assert "only to check a specific day" in text


def test_claim_signals_reach_the_specialist_with_their_innocent_reading():
    req = request(
        evidence={
            "claim_history": {
                "signals": [
                    {
                        "code": "repeat_claims_same_parcel",
                        "severity": "warning",
                        "detail": "claimed on in 3 separate years",
                        "innocent_explanation": (
                            "land that floods is damaged most years"
                        ),
                    }
                ]
            }
        }
    )
    text = satellite_prompt(req)
    assert "repeat_claims_same_parcel" in text
    assert "land that floods" in text
    assert "determine nothing" in text


def test_no_signals_means_no_history_section_at_all():
    # A heading with nothing under it reads as an absence of history rather
    # than as nothing worth raising.
    req = request(evidence={"claim_history": {"signals": []}})
    text = satellite_prompt(req)
    assert "claimed on this land before" not in text


def test_a_packet_with_an_unknown_field_does_not_break_anything():
    # The Go service owns this shape and will add to it. A panel that refused
    # an assessment for carrying a field it had not been taught about would
    # be the wrong way round.
    req = request(
        evidence={
            "absent": [],
            "something_added_next_quarter": {"nested": [1, 2, 3]},
        }
    )
    for prompt in ALL_PROMPTS:
        assert prompt(req)


def test_a_malformed_packet_degrades_rather_than_raising():
    # Defensive on purpose: this arrives over the wire.
    for bad in ({"denominators": "not a mapping"}, {"radar": 7}, {"absent": None}):
        req = request(evidence=bad)
        for prompt in ALL_PROMPTS:
            assert prompt(req)


# ---------------------------------------------------------------------------
# Moisture
# ---------------------------------------------------------------------------


def moisture(**over):
    m = {
        "canopy": {"near_incident_ndmi": 0.19, "anomaly_vs_prior_seasons": -0.14,
                   "prior_seasons_observed": 3},
        "findings": [{"code": "canopy_water_stress", "confidence": "high",
                      "detail": "drier than its history and its neighbours"}],
        "relevance": "Strong. Leaf water falls before greenness does.",
    }
    m.update(over)
    return m


def test_moisture_reaches_the_satellite_and_weather_specialists():
    req = request(calamity_type="drought", evidence={"moisture": moisture()})
    for prompt in (satellite_prompt, weather_prompt):
        text = prompt(req)
        assert "canopy_water_stress" in text, prompt.__name__
        assert "Strong. Leaf water" in text, prompt.__name__


def test_moisture_leads_with_what_it_bears_on():
    # An NDMI anomaly handed to a hailstorm specialist with no framing will be
    # reasoned about as though it bore on breakage.
    weak = moisture(relevance="Weak. This calamity breaks the crop.")
    req = request(evidence={"moisture": weak})
    text = satellite_prompt(req)
    assert text.index("Weak. This calamity") < text.index("canopy_water_stress")


def test_the_specialist_is_told_ndmi_is_not_a_damage_figure():
    req = request(evidence={"moisture": moisture()})
    assert "never turn an NDMI figure into a damage percentage" in satellite_prompt(req)


def test_a_generated_moisture_record_is_named_to_the_specialist():
    req = request(evidence={"moisture": moisture(synthetic=True)})
    assert "NOT AN OBSERVATION" in satellite_prompt(req)


def test_no_moisture_reading_is_said_rather_than_omitted():
    text = satellite_prompt(request(evidence={}))
    assert "No moisture reading was available." in text


def test_a_malformed_moisture_reading_degrades_rather_than_raising():
    for bad in ("not a mapping", 7, None, []):
        satellite_prompt(request(evidence={"moisture": bad}))
        weather_prompt(request(evidence={"moisture": bad}))


# ---------------------------------------------------------------------------
# A declared calamity is not a claim to be checked
# ---------------------------------------------------------------------------


def test_a_district_declaration_forbids_contradicting_it_on_the_grid():
    # Barshi, September 2026. The specialist was told the calamity was
    # officially declared and still returned "contradicts", because nothing
    # said what a declaration does to a disagreement. It is now a rule.
    req = request(calamity_type="drought", calamity_declared_by="district")
    text = weather_prompt(req)

    assert "declared by the district" in text
    assert 'do not return "contradicts"' in text
    assert "limitation of the instrument" in text or "limitations" in text
    # And the part that stays the specialist's own: a declaration covers an
    # area, not a field.
    assert "every field in it lost a crop" in text


def test_a_farmers_claim_gets_no_such_protection():
    req = request(calamity_type="drought", calamity_declared_by="farmer")
    text = weather_prompt(req)
    assert 'do not return "contradicts"' not in text


def test_a_season_of_traces_is_flagged_as_an_upper_bound():
    # 468 mm across 49 rain days and 52 trace days reads as an adequate
    # season and was a drought. The specialist is told which figure to trust.
    req = request(
        calamity_type="drought",
        evidence={
            "weather": {
                "season": {
                    "rain_mm": 468.8,
                    "rain_days": 49,
                    "trace_rain_days": 52,
                    "longest_dry_spell_days": 15,
                }
            }
        },
    )
    text = weather_prompt(req)

    assert "upper bound" in text
    assert "52 days held only a trace" in text
    assert "least reliable number" in text


def test_an_ordinary_season_is_not_second_guessed():
    # The caveat is for the case it describes. A season whose rain days
    # outnumber its trace days is reported without it.
    req = request(
        evidence={
            "weather": {
                "season": {"rain_mm": 620.0, "rain_days": 48, "trace_rain_days": 11}
            }
        }
    )
    assert "upper bound" not in weather_prompt(req)


# ---------------------------------------------------------------------------
# The state's gauges outrank the reanalysis on the amount
# ---------------------------------------------------------------------------


def _gauges():
    return {
        "source": "maharain_mh_gov_in",
        "unit_level": "sub_district",
        "unit_name": "Barshi",
        "crop_year": 2026,
        "months": [
            {"month": 8, "label": "August 2026", "normal_mm": 124.5,
             "actual_mm": 19.4, "percent_of_normal": 15.6},
        ],
        "season_to_date": {"label": "1 June to 28 September 2026",
                           "normal_mm": 516.6, "actual_mm": 286.0,
                           "percent_of_normal": 55.4},
    }


def test_the_gauge_figure_reaches_the_specialist_with_its_precedence():
    # ERA5 said 505 mm for this season. The state's gauges say 286 mm of a
    # normal 516.6. Handing the model both with no ordering invites it to
    # average them, and the average is wrong.
    req = request(
        calamity_type="drought",
        evidence={
            "official_rainfall": _gauges(),
            "weather": {"season": {"rain_mm": 505.4, "rain_days": 42}},
        },
    )
    text = weather_prompt(req)

    assert "286.0" in text
    assert "516.6" in text
    assert "not model output" in text
    # The precedence, stated rather than left to inference.
    assert "the state's gauges are right" in text
    assert "do not average them" in text.lower()


def test_the_reanalysis_keeps_the_job_it_is_good_at():
    # It is still the only daily field available for the current season, so dry
    # spells and heat runs come from it and must not be discarded along with its
    # rainfall total.
    req = request(calamity_type="drought", evidence={"official_rainfall": _gauges()})
    text = weather_prompt(req)
    assert "Which days the rain fell on" in text
    assert "no daily figures for the current year" in text


def test_a_missing_gauge_figure_is_said_rather_than_passed_over():
    # Most districts have nothing loaded. The model must not treat a reanalysis
    # total as a measured amount just because nothing contradicted it.
    req = request(calamity_type="drought",
                  evidence={"weather": {"season": {"rain_mm": 505.4}}})
    text = weather_prompt(req)

    assert "No published rainfall figure was available" in text
    assert "no normal to compare it against" in text


def test_the_monthly_rows_are_pointed_at():
    # A season at 55% of normal reads as a poor year; August at 16% through
    # grain fill is a crop failure. The seasonal figure hides it.
    req = request(calamity_type="drought", evidence={"official_rainfall": _gauges()})
    text = weather_prompt(req)
    assert "hides the thing that kills a crop" in text
