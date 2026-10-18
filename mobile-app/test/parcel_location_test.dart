import 'package:esurvey/core/parcel_location.dart';
import 'package:esurvey/core/sensors.dart';
import 'package:flutter_test/flutter_test.dart';

/// How far the farmer is from the field they are describing.
///
/// The app used to answer a different question — how precise its own fix was,
/// "±9 m" — and never this one. Somebody six hundred kilometres from Malegaon
/// could fill in a whole report and take every photograph before anything
/// objected, because nothing on the handset compared where they stood to where
/// the land is.

/// The Malegaon survey number 453 boundary, exactly as `plot_geometry` arrives
/// in the registry record: a MultiPolygon, [lon, lat], a strip roughly 100 m
/// across and 630 m long.
final reference = {
  'type': 'MultiPolygon',
  'coordinates': [
    [
      [
        [75.813582, 18.1727367],
        [75.8128843, 18.1727976],
        [75.8128041, 18.1717538],
        [75.8128121, 18.1706871],
        [75.8126999, 18.1695823],
        [75.812645, 18.1685947],
        [75.8127158, 18.1680983],
        [75.8128221, 18.1670804],
        [75.8135658, 18.1672486],
        [75.813371, 18.1683002],
        [75.8133622, 18.1697051],
        [75.8136012, 18.1706221],
        [75.8136012, 18.1727167],
        [75.813582, 18.1727367],
      ],
    ],
  ],
};

Fix at(double lat, double lon) =>
    Fix(lat: lat, lon: lon, accuracyM: 9, at: DateTime.now());

void main() {
  group('an unknown distance', _unknownDistanceGroup);

  final shape = ParcelShape.fromGeoJson(reference);

  group('reading the registry geometry', () {
    test('the reference parcel parses', () {
      expect(shape.isEmpty, isFalse);
      expect(shape.rings, hasLength(1));
      expect(shape.rings.first.length, greaterThan(10));
    });

    test('lon and lat are not swapped', () {
      // GeoJSON is [lon, lat]. Reading it the other way puts Maharashtra in
      // the Indian Ocean — an error that looks like a working app until
      // somebody checks a distance.
      final p = shape.rings.first.first;
      expect(p.lat, closeTo(18.17, 0.1), reason: 'latitude should be ~18°N');
      expect(p.lon, closeTo(75.81, 0.1), reason: 'longitude should be ~75°E');
    });

    test('a null geometry is empty, not an error', () {
      // Live AgriStack sends plot_geometry: null for every farmer we can
      // reach, so this is the ordinary case.
      expect(ParcelShape.fromGeoJson(null).isEmpty, isTrue);
      expect(ParcelShape.fromGeoJson('nonsense').isEmpty, isTrue);
      expect(ParcelShape.fromGeoJson({'type': 'Point'}).isEmpty, isTrue);
    });

    test('a plain Polygon parses too', () {
      final poly = ParcelShape.fromGeoJson({
        'type': 'Polygon',
        'coordinates': [
          [
            [75.8128, 18.1700],
            [75.8136, 18.1700],
            [75.8136, 18.1710],
            [75.8128, 18.1710],
            [75.8128, 18.1700],
          ],
        ],
      });
      expect(poly.isEmpty, isFalse);
    });
  });

  group('standing in the field', () {
    test('a point inside is zero away', () {
      final f = locate(fix: at(18.1700, 75.8131), shape: shape);
      expect(f.inside, isTrue);
      expect(f.distanceM, 0);
      expect(f.atParcel, isTrue);
    });

    test('on the bund, a few metres out, still counts', () {
      // A farmer photographing from the edge of their own plot is outside the
      // polygon and is not lying about anything.
      final f = locate(fix: at(18.17000, 75.81270), shape: shape);
      expect(f.inside, isFalse);
      expect(f.distanceM, lessThan(atParcelWithinM));
      expect(f.atParcel, isTrue);
    });

    test('the neighbouring field does not count', () {
      // ~200 m west: comfortably past the tolerance.
      final f = locate(fix: at(18.1700, 75.8107), shape: shape);
      expect(f.atParcel, isFalse);
      expect(f.distanceM, greaterThan(100));
    });
  });

  group('six hundred kilometres away', () {
    test('Pune is refused, and the distance is honest', () {
      // The case that started this: a desk in Pune, a field in Malegaon, and
      // an app that happily took five photographs.
      final f = locate(fix: at(18.5204, 73.8567), shape: shape);

      expect(f.atParcel, isFalse);
      expect(f.inside, isFalse);
      expect(
        f.distanceM,
        greaterThan(100_000),
        reason: 'Pune to Malegaon is a couple of hundred kilometres',
      );
    });

    test('a long way away reads in kilometres, not metres', () {
      // "598000 मी" tells a farmer nothing.
      final f = locate(fix: at(18.5204, 73.8567), shape: shape);
      expect(f.pretty, contains('किमी'));
      expect(f.pretty, isNot(contains('मी.')));
    });

    test('near the field reads in metres', () {
      final f = locate(fix: at(18.1690, 75.8131), shape: shape);
      expect(f.pretty, contains('मी'));
    });
  });

  group('when there is nothing to check against', () {
    test('no boundary means not at the parcel', () {
      // A parcel nobody has walked yet. The caller sends the farmer to trace
      // it rather than pretending they are standing on it.
      final f = locate(fix: at(18.1700, 75.8131), shape: const ParcelShape([]));
      expect(f.atParcel, isFalse);
      expect(f.distanceM, double.infinity);
    });

    test('no position means not at the parcel', () {
      // Indoors, or before the first fix. Refusing is right: the alternative
      // is letting somebody survey a field the app cannot place them in.
      final f = locate(fix: null, shape: shape);
      expect(f.atParcel, isFalse);
    });
  });

  group('the debug override', () {
    test('pretending puts you in the field', () {
      // Tests run in debug mode, so the override is available here — which is
      // the point: the whole walk is exercisable from a desk.
      final f = locate(fix: at(18.5204, 73.8567), shape: shape, simulate: true);
      expect(f.atParcel, isTrue);
      expect(f.simulated, isTrue);
    });

    test('a simulated fix says it is simulated', () {
      // A pretend position that looks real is how a demo turns into a false
      // claim. The screen shows a banner because of this flag.
      final f = locate(fix: at(18.5204, 73.8567), shape: shape, simulate: true);
      expect(f.simulated, isTrue);

      final real = locate(fix: at(18.1700, 75.8131), shape: shape);
      expect(real.simulated, isFalse);
    });

    test('it cannot rescue a parcel with no boundary', () {
      // Nothing to pretend to be standing in.
      final f = locate(
        fix: at(18.5204, 73.8567),
        shape: const ParcelShape([]),
        simulate: true,
      );
      expect(f.atParcel, isFalse);
    });

    test('not asked for, not applied', () {
      final f = locate(fix: at(18.5204, 73.8567), shape: shape);
      expect(f.simulated, isFalse);
      expect(f.atParcel, isFalse);
    });
  });

  group('the tolerance matches the server', () {
    test('25 metres, the same number the geofence uses', () {
      // Two different numbers on the two sides is how a farmer gets told they
      // have arrived and then has the capture refused.
      expect(atParcelWithinM, 25);
    });
  });
}

// --- the infinity this class produces itself ---------------------------------

void _unknownDistanceGroup() {
  test('a fix that has not arrived is not a distance', () {
    // locate() answers with an infinite distance for two states that are not
    // "far away": no position yet, and no outline to measure against.
    final f = locate(fix: null, shape: ParcelShape(const []));

    expect(f.known, isFalse);
    expect(f.atParcel, isFalse, reason: 'unknown is not "close enough"');
  });

  test('formatting an unknown distance says so instead of throwing', () {
    // The crash found on a handset by the fault log:
    //
    //   Unsupported operation: Infinity or NaN toInt
    //   #0 ParcelFix.pretty
    //   #1 _FileReportScreenState.build
    //
    // Thrown from a build method, so the report screen failed to render at all
    // for any farmer whose location was denied or whose first fix had not yet
    // arrived -- which is every farmer, for the first moment.
    final f = locate(fix: null, shape: ParcelShape(const []));

    expect(() => f.pretty, returnsNormally);
    expect(f.pretty, 'अंतर माहीत नाही');
  });

  test('a real distance still reads as before', () {
    expect(
      const ParcelFix(distanceM: 240, inside: false, simulated: false).pretty,
      '240 मी',
    );
    expect(
      const ParcelFix(distanceM: 2400, inside: false, simulated: false).pretty,
      '2 किमी',
    );
    expect(
      const ParcelFix(distanceM: 0, inside: true, simulated: false).known,
      isTrue,
    );
  });

  test('a NaN distance is handled like an unknown one', () {
    // Not produced today, but the arithmetic upstream is trigonometric and a
    // NaN would reach the same .round() and throw the same way.
    const f = ParcelFix(distanceM: double.nan, inside: false, simulated: false);
    expect(f.known, isFalse);
    expect(() => f.pretty, returnsNormally);
  });
}
