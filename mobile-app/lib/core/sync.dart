import 'dart:async';
import 'dart:convert';
import 'dart:io';
import 'dart:math' as math;

import 'package:flutter/foundation.dart';
import 'package:http/http.dart' as http;
import 'package:path/path.dart' as p;

import 'accounts.dart';
import 'api.dart';
import 'config.dart';
import 'dates.dart';
import 'db.dart';
import 'faults.dart';
import 'retry.dart';
import 'session.dart';
import 'uploader.dart';

/// Getting what is on the phone to the office.
///
/// # Why the order is not arbitrary
///
/// Boundaries, then reports, then photographs. Each depends on the one before
/// it in a way that is invisible until it goes wrong.
///
/// A photograph is geofenced against the parcel boundary at the moment it is
/// received, and the gate result is written into the record then rather than
/// re-evaluated later. A photograph that lands before its boundary is checked
/// against nothing and is recorded as such permanently. A photograph needs its
/// report's server id, which only exists once the report has landed.
///
/// So the order is a correctness requirement, not a preference, and a pass
/// that gets partway is still correct -- everything left keeps its place.
///
/// # What makes it safe to send anything twice
///
/// Every item carries a key chosen when it was queued, sent as an
/// Idempotency-Key. A reply lost on a village connection is indistinguishable
/// from a request that never arrived, so the queue sends again -- and the
/// server answers with what it already did instead of filing a second claim.
///
/// # What is never given up
///
/// Work the office could not take right now. A server error, a timeout at the
/// office's end, a request to slow down: none of these is a verdict on the
/// claim. Such an item waits -- longer each time, and by a random amount so a
/// village's phones do not return in lockstep -- and tries again, for as long
/// as it takes. Only a refusal ends it, and a refusal is shown to the farmer.
/// See retry.dart.
///
/// # When the pass stops early
///
/// Losing the network: once nothing can get out, everything after it would
/// fail the same way. The office asking every request to wait: continuing
/// would ignore it. Three server errors in a row: the office is struggling,
/// and pressing on sends every queued item into the same failure while
/// adding to the load that is causing it. And a build too old to send, which
/// stops everything until the farmer updates, with the queue kept intact.
///
/// # Whose work, under whose name
///
/// Every row carries the account that queued it, and is sent only with that
/// account's token. The signed-in farmer's work goes first, with [api]. Then
/// the work of farmers who signed out of this phone before their claims had
/// gone, each with their own parked credentials from [accounts] -- and once
/// the office has all of a farmer's work, it is removed from the phone.
class Sync {
  Sync({
    required this.store,
    required this.api,
    this.accounts,
    Api Function(Session session, void Function() onRefused)? apiFor,
    this.onPurged,
    this.onStillPending,
    DateTime Function()? clock,
    this.policy = const RetryPolicy(),
    math.Random? random,
    this.reportStatus = true,
    this.budget,
  }) : _now = clock ?? DateTime.now,
       _random = random ?? math.Random(),
       _apiFor =
           apiFor ??
           ((session, onRefused) =>
               Api(session: session, onSignedOut: onRefused)),
       _api = api;

  final Store store;

  /// The signed-in farmer's client.
  final Api api;

  /// Farmers who signed out with work unsent. Null where the caller has none
  /// to offer, such as a test that only ever signs in one farmer.
  final Accounts? accounts;

  /// Builds a client for a parked farmer. Injected so tests can see which
  /// token each request carried.
  final Api Function(Session session, void Function() onRefused) _apiFor;

  /// Called after a departed farmer's rows were removed, so the caller can
  /// delete the files nothing refers to any more.
  final Future<void> Function()? onPurged;

  /// The client for the farmer whose rows are being sent right now.
  Api _api;

  /// How long one pass may run, or null for as long as it takes.
  ///
  /// The background worker is stopped by Android after about ten minutes. A
  /// pass killed mid-upload leaves the device-wide lease held until it
  /// expires, blocking the app's own passes; one that stops itself first
  /// releases it, and the next pass resumes the upload where it stopped.
  final Duration? budget;

  DateTime? _passStarted;

  bool get _outOfTime {
    final b = budget;
    final started = _passStarted;
    return b != null && started != null && _now().difference(started) >= b;
  }

  /// Asked to arrange a retry when a pass ends with work still queued that
  /// this pass could not send.
  ///
  /// A callback rather than a direct call into the scheduler, because this
  /// file must not depend on a platform plugin: [Sync] runs in the app
  /// isolate, in the background isolate, and in tests, and only the first of
  /// those can schedule anything.
  ///
  /// Given how long to wait first: zero when the pass simply had no network
  /// (the operating system's own network constraint does the waiting), or the
  /// time until the earliest waiting item is due, or the office's pause ends.
  /// Waking the phone before then would find nothing to do.
  final Future<void> Function(Duration after)? onStillPending;

  /// When a waiting item may be tried again.
  final RetryPolicy policy;

  /// Whether to tell the office how this phone's queue is doing after a pass.
  final bool reportStatus;

  final DateTime Function() _now;
  final math.Random _random;

  bool _running = false;
  Timer? _timer;

  final _results = StreamController<SyncResult>.broadcast();

  /// What each pass did, for a screen that shows the queue draining.
  Stream<SyncResult> get results => _results.stream;

  /// How many consecutive server failures end a pass.
  static const breakAfter = 3;

  /// Keys in the cache. Underscored, like the lease, so no cached endpoint can
  /// collide with them, and cleared on sign-out with everything else.
  static const pausedUntilKey = '__sync_paused_until';
  static const upgradeRequiredKey = '__sync_upgrade_required';
  static const lastSuccessKey = '__sync_last_success';
  static const lastErrorKey = '__sync_last_error';
  static const lastReportKey = '__sync_last_report';

  /// Try every item that is due, once.
  ///
  /// Never throws. A sync failure is the ordinary state of a handset in a
  /// field, and a caller should not have to wrap it.
  Future<SyncResult> run() async {
    // One pass at a time in this isolate.
    if (_running) return const SyncResult.skipped();
    _running = true;

    // And one pass at a time on the device: the background worker runs in its
    // own isolate against the same database.
    if (!await store.claimSyncLease()) {
      _running = false;
      return const SyncResult.skipped();
    }

    final tally = _Tally();
    _passStarted = _now();

    try {
      final hold = await _holdReason();
      if (hold != null) {
        tally.stop = hold;
      } else {
        await _drainEveryone(tally);
      }
    } catch (e) {
      // A pass that throws must not leave the engine wedged, and the queue is
      // on disk so nothing is lost by giving up on this attempt.
      // Written down, not only printed. A pass that failed is the single
      // most consequential thing this app can swallow: the farmer sees a
      // queue that is not moving and no reason for it.
      Faults.note('sync.pass', e);
    } finally {
      try {
        await store.releaseSyncLease();
      } catch (_) {
        // The store is gone -- the app is shutting down. The ttl covers it.
      }
      _running = false;
    }

    if (tally.sent > 0) {
      await _safely(() => store.put(lastSuccessKey, _now().toIso8601String()));
    }

    final outstanding = await store.outstanding();
    // Every account and every farmer: this decides whether the scheduler
    // keeps waking, and a farmer switching who they are looking at must not
    // stop somebody else's claim from being sent.
    final anyoneWaiting = (await store.outstanding(
      owner: Store.everyone,
      only: Store.everyone,
    )).any;
    final result = SyncResult(
      boundariesSent: tally.boundaries,
      draftsSent: tally.drafts,
      capturesSent: tally.captures,
      appealsSent: tally.appeals,
      failed: tally.failed,
      waiting: tally.waiting,
      offline: tally.stop == _Stop.offline,
      throttled: tally.stop == _Stop.throttled,
      upgradeRequired: tally.stop == _Stop.upgradeRequired,
      outstanding: outstanding,
    );
    if (!_results.isClosed) _results.add(result);

    if (reportStatus && api.session.signedIn) {
      await _safely(() => _reportStatus(result));
    }

    // Something is still queued that this pass could not send. Hand it to the
    // operating system, which can wait for a network -- or for the office to
    // recover -- that this pass cannot.
    if (anyoneWaiting && result.shouldRetryLater) {
      try {
        final after = await _wakeAfter(tally.stop);
        await onStillPending?.call(after);
      } catch (e) {
        // Nothing will wake the phone to try again. Worth a fault: the
        // queue then waits for the farmer to open the app.
        Faults.note('sync.schedule', e);
      }
    }

    return result;
  }

  /// How long the next background pass should wait before running.
  /// How long to wait after the office would not say who this phone is.
  ///
  /// Fifteen minutes. The usual remedy is the farmer signing in again, and that
  /// runs a pass of its own the moment it happens -- so this is not the path the
  /// common case takes. It is the floor for the other one: a grant the office has
  /// yet to issue, which resolves without the farmer doing anything and should be
  /// picked up without asking every thirty seconds until it does.
  static const unauthorizedFloor = Duration(minutes: 15);

  Future<Duration> _wakeAfter(_Stop? stop) async {
    if (stop == _Stop.offline) return Duration.zero;

    final now = _now();
    var at = await store.nextDueAt(owner: Store.everyone) ?? now;
    final paused = await store.get<int>(pausedUntilKey);
    if (paused != null) {
      final until = DateTime.fromMillisecondsSinceEpoch(paused);
      if (until.isAfter(at)) at = until;
    }
    var after = at.difference(now);

    // A pass cut short leaves its untried items due now, so waking straight
    // back into the same refusal would burn a pass and a farmer's battery
    // without moving anything. Two cases need a floor under the wait.
    //
    // A failing office: what the breaker exists to prevent.
    //
    // An authorisation refusal: the items were deliberately left untouched --
    // not penalised, because they did nothing wrong -- which means they are all
    // due now. The longer floor is because the remedy is usually a person: the
    // farmer signs in again, and signing in runs a pass immediately. Where the
    // cause is a grant the office has yet to issue, this retries quietly in the
    // background instead of every thirty seconds.
    final floor = switch (stop) {
      _Stop.serverFailing => policy.base,
      _Stop.unauthorized => unauthorizedFloor,
      _ => Duration.zero,
    };
    if (after < floor) after = floor;
    return after;
  }

  /// Why this pass should not send anything at all, if there is a reason.
  Future<_Stop?> _holdReason() async {
    final upgrade = await store.get<String>(upgradeRequiredKey);
    if (upgrade != null) {
      // Recorded against the version that was told. An update clears it.
      if (upgrade == Config.appVersion) return _Stop.upgradeRequired;
      await store.put(upgradeRequiredKey, null);
    }

    final paused = await store.get<int>(pausedUntilKey);
    if (paused != null && _now().millisecondsSinceEpoch < paused) {
      return _Stop.throttled;
    }
    return null;
  }

  /// The signed-in farmer's work, then each parked farmer's, then tidy up.
  Future<void> _drainEveryone(_Tally t) async {
    // Nobody signed in -- or a session that has lapsed -- has no token to send
    // with. Its rows wait for the farmer to sign in again.
    if (api.session.signedIn) {
      _api = api;
      await _drain(t, owner: store.owner);
    }
    if (_stopsEveryone(t.stop)) return;

    final parked = accounts;
    if (parked == null) return;

    for (final who in await parked.parked()) {
      if (who == store.owner && api.session.signedIn) {
        // Signed in again: the fresh session supersedes the parked one.
        await parked.forget(who);
        continue;
      }
      if ((await store.outstanding(owner: who, only: Store.everyone)).any) {
        final session = await parked.session(who);
        if (!session.signedIn) {
          await parked.forget(who);
          continue;
        }
        // A refused parked token forgets the credentials, never the rows:
        // they wait, hidden, for the farmer to sign in on this phone again.
        final parkedApi = _apiFor(session, () => unawaited(parked.forget(who)));
        _api = parkedApi;
        try {
          await _drain(t, owner: who);
        } finally {
          _api = api;
          // Built for this pass; left open it leaks a client, and its sockets,
          // every pass while the app is open.
          parkedApi.close();
        }
        if (_stopsEveryone(t.stop)) return;
      }
    }
    _api = api;
    await _purgeDeparted();
  }

  /// Every stop is about the phone or the office rather than one farmer, so
  /// any of them ends the pass for everyone.
  static bool _stopsEveryone(_Stop? stop) => stop != null;

  /// Remove farmers other than the signed-in one whose work has all gone.
  Future<void> _purgeDeparted() async {
    var removed = false;
    for (final who in await store.ownersOtherThan(store.owner)) {
      if (await store.purgeOwnerIfDone(who, now: _now())) {
        removed = true;
        await accounts?.forget(who);
      }
    }
    if (removed) {
      try {
        await onPurged?.call();
      } catch (e) {
        debugPrint('could not remove departed files: $e');
      }
    }
  }

  Future<void> _drain(_Tally t, {required String? owner}) async {
    final now = _now();

    // Each table is read only when its turn comes, so what it returns reflects
    // everything sent before it -- a photograph's report id included.
    Future<bool> each(
      Future<List<Map<String, Object?>>> Function() rows,
      Future<_Outcome> Function(Map<String, Object?>) send,
      void Function() onSent,
    ) async {
      for (final row in await rows()) {
        if (_outOfTime) {
          // What is left waits for the next pass, which is already arranged.
          t.waiting++;
          return false;
        }
        await store.touchSyncLease();
        final outcome = await send(row);
        switch (outcome) {
          case _Outcome.sent:
            onSent();
            t.consecutiveFailures = 0;
          case _Outcome.failed:
            t.failed++;
            t.consecutiveFailures = 0;
          case _Outcome.waiting:
            t.waiting++;
          case _Outcome.transient:
            t.waiting++;
            t.consecutiveFailures++;
            if (t.consecutiveFailures >= breakAfter) {
              t.stop = _Stop.serverFailing;
              return false;
            }
          case _Outcome.offline:
            t.stop = _Stop.offline;
            return false;
          case _Outcome.unauthorized:
            t.stop = _Stop.unauthorized;
            return false;
          case _Outcome.throttled:
            t.stop = _Stop.throttled;
            return false;
          case _Outcome.upgradeRequired:
            t.stop = _Stop.upgradeRequired;
            return false;
        }
      }
      return true;
    }

    if (!await each(
      () => store.pendingBoundaries(now: now, owner: owner),
      _sendBoundary,
      () => t.boundaries++,
    )) {
      return;
    }
    if (!await each(
      () => store.pendingDrafts(now: now, owner: owner),
      _sendDraft,
      () => t.drafts++,
    )) {
      return;
    }
    if (!await each(
      () => store.pendingCaptures(now: now, owner: owner),
      _sendCapture,
      () => t.captures++,
    )) {
      return;
    }
    // Appeals last, and the one item in the queue that depends on nothing
    // else in it: the report being objected to is already at the office.
    await each(
      () => store.pendingAppeals(now: now, owner: owner),
      _sendAppeal,
      () => t.appeals++,
    );
  }

  /// A pass the farmer asked for: waiting work is tried now, not when due.
  ///
  /// A pause the office asked for and a build too old to send still hold --
  /// a button press changes neither.
  Future<SyncResult> sendNow() async {
    await _safely(store.makeDue);
    return run();
  }

  /// Whether the office has refused this build, as last recorded.
  Future<bool> upgradeRequired() async =>
      await store.get<String>(upgradeRequiredKey) == Config.appVersion;

  /// Drain periodically while the app is open, and when the network returns.
  ///
  /// [online] reports connectivity changes. A return to the network starts a
  /// pass after a random wait of up to [reconnectSpread]: when a tower comes
  /// back, every phone under it sees the network in the same second, and
  /// without the spread they would all reach the office in that second too.
  void start({Stream<bool>? online}) {
    _timer ??= Timer.periodic(Config.syncInterval, (_) => unawaited(run()));
    _online ??= online?.distinct().listen((up) {
      if (!up) return;
      _reconnect?.cancel();
      final wait = Duration(
        milliseconds: _random.nextInt(reconnectSpread.inMilliseconds + 1),
      );
      _reconnect = Timer(wait, () => unawaited(run()));
    });
    unawaited(run());
  }

  /// The longest random wait before a pass after the network returns.
  static const reconnectSpread = Duration(seconds: 20);

  StreamSubscription<bool>? _online;
  Timer? _reconnect;

  void stop() {
    _timer?.cancel();
    _timer = null;
    _reconnect?.cancel();
    _reconnect = null;
    unawaited(_online?.cancel());
    _online = null;
  }

  Future<void> dispose() async {
    stop();
    await _results.close();
  }

  // --- one item at a time --------------------------------------------------

  Future<_Outcome> _sendBoundary(Map<String, Object?> row) async {
    final farmId = row['farm_id'] as String;
    final points = jsonDecode(row['points'] as String) as List<dynamic>;

    Future<void> write({
      required String status,
      int? attempts,
      String? lastError,
      int? nextAttemptAt,
    }) => store.putBoundary(
      farmId: farmId,
      surveyNumber: row['survey_number'] as String,
      points: points.cast<Map<String, Object?>>(),
      areaHa: (row['area_ha'] as num).toDouble(),
      worstAccuracyM: (row['worst_accuracy_m'] as num).toDouble(),
      // Carried through rather than left to default: this runs in the
      // background isolate, where no farmer is on screen, and defaulting
      // would erase whose parcel it was on the first status change.
      farmerId: row['farmer_id'] as String?,
      status: status,
      attempts: attempts ?? (row['attempts'] as int? ?? 0),
      lastError: lastError,
      nextAttemptAt: nextAttemptAt,
    );

    // A boundary is walked for one farmer's parcel, and the office checks it
    // is theirs. The second farmer on a handset needs the grant for the same
    // reason their claim does.
    final res = await _api.postJson(
      '/api/v1/parcels/${Uri.encodeComponent(farmId)}/boundary'
      '?farmer_id=${Uri.encodeComponent('${row['farmer_id'] ?? ''}')}',
      {'points': points},
      idempotencyKey: row['sync_key'] as String?,
      farmerGrant: await store.grantFor('${row['farmer_id'] ?? ''}'),
    );

    return _record(
      res,
      row,
      onSent: () => write(status: 'sent', nextAttemptAt: 0),
      onFailed: (message) => write(status: 'failed', lastError: message),
      onRetry: (attempts, due, message) => write(
        status: 'pending',
        attempts: attempts,
        lastError: message,
        nextAttemptAt: due,
      ),
    );
  }

  Future<_Outcome> _sendDraft(Map<String, Object?> row) async {
    // One signed-in number may act for several farmers, and the office will
    // not take a claim for one it has not been shown a grant for -- it refuses
    // rather than filing it against the wrong person. Absent for the account's
    // own farmer, who needs none.
    final grant = await store.grantFor('${row['farmer_id'] ?? ''}');

    // How many photographs and clips are going to follow this claim.
    //
    // The claim goes first and its evidence one piece at a time, so without
    // this the office cannot tell "still uploading" from "that was
    // everything" -- and it now starts assessing a claim the moment it is
    // complete. Counted here rather than promised earlier because this is the
    // moment the set is fixed: the claim is only released for sending once
    // its evidence is whole.
    final expected = (await store.capturesFor(
      row['local_id'] as String,
    )).length;

    final res = await _api.postJson(
      '/api/v1/reports',
      {
        'survey_event_id': row['survey_event_id'],
        'farmer_id': row['farmer_id'],
        'season': row['season'],
        'crop_year': row['crop_year'],
        'loss_category': row['loss_category'],
        // Who filed it, and from where.
        //
        // The app never sent this, and the server defaults an unsent channel
        // to 'field_officer' -- so every claim a farmer filed from their own
        // phone was recorded as having been filed by an officer standing in
        // their field. Nothing read it wrongly yet; it is simply not true, and
        // the audit trail of a claim is the wrong place for a value nobody
        // chose.
        'reported_channel': 'farmer_app',
        'calamity_type': row['calamity_type'],
        // Normalised here as well as when the report is written, so a draft
        // already sitting in the queue with a bare date is repaired on its way
        // out rather than refused forever.
        'occurred_at': occurredAtFrom(row['occurred_at']),
        'expected_captures': expected,
        // When the farmer filled the form, which is not now. The office
        // measures the intimation window on it, and keeps both instants so
        // the delay this queue introduces is visible rather than charged to
        // the farmer. Omitted for a draft queued before the app recorded it,
        // where there is no honest answer and arrival stands.
        if (row['filed_at'] != null)
          'filed_at': DateTime.fromMillisecondsSinceEpoch(
            row['filed_at'] as int,
          ).toUtc().toIso8601String(),
        'declaration_accepted': (row['declaration_accepted'] as int? ?? 0) == 1,
        'farmer_signature_kind': row['signature_kind'],
        'items': [
          {
            'line_no': 1,
            'survey_number': row['survey_number'],
            'farm_id': row['farm_id'],
            'crop_name': row['crop_name'],
            'area_sown_ha': row['area_sown_ha'],
            'area_affected_ha': row['area_affected_ha'],
            'damage_percent': row['damage_percent'],
          },
        ],
      },
      // The report's own key. The server stores it on the report under a
      // unique index, so however many times this is sent -- and whether the
      // first reply was lost or the server died mid-request -- one claim is
      // filed.
      idempotencyKey: row['sync_key'] as String?,
      farmerGrant: grant,
    );

    return _record(
      res,
      row,
      onSent: () => store.putDraft({
        ...row,
        // The server id is what makes this report real, and it is what every
        // queued photograph is waiting for.
        'server_id': res.body?['id'],
        'reference': res.body?['reference'],
        'status': 'sent',
        'last_error': null,
        'next_attempt_at': 0,
      }),
      onFailed: (message) =>
          store.putDraft({...row, 'status': 'failed', 'last_error': message}),
      onRetry: (attempts, due, message) => store.putDraft({
        ...row,
        'status': 'pending',
        'attempts': attempts,
        'next_attempt_at': due,
        'last_error': message,
      }),
    );
  }

  /// Sends a station clip in chunks, resuming where the last pass stopped.
  Future<_Outcome> _sendClip(
    Map<String, Object?> row,
    String reportId,
    File file,
  ) async {
    // The same grant the claim went up with: see the note on the photograph
    // path. A clip is resumed across passes, so this is read per pass rather
    // than carried, and an expired grant stops the upload cleanly instead of
    // sending chunks the office will refuse.
    final draft = await store.draft('${row['draft_local_id']}');
    final grant = await store.grantFor('${draft?['farmer_id'] ?? ''}');

    final outcome = await Uploader(api: _api, farmerGrant: grant).send(
      knownSha256: row['sha256'] as String?,
      onChunk: store.touchSyncLease,
      shouldStop: () => _outOfTime,
      reportId: reportId,
      uploadKey: '${row['upload_key'] ?? row['sync_key'] ?? row['local_id']}',
      file: file,
      contentType: '${row['content_type']}',
      metadata: {
        'kind': row['kind'],
        'caption': row['caption'] ?? '',
        'captured_at': row['captured_at'],
        if (row['lat'] != null) 'lat': row['lat'],
        if (row['lon'] != null) 'lon': row['lon'],
        if (row['accuracy_m'] != null) 'accuracy_m': row['accuracy_m'],
        if (row['heading_deg'] != null) 'heading_deg': row['heading_deg'],
        if (row['station_no'] != null) 'station_no': row['station_no'],
        if (row['duration_ms'] != null) 'duration_ms': row['duration_ms'],
        if (row['is_mocked'] != null) 'is_mocked': row['is_mocked'] == 1,
        // What this handset's encoder agreed to, which is not always what it
        // was asked for: a Vivo V2253 asked for 2.5 Mbit/s and recorded at
        // 8.5, so a four-clip walk came to 65 MB rather than 18. The app has
        // recorded this locally since schema 8 and never sent it, which made
        // it useless to the one person it was for -- an operator looking at a
        // 65 MB claim beside an 18 MB one and needing to know which is the
        // phone and which is a longer walk.
        //
        // Conditional, not defaulted: a clip captured before the app recorded
        // this leaves the column null rather than having a profile invented
        // for it.
        if (row['encoding_profile'] != null)
          'encoding_profile': row['encoding_profile'],
        if (row['encoding_fps'] != null) 'encoding_fps': row['encoding_fps'],
        if (row['encoding_bitrate_bps'] != null)
          'encoding_bitrate_bps': row['encoding_bitrate_bps'],
        // What the assistant asked the farmer to show in this clip. Sent so the
        // assessment can tell a directed observation from the farmer's own
        // selection, which is what answers a selective-evidence challenge.
        if (row['guidance_asked_for'] != null)
          'guidance_asked_for': row['guidance_asked_for'],
        if (row['guidance_model'] != null)
          'guidance_model': row['guidance_model'],
        'device_label': row['device'] ?? '',
      },
    );

    if (outcome.complete) {
      await store.putCapture({
        ...row,
        'status': 'sent',
        'sent_bytes': outcome.sentBytes,
        'last_error': null,
        'next_attempt_at': 0,
      });
      return _Outcome.sent;
    }

    if (outcome.permanentFailure != null) {
      // Outside the parcel, a mocked position, a checksum that failed after
      // the server's restarts. Retrying spends a farmer's battery to be told
      // the same thing again.
      //
      // The code is kept alongside the message, because some of these refusals
      // are the farmer's to fix by recording the point somewhere else and the
      // home screen can only offer them the camera if it knows which.
      await store.putCapture({
        ...row,
        'status': 'failed',
        'sent_bytes': outcome.sentBytes,
        'last_error': outcome.permanentFailure,
        'last_error_code': outcome.permanentCode,
      });
      return _Outcome.failed;
    }

    // The bytes already acknowledged stay acknowledged; the next pass asks
    // where it got to.
    switch (outcome.verdict) {
      case Verdict.offline:
        await store.putCapture({...row, 'sent_bytes': outcome.sentBytes});
        return _Outcome.offline;
      case Verdict.upgradeRequired:
        await _requireUpgrade();
        return _Outcome.upgradeRequired;
      case Verdict.throttled:
        await _pauseFor(outcome.retryAfter);
        await store.putCapture({...row, 'sent_bytes': outcome.sentBytes});
        return _Outcome.throttled;
      default:
        final attempts = (row['attempts'] as int? ?? 0) + 1;
        await store.putCapture({
          ...row,
          'sent_bytes': outcome.sentBytes,
          'attempts': attempts,
          'next_attempt_at': _dueAfter(attempts, outcome.retryAfter),
        });
        return _Outcome.transient;
    }
  }

  Future<_Outcome> _sendCapture(Map<String, Object?> row) async {
    final draft = await store.draft(row['draft_local_id'] as String);
    final reportId = draft?['server_id'] as String?;

    // Its report was refused. The photograph can never be sent on its own,
    // and left waiting it counts as outstanding for ever: the queue card never
    // clears, the office sees a phone with work pending that will never move,
    // and a farmer who signed out is never removed from a shared phone
    // (docs/SYNC_SCALE_REVIEW.md, H3). Marked refused with the report's reason;
    // "try again" on the home screen puts both back together.
    if (reportId == null && (draft == null || draft['status'] == 'failed')) {
      await store.putCapture({
        ...row,
        'status': 'failed',
        'last_error': draft == null
            ? 'Its report is no longer on this phone.'
            : 'Its report was refused: ${draft['last_error'] ?? ''}',
      });
      return _Outcome.failed;
    }

    // Its report has not landed yet. Not a failure and not an attempt: the
    // photograph is waiting on something that is itself still queued.
    if (reportId == null) return _Outcome.waiting;

    final path = row['file_path'] as String;
    final file = File(path);
    if (!file.existsSync()) {
      // Android reclaimed the file, or the farmer cleared storage. Nothing
      // will bring it back, so it is marked rather than retried forever.
      await store.putCapture({
        ...row,
        'status': 'failed',
        'last_error': 'The photograph is no longer on this phone.',
      });
      return _Outcome.failed;
    }

    // A clip goes up in chunks, a photograph in one request.
    if (_isVideo(row)) {
      return _sendClip(row, reportId, file);
    }

    // The office resolves the claim through the scope of the farmer being
    // acted for, so evidence for the second farmer on a handset needs the same
    // grant the claim itself went up with. Without it the claim is filed, the
    // field is walked, and every photograph comes back "no such report".
    final grant = await store.grantFor('${draft?['farmer_id'] ?? ''}');

    final res = await _api.postMultipart(
      '/api/v1/reports/${Uri.encodeComponent(reportId)}/captures',
      {
        'kind': row['kind'] as String,
        'caption': row['caption'] as String? ?? '',
        'farm_id': draft?['farm_id'] as String? ?? '',
        if (row['lat'] != null) 'lat': '${row['lat']}',
        if (row['lon'] != null) 'lon': '${row['lon']}',
        if (row['accuracy_m'] != null) 'accuracy_m': '${row['accuracy_m']}',
        if (row['heading_deg'] != null) 'heading_deg': '${row['heading_deg']}',
        // Which assigned point this was taken at. The clip path has always
        // sent these; this one never did, so the office received four
        // photographs belonging to no point and the report had nothing to
        // print beside the map of the walk.
        if (row['station_no'] != null) 'station_no': '${row['station_no']}',
        if (row['station_offset_m'] != null)
          'station_offset_m': '${row['station_offset_m']}',
        if (row['is_mocked'] != null)
          'is_mocked': row['is_mocked'] == 1 ? 'true' : 'false',
        'captured_at': row['captured_at'] as String,
        'device': row['device'] as String? ?? '',
      },
      () => http.MultipartFile.fromPath(
        'image',
        path,
        filename: p.basename(path),
      ),
      idempotencyKey: row['sync_key'] as String?,
      farmerGrant: grant,
    );

    return _record(
      res,
      row,
      onSent: () async {
        await store.putCapture({
          ...row,
          'status': 'sent',
          'last_error': null,
          'next_attempt_at': 0,
        });
        // The photograph is on the server now. Keeping a copy would fill a
        // handset that had 2 GB free to begin with.
        unawaited(file.delete().catchError((_) => file));
      },
      onFailed: (message) =>
          store.putCapture({...row, 'status': 'failed', 'last_error': message}),
      onRetry: (attempts, due, message) => store.putCapture({
        ...row,
        'status': 'pending',
        'attempts': attempts,
        'next_attempt_at': due,
        'last_error': message,
      }),
    );
  }

  Future<_Outcome> _sendAppeal(Map<String, Object?> row) async {
    final res = await _api.postJson('/api/v1/grievances', {
      'report_id': row['report_id'],
      'ground': row['ground'],
      'statement': row['statement'],
      'channel': 'app',
    }, idempotencyKey: row['sync_key'] as String?);

    return _record(
      res,
      row,
      onSent: () => store.putAppeal({
        ...row,
        'server_id': res.body?['id'],
        'reference': res.body?['reference'],
        'status': 'sent',
        'last_error': null,
        'next_attempt_at': 0,
      }),
      onFailed: (message) =>
          store.putAppeal({...row, 'status': 'failed', 'last_error': message}),
      onRetry: (attempts, due, message) => store.putAppeal({
        ...row,
        'status': 'pending',
        'attempts': attempts,
        'next_attempt_at': due,
        'last_error': message,
      }),
    );
  }

  /// Turn one response into an outcome, and write what happened.
  Future<_Outcome> _record(
    Result res,
    Map<String, Object?> row, {
    required Future<void> Function() onSent,
    required Future<void> Function(String message) onFailed,
    required Future<void> Function(int attempts, int due, String message)
    onRetry,
  }) async {
    switch (classify(res)) {
      case Verdict.sent:
        await onSent();
        return _Outcome.sent;

      case Verdict.offline:
        // Nothing reached. The item keeps its place and its attempt count.
        return _Outcome.offline;

      case Verdict.unauthorized:
        // The item is untouched: not sent, not failed, not even penalised with
        // a retry count. It did nothing wrong, and the office will take the same
        // bytes once this phone can say who it is.
        //
        // The pass stops, because every remaining item would meet the same
        // refusal and marching through the queue collecting them would waste a
        // pass and a farmer's data.
        Faults.note('sync.unauthorized', 'HTTP ${res.status}: ${res.message}');
        return _Outcome.unauthorized;

      case Verdict.permanent:
        await onFailed(res.message);
        await _noteError(res);
        return _Outcome.failed;

      case Verdict.upgradeRequired:
        await _requireUpgrade();
        return _Outcome.upgradeRequired;

      case Verdict.throttled:
        // The office asked this phone to wait. The item that was told is not
        // penalised: it did nothing wrong, and it is first in line when the
        // pause ends.
        await _pauseFor(res.retryAfter);
        await _noteError(res);
        return _Outcome.throttled;

      case Verdict.transient:
        final attempts = (row['attempts'] as int? ?? 0) + 1;
        await onRetry(
          attempts,
          _dueAfter(attempts, res.retryAfter),
          res.message,
        );
        await _noteError(res);
        return _Outcome.transient;
    }
  }

  int _dueAfter(int attempts, Duration? retryAfter) => _now()
      .add(policy.delay(attempts, retryAfter: retryAfter, random: _random))
      .millisecondsSinceEpoch;

  /// Pause every request from this phone, for as long as the office asked --
  /// plus up to a fifth again at random, so the phones it told do not all come
  /// back in the same second.
  Future<void> _pauseFor(Duration? retryAfter) async {
    final base = retryAfter ?? policy.base;
    final spread = base.inMilliseconds ~/ 5;
    final extra = spread > 0 ? _random.nextInt(spread + 1) : 0;
    final until = _now().add(base).millisecondsSinceEpoch + extra;
    await store.put(pausedUntilKey, until);
  }

  Future<void> _requireUpgrade() =>
      store.put(upgradeRequiredKey, Config.appVersion);

  Future<void> _noteError(Result res) async {
    final code = res.code ?? (res.status > 0 ? 'HTTP_${res.status}' : null);
    if (code != null) await _safely(() => store.put(lastErrorKey, code));
  }

  // --- telling the office how the queue is doing --------------------------

  /// How often a phone reports when nothing has changed.
  static const statusEvery = Duration(minutes: 15);

  /// Report this phone's queue, throttled to changes and to [statusEvery].
  ///
  /// Counts only -- no content, no photographs, no positions. It exists so an
  /// operator can find the phones whose claims have been waiting too long,
  /// before the farmers find out that they have.
  Future<void> _reportStatus(SyncResult result) async {
    final out = result.outstanding;
    final failed = await store.failedCount();
    final fingerprint =
        '${out.drafts}/${out.captures}/${out.boundaries}/'
        '${out.appeals}/$failed/${result.upgradeRequired}';

    final last = await store.get<Map<String, dynamic>>(lastReportKey);
    final now = _now();
    if (last != null && last['fingerprint'] == fingerprint) {
      final at = DateTime.tryParse('${last['at']}');
      if (at != null && now.difference(at) < statusEvery) return;
    }

    final oldest = await store.oldestPendingAt();
    final lastSuccess = await store.get<String>(lastSuccessKey);
    final res = await api.postJson('/api/v1/devices/sync-status', {
      'device_id': await store.deviceId(),
      'app_version': Config.appVersion,
      'platform': Platform.operatingSystem,
      'pending_reports': out.drafts,
      'pending_captures': out.captures,
      'pending_boundaries': out.boundaries,
      'pending_appeals': out.appeals,
      'failed_items': failed,
      'pending_bytes': await store.pendingBytes(),
      if (oldest != null) 'oldest_pending_at': oldest.toUtc().toIso8601String(),
      if (lastSuccess != null)
        'last_success_at': DateTime.parse(
          lastSuccess,
        ).toUtc().toIso8601String(),
      'last_error_code': ?await store.get<String>(lastErrorKey),
    });

    if (res.ok) {
      await store.put(lastReportKey, {
        'fingerprint': fingerprint,
        'at': now.toIso8601String(),
      });
    }
  }

  Future<void> _safely(Future<void> Function() f) async {
    try {
      await f();
    } catch (e) {
      Faults.note('sync.bookkeeping', e);
    }
  }
}

enum _Outcome {
  sent,
  failed,
  offline,
  waiting,
  transient,
  throttled,
  upgradeRequired,

  /// The office would not say who this phone is. The item is kept untouched and
  /// the pass stops: an authorisation problem is never a verdict on the data.
  unauthorized,
}

enum _Stop { offline, throttled, serverFailing, upgradeRequired, unauthorized }

class _Tally {
  var boundaries = 0;
  var drafts = 0;
  var captures = 0;
  var appeals = 0;
  var failed = 0;
  var waiting = 0;
  var consecutiveFailures = 0;
  _Stop? stop;

  int get sent => boundaries + drafts + captures + appeals;
}

/// What a pass did.
class SyncResult {
  const SyncResult({
    required this.boundariesSent,
    required this.draftsSent,
    required this.capturesSent,
    this.appealsSent = 0,
    required this.failed,
    required this.offline,
    required this.outstanding,
    this.waiting = 0,
    this.throttled = false,
    this.upgradeRequired = false,
  });

  const SyncResult.skipped()
    : boundariesSent = 0,
      draftsSent = 0,
      capturesSent = 0,
      appealsSent = 0,
      failed = 0,
      waiting = 0,
      offline = false,
      throttled = false,
      upgradeRequired = false,
      outstanding = const Outstanding(drafts: 0, captures: 0, boundaries: 0);

  final int boundariesSent;
  final int draftsSent;
  final int capturesSent;
  final int appealsSent;

  /// Refused by the office this pass.
  final int failed;

  /// Tried and waiting to try again, or waiting on something else queued.
  final int waiting;

  /// Nothing could get out. Not a failure: it is where this app lives.
  final bool offline;

  /// The office asked this phone to wait before sending more.
  final bool throttled;

  /// This build is too old to send. The queue is kept until the app updates.
  final bool upgradeRequired;

  final Outstanding outstanding;

  int get sent => boundariesSent + draftsSent + capturesSent + appealsSent;
  bool get anythingHappened => sent > 0;

  /// Whether the operating system should be asked to try again later.
  ///
  /// Not after an update is required -- a background retry cannot fix that --
  /// and not after a pass that sent everything it could.
  bool get shouldRetryLater =>
      !upgradeRequired && (offline || throttled || waiting > 0);
}

/// Whether a queued capture is a video.
///
/// By content type rather than by kind, because it is the size that decides
/// which path it takes and the content type is what says how big it will be.
bool _isVideo(Map<String, Object?> row) =>
    '${row['content_type'] ?? ''}'.startsWith('video/');
