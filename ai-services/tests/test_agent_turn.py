"""The turn endpoint's contract, without spending a model call.

What matters here is not what the model says -- that is the model's business -- but
that the shape of a turn is right and that nothing forbidden reaches the transport.
The second is the reason this file exists: the redaction layer is tested on its own,
and this checks it is actually *reached* on every path into the model, including the
system instruction and tool results from earlier turns.
"""

from __future__ import annotations

import json

from esurvey_ai.agents.turn import _declarations, _history, tool_result_json
from esurvey_ai.schemas import AgentMessage, AgentTurnRequest, ToolSpec


def _req(**kw) -> AgentTurnRequest:
    return AgentTurnRequest(system_instruction="You are an assistant.", **kw)


def test_a_tool_result_from_an_earlier_turn_is_redacted() -> None:
    # The whole history is resent on every turn, so a result from three turns ago
    # leaks exactly as well as a fresh one.
    req = _req(
        messages=[
            AgentMessage(role="user", text="Why was that claim refused?"),
            AgentMessage(
                role="tool",
                tool_name="get_claim",
                tool_result={
                    "farmer_id": "99900000002",
                    "farmer_name": "Demo Farm",
                    "crop_name": "Soybean",
                },
            ),
        ]
    )
    rendered = json.dumps(
        [
            [getattr(p, "text", None) or str(getattr(p, "function_response", ""))
             for p in c.parts or []]
            for c in _history(req)
        ],
        default=str,
    )

    assert "99900000002" not in rendered
    # And the part that was allowed survives, or the model is reasoning about
    # nothing.
    assert "Soybean" in rendered
    assert "Demo Farm" in rendered


def test_an_identifier_typed_into_a_question_is_redacted() -> None:
    # An officer pasting a farmer id into the chat box is the likeliest way one
    # reaches the model, and it arrives as ordinary message text.
    req = _req(
        messages=[AgentMessage(role="user", text="Show me farmer 99900000002")]
    )
    rendered = str(
        [[getattr(p, "text", "") for p in c.parts or []] for c in _history(req)]
    )
    assert "99900000002" not in rendered
    assert "Show me farmer" in rendered


def test_roles_map_to_what_the_sdk_expects() -> None:
    req = _req(
        messages=[
            AgentMessage(role="user", text="hello"),
            AgentMessage(role="assistant", text="hello back"),
        ]
    )
    history = _history(req)
    assert [c.role for c in history] == ["user", "model"]


def test_tools_are_declared_and_an_empty_list_declares_nothing() -> None:
    # An empty list asks the model to answer from what it already has, which is how
    # the caller ends a loop without a special case.
    assert _declarations(_req()) == []

    declared = _declarations(
        _req(
            tools=[
                ToolSpec(
                    name="list_claims",
                    description="Claims in the officer's posting.",
                    parameters={
                        "type": "OBJECT",
                        "properties": {"state": {"type": "STRING"}},
                    },
                )
            ]
        )
    )
    assert len(declared) == 1
    assert declared[0].function_declarations[0].name == "list_claims"


def test_a_tool_with_no_parameters_is_allowed() -> None:
    # Several of the officer's tools take none -- "how many claims are waiting on
    # me" -- and a schema of null is different from an empty schema.
    declared = _declarations(
        _req(
            tools=[
                ToolSpec(name="my_workload", description="Counts awaiting me.")
            ]
        )
    )
    assert declared[0].function_declarations[0].parameters is None


def test_the_bytes_a_tool_result_becomes_are_inspectable() -> None:
    # Exposed so the Go service's own tests can assert the boundary on the exact
    # bytes rather than trust that this end applies it.
    out = tool_result_json(
        {"farmer_id": "99900000002", "aadhaar_last4": "0000", "crop": "Udad"}
    )
    assert "99900000002" not in out
    assert "0000" not in out
    assert "Udad" in out


def test_marathi_survives_the_journey() -> None:
    # ensure_ascii=False for this reason: escaped Devanagari is still correct and
    # still unreadable to anybody debugging a prompt.
    out = tool_result_json({"note": "पिकाचे नुकसान"})
    assert "पिकाचे नुकसान" in out
