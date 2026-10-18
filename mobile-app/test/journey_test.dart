import 'dart:io';

import 'package:esurvey/capture/station_guide.dart';
import 'package:esurvey/core/db.dart';
import 'package:esurvey/report/journey.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:sqflite_common_ffi/sqflite_ffi.dart';

/// A claim in progress survives the phone being killed at any step, and comes
/// back at that step.

late Directory _tmp;

Future<Store> freshStore() async {
  final dir = Directory('${_tmp.path}/${DateTime.now().microsecondsSinceEpoch}')
    ..createSync(recursive: true);
  return Store.open(directory: dir.path);
}

List<Station> fourPoints() => [
  for (var i = 1; i <= 4; i++)
    Station.fromJson({
      'station_no': i,
      'lat': 18.0 + i / 1000,
      'lon': 75.0,
      'role': 'low',
      'placement': 'geometric',
    }),
];

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

Map<String, Object?> capture(
  String id,
  String draft, {
  required String stage,
  String type = 'image/jpeg',
  int? station,
}) => {
  'local_id': id,
  'draft_local_id': draft,
  'kind': type.startsWith('video') ? 'walkthrough' : 'damage_closeup',
  'stage': stage,
  'file_path': '/nowhere/$id',
  'content_type': type,
  'byte_size': 4,
  'sha256': 'abc',
  'captured_at': '2026-08-01T10:05:00Z',
  'station_no': station,
  'status': 'pending',
  'attempts': 0,
};

void main() {
  setUpAll(() {
    sqfliteFfiInit();
    databaseFactory = databaseFactoryFfi;
    _tmp = Directory.systemTemp.createTempSync('esurvey-journey-test');
  });

  tearDownAll(() {
    try {
      _tmp.deleteSync(recursive: true);
    } catch (_) {
      // Windows holds handles briefly after close.
    }
  });

  group('a form being typed', () {
    test('is saved and read back exactly', () async {
      final store = await freshStore();
      await store.putJourney({
        'local_id': 'j1',
        'farmer_id': 'F1',
        'event_id': 'E1',
        'farm_id': '453',
        'survey_number': '453',
        'stage': 'form',
        'form': {
          'crop': 'Soybean',
          'sown': '1.6',
          'affected': '1.2',
          'damage': 65.0,
          'declared': true,
        },
        'event': {'id': 'E1', 'calamity_type': 'hailstorm'},
        'parcel': {'farm_id': '453', 'survey_number': '453'},
      });

      final j = Journey.fromRow((await store.journey('j1'))!);
      expect(j.stage, 'form');
      expect(j.form['crop'], 'Soybean');
      expect(j.form['damage'], 65.0);
      expect(j.form['declared'], isTrue);
      expect(j.formResumable, isTrue);
      expect(j.event?['calamity_type'], 'hailstorm');
      await store.close();
    });

    test('updates in place as the farmer types more', () async {
      final store = await freshStore();
      final base = {
        'local_id': 'j1',
        'event_id': 'E1',
        'farm_id': '453',
        'stage': 'form',
      };
      await store.putJourney({
        ...base,
        'form': {'crop': 'Soy'},
      });
      final created = (await store.journey('j1'))!['created_at'];
      await store.putJourney({
        ...base,
        'form': {'crop': 'Soybean'},
      });

      final rows = await store.openJourneys();
      expect(rows, hasLength(1));
      expect(Journey.fromRow(rows.single).form['crop'], 'Soybean');
      expect(
        rows.single['created_at'],
        created,
        reason: 'when it was started does not move',
      );
      await store.close();
    });

    test('choosing the same field again continues that claim', () async {
      final store = await freshStore();
      await store.putJourney({
        'local_id': 'j1',
        'event_id': 'E1',
        'farm_id': '453',
        'stage': 'form',
      });

      final same = await store.openJourneyFor(eventId: 'E1', farmId: '453');
      final other = await store.openJourneyFor(eventId: 'E1', farmId: '454');
      expect(same?['local_id'], 'j1');
      expect(other, isNull, reason: 'another parcel is another claim');

      await store.setJourneyStage('j1', 'done');
      expect(
        await store.openJourneyFor(eventId: 'E1', farmId: '453'),
        isNull,
        reason: 'a finished claim is not continued',
      );
      await store.close();
    });
  });

  group('an interrupted walk', () {
    test('resumes at the first point without its clip', () {
      final points = fourPoints();
      expect(firstStationToCapture(points, {}), 0);
      expect(firstStationToCapture(points, {1, 2}), 2);
      expect(
        firstStationToCapture(points, {1, 3}),
        1,
        reason: 'a gap is filled before going on',
      );
      expect(firstStationToCapture(points, {1, 2, 3, 4}), 4);
    });

    test('counts a point done only once its clip is saved', () async {
      // The still is written before the clip. Killed between the two, the
      // point still needs its clip.
      final store = await freshStore();
      await store.putDraft(draftRow('d1'));
      await store.putCapture(
        capture('s1', 'd1', stage: 'station_1', station: 1),
      );
      await store.putCapture(
        capture('v1', 'd1', stage: 'station_1', station: 1, type: 'video/mp4'),
      );
      await store.putCapture(
        capture('s2', 'd1', stage: 'station_2', station: 2),
      );

      expect(await store.stationsWithVideo('d1'), {1});

      final j = Journey.fromRow({
        'local_id': 'd1',
        'stage': 'evidence',
        'stations':
            '[${[for (final s in encodeStations(fourPoints())) '{"station_no":${s['station_no']},"lat":${s['lat']},"lon":${s['lon']}}'].join(',')}]',
        'evidence_mode': 'stations',
      });
      final progress = await evidenceProgress(store, j);
      expect(progress.done, 1);
      expect(progress.total, 4);
      expect(progress.complete, isFalse);
      await store.close();
    });
  });

  group('one loss, one claim', () {
    test('a parcel already claimed for this event is found', () async {
      final store = await freshStore();
      await store.putDraft({
        ...draftRow('d1'),
        'survey_event_id': 'E1',
        'farm_id': 'FARM-A',
        'status': 'pending',
      });

      final filed = await store.filedClaimFor(eventId: 'E1', farmId: 'FARM-A');
      expect(
        filed,
        isNotNull,
        reason: 'the walk must be declined before it is walked',
      );
      await store.close();
    });

    test('a claim still being filled in is not a filed claim', () async {
      final store = await freshStore();
      // Holding: the farmer is part-way through and should be sent back to
      // it, not told the field is already claimed.
      await store.putDraft({
        ...draftRow('d1'),
        'survey_event_id': 'E1',
        'farm_id': 'FARM-A',
        'status': 'holding',
      });

      expect(
        await store.filedClaimFor(eventId: 'E1', farmId: 'FARM-A'),
        isNull,
      );
      await store.close();
    });

    test('another calamity on the same field is a new claim', () async {
      final store = await freshStore();
      await store.putDraft({
        ...draftRow('d1'),
        'survey_event_id': 'E1',
        'farm_id': 'FARM-A',
        'status': 'pending',
      });

      // A hailstorm in August and a drought in September are two losses.
      expect(
        await store.filedClaimFor(eventId: 'E2', farmId: 'FARM-A'),
        isNull,
      );
      await store.close();
    });
  });

  group('a walked claim is not a sent claim', () {
    // The home screen marked any journey with complete evidence 'done', on
    // the reasoning that the phone had been stopped before it could say so.
    // Only the review screen releases a draft into the queue, so a claim the
    // farmer walked and did not send became invisible: out of the queue
    // because it was still holding, and out of the unfinished list because
    // 'done' hides it. Two on the first handset to try it were in that state,
    // four clips and four photographs each.

    test('a held claim stays in the list after it is marked done', () async {
      final store = await freshStore();
      await store.putDraft({...draftRow('d1'), 'status': 'holding'});
      await store.putJourney({
        'local_id': 'd1',
        'stations': null,
        'evidence_mode': 'stations',
      });
      await store.setJourneyStage('d1', 'done');

      final open = await store.openJourneys();
      expect(
        open.map((r) => r['local_id']),
        contains('d1'),
        reason: 'evidence walked and never sent must still be reachable',
      );
      await store.close();
    });

    test('a sent claim leaves the list', () async {
      final store = await freshStore();
      await store.putDraft({...draftRow('d1'), 'status': 'holding'});
      await store.putJourney({
        'local_id': 'd1',
        'stations': null,
        'evidence_mode': 'stations',
      });

      // What the review screen does: release, then mark done.
      await store.releaseDraft('d1');
      await store.setJourneyStage('d1', 'done');

      final open = await store.openJourneys();
      expect(open.map((r) => r['local_id']), isNot(contains('d1')));
      await store.close();
    });
  });

  group('a field with no recorded outline', () {
    // Filing no longer stops to ask for a boundary, so these journeys reach
    // the walk with no placed points. The first cut of that shipped three
    // ways of doing nothing -- the record button never enabled, the clip was
    // never written, and the walk ended after one point -- because every one
    // of them was guarded on a placed point that is null here.

    test('the walk is four clips, and knows where it got to', () async {
      final store = await freshStore();
      await store.putDraft(draftRow('d1'));
      await store.putJourney({
        'local_id': 'd1',
        'stations': null,
        'evidence_mode': 'stations',
      });

      // A field with no outline still walks: four points of the farmer's own,
      // numbered in the order they were taken. There is no photograph
      // sequence to drop to any more.
      final journey = Journey.fromRow((await store.journey('d1'))!);

      var progress = await evidenceProgress(store, journey);
      expect(progress.total, freeWalkPoints);
      expect(progress.done, 0);

      // The first point: a clip and a still, numbered by the order taken.
      await store.putCapture(
        capture('v1', 'd1', stage: 'station_1', station: 1, type: 'video/mp4'),
      );
      await store.putCapture(
        capture('s1', 'd1', stage: 'station_1', station: 1),
      );

      progress = await evidenceProgress(store, journey);
      expect(progress.done, 1);
      expect(
        firstFreePointToCapture(await store.stationsComplete('d1')),
        1,
        reason: 'the walk carries on at the second point',
      );

      await store.close();
    });

    test('a video is what the point is for', () async {
      final store = await freshStore();
      await store.putDraft(draftRow('d1'));
      // A still alone is not a point. The clip is the evidence the
      // ground-vision measurement reads; a photograph cannot stand in for it.
      await store.putCapture(
        capture('s1', 'd1', stage: 'station_1', station: 1),
      );
      expect(await store.stationsComplete('d1'), isEmpty);

      await store.putCapture(
        capture('v1', 'd1', stage: 'station_1', station: 1, type: 'video/mp4'),
      );
      expect(await store.stationsComplete('d1'), {1});
      await store.close();
    });
  });

  group('a point is its clip', () {
    test('the clip alone completes a point', () async {
      // It used to need a photograph as well: the walk recorded both at each
      // point, and a still that failed to save left the point unfinished.
      // There is no second file now -- the frame the report prints is taken
      // out of the clip by the office -- so the clip is the whole of what a
      // point is, and nothing else can fail to arrive.
      final store = await freshStore();
      await store.putDraft(draftRow('d1'));
      await store.putCapture(
        capture('v1', 'd1', stage: 'station_1', station: 1, type: 'video/mp4'),
      );

      expect(await store.stationsComplete('d1'), {1});
      await store.close();
    });

    test('a photograph on its own does not complete a point', () async {
      // Whatever else is attached to a claim, a point is not covered until it
      // has been filmed: the clip is what the ground-vision measurement reads
      // and what the report's frame comes out of.
      final store = await freshStore();
      await store.putDraft(draftRow('d1'));
      await store.putCapture(
        capture('s1', 'd1', stage: 'station_1', station: 1),
      );

      expect(await store.stationsComplete('d1'), isEmpty);
      await store.close();
    });

    test('the walk resumes at the first point not yet filmed', () async {
      final store = await freshStore();
      await store.putDraft(draftRow('d1'));
      // Points 1 and 2 filmed; 3 and 4 not.
      for (final n in [1, 2]) {
        await store.putCapture(
          capture(
            'v$n',
            'd1',
            stage: 'station_$n',
            station: n,
            type: 'video/mp4',
          ),
        );
      }

      final done = await store.stationsComplete('d1');
      // The index into the list, not the station number: it is what the walk
      // screen sets _index to.
      expect(
        firstStationToCapture(fourPoints(), done),
        2,
        reason:
            'the walk carries on at the third point, the first with no clip',
      );
      await store.close();
    });
  });
  group('a claim waits for its evidence', () {
    test(
      'a held claim is not queued, and its photographs are not either',
      () async {
        // The report used to enter the queue the moment the form was filed, so
        // an interrupted farmer had already filed a claim with nothing behind
        // it (docs/EVIDENCE_REVALIDATION.md, V4).
        final store = await freshStore();
        await store.putDraft({
          ...draftRow('held'),
          'status': Store.statusHolding,
        });
        await store.putCapture(
          capture('c1', 'held', stage: 'station_1', station: 1),
        );

        expect(await store.pendingDrafts(), isEmpty);
        final out = await store.outstanding();
        expect(out.drafts, 0, reason: 'a held claim is not waiting to be sent');
        expect(
          out.captures,
          0,
          reason: 'nor are the photographs of a claim still being gathered',
        );
        expect(await store.pendingCaptures(), isEmpty);
      },
    );

    test(
      'sending it puts the claim and its photographs in the queue',
      () async {
        final store = await freshStore();
        await store.putDraft({
          ...draftRow('held'),
          'status': Store.statusHolding,
        });
        await store.putCapture(
          capture('c1', 'held', stage: 'station_1', station: 1),
        );

        await store.releaseDraft('held');

        expect((await store.pendingDrafts()).single['local_id'], 'held');
        expect((await store.pendingCaptures()).single['local_id'], 'c1');
        final out = await store.outstanding();
        expect(out.drafts, 1);
        expect(out.captures, 1);
      },
    );

    test('releasing something that was never held changes nothing', () async {
      // The button can be pressed twice, and a claim already on its way must
      // not have its attempts reset under the sync engine.
      final store = await freshStore();
      await store.putDraft({
        ...draftRow('sending'),
        'status': 'pending',
        'attempts': 3,
      });

      await store.releaseDraft('sending');

      expect((await store.drafts()).single['attempts'], 3);
    });
  });
}
