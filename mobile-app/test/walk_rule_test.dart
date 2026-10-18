import 'package:esurvey/capture/station_guide.dart';
import 'package:esurvey/core/parcel_location.dart';
import 'package:esurvey/core/sensors.dart';
import 'package:flutter_test/flutter_test.dart';

/// What the walk asks of a farmer standing in their field.
///
/// Two things: be inside the field, and be somewhere the walk has not already
/// been. Not a third: standing on an assigned coordinate. The points say where
/// to go; they are not a fence, and holding the record button until the
/// handset agrees with one is how a farmer ends up in the mud watching a
/// disabled button while the evidence that matters -- several clips from
/// across the same field -- was obtainable the whole time.

Fix at(double lat, double lon) =>
    Fix(lat: lat, lon: lon, accuracyM: 8, at: DateTime.now());

/// A parcel about 120 m across, with its corner near the reference plot.
const field = ParcelShape([
  [
    (lat: 18.1690, lon: 75.8120),
    (lat: 18.1690, lon: 75.8140),
    (lat: 18.1710, lon: 75.8140),
    (lat: 18.1710, lon: 75.8120),
    (lat: 18.1690, lon: 75.8120),
  ],
]);

void main() {
  group('the fence', () {
    test('outside the field, nothing records', () {
      final s = walkStatus(
        fix: at(18.1600, 75.8131), // about a kilometre south of the parcel
        shape: field,
        station: 1,
        recorded: const [],
      );
      expect(s.canRecord, isFalse);
      expect(s.outsideByM, greaterThan(100));
      // And it says which way out of the problem.
      expect(s.en, contains('outside your field'));
      expect(s.mr, contains('शेताच्या बाहेर'));
    });

    test('inside the field, the first point records anywhere', () {
      // Nowhere near any assigned point, which is the whole change.
      final s = walkStatus(
        fix: at(18.1695, 75.8124),
        shape: field,
        station: 1,
        recorded: const [],
      );
      expect(s.canRecord, isTrue);
    });
  });

  group('the walk has to move', () {
    test('standing where the last point was recorded does not record', () {
      final s = walkStatus(
        fix: at(18.16951, 75.81241),
        shape: field,
        station: 2,
        recorded: const [(stationNo: 1, lat: 18.1695, lon: 75.8124)],
      );
      expect(s.canRecord, isFalse);
      expect(s.nearestOtherM, lessThan(minSpacingM));
      expect(s.en, contains('another part of your field'));
    });

    test('a short walk away records', () {
      // About 30 m north of the last point, still well inside the parcel.
      final s = walkStatus(
        fix: at(18.16977, 75.8124),
        shape: field,
        station: 2,
        recorded: const [(stationNo: 1, lat: 18.1695, lon: 75.8124)],
      );
      expect(s.canRecord, isTrue);
    });

    test('re-recording the same point where it was first tried is allowed', () {
      // The phone lost the clip. Refusing here would strand the farmer at a
      // point they have already walked to, for having walked to it twice.
      final s = walkStatus(
        fix: at(18.1695, 75.8124),
        shape: field,
        station: 1,
        recorded: const [(stationNo: 1, lat: 18.1695, lon: 75.8124)],
      );
      expect(s.canRecord, isTrue);
    });

    test('the fence outranks the spread', () {
      // Far enough from the last point, and outside the parcel: the office
      // would refuse this clip, so the app must not let it be recorded.
      final s = walkStatus(
        fix: at(18.1600, 75.8131),
        shape: field,
        station: 2,
        recorded: const [(stationNo: 1, lat: 18.1695, lon: 75.8124)],
      );
      expect(s.canRecord, isFalse);
      expect(s.en, contains('outside your field'));
    });
  });

  group('the placed point is never named aloud', () {
    // It used to be, alongside permission to record: "you can record here. For
    // the high part of the field, walk 45 metres north-west." Two instructions
    // that argue with each other, and a farmer follows the one that sounds like
    // work -- so the app sent people across their own field having just told them
    // they need not go.
    //
    // A farmer reported exactly that, and the compass with it: nobody standing in
    // a field is told to go north-west.

    test('a farmer far from the placed point is simply told to record', () {
      final s = walkStatus(
        fix: at(18.1692, 75.8122),
        shape: field,
        station: 2,
        recorded: const [],
      );
      expect(s.canRecord, isTrue, reason: 'the point is not a condition');
      expect(s.en, 'Stop here and record the video.');
      expect(
        s.en,
        isNot(contains('walk')),
        reason: 'permission to record and an instruction to walk cannot both '
            'be true',
      );
    });

    test('and so is one standing on it', () {
      // The same sentence either way. Where the farmer happens to be relative to
      // a point they are not held to changes nothing about what they should do.
      final s = walkStatus(
        fix: at(18.17085, 75.81385),
        shape: field,
        station: 2,
        recorded: const [],
      );
      expect(s.canRecord, isTrue);
      expect(s.en, 'Stop here and record the video.');
    });
  });

  group('the spread fits the field', () {
    test('a small plot is not asked for a walk it cannot hold', () {
      // Nine metres across, which is the smallest parcel in the first real
      // village loaded. Fifteen metres there is an instruction to stand
      // outside the field, which the office then refuses.
      const tiny = ParcelShape([
        [
          (lat: 18.1700, lon: 75.8131),
          (lat: 18.1700, lon: 75.81318),
          (lat: 18.17008, lon: 75.81318),
          (lat: 18.17008, lon: 75.8131),
          (lat: 18.1700, lon: 75.8131),
        ],
      ]);
      expect(tiny.areaM2, lessThan(120));
      expect(spacingFor(tiny), smallestSpacingM);

      // Four metres apart on a nine-metre plot is a different corner of it.
      final s = walkStatus(
        fix: at(18.17004, 75.81314),
        shape: tiny,
        station: 2,
        recorded: const [(stationNo: 1, lat: 18.1700, lon: 75.8131)],
      );
      expect(s.canRecord, isTrue);
    });

    test('a full field is asked for the full spread', () {
      expect(spacingFor(field), minSpacingM);
    });

    test('an unmapped parcel keeps the ordinary figure', () {
      expect(spacingFor(const ParcelShape([])), minSpacingM);
    });
  });

  group('how much further to walk', () {
    // The walk tells a farmer the remaining distance on a timer rather than on
    // position events: walking produces about one event a metre, and Voice.say
    // stops whatever is speaking before it starts, so announcing from there gives
    // a farmer a stream of half-sentences. That needs the distance as a number,
    // not only inside the sentence.

    test('a blocked point says how much further, as a number', () {
      final s = walkStatus(
        fix: at(18.16951, 75.81241),
        shape: const ParcelShape([]),
        station: 2,
        recorded: const [(stationNo: 1, lat: 18.1695, lon: 75.8124)],
      );
      expect(s.canRecord, isFalse);
      expect(s.shortfallM, isNotNull);
      expect(s.shortfallM, greaterThan(0));
      // The shortfall is what is left of the requirement, not the whole of it.
      expect(s.shortfallM, lessThan(minSpacingM));
      expect(s.shortfallM! + s.nearestOtherM!, closeTo(minSpacingM, 0.5));
    });

    test('shrinks as the farmer walks', () {
      // The number the timer speaks has to fall, or a farmer following it walks
      // for ever. Two fixes, the second further from the recorded point.
      const recorded = [(stationNo: 1, lat: 18.1695, lon: 75.8124)];
      final near = walkStatus(
        fix: at(18.16951, 75.81241),
        shape: const ParcelShape([]),
        station: 2,
        recorded: recorded,
      );
      final further = walkStatus(
        fix: at(18.16959, 75.81249),
        shape: const ParcelShape([]),
        station: 2,
        recorded: recorded,
      );
      expect(further.shortfallM, lessThan(near.shortfallM!));
    });

    test('is absent once the farmer may record', () {
      // Nothing to announce. The timer reads this to decide whether to speak at
      // all, so a stale number here would have it talking after the farmer had
      // already arrived.
      final s = walkStatus(
        fix: at(18.1700, 75.8131),
        shape: field,
        station: 1,
        recorded: const [],
      );
      expect(s.canRecord, isTrue);
      expect(s.shortfallM, isNull);
    });

    test('is absent when the field, not the distance, is the problem', () {
      // Outside the boundary. Telling somebody to walk another twelve metres when
      // they are standing in the wrong field is wrong advice, and the timer must
      // not offer it.
      final s = walkStatus(
        fix: at(18.1600, 75.8131),
        shape: field,
        station: 1,
        recorded: const [],
      );
      expect(s.canRecord, isFalse);
      expect(s.outsideByM, isNotNull);
      expect(s.shortfallM, isNull);
    });

    test('is absent when the ground belongs to another claim', () {
      // Another claim on the survey number filmed here. "Walk 8 metres more" is
      // wrong: the ground is somebody else's evidence however far along it they
      // go, and the remedy is their own share of the field.
      final s = walkStatus(
        fix: at(18.17001, 75.81311),
        shape: field,
        station: 1,
        recorded: const [],
        takenPoints: const [(lat: 18.1700, lon: 75.8131, sameFarmer: false)],
      );
      expect(s.canRecord, isFalse);
      expect(s.taken, isNotNull);
      expect(s.shortfallM, isNull);
    });
  });

  test('with no outline the spread is still enforced', () {
    // A parcel the registry never mapped: there is no fence to apply, and the
    // walk still has to move.
    final s = walkStatus(
      fix: at(18.16951, 75.81241),
      shape: const ParcelShape([]),
      station: 2,
      recorded: const [(stationNo: 1, lat: 18.1695, lon: 75.8124)],
    );
    expect(s.canRecord, isFalse);
    expect(s.nearestOtherM, lessThan(minSpacingM));
  });
}
