"""The live session's bounds and what the model is told.

No model is called here. What is worth testing is everything around it: that a
session cannot run for ever or cost without limit, that a frame nobody asked for
cannot arrive at any size, that the farmer is told before a session ends, and that
the instruction says the things the guidance exists for.
"""

from __future__ import annotations

import asyncio
import base64
from typing import Any

import pytest

from esurvey_ai import live


class FakeTransport:
    """A handset, scripted.

    [close_when] lets a test wait for the model's side to have done something before
    the handset hangs up. Without it the two directions race: the handset closing
    sets the session's done flag, which cancels the model reader mid-flight -- correct
    behaviour, since a farmer who has gone should not be waited for, but it makes
    anything about the downlink untestable.
    """

    def __init__(
        self,
        incoming: list[dict[str, Any]],
        close_when: Any = None,
    ) -> None:
        self._incoming = list(incoming)
        self._close_when = close_when
        self.audio_out: list[bytes] = []
        self.events: list[dict[str, Any]] = []

    async def receive(self) -> dict[str, Any] | None:
        if self._incoming:
            return self._incoming.pop(0)
        if self._close_when is not None:
            # Wait for what the test is watching for, then hang up.
            while not self._close_when(self):
                await asyncio.sleep(0)
            return {"type": "close"}
        return None

    async def send_audio(self, pcm: bytes) -> None:
        self.audio_out.append(pcm)

    async def send_event(self, event: dict[str, Any]) -> None:
        self.events.append(event)


class FakeSession:
    """The Live API, scripted."""

    def __init__(self, replies: list[Any] | None = None) -> None:
        self.audio_in: list[bytes] = []
        self.video_in: list[bytes] = []
        self.text_in: list[str] = []
        self.stream_ends = 0
        self._replies = replies or []

    async def send_realtime_input(
        self,
        *,
        audio: Any = None,
        video: Any = None,
        text: str | None = None,
        audio_stream_end: bool | None = None,
        **_: Any,
    ) -> None:
        if audio is not None:
            self.audio_in.append(audio.data)
        if video is not None:
            self.video_in.append(video.data)
        if text is not None:
            self.text_in.append(text)
        if audio_stream_end:
            self.stream_ends += 1

    async def receive(self):
        # Drained as it goes, like the real one: a live session's receive is a
        # single continuous stream, not a list that can be replayed. Yielding the
        # same replies again on a second call would count every chunk twice.
        while self._replies:
            yield self._replies.pop(0)
        # Then nothing, for ever, like a session waiting on a quiet farmer.
        await asyncio.Event().wait()


def b64(raw: bytes) -> str:
    return base64.b64encode(raw).decode("ascii")


@pytest.mark.asyncio
async def test_audio_reaches_the_model_and_is_counted() -> None:
    # Counted because live audio is billed by the minute, and a feature nobody
    # measures is a feature nobody can cap.
    transport = FakeTransport([
        {"type": "audio", "data": b64(b"\x01\x02" * 100)},
        {"type": "close"},
    ])
    session = FakeSession()
    stats = live.SessionStats()

    await live._pump(transport, session, stats, live.SessionLimits())

    assert len(session.audio_in) == 1
    assert stats.audio_chunks_in == 1
    assert stats.audio_bytes_in == 200
    assert stats.ended_because == "handset closed"


@pytest.mark.asyncio
async def test_an_oversized_frame_is_refused_rather_than_truncated() -> None:
    # Half a JPEG is not an image, and the model would describe whatever it made of
    # it -- which is worse than having no picture, because it sounds like an
    # observation.
    too_big = b"\xff\xd8" + b"x" * (live.MAX_FRAME_BYTES + 1)
    transport = FakeTransport([
        {"type": "frame", "data": b64(too_big)},
        {"type": "close"},
    ])
    session = FakeSession()
    stats = live.SessionStats()

    await live._pump(transport, session, stats, live.SessionLimits())

    assert session.video_in == []
    assert any(e["type"] == "frame_rejected" for e in transport.events)


@pytest.mark.asyncio
async def test_a_frame_within_the_budget_gets_through() -> None:
    frame = b"\xff\xd8" + b"x" * 20_000
    transport = FakeTransport([
        {"type": "frame", "data": b64(frame)},
        {"type": "close"},
    ])
    session = FakeSession()
    stats = live.SessionStats()

    await live._pump(transport, session, stats, live.SessionLimits())

    assert len(session.video_in) == 1
    assert stats.frames_in == 1


@pytest.mark.asyncio
async def test_rubbish_is_dropped_without_ending_the_session() -> None:
    # One bad chunk in a session is noise, not a fault. Ending the conversation over
    # it would have a farmer lose their guidance to a single corrupt packet.
    transport = FakeTransport([
        {"type": "audio", "data": "not base64 at all!!"},
        {"type": "audio", "data": b64(b"\x01\x02")},
        {"type": "close"},
    ])
    session = FakeSession()
    stats = live.SessionStats()

    await live._pump(transport, session, stats, live.SessionLimits())

    assert len(session.audio_in) == 1
    assert stats.ended_because == "handset closed"


@pytest.mark.asyncio
async def test_typed_text_is_redacted_on_the_way_through() -> None:
    # A farmer in a noisy place types instead of speaking, and what they type can
    # carry an identifier.
    transport = FakeTransport([
        {"type": "text", "text": "my id is 99900000002, what should I film?"},
        {"type": "close"},
    ])
    session = FakeSession()

    await live._pump(transport, session, live.SessionStats(), live.SessionLimits())

    assert session.text_in, "the text never reached the model"
    assert "99900000002" not in session.text_in[0]
    assert "what should I film" in session.text_in[0]


@pytest.mark.asyncio
async def test_a_session_that_runs_too_long_ends_and_says_so() -> None:
    # A farmer whose guidance stops mid-walk with no explanation concludes the app
    # is broken and starts it again, which begins a new session and the same wait.
    transport = FakeTransport([])  # never closes on its own
    session = FakeSession()
    stats = live.SessionStats()

    # The transport returning None would end it, so give it something that blocks.
    async def blocking_receive() -> dict[str, Any] | None:
        await asyncio.sleep(10)
        return None

    transport.receive = blocking_receive  # type: ignore[method-assign]

    await asyncio.wait_for(
        live._pump(
            transport, session, stats,
            live.SessionLimits(max_seconds=0, idle_seconds=999),
        ),
        timeout=5,
    )

    assert stats.ended_because == "session limit"
    assert any(
        e["type"] == "ended" and e["reason"] == "session limit"
        for e in transport.events
    )


@pytest.mark.asyncio
async def test_a_silent_session_is_let_go() -> None:
    # A farmer who has stopped talking and stopped walking is finished, whatever the
    # screen still shows.
    transport = FakeTransport([])
    session = FakeSession()
    stats = live.SessionStats()

    async def blocking_receive() -> dict[str, Any] | None:
        await asyncio.sleep(10)
        return None

    transport.receive = blocking_receive  # type: ignore[method-assign]

    await asyncio.wait_for(
        live._pump(
            transport, session, stats,
            live.SessionLimits(max_seconds=999, idle_seconds=0),
        ),
        timeout=5,
    )

    assert stats.ended_because == "idle"


@pytest.mark.asyncio
async def test_done_speaking_closes_the_farmers_turn() -> None:
    transport = FakeTransport([
        {"type": "audio", "data": b64(b"\x01\x02")},
        {"type": "done_speaking"},
        {"type": "close"},
    ])
    session = FakeSession()

    await live._pump(transport, session, live.SessionStats(), live.SessionLimits())

    assert session.stream_ends == 1


class Chunk:
    """One thing the Live API sends back."""

    def __init__(self, data: bytes | None = None, server_content: Any = None) -> None:
        self.data = data
        self.server_content = server_content


class Served:
    def __init__(self, text: str | None = None, turn_complete: bool = False) -> None:
        self.turn_complete = turn_complete
        self.model_turn = _Turn(text) if text else None


class _Turn:
    def __init__(self, text: str) -> None:
        self.parts = [_Part(text)]


class _Part:
    def __init__(self, text: str) -> None:
        self.text = text


@pytest.mark.asyncio
async def test_the_models_audio_reaches_the_farmer_and_is_counted() -> None:
    transport = FakeTransport([], close_when=lambda t: bool(t.audio_out))
    session = FakeSession(replies=[Chunk(data=b"\x10" * 480)])
    stats = live.SessionStats()

    await live._pump(transport, session, stats, live.SessionLimits())

    assert transport.audio_out == [b"\x10" * 480]
    assert stats.audio_bytes_out == 480


@pytest.mark.asyncio
async def test_what_the_model_said_is_also_sent_as_text() -> None:
    # A farmer in a noisy field may not have heard it, and a line on screen costs
    # nothing to deliver.
    transport = FakeTransport(
        [],
        close_when=lambda t: any(e["type"] == "turn_complete" for e in t.events),
    )
    session = FakeSession(replies=[
        Chunk(server_content=Served(text="शेंगा जवळून दाखवा", turn_complete=True)),
    ])

    await live._pump(transport, session, live.SessionStats(), live.SessionLimits())

    assert any(
        e["type"] == "said" and "शेंगा" in e["text"] for e in transport.events
    )
    assert any(e["type"] == "turn_complete" for e in transport.events)


def test_the_instruction_covers_the_cases_the_guidance_exists_for() -> None:
    # The agronomy is the whole point. A farmer filming a drought-struck field sees
    # a green canopy and films the green canopy, and the loss is in the pods.
    text = live.marathi_instruction()

    assert "शेंगा" in text, "the pod-set case is missing"
    assert "मुळे" in text, "the waterlogged-roots case is missing"
    assert "गारपिटी" in text, "the hail case is missing"
    assert "किडी" in text, "the pest case is missing"


def test_the_instruction_forbids_deciding_what_the_walk_requires() -> None:
    # Completeness is settled offline by the walk. A model that could extend the
    # requirement would make it depend on a network, and two farmers with the same
    # loss would owe different evidence.
    text = live.marathi_instruction()
    assert "ॲप ठरवते" in text, "the model is not told the app decides"
    assert "टक्केवारी तुम्ही ठरवत नाही" in text, (
        "the model is not told it does not set the damage percentage"
    )


def test_context_is_redacted_before_it_reaches_the_instruction() -> None:
    # Redacted here as well as in the Go service that supplies it: a boundary
    # applied at one end only is a boundary that holds until somebody adds a second
    # caller.
    text = live.marathi_instruction({
        "crop": "Soybean",
        "farmer_id": "99900000002",
        "note": "owner 123456789012",
    })
    assert "Soybean" in text
    assert "99900000002" not in text
    assert "123456789012" not in text


def test_the_live_model_is_the_one_that_was_verified() -> None:
    # Checked against the account's own model list rather than assumed from a
    # document: gemini-3.8-live advertises bidiGenerateContent, which is what a live
    # session needs.
    assert live.LIVE_MODEL == "gemini-3.8-live"


def test_the_sample_rates_are_what_the_api_works_in() -> None:
    assert live.INPUT_SAMPLE_RATE == 16000
    assert live.OUTPUT_SAMPLE_RATE == 24000
    assert live.INPUT_MIME == "audio/pcm;rate=16000"


# --- the session that only listens -------------------------------------------
#
# An officer speaks their question at a console, this writes it down, and the platform's
# own agent answers it -- the agent that reads and drafts and cannot approve, return or
# revise anything. That division is the whole point, so what is tested here is that this
# end cannot become the thing answering: it does not speak, it is not shown pictures,
# and it is told nothing about any claim.


@pytest.mark.asyncio
async def test_a_transcribe_only_session_sends_no_audio_to_the_officer() -> None:
    # The model is configured for text, so audio here is the model ignoring its
    # configuration -- which is exactly the case worth a second refusal. A mode the
    # model could override by answering out loud is a request, not a mode.
    transport = FakeTransport(
        [],
        close_when=lambda t: any(e["type"] == "said" for e in t.events),
    )
    session = FakeSession(replies=[
        Chunk(data=b"\x10" * 480),
        Chunk(server_content=Served(text="how many claims are waiting on me")),
    ])
    stats = live.SessionStats()

    await live._pump(
        transport, session, stats, live.SessionLimits(transcribe_only=True)
    )

    assert transport.audio_out == [], "the officer was spoken to"
    assert stats.audio_bytes_out == 0, "undelivered audio was counted anyway"


@pytest.mark.asyncio
async def test_a_transcribe_only_session_emits_what_it_heard_as_text() -> None:
    # The transcript is the entire product of the session: it goes to the same endpoint
    # the typed question goes to, so if this event does not arrive there is nothing to
    # ask.
    transport = FakeTransport(
        [],
        close_when=lambda t: any(e["type"] == "turn_complete" for e in t.events),
    )
    session = FakeSession(replies=[
        Chunk(server_content=Served(
            text="CLR-2026-496-00001 ची स्थिती काय आहे", turn_complete=True,
        )),
    ])

    await live._pump(
        transport, session, live.SessionStats(),
        live.SessionLimits(transcribe_only=True),
    )

    assert any(
        e["type"] == "said" and "CLR-2026-496-00001" in e["text"]
        for e in transport.events
    ), "the transcript never left the relay"


@pytest.mark.asyncio
async def test_a_transcribe_only_session_refuses_a_picture() -> None:
    # A model shown an image and asked for a transcript describes the image, which is
    # the model answering -- the one thing this mode exists to prevent.
    frame = b"\xff\xd8" + b"x" * 20_000
    transport = FakeTransport([
        {"type": "frame", "data": b64(frame)},
        {"type": "close"},
    ])
    session = FakeSession()
    stats = live.SessionStats()

    await live._pump(
        transport, session, stats, live.SessionLimits(transcribe_only=True)
    )

    assert session.video_in == [], "a frame reached a session that only listens"
    assert stats.frames_in == 0
    assert any(e["type"] == "frame_rejected" for e in transport.events)


@pytest.mark.asyncio
async def test_a_transcribe_only_session_still_carries_speech_to_the_model() -> None:
    # The listening half has to keep working, or the mode is merely a way of turning
    # the feature off.
    transport = FakeTransport([
        {"type": "audio", "data": b64(b"\x01\x02" * 100)},
        {"type": "done_speaking"},
        {"type": "close"},
    ])
    session = FakeSession()
    stats = live.SessionStats()

    await live._pump(
        transport, session, stats, live.SessionLimits(transcribe_only=True)
    )

    assert len(session.audio_in) == 1
    assert session.stream_ends == 1
    assert stats.audio_bytes_in == 200


@pytest.mark.asyncio
async def test_a_farmers_session_is_unchanged_and_still_speaks_back() -> None:
    # Stated beside the mode it is distinguished from. The farmer's walk is what this
    # relay was built for, and a default that quietly acquired the officer's
    # restrictions would take the voice out of a field with nobody noticing.
    transport = FakeTransport([], close_when=lambda t: bool(t.audio_out))
    session = FakeSession(replies=[Chunk(data=b"\x10" * 480)])
    stats = live.SessionStats()

    assert live.SessionLimits().transcribe_only is False

    await live._pump(transport, session, stats, live.SessionLimits())

    assert transport.audio_out == [b"\x10" * 480]
    assert stats.audio_bytes_out == 480


def test_the_transcription_instruction_asks_for_words_and_forbids_answering() -> None:
    text = live.transcription_instruction()

    assert "transcribe" in text.lower(), "the model is not told what its work is"
    assert "Do not answer the speaker." in text, (
        "nothing stops the model answering, which would make this a second "
        "assistant with none of the officer agent's limits"
    )
    assert "do not translate" in text.lower(), (
        "a claim reference spoken in English has to arrive in English"
    )
    assert "Marathi" in text and "English" in text


def test_a_transcribing_session_is_told_nothing_about_any_claim() -> None:
    # The officer console has a claim on screen and no business sending it here: the
    # session transcribes speech and the platform's agent, which is scoped to that
    # officer's own posting, is what knows about claims.
    assert live.session_context(
        {
            "crop": "Soybean",
            "calamity": "hail",
            "district": "सोलापूर",
            "farmer_id": "99900000002",
        },
        transcribe_only=True,
    ) == {}


def test_a_farmers_session_carries_the_crop_and_never_the_farmer() -> None:
    # The other side of the same list. A field a caller invents is a field that does not
    # travel, and farmer_id is the one that must never travel however it is spelled.
    got = live.session_context({
        "crop": "Soybean",
        "calamity": "hail",
        "ticket": "1780000000.480.abc",
        "mode": "transcribe",
        "farmer_id": "99900000002",
        "aadhaar": "123456789012",
    })

    assert got == {"crop": "Soybean", "calamity": "hail"}


@pytest.mark.asyncio
async def test_a_transcripts_own_spacing_survives_the_relay() -> None:
    # A transcript arrives in pieces and the console joins them end to end, so a
    # relay that trimmed each piece would deliver "how manyclaims" -- a question the
    # assistant cannot answer, blamed on the microphone.
    transport = FakeTransport(
        [],
        close_when=lambda t: len([e for e in t.events if e["type"] == "said"]) == 2,
    )
    session = FakeSession(replies=[
        Chunk(server_content=Served(text="how many claims")),
        Chunk(server_content=Served(text=" are waiting on me")),
    ])

    await live._pump(
        transport, session, live.SessionStats(),
        live.SessionLimits(transcribe_only=True),
    )

    said = "".join(e["text"] for e in transport.events if e["type"] == "said")
    assert said == "how many claims are waiting on me"


@pytest.mark.asyncio
async def test_a_piece_of_nothing_but_whitespace_is_not_sent_as_a_transcript() -> None:
    # The other half of keeping the spacing: an event whose whole content is a space
    # is noise on the wire and, on a farmer's screen, a line that blinks empty.
    transport = FakeTransport(
        [],
        close_when=lambda t: any(e["type"] == "turn_complete" for e in t.events),
    )
    session = FakeSession(replies=[
        Chunk(server_content=Served(text="   ")),
        Chunk(server_content=Served(turn_complete=True)),
    ])

    await live._pump(
        transport, session, live.SessionStats(),
        live.SessionLimits(transcribe_only=True),
    )

    assert [e for e in transport.events if e["type"] == "said"] == []


def test_the_transcribe_mode_is_named_the_same_in_both_processes() -> None:
    # The Go service puts this word in the query string and this reads it out. Pinned
    # because a rename on one side is a silent fall back to a talking farmer session,
    # which is the failure this mode was introduced to avoid.
    assert live.TRANSCRIBE_MODE == "transcribe"
