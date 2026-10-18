"""HTTP surface for the assessment agent.

One endpoint that matters. The Go backend calls it synchronously and applies
its own guardrails to whatever comes back — this service is not trusted, it is
consulted.
"""

from __future__ import annotations

import logging
import time
from collections.abc import AsyncIterator
from contextlib import asynccontextmanager

from fastapi import Depends, FastAPI, HTTPException, WebSocket
from fastapi.responses import JSONResponse

from . import __version__, guardrails, live, live_ticket, live_webrtc, live_ws
from .agents.ground import GROUND_VERSION, measurer, prompt_for
from .agents.panel import PROMPT_VERSION, run_panel
from .agents.turn import run_turn
from .callers import require_caller
from .config import settings
from .failures import scrub
from .schemas import (
    AgentTurnRequest,
    AgentTurnResponse,
    AssessRequest,
    AssessResponse,
    GroundMeasurement,
    GroundMeasureRequest,
    GroundMeasureResponse,
    HealthResponse,
    SpecialistSummary,
)

logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s %(levelname)-8s %(name)s: %(message)s",
)
log = logging.getLogger("esurvey_ai")


@asynccontextmanager
async def lifespan(_: FastAPI) -> AsyncIterator[None]:
    log.info(
        "esurvey-ai %s starting — model=%s prompt=%s configured=%s",
        __version__,
        settings.model,
        PROMPT_VERSION,
        settings.configured,
    )
    if not settings.configured:
        # Warn but start. /healthz reports degraded, which is more useful to
        # an operator than a container that will not boot.
        log.warning(
            "no Gemini credential configured; assessment calls will fail. "
            "Set ESURVEY_AI_GOOGLE_API_KEY or ESURVEY_AI_USE_VERTEX."
        )
    yield


app = FastAPI(
    title="eSurvey AI",
    version=__version__,
    description="Crop loss assessment panel: four specialists and a synthesiser.",
    lifespan=lifespan,
)


@app.get("/healthz", response_model=HealthResponse)
async def healthz() -> HealthResponse:
    return HealthResponse(
        status="ok" if settings.configured else "degraded",
        model=settings.model,
        prompt_version=PROMPT_VERSION,
        gemini_reachable=None,
    )


@app.post(
    "/api/v1/ground-measure",
    response_model=GroundMeasureResponse,
    dependencies=[Depends(require_caller)],
)
async def ground_measure(req: GroundMeasureRequest) -> GroundMeasureResponse:
    """Measure one survey clip from its keyframes.

    The heaviest signal in the specification's fusion, and the one that was
    absent in production because the only implementation was a classical
    baseline the platform refused. It lives here rather than in the vision
    service because this is where the model credential is.

    Never raises on a model failure: the caller is told there is no
    measurement and reports the ground signal absent, which is the same path
    every other evidence source takes when it cannot answer.
    """
    started = time.monotonic()
    report = await measurer.run(
        prompt_for(req.crop_name, req.station_no, len(req.keyframes)),
        req.keyframes,
    )
    took = int((time.monotonic() - started) * 1000)

    if not report.ran or report.finding is None:
        log.warning("ground measurement did not run: %s", report.error)
        return GroundMeasureResponse(
            measured=False,
            failed=True,
            reason=scrub(report.error or "the model returned no measurement"),
            model=settings.model,
            prompt_version=GROUND_VERSION,
            latency_ms=took,
        )

    m = report.finding
    if not isinstance(m, GroundMeasurement):
        # The schema is locked, so this is a contract change rather than a bad
        # answer. Refused rather than coerced.
        log.error("ground measurement returned %s", type(m).__name__)
        return GroundMeasureResponse(
            measured=False,
            failed=True,
            reason="the model returned something that is not a measurement",
            model=settings.model,
            prompt_version=GROUND_VERSION,
            latency_ms=took,
        )

    if not m.crop_visible:
        # A finding about the footage, not a fault: the camera saw a path, a
        # bund or the sky. Scoring it would put a number on nothing.
        return GroundMeasureResponse(
            measured=False,
            reason="no crop is visible in the footage",
            measurement=m,
            model=settings.model,
            prompt_version=GROUND_VERSION,
            latency_ms=took,
        )

    log.info(
        "ground measured: angle=%.0f submerged=%.2f necrotic=%.2f quality=%.2f in %dms",
        m.stem_angle_degrees,
        m.submerged_fraction,
        m.necrotic_fraction,
        m.quality,
        took,
    )
    return GroundMeasureResponse(
        measured=True,
        measurement=m,
        model=settings.model,
        prompt_version=GROUND_VERSION,
        latency_ms=took,
    )


@app.post(
    "/api/v1/assess",
    response_model=AssessResponse,
    dependencies=[Depends(require_caller)],
)
async def assess(req: AssessRequest) -> AssessResponse:
    started = time.monotonic()

    # Refuse before spending a call when nothing could support a conclusion.
    refusal = guardrails.preflight(req, settings.min_observations)
    if refusal is not None:
        log.info(
            "%s refused pre-flight: %s",
            req.assessment_id,
            ",".join(refusal.guardrails_applied),
        )
        return AssessResponse(
            assessment_id=req.assessment_id,
            result=refusal,
            latency_ms=int((time.monotonic() - started) * 1000),
        )

    # The panel never raises. A specialist that fails is a recorded gap, and
    # a panel that cannot run at all degrades to a deterministic assessment
    # rather than to nothing — which is what lets an assessment be attempted
    # the moment an offline-filed report syncs, instead of only when every
    # upstream is healthy.
    panel = await run_panel(req)
    result = guardrails.postflight(panel.assessment, req)

    specialists = [
        SpecialistSummary(
            agent=r.agent,
            ran=r.ran,
            duration_ms=r.duration_ms,
            # Scrubbed at the boundary as well as at the source: this list is
            # rendered per specialist on the assessment screen.
            error=scrub(r.error),
            verdict=getattr(r.finding, "verdict", None) if r.finding else None,
            confidence=getattr(r.finding, "confidence", None) if r.finding else None,
            reasoning=getattr(r.finding, "reasoning", None) if r.finding else None,
            limitations=getattr(r.finding, "limitations", []) if r.finding else [],
        )
        for r in panel.reports
    ]

    log.info(
        "%s assessed: presence=%s damage=%s confidence=%.2f refused=%s "
        "fraud=%s guardrails=%s panel=%d/%d degraded=%s",
        req.assessment_id,
        result.crop_presence.value,
        f"{result.damage_percent:.2f}" if result.damage_percent is not None else "-",
        result.confidence,
        result.insufficient_evidence,
        ",".join(f.code for f in result.fraud_indicators) or "none",
        ",".join(result.guardrails_applied) or "none",
        sum(1 for r in panel.reports if r.ran),
        len(panel.reports),
        panel.degraded,
    )

    return AssessResponse(
        assessment_id=req.assessment_id,
        result=result,
        latency_ms=int((time.monotonic() - started) * 1000),
        specialists=specialists,
        degraded=panel.degraded,
        degraded_reason=scrub(panel.degraded_reason),
    )


@app.post(
    "/api/v1/agent/turn",
    response_model=AgentTurnResponse,
    dependencies=[Depends(require_caller)],
)
async def agent_turn(req: AgentTurnRequest) -> AgentTurnResponse:
    """One turn of an officer's conversation.

    The loop is the caller's: it owns the tools, and an officer's posting is what
    decides which claims those tools can reach. This owns the credential and the
    prompt, and answers with a thing to say or one tool to run.

    Everything on the way past is redacted -- the instruction, every message, every
    tool result -- because the whole history is resent on each turn and a result
    from three turns ago leaks exactly as well as a fresh one. That includes the
    text read out of a file the officer attached, and the file's name with it. The
    pixels of a photograph are not examined for what they depict; the officer is
    warned and decides, and every attachment is audited by the caller.
    """
    return await run_turn(req)


@app.post("/api/v1/live/webrtc/offer")
async def live_webrtc_offer(body: dict) -> dict:
    """Answer a handset's WebRTC offer and hold the session behind it.

    The same conversation as the WebSocket route, over UDP instead of TCP. A
    WebSocket will not deliver packet 4 until packet 3 arrives, so on a link that
    loses a few per cent one missing audio packet stalls everything behind it --
    silence, then a burst, then another stall, which a farmer reads as the app being
    broken. WebRTC lets the codec conceal the loss and carry on.

    Both routes stay. This one has an SDP exchange, an ICE negotiation and a media
    stack to go wrong, and it fails in ways that are hard to diagnose from a field: a
    symmetric NAT, a carrier blocking UDP, candidates that never gather. So a handset
    tries this and falls back to the socket rather than losing the assistant.

    One round trip: the answer comes back and the media flows over UDP, which is why
    nothing is held open here.
    """
    # A handset's credential is the platform's ticket, not the internal token: the
    # internal token authenticates the platform to this service and must never be on a
    # phone. So this route is not behind require_caller, and is behind the ticket.
    try:
        allowed = live_ticket.verify(str(body.get("ticket") or ""))
    except live_ticket.TicketError as exc:
        raise HTTPException(status_code=401, detail=str(exc)) from exc

    sdp = str(body.get("sdp") or "")
    sdp_type = str(body.get("type") or "offer")
    if not sdp:
        raise ValueError("no offer was sent")

    # An officer dictating rather than a farmer walking, said the same way the socket
    # route says it and honoured the same way: no voice back, no picture accepted, and
    # no claim described. Read here as well as there because the two transports must not
    # disagree about what a session may be -- a mode the socket enforces and this one
    # does not is a mode that lapses the day a console changes transport.
    transcribe_only = str(body.get("mode") or "") == live.TRANSCRIBE_MODE

    # The same fixed list as the WebSocket route, and it is the same list because both
    # routes read it from live.SESSION_CONTEXT_KEYS. What crosses the boundary to the
    # model is one decision, and it was previously written out twice.
    context = live.session_context(
        body.get("context") or {}, transcribe_only=transcribe_only
    )
    return await live_webrtc.offer(
        sdp,
        sdp_type,
        context or None,
        max_seconds=allowed or None,
        transcribe_only=transcribe_only,
    )


@app.websocket("/api/v1/live/session")
async def live_session(ws: WebSocket) -> None:
    """Two-way spoken guidance for a farmer in a field, or an officer dictating.

    Reached through the Go service, which authenticates the caller and decides
    whether the connection is good enough to be worth opening -- see the network
    probe there. This end holds the credential and the session.

    The claim's context arrives as query parameters rather than a body, because a
    WebSocket has no request body to read. Only what the Go service chose to send
    is here, and it is redacted again on the way to the model.

    # Why one path serves both callers

    `mode=transcribe` is an officer at a console whose spoken question is written down
    and then answered by the platform's own agent, which is the half of this that must
    not be a second brain: this end never speaks back, is given no claim, and is told
    that transcribing is the whole of its work. Anything else is a farmer in a field,
    which is what the rest of this route is about.

    Both are the same audio moving over the same socket with the same ticket and the
    same ceilings, so they are one route. A second relay would be a second pump loop,
    and the pump loop is where a mistake costs a farmer their guidance mid-walk.
    """
    # The platform's permission, checked before the socket is accepted.
    #
    # Without this the relay is an open door onto a per-minute model: an endpoint a
    # handset can reach is one anybody can reach. The ticket says somebody vouched for
    # may talk and for how long; it never says who.
    try:
        allowed = live_ticket.verify(ws.query_params.get("ticket", ""))
    except live_ticket.TicketError:
        # Closed before accepting, so nothing is spent on an unvouched connection.
        # 1008 is "policy violation", which is what this is.
        await ws.close(code=1008)
        return

    transcribe_only = ws.query_params.get("mode") == live.TRANSCRIBE_MODE

    # The ticket is not context, and neither is the mode, nor anything a future caller
    # adds by accident: an explicit list beats filtering after the fact. A transcribing
    # session gets none of it at all.
    context = live.session_context(
        ws.query_params, transcribe_only=transcribe_only
    )
    await live_ws.serve(
        ws,
        context or None,
        max_seconds=allowed or None,
        transcribe_only=transcribe_only,
    )


@app.exception_handler(ValueError)
async def value_error_handler(_, exc: ValueError) -> JSONResponse:
    return JSONResponse(status_code=400, content={"detail": str(exc)})


def run() -> None:
    import uvicorn

    uvicorn.run(
        "esurvey_ai.main:app",
        host=settings.host,
        port=settings.port,
        log_config=None,
    )


if __name__ == "__main__":
    run()
