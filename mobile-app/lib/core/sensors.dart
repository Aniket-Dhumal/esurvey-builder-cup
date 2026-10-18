import 'dart:async';
import 'dart:math' as math;

import 'package:flutter/foundation.dart';
import 'package:geolocator/geolocator.dart';
import 'package:sensors_plus/sensors_plus.dart';

import 'config.dart';
import 'faults.dart';

/// Where the handset thinks it is, and how sure it is.
///
/// # Why the accuracy is never hidden
///
/// A photograph's whole evidential value is that it was taken inside the plot
/// it claims to show. That check is only as good as the position behind it, and
/// a fifty-metre fix cannot distinguish one field from the next. So the
/// accuracy is on screen the entire time a farmer is capturing, and a capture
/// taken on a poor fix is recorded as such rather than quietly treated as
/// equal to a good one.
///
/// # Why it watches rather than asks once
///
/// A handset that has just been taken out of a pocket reports sixty metres and
/// settles to six over about half a minute. Asking once takes the first
/// number; watching lets the farmer see it tighten, which is both a better
/// reading and the only honest way to ask somebody to wait.
enum FixStatus { idle, locating, ready, denied, unavailable }

class Fix {
  const Fix({
    required this.lat,
    required this.lon,
    required this.accuracyM,
    required this.at,
    this.altitudeM,
    this.headingDeg,
    this.isMocked,
    this.fromCache = false,
  });

  final double lat;
  final double lon;
  final double accuracyM;
  final DateTime at;
  final double? altitudeM;
  final double? headingDeg;

  /// Whether Android reported this fix as coming from a mock provider.
  ///
  /// Null where the platform did not say. That is deliberately not the same
  /// as false: a geofence that treats "did not tell us" as "genuine" is a
  /// geofence anyone can walk through by using an older handset, and the
  /// server needs to be able to tell the two apart.
  final bool? isMocked;

  /// Whether this is the position Android remembered rather than one taken now.
  ///
  /// # Why it is kept and why it is marked
  ///
  /// Android holds the last position it worked out, and handing that over costs
  /// nothing and takes no time. It is what lets a screen show where the farmer is
  /// the instant it opens, instead of an empty map and "checking your location"
  /// while the receiver warms up.
  ///
  /// It must never be written into a capture. It can be minutes old, and a
  /// recording stamped with where the farmer used to be is worse evidence than
  /// one with no position at all -- it is wrong rather than missing, and the
  /// office judges the walk on exactly these coordinates. So it is marked, the
  /// status stays [FixStatus.locating] until something live arrives, and nothing
  /// that records may use it.
  final bool fromCache;

  /// How long ago this position was taken.
  Duration get age => DateTime.now().difference(at);

  /// Whether this position describes where the farmer is now.
  ///
  /// # Why an age and not just a flag
  ///
  /// [fromCache] catches a position Android remembered from before the app asked.
  /// It does not catch the other way a position goes stale, which is the one the
  /// walk actually met: a live stream that stops delivering. The last reading then
  /// sits in memory with fromCache false, looking current, while the farmer walks
  /// away from the place it describes -- and the spacing rule, which decides whether
  /// they may record, judges the place they have left.
  ///
  /// Twelve seconds, against updates asked for every one. Generous enough that a
  /// receiver struggling under canopy is not called dead, short enough that a clip
  /// is never stamped with a position from the previous point of the walk.
  bool get current => !fromCache && age <= const Duration(seconds: 12);

  /// Good enough to mark a boundary corner or geofence a photograph.
  bool get usable => accuracyM <= Config.worstUsableAccuracyM;

  /// As good as a handset will give.
  bool get good => accuracyM <= Config.goodAccuracyM;
}

class PositionState {
  const PositionState(this.status, {this.fix, this.reason});

  final FixStatus status;
  final Fix? fix;

  /// Why there is no position, in words a farmer can act on.
  final String? reason;
}

/// A live position, with the best fix of the session kept.
///
/// Accuracy wanders. Keeping the tightest reading is a fairer record than
/// whichever happened to arrive last, and it also means a farmer who waited
/// for a good fix is not penalised because a cloud passed.
class Positioning {
  Positioning({GeolocatorPlatform? geolocator})
    : _geo = geolocator ?? GeolocatorPlatform.instance;

  final GeolocatorPlatform _geo;

  final _controller = StreamController<PositionState>.broadcast();
  StreamSubscription<Position>? _watch;

  PositionState _state = const PositionState(FixStatus.idle);
  Fix? _best;

  /// When a position last arrived, and how many times the receiver has been
  /// restarted since it last worked.
  DateTime? _lastUpdate;
  int _restarts = 0;
  Timer? _heartbeat;
  Timer? _restartAt;

  /// Whether the receiver has refused for a reason retrying cannot fix.
  ///
  /// Permission and a switched-off service are answers, not failures. Restarting
  /// into either of them forever would be a loop that spends the battery and
  /// tells the farmer the same thing every two seconds.
  bool _refused = false;

  /// How long a silence means the stream has died rather than gone quiet.
  ///
  /// Eight seconds, against updates asked for every one. A receiver delivering
  /// nothing for eight seconds while it holds a fix and has been given no error is
  /// not a slow receiver; it is a callback Android has stopped calling.
  ///
  /// This is the failure the walk actually met: a farmer halfway through a walk
  /// whose position simply stopped changing. No error arrived, the stream did not
  /// close, and nothing noticed -- so the last fix sat on screen looking current
  /// while the spacing rule was judging a place the farmer had left. Vendor power
  /// management does this, and this handset's vendor is one of the worst for it.
  static const _silenceMeansDead = Duration(seconds: 8);

  /// How long to wait before asking again, growing to a ceiling.
  ///
  /// A receiver that has just failed usually fails again immediately, and a tight
  /// loop against it is a flat battery in a field. Fifteen seconds is the ceiling
  /// because a farmer standing in a field waiting for a position will not wait
  /// longer than that without concluding the app is broken.
  Duration get _backoff {
    final seconds = 1 << (_restarts < 4 ? _restarts : 4);
    return Duration(seconds: seconds > 15 ? 15 : seconds);
  }

  /// How the handset is asked for positions.
  ///
  /// Named rather than inline so there is one place that says what the walk needs
  /// of the receiver, and so a test can assert it: the filter below is the
  /// difference between a farmer standing still being updated and being ignored,
  /// and that is not something a stand-in can demonstrate.
  static final locationSettings = AndroidSettings(
    accuracy: LocationAccuracy.best,
    // Fused location, not the legacy manager: it answers from wifi and cell
    // towers while the satellites are still being acquired, which is the
    // difference between a position in two seconds and one in forty. Explicit
    // because it is the whole reason a first fix indoors arrives at all.
    forceLocationManager: false,
    // A second between updates.
    //
    // Stated rather than left to the platform. With no distance filter the
    // receiver will report as fast as it can, and every report rebuilds a screen
    // showing a camera preview; a second is far finer than a walking farmer needs
    // and leaves the preview alone the rest of the time.
    intervalDuration: const Duration(seconds: 1),
    // Every update, not every metre.
    //
    // It filtered to a metre, on the reasoning that a farmer walking a boundary
    // moves slowly and a coarser filter would miss the corner they stopped at.
    // The cost was the opposite case: a farmer who is *not* moving gets no
    // updates at all, so a first fix of forty metres never improves on screen
    // however long they wait, and the walk cannot re-judge where they stand.
    //
    // Both of tonight's faults came from that. Four clips were recorded in one
    // spot because the spacing rule was never re-evaluated while the farmer stood
    // still, and the location looked slow to arrive when it had already arrived.
    //
    // The extra callbacks cost nothing worth counting: the receiver is already
    // running at this accuracy, and about one update a second is well inside what
    // the screen and the speech throttles handle.
    distanceFilter: 0,
  );

  /// Position updates, beginning with what is already known.
  ///
  /// # Why this replays
  ///
  /// It did not, and that is why the app looked slow at finding a position when
  /// the phone had already found one.
  ///
  /// The underlying controller is a broadcast stream, so a new listener heard
  /// nothing until the next update -- and the next update only comes when the
  /// handset moves a metre. A farmer standing still, with a good fix already in
  /// memory, saw "checking your location" for as long as they stood there. It
  /// happens on every entry to the walk, and again every time Android rebuilds
  /// the activity, which this handset does on opening the camera.
  ///
  /// So each listener is given the current state first. Nothing waits on the
  /// satellites for something the phone worked out a minute ago.
  ///
  /// [FixStatus.idle] is not replayed: it means nothing has been asked for yet,
  /// which is not news and would make a screen flash "unavailable" before
  /// start() has run.
  Stream<PositionState> get stream async* {
    if (_state.status != FixStatus.idle) yield _state;
    yield* _controller.stream;
  }

  PositionState get state => _state;

  /// The tightest fix seen since [start].
  Fix? get best => _best;

  /// When the receiver was asked, so the wait can be reported rather than
  /// argued about.
  DateTime? _askedAt;

  /// How long a first fix may take before it is worth recording as a fault.
  ///
  /// Ten seconds. Indoors or under canopy a cold start legitimately takes longer,
  /// and this does not stop anything -- it writes down what happened so a farmer
  /// reporting "the location takes ages" can be answered with a number instead of
  /// a guess.
  static const _firstFixWithin = Duration(seconds: 10);

  Future<void> start() async {
    if (_watch != null) return;
    _restartAt?.cancel();
    _restartAt = null;
    _askedAt = DateTime.now();
    // The last known position is carried into the wait rather than blanked.
    //
    // On a cold start there is none and this is the same as before. On a restart
    // there is, and a screen that shows roughly where the farmer is beats an empty
    // one -- which is the same reason the remembered position is seeded below.
    // Recording against it is prevented by Fix.current, not by hiding it.
    _emit(PositionState(FixStatus.locating, fix: _state.fix));

    try {
      if (!await _geo.isLocationServiceEnabled()) {
        _emit(
          const PositionState(
            FixStatus.unavailable,
            reason: 'Location is switched off on this phone.',
          ),
        );
        // An answer, not a failure: retrying cannot switch it on. The watchdog
        // stands down rather than asking every two seconds forever.
        _refused = true;
        _stopWatching();
        return;
      }

      var permission = await _geo.checkPermission();
      if (permission == LocationPermission.denied) {
        permission = await _geo.requestPermission();
      }
      if (permission == LocationPermission.denied ||
          permission == LocationPermission.deniedForever) {
        _emit(
          const PositionState(
            FixStatus.denied,
            reason: 'This app has not been allowed to use your location.',
          ),
        );
        _refused = true;
        _stopWatching();
        return;
      }

      // What Android already worked out, while the receiver warms up.
      //
      // Costs nothing and arrives at once, so the screen can show where the
      // farmer is instead of an empty wait. Deliberately emitted as `locating`
      // rather than `ready`: it may be minutes old, and nothing that records a
      // position may use it -- see Fix.fromCache.
      unawaited(_seedFromCache());

      _refused = false;
      _watch = _geo
          .getPositionStream(locationSettings: locationSettings)
          .listen(
            _onPosition,
            onError: _onError,
            // A stream that ends is the other way this dies, and it used to end
            // in silence: the subscription completed, nothing emitted again, and
            // the walk went on judging the last position it had.
            onDone: _onStreamEnded,
            // The subscription survives an error so a single bad reading does not
            // end the walk's location. A fatal one arrives as onDone behind it.
            cancelOnError: false,
          );
      _lastUpdate = DateTime.now();
      _watchForSilence();
    } catch (e) {
      // The reason reaches the screen, so it has to be a sentence rather than
      // a PlatformException. The exception goes to the log.
      debugPrint('[sensors.watch] $e');
      _emit(
        PositionState(
          FixStatus.unavailable,
          reason: 'ठिकाण मिळत नाही · Location is unavailable on this phone.',
        ),
      );
    }
  }

  /// Show the last position Android remembers, until a live one arrives.
  Future<void> _seedFromCache() async {
    try {
      final p = await _geo.getLastKnownPosition();
      // Overtaken already: a live fix arrived while this was being fetched, and
      // that one is better in every way.
      if (p == null || _state.status == FixStatus.ready) return;
      _emit(
        PositionState(
          FixStatus.locating,
          fix: Fix(
            lat: p.latitude,
            lon: p.longitude,
            accuracyM: p.accuracy,
            at: p.timestamp,
            altitudeM: p.altitude,
            headingDeg: p.heading >= 0 ? p.heading : null,
            isMocked: p.isMocked,
            fromCache: true,
          ),
        ),
      );
    } catch (e) {
      // No remembered position, or the platform refused. Nothing is lost: the
      // live stream is what everything actually waits on.
      debugPrint('[sensors.cache] $e');
    }
  }

  void _onPosition(Position p) {
    final asked = _askedAt;
    if (asked != null) {
      final took = DateTime.now().difference(asked);
      _askedAt = null;
      debugPrint(
        '[sensors] first fix in ${took.inMilliseconds} ms, '
        'accuracy ${p.accuracy.round()} m',
      );
      if (took > _firstFixWithin) {
        Faults.note(
          'sensors.slowfix',
          'first fix took ${took.inSeconds}s at ${p.accuracy.round()} m',
        );
      }
    }

    final fix = Fix(
      lat: p.latitude,
      lon: p.longitude,
      accuracyM: p.accuracy,
      at: p.timestamp,
      altitudeM: p.altitude,
      headingDeg: p.heading >= 0 ? p.heading : null,
      isMocked: p.isMocked,
    );

    final best = _best;
    if (best == null || fix.accuracyM < best.accuracyM) _best = fix;

    _lastUpdate = DateTime.now();
    // Working again, so the next failure starts its backoff from one second rather
    // than from wherever the last run of bad luck left it.
    _restarts = 0;
    _emit(PositionState(FixStatus.ready, fix: fix));
  }

  void _onError(Object e) {
    debugPrint('position error: $e');
    Faults.note('sensors.stream', e);
    // Not final. This used to emit unavailable and stop there, and because start()
    // returns early while a subscription exists, location was then finished for the
    // life of the app -- one hiccup from the fused provider and a farmer's walk had
    // no position again however long they waited.
    _reacquire('the receiver reported an error');
  }

  /// The stream ended on its own.
  ///
  /// Nothing did this deliberately. Android ends it: a provider restarted, a vendor
  /// power manager deciding an app has had enough, the camera taking resources the
  /// location service wanted. It arrived as silence, which is the worst way for it
  /// to arrive, because the last fix stayed on screen looking current.
  void _onStreamEnded() => _reacquire('the receiver stopped sending');

  /// Watch for a stream that has stopped delivering without saying so.
  ///
  /// The case neither an error nor an ending catches, and the one the walk actually
  /// met. Checked rather than trusted, because a position that is quietly eight
  /// seconds old is indistinguishable on screen from one that is current -- and the
  /// spacing rule, which decides whether a farmer may record, is judging it.
  void _watchForSilence() {
    _heartbeat?.cancel();
    _heartbeat = Timer.periodic(const Duration(seconds: 2), (_) {
      if (_watch == null || _refused) return;
      final last = _lastUpdate;
      if (last == null) return;
      if (DateTime.now().difference(last) > _silenceMeansDead) {
        _reacquire('the receiver went quiet');
      }
    });
  }

  /// Throw the receiver away and ask for a new one.
  ///
  /// The last fix is kept and still emitted, because a screen that knows roughly
  /// where the farmer is beats an empty one -- but the state goes back to locating,
  /// which is what stops the walk recording a clip against it. See Fix.age and
  /// _fixIsLive: a position that is not current guides and does not record.
  void _reacquire(String why) {
    if (_refused) return;
    // The wait is read before the count is raised, so the first attempt after a
    // working receiver comes a second later rather than two. Most of these are a
    // single hiccup, and a farmer standing in a field notices the difference.
    final wait = _backoff;
    _restarts++;
    debugPrint(
      '[sensors] $why; asking again in ${wait.inSeconds}s '
      '(attempt $_restarts)',
    );

    unawaited(_watch?.cancel());
    _watch = null;
    _lastUpdate = null;

    _emit(
      PositionState(
        FixStatus.locating,
        fix: _state.fix,
        reason: 'ठिकाण पुन्हा तपासत आहे · Checking your position again.',
      ),
    );

    _restartAt?.cancel();
    _restartAt = Timer(wait, () => unawaited(start()));
  }

  void _stopWatching() {
    _heartbeat?.cancel();
    _heartbeat = null;
    _restartAt?.cancel();
    _restartAt = null;
  }

  /// Forget the best fix, for a farmer starting a fresh boundary or a fresh
  /// survey. Without this a stale reading from another field would follow
  /// them.
  void resetBest() => _best = null;

  Future<void> stop() async {
    _stopWatching();
    await _watch?.cancel();
    _watch = null;
    _lastUpdate = null;
    _emit(const PositionState(FixStatus.idle));
  }

  Future<void> dispose() async {
    await stop();
    await _controller.close();
  }

  void _emit(PositionState s) {
    _state = s;
    if (!_controller.isClosed) _controller.add(s);
  }
}

/// How good a fix is, in words rather than metres.
///
/// A farmer is not helped by "±34 m". They are helped by being told the
/// position is poor and that standing still in the open will fix it.
({String mr, String en, AccuracyTone tone}) describeAccuracy(double m) {
  if (m <= Config.goodAccuracyM) {
    return (
      mr: 'ठिकाण चांगले',
      en: 'Position is good',
      tone: AccuracyTone.good,
    );
  }
  if (m <= 25) {
    return (mr: 'ठिकाण ठीक', en: 'Position is usable', tone: AccuracyTone.fair);
  }
  if (m <= Config.worstUsableAccuracyM) {
    return (mr: 'ठिकाण कमजोर', en: 'Position is weak', tone: AccuracyTone.weak);
  }
  return (
    mr: 'ठिकाण मिळत नाही',
    en: 'Position is too uncertain',
    tone: AccuracyTone.unusable,
  );
}

enum AccuracyTone { good, fair, weak, unusable }

/// Metres between two positions.
///
/// The haversine formula on a spherical earth. At field scale the error
/// against a proper geodesic is centimetres, far below the GPS uncertainty
/// this is compared against.
double metresBetween(double lat1, double lon1, double lat2, double lon2) {
  const r = 6371000.0;
  final dLat = _rad(lat2 - lat1);
  final dLon = _rad(lon2 - lon1);
  final a =
      math.sin(dLat / 2) * math.sin(dLat / 2) +
      math.cos(_rad(lat1)) *
          math.cos(_rad(lat2)) *
          math.sin(dLon / 2) *
          math.sin(dLon / 2);
  return r * 2 * math.atan2(math.sqrt(a), math.sqrt(1 - a));
}

double _rad(double deg) => deg * math.pi / 180;

/// The area a closed walk encloses, in hectares.
///
/// A local flat-earth approximation, for showing a farmer the area as they
/// walk so a wrong turn is obvious before they pace another kilometre. The
/// authoritative figure is computed by PostGIS on the server; at field scale
/// the difference is far below the GPS uncertainty already on screen.
double hectaresOf(List<({double lat, double lon})> ring) {
  if (ring.length < 3) return 0;

  final lat0 = _rad(
    ring.map((p) => p.lat).reduce((a, b) => a + b) / ring.length,
  );
  const mPerLat = 111132.0;
  final mPerLon = 111320.0 * math.cos(lat0);

  var twice = 0.0;
  for (var i = 0; i < ring.length; i++) {
    final a = ring[i];
    final b = ring[(i + 1) % ring.length];
    twice +=
        (a.lon * mPerLon) * (b.lat * mPerLat) -
        (b.lon * mPerLon) * (a.lat * mPerLat);
  }
  return (twice.abs() / 2) / 10000;
}

/// A guntha, in hectares.
///
/// # The correction
///
/// This was forty to the hectare, and forty is the number of guntha in an *acre*.
/// A guntha is 33 feet square -- 1089 square feet, 101.17 square metres -- so a
/// hectare is a little under ninety-nine of them, not forty.
///
/// It was wrong where it mattered most. The boundary screen shows a farmer the area
/// of the outline they have just walked, in hectares and in guntha, so they can
/// check it against what they know their land to be. A farmer whose plot is a
/// hundred guntha was being shown forty, and the honest conclusion from that is
/// that the trace had failed.
///
/// Derived from the acre rather than written as a decimal, because the relationship
/// that is actually memorable -- forty guntha to an acre -- is then visible, and a
/// constant nobody can check is a constant that drifts.
const double hectaresPerAcre = 0.404686;
const double gunthaPerAcre = 40;
const double hectaresPerGuntha = hectaresPerAcre / gunthaPerAcre;

/// Hectares as guntha, because a farmer in Maharashtra says guntha.
///
/// Shown alongside rather than instead: the record is kept in hectares and the
/// farmer thinks in guntha, and only showing one of them fails somebody.
double gunthaOf(double hectares) => hectares / hectaresPerGuntha;

/// How far the camera is tilted below the horizon.
///
/// # Why this is not the compass
///
/// An earlier version fed compass heading into the tilt guidance, which is a
/// different sensor answering a different question: heading is which way the
/// farmer is facing, and tilt is whether the camera is pointing at the field
/// or at their feet. A wide shot taken pointing at the ground establishes
/// nothing, and no amount of turning around fixes it.
///
/// # The geometry
///
/// The accelerometer reports gravity in the handset's own frame: x across the
/// screen, y up it, z out of it toward the person holding it. So with the
/// phone held upright and the rear camera looking at the horizon, gravity
/// runs down the screen -- y is about -9.81 and z is near zero. Lay it flat,
/// face up, and the rear camera looks straight down: z becomes about +9.81
/// and y near zero.
///
/// The angle between those is the tilt, and atan2 gives it without a
/// discontinuity at either end.
class Tilt {
  Tilt({Stream<AccelerometerEvent>? events})
    : _events =
          events ??
          accelerometerEventStream(
            // Ten a second. Enough for a nudge that keeps up with somebody
            // raising a phone, and slow enough not to wake the CPU
            // constantly on a handset whose battery is the reason the
            // farmer came outside once rather than twice.
            samplingPeriod: const Duration(milliseconds: 100),
          );

  final Stream<AccelerometerEvent> _events;
  StreamSubscription<AccelerometerEvent>? _sub;
  final _controller = StreamController<double>.broadcast();

  double? _smoothed;

  /// Degrees below horizontal: 0 looking at the horizon, 90 straight down.
  Stream<double> get stream => _controller.stream;
  double? get degrees => _smoothed;

  void start() {
    _sub ??= _events.listen((e) {
      final raw = math.atan2(e.z, -e.y) * 180 / math.pi;

      // A hand shakes, and a nudge that flickers between "tilt up" and "tilt
      // down" is worse than none. A light exponential smoothing settles it
      // without adding a lag somebody would notice.
      final previous = _smoothed;
      _smoothed = previous == null ? raw : previous + (raw - previous) * 0.25;

      if (!_controller.isClosed) _controller.add(_smoothed!);
    });
  }

  Future<void> dispose() async {
    await _sub?.cancel();
    _sub = null;
    await _controller.close();
  }
}
