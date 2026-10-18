"""The WebSocket a handset actually connects to.

Thin on purpose: it turns frames on a socket into the small message vocabulary
`live.Transport` deals in, and nothing else. The session's behaviour -- what is
sent, what is bounded, what the model is told -- lives in live.py, where it can be
read without a socket in the way.
"""

from __future__ import annotations

import contextlib
import logging
from typing import Any

from fastapi import WebSocket, WebSocketDisconnect

from . import live

log = logging.getLogger(__name__)


class WebSocketTransport:
    """`live.Transport` over one FastAPI WebSocket.

    Audio goes as binary and events as text, which saves base64 on the half of the
    traffic that is continuous. A farmer's downlink is mostly speech, and base64
    would add a third to it for nothing.
    """

    def __init__(self, ws: WebSocket) -> None:
        self._ws = ws
        self._open = True

    async def receive(self) -> dict[str, Any] | None:
        if not self._open:
            return None
        try:
            message = await self._ws.receive()
        except (WebSocketDisconnect, RuntimeError):
            self._open = False
            return None

        if message.get("type") == "websocket.disconnect":
            self._open = False
            return None

        # Binary is always audio. Framing it as JSON with a base64 payload would
        # add a third to the continuous half of the traffic, which is the half that
        # matters on a 256 kbit link.
        if (raw := message.get("bytes")) is not None:
            return {"type": "audio", "data": _b64(raw)}

        text = message.get("text")
        if not text:
            return {"type": "noop"}
        try:
            import json

            parsed = json.loads(text)
        except ValueError:
            return {"type": "noop"}
        return parsed if isinstance(parsed, dict) else {"type": "noop"}

    async def send_audio(self, pcm: bytes) -> None:
        if not self._open:
            return
        try:
            await self._ws.send_bytes(pcm)
        except (WebSocketDisconnect, RuntimeError):
            self._open = False

    async def send_event(self, event: dict[str, Any]) -> None:
        if not self._open:
            return
        try:
            await self._ws.send_text(live.encode_json(event))
        except (WebSocketDisconnect, RuntimeError):
            self._open = False


def _b64(raw: bytes) -> str:
    import base64

    return base64.b64encode(raw).decode("ascii")


async def serve(
    ws: WebSocket,
    context: dict[str, Any] | None = None,
    max_seconds: int | None = None,
    transcribe_only: bool = False,
) -> None:
    """Accept a caller and hold the session.

    The context is whatever the Go service chose to pass about the claim being
    walked -- the crop, the calamity, the extent. It is redacted again inside
    live.py rather than trusted here, because a boundary applied at one end only is
    a boundary that holds until somebody adds a second caller.

    `transcribe_only` is an officer at a desk rather than a farmer in a field: their
    speech is written down and answered by the platform's own agent, so this end must
    not speak back and is told nothing about any claim. See live.SessionLimits.
    """
    await ws.accept()
    transport = WebSocketTransport(ws)
    try:
        # Bounded by what the platform allowed, where it said.
        #
        # A farmer with four minutes of their day left should get four, not this
        # service's own eight and then a refusal on the next session. The platform
        # knows the budget; this knows the conversation.
        limits = live.SessionLimits(
            max_seconds=max_seconds or live.MAX_SESSION_SECONDS,
            transcribe_only=transcribe_only,
        )
        stats = await live.relay(transport, context=context, limits=limits)
        log.info(
            "live ws closed: %s, %.0fs, %d frames",
            stats.ended_because,
            stats.seconds,
            stats.frames_in,
        )
    finally:
        # Already gone is the ordinary ending, not a failure.
        with contextlib.suppress(RuntimeError):
            await ws.close()
