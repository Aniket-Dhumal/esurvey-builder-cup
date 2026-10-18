"""No provider error reaches an officer.

The case these were written for happened: the Gemini key's IP restriction
refused every call, and every assessment was stored with Google's raw error
body in its reasoning -- the server's public IP, the project number and the
key's restriction policy, on a record that is printed and handed to farmers.
"""

from __future__ import annotations

import asyncio

import httpx
import pytest
from google.genai import errors as genai_errors

from esurvey_ai import failures
from esurvey_ai.agents import panel as panel_mod
from esurvey_ai.agents.base import PanelUnavailableError, Specialist
from esurvey_ai.agents.findings import SpecialistReport

# The body Google actually returned on 14 September 2026, with the real IP and
# project number replaced by documentation values that match the same shapes.
IP_RESTRICTED = {
    "error": {
        "code": 403,
        "message": (
            "The provided API key has an IP address restriction. The "
            "originating IP address of the call (203.0.113.8) violates this "
            "restriction."
        ),
        "status": "PERMISSION_DENIED",
        "details": [{
            "@type": "type.googleapis.com/google.rpc.ErrorInfo",
            "reason": "API_KEY_IP_ADDRESS_BLOCKED",
            "domain": "googleapis.com",
            "metadata": {
                "service": "generativelanguage.googleapis.com",
                "callerIp": "203.0.113.8",
                "consumer": "projects/100000000001",
            },
        }],
    }
}

LEAKS = ("203.0.113.8", "projects/", "googleapis", "PERMISSION_DENIED",
         "API_KEY", "callerIp", "{")


def assert_clean(text: str | None) -> None:
    for fragment in LEAKS:
        assert fragment not in (text or ""), f"{fragment!r} leaked into: {text!r}"


def ip_restricted() -> genai_errors.ClientError:
    return genai_errors.ClientError(403, IP_RESTRICTED)


def test_the_real_ip_restriction_error_is_described_not_repeated():
    exc = ip_restricted()
    # The precondition: the SDK's own string is exactly the leak.
    assert "203.0.113.8" in str(exc)

    said = failures.describe(exc)
    assert said == failures.ACCESS_DENIED
    assert_clean(said)


@pytest.mark.parametrize("code, expected", [
    (401, failures.ACCESS_DENIED),
    (403, failures.ACCESS_DENIED),
    (429, failures.RATE_LIMITED),
    (400, failures.REJECTED),
    (500, failures.UNAVAILABLE),
    (503, failures.UNAVAILABLE),
])
def test_status_codes_map_to_sentences(code, expected):
    body = {"error": {"code": code, "message": "secret 203.0.113.8", "status": "X"}}
    cls = genai_errors.ServerError if code >= 500 else genai_errors.ClientError
    assert failures.describe(cls(code, body)) == expected


def test_a_spent_billing_cap_is_not_reported_as_a_busy_minute():
    # Seen live on 18 September 2026: the ground measurement came back as
    # "try again shortly" while the account's monthly cap was spent, so every
    # assessment ran without its model and the log said to wait.
    body = {
        "error": {
            "code": 429,
            "message": (
                "Your billing account has exceeded its monthly spending cap. "
                "Please go to AI Studio at https://ai.studio/billing"
            ),
            "status": "RESOURCE_EXHAUSTED",
        }
    }
    said = failures.describe(genai_errors.ClientError(429, body))
    assert said == failures.QUOTA_EXHAUSTED
    assert said != failures.RATE_LIMITED
    assert_clean(said)


def test_ordinary_throttling_is_still_a_busy_minute():
    body = {
        "error": {
            "code": 429,
            "message": "Too many requests, please slow down",
            "status": "RESOURCE_EXHAUSTED",
        }
    }
    assert (
        failures.describe(genai_errors.ClientError(429, body))
        == failures.RATE_LIMITED
    )


def test_a_dropped_connection_is_unavailable():
    exc = httpx.ConnectError("connect to 203.0.113.8:443 failed")
    assert failures.describe(exc) == failures.UNAVAILABLE


def test_an_unknown_exception_says_nothing_about_itself():
    exc = RuntimeError("key AIzaSyA-EXAMPLE-EXAMPLE-EXAMPLE-123 at 203.0.113.8")
    said = failures.describe(exc)
    assert said == failures.INTERNAL


def test_our_own_unavailable_message_survives():
    msg = "no credential path is configured for the panel"
    assert failures.describe(PanelUnavailableError(msg)) == msg


@pytest.mark.parametrize("text", [
    str(IP_RESTRICTED),
    "called from 203.0.113.8",
    "consumer projects/100000000001",
    "key AIzaSyA0123456789abcdefghijklmnopq",
    'Traceback (most recent call last):\n  File "/app/x.py", line 3',
])
def test_scrub_replaces_anything_with_a_provider_fingerprint(text):
    assert failures.scrub(text) == failures.INTERNAL


@pytest.mark.parametrize("text", [
    "timed out after 90s",
    "no ground photographs were supplied",
    "the parcel has no centroid, so no weather could be looked up",
    "NDVI peaked at 0.72 on 2026-08-10",
])
def test_scrub_leaves_our_own_sentences_alone(text):
    assert failures.scrub(text) == text


def test_scrub_passes_empty_values_through():
    assert failures.scrub(None) is None
    assert failures.scrub("") == ""


# --- through the real code paths ---------------------------------------------


class _Refused(Specialist):
    def __init__(self):  # no model is built
        self.name = "weather_specialist"
        self.timeout_s = 5

    async def _invoke(self, prompt, images):
        raise ip_restricted()


def test_a_refused_specialist_records_a_sentence():
    report = asyncio.run(_Refused().run("prompt"))
    assert report.ran is False
    assert report.error == failures.ACCESS_DENIED
    assert_clean(report.error)


def test_the_deterministic_reasoning_carries_no_provider_text(monkeypatch):
    # Even a report built somewhere that forgot describe(): the stored
    # reasoning is scrubbed on the way out.
    leaked = SpecialistReport(agent="weather_specialist", ran=False,
                              error=str(ip_restricted()))
    from tests.test_grounding import request

    result = panel_mod._deterministic(request(), [leaked], 0.0,
                                      "no specialist had evidence to work with")
    assert_clean(result.assessment.reasoning)
    assert_clean(result.degraded_reason)
    assert failures.INTERNAL in result.assessment.reasoning
