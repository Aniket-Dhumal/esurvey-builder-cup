import 'dart:io';

import 'package:esurvey/core/db.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:path/path.dart' as p;
import 'package:sqflite_common_ffi/sqflite_ffi.dart';

/// What holds a sign-out, and what does not.
///
/// The rule: a farmer who has finished a claim must not be able to sign out of
/// the evidence by accident. On the trial a session lapsed, the farmer signed in
/// on a different number, and two and a half recordings sat on the phone
/// invisible for an hour -- while the app said they were on their way.
///
/// The counterweight matters as much. A handset in a village is borrowed, and
/// refusing to release one over a draft somebody is abandoning would make the
/// phone unusable by anybody until signal returned. So the question this count
/// answers is narrow: has the farmer finished, and is the office still missing
/// the evidence?

late Directory _tmp;

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
    _tmp = Directory.systemTemp.createTempSync('esurvey-signout-test');
  });

  tearDownAll(() {
    try {
      _tmp.deleteSync(recursive: true);
    } catch (_) {
      // Windows keeps a handle on a database file for a moment after close.
    }
  });

  Future<Store> store(String name) async {
    final dir = Directory(p.join(_tmp.path, name))..createSync();
    final s = await Store.open(directory: dir.path);
    s.owner = 'A';
    addTearDown(s.close);
    return s;
  }

  // The real shape, taken from the app's own fixtures rather than invented:
  // a draft row this test made up would pass while the column it needs is
  // missing, which is how the first version of this file failed.
  Map<String, Object?> draft(String id, String status) => {
    ...draftRow(id),
    'status': status,
  };

  Map<String, Object?> capture(String id, String draftId, String status) => {
    'local_id': id,
    'draft_local_id': draftId,
    'kind': 'station_video',
    'stage': 'station_1',
    'file_path': '/tmp/$id.mp4',
    'content_type': 'video/mp4',
    'byte_size': 4700000,
    'sha256': 'x',
    'status': status,
    'attempts': 0,
    'captured_at': DateTime.now().toUtc().toIso8601String(),
  };

  group('evidence for a finished claim', () {
    test('holds the sign-out', () async {
      // The case the guard is for: the form is at the office, the recordings
      // are not, and the farmer has done everything asked of them.
      final s = await store('filed');
      await s.putDraft(draft('d1', 'queued'));
      await s.putCapture(capture('c1', 'd1', 'pending'));
      await s.putCapture(capture('c2', 'd1', 'pending'));

      expect(await s.unsentFiledEvidence(owner: 'A'), 2);
    });

    test('counts an item waiting out a backoff', () async {
      // An item due in twenty minutes is no less unsent than one due now, and
      // a farmer signing out would strand it just the same. The queue's own
      // due-date filter must not reach this count.
      final s = await store('backoff');
      await s.putDraft(draft('d1', 'queued'));
      await s.putCapture({
        ...capture('c1', 'd1', 'pending'),
        'next_attempt_at': DateTime.now()
            .add(const Duration(minutes: 20))
            .millisecondsSinceEpoch,
        'attempts': 3,
      });

      expect(await s.unsentFiledEvidence(owner: 'A'), 1);
    });

    test('stops holding it once the office has everything', () async {
      final s = await store('sent');
      await s.putDraft(draft('d1', 'queued'));
      await s.putCapture(capture('c1', 'd1', 'sent'));

      expect(await s.unsentFiledEvidence(owner: 'A'), 0);
    });

    test('does not hold it for evidence that was refused', () async {
      // A refused capture will never reach the office however long the farmer
      // waits. Holding the phone for it would be holding it forever.
      final s = await store('failed');
      await s.putDraft(draft('d1', 'queued'));
      await s.putCapture(capture('c1', 'd1', 'failed'));

      expect(await s.unsentFiledEvidence(owner: 'A'), 0);
    });
  });

  group('work that must not hold the phone', () {
    test('a claim still being gathered', () async {
      // 'holding' means the farmer has not finished filing. They may be
      // abandoning it, and a borrowed handset must not be locked over that --
      // nor is the evidence lost: it stays for the next sign-in on that number.
      final s = await store('holding');
      await s.putDraft(draft('d1', 'holding'));
      await s.putCapture(capture('c1', 'd1', 'pending'));

      expect(await s.unsentFiledEvidence(owner: 'A'), 0);
    });

    test('another account\'s evidence', () async {
      // Signing out of account A must not be blocked by work belonging to B.
      // B's work is B's to send, under B's own parked credentials, and A cannot
      // send it however long they wait.
      final s = await store('other-owner');
      await s.putDraft(draft('d1', 'queued'));
      await s.putCapture(capture('c1', 'd1', 'pending'));

      expect(await s.unsentFiledEvidence(owner: 'A'), 1);
      expect(await s.unsentFiledEvidence(owner: 'B'), 0);
    });
  });

  group('the mix a real phone holds', () {
    test('only the finished claim counts', () async {
      // A farmer part-way through a second claim while the first is still
      // uploading. The first holds the sign-out; the second does not.
      final s = await store('mixed');

      await s.putDraft(draft('done', 'queued'));
      await s.putCapture(capture('c1', 'done', 'pending'));
      await s.putCapture(capture('c2', 'done', 'sent'));

      await s.putDraft(draft('gathering', 'holding'));
      await s.putCapture(capture('c3', 'gathering', 'pending'));
      await s.putCapture(capture('c4', 'gathering', 'pending'));

      expect(
        await s.unsentFiledEvidence(owner: 'A'),
        1,
        reason:
            'one unsent capture on the filed claim, and nothing from the '
            'one still being gathered',
      );

      // And it is the same line the queue draws. Asserted rather than assumed:
      // the first version of unsentFiledEvidence repeated this predicate in its
      // own SQL, and a copy that drifts would let a farmer sign out of evidence
      // the queue still believes it is sending.
      final all = await s.outstanding(owner: 'A', only: Store.everyone);
      expect(all.captures, await s.unsentFiledEvidence(owner: 'A'));
    });
  });
}
