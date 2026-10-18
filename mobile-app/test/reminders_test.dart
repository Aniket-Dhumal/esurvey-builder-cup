import 'dart:io';

import 'package:esurvey/core/db.dart';
import 'package:esurvey/core/reminders.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:sqflite_common_ffi/sqflite_ffi.dart';

/// Telling a farmer about a claim that never left the phone.
///
/// A claim is not sent until its evidence is complete, so a farmer interrupted
/// between the form and the walk has a whole claim on the handset that the
/// office will never hear about. Before this, the only thing that would say so
/// was opening the app -- and the background worker exists precisely because
/// that does not happen: a farmer files once a year and has no reason to
/// reopen it.
///
/// These are about the decision, not the plugin: which claims count as
/// unfinished, and for how long they have to have been sitting.

late Directory _tmp;

Future<Store> freshStore() async {
  final dir = Directory('${_tmp.path}/${DateTime.now().microsecondsSinceEpoch}')
    ..createSync(recursive: true);
  return Store.open(directory: dir.path);
}

Map<String, Object?> heldDraft(String id, {required DateTime updatedAt}) => {
  'local_id': id,
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
  'status': Store.statusHolding,
  'attempts': 0,
  'updated_at': updatedAt.millisecondsSinceEpoch,
};

/// The store stamps `updated_at` itself on every put, so a test cannot write
/// a draft into the past. It moves the clock forward instead.
DateTime inHours(int n) => DateTime.now().add(Duration(hours: n));

void main() {
  setUpAll(() {
    sqfliteFfiInit();
    databaseFactory = databaseFactoryFfi;
    _tmp = Directory.systemTemp.createTempSync('esurvey-reminders-test');
  });

  tearDownAll(() {
    try {
      _tmp.deleteSync(recursive: true);
    } catch (_) {
      // Windows holds handles briefly after close.
    }
  });

  group('what counts as unfinished', () {
    test('a claim held since yesterday', () async {
      final store = await freshStore();
      await store.putDraft(heldDraft('d1', updatedAt: DateTime.now()));

      expect(await store.heldSince(Reminders.after, now: inHours(30)), 1);
    });

    test('a claim the farmer is still working on is left alone', () async {
      // Somebody who walked back for a charger and returned in twenty minutes
      // does not need telling. updated_at moves with every photograph, so a
      // walk done over an afternoon never trips this.
      final store = await freshStore();
      await store.putDraft(heldDraft('d1', updatedAt: DateTime.now()));

      expect(await store.heldSince(Reminders.after, now: inHours(2)), 0);
    });

    test('a claim already in the queue is not unfinished', () async {
      // It has its evidence and it is on its way. A reminder here would tell
      // a farmer their claim is incomplete when the only thing it is waiting
      // for is a network.
      final store = await freshStore();
      await store.putDraft(heldDraft('d1', updatedAt: DateTime.now()));
      await store.releaseDraft('d1');

      expect(await store.heldSince(Reminders.after, now: inHours(72)), 0);
    });

    test('a borrowed handset counts every farmer on it', () async {
      // The farmer least able to find out about their own unfinished claim is
      // the one who has handed the phone back.
      final store = await freshStore();
      store.owner = 'A';
      await store.putDraft({
        ...heldDraft('d1', updatedAt: DateTime.now()),
        'owner_id': 'A',
      });
      await store.putDraft({
        ...heldDraft('d2', updatedAt: DateTime.now()),
        'owner_id': 'B',
      });
      expect(
        await store.heldSince(Reminders.after, now: inHours(48)),
        1,
        reason: 'the signed-in farmer sees only their own',
      );
      expect(
        await store.heldSince(
          Reminders.after,
          owner: Store.everyone,
          now: inHours(48),
        ),
        2,
        reason: 'the reminder speaks for the whole handset',
      );
    });
  });
}
