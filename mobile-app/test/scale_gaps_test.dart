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

/// Gaps found probing the sync system for production scale (2026-09-15), kept
/// as regression tests now they are fixed. See docs/SYNC_SCALE_REVIEW.md.

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

/// The server's refresh rotation, as internal/auth/session.go does it: each
/// token is single-use, presenting a spent one revokes the whole family --
/// except the immediately previous token while its successor is unused, which
/// is answered with a fresh successor (a lost reply, or another process).
class _RotatingOffice {
  final live = <String>{'refresh-1'};
  final successorOf = <String, String>{};
  var revoked = false;
  var n = 1;

  http.Response _issue(String? replacing) {
    final next = 'refresh-${++n}';
    live.add(next);
    if (replacing != null) successorOf[replacing] = next;
    return http.Response(
      jsonEncode({
        'access_token': 'access-$n',
        'expires_in': 900,
        'refresh_token': next,
      }),
      200,
    );
  }

  Future<http.Response> handle(http.Request req) async {
    if (req.url.path == '/api/v1/auth/refresh') {
      final token =
          (jsonDecode(req.body) as Map<String, dynamic>)['refresh_token']
              as String;
      if (revoked) return http.Response('{}', 401);
      if (live.remove(token)) return _issue(token);
      final successor = successorOf[token];
      if (successor != null && live.remove(successor)) {
        final res = _issue(token);
        successorOf[successor] = successorOf[token]!;
        return res;
      }
      if (successor != null) {
        revoked = true;
        live.clear();
      }
      return http.Response('{}', 401);
    }
    return http.Response(jsonEncode({'id': 'R1'}), 201);
  }
}

late Directory _tmp;

void main() {
  setUpAll(() {
    sqfliteFfiInit();
    databaseFactory = databaseFactoryFfi;
    _tmp = Directory.systemTemp.createTempSync('esurvey-gaps-test');
  });

  tearDownAll(() {
    try {
      _tmp.deleteSync(recursive: true);
    } catch (_) {}
  });

  test(
    'the app and the background worker refreshing do not sign the farmer out',
    () async {
      // Two isolates, one keystore. The worker refreshes and rotates the
      // token; the app's Session still holds the spent one in memory.
      final keystore = _Keystore();
      final office = _RotatingOffice();
      final seed = Session(storage: keystore);
      await seed.save(
        accessToken: 'access-1',
        accessTtl: Duration.zero,
        refreshToken: 'refresh-1',
        principal: {'id': 'A'},
      );

      final app = Session(storage: keystore);
      await app.load();
      final worker = Session(storage: keystore);
      await worker.load();

      final workerApi = Api(session: worker, client: MockClient(office.handle));
      expect((await workerApi.get('/api/v1/me')).ok, isTrue);

      // Fifteen minutes later the app's access token is spent as well.
      await app.forgetAccess();
      var lapsed = false;
      final appApi = Api(
        session: app,
        client: MockClient(office.handle),
        onSignedOut: () => lapsed = true,
      );
      await appApi.get('/api/v1/me');

      expect(
        office.revoked,
        isFalse,
        reason: 'the app presented a token the worker had already spent',
      );
      expect(lapsed, isFalse);
    },
  );

  test('a lost reply to a refresh does not sign the farmer out', () async {
    // 2G drops the response after the server has rotated the token.
    final keystore = _Keystore();
    final office = _RotatingOffice();
    final session = Session(storage: keystore);
    await session.save(
      accessToken: 'access-1',
      accessTtl: Duration.zero,
      refreshToken: 'refresh-1',
      principal: {'id': 'A'},
    );
    var dropped = false;
    final api = Api(
      session: session,
      client: MockClient((req) async {
        final res = await office.handle(req);
        if (req.url.path == '/api/v1/auth/refresh' && !dropped) {
          dropped = true;
          throw const SocketException('connection reset');
        }
        return res;
      }),
    );

    await api.get('/api/v1/me'); // rotated on the server, reply lost
    await api.get('/api/v1/me'); // retried with the spent token

    expect(
      office.revoked,
      isFalse,
      reason: 'one dropped response revoked the whole session',
    );
  });

  test(
    'photographs of a report the office refused do not wait for ever',
    () async {
      final dir = Directory('${_tmp.path}/refused')..createSync();
      final store = await Store.open(directory: dir.path);
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
      final photo = File('${dir.path}/p.jpg')..writeAsBytesSync([1, 2, 3]);
      await store.putCapture({
        'local_id': 'c1',
        'draft_local_id': 'd1',
        'kind': 'damage_closeup',
        'stage': 'damage',
        'file_path': photo.path,
        'content_type': 'image/jpeg',
        'byte_size': 3,
        'sha256': 'abc',
        'captured_at': '2026-08-01T10:05:00Z',
        'status': 'pending',
        'attempts': 0,
      });

      final keystore = _Keystore();
      final session = Session(storage: keystore);
      await session.save(
        accessToken: 'a',
        accessTtl: const Duration(minutes: 15),
        refreshToken: 'r',
        principal: {'id': 'A'},
      );
      await Sync(
        store: store,
        api: Api(
          session: session,
          client: MockClient(
            (req) async => http.Response(
              jsonEncode({
                'error': {'code': 'EVENT_CLOSED', 'message': 'closed'},
              }),
              422,
            ),
          ),
        ),
        reportStatus: false,
      ).run();

      final out = await store.outstanding();
      expect(out.drafts, 0, reason: 'the report was refused');
      expect(
        out.captures,
        0,
        reason:
            'its photograph can never be sent, yet still counts as '
            'waiting: the queue card never clears and a departed farmer is '
            'never purged',
      );
      await store.close();
    },
  );
}
