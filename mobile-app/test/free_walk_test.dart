import 'package:flutter_test/flutter_test.dart';

import 'package:esurvey/capture/station_guide.dart';
import 'package:esurvey/core/parcel_location.dart';
import 'package:esurvey/core/sensors.dart';
import 'package:esurvey/report/journey.dart';

/// A field with no recorded outline still walks.
///
/// Filing used to stop at a parcel the registry has no boundary for and ask
/// the farmer to walk its perimeter, because the office places the four
/// points from that outline. Half a village's parcels have no outline, and a
/// farmer standing in a hailed field was being asked for a survey before they
/// could report the damage. Now they record four points of their own, held
/// apart by the same spacing rule, and the boundary is offered afterwards.

void main() {
  // A field the registry has no outline for.
  const empty = ParcelShape([]);
  Fix at(double lat, double lon) =>
      Fix(lat: lat, lon: lon, accuracyM: 8, at: DateTime.now());

  test('a field with no outline still asks for four clips', () {
    expect(freeWalkPoints, 4);
  });

  test('a free walk resumes where it was interrupted', () {
    expect(firstFreePointToCapture(<int>{}), 0, reason: 'nothing recorded');
    expect(firstFreePointToCapture({1}), 1);
    expect(firstFreePointToCapture({1, 2, 3}), 3);
    expect(
      firstFreePointToCapture({1, 2, 3, 4}),
      freeWalkPoints,
      reason: 'the walk is done',
    );
  });

  test('a clip lost and refilmed does not push the walk past its end', () {
    // Point two was refilmed, so three and four are still owed.
    expect(firstFreePointToCapture({1, 2}), 2);
  });

  test('with no outline the walk holds points fifteen metres apart', () {
    // No area to scale by, so the widest spacing applies -- the same figure
    // the server uses, because a farmer must never record something the
    // office will refuse.
    expect(spacingFor(empty), minSpacingM);
  });

  test(
    'a farmer may record anywhere when there is no boundary to be outside of',
    () {
      final status = walkStatus(
        fix: at(18.16, 75.81),
        shape: empty,
        station: 1,
        recorded: const [],
      );
      expect(status.canRecord, isTrue);
    },
  );

  test('but not twice in the same spot', () {
    final status = walkStatus(
      fix: at(18.16, 75.81),
      shape: empty,
      station: 2,
      // A clip already taken a few metres away: closer than the spacing.
      recorded: const [(stationNo: 1, lat: 18.16, lon: 75.81)],
    );
    expect(status.canRecord, isFalse);
    expect(status.en, contains('metres'));
  });

  test('refilming the same point is not the walk standing still', () {
    final status = walkStatus(
      fix: at(18.16, 75.81),
      shape: empty,
      station: 1,
      // The clip the phone lost, at the place it was first taken.
      recorded: const [(stationNo: 1, lat: 18.16, lon: 75.81)],
    );
    expect(status.canRecord, isTrue);
  });
}
