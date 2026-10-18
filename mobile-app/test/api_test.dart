import 'dart:convert';

import 'package:esurvey/core/api.dart';
import 'package:esurvey/core/session.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';

/// The server keeps refresh tokens in families and treats a replayed one as a
/// stolen token, ending every session in the family. That is right on the
/// server, and it means the client must never refresh twice at once.
///
/// A queue draining after signal returns fires a dozen requests together. If
/// each sees a 401 and refreshes, eleven present a token already spent, the
/// server reads it as theft, and the farmer is signed out of a handset holding
/// the only copy of their claim. Most of this file is about that.

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

Future<Session> signedIn({
  Duration ttl = const Duration(minutes: 15),
  SecureStore? store,
}) async {
  final s = Session(storage: store ?? _FakeStore());
  await s.save(
    accessToken: 'access-1',
    accessTtl: ttl,
    refreshToken: 'refresh-1',
    principal: {'farmer_id': 'F1'},
  );
  return s;
}

String refreshBody({String access = 'access-2', String? refresh}) =>
    jsonEncode({
      'access_token': access,
      'expires_in': 900,
      'refresh_token': ?refresh,
      'user': {'farmer_id': 'F1'},
    });

void main() {
  group('a draining queue refreshes once', () {
    test('ten concurrent 401s produce one refresh', () async {
      // The case that ends a farmer's session if it is wrong.
      var refreshes = 0;
      var protectedCalls = 0;

      final client = MockClient((req) async {
        if (req.url.path.endsWith('/auth/refresh')) {
          refreshes++;
          // A real refresh is not instant, which is what gives the other
          // callers time to pile in.
          await Future<void>.delayed(const Duration(milliseconds: 40));
          return http.Response(refreshBody(), 200);
        }
        protectedCalls++;
        final auth = req.headers['Authorization'];
        if (auth == 'Bearer access-1') return http.Response('{}', 401);
        return http.Response('{"ok":true}', 200);
      });

      final api = Api(session: await signedIn(), client: client);

      final results = await Future.wait([
        for (var i = 0; i < 10; i++) api.get('/api/v1/reports'),
      ]);

      expect(
        refreshes,
        1,
        reason: 'a second refresh presents a spent token and reads as theft',
      );
      expect(results.every((r) => r.ok), isTrue);
      // Ten first attempts that 401, one refresh, ten retries.
      expect(protectedCalls, 20);
    });

    test(
      'a token known to be expired is refreshed before it is spent',
      () async {
        // Rather than paying a round trip to be told what the clock already
        // said. On a rural connection that is seconds per request.
        var refreshes = 0;
        var unauthorised = 0;

        final client = MockClient((req) async {
          if (req.url.path.endsWith('/auth/refresh')) {
            refreshes++;
            return http.Response(refreshBody(), 200);
          }
          if (req.headers['Authorization'] == 'Bearer access-1') unauthorised++;
          return http.Response('{"ok":true}', 200);
        });

        final api = Api(
          session: await signedIn(ttl: const Duration(seconds: -1)),
          client: client,
        );
        final r = await api.get('/api/v1/reports');

        expect(r.ok, isTrue);
        expect(refreshes, 1);
        expect(
          unauthorised,
          0,
          reason: 'the expired token should never have been sent',
        );
      },
    );

    test('a later request after a failed refresh tries again', () async {
      // A failed refresh must not wedge every future request on a future that
      // will never succeed.
      var refreshes = 0;
      final client = MockClient((req) async {
        if (req.url.path.endsWith('/auth/refresh')) {
          refreshes++;
          // Server having a bad day, twice, then recovering.
          if (refreshes < 3) return http.Response('{}', 503);
          return http.Response(refreshBody(), 200);
        }
        return http.Response('{"ok":true}', 200);
      });

      final session = await signedIn(ttl: const Duration(seconds: -1));
      final api = Api(session: session, client: client);

      expect((await api.get('/a')).offline, isTrue);
      expect((await api.get('/b')).offline, isTrue);
      expect((await api.get('/c')).ok, isTrue);
      expect(refreshes, 3);
      expect(
        session.signedIn,
        isTrue,
        reason: 'a 5xx on refresh is the server, not a logout',
      );
    });
  });

  group('what signs a farmer out, and what does not', () {
    test('a refused refresh token is the only logout', () async {
      var signedOut = false;
      final client = MockClient((req) async {
        if (req.url.path.endsWith('/auth/refresh')) {
          return http.Response('{}', 401);
        }
        return http.Response('{}', 401);
      });

      final session = await signedIn(ttl: const Duration(seconds: -1));
      final api = Api(
        session: session,
        client: client,
        onSignedOut: () => signedOut = true,
      );

      final r = await api.get('/api/v1/reports');

      expect(r.offline, isTrue);
      expect(signedOut, isTrue);
      expect(session.signedIn, isFalse);
    });

    test('no network is not a logout', () async {
      // The ordinary state of a handset in a field.
      final client = MockClient((req) async {
        throw http.ClientException('Failed host lookup');
      });

      var signedOut = false;
      final session = await signedIn();
      final api = Api(
        session: session,
        client: client,
        onSignedOut: () => signedOut = true,
      );

      final r = await api.get('/api/v1/reports');

      expect(r.offline, isTrue);
      expect(signedOut, isFalse);
      expect(
        session.signedIn,
        isTrue,
        reason: 'a farmer with no signal is not signed out',
      );
    });

    test('a server error is not a logout', () async {
      final client = MockClient((req) async => http.Response('{}', 500));

      var signedOut = false;
      final session = await signedIn();
      final api = Api(
        session: session,
        client: client,
        onSignedOut: () => signedOut = true,
      );

      final r = await api.get('/api/v1/reports');

      expect(r.status, 500);
      expect(r.permanent, isFalse, reason: 'a 500 is worth retrying');
      expect(signedOut, isFalse);
      expect(session.signedIn, isTrue);
    });
  });

  group('a rotated refresh token is kept', () {
    test('the new one replaces the old', () async {
      final client = MockClient((req) async {
        if (req.url.path.endsWith('/auth/refresh')) {
          return http.Response(refreshBody(refresh: 'refresh-2'), 200);
        }
        return http.Response('{"ok":true}', 200);
      });

      final session = await signedIn(ttl: const Duration(seconds: -1));
      await Api(session: session, client: client).get('/x');

      expect(session.refreshToken, 'refresh-2');
      expect(session.accessToken, 'access-2');
    });

    test('a refresh with no new refresh token keeps the old one', () async {
      final client = MockClient((req) async {
        if (req.url.path.endsWith('/auth/refresh')) {
          return http.Response(refreshBody(), 200);
        }
        return http.Response('{"ok":true}', 200);
      });

      final session = await signedIn(ttl: const Duration(seconds: -1));
      await Api(session: session, client: client).get('/x');

      expect(session.refreshToken, 'refresh-1');
      expect(session.signedIn, isTrue);
    });
  });

  group('one refresh, one retry', () {
    test('a token refused twice is not retried forever', () async {
      // A loop here would hammer a server that has already said no.
      var calls = 0;
      final client = MockClient((req) async {
        if (req.url.path.endsWith('/auth/refresh')) {
          return http.Response(refreshBody(), 200);
        }
        calls++;
        return http.Response('{}', 401);
      });

      final api = Api(session: await signedIn(), client: client);
      final r = await api.get('/api/v1/reports');

      expect(calls, 2, reason: 'the first attempt and exactly one retry');
      expect(r.status, 401);
    });
  });

  group('what a caller can act on', () {
    test('offline is distinguished from declined', () async {
      final offline = MockClient((req) async {
        throw http.ClientException('no route to host');
      });
      final declined = MockClient(
        (req) async => http.Response(
          jsonEncode({
            'error': {'code': 'BAD_BOUNDARY', 'message': 'only two corners'},
          }),
          422,
        ),
      );

      final a = await Api(session: await signedIn(), client: offline).get('/x');
      final b = await Api(
        session: await signedIn(),
        client: declined,
      ).get('/x');

      expect(a.offline, isTrue);
      expect(a.permanent, isFalse, reason: 'keep the work and try later');

      expect(b.offline, isFalse);
      expect(b.permanent, isTrue, reason: 'no retry changes two corners');

      // The server's own wording is kept, and kept off the screen. It is
      // English-only and written for a program; a farmer gets the same meaning
      // in both languages, chosen from the stable code.
      expect(b.detail, 'only two corners');
      expect(b.code, 'BAD_BOUNDARY');
      expect(b.message, isNot(contains('only two corners')));
      expect(b.message, contains('·'), reason: 'bilingual, like every other');

      // Offline and declined must still read differently, or a farmer cannot
      // tell "keep walking, this is saved" from "this was refused".
      expect(a.message, isNot(b.message));
    });

    test('a rate limit is not permanent', () async {
      final client = MockClient((req) async => http.Response('{}', 429));
      final r = await Api(session: await signedIn(), client: client).get('/x');

      expect(
        r.permanent,
        isFalse,
        reason: 'a 429 is the server asking for patience, not refusing',
      );
    });

    test('an HTML error page does not become the message', () async {
      // A proxy in front of the API returns markup, and it would otherwise
      // land in the last_error column of every queued row.
      final client = MockClient(
        (req) async => http.Response('<html><body>502</body></html>', 502),
      );
      final r = await Api(session: await signedIn(), client: client).get('/x');

      expect(r.body, isNull);

      // Neither the markup nor the number reaches the farmer. "502" is not a
      // thing anybody standing in a field can act on; the sentence says the
      // service is down and to try later, which is the whole of the advice.
      expect(r.message, isNot(contains('502')));
      expect(r.message, isNot(contains('<')));
      expect(r.message, contains('·'));
      expect(r.message.length, lessThan(80));
    });
  });

  group('a signed-out app', () {
    test('does not try to refresh nothing', () async {
      var refreshes = 0;
      final client = MockClient((req) async {
        if (req.url.path.endsWith('/auth/refresh')) refreshes++;
        return http.Response('{}', 401);
      });

      final session = Session(storage: _FakeStore());
      await session.load();

      var signedOut = false;
      final api = Api(
        session: session,
        client: client,
        onSignedOut: () => signedOut = true,
      );
      final r = await api.get('/x');

      expect(r.status, 401);
      expect(refreshes, 0, reason: 'there is no refresh token to present');
      expect(
        signedOut,
        isFalse,
        reason: 'already signed out; nothing to announce',
      );
    });
  });
}
