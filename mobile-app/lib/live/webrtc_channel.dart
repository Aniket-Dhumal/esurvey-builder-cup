import 'dart:async';

import 'package:flutter/foundation.dart';
import 'package:flutter_webrtc/flutter_webrtc.dart';

import '../core/api.dart';
import 'live_session.dart';

/// The session over WebRTC, where the audio does not pass through Dart at all.
///
/// # Why this is worth the extra machinery
///
/// A WebSocket is TCP, and TCP will not deliver packet four until packet three
/// arrives. On a clean connection that costs nothing; on a rural cellular link, where
/// a few per cent loss is ordinary, one missing audio packet stalls every packet
/// behind it. The farmer hears silence, then a burst, then another stall, and reads
/// that as the app being broken. They are not wrong to.
///
/// Over WebRTC a missing packet is a missing packet: Opus conceals it and the
/// conversation carries on.
///
/// # Why the audio bypasses the session controller
///
/// Because the platform does it better. The microphone track is captured, encoded to
/// Opus and sent by the native stack, and the assistant's voice is decoded and played
/// the same way. Dart never sees a sample, which removes the two places a sample rate
/// could be wrong and the buffering that would otherwise have to be written per
/// platform.
///
/// The consequence is that [sendBytes] does nothing here. That reads as a bug and is
/// not one, which is why it says so.
///
/// # What still goes over the data channel
///
/// Frames and events -- everything small and occasional. Separate from the audio on
/// purpose: an image upload on the same stream as the voice would stall the voice
/// while it went, and the frame is the part that can wait.
class WebRtcChannel implements LiveChannel {
  WebRtcChannel._(this._pc, this._channel, this._events, this._localStream);

  final RTCPeerConnection _pc;
  final RTCDataChannel _channel;
  final StreamController<dynamic> _events;
  final MediaStream _localStream;

  /// Negotiate a session with the relay.
  ///
  /// One round trip: the offer goes, the answer comes back, and the media flows over
  /// UDP from then on. Throws where the negotiation fails, which the caller reads as
  /// "fall back to the socket" rather than as the assistant being unavailable -- a
  /// symmetric NAT or a carrier blocking UDP is a property of where the farmer is
  /// standing, not of whether they may talk.
  /// [ticket] is the platform's signed permission, which the relay checks instead of
  /// authenticating the farmer itself. Without it the relay would be an open door onto
  /// a per-minute model, so it refuses an offer that does not carry one.
  static Future<WebRtcChannel> connect({
    required Api api,
    required String ticket,
    Map<String, String> context = const {},
  }) async {
    final pc = await createPeerConnection({
      // Google's public STUN. Enough for the common case; a farm behind a
      // symmetric NAT needs TURN, which is a server to run and a cost to carry, and
      // the WebSocket is the answer there instead.
      'iceServers': [
        {'urls': 'stun:stun.l.google.com:19302'},
      ],
      'sdpSemantics': 'unified-plan',
    });

    final events = StreamController<dynamic>.broadcast();

    // The microphone, with the processing a phone in a field needs. Echo
    // cancellation because the assistant's voice comes out of the same handset's
    // speaker, and noise suppression because the ambient in a field at midday is
    // wind and machinery.
    final local = await navigator.mediaDevices.getUserMedia({
      'audio': {
        'echoCancellation': true,
        'noiseSuppression': true,
        'autoGainControl': true,
        // Sixteen kilohertz is what the relay resamples to anyway, so asking for
        // more spends uplink on detail that is discarded.
        'sampleRate': 16000,
        'channelCount': 1,
      },
      'video': false,
    });
    for (final track in local.getAudioTracks()) {
      await pc.addTrack(track, local);
    }

    // The assistant's voice. Attached to nothing in Dart: the native stack plays it,
    // which is the whole point of doing it this way.
    pc.onTrack = (RTCTrackEvent event) {
      if (event.track.kind == 'audio') {
        debugPrint('[webrtc] the assistant has a voice track');
      }
    };

    final channel = await pc.createDataChannel(
      'events',
      RTCDataChannelInit()..ordered = true,
    );
    channel.onMessage = (RTCDataChannelMessage message) {
      if (message.isBinary) return;
      events.add(message.text);
    };

    final offer = await pc.createOffer({
      'offerToReceiveAudio': true,
      'offerToReceiveVideo': false,
    });
    await pc.setLocalDescription(offer);

    // Wait for candidates to gather before sending, rather than trickling them.
    //
    // Trickle ICE needs a signalling channel that stays open, and the relay answers
    // in one HTTP round trip on purpose. Gathering first costs a second or two and
    // removes a whole class of failure from a farmer's first experience of the
    // feature.
    await _gathered(pc);

    final local0 = await pc.getLocalDescription();
    final res = await api.postJson('/api/v1/live/webrtc/offer', {
      'sdp': local0?.sdp,
      'type': local0?.type,
      'ticket': ticket,
      if (context.isNotEmpty) 'context': context,
    });
    if (!res.ok) {
      await local.dispose();
      await pc.close();
      throw StateError('the relay refused the offer (${res.status})');
    }

    final sdp = '${res.body?['sdp'] ?? ''}';
    final type = '${res.body?['type'] ?? 'answer'}';
    if (sdp.isEmpty) {
      await local.dispose();
      await pc.close();
      throw StateError('the relay sent no answer');
    }
    await pc.setRemoteDescription(RTCSessionDescription(sdp, type));

    return WebRtcChannel._(pc, channel, events, local);
  }

  /// Wait until ICE has finished gathering, or long enough.
  ///
  /// Bounded, because gathering can wait on a STUN server that never answers, and a
  /// farmer watching a spinner has no idea that is what is happening. Three seconds
  /// is generous on a working connection; past that the candidates in hand are what
  /// there is.
  static Future<void> _gathered(RTCPeerConnection pc) async {
    if (pc.iceGatheringState ==
        RTCIceGatheringState.RTCIceGatheringStateComplete) {
      return;
    }
    final done = Completer<void>();
    pc.onIceGatheringState = (RTCIceGatheringState state) {
      if (state == RTCIceGatheringState.RTCIceGatheringStateComplete &&
          !done.isCompleted) {
        done.complete();
      }
    };
    await done.future.timeout(
      const Duration(seconds: 3),
      onTimeout: () => null,
    );
  }

  @override
  Stream<dynamic> get stream => _events.stream;

  /// Nothing, deliberately.
  ///
  /// The farmer's speech is a media track the platform captures, encodes and sends.
  /// Dart never holds a sample, so there is nothing here to send -- and writing the
  /// audio to the data channel instead would put it back on an ordered stream, which
  /// is exactly what this transport exists to avoid.
  @override
  void sendBytes(List<int> data) {}

  @override
  void sendText(String text) {
    if (_channel.state != RTCDataChannelState.RTCDataChannelOpen) return;
    _channel.send(RTCDataChannelMessage(text));
  }

  /// A frame, as bytes rather than base64.
  ///
  /// Saves a third of an image on a link that has a quarter of a megabit. The relay
  /// treats anything binary on this channel as a frame, because audio has its own
  /// track and never arrives here.
  void sendFrameBytes(Uint8List jpeg) {
    if (_channel.state != RTCDataChannelState.RTCDataChannelOpen) return;
    _channel.send(RTCDataChannelMessage.fromBinary(jpeg));
  }

  @override
  Future<void> close() async {
    try {
      await _channel.close();
    } catch (_) {
      // Already gone, which is the ordinary ending.
    }
    for (final track in _localStream.getTracks()) {
      await track.stop();
    }
    await _localStream.dispose();
    await _pc.close();
    if (!_events.isClosed) await _events.close();
  }
}

/// Which transport a session used, for reporting and for deciding what to try next.
enum LiveTransportKind { webRtc, webSocket }

/// Open the best transport this farm will carry.
///
/// WebRTC first, because a lossy link is the ordinary case and it is the one that
/// survives loss. The socket is the fallback rather than the default: it always
/// connects, and always connecting is not the same as always working.
///
/// A failure to negotiate is not reported to the farmer. It is a property of where
/// they are standing -- a symmetric NAT, a carrier blocking UDP -- and the assistant
/// works either way, so the only thing they would learn is that something they cannot
/// see went wrong.
Future<({LiveChannel channel, LiveTransportKind kind})> openBestTransport({
  required Api api,
  required String socketUrl,
  String ticket = '',
  Map<String, String> context = const {},
  Future<LiveChannel> Function(String url)? openSocket,
}) async {
  try {
    final channel = await WebRtcChannel.connect(
      api: api,
      ticket: ticket,
      context: context,
    );
    return (channel: channel as LiveChannel, kind: LiveTransportKind.webRtc);
  } catch (e) {
    debugPrint('[webrtc] falling back to the socket: $e');
  }
  final open = openSocket ?? connectWebSocket;
  return (channel: await open(socketUrl), kind: LiveTransportKind.webSocket);
}
