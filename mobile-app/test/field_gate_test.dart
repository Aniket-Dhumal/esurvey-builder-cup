import 'dart:async';

import 'package:esurvey/capture/field_gate.dart';
import 'package:esurvey/capture/station_guide.dart';
import 'package:esurvey/core/parcel_location.dart';
import 'package:esurvey/core/sensors.dart';
import 'package:esurvey/core/voice.dart';
import 'package:esurvey/ui/theme.dart';
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';

/// The survey starts at the field.
///
/// A claim resumed from home, three hundred kilometres from the parcel, used
/// to open the camera and say "walk 329,730 metres south". Every clip recorded
/// from there was refused by the office. Nothing of the survey may open until
/// the farmer is at their farm.

Fix at(double lat, double lon) =>
    Fix(lat: lat, lon: lon, accuracyM: 8, at: DateTime.now());

/// A square field about 110 m on a side, near Solapur.
final field = ParcelShape.fromGeoJson({
  'type': 'Polygon',
  'coordinates': [
    [
      [75.8130, 18.1670],
      [75.8140, 18.1670],
      [75.8140, 18.1680],
      [75.8130, 18.1680],
      [75.8130, 18.1670],
    ],
  ],
});

const inField = (lat: 18.1675, lon: 75.8135);
const faraway = (lat: 19.0760, lon: 72.8777); // Mumbai, ~300 km off

class _FakePositioning implements Positioning {
  final _controller = StreamController<PositionState>.broadcast();
  Fix? _best;

  void move(Fix fix) {
    _best = fix;
    _controller.add(PositionState(FixStatus.ready, fix: fix));
  }

  @override
  Stream<PositionState> get stream => _controller.stream;

  @override
  Fix? get best => _best;

  @override
  Future<void> start() async {}

  @override
  dynamic noSuchMethod(Invocation invocation) => super.noSuchMethod(invocation);
}

Future<void> _open(
  WidgetTester tester,
  _FakePositioning positioning, {
  ParcelShape? shape,
}) async {
  await tester.pumpWidget(
    MaterialApp(
      theme: esurveyTheme(),
      home: FieldGate(
        positioning: positioning,
        voice: Voice(),
        shape: shape ?? field,
        surveyNumber: '453',
        builder: (_) => const Text('THE SURVEY'),
      ),
    ),
  );
}

void main() {
  group('whether the farmer is at the farm', () {
    test('inside the outline is at the farm', () {
      final r = atFarm(fix: at(inField.lat, inField.lon), shape: field);
      expect(r.near, isTrue);
    });

    test('three hundred kilometres away is not', () {
      final r = atFarm(fix: at(faraway.lat, faraway.lon), shape: field);
      expect(r.near, isFalse);
      expect(r.distance, greaterThan(100000));
    });

    test('with no outline, the assigned points decide', () {
      const point = Station(
        stationNo: 1,
        lat: 18.1675,
        lon: 75.8135,
        role: 'high',
        placement: 'terrain',
      );
      expect(
        atFarm(
          fix: at(18.1676, 75.8135),
          shape: const ParcelShape([]),
          stations: const [point],
        ).near,
        isTrue,
      );
      expect(
        atFarm(
          fix: at(faraway.lat, faraway.lon),
          shape: const ParcelShape([]),
          stations: const [point],
        ).near,
        isFalse,
      );
    });
  });

  group('the gate', () {
    testWidgets('far from the farm, the survey does not open', (tester) async {
      final positioning = _FakePositioning()
        ..move(at(faraway.lat, faraway.lon));
      await _open(tester, positioning);
      await tester.pump();

      expect(find.text('THE SURVEY'), findsNothing);
      expect(find.text('आधी तुमच्या शेतात जा'), findsOneWidget);
      // Kilometres, not a six-digit count of metres.
      expect(find.textContaining('किमी'), findsOneWidget);
      expect(find.textContaining('मीटर चालत'), findsNothing);
    });

    testWidgets('arriving at the farm opens it by itself', (tester) async {
      final positioning = _FakePositioning()
        ..move(at(faraway.lat, faraway.lon));
      await _open(tester, positioning);
      await tester.pump();
      expect(find.text('THE SURVEY'), findsNothing);

      positioning.move(at(inField.lat, inField.lon));
      await tester.pump();
      await tester.pump();

      expect(find.text('THE SURVEY'), findsOneWidget);
    });

    testWidgets('with no position yet, it waits rather than opening', (
      tester,
    ) async {
      await _open(tester, _FakePositioning());
      await tester.pump();

      expect(find.text('THE SURVEY'), findsNothing);
      expect(find.text('तुमचे ठिकाण शोधत आहे…'), findsOneWidget);
    });

    testWidgets('a parcel with nothing to fence against is not held back', (
      tester,
    ) async {
      await _open(tester, _FakePositioning(), shape: const ParcelShape([]));
      expect(find.text('THE SURVEY'), findsOneWidget);
    });
  });
}
