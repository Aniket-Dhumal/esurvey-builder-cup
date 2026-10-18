import 'dart:convert';
import 'dart:io';

import 'package:esurvey/core/api.dart';
import 'package:esurvey/core/db.dart';
import 'package:esurvey/core/farmer_data.dart';
import 'package:esurvey/core/session.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';
import 'package:sqflite_common_ffi/sqflite_ffi.dart';

/// Cache-first, not network-first-with-a-cache.
///
/// The two sound alike and behave completely differently. Network-first shows
/// a spinner, waits for a request that will not arrive, and eventually falls
/// back — so a farmer with no signal watches a loading state before seeing
/// anything they already had. A farmer standing in a field is the normal case,
/// not the degraded one.

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

String eventsBody(List<String> titles) => jsonEncode({
  'items': [
    for (final t in titles) {'id': t, 'title': t},
  ],
});

void main() {
  setUpAll(() {
    sqfliteFfiInit();
    databaseFactory = databaseFactoryFfi;
    _tmp = Directory.systemTemp.createTempSync('esurvey-data-test');
  });

  tearDownAll(() {
    try {
      _tmp.deleteSync(recursive: true);
    } catch (_) {
      // Windows holds handles briefly after close.
    }
  });

  group('what a farmer sees with no signal', () {
    test('the last good answer, not an empty screen', () async {
      final store = await freshStore();
      var online = true;

      final client = MockClient((req) async {
        if (!online) throw http.ClientException('Failed host lookup');
        return http.Response(eventsBody(['Hailstorm']), 200);
      });

      final data = FarmerData(
        store: store,
        api: Api(session: await signedIn(), client: client),
      );

      final first = await data.events();
      expect(first.value, hasLength(1));
      expect(first.fresh, isTrue);

      // The farmer walks out of signal.
      online = false;

      final offline = await data.events();
      expect(
        offline.value,
        hasLength(1),
        reason: 'the phone still knows what it was told',
      );
      expect(offline.fresh, isFalse);
    });

    test('a first run with no signal is empty rather than broken', () async {
      final store = await freshStore();
      final client = MockClient((req) async {
        throw http.ClientException('Failed host lookup');
      });

      final data = FarmerData(
        store: store,
        api: Api(session: await signedIn(), client: client),
      );

      final result = await data.events();
      expect(result.value, isNull);
      expect(result.isEmpty, isTrue);
      // No exception. A screen can render "nothing notified yet", which is
      // true and actionable, rather than an error nobody can do anything with.
    });
  });

  group('how old the answer is', () {
    test('a fresh read carries no age', () async {
      final store = await freshStore();
      final client = MockClient(
        (req) async => http.Response(eventsBody(['Hailstorm']), 200),
      );

      final result = await FarmerData(
        store: store,
        api: Api(session: await signedIn(), client: client),
      ).events();

      expect(result.fresh, isTrue);
      expect(result.age, isNull, reason: 'nothing to warn about');
    });

    test('a stale read says how far behind it is', () async {
      // A farmer looking at a list of open events with no signal is entitled
      // to know it is yesterday's list. Showing it silently is how somebody
      // files against an event that closed on Tuesday.
      final store = await freshStore();

      final stale = Cached<List<dynamic>>(
        const ['x'],
        DateTime.now().subtract(const Duration(hours: 30)),
        fresh: false,
      );
      expect(stale.age, contains('day'));

      final hours = Cached<List<dynamic>>(
        const ['x'],
        DateTime.now().subtract(const Duration(hours: 3)),
        fresh: false,
      );
      expect(hours.age, contains('3'));

      // Coarse on purpose: "127 minutes ago" is arithmetic somebody has to do
      // while standing in a field.
      final recent = Cached<List<dynamic>>(
        const ['x'],
        DateTime.now().subtract(const Duration(minutes: 2)),
        fresh: false,
      );
      expect(recent.age, isNull);
      await store.close();
    });
  });

  group('a server that answers with something unexpected', () {
    test('the last good copy stands rather than blanking the screen', () async {
      // A field renamed upstream must not empty a farmer's screen.
      final store = await freshStore();
      var shape = 'good';

      final client = MockClient((req) async {
        if (shape == 'good') {
          return http.Response(eventsBody(['Hailstorm']), 200);
        }
        return http.Response(jsonEncode({'items': 'not-a-list'}), 200);
      });

      final data = FarmerData(
        store: store,
        api: Api(session: await signedIn(), client: client),
      );

      await data.events();
      shape = 'bad';

      final result = await data.events();
      expect(
        result.value,
        hasLength(1),
        reason: 'the good copy survives a shape this app cannot read',
      );
      expect(result.fresh, isFalse);
    });

    test('a declined request leaves the cache alone', () async {
      final store = await freshStore();
      var status = 200;

      final client = MockClient((req) async {
        if (status == 200) {
          return http.Response(eventsBody(['Hailstorm']), 200);
        }
        return http.Response('{}', 403);
      });

      final data = FarmerData(
        store: store,
        api: Api(session: await signedIn(), client: client),
      );

      await data.events();
      status = 403;

      final result = await data.events();
      expect(result.value, hasLength(1));
    });
  });

  group('reading without asking', () {
    test('refresh: false never touches the network', () async {
      final store = await freshStore();
      var calls = 0;

      final client = MockClient((req) async {
        calls++;
        return http.Response(eventsBody(['Hailstorm']), 200);
      });

      final data = FarmerData(
        store: store,
        api: Api(session: await signedIn(), client: client),
      );

      await data.events();
      expect(calls, 1);

      // What the home screen does first, so the screen is painted before any
      // request is in flight.
      final cached = await data.events(refresh: false);
      expect(calls, 1, reason: 'a cache read is not a request');
      expect(cached.value, hasLength(1));
    });
  });

  group('a fresher answer replaces the old one', () {
    test('the cache is updated, not appended to', () async {
      final store = await freshStore();
      var titles = ['Hailstorm'];

      final client = MockClient(
        (req) async => http.Response(eventsBody(titles), 200),
      );

      final data = FarmerData(
        store: store,
        api: Api(session: await signedIn(), client: client),
      );

      await data.events();
      titles = ['Flood', 'Cyclone'];

      final result = await data.events();
      expect(result.value, hasLength(2));

      final cached = await data.events(refresh: false);
      expect(
        cached.value,
        hasLength(2),
        reason: 'the phone should hold the newer list, not both',
      );
    });
  });

  _stationTests();
}

/// The walk's four points, in a dead zone.
///
/// Placement is the server's job -- PostGIS puts the points on the parcel
/// boundary -- and they used to be fetched at the moment the farmer picked the
/// parcel, on the form screen, standing in the field. No signal there meant no
/// points, and the claim silently dropped to the older photograph sequence:
/// the heaviest evidence in the specification, skipped, with nobody told.
///
/// So they are pulled for every parcel while there is a signal and read off
/// the phone when there is not.
void _stationTests() {
  group('the four points when there is no signal', () {
    String stationsBody() => jsonEncode({
      'stations': [
        for (var i = 1; i <= 4; i++)
          {'station_no': i, 'lat': 18.17 + i / 1000, 'lon': 75.81, 'role': 'x'},
      ],
    });

    test('prefetched while online, walked while offline', () async {
      final store = await freshStore();
      var online = true;
      final asked = <String>[];

      final client = MockClient((req) async {
        if (!online) throw http.ClientException('Failed host lookup');
        asked.add(req.url.path);
        return http.Response(stationsBody(), 200);
      });

      final data = FarmerData(
        store: store,
        api: Api(session: await signedIn(), client: client),
      );

      // The home screen refreshes the record and pulls the points behind it.
      await data.prefetch([
        {'farm_id': 'FARM-1'},
        {'farm_id': 'FARM-2'},
      ]);
      expect(asked, hasLength(2));

      // The farmer drives to the field and loses the signal on the way.
      online = false;

      final points = await data.stations('FARM-1');
      expect(
        points.value,
        hasLength(4),
        reason: 'the walk is directed from the phone, with no signal at all',
      );
      expect(points.fresh, isFalse);
    });

    test('a parcel never seen online has no points, not an error', () async {
      final store = await freshStore();
      final client = MockClient((req) async {
        throw http.ClientException('Failed host lookup');
      });

      final data = FarmerData(
        store: store,
        api: Api(session: await signedIn(), client: client),
      );

      final points = await data.stations('FARM-9');
      expect(points.value, isNull);
      // The form screen reads this as "no points" and offers the photograph
      // sequence, which is the old behaviour and still a filed claim.
    });

    test('one parcel failing does not stop the rest being pulled', () async {
      final store = await freshStore();
      final asked = <String>[];

      final client = MockClient((req) async {
        asked.add(req.url.path);
        // A parcel with no boundary cannot be sampled: the server says so,
        // and it is a real answer rather than a failure to retry.
        if (req.url.path.contains('FARM-2')) {
          return http.Response('{"error":"no boundary"}', 409);
        }
        return http.Response(stationsBody(), 200);
      });

      final data = FarmerData(
        store: store,
        api: Api(session: await signedIn(), client: client),
      );

      await data.prefetch([
        {'farm_id': 'FARM-1'},
        {'farm_id': 'FARM-2'},
        {'farm_id': 'FARM-3'},
      ]);

      expect(asked, hasLength(3));
      expect(
        (await data.stations('FARM-3', refresh: false)).value,
        hasLength(4),
      );
      expect((await data.stations('FARM-2', refresh: false)).value, isNull);
    });
  });

  group('the assessment report of a decided claim', () {
    const page =
        '<!doctype html><html><head><meta charset="utf-8"></head><body>'
        '<section class="report" lang="mr">मराठी</section>'
        '<section class="report" lang="en">English</section></body></html>';

    test('is kept on the phone and opens again with no signal', () async {
      // Taken to an office, or read again in a field: neither has a signal
      // to count on.
      final store = await freshStore();
      var online = true;
      String? grantSent;
      final client = MockClient((req) async {
        if (!online) throw http.ClientException('Failed host lookup');
        grantSent = req.headers['X-Farmer-Grant'];
        expect(req.url.path, '/api/v1/reports/R1/assessment-report');
        return http.Response(
          page,
          200,
          headers: {'content-type': 'text/html; charset=utf-8'},
        );
      });
      final data = FarmerData(
        store: store,
        api: Api(session: await signedIn(), client: client),
      );

      final first = await data.assessmentReport('R1', grant: 'G-1');
      expect(first.html, page);
      expect(first.fresh, isTrue);
      expect(grantSent, 'G-1', reason: 'a second farmer needs their grant');

      online = false;
      final again = await data.assessmentReport('R1');
      expect(again.html, page);
      expect(again.fresh, isFalse);
      expect(again.offline, isTrue);
    });

    test('an undecided claim has none, and says so', () async {
      final store = await freshStore();
      final client = MockClient(
        (_) async => http.Response(
          jsonEncode({
            'error': {'code': 'REPORT_NOT_DECIDED', 'message': 'not yet'},
          }),
          409,
          headers: {'content-type': 'application/json'},
        ),
      );
      final data = FarmerData(
        store: store,
        api: Api(session: await signedIn(), client: client),
      );

      final r = await data.assessmentReport('R2');
      expect(r.notYet, isTrue);
      expect(r.html, isNull);
    });

    test('an error page from a gateway is not kept as the report', () async {
      final store = await freshStore();
      final client = MockClient(
        (_) async => http.Response(
          '<html>502 Bad Gateway</html>',
          502,
          headers: {'content-type': 'text/html'},
        ),
      );
      final data = FarmerData(
        store: store,
        api: Api(session: await signedIn(), client: client),
      );

      final r = await data.assessmentReport('R3');
      expect(r.html, isNull);
    });
  });
}
