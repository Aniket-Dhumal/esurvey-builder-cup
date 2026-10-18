import 'dart:async';

import 'package:esurvey/core/sensors.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:geolocator/geolocator.dart';

/// What happens when the receiver stops, which it does.
///
/// # The fault this is about
///
/// A farmer halfway through a walk whose position simply stopped changing. No error
/// on screen, no crash, no message: the last reading stayed where it was, the
/// spacing rule went on judging a place the farmer had already left, and the Record
/// button stayed enabled -- so a clip could be stamped with the coordinates of the
/// previous point.
///
/// Three separate things were wrong, and each is a test below.
///
/// An error ended location for the life of the app. `_onError` reported
/// unavailability and left the subscription in place, and `start()` returns early
/// when a subscription exists -- so one hiccup from the fused provider and no
/// position ever arrived again, however long the farmer waited.
///
/// A stream that ended did so in silence. Nothing noticed and nothing asked again.
///
/// And a stream that simply stopped delivering was invisible, because a position
/// eight seconds old looks exactly like a current one. This is the one the walk
/// met, and it is what vendor power management does to a foreground app -- this
/// handset's vendor being among the worst for it.
///
/// # Why the last fix is kept through all of it
///
/// A screen that knows roughly where the farmer is beats an empty one, so the stale
/// reading stays on display. What must not happen is recording against it, and that
/// is [Fix.current]'s job rather than this class's: a position that is not current
/// guides and does not record.
class _FlakyGeo extends GeolocatorPlatform {
  _FlakyGeo();

  /// A fresh controller per subscription, so a restart is observable: the class
  /// under test is expected to throw the old stream away and ask for another.
  final streams = <StreamController<Position>>[];

  bool serviceOn = true;
  LocationPermission permission = LocationPermission.whileInUse;

  @override
  Future<bool> isLocationServiceEnabled() async => serviceOn;

  @override
  Future<LocationPermission> checkPermission() async => permission;

  @override
  Future<LocationPermission> requestPermission() async => permission;

  @override
  Future<Position?> getLastKnownPosition({
    bool forceLocationManager = false,
  }) async => null;

  @override
  Stream<Position> getPositionStream({LocationSettings? locationSettings}) {
    final c = StreamController<Position>();
    streams.add(c);
    return c.stream;
  }

  /// How many times the receiver has been asked for a stream.
  int get asked => streams.length;

  StreamController<Position> get latest => streams.last;

  void emit({double lat = 18.17, double lon = 75.8131, double accuracy = 6}) =>
      latest.add(
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

  Future<void> closeAll() async {
    for (final c in streams) {
      if (!c.isClosed) await c.close();
    }
  }
}

/// Wait for real time to pass, since the watchdog runs on real timers.
Future<void> _realSeconds(num s) =>
    Future<void>.delayed(Duration(milliseconds: (s * 1000).round()));

void main() {
  test('an error does not end location for the rest of the walk', () async {
    // The whole of the first fault. One error used to be permanent, because the
    // subscription stayed in place and start() refuses to run twice.
    final geo = _FlakyGeo();
    final positioning = Positioning(geolocator: geo);
    addTearDown(() async {
      await positioning.dispose();
      await geo.closeAll();
    });

    await positioning.start();
    geo.emit();
    await _realSeconds(0.1);
    expect(positioning.state.status, FixStatus.ready);

    geo.latest.addError(Exception('the fused provider hiccupped'));
    await _realSeconds(1.4);

    expect(
      geo.asked,
      greaterThan(1),
      reason: 'the receiver was never asked again, so the walk had no position',
    );

    // And it works again, which is the point rather than merely being retried.
    geo.emit(lat: 18.1701);
    await _realSeconds(0.1);
    expect(positioning.state.status, FixStatus.ready);
    expect(positioning.state.fix?.lat, 18.1701);
  });

  test('a stream that ends is noticed and replaced', () async {
    // Android ends it: a provider restarting, the camera taking resources the
    // location service wanted. It arrived as silence.
    final geo = _FlakyGeo();
    final positioning = Positioning(geolocator: geo);
    addTearDown(() async {
      await positioning.dispose();
      await geo.closeAll();
    });

    await positioning.start();
    geo.emit();
    await _realSeconds(0.1);

    await geo.latest.close();
    await _realSeconds(1.4);

    expect(geo.asked, greaterThan(1));
  });

  test(
    'a receiver that goes quiet is thrown away rather than believed',
    () async {
      // The fault the walk actually met, and the one neither an error nor an ending
      // catches. Nothing arrives; the last reading simply ages.
      //
      // Eight seconds of silence is the threshold, so this test takes about nine. It
      // is worth the wall clock: the alternative is a farmer recording four clips
      // against one position and nobody finding out until an officer reads the claim.
      final geo = _FlakyGeo();
      final positioning = Positioning(geolocator: geo);
      addTearDown(() async {
        await positioning.dispose();
        await geo.closeAll();
      });

      await positioning.start();
      geo.emit();
      await _realSeconds(0.1);
      expect(positioning.state.status, FixStatus.ready);

      await _realSeconds(12);

      expect(
        geo.asked,
        greaterThan(1),
        reason: 'a receiver silent for eight seconds is not a slow receiver',
      );
      expect(
        positioning.state.status,
        FixStatus.locating,
        reason: 'the screen must stop claiming the position is current',
      );
      expect(
        positioning.state.fix,
        isNotNull,
        reason:
            'the last position is still worth showing; it is not worth recording',
      );
    },
    timeout: const Timeout(Duration(seconds: 40)),
  );

  test(
    'a refusal is an answer, so it is not retried forever',
    () async {
      // Permission and a switched-off service cannot be fixed by asking again. A
      // watchdog looping into either is a flat battery and the same message every two
      // seconds.
      final geo = _FlakyGeo()..permission = LocationPermission.deniedForever;
      final positioning = Positioning(geolocator: geo);
      addTearDown(() async {
        await positioning.dispose();
        await geo.closeAll();
      });

      await positioning.start();
      expect(positioning.state.status, FixStatus.denied);

      final askedOnce = geo.asked;
      await _realSeconds(5);
      expect(
        geo.asked,
        askedOnce,
        reason: 'a denied permission was asked about again and again',
      );
    },
    timeout: const Timeout(Duration(seconds: 30)),
  );

  group('a position that is no longer current', () {
    test('guides but does not record', () async {
      // The rule that makes a stale reading safe. Twelve seconds against updates
      // asked for every one.
      final stale = Fix(
        lat: 18.17,
        lon: 75.8131,
        accuracyM: 6,
        at: DateTime.now().subtract(const Duration(seconds: 20)),
      );
      expect(stale.current, isFalse);
      expect(
        stale.usable,
        isTrue,
        reason: 'accuracy and currency are different questions',
      );
    });

    test('a reading from a moment ago is current', () async {
      final fresh = Fix(
        lat: 18.17,
        lon: 75.8131,
        accuracyM: 6,
        at: DateTime.now().subtract(const Duration(seconds: 2)),
      );
      expect(fresh.current, isTrue);
    });

    test('and one Android merely remembered never is', () async {
      // The case the old check caught, which must keep being caught: a remembered
      // position may be minutes old and the office judges the walk on exactly these
      // coordinates.
      final remembered = Fix(
        lat: 18.17,
        lon: 75.8131,
        accuracyM: 6,
        at: DateTime.now(),
        fromCache: true,
      );
      expect(remembered.current, isFalse);
    });
  });
}
