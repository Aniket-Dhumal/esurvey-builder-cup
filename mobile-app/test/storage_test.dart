import 'dart:io';

import 'package:esurvey/core/db.dart';
import 'package:esurvey/core/storage.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:path/path.dart' as p;
import 'package:sqflite_common_ffi/sqflite_ffi.dart';

/// Running out of room, and what is left behind when a write fails.
///
/// These handsets ship with 8 or 16 GB and are full of other people's
/// forwards. Both of the things tested here only ever happen on a phone that
/// is nearly full -- which is the phone least able to absorb the consequences
/// of getting them wrong.

late Directory _tmp;
late Directory _base;

Future<Store> freshStore() async {
  final dir = Directory(
    '${_tmp.path}/db-${DateTime.now().microsecondsSinceEpoch}',
  )..createSync(recursive: true);
  return Store.open(directory: dir.path);
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

Map<String, Object?> captureRow(String localId, String path) => {
  'local_id': localId,
  'draft_local_id': 'd1',
  'kind': 'damage_closeup',
  'stage': 'damage',
  'caption': 'Farmer-captured',
  'file_path': path,
  'content_type': 'image/jpeg',
  'byte_size': 4,
  'sha256': 'abc',
  'captured_at': '2026-08-01T10:05:00Z',
  'device': 'android',
  'status': 'pending',
  'attempts': 0,
};

/// A base path that cannot hold a directory, because it is a file.
Directory _blocked() {
  final file = File('${_base.path}/not-a-directory')..writeAsStringSync('x');
  return Directory(file.path);
}

/// Write a file into the photograph directory the way a capture would.
Future<File> photoFile(String name) async {
  final dir = await Photos.directory(into: _base);
  return File(p.join(dir.path, name))..writeAsBytesSync([1, 2, 3, 4]);
}

void main() {
  setUpAll(() {
    sqfliteFfiInit();
    databaseFactory = databaseFactoryFfi;
    _tmp = Directory.systemTemp.createTempSync('esurvey-storage-test');
  });

  setUp(() {
    _base = Directory(
      '${_tmp.path}/run-${DateTime.now().microsecondsSinceEpoch}',
    )..createSync(recursive: true);
  });

  tearDownAll(() {
    try {
      _tmp.deleteSync(recursive: true);
    } catch (_) {
      // Windows holds handles briefly after close.
    }
  });

  group('is there room for another photograph', () {
    test('a writable directory has room', () async {
      expect(await Photos.hasRoom(bytes: 1024, into: _base), isTrue);
    });

    test('the probe file is not left behind', () async {
      // It is two megabytes on a phone that is short of space. Leaving it
      // would make the check itself part of the problem it is checking for.
      await Photos.hasRoom(bytes: 1024, into: _base);

      final dir = await Photos.directory(into: _base);
      expect(dir.listSync(), isEmpty);
    });

    test('a directory that cannot be written to has no room', () async {
      // Stands in for a full disk: what the caller needs is a false, and the
      // reason the write failed is not something it can act on differently.
      //
      // A file used as a parent directory is the portable way to make a
      // write impossible -- no filesystem will create a folder inside a
      // regular file, and unlike a permission trick it behaves the same on
      // Windows and on the handset.
      expect(await Photos.hasRoom(bytes: 1024, into: _blocked()), isFalse);
    });

    test('a refusal is a false and never an exception', () async {
      // It is called from a catch block in the capture path. One that threw
      // would replace a storage error with a crash.
      await expectLater(
        Photos.hasRoom(bytes: 1024, into: _blocked()),
        completion(isFalse),
      );
    });
  });

  group('photographs no row points at', () {
    test('an orphan is removed', () async {
      // The file was written and the row was not: a disk that filled between
      // the two operations. Nothing will ever upload it, because the queue is
      // built from rows, and nothing will ever delete it, because deletion
      // happens when a row is sent.
      final store = await freshStore();
      final orphan = await photoFile('orphan.jpg');

      expect(await Photos.sweepOrphans(store, into: _base), 1);
      expect(orphan.existsSync(), isFalse);

      await store.close();
    });

    test('a photograph with a row is left alone', () async {
      final store = await freshStore();
      await store.putDraft(draftRow('d1'));

      final kept = await photoFile('kept.jpg');
      await store.putCapture(captureRow('c1', kept.path));

      expect(await Photos.sweepOrphans(store, into: _base), 0);
      expect(
        kept.existsSync(),
        isTrue,
        reason: 'this one is queued and is the only copy of the evidence',
      );

      await store.close();
    });

    test('a failed photograph is kept, not swept', () async {
      // Its row is still there, so it is not an orphan. It is also the
      // farmer's only copy of something an officer may yet ask about, which
      // is exactly why "failed" does not mean "delete".
      final store = await freshStore();
      await store.putDraft(draftRow('d1'));

      final failed = await photoFile('failed.jpg');
      await store.putCapture({
        ...captureRow('c1', failed.path),
        'status': 'failed',
        'attempts': 5,
      });

      expect(await Photos.sweepOrphans(store, into: _base), 0);
      expect(failed.existsSync(), isTrue);

      await store.close();
    });

    test('orphans go and the queued ones stay, in one pass', () async {
      final store = await freshStore();
      await store.putDraft(draftRow('d1'));

      final kept = await photoFile('kept.jpg');
      await store.putCapture(captureRow('c1', kept.path));

      final orphans = [
        await photoFile('a.jpg'),
        await photoFile('b.jpg'),
        await photoFile('c.jpg'),
      ];

      expect(await Photos.sweepOrphans(store, into: _base), 3);
      expect(kept.existsSync(), isTrue);
      for (final o in orphans) {
        expect(o.existsSync(), isFalse);
      }

      await store.close();
    });

    test('signing out takes every photograph off the phone', () async {
      // The borrowed-handset case, and the whole point of signing out.
      // clearPersonalData deletes rows and knows nothing about files -- with
      // every row gone each file is an orphan, which is what makes the sweep
      // the right tool rather than a second deletion path to keep in step.
      final store = await freshStore();
      await store.putDraft(draftRow('d1'));

      final mine = await photoFile('mine.jpg');
      await store.putCapture(captureRow('c1', mine.path));
      expect(await Photos.sweepOrphans(store, into: _base), 0);

      await store.clearPersonalData();
      expect(await Photos.sweepOrphans(store, into: _base), 1);
      expect(
        mine.existsSync(),
        isFalse,
        reason: 'the next person to pick this phone up must not find it',
      );

      await store.close();
    });

    test('nothing to sweep is not a failure', () async {
      final store = await freshStore();
      expect(await Photos.sweepOrphans(store, into: _base), 0);
      await store.close();
    });

    test('a sweep that cannot run returns zero rather than throwing', () async {
      // It runs at startup. Housekeeping must never be the reason the app
      // fails to open, because a farmer cannot do anything about it and the
      // queue is fine either way.
      final store = await freshStore();
      await store.close(); // the store is gone; every query will throw

      await expectLater(Photos.sweepOrphans(store, into: _base), completion(0));
    });
  });
}
