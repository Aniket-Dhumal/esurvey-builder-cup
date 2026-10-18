"""The WebRTC path, without a peer on the other end.

A real negotiation needs two stacks and a network, so what is tested here is
everything either side of it: that Opus frames become the PCM the Live API wants, that
the outgoing track keeps producing rather than stalling in a pause, and that the data
channel carries frames and events while audio stays off it.

The stall is the one worth being careful about. A track that stops producing frames is
a track the far end treats as failed, and the connection is then torn down in the
middle of a farmer drawing breath.
"""

from __future__ import annotations

import asyncio
import base64
import fractions
import json
import time
from typing import Any

import pytest
from av import AudioFrame

from esurvey_ai import live_webrtc


def frame_at(rate: int, samples: int = 960) -> AudioFrame:
    """One frame of silence at [rate], as a decoded Opus frame arrives."""
    f = AudioFrame(format="s16", layout="mono", samples=samples)
    f.planes[0].update(b"\x01\x02" * samples)
    f.sample_rate = rate
    f.pts = 0
    f.time_base = fractions.Fraction(1, rate)
    return f


class FakeTrack:
    """A track that yields a few frames and then ends, like a handset hanging up."""

    kind = "audio"

    def __init__(self, frames: list[AudioFrame]) -> None:
        self._frames = list(frames)

    async def recv(self) -> AudioFrame:
        if not self._frames:
            raise RuntimeError("track ended")
        return self._frames.pop(0)


class FakeChannel:
    """A data channel the test can read."""

    # Mirrors the WebRTC API's own spelling, which is what the code under test reads.
    readyState = "open"  # noqa: N815

    def __init__(self) -> None:
        self.sent: list[str] = []
        self._handlers: dict[str, Any] = {}

    def on(self, event: str):
        def register(fn: Any) -> Any:
            self._handlers[event] = fn
            return fn

        return register

    def send(self, text: str) -> None:
        self.sent.append(text)

    def deliver(self, message: Any) -> None:
        self._handlers["message"](message)


@pytest.mark.asyncio
async def test_a_48khz_opus_track_becomes_16khz_pcm() -> None:
    # The whole reason this relay exists. The Live API works in 16 kHz PCM and a phone
    # should not: doing the codec here is one implementation rather than one per
    # platform.
    got: list[bytes] = []

    async def collect(pcm: bytes) -> None:
        got.append(pcm)

    await live_webrtc.pump_inbound(
        FakeTrack([frame_at(48000), frame_at(48000)]), collect
    )

    assert got, "nothing was resampled"
    # 960 samples at 48 kHz is 20 ms, which is 320 samples at 16 kHz, 640 bytes. The
    # resampler buffers, so the total is what matters rather than the split -- but it
    # cannot exceed what two frames of 48 kHz audio contain once resampled, and if it
    # does, what is over is padding rather than sound. See the next test.
    assert 0 < sum(len(c) for c in got) <= 2 * 640


@pytest.mark.asyncio
async def test_a_track_that_ends_does_not_raise_at_the_caller() -> None:
    # A handset hanging up is the ordinary ending, not a fault. Raising here would
    # surface as an unhandled task error on a server holding many sessions.
    async def collect(_: bytes) -> None:
        return None

    await live_webrtc.pump_inbound(FakeTrack([]), collect)


@pytest.mark.asyncio
async def test_the_outgoing_track_pads_silence_rather_than_stalling() -> None:
    # The important one. A track that stops producing frames is treated as failed by
    # the far end, and the connection is torn down mid-pause.
    track = live_webrtc.PcmTrack()

    frame = await asyncio.wait_for(track.recv(), timeout=1)

    assert frame.samples == live_webrtc.OUT_FRAME_SAMPLES
    assert frame.sample_rate == live_webrtc.PCM_OUT_RATE


@pytest.mark.asyncio
async def test_the_outgoing_track_sends_what_the_model_said() -> None:
    track = live_webrtc.PcmTrack()
    want = live_webrtc.OUT_FRAME_SAMPLES * 2
    track.feed(b"\x11\x22" * live_webrtc.OUT_FRAME_SAMPLES)

    frame = await asyncio.wait_for(track.recv(), timeout=1)

    assert bytes(frame.planes[0])[:want] == b"\x11\x22" * live_webrtc.OUT_FRAME_SAMPLES


@pytest.mark.asyncio
async def test_timestamps_advance_so_the_far_end_can_order_frames() -> None:
    # Two frames with the same pts is a stream the receiver cannot sequence, and it
    # plays as a stutter that looks exactly like packet loss.
    track = live_webrtc.PcmTrack()
    first = await asyncio.wait_for(track.recv(), timeout=1)
    second = await asyncio.wait_for(track.recv(), timeout=1)

    assert second.pts == first.pts + live_webrtc.OUT_FRAME_SAMPLES


@pytest.mark.asyncio
async def test_a_frame_on_the_data_channel_reaches_the_session() -> None:
    # Frames go over the data channel, not the audio track, so that an image upload
    # cannot starve the voice: they are separate streams and one stalling does not
    # hold the other.
    transport = live_webrtc.WebRtcTransport()
    channel = FakeChannel()
    transport.attach_channel(channel)

    channel.deliver(b"\xff\xd8fake jpeg")

    message = await asyncio.wait_for(transport.receive(), timeout=1)
    assert message["type"] == "frame"
    assert base64.b64decode(message["data"]) == b"\xff\xd8fake jpeg"


@pytest.mark.asyncio
async def test_bytes_on_the_channel_are_a_frame_and_never_audio() -> None:
    # Audio has its own track. Anything binary on the data channel is an image, and
    # treating it as audio would feed JPEG bytes to the model as sound.
    transport = live_webrtc.WebRtcTransport()
    channel = FakeChannel()
    transport.attach_channel(channel)
    channel.deliver(b"\x00\x01\x02")

    message = await asyncio.wait_for(transport.receive(), timeout=1)
    assert message["type"] == "frame"


@pytest.mark.asyncio
async def test_typed_text_on_the_channel_is_passed_through() -> None:
    transport = live_webrtc.WebRtcTransport()
    channel = FakeChannel()
    transport.attach_channel(channel)

    channel.deliver(json.dumps({"type": "text", "text": "किती नुकसान?"}))

    message = await asyncio.wait_for(transport.receive(), timeout=1)
    assert message["text"] == "किती नुकसान?"


@pytest.mark.asyncio
async def test_rubbish_on_the_channel_is_dropped_not_forwarded() -> None:
    transport = live_webrtc.WebRtcTransport()
    channel = FakeChannel()
    transport.attach_channel(channel)

    channel.deliver("not json at all")
    transport.push({"type": "close"})

    message = await asyncio.wait_for(transport.receive(), timeout=1)
    assert message["type"] == "close", "the rubbish was forwarded"


@pytest.mark.asyncio
async def test_an_event_before_the_channel_opens_is_dropped_quietly() -> None:
    # An event is a courtesy -- a transcript, a notice -- and the audio is the
    # substance. Losing one must not end a session.
    transport = live_webrtc.WebRtcTransport()
    await transport.send_event({"type": "ready"})


@pytest.mark.asyncio
async def test_an_event_on_a_closed_channel_is_dropped_quietly() -> None:
    transport = live_webrtc.WebRtcTransport()
    channel = FakeChannel()
    channel.readyState = "closed"
    transport.attach_channel(channel)

    await transport.send_event({"type": "ready"})

    assert channel.sent == []


@pytest.mark.asyncio
async def test_closing_ends_the_session_the_relay_is_holding() -> None:
    # The relay waits on receive(); None is how it is told the handset has gone.
    transport = live_webrtc.WebRtcTransport()
    transport.close()

    assert await asyncio.wait_for(transport.receive(), timeout=1) is None


@pytest.mark.asyncio
async def test_the_models_audio_never_touches_the_data_channel() -> None:
    # It goes to the track. Sending 24 kHz PCM over a data channel would be TCP-like
    # ordering on the one stream that must not have it, which is the whole reason this
    # path exists.
    transport = live_webrtc.WebRtcTransport()
    channel = FakeChannel()
    transport.attach_channel(channel)

    await transport.send_audio(b"\x01" * 960)

    assert channel.sent == []


def test_the_frame_size_is_opus_own() -> None:
    # Twenty milliseconds. Anything else makes the encoder repacketise, which costs
    # CPU on a server holding many sessions for no gain in quality.
    assert live_webrtc.OUT_FRAME_SAMPLES == live_webrtc.PCM_OUT_RATE // 50
    assert live_webrtc.PCM_IN_RATE == 16000
    assert live_webrtc.PCM_OUT_RATE == 24000


class FakeWebSocket:
    """A FastAPI WebSocket, scripted, for comparing the two transports' vocabularies."""

    def __init__(self, messages: list[dict[str, Any]]) -> None:
        self._messages = list(messages)
        self.sent_bytes: list[bytes] = []
        self.sent_text: list[str] = []

    async def receive(self) -> dict[str, Any]:
        if self._messages:
            return self._messages.pop(0)
        return {"type": "websocket.disconnect"}

    async def send_bytes(self, raw: bytes) -> None:
        self.sent_bytes.append(raw)

    async def send_text(self, text: str) -> None:
        self.sent_text.append(text)


@pytest.mark.asyncio
async def test_no_alignment_padding_reaches_the_model_as_audio() -> None:
    # A resampled plane is allocated with alignment padding: 320 samples of 16-bit mono
    # is 640 bytes of sound in a 768-byte buffer. Handing the plane over whole sent the
    # model 128 bytes of uninitialised memory every twenty milliseconds, which is a
    # click inside every syllable and a fifth of the uplink spent producing it.
    got: list[bytes] = []

    async def collect(pcm: bytes) -> None:
        got.append(pcm)

    await live_webrtc.pump_inbound(
        FakeTrack([frame_at(48000) for _ in range(4)]), collect
    )

    assert got
    # The frames are filled with 0x01 0x02 throughout, so a constant signal comes back
    # as the same two bytes. Anything else in here is padding.
    assert set(b"".join(got)) <= {1, 2}
    # And four 20 ms frames cannot resample to more than four 20 ms frames.
    assert sum(len(c) for c in got) <= 4 * 640


@pytest.mark.asyncio
async def test_the_outgoing_track_is_one_the_media_stack_will_actually_send() -> None:
    # The bug this exists to catch was silent in every possible sense. addTrack accepts
    # anything with a `kind`, and a sender given something that is not one of aiortc's
    # own tracks keeps the object as its kind and its track as None: the negotiation
    # succeeds, the farmer is heard, the model answers, and not one packet of the answer
    # is sent for the length of the session.
    from aiortc import RTCPeerConnection

    pc = RTCPeerConnection()
    track = live_webrtc.PcmTrack()
    try:
        sender = pc.addTrack(track)

        assert sender.track is track, "the sender was given no track to read"
        assert sender.kind == "audio"
    finally:
        await pc.close()


@pytest.mark.asyncio
async def test_a_finished_track_ends_rather_than_padding_silence_for_ever() -> None:
    # Silence while the farmer is listening is right; silence after the conversation is
    # over is a sender left on the air. Ending the track is what takes it off.
    from aiortc.mediastreams import MediaStreamError

    track = live_webrtc.PcmTrack()
    track.feed(b"\x11\x22" * live_webrtc.OUT_FRAME_SAMPLES)
    track.stop()

    # What was already said is still sent.
    await asyncio.wait_for(track.recv(), timeout=1)

    with pytest.raises(MediaStreamError):
        await asyncio.wait_for(track.recv(), timeout=1)


@pytest.mark.asyncio
async def test_events_said_before_the_channel_opens_arrive_when_it_does() -> None:
    # There is a second or two between answering the offer and the channel opening --
    # ICE, then DTLS, then SCTP -- and the session starts talking to the model straight
    # away. `ready`, and `unavailable` if the model refused us on the spot, both happen
    # inside that gap, and a handset that never heard the second one waits for a session
    # that has already failed.
    transport = live_webrtc.WebRtcTransport()
    await transport.send_event({"type": "ready"})
    await transport.send_event({"type": "unavailable", "reason": "no"})

    channel = FakeChannel()
    transport.attach_channel(channel)

    assert [json.loads(s)["type"] for s in channel.sent] == ["ready", "unavailable"]


@pytest.mark.asyncio
async def test_a_channel_that_never_opens_cannot_fill_memory_with_events() -> None:
    transport = live_webrtc.WebRtcTransport()
    for _ in range(live_webrtc.MAX_PENDING_EVENTS + 20):
        await transport.send_event({"type": "said", "text": "..."})

    channel = FakeChannel()
    transport.attach_channel(channel)

    assert len(channel.sent) == live_webrtc.MAX_PENDING_EVENTS


# --- the vocabulary, on both transports -------------------------------------------

# What live._pump acts on. Written out here rather than derived, because the point of
# the test below is that a message kind cannot be added to one transport alone.
HANDSET_VOCABULARY = frozenset({"audio", "frame", "text", "done_speaking", "close"})


@pytest.mark.asyncio
async def test_the_webrtc_path_understands_every_kind_the_socket_does() -> None:
    from esurvey_ai import live_ws

    socket = live_ws.WebSocketTransport(
        FakeWebSocket([
            {"type": "websocket.receive", "bytes": b"\x01\x02"},
            {"type": "websocket.receive", "text": json.dumps(
                {"type": "frame", "data": ""}
            )},
            {"type": "websocket.receive", "text": json.dumps(
                {"type": "text", "text": "hello"}
            )},
            {"type": "websocket.receive", "text": json.dumps(
                {"type": "done_speaking"}
            )},
            {"type": "websocket.receive", "text": json.dumps({"type": "close"})},
        ])
    )
    over_socket = {
        (await asyncio.wait_for(socket.receive(), timeout=1))["type"]
        for _ in range(5)
    }

    rtc = live_webrtc.WebRtcTransport()
    channel = FakeChannel()
    rtc.attach_channel(channel)
    # Audio arrives as a media track and is pushed in by pump_inbound; everything else
    # arrives on the data channel, where bytes are a frame and text is JSON.
    rtc.push({"type": "audio", "data": base64.b64encode(b"\x01\x02").decode()})
    channel.deliver(b"\xff\xd8fake jpeg")
    for kind in ("text", "done_speaking", "close"):
        channel.deliver(json.dumps({"type": kind, "text": "hello"}))
    over_rtc = {
        (await asyncio.wait_for(rtc.receive(), timeout=1))["type"] for _ in range(5)
    }

    assert over_socket == HANDSET_VOCABULARY
    assert over_rtc == HANDSET_VOCABULARY


@pytest.mark.asyncio
async def test_binary_is_audio_on_the_socket_and_a_frame_over_webrtc() -> None:
    # The one asymmetry, and it is deliberate rather than an oversight. On the socket
    # the continuous half of the traffic is audio and base64 would add a third to it.
    # Over WebRTC audio has a track of its own and never reaches the channel, so the
    # only thing binary can be there is an image.
    from esurvey_ai import live_ws

    socket = live_ws.WebSocketTransport(
        FakeWebSocket([{"type": "websocket.receive", "bytes": b"\x01\x02"}])
    )
    assert (await socket.receive())["type"] == "audio"

    rtc = live_webrtc.WebRtcTransport()
    channel = FakeChannel()
    rtc.attach_channel(channel)
    channel.deliver(b"\x01\x02")
    assert (await rtc.receive())["type"] == "frame"


# --- Opus, explicitly -------------------------------------------------------------

# What every line of an SDP ends with, named because a test that writes it inline reads
# as though the string were broken.
CRLF = "\r\n"

# An answer shaped like the one aiortc composes, down to the line order.
ANSWER_SDP = CRLF.join([
    "v=0",
    "o=- 1 1 IN IP4 0.0.0.0",
    "s=-",
    "t=0 0",
    "m=audio 9 UDP/TLS/RTP/SAVPF 96",
    "c=IN IP4 0.0.0.0",
    "a=sendrecv",
    "a=mid:0",
    "a=rtpmap:96 opus/48000/2",
    "a=fmtp:96 minptime=10",
    "m=application 9 UDP/DTLS/SCTP webrtc-datachannel",
    "c=IN IP4 0.0.0.0",
    "a=mid:1",
    "",
])


def test_the_answer_asks_the_handset_for_speech_grade_opus() -> None:
    # Twenty-four kilobits of mono, with in-band FEC, on a link that has a quarter of a
    # megabit for everything. Left unsaid, a handset encodes at whatever its own default
    # is, and the uplink is the half of a cellular link with nothing to spare.
    out = live_webrtc.constrain_opus_sdp(ANSWER_SDP)

    assert f"maxaveragebitrate={live_webrtc.OPUS_BITRATE}" in out
    assert f"maxplaybackrate={live_webrtc.PCM_IN_RATE}" in out
    assert "stereo=0" in out
    assert "sprop-stereo=0" in out
    assert "useinbandfec=1" in out
    assert f"b=AS:{live_webrtc.OPUS_SESSION_KBPS}" in out
    assert f"a=ptime:{live_webrtc.OPUS_PTIME_MS}" in out


def test_the_bandwidth_line_sits_where_the_grammar_wants_it() -> None:
    # After the connection line and before the attributes, which is the order RFC 4566
    # gives and which some stacks refuse a description for getting wrong.
    lines = live_webrtc.constrain_opus_sdp(ANSWER_SDP).split("\r\n")
    at = lines.index(f"b=AS:{live_webrtc.OPUS_SESSION_KBPS}")

    assert lines[at - 1].startswith("c=")
    assert lines[at + 1].startswith("a=")


def test_the_handsets_own_opus_parameters_are_replaced_not_duplicated() -> None:
    # Two fmtp lines for one payload type is a description whose meaning depends on
    # which one the far end reads first.
    out = live_webrtc.constrain_opus_sdp(ANSWER_SDP)

    assert len([ln for ln in out.split("\r\n") if ln.startswith("a=fmtp:96")]) == 1
    assert "minptime=10" not in out


def test_the_wire_cost_covers_the_payload_and_its_envelope() -> None:
    # The platform's bandwidth floor is twice OPUS_WIRE_KBPS, so a payload raised here
    # without raising that would quietly put the gate below what a session costs.
    assert live_webrtc.OPUS_WIRE_KBPS >= live_webrtc.OPUS_BITRATE // 1000 + 20


def test_an_answer_that_is_not_opus_is_left_exactly_as_it_was() -> None:
    # This adds constraints to Opus and must not touch a description it does not
    # understand. A negotiation that stops working is a farmer back on the socket.
    telephony = CRLF.join([
        "v=0",
        "m=audio 9 UDP/TLS/RTP/SAVPF 0",
        "c=IN IP4 0.0.0.0",
        "a=rtpmap:0 PCMU/8000",
        "",
    ])

    assert live_webrtc.constrain_opus_sdp(telephony) == telephony


def test_a_description_with_no_audio_is_left_exactly_as_it_was() -> None:
    # This adds constraints. It must never be the reason a negotiation stops working,
    # because a negotiation that stops working is a farmer back on the socket.
    only_data = "\r\n".join([
        "v=0",
        "m=application 9 UDP/DTLS/SCTP webrtc-datachannel",
        "c=IN IP4 0.0.0.0",
        "",
    ])

    assert live_webrtc.constrain_opus_sdp(only_data) == only_data


def test_the_outgoing_opus_costs_what_the_link_can_afford() -> None:
    # aiortc encodes at 96 kbit/s stereo, which is a music setting: measured on
    # speech-like input it produces about 76 kbit/s of payload, and the farm has 256
    # kbit/s for everything. Constrained, the same input produces about 23.
    import math
    import struct

    import aiortc.codecs

    live_webrtc.constrain_opus_encoder()
    encoder = aiortc.codecs.OpusEncoder()

    total = 0
    packets = 0
    # A tone with noise on it, because a pure tone is not what a farmer sounds like and
    # Opus spends a different number of bits on the two. Deterministic, so the figure
    # this asserts is the same figure on every machine.
    seed = 12345

    def noise() -> int:
        nonlocal seed
        seed = (1103515245 * seed + 12345) % (1 << 31)
        return seed % 4000 - 2000

    for i in range(100):
        pcm = b"".join(
            struct.pack(
                "<h",
                int(6000 * math.sin((i * live_webrtc.OUT_FRAME_SAMPLES + k) / 9.0))
                + noise(),
            )
            for k in range(live_webrtc.OUT_FRAME_SAMPLES)
        )
        frame = AudioFrame(
            format="s16", layout="mono", samples=live_webrtc.OUT_FRAME_SAMPLES
        )
        frame.planes[0].update(pcm)
        frame.sample_rate = live_webrtc.PCM_OUT_RATE
        frame.pts = i * live_webrtc.OUT_FRAME_SAMPLES
        frame.time_base = fractions.Fraction(1, live_webrtc.PCM_OUT_RATE)
        payloads, _ = encoder.encode(frame)
        total += sum(len(p) for p in payloads)
        packets += len(payloads)

    assert packets > 90, "the model's voice did not encode at all"
    kbps = total * 8 / (packets * live_webrtc.OPUS_PTIME_MS / 1000) / 1000
    assert kbps <= 32, f"{kbps:.0f} kbit/s is more than this link can carry"


# --- the whole path, over a real peer connection -----------------------------------


@pytest.mark.asyncio
async def test_a_conversation_crosses_a_real_peer_connection_both_ways(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    """The only test here that proves the path rather than a piece of it.

    Two aiortc stacks on one machine, which is a real SDP exchange, a real ICE
    negotiation, real DTLS and real Opus -- everything except the model, which is
    replaced by a relay that says one thing and listens. Worth the second it takes:
    every piece below had a passing test while the assistant was silent over this
    transport for the length of every session, because what was missing was in none of
    the pieces.
    """
    from aiortc import RTCPeerConnection, RTCSessionDescription
    from aiortc.mediastreams import MediaStreamTrack

    from esurvey_ai import live

    heard: list[bytes] = []
    spoke = asyncio.Event()

    async def fake_relay(transport: Any, **_: Any) -> Any:
        await transport.send_event({"type": "ready"})
        await transport.send_audio(b"\x10\x20" * (live_webrtc.PCM_OUT_RATE // 5))
        spoke.set()
        while True:
            message = await transport.receive()
            if message is None:
                return live.SessionStats()
            if message.get("type") == "audio":
                heard.append(base64.b64decode(message["data"]))

    monkeypatch.setattr(live, "relay", fake_relay)

    class Microphone(MediaStreamTrack):
        """Twenty milliseconds of the farmer at a time, at the rate a phone captures."""

        kind = "audio"

        def __init__(self) -> None:
            super().__init__()
            self._pts = 0

        async def recv(self) -> AudioFrame:
            await asyncio.sleep(0.02)
            frame = frame_at(16000, samples=320)
            frame.pts = self._pts
            self._pts += 320
            return frame

    handset = RTCPeerConnection()
    # Held so the reader is not collected mid-conversation, the same reason the relay
    # holds its own tasks.
    listening: list[asyncio.Task[None]] = []
    events: list[str] = []
    voice: list[AudioFrame] = []
    a_voice = asyncio.Event()

    channel = handset.createDataChannel("events")

    @channel.on("message")
    def _on_message(message: Any) -> None:
        events.append(message)

    handset.addTrack(Microphone())

    @handset.on("track")
    def _on_track(track: Any) -> None:
        async def listen() -> None:
            while True:
                try:
                    voice.append(await track.recv())
                except Exception:
                    return
                a_voice.set()

        listening.append(asyncio.ensure_future(listen()))

    try:
        await handset.setLocalDescription(await handset.createOffer())
        answer = await asyncio.wait_for(
            live_webrtc.offer(
                handset.localDescription.sdp, handset.localDescription.type
            ),
            timeout=10,
        )

        # Opus and nothing else: G.711 would be 64 kbit/s of payload for narrowband
        # telephone audio, which costs nearly three times what Opus costs.
        audio_line = next(
            ln
            for ln in answer["sdp"].split("\r\n")
            if ln.startswith("m=audio")
        )
        assert len(audio_line.split()[3:]) == 1, audio_line
        assert "opus/48000/2" in answer["sdp"]

        await handset.setRemoteDescription(
            RTCSessionDescription(sdp=answer["sdp"], type=answer["type"])
        )

        # The assistant's voice reaches the handset, which is what was broken.
        await asyncio.wait_for(a_voice.wait(), timeout=15)
        assert voice[0].sample_rate == 48000, "Opus is always coded at 48 kHz"

        # And the farmer's reaches the model, at the rate the Live API works in.
        await asyncio.wait_for(spoke.wait(), timeout=10)
        for _ in range(80):
            if len(heard) >= 3:
                break
            await asyncio.sleep(0.05)
        assert len(heard) >= 3, "the farmer was not heard"
        # 20 ms of 16 kHz mono 16-bit is 640 bytes. Anything else is padding or a rate
        # the model was not promised.
        assert {len(chunk) for chunk in heard[1:]} == {640}

        # An event said before the channel opened still arrived.
        assert json.loads(events[0])["type"] == "ready"
    finally:
        await handset.close()
        # Let the relay notice and the offer's own tasks wind up, so the next test does
        # not start with a peer connection still tearing down.
        for _ in range(20):
            await asyncio.sleep(0.05)


# --- the ticket, on the route that answers an offer --------------------------------

TICKET_SECRET = "a-shared-secret"


def issue_ticket(expiry: int, seconds: int, secret: str = TICKET_SECRET) -> str:
    """A ticket exactly as the Go issuer builds one. See test_live_ticket."""
    import base64 as b64mod
    import hashlib
    import hmac

    payload = f"{expiry}.{seconds}"
    signature = (
        b64mod.urlsafe_b64encode(
            hmac.new(secret.encode(), payload.encode(), hashlib.sha256).digest()
        )
        .rstrip(b"=")
        .decode()
    )
    return f"{payload}.{signature}"


@pytest.fixture
def relay_client(monkeypatch: pytest.MonkeyPatch):
    """The HTTP surface, with a signing secret and no media stack behind it."""
    from fastapi.testclient import TestClient

    from esurvey_ai import live_webrtc as module
    from esurvey_ai.config import settings
    from esurvey_ai.main import app

    monkeypatch.setattr(settings, "caller_token", TICKET_SECRET)
    answered: list[dict[str, Any]] = []

    async def fake_offer(
        sdp: str,
        sdp_type: str,
        context: dict[str, Any] | None = None,
        max_seconds: int | None = None,
        transcribe_only: bool = False,
    ) -> dict[str, str]:
        answered.append({
            "context": context,
            "max_seconds": max_seconds,
            "transcribe_only": transcribe_only,
        })
        return {"sdp": "v=0", "type": "answer"}

    monkeypatch.setattr(module, "offer", fake_offer)
    return TestClient(app), answered


def test_an_offer_with_no_ticket_is_refused(relay_client: Any) -> None:
    # Without this the relay is an open door onto a per-minute model: an endpoint a
    # handset can reach is an endpoint anybody can reach, and the bill arrives a month
    # later with nobody's name on it.
    client, answered = relay_client

    res = client.post("/api/v1/live/webrtc/offer", json={"sdp": "v=0"})

    assert res.status_code == 401
    assert answered == [], "a session was started for an unvouched caller"


def test_an_offer_with_a_forged_ticket_is_refused(relay_client: Any) -> None:
    client, answered = relay_client
    forged = issue_ticket(int(time.time()) + 60, 480, secret="not-the-secret")

    res = client.post(
        "/api/v1/live/webrtc/offer", json={"sdp": "v=0", "ticket": forged}
    )

    assert res.status_code == 401
    assert answered == []


def test_an_offer_with_an_expired_ticket_is_refused(relay_client: Any) -> None:
    client, answered = relay_client
    stale = issue_ticket(int(time.time()) - 1, 480)

    res = client.post(
        "/api/v1/live/webrtc/offer", json={"sdp": "v=0", "ticket": stale}
    )

    assert res.status_code == 401
    assert answered == []


def test_the_platforms_allowance_is_what_a_webrtc_session_gets(
    relay_client: Any,
) -> None:
    client, answered = relay_client

    res = client.post(
        "/api/v1/live/webrtc/offer",
        json={
            "sdp": "v=0",
            "ticket": issue_ticket(int(time.time()) + 60, 240),
            "context": {"crop": "soybean", "farmer_id": "F-1", "aadhaar": "1"},
        },
    )

    assert res.status_code == 200
    assert answered[0]["max_seconds"] == 240
    # Only the agreed list crosses to the model, whatever the caller sent.
    assert answered[0]["context"] == {"crop": "soybean"}


def test_a_transcribing_webrtc_session_is_told_nothing_about_a_claim(
    relay_client: Any,
) -> None:
    # The officer's mode, which the socket has always honoured and this path did not
    # have at all: no voice back, no picture accepted, and no claim described. A mode
    # that existed on one transport only would be a mode that lapsed the day a console
    # changed transport.
    client, answered = relay_client

    res = client.post(
        "/api/v1/live/webrtc/offer",
        json={
            "sdp": "v=0",
            "ticket": issue_ticket(int(time.time()) + 60, 240),
            "mode": "transcribe",
            "context": {"crop": "soybean"},
        },
    )

    assert res.status_code == 200
    assert answered[0]["transcribe_only"] is True
    assert answered[0]["context"] is None
