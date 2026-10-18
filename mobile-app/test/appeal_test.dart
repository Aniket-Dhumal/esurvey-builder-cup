import 'dart:convert';
import 'dart:io';

import 'package:esurvey/core/api.dart';
import 'package:esurvey/core/db.dart';
import 'package:esurvey/core/session.dart';
import 'package:esurvey/core/sync.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';
import 'package:sqflite_common_ffi/sqflite_ffi.dart';

/// Objecting to a decision, from the phone that made the claim.
///
/// A farmer reads a decision they disagree with wherever they happen to be,
/// which on this platform means probably without signal. If objecting needed
/// a network at that moment, the objection would be written, lost, and never
/// made again -- the anger passes, the deadline does not wait, and the wrong
/// figure stands.

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
    accessToken: 'a',
    accessTtl: const Duration(minutes: 15),
    refreshToken: 'r',
    principal: {'farmer_id': 'F1'},
  );
  return s;
}

Map<String, Object?> appealRow(String localId, {String report = 'R1'}) => {
  'local_id': localId,
  'report_id': report,
  'ground': 'damage_percent_low',
  'statement': 'The whole field was flattened, not half of it.',
  'status': 'pending',
  'attempts': 0,
};

void main() {
  setUpAll(() {
    sqfliteFfiInit();
    databaseFactory = databaseFactoryFfi;
    _tmp = Directory.systemTemp.createTempSync('esurvey-appeal-test');
  });

  tearDownAll(() {
    try {
      _tmp.deleteSync(recursive: true);
    } catch (_) {
      // Windows holds handles briefly after close.
    }
  });

  group('an objection survives having no signal', () {
    test('it is kept on the phone and sent later', () async {
      final store = await freshStore();
      var online = false;

      final client = MockClient((req) async {
        if (!online) throw http.ClientException('Failed host lookup');
        return http.Response(
          jsonEncode({'id': 'G1', 'reference': 'GRV-1'}),
          201,
        );
      });

      final sync = Sync(
        store: store,
        api: Api(session: await signedIn(), client: client),
      );

      await store.putAppeal(appealRow('a1'));

      final offline = await sync.run();
      expect(offline.offline, isTrue);
      expect(offline.appealsSent, 0);
      expect(
        await store.pendingAppeals(),
        hasLength(1),
        reason: 'the objection is still on the phone, not lost',
      );

      // The farmer walks back into signal.
      online = true;
      final sent = await sync.run();

      expect(sent.appealsSent, 1);
      expect(await store.pendingAppeals(), isEmpty);

      final all = await store.appeals();
      expect(all.first['server_id'], 'G1');
      expect(
        all.first['reference'],
        'GRV-1',
        reason: 'the farmer needs the number the office knows it by',
      );

      await store.close();
    });

    test('a refusal is recorded and not retried forever', () async {
      // The server refuses a second open objection on one report. Retrying
      // that until the attempt budget runs out wastes a farmer's battery to
      // be told the same thing five times.
      final store = await freshStore();
      final client = MockClient(
        (req) async => http.Response(
          jsonEncode({
            'error': {'message': 'a grievance is already open'},
          }),
          409,
        ),
      );

      await store.putAppeal(appealRow('a1'));
      final result = await Sync(
        store: store,
        api: Api(session: await signedIn(), client: client),
      ).run();

      expect(result.failed, 1);
      expect(
        await store.pendingAppeals(),
        isEmpty,
        reason: 'a permanent refusal stops being retried',
      );

      await store.close();
    });
  });

  group('one objection per report', () {
    test('the phone knows it has already objected', () async {
      // Checked before the farmer writes a second one out, rather than after
      // it has queued and failed days later.
      final store = await freshStore();

      expect(await store.hasAppeal('R1'), isFalse);
      await store.putAppeal(appealRow('a1'));
      expect(await store.hasAppeal('R1'), isTrue);
      expect(await store.hasAppeal('R2'), isFalse);

      await store.close();
    });
  });

  group('the queue order', () {
    test('an objection does not block the evidence behind it', () async {
      // Appeals go last on purpose: the report being objected to is already
      // at the office, so nothing else in the queue is waiting on one. A
      // refused objection must not hold up a photograph.
      final store = await freshStore();
      final calls = <String>[];

      final client = MockClient((req) async {
        calls.add(req.url.path);
        if (req.url.path.contains('grievances')) {
          return http.Response('{}', 409);
        }
        return http.Response(
          jsonEncode({'id': 'R1', 'reference': 'REF-1'}),
          201,
        );
      });

      await store.putDraft({
        'local_id': 'd1',
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
      });
      await store.putAppeal(appealRow('a1'));

      final result = await Sync(
        store: store,
        api: Api(session: await signedIn(), client: client),
      ).run();

      expect(result.draftsSent, 1, reason: 'the report still went');
      expect(
        calls.first,
        contains('reports'),
        reason: 'the report is sent before the objection',
      );

      await store.close();
    });
  });

  group('signing out on a borrowed handset', () {
    test('takes the objections too', () async {
      // Whatever a farmer wrote about their own claim is theirs, and the next
      // person to pick the phone up must not find it.
      final store = await freshStore();
      await store.putAppeal(appealRow('a1'));

      await store.clearPersonalData();

      expect(await store.appeals(), isEmpty);
      await store.close();
    });
  });
}
