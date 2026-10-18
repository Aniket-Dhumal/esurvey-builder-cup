"""Checking that the platform vouched for whoever is about to talk.

# Why the relay does not authenticate the farmer

Because it cannot, and should not learn how. This process holds the model credential
and knows nothing about sessions, jurisdictions or land records; teaching it to verify
platform tokens would put a second copy of the authentication rules in a service whose
job is audio, and a second copy is a copy that drifts.

# Why something is needed

Because the relay has to be reachable from a handset -- that is the entire point -- so
"it is only on an internal network" stops being true the moment the feature works. An
endpoint a phone can reach is an endpoint anybody can reach, and a WebSocket anybody can
hold open onto a per-minute model is a bill that arrives a month later with nobody's
name on it.

So the platform, which has already authenticated the farmer and checked their daily
budget, signs a short-lived permission. This verifies a signature and a deadline and
learns nothing else: somebody vouched for may talk, for this long.

# What is deliberately absent

The farmer. A ticket travels in a query string, and query strings go through logs,
proxies and crash reports. It carries a permission and an expiry, which is all this end
needs, and nothing that identifies a person.
"""

from __future__ import annotations

import base64
import hashlib
import hmac
import time

from .config import settings


class TicketError(Exception):
    """The ticket was missing, forged, or past its moment."""


def verify(ticket: str, *, now: float | None = None) -> int:
    """Return the seconds this ticket allows, or raise.

    The format is ``expiry.seconds.signature``, matching the Go issuer exactly. Not
    JSON, deliberately: it is read by two processes and nothing else, and a format with
    no parser has no parser bugs.
    """
    secret = settings.caller_token
    if not secret:
        # No shared secret configured, which is development. The platform is not
        # signing anything either, so demanding a signature here would break the only
        # setup where both ends are on one machine.
        return 0

    if not ticket:
        raise TicketError("no ticket")

    parts = ticket.split(".")
    if len(parts) != 3:
        raise TicketError("malformed ticket")

    payload = f"{parts[0]}.{parts[1]}"
    expected = base64.urlsafe_b64encode(
        hmac.new(secret.encode(), payload.encode(), hashlib.sha256).digest()
    ).rstrip(b"=").decode()

    # Constant time. A timing oracle on the prefix is a slow way to forge a ticket, and
    # slow is not the same as impossible.
    if not hmac.compare_digest(parts[2], expected):
        raise TicketError("that ticket was not issued by the platform")

    try:
        expiry = int(parts[0])
        seconds = int(parts[1])
    except ValueError as exc:
        raise TicketError("unreadable ticket") from exc

    if (now or time.time()) > expiry:
        raise TicketError("that ticket has expired")
    if seconds <= 0:
        raise TicketError("that ticket allows no time")

    return seconds
