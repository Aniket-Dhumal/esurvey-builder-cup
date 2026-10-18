import 'dart:math' as math;

import '../core/parcel_location.dart';
import '../core/sensors.dart';

/// Walking a farmer to a point in their own field.
///
/// # Why this is not turn-by-turn
///
/// A handset's heading is course-over-ground, so it only means anything once
/// somebody is already moving, and the magnetometer that would answer while
/// standing still is routinely uncalibrated. A confident "turn left" that is
/// forty degrees wrong is worse than no instruction at all: it sends someone
/// across their own field in the wrong direction and costs them the trust to
/// follow the next one.
///
/// So the guidance is a direction and a number. The direction is coarse enough
/// to survive being wrong by twenty degrees -- "north-east" is a quarter of
/// the horizon -- and the number is what actually does the work. It is correct
/// regardless of which way anybody is facing, it updates as they walk, and a
/// farmer resolves it by looking at their own field, which they know far
/// better than any of this does.

/// One of the four points the server assigned.
class Station {
  const Station({
    required this.stationNo,
    required this.lat,
    required this.lon,
    required this.role,
    required this.placement,
    this.requiredSpacingM = 0,
  });

  factory Station.fromJson(Map<String, Object?> json) => Station(
    stationNo: (json['station_no'] as num?)?.toInt() ?? 0,
    lat: (json['lat'] as num?)?.toDouble() ?? 0,
    lon: (json['lon'] as num?)?.toDouble() ?? 0,
    role: '${json['role'] ?? ''}',
    placement: '${json['placement'] ?? ''}',
    requiredSpacingM: (json['required_spacing_m'] as num?)?.toDouble() ?? 0,
  );

  final int stationNo;
  final double lat;
  final double lon;

  /// low, mid_left, mid_right, high.
  final String role;

  /// terrain when the ends mean high and low; geometric when the parcel was
  /// too flat for that and they are simply the ends of the field.
  final String placement;

  /// How far the office requires this claim's walk to move between clips.
  ///
  /// The office's own figure, computed from this crop's sown extent and sent
  /// with the points. Zero where the office did not say -- an older server, or a
  /// draft saved before this existed -- in which case [spacingFor] falls back to
  /// working it out locally.
  ///
  /// Obeyed rather than recomputed. The app used to hold its own copy of the
  /// arithmetic, and the copies only had to disagree once for a farmer to record
  /// three clips the office refused on every pass afterwards.
  final double requiredSpacingM;

  /// What to call this station out loud.
  ///
  /// Under geometric placement the ends are not higher or lower than each
  /// other in any way a farmer would recognise, so calling one "the low part"
  /// would be a small lie told four times per report. They become the ends of
  /// the field, which is what they are.
  ({String mr, String en}) get label {
    final terrain = placement == 'terrain';
    switch (role) {
      case 'low':
        return terrain
            ? (mr: 'शेताचा खालचा भाग', en: 'the low part of the field')
            : (mr: 'शेताचे पहिले टोक', en: 'the first end of the field');
      case 'high':
        return terrain
            ? (mr: 'शेताचा उंच भाग', en: 'the high part of the field')
            : (mr: 'शेताचे दुसरे टोक', en: 'the far end of the field');
      case 'mid_left':
        return (mr: 'शेताच्या मधली डावी बाजू', en: 'the middle, left side');
      case 'mid_right':
        return (mr: 'शेताच्या मधली उजवी बाजू', en: 'the middle, right side');
      default:
        return (mr: 'पुढचे ठिकाण', en: 'the next point');
    }
  }
}

/// How far the walk must move between one point and the next.
///
/// The same figure the server enforces (survey.MinStationSpacingM). It is not
/// a distance to an assigned coordinate: it is the distance from where the
/// farmer actually recorded the last point. Fifteen metres is a few seconds'
/// walk on the smallest plot here, and further than a handset's own error can
/// invent while standing still.
const double minSpacingM = 15;

/// The spread asked of the smallest plots.
///
/// Of 437 mapped parcels in the first real village loaded, three are under
/// thirty metres across and the smallest is nine. Fifteen metres there is an
/// instruction to stand outside the field, which the office then refuses.
const double smallestSpacingM = 5;

/// The floor the office enforces, which the walk must never fall below.
///
/// A third of the plot's span, between the two figures above -- the same
/// arithmetic the server applies (survey.spacingFor), because a farmer must
/// never be allowed to record something the office will refuse.
double officeSpacingFor(ParcelShape shape) {
  final area = shape.areaM2;
  if (area <= 0) return minSpacingM;
  final span = math.sqrt(area) / 3;
  return math.min(math.max(span, smallestSpacingM), minSpacingM);
}

/// How much of the parcel four clips should between them enclose.
///
/// Three quarters. The clips are a sample of a field, and four of them taken
/// within fifteen metres of each other on a 1.6 ha plot sample one corner of it
/// -- which is what the office was accepting: [minSpacingM] caps the floor at
/// fifteen metres, so every parcel above about a fifth of a hectare asked for
/// the same short walk no matter how large it was.
///
/// Not a hundred per cent, and not a hard gate. A farmer cannot stand on the
/// boundary, standing water and a neighbour's crop are real, and a rule that
/// demands the whole field is a rule that strands somebody in the mud watching
/// a disabled button. Three quarters is what four well-spread points reach
/// comfortably on the parcels in the registry.
const double coverageTarget = 0.75;

/// How far a clip may be from the rest of the walk on a field with no outline.
///
/// 150 metres, which is the office's figure (UnmappedFieldRadiusM). Stated here so
/// the app refuses it standing in the field rather than after the upload: with no
/// boundary there is nothing to geofence against, and this is the only thing
/// stopping one claim being evidenced from four different fields.
///
/// It must equal the server's. A test asserts that it does, by reading the Go
/// source, because two numbers in two languages agreeing by coincidence is how this
/// feature has been broken before.
const double unmappedFieldRadiusM = 150;

/// How much of the office's own spread to require of the farmer.
///
/// The four assigned points are placed at the ends and the middle of the
/// parcel, so the distance between the closest two of them is, by construction,
/// a spread this particular field can actually support. Seven tenths of it
/// leaves room for a farmer who had to walk around something, while still
/// asking for a real traverse of the plot rather than fifteen metres.
const double stationSpreadFraction = 0.7;

/// How far this parcel requires the walk to move.
///
/// # Why the parcel's own points decide it
///
/// The arithmetic alone cannot: a third of the span is capped at fifteen metres,
/// which on anything above a fifth of a hectare stops scaling with the field. A
/// formula that scales without a cap is worse, because a 70 m by 30 m plot
/// cannot support a 37 m spread in its short direction no matter what the area
/// says, and asking for one puts the farmer outside their own boundary.
///
/// The office has already solved this. It placed four points inside this
/// polygon, at its ends and its middle, and the smallest gap between any two of
/// them is a spread this plot demonstrably supports. So that is what is asked
/// for, less a tolerance -- derived from the actual geometry rather than guessed
/// from its area.
///
/// # Why the office's own figure wins where it sent one
///
/// Because it is sized to the crop, which this cannot see. A survey number is
/// routinely shared: the same polygon carries one farmer's hectare of soybean,
/// that farmer's hectare of udad and a neighbour's five hectares, and each of
/// those is a different walk. The parcel's area answers none of them. The office
/// knows the sown extent of the claim being walked, computes the spread from it,
/// and sends it with the points before the walk begins -- so it is there with no
/// signal, and there is exactly one figure in play instead of two that have to
/// be kept in step.
///
/// [stations] empty, or carrying no figure (an older server, a draft saved before
/// this existed) falls back to what can be worked out here. Never below the
/// office's floor: below it the office refuses, and a refused clip is a farmer's
/// walk spent twice.
double spacingFor(
  ParcelShape shape, {
  List<Station> stations = const [],
  double sownHa = 0,
}) {
  // The office's figure, never below the instrument.
  //
  // Clamped rather than trusted outright. The app obeys this figure precisely so
  // that it can never permit what the office refuses, which means a wrong figure
  // is obeyed just as faithfully as a right one -- a frozen 0.5 m would have the
  // app accept four clips of the same square metre, and the office accept them
  // too, because both are reading the same wrong number.
  for (final st in stations) {
    if (st.requiredSpacingM > 0) {
      return math.max(st.requiredSpacingM, smallestSpacingM);
    }
  }

  // The office's figure, worked out here, for a walk that starts before one
  // arrived: a draft saved against a stale station cache, or a server too old to
  // send it.
  //
  // Deliberately the same arithmetic as walk_required_spacing, now at migration
  // 0056. Being stricter than the office would strand a farmer; being looser would
  // have them record what the office then refuses. Only identical is safe.
  //
  // # Why the parcel no longer floors this
  //
  // It did, and it stranded people. The floor was the old parcel rule, kept in
  // case an app too old to read the office's figure permitted something the office
  // would refuse -- but that rule is *larger* than the crop figure for a small
  // crop on a bigger parcel, so such an app demands more walking, not less, and
  // nothing it permits is ever refused. The floor could only raise the demand.
  //
  // What it did instead: a farmer with half a guntha, about seven metres across,
  // was told to walk 14.4 metres because the parcel around their share is 1875
  // square metres. Nothing inside their own plot satisfies that, so the record
  // button never came alive and the claim could not be made.
  //
  // Five metres floors it now, and that is the instrument rather than a policy: a
  // handset's fix is good to about five metres, so two points closer than that
  // cannot be shown to be different places -- which is the only thing the spacing
  // rule is for.
  if (sownHa > 0) {
    var ideal = math.sqrt(coverageTarget * sownHa * 10000);

    // Nothing may be asked for that the ground cannot hold.
    //
    // The extent is a number somebody typed, and a wrong one is unrecoverable
    // without this: half a guntha entered as 100 hectares asks for 866 m between
    // clips on a plot seven metres across, the record button never comes alive,
    // and the farmer cannot file at all. A registry figure in the wrong unit does
    // the same thing.
    //
    // Capped at the width of a square with this parcel's diagonal, because that
    // is what a four-corner arrangement of a square actually supports: corner to
    // corner is the diagonal over root two. It is deliberately not the parcel's
    // narrowest width -- an earlier version tried that and clamped a 200 m ribbon
    // to 10 m, when four points sit sixty metres apart along it without trouble.
    final across = _diameterM(shape);
    if (across > 0) ideal = math.min(ideal, across / math.sqrt2);

    return math.max(ideal, smallestSpacingM);
  }

  // No extent recorded: the parcel is all there is to go on, which is the rule
  // every claim used before the crop decided it.
  final floor = officeSpacingFor(shape);
  final supported = _closestPair(stations);
  if (supported == null) return floor;
  return math.max(floor, supported * stationSpreadFraction);
}

/// The longest way across the parcel, in metres.
///
/// Two points inside a shape cannot be further apart than this, so it is the hard
/// ceiling on anything the walk asks for. Taken over the hull's own corners, which
/// is where the extremes of a convex shape always are.
double _diameterM(ParcelShape shape) {
  if (shape.isEmpty || shape.rings.first.length < 4) return 0;
  final ring = shape.rings.first;

  final lat0 = ring.first.lat * math.pi / 180;
  const mPerLat = 111320.0;
  final mPerLon = 111320.0 * math.cos(lat0);
  final hull = _convexHull([
    for (final p in ring) (x: p.lon * mPerLon, y: p.lat * mPerLat),
  ]);

  var longest = 0.0;
  for (var i = 0; i < hull.length; i++) {
    for (var j = i + 1; j < hull.length; j++) {
      final dx = hull[i].x - hull[j].x, dy = hull[i].y - hull[j].y;
      final d = math.sqrt(dx * dx + dy * dy);
      if (d > longest) longest = d;
    }
  }
  return longest;
}

/// The distance between the two closest assigned points, or null if there are
/// fewer than two to compare.
double? _closestPair(List<Station> stations) {
  if (stations.length < 2) return null;
  double? closest;
  for (var i = 0; i < stations.length; i++) {
    for (var j = i + 1; j < stations.length; j++) {
      final d = metresBetween(
        stations[i].lat,
        stations[i].lon,
        stations[j].lat,
        stations[j].lon,
      );
      if (closest == null || d < closest) closest = d;
    }
  }
  return closest;
}

/// How much of the parcel the recorded points between them enclose.
///
/// The convex hull of where the farmer actually stood, over the parcel's area.
/// It is the honest measure of a walk: four clips in a line down one edge have
/// almost no hull and sample almost nothing, however far apart they are, and a
/// spacing rule alone cannot tell that apart from four across the field.
///
/// Used to check the walk rather than to gate it -- see [coverageTarget] -- and
/// to prove in a test that points satisfying [spacingFor] on a real parcel do
/// reach the target, which is the only way that claim means anything.
double coverageOf(List<RecordedPoint> recorded, ParcelShape shape) {
  final area = shape.areaM2;
  if (area <= 0 || recorded.length < 3) return 0;

  // The same flat-earth projection ParcelShape.areaM2 uses, so the two areas
  // are measured the same way and their ratio means something.
  final lat0 = recorded.first.lat * math.pi / 180;
  const mPerLat = 111320.0;
  final mPerLon = 111320.0 * math.cos(lat0);
  final points = [
    for (final r in recorded) (x: r.lon * mPerLon, y: r.lat * mPerLat),
  ];

  final hull = _convexHull(points);
  if (hull.length < 3) return 0;

  var twice = 0.0;
  for (var i = 0; i < hull.length; i++) {
    final a = hull[i];
    final b = hull[(i + 1) % hull.length];
    twice += a.x * b.y - b.x * a.y;
  }
  return (twice.abs() / 2) / area;
}

/// Andrew's monotone chain. Small n, so the sort dominates and that is fine.
List<({double x, double y})> _convexHull(List<({double x, double y})> points) {
  if (points.length < 3) return points;
  final sorted = [...points]
    ..sort((a, b) => a.x == b.x ? a.y.compareTo(b.y) : a.x.compareTo(b.x));

  double cross(
    ({double x, double y}) o,
    ({double x, double y}) a,
    ({double x, double y}) b,
  ) => (a.x - o.x) * (b.y - o.y) - (a.y - o.y) * (b.x - o.x);

  final lower = <({double x, double y})>[];
  for (final p in sorted) {
    while (lower.length >= 2 &&
        cross(lower[lower.length - 2], lower.last, p) <= 0) {
      lower.removeLast();
    }
    lower.add(p);
  }
  final upper = <({double x, double y})>[];
  for (final p in sorted.reversed) {
    while (upper.length >= 2 &&
        cross(upper[upper.length - 2], upper.last, p) <= 0) {
      upper.removeLast();
    }
    upper.add(p);
  }
  return [...lower..removeLast(), ...upper..removeLast()];
}

/// How many clips a walk wants where the office placed no points.
///
/// Four, the same as a placed walk: the evidence is four views of one field
/// from four parts of it, and that does not change because nobody has traced
/// the outline yet.
const int freeWalkPoints = 4;

// The compass is gone from here, with the bearing arithmetic and the two
// distances that decided when a suggested point was worth naming.
//
// It existed to say "for the first end of the field, walk 45 metres north-west"
// alongside "you can record here" -- two instructions that argue with each other,
// and a farmer follows the one that sounds like work. Nobody standing in a field
// describes where to go by compass point either.
//
// Nothing replaced it with left and right. That needs to know which way somebody
// is facing, and this handset cannot say: heading is course-over-ground, so it
// means nothing until they are already walking, and there is no magnetometer
// reading here to fall back on. A confident "turn right" that is ninety degrees
// wrong sends a farmer across their own field.

/// Rounds a distance to something worth saying out loud.
///
/// Nobody walks 37.4 metres. Past ten metres the number is rounded to five, so
/// the spoken instruction stops changing on every GPS twitch -- a countdown
/// that recites a different number every second is noise, and a farmer stops
/// listening to it well before they reach the station.
int spokenDistance(double metres) {
  if (metres < 10) return metres.round();
  return (metres / 5).round() * 5;
}

/// One point of the walk, as it was actually recorded.
typedef RecordedPoint = ({int stationNo, double lat, double lon});

/// A point on this survey number that some other claim already recorded.
///
/// # Why the walk knows about these at all
///
/// So it can refuse before the farmer records, not after they have walked home.
/// A survey number is routinely shared -- a farmer's soybean and udad are two
/// claims on it, and a neighbour's holding is a third -- and ground filmed for one
/// is not independent evidence for another. The office refuses such a clip when it
/// arrives, which is a walk already spent: three clips of one trial claim were
/// refused on every pass, and nothing the farmer could do from the queue would
/// ever have changed that.
///
/// Fetched with the four points, before the walk, so it holds with no signal. A
/// stale set can only be incomplete, never wrong, and the office still catches
/// what this misses.
typedef TakenPoint = ({double lat, double lon, bool sameFarmer});

/// Whether the farmer may record here, and what to say.
///
/// # What is required, and what is not
///
/// Required: being inside the field, and being somewhere the walk has not
/// already been. Not required: standing on the assigned coordinate. The points
/// are where the walk suggests going -- the ends and the middle of the parcel,
/// so the clips are spread across it -- and a farmer who cannot reach one, for
/// water or a bund or a neighbour's crop, has walked perfectly well by
/// recording elsewhere in their own field.
///
/// This used to hold the record button until the handset was within twelve
/// metres of a surveyed point. In a wet field, under trees, or on a phone with
/// a poor fix, that is a farmer standing in the mud watching a disabled
/// button.
class WalkStatus {
  const WalkStatus({
    required this.canRecord,
    required this.mr,
    required this.en,
    this.outsideByM,
    this.nearestOtherM,
    this.shortfallM,
    this.taken,
  });

  /// Whether the record button is live.
  final bool canRecord;

  final String mr;
  final String en;

  /// How far outside the parcel the farmer is, when they are.
  final double? outsideByM;

  /// How far the nearest other recorded point is, when that is what is
  /// holding the button.
  final double? nearestOtherM;

  /// How much further the farmer must walk before they may record, when that is
  /// what is holding the button.
  ///
  /// Kept as a number rather than left inside the sentence so the walk can say it
  /// on its own cadence. A farmer walking generates a position event every metre,
  /// and re-speaking the whole instruction on each one talks over itself; the
  /// screen announces the remaining distance on a timer instead.
  final double? shortfallM;

  /// Set when the button is held because another claim on this survey number
  /// already filmed this ground, rather than because this walk has.
  ///
  /// Kept apart because the remedy differs: not "walk further within this claim"
  /// but "this is not your share of the survey number, go to yours". Telling a
  /// farmer to walk on would be wrong advice -- the ground is somebody else's
  /// evidence however far along it they go.
  final TakenPoint? taken;
}

/// The walk's rule, evaluated where the farmer is standing.
///
/// [station] is the point being recorded now; clips of that same point are
/// ignored when measuring the spread, because re-recording a clip the phone
/// lost happens where the first attempt was made and must not be refused.
WalkStatus walkStatus({
  required Fix fix,
  required ParcelShape shape,
  required int station,
  required List<RecordedPoint> recorded,
  List<Station> stations = const [],
  double sownHa = 0,
  List<TakenPoint> takenPoints = const [],
}) {
  // A faked position first, because it is the one refusal with no way back.
  //
  // The office refuses a clip taken on a mocked location and does not offer it to
  // the camera again -- it is not a "record again" refusal, it is a spent walk. A
  // farmer with a developer setting left on from something else would film four
  // clips, upload them, and lose the claim's evidence with no route to redo it.
  //
  // Checked here rather than silently, because the remedy is on the phone and the
  // farmer can act on it: turn the other app off. Android reports this per fix and
  // the walk already stores it on every capture, so the app has always known.
  if (fix.isMocked == true) {
    return const WalkStatus(
      canRecord: false,
      mr:
          'फोनवर खोटे लोकेशन देणारे अ‍ॅप सुरू आहे. ते बंद करा, मग व्हिडिओ '
          'काढा.',
      en:
          'An app on this phone is supplying a false location. Turn it off, '
          'then record.',
    );
  }

  // In the field. The office refuses a clip from outside the boundary
  // outright, so recording one here would cost the farmer the walk twice.
  if (!shape.isEmpty) {
    final at = locate(fix: fix, shape: shape);
    if (!at.atParcel) {
      final say = spokenDistance(at.distanceM);
      return WalkStatus(
        canRecord: false,
        outsideByM: at.distanceM,
        mr: 'तुम्ही शेताच्या बाहेर आहात. $say मीटर आत जा, मग व्हिडिओ काढा.',
        en: 'You are outside your field. Go $say metres in, then record.',
      );
    }
  }

  final spacing = spacingFor(shape, stations: stations, sownHa: sownHa);

  double? nearest;
  for (final r in recorded) {
    if (r.stationNo == station) continue;
    final d = metresBetween(fix.lat, fix.lon, r.lat, r.lon);
    if (nearest == null || d < nearest) nearest = d;
  }

  // Ground another claim on this survey number has already filmed.
  //
  // Checked before the walk's own spacing, because it is the stronger statement:
  // "walk another 20 metres" is wrong advice when the ground belongs to somebody
  // else's claim, and a farmer who follows it arrives somewhere just as refused.
  TakenPoint? takenNearby;
  var takenDistance = double.infinity;
  for (final t in takenPoints) {
    final d = metresBetween(fix.lat, fix.lon, t.lat, t.lon);
    if (d < spacing && d < takenDistance) {
      takenDistance = d;
      takenNearby = t;
    }
  }
  if (takenNearby != null) {
    return WalkStatus(
      canRecord: false,
      taken: takenNearby,
      mr: takenNearby.sameFarmer
          ? 'हा भाग तुमच्या दुसऱ्या पिकाच्या नोंदीत आधीच चित्रित झाला आहे. '
                'या पिकाच्या वाटणीत जाऊन व्हिडिओ काढा.'
          : 'तुमच्या सर्व्हे नंबरमध्ये या ठिकाणी दुसऱ्या शेतकऱ्याने आधीच '
                'चित्रीकरण केले आहे. तुमच्या वाटणीत जा.',
      en: takenNearby.sameFarmer
          ? 'Your claim for the other crop already recorded this ground. '
                'Record on the share of the field this crop is on.'
          : 'Another farmer in your survey number has already recorded here. '
                'Go to your own share of the field.',
    );
  }

  // Too far from the rest of the walk, on a field with no recorded outline.
  //
  // With no boundary there is nothing to fence against, so the office fences the
  // claim to one place instead: every clip within 150 metres of the first. It is
  // the only thing stopping a claim with no polygon being evidenced from four
  // different fields, and the app was not checking it at all -- so a farmer whose
  // parcel has no boundary could walk out of range, film, and be refused.
  //
  // Only where there is no outline, matching the office exactly: with a boundary
  // the geofence above is the stronger fence and this does not apply.
  if (shape.isEmpty && nearest != null && nearest > unmappedFieldRadiusM) {
    final say = spokenDistance(nearest - unmappedFieldRadiusM);
    return WalkStatus(
      canRecord: false,
      nearestOtherM: nearest,
      mr:
          'हे ठिकाण आधीच्या चित्रीकरणापासून फार दूर आहे. $say मीटर मागे '
          'येऊन व्हिडिओ काढा.',
      en:
          'This spot is too far from the rest of the walk. Come back $say '
          'metres, then record.',
    );
  }

  if (nearest != null && nearest < spacing) {
    final say = spokenDistance(spacing - nearest);
    return WalkStatus(
      canRecord: false,
      nearestOtherM: nearest,
      shortfallM: spacing - nearest,
      mr:
          'हा भाग तुम्ही आधीच चित्रित केला आहे. शेतात दुसरीकडे '
          'किमान $say मीटर चालत जा.',
      en:
          'You have already recorded this spot. Walk at least $say metres to '
          'another part of your field.',
    );
  }

  // Free to record, and that is the whole of it.
  //
  // # Why the assigned point is no longer named
  //
  // It used to be: "you can record here. For the first end of the field, walk 45
  // metres north-west." Two instructions that argue with each other. If the
  // recording is acceptable where the farmer is standing then walking is not
  // being asked of them -- and told both, a farmer does the one that sounds like
  // an instruction, which is the walk. So the app sent people across their own
  // field for no reason, having just told them they need not go.
  //
  // The points were never a fence (see the note on this function) and the report
  // does not claim the farmer stood on them. What makes four clips a sample of a
  // field is the distance between them, which is enforced above and sized to the
  // crop's own extent -- so naming the points aloud bought nothing.
  //
  // It also takes the compass out of a farmer's instructions. "वायव्येकडे" is not
  // how anybody standing in a field describes where to go, and a bearing from a
  // handset is not reliable enough to be worth translating into anything better:
  // course-over-ground means nothing until somebody is already moving, which is
  // exactly when they are not listening for where to start.
  return const WalkStatus(
    canRecord: true,
    mr: 'इथे थांबा आणि व्हिडिओ काढा.',
    en: 'Stop here and record the video.',
  );
}
