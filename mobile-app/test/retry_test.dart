import 'dart:io';
import 'dart:math' as math;

import 'package:esurvey/core/api.dart';
import 'package:esurvey/core/db.dart';
import 'package:esurvey/core/retry.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;
import 'package:path/path.dart' as p;
import 'package:sqflite_common_ffi/sqflite_ffi.dart';

/// What an answer from the office means, and how long work waits after one.
///
/// These are the decisions that decide whether a farmer's claim is ever lost,
/// so each is pinned down on its own rather than only through a sync pass.

Result answer(int status, {String? body, Map<String, String>? headers}) =>
    Result.of(http.Response(body ?? '{}', status, headers: headers ?? {}));

Result conflict(String code) =>
    answer(409, body: '{"error":{"code":"$code","message":"x"}}');

void main() {
  group('an answer', () {
    test('that never arrived is offline, not a failure', () {
      expect(classify(const Result.offline()), Verdict.offline);
    });

    test('that accepted the work is sent, replayed or not', () {
      expect(classify(answer(201)), Verdict.sent);
      expect(
        classify(answer(200, headers: {'idempotency-replayed': 'true'})),
        Verdict.sent,
      );
      expect(
        answer(200, headers: {'idempotency-replayed': 'true'}).replayed,
        isTrue,
      );
    });

    test('from a failing or overloaded office is worth trying again', () {
      for (final status in [500, 502, 503, 504, 408, 425, 429]) {
        expect(classify(answer(status)), Verdict.transient, reason: '$status');
      }
    });

    test('asking the phone to slow down, with a time, pauses everything', () {
      expect(
        classify(answer(429, headers: {'retry-after': '30'})),
        Verdict.throttled,
      );
      expect(
        classify(answer(503, headers: {'retry-after': '30'})),
        Verdict.throttled,
      );
    });

    test('saying the build is too old stops the queue, not the claim', () {
      expect(classify(answer(426)), Verdict.upgradeRequired);
    });

    test('a 409 that means "not yet" is retried; any other is a refusal', () {
      expect(classify(conflict('IDEMPOTENCY_IN_PROGRESS')), Verdict.transient);
      expect(classify(conflict('UPLOAD_RESTARTED')), Verdict.transient);
      expect(classify(conflict('IDEMPOTENCY_KEY_REUSED')), Verdict.permanent);
      expect(classify(conflict('DUPLICATE_IMAGE')), Verdict.permanent);
    });

    test('an upload the server swept is tried again', () {
      expect(
        classify(
          answer(
            404,
            body: '{"error":{"code":"UPLOAD_NOT_FOUND","message":"x"}}',
          ),
        ),
        Verdict.transient,
      );
      expect(
        classify(answer(404)),
        Verdict.permanent,
        reason: 'any other 404 is still a refusal',
      );
    });

    test('that refused the work is permanent', () {
      // 401 and 403 used to be in this list. They are refusals of the *phone*,
      // not of the work, and permanent marks a capture failed -- terminal, never
      // retried, the farmer's recording gone.
      for (final status in [400, 404, 413, 422]) {
        expect(classify(answer(status)), Verdict.permanent, reason: '$status');
      }
    });

    test('that would not say who the phone is keeps the work', () {
      // A 401 is a token the server would not accept; a 403 is a grant it did
      // not find. The bytes are fine in both cases and are acceptable the moment
      // somebody signs in or the grant is issued.
      //
      // This was not reachable, and only just: a 401 is caught a layer up, where
      // a failed refresh is reported as offline, and offline stops the pass
      // before a second item is tried with a dead token. Three separate facts
      // holding at once -- and the trial had a grant bug producing exactly these
      // codes on a neighbouring route, so a capture that met it would have been
      // abandoned with no way back.
      for (final status in [401, 403]) {
        expect(
          classify(answer(status)),
          Verdict.unauthorized,
          reason: '$status must never be a verdict on the data',
        );
      }
    });
  });

  group('Retry-After', () {
    test('reads seconds', () {
      expect(parseRetryAfter('120'), const Duration(minutes: 2));
      expect(parseRetryAfter(' 5 '), const Duration(seconds: 5));
    });

    test('reads an HTTP date', () {
      final now = DateTime.utc(2026, 9, 14, 10);
      expect(
        parseRetryAfter('Mon, 14 Sep 2026 10:05:00 GMT', now: now),
        const Duration(minutes: 5),
      );
      expect(
        parseRetryAfter('Mon, 14 Sep 2026 09:00:00 GMT', now: now),
        Duration.zero,
        reason: 'a date already past means now',
      );
    });

    test('ignores what it cannot read rather than guessing', () {
      expect(parseRetryAfter(null), isNull);
      expect(parseRetryAfter(''), isNull);
      expect(parseRetryAfter('soon'), isNull);
    });

    test('cannot park a queue for more than a day', () {
      expect(parseRetryAfter('9999999'), const Duration(days: 1));
    });
  });

  group('the wait before trying again', () {
    const policy = RetryPolicy();

    test('has a floor, so a crowd of phones cannot retry at once', () {
      for (var seed = 0; seed < 50; seed++) {
        final d = policy.delay(0, random: math.Random(seed));
        expect(d, greaterThanOrEqualTo(policy.base ~/ 2));
        expect(d, lessThanOrEqualTo(policy.base));
      }
    });

    test('doubles until the cap and stays there', () {
      Duration most(int attempts) =>
          policy.delay(attempts, random: _Fixed(1.0));
      expect(most(1), policy.base * 2);
      expect(most(3), policy.base * 8);
      expect(most(30), policy.cap);
      expect(most(1000), policy.cap, reason: 'no overflow on a long outage');
    });

    test('a shorter Retry-After does not cut the wait short', () {
      final d = policy.delay(
        6,
        retryAfter: const Duration(seconds: 1),
        random: _Fixed(0),
      );
      expect(d, greaterThan(const Duration(seconds: 1)));
    });
  });

  group('upgrading from schema 3', () {
    late Directory tmp;

    setUpAll(() {
      sqfliteFfiInit();
      databaseFactory = databaseFactoryFfi;
      tmp = Directory.systemTemp.createTempSync('esurvey-retry-test');
    });

    tearDownAll(() {
      try {
        tmp.deleteSync(recursive: true);
      } catch (_) {
        // Windows holds handles briefly after close.
      }
    });

    test('brings back the claims the old attempt budget abandoned', () async {
      final dir = Directory(p.join(tmp.path, 'v3'))..createSync();
      final store = await Store.open(directory: dir.path);
      await store.putDraft(_draft('abandoned'));
      await store.putDraft(_draft('refused'));
      await store.putDraft(_draft('sent'));
      await store.close();

      // Put the file back the way schema 3 left it: no keys, no due times,
      // one row stuck at the budget, one refused, one sent.
      final raw = await openDatabase(p.join(dir.path, 'esurvey.db'));
      await raw.execute("UPDATE drafts SET sync_key = NULL");
      await raw.execute(
        "UPDATE drafts SET attempts = 5, next_attempt_at = 0 "
        "WHERE local_id = 'abandoned'",
      );
      await raw.execute(
        "UPDATE drafts SET status = 'failed', attempts = 1 "
        "WHERE local_id = 'refused'",
      );
      await raw.execute(
        "UPDATE drafts SET status = 'sent', server_id = 'R9' "
        "WHERE local_id = 'sent'",
      );
      await raw.execute('DROP TABLE device');
      await raw.setVersion(3);
      await raw.close();

      final upgraded = await Store.open(directory: dir.path);
      final rows = {for (final r in await upgraded.drafts()) r['local_id']: r};

      expect(rows['abandoned']!['attempts'], 0);
      expect(
        (await upgraded.pendingDrafts()).map((r) => r['local_id']),
        ['abandoned'],
        reason: 'the abandoned claim goes; the refused and sent ones do not',
      );
      expect(rows['refused']!['status'], 'failed');
      expect(rows['sent']!['status'], 'sent');

      final keys = rows.values.map((r) => r['sync_key']).toSet();
      expect(keys, hasLength(3), reason: 'every row gets a key of its own');
      expect(keys.every((k) => k is String && k.startsWith('q')), isTrue);

      expect(await upgraded.deviceId(), isNotEmpty);
      await upgraded.close();
    });
  });
}

Map<String, Object?> _draft(String localId) => {
  'local_id': localId,
  'survey_event_id': 'E1',
  'farmer_id': 'F1',
  'season': 'kharif',
  'crop_year': '2026-27',
  'loss_category': 'standing_crop',
  'calamity_type': 'hailstorm',
  'occurred_at': '2026-08-01T10:00:00Z',
  'farm_id': '453',
  'survey_number': '453',
  'crop_name': 'Soybean',
  'area_sown_ha': 1.6,
  'area_affected_ha': 1.2,
  'damage_percent': 70.0,
  'declaration_accepted': 1,
  'signature_kind': 'thumb',
  'status': 'pending',
  'attempts': 0,
};

/// A random source that always gives the same fraction of its range.
class _Fixed implements math.Random {
  _Fixed(this.fraction);
  final double fraction;

  @override
  int nextInt(int max) => (fraction * (max - 1)).round().clamp(0, max - 1);

  @override
  double nextDouble() => fraction;

  @override
  bool nextBool() => fraction >= 0.5;
}
