import 'dart:async';
import 'dart:convert';

import 'package:flutter/foundation.dart';
import 'package:web_socket_channel/web_socket_channel.dart';

import '../core/api.dart';
import '../core/faults.dart';
import '../core/link_quality.dart';
import '../core/live_consent.dart';
import 'webrtc_channel.dart';

/// Talking to the assistant, and every reason not to.
///
/// # Why there are four gates before a word is spoken
///
/// Because each of them is a different way this can be the wrong thing to do, and a
/// farmer standing in a field cannot be asked to work out which.
///
///   1. The build has it enabled. A trial can run without it.
///   2. The farmer has agreed to be recorded. Not implied by the camera.
///   3. The link can carry it, measured rather than asked of Android.
///   4. The office has budget left today.
///
/// Failing any of them ends in the same place: the scripted guidance, which needs no
/// network, no model and no permission beyond the camera. That is the whole design --
/// the assistant is an addition, so every one of these can refuse without refusing the
/// farmer anything.
///
/// # Why the audio is raw PCM both ways
///
/// Because the Live API works in it, and every conversion between here and there is a
/// place for a sample rate to be wrong. Opus would halve the uplink and is the right
/// next step; it belongs in the relay, where one implementation serves every handset,
/// rather than here where it would be a codec per platform.
enum LiveState {
  /// Not offered. See [LiveSession.unavailableBecause].
  unavailable,

  /// Offered, not started.
  ready,

  /// Measuring, granting, connecting.
  opening,

  /// The farmer may speak.
  listening,

  /// The assistant is speaking.
  speaking,

  /// Over, for a reason worth telling them.
  ended,
}

/// What a session is for, and what it cost.
class LiveOutcome {
  const LiveOutcome({
    required this.seconds,
    required this.endedBecause,
    this.askedFor = const [],
  });

  final int seconds;
  final String endedBecause;

  /// Everything the assistant asked the farmer to show, in order.
  ///
  /// Kept because the last one is what the next clip answers, and it is written
  /// against that clip so the assessment can tell a directed observation from the
  /// farmer's own selection.
  final List<String> askedFor;

  String? get latestRequest => askedFor.isEmpty ? null : askedFor.last;
}

/// One conversation.
class LiveSession extends ChangeNotifier {
  LiveSession({
    required this.api,
    required this.consent,
    required this.probe,
    required this.farmerId,
    this.reportId,
    this.connect,
  });

  final Api api;
  final LiveConsent consent;
  final LinkProbe probe;
  final String farmerId;
  final String? reportId;

  /// How the transport is opened, for tests only.
  ///
  /// Null in the app, and that is the point: null means the real thing, which tries
  /// WebRTC and falls back to the socket. Named as a seam rather than as a choice --
  /// a test needs a channel with no network behind it, and nothing else does.
  final Future<LiveChannel> Function(String url)? connect;

  LiveState _state = LiveState.unavailable;
  LiveState get state => _state;

  String? _unavailableBecause;

  /// Why the assistant is not on offer, in words for the farmer.
  ///
  /// Said rather than hidden. A farmer offered a talking assistant on one visit and
  /// not the next concludes the app is broken, and every one of these reasons is
  /// something they can understand about where they are or what they chose.
  String? get unavailableBecause => _unavailableBecause;

  final List<String> _askedFor = [];

  /// What the assistant has asked to be shown, oldest first.
  List<String> get askedFor => List.unmodifiable(_askedFor);

  /// The most recent thing it asked for, which the next clip answers.
  String? get latestRequest => _askedFor.isEmpty ? null : _askedFor.last;

  /// What it last said, for showing on screen. A farmer in a noisy field may not
  /// have heard it.
  String? said;

  /// Which transport carried the conversation, once one is open.
  ///
  /// Worth knowing above this class, because the two differ in who holds the
  /// microphone. Over WebRTC the platform captures, encodes and sends it and Dart
  /// never sees a sample; over the socket Dart captures raw PCM and pushes it. A
  /// screen that started its own capture on the WebRTC path would be a second
  /// claimant on one microphone, and Android gives it to neither.
  LiveTransportKind? transport;

  /// Whether this conversation needs Dart to capture and play audio.
  ///
  /// False over WebRTC, where the platform does both. See transport.
  bool get audioIsOurs => transport == LiveTransportKind.webSocket;

  /// Which model answered, as the office named it in the grant.
  ///
  /// Read from the grant rather than compiled in, because the handset must not be the
  /// thing that decides -- or records -- which model a claim's guidance came from. An
  /// app pinned to a name would keep stamping it after the office moved on, and the
  /// stamp is what an officer reads a year later when asking why a clip was framed
  /// the way it was.
  String? model;

  LiveChannel? _channel;
  StreamSubscription<dynamic>? _incoming;
  DateTime? _openedAt;
  int _maxSeconds = 0;
  Timer? _ceiling;

  /// Whether the assistant may be offered at all, checked in order of cost.
  ///
  /// The cheap local checks first: a build flag and a stored answer cost nothing,
  /// and there is no reason to measure a link for a farmer who has declined.
  Future<bool> offer() async {
    if (!liveGuidanceEnabled) {
      _unavailable('not enabled in this build');
      return false;
    }
    if (!await consent.granted(farmerId)) {
      _unavailable('the farmer has not agreed to be recorded');
      return false;
    }

    final link = await probe.measure();
    if (!link.canTalk) {
      final why = link.refusal;
      _unavailable(why?.mr ?? 'the connection here is too slow');
      return false;
    }

    _set(LiveState.ready);
    _unavailableBecause = null;
    return true;
  }

  /// Ask the office for a session and open it.
  ///
  /// The grant carries how long it may run, which is the smaller of the relay's own
  /// ceiling and what is left of today's allowance -- so a farmer with four minutes
  /// left gets four rather than being cut off mid-sentence at eight.
  Future<bool> start({
    String? crop,
    String? calamity,
    String? season,
    String? district,
  }) async {
    if (_state == LiveState.listening || _state == LiveState.speaking) {
      return true;
    }
    _set(LiveState.opening);

    final res = await api.postJson('/api/v1/live/session', {
      'farmer_id': farmerId,
      'report_id': ?reportId,
      'crop': ?crop,
      'calamity': ?calamity,
      'season': ?season,
      'district': ?district,
    });

    if (!res.ok) {
      // Over budget is its own answer, and the farmer is told which: the assistant
      // is finished for today, the walk is not, and nothing about the claim has
      // changed.
      _unavailable(
        res.status == 429
            ? 'सहाय्यकाची आजची मर्यादा संपली आहे. पाहणी सुरू ठेवा.'
            : 'सहाय्यक आता उपलब्ध नाही. सूचना ऐकू येतील.',
      );
      return false;
    }

    // The relay address, with the platform's signed ticket already on it: the grant
    // builds the query string, because the ticket is the office's to issue and the
    // handset's only job is to carry it.
    final url = '${res.body?['url'] ?? ''}';
    if (url.isEmpty) {
      _unavailable('सहाय्यक आता उपलब्ध नाही.');
      return false;
    }
    _maxSeconds = (res.body?['max_seconds'] as num?)?.toInt() ?? 0;
    final named = '${res.body?['model'] ?? ''}'.trim();
    model = named.isEmpty ? null : named;

    try {
      final channel = await _open(url);
      _channel = channel;
      _openedAt = DateTime.now();
      _incoming = channel.stream.listen(
        _onMessage,
        onError: (Object e) => _finish('failed', e),
        onDone: () => _finish('closed', null),
      );

      // Bounded here as well as at the relay. A socket that stops answering would
      // otherwise leave a session counted as running until the farmer noticed.
      if (_maxSeconds > 0) {
        _ceiling = Timer(
          Duration(seconds: _maxSeconds),
          () => _finish('session limit', null),
        );
      }

      _set(LiveState.listening);
      return true;
    } catch (e) {
      Faults.note('live.connect', e);
      _unavailable('सहाय्यकाशी जोडता आले नाही.');
      return false;
    }
  }

  /// Send a chunk of the farmer's speech.
  ///
  /// Binary, not base64: this is the continuous half of the traffic, and base64 adds
  /// a third to it for nothing on a link that has a quarter of a megabit.
  void sendAudio(Uint8List pcm) {
    if (_state != LiveState.listening && _state != LiveState.speaking) return;
    _channel?.sendBytes(pcm);
  }

  /// Tell the assistant the farmer has stopped speaking.
  void doneSpeaking() => _send({'type': 'done_speaking'});

  /// Send one frame, which happens when the assistant asks or the farmer presses the
  /// shutter -- never on a timer.
  ///
  /// A frame every three seconds is eighty kilobits and would dominate the budget. A
  /// frame when one is wanted is a few kilobits averaged out, and it is the largest
  /// saving available on a rural link.
  void sendFrame(Uint8List jpeg) =>
      _send({'type': 'frame', 'data': base64Encode(jpeg)});

  /// Type instead of speaking, which is what a farmer in a noisy or a quiet place
  /// does.
  void sendText(String text) {
    final said = text.trim();
    if (said.isEmpty) return;
    _send({'type': 'text', 'text': said});
  }

  /// End it, and report what it cost.
  ///
  /// Reported from here rather than by the relay, because this knows which farmer and
  /// which claim it was for -- and a relay told those would hold identifying data it
  /// has no other use for.
  Future<LiveOutcome> stop([String why = 'closed']) async {
    _send({'type': 'close'});
    return _finish(why, null);
  }

  /// Open the best transport the farm will carry, or whatever a test supplied.
  ///
  /// # Why WebRTC rather than the socket
  ///
  /// Because of what the audio costs. Raw PCM at 16 kHz is 256 kbit/s going up and
  /// the model's reply is 384 coming down, so the socket needs something near 640
  /// kbit/s before a word is exchanged -- which is more than the link this feature
  /// exists for has in total. Opus over WebRTC is nearer 24 each way.
  ///
  /// Loss is the other half. A socket is TCP and will not deliver the fourth packet
  /// until the third arrives, so on a rural link a few per cent loss becomes silence,
  /// then a burst, then more silence. Over WebRTC a lost packet is concealed and the
  /// conversation carries on.
  ///
  /// The socket remains the fallback, because a symmetric NAT or a carrier blocking
  /// UDP is a property of where a farmer is standing rather than of whether they may
  /// talk. It always connects -- and always connecting is not the same as always
  /// working, which is why the link is judged again once the transport is known.
  Future<LiveChannel> _open(String url) async {
    final seam = connect;
    if (seam != null) {
      transport = LiveTransportKind.webSocket;
      return seam(url);
    }

    // The ticket and the claim's context are already on the granted address, because
    // the office put them there. Read back out rather than threaded separately: one
    // string carries the permission whichever transport wins, and there is no second
    // copy to fall out of step.
    final granted = Uri.parse(url);
    final best = await openBestTransport(
      api: api,
      socketUrl: url,
      ticket: granted.queryParameters['ticket'] ?? '',
      context: {
        for (final e in granted.queryParameters.entries)
          if (e.key != 'ticket') e.key: e.value,
      },
    );
    transport = best.kind;
    debugPrint('[live] talking over ${best.kind.name}');

    // The fallback is judged against its own floor, and only now that it is the
    // answer.
    //
    // WebRTC was admitted on a hundred kilobits, which is right for Opus and nowhere
    // near enough for raw PCM: the socket wants six times that. Judging it up front
    // would refuse WebRTC on links that carry it perfectly; not judging it at all is
    // what "the socket always connects" buys -- a session that opens, bills by the
    // minute, and stalls. So a farm that cannot carry the fallback is told so rather
    // than talked into a stream that will not keep up.
    if (best.kind == LiveTransportKind.webSocket) {
      final link = probe.cached;
      if (link != null && !link.canTalkOverSocket) {
        await best.channel.close();
        throw StateError(
          'this farm carries neither WebRTC nor the raw audio the socket needs '
          '(${link.downKbps.round()} down, ${link.upKbps.round()} up, '
          'the socket wants ${link.socketMinKbps})',
        );
      }
    }
    return best.channel;
  }

  void _onMessage(dynamic message) {
    if (message is List<int>) {
      // The assistant's voice. Handed to whoever is playing it; this class does not
      // own the speaker, so that a test can hold a conversation in silence.
      onAudio?.call(Uint8List.fromList(message));
      _set(LiveState.speaking);
      return;
    }
    if (message is! String) return;

    Map<String, Object?> event;
    try {
      final parsed = jsonDecode(message);
      if (parsed is! Map) return;
      event = parsed.cast<String, Object?>();
    } catch (_) {
      return;
    }

    switch (event['type']) {
      case 'said':
        final text = '${event['text'] ?? ''}'.trim();
        if (text.isEmpty) break;
        said = text;
        // Anything phrased as a request to show something is kept, because the next
        // clip answers it and is stamped with it.
        if (_looksLikeARequest(text)) _askedFor.add(text);
        notifyListeners();
      case 'turn_complete':
        _set(LiveState.listening);
      case 'frame_rejected':
        // Worth a fault rather than a screen message: the farmer can do nothing
        // about a frame that was too large, and the app should not have sent one.
        Faults.note('live.frame', 'the relay refused a frame');
      case 'ended':
        unawaited(_finish('${event['reason'] ?? 'ended'}', null));
      case 'unavailable':
        unawaited(_finish('unavailable', null));
    }
  }

  /// Whether a line is the assistant asking for something to be shown.
  ///
  /// Deliberately crude and deliberately generous. Getting it wrong in one direction
  /// stores a sentence against a clip that was not really a request, which is
  /// harmless -- an officer reads it and sees what was said. Getting it wrong the
  /// other way loses the record that the clip was directed at all, which is the whole
  /// value. So anything imperative enough to be an instruction counts.
  static bool _looksLikeARequest(String text) {
    const asks = [
      'दाखवा',
      'काढा',
      'जवळून',
      'उपटून',
      'फोडून',
      'दाखवायला',
      'show',
      'record',
      'closer',
      'film',
    ];
    return asks.any(text.contains);
  }

  /// Called with each chunk of the assistant's speech. Set by the screen that owns
  /// the speaker.
  void Function(Uint8List pcm)? onAudio;

  Future<LiveOutcome> _finish(String why, Object? error) async {
    _ceiling?.cancel();
    _ceiling = null;
    await _incoming?.cancel();
    _incoming = null;
    await _channel?.close();
    _channel = null;

    final seconds = _openedAt == null
        ? 0
        : DateTime.now().difference(_openedAt!).inSeconds;
    _openedAt = null;

    if (error != null) Faults.note('live.session', error);

    // Reported best effort. A handset that cannot report leaves the session
    // uncounted, which gives the farmer slightly more than their allowance -- the
    // right way to be wrong, since overcounting refuses time nobody used.
    if (seconds > 0) {
      unawaited(
        api.postJson('/api/v1/live/session/end', {
          'farmer_id': farmerId,
          if (reportId != null) 'report_id': reportId,
          'seconds': seconds,
          'ended_because': why,
        }),
      );
    }

    _set(LiveState.ended);
    return LiveOutcome(
      seconds: seconds,
      endedBecause: why,
      askedFor: List.unmodifiable(_askedFor),
    );
  }

  void _send(Map<String, Object?> event) {
    if (_channel == null) return;
    _channel!.sendText(jsonEncode(event));
  }

  void _unavailable(String why) {
    _unavailableBecause = why;
    _set(LiveState.unavailable);
  }

  void _set(LiveState next) {
    if (_state == next) return;
    _state = next;
    notifyListeners();
  }

  @override
  void dispose() {
    _ceiling?.cancel();
    unawaited(_incoming?.cancel());
    unawaited(_channel?.close());
    super.dispose();
  }
}

/// The socket, behind an interface.
///
/// So a conversation can be held in a test with no network and no model. The real one
/// is a WebSocket; the relay is written so this can become WebRTC without anything
/// above it changing.
abstract class LiveChannel {
  Stream<dynamic> get stream;
  void sendBytes(List<int> data);
  void sendText(String text);
  Future<void> close();
}

class _WebSocketChannelAdapter implements LiveChannel {
  _WebSocketChannelAdapter(this._channel);

  final WebSocketChannel _channel;

  @override
  Stream<dynamic> get stream => _channel.stream;

  @override
  void sendBytes(List<int> data) => _channel.sink.add(data);

  @override
  void sendText(String text) => _channel.sink.add(text);

  @override
  Future<void> close() => _channel.sink.close();
}

/// Open the WebSocket transport.
///
/// Exported rather than private so the WebRTC path can fall back to it without
/// reaching for an implementation detail: a farm behind a symmetric NAT, or on a
/// carrier that blocks UDP, keeps the assistant on a socket.
Future<LiveChannel> connectWebSocket(String url) async {
  final channel = WebSocketChannel.connect(Uri.parse(url));
  await channel.ready;
  return _WebSocketChannelAdapter(channel);
}
