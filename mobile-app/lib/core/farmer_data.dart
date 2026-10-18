import 'dart:async';

import 'package:flutter/foundation.dart';

import 'api.dart';
import 'db.dart';

/// What the farmer's screen needs, read from the phone first.
///
/// # Why cache-first and not network-first with a cache
///
/// The two sound alike and behave completely differently. Network-first shows
/// a spinner, waits for a request that will not arrive, and eventually falls
/// back -- so a farmer with no signal watches a loading state for thirty
/// seconds before seeing anything. Cache-first paints what the phone already
/// knows immediately and quietly replaces it if a fresher answer arrives.
///
/// A farmer standing in a field is the normal case, not the degraded one, and
/// the app should not behave as though something has gone wrong.
///
/// # Why the age is carried
///
/// A farmer looking at a list of open events with no signal is entitled to
/// know it is yesterday's list. Showing stale data silently is how somebody
/// files against an event that closed on Tuesday.
class FarmerData {
  FarmerData({required this.store, required this.api});

  final Store store;
  final Api api;

  static const _kEvents = 'events';

  /// One key per farmer, not one per handset.
  ///
  /// A single signed-in number may act for several farmers, and a shared key
  /// meant the second farmer's record overwrote the first -- so switching
  /// between them showed whichever had been fetched last, and offline showed
  /// the wrong person's land.
  static String recordKey(String farmerId) => 'farmer_record:$farmerId';
  static String reportsKey(String farmerId) => 'reports:$farmerId';

  /// Survey events a farmer may file against.
  ///
  /// [villages] are the farmer's parcel villages. The office answers with the
  /// events that cover at least one of them, by the same rule a filing is
  /// refused by. Without them it sends every open event in the state, and the
  /// district filter this app applied on top could not see the talukas and
  /// circles an event actually covers -- so on the first real run it offered
  /// an event that did not cover the farmer's village, and the claim was
  /// gathered in full before being refused: "This plot is outside the survey
  /// area."
  ///
  /// Cached per set of villages, so switching to another farmer on the same
  /// handset does not show them the previous one's events offline.
  Future<Cached<List<dynamic>>> events({
    bool refresh = true,
    List<String> villages = const [],
  }) {
    final codes = {...villages.where((v) => v.isNotEmpty)}.toList()..sort();
    final query = codes
        .map((v) => '&village=${Uri.encodeQueryComponent(v)}')
        .join();
    return _read(
      codes.isEmpty ? _kEvents : '$_kEvents:${codes.join(',')}',
      '/api/v1/events?state=open&limit=50$query',
      extract: (body) => body['items'] as List<dynamic>? ?? const [],
      refresh: refresh,
    );
  }

  /// The villages a farmer's parcels are in, from their record.
  ///
  /// Falls back to the farmer's own village for a record cached before
  /// parcels carried theirs, so a handset that has not refreshed still filters
  /// rather than being offered every event in the state.
  static List<String> villagesOf(Map<String, dynamic>? record) {
    final fromParcels = [
      for (final p in (record?['parcels'] as List<dynamic>? ?? const []))
        if (p is Map && '${p['village_lgd_code'] ?? ''}'.isNotEmpty)
          '${p['village_lgd_code']}',
    ];
    if (fromParcels.isNotEmpty) return fromParcels;
    final own = '${record?['village_lgd_code'] ?? ''}';
    return own.isEmpty ? const [] : [own];
  }

  /// The registry's record of this farmer: their land, their crops.
  ///
  /// The farmer-facing route, not `/api/v1/farmers/{id}`. That one is the
  /// officer listing and is guarded by a permission no farmer holds, so every
  /// refresh from this app answered 403 and quietly fell back to the cache --
  /// a record that could be fetched once and then never updated again.
  ///
  /// [grant] is the proof this account answered for this farmer, for the ones
  /// whose registered number is not the one that signed in. Without it the
  /// server allows only the record belonging to the verified number.
  Future<Cached<Map<String, dynamic>?>> record(
    String farmerId, {
    bool refresh = true,
    String? grant,
  }) => _read<Map<String, dynamic>?>(
    recordKey(farmerId),
    '/api/v1/me/registry/${Uri.encodeComponent(farmerId)}',
    extract: (body) => body,
    refresh: refresh,
    grant: grant,
    forFarmer: farmerId,
  );

  /// Reports the office has for one farmer, whatever their state.
  ///
  /// The grant is not optional in practice. This route names a farmer, and
  /// the office answers for a farmer this account has confirmed -- which for
  /// an account acting on somebody else's behalf is only ever proved by the
  /// grant. Without it the answer is 403 and the farmer's history is empty:
  /// a claim filed, acknowledged and assessed, and a My reports screen that
  /// says nothing was ever sent.
  Future<Cached<List<dynamic>>> reports(
    String farmerId, {
    bool refresh = true,
    String? grant,
  }) => _read(
    reportsKey(farmerId),
    '/api/v1/reports?limit=100&farmer_id=${Uri.encodeComponent(farmerId)}',
    extract: (body) => body['items'] as List<dynamic>? ?? const [],
    refresh: refresh,
    grant: grant,
    forFarmer: farmerId,
  );

  /// Where one claim has got to, and what it is waiting for.
  ///
  /// Cached per claim like everything else, because this is the screen a
  /// farmer opens weeks after filing to find out whether anybody is doing
  /// anything -- often from the field, with no signal. A progress screen that
  /// is blank offline answers the question with silence, which is the answer
  /// they were already afraid of.
  ///
  /// The office sends keys rather than sentences, so what is cached survives
  /// the app being translated and renders in Marathi with nothing reachable.
  Future<Cached<Map<String, dynamic>?>> progress(
    String reportId, {
    bool refresh = true,
    String? grant,
  }) => _read<Map<String, dynamic>?>(
    progressKey(reportId),
    '/api/v1/reports/${Uri.encodeComponent(reportId)}/progress',
    extract: (body) => body,
    refresh: refresh,
    grant: grant,
  );

  static String progressKey(String reportId) => 'progress:$reportId';

  /// The assessment report of a decided claim, and a copy kept on the phone.
  ///
  /// Kept because a farmer takes it to an office, or opens it at an appeal,
  /// where there may be no signal. The office renders it; the phone stores the
  /// page exactly as sent, so the copy shown offline is the copy the office
  /// issued. Cleared with the rest of a farmer's cache on sign-out.
  Future<AssessmentReport> assessmentReport(
    String reportId, {
    String? grant,
  }) async {
    final key = assessmentReportKey(reportId);
    final cached = await store.get<String>(key);
    final cachedAt = await store.cachedAt(key);

    final res = await api.get(
      '/api/v1/reports/${Uri.encodeComponent(reportId)}/assessment-report',
      farmerGrant: grant,
    );
    final page = res.document;
    if (res.ok && page != null) {
      await store.put(key, page);
      return AssessmentReport(page, DateTime.now(), fresh: true);
    }
    if (res.status == 409) {
      return const AssessmentReport(null, null, fresh: false, notYet: true);
    }
    return AssessmentReport(
      cached,
      cachedAt,
      fresh: false,
      offline: res.offline,
    );
  }

  static String assessmentReportKey(String reportId) =>
      'assessment_report:$reportId';

  /// Name a farmer and answer for them, which is how one is added.
  ///
  /// Returns the record and the grant to keep beside it, or null when the
  /// office does not recognise the pair. The date of birth is sent in the
  /// body and is never stored on this phone: what is kept is the grant the
  /// answer earned.
  Future<LookupResult> lookup(String farmerId, String dob) async {
    final res = await api.postJson('/api/v1/me/registry/lookup', {
      'farmer_id': farmerId,
      'dob': dob,
    });

    // Offline is not a wrong answer, and must not be told as one. A farmer
    // sent to check a document that was never the problem will check it,
    // twice, and then give up -- in a field, where the network was the whole
    // difficulty.
    if (res.offline) return const LookupResult.offline();

    final body = res.body;
    if (!res.ok || body == null) return const LookupResult.refused();

    final grant = '${body['farmer_grant'] ?? ''}';
    if (grant.isEmpty) return const LookupResult.refused();

    await store.put(recordKey(farmerId), body);
    return LookupResult(
      found: (
        record: body,
        grant: grant,
        expires: DateTime.tryParse('${body['grant_expires_at'] ?? ''}'),
      ),
    );
  }

  /// The four points the walk sends the farmer to, for one parcel.
  ///
  /// # Why these are cached at all
  ///
  /// The points are placed by PostGIS from the parcel boundary, so they come
  /// from the server -- and they were fetched at the moment the farmer picked
  /// the parcel, on the form screen, standing in the field. No signal there
  /// meant no points, and the claim fell back to the older photograph
  /// sequence without anybody being told that the heaviest evidence in the
  /// specification had been skipped.
  ///
  /// A dead zone is the normal case for this app, not the degraded one. So
  /// the points are fetched whenever there is a signal -- by [prefetch],
  /// which runs when the farmer's record refreshes -- and read from the phone
  /// when there is not.
  ///
  /// # Why a stale set is still the right answer
  ///
  /// Placement is deterministic given a boundary and an aspect, so a cached
  /// set only goes wrong if the boundary itself is corrected. When that
  /// happens the next refresh replaces it, and the server freezes its own
  /// placement against the report on the first capture rather than trusting
  /// what the handset walked (see survey.RecordDirection) -- so a stale set
  /// can send a farmer to slightly the wrong spot, but cannot put wrong
  /// points into the record.
  /// [forFarmer] is whose parcel this is, which is not always whoever signed
  /// in. A handset acting for a second farmer holds a grant, and without it
  /// the office scopes the lookup to the account's own land and answers "that
  /// plot is not on your record" -- a 404 the form swallows, leaving a farmer
  /// tapping a button that does nothing.
  /// [sownHa] is the extent of the crop about to be walked, and it is what
  /// decides how far apart the office requires the clips to be.
  ///
  /// It has to be part of the key as well as the request. A survey number is
  /// routinely shared: the same parcel carries a farmer's hectare of soybean,
  /// their hectare of udad and a neighbour's five hectares, and those are three
  /// different walks. Cached under the parcel alone, the first claim's figure
  /// would have been served to all three -- so the 5 ha holding would have been
  /// walked to the 1 ha figure, which is the fault this was meant to fix.
  ///
  /// Zero is a real value and keys separately: it is what [prefetch] stores,
  /// before any crop is known.
  Future<Cached<List<dynamic>>> stations(
    String farmId, {
    bool refresh = true,
    String? forFarmer,
    double sownHa = 0,
  }) => _read(
    stationsKey(farmId, sownHa: sownHa),
    '/api/v1/parcels/${Uri.encodeComponent(farmId)}/stations'
    '${sownHa > 0 ? '?sown_ha=$sownHa' : ''}',
    extract: (body) => body['stations'] as List<dynamic>? ?? const [],
    refresh: refresh,
    forFarmer: forFarmer,
  );

  /// The whole sampling plan for one parcel: the points, and the points other
  /// claims on the survey number have already recorded.
  ///
  /// A second method rather than widening [stations], because that one's cached
  /// value is a list and this one's is the whole answer. Changing its shape would
  /// have every entry already on a phone read back as the wrong type -- in a dead
  /// zone, which is where this is read.
  Future<Cached<Map<String, Object?>>> stationPlan(
    String farmId, {
    bool refresh = true,
    String? forFarmer,
    double sownHa = 0,
  }) => _read(
    'plan:${stationsKey(farmId, sownHa: sownHa)}',
    '/api/v1/parcels/${Uri.encodeComponent(farmId)}/stations'
        '${sownHa > 0 ? '?sown_ha=$sownHa' : ''}',
    extract: (body) => body,
    refresh: refresh,
    forFarmer: forFarmer,
  );

  /// The cache key for one parcel's points.
  ///
  /// The extent is rounded to two decimals -- a hundredth of a hectare, which is
  /// a hundred square metres -- so that a farmer who retypes 1.0 as 1.001 reads
  /// the set already on the phone instead of finding nothing there and losing the
  /// walk to a dead zone.
  static String stationsKey(String farmId, {double sownHa = 0}) => sownHa > 0
      ? 'stations:$farmId:${sownHa.toStringAsFixed(2)}'
      : 'stations:$farmId';

  /// Pull the points for every parcel the farmer has, while there is a signal.
  ///
  /// Best effort and deliberately quiet: this runs so that the walk works
  /// later, and nothing on screen depends on it finishing. Sequential rather
  /// than at once, because a handful of parcels on a village connection is a
  /// queue, not a burst.
  Future<void> prefetch(List<dynamic> parcels, {String? forFarmer}) async {
    for (final p in parcels) {
      final farmId = (p is Map) ? '${p['farm_id'] ?? ''}' : '';
      if (farmId.isEmpty) continue;
      await stations(farmId, forFarmer: forFarmer);
    }
  }

  /// Read the cache, then try the network and update it.
  ///
  /// A failed refresh is not an error the caller has to handle: what is on the
  /// phone stands, and the age says how far behind it may be.
  /// [forFarmer], where given, is the farmer the route names. Its grant is
  /// looked up here when the caller did not pass one.
  ///
  /// Resolved in one place on purpose. The grant is what lets the office
  /// answer for a farmer this account acts for rather than is, and it used to
  /// be the caller's job to remember it: record and progress did, the reports
  /// listing did not, and so a farmer added by id and date of birth had a
  /// history that was silently empty -- a claim filed, acknowledged and
  /// assessed, and a screen saying nothing had ever been sent. A call site
  /// that forgets should not be able to cause that again.
  Future<Cached<T>> _read<T>(
    String key,
    String path, {
    required T Function(Map<String, Object?> body) extract,
    required bool refresh,
    String? grant,
    String? forFarmer,
  }) async {
    final cached = await store.get<T>(key);
    final cachedAt = await store.cachedAt(key);

    if (!refresh) {
      return Cached(cached, cachedAt, fresh: false);
    }

    if (grant == null && forFarmer != null && forFarmer.isNotEmpty) {
      grant = await store.grantFor(forFarmer);
    }

    final res = await api.get(path, farmerGrant: grant);
    if (!res.ok || res.body == null) {
      // Offline, or the server declined. Either way the phone's copy is the
      // best answer available and pretending otherwise helps nobody.
      if (!res.offline) {
        debugPrint('refresh of $key failed: ${res.status} ${res.message}');
      }
      return Cached(cached, cachedAt, fresh: false);
    }

    try {
      final value = extract(res.body!);
      await store.put(key, value);
      return Cached(value, DateTime.now(), fresh: true);
    } catch (e) {
      // A shape this app does not understand. Keeping the last good copy is
      // far better than blanking a farmer's screen because a field was
      // renamed upstream.
      debugPrint('could not read $key: $e');
      return Cached(cached, cachedAt, fresh: false);
    }
  }
}

/// An assessment report, as far as it could be had.
class AssessmentReport {
  const AssessmentReport(
    this.html,
    this.at, {
    required this.fresh,
    this.notYet = false,
    this.offline = false,
  });

  /// The page, or null when there is none on the phone or from the office.
  final String? html;
  final DateTime? at;
  final bool fresh;

  /// The claim is not decided yet, so there is no report to give.
  final bool notYet;

  /// Nothing was reached; [html] is the phone's copy if it has one.
  final bool offline;
}

/// A value and how old it is.
class Cached<T> {
  const Cached(this.value, this.at, {required this.fresh});

  final T? value;
  final DateTime? at;

  /// Whether this came from the network on this read.
  final bool fresh;

  bool get isEmpty => value == null;

  /// How stale this is, in words a farmer can weigh.
  ///
  /// Deliberately coarse. "Two hours ago" is actionable; "127 minutes ago" is
  /// a number somebody has to do arithmetic on while standing in a field.
  String? get age {
    final at = this.at;
    if (at == null || fresh) return null;

    final elapsed = DateTime.now().difference(at);
    if (elapsed.inMinutes < 5) return null;
    if (elapsed.inHours < 1) {
      return 'काही वेळापूर्वीची माहिती · a few minutes old';
    }
    if (elapsed.inHours < 24) {
      return '${elapsed.inHours} तासांपूर्वीची माहिती · ${elapsed.inHours}h old';
    }
    final days = elapsed.inDays;
    return '$days दिवसांपूर्वीची माहिती · $days day${days > 1 ? "s" : ""} old';
  }
}

/// What came of naming a farmer and answering for them.
///
/// Three outcomes, not two. "Not found" and "could not ask" look identical in
/// a boolean and are completely different to the person holding the phone:
/// one means fetch the 7/12 extract, the other means walk up the hill.
class LookupResult {
  const LookupResult({this.found}) : offline = false;
  const LookupResult.offline() : found = null, offline = true;
  const LookupResult.refused() : found = null, offline = false;

  /// The record and the grant to keep beside it, when the office recognised
  /// the pair.
  final ({Map<String, dynamic> record, String grant, DateTime? expires})? found;

  /// Nothing was reached. Says nothing about whether the details were right.
  final bool offline;
}

/// The events this farmer could actually claim against.
///
/// # Why the app filters at all
///
/// A farmer holds no jurisdiction, so the office's event listing is not
/// narrowed for them: they are sent every open event in the state. That is
/// not a cosmetic problem. The home screen says a calamity has been notified
/// "for your area", the form takes an event without asking which, and a claim
/// filed against another district's event is filled in, walked, photographed
/// and only then refused -- for a village that event never covered.
///
/// # Why here and not at the office
///
/// This has to work with no signal. The farmer's record and the event list
/// are both already on the phone, and matching a district code needs neither
/// a request nor a round trip.
///
/// # Why a district and not the exact coverage
///
/// An event covers particular talukas and circles within its district, which
/// this cannot see. Matching the district removes the gross error -- a
/// Nagpur hailstorm offered to a farmer in Solapur -- and leaves the office
/// as the authority on the rest, which it has to be anyway.
///
/// A farmer whose record has not arrived yet is shown everything rather than
/// nothing: an empty screen would read as "no calamity has been declared",
/// which is a different and worse falsehood.
List<dynamic> eventsForFarmer(
  List<dynamic> events,
  Map<String, dynamic>? record,
) {
  final district = '${record?['district_lgd_code'] ?? ''}';
  if (district.isEmpty) return events;
  return [
    for (final e in events)
      if (e is Map && '${e['district_code'] ?? ''}' == district) e,
  ];
}
