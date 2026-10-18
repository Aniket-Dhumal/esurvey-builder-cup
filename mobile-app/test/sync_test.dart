import 'dart:convert';
import 'dart:io';
import 'dart:math' as math;

import 'package:esurvey/core/api.dart';
import 'package:esurvey/core/db.dart';
import 'package:esurvey/core/farmer_data.dart';
import 'package:esurvey/core/retry.dart';
import 'package:esurvey/core/session.dart';
import 'package:esurvey/core/sync.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';
import 'package:sqflite_common_ffi/sqflite_ffi.dart';

/// Getting what is on the phone to the office.
///
/// The ordering is the part worth testing hardest. A photograph is geofenced
/// against the parcel boundary when it is received, and that result is written
/// into the record then rather than re-evaluated later -- so a photograph that
/// lands before its boundary is checked against nothing, permanently. And a
/// photograph needs its report's server id, which only exists once the report
/// has landed.

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

late Directory _tmp;

Future<Store> freshStore() async {
  final dir = Directory('${_tmp.path}/${DateTime.now().microsecondsSinceEpoch}')
    ..createSync(recursive: true);
  return Store.open(directory: dir.path);
}

Future<Session> signedIn() async {
  final s = Session(storage: _FakeStore());
  await s.save(
    accessToken: 'access-1',
    accessTtl: const Duration(minutes: 15),
    refreshToken: 'refresh-1',
    principal: {'farmer_id': 'F1'},
  );
  return s;
}

/// A draft as the capture flow writes one.
Map<String, Object?> draftRow(String localId) => {
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
  'status': 'pending',
  'attempts': 0,
};

Future<String> photoOn(Directory dir, String name) async {
  final f = File('${dir.path}/$name')..writeAsBytesSync([1, 2, 3, 4]);
  return f.path;
}

Map<String, Object?> captureRow(
  String localId,
  String draftLocalId,
  String path,
) => {
  'local_id': localId,
  'draft_local_id': draftLocalId,
  'kind': 'damage_closeup',
  'stage': 'damage',
  'caption': 'Farmer-captured: The damage',
  'file_path': path,
  'content_type': 'image/jpeg',
  'byte_size': 4,
  'sha256': 'abc',
  'lat': 18.17,
  'lon': 75.81,
  'accuracy_m': 6.0,
  'captured_at': '2026-08-01T10:05:00Z',
  'device': 'android',
  'status': 'pending',
  'attempts': 0,
};

/// The value a multipart body carries for one field.
String? field(String body, String name) {
  final start = body.indexOf('name="$name"');
  if (start < 0) return null;
  const sep = '\r\n\r\n';
  final from = body.indexOf(sep, start);
  if (from < 0) return null;
  final end = body.indexOf('\r\n', from + sep.length);
  if (end < 0) return null;
  return body.substring(from + sep.length, end);
}

void main() {
  setUpAll(() {
    // sqflite needs a platform channel on a device. The FFI implementation is
    // the same SQLite in-process, which is what makes the queue's actual
    // behaviour testable rather than mocked.
    sqfliteFfiInit();
    databaseFactory = databaseFactoryFfi;
    _tmp = Directory.systemTemp.createTempSync('esurvey-sync-test');
  });

  tearDownAll(() {
    try {
      _tmp.deleteSync(recursive: true);
    } catch (_) {
      // Windows holds file handles briefly after close. Not worth failing a
      // suite over a temp directory.
    }
  });

  test('a photograph goes up as the field the office reads', () async {
    // The app sent it as "file"; the office reads "image". Every photograph
    // from every phone came back "attach the photograph as the 'image' field",
    // which the app filed as a verdict and stopped sending.
    final store = await freshStore();
    String? body;

    final client = MockClient((req) async {
      final path = req.url.path;
      if (path.endsWith('/captures')) {
        body = latin1.decode(req.bodyBytes);
        return http.Response('{}', 201);
      }
      if (path.endsWith('/reports')) {
        return http.Response(jsonEncode({'id': 'R1'}), 201);
      }
      return http.Response('{}', 201);
    });

    await store.putDraft(draftRow('d1'));
    await store.putCapture(
      captureRow('c1', 'd1', await photoOn(_tmp, 'a.jpg')),
    );

    await Sync(
      store: store,
      api: Api(session: await signedIn(), client: client),
    ).run();

    expect(body, isNotNull);
    expect(body, contains('name="image"'));
  });

  test('a claim says it was filed from the app', () async {
    // The app sent no channel, and the server defaults an unsent one to
    // 'field_officer' -- so every claim a farmer filed from their own phone
    // was recorded as filed by an officer standing in their field.
    final store = await freshStore();
    Map<String, Object?>? body;

    final client = MockClient((req) async {
      if (req.url.path.endsWith('/reports')) {
        body = jsonDecode(req.body) as Map<String, Object?>;
        return http.Response(jsonEncode({'id': 'R1'}), 201);
      }
      return http.Response('{}', 201);
    });

    await store.putDraft(draftRow('d1'));
    await Sync(
      store: store,
      api: Api(session: await signedIn(), client: client),
    ).run();

    expect(body?['reported_channel'], 'farmer_app');
  });

  test('a station photograph says which point it was taken at', () async {
    // The walk records a photograph and then a clip at each of the four
    // points. The clip goes up in chunks and carried its station number from
    // the start; the photograph goes up in one request and did not, so the
    // office held four photographs belonging to no point -- and the report
    // drew the map of the walk with nothing printed beside it.
    //
    // Nothing failed while this was broken, which is why it is asserted here.
    final store = await freshStore();
    String? body;

    final client = MockClient((req) async {
      final path = req.url.path;
      if (path.endsWith('/captures')) {
        body = latin1.decode(req.bodyBytes);
        return http.Response('{}', 201);
      }
      if (path.endsWith('/reports')) {
        return http.Response(jsonEncode({'id': 'R1'}), 201);
      }
      return http.Response('{}', 201);
    });

    await store.putDraft(draftRow('d1'));
    await store.putCapture({
      ...captureRow('c1', 'd1', await photoOn(_tmp, 'a.jpg')),
      'station_no': 3,
      'station_offset_m': 4.5,
      'is_mocked': 0,
    });

    await Sync(
      store: store,
      api: Api(session: await signedIn(), client: client),
    ).run();

    expect(body, isNotNull);
    // The value, not just the field name: a form carrying an empty
    // station_no would satisfy a test that only looked for the name.
    expect(field(body!, 'station_no'), '3');
    expect(field(body!, 'station_offset_m'), '4.5');
    // A handset that reported a genuine fix said so; "did not tell us" and
    // "told us it was genuine" are different facts and only one is evidence.
    expect(field(body!, 'is_mocked'), 'false');
  });

  test('a clip says what the phone actually recorded it at', () async {
    // The app has recorded the encoding profile in its own table since schema
    // 8 and never put it in the request, so every clip reached the office with
    // three null columns -- and the office had no way to tell a 65 MB claim
    // caused by a handset that ignores the bitrate it is given from one caused
    // by a longer walk.
    //
    // Measured on a Vivo V2253: asked for 2.5 Mbit/s, recorded at 8.5.
    final store = await freshStore();
    Map<String, Object?>? begun;

    final client = MockClient((req) async {
      final path = req.url.path;
      if (req.method == 'POST' && path.endsWith('/uploads')) {
        begun = jsonDecode(req.body) as Map<String, Object?>;
        return http.Response(jsonEncode({'id': 'U1'}), 202);
      }
      if (path.contains('/chunks/')) {
        return http.Response(jsonEncode({'capture_id': 'C1'}), 201);
      }
      if (path.endsWith('/reports')) {
        return http.Response(jsonEncode({'id': 'R1'}), 201);
      }
      return http.Response('{}', 201);
    });

    await store.putDraft(draftRow('d1'));
    await store.putCapture({
      ...captureRow('c1', 'd1', await photoOn(_tmp, 'clip.mp4')),
      'kind': 'station_video',
      'content_type': 'video/mp4',
      'station_no': 1,
      'duration_ms': 15000,
      'encoding_profile': 'preferred',
      'encoding_fps': 15,
      'encoding_bitrate_bps': 2500000,
    });

    await Sync(
      store: store,
      api: Api(session: await signedIn(), client: client),
    ).run();

    expect(begun, isNotNull, reason: 'the clip never started an upload');
    expect(begun?['encoding_profile'], 'preferred');
    expect(begun?['encoding_fps'], 15);
    expect(begun?['encoding_bitrate_bps'], 2500000);
  });

  test(
    'a clip recorded before the phone knew its profile sends no profile',
    () async {
      // Null, not a default. A clip from an older build has no measured profile,
      // and inventing 'preferred' for it would put a figure in front of an
      // operator that nobody observed.
      final store = await freshStore();
      Map<String, Object?>? begun;

      final client = MockClient((req) async {
        final path = req.url.path;
        if (req.method == 'POST' && path.endsWith('/uploads')) {
          begun = jsonDecode(req.body) as Map<String, Object?>;
          return http.Response(jsonEncode({'id': 'U1'}), 202);
        }
        if (path.contains('/chunks/')) {
          return http.Response(jsonEncode({'capture_id': 'C1'}), 201);
        }
        if (path.endsWith('/reports')) {
          return http.Response(jsonEncode({'id': 'R1'}), 201);
        }
        return http.Response('{}', 201);
      });

      await store.putDraft(draftRow('d1'));
      await store.putCapture({
        ...captureRow('c1', 'd1', await photoOn(_tmp, 'clip.mp4')),
        'kind': 'station_video',
        'content_type': 'video/mp4',
        'station_no': 1,
      });

      await Sync(
        store: store,
        api: Api(session: await signedIn(), client: client),
      ).run();

      expect(begun, isNotNull);
      expect(begun!.containsKey('encoding_profile'), isFalse);
      expect(begun!.containsKey('encoding_fps'), isFalse);
      expect(begun!.containsKey('encoding_bitrate_bps'), isFalse);
    },
  );

  test('a clip refused over its position records why, in the office\'s own '
      'code', () async {
    // The link that would have killed the recovery flow silently.
    //
    // The home screen finds these clips by last_error_code, and the store
    // tests that group and discard them pass whether or not sync ever writes
    // it. If this were dropped -- as the same three fields were dropped in the
    // upload handler -- the card would never appear, the farmer would be shown
    // the retry button again, and every test but this one would still be green.
    final store = await freshStore();

    final client = MockClient((req) async {
      final path = req.url.path;
      if (req.method == 'POST' && path.endsWith('/uploads')) {
        return http.Response(
          jsonEncode({
            'error': {
              'code': 'SAME_POSITION',
              'message':
                  'record this point somewhere else in the field: this point '
                  'of the walk was recorded where another one already was: '
                  '0 m from the last one, and this field needs 14 m',
            },
          }),
          422,
        );
      }
      if (path.endsWith('/reports')) {
        return http.Response(jsonEncode({'id': 'R1'}), 201);
      }
      return http.Response('{}', 201);
    });

    await store.putDraft(draftRow('d1'));
    await store.putCapture({
      ...captureRow('c1', 'd1', await photoOn(_tmp, 'clip.mp4')),
      'kind': 'station_video',
      'content_type': 'video/mp4',
      'station_no': 2,
    });

    await Sync(
      store: store,
      api: Api(session: await signedIn(), client: client),
    ).run();

    final row = (await store.raw.query('captures')).first;
    expect(row['status'], 'failed');
    expect(row['last_error_code'], 'SAME_POSITION');
    // And the farmer-facing sentence is kept too: the code is what the app
    // acts on, the message is what somebody helping them reads.
    expect('${row['last_error']}', contains('somewhere else'));

    // Which is what puts it in front of the farmer as a walk.
    final again = await store.capturesToRecordAgain();
    expect(again, hasLength(1));
    expect(again.first.stations, [2]);
  });

  test('a clip carries what the assistant asked the farmer to show', () async {
    // Evidence about evidence. A drought claim whose canopy is green and whose pods
    // are empty is the case the guidance exists for: the farmer films the green, the
    // loss is in the pods, and nobody untrained films what is not there.
    //
    // A close-up of split pods that the assistant asked for is a directed
    // observation rather than the farmer's own selection, and that distinction is
    // what answers a selective-evidence challenge. It only holds if it travels with
    // the clip -- a transcript in a log joins to nothing a year later.
    final store = await freshStore();
    Map<String, Object?>? begun;

    final client = MockClient((req) async {
      final path = req.url.path;
      if (req.method == 'POST' && path.endsWith('/uploads')) {
        begun = jsonDecode(req.body) as Map<String, Object?>;
        return http.Response(jsonEncode({'id': 'U1'}), 202);
      }
      if (path.contains('/chunks/')) {
        return http.Response(jsonEncode({'capture_id': 'C1'}), 201);
      }
      if (path.endsWith('/reports')) {
        return http.Response(jsonEncode({'id': 'R1'}), 201);
      }
      return http.Response('{}', 201);
    });

    await store.putDraft(draftRow('d1'));
    await store.putCapture({
      ...captureRow('c1', 'd1', await photoOn(_tmp, 'clip.mp4')),
      'kind': 'station_video',
      'content_type': 'video/mp4',
      'station_no': 2,
      'guidance_asked_for': 'शेंगा फोडून आतले दाणे जवळून दाखवा',
      'guidance_model': 'gemini-3.8-live',
    });

    await Sync(
      store: store,
      api: Api(session: await signedIn(), client: client),
    ).run();

    expect(begun?['guidance_asked_for'], 'शेंगा फोडून आतले दाणे जवळून दाखवा');
    expect(begun?['guidance_model'], 'gemini-3.8-live');
  });

  test(
    'a clip nobody was guided on says nothing, rather than saying empty',
    () async {
      // Null means nothing was asked. It must never read as a clip that is missing
      // something: most clips are recorded with the assistant off, out of range, or
      // not built in at all, and they are complete on exactly the same terms.
      final store = await freshStore();
      Map<String, Object?>? begun;

      final client = MockClient((req) async {
        final path = req.url.path;
        if (req.method == 'POST' && path.endsWith('/uploads')) {
          begun = jsonDecode(req.body) as Map<String, Object?>;
          return http.Response(jsonEncode({'id': 'U1'}), 202);
        }
        if (path.contains('/chunks/')) {
          return http.Response(jsonEncode({'capture_id': 'C1'}), 201);
        }
        if (path.endsWith('/reports')) {
          return http.Response(jsonEncode({'id': 'R1'}), 201);
        }
        return http.Response('{}', 201);
      });

      await store.putDraft(draftRow('d1'));
      await store.putCapture({
        ...captureRow('c1', 'd1', await photoOn(_tmp, 'clip.mp4')),
        'kind': 'station_video',
        'content_type': 'video/mp4',
        'station_no': 1,
      });

      await Sync(
        store: store,
        api: Api(session: await signedIn(), client: client),
      ).run();

      expect(begun, isNotNull);
      expect(begun!.containsKey('guidance_asked_for'), isFalse);
      expect(begun!.containsKey('guidance_model'), isFalse);
    },
  );

  group('the order things are sent in', () {
    test('a boundary goes before the report and the photographs', () async {
      // A photograph that lands before its boundary is geofenced against
      // nothing, and that result is permanent.
      final store = await freshStore();
      final order = <String>[];

      final client = MockClient((req) async {
        final path = req.url.path;
        if (path.contains('/boundary')) {
          order.add('boundary');
          return http.Response('{}', 201);
        }
        if (path.endsWith('/captures')) {
          order.add('capture');
          return http.Response('{}', 201);
        }
        if (path.endsWith('/reports')) {
          order.add('report');
          return http.Response(jsonEncode({'id': 'R1'}), 201);
        }
        return http.Response('{}', 404);
      });

      await store.putBoundary(
        farmId: '453',
        surveyNumber: '453',
        points: [
          {'lat': 18.17, 'lon': 75.81, 'accuracy_m': 5.0, 'at': 'now'},
        ],
        areaHa: 1.0,
        worstAccuracyM: 5,
      );
      await store.putDraft(draftRow('d1'));
      await store.putCapture(
        captureRow('c1', 'd1', await photoOn(_tmp, 'a.jpg')),
      );

      final sync = Sync(
        store: store,
        api: Api(session: await signedIn(), client: client),
      );
      final result = await sync.run();

      expect(order, ['boundary', 'report', 'capture']);
      expect(result.sent, 3);
      expect(result.outstanding.any, isFalse);
    });

    test(
      'a photograph waits for its report without spending an attempt',
      () async {
        // The report is queued too. Counting this against the photograph's
        // budget would exhaust it while the report was still trying.
        final store = await freshStore();

        final client = MockClient((req) async {
          if (req.url.path.endsWith('/reports')) {
            return http.Response('{}', 503); // the report cannot land yet
          }
          return http.Response('{}', 201);
        });

        await store.putDraft(draftRow('d1'));
        await store.putCapture(
          captureRow('c1', 'd1', await photoOn(_tmp, 'b.jpg')),
        );

        await Sync(
          store: store,
          api: Api(session: await signedIn(), client: client),
        ).run();

        final captures = await store.pendingCaptures();
        expect(captures, hasLength(1));
        expect(
          captures.first['attempts'],
          0,
          reason: 'waiting on a queued report is not a failed attempt',
        );
      },
    );
  });

  group('losing the network', () {
    test('stops the pass rather than burning every attempt budget', () async {
      // Once nothing can get out, everything after it fails the same way.
      // Spending the budget of every queued row on a dead connection is how a
      // farmer ends up with a queue that has given up.
      final store = await freshStore();

      final client = MockClient((req) async {
        throw http.ClientException('Failed host lookup');
      });

      for (var i = 0; i < 3; i++) {
        await store.putDraft(draftRow('d$i'));
      }

      final result = await Sync(
        store: store,
        api: Api(session: await signedIn(), client: client),
      ).run();

      expect(result.offline, isTrue);
      expect(result.failed, 0, reason: 'offline is not a failure');

      for (final row in await store.pendingDrafts()) {
        expect(
          row['attempts'],
          0,
          reason: 'no attempt should have been spent while offline',
        );
      }
    });

    test('nothing is lost: the queue is exactly as it was', () async {
      final store = await freshStore();
      final client = MockClient((req) async {
        throw const SocketException('no route to host');
      });

      await store.putDraft(draftRow('d1'));
      await store.putCapture(
        captureRow('c1', 'd1', await photoOn(_tmp, 'c.jpg')),
      );

      final before = await store.outstanding();
      final result = await Sync(
        store: store,
        api: Api(session: await signedIn(), client: client),
      ).run();

      expect(result.outstanding.total, before.total);
    });
  });

  group('what is worth retrying', () {
    test('a refusal spends the whole budget rather than looping', () async {
      // No number of retries changes a server's mind about a report it has
      // declined, and retrying forever hides the reason from the farmer.
      final store = await freshStore();

      final client = MockClient(
        (req) async => http.Response(
          jsonEncode({
            // The code the API really sends when surveying has closed; see
            // event_handlers.go, where ErrSurveyClosed maps to NOT_ELIGIBLE.
            'error': {
              'code': 'NOT_ELIGIBLE',
              'message': 'surveying for this event has closed',
            },
          }),
          422,
        ),
      );

      await store.putDraft(draftRow('d1'));
      await Sync(
        store: store,
        api: Api(session: await signedIn(), client: client),
      ).run();

      expect(
        await store.pendingDrafts(),
        isEmpty,
        reason: 'a refused report should stop being retried',
      );

      final all = await store.drafts();
      expect(all.first['status'], 'failed');
      // Not the server's words. Those are English-only and written for a
      // program; what is kept is the same meaning in both languages, chosen
      // from the stable code.
      final shown = all.first['last_error'] as String;
      expect(shown, contains('·'), reason: 'bilingual, like every other');
      expect(shown, isNot(contains('surveying for this event')));
      expect(shown, contains('survey'), reason: 'still says what went wrong');
    });

    test(
      'a server error keeps its place and waits before trying again',
      () async {
        final store = await freshStore();
        final client = MockClient((req) async => http.Response('{}', 500));
        var now = DateTime(2026, 9, 14, 10);

        await store.putDraft(draftRow('d1'));
        final sync = Sync(
          store: store,
          api: Api(session: await signedIn(), client: client),
          clock: () => now,
          reportStatus: false,
        );

        await sync.run();
        final first = (await store.drafts()).first;
        expect(first['attempts'], 1);
        expect(first['status'], 'pending');
        expect(
          first['next_attempt_at'] as int,
          greaterThan(now.millisecondsSinceEpoch),
          reason: 'it waits rather than being tried again at once',
        );

        // Straight away: not due, so not tried.
        await sync.run();
        expect((await store.drafts()).first['attempts'], 1);

        // Once due, tried again.
        now = now.add(const Duration(hours: 7));
        await sync.run();
        expect((await store.drafts()).first['attempts'], 2);
      },
    );

    test('a server error is never given up on', () async {
      // The bug this replaces: five server errors -- two and a half minutes
      // of foreground timer during an outage -- left the claim on the phone,
      // no longer tried and no longer shown.
      final store = await freshStore();
      final client = MockClient((req) async => http.Response('{}', 503));
      var now = DateTime(2026, 9, 14, 10);

      await store.putDraft(draftRow('d1'));
      final sync = Sync(
        store: store,
        api: Api(session: await signedIn(), client: client),
        clock: () => now,
        reportStatus: false,
      );

      for (var i = 0; i < 40; i++) {
        await sync.run();
        now = now.add(const Duration(hours: 7));
      }

      final row = (await store.drafts()).first;
      expect(
        row['status'],
        'pending',
        reason: 'still trying after 40 attempts',
      );
      expect(row['attempts'], 40);
      expect(
        (await store.outstanding()).drafts,
        1,
        reason: 'still on the home screen',
      );
      expect(
        (await store.stuck()).any,
        isFalse,
        reason: 'a server failure is not a refusal',
      );
    });

    test(
      'the waits grow, and are spread so phones do not return together',
      () async {
        const policy = RetryPolicy();
        final a = math.Random(1), b = math.Random(2);
        final first = policy.delay(1, random: a);
        final fifth = policy.delay(5, random: a);
        expect(fifth, greaterThan(first));

        // Two phones on the same attempt wait different amounts.
        final waits = {
          for (var i = 0; i < 20; i++) policy.delay(4, random: math.Random(i)),
        };
        expect(waits.length, greaterThan(10));

        // Never beyond the cap, however many attempts.
        expect(policy.delay(60, random: b), lessThanOrEqualTo(policy.cap));
        // A server that asked for longer is believed.
        expect(
          policy.delay(0, retryAfter: const Duration(minutes: 20), random: b),
          const Duration(minutes: 20),
        );
      },
    );

    test('pressing try now sends waiting work without waiting', () async {
      // The farmer is looking at the screen and has just walked into signal.
      // The backoff cannot know that; the farmer can.
      final store = await freshStore();
      var status = 500;
      final client = MockClient((req) async {
        if (status != 201) return http.Response('{}', status);
        return http.Response(jsonEncode({'id': 'R1'}), 201);
      });
      final now = DateTime(2026, 9, 14, 10);

      await store.putDraft(draftRow('d1'));
      final sync = Sync(
        store: store,
        api: Api(session: await signedIn(), client: client),
        clock: () => now,
        reportStatus: false,
      );
      await sync.run();
      status = 201;

      await sync.run();
      expect(
        (await store.drafts()).first['server_id'],
        isNull,
        reason: 'a timer pass respects the wait',
      );

      await sync.sendNow();
      final row = (await store.drafts()).first;
      expect(row['server_id'], 'R1');
    });

    test('a pass with a time budget stops when it runs out', () async {
      // The background worker is stopped by Android at about ten minutes; a
      // pass that stops itself first releases the lease cleanly.
      final store = await freshStore();
      var now = DateTime(2026, 9, 14, 10);
      var requests = 0;
      final client = MockClient((req) async {
        requests++;
        now = now.add(const Duration(minutes: 3));
        return http.Response(jsonEncode({'id': 'R$requests'}), 201);
      });
      for (var i = 0; i < 6; i++) {
        await store.putDraft(draftRow('d$i'));
      }
      final result = await Sync(
        store: store,
        api: Api(session: await signedIn(), client: client),
        clock: () => now,
        budget: const Duration(minutes: 8),
        reportStatus: false,
      ).run();

      expect(requests, 3, reason: 'three sends take nine minutes');
      expect(result.outstanding.drafts, 3);
      expect(result.shouldRetryLater, isTrue);
    });

    test('three server errors in a row end the pass', () async {
      // The office is struggling. Sending the rest of the queue into the same
      // failure only adds to the load that is causing it.
      final store = await freshStore();
      var requests = 0;
      final client = MockClient((req) async {
        requests++;
        return http.Response('{}', 502);
      });

      for (var i = 0; i < 6; i++) {
        await store.putDraft(draftRow('d$i'));
      }
      final result = await Sync(
        store: store,
        api: Api(session: await signedIn(), client: client),
        reportStatus: false,
      ).run();

      expect(requests, Sync.breakAfter);
      expect(result.outstanding.drafts, 6, reason: 'nothing is lost');
      expect(result.shouldRetryLater, isTrue);
    });

    test('one refusal does not hold the others hostage', () async {
      // A farmer may have three reports queued and one refused for good
      // reason.
      final store = await freshStore();

      final client = MockClient((req) async {
        final body = jsonDecode(req.body) as Map<String, Object?>;
        if (body['farmer_id'] == 'BAD') {
          return http.Response('{}', 422);
        }
        return http.Response(jsonEncode({'id': 'R1'}), 201);
      });

      await store.putDraft(draftRow('d1'));
      await store.putDraft({...draftRow('d2'), 'farmer_id': 'BAD'});
      await store.putDraft(draftRow('d3'));

      final result = await Sync(
        store: store,
        api: Api(session: await signedIn(), client: client),
      ).run();

      expect(result.draftsSent, 2);
      expect(result.failed, 1);
    });
  });

  group('a refusal of the phone rather than of the work', () {
    test('keeps the evidence instead of marking it failed', () async {
      // The data-loss path this closes. A 401 is a token the server would not
      // accept and a 403 is a grant it did not find -- neither is a statement
      // about the clip. Classified as permanent, they marked a capture `failed`,
      // which is terminal: pendingCaptures excludes it and nothing ever retries.
      //
      // The trial had a grant bug producing exactly these codes on a
      // neighbouring route. A capture that had met it would have been abandoned
      // with no way back, and the farmer would have been told their claim was
      // complete.
      for (final status in [401, 403]) {
        final store = await freshStore();
        final client = MockClient(
          (req) async => http.Response(
            jsonEncode({
              'error': {'code': 'FORBIDDEN', 'message': 'not your farmer'},
            }),
            status,
          ),
        );

        await store.putDraft({...draftRow('d1'), 'server_id': 'R1'});
        await store.putCapture(
          captureRow('c1', 'd1', await photoOn(_tmp, 'u$status.jpg')),
        );

        await Sync(
          store: store,
          api: Api(session: await signedIn(), client: client),
        ).run();

        final rows = await store.capturesFor('d1');
        expect(
          rows.single['status'],
          isNot('failed'),
          reason: '$status marked a recording permanently failed',
        );
        expect(
          await store.pendingCaptures(
            now: DateTime.now().add(const Duration(hours: 1)),
          ),
          isNotEmpty,
          reason: '$status left nothing for a later pass to send',
        );
        await store.close();
      }
    });

    test('does not penalise the item that met it', () async {
      // The item did nothing wrong. Charging it a retry would push it down the
      // backoff for a refusal that was never about it.
      final store = await freshStore();
      final client = MockClient(
        (req) async => http.Response(
          jsonEncode({
            'error': {'code': 'UNAUTHORIZED', 'message': 'no'},
          }),
          401,
        ),
      );

      await store.putDraft({...draftRow('d1'), 'server_id': 'R1'});
      await store.putCapture(
        captureRow('c1', 'd1', await photoOn(_tmp, 'unauth.jpg')),
      );

      await Sync(
        store: store,
        api: Api(session: await signedIn(), client: client),
      ).run();

      expect((await store.capturesFor('d1')).single['attempts'], 0);
      await store.close();
    });
  });

  group('sending the same thing twice is safe', () {
    test(
      'every write carries its item\'s key, the same on every attempt',
      () async {
        final store = await freshStore();
        final keys = <String>[];
        var calls = 0;
        final client = MockClient((req) async {
          if (req.url.path == '/api/v1/reports') {
            keys.add(req.headers['Idempotency-Key'] ?? '');
            calls++;
            // The first reply is lost: the server did the work, the phone never
            // heard. To the phone it is a timeout.
            if (calls == 1) return http.Response('{}', 504);
            return http.Response(
              jsonEncode({'id': 'R1', 'reference': 'CLR-1'}),
              200,
              headers: {'idempotency-replayed': 'true'},
            );
          }
          return http.Response('{}', 404);
        });
        var now = DateTime(2026, 9, 14, 10);

        await store.putDraft(draftRow('d1'));
        final sync = Sync(
          store: store,
          api: Api(session: await signedIn(), client: client),
          clock: () => now,
          reportStatus: false,
        );
        await sync.run();
        now = now.add(const Duration(hours: 7));
        await sync.run();

        expect(keys, hasLength(2));
        expect(keys.first, matches(RegExp(r'^q[0-9a-f]{32}$')));
        expect(keys[1], keys[0], reason: 'a retry is the same request');
        expect((await store.drafts()).first['server_id'], 'R1');
      },
    );

    test('every request says which build sent it', () async {
      final store = await freshStore();
      String? version;
      final client = MockClient((req) async {
        version ??= req.headers['X-App-Version'];
        return http.Response(jsonEncode({'id': 'R1'}), 201);
      });
      await store.putDraft(draftRow('d1'));
      await Sync(
        store: store,
        api: Api(session: await signedIn(), client: client),
        reportStatus: false,
      ).run();
      expect(version, isNotEmpty);
    });
  });

  group('when the office asks the phone to slow down', () {
    test('every request waits, not only the one that was told', () async {
      final store = await freshStore();
      var requests = 0;
      final client = MockClient((req) async {
        requests++;
        return http.Response(
          jsonEncode({
            'error': {'code': 'RATE_LIMITED', 'message': 'slow down'},
          }),
          429,
          headers: {'retry-after': '120'},
        );
      });
      var now = DateTime(2026, 9, 14, 10);

      await store.putDraft(draftRow('d1'));
      await store.putDraft(draftRow('d2'));
      final sync = Sync(
        store: store,
        api: Api(session: await signedIn(), client: client),
        clock: () => now,
        reportStatus: false,
      );

      final first = await sync.run();
      expect(first.throttled, isTrue);
      expect(requests, 1, reason: 'the pass stopped at the first 429');
      expect(
        (await store.drafts()).every((d) => d['attempts'] == 0),
        isTrue,
        reason: 'being told to wait is not a failed attempt',
      );

      // Inside the pause: nothing is sent at all.
      now = now.add(const Duration(seconds: 60));
      await sync.run();
      expect(requests, 1);

      // After it: sending resumes.
      now = now.add(const Duration(minutes: 5));
      await sync.run();
      expect(requests, greaterThan(1));
    });
  });

  group('a build too old to send', () {
    test('stops and keeps the whole queue until the app is updated', () async {
      final store = await freshStore();
      var requests = 0;
      final client = MockClient((req) async {
        requests++;
        return http.Response(
          jsonEncode({
            'error': {'code': 'UPGRADE_REQUIRED', 'message': 'update'},
          }),
          426,
        );
      });

      await store.putDraft(draftRow('d1'));
      final sync = Sync(
        store: store,
        api: Api(session: await signedIn(), client: client),
        reportStatus: false,
      );
      final result = await sync.run();

      expect(result.upgradeRequired, isTrue);
      expect(
        result.shouldRetryLater,
        isFalse,
        reason: 'a background retry cannot fix an old build',
      );
      final row = (await store.drafts()).first;
      expect(row['status'], 'pending', reason: 'the claim is not refused');

      await sync.run();
      expect(requests, 1, reason: 'no more requests until the app updates');
    });
  });

  group('telling the office how the queue is doing', () {
    test('reports counts, and only when something changed', () async {
      final store = await freshStore();
      final reports = <Map<String, dynamic>>[];
      final client = MockClient((req) async {
        if (req.url.path == '/api/v1/devices/sync-status') {
          reports.add(jsonDecode(req.body) as Map<String, dynamic>);
          return http.Response('', 204);
        }
        return http.Response('{}', 500);
      });
      var now = DateTime(2026, 9, 14, 10);

      await store.putDraft(draftRow('d1'));
      final sync = Sync(
        store: store,
        api: Api(session: await signedIn(), client: client),
        clock: () => now,
      );

      await sync.run();
      expect(reports, hasLength(1));
      final r = reports.single;
      expect(r['device_id'], startsWith('dev-'));
      expect(r['pending_reports'], 1);
      expect(r['oldest_pending_at'], isNotNull);
      expect(r.containsKey('statement'), isFalse);

      // Nothing changed, five minutes later: no report.
      now = now.add(const Duration(minutes: 5));
      await sync.run();
      expect(reports, hasLength(1));

      // Nothing changed, twenty minutes later: reported again.
      now = now.add(const Duration(minutes: 20));
      await sync.run();
      expect(reports, hasLength(2));
    });
  });

  group('a photograph the phone no longer has', () {
    test('is marked rather than retried forever', () async {
      // Android reclaimed the file, or the farmer cleared storage. An officer
      // seeing a report with a missing photograph is better than a queue that
      // never drains.
      final store = await freshStore();
      final client = MockClient(
        (req) async => http.Response(jsonEncode({'id': 'R1'}), 201),
      );

      await store.putDraft(draftRow('d1'));
      await store.putCapture(
        captureRow('c1', 'd1', '${_tmp.path}/never-existed.jpg'),
      );

      final result = await Sync(
        store: store,
        api: Api(session: await signedIn(), client: client),
      ).run();

      expect(result.failed, 1);
      expect(await store.pendingCaptures(), isEmpty);
    });
  });

  group('after a photograph lands', () {
    test('the copy on the phone is released', () async {
      // A handset that had 2 GB free to begin with fills up, and a farmer
      // whose phone is full stops taking photographs.
      final store = await freshStore();
      final path = await photoOn(_tmp, 'keep.jpg');

      final client = MockClient((req) async {
        if (req.url.path.endsWith('/reports')) {
          return http.Response(jsonEncode({'id': 'R1'}), 201);
        }
        return http.Response('{}', 201);
      });

      await store.putDraft(draftRow('d1'));
      await store.putCapture(captureRow('c1', 'd1', path));

      await Sync(
        store: store,
        api: Api(session: await signedIn(), client: client),
      ).run();

      // The delete is fire-and-forget so the upload is not held up by the
      // filesystem; give it a moment.
      await Future<void>.delayed(const Duration(milliseconds: 50));
      expect(File(path).existsSync(), isFalse);
    });

    test('the report keeps the id its photographs need', () async {
      final store = await freshStore();
      final client = MockClient(
        (req) async => http.Response(
          jsonEncode({'id': 'R1', 'reference': 'CLR-2026-27-000123'}),
          201,
        ),
      );

      await store.putDraft(draftRow('d1'));
      await Sync(
        store: store,
        api: Api(session: await signedIn(), client: client),
      ).run();

      final row = await store.draft('d1');
      expect(row?['server_id'], 'R1');
      expect(
        row?['reference'],
        'CLR-2026-27-000123',
        reason: 'the reference is what a farmer quotes to ask about it',
      );
    });
  });

  group('two passes at once', () {
    test('the second is skipped rather than sending twice', () async {
      // Two concurrent passes would send the same photograph twice and race
      // each other's attempt counters.
      final store = await freshStore();
      var reports = 0;

      final client = MockClient((req) async {
        if (req.url.path == '/api/v1/reports') reports++;
        await Future<void>.delayed(const Duration(milliseconds: 60));
        return http.Response(jsonEncode({'id': 'R1'}), 201);
      });

      await store.putDraft(draftRow('d1'));
      final sync = Sync(
        store: store,
        api: Api(session: await signedIn(), client: client),
      );

      await Future.wait([sync.run(), sync.run(), sync.run()]);
      expect(reports, 1);
    });
  });

  group('an empty queue', () {
    test('is a pass that did nothing and said so', () async {
      final store = await freshStore();
      final client = MockClient((req) async => http.Response('{}', 500));

      final result = await Sync(
        store: store,
        api: Api(session: await signedIn(), client: client),
      ).run();

      expect(result.sent, 0);
      expect(result.failed, 0);
      expect(result.outstanding.any, isFalse);
      expect(result.anythingHappened, isFalse);
    });
  });

  group('signing out on a borrowed handset', () {
    test('leaves nothing of the previous farmer behind', () async {
      // A phone in a village is borrowed. One farmer's photographs must not be
      // sitting in the app when the next person opens it.
      final store = await freshStore();

      await store.putDraft(draftRow('d1'));
      await store.putCapture(
        captureRow('c1', 'd1', await photoOn(_tmp, 'd.jpg')),
      );
      await store.putBoundary(
        farmId: '453',
        surveyNumber: '453',
        points: const [],
        areaHa: 1,
        worstAccuracyM: 5,
      );
      await store.put('events', ['E1']);

      expect((await store.outstanding()).any, isTrue);

      await store.clearPersonalData();

      expect((await store.outstanding()).any, isFalse);
      expect(await store.drafts(), isEmpty);
      expect(await store.get<List<dynamic>>('events'), isNull);
    });
  });

  _filedAtTests();
}

/// When the farmer filed, sent with the claim.
///
/// The office measured the 48-hour intimation window on arrival, which for
/// this app is not when the farmer acted: the form is filled in a field with
/// no signal, held until its evidence is complete, and delivered when a
/// network appears. A farmer who filled the form on the first morning and
/// found a signal on the third day was recorded as having filed late.
void _filedAtTests() {
  group('when the farmer filed', () {
    test('the claim carries the moment the form was completed', () async {
      final store = await freshStore();
      Map<String, dynamic>? body;

      final client = MockClient((req) async {
        if (req.url.path.endsWith('/reports')) {
          body = jsonDecode(req.body) as Map<String, dynamic>;
          return http.Response(jsonEncode({'id': 'R1'}), 201);
        }
        return http.Response('{}', 404);
      });

      final filed = DateTime.utc(2026, 8, 1, 14, 30);
      await store.putDraft({
        ...draftRow('d1'),
        'filed_at': filed.millisecondsSinceEpoch,
      });

      await Sync(
        store: store,
        api: Api(session: await signedIn(), client: client),
      ).run();

      expect(body?['filed_at'], isNotNull);
      expect(
        DateTime.parse(body!['filed_at'] as String).toUtc(),
        filed,
        reason:
            'the window is measured on this, so it must be the moment the '
            'farmer filed and not the moment the queue got out',
      );
    });

    test('the claim says how much evidence is coming behind it', () async {
      // The office cannot tell "still uploading" from "that was everything",
      // and it now starts assessing on complete receipt. A claim that
      // understates its evidence is assessed on half a walk; one that
      // overstates it waits two days for a photograph that was never taken.
      final store = await freshStore();
      Map<String, dynamic>? body;

      final client = MockClient((req) async {
        if (req.url.path.endsWith('/reports')) {
          body = jsonDecode(req.body) as Map<String, dynamic>;
          return http.Response(jsonEncode({'id': 'R1'}), 201);
        }
        return http.Response('{}', 201);
      });

      await store.putDraft(draftRow('d1'));
      for (final name in ['a.jpg', 'b.jpg', 'c.jpg']) {
        await store.putCapture(
          captureRow('c-$name', 'd1', await photoOn(_tmp, name)),
        );
      }

      await Sync(
        store: store,
        api: Api(session: await signedIn(), client: client),
      ).run();

      expect(body?['expected_captures'], 3);
    });

    test("a second farmer's evidence carries their grant", () async {
      // The break this closes. One mobile number acts for several farmers;
      // the office resolves a claim through the scope of the farmer being
      // acted for. Without the grant on the evidence too, the claim is filed,
      // the field is walked, and every photograph comes back "no such
      // report" -- after the work is done and from a field with no signal.
      final store = await freshStore();
      final seen = <String, String?>{};

      final client = MockClient((req) async {
        seen[req.url.path] = req.headers['x-farmer-grant'];
        if (req.url.path.endsWith('/reports')) {
          return http.Response(jsonEncode({'id': 'R1'}), 201);
        }
        return http.Response('{}', 201);
      });

      store.owner = 'acct-1';
      await store.linkFarmer(
        farmerId: 'F-2',
        name: 'Hari',
        village: 'V1',
        grantToken: 'grant-for-F2',
      );
      await store.putDraft({...draftRow('d1'), 'farmer_id': 'F-2'});
      await store.putCapture(
        captureRow('c1', 'd1', await photoOn(_tmp, 'evidence.jpg')),
      );

      await Sync(
        store: store,
        api: Api(session: await signedIn(), client: client),
      ).run();

      expect(
        seen['/api/v1/reports'],
        'grant-for-F2',
        reason: 'the claim named a farmer the account acts for',
      );
      expect(
        seen['/api/v1/reports/R1/captures'],
        'grant-for-F2',
        reason: 'and so must its evidence, or the office cannot find the claim',
      );
    });

    test('a draft queued before this app knew is sent without one', () async {
      // There is no honest answer for a row filed by an older build: the
      // moment has passed and inventing it would be inventing a record. The
      // server falls back to arrival for those, exactly as before.
      final store = await freshStore();
      Map<String, dynamic>? body;

      final client = MockClient((req) async {
        if (req.url.path.endsWith('/reports')) {
          body = jsonDecode(req.body) as Map<String, dynamic>;
          return http.Response(jsonEncode({'id': 'R1'}), 201);
        }
        return http.Response('{}', 404);
      });

      await store.putDraft(draftRow('d1'));
      await Sync(
        store: store,
        api: Api(session: await signedIn(), client: client),
      ).run();

      expect(body, isNotNull);
      expect(body!.containsKey('filed_at'), isFalse);
    });
  });

  test('a second farmer\'s parcel is looked up with their grant', () async {
    // Acting for a farmer whose number did not sign in. Without the grant the
    // office scopes the lookup to the account's own land and answers "that
    // plot is not on your record" -- a 404 the form swallows, so the farmer
    // taps Start report and nothing happens.
    final store = await freshStore();
    await store.linkFarmer(
      farmerId: 'F-OTHER',
      name: 'Another Farmer',
      village: '562111',
      grantToken: 'grant-token-xyz',
      grantExpiresAt: DateTime.now()
          .add(const Duration(hours: 2))
          .millisecondsSinceEpoch,
    );

    String? sentGrant;
    final client = MockClient((req) async {
      if (req.url.path.contains('/stations')) {
        sentGrant = req.headers['X-Farmer-Grant'];
        return http.Response(jsonEncode({'stations': []}), 200);
      }
      return http.Response('{}', 200);
    });

    await FarmerData(
      store: store,
      api: Api(session: await signedIn(), client: client),
    ).stations('FARM-9', forFarmer: 'F-OTHER');

    expect(sentGrant, 'grant-token-xyz');
  });
}
