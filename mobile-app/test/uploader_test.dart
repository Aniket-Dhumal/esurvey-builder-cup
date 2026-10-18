import 'dart:convert';
import 'dart:io';

import 'package:esurvey/core/api.dart';
import 'package:esurvey/core/session.dart';
import 'package:esurvey/core/uploader.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';

/// Sending a video off a phone that keeps losing signal.
///
/// The interesting cases are all failures: a connection that drops mid-file, a
/// reply that never arrives so the app retries, a refusal that no amount of
/// retrying will fix. Those are the ordinary conditions on these connections,
/// not the exceptional ones.

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

late Directory tmp;

File clipOf(int bytes) {
  final f = File(
    '${tmp.path}/clip-${DateTime.now().microsecondsSinceEpoch}.mp4',
  );
  f.writeAsBytesSync(List<int>.generate(bytes, (i) => i % 251));
  return f;
}

/// A server that accepts chunks, optionally going silent after a few.
///
/// Models the real one closely enough to matter: it is the server's
/// next_chunk that drives the client, not the client's own counting.
MockClient chunkServer({
  int? dropAfterChunk,
  // What a real server answers with: it honours the size the client asked for,
  // up to its own ceiling (survey.MaxChunkBytes). This defaulted to a hardcoded
  // 2 MB, which matched the client's constant at the time and stopped matching
  // when the constant changed -- so the mock was answering with a size no real
  // server would have chosen, and the client now honours the answer.
  int chunkSize = chunkBytes,
  List<String>? seen,
}) {
  var received = 0;
  var next = 0;
  var total = 0;

  return MockClient((req) async {
    seen?.add('${req.method} ${req.url.path}');

    if (req.method == 'POST' && req.url.path.endsWith('/uploads')) {
      final body = jsonDecode(req.body) as Map;
      total = (body['total_bytes'] as num).toInt();
      return http.Response(
        jsonEncode({
          'id': 'up-1',
          'report_id': 'R1',
          'total_bytes': total,
          'received_bytes': received,
          'chunk_size': chunkSize,
          'next_chunk': next,
        }),
        202,
      );
    }

    if (req.method == 'PUT') {
      if (dropAfterChunk != null && next >= dropAfterChunk) {
        throw http.ClientException('Connection closed');
      }
      final sent = req.bodyBytes.length;
      received += sent;
      next += 1;

      final done = received >= total;
      return http.Response(
        jsonEncode({
          'id': 'up-1',
          'report_id': 'R1',
          'total_bytes': total,
          'received_bytes': received,
          'chunk_size': chunkSize,
          'next_chunk': next,
          if (done) 'capture_id': 'cap-1',
        }),
        done ? 201 : 200,
      );
    }

    return http.Response('{}', 404);
  });
}

Future<UploadOutcome> sendWith(MockClient client, File clip) async {
  final api = Api(session: await signedIn(), client: client);
  return Uploader(api: api).send(
    reportId: 'R1',
    uploadKey: 'station-1',
    file: clip,
    contentType: 'video/mp4',
    metadata: const {'kind': 'station_video', 'station_no': 1},
  );
}

void main() {
  setUpAll(() => tmp = Directory.systemTemp.createTempSync('esurvey-upload'));
  tearDownAll(() {
    try {
      tmp.deleteSync(recursive: true);
    } catch (_) {
      // Windows holds handles briefly after close.
    }
  });

  group('a clip that gets through', () {
    test('arrives in chunks and comes back as one capture', () async {
      final clip = clipOf(5 * 1024 * 1024);
      final out = await sendWith(chunkServer(), clip);

      expect(out.complete, isTrue);
      expect(out.captureId, 'cap-1');
      expect(out.sentBytes, clip.lengthSync());
    });

    test('a small clip still takes at least one chunk', () async {
      final clip = clipOf(1024);
      final calls = <String>[];
      final out = await sendWith(chunkServer(seen: calls), clip);

      expect(out.complete, isTrue);
      expect(
        calls.where((c) => c.startsWith('PUT')).length,
        1,
        reason: 'a file smaller than a chunk is one request, not zero',
      );
    });
  });

  group('an upgrade part-way through', _resumeAtServerChunkSize);

  group('a connection that drops', () {
    test('keeps the bytes already acknowledged', () async {
      // The farmer walks out of signal two chunks in. What matters is that the
      // pass reports progress rather than failure: those bytes are at the
      // office and the next pass must not resend them.
      // Sized in chunks rather than megabytes: the chunk size is tuned for
      // the connections farmers actually have -- it went from 2 MB to 512 KB
      // after a trial where cell hops kept killing 2 MB chunks -- and a test
      // that hardcodes it is testing the constant rather than the resume.
      final clip = clipOf(3 * chunkBytes);
      final out = await sendWith(chunkServer(dropAfterChunk: 2), clip);

      expect(out.complete, isFalse);
      expect(out.sentBytes, 2 * chunkBytes);
      expect(
        out.shouldRetry,
        isTrue,
        reason: 'losing signal is not a reason to abandon a claim',
      );
      expect(out.permanentFailure, isNull);
    });

    test('resumes from where the server says, not from zero', () async {
      // The heart of it. The server reports three chunks already in hand, and
      // the client must send the fourth -- not the first.
      final clip = clipOf(4 * chunkBytes);
      const chunk = chunkBytes;
      final puts = <int>[];

      var received = 3 * chunk;
      var next = 3;

      final client = MockClient((req) async {
        if (req.method == 'POST') {
          return http.Response(
            jsonEncode({
              'id': 'up-1',
              'total_bytes': clip.lengthSync(),
              'received_bytes': received,
              'chunk_size': chunk,
              'next_chunk': next,
            }),
            202,
          );
        }
        puts.add(int.parse(req.url.pathSegments.last));
        received += req.bodyBytes.length;
        next += 1;
        final done = received >= clip.lengthSync();
        return http.Response(
          jsonEncode({
            'id': 'up-1',
            'total_bytes': clip.lengthSync(),
            'received_bytes': received,
            'chunk_size': chunk,
            'next_chunk': next,
            if (done) 'capture_id': 'cap-9',
          }),
          done ? 201 : 200,
        );
      });

      final out = await sendWith(client, clip);

      expect(out.complete, isTrue);
      expect(
        puts,
        [3],
        reason:
            'three chunks were already at the office; only the fourth '
            'should have been sent',
      );
    });

    test('an upload already finished server-side is not sent again', () async {
      // The reply to the last chunk was lost, so the app retries the whole
      // file. Resending fifty megabytes over 2G because an acknowledgement
      // went missing is the exact outcome the upload key exists to prevent.
      final clip = clipOf(4 * 1024 * 1024);
      var puts = 0;

      final client = MockClient((req) async {
        if (req.method == 'POST') {
          return http.Response(
            jsonEncode({
              'id': 'up-1',
              'total_bytes': clip.lengthSync(),
              'received_bytes': clip.lengthSync(),
              'chunk_size': 2 * 1024 * 1024,
              'next_chunk': 2,
              'capture_id': 'cap-already',
            }),
            202,
          );
        }
        puts++;
        return http.Response('{}', 200);
      });

      final out = await sendWith(client, clip);

      expect(out.complete, isTrue);
      expect(out.captureId, 'cap-already');
      expect(puts, 0, reason: 'nothing should have been re-sent');
    });
  });

  group('a refusal that retrying will not fix', () {
    test('outside the parcel stops the upload', () async {
      // The server refuses before a byte moves. Retrying would spend a
      // farmer's battery being told the same thing five times.
      final clip = clipOf(3 * 1024 * 1024);
      final client = MockClient(
        (req) async => http.Response(
          jsonEncode({
            'error': {
              'code': 'OUTSIDE_PARCEL',
              'message': 'the capture was taken 900 m from the boundary',
            },
          }),
          422,
        ),
      );

      final out = await sendWith(client, clip);

      expect(out.complete, isFalse);
      expect(out.shouldRetry, isFalse);
      expect(out.permanentFailure, contains('900 m'));
    });

    test('a server fault is retried, not abandoned', () async {
      // 500 is the office's problem, not the farmer's, and their evidence
      // must not be dropped because of it.
      final clip = clipOf(1024);
      final client = MockClient((req) async => http.Response('{}', 500));

      final out = await sendWith(client, clip);

      expect(out.shouldRetry, isTrue);
      expect(out.permanentFailure, isNull);
    });

    test('a file that has vanished is not retried forever', () async {
      final missing = File('${tmp.path}/never-existed.mp4');
      final api = Api(session: await signedIn(), client: chunkServer());

      final out = await Uploader(api: api).send(
        reportId: 'R1',
        uploadKey: 'station-1',
        file: missing,
        contentType: 'video/mp4',
        metadata: const {},
      );

      expect(out.shouldRetry, isFalse);
      expect(out.permanentFailure, isNotNull);
    });
  });

  group('what the server is told', () {
    test('the declared hash is of the file actually being sent', () async {
      // The server checks the assembled bytes against this. A hash of
      // something else would refuse every upload at the last chunk, after the
      // whole file had crossed the connection.
      final clip = clipOf(2048);
      Map<String, Object?>? declared;

      final client = MockClient((req) async {
        if (req.method == 'POST') {
          declared = jsonDecode(req.body) as Map<String, Object?>;
          return http.Response(
            jsonEncode({
              'id': 'up-1',
              'total_bytes': clip.lengthSync(),
              'received_bytes': 0,
              'chunk_size': 2 * 1024 * 1024,
              'next_chunk': 0,
            }),
            202,
          );
        }
        return http.Response(
          jsonEncode({
            'id': 'up-1',
            'total_bytes': clip.lengthSync(),
            'received_bytes': clip.lengthSync(),
            'chunk_size': 2 * 1024 * 1024,
            'next_chunk': 1,
            'capture_id': 'cap-1',
          }),
          201,
        );
      });

      await sendWith(client, clip);

      expect(declared, isNotNull);
      expect(declared!['total_bytes'], clip.lengthSync());
      expect('${declared!['sha256']}'.length, 64);
      expect(
        declared!['upload_key'],
        'station-1',
        reason: 'the key is what makes a retry resume instead of duplicate',
      );
      expect(declared!['station_no'], 1);
    });
  });

  group('at scale', () {
    test('the hash already on the row is sent, not recomputed', () async {
      // Re-hashing sixty megabytes on every resume spends a farmer's battery
      // to learn what the capture row already says.
      final clip = clipOf(4096);
      Map<String, Object?>? declared;
      final client = MockClient((req) async {
        declared = jsonDecode(req.body) as Map<String, Object?>;
        // Already finished on an earlier pass: no chunks needed.
        return http.Response(
          jsonEncode({
            'id': 'up-1',
            'total_bytes': 4096,
            'received_bytes': 4096,
            'chunk_size': chunkBytes,
            'next_chunk': 1,
            'capture_id': 'cap-1',
          }),
          202,
        );
      });

      final known = 'a' * 64;
      await Uploader(
        api: Api(session: await signedIn(), client: client),
      ).send(
        reportId: 'R1',
        uploadKey: 'station-1',
        file: clip,
        contentType: 'video/mp4',
        metadata: const {},
        knownSha256: known,
      );
      expect(declared?['sha256'], known);
    });

    test('the lease is renewed after every chunk', () async {
      final clip = clipOf(3 * chunkBytes + 10);
      var renewed = 0;
      await Uploader(
        api: Api(session: await signedIn(), client: chunkServer()),
      ).send(
        reportId: 'R1',
        uploadKey: 'station-1',
        file: clip,
        contentType: 'video/mp4',
        metadata: const {},
        onChunk: () async => renewed++,
      );
      expect(renewed, 4);
    });

    test(
      'a pass out of time stops between chunks, keeping what was sent',
      () async {
        final clip = clipOf(3 * chunkBytes + 10);
        var chunks = 0;
        final outcome =
            await Uploader(
              api: Api(session: await signedIn(), client: chunkServer()),
            ).send(
              reportId: 'R1',
              uploadKey: 'station-1',
              file: clip,
              contentType: 'video/mp4',
              metadata: const {},
              onChunk: () async => chunks++,
              shouldStop: () => chunks >= 2,
            );
        expect(outcome.complete, isFalse);
        expect(outcome.permanentFailure, isNull);
        expect(outcome.sentBytes, 2 * chunkBytes);
      },
    );

    test('an upload the server swept is started again, not refused', () async {
      final clip = clipOf(4096);
      final client = MockClient((req) async {
        if (req.method == 'POST') {
          return http.Response(
            jsonEncode({
              'id': 'up-1',
              'total_bytes': 4096,
              'received_bytes': 0,
              'chunk_size': chunkBytes,
              'next_chunk': 0,
            }),
            202,
          );
        }
        return http.Response(
          jsonEncode({
            'error': {'code': 'UPLOAD_NOT_FOUND', 'message': 'gone'},
          }),
          404,
        );
      });
      final outcome = await sendWith(client, clip);
      expect(outcome.complete, isFalse);
      expect(
        outcome.permanentFailure,
        isNull,
        reason: 'a swept upload is not a refusal of the clip',
      );
    });
  });
}

// --- an upgrade part-way through an upload -----------------------------------

void _resumeAtServerChunkSize() {
  test('resumes at the size the server is counting in, not this build\'s', () async {
    // The hazard this release introduced. The chunk size went from 2 MB to
    // 512 KB, and the server assembles by concatenating the parts it was given
    // in order -- so the slices of one upload have to agree with each other.
    // `next_chunk` counts them at whatever size the upload began with.
    //
    // A farmer who upgrades part-way through resumes an upload the server is
    // counting in 2 MB chunks. Slicing from this build's constant would read at
    // 1.5 MB where the server already holds 6, and the assembled file would be
    // wrong. The server's checksum catches it and restarts, so nothing is lost --
    // but it costs the farmer the whole clip again over a village connection.
    const serverChunk = 2 * 1024 * 1024;
    final clip = clipOf(4 * serverChunk);
    final offsets = <int>[];

    var received = 3 * serverChunk;
    var next = 3;

    final client = MockClient((req) async {
      if (req.method == 'POST') {
        return http.Response(
          jsonEncode({
            'id': 'up-1',
            'total_bytes': clip.lengthSync(),
            'received_bytes': received,
            // The size this upload was begun at, by an older build.
            'chunk_size': serverChunk,
            'next_chunk': next,
          }),
          202,
        );
      }
      // The bytes actually sent tell us where the client read from.
      offsets.add(received);
      received += req.bodyBytes.length;
      next += 1;
      final done = received >= clip.lengthSync();
      return http.Response(
        jsonEncode({
          'id': 'up-1',
          'total_bytes': clip.lengthSync(),
          'received_bytes': received,
          'chunk_size': serverChunk,
          'next_chunk': next,
          if (done) 'capture_id': 'cap-9',
        }),
        done ? 201 : 200,
      );
    });

    final out = await sendWith(client, clip);

    expect(out.complete, isTrue);
    expect(
      offsets,
      [3 * serverChunk],
      reason:
          'the fourth 2 MB chunk should have been sent, from 6 MB in -- '
          'not a 512 KB slice from 1.5 MB',
    );
    expect(out.sentBytes, clip.lengthSync());
  });

  test(
    'falls back to this build\'s size when the server does not say',
    () async {
      // An older server, or a reply missing the field. Better to slice at a size
      // we know than to divide by zero or send nothing.
      final clip = clipOf(2 * chunkBytes);
      final puts = <int>[];

      var received = 0;
      var next = 0;

      final client = MockClient((req) async {
        if (req.method == 'POST') {
          return http.Response(
            jsonEncode({
              'id': 'up-1',
              'total_bytes': clip.lengthSync(),
              'received_bytes': 0,
              'next_chunk': 0,
            }),
            202,
          );
        }
        puts.add(req.bodyBytes.length);
        received += req.bodyBytes.length;
        next += 1;
        final done = received >= clip.lengthSync();
        return http.Response(
          jsonEncode({
            'id': 'up-1',
            'total_bytes': clip.lengthSync(),
            'received_bytes': received,
            'next_chunk': next,
            if (done) 'capture_id': 'cap-9',
          }),
          done ? 201 : 200,
        );
      });

      final out = await sendWith(client, clip);
      expect(out.complete, isTrue);
      expect(puts, everyElement(lessThanOrEqualTo(chunkBytes)));
    },
  );

  test('a nonsense chunk size is not divided by', () async {
    // A zero or negative figure from a confused server must not produce an
    // infinite loop of empty reads.
    final clip = clipOf(chunkBytes);
    final client = MockClient((req) async {
      if (req.method == 'POST') {
        return http.Response(
          jsonEncode({
            'id': 'up-1',
            'total_bytes': clip.lengthSync(),
            'received_bytes': 0,
            'chunk_size': 0,
            'next_chunk': 0,
          }),
          202,
        );
      }
      return http.Response(
        jsonEncode({
          'id': 'up-1',
          'total_bytes': clip.lengthSync(),
          'received_bytes': clip.lengthSync(),
          'next_chunk': 1,
          'capture_id': 'cap-9',
        }),
        201,
      );
    });

    final out = await sendWith(client, clip);
    expect(out.complete, isTrue);
  });
}
