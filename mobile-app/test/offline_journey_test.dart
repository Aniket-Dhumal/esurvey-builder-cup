import 'dart:convert';
import 'dart:io';

import 'package:esurvey/core/api.dart';
import 'package:esurvey/core/db.dart';
import 'package:esurvey/core/farmer_data.dart';
import 'package:esurvey/core/session.dart';
import 'package:esurvey/report/farmer_picker_screen.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';
import 'package:sqflite_common_ffi/sqflite_ffi.dart';

/// The whole offline story, farmer by farmer.
///
/// One phone, one signed-in mobile number, several farmers behind it. Every
/// screen after the picker is answering "for this farmer, right now, with no
/// signal" -- and each of those answers is served from what this phone was
/// told the last time it had one.
///
/// What these are about:
///
///   - the session outliving the app, and the farmer choice with it;
///   - each farmer's record, reports and walk points kept apart, so switching
///     never shows one person the other's land;
///   - the claims, photographs and boundaries of one farmer staying invisible
///     to the next while continuing to send;
///   - the failures that look like success -- a cache that answers for the
///     wrong farmer, a refusal that reads as "wrong details" when it was only
///     a dead network.

late Directory _tmp;

Future<Store> freshStore() async {
  final dir = Directory('${_tmp.path}/${DateTime.now().microsecondsSinceEpoch}')
    ..createSync(recursive: true);
  return Store.open(directory: dir.path);
}

class _FakeSecure implements SecureStore {
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
  final s = Session(storage: _FakeSecure());
  await s.save(
    accessToken: 'a',
    accessTtl: const Duration(minutes: 15),
    refreshToken: 'r',
    principal: {'id': 'acct-1', 'farmer_id': 'F-1'},
  );
  return s;
}

/// A registry record as the farmer-facing route projects it.
String recordBody({
  required String farmerId,
  required String name,
  required String district,
  String village = 'V1',
  String farmId = 'FARM-1',
}) => jsonEncode({
  'farmer_id': farmerId,
  'farmer_name': name,
  'district_lgd_code': district,
  'village_lgd_code': village,
  'parcels': [
    {
      'farm_id': farmId,
      'survey_number': '453',
      'village_lgd_code': village,
      'owner_extent': '1.6',
    },
  ],
});

String eventsBody(List<({String id, String district})> events) => jsonEncode({
  'items': [
    for (final e in events)
      {
        'id': e.id,
        'reference': e.id,
        'calamity_type': 'hailstorm',
        'title': 'Hail in ${e.district}',
        'district_code': e.district,
        'district_name': e.district,
        'incident_start': '2026-08-20',
        'incident_end': '2026-08-21',
        'state': 'open',
      },
  ],
});

Map<String, Object?> draftFor(
  String localId,
  String farmerId, {
  String owner = 'acct-1',
  String farmId = 'FARM-1',
}) => {
  'local_id': localId,
  'owner_id': owner,
  'survey_event_id': 'E1',
  'farmer_id': farmerId,
  'season': 'kharif',
  'crop_year': '2026-27',
  'loss_category': 'standing_crop',
  'calamity_type': 'hailstorm',
  'occurred_at': '2026-08-20T10:00:00Z',
  'farm_id': farmId,
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

Future<String> photoOn(String dir, String name) async {
  final f = File('$dir/$name')..writeAsBytesSync([1, 2, 3, 4]);
  return f.path;
}

Map<String, Object?> captureFor(
  String localId,
  String draftLocalId,
  String path, {
  String owner = 'acct-1',
}) => {
  'local_id': localId,
  'owner_id': owner,
  'draft_local_id': draftLocalId,
  'kind': 'damage_closeup',
  'stage': 'damage',
  'file_path': path,
  'content_type': 'image/jpeg',
  'byte_size': 4,
  'sha256': 'abc',
  'captured_at': '2026-08-21T10:00:00Z',
  'status': 'pending',
  'attempts': 0,
};

void main() {
  setUpAll(() {
    sqfliteFfiInit();
    databaseFactory = databaseFactoryFfi;
    _tmp = Directory.systemTemp.createTempSync('esurvey-offline-journey');
  });

  tearDownAll(() {
    try {
      _tmp.deleteSync(recursive: true);
    } catch (_) {
      // Windows holds handles briefly after close.
    }
  });

  // ---------------------------------------------------------------- session

  group('the session, and who it is working as', () {
    test('the farmer choice survives the app being closed', () async {
      final store = await freshStore();
      store.owner = 'acct-1';
      await store.linkFarmer(farmerId: 'F-2', name: 'Hari', village: 'V1');
      await store.setDeviceValue('active_farmer', 'F-2');

      // The app is killed and opened again: a new Store over the same file.
      // Closed first, as a killed app's would be, so this does not depend on
      // two handles to one database file behaving on every platform.
      final dir = File(store.raw.path).parent.path;
      await store.close();
      final reopened = await Store.open(directory: dir);
      reopened.owner = 'acct-1';

      final last = await reopened.deviceValue('active_farmer');
      expect(last, 'F-2');
      expect(
        await reopened.linkedFarmer(last!, owner: 'acct-1'),
        isNotNull,
        reason: 'the picker offers the last choice in one tap',
      );
    });

    test('a farmer removed since is not silently worked as', () async {
      // The remembered choice is checked against the list rather than
      // trusted. Otherwise the app scopes every screen to a farmer it holds
      // no grant for, and every refresh answers 404 with no explanation.
      final store = await freshStore();
      store.owner = 'acct-1';
      await store.linkFarmer(farmerId: 'F-2', name: 'Hari', village: 'V1');
      await store.setDeviceValue('active_farmer', 'F-2');
      await store.unlinkFarmer('F-2');

      final last = await store.deviceValue('active_farmer');
      expect(last, 'F-2');
      expect(
        await store.linkedFarmer(last!, owner: 'acct-1'),
        isNull,
        reason: 'boot must fall back to the picker',
      );
    });

    test('the device key does not survive a sign-out', () async {
      final store = await freshStore();
      store.owner = 'acct-1';
      await store.linkFarmer(farmerId: 'F-2', name: 'Hari', village: 'V1');
      await store.clearPersonalData();
      expect(await store.linkedFarmers(), isEmpty);
    });
  });

  // ------------------------------------------------------- data, per farmer

  group('each farmer\'s own data', () {
    test('two records on one phone do not overwrite each other', () async {
      // The failure this guards: one cache key for "the farmer record" meant
      // the second farmer's land replaced the first's, and offline the app
      // showed whoever had been fetched last under whichever name was picked.
      final store = await freshStore();
      final client = MockClient((req) async {
        if (req.url.path.endsWith('/F-1')) {
          return http.Response(
            recordBody(farmerId: 'F-1', name: 'Sitabai', district: 'D-SOL'),
            200,
          );
        }
        return http.Response(
          recordBody(
            farmerId: 'F-2',
            name: 'Hari',
            district: 'D-SOL',
            farmId: 'FARM-2',
          ),
          200,
        );
      });
      final data = FarmerData(
        store: store,
        api: Api(session: await signedIn(), client: client),
      );

      await data.record('F-1');
      await data.record('F-2');

      final one = await data.record('F-1', refresh: false);
      final two = await data.record('F-2', refresh: false);
      expect(one.value?['farmer_name'], 'Sitabai');
      expect(two.value?['farmer_name'], 'Hari');
    });

    test('a farmer added while online opens with no signal at all', () async {
      var online = true;
      final store = await freshStore();
      final client = MockClient((req) async {
        if (!online) throw http.ClientException('Failed host lookup');
        return http.Response(
          recordBody(farmerId: 'F-1', name: 'Sitabai', district: 'D-SOL'),
          200,
        );
      });
      final data = FarmerData(
        store: store,
        api: Api(session: await signedIn(), client: client),
      );

      await data.record('F-1');
      online = false;

      final offline = await data.record('F-1');
      expect(offline.value?['farmer_name'], 'Sitabai');
      expect(offline.fresh, isFalse);
    });

    test('reports are kept apart per farmer', () async {
      final store = await freshStore();
      final client = MockClient((req) async {
        final id = req.url.queryParameters['farmer_id'];
        return http.Response(
          jsonEncode({
            'items': [
              {'id': 'R-$id', 'reference': 'REF-$id'},
            ],
          }),
          200,
        );
      });
      final data = FarmerData(
        store: store,
        api: Api(session: await signedIn(), client: client),
      );

      await data.reports('F-1');
      await data.reports('F-2');

      final one = await data.reports('F-1', refresh: false);
      expect((one.value!.first as Map)['id'], 'R-F-1');
    });

    test('only events for this farmer\'s district are offered', () async {
      // A farmer sees every open event in the state: the listing is not
      // narrowed for them, because a farmer holds no jurisdiction. Offering
      // all of them is not a cosmetic problem -- the home screen says "for
      // your area", the form takes one without asking, and a claim filed
      // against another district's event is gathered in full and then refused
      // on arrival for a village the event does not cover.
      final store = await freshStore();
      final client = MockClient((req) async {
        if (req.url.path.contains('/events')) {
          return http.Response(
            eventsBody([
              (id: 'E-NAG', district: 'D-NAG'),
              (id: 'E-SOL', district: 'D-SOL'),
            ]),
            200,
          );
        }
        return http.Response(
          recordBody(farmerId: 'F-1', name: 'Sitabai', district: 'D-SOL'),
          200,
        );
      });
      final data = FarmerData(
        store: store,
        api: Api(session: await signedIn(), client: client),
      );

      final record = (await data.record('F-1')).value;
      final all = (await data.events()).value ?? const [];
      expect(all, hasLength(2), reason: 'the office sends every open event');

      final mine = eventsForFarmer(all, record);
      expect(mine, hasLength(1));
      expect((mine.first as Map)['id'], 'E-SOL');
    });

    test(
      'with no record to place them, every event is still offered',
      () async {
        // A farmer whose record has not arrived is not shown an empty screen:
        // the office is the authority on coverage and refuses what it must.
        final all =
            jsonDecode(eventsBody([(id: 'E-NAG', district: 'D-NAG')]))['items']
                as List<dynamic>;
        expect(eventsForFarmer(all, null), hasLength(1));
      },
    );

    test('the walk points survive for each parcel', () async {
      var online = true;
      final store = await freshStore();
      final client = MockClient((req) async {
        if (!online) throw http.ClientException('Failed host lookup');
        return http.Response(
          jsonEncode({
            'stations': [
              for (var i = 1; i <= 4; i++)
                {'station_no': i, 'lat': 18.1 + i / 1000, 'lon': 75.8},
            ],
          }),
          200,
        );
      });
      final data = FarmerData(
        store: store,
        api: Api(session: await signedIn(), client: client),
      );

      await data.prefetch([
        {'farm_id': 'FARM-1'},
        {'farm_id': 'FARM-2'},
      ]);
      online = false;

      expect((await data.stations('FARM-1')).value, hasLength(4));
      expect((await data.stations('FARM-2')).value, hasLength(4));
    });
  });

  // --------------------------------------------------- journeys, per farmer

  group('the work on the phone, farmer by farmer', () {
    test('photographs follow their claim, not the handset', () async {
      final store = await freshStore();
      store.owner = 'acct-1';
      final dir = Directory('${_tmp.path}/photos')..createSync();

      await store.putDraft(draftFor('d1', 'F-1'));
      await store.putDraft(draftFor('d2', 'F-2'));
      await store.putCapture(
        captureFor('c1', 'd1', await photoOn(dir.path, 'a.jpg')),
      );
      await store.putCapture(
        captureFor('c2', 'd2', await photoOn(dir.path, 'b.jpg')),
      );

      store.farmer = 'F-1';
      expect((await store.outstanding()).captures, 1);
      store.farmer = 'F-2';
      expect((await store.outstanding()).captures, 1);
      store.farmer = null;
      expect((await store.outstanding()).captures, 2);
    });

    test('a boundary is attributed to the farmer who walked it', () async {
      // Two farmers in one household may hold neighbouring parcels, and a
      // boundary walked for one is not the other's to see. Without an owner
      // on the row the queue card counted somebody else's work as yours.
      final store = await freshStore();
      store.owner = 'acct-1';

      await store.putBoundary(
        farmId: 'FARM-1',
        surveyNumber: '453',
        points: [
          {'lat': 18.17, 'lon': 75.81, 'accuracy_m': 5.0, 'at': 'now'},
        ],
        areaHa: 1.0,
        worstAccuracyM: 5,
        farmerId: 'F-1',
      );

      store.farmer = 'F-1';
      expect((await store.outstanding()).boundaries, 1);
      store.farmer = 'F-2';
      expect(
        (await store.outstanding()).boundaries,
        0,
        reason: 'another farmer\'s parcel is not this one\'s work',
      );
    });

    test('an unfinished claim belongs to one farmer only', () async {
      final store = await freshStore();
      store.owner = 'acct-1';

      await store.putJourney({
        'local_id': 'j1',
        'owner_id': 'acct-1',
        'farmer_id': 'F-1',
        'event_id': 'E1',
        'farm_id': 'FARM-1',
        'survey_number': '453',
        'stage': 'evidence',
      });

      store.farmer = 'F-1';
      expect(await store.openJourneys(), hasLength(1));
      store.farmer = 'F-2';
      expect(await store.openJourneys(), isEmpty);

      // And the same parcel does not continue somebody else's claim.
      expect(
        await store.openJourneyFor(eventId: 'E1', farmId: 'FARM-1'),
        isNull,
        reason: 'two farmers may hold the same survey number between them',
      );
    });
  });

  // ---------------------------------------------------------------- offline

  group('what the farmer is told with no network', () {
    test(
      'adding a farmer says the network is missing, not the details',
      () async {
        // The failure that reads as success in reverse: offline, the lookup
        // could not be made at all, and telling somebody their Farmer ID and
        // date of birth do not match sends them to look for a document that was
        // never the problem.
        final store = await freshStore();
        final client = MockClient((req) async {
          throw http.ClientException('Failed host lookup');
        });
        final data = FarmerData(
          store: store,
          api: Api(session: await signedIn(), client: client),
        );

        final result = await data.lookup('F-1', '01-01-1973');
        expect(result.offline, isTrue);
        expect(result.found, isNull);
      },
    );

    test('a wrong answer is a refusal, not a network problem', () async {
      final store = await freshStore();
      final client = MockClient(
        (req) async => http.Response('{"error":"no"}', 404),
      );
      final data = FarmerData(
        store: store,
        api: Api(session: await signedIn(), client: client),
      );

      final result = await data.lookup('F-1', '01-01-1900');
      expect(result.offline, isFalse);
      expect(result.found, isNull);
    });

    test('switching farmers needs no signal', () async {
      var online = true;
      final store = await freshStore();
      store.owner = 'acct-1';
      final client = MockClient((req) async {
        if (!online) throw http.ClientException('Failed host lookup');
        final f = req.url.path.endsWith('F-2') ? 'F-2' : 'F-1';
        return http.Response(
          recordBody(
            farmerId: f,
            name: f == 'F-1' ? 'Sitabai' : 'Hari',
            district: 'D-SOL',
          ),
          200,
        );
      });
      final data = FarmerData(
        store: store,
        api: Api(session: await signedIn(), client: client),
      );

      await data.record('F-1');
      await data.record('F-2');
      await store.linkFarmer(farmerId: 'F-1', name: 'Sitabai', village: 'V1');
      await store.linkFarmer(farmerId: 'F-2', name: 'Hari', village: 'V1');

      online = false;

      // The picker, then each farmer's own land, with nothing reachable.
      expect(await store.linkedFarmers(), hasLength(2));
      store.farmer = 'F-2';
      expect((await data.record('F-2')).value?['farmer_name'], 'Hari');
      store.farmer = 'F-1';
      expect((await data.record('F-1')).value?['farmer_name'], 'Sitabai');
    });
  });

  // ----------------------------------------------------- a grant running out

  group('a confirmation that has run out', () {
    int ms(Duration d) => DateTime.now().add(d).millisecondsSinceEpoch;

    test(
      'is shown on the card instead of being met at the end of a walk',
      () async {
        // The office refuses a claim for a farmer this handset cannot show a
        // grant for. Working as them anyway means a form filled in, four points
        // walked and a field photographed, and a refusal on sending.
        expect(
          FarmerPickerScreen.needsConfirming({
            'grant_token': 'g',
            'grant_expires_at': ms(const Duration(days: -1)),
          }),
          isTrue,
        );
        expect(
          FarmerPickerScreen.needsConfirming({
            'grant_token': 'g',
            'grant_expires_at': ms(const Duration(days: 30)),
          }),
          isFalse,
        );
      },
    );

    test("the account's own farmer never needs one", () async {
      // Their registered number signed in. The office asks nothing further.
      expect(
        FarmerPickerScreen.needsConfirming({
          'grant_token': null,
          'grant_expires_at': null,
        }),
        isFalse,
      );
    });

    test('and the queue stops offering the expired grant', () async {
      final store = await freshStore();
      store.owner = 'acct-1';
      await store.linkFarmer(
        farmerId: 'F-1',
        name: 'Sitabai',
        village: 'V1',
        grantToken: 'stale',
        grantExpiresAt: ms(const Duration(days: -1)),
      );
      expect(
        await store.grantFor('F-1'),
        isNull,
        reason: 'sending it would only be refused',
      );
    });
  });

  // ------------------------------------------------------------------ scale

  group('a handset that has been in use for a season', () {
    test('many farmers and many claims stay correctly apart', () async {
      final store = await freshStore();
      store.owner = 'acct-1';

      for (var f = 0; f < 12; f++) {
        await store.linkFarmer(
          farmerId: 'F-$f',
          name: 'Farmer $f',
          village: 'V1',
        );
        for (var d = 0; d < 20; d++) {
          await store.putDraft(draftFor('d-$f-$d', 'F-$f'));
        }
      }

      expect(await store.linkedFarmers(), hasLength(12));

      store.farmer = 'F-7';
      final theirs = await store.drafts();
      expect(theirs, hasLength(20));
      expect(
        theirs.every((r) => r['farmer_id'] == 'F-7'),
        isTrue,
        reason: 'no other farmer\'s claim may appear in this list',
      );

      store.farmer = null;
      expect(await store.pendingDrafts(), hasLength(240));
    });
  });

  _progressTests();
}

/// What a farmer is told about a claim that is already with the office.
///
/// This is the screen opened weeks after filing, usually from a field, usually
/// anxiously. Two things have to hold: it must answer offline from what the
/// phone was last told, and it must not release the officers' remarks or the
/// loss figure before both officers have recorded -- the office withholds
/// them, and the app must render that absence as "still to come" rather than
/// as nothing at all.
void _progressTests() {
  String progressBody({
    required String reference,
    required List<({String key, String state, String why})> stages,
    double? percent,
    List<({String role, String text})> remarks = const [],
  }) => jsonEncode({
    'reference': reference,
    'stages': [
      for (final s in stages)
        {'key': s.key, 'state': s.state, if (s.why.isNotEmpty) 'why': s.why},
    ],
    'assessed_percent': ?percent,
    if (remarks.isNotEmpty)
      'remarks': [
        for (final r in remarks)
          {'role': r.role, 'text': r.text, 'at': '2026-09-01T10:00:00Z'},
      ],
  });

  group('how far a claim has got', () {
    test('answers from the phone when there is no signal', () async {
      var online = true;
      final store = await freshStore();
      final client = MockClient((req) async {
        if (!online) throw http.ClientException('Failed host lookup');
        return http.Response(
          progressBody(
            reference: 'SOL-2026-000123',
            stages: [
              (key: 'received', state: 'done', why: ''),
              (key: 'field', state: 'current', why: 'awaiting_second_officer'),
            ],
          ),
          200,
        );
      });
      final data = FarmerData(
        store: store,
        api: Api(session: await signedIn(), client: client),
      );

      await data.progress('R1');
      online = false;

      final offline = await data.progress('R1');
      expect(offline.value?['reference'], 'SOL-2026-000123');
      expect(offline.fresh, isFalse);
      expect(
        (offline.value?['stages'] as List).length,
        2,
        reason: 'the stages are what the farmer came to read',
      );
    });

    test('two claims do not overwrite each other', () async {
      final store = await freshStore();
      final client = MockClient((req) async {
        final id = req.url.path.contains('R1') ? 'R1' : 'R2';
        return http.Response(
          progressBody(
            reference: 'REF-$id',
            stages: [(key: 'received', state: 'done', why: '')],
          ),
          200,
        );
      });
      final data = FarmerData(
        store: store,
        api: Api(session: await signedIn(), client: client),
      );

      await data.progress('R1');
      await data.progress('R2');

      expect(
        (await data.progress('R1', refresh: false)).value?['reference'],
        'REF-R1',
      );
      expect(
        (await data.progress('R2', refresh: false)).value?['reference'],
        'REF-R2',
      );
    });

    test('nothing is released before both officers have recorded', () async {
      // The office withholds them; this is the app seeing that absence and
      // not inventing anything to fill it.
      final store = await freshStore();
      final client = MockClient(
        (req) async => http.Response(
          progressBody(
            reference: 'SOL-2026-000123',
            stages: [
              (key: 'field', state: 'current', why: 'awaiting_second_officer'),
            ],
          ),
          200,
        ),
      );
      final data = FarmerData(
        store: store,
        api: Api(session: await signedIn(), client: client),
      );

      final p = (await data.progress('R1')).value!;
      expect(p['assessed_percent'], isNull);
      expect(p['remarks'], isNull);
    });

    test('the figure and the remarks arrive together', () async {
      final store = await freshStore();
      final client = MockClient(
        (req) async => http.Response(
          progressBody(
            reference: 'SOL-2026-000123',
            stages: [(key: 'field', state: 'done', why: '')],
            percent: 61,
            remarks: [
              (role: 'talathi', text: 'Standing water across the east.'),
              (role: 'krushi_sahayak', text: 'Soybean at pod fill, flattened.'),
            ],
          ),
          200,
        ),
      );
      final data = FarmerData(
        store: store,
        api: Api(session: await signedIn(), client: client),
      );

      final p = (await data.progress('R1')).value!;
      expect(p['assessed_percent'], 61);
      expect((p['remarks'] as List), hasLength(2));
      for (final r in p['remarks'] as List) {
        expect(
          (r as Map)['by'],
          isNull,
          reason: 'the office sends the rank and not the officer\u2019s name',
        );
      }
    });
  });
}
