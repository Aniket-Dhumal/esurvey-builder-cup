import 'dart:io';

import 'package:esurvey/core/db.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:sqflite_common_ffi/sqflite_ffi.dart';

/// The two ways a farmer's work disappears without anybody being told.
///
/// Both are quiet, which is what makes them the worst faults in the app. A
/// queue that stops draining is visible. A queue that stops *counting* looks
/// exactly like a queue that finished.

late Directory _tmp;

Future<Store> freshStore() async {
  final dir = Directory('${_tmp.path}/${DateTime.now().microsecondsSinceEpoch}')
    ..createSync(recursive: true);
  return Store.open(directory: dir.path);
}

Map<String, Object?> draftRow(String localId, {String? serverId}) => {
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
  'server_id': ?serverId,
};

Map<String, Object?> captureRow(String localId, String draftLocalId) => {
  'local_id': localId,
  'draft_local_id': draftLocalId,
  'kind': 'damage_closeup',
  'stage': 'damage',
  'caption': 'Farmer-captured',
  'file_path': '/tmp/$localId.jpg',
  'content_type': 'image/jpeg',
  'byte_size': 4,
  'sha256': 'abc',
  'captured_at': '2026-08-01T10:05:00Z',
  'device': 'android',
  'status': 'pending',
  'attempts': 0,
};

void main() {
  setUpAll(() {
    sqfliteFfiInit();
    databaseFactory = databaseFactoryFfi;
    _tmp = Directory.systemTemp.createTempSync('esurvey-recovery-test');
  });

  tearDownAll(() {
    try {
      _tmp.deleteSync(recursive: true);
    } catch (_) {
      // Windows holds handles briefly after close.
    }
  });

  group('a report the office refused', () {
    test('stops being counted as outstanding', () async {
      // Not a bug -- it is what the pending queries are for. It is recorded
      // here because it is the reason `stuck` has to exist: the moment the
      // office refuses a row it leaves every count the home screen was
      // reading.
      final store = await freshStore();
      await store.putDraft({
        ...draftRow('d1'),
        'status': 'failed',
        'attempts': 5,
      });

      expect((await store.outstanding()).any, isFalse);
      await store.close();
    });

    test('is still found, so somebody can be told', () async {
      final store = await freshStore();
      await store.putDraft({
        ...draftRow('d1'),
        'status': 'failed',
        'attempts': 5,
      });

      expect((await store.stuck()).drafts, 1);
      await store.close();
    });

    test('a row still trying is not counted as stuck', () async {
      // Otherwise the loudest card on the home screen would appear the first
      // time a farmer walks out of signal, which is every day.
      final store = await freshStore();
      await store.putDraft({...draftRow('d1'), 'attempts': 2});

      expect((await store.stuck()).any, isFalse);
      expect((await store.outstanding()).drafts, 1);
      await store.close();
    });

    test('retrying puts it back in the queue', () async {
      // A refusal is usually final, but not always: the farmer may have
      // corrected the boundary, or the office reopened the event. The farmer
      // knows something the refusal did not.
      final store = await freshStore();
      await store.putDraft({
        ...draftRow('d1'),
        'status': 'failed',
        'attempts': 5,
      });

      await store.retryStuck();

      expect((await store.stuck()).any, isFalse);
      expect(
        (await store.outstanding()).drafts,
        1,
        reason: 'it is queued again, not merely un-flagged',
      );
      await store.close();
    });

    test('retrying revives photographs and boundaries too', () async {
      final store = await freshStore();
      await store.putDraft(draftRow('d1'));
      await store.putCapture({
        ...captureRow('c1', 'd1'),
        'status': 'failed',
        'attempts': 5,
      });
      await store.putBoundary(
        farmId: '453',
        surveyNumber: '453',
        points: const [
          {'lat': 18.1, 'lon': 75.8},
        ],
        areaHa: 1.0,
        worstAccuracyM: 6,
        status: 'failed',
        attempts: 5,
      );

      final before = await store.stuck();
      expect(before.captures, 1);
      expect(before.boundaries, 1);

      await store.retryStuck();
      expect((await store.stuck()).any, isFalse);

      await store.close();
    });
  });

  group('work that is waiting to try again', () {
    // The rule the attempt budget used to break: an item the office could not
    // take right now is never abandoned. It waits, and it stays on the home
    // screen while it waits.
    test('is still outstanding while it waits', () async {
      final store = await freshStore();
      final later = DateTime.now().add(const Duration(minutes: 10));
      await store.putDraft({
        ...draftRow('d1'),
        'attempts': 40,
        'next_attempt_at': later.millisecondsSinceEpoch,
      });

      expect(
        (await store.outstanding()).drafts,
        1,
        reason: 'the card must not blink out between attempts',
      );
      expect(
        await store.pendingDrafts(),
        isEmpty,
        reason: 'but it is not tried before it is due',
      );
      expect(
        await store.pendingDrafts(now: later.add(const Duration(seconds: 1))),
        hasLength(1),
      );
      expect(
        (await store.stuck()).any,
        isFalse,
        reason: 'many attempts is not a refusal',
      );
      await store.close();
    });

    test('a wait far beyond any real one is treated as due', () async {
      // The clock moved: set back by the farmer, or wrong when the wait was
      // written. Nothing legitimately waits a week.
      final store = await freshStore();
      await store.putDraft({
        ...draftRow('d1'),
        'next_attempt_at': DateTime.now()
            .add(const Duration(days: 7))
            .millisecondsSinceEpoch,
      });
      expect(await store.pendingDrafts(), hasLength(1));
      await store.close();
    });

    test(
      'the next due time is what the scheduler is asked to wake for',
      () async {
        final store = await freshStore();
        final soon = DateTime.now().add(const Duration(minutes: 3));
        await store.putDraft({
          ...draftRow('d1'),
          'next_attempt_at': soon.millisecondsSinceEpoch,
        });
        await store.putDraft({
          ...draftRow('d2'),
          'next_attempt_at': soon
              .add(const Duration(hours: 1))
              .millisecondsSinceEpoch,
        });
        expect(
          (await store.nextDueAt())?.millisecondsSinceEpoch,
          soon.millisecondsSinceEpoch,
        );
        await store.close();
      },
    );
  });

  group('every queued item has a key of its own', () {
    test('a new draft, photograph and appeal are each given one', () async {
      final store = await freshStore();
      await store.putDraft(draftRow('d1'));
      await store.putCapture(captureRow('c1', 'd1'));

      final d = (await store.drafts()).first;
      final c = (await store.capturesFor('d1')).first;
      expect(d['sync_key'], matches(RegExp(r'^q[0-9a-f]{32}$')));
      expect(c['sync_key'], matches(RegExp(r'^q[0-9a-f]{32}$')));
      expect(d['sync_key'], isNot(c['sync_key']));
      await store.close();
    });

    test('the key survives every update to the row', () async {
      // A key that changed between attempts would make each retry a new
      // request -- the duplicate it exists to prevent.
      final store = await freshStore();
      await store.putDraft(draftRow('d1'));
      final key = (await store.drafts()).first['sync_key'];

      final row = (await store.drafts()).first;
      await store.putDraft({...row, 'attempts': 3, 'last_error': 'busy'});
      await store.putDraft({...draftRow('d1'), 'status': 'pending'});

      expect((await store.drafts()).first['sync_key'], key);
      await store.close();
    });

    test('walking a boundary again is a new request with a new key', () async {
      final store = await freshStore();
      Future<void> walk(double lat) => store.putBoundary(
        farmId: '453',
        surveyNumber: '453',
        points: [
          {'lat': lat, 'lon': 75.8},
        ],
        areaHa: 1.0,
        worstAccuracyM: 6,
      );
      await walk(18.1);
      final first = (await store.boundary('453'))!['sync_key'];

      // Recording an attempt on the same walk keeps it.
      await walk(18.1);
      expect((await store.boundary('453'))!['sync_key'], first);

      // A corrected walk does not.
      await walk(18.2);
      expect((await store.boundary('453'))!['sync_key'], isNot(first));
      await store.close();
    });

    test('the device keeps one identity across calls', () async {
      final store = await freshStore();
      final a = await store.deviceId();
      final b = await store.deviceId();
      expect(a, b);
      expect(a, startsWith('dev-'));
      await store.close();
    });
  });

  group('the counts agree with the lists they describe', () {
    // outstanding() counts in SQL and the sync pass iterates the rows. If the
    // two conditions ever drift apart, the home screen shows a queue card for
    // work no pass will pick up -- a farmer watching a number that never goes
    // down, with nothing wrong that anybody can find.
    test('a sent draft is in neither', () async {
      final store = await freshStore();
      await store.putDraft(draftRow('d1', serverId: 'R1'));

      expect((await store.pendingDrafts()), isEmpty);
      expect((await store.outstanding()).drafts, 0);
      await store.close();
    });

    test('every table counts what its pending query returns', () async {
      final store = await freshStore();

      await store.putDraft(draftRow('d1'));
      await store.putDraft(draftRow('d2', serverId: 'R2'));
      // Refused, so in neither.
      await store.putDraft({...draftRow('d3'), 'status': 'failed'});

      await store.putCapture(captureRow('c1', 'd1'));
      await store.putCapture({...captureRow('c2', 'd1'), 'status': 'sent'});

      await store.putBoundary(
        farmId: '453',
        surveyNumber: '453',
        points: const [
          {'lat': 18.1, 'lon': 75.8},
        ],
        areaHa: 1.0,
        worstAccuracyM: 6,
        status: 'pending',
      );

      final counts = await store.outstanding();
      expect(counts.drafts, (await store.pendingDrafts()).length);
      expect(counts.captures, (await store.pendingCaptures()).length);
      expect(counts.boundaries, (await store.pendingBoundaries()).length);

      // And the values themselves, so a change that broke both in the same
      // direction would still be caught.
      expect(counts.drafts, 1);
      expect(counts.captures, 1);
      expect(counts.boundaries, 1);

      await store.close();
    });
  });

  group('a capture that was interrupted', () {
    test('a draft short of photographs is reported with its count', () async {
      // A flat battery, a dropped phone, a back press. The capture sequence
      // is otherwise only reachable by going forward from the report form, so
      // without this the walk to the field is lost.
      final store = await freshStore();
      await store.putDraft(draftRow('d1'));
      await store.putCapture(captureRow('c1', 'd1'));
      await store.putCapture(captureRow('c2', 'd1'));

      final rows = await store.draftsWithPhotographs();
      expect(rows, hasLength(1));
      expect(rows.first.photographs, 2);
      expect(rows.first.draft['local_id'], 'd1');

      await store.close();
    });

    test('a draft with no photographs at all is reported as zero', () async {
      // The join must not drop it. This is the worst case -- somebody filed
      // and was interrupted before the first shutter press -- and it is
      // exactly the row an INNER JOIN would lose.
      final store = await freshStore();
      await store.putDraft(draftRow('d1'));

      final rows = await store.draftsWithPhotographs();
      expect(rows, hasLength(1));
      expect(rows.first.photographs, 0);

      await store.close();
    });

    test('the count is per draft, not shared between them', () async {
      final store = await freshStore();
      await store.putDraft(draftRow('d1'));
      await store.putDraft(draftRow('d2'));
      await store.putCapture(captureRow('c1', 'd1'));
      await store.putCapture(captureRow('c2', 'd1'));
      await store.putCapture(captureRow('c3', 'd2'));

      final byId = {
        for (final row in await store.draftsWithPhotographs())
          row.draft['local_id']: row.photographs,
      };

      expect(byId['d1'], 2);
      expect(byId['d2'], 1);
      await store.close();
    });

    test('the draft fields come back intact', () async {
      // The home screen hands `local_id` straight to the capture screen. A
      // join that mangled the row would resume the wrong report.
      final store = await freshStore();
      await store.putDraft(draftRow('d1'));

      final row = (await store.draftsWithPhotographs()).first;
      expect(row.draft['crop_name'], 'Soybean');
      expect(row.draft['farm_id'], '453');
      expect(
        row.draft.containsKey('photographs'),
        isFalse,
        reason: 'the count is not one of the draft columns',
      );

      await store.close();
    });

    group('a claim the office no longer has', () {
      // One loss, one claim: filedClaimFor stops a farmer walking the same field
      // twice for one calamity by matching any draft that has left `holding`.
      //
      // It cannot see the office deleting a claim underneath it. The first time a
      // test claim was cleared at the office the handset answered "the claim is
      // filed" about something nothing had a record of, and that parcel could not
      // be claimed again short of wiping the app.

      test('stops blocking a new claim once it is forgotten', () async {
        final store = await freshStore();
        await store.putDraft({
          ...draftRow('d1', serverId: 'R1'),
          'status': 'sent',
        });

        expect(
          await store.filedClaimFor(eventId: 'E1', farmId: '453'),
          isNotNull,
          reason: 'a sent claim blocks a second one, which is the point',
        );

        await store.forgetFiledClaim('d1');

        expect(
          await store.filedClaimFor(eventId: 'E1', farmId: '453'),
          isNull,
          reason: 'the office has no record of it, so it blocks nothing',
        );

        await store.close();
      });

      test('is not put back in the queue by forgetting it', () async {
        // Withdrawn is not holding. Holding would say the claim is the farmer's to
        // send and would put it back in front of the sync engine, which would file
        // a duplicate of a claim the office deliberately removed.
        final store = await freshStore();
        await store.putDraft({
          ...draftRow('d1', serverId: 'R1'),
          'status': 'sent',
        });
        await store.forgetFiledClaim('d1');

        final row = (await store.raw.query('drafts')).first;
        expect(row['status'], Store.statusWithdrawn);
        expect(await store.pendingDrafts(), isEmpty);

        await store.close();
      });

      test('a claim that is still queued keeps blocking', () async {
        // Not yet acknowledged, so there is nothing to ask the office about. It is
        // either still going or it failed, and both are the farmer's own claim
        // rather than a ghost -- walking the field again would waste the walk.
        final store = await freshStore();
        await store.putDraft({...draftRow('d1'), 'status': 'pending'});

        expect(
          await store.filedClaimFor(eventId: 'E1', farmId: '453'),
          isNotNull,
        );

        await store.close();
      });

      test('forgetting one claim leaves another parcel alone', () async {
        final store = await freshStore();
        await store.putDraft({
          ...draftRow('d1', serverId: 'R1'),
          'status': 'sent',
        });
        await store.putDraft({
          ...draftRow('d2', serverId: 'R2'),
          'status': 'sent',
          'farm_id': '999',
          'survey_number': '999',
        });

        await store.forgetFiledClaim('d1');

        expect(await store.filedClaimFor(eventId: 'E1', farmId: '453'), isNull);
        expect(
          await store.filedClaimFor(eventId: 'E1', farmId: '999'),
          isNotNull,
          reason: 'the other parcel was never in question',
        );

        await store.close();
      });
    });

    group('a clip refused over where it was recorded', () {
      // The loop this group exists to close.
      //
      // A filed claim's three station clips were refused SAME_POSITION -- they
      // were recorded 0 to 1 m apart in a field needing 14 -- and the app marked
      // them failed, counted them on the stuck card, and offered the retry
      // button. Every tap sent the same bytes and got the same refusal, three
      // times in one evening, because the position travels in the file. The
      // farmer had four recordings and the office had one.

      Map<String, Object?> refused(String localId, int station) => {
        ...captureRow(localId, 'd1'),
        'kind': 'station_video',
        'content_type': 'video/mp4',
        'station_no': station,
        'status': 'failed',
        'last_error': 'record this point somewhere else in the field',
        'last_error_code': 'SAME_POSITION',
      };

      test('is not put back in the queue by "try again"', () async {
        final store = await freshStore();
        await store.putDraft(draftRow('d1', serverId: 'R1'));
        await store.putCapture(refused('c1', 2));

        await store.retryStuck();

        final rows = await store.raw.query('captures');
        expect(
          rows.first['status'],
          'failed',
          reason: 'retrying the same bytes can only be refused again',
        );

        await store.close();
      });

      test('is not counted on the card whose button cannot help', () async {
        final store = await freshStore();
        await store.putDraft(draftRow('d1', serverId: 'R1'));
        await store.putCapture(refused('c1', 2));

        expect((await store.stuck()).captures, 0);

        await store.close();
      });

      test('a refusal retrying can fix is still revived', () async {
        // The distinction, not a blanket rule. A checksum the office rejected
        // after its own restart is worth sending again, and this must not have
        // become "no refusal is ever retried".
        final store = await freshStore();
        await store.putDraft(draftRow('d1', serverId: 'R1'));
        await store.putCapture({
          ...captureRow('c1', 'd1'),
          'status': 'failed',
          'last_error': 'the file did not match its checksum',
          'last_error_code': 'CHECKSUM_MISMATCH',
        });

        expect((await store.stuck()).captures, 1);
        await store.retryStuck();

        final rows = await store.raw.query('captures');
        expect(rows.first['status'], 'pending');

        await store.close();
      });

      test(
        'a refusal from a build that sent no code is still revived',
        () async {
          // Every clip refused before the code was stored has a null there.
          // Treating null as "cannot be retried" would abandon them.
          final store = await freshStore();
          await store.putDraft(draftRow('d1', serverId: 'R1'));
          await store.putCapture({
            ...captureRow('c1', 'd1'),
            'status': 'failed',
            'last_error': 'the office refused this upload (422)',
          });

          await store.retryStuck();

          final rows = await store.raw.query('captures');
          expect(rows.first['status'], 'pending');

          await store.close();
        },
      );

      test(
        'is offered back to the farmer as a walk, grouped by claim',
        () async {
          final store = await freshStore();
          await store.putDraft(draftRow('d1', serverId: 'R1'));
          await store.putCapture(refused('c2', 2));
          await store.putCapture(refused('c3', 3));
          await store.putCapture(refused('c4', 4));

          final again = await store.capturesToRecordAgain();
          expect(again, hasLength(1), reason: 'one claim, one walk');
          expect(again.first.draftLocalId, 'd1');
          expect(again.first.stations, [2, 3, 4]);
          expect(again.first.count, 3);
          expect(again.first.code, 'SAME_POSITION');

          await store.close();
        },
      );

      test('forgetting a row takes the recording off the phone too', () async {
        // Tens of megabytes the office has already refused. Left behind they
        // would sit until the orphan sweep noticed.
        final store = await freshStore();
        final clip = File('${_tmp.path}/refused.mp4')
          ..writeAsBytesSync([1, 2, 3]);
        await store.putDraft(draftRow('d1', serverId: 'R1'));
        await store.putCapture({...refused('c2', 2), 'file_path': clip.path});

        // Through the path the walk actually takes: a replacement stands at the
        // point, so the refused clip may go.
        await store.putCapture({
          ...captureRow('new2', 'd1'),
          'kind': 'station_video',
          'content_type': 'video/mp4',
          'station_no': 2,
        });
        await store.replaceRefusedAt('d1', 2);

        expect(
          [for (final r in await store.raw.query('captures')) r['local_id']],
          ['new2'],
        );
        expect(clip.existsSync(), isFalse);

        await store.close();
      });

      test('forgetting a row whose file has already gone still works', () async {
        // A farmer who cleared storage, or an Android that reclaimed the cache.
        // The row has to go regardless, or the claim stays stuck behind a file
        // that does not exist.
        final store = await freshStore();
        await store.putDraft(draftRow('d1', serverId: 'R1'));
        await store.putCapture({
          ...refused('c2', 2),
          'file_path': '${_tmp.path}/never-existed.mp4',
        });

        await store.replaceRefusedAt('d1', 2);

        expect(await store.raw.query('captures'), isEmpty);

        await store.close();
      });

      test('does not count as a point that has been recorded', () async {
        // What lets the walk be reopened without deleting anything. The refused
        // clip is still on the phone, but the point it was meant to evidence is
        // not standing, so the walk goes there.
        final store = await freshStore();
        await store.putDraft(draftRow('d1', serverId: 'R1'));
        await store.putCapture({
          ...captureRow('c1', 'd1'),
          'kind': 'station_video',
          'content_type': 'video/mp4',
          'station_no': 1,
          'status': 'sent',
        });
        for (final n in [2, 3, 4]) {
          await store.putCapture(refused('c$n', n));
        }

        expect(
          await store.stationsComplete('d1'),
          {1},
          reason: 'only the clip the office accepted stands',
        );

        await store.close();
      });

      test('survives a farmer who opens the walk and closes it', () async {
        // The fault this group was rewritten for.
        //
        // The rows used to be deleted when the walk opened, so that
        // stationsComplete would send the farmer to those points. A farmer who
        // opened the walk and backed out without recording was left with no
        // clips and no card -- the card is built from these very rows -- and so
        // nothing on the phone said the claim was short of its evidence. Three
        // recordings of a filed claim went exactly that way.
        //
        // Opening the walk now changes nothing, which is what this asserts.
        final store = await freshStore();
        await store.putDraft(draftRow('d1', serverId: 'R1'));
        for (final n in [2, 3, 4]) {
          await store.putCapture(refused('c$n', n));
        }

        final before = await store.capturesToRecordAgain();
        expect(before, hasLength(1));

        // ... the farmer records nothing and comes back.
        final after = await store.capturesToRecordAgain();
        expect(
          after.first.localIds,
          before.first.localIds,
          reason: 'backing out of the walk must cost the farmer nothing',
        );
        expect(await store.raw.query('captures'), hasLength(3));

        await store.close();
      });

      test(
        'is forgotten only once a replacement stands at that point',
        () async {
          final store = await freshStore();
          final clip = File('${_tmp.path}/refused-2.mp4')
            ..writeAsBytesSync([1, 2, 3]);
          await store.putDraft(draftRow('d1', serverId: 'R1'));
          await store.putCapture({...refused('c2', 2), 'file_path': clip.path});
          await store.putCapture(refused('c3', 3));

          // The farmer records point two again.
          await store.putCapture({
            ...captureRow('new2', 'd1'),
            'kind': 'station_video',
            'content_type': 'video/mp4',
            'station_no': 2,
          });
          await store.replaceRefusedAt('d1', 2);

          final rows = await store.raw.query('captures', orderBy: 'station_no');
          expect(
            [for (final r in rows) r['local_id']],
            ['new2', 'c3'],
            reason: 'the replaced clip goes; the one still owed stays',
          );
          expect(clip.existsSync(), isFalse);

          // Point two now stands, while three does not.
          expect(await store.stationsComplete('d1'), {2});

          await store.close();
        },
      );

      test('replacing a point the office accepted takes nothing away', () async {
        // replaceRefusedAt must only ever remove a refusal. A walk re-recording
        // point two must not be able to delete the clip the office holds.
        final store = await freshStore();
        await store.putDraft(draftRow('d1', serverId: 'R1'));
        await store.putCapture({
          ...captureRow('c1', 'd1'),
          'kind': 'station_video',
          'content_type': 'video/mp4',
          'station_no': 1,
          'status': 'sent',
        });

        await store.replaceRefusedAt('d1', 1);

        expect(await store.raw.query('captures'), hasLength(1));

        await store.close();
      });

      test('the accepted point still constrains a replacement', () async {
        // The clip the office took stays on the phone, so its position is still
        // in the spacing rule -- the rule that was broken to begin with.
        final store = await freshStore();
        await store.putDraft(draftRow('d1', serverId: 'R1'));
        await store.putCapture({
          ...captureRow('c1', 'd1'),
          'kind': 'station_video',
          'content_type': 'video/mp4',
          'station_no': 1,
          'lat': 19.1156,
          'lon': 72.8618,
          'status': 'sent',
        });

        final points = await store.recordedStationPoints('d1');
        expect(points, hasLength(1));
        expect(points.first.stationNo, 1);

        await store.close();
      });
    });

    test(
      'a draft that reached the office is still listed with its id',
      () async {
        // The home screen filters on server_id being null, because adding
        // photographs to a report the office already has is not something this
        // app can do. The store reports the fact; the screen decides.
        final store = await freshStore();
        await store.putDraft(draftRow('d1', serverId: 'R1'));

        final row = (await store.draftsWithPhotographs()).first;
        expect(row.draft['server_id'], 'R1');

        await store.close();
      },
    );
  });
}
