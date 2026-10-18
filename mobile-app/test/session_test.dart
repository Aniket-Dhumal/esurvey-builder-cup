import 'dart:convert';

import 'package:esurvey/core/session.dart';
import 'package:flutter_test/flutter_test.dart';

/// A farmer standing in a field with a dead signal, halfway through
/// photographing their damage, whose access token expires.
///
/// That is the case this file exists for. Getting it wrong means a sign-in
/// screen asking for an OTP that needs the network they do not have, with the
/// only copy of their claim sitting on the handset behind it.

/// An in-memory stand-in for the keystore.
///
/// The real one needs a platform channel and cannot run in a unit test, and
/// what is under test here is the session's reasoning rather than Android's
/// encryption.
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

void main() {
  late _FakeStore storage;
  late Session session;

  setUp(() {
    storage = _FakeStore();
    session = Session(storage: storage);
  });

  Future<void> signIn({Duration ttl = const Duration(minutes: 15)}) async {
    await session.save(
      accessToken: 'access-1',
      accessTtl: ttl,
      refreshToken: 'refresh-1',
      principal: {'farmer_id': 'F1', 'full_name': 'सुनीता पवार'},
    );
  }

  group('an expired access token is not a logout', () {
    test('a farmer with an expired token is still signed in', () async {
      // The whole point. Their photographs are on the phone, unsent, and a
      // sign-in screen would ask for an OTP over a network they do not have.
      await signIn(ttl: const Duration(seconds: -1));

      expect(
        session.accessUsable,
        isFalse,
        reason: 'the token has expired and cannot be used for a request',
      );
      expect(
        session.signedIn,
        isTrue,
        reason: 'an expired access token must not sign a farmer out',
      );
    });

    test('only a missing refresh token is a logout', () async {
      await signIn();
      expect(session.signedIn, isTrue);

      await session.end();
      expect(session.signedIn, isFalse);
    });

    test('a spent access token can be forgotten without signing out', () async {
      // What a 401 means when the refresh token is still good: this
      // particular token is spent, and the queue keeps working.
      await signIn();
      await session.forgetAccess();

      expect(session.accessUsable, isFalse);
      expect(session.signedIn, isTrue);
      expect(session.refreshToken, 'refresh-1');
    });
  });

  group('the token is treated as expiring slightly early', () {
    test('a token with seconds left is not used', () async {
      // It would expire mid-upload on a slow rural connection, and a refused
      // upload costs a whole retry cycle.
      await signIn(ttl: const Duration(seconds: 20));
      expect(session.accessUsable, isFalse);
    });

    test('a fresh token is used', () async {
      await signIn(ttl: const Duration(minutes: 15));
      expect(session.accessUsable, isTrue);
    });
  });

  group('what survives the app being closed', () {
    test('a signed-in farmer is still signed in after a restart', () async {
      // A farmer files one claim a year. Being asked to sign in again because
      // the app was killed by the OS overnight is where they stop.
      await signIn();

      final restarted = Session(storage: storage);
      await restarted.load();

      expect(restarted.signedIn, isTrue);
      expect(restarted.farmerId, 'F1');
      expect(restarted.name, 'सुनीता पवार');
    });

    test('who they are is readable with no signal at all', () async {
      await signIn();

      final restarted = Session(storage: storage);
      await restarted.load();

      // The screen can greet them and knows their farmer id without a single
      // request having been made.
      expect(restarted.principal, isNotNull);
      expect(restarted.farmerId, isNotNull);
    });

    test(
      'an expired token survives a restart as expired, not as absent',
      () async {
        await signIn(ttl: const Duration(seconds: -1));

        final restarted = Session(storage: storage);
        await restarted.load();

        expect(restarted.signedIn, isTrue);
        expect(restarted.accessUsable, isFalse);
      },
    );

    test('load is safe to call more than once', () async {
      await signIn();
      final s = Session(storage: storage);
      await s.load();
      await s.load();
      expect(s.farmerId, 'F1');
    });
  });

  group('signing out', () {
    test('leaves nothing behind in the keystore', () async {
      // A handset in a village is borrowed.
      await signIn();
      expect(storage.values, isNotEmpty);

      await session.end();
      expect(storage.values, isEmpty);
      expect(session.principal, isNull);
      expect(session.accessToken, isNull);
    });
  });

  group('a fresh install', () {
    test('is signed out and says so without throwing', () async {
      final s = Session(storage: _FakeStore());
      await s.load();

      expect(s.signedIn, isFalse);
      expect(s.accessUsable, isFalse);
      expect(s.farmerId, isNull);
    });
  });

  group('refreshing', () {
    test('a new access token does not require a new refresh token', () async {
      // The ordinary refresh: the server hands back an access token and keeps
      // the family. Requiring both would drop the session on every refresh
      // that did not rotate.
      await signIn();
      await session.save(
        accessToken: 'access-2',
        accessTtl: const Duration(minutes: 15),
      );

      expect(session.accessToken, 'access-2');
      expect(session.refreshToken, 'refresh-1');
      expect(session.signedIn, isTrue);
    });

    test('a rotated refresh token replaces the old one', () async {
      await signIn();
      await session.save(
        accessToken: 'access-2',
        accessTtl: const Duration(minutes: 15),
        refreshToken: 'refresh-2',
      );

      expect(session.refreshToken, 'refresh-2');
      expect(
        jsonDecode(storage.values['principal']!)['farmer_id'],
        'F1',
        reason: 'a rotation must not lose who the farmer is',
      );
    });
  });
}
