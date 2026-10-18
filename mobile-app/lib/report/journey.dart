import 'dart:async';
import 'dart:convert';

import 'package:flutter/material.dart';

import '../capture/field_gate.dart';
import '../capture/station_guide.dart';
import '../capture/station_walk_screen.dart';
import '../onboarding/access_gate.dart';
import '../core/db.dart';
import '../core/parcel_location.dart';
import '../core/background.dart';
import '../core/sensors.dart';
import '../core/sync.dart';
import '../core/voice.dart';
import '../live/mode_gate.dart';

/// A claim in progress, as the `journeys` table holds it.
///
/// # Why this exists
///
/// A claim is made in a field, over several screens, on a phone that can be
/// killed, dropped or run flat at any point. Before this, the typed form lived
/// only in a widget until "File" was pressed, and an interrupted walk was
/// resumed into the photograph sequence -- so the directed evidence the walk
/// had gathered was quietly abandoned. The row is written as the farmer goes,
/// and resuming reads it back to the exact step.
class Journey {
  const Journey({
    required this.id,
    required this.stage,
    required this.farmId,
    required this.eventId,
    required this.surveyNumber,
    this.farmerId = '',
    this.form = const {},
    this.event,
    this.parcel,
    this.stations,
    this.evidenceMode,
    this.takenPoints = const [],
  });

  factory Journey.fromRow(Map<String, Object?> row) {
    Object? decode(Object? v) {
      if (v is! String || v.isEmpty) return null;
      try {
        return jsonDecode(v);
      } catch (_) {
        return null;
      }
    }

    final stationsJson = decode(row['stations']);
    return Journey(
      id: row['local_id'] as String,
      stage: '${row['stage'] ?? 'form'}',
      farmId: '${row['farm_id'] ?? ''}',
      eventId: '${row['event_id'] ?? ''}',
      surveyNumber: '${row['survey_number'] ?? ''}',
      farmerId: '${row['farmer_id'] ?? ''}',
      form: (decode(row['form']) as Map?)?.cast<String, Object?>() ?? const {},
      event: (decode(row['event']) as Map?)?.cast<String, dynamic>(),
      parcel: (decode(row['parcel']) as Map?)?.cast<String, dynamic>(),
      stations: stationsJson is List && stationsJson.isNotEmpty
          ? [
              for (final s in stationsJson)
                Station.fromJson((s as Map).cast<String, Object?>()),
            ]
          : null,
      evidenceMode: row['evidence_mode'] as String?,
      takenPoints: [
        for (final t in (decode(row['taken_points']) as List? ?? const []))
          if (t is Map && t['lat'] is num && t['lon'] is num)
            (
              lat: (t['lat'] as num).toDouble(),
              lon: (t['lon'] as num).toDouble(),
              sameFarmer: t['same_farmer'] == true,
            ),
      ],
    );
  }

  final String id;

  /// form, evidence or done.
  final String stage;
  final String farmId;
  final String eventId;
  final String surveyNumber;

  /// Who the claim is for.
  ///
  /// Carried so the assistant can be governed per farmer -- their consent, their
  /// daily allowance -- on a handset that is shared between several. It is not passed
  /// to the model; see liveConsentTerms.
  final String farmerId;
  final Map<String, Object?> form;
  final Map<String, dynamic>? event;
  final Map<String, dynamic>? parcel;

  /// The parcel's outline, as the registry or the farmer's own walk gave it.
  /// Carried into the walk so a point the farmer cannot reach is not moved
  /// outside their own field.
  ParcelShape get shape => ParcelShape.fromGeoJson(parcel?['plot_geometry']);
  final List<Station>? stations;
  final String? evidenceMode;

  /// Ground other claims on this survey number have already filmed.
  ///
  /// A survey number is routinely shared -- this farmer's soybean and udad are two
  /// claims on it, and a neighbour's holding is a third -- and none of them may
  /// count another's ground as its own evidence. Carried so the walk refuses it
  /// where the farmer is standing, rather than the office refusing it a day later
  /// when the clip arrives and the walk has already been spent.
  final List<TakenPoint> takenPoints;

  /// Whether the evidence is the directed walk. A journey from before the
  /// mode was recorded is a walk exactly when it carries points.
  /// Whether the form can be shown again with no network: it needs the event
  /// and the parcel it was being filled against.
  bool get formResumable => event != null && parcel != null;

  /// Whether the evidence is gathered and the claim is waiting to be sent.
  bool get atReview => stage == 'review';

  /// The extent of the crop being claimed, in hectares.
  ///
  /// What sizes the walk. The parcel cannot answer it: a survey number is
  /// routinely shared between a farmer's own crops and between farmers, so the
  /// same polygon carries a hectare of soybean, a hectare of udad and a
  /// neighbour's five hectares, and those are three walks of different sizes.
  ///
  /// Read from the form, which is where the farmer typed it. Zero where the form
  /// did not record it -- a draft from an older build -- and [spacingFor] then
  /// falls back to the parcel rule rather than inventing an extent.
  double get sownHa => double.tryParse('${form['sown'] ?? ''}') ?? 0;

  /// The crop the farmer typed, for the assistant to advise about.
  String? get crop {
    final typed = '${form['crop'] ?? ''}'.trim();
    return typed.isEmpty ? null : typed;
  }

  /// Which calamity this claim is against, from the event the office published.
  ///
  /// Read from the event rather than from the form: the farmer does not choose it,
  /// and an assistant told the wrong calamity would advise on the wrong damage --
  /// green canopy with empty pods means drought, and means something else entirely
  /// after hail.
  String? get calamity {
    for (final key in ['calamity', 'calamity_type', 'type']) {
      final value = '${event?[key] ?? ''}'.trim();
      if (value.isNotEmpty) return value;
    }
    return null;
  }

  String? get season {
    final value = '${event?['season'] ?? ''}'.trim();
    return value.isEmpty ? null : value;
  }

  // There is deliberately no district here.
  //
  // A district is worth the assistant knowing -- it is the unit an agronomic answer
  // turns on, a sowing window and a variety and the rain that did or did not come,
  // and it is coarse enough to identify nobody. But the handset does not know it. All
  // it holds is an LGD code, and a numeric code is not a place to a model that reads
  // words; turning one into a name here would mean the app inventing a fact about
  // where a farm is, which is the one thing this app is never allowed to do.
  //
  // So the platform answers it. It has already authenticated the farmer and already
  // looked up whose land this is, and it holds the reference data that turns a code
  // into a name -- see liveURL in the Go service.
}

/// Stations encoded the way a journey stores them.
List<Map<String, Object?>> encodeTakenPoints(List<TakenPoint> points) => [
  for (final p in points)
    {'lat': p.lat, 'lon': p.lon, 'same_farmer': p.sameFarmer},
];

/// Taken points as the office sends them.
List<TakenPoint> decodeTakenPoints(Object? body) => [
  for (final t in (body as List? ?? const []))
    if (t is Map && t['lat'] is num && t['lon'] is num)
      (
        lat: (t['lat'] as num).toDouble(),
        lon: (t['lon'] as num).toDouble(),
        sameFarmer: t['same_farmer'] == true,
      ),
];

List<Map<String, Object?>> encodeStations(List<Station> stations) => [
  for (final st in stations)
    {
      'station_no': st.stationNo,
      'lat': st.lat,
      'lon': st.lon,
      'role': st.role,
      'placement': st.placement,
      // The office's own spacing figure, which this used to drop.
      //
      // Station.fromJson reads it and spacingFor prefers it over any arithmetic
      // of its own -- the whole point being that one rule is decided in one place
      // and obeyed rather than recomputed. But every route into the walk goes
      // through a journeys row, and this is what writes one, so the field arrived
      // as zero every time and the app always fell back to working it out again.
      //
      // Two implementations of one rule do not stay equal. They differed in the
      // sanity ceiling: the app measures the first ring of its own cached
      // geometry on a flat-earth projection and the office measures the stored
      // polygon in UTM, so wherever the app's figure came out smaller it let a
      // farmer record a clip the office then refused -- and the farmer walked the
      // field again to replace a clip that was never wrong.
      'required_spacing_m': st.requiredSpacingM,
    },
];

/// How far a journey's evidence has got.
class EvidenceProgress {
  const EvidenceProgress({required this.done, required this.total});

  final int done;
  final int total;

  bool get complete => total > 0 && done >= total;
}

/// The first point of the walk still without its clip, or the length of the
/// list when every point is done.
/// Where a free walk carries on from after the app was killed.
///
/// The clips are numbered in the order they were taken, so the next one is
/// the first number with nothing against it.
int firstFreePointToCapture(Set<int> done) {
  for (var n = 1; n <= freeWalkPoints; n++) {
    if (!done.contains(n)) return n - 1;
  }
  return freeWalkPoints;
}

int firstStationToCapture(List<Station> stations, Set<int> withVideo) {
  for (var i = 0; i < stations.length; i++) {
    if (!withVideo.contains(stations[i].stationNo)) return i;
  }
  return stations.length;
}

/// Count what is on the phone for this journey.
Future<EvidenceProgress> evidenceProgress(Store store, Journey j) async {
  final stations = j.stations;
  final done = await store.stationsComplete(j.id);
  if (stations == null || stations.isEmpty) {
    // A field with no boundary: four points of the farmer's own, numbered
    // in the order they were taken.
    return EvidenceProgress(
      done: done.where((n) => n >= 1 && n <= freeWalkPoints).length,
      total: freeWalkPoints,
    );
  }
  return EvidenceProgress(
    done: stations.where((s) => done.contains(s.stationNo)).length,
    total: stations.length,
  );
}

/// The screen that continues a journey's evidence where it stopped.
///
/// [onFinished] runs when the last piece is captured. The journey moves to
/// `review` rather than `done`: the claim is not sent by finishing the walk,
/// it is sent by the farmer on the review screen, once the evidence is whole
/// (docs/EVIDENCE_REVALIDATION.md, V4).
///
/// [filed] is for a claim the office already has, where the walk is being
/// reopened to replace clips it refused. Such a claim must not go back through
/// review: review is what releases a draft into the queue, and this draft was
/// released weeks ago. The replacement clips reach the office on their own,
/// against the report id the draft already carries.
Route<void> evidenceRoute({
  required Store store,
  required Voice voice,
  required Positioning positioning,
  required Journey journey,
  Sync? sync,
  bool simulate = false,
  bool filed = false,
  VoidCallback? onFinished,
}) {
  // The claim goes as soon as its evidence is whole.
  //
  // # Why there is no longer a button
  //
  // There was: the walk ended at a review screen and the farmer pressed
  // "नोंद पाठवा". Everything that screen asked of them had already been settled
  // -- the declaration is accepted on the form, and the walk is what makes the
  // evidence complete -- so the button added a step that could only be missed. A
  // farmer who put the phone in their pocket at the last point had walked the
  // whole field and filed nothing, and the home screen then showed the claim as
  // unfinished work with no hint that one tap was all that was left.
  //
  // Nothing is sent that was not already decided. This releases the draft into
  // the queue, which is where every other piece of the claim already goes.
  Future<void> finished() async {
    if (filed) {
      // A claim the office already has, walked again to replace clips it
      // refused. The draft was released weeks ago; releasing it again would file
      // a second claim. The new clips go on their own.
      unawaited(sync?.sendNow());
      onFinished?.call();
      return;
    }

    await store.releaseDraft(journey.id);
    await store.setJourneyStage(journey.id, 'done');
    // Nothing is unfinished any more. Stood down before the send rather than
    // after: from here the queue owns the claim, and a farmer whose claim is
    // waiting on a network must not be told their evidence is incomplete.
    unawaited(BackgroundSync.stopReminding());
    unawaited(sync?.sendNow());
    onFinished?.call();
  }

  final stations = journey.stations;
  // Every claim walks. A field with no boundary gets four points of the
  // farmer's own instead of four the office placed, held apart by the same
  // spacing rule -- but it walks, because the clips are what the ground-vision
  // measurement reads and that carries more weight than radar or NDVI.
  //
  // There used to be a photograph sequence here for parcels with no outline.
  // It had been unreachable since the form began writing every claim as a
  // walk: four stages of prompts, a screen, and their tests, for a path no
  // farmer could arrive at.
  return MaterialPageRoute<void>(
    // Two gates, in this order because the second needs what the first grants.
    //
    // AccessGate: the app must be allowed to record and to place the recording.
    // Checked here rather than at the first point of the walk, because the
    // remedy for a refusal is Settings and often a signal, and a farmer standing
    // in their field has neither to hand.
    //
    // FieldGate: nothing of the survey opens until the farmer is at their farm.
    // It cannot know where they are without the location AccessGate asks for.
    builder: (_) => AccessGate(
      store: store,
      voice: voice,
      child: FieldGate(
        positioning: positioning,
        voice: voice,
        shape: journey.shape,
        stations: stations,
        surveyNumber: journey.surveyNumber,
        simulate: simulate,
        // The engine is chosen here, once, and inside the field gate on purpose:
        // the measurement is about the link where the farmer is actually standing,
        // and a form filled at home on wifi says nothing about the field. Whatever
        // it decides holds for the whole survey, so the walk cannot change
        // character underneath somebody halfway through it.
        builder: (simulated) => ModeGate(
          store: store,
          voice: voice,
          api: sync?.api,
          farmerId: journey.farmerId,
          builder: (mode) => StationWalkScreen(
            store: store,
            voice: voice,
            positioning: positioning,
            draftLocalId: journey.id,
            stations: stations ?? const [],
            shape: journey.shape,
            sownHa: journey.sownHa,
            takenPoints: journey.takenPoints,
            // The assistant, where the app has a way to reach it.
            //
            // Taken from the sync rather than threaded through five call sites: the
            // same authenticated client the claim itself is uploaded with, which is
            // the only one that exists. A route built without one -- a test, a
            // screenshot harness -- simply has no assistant.
            api: sync?.api,
            farmerId: journey.farmerId,
            crop: journey.crop,
            calamity: journey.calamity,
            season: journey.season,
            mode: mode,
            simulate: simulated,
            onFinished: () => unawaited(finished()),
          ),
        ),
      ),
    ),
  );
}
