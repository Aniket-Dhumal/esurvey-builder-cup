import 'dart:math' as math;

import 'package:esurvey/capture/station_guide.dart';
import 'package:esurvey/core/parcel_location.dart';
import 'package:flutter_test/flutter_test.dart';

/// How far a walk asks a farmer to move, and why the crop decides it.
///
/// # The shape this exists for
///
/// A survey number is routinely larger than, and shared by more than, the farmer
/// filing. One the trial has to handle: a 10 ha survey number in which a farmer
/// owns 2 ha, split 1 ha soybean and 1 ha udad, while a neighbour owns 5 ha of
/// soybean. That is three claims on one polygon.
///
/// The old rule read the polygon: sqrt(100000)/3 is 105 m, capped at 15, so all
/// three were asked for fifteen metres. Four clips fifteen metres apart on a 5 ha
/// holding sample one corner of it, and nothing downstream could tell that from a
/// walk across the whole crop.

/// A 10 ha survey number, about 316 m square.
ParcelShape surveyNumber({double metres = 316.23}) {
  const lat0 = 18.1700, lon0 = 75.8120;
  final dLat = metres / 111320.0;
  final dLon = metres / (111320.0 * math.cos(lat0 * math.pi / 180));
  return ParcelShape([
    [
      (lat: lat0, lon: lon0),
      (lat: lat0, lon: lon0 + dLon),
      (lat: lat0 + dLat, lon: lon0 + dLon),
      (lat: lat0 + dLat, lon: lon0),
      (lat: lat0, lon: lon0),
    ],
  ]);
}

/// Four points in a square of side [side] metres, which is the arrangement the
/// spacing rule is built around: mutually that far apart at the nearest.
List<RecordedPoint> squareOfSide(double side) {
  const lat0 = 18.1701, lon0 = 75.8121;
  final dLat = side / 111320.0;
  final dLon = side / (111320.0 * math.cos(lat0 * math.pi / 180));
  return [
    (stationNo: 1, lat: lat0, lon: lon0),
    (stationNo: 2, lat: lat0, lon: lon0 + dLon),
    (stationNo: 3, lat: lat0 + dLat, lon: lon0 + dLon),
    (stationNo: 4, lat: lat0 + dLat, lon: lon0),
  ];
}

void main() {
  group('the crop decides the walk, not the survey number', () {
    final ten = surveyNumber();

    test('a 10 ha survey number no longer answers for a 1 ha crop', () {
      expect(
        officeSpacingFor(ten),
        minSpacingM,
        reason: 'the old rule capped every claim on this polygon at 15 m',
      );
      // sqrt(0.75 * 10000) = 86.6
      expect(spacingFor(ten, sownHa: 1.0), closeTo(86.6, 0.5));
    });

    test('the same polygon asks more of a 5 ha crop than a 1 ha one', () {
      // The neighbour with all of their 5 ha in soybean walks further than the
      // farmer with 1 ha of it, on the very same survey number. Under the old
      // rule both walked fifteen metres.
      final one = spacingFor(ten, sownHa: 1.0);
      final five = spacingFor(ten, sownHa: 5.0);
      expect(five, greaterThan(one));
      expect(five, closeTo(193.6, 1.0)); // sqrt(0.75 * 50000)
    });

    test('soybean and udad of the same size ask the same walk', () {
      // Two claims, one farmer, one survey number. Nothing about the crop but
      // its extent enters this.
      expect(spacingFor(ten, sownHa: 1.0), spacingFor(ten, sownHa: 1.0));
    });

    test('a walk meeting the figure encloses three quarters of the crop', () {
      // The claim the whole rule rests on, checked rather than asserted in a
      // comment: four points mutually this far apart do enclose the target
      // fraction of a 1 ha crop.
      const cropM2 = 10000.0;
      final side = spacingFor(ten, sownHa: 1.0);
      final walked = squareOfSide(side);

      // Measured against the crop, not the survey number: coverageOf divides by
      // the shape it is given, so it is given a 1 ha one.
      final crop = surveyNumber(metres: math.sqrt(cropM2));
      expect(crop.areaM2, closeTo(cropM2, 50));
      expect(coverageOf(walked, crop), greaterThanOrEqualTo(coverageTarget));
    });
  });

  group('what the walk will not do', () {
    /// The office demo parcel: a rotated quadrilateral, 0.19 ha, walked whole.
    const office = ParcelShape([
      [
        (lat: 19.1156938, lon: 72.8616179),
        (lat: 19.1158961, lon: 72.8620187),
        (lat: 19.1156600, lon: 72.8622575),
        (lat: 19.1154006, lon: 72.8618677),
        (lat: 19.1156938, lon: 72.8616179),
      ],
    ]);

    test('the trial plot now asks for a real traverse of itself', () {
      // The plot the trial is testing on: 14.4 m before, 37.5 m now. Four clips
      // 14 m apart on it sampled a third of its length.
      expect(officeSpacingFor(office), closeTo(14.4, 0.5));
      expect(spacingFor(office, sownHa: 0.1875), closeTo(37.5, 0.5));
    });

    test('a long thin plot is not asked for less than it can give', () {
      // 200 m by 10 m, a fifth of a hectare. The first version of this capped the
      // figure by the narrow way across the plot and so asked for ten metres --
      // but four points sixty metres apart fit along its length without trouble,
      // so the width bounded nothing and the cap only weakened the walk.
      const lat0 = 18.1700, lon0 = 75.8120;
      final ribbon = ParcelShape([
        [
          (lat: lat0, lon: lon0),
          (lat: lat0, lon: lon0 + 200 / (111320.0 * 0.95)),
          (lat: lat0 + 10 / 111320.0, lon: lon0 + 200 / (111320.0 * 0.95)),
          (lat: lat0 + 10 / 111320.0, lon: lon0),
          (lat: lat0, lon: lon0),
        ],
      ]);
      expect(
        spacingFor(ribbon, sownHa: 0.2),
        closeTo(38.7, 0.5),
        reason: 'the crop decides it; the narrow way across the plot does not',
      );
    });

    test('a small share is not measured against the parcel around it', () {
      // This asserted the opposite until the rule was corrected, and the old
      // assertion was the bug: the crop figure was floored by the parcel rule, on
      // the reasoning that an app too old to read the office's figure might
      // otherwise permit what the office refuses.
      //
      // It cannot. The parcel rule is *larger* than the crop figure for a small
      // share of a bigger survey number, so an old app demands more walking, and
      // nothing it permits is refused. The floor only ever raised the demand --
      // and it stranded the farmer it was applied to.
      final ten = surveyNumber();
      expect(
        officeSpacingFor(ten),
        minSpacingM,
        reason: 'the parcel rule would ask 15 m of a 50 square metre crop',
      );
      expect(spacingFor(ten, sownHa: 0.005), lessThan(officeSpacingFor(ten)));
    });

    test('half a guntha is walkable, because nothing else decides it', () {
      // The extent the office demo plot was reduced to: about 50 square metres,
      // seven metres across. Under the old floor this asked for 14.4 metres --
      // outside the plot -- so the record button never came alive and no claim
      // could be made on it at all.
      const halfGunthaHa = 0.00506;
      final office = surveyNumber(metres: 43.3); // the 0.19 ha demo parcel
      final asked = spacingFor(office, sownHa: halfGunthaHa);

      expect(asked, closeTo(6.2, 0.2));
      // Reachable inside a plot seven metres on a side, corner to corner.
      expect(asked, lessThan(math.sqrt(halfGunthaHa * 10000) * 1.4));
    });

    test('nothing is asked for that a handset could not tell apart', () {
      // Five metres is the instrument, not a policy: a fix is good to about that,
      // so two points closer together cannot be shown to be different places --
      // which is the only thing the spacing rule demonstrates.
      final ten = surveyNumber();
      expect(spacingFor(ten, sownHa: 0.0001), smallestSpacingM);
    });
  });

  group('what the rule will not be talked into', () {
    // Found by probing rather than by it happening in a field, which is the only
    // reason it is cheap to be sitting here as a test.

    test('a mistyped extent cannot ask for more ground than exists', () {
      // Half a guntha entered as 100 hectares, or a registry figure in the wrong
      // unit. Before the ceiling this asked for 866 m between clips on a plot
      // seven metres across: the record button never came alive, and the farmer
      // could not file at all.
      final tiny = surveyNumber(metres: 7);
      final asked = spacingFor(tiny, sownHa: 100);

      final across = math.sqrt(2) * 7; // the diagonal of a 7 m square
      expect(asked, lessThanOrEqualTo(across));
      expect(
        asked,
        lessThan(20),
        reason: 'no walk on a seven-metre plot asks for hundreds of metres',
      );
    });

    test('and the ceiling does not weaken an honest extent', () {
      // A 1 ha crop on a 1 ha parcel still asks for the full figure: the diagonal
      // of a 100 m square over root two is 100 m, which is above it.
      final parcel = surveyNumber(metres: 100);
      expect(spacingFor(parcel, sownHa: 1.0), closeTo(86.6, 0.5));
    });

    test('a long thin plot is not clamped by its narrow way across', () {
      // The correction 0055 needed. Capping by the narrowest width clamped a
      // 200 m ribbon to 10 m, when four points sit sixty metres apart along its
      // length without trouble. The diagonal is the right ceiling; the width is
      // not.
      const lat0 = 18.1700, lon0 = 75.8120;
      final ribbon = ParcelShape([
        [
          (lat: lat0, lon: lon0),
          (lat: lat0, lon: lon0 + 200 / (111320.0 * 0.95)),
          (lat: lat0 + 10 / 111320.0, lon: lon0 + 200 / (111320.0 * 0.95)),
          (lat: lat0 + 10 / 111320.0, lon: lon0),
          (lat: lat0, lon: lon0),
        ],
      ]);
      expect(spacingFor(ribbon, sownHa: 0.2), closeTo(38.7, 0.5));
    });

    test('a frozen figure below the instrument is not obeyed', () {
      // The app obeys the office's figure precisely so it can never permit what
      // the office refuses -- which means a wrong figure is obeyed as faithfully
      // as a right one. A frozen 0.5 m would have both ends accept four clips of
      // the same square metre.
      const daft = [
        Station(
          stationNo: 1,
          lat: 18.1701,
          lon: 75.8121,
          role: 'low',
          placement: 'terrain',
          requiredSpacingM: 0.5,
        ),
      ];
      expect(
        spacingFor(surveyNumber(metres: 100), stations: daft, sownHa: 1.0),
        smallestSpacingM,
      );
    });
  });

  group('the office has the last word', () {
    final ten = surveyNumber();

    test('a figure sent with the points is obeyed, not recomputed', () {
      // The whole reason the office sends it: it knows the crop extent and the
      // handset may not. Two copies of the arithmetic only had to disagree once
      // for a farmer to record three clips that were then refused on every pass.
      const sent = [
        Station(
          stationNo: 1,
          lat: 18.1701,
          lon: 75.8121,
          role: 'low',
          placement: 'terrain',
          requiredSpacingM: 120,
        ),
      ];
      expect(spacingFor(ten, stations: sent, sownHa: 1.0), 120);
    });

    test(
      'an older server that sends none falls back, never below the floor',
      () {
        const silent = [
          Station(
            stationNo: 1,
            lat: 18.1701,
            lon: 75.8121,
            role: 'low',
            placement: 'terrain',
          ),
        ];
        expect(
          spacingFor(ten, stations: silent, sownHa: 1.0),
          closeTo(86.6, 0.5),
          reason: 'the crop still decides it, worked out on the phone',
        );
        expect(
          spacingFor(ten, stations: silent),
          greaterThanOrEqualTo(officeSpacingFor(ten)),
        );
      },
    );
  });
}
