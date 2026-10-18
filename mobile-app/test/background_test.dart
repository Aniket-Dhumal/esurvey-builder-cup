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

/// The two isolates, and what stops them treading on each other.
///
/// The app has one isolate and the background worker has another. They cannot
/// see each other's memory, so every in-process guard the sync engine has is
/// invisible across the boundary -- and both of them open the same database
/// file. What follows is about the one guard that does hold.

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
late String _dbDir;

/// Two stores over the *same* file, which is what the two isolates have.
Future<Store> openSharedStore() => Store.open(directory: _dbDir);

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
  'signature_kind': 'thumb',
  'status': 'pending',
  'attempts': 0,
};

http.Response created(http.BaseRequest _) =>
    http.Response(jsonEncode({'id': 'R1', 'reference': 'REF-1'}), 201);

Never noNetwork(http.BaseRequest _) =>
    throw http.ClientException('Failed host lookup');

void main() {
  setUpAll(() {
    sqfliteFfiInit();
    databaseFactory = databaseFactoryFfi;
    _tmp = Directory.systemTemp.createTempSync('esurvey-bg-test');
  });

  setUp(() {
    _dbDir = '${_tmp.path}/${DateTime.now().microsecondsSinceEpoch}';
    Directory(_dbDir).createSync(recursive: true);
  });

  tearDownAll(() {
    try {
      _tmp.deleteSync(recursive: true);
    } catch (_) {
      // Windows holds handles briefly after close.
    }
  });

  group('only one isolate syncs at a time', () {
    test('the second claim on a held lease is refused', () async {
      final app = await openSharedStore();
      final worker = await openSharedStore();

      expect(await app.claimSyncLease(), isTrue);
      expect(
        await worker.claimSyncLease(),
        isFalse,
        reason: 'the app is mid-pass, so the worker must stand down',
      );

      await app.close();
      await worker.close();
    });

    test('the lease is available again once released', () async {
      final app = await openSharedStore();
      final worker = await openSharedStore();

      expect(await app.claimSyncLease(), isTrue);
      await app.releaseSyncLease();
      expect(await worker.claimSyncLease(), isTrue);

      await app.close();
      await worker.close();
    });

    test('a lease left by a killed isolate expires', () async {
      // Android kills background isolates without warning and routinely. A
      // lease with no expiry would mean one such death stops this phone
      // syncing for good -- the worst outcome for a lock whose whole job is
      // to protect a queue that must eventually drain.
      final dead = await openSharedStore();
      final worker = await openSharedStore();

      expect(await dead.claimSyncLease(), isTrue);
      // ...and the isolate holding it is gone, without ever releasing.

      expect(
        await worker.claimSyncLease(ttl: Duration.zero),
        isTrue,
        reason: 'a stale lease must not outlive the isolate that took it',
      );

      await dead.close();
      await worker.close();
    });

    test('a pass that cannot get the lease sends nothing', () async {
      // The point of the lease, end to end: two passes over one queue must
      // not both upload the same report. The office receiving a farmer's
      // evidence twice, with the same timestamp, is exactly what a fraud
      // check is built to notice.
      final app = await openSharedStore();
      final worker = await openSharedStore();

      await app.putDraft(draftRow('d1'));

      var posts = 0;
      final client = MockClient((req) async {
        posts++;
        return created(req);
      });

      // The app is mid-pass and holding the lease.
      expect(await app.claimSyncLease(), isTrue);

      final result = await Sync(
        store: worker,
        api: Api(session: await signedIn(), client: client),
      ).run();

      expect(
        posts,
        0,
        reason: 'the worker must not send what the app is sending',
      );
      expect(result.sent, 0);

      await app.close();
      await worker.close();
    });

    test('a normal pass takes the lease and gives it back', () async {
      final store = await openSharedStore();
      await store.putDraft(draftRow('d1'));

      final result = await Sync(
        store: store,
        api: Api(
          session: await signedIn(),
          client: MockClient((req) async => created(req)),
        ),
      ).run();

      expect(result.draftsSent, 1);

      // Held for the pass and no longer -- otherwise the next worker to fire
      // would find a lease nobody owns and wait out the whole ttl.
      final other = await openSharedStore();
      expect(await other.claimSyncLease(), isTrue);

      await store.close();
      await other.close();
    });
  });

  group('handing an offline queue to the operating system', () {
    test('a pass that ends offline with work left asks for a retry', () async {
      final store = await openSharedStore();
      await store.putDraft(draftRow('d1'));

      var asked = 0;
      final result = await Sync(
        store: store,
        api: Api(
          session: await signedIn(),
          client: MockClient((req) async => noNetwork(req)),
        ),
        onStillPending: (_) async => asked++,
      ).run();

      expect(result.offline, isTrue);
      expect(
        asked,
        1,
        reason: 'the farmer is about to pocket the phone and walk home',
      );

      await store.close();
    });

    test('a pass that sends everything asks for nothing', () async {
      final store = await openSharedStore();
      await store.putDraft(draftRow('d1'));

      var asked = 0;
      await Sync(
        store: store,
        api: Api(
          session: await signedIn(),
          client: MockClient((req) async => created(req)),
        ),
        onStillPending: (_) async => asked++,
      ).run();

      expect(
        asked,
        0,
        reason: 'an empty queue is not worth waking a phone for',
      );
      await store.close();
    });

    test('an empty queue offline asks for nothing', () async {
      // Offline is the ordinary state of this handset. Scheduling a wake-up
      // every time a pass finds no network and nothing to send would have the
      // app asking the OS for work it has no reason to do.
      final store = await openSharedStore();

      var asked = 0;
      await Sync(
        store: store,
        api: Api(
          session: await signedIn(),
          client: MockClient((req) async => noNetwork(req)),
        ),
        onStillPending: (_) async => asked++,
      ).run();

      expect(asked, 0);
      await store.close();
    });

    test('offline asks for a pass as soon as there is a network', () async {
      final store = await openSharedStore();
      await store.putDraft(draftRow('d1'));

      Duration? after;
      await Sync(
        store: store,
        api: Api(
          session: await signedIn(),
          client: MockClient((req) async => noNetwork(req)),
        ),
        onStillPending: (d) async => after = d,
        reportStatus: false,
      ).run();

      expect(after, Duration.zero);
      await store.close();
    });

    test('a failing office asks for a pass when the work is due', () async {
      // Waking the phone the moment the pass ends would only find the item
      // not yet due -- or, after the breaker, walk back into the failure.
      final store = await openSharedStore();
      await store.putDraft(draftRow('d1'));
      final now = DateTime(2026, 9, 14, 10);

      Duration? after;
      await Sync(
        store: store,
        api: Api(
          session: await signedIn(),
          client: MockClient((req) async => http.Response('{}', 500)),
        ),
        clock: () => now,
        onStillPending: (d) async => after = d,
        reportStatus: false,
      ).run();

      final due = DateTime.fromMillisecondsSinceEpoch(
        (await store.drafts()).first['next_attempt_at'] as int,
      );
      expect(after, due.difference(now));
      expect(after, greaterThan(Duration.zero));
      await store.close();
    });

    test('a pause from the office is waited out before waking', () async {
      final store = await openSharedStore();
      await store.putDraft(draftRow('d1'));
      final now = DateTime(2026, 9, 14, 10);

      Duration? after;
      await Sync(
        store: store,
        api: Api(
          session: await signedIn(),
          client: MockClient(
            (req) async =>
                http.Response('{}', 429, headers: {'retry-after': '900'}),
          ),
        ),
        clock: () => now,
        onStillPending: (d) async => after = d,
        reportStatus: false,
      ).run();

      // As long as asked, plus up to a fifth again so the phones told at the
      // same moment do not all return at the same moment.
      expect(after, greaterThanOrEqualTo(const Duration(minutes: 15)));
      expect(after, lessThanOrEqualTo(const Duration(minutes: 18)));
      await store.close();
    });

    test('a scheduler that refuses does not fail the pass', () async {
      // WorkManager can decline -- quota, a restricted phone, an OEM battery
      // saver. The queue is still on disk and the foreground timer still
      // runs, so this is a worse outcome and not a broken one.
      final store = await openSharedStore();
      await store.putDraft(draftRow('d1'));

      final result = await Sync(
        store: store,
        api: Api(
          session: await signedIn(),
          client: MockClient((req) async => noNetwork(req)),
        ),
        onStillPending: (_) async => throw Exception('no background quota'),
      ).run();

      expect(result.offline, isTrue);
      expect(result.outstanding.drafts, 1, reason: 'nothing was lost');

      await store.close();
    });
  });
}
