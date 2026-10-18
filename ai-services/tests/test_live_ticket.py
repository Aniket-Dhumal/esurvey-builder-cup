"""The permission a handset carries to the relay.

Written against the Go issuer's format rather than against this module's own idea of it,
because that is the mistake that has already been made once in this feature: the officer
agent sent a header nothing read, and both ends' tests passed because each was checked
against its own assumption.

The fixtures below are therefore produced the way Go produces them -- hmac-sha256 over
"expiry.seconds", base64url without padding -- and if the issuer changes, these fail.
"""

from __future__ import annotations

import base64
import hashlib
import hmac
import time

import pytest

from esurvey_ai import live_ticket
from esurvey_ai.config import settings

SECRET = "a-shared-secret"


def issue(expiry: int, seconds: int, secret: str = SECRET) -> str:
    """A ticket exactly as the Go side builds one."""
    payload = f"{expiry}.{seconds}"
    sig = (
        base64.urlsafe_b64encode(
            hmac.new(secret.encode(), payload.encode(), hashlib.sha256).digest()
        )
        .rstrip(b"=")
        .decode()
    )
    return f"{payload}.{sig}"


@pytest.fixture(autouse=True)
def _with_secret(monkeypatch: pytest.MonkeyPatch):
    monkeypatch.setattr(settings, "caller_token", SECRET)
    yield


def test_a_ticket_the_platform_issued_is_accepted() -> None:
    allowed = live_ticket.verify(issue(int(time.time()) + 60, 480))
    assert allowed == 480


def test_the_allowance_is_what_the_platform_said() -> None:
    # A farmer with four minutes of their day left should get four, not the relay's own
    # eight and then a refusal on the next session.
    assert live_ticket.verify(issue(int(time.time()) + 60, 240)) == 240


def test_a_forged_ticket_is_refused() -> None:
    # The whole point. Without this the relay is an open door onto a per-minute model:
    # an endpoint a handset can reach is one anybody can reach.
    forged = issue(int(time.time()) + 60, 480, secret="not-the-secret")
    with pytest.raises(live_ticket.TicketError):
        live_ticket.verify(forged)


def test_a_tampered_allowance_is_refused() -> None:
    # Raising the number is the obvious attack: the signature covers it.
    good = issue(int(time.time()) + 60, 60)
    expiry, _, sig = good.split(".")
    with pytest.raises(live_ticket.TicketError):
        live_ticket.verify(f"{expiry}.99999.{sig}")


def test_an_expired_ticket_is_refused() -> None:
    # Two minutes is long enough to negotiate ICE on a slow link and short enough that
    # one captured from a log is worthless by the time anybody reads it.
    with pytest.raises(live_ticket.TicketError):
        live_ticket.verify(issue(int(time.time()) - 1, 480))


def test_a_ticket_expiring_this_second_is_still_good() -> None:
    # Whichever way the boundary falls it should be deliberate. A farmer whose ticket
    # expires as they connect has met it.
    now = int(time.time())
    assert live_ticket.verify(issue(now, 480), now=now) == 480


def test_a_missing_ticket_is_refused() -> None:
    with pytest.raises(live_ticket.TicketError):
        live_ticket.verify("")


def test_rubbish_is_refused_rather_than_crashing() -> None:
    for bad in ["x", "a.b", "a.b.c.d", "....", "notanumber.480.sig"]:
        with pytest.raises(live_ticket.TicketError):
            live_ticket.verify(bad)


def test_a_ticket_allowing_no_time_is_refused() -> None:
    # A zero allowance would open a session and end it immediately, which is a
    # connection, a model handshake and a puzzled farmer for nothing.
    with pytest.raises(live_ticket.TicketError):
        live_ticket.verify(issue(int(time.time()) + 60, 0))


def test_no_configured_secret_lets_development_work(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    # Both ends on one machine with nothing configured. The platform is not signing
    # either, so demanding a signature here would break the only setup where a
    # developer can run the whole thing.
    monkeypatch.setattr(settings, "caller_token", "")
    assert live_ticket.verify("") == 0
    assert live_ticket.verify("nonsense") == 0
