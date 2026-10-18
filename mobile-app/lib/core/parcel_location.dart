import 'dart:math' as math;

import 'package:flutter/foundation.dart';

import 'sensors.dart';

/// Where the farmer is, relative to the field they are claiming for.
///
/// # Why this exists
///
/// The app knew the accuracy of its own position — "±9 m" — and nothing about
/// whether that position was anywhere near the parcel. A farmer six hundred
/// kilometres away could fill in the whole report and take every photograph,
/// and only the server, at submission, would refuse it.
///
/// That is the wrong end of the process to say no at. Everything here answers
/// the question the app should have been asking all along: *how far is this
/// person from the land they are describing?*
///
/// # Why the geometry is done on the handset
///
/// The parcel polygon already arrives with the registry record, so the answer
/// costs no request and works with no signal — which matters, because the
/// place this question gets asked is a field.

/// How far outside the parcel still counts as being at it.
///
/// The same 25 m the server's geofence uses. Consumer GNSS is good to 5–15 m
/// under open sky and worse under a canopy, and a farmer standing on their own
/// bund is a few metres outside the polygon and is not lying about anything.
const double atParcelWithinM = 25;

/// Where the farmer stands in relation to a parcel.
class ParcelFix {
  const ParcelFix({
    required this.distanceM,
    required this.inside,
    required this.simulated,
  });

  /// Metres to the nearest edge. Zero when inside the polygon.
  final double distanceM;

  final bool inside;

  /// True when a debug build is pretending. Never true in release.
  final bool simulated;

  /// Close enough to survey from.
  bool get atParcel => distanceM <= atParcelWithinM;

  /// Whether there is a distance at all.
  ///
  /// [locate] returns an infinite distance as a sentinel for two states that are
  /// not "far away": no position fix yet, and no parcel outline to measure
  /// against. Neither is a distance, and a screen that treats them as one tells
  /// the farmer something false.
  bool get known => distanceM.isFinite;

  /// Distance in words, at the scale a person thinks in.
  ///
  /// Metres near the field, kilometres when the answer is "not remotely" — a
  /// farmer told they are 598,000 m away has been told nothing.
  ///
  /// The first branch used to be all there was, and `.round()` on the infinity
  /// this class produces itself throws `Unsupported operation: Infinity or NaN
  /// toInt`. Thrown from a build method, which meant the report screen failed to
  /// render at all for any farmer whose location was denied or whose first fix
  /// had not yet arrived. Found on a handset, by the fault log.
  String get pretty {
    if (!known) return 'अंतर माहीत नाही';
    if (distanceM < 1000) return '${distanceM.round()} मी';
    return '${(distanceM / 1000).round()} किमी';
  }
}

/// The parcel's outline, as the registry gave it.
///
/// Holds the outer rings only. Holes would matter for area and do not matter
/// for "is this person standing at this field", which is the one question
/// asked here.
class ParcelShape {
  const ParcelShape(this.rings);

  /// Each ring is a closed list of (lat, lon).
  final List<List<({double lat, double lon})>> rings;

  bool get isEmpty => rings.isEmpty;

  /// The outer ring's area in square metres.
  ///
  /// A flat-earth approximation, which at the scale of a field is exact
  /// enough for what it is used for: deciding how far the walk should ask a
  /// farmer to move on this particular plot. A tenth of a percent either way
  /// changes nothing about that.
  double get areaM2 {
    if (rings.isEmpty || rings.first.length < 4) return 0;
    final ring = rings.first;
    final lat0 = ring.first.lat * math.pi / 180;
    final mPerLat = 111320.0;
    final mPerLon = 111320.0 * math.cos(lat0);
    var twice = 0.0;
    for (var i = 0; i < ring.length - 1; i++) {
      final x1 = ring[i].lon * mPerLon, y1 = ring[i].lat * mPerLat;
      final x2 = ring[i + 1].lon * mPerLon, y2 = ring[i + 1].lat * mPerLat;
      twice += x1 * y2 - x2 * y1;
    }
    return twice.abs() / 2;
  }

  /// Reads GeoJSON as `plot_geometry` delivers it.
  ///
  /// Returns an empty shape rather than throwing on anything unexpected. Live
  /// AgriStack sends `plot_geometry: null` for every farmer we can reach, so a
  /// missing or malformed polygon is the ordinary case and must not be an
  /// error — the caller reads emptiness as "no boundary to check against".
  factory ParcelShape.fromGeoJson(Object? geometry) {
    if (geometry is! Map) return const ParcelShape([]);

    final type = '${geometry['type'] ?? ''}';
    final coords = geometry['coordinates'];
    if (coords is! List) return const ParcelShape([]);

    final rings = <List<({double lat, double lon})>>[];

    void addRing(Object? ring) {
      if (ring is! List) return;
      final pts = <({double lat, double lon})>[];
      for (final p in ring) {
        // GeoJSON is [lon, lat]. Getting that backwards puts Maharashtra in
        // the Indian Ocean, which is the kind of error that looks like a
        // working app right up until somebody checks a distance.
        if (p is List && p.length >= 2 && p[0] is num && p[1] is num) {
          pts.add((
            lat: (p[1] as num).toDouble(),
            lon: (p[0] as num).toDouble(),
          ));
        }
      }
      if (pts.length >= 3) rings.add(pts);
    }

    switch (type) {
      case 'Polygon':
        // [ring, hole, hole...] — the first is the outline.
        if (coords.isNotEmpty) addRing(coords.first);
      case 'MultiPolygon':
        for (final polygon in coords) {
          if (polygon is List && polygon.isNotEmpty) addRing(polygon.first);
        }
      default:
        return const ParcelShape([]);
    }

    return ParcelShape(rings);
  }

  /// A point inside the parcel, for the debug override and for centring a map.
  ({double lat, double lon})? get centroid {
    if (rings.isEmpty) return null;
    final ring = rings.first;
    var lat = 0.0, lon = 0.0;
    for (final p in ring) {
      lat += p.lat;
      lon += p.lon;
    }
    return (lat: lat / ring.length, lon: lon / ring.length);
  }
}

/// How far a position is from a parcel.
///
/// Zero when inside. Otherwise the distance to the nearest edge, which is what
/// a farmer standing on a bund wants told to them — not the distance to some
/// centre point they have no reason to care about.
double distanceToParcel(double lat, double lon, ParcelShape shape) {
  if (shape.isEmpty) return double.infinity;

  for (final ring in shape.rings) {
    if (_inside(lat, lon, ring)) return 0;
  }

  var best = double.infinity;
  for (final ring in shape.rings) {
    for (var i = 0; i < ring.length; i++) {
      final a = ring[i];
      final b = ring[(i + 1) % ring.length];
      final d = _toSegment(lat, lon, a, b);
      if (d < best) best = d;
    }
  }
  return best;
}

/// Ray casting, in degrees.
///
/// Fine at this scale: a parcel spans a few hundred metres, where a degree of
/// longitude is locally a straight line and the shape does not wrap anything.
bool _inside(double lat, double lon, List<({double lat, double lon})> ring) {
  var inside = false;
  for (var i = 0, j = ring.length - 1; i < ring.length; j = i++) {
    final yi = ring[i].lat, xi = ring[i].lon;
    final yj = ring[j].lat, xj = ring[j].lon;
    final crosses =
        (yi > lat) != (yj > lat) &&
        lon < (xj - xi) * (lat - yi) / (yj - yi) + xi;
    if (crosses) inside = !inside;
  }
  return inside;
}

/// Distance in metres from a point to a segment.
///
/// Projected to a local metric plane first — longitude compressed by the
/// cosine of the latitude — because a degree of longitude at 18°N is about
/// 106 km against a degree of latitude's 111 km, and treating them as equal
/// puts the answer out by five per cent in one axis.
double _toSegment(
  double lat,
  double lon,
  ({double lat, double lon}) a,
  ({double lat, double lon}) b,
) {
  const metresPerDegreeLat = 111_320.0;
  final metresPerDegreeLon = metresPerDegreeLat * math.cos(lat * math.pi / 180);

  double x(double d) => d * metresPerDegreeLon;
  double y(double d) => d * metresPerDegreeLat;

  final px = x(lon), py = y(lat);
  final ax = x(a.lon), ay = y(a.lat);
  final bx = x(b.lon), by = y(b.lat);

  final dx = bx - ax, dy = by - ay;
  final lenSq = dx * dx + dy * dy;
  if (lenSq == 0) return metresBetween(lat, lon, a.lat, a.lon);

  // Where the perpendicular lands, clamped to the segment's ends.
  var t = ((px - ax) * dx + (py - ay) * dy) / lenSq;
  t = t.clamp(0.0, 1.0);

  final cx = ax + t * dx, cy = ay + t * dy;
  return math.sqrt((px - cx) * (px - cx) + (py - cy) * (py - cy));
}

/// Whether this build may pretend to be somewhere it is not.
///
/// # Why this exists at all
///
/// The fence is the point of the survey, and it makes the survey untestable
/// from anywhere but the field. A developer six hundred kilometres away cannot
/// walk four stations across a parcel in Malegaon, so without this the whole
/// flow can only be exercised by travelling to Solapur.
///
/// # Why it cannot reach a release build
///
/// kDebugMode is a compile-time constant. In a release build this returns
/// false, the branch is dropped by the compiler, and there is no flag, no
/// setting and no menu that turns it back on. The override is not disabled in
/// production — it is absent from it.
bool get canSimulateLocation => kDebugMode;

/// Where the app should treat the farmer as standing.
///
/// Returns the real fix unless a debug build has been asked to pretend, in
/// which case it returns the parcel's centroid. Callers get a [ParcelFix] that
/// says which happened, so the screen can say so too — a simulated position
/// that looks real is how a demo becomes a false claim.
ParcelFix locate({
  required Fix? fix,
  required ParcelShape shape,
  bool simulate = false,
}) {
  if (simulate && canSimulateLocation) {
    final c = shape.centroid;
    if (c != null) {
      return const ParcelFix(distanceM: 0, inside: true, simulated: true);
    }
  }

  if (fix == null || shape.isEmpty) {
    return const ParcelFix(
      distanceM: double.infinity,
      inside: false,
      simulated: false,
    );
  }

  final d = distanceToParcel(fix.lat, fix.lon, shape);
  return ParcelFix(distanceM: d, inside: d == 0, simulated: false);
}
