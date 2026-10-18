"""Who may ask this service to do work.

# Why an internal service needs this at all

Nothing here was ever reachable from outside a host: the compose file binds it
to loopback, and the only caller is the platform API on the same machine. That
is a fact about one deployment, though, and not a property of the service. Put
the same containers on an orchestrated network -- which is where this is
going -- and anything able to resolve the service name can ask it to run Earth
Engine work against arbitrary geometry, on somebody's billing account.

"It is only reachable internally" is the assumption that has ended more
incidents than it has prevented, and it costs one header to stop relying on it.

# Why a shared secret rather than something better

Because the something better is mutual TLS, and a service mesh is a larger
change than this platform is ready for. A bearer token checked in constant time
is the control that fits today: it is not defence against an attacker who has
already read the environment of a container on the same host, and it is
complete defence against everything short of that -- a misconfigured network
policy, a second workload on the same cluster, a port accidentally published.

# Why an unset token disables the check

A laptop runs these services without any of this, and a rule that stops a
developer starting the stack is a rule they will delete rather than satisfy.
The platform API refuses to start in a hardened environment without the token
configured, so the permissive default cannot survive into production.
"""

from __future__ import annotations

import hmac

from fastapi import Header, HTTPException, status

from .config import settings


def require_caller(
    authorization: str | None = Header(default=None),
    x_api_key: str | None = Header(default=None),
) -> None:
    """Reject a request that does not carry the shared token.

    Applied to the working endpoints and deliberately not to /healthz or
    /readyz: an orchestrator polls those before it has any credentials, and a
    liveness probe that fails authentication restarts the container it was
    checking.
    """
    expected = settings.caller_token
    if not expected:
        return

    # Either header. X-API-Key is what the platform's satellite client has
    # always sent, and making it learn a second convention for one of the two
    # services it talks to would be a change with no security value.
    presented = ""
    if authorization and authorization.lower().startswith("bearer "):
        presented = authorization[len("bearer ") :].strip()
    elif x_api_key:
        presented = x_api_key.strip()

    # Constant time, so the comparison does not leak the token's prefix to
    # somebody able to time many attempts.
    if not hmac.compare_digest(presented, expected):
        raise HTTPException(
            status_code=status.HTTP_401_UNAUTHORIZED,
            detail="this service is called by the eSurvey platform API only",
            headers={"WWW-Authenticate": "Bearer"},
        )
