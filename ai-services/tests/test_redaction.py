"""The data boundary, checked rather than trusted.

Every case here is a way an identifier reaches a model despite a field having been
removed, because that is the only interesting question: dropping a `farmer_id` key
is easy and does not, on its own, stop a farmer id leaving.
"""

from __future__ import annotations

from esurvey_ai.redaction import redact


def test_the_two_named_fields_are_removed() -> None:
    payload = {
        "farmer_id": "99900000002",
        "aadhaar_last4": "0000",
        "aadhaar_hash": (
            "487abd86d12d8cb468aeb5c8ed42077a"
            "947b91a5b142b4f3b1927d53da681583"
        ),
        "crop_name": "Soybean",
        "area_sown_ha": 0.00506,
    }
    cleaned, removed = redact(payload)

    assert "farmer_id" not in cleaned
    assert "aadhaar_last4" not in cleaned
    assert "aadhaar_hash" not in cleaned
    assert cleaned["crop_name"] == "Soybean"
    assert cleaned["area_sown_ha"] == 0.00506
    assert removed == ["aadhaar_hash", "aadhaar_last4", "farmer_id"]


def test_the_name_and_boundary_are_kept() -> None:
    # Allowed explicitly: the name is what makes a filled report usable to an
    # officer, and the boundary is what the guidance reasons about.
    payload = {
        "farmer_name": "Harichndra Yedaba Dhumal",
        "plot_geometry": {
            "type": "Polygon",
            "coordinates": [[[72.8616179, 19.1156938], [72.8620187, 19.1158961]]],
        },
    }
    cleaned, removed = redact(payload)

    assert cleaned["farmer_name"] == "Harichndra Yedaba Dhumal"
    assert cleaned["plot_geometry"]["coordinates"][0][0] == [72.8616179, 19.1156938]
    assert removed == []


def test_the_mobile_number_goes_with_them() -> None:
    # Not named by the user, withheld anyway: it serves nothing any agent does, and
    # a contact number is the field whose leaking has an immediate consequence for
    # the person.
    cleaned, removed = redact({"mobile_no": "9820000002", "crop_name": "Udad"})

    assert "mobile_no" not in cleaned
    assert cleaned["crop_name"] == "Udad"
    assert removed == ["mobile_no"]


def test_spelling_does_not_decide_the_boundary() -> None:
    # The same field arrives as farmer_id, farmerId and FarmerID depending on which
    # layer produced it. A boundary that depends on spelling is not a boundary.
    for key in ("farmer_id", "farmerId", "FarmerID", "FARMER-ID", "farmer id"):
        cleaned, removed = redact({key: "99900000002", "crop": "Soybean"})
        assert key not in cleaned, key
        assert removed, key


def test_an_identifier_inside_free_text_is_caught() -> None:
    # The case a key-based filter misses entirely. Identifiers turn up in address
    # lines, officer notes and captions somebody typed.
    payload = {
        "address": "Plot behind the school, farmer 99900000002, Malegaon",
        "note": "Spoke to owner on 9820000002 about aadhaar 123456789012",
    }
    cleaned, removed = redact(payload)

    assert "99900000002" not in cleaned["address"]
    assert "9820000002" not in cleaned["note"]
    assert "123456789012" not in cleaned["note"]
    # The surrounding sentence survives; only the identifier goes.
    assert "Malegaon" in cleaned["address"]
    assert "Spoke to owner" in cleaned["note"]
    assert any("farmer-id-shaped" in r for r in removed)
    assert any("aadhaar-shaped" in r for r in removed)
    assert any("mobile-shaped" in r for r in removed)


def test_an_aadhaar_grouped_the_way_the_card_prints_it_is_caught() -> None:
    # The form the number takes when a person typed it rather than when a system
    # stored it: four digits, a space or a hyphen, four, four. An uploaded
    # spreadsheet is full of it, and the bare twelve-digit shape walks past it.
    for written in ("2345 6789 0123", "2345-6789-0123", "2345 6789-0123"):
        cleaned, removed = redact({"note": f"aadhaar {written} of Malegaon"})
        assert written not in cleaned["note"], written
        assert any("aadhaar-shaped" in r for r in removed), written
        assert "Malegaon" in cleaned["note"]


def test_the_grouped_shape_does_not_eat_dates_or_survey_numbers() -> None:
    # The cost of the shape above, bounded. A date is four digits then two and
    # two, a financial year is four and four, and a survey number is short: none
    # of them is three groups of four.
    payload = {
        "occurred_at": "2026-09-29",
        "season": "2026-2027",
        "survey_number": "102/2A",
        "window": "2026-09-29 to 2026-10-02",
    }
    cleaned, removed = redact(payload)

    assert cleaned == payload
    assert removed == []


def test_an_identifier_nested_deep_in_a_claim_is_caught() -> None:
    # A real claim is nested: report, items, captures, registry record. A filter
    # that only reads the top level reads almost none of it.
    payload = {
        "report": {
            "items": [
                {"crop_name": "Soybean", "farmer_id": "99900000002"},
                {"crop_name": "Udad", "notes": ["owner 56567763672"]},
            ]
        }
    }
    cleaned, removed = redact(payload)

    assert "farmer_id" not in cleaned["report"]["items"][0]
    assert "56567763672" not in cleaned["report"]["items"][1]["notes"][0]
    assert len(removed) == 2


def test_figures_that_merely_look_long_are_left_alone() -> None:
    # Coordinates, areas and percentages carry decimal points; survey numbers are
    # short; timestamps are longer than any identifier shape. A redactor that eats
    # these is worse than none, because the guidance then reasons about nothing.
    payload = {
        "lat": 19.1156938,
        "lon": 72.8622575,
        "area_sown_ha": 0.00506,
        "damage_percent": 70.0,
        "survey_number": "102",
        "captured_at": "2026-09-29T18:24:42Z",
        "byte_size": 16307786,
        "epoch_ms": "1790697930053289",
    }
    cleaned, removed = redact(payload)

    assert cleaned == payload
    assert removed == []


def test_a_twelve_digit_run_inside_a_longer_number_is_not_half_eaten() -> None:
    # Anchored on non-digits for this reason: a checksum or an epoch containing
    # twelve consecutive digits must come through whole or the figure is corrupted
    # rather than protected.
    cleaned, _ = redact({"ref": "99123456789012345"})
    assert cleaned["ref"] == "99123456789012345"


def test_nothing_removed_is_reported_by_content() -> None:
    # A log of removals that quoted the identifiers would recreate the problem in
    # the log file, where it lives longer and is read by more people.
    _, removed = redact({"farmer_id": "99900000002", "note": "aadhaar 123456789012"})
    joined = " ".join(removed)
    assert "99900000002" not in joined
    assert "123456789012" not in joined


def test_a_bare_string_and_a_bare_list_are_handled() -> None:
    # Called on whatever an agent is about to send, which is not always a dict.
    cleaned, removed = redact("owner 99900000002 of Malegaon")
    assert "99900000002" not in cleaned
    assert removed

    cleaned, removed = redact(["Soybean", "owner 123456789012"])
    assert cleaned[0] == "Soybean"
    assert "123456789012" not in cleaned[1]


def test_none_and_numbers_pass_through() -> None:
    cleaned, removed = redact({"a": None, "b": 7, "c": 1.5, "d": True})
    assert cleaned == {"a": None, "b": 7, "c": 1.5, "d": True}
    assert removed == []
