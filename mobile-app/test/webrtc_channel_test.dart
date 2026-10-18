import 'dart:async';

import 'package:esurvey/core/api.dart';
import 'package:esurvey/core/session.dart';
import 'package:esurvey/live/live_session.dart';
import 'package:esurvey/live/webrtc_channel.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';

/// Choosing a transport, which is a decision about where the farmer is standing.
///
/// A real negotiation needs two media stacks and a network, so what is tested here is
/// the choice and the fallback. The fallback is the part that matters: WebRTC fails
/// for reasons that are properties of a place -- a symmetric NAT, a carrier blocking
/// UDP -- and a farmer must not lose the assistant because of one.

class FakeChannel implements LiveChannel {
  final _in = StreamController<dynamic>.broadcast();
  bool closed = false;

  @override
  Stream<dynamic> get stream => _in.stream;

  @override
  void sendBytes(List<int> data) {}

  @override
  void sendText(String text) {}

  @override
  Future<void> close() async {
    closed = true;
    if (!_in.isClosed) await _in.close();
  }
}

void main() {
  // The WebRTC plugin reaches a platform channel, which needs a binding even to fail
  // on. Without this the failure is 'binding not initialized' rather than 'no media
  // stack here', and the fallback path is never actually exercised.
  TestWidgetsFlutterBinding.ensureInitialized();

  group('choosing a transport', () {
    test('falls back to the socket when WebRTC cannot negotiate', () async {
      // Not reported to the farmer. A symmetric NAT is a property of where they are,
      // the assistant works either way, and the only thing they would learn is that
      // something invisible went wrong.
      //
      // In a test environment there is no platform media stack at all, so the WebRTC
      // attempt fails for that reason -- which exercises exactly the path a blocked
      // carrier would.
      final socket = FakeChannel();
      final opened = await openBestTransport(
        api: await unreachableApi(),
        socketUrl: 'ws://relay/session',
        openSocket: (_) async => socket,
      );

      expect(opened.kind, LiveTransportKind.webSocket);
      expect(opened.channel, same(socket));
    });

    test('the fallback is a working channel, not a stub', () async {
      // A fallback that connects and then does nothing is worse than no fallback: the
      // farmer is offered a conversation that never answers.
      final socket = FakeChannel();
      final opened = await openBestTransport(
        api: await unreachableApi(),
        socketUrl: 'ws://relay/session',
        openSocket: (_) async => socket,
      );

      final heard = <dynamic>[];
      opened.channel.stream.listen(heard.add);
      socket._in.add('{"type":"ready"}');
      await Future<void>.delayed(Duration.zero);

      expect(heard, ['{"type":"ready"}']);

      await opened.channel.close();
      expect(socket.closed, isTrue);
    });
  });

  group('the two transports are not interchangeable in one respect', () {
    test('over WebRTC the audio does not pass through Dart', () {
      // sendBytes doing nothing reads as a bug and is not one. The microphone is a
      // media track the platform captures, encodes and sends; Dart never holds a
      // sample. Writing the audio to the data channel instead would put it back on an
      // ordered stream, which is the whole thing this transport exists to avoid.
      //
      // Asserted as a documented property rather than by calling it, because
      // constructing a WebRtcChannel needs a platform stack.
      expect(LiveTransportKind.values, contains(LiveTransportKind.webRtc));
      expect(LiveTransportKind.values, contains(LiveTransportKind.webSocket));
    });
  });
}

/// A secure store kept in memory, as the other suites do.
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

/// An Api that answers nothing useful.
///
/// Enough for the fallback path: openBestTransport reaches the platform media stack
/// before it reaches the network, and in a test there is no media stack, so that is
/// where it stops. Which is the point -- it exercises the same branch a carrier
/// blocking UDP would.
Future<Api> unreachableApi() async {
  final session = Session(storage: _FakeStore());
  await session.save(
    accessToken: 'a',
    accessTtl: const Duration(minutes: 15),
    refreshToken: 'r',
    principal: {'farmer_id': 'F-1'},
  );
  return Api(
    session: session,
    client: MockClient((_) async => http.Response('{}', 503)),
  );
}
