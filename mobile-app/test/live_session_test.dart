import 'dart:async';
import 'dart:convert';
import 'dart:io';
import 'dart:typed_data';

import 'package:esurvey/core/api.dart';
import 'package:esurvey/core/db.dart';
import 'package:esurvey/core/link_quality.dart';
import 'package:esurvey/core/live_consent.dart';
import 'package:esurvey/live/live_session.dart';
import 'package:esurvey/live/webrtc_channel.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:http/testing.dart';
import 'package:http/http.dart' as http;
import 'package:esurvey/core/session.dart';
import 'package:sqflite_common_ffi/sqflite_ffi.dart';

/// A conversation, held with no network and no model.
///
/// The gates are the interesting part. Each is a different way talking can be the
/// wrong thing to do, and failing any of them has to end in the same place: the
/// scripted guidance, which needs no network, no model and no permission beyond the
/// camera. If a gate could ever leave the farmer worse off than not offering the
/// assistant at all, the design has failed.

/// A secure store that keeps things in memory, as the other suites do.
class _FakeStore implements SecureStore {
  final Map<String, String> values = {};

  @override
  Future<String?> read(String key) async => values[key];

  @override
  Future<void> write(String key, String value) async => values[key] = value;

  @override
  Future<void> delete(String key) async => values.remove(key);

  @override
  Future<void> deleteAll() async => values.clear();
}

Future<Session> signedIn() async {
  final s = Session(storage: _FakeStore());
  await s.save(
    accessToken: 'a',
    accessTtl: const Duration(minutes: 15),
    refreshToken: 'r',
    principal: {'farmer_id': 'F-1'},
  );
  return s;
}

late Directory _tmp;

Future<Store> freshStore() async {
  final dir = Directory('${_tmp.path}/${DateTime.now().microsecondsSinceEpoch}')
    ..createSync(recursive: true);
  return Store.open(directory: dir.path);
}

/// A channel the test drives directly.
class FakeChannel implements LiveChannel {
  final _in = StreamController<dynamic>.broadcast();
  final sentBytes = <List<int>>[];
  final sentText = <String>[];
  bool closed = false;

  @override
  Stream<dynamic> get stream => _in.stream;

  @override
  void sendBytes(List<int> data) => sentBytes.add(data);

  @override
  void sendText(String text) => sentText.add(text);

  @override
  Future<void> close() async {
    closed = true;
    if (!_in.isClosed) await _in.close();
  }

  /// What the relay says.
  void say(Map<String, Object?> event) => _in.add(jsonEncode(event));

  /// The assistant's voice.
  void speak(List<int> pcm) => _in.add(pcm);
}

/// An office that grants sessions.
MockClient granting({
  int status = 200,
  String url = 'ws://relay/api/v1/live/session',
  int maxSeconds = 480,
  List<String>? ended,
}) => MockClient((req) async {
  if (req.url.path.endsWith('/live/session/end')) {
    ended?.add(req.body);
    return http.Response('', 204);
  }
  if (req.url.path.endsWith('/live/session')) {
    if (status != 200) return http.Response('{}', status);
    return http.Response(
      jsonEncode({'url': url, 'max_seconds': maxSeconds, 'model': 'test'}),
      200,
    );
  }
  return http.Response('{}', 200);
});

Future<LiveSession> sessionWith({
  required Store store,
  MockClient? client,
  FakeChannel? channel,
}) async {
  return LiveSession(
    api: Api(session: await signedIn(), client: client ?? granting()),
    consent: LiveConsent(store),
    probe: LinkProbe(
      api: Api(session: await signedIn(), client: granting()),
    ),
    farmerId: 'F-1',
    connect: (_) async => channel ?? FakeChannel(),
  );
}

void main() {
  setUpAll(() {
    sqfliteFfiInit();
    databaseFactory = databaseFactoryFfi;
    _tmp = Directory.systemTemp.createTempSync('esurvey-live-test');
  });

  tearDownAll(() {
    try {
      _tmp.deleteSync(recursive: true);
    } catch (_) {
      // Windows holds handles briefly after close.
    }
  });

  group('the gates before a word is spoken', () {
    test('a build without it enabled never offers', () async {
      // liveGuidanceEnabled is a compile-time define and is off by default, so a
      // trial runs without any of this and a handset cannot be surprised by a
      // feature nobody tested there.
      final store = await freshStore();
      final s = await sessionWith(store: store);

      expect(await s.offer(), isFalse);
      expect(s.state, LiveState.unavailable);
      expect(s.unavailableBecause, contains('not enabled'));

      await store.close();
    });

    test('a farmer who has not agreed is not offered it', () async {
      // Checked before the link is measured, because there is no reason to spend a
      // farmer's data finding out whether a connection could carry something they
      // have declined.
      final store = await freshStore();
      final s = await sessionWith(store: store);

      expect(await s.offer(), isFalse);
      // The build flag refuses first here, which is itself the ordering being
      // asserted: the cheap local checks come before the measurement.
      expect(s.state, LiveState.unavailable);

      await store.close();
    });
  });

  group('holding a conversation', () {
    test('the farmer speech goes as bytes, not base64', () async {
      // The continuous half of the traffic. Base64 would add a third to it for
      // nothing on a link that has a quarter of a megabit.
      final store = await freshStore();
      final channel = FakeChannel();
      final s = await sessionWith(store: store, channel: channel);

      await s.start();
      s.sendAudio(Uint8List.fromList([1, 2, 3, 4]));

      expect(channel.sentBytes, [
        [1, 2, 3, 4],
      ]);
      expect(channel.sentText.where((t) => t.contains('audio')), isEmpty);

      await store.close();
    });

    test('a frame goes only when something asks for one', () async {
      // Never on a timer. A frame every three seconds is eighty kilobits and would
      // dominate the budget; a frame when one is wanted is a few kilobits averaged
      // out, and it is the largest saving available on a rural link.
      final store = await freshStore();
      final channel = FakeChannel();
      final s = await sessionWith(store: store, channel: channel);

      await s.start();
      expect(
        channel.sentText.where((t) => t.contains('frame')),
        isEmpty,
        reason: 'nothing sends a frame on its own',
      );

      s.sendFrame(Uint8List.fromList([0xFF, 0xD8, 0x00]));
      expect(channel.sentText.where((t) => t.contains('frame')), hasLength(1));

      await store.close();
    });

    test('what the assistant says is kept for the screen', () async {
      // A farmer in a noisy field may not have heard it.
      final store = await freshStore();
      final channel = FakeChannel();
      final s = await sessionWith(store: store, channel: channel);

      await s.start();
      channel.say({'type': 'said', 'text': 'शेंगा फोडून दाखवा'});
      await Future<void>.delayed(Duration.zero);

      expect(s.said, 'शेंगा फोडून दाखवा');

      await store.close();
    });

    test('a request to show something is remembered for the next clip', () async {
      // The whole point of the guidance: the next clip answers the last request, and
      // is stamped with it so the assessment can tell a directed observation from
      // the farmer own selection.
      final store = await freshStore();
      final channel = FakeChannel();
      final s = await sessionWith(store: store, channel: channel);

      await s.start();
      channel.say({'type': 'said', 'text': 'आता पुढच्या ठिकाणी जा'});
      channel.say({'type': 'said', 'text': 'शेंगा फोडून आतले दाणे दाखवा'});
      await Future<void>.delayed(Duration.zero);

      expect(s.latestRequest, 'शेंगा फोडून आतले दाणे दाखवा');
      expect(
        s.askedFor,
        hasLength(1),
        reason: 'a line that is not a request is not stored against a clip',
      );

      await store.close();
    });

    test('the requests only ever grow, and keep the order they were made in', () async {
      // The walk keeps its place in this list by counting how many of them it has
      // already passed on, which is only correct while nothing is ever removed or
      // reordered. A list that dropped an entry would have a clip stamped with the
      // wrong request -- an officer reading it a year later would believe the farmer
      // was asked to show something they were not -- and one the walk could mutate
      // would let a screen quietly rewrite what the assistant said.
      final store = await freshStore();
      final channel = FakeChannel();
      final s = await sessionWith(store: store, channel: channel);

      await s.start();
      channel.say({'type': 'said', 'text': 'शेंगा फोडून दाखवा'});
      await Future<void>.delayed(Duration.zero);
      final first = s.askedFor.toList();

      channel.say({'type': 'said', 'text': 'जवळून दाखवा'});
      await Future<void>.delayed(Duration.zero);

      expect(s.askedFor.take(first.length), first);
      expect(s.askedFor, hasLength(2));
      expect(() => s.askedFor.add('never said'), throwsUnsupportedError);

      await store.close();
    });

    test('the relay ending it ends the session here', () async {
      final store = await freshStore();
      final channel = FakeChannel();
      final s = await sessionWith(store: store, channel: channel);

      await s.start();
      channel.say({'type': 'ended', 'reason': 'session limit'});
      await Future<void>.delayed(Duration.zero);

      expect(s.state, LiveState.ended);

      await store.close();
    });
  });

  group('what it costs is reported', () {
    test('a finished session tells the office how long it ran', () async {
      // Reported from the handset because this knows which farmer and which claim it
      // was for. A relay told those would hold identifying data it has no other use
      // for.
      final store = await freshStore();
      final ended = <String>[];
      final channel = FakeChannel();
      final s = await sessionWith(
        store: store,
        client: granting(ended: ended),
        channel: channel,
      );

      await s.start();
      await Future<void>.delayed(const Duration(milliseconds: 1100));
      final outcome = await s.stop('closed');

      expect(outcome.seconds, greaterThanOrEqualTo(1));

      // The report is deliberately not awaited in production -- a farmer who has
      // finished walking must not be held on a request -- so the test waits for it
      // rather than assuming it has already gone.
      for (var i = 0; i < 50 && ended.isEmpty; i++) {
        await Future<void>.delayed(const Duration(milliseconds: 10));
      }
      expect(ended, isNotEmpty, reason: 'the office was never told');
      expect(ended.first, contains('"farmer_id":"F-1"'));

      await store.close();
    });

    test('a session that never opened reports nothing', () async {
      // It cost nothing, and a row for it would count against a farmer allowance
      // for a conversation that did not happen.
      final store = await freshStore();
      final ended = <String>[];
      final s = await sessionWith(
        store: store,
        client: granting(ended: ended),
      );

      await s.stop('never started');

      expect(ended, isEmpty);

      await store.close();
    });
  });

  group('which transport carries it', () {
    // WebRTC is the primary transport rather than an optimisation, and this is the
    // arithmetic. Raw PCM at 16 kHz 16-bit mono is 256 kbit/s going up, and the
    // model's reply at 24 kHz is 384 coming down, so a socket needs something near
    // 640 kbit/s before a word is exchanged. The link this feature exists for has 256
    // in total; Opus over WebRTC is nearer 24 each way.
    //
    // Loss is the other half. A socket is TCP and will not deliver the fourth packet
    // until the third arrives, so a few per cent loss on a rural link becomes
    // silence, then a burst, then more silence, which a farmer reads as the app being
    // broken. They are not wrong to.
    //
    // What these pin: openBestTransport was written, tested and never called. The
    // session's default was the socket, so every conversation took the path that
    // cannot work at 256 kbit/s while twelve megabytes of native WebRTC sat unused in
    // the APK.

    test('over the socket the microphone is ours to hold', () async {
      // Nothing else will capture and play raw PCM, so the screen starts a recorder.
      final store = await freshStore();
      final s = await sessionWith(store: store);
      s.transport = LiveTransportKind.webSocket;
      expect(s.audioIsOurs, isTrue);
    });

    test('over WebRTC the platform holds it instead', () async {
      // The decisive one. The native stack captures, encodes and sends the microphone
      // and decodes and plays the reply, so a screen that also started its own
      // capture would be a second claimant on one microphone -- and Android gives it
      // to neither. The recorder fails or the track goes silent, and both look
      // exactly like an assistant that is not listening.
      final store = await freshStore();
      final s = await sessionWith(store: store);
      s.transport = LiveTransportKind.webRtc;
      expect(s.audioIsOurs, isFalse);
    });

    test('and before a transport is open it belongs to nobody', () async {
      // Read too early this is null, and a screen acting on it would start a recorder
      // on the strength of nothing. It is why the audio setup happens after start()
      // rather than before it.
      final store = await freshStore();
      final s = await sessionWith(store: store);
      expect(s.transport, isNull);
      expect(s.audioIsOurs, isFalse);
    });
  });

  group('being refused', () {
    test('over budget is said as its own thing', () async {
      // The farmer is told the assistant is finished for today, not that something
      // went wrong: the walk is unaffected and nothing about the claim has changed.
      final store = await freshStore();
      final s = await sessionWith(store: store, client: granting(status: 429));

      expect(await s.start(), isFalse);
      expect(s.state, LiveState.unavailable);
      expect(s.unavailableBecause, contains('मर्यादा'));

      await store.close();
    });

    test('an office that cannot grant one is said as unavailability', () async {
      final store = await freshStore();
      final s = await sessionWith(store: store, client: granting(status: 503));

      expect(await s.start(), isFalse);
      expect(s.unavailableBecause, contains('उपलब्ध नाही'));

      await store.close();
    });

    test('a socket that refuses to open does not throw at the farmer', () async {
      // A farmer standing in a field cannot act on an exception. Every failure here
      // ends as the scripted guidance, which is what the walk uses anyway.
      final store = await freshStore();
      final s = LiveSession(
        api: Api(session: await signedIn(), client: granting()),
        consent: LiveConsent(store),
        probe: LinkProbe(
          api: Api(session: await signedIn(), client: granting()),
        ),
        farmerId: 'F-1',
        connect: (_) async => throw const SocketException('refused'),
      );

      expect(await s.start(), isFalse);
      expect(s.state, LiveState.unavailable);

      await store.close();
    });
  });
}
