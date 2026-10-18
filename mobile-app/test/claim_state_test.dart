import 'package:esurvey/core/db.dart';
import 'package:esurvey/report/claim_state.dart';
import 'package:esurvey/report/journey.dart';
import 'package:flutter_test/flutter_test.dart';

/// What a claim is, worked out from the rows rather than from its label.
///
/// Every case here is one where the stored `stage` and the farmer's actual work
/// disagreed, because that is the only interesting question: the label is written
/// after the fact by an app that Android kills without warning, and each time the
/// two parted company a farmer lost a walk.

Journey journeyAt(String stage, {int clips = 0}) => Journey.fromRow({
  'local_id': 'j1',
  'stage': stage,
  'farm_id': 'F1',
  'event_id': 'E1',
  'survey_number': '453',
  'form': '{"sown":"1.6"}',
});

Map<String, Object?> draft({
  String status = Store.statusHolding,
  String? serverId,
  String? reference,
}) => {
  'local_id': 'j1',
  'status': status,
  'server_id': serverId,
  'reference': reference,
};

EvidenceProgress progress(int done, int total) =>
    EvidenceProgress(done: done, total: total);

void main() {
  group('a claim being typed', () {
    test('is at the form while nothing has been recorded', () {
      final s = ClaimState.derive(
        journey: journeyAt('form'),
        draft: draft(),
        progress: progress(0, 4),
      );
      expect(s.stage, ClaimStage.form);
      expect(s.owesFieldWork, isTrue);
    });

    test('is not sent back to the form once clips exist', () {
      // The label still says `form` because the app was killed after the walk
      // began. Believing it would drop the farmer back into the typing and lose
      // the clips already on the phone.
      final s = ClaimState.derive(
        journey: journeyAt('form'),
        draft: draft(),
        progress: progress(2, 4),
      );
      expect(s.stage, ClaimStage.evidence);
      expect(s.evidenceIncomplete, isTrue);
    });
  });

  group('a claim interrupted mid-walk', () {
    test('owes the rest of its evidence', () {
      final s = ClaimState.derive(
        journey: journeyAt('evidence'),
        draft: draft(),
        progress: progress(1, 4),
      );
      expect(s.stage, ClaimStage.evidence);
      expect(s.evidenceIncomplete, isTrue);
      expect(s.shouldHaveBeenFiled, isFalse);
    });

    test('with every clip taken and never released, should have been filed', () {
      // The fault this flag exists for. Two claims on the first handset to try
      // the walk were in exactly this state: four clips and four photographs
      // each, walked in the sun, with the draft still holding -- so the queue did
      // not have them and the unfinished list hid them behind their own label.
      final s = ClaimState.derive(
        journey: journeyAt('done'),
        draft: draft(),
        progress: progress(4, 4),
      );
      expect(s.stage, ClaimStage.evidence);
      expect(s.shouldHaveBeenFiled, isTrue);
      expect(
        s.evidenceIncomplete,
        isFalse,
        reason: 'nothing is missing; the release is what did not happen',
      );
    });
  });

  group('a claim that has gone', () {
    test('released into the queue owes the farmer nothing', () {
      final s = ClaimState.derive(
        journey: journeyAt('done'),
        draft: draft(status: 'pending'),
        progress: progress(4, 4),
      );
      expect(s.stage, ClaimStage.filed);
      expect(
        s.owesFieldWork,
        isFalse,
        reason: 'waiting on a network is not work a farmer can do',
      );
    });

    test('answered by the office is settled, whatever the label says', () {
      // A reference is the strongest fact on the phone. A journey still labelled
      // `evidence` -- killed before the label caught up -- must not be offered
      // for review, which would file the claim a second time.
      final s = ClaimState.derive(
        journey: journeyAt('evidence'),
        draft: draft(
          status: 'sent',
          serverId: 'R1',
          reference: 'CLR-2026-496-00002',
        ),
        progress: progress(4, 4),
      );
      expect(s.stage, ClaimStage.settled);
      expect(s.filedReference, 'CLR-2026-496-00002');
      expect(s.owesFieldWork, isFalse);
    });
  });

  group('a claim the office would not take all of', () {
    test('owes a walk even though it has been accepted', () {
      // The shape of the trial claim that started this: a reference, one clip
      // accepted, three refused for being recorded in the same spot. Accepted and
      // short of its evidence at the same time, and nothing on the phone said so.
      final s = ClaimState.derive(
        journey: journeyAt('done'),
        draft: draft(
          status: 'sent',
          serverId: 'R1',
          reference: 'CLR-2026-496-00002',
        ),
        progress: progress(1, 4),
        refusedClips: 3,
      );
      expect(s.stage, ClaimStage.refused);
      expect(s.owesFieldWork, isTrue);
      expect(s.refusedClips, 3);
      expect(s.filedReference, isNotEmpty);
    });

    test('a refusal does not drag a half-typed form into the field', () {
      // Nothing was claimed, so there is nothing to record again. A form with a
      // stray refused row against it is a form.
      final s = ClaimState.derive(
        journey: journeyAt('form'),
        draft: draft(),
        progress: progress(0, 4),
        refusedClips: 2,
      );
      expect(s.stage, ClaimStage.form);
    });
  });

  test('a claim whose draft has gone is not owed to anybody', () {
    // A sign-out caught mid-write, or a delete that half-happened. There is
    // nothing to send and nothing to claim; the journey is all that is left to
    // tidy away, and it must not be presented as work.
    final s = ClaimState.derive(
      journey: journeyAt('evidence'),
      draft: null,
      progress: progress(2, 4),
    );
    expect(s.stage, ClaimStage.form);
    expect(s.shouldHaveBeenFiled, isFalse);
  });
}
