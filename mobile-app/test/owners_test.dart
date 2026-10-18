import 'dart:convert';
import 'dart:io';

import 'package:esurvey/core/accounts.dart';
import 'package:esurvey/core/api.dart';
import 'package:esurvey/core/db.dart';
import 'package:esurvey/core/session.dart';
import 'package:esurvey/core/sync.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';
import 'package:path/path.dart' as p;
import 'package:sqflite_common_ffi/sqflite_ffi.dart';

/// One phone, more than one farmer.
///
/// The decisions these pin down (2026-09-14): a token expiring deletes
/// nothing; a farmer who signs out with work unsent has it sent under their
/// own name, hidden from whoever signs in next, and removed from the phone
/// once the office has all of it.

class _Keystore implements SecureStore {
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
  final dir = Directory(
    p.join(_tmp.path, '${DateTime.now().microsecondsSinceEpoch}'),
  )..createSync(recursive: true);
  return Store.open(directory: dir.path);
}

Future<Session> farmer(
  SecureStore keystore,
  String id, {
  String prefix = '',
  Duration accessTtl = const Duration(minutes: 15),
}) async {
  final s = Session(storage: keystore, prefix: prefix);
  await s.save(
    accessToken: 'access-$id',
    accessTtl: accessTtl,
    refreshToken: 'refresh-$id',
    principal: {'id': id, 'farmer_id': 'F-$id', 'full_name': 'Farmer $id'},
  );
  return s;
}

Map<String, Object?> draftRow(String localId, {String farmerId = 'F'}) => {
  'local_id': localId,
  'survey_event_id': 'E1',
  'farmer_id': farmerId,
  'season': 'kharif',
  'crop_year': '2026-27',
  'loss_category': 'standing_crop',
  'calamity_type': 'hailstorm',
  'occurred_at': '2026-08-01T10:00:00Z',
  'farm_id': 'farm-$localId',
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

void main() {
  setUpAll(() {
    sqfliteFfiInit();
    databaseFactory = databaseFactoryFfi;
    _tmp = Directory.systemTemp.createTempSync('esurvey-owners-test');
  });

  tearDownAll(() {
    try {
      _tmp.deleteSync(recursive: true);
    } catch (_) {
      // Windows holds handles briefly after close.
    }
  });

  group('a session that lapses', () {
    test('keeps who the farmer is and deletes nothing', () async {
      // The bug this replaces: a thirty-day refresh token expiring while a
      // farmer was out of signal deleted the claim they had filed.
      final keystore = _Keystore();
      final store = await freshStore();
      final session = await farmer(keystore, 'A', accessTtl: Duration.zero);
      store.owner = session.ownerId;
      await store.putDraft(draftRow('d1'));

      var lapsed = 0;
      final client = MockClient((req) async {
        if (req.url.path == '/api/v1/auth/refresh') {
          return http.Response('{}', 401);
        }
        return http.Response('{}', 500);
      });
      await Sync(
        store: store,
        api: Api(session: session, client: client, onSignedOut: () => lapsed++),
        reportStatus: false,
      ).run();
      await Future<void>.delayed(Duration.zero);

      expect(lapsed, 1);
      expect(session.signedIn, isFalse);
      expect(session.ownerId, 'A', reason: 'who they were is kept');
      expect(session.name, 'Farmer A');
      expect((await store.outstanding()).drafts, 1, reason: 'nothing deleted');
      await store.close();
    });

    test('ending one session leaves other parked credentials alone', () async {
      final keystore = _Keystore();
      final a = await farmer(keystore, 'A');
      await Accounts(storage: keystore).park(a);
      await a.end();

      final b = await farmer(keystore, 'B');
      await b.end();

      final parked = await Accounts(storage: keystore).session('A');
      expect(parked.signedIn, isTrue);
      expect(parked.refreshToken, 'refresh-A');
    });
  });

  group('whose rows a screen sees', () {
    test('only the signed-in farmer\'s', () async {
      final store = await freshStore();
      store.owner = 'A';
      await store.putDraft(draftRow('a1'));
      await store.putJourney({
        'local_id': 'ja',
        'stage': 'form',
        'farm_id': 'x',
        'event_id': 'E1',
      });

      store.owner = 'B';
      await store.putDraft(draftRow('b1'));

      expect((await store.outstanding()).drafts, 1);
      expect((await store.pendingDrafts()).single['local_id'], 'b1');
      expect(await store.openJourneys(), isEmpty);
      expect((await store.drafts()).map((d) => d['local_id']), ['b1']);

      expect(
        (await store.outstanding(owner: Store.everyone)).drafts,
        2,
        reason: 'the phone as a whole still knows about both',
      );
      await store.close();
    });

    test(
      'rows from before owners were recorded go to the signed-in farmer',
      () async {
        final store = await freshStore();
        await store.putDraft(draftRow('old')); // no owner yet
        store.owner = 'A';
        expect(await store.drafts(), isEmpty);

        await store.adoptUnowned('A');
        expect((await store.drafts()).single['owner_id'], 'A');
        await store.close();
      },
    );
  });

  group('a farmer who signed out with work unsent', () {
    test('has it sent under their own name, not the next farmer\'s', () async {
      final keystore = _Keystore();
      final accounts = Accounts(storage: keystore);
      final store = await freshStore();

      // A queues a claim, then signs out with it unsent.
      final a = await farmer(keystore, 'A');
      store.owner = 'A';
      await store.putDraft(draftRow('a1', farmerId: 'F-A'));
      await accounts.park(a);
      await a.end();

      // B signs in on the same phone and queues their own.
      final b = await farmer(keystore, 'B');
      store.owner = 'B';
      await store.putDraft(draftRow('b1', farmerId: 'F-B'));

      final sentWith = <String, String>{};
      Future<http.Response> office(http.Request req) async {
        if (req.url.path == '/api/v1/reports') {
          final body = jsonDecode(req.body) as Map<String, dynamic>;
          sentWith[body['farmer_id'] as String] =
              req.headers['Authorization'] ?? '';
          return http.Response(
            jsonEncode({'id': 'R-${body['farmer_id']}', 'reference': 'X'}),
            201,
          );
        }
        return http.Response('{}', 404);
      }

      var purged = 0;
      await Sync(
        store: store,
        api: Api(session: b, client: MockClient(office)),
        accounts: accounts,
        apiFor: (session, onRefused) => Api(
          session: session,
          client: MockClient(office),
          onSignedOut: onRefused,
        ),
        onPurged: () async => purged++,
        reportStatus: false,
      ).run();

      expect(sentWith['F-B'], 'Bearer access-B');
      expect(
        sentWith['F-A'],
        'Bearer access-A',
        reason: 'A\'s claim is never sent as B',
      );

      // All of A's work reached the office: it is gone from the phone.
      expect((await store.drafts(owner: 'A')), isEmpty);
      expect(await accounts.parked(), isEmpty);
      expect(purged, 1, reason: 'the caller removes the files');

      // B's own sent report stays: B is signed in and it is theirs.
      expect((await store.drafts()).single['server_id'], 'R-F-B');
      await store.close();
    });

    test('is held, not deleted, when their parked token is refused', () async {
      final keystore = _Keystore();
      final accounts = Accounts(storage: keystore);
      final store = await freshStore();

      final a = await farmer(keystore, 'A', accessTtl: Duration.zero);
      store.owner = 'A';
      await store.putDraft(draftRow('a1'));
      await accounts.park(a);
      await a.end();
      store.owner = null;

      final client = MockClient((req) async {
        if (req.url.path == '/api/v1/auth/refresh') {
          return http.Response('{}', 401);
        }
        return http.Response('{}', 201);
      });
      final nobody = Session(storage: keystore);
      await nobody.load();

      await Sync(
        store: store,
        api: Api(session: nobody, client: client),
        accounts: accounts,
        apiFor: (session, onRefused) =>
            Api(session: session, client: client, onSignedOut: onRefused),
        reportStatus: false,
      ).run();
      await Future<void>.delayed(Duration.zero);

      expect(
        await accounts.parked(),
        isEmpty,
        reason: 'credentials that cannot send are forgotten',
      );
      expect(
        (await store.outstanding(owner: 'A')).drafts,
        1,
        reason: 'the claim waits for A to sign in on this phone again',
      );
      await store.close();
    });

    test('with nobody signed in and nothing parked, nothing is sent', () async {
      final keystore = _Keystore();
      final store = await freshStore();
      store.owner = 'A';
      await store.putDraft(draftRow('a1'));
      store.owner = null;

      var requests = 0;
      final nobody = Session(storage: keystore);
      await nobody.load();
      await Sync(
        store: store,
        api: Api(
          session: nobody,
          client: MockClient((req) async {
            requests++;
            return http.Response('{}', 201);
          }),
        ),
        accounts: Accounts(storage: keystore),
        reportStatus: false,
      ).run();

      expect(requests, 0, reason: 'there is no token that may send A\'s work');
      expect((await store.outstanding(owner: 'A')).drafts, 1);
      await store.close();
    });
  });

  group('removing a departed farmer', () {
    test('waits while anything of theirs is unsent', () async {
      final store = await freshStore();
      store.owner = 'A';
      await store.putDraft(draftRow('a1'));
      expect(await store.purgeOwnerIfDone('A'), isFalse);
      expect(await store.drafts(owner: 'A'), hasLength(1));
      await store.close();
    });

    test('keeps a recent refusal for a while, then lets it go', () async {
      final store = await freshStore();
      store.owner = 'A';
      await store.putDraft({...draftRow('a1'), 'status': 'failed'});

      expect(await store.purgeOwnerIfDone('A'), isFalse);
      expect(
        await store.purgeOwnerIfDone(
          'A',
          now: DateTime.now().add(const Duration(days: 31)),
        ),
        isTrue,
      );
      expect(await store.drafts(owner: 'A'), isEmpty);
      await store.close();
    });
  });

  group('upgrading from schema 4', () {
    test('records owners and turns unsent drafts into journeys', () async {
      final dir = Directory(p.join(_tmp.path, 'v4'))..createSync();
      final store = await Store.open(directory: dir.path);
      await store.putDraft({
        ...draftRow('unsent'),
        'stations': '[{"station_no":1}]',
      });
      await store.putDraft({...draftRow('sent'), 'server_id': 'R1'});
      await store.close();

      final raw = await openDatabase(p.join(dir.path, 'esurvey.db'));
      await raw.execute('DROP TABLE journeys');
      await raw.setVersion(4);
      await raw.close();

      final upgraded = await Store.open(directory: dir.path);
      final journeys = await upgraded.openJourneys();
      expect(journeys.map((j) => j['local_id']), ['unsent']);
      expect(journeys.single['stage'], 'evidence');
      expect(journeys.single['evidence_mode'], 'stations');

      await upgraded.adoptUnowned('A');
      upgraded.owner = 'A';
      expect(await upgraded.openJourneys(), hasLength(1));
      expect(await upgraded.drafts(), hasLength(2));
      await upgraded.close();
    });
  });
}
