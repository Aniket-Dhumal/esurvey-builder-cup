import 'package:esurvey/capture/station_guide.dart';
import 'package:esurvey/core/parcel_location.dart';
import 'package:esurvey/core/sensors.dart';
import 'package:flutter_test/flutter_test.dart';

/// Walking a farmer across their own field.
///
/// The instruction is spoken to somebody who is not looking at the screen, in
/// a field, possibly one-handed. It has to be right, and it has to stop
/// changing every second or they will stop listening to it.

Fix at(double lat, double lon) =>
    Fix(lat: lat, lon: lon, accuracyM: 8, at: DateTime.now());

const station = Station(
  stationNo: 4,
  lat: 18.1678,
  lon: 75.8131,
  role: 'high',
  placement: 'terrain',
);

void main() {
  group('recording in the same spot', _sameSpotGroup);

  // The compass tests are gone with the compass.
  //
  // They tested the arithmetic faithfully -- bearings, wrap-around at 350 degrees,
  // a Marathi name at every point -- and all of it was correct. It was the
  // instruction built on top that was wrong: a farmer was told "you can record
  // here" and then sent 45 metres north-west, and they followed the walk because
  // it sounded like the instruction. Nobody standing in a field goes north-west.
  //
  // Nothing replaced it with left and right, which needs to know which way
  // somebody faces. Course-over-ground says nothing until they are already
  // walking, and there is no magnetometer reading here.

  group('how far', () {
    test('a distance is rounded to something worth saying', () {
      // Nobody walks 37.4 metres, and a countdown that recites a new number
      // every second is noise a farmer stops listening to.
      expect(spokenDistance(37.4), 35);
      expect(spokenDistance(38.0), 40);
      expect(spokenDistance(4.2), 4);
    });

    test('small twitches do not change what is said', () {
      // Two readings a metre apart, which is ordinary GNSS scatter standing
      // still, must produce the same spoken number.
      expect(spokenDistance(41.0), spokenDistance(42.0));
    });
  });

  group('naming the station out loud', () {
    test('a flat field has ends, not a high part', () {
      // Under geometric placement neither end is higher in any way a farmer
      // would recognise. Calling one "the high part" would be a small lie,
      // told four times a report, in the farmer's own field where they can
      // see it is not true.
      const flat = Station(
        stationNo: 4,
        lat: 18.1678,
        lon: 75.8131,
        role: 'high',
        placement: 'geometric',
      );
      expect(flat.label.mr, isNot(contains('उंच')));
      expect(flat.label.en, contains('end'));
    });

    test('a sloping field says high and low', () {
      expect(station.label.mr, contains('उंच'));

      const low = Station(
        stationNo: 1,
        lat: 18.1721,
        lon: 75.8131,
        role: 'low',
        placement: 'terrain',
      );
      expect(low.label.mr, contains('खालचा'));
    });

    test('the flanks are named as sides', () {
      for (final role in ['mid_left', 'mid_right']) {
        final s = Station(
          stationNo: 2,
          lat: 18.17,
          lon: 75.813,
          role: role,
          placement: 'terrain',
        );
        expect(s.label.mr, contains('बाजू'));
      }
    });

    test('an unknown role still says something', () {
      // The server could add a role this build has not heard of. Falling
      // through to an empty instruction would leave a farmer standing in a
      // field being told nothing.
      const s = Station(
        stationNo: 9,
        lat: 18.17,
        lon: 75.813,
        role: 'something_new',
        placement: 'terrain',
      );
      expect(s.label.mr, isNotEmpty);
      expect(s.label.en, isNotEmpty);
    });
  });

  group('reading what the server sent', () {
    test('a station decodes from the API shape', () {
      final s = Station.fromJson({
        'station_no': 2,
        'lat': 18.170649,
        'lon': 75.813368,
        'role': 'mid_left',
        'placement': 'geometric',
      });
      expect(s.stationNo, 2);
      expect(s.lat, closeTo(18.170649, 1e-9));
      expect(s.role, 'mid_left');
    });

    test('a malformed station does not crash the walk', () {
      // Better a station at 0,0 that the geofence refuses than an exception
      // that ends the report while somebody is standing in a field.
      final s = Station.fromJson({});
      expect(s.stationNo, 0);
      expect(s.lat, 0);
    });
  });
}

// --- the guard has to be re-run, not only re-received ------------------------

void _sameSpotGroup() {
  const shape = ParcelShape([
    [
      (lat: 19.1156938, lon: 72.8616179),
      (lat: 19.1158961, lon: 72.8620187),
      (lat: 19.1156600, lon: 72.8622575),
      (lat: 19.1154006, lon: 72.8618677),
      (lat: 19.1156938, lon: 72.8616179),
    ],
  ]);

  final here = at(19.115658, 72.861937);

  test('recording twice in one spot is refused', () {
    // The office refuses it too, with 422 SAME_POSITION, and on an office test
    // three of four clips were lost that way. The client has always known how to
    // stop it -- what it did not do was ask again after a clip was recorded, and
    // the position stream is created with distanceFilter: 1, so a farmer standing
    // still generates no event to ask on.
    final after = walkStatus(
      fix: here,
      shape: shape,
      station: 2,
      recorded: const [(stationNo: 1, lat: 19.115658, lon: 72.861937)],
    );

    expect(
      after.canRecord,
      isFalse,
      reason: 'the second clip is being taken where the first was',
    );
    expect(after.en, contains('already recorded this spot'));
    expect(after.en, contains('Walk at least'));
  });

  test('a metre of drift is still the same spot', () {
    // The office measured 0 m and 1 m between the four clips of that walk. GPS
    // noise must not read as having moved.
    final s = walkStatus(
      fix: here,
      shape: shape,
      station: 2,
      recorded: const [(stationNo: 1, lat: 19.115667, lon: 72.861937)],
    );
    expect(s.canRecord, isFalse);
  });

  test('the far corner of the same small plot is far enough', () {
    // 0.19 ha, so the spacing asked for is about 14 m. The plot's diagonal is
    // 71 m, so four points are placeable -- the rule is satisfiable, which is
    // what makes refusing the same spot fair.
    final s = walkStatus(
      fix: at(19.1154006, 72.8618677),
      shape: shape,
      station: 2,
      recorded: const [(stationNo: 1, lat: 19.1158961, lon: 72.8620187)],
    );
    expect(s.canRecord, isTrue);
  });

  test('the point being re-recorded does not block itself', () {
    // Returning to station 1 to redo it must not be refused on the grounds that
    // station 1 is already there.
    final s = walkStatus(
      fix: here,
      shape: shape,
      station: 1,
      recorded: const [(stationNo: 1, lat: 19.115658, lon: 72.861937)],
    );
    expect(s.canRecord, isTrue);
  });
}
