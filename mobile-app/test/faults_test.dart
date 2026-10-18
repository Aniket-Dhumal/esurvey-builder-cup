import 'dart:io';

import 'package:esurvey/core/db.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:path/path.dart' as p;
import 'package:sqflite_common_ffi/sqflite_ffi.dart';

/// Errors nobody was going to hear about.
///
/// The app had no error handler of any kind and has ninety-odd fire-and-forget
/// futures, so a throw in any of them either killed a walk in progress or
/// vanished. Killing it is the worse outcome: a farmer three points into a walk
/// loses the walk, and the record of why is on a phone with no signal.
///
/// These tests are about the store behind that: bounded, readable, and never
/// itself a reason for the app to fail.

late Directory _tmp;

void main() {
  setUpAll(() {
    sqfliteFfiInit();
    databaseFactory = databaseFactoryFfi;
    _tmp = Directory.systemTemp.createTempSync('esurvey-faults-test');
  });

  tearDownAll(() {
    try {
      _tmp.deleteSync(recursive: true);
    } catch (_) {
      // Windows holds the file for a moment after close.
    }
  });

  Future<Store> store(String name) async {
    final dir = Directory(p.join(_tmp.path, name))..createSync();
    final s = await Store.open(directory: dir.path);
    addTearDown(s.close);
    return s;
  }

  test('a fault is written down and read back newest first', () async {
    final s = await store('order');
    await s.recordFault(where: 'async', message: 'first', stack: '#0 a');
    await s.recordFault(where: 'flutter', message: 'second', stack: '#0 b');

    final rows = await s.faults();
    expect(rows.map((r) => r['message']), ['second', 'first']);
    expect(rows.first['where_at'], 'flutter');
    expect(rows.first['stack'], '#0 b');
    expect(rows.first['at'], isA<int>());
  });

  test('the table is bounded, and trimmed on write', () async {
    // An unbounded table on a 2 GB handset is its own defect, and a fault storm
    // is exactly when nobody is running a sweep -- so the trim happens on the
    // write rather than on a schedule.
    final s = await store('bounded');
    for (var i = 0; i < 30; i++) {
      await s.recordFault(
        where: 'async',
        message: 'fault $i',
        stack: '#0 frame',
        keep: 10,
      );
    }

    final rows = await s.faults();
    expect(rows, hasLength(10));
    // And it keeps the newest, which are the useful ones.
    expect(rows.first['message'], 'fault 29');
    expect(rows.last['message'], 'fault 20');
  });

  test('signing out with nothing waiting clears them', () async {
    final s = await store('cleared');
    await s.recordFault(where: 'async', message: 'x', stack: '');
    expect(await s.faults(), hasLength(1));

    await s.clearFaults();
    expect(await s.faults(), isEmpty);
  });

  test('an empty log is an ordinary state, not an error', () async {
    final s = await store('empty');
    expect(await s.faults(), isEmpty);
  });

  test('a fault with no stack is still stored', () async {
    // PlatformDispatcher.onError can hand over a null stack, and a handler that
    // threw on that would turn one error into two.
    final s = await store('nostack');
    await s.recordFault(where: 'async', message: 'no trace', stack: '');
    expect((await s.faults()).single['message'], 'no trace');
  });

  test('a long stack is stored as given, having been trimmed upstream', () async {
    // Faults trims to the top twelve frames before it gets here; the store does
    // not second-guess that. Asserted so nobody adds a second truncation and
    // loses the frame that mattered.
    final s = await store('long');
    final stack = List.generate(12, (i) => '#$i frame').join('\n');
    await s.recordFault(where: 'flutter', message: 'm', stack: stack);
    expect((await s.faults()).single['stack'], stack);
  });
}
