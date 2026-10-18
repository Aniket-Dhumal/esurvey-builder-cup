"""The same session over WebRTC, for a link that loses packets.

# Why this exists alongside the WebSocket

A WebSocket is TCP, and TCP will not deliver packet 4 until packet 3 arrives. On a
clean connection that costs nothing. On a rural cellular link, where a few per cent
loss is ordinary, it means one lost audio packet stalls every packet behind it -- so
a conversation that would have had a moment of static instead has a second of
silence, then a burst, then another stall. A farmer reads that as the app being
broken, and they are not wrong to.

WebRTC runs the audio over UDP with its own loss concealment. A missing packet is a
missing packet: the codec fills it and the conversation carries on.

# What it costs, which is why the WebSocket stays

An SDP exchange, an ICE negotiation, a media stack, and Opus. That is a great deal
more to go wrong than a socket, and it fails in ways that are harder to diagnose from
a field -- a symmetric NAT, a carrier that blocks UDP, an ICE candidate that never
gathers. So both exist: this is offered where it works, and the WebSocket is what a
handset falls back to rather than losing the assistant entirely.

# Where the conversion happens

    handset ──Opus/RTP over UDP──▶  aiortc  ──PCM 16 kHz──▶  Live API
            ◀─Opus/RTP over UDP──         ◀──PCM 24 kHz──
            ──JPEG over data channel─▶

Opus in and out, PCM in the middle, because the Live API works in PCM and the handset
should not. Doing the codec here means one implementation rather than one per
platform, and it is the reason this relay is worth having at all.

# Why the Opus that is negotiated is not the Opus that comes out of the box

Because the defaults are for music. aiortc offers Opus at 48 kHz stereo and encodes at
96 kbit/s, which is a fine choice on an office connection and a third of a farm's
entire link spent on one voice. The farm has a quarter of a megabit, the source is a
single mono stream of speech, and Opus was designed to carry exactly that in twenty
kilobits. So the answer this relay sends says so -- in the SDP, where it binds the
handset's encoder, and in the encoder here, where the SDP has no authority at all.
Both halves are in `constrain_opus_sdp` and `constrain_opus_encoder` below, with the
arithmetic.
"""

from __future__ import annotations

import asyncio
import base64
import fractions
import json
import logging
from typing import Any

# The one part of the media stack that cannot be imported late.
#
# Everything else here reaches for aiortc inside the function that needs it, which
# keeps a service that never holds a WebRTC session from paying for the stack. A base
# class is different: `PcmTrack` below has to *be* one of aiortc's tracks at the moment
# it is defined, because a sender given anything else silently sends nothing. See the
# note on that class.
from aiortc.mediastreams import MediaStreamError, MediaStreamTrack

from . import live

log = logging.getLogger(__name__)

# Tasks that must outlive the call that started them.
#
# asyncio holds only a weak reference to a task, so one created and not kept can be
# collected while it is still running -- and here that is a conversation ending
# mid-sentence for no reason anybody could find afterwards. The offer handler returns
# as soon as it has answered, by design, so the session's own tasks have nowhere else
# to live.
_running: set[asyncio.Task[Any]] = set()


def _hold(coro: Any) -> asyncio.Task[Any]:
    """Start a task and keep it alive until it finishes."""
    task = asyncio.ensure_future(coro)
    _running.add(task)
    task.add_done_callback(_running.discard)
    return task

# What the Live API speaks, and what a phone's Opus stream is resampled to.
PCM_IN_RATE = live.INPUT_SAMPLE_RATE
PCM_OUT_RATE = live.OUTPUT_SAMPLE_RATE

# 20 ms of 24 kHz mono 16-bit, which is what the outgoing track hands over per frame.
#
# Twenty milliseconds because that is Opus's own frame size: anything else makes the
# encoder repacketise, which costs CPU on a server holding many sessions for no gain
# in quality.
OUT_FRAME_SAMPLES = PCM_OUT_RATE // 50

# What Opus is told to spend on a farmer's voice, in bits per second.
#
# Twenty-four kilobits. Opus carries mono speech at that rate at a quality nobody in a
# field will find wanting -- it is the rate the codec's own authors give for wideband
# speech -- and the alternative is aiortc's default of 96 kbit/s, which is a music
# setting and would spend more than a third of a 256 kbit link on one direction of one
# conversation. Measured on this machine: the default produces about 76 kbit/s of
# payload on speech-like input, this produces about 23.
OPUS_BITRATE = 24_000

# What a session actually costs on the wire, per direction, in kilobits per second.
#
# The payload above is not the bill. At 20 ms a frame there are fifty packets a second,
# and each carries an IPv4 header (20 bytes), UDP (8), RTP (12) and the SRTP tag (10) --
# 50 bytes of envelope, which is 400 bits, which is 20 kbit/s of pure overhead. So
# 24 + 20 is 44, and a few hundred bits for RTCP and ICE keepalives rounds it to 48.
#
# Written down here because the platform's bandwidth gate is derived from it and the two
# numbers must not drift apart; the Go side says where it got its floor.
OPUS_WIRE_KBPS = 48

# The ceiling offered to the handset's encoder, as a media-level `b=AS`.
#
# Forty kilobits: the payload above plus room for the envelope, which is what libwebrtc
# reads this line as. Stated as well as `maxaveragebitrate` because the two bind
# different things -- one is what Opus is asked to average, the other is what the
# transport is allowed to reach -- and an encoder that honours only one of them still
# ends up where we want it.
OPUS_SESSION_KBPS = 40

# How much loss Opus is told to expect, as a percentage.
#
# Five per cent. In-band FEC spends part of the bitrate on a redundant copy of the
# previous frame, which is only worth doing if packets are actually being lost -- and on
# a rural cellular link a few per cent is the ordinary state of affairs rather than a
# fault. This is the whole reason the session is on UDP: told nothing, the codec would
# conceal the loss by inventing something, and told this it can reconstruct it.
OPUS_PACKET_LOSS_PERCENT = 5

# The packet length asked for, and the longest one accepted, in milliseconds.
#
# Twenty is Opus's own frame and what the outgoing track produces. Longer packets would
# cut the envelope above -- forty milliseconds halves it -- but they also add their own
# length to the delay before a farmer hears a syllable, and a spoken exchange is already
# carrying the model's thinking time. Forty is allowed, since a handset that chooses it
# has presumably decided its link needs the saving more than the conversation needs the
# millisecond.
OPUS_PTIME_MS = 20
OPUS_MAX_PTIME_MS = 40

# How many events are held while the data channel is still being set up.
#
# There is a gap of a second or two between answering an offer and the channel opening:
# ICE, then DTLS, then SCTP. The session starts talking to the model immediately, so the
# first events -- `ready`, and `unavailable` if the model refused us on the spot --
# happen inside that gap, and a path that dropped them would leave a handset waiting for
# a session that had already failed.
#
# Bounded because the gap is not guaranteed to end. Thirty-two is far more than the
# opening of a conversation produces, and once it is reached the channel is not coming.
MAX_PENDING_EVENTS = 32


class WebRtcTransport:
    """`live.Transport` over a peer connection.

    Audio does not pass through here in either direction: the inbound track is
    resampled straight into the session, and the outbound is pulled by the media
    stack. What crosses this is the small stuff -- frames the farmer sends, events the
    relay reports -- which travels on the data channel.
    """

    def __init__(self) -> None:
        self._inbox: asyncio.Queue[dict[str, Any] | None] = asyncio.Queue()
        self.outbound = PcmTrack()
        self._channel: Any = None
        self._pending: list[dict[str, Any]] = []

    # --- the relay's side -----------------------------------------------------

    async def receive(self) -> dict[str, Any] | None:
        return await self._inbox.get()

    async def send_audio(self, pcm: bytes) -> None:
        # Handed to the outgoing track, which the media stack pulls from at its own
        # pace. Queued rather than blocking: the model produces audio in bursts and
        # the track sends it at twenty milliseconds a frame.
        self.outbound.feed(pcm)

    async def send_event(self, event: dict[str, Any]) -> None:
        channel = self._channel
        if channel is None or channel.readyState != "open":
            # The channel is still being negotiated, or it has gone. Held rather than
            # dropped while there is somewhere to hold it: the events at the start of a
            # session are the ones that say whether there is a session, and by the time
            # the queue is full there is no channel coming. See MAX_PENDING_EVENTS.
            if len(self._pending) < MAX_PENDING_EVENTS:
                self._pending.append(event)
            return
        self._write(event)

    def _write(self, event: dict[str, Any]) -> None:
        """Put one event on the channel, whatever the channel thinks of it.

        An event is a courtesy -- a transcript, a notice -- and the audio is the
        substance, so a send that fails must not end a session.
        """
        try:
            self._channel.send(live.encode_json(event))
        except Exception as exc:
            log.debug("dropping an event on a closed channel: %s", exc)

    # --- the peer connection's side ------------------------------------------

    def attach_channel(self, channel: Any) -> None:
        self._channel = channel

        # Whatever the session said before the channel could carry it. Flushed on
        # arrival if the channel is already open, which is how aiortc reports one, and
        # otherwise when it says it is.
        if getattr(channel, "readyState", None) == "open":
            self._flush()
        else:

            @channel.on("open")
            def _on_open() -> None:
                self._flush()

        @channel.on("message")
        def _on_message(message: Any) -> None:
            # Frames and typed text arrive here; audio never does.
            if isinstance(message, bytes):
                # A frame sent as bytes rather than base64, which saves a third of
                # it. Wrapped to the shape the relay expects.
                self._inbox.put_nowait(
                    {"type": "frame", "data": base64.b64encode(message).decode()}
                )
                return
            try:
                parsed = json.loads(message)
            except (TypeError, ValueError):
                return
            if isinstance(parsed, dict):
                self._inbox.put_nowait(parsed)

    def _flush(self) -> None:
        held, self._pending = self._pending, []
        for event in held:
            self._write(event)

    def push(self, message: dict[str, Any] | None) -> None:
        """Hand the relay a message, or None to end the session."""
        self._inbox.put_nowait(message)

    def close(self) -> None:
        self._inbox.put_nowait(None)
        self.outbound.stop()


class PcmTrack(MediaStreamTrack):
    """The assistant's voice, as a track the media stack can pull.

    Silence is produced when there is nothing to say, rather than nothing at all. A
    track that stops producing frames is a track the far end treats as failed, and the
    connection is then torn down in the middle of a pause for breath.

    # Why this inherits, when duck typing would have done

    Because it would not have. `RTCPeerConnection.addTrack` accepts anything with a
    `kind`, and `RTCRtpSender` then asks whether what it was given is one of aiortc's
    own tracks: if it is not, it keeps the object as its *kind* and sets its track to
    None. Nothing raises. The sender's RTP loop finds no track, sleeps twenty
    milliseconds, finds no track again, and does that for the length of the session --
    so the negotiation succeeds, the farmer's voice arrives, the model answers, and not
    one packet of the answer is ever sent. This class was a plain object for exactly
    that long.
    """

    kind = "audio"

    def __init__(self) -> None:
        super().__init__()
        self._queue: asyncio.Queue[bytes] = asyncio.Queue()
        self._buffer = bytearray()
        self._timestamp = 0
        self._stopped = False

    def feed(self, pcm: bytes) -> None:
        if not self._stopped:
            self._queue.put_nowait(pcm)

    def stop(self) -> None:
        self._stopped = True
        # And told to the media stack, which is what takes the sender off the air
        # rather than leaving it pushing silence at a handset that has gone.
        super().stop()

    async def recv(self) -> Any:
        from av import AudioFrame

        want = OUT_FRAME_SAMPLES * 2  # 16-bit mono
        while len(self._buffer) < want:
            if self._stopped:
                # Finished, but not necessarily finished speaking: the last thing the
                # model said may still be queued, and it is a sentence a farmer is owed.
                # Taken without waiting for more, since no more is coming.
                while not self._queue.empty():
                    self._buffer.extend(self._queue.get_nowait())
                break
            try:
                chunk = await asyncio.wait_for(self._queue.get(), timeout=0.02)
                self._buffer.extend(chunk)
            except TimeoutError:
                # Nothing to say. Pad with silence rather than stall: see the class
                # note.
                break

        if self._stopped and not self._buffer:
            # Over, and everything said has been sent. Ending the track is what takes
            # the sender off the air; padding silence for ever would keep a finished
            # conversation on it.
            raise MediaStreamError

        take = bytes(self._buffer[:want])
        del self._buffer[:want]
        if len(take) < want:
            take = take + b"\x00" * (want - len(take))

        frame = AudioFrame(format="s16", layout="mono", samples=OUT_FRAME_SAMPLES)
        frame.planes[0].update(take)
        frame.sample_rate = PCM_OUT_RATE
        frame.pts = self._timestamp
        frame.time_base = fractions.Fraction(1, PCM_OUT_RATE)
        self._timestamp += OUT_FRAME_SAMPLES
        return frame


async def pump_inbound(track: Any, session_audio: Any) -> None:
    """Resample the farmer's Opus track to PCM and hand it to the session.

    [session_audio] is called with each chunk of 16 kHz mono PCM. Separated from the
    relay so this can be tested with a fake track and no peer connection at all.
    """
    from av import AudioResampler

    resampler = AudioResampler(format="s16", layout="mono", rate=PCM_IN_RATE)
    while True:
        try:
            frame = await track.recv()
        except Exception:
            # The track ended, which is how a handset hanging up presents.
            return
        for resampled in resampler.resample(frame):
            # Only the samples, not the plane.
            #
            # A resampled plane is allocated with alignment padding, so a 320-sample
            # frame of 16-bit mono sits in a 768-byte buffer with 640 bytes of audio in
            # it. Handing the whole plane over sent the model a hundred and twenty-eight
            # bytes of whatever was in that memory every twenty milliseconds, which is a
            # click in the middle of every syllable and a fifth of the uplink spent on
            # it.
            data = bytes(resampled.planes[0])[: resampled.samples * 2]
            if data:
                await session_audio(data)


def prefer_opus(pc: Any) -> None:
    """Answer with Opus and nothing else.

    aiortc will otherwise agree to G.722, PCMU and PCMA as well, and whichever of them
    the handset happens to prefer is what gets used. On this link none of them is a
    choice worth leaving open: G.711 is 64 kbit/s of payload for narrowband telephone
    audio, which costs nearly three times what Opus costs and sounds worse doing it.

    Called before the remote description is set, since that is where the common list is
    decided. A handset that somehow offered no Opus at all then fails to negotiate and
    falls back to the socket, which is the same thing that happens when its carrier
    blocks UDP -- an outcome this path is built to survive.
    """
    from aiortc import RTCRtpSender

    opus = [
        codec
        for codec in RTCRtpSender.getCapabilities("audio").codecs
        if codec.mimeType.lower() == "audio/opus"
    ]
    if not opus:
        return
    for transceiver in pc.getTransceivers():
        if transceiver.kind == "audio":
            transceiver.setCodecPreferences(opus)


def constrain_opus_sdp(sdp: str) -> str:
    """Say in the answer what the handset's Opus encoder should cost us.

    # Why this is in the SDP and not in a setting

    Because the encoder in question is on the phone, and the answer is the only thing
    this end gets to say to it. `maxaveragebitrate` is what RFC 7587 defines for exactly
    this -- the receiver telling the sender what it wants to be sent -- and libwebrtc,
    which is what the farmer app's stack is, honours it. Left unsaid, a handset encodes
    at whatever its own default is, and the uplink is the half of a cellular link that
    has nothing to spare.

    # What each parameter is doing

      * maxaveragebitrate=24000 -- see OPUS_BITRATE. Mono speech, well carried.
      * maxplaybackrate=16000 -- what this end can do anything with. The farmer's voice
        is resampled to 16 kHz for the Live API the moment it arrives, so a handset that
        spent uplink on the octave above that would be spending it on something thrown
        away in this process.
      * stereo=0 and sprop-stereo=0 -- one microphone, one voice. A stereo stream would
        be the same sound twice at twice the price. (The rtpmap still says two channels
        because RFC 7587 requires it to; these are how mono is actually signalled.)
      * useinbandfec=1 -- the packets will be lost. Let the codec carry a redundant copy
        rather than guess at what was in the gap.
      * cbr=0 -- variable, so silence between sentences costs nothing. A farmer walking
        between points is not talking, and most of a session is that.
      * b=AS -- see OPUS_SESSION_KBPS.
      * ptime and maxptime -- see OPUS_PTIME_MS.

    An answer with no audio section, or one that settled on something other than Opus,
    is returned exactly as it came: this adds constraints and never removes a line the
    negotiation depends on.
    """
    if "opus/" not in sdp.lower():
        return sdp

    parameters = ";".join(
        [
            f"maxaveragebitrate={OPUS_BITRATE}",
            f"maxplaybackrate={PCM_IN_RATE}",
            "stereo=0",
            "sprop-stereo=0",
            "useinbandfec=1",
            "cbr=0",
        ]
    )

    ending = "\r\n" if "\r\n" in sdp else "\n"
    out: list[str] = []
    audio = False
    payload = ""

    for line in sdp.replace("\r\n", "\n").split("\n"):
        if line.startswith("m="):
            audio = line.startswith("m=audio")
            payload = ""
        if audio and line.startswith("a=rtpmap:") and "opus/" in line.lower():
            payload = line.split(":", 1)[1].split(" ", 1)[0]
            out.append(line)
            out.append(f"a=fmtp:{payload} {parameters}")
            out.append(f"a=ptime:{OPUS_PTIME_MS}")
            out.append(f"a=maxptime:{OPUS_MAX_PTIME_MS}")
            continue
        if audio and payload and line.startswith(f"a=fmtp:{payload} "):
            # Ours has already been written in its place.
            continue
        if audio and line.startswith(("a=ptime:", "a=maxptime:")):
            continue
        out.append(line)
        if audio and line.startswith("c="):
            # Where a bandwidth line belongs, between the connection and the
            # attributes, which is the order RFC 4566 gives and some stacks enforce.
            out.append(f"b=AS:{OPUS_SESSION_KBPS}")

    return ending.join(out)


# Whether aiortc's Opus encoder has been told what link it is encoding for.
_encoder_constrained = False


def constrain_opus_encoder() -> None:
    """Make the outgoing Opus speech-grade rather than music-grade.

    # Why this reaches into aiortc

    Because the SDP has no authority here. `maxaveragebitrate` in our answer binds the
    handset's encoder and says nothing to ours, and aiortc offers no way to ask for a
    send bitrate -- its Opus encoder is built privately, on the first frame, hard-wired
    to 96 kbit/s stereo. Ninety-six kilobits is a third of a farm's entire link spent on
    one direction of one conversation, and the source is a single mono stream of speech
    that Opus carries in twenty-four.

    So the factory aiortc builds its encoder from is replaced with one that is told
    about the link. That is a reach into another library's namespace, which is worth
    being uncomfortable about; the alternative was a copy of the sender's RTP loop,
    which is a great deal more of somebody else's code to own.

    Idempotent, because it is called once per offer and there may be many.
    """
    global _encoder_constrained
    if _encoder_constrained:
        return

    import aiortc.codecs
    from aiortc.codecs.opus import OpusEncoder

    def speech_encoder() -> OpusEncoder:
        from av import AudioResampler

        encoder = OpusEncoder()
        encoder.codec.bit_rate = OPUS_BITRATE
        encoder.codec.layout = "mono"
        # Told what the link does to packets, which is what makes in-band FEC spend
        # part of the bitrate usefully instead of not at all. `voip` is aiortc's own
        # choice and the right one; it is restated because these options replace the
        # dictionary rather than adding to it.
        encoder.codec.options = {
            "application": "voip",
            "fec": "1",
            "packet_loss": str(OPUS_PACKET_LOSS_PERCENT),
        }
        # aiortc's own resampler, with the channel count brought into line. It is what
        # turns the track's 24 kHz mono into the 48 kHz Opus is always coded at, and it
        # has to agree with the encoder about channels or nothing encodes at all.
        encoder.resampler = AudioResampler(
            format="s16", layout="mono", rate=48000, frame_size=960
        )
        return encoder

    aiortc.codecs.OpusEncoder = speech_encoder  # type: ignore[assignment,attr-defined]
    _encoder_constrained = True


async def offer(
    sdp: str,
    sdp_type: str,
    context: dict[str, Any] | None = None,
    max_seconds: int | None = None,
    transcribe_only: bool = False,
) -> dict[str, str]:
    """Answer a handset's offer and hold the session for as long as it lasts.

    Returns the answer immediately; the conversation runs in the background until the
    peer connection closes. That is the shape WebRTC signalling wants -- one HTTP
    round trip, then media over UDP -- and it is why there is no socket to hold open
    here.

    `transcribe_only` is the same bound the socket route carries: an officer dictating
    rather than a farmer walking, so this end must not speak back and is told nothing
    about any claim. It is here so that the two transports cannot disagree about what a
    session is allowed to be -- a mode that existed on one of them only would be a mode
    that stopped applying the day somebody changed transport.
    """
    from aiortc import RTCPeerConnection, RTCSessionDescription

    constrain_opus_encoder()

    pc = RTCPeerConnection()
    transport = WebRtcTransport()
    pc.addTrack(transport.outbound)

    done = asyncio.Event()

    @pc.on("datachannel")
    def _on_channel(channel: Any) -> None:
        transport.attach_channel(channel)

    @pc.on("track")
    def _on_track(track: Any) -> None:
        if track.kind != "audio":
            return

        async def feed(pcm: bytes) -> None:
            transport.push(
                {"type": "audio", "data": base64.b64encode(pcm).decode()}
            )

        _hold(pump_inbound(track, feed))

    @pc.on("connectionstatechange")
    async def _on_state() -> None:
        # Failed and closed are endings. Disconnected is not: it is what ICE reports
        # when packets have stopped arriving for a few seconds, which on a rural link is
        # a farmer walking behind a shed and coming out the other side. Ending a session
        # there would cut the conversation exactly where this transport was chosen to
        # survive it; a link that is genuinely gone is caught by the relay's own idle
        # timeout instead.
        if pc.connectionState in {"failed", "closed"}:
            transport.close()
            done.set()

    # Before the remote description, because that is where aiortc settles which codecs
    # the two ends have in common: a preference stated afterwards is a preference stated
    # about a decision already taken.
    prefer_opus(pc)

    await pc.setRemoteDescription(RTCSessionDescription(sdp=sdp, type=sdp_type))
    answer = await pc.createAnswer()
    await pc.setLocalDescription(answer)

    async def hold() -> None:
        try:
            limits = live.SessionLimits(
                max_seconds=max_seconds or live.MAX_SESSION_SECONDS,
                transcribe_only=transcribe_only,
            )
            stats = await live.relay(
                transport, context=context, limits=limits
            )
            log.info(
                "webrtc session ended: %s after %.0fs, %d frames",
                stats.ended_because,
                stats.seconds,
                stats.frames_in,
            )
        finally:
            transport.close()
            await pc.close()
            done.set()

    _hold(hold())

    return {
        # Constrained on the way out rather than in aiortc's own description: what is
        # added here is advice to the handset's encoder, and this end's view of the
        # negotiation must stay exactly what it agreed to.
        "sdp": constrain_opus_sdp(pc.localDescription.sdp),
        "type": pc.localDescription.type,
    }
