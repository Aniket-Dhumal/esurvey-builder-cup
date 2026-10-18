"""What an officer is told when a specialist could not run.

# The leak this exists to close

A specialist that failed used to record ``str(exc)``, and that string travelled
into three places an officer reads: the assessment's reasoning, the degraded
reason, and the per-specialist list on the assessment screen. When the Gemini
key's IP restriction refused a call, every assessment on the platform was
stored with Google's raw error body in its reasoning, including the
calling server's public IP address, the Google Cloud project number, and the
key's restriction policy. That reasoning is a compensation record: it is
printed, handed to farmers, and read at appeals.

The full exception still reaches the log, which is where an operator debugging
the model integration needs it. What is stored is a sentence about what the
failure means for the assessment.

# Two layers, deliberately

``describe`` turns an exception into a sentence, by type and status code, and
never looks at the message text. That is the real fix, and it covers every
exception the SDK raises today.

``scrub`` is the backstop for text that did not come through ``describe``:
our own strings interpolated with something unexpected, a future code path
that forgets. It recognises the fingerprints of a provider error and replaces
the whole message rather than trying to cut the dangerous part out, because a
partly redacted provider error is still a provider error in front of a farmer.
"""

from __future__ import annotations

import re

# Stable, officer-readable descriptions. Each says what failed and what it
# means, never how the provider phrased it.
ACCESS_DENIED = (
    "the model service refused this deployment's credential; an operator "
    "needs to check the key or its access restrictions"
)
RATE_LIMITED = (
    "the model service is limiting requests from this deployment; the "
    "assessment can be run again shortly"
)
# A spending cap or an exhausted quota is not a busy minute. Told apart from
# ordinary throttling because the operator action is different and the wait is
# not "shortly": until somebody raises a limit, every assessment on this
# deployment is running without its model.
QUOTA_EXHAUSTED = (
    "the model service has stopped answering because this deployment's "
    "billing cap or quota is spent; an operator has to raise it, and until "
    "then nothing here is waiting on a busy minute"
)
UNAVAILABLE = "the model service was unavailable"
REJECTED = "the model service rejected the request"
UNREADABLE = "the model's answer could not be read"
INTERNAL = "an internal error prevented it"

_FINGERPRINTS = re.compile(
    r"""
      \b\d{1,3}(?:\.\d{1,3}){3}\b          # an IPv4 address
    | projects/\d+                          # a Google Cloud project number
    | AIza[0-9A-Za-z_\-]{20,}               # a Google API key
    | googleapis\.com
    | PERMISSION_DENIED | RESOURCE_EXHAUSTED | UNAUTHENTICATED
    | API_KEY_[A-Z_]+
    | callerIp | ErrorInfo | @type
    | \{\s*['"]error['"]\s*:               # a JSON or repr'd error body
    | Traceback\ \(most\ recent\ call\ last\)
    | File\ "[^"]+",\ line\ \d+
    """,
    re.VERBOSE,
)


def looks_internal(text: str | None) -> bool:
    """Whether a message carries the fingerprint of a provider or a stack."""
    return bool(text) and bool(_FINGERPRINTS.search(text))


def scrub(text: str | None, fallback: str = INTERNAL) -> str | None:
    """Replace a message that looks like a provider error.

    None and empty pass through, so callers can apply this to optional fields
    without branching.
    """
    if not text:
        return text
    return fallback if looks_internal(text) else text


def describe(exc: BaseException) -> str:
    """An officer-safe sentence for an exception.

    Reads the message in exactly one case: our own PanelUnavailableError,
    whose text we wrote -- and scrubbed even then.
    """
    if type(exc).__name__ == "PanelUnavailableError":
        return scrub(str(exc)) or INTERNAL

    code = _status_code(exc)
    if code in (401, 403):
        return ACCESS_DENIED
    if code == 429:
        # The provider uses one status for "too fast" and "out of money"; the
        # body is what tells them apart, and it is read here rather than
        # passed on, because the body carries project numbers and keys.
        blob = str(exc).lower()
        if any(
            w in blob
            for w in ("spending cap", "billing", "quota", "exceeded your current")
        ):
            return QUOTA_EXHAUSTED
        return RATE_LIMITED
    if code is not None and code >= 500:
        return UNAVAILABLE
    if code is not None and 400 <= code < 500:
        return REJECTED

    name = type(exc).__name__
    if name in (
        "ConnectError", "ConnectTimeout", "ReadTimeout", "RemoteProtocolError",
        "ClientConnectorError", "ServerDisconnectedError",
    ):
        return UNAVAILABLE
    # ConnectionError and TimeoutError are both OSErrors.
    if isinstance(exc, OSError):
        return UNAVAILABLE
    if isinstance(exc, ValueError | KeyError) or name in (
        "ValidationError", "JSONDecodeError",
    ):
        return UNREADABLE
    return INTERNAL


def _status_code(exc: BaseException) -> int | None:
    """The HTTP status an SDK error carries, whichever SDK raised it."""
    for attr in ("code", "status_code"):
        value = getattr(exc, attr, None)
        if isinstance(value, int) and 100 <= value < 600:
            return value
    response = getattr(exc, "response", None)
    value = getattr(response, "status_code", None)
    if isinstance(value, int) and 100 <= value < 600:
        return value
    return None
