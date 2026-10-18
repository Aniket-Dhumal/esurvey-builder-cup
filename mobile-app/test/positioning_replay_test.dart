import 'dart:async';

import 'package:esurvey/core/sensors.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:geolocator/geolocator.dart';

/// Why the app looked slow at finding a position the phone had already found.
///
/// The position stream is a broadcast stream, so a listener that arrived late
/// heard nothing until the next update -- and updates only came when the handset
/// moved a metre. A farmer standing still, with a good fix already in memory, was
/// shown "checking your location" for as long as they stood there.
///
/// It happens on every entry to the walk, and again each time Android rebuilds
/// the activity, which this handset does when the camera opens. Twice in one
/// evening on the trial phone.

/// A geolocator stand-in that emits what a test tells it to.
///
/// Extends the platform interface rather than reimplementing it, so an added
/// method arrives as an inherited one that throws when used rather than as a
/// silently missing behaviour.
class _FakeGeo extends GeolocatorPlatform {
  final _positions = StreamController<Position>.broadcast();

  @override
  Future<bool> isLocationServiceEnabled() async => true;

  @override
  Future<LocationPermission> checkPermission() async =>
      LocationPermission.whileInUse;

  @override
  Future<LocationPermission> requestPermission() async =>
      LocationPermission.whileInUse;

  /// What Android remembers, if a test gives it one.
  Position? remembered;

  @override
  Future<Position?> getLastKnownPosition({
    bool forceLocationManager = false,
  }) async => remembered;

  @override
  Stream<Position> getPositionStream({LocationSettings? locationSettings}) =>
      _positions.stream;

  /// A position object, for either the stream or the remembered slot.
  static Position at(double lat, double lon, {double accuracy = 6}) => Position(
    latitude: lat,
    longitude: lon,
    timestamp: DateTime.now(),
    accuracy: accuracy,
    altitude: 0,
    altitudeAccuracy: 0,
    heading: -1,
    headingAccuracy: 0,
    speed: 0,
    speedAccuracy: 0,
  );

  void emit(double lat, double lon, {double accuracy = 6}) {
    _positions.add(
      Position(
        latitude: lat,
        longitude: lon,
        timestamp: DateTime.now(),
        accuracy: accuracy,
        altitude: 0,
        altitudeAccuracy: 0,
        heading: -1,
        headingAccuracy: 0,
        speed: 0,
        speedAccuracy: 0,
      ),
    );
  }

  Future<void> close() => _positions.close();
}

void main() {
  test('a late listener is given the fix the phone already has', () async {
    final geo = _FakeGeo();
    final positioning = Positioning(geolocator: geo);
    await positioning.start();

    // A fix arrives, and the farmer then stands still.
    geo.emit(18.1700, 75.8131);
    await Future<void>.delayed(Duration.zero);

    // The walk screen opens now -- or the activity was rebuilt and it opened
    // again. Nothing will move for a while, so nothing new will be emitted.
    final seen = <PositionState>[];
    final sub = positioning.stream.listen(seen.add);
    await Future<void>.delayed(Duration.zero);

    expect(
      seen,
      isNotEmpty,
      reason:
          'a farmer standing still must not be told the phone is still '
          'looking for a position it has already found',
    );
    expect(seen.first.status, FixStatus.ready);
    expect(seen.first.fix?.lat, 18.1700);

    await sub.cancel();
    await geo.close();
  });

  test('nothing is replayed before a position was ever asked for', () async {
    // Idle means "not started", which is not news. Replaying it would have a
    // screen decide the position is unavailable before start() has run.
    final geo = _FakeGeo();
    final positioning = Positioning(geolocator: geo);

    final seen = <PositionState>[];
    final sub = positioning.stream.listen(seen.add);
    await Future<void>.delayed(Duration.zero);

    expect(seen, isEmpty);

    await sub.cancel();
    await geo.close();
  });

  test('later updates still arrive after the replayed one', () async {
    // The replay must not replace the subscription. A farmer who starts walking
    // needs every subsequent fix, which is what the spacing rule is judged on.
    final geo = _FakeGeo();
    final positioning = Positioning(geolocator: geo);
    await positioning.start();
    geo.emit(18.1700, 75.8131);
    await Future<void>.delayed(Duration.zero);

    final seen = <PositionState>[];
    final sub = positioning.stream.listen(seen.add);
    await Future<void>.delayed(Duration.zero);

    geo.emit(18.1701, 75.8132);
    await Future<void>.delayed(Duration.zero);

    expect(seen, hasLength(2));
    expect(seen.last.fix?.lat, 18.1701);

    await sub.cancel();
    await geo.close();
  });

  test('a fix that has not moved still arrives, so accuracy can improve', () {
    // The other half of the same fault: the stream filtered to a metre, so a
    // stationary farmer whose first fix was forty metres never saw it improve.
    // Asserted on the settings rather than the behaviour, because the filtering
    // happens inside the platform and a stand-in cannot show it.
    expect(
      Positioning.locationSettings.distanceFilter,
      0,
      reason: 'a farmer who is not moving must still get updates',
    );
  });

  group('the position Android already remembers', () {
    // Shown so a screen is not blank while the receiver warms up: a farmer
    // reported the location taking a long time, and part of it was the app
    // waiting for a first fix while a usable answer sat in the platform.

    test('is offered at once, before any live fix', () async {
      final geo = _FakeGeo()..remembered = _FakeGeo.at(18.1700, 75.8131);
      final positioning = Positioning(geolocator: geo);

      final seen = <PositionState>[];
      final sub = positioning.stream.listen(seen.add);
      await positioning.start();
      await Future<void>.delayed(Duration.zero);

      final withFix = seen.where((s) => s.fix != null);
      expect(withFix, isNotEmpty, reason: 'the remembered position is known');
      expect(withFix.first.fix!.lat, 18.1700);

      await sub.cancel();
      await geo.close();
    });

    test('is never reported as ready, so nothing records on it', () async {
      // It can be minutes old, and the office judges the walk on exactly these
      // coordinates. A clip stamped with where the farmer used to be is wrong
      // evidence rather than missing evidence.
      final geo = _FakeGeo()..remembered = _FakeGeo.at(18.1700, 75.8131);
      final positioning = Positioning(geolocator: geo);

      final seen = <PositionState>[];
      final sub = positioning.stream.listen(seen.add);
      await positioning.start();
      await Future<void>.delayed(Duration.zero);

      final cached = seen.firstWhere((s) => s.fix?.fromCache == true);
      expect(cached.status, FixStatus.locating);
      expect(
        seen.any((s) => s.status == FixStatus.ready),
        isFalse,
        reason: 'nothing is ready until the receiver answers',
      );

      await sub.cancel();
      await geo.close();
    });

    test('a live fix replaces it and is marked live', () async {
      final geo = _FakeGeo()..remembered = _FakeGeo.at(18.1700, 75.8131);
      final positioning = Positioning(geolocator: geo);
      await positioning.start();
      await Future<void>.delayed(Duration.zero);

      geo.emit(18.1705, 75.8135);
      await Future<void>.delayed(Duration.zero);

      expect(positioning.state.status, FixStatus.ready);
      expect(positioning.state.fix!.fromCache, isFalse);
      expect(positioning.state.fix!.lat, 18.1705);

      await geo.close();
    });

    test('does not overwrite a live fix that arrived first', () async {
      // The remembered position is fetched asynchronously. If the receiver
      // answers while that is in flight, the older answer must not land on top
      // of the newer one.
      final geo = _FakeGeo()..remembered = _FakeGeo.at(18.1700, 75.8131);
      final positioning = Positioning(geolocator: geo);
      await positioning.start();
      geo.emit(18.1705, 75.8135);
      await Future<void>.delayed(Duration.zero);
      await Future<void>.delayed(Duration.zero);

      expect(positioning.state.fix!.lat, 18.1705);
      expect(positioning.state.fix!.fromCache, isFalse);

      await geo.close();
    });

    test('a phone with nothing remembered still works', () async {
      final geo = _FakeGeo();
      final positioning = Positioning(geolocator: geo);
      await positioning.start();
      await Future<void>.delayed(Duration.zero);

      expect(positioning.state.status, FixStatus.locating);
      expect(positioning.state.fix, isNull);

      geo.emit(18.1700, 75.8131);
      await Future<void>.delayed(Duration.zero);
      expect(positioning.state.status, FixStatus.ready);

      await geo.close();
    });
  });
}
