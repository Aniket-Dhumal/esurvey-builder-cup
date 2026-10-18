"""Two-way spoken guidance, relayed to the Live API.

# Why this is a relay and not a direct connection

Because the credential must not be on the handset. A phone that holds the model key
holds it for every other purpose too, and a key on ten thousand handsets is a key
that has leaked. So the phone talks to this service, and this service talks to
Google.

The relay earns its place twice over. It is where the session is bounded -- a
farmer who walks away with the app open stops costing money after a few minutes
rather than a few hours -- and it is where the visual channel is throttled
independently of the voice one.

# The shape of a session

    handset  ──audio frames (PCM 16 kHz)──▶  relay  ──▶  Live API
             ──JPEG, on demand only───────▶        ◀──  audio (PCM 24 kHz)
             ◀─audio + a little JSON──────

One WebSocket in each direction. Audio is the only thing that flows continuously;
frames are sent when the model asks for one or when the farmer presses the shutter,
which is the single largest saving available on a rural connection. A frame every
three seconds is 80 kbit/s and dominates the budget; a frame when it is actually
wanted is a few kilobits averaged out.

# Why the WebSocket is now the fallback rather than the transport

Because of what the picture above costs. Raw PCM at 16 kHz is 256 kbit/s going up and
the reply at 24 kHz is 384 coming down, so a socket needs something near 640 kbit/s
before a word is exchanged -- which is more than the link this feature exists for has
in total. Opus over WebRTC is nearer 44 each way. Loss is the other half of it: a
socket is TCP and will not deliver the fourth packet until the third arrives, so a few
per cent loss becomes silence, then a burst, then more silence.

So the handset tries WebRTC first -- see live_webrtc.py, which is where the codec and
the resampling live -- and falls back here. This stays because a symmetric NAT or a
carrier blocking UDP is a property of where a farmer is standing rather than of
whether they may talk, and because a socket always connects. Everything below is
written against `Transport` rather than against either one, which is what keeps the
two from drifting into different sessions.
"""

from __future__ import annotations

import asyncio
import base64
import contextlib
import json
import logging
import time
from collections.abc import Mapping
from dataclasses import dataclass, field
from typing import Any, Protocol

from .redaction import redact

log = logging.getLogger(__name__)

# The model that does the talking.
#
# Verified present on this account rather than assumed: gemini-3.8-live advertises
# bidiGenerateContent, which is what a live session needs.
LIVE_MODEL = "gemini-3.8-live"

# What the handset sends and what comes back.
#
# 16 kHz in and 24 kHz out is what the Live API works in. The handset resamples; the
# relay does not, because resampling here would mean decoding and re-encoding every
# chunk of every session for no gain in quality.
INPUT_SAMPLE_RATE = 16000
OUTPUT_SAMPLE_RATE = 24000
INPUT_MIME = f"audio/pcm;rate={INPUT_SAMPLE_RATE}"

# How long one session may run.
#
# Eight minutes. Long enough for a walk of four points with questions between them,
# short enough that a handset left in a pocket with the screen on costs minutes
# rather than hours. The farmer is told before it ends rather than cut off.
MAX_SESSION_SECONDS = 8 * 60

# How long a silent session is held open.
#
# A farmer who has stopped talking and stopped walking is finished, whatever the
# screen still shows. Ninety seconds is longer than a pause for thought and shorter
# than a forgotten session.
IDLE_TIMEOUT_SECONDS = 90

# The largest single frame accepted.
#
# The design budget is a 640x640 JPEG at quality 0.55, which is 25 to 35 KB. Double
# that is a generous ceiling; anything beyond it is a handset ignoring its own
# throttle, and accepting it would spend the farmer's bandwidth on one image.
MAX_FRAME_BYTES = 80 * 1024

# The largest audio chunk accepted, about a second at 16 kHz mono 16-bit.
MAX_AUDIO_BYTES = 64 * 1024

# The name of the mode in which this relay only listens.
#
# One word in a query string, because the Go service asks for it there and this is the
# only other place that reads it. Named rather than spelled twice: the two processes
# agree on this string the way they agree on the ticket's format, and a literal in two
# files is a literal that drifts.
TRANSCRIBE_MODE = "transcribe"

# What a session may be told about the claim being walked.
#
# An explicit set rather than a filter applied after the fact, because this is the list
# of things that cross the boundary to the model. A caller who adds a field to their
# request does not thereby add it to what leaves the platform; somebody has to come
# here and decide.
SESSION_CONTEXT_KEYS = frozenset(
    {"crop", "calamity", "season", "stage", "extent_ha", "district"}
)


class Transport(Protocol):
    """What a relay needs of its connection to the handset.

    Isolated so that replacing the WebSocket with WebRTC later is one class rather
    than a rewrite. Everything above this deals in messages, not in sockets.
    """

    async def receive(self) -> dict[str, Any] | None:
        """The next message from the handset, or None when it has gone."""

    async def send_audio(self, pcm: bytes) -> None:
        """Audio for the farmer to hear."""

    async def send_event(self, event: dict[str, Any]) -> None:
        """A small JSON message: a transcript, a request for a frame, a notice."""


@dataclass
class SessionLimits:
    """What bounds one session, so the caller can tighten it per farmer."""

    max_seconds: int = MAX_SESSION_SECONDS
    idle_seconds: int = IDLE_TIMEOUT_SECONDS

    # Whether this session may only listen.
    #
    # # Why a bound and not a separate relay
    #
    # Because it is a bound. A transcribe-only session is this same conversation with
    # things taken away -- it may not speak, it may not be shown a picture, it may not
    # be told which claim anybody is looking at -- and the alternative was a second
    # copy of the pump loop below, which is the one place in this file where getting it
    # wrong costs a farmer their guidance mid-walk.
    #
    # # Why it rides here rather than being a parameter
    #
    # Limits are what already reach every part of a session: `relay` takes them and
    # hands them to `_pump`, which is where the downlink has to be refused. A mode the
    # pump cannot see is a mode the pump cannot honour, and a mode honoured only by the
    # configuration sent to the model is a request rather than a guarantee.
    transcribe_only: bool = False


@dataclass
class SessionStats:
    """What the session cost, for the caller to record.

    Kept because live audio is billed by the minute and a feature nobody measures is
    a feature nobody can cap. Counted here rather than inferred from the model's
    invoice a month later.
    """

    started_at: float = field(default_factory=time.monotonic)
    audio_chunks_in: int = 0
    audio_bytes_in: int = 0
    frames_in: int = 0
    frame_bytes_in: int = 0
    audio_bytes_out: int = 0
    ended_because: str = "closed"

    @property
    def seconds(self) -> float:
        return time.monotonic() - self.started_at


def marathi_instruction(context: dict[str, Any] | None = None) -> str:
    """What the model is, in the session it is about to hold.

    The agronomy is the point. A farmer filming a drought-struck field sees a green
    canopy and films the green canopy, and the loss is in the pods -- fewer of them,
    smaller, empty. Nobody untrained films what is not there. So the model is told
    to watch for the cases where the visible crop and the actual loss disagree, and
    to ask for the next clip to show the part that carries the evidence.

    It asks for a *different* next clip. It does not add one: what makes a claim
    complete is settled offline by the walk, and a model that could extend the
    requirement would make completeness depend on a network.
    """
    base = """तुम्ही महाराष्ट्रातील शेतकऱ्यांना मदत करणारे कृषी सहाय्यक आहात. तुमचे
बोलणे स्पष्ट, नम्र आणि साध्या मराठीत असावे. एका वेळी एक वाक्य बोला — शेतकरी शेतात
उभा आहे आणि फोन हातात धरून आहे.

तुमचे काम दोन गोष्टींचे आहे:

१. शेतकऱ्याच्या प्रश्नांना थोडक्यात उत्तर द्या.

२. तो जे चित्रीकरण करत आहे त्यात नुकसान नीट दिसते का ते पाहा, आणि पुढच्या
   व्हिडिओत काय दाखवायचे ते सांगा.

दुसरे काम महत्त्वाचे आहे. दिसणारे पीक आणि खरे नुकसान वेगळे असू शकते:

  * दुष्काळात पीक वरून हिरवे दिसते, पण शेंगा कमी लागतात, लहान राहतात किंवा पोकळ
    असतात. वरचा हिरवा भाग चित्रित केल्याने नुकसान दिसत नाही. शेंगा फोडून आतले दाणे
    जवळून दाखवायला सांगा.
  * पाणी साचल्यास वरून काही दिसत नाही, पण मुळे कुजतात. एक रोप उपटून मुळे
    दाखवायला सांगा.
  * गारपिटीत पाने फाटतात, पण नुकसान खोडावरच्या जखमांत आणि गळून पडलेल्या फुलांत
    असते. जमिनीवर पडलेले दाखवायला सांगा.
  * किडीचा प्रादुर्भाव पानांच्या खालच्या बाजूला असतो.

महत्त्वाचे नियम:

  * किती व्हिडिओ काढायचे हे तुम्ही ठरवत नाही. ते ॲप ठरवते. तुम्ही फक्त पुढचा
    व्हिडिओ कशाचा काढावा ते सुचवता.
  * जे तुम्हाला दिसत नाही त्याबद्दल अंदाज बांधू नका. फोटो अस्पष्ट असेल तर तसे
    सांगा आणि जवळून घ्यायला सांगा.
  * नुकसानीची टक्केवारी तुम्ही ठरवत नाही. ती कार्यालय ठरवते.
  * तुम्हाला पीक स्पष्ट दिसत नसेल तर एक फोटो मागा."""

    if not context:
        return base

    cleaned, removed = redact(context)
    if removed:
        log.info("redacted from the live session context: %s", ",".join(removed))

    lines = [base, "", "या नोंदीची माहिती:"]
    for key, value in cleaned.items():
        lines.append(f"  {key}: {value}")
    return "\n".join(lines)


def transcription_instruction() -> str:
    """What the model is told when its only work is to write down what it heard.

    # Why the transcriber is not an assistant

    An officer's question is answered elsewhere, by the officer agent in the platform,
    whose entire guarantee is that it reads and drafts: it cannot approve, return,
    reassign or close a claim, and cannot change a figure. That guarantee lives in Go
    and is tested there. A model that both heard the question and answered it would be
    a second assistant with none of those limits, reached by speaking instead of
    typing -- so this one is given no room to answer at all. It writes down the words,
    and the words go to the same endpoint the typed ones go to.

    # Why it is told not to translate

    Because the officers this is for talk in both languages at once: a Marathi sentence
    with "approve", "kharif" and a claim reference in it. A reference spoken in English
    has to arrive in English or the agent cannot find the claim, and a Marathi village
    name transliterated into Latin letters is a village name nobody can look up.
    """
    return """You transcribe speech. That is the whole of your work.

Write down exactly what the speaker said, word for word, and output nothing else.
The speaker is a government officer in Maharashtra and will talk in Marathi, in
English, or in both within one sentence. Keep every word in the language and
script it was spoken in: do not translate, and do not rewrite a Marathi word in
Latin letters or an English word in Devanagari.

Claim references such as CLR-2026-496-00001, numbers, percentages, place names
and crop names are written exactly as they were spoken.

Do not answer the speaker. Do not greet them, explain, summarise, comment,
apologise, or offer to help. You are not in a conversation with anybody.

If you cannot make out what was said, output nothing."""


def session_context(
    params: Mapping[str, Any], *, transcribe_only: bool = False
) -> dict[str, str]:
    """What of a caller's request is allowed to reach the model.

    Nothing at all for a session that only listens. A transcriber has no claim: it is
    given a stream of speech and asked for the words in it, and the safest thing to
    tell it about the person talking is nothing. Told here rather than trusted to be
    absent, because the officer console and the farmer app reach the same socket and
    only one of them has a claim to describe.
    """
    if transcribe_only:
        return {}
    return {k: str(v) for k, v in params.items() if k in SESSION_CONTEXT_KEYS}


async def relay(
    transport: Transport,
    *,
    context: dict[str, Any] | None = None,
    limits: SessionLimits | None = None,
    model: str | None = None,
) -> SessionStats:
    """Hold one session until it ends, and say what it cost.

    Never raises for an ordinary ending -- the handset closing, the budget running
    out, the model dropping. A farmer standing in a field cannot act on an
    exception, and the walk must continue regardless: everything the app needs to
    file a claim works without any of this.
    """
    from .agents.base import export_sdk_credentials

    export_sdk_credentials()
    from google import genai
    from google.genai import types as genai_types

    limits = limits or SessionLimits()
    stats = SessionStats()

    if limits.transcribe_only:
        # Asked for text, and given no voice.
        #
        # A voice configured for a session that must never speak is a voice somebody
        # will one day wonder about, so there is none: the modality says the model
        # answers in words on a wire, and `_pump` drops any audio that arrives in spite
        # of it. Two refusals in series, because a mode the model could ignore is not a
        # mode.
        config = genai_types.LiveConnectConfig(
            response_modalities=["TEXT"],
            system_instruction=transcription_instruction(),
        )
        if context:
            # Dropped rather than redacted. Everywhere else in this file the context is
            # the point -- the crop and the calamity are what the guidance is about --
            # but a transcriber has no claim, and a caller who sent one has the wrong
            # mode rather than an over-full request.
            log.warning(
                "a transcribe-only session was given context; it was not sent"
            )
    else:
        config = genai_types.LiveConnectConfig(
            response_modalities=["AUDIO"],
            system_instruction=marathi_instruction(context),
            speech_config=genai_types.SpeechConfig(
                language_code="mr-IN",
                voice_config=genai_types.VoiceConfig(
                    prebuilt_voice_config=genai_types.PrebuiltVoiceConfig(
                        voice_name="Kore"
                    )
                ),
            ),
        )

    client = genai.Client()
    try:
        async with client.aio.live.connect(
            model=model or LIVE_MODEL, config=config
        ) as session:
            await transport.send_event({"type": "ready"})
            await _pump(transport, session, stats, limits)
    except asyncio.CancelledError:
        stats.ended_because = "cancelled"
        raise
    except Exception as exc:  # see the docstring
        stats.ended_because = "failed"
        log.warning("the live session failed: %s", exc)
        # Told in a form the app can act on: it falls back to the scripted
        # guidance, which needs no network at all.
        with _quiet():
            await transport.send_event(
                {"type": "unavailable", "reason": "the assistant stopped"}
            )

    log.info(
        "live session ended: %s after %.0fs, %d audio chunks in, %d frames in, "
        "%d bytes out",
        stats.ended_because,
        stats.seconds,
        stats.audio_chunks_in,
        stats.frames_in,
        stats.audio_bytes_out,
    )
    return stats


async def _pump(
    transport: Transport,
    session: Any,
    stats: SessionStats,
    limits: SessionLimits,
) -> None:
    """Move audio both ways until something ends it."""
    last_heard = time.monotonic()
    done = asyncio.Event()

    async def from_handset() -> None:
        nonlocal last_heard
        while not done.is_set():
            message = await transport.receive()
            if message is None:
                stats.ended_because = "handset closed"
                done.set()
                return

            kind = message.get("type")
            if kind == "audio":
                raw = _decode(message.get("data"), MAX_AUDIO_BYTES)
                if raw is None:
                    continue
                last_heard = time.monotonic()
                stats.audio_chunks_in += 1
                stats.audio_bytes_in += len(raw)
                await session.send_realtime_input(
                    audio=genai_blob(raw, INPUT_MIME)
                )
            elif kind == "frame":
                if limits.transcribe_only:
                    # There is nothing to look at in a session whose only output is
                    # the words that were said. A model shown a picture and asked for
                    # a transcript would describe it, which is the model answering --
                    # the one thing this mode exists to prevent.
                    await transport.send_event({
                        "type": "frame_rejected",
                        "reason": "this session only listens",
                    })
                    continue
                raw = _decode(message.get("data"), MAX_FRAME_BYTES)
                if raw is None:
                    # Refused rather than truncated. Half a JPEG is not an image
                    # and the model would describe whatever it made of it.
                    await transport.send_event(
                        {"type": "frame_rejected", "reason": "too large"}
                    )
                    continue
                last_heard = time.monotonic()
                stats.frames_in += 1
                stats.frame_bytes_in += len(raw)
                await session.send_realtime_input(
                    video=genai_blob(raw, "image/jpeg")
                )
            elif kind == "text":
                # Typed rather than spoken, which is what a farmer in a noisy
                # place or a quiet one does.
                said, _ = redact(str(message.get("text") or ""))
                if said:
                    last_heard = time.monotonic()
                    await session.send_realtime_input(text=said)
            elif kind == "done_speaking":
                await session.send_realtime_input(audio_stream_end=True)
            elif kind == "close":
                stats.ended_because = "handset closed"
                done.set()
                return

    async def from_model() -> None:
        while not done.is_set():
            async for chunk in session.receive():
                if done.is_set():
                    return
                data = getattr(chunk, "data", None)
                if data and limits.transcribe_only:
                    # Configured for text and sent audio anyway. Dropped without
                    # counting it, because nothing was delivered and a figure the
                    # caller is billed against should be a figure somebody heard.
                    log.warning("a transcribe-only session was sent audio; dropped it")
                elif data:
                    stats.audio_bytes_out += len(data)
                    await transport.send_audio(data)

                served = getattr(chunk, "server_content", None)
                if served is None:
                    continue

                # The model's own words, where it gave them. Sent so the app can
                # show what was said: a farmer in a noisy field may not have heard
                # it, and a line on screen costs nothing to deliver.
                text = _transcript(served)
                if text:
                    await transport.send_event({"type": "said", "text": text})

                if getattr(served, "turn_complete", False):
                    await transport.send_event({"type": "turn_complete"})

    async def watchdog() -> None:
        while not done.is_set():
            await asyncio.sleep(1)
            if stats.seconds > limits.max_seconds:
                stats.ended_because = "session limit"
                await _tell_and_end(transport, done, "session limit")
                return
            if time.monotonic() - last_heard > limits.idle_seconds:
                stats.ended_because = "idle"
                await _tell_and_end(transport, done, "idle")
                return

    tasks = [
        asyncio.create_task(from_handset()),
        asyncio.create_task(from_model()),
        asyncio.create_task(watchdog()),
    ]
    try:
        await done.wait()
    finally:
        for t in tasks:
            t.cancel()
        await asyncio.gather(*tasks, return_exceptions=True)


async def _tell_and_end(
    transport: Transport, done: asyncio.Event, why: str
) -> None:
    """End a session, having said so first.

    A farmer whose guidance stops mid-walk with no explanation concludes the app is
    broken and starts it again, which begins a new session and the same wait.
    """
    with _quiet():
        await transport.send_event({"type": "ended", "reason": why})
    done.set()


def genai_blob(data: bytes, mime: str) -> Any:
    from google.genai import types as genai_types

    return genai_types.Blob(data=data, mime_type=mime)


def _decode(value: Any, limit: int) -> bytes | None:
    """Base64 from the handset, bounded.

    None for anything unusable, which the caller treats as a dropped frame rather
    than an error: one bad chunk in a session is noise, not a fault.
    """
    if not isinstance(value, str):
        return None
    try:
        raw = base64.b64decode(value, validate=True)
    except Exception:
        return None
    if not raw or len(raw) > limit:
        return None
    return raw


def _transcript(served: Any) -> str:
    """Whatever text the model included with its audio.

    The piece's own spacing is kept, and only a piece that is nothing but whitespace
    is dropped. This used to be stripped, which was harmless while the text was a
    line on a farmer's screen and is not harmless now: a transcript arrives in pieces
    that the caller joins end to end, and stripping each one turns "how many" and
    " claims" into "how manyclaims".
    """
    out: list[str] = []
    turn = getattr(served, "model_turn", None)
    for part in (getattr(turn, "parts", None) or []) if turn else []:
        text = getattr(part, "text", None)
        if text:
            out.append(text)
    joined = "".join(out)
    return joined if joined.strip() else ""


# Swallowing a failure while shutting down.
#
# A send on a socket the handset has already closed raises, and raising inside the
# code that reports why a session ended replaces a clear ending with a stack trace.
def _quiet() -> Any:
    return contextlib.suppress(Exception)


def encode_json(event: dict[str, Any]) -> str:
    """An event as the handset reads it. Marathi is not escaped."""
    return json.dumps(event, ensure_ascii=False)


# Restated for anything importing this module rather than reading it.
__all__ = [
    "INPUT_MIME",
    "INPUT_SAMPLE_RATE",
    "LIVE_MODEL",
    "MAX_FRAME_BYTES",
    "OUTPUT_SAMPLE_RATE",
    "SESSION_CONTEXT_KEYS",
    "TRANSCRIBE_MODE",
    "SessionLimits",
    "SessionStats",
    "Transport",
    "marathi_instruction",
    "relay",
    "session_context",
    "transcription_instruction",
]
