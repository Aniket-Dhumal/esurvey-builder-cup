import 'dart:async';

import 'package:flutter/material.dart';

import '../boundary/boundary_screen.dart';
import '../core/api.dart';
import '../core/background.dart';
import '../core/db.dart';
import '../core/farmer_data.dart';
import '../core/reminders.dart';
import '../core/sensors.dart';
import '../core/session.dart';
import '../core/sync.dart';
import '../core/voice.dart';
import '../report/file_report_screen.dart';
import '../report/parcel_detail_screen.dart';
import '../report/assessment_report_screen.dart';
import '../ui/theme.dart';
import '../report/report_card.dart';
import '../report/claim_state.dart';
import '../report/journey.dart';
import '../report/progress_screen.dart';
import '../report/review_screen.dart';
import '../appeal/appeal_screen.dart';
import '../help/help_screen.dart';
import '../core/dates.dart';
import '../core/language.dart';

/// The farmer's own screen.
///
/// # What it is for
///
/// Three questions, in the order somebody actually asks them: is there
/// anything I can report right now, is what I already sent safe, and what does
/// the office know about my land.
///
/// # Why nothing here waits for the network
///
/// Everything is painted from what the phone already knows and quietly
/// replaced if a fresher answer arrives. A farmer with no signal is the normal
/// case, not the degraded one, and an app that shows a spinner for thirty
/// seconds before admitting it cannot reach anything has told them nothing
/// and wasted their battery doing it.
class HomeScreen extends StatefulWidget {
  const HomeScreen({
    super.key,
    required this.store,
    required this.data,
    required this.api,
    required this.session,
    required this.sync,
    required this.voice,
    required this.positioning,
    required this.onSignOut,
    required this.onSwitchFarmer,
    this.online,
  });

  final Store store;
  final FarmerData data;
  final Api api;
  final Session session;
  final Sync sync;
  final Voice voice;
  final Positioning positioning;
  final VoidCallback onSignOut;

  /// Back to the list of people this handset can act for. A household files
  /// for one person and then the next, and making them sign out in between
  /// would cost an SMS and a code every time.
  final VoidCallback onSwitchFarmer;

  /// Connectivity changes, true when a network is available. A return to the
  /// network starts a pass rather than waiting for the next timer tick.
  final Stream<bool>? online;

  @override
  State<HomeScreen> createState() => _HomeScreenState();
}

class _HomeScreenState extends State<HomeScreen> {
  Cached<List<dynamic>> _events = const Cached(null, null, fresh: false);
  Cached<Map<String, dynamic>?> _record = const Cached(
    null,
    null,
    fresh: false,
  );
  Cached<List<dynamic>> _reports = const Cached(null, null, fresh: false);

  Outstanding _outstanding = const Outstanding(
    drafts: 0,
    captures: 0,
    boundaries: 0,
  );

  /// Rows that spent their attempts and stopped trying. Counted separately
  /// because the pending queries filter them out, which is how they came to
  /// be invisible in the first place.
  Outstanding _stuck = const Outstanding(drafts: 0, captures: 0, boundaries: 0);

  /// Claims started on this phone and not finished, with how far each got.
  List<({Journey journey, EvidenceProgress? progress})> _unfinished = const [];

  /// Clips the office refused over where they were recorded, by claim.
  ///
  /// Kept apart from [_stuck] because the remedy is different in kind: these
  /// need the camera and a walk to another part of the field, and no amount of
  /// resending will do. Three clips of a filed claim sat in the queue being
  /// refused on every pass, and the retry button was the only thing the farmer
  /// was given.
  List<RecordAgain> _recordAgain = const [];

  /// Bytes of evidence acknowledged by the office, over bytes still to go.
  ///
  /// The count of items barely moves while a clip uploads -- one clip can take
  /// twenty minutes on a village connection -- so a farmer watching only the
  /// words concludes nothing is happening.
  ({int sent, int total})? _progress;

  /// Keeps the bar moving while a clip is going up.
  ///
  /// The sync engine reports once per pass, and a pass carrying a 16 MB clip on a
  /// village connection is twenty minutes long -- so a bar driven by pass results
  /// would sit still for twenty minutes and then jump. The uploader acknowledges
  /// every half-megabyte to the phone's own rows, so those rows do move, and this
  /// reads them.
  ///
  /// A sum over a handful of rows every two seconds, and only while something is
  /// waiting. Cheaper than the frame it redraws.
  Timer? _progressTicker;

  StreamSubscription<SyncResult>? _syncSub;
  bool _loading = true;

  /// The office has refused this build. The queue is kept, and only an update
  /// will send it, which the farmer needs to be told.
  bool _upgradeRequired = false;

  @override
  void initState() {
    super.initState();

    _syncSub = widget.sync.results.listen((r) {
      if (!mounted) return;
      setState(() {
        _outstanding = r.outstanding;
        _upgradeRequired = r.upgradeRequired;
      });
      // Something left the phone. The reports list is now behind, and that
      // needs the network.
      if (r.anythingHappened) unawaited(_load(refresh: true));
      // Whether or not anything left, what the phone is holding has changed.
      //
      // anythingHappened is `sent > 0`, so a pass that only collected refusals
      // reported nothing and refreshed nothing: the farmer pressed "try again",
      // three clips were refused, and the screen did not move. That silence is
      // the fault this app guards against everywhere else -- and it would have
      // hidden the card that tells them to record those clips again, which is
      // the one thing they could actually have done.
      //
      // Unconditional because these are three local counts, not a request. The
      // cost of reading them after every pass is nothing; the cost of not
      // reading them is a farmer who cannot see the only work left to do.
      unawaited(_refreshOutstanding());
    });

    _progressTicker = Timer.periodic(const Duration(seconds: 2), (_) async {
      if (!mounted || !_outstanding.any) return;
      final p = await widget.store.uploadProgress();
      if (!mounted) return;
      // Only when it has actually moved: a setState every two seconds for a
      // figure that has not changed is a rebuild of the whole list for nothing.
      if (p.sent == _progress?.sent && p.total == _progress?.total) return;
      setState(() => _progress = p);
    });

    unawaited(_load(refresh: true));
    unawaited(
      widget.sync.upgradeRequired().then((v) {
        if (mounted && v) setState(() => _upgradeRequired = true);
      }),
    );
    widget.sync.start(online: widget.online);
  }

  @override
  void dispose() {
    _progressTicker?.cancel();
    unawaited(_syncSub?.cancel());
    super.dispose();
  }

  Future<void> _load({required bool refresh}) async {
    // The farmer picked on this handset, not the one on the account. A single
    // signed-in number may act for several, and this screen belongs to
    // whichever of them was chosen. The account's own farmer is the fallback
    // for a session from before the picker existed.
    final farmerId = widget.store.farmer ?? widget.session.farmerId;
    // What lets the office answer for a farmer whose registered number is not
    // the one that signed in. Absent for the account's own farmer.
    final grant = farmerId == null
        ? null
        : await widget.store.grantFor(farmerId);

    // Read the cache first and paint it, so the screen is never blank while a
    // request that may never arrive is in flight.
    // The record first: its parcels say which villages to ask events for.
    final cachedRecord = farmerId == null
        ? const Cached<Map<String, dynamic>?>(null, null, fresh: false)
        : await widget.data.record(farmerId, refresh: false);
    final cachedEvents = await widget.data.events(
      refresh: false,
      villages: FarmerData.villagesOf(cachedRecord.value),
    );
    final cachedReports = await widget.data.reports(
      farmerId ?? '',
      refresh: false,
    );

    if (!mounted) return;
    setState(() {
      _events = cachedEvents;
      _record = cachedRecord;
      _reports = cachedReports;
      _loading = false;
    });

    unawaited(_refreshOutstanding());
    if (!refresh) return;

    final record = farmerId == null
        ? cachedRecord
        : await widget.data.record(farmerId, grant: grant);
    // Only events that cover one of this farmer's villages. See
    // FarmerData.events for the claim that was refused without this.
    final events = await widget.data.events(
      villages: FarmerData.villagesOf(record.value ?? cachedRecord.value),
    );
    final reports = await widget.data.reports(farmerId ?? '', grant: grant);

    if (!mounted) return;
    setState(() {
      _events = events;
      _record = record;
      _reports = reports;
    });

    // While there is a signal, pull the walk's points for every parcel this
    // farmer has. The walk itself happens in a field with no signal, and
    // without this the strongest evidence quietly became the weakest there.
    // Nothing on this screen waits for it.
    final parcels = (record.value?['parcels'] as List<dynamic>?) ?? const [];
    if (parcels.isNotEmpty) {
      // For whoever is being worked as, not whoever signed in: the grant is
      // what makes a second farmer's parcel readable at all.
      unawaited(widget.data.prefetch(parcels, forFarmer: farmerId));
    }
  }

  /// What has happened to one claim, in full.
  Future<void> _openProgress(Map<String, dynamic> report) async {
    final farmerId = widget.store.farmer ?? widget.session.farmerId ?? '';
    final grant = farmerId.isEmpty
        ? null
        : await widget.store.grantFor(farmerId);
    if (!mounted) return;
    await Navigator.of(context).push(
      MaterialPageRoute<void>(
        builder: (_) => ProgressScreen(
          data: widget.data,
          voice: widget.voice,
          report: report,
          grant: grant,
        ),
      ),
    );
  }

  Future<void> _refreshOutstanding() async {
    final o = await widget.store.outstanding();
    final stuck = await widget.store.stuck();
    final recordAgain = await widget.store.capturesToRecordAgain();
    final byClaim = {for (final r in recordAgain) r.draftLocalId: r};
    final progress = await widget.store.uploadProgress();
    final unfinished = <({Journey journey, EvidenceProgress? progress})>[];
    for (final row in await widget.store.openJourneys()) {
      final j = Journey.fromRow(row);
      if (j.stage == 'form') {
        // A form with nothing to show it against cannot be continued offline;
        // it was never filed, so there is nothing on the phone to lose.
        if (j.formResumable) unfinished.add((journey: j, progress: null));
        continue;
      }
      final draftRow = await widget.store.draft(j.id);
      if (draftRow == null) {
        await widget.store.deleteJourney(j.id);
        continue;
      }
      final evidence = await evidenceProgress(widget.store, j);

      // What the claim actually is, from the rows rather than from its label.
      //
      // The label is written after the fact by an app Android kills without
      // warning, and every time the two disagreed a farmer lost a walk. See
      // ClaimState.
      final state = ClaimState.derive(
        journey: j,
        draft: draftRow,
        progress: evidence,
        refusedClips: byClaim[j.id]?.count ?? 0,
      );

      // Walked in full and never released into the queue.
      //
      // This is the state that cost two farmers on the first handset their walk:
      // four clips each, taken in the sun, with the draft still holding -- so the
      // queue did not have the claim and this list hid it behind its own label.
      // It used to be handled by moving the label on, which left the claim
      // exactly as stranded and merely stopped mentioning it.
      //
      // Releasing it is the right answer and needs nothing from the farmer. The
      // evidence is complete and the declaration was accepted on the form, so
      // there is no decision left to take -- only a write that did not happen.
      if (state.shouldHaveBeenFiled) {
        await widget.store.releaseDraft(j.id);
        await widget.store.setJourneyStage(j.id, 'done');
        unawaited(widget.sync.sendNow());
        continue;
      }

      // A claim waiting on a network is not unfinished work. Saying otherwise
      // asks a farmer to do something about a signal.
      if (!state.owesFieldWork) continue;

      unfinished.add((journey: j, progress: evidence));
    }
    if (!mounted) return;
    setState(() {
      _outstanding = o;
      _stuck = stuck;
      _recordAgain = recordAgain;
      _progress = progress;
      _unfinished = unfinished;
    });

    // The reminder has done its job: the farmer is looking at the list. It is
    // taken down here rather than on tapping it, because reaching this screen
    // any other way answers it just as well -- and a notification that
    // outlives the thing it is about teaches people to ignore notifications.
    unawaited(Reminders().clear());
    // And re-armed, or stood down, against what is actually on the phone now.
    // A farmer who has just finished the last point should not be reminded
    // tomorrow; one who has left a claim half-walked should.
    unawaited(
      unfinished.isEmpty
          ? BackgroundSync.stopReminding()
          : BackgroundSync.remindAbout(Reminders.after),
    );
  }

  /// The farmer's parcels, as the office sends them.
  ///
  /// `parcels`, not `land_data`. The latter is AgriStack's own wire format,
  /// which the backend's registry adapter reads and does not pass on -- so
  /// this getter was always empty, and the "Report damage" button on this
  /// screen always answered "your land records have not arrived yet" to a
  /// farmer whose land records were sitting in the row above it. Filing
  /// worked only by the other route, through the Farmer ID screen, which
  /// reads the same record under the right key.
  List<dynamic> get _parcels =>
      (_record.value?['parcels'] as List<dynamic>?) ?? const [];

  /// The farmer's fields with no recorded outline.
  ///
  /// Filing a claim no longer waits for one: a field with no boundary is
  /// walked as four points of the farmer's own choosing. But the satellite
  /// evidence is read over the outline, so a claim on a field without one
  /// rests on its clips alone -- and that is worth a few minutes on a dry
  /// day, which is what this card is for and why it is here rather than in
  /// the middle of reporting damage.
  List<Map<String, dynamic>> get _unmapped => [
    for (final p in _parcels)
      if (p is Map && (p['plot_geometry'] == null)) p.cast<String, dynamic>(),
  ];

  @override
  Widget build(BuildContext context) {
    if (_loading) {
      return const Scaffold(body: Center(child: CircularProgressIndicator()));
    }

    // The farmer the phone is working for, not the account it signed in with.
    //
    // A village handset acts for several people in turn -- a household files for
    // one and then the next -- so the account's own name is the wrong name to greet
    // somebody by. The registry record is who the screen below is about, and the
    // header should agree with it. The account name remains the fallback for the
    // moment before a farmer has been chosen.
    final name =
        '${_record.value?['farmer_name'] ?? ''}'.trim().isNotEmpty == true
        ? '${_record.value!['farmer_name']}'.trim()
        : widget.session.name;
    final reports = _reports.value ?? const [];

    return Scaffold(
      appBar: AppBar(
        toolbarHeight: 64,
        title: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Text(
              tr('नमस्कार', 'Hello'),
              style: Theme.of(context).textTheme.labelMedium,
            ),
            Text(
              name ?? 'eSurvey',
              maxLines: 1,
              overflow: TextOverflow.ellipsis,
            ),
          ],
        ),
        actions: [
          // Switching who the phone is working for. A household files for one
          // person and then the next, and signing out in between would cost
          // an SMS and a code each time.
          IconButton(
            tooltip: tr('दुसरा शेतकरी निवडा', 'Switch farmer'),
            onPressed: widget.onSwitchFarmer,
            icon: const Icon(Icons.people_alt_outlined),
          ),
          PopupMenuButton<String>(
            tooltip: tr('अधिक', 'More'),
            icon: const Icon(Icons.more_vert),
            onSelected: (v) {
              if (v == 'help') {
                Navigator.of(context).push(
                  MaterialPageRoute<void>(
                    builder: (_) => HelpScreen(voice: widget.voice),
                  ),
                );
              } else if (v == 'signout') {
                unawaited(_confirmSignOut());
              }
            },
            itemBuilder: (_) => [
              PopupMenuItem(
                value: 'help',
                child: ListTile(
                  leading: const Icon(Icons.help_outline),
                  title: Text(tr('मदत', 'Help')),
                  contentPadding: EdgeInsets.zero,
                ),
              ),
              PopupMenuItem(
                value: 'signout',
                child: ListTile(
                  leading: const Icon(Icons.logout),
                  title: Text(tr('बाहेर पडा', 'Sign out')),
                  contentPadding: EdgeInsets.zero,
                ),
              ),
            ],
          ),
          const SizedBox(width: 4),
        ],
      ),
      body: RefreshIndicator(
        onRefresh: () => _load(refresh: true),
        child: ListView(
          padding: Space.page,
          children: [
            // Above the stuck card: this is work the farmer can actually
            // finish, and the other one is work only the office can.
            for (final again in _recordAgain)
              _RecordAgainCard(
                again: again,
                onRecord: () => _recordRefusedAgain(again),
              ),
            if (_stuck.any)
              _StuckCard(
                stuck: _stuck,
                onRetry: () async {
                  await widget.store.retryStuck();
                  await _refreshOutstanding();
                  unawaited(widget.sync.sendNow());
                },
              ),
            // Below the work that is actually owed: a boundary is useful and
            // never urgent, and a farmer with a claim half-filed should not
            // be offered a survey first.
            for (final parcel in _unmapped)
              _BoundaryCard(
                parcel: parcel,
                onWalk: () => _walkBoundary(parcel),
              ),
            for (final row in _unfinished)
              _UnfinishedCard(
                journey: row.journey,
                progress: row.progress,
                onResume: () => _resume(row.journey),
                // Only an unfiled form can be thrown away. Once filed, the
                // report is in the queue and its evidence is what is owed.
                onDiscard: row.journey.stage == 'form'
                    ? () async {
                        await widget.store.deleteJourney(row.journey.id);
                        await _refreshOutstanding();
                      }
                    : null,
              ),
            if (_outstanding.any)
              _QueueCard(
                outstanding: _outstanding,
                upgradeRequired: _upgradeRequired,
                progress: _progress,
                onSendNow: () => unawaited(widget.sync.sendNow()),
              ),
            SectionHeader(tr('जाहीर आपत्ती', 'Declared calamities')),
            // Narrowed to this farmer's district. The office sends every open
            // event in the state -- a farmer holds no jurisdiction to be
            // filtered by -- and the heading above promises their area.
            _EventsCard(
              events: Cached(
                eventsForFarmer(_events.value ?? const [], _record.value),
                _events.at,
                fresh: _events.fresh,
              ),
              parcels: _parcels,
              onFile: _fileReport,
            ),
            SectionHeader(tr('माझी जमीन', 'My land')),
            if (_parcels.isEmpty)
              EmptyState(
                icon: Icons.landscape_outlined,
                message: tr(
                  'तुमच्या जमिनीची माहिती अजून मिळालेली नाही.',
                  'The registry has not sent your land records yet.',
                ),
              )
            else
              for (final parcel in _parcels)
                _ParcelCard(
                  parcel: parcel as Map<String, dynamic>,
                  store: widget.store,
                  onTrace: () => _traceBoundary(parcel),
                  // The first open calamity, which is what the crops shown should
                  // be narrowed to. A farmer reading their land record during a
                  // drought survey wants this season's crops, not last rabi's.
                  event: _openEvent,
                ),
            if (_record.age != null) _Age(_record.age!),
            SectionHeader(tr('माझ्या नोंदी', 'My reports')),
            if (reports.isEmpty)
              EmptyState(
                icon: Icons.description_outlined,
                message: tr(
                  'अजून कोणतीही नोंद पाठवलेली नाही.',
                  'You have not sent any reports yet.',
                ),
              )
            else
              for (final report in reports)
                ReportCard(
                  report: report as Map<String, dynamic>,
                  onOpen: () => unawaited(_openProgress(report)),
                  onReport: _decided(report)
                      ? () => unawaited(_openReport(report))
                      : null,
                  onAppeal: _appealable(report)
                      ? () => unawaited(_appeal(report))
                      : null,
                ),
          ],
        ),
      ),
    );
  }

  /// Whether the claim has a decision, and so an assessment report.
  bool _decided(dynamic report) =>
      const {'adjudicated', 'closed'}.contains('${(report as Map)['state']}');

  /// The farmer's copy of the decided claim's assessment report.
  Future<void> _openReport(Map<String, dynamic> report) async {
    final farmerId = widget.store.farmer ?? widget.session.farmerId ?? '';
    final grant = farmerId.isEmpty
        ? null
        : await widget.store.grantFor(farmerId);
    if (!mounted) return;
    await Navigator.of(context).push(
      MaterialPageRoute<void>(
        builder: (_) => AssessmentReportScreen(
          data: widget.data,
          reportId: '${report['id'] ?? ''}',
          reference: '${report['reference'] ?? ''}',
          grant: grant,
        ),
      ),
    );
  }

  /// Whether there is a decision to object to.
  ///
  /// The server's own rule, applied here so a farmer is not offered a button
  /// that will be refused: nothing is appealable until officers have recorded
  /// findings, and a withdrawn report has no decision to argue with.
  bool _appealable(dynamic report) {
    final state = '${(report as Map)['state'] ?? ''}';
    return state == 'verified' || state == 'adjudicated' || state == 'closed';
  }

  Future<void> _appeal(dynamic report) async {
    final filed = await Navigator.of(context).push<bool>(
      MaterialPageRoute<bool>(
        builder: (_) => AppealScreen(
          store: widget.store,
          voice: widget.voice,
          sync: widget.sync,
          report: report as Map<String, dynamic>,
        ),
      ),
    );
    if (filed == true) await _refreshOutstanding();
  }

  /// Back into a capture sequence that was interrupted.
  ///
  /// The screen resumes from what is already on the phone -- it reads the
  /// photographs the draft has and starts at the first stage still short --
  /// so this hands it the draft id and nothing else.
  /// Walk one field's edge, from the card above.
  Future<void> _walkBoundary(Map<String, dynamic> parcel) async {
    await Navigator.of(context).push(
      MaterialPageRoute<void>(
        builder: (_) => BoundaryScreen(
          store: widget.store,
          voice: widget.voice,
          positioning: widget.positioning,
          sync: widget.sync,
          farmId: '${parcel['farm_id'] ?? ''}',
          surveyNumber: '${parcel['survey_number'] ?? ''}',
          registryExtentHa: double.tryParse('${parcel['owner_extent'] ?? ''}'),
        ),
      ),
    );
    if (!mounted) return;
    // The outline is queued for the office, which is what turns it into four
    // placed points and lets the satellite evidence run.
    unawaited(widget.sync.sendNow());
    await _load(refresh: true);
  }

  /// Walk the points the office refused, for a claim it already has.
  ///
  /// # What this is for
  ///
  /// A clip carries the position it was taken at, so a clip refused over that
  /// position is refused for ever -- and the app's only answer used to be the
  /// retry button on the stuck card. A filed claim's journey is `done` and its
  /// draft is `sent`, so it appears in no list of unfinished work and the walk
  /// could not be reached at all. One trial claim reached the office with one
  /// clip of four for exactly this reason, while three sat on the phone being
  /// refused on every pass.
  ///
  /// Nothing is deleted here. The refused clips stay on the phone until a
  /// replacement is actually recorded, one point at a time, by the walk itself:
  /// stationsComplete does not count a clip the office refused over its
  /// position, so those points are already the ones the walk will resume at.
  ///
  /// It was the other way round for one build. The rows were deleted before the
  /// walk opened, and a farmer who opened it and closed it again without
  /// recording was left with no clips and no card -- the card is built from
  /// those very rows -- so nothing on the phone said the claim was short of its
  /// evidence. Three recordings went that way.
  ///
  /// The accepted point still counts for spacing: recordedStationPoints reads
  /// every row of the claim, sent or not, so a replacement cannot be taken where
  /// the accepted one was.
  Future<void> _recordRefusedAgain(RecordAgain again) async {
    final row = await widget.store.journey(again.draftLocalId);
    if (!mounted) return;
    if (row == null) {
      // No journey to walk. The rows would otherwise sit on the phone for
      // ever, so say so rather than leaving a button that does nothing.
      await _saySoftly(
        tr(
          'ही नोंद या फोनवर सापडली नाही. तालुका कार्यालयात विचारा.',
          'This claim is no longer on this phone. Ask at the taluka office.',
        ),
      );
      return;
    }

    await Navigator.of(context).push(
      evidenceRoute(
        store: widget.store,
        voice: widget.voice,
        positioning: widget.positioning,
        journey: Journey.fromRow(row),
        sync: widget.sync,
        // The office has this claim. The replacements go up against the report
        // id the draft already carries; nothing is filed a second time.
        filed: true,
      ),
    );
    if (!mounted) return;
    await _refreshOutstanding();
    unawaited(widget.sync.sendNow());
  }

  /// A short message for something the farmer can do nothing about.
  Future<void> _saySoftly(String message) async {
    if (!mounted) return;
    ScaffoldMessenger.of(
      context,
    ).showSnackBar(SnackBar(content: Text(message)));
  }

  Future<void> _resume(Journey journey) async {
    if (journey.stage == 'form') {
      await Navigator.of(context).push(
        MaterialPageRoute<void>(
          builder: (_) => FileReportScreen(
            store: widget.store,
            voice: widget.voice,
            positioning: widget.positioning,
            sync: widget.sync,
            api: widget.api,
            farmerId: widget.store.farmer ?? widget.session.farmerId ?? '',
            event: journey.event!,
            parcels: [journey.parcel!],
            resume: journey,
          ),
        ),
      );
    } else if (journey.atReview) {
      // The evidence is gathered; what is left is for the farmer to look at
      // it and send.
      await Navigator.of(context).push(
        MaterialPageRoute<void>(
          builder: (_) => ReviewScreen(
            store: widget.store,
            voice: widget.voice,
            positioning: widget.positioning,
            sync: widget.sync,
            journey: journey,
          ),
        ),
      );
    } else {
      // Back to the exact step: the point of the walk that still needs its
      // clip, or the photograph still missing. Finishing it lands on the
      // review screen, which is where a claim is sent.
      await Navigator.of(context).push(
        evidenceRoute(
          store: widget.store,
          voice: widget.voice,
          positioning: widget.positioning,
          journey: journey,
          sync: widget.sync,
        ),
      );
      if (!mounted) return;
      final row = await widget.store.journey(journey.id);
      if (!mounted || row == null) return;
      final now = Journey.fromRow(row);
      if (now.atReview) {
        await Navigator.of(context).push(
          MaterialPageRoute<void>(
            builder: (_) => ReviewScreen(
              store: widget.store,
              voice: widget.voice,
              positioning: widget.positioning,
              sync: widget.sync,
              journey: now,
            ),
          ),
        );
      }
    }
    // The card should go once the sequence is complete, and stay if the
    // farmer left it short again.
    await _refreshOutstanding();
  }

  /// The first calamity open for this farmer, or null where none is.
  ///
  /// Only for narrowing what the land screens show to the season being surveyed.
  /// Nothing is claimed from here, so the first is enough: events open at once in
  /// one village are the exception and they share a season.
  Map<String, dynamic>? get _openEvent {
    final open = eventsForFarmer(_events.value ?? const [], _record.value);
    for (final e in open) {
      if (e is Map) return e.cast<String, dynamic>();
    }
    return null;
  }

  Future<void> _fileReport(
    Map<String, dynamic> event,
    String farmId,
    Map<String, dynamic> crop,
  ) async {
    // The farmer picked on this handset, whose parcels these are -- not the
    // account's own, which is a different person once the phone works for a
    // second farmer.
    final farmerId = widget.store.farmer ?? widget.session.farmerId;
    if (farmerId == null) return;

    if (_parcels.isEmpty) {
      // Nothing to file against. Said here rather than on a form with an
      // empty picker, which reads as a broken screen.
      ScaffoldMessenger.of(context).showSnackBar(
        const SnackBar(
          content: Text(
            'तुमच्या जमिनीची माहिती अजून मिळालेली नाही · '
            'Your land records have not arrived yet',
          ),
        ),
      );
      return;
    }

    await Navigator.of(context).push(
      MaterialPageRoute<void>(
        builder: (_) => FileReportScreen(
          store: widget.store,
          voice: widget.voice,
          positioning: widget.positioning,
          sync: widget.sync,
          api: widget.api,
          farmerId: farmerId,
          event: event,
          parcels: _parcels,
          // Carried so the form opens on what the farmer already tapped. An empty
          // crop means the registry recorded none and the form must ask.
          startOn: crop.isEmpty ? null : (farmId: farmId, crop: crop),
        ),
      ),
    );
    if (mounted) unawaited(_load(refresh: true));
  }

  Future<void> _traceBoundary(Map<String, dynamic> parcel) async {
    await Navigator.of(context).push(
      MaterialPageRoute<void>(
        builder: (_) => BoundaryScreen(
          store: widget.store,
          voice: widget.voice,
          positioning: widget.positioning,
          sync: widget.sync,
          farmId: parcel['farm_id'] as String,
          surveyNumber: parcel['survey_number'] as String? ?? '',
          registryExtentHa: double.tryParse('${parcel['owner_extent'] ?? ''}'),
        ),
      ),
    );
    if (mounted) {
      setState(() {});
      unawaited(_refreshOutstanding());
    }
  }

  Future<void> _confirmSignOut() async {
    // Evidence for a finished claim is not handled here.
    //
    // It is guarded in the sign-out itself, because this is not the only way
    // out: the farmer picker has a logout icon that signs out on one tap with
    // no dialog at all. A check in this dialog would have left that path open,
    // which is the shape of bug that produced the trial's stranded recordings
    // in the first place.
    //
    // What is left here is the ordinary case, and the wording that was wrong
    // about it: any waiting work used to be described as "keeps sending from
    // this phone after you sign out", which holds only while the signed-out
    // account's parked credentials survive.
    final go = await showDialog<bool>(
      context: context,
      builder: (ctx) => AlertDialog(
        title: Text(tr('बाहेर पडायचे?', 'Sign out?')),
        // A handset in a village is borrowed, and a farmer handing it on needs
        // to know what happens to a draft they have not finished.
        content: Text(
          _outstanding.any
              ? tr(
                  'तुमची अपूर्ण नोंद या फोनवर राहील आणि पुढच्या वेळी त्याच '
                      'नंबरने साइन इन केल्यावर पुन्हा दिसेल. ती इतर कोणाला '
                      'दिसणार नाही.',
                  'Your unfinished claim stays on this phone and comes back '
                      'when you sign in again with the same number. Nobody '
                      'else can see it.',
                )
              : tr(
                  'या फोनवरून तुम्ही बाहेर पडाल.',
                  'You will be signed out of this phone.',
                ),
        ),
        actions: [
          TextButton(
            onPressed: () => Navigator.of(ctx).pop(false),
            child: Text(tr('नको', 'Cancel')),
          ),
          FilledButton(
            onPressed: () => Navigator.of(ctx).pop(true),
            child: Text(tr('बाहेर पडा', 'Sign out')),
          ),
        ],
      ),
    );

    if (go == true) widget.onSignOut();
  }
}

/// Work that gave up.
///
/// This is the only place in the app a farmer is told that something stopped
/// trying. Without it the row simply stops being counted, and the farmer
/// concludes the report went. Silence that reads as success is the worst thing
/// this app can produce, so this is the one danger-toned card on the screen.
/// A field with no outline, offered as work for a dry day.
class _BoundaryCard extends StatelessWidget {
  const _BoundaryCard({required this.parcel, required this.onWalk});

  final Map<String, dynamic> parcel;
  final VoidCallback onWalk;

  @override
  Widget build(BuildContext context) {
    final survey = '${parcel['survey_number'] ?? ''}';
    return Notice(
      tone: Tone.info,
      icon: Icons.map_outlined,
      title: tr(
        'सर्वे $survey ची हद्द नोंदवा',
        'Record the boundary of survey $survey',
      ),
      message: tr(
        'या शेताची हद्द नोंदलेली नाही. हद्द नोंदवली की उपग्रहाचा पुरावा '
            'तपासता येतो. काही मिनिटे लागतात, कोरड्या दिवशी करा.',
        'This field has no recorded outline. With one, the satellite evidence '
            'can be checked too. It takes a few minutes; do it on a dry day.',
      ),
      actions: [
        OutlinedButton.icon(
          onPressed: onWalk,
          icon: const Icon(Icons.map_outlined, size: 20),
          label: Text(tr('हद्द नोंदवा', 'Walk the edge')),
        ),
      ],
    );
  }
}

class _StuckCard extends StatelessWidget {
  const _StuckCard({required this.stuck, required this.onRetry});

  final Outstanding stuck;
  final Future<void> Function() onRetry;

  @override
  Widget build(BuildContext context) {
    return Notice(
      tone: Tone.danger,
      icon: Icons.error_outline,
      title: tr(
        '${_count(stuck)} पाठवता आले नाही',
        '${_count(stuck)} could not be sent',
      ),
      message: tr(
        'हे अजून कार्यालयात पोहोचलेले नाही. पुन्हा प्रयत्न करा, किंवा '
            'तालुका कार्यालयात विचारा.',
        'This has not reached the office. Try again, or ask at the taluka '
            'office.',
      ),
      actions: [
        FilledButton.icon(
          onPressed: () => unawaited(onRetry()),
          icon: const Icon(Icons.refresh, size: 20),
          label: Text(tr('पुन्हा प्रयत्न करा', 'Try again')),
        ),
      ],
    );
  }
}

/// Clips the office refused over where they were recorded.
///
/// The one card in this app that asks the farmer to walk back into their field.
/// It is worth that: the alternative is a claim assessed on one clip instead of
/// four, and the ground-vision measurement those clips feed carries more weight
/// than radar or NDVI.
///
/// The message says what was wrong and what to do about it, in that order. A
/// farmer who is told only "could not be sent" reaches for the button that
/// cannot help -- which is what happened, three times in one evening, on the
/// first handset to hit this.
class _RecordAgainCard extends StatelessWidget {
  const _RecordAgainCard({required this.again, required this.onRecord});

  final RecordAgain again;
  final Future<void> Function() onRecord;

  @override
  Widget build(BuildContext context) {
    final n = again.count;
    return Notice(
      tone: Tone.warning,
      icon: Icons.videocam_outlined,
      title: tr(
        '$n व्हिडिओ पुन्हा काढावे लागतील',
        n == 1
            ? '1 recording must be taken again'
            : '$n recordings must be '
                  'taken again',
      ),
      message: _why(again.code),
      actions: [
        FilledButton.icon(
          onPressed: () => unawaited(onRecord()),
          icon: const Icon(Icons.videocam, size: 20),
          label: Text(tr('पुन्हा काढा', 'Record again')),
        ),
      ],
    );
  }

  /// Why the office refused, and what the farmer does about it.
  ///
  /// Written from the code rather than passed through the office's own
  /// sentence: the server's wording is exact but it is written for an operator
  /// ("0 m from the last one, and this field needs 14 m"), and a farmer needs
  /// the instruction, not the measurement.
  static String _why(String code) => switch (code) {
    'SAME_POSITION' => tr(
      'हे व्हिडिओ एकाच जागी काढले गेले. शेताच्या वेगवेगळ्या भागांत जाऊन '
          'पुन्हा काढा.',
      'These were recorded at the same spot. Walk to different parts of the '
          'field and record them again.',
    ),
    'OUTSIDE_PARCEL' => tr(
      'हे व्हिडिओ तुमच्या शेताच्या हद्दीबाहेर काढले गेले. शेतात उभे राहून '
          'पुन्हा काढा.',
      'These were recorded outside your field boundary. Stand inside the '
          'field and record them again.',
    ),
    'POINT_TAKEN_IN_SURVEY' => tr(
      'तुमच्या सर्व्हे नंबरमध्ये या ठिकाणी आधीच चित्रीकरण झाले आहे. तुमच्या '
          'वाटणीत जाऊन व्हिडिओ काढा.',
      'This spot has already been recorded under your survey number. Record on '
          'your own share of it.',
    ),
    'AWAY_FROM_FIELD' => tr(
      'हे व्हिडिओ या नोंदीच्या शेतापासून दूर काढले गेले. शेतावर जाऊन पुन्हा '
          'काढा.',
      'These were recorded away from this claim\'s field. Go to the field and '
          'record them again.',
    ),
    // Not reached while this list matches recordAgainRefusals, and written
    // anyway: a code added there and forgotten here must still say something
    // true rather than showing an empty card.
    _ => tr(
      'कार्यालयाने हे व्हिडिओ नाकारले. शेतावर जाऊन पुन्हा काढा.',
      'The office refused these recordings. Go to the field and record them '
          'again.',
    ),
  };
}

/// What a count of queued work is made of: "1 नोंद, 5 फोटो/व्हिडिओ".
String _count(Outstanding o) => [
  if (o.boundaries > 0) tr('${o.boundaries} हद्द', '${o.boundaries} boundary'),
  if (o.drafts > 0) tr('${o.drafts} नोंद', '${o.drafts} report'),
  if (o.captures > 0) tr('${o.captures} फोटो/व्हिडिओ', '${o.captures} media'),
  if (o.appeals > 0) tr('${o.appeals} हरकत', '${o.appeals} appeal'),
].join(', ');

/// A claim whose evidence was never finished.
///
/// A flat battery, a dropped phone, a back press at the wrong moment. The
/// capture sequence is otherwise only reachable by going forward from the
/// report form, so without this the walk to the field is simply lost.
class _UnfinishedCard extends StatelessWidget {
  const _UnfinishedCard({
    required this.journey,
    required this.progress,
    required this.onResume,
    this.onDiscard,
  });

  final Journey journey;
  final EvidenceProgress? progress;
  final VoidCallback onResume;
  final Future<void> Function()? onDiscard;

  @override
  Widget build(BuildContext context) {
    final p = progress;
    final text = Theme.of(context).textTheme;
    return AppCard(
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Row(
            children: [
              IconBadge(
                journey.stage == 'form'
                    ? Icons.edit_note
                    : Icons.photo_camera_outlined,
                color: Palette.moderateFg,
                background: Palette.moderateBg,
              ),
              const SizedBox(width: Space.md),
              Expanded(
                child: Column(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    Text(_headline, style: text.titleMedium),
                    if (journey.surveyNumber.isNotEmpty)
                      Text(
                        tr(
                          'सर्व्हे नं. ${journey.surveyNumber}',
                          'Survey no. ${journey.surveyNumber}',
                        ),
                        style: text.bodySmall,
                      ),
                  ],
                ),
              ),
            ],
          ),
          if (p != null && p.total > 0 && !journey.atReview) ...[
            const SizedBox(height: Space.md),
            ClipRRect(
              borderRadius: BorderRadius.circular(99),
              child: LinearProgressIndicator(
                value: p.done / p.total,
                minHeight: 6,
              ),
            ),
          ],
          const SizedBox(height: Space.md),
          Text(_detail, style: text.bodyMedium),
          const SizedBox(height: Space.md),
          Row(
            children: [
              Expanded(
                child: FilledButton(
                  onPressed: onResume,
                  child: Text(tr('पुढे सुरू ठेवा', 'Continue')),
                ),
              ),
              if (onDiscard != null) ...[
                const SizedBox(width: Space.sm),
                TextButton(
                  onPressed: () => unawaited(onDiscard!()),
                  style: TextButton.styleFrom(
                    foregroundColor: Palette.severeFg,
                  ),
                  child: Text(tr('रद्द करा', 'Discard')),
                ),
              ],
            ],
          ),
        ],
      ),
    );
  }

  String get _headline {
    final p = progress;
    if (journey.atReview) return tr('पाठवायचे बाकी', 'Ready to send');
    if (journey.stage == 'form' || p == null) {
      return tr('माहिती अपूर्ण', 'Form not finished');
    }
    return tr(
      'शेतातील ठिकाणे ${p.done}/${p.total}',
      'Field points ${p.done}/${p.total}',
    );
  }

  String get _detail {
    if (journey.atReview) {
      return tr(
        'पुरावा पूर्ण आहे. नोंद तपासून पाठवा.',
        'The evidence is complete. Check the claim and send it.',
      );
    }
    if (journey.stage == 'form' || progress == null) {
      return tr(
        'भरलेली माहिती फोनवर जतन आहे. तिथूनच पुढे भरा.',
        'What you filled in is saved. Carry on from where you stopped.',
      );
    }
    return tr(
      'शेतात जाऊन पुढच्या ठिकाणापासून सुरू करा.',
      'Go to the field and carry on from the next point.',
    );
  }
}

/// What is still on the phone.
///
/// Shown only when there is something, because a permanent indicator that
/// usually says "all fine" is one people stop reading.
class _QueueCard extends StatelessWidget {
  const _QueueCard({
    required this.outstanding,
    required this.onSendNow,
    this.progress,
    this.upgradeRequired = false,
  });

  final Outstanding outstanding;
  final VoidCallback onSendNow;

  /// Bytes acknowledged by the office, over bytes still to go.
  ///
  /// Shown because the count above it barely moves: a claim is four clips, and
  /// the first can take twenty minutes on a village connection while the words
  /// stay identical. A farmer watching that concludes the app has stopped.
  final ({int sent, int total})? progress;
  final bool upgradeRequired;

  @override
  Widget build(BuildContext context) {
    return Notice(
      tone: upgradeRequired ? Tone.warning : Tone.info,
      icon: upgradeRequired ? Icons.system_update : Icons.cloud_upload_outlined,
      title: tr(
        '${_count(outstanding)} पाठवायचे बाकी',
        '${_count(outstanding)} waiting to send',
      ),
      // "Even if you close the app" is the part that matters: a farmer who
      // believes it stops carrying the phone around with the app open.
      message: upgradeRequired
          ? tr(
              'हे पाठवण्यासाठी ॲप अपडेट करा. तुमचे काम फोनवर सुरक्षित आहे.',
              'Update the app to send this. Your work is safe on this phone.',
            )
          : tr(
              'फोनवर सुरक्षित आहे. ॲप बंद केले तरी नेटवर्क आल्यावर आपोआप जाईल.',
              'Safe on this phone. It sends by itself when there is a '
                  'signal, even if you close the app.',
            ),
      // Above the button: what is happening, before what to do about it.
      below: upgradeRequired ? null : _bar(context),
      actions: [
        if (!upgradeRequired)
          OutlinedButton.icon(
            onPressed: onSendNow,
            icon: const Icon(Icons.sync, size: 20),
            label: Text(tr('आत्ता पाठवा', 'Send now')),
          ),
      ],
    );
  }

  /// The bar, and the megabytes under it.
  ///
  /// Determinate only where there is something to be determinate about. A claim
  /// whose evidence has no recorded size -- a form on its own, or a row from
  /// before sizes were stored -- gets no bar rather than one stuck at zero, which
  /// reads as broken.
  Widget? _bar(BuildContext context) {
    final p = progress;
    if (p == null || p.total <= 0) return null;

    final done = p.sent.clamp(0, p.total);
    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        ClipRRect(
          borderRadius: BorderRadius.circular(99),
          child: LinearProgressIndicator(value: done / p.total, minHeight: 6),
        ),
        const SizedBox(height: 6),
        Text(
          tr(
            '${_mb(done)} पैकी ${_mb(p.total)} MB पाठवले',
            '${_mb(done)} of ${_mb(p.total)} MB sent',
          ),
          style: Theme.of(context).textTheme.bodySmall,
        ),
      ],
    );
  }

  static String _mb(int bytes) => (bytes / (1024 * 1024)).toStringAsFixed(1);
}

/// The events a farmer may file against.
/// One thing a farmer can claim: a parcel, and the crop on it.
///
/// The crop is null where the registry records none for that land. That is not an
/// error and must not be treated as one -- an incomplete land record is not a reason
/// to refuse somebody a claim, and the form asks what is growing in that one case.
typedef Claimable = ({
  String farmId,
  Map<String, dynamic>? crop,
  String cropName,
});

class _EventsCard extends StatelessWidget {
  const _EventsCard({
    required this.events,
    required this.parcels,
    required this.onFile,
  });

  final Cached<List<dynamic>> events;

  /// The farmer's land, because a calamity is claimed crop by crop.
  ///
  /// One farm carries several crops -- soybean on part of it and udad on another --
  /// and each is sown, damaged and paid separately, so each is its own claim. The
  /// card offered one "report damage" button for the whole event, which sent a
  /// farmer with two crops to a form where the second one could not be reached.
  final List<dynamic> parcels;

  final void Function(
    Map<String, dynamic> event,
    String farmId,
    Map<String, dynamic> crop,
  )
  onFile;

  /// Every parcel-and-crop pair this event can be claimed for.
  ///
  /// Narrowed by season and year the same way the form narrows it, so the home
  /// screen and the form agree about what is claimable -- a button that leads to a
  /// form which then offers something different is worse than no button.
  ///
  /// Not narrowed by the crops the event names. A crop the event did not list is a
  /// discrepancy the village pair settle, never a refusal, so hiding it here would
  /// be the app refusing what the office accepts.
  List<Claimable> _claimable(Map<String, dynamic> event) {
    final out = <Claimable>[];
    for (final p in parcels) {
      if (p is! Map) continue;
      final parcel = p.cast<String, dynamic>();
      final farmId = '${parcel['farm_id'] ?? ''}';
      if (farmId.isEmpty) continue;

      final crops = cropsFor(parcel, event);
      if (crops.isEmpty) {
        out.add((farmId: farmId, crop: null, cropName: ''));
        continue;
      }
      for (final crop in crops) {
        out.add((
          farmId: farmId,
          crop: crop,
          cropName: '${crop['crop_name'] ?? ''}',
        ));
      }
    }
    return out;
  }

  @override
  Widget build(BuildContext context) {
    final list = events.value ?? const [];
    final text = Theme.of(context).textTheme;

    if (list.isEmpty) {
      return EmptyState(
        icon: Icons.event_available_outlined,
        message: tr(
          'सध्या तुमच्या भागात कोणतीही आपत्ती जाहीर झालेली नाही.',
          'No calamity has been declared for your area.',
        ),
      );
    }

    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        for (final event in list)
          AppCard(
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                Row(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    const IconBadge(
                      Icons.thunderstorm_outlined,
                      color: Palette.severeFg,
                      background: Palette.severeBg,
                    ),
                    const SizedBox(width: Space.md),
                    Expanded(
                      child: Column(
                        crossAxisAlignment: CrossAxisAlignment.start,
                        children: [
                          Text(
                            '${(event as Map)['title'] ?? ''}',
                            style: text.titleMedium,
                          ),
                          const SizedBox(height: 2),
                          Text(
                            '${formatDay(event['incident_start'])} – '
                            '${formatDay(event['incident_end'])}',
                            style: text.bodySmall,
                          ),
                        ],
                      ),
                    ),
                  ],
                ),
                const SizedBox(height: Space.md),
                // One button per crop this event can be claimed for.
                //
                // Where the registry knows of no crop on this land the farmer still
                // gets a way in, because the land record being incomplete is not a
                // reason to refuse somebody a claim -- the form then asks what is
                // growing, which is the one case where it does.
                for (final choice in _claimable(event.cast<String, dynamic>()))
                  Padding(
                    padding: const EdgeInsets.only(bottom: Space.sm),
                    child: BigButton(
                      label: choice.crop == null
                          ? tr('नुकसान कळवा', 'Report damage')
                          : tr(
                              '${choice.cropName} — नुकसान कळवा',
                              '${choice.cropName} — report damage',
                            ),
                      icon: Icons.add,
                      onPressed: () => onFile(
                        event.cast<String, dynamic>(),
                        choice.farmId,
                        choice.crop ?? const {},
                      ),
                    ),
                  ),
              ],
            ),
          ),
        if (events.age != null) _Age(events.age!),
      ],
    );
  }
}

/// One parcel, and whether it has a boundary yet.
class _ParcelCard extends StatefulWidget {
  const _ParcelCard({
    required this.parcel,
    required this.store,
    required this.onTrace,
    this.event,
  });

  final Map<String, dynamic> parcel;
  final Store store;
  final VoidCallback onTrace;

  /// The survey this land might be claimed under, so the crops shown are narrowed
  /// to its season exactly as the claim form narrows them. Null where no calamity
  /// is open, and then every crop the registry holds is shown -- which is the
  /// honest answer to "what does the record say about my field" when nothing is
  /// being claimed.
  final Map<String, dynamic>? event;

  @override
  State<_ParcelCard> createState() => _ParcelCardState();
}

class _ParcelCardState extends State<_ParcelCard> {
  bool? _hasBoundary;

  @override
  void initState() {
    super.initState();
    unawaited(_check());
  }

  @override
  void didUpdateWidget(_ParcelCard old) {
    super.didUpdateWidget(old);
    unawaited(_check());
  }

  Future<void> _check() async {
    // The phone first. A farmer who walked this an hour ago with no signal
    // must not be asked to walk it again, and the registry's answer will not
    // reflect their walk until it has been sent.
    final local = await widget.store.boundary(
      widget.parcel['farm_id'] as String,
    );
    final fromRegistry = widget.parcel['plot_geometry'] != null;

    if (!mounted) return;
    setState(() => _hasBoundary = local != null || fromRegistry);
  }

  @override
  Widget build(BuildContext context) {
    final parcel = widget.parcel;
    final text = Theme.of(context).textTheme;
    final extent = '${parcel['owner_extent'] ?? ''}';

    return AppCard(
      // The card opens what the record says about this field.
      //
      // A farmer has never seen these figures, and the claim is measured against
      // them: the sown area is the registry's and not theirs to type, and the
      // walk's spacing is derived from it. Somebody whose payout comes back smaller
      // than they expected is owed the numbers it was worked out from, while the
      // survey window is still open and the taluka office can still correct them.
      onTap: () => Navigator.of(context).push(
        MaterialPageRoute<void>(
          builder: (_) => ParcelDetailScreen(
            parcel: widget.parcel,
            crops: cropsFor(widget.parcel, widget.event),
            hasBoundary: _hasBoundary,
          ),
        ),
      ),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Row(
            children: [
              const IconBadge(Icons.landscape_outlined),
              const SizedBox(width: Space.md),
              Expanded(
                child: Column(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    Text(
                      tr(
                        'सर्व्हे नं. ${parcel['survey_number'] ?? ''}',
                        'Survey no. ${parcel['survey_number'] ?? ''}',
                      ),
                      style: text.titleMedium,
                    ),
                    if (extent.isNotEmpty)
                      Text(
                        tr('$extent हेक्टर', '$extent ha'),
                        style: text.bodySmall,
                      ),
                  ],
                ),
              ),
              // No "boundary saved" tag.
              //
              // It told a farmer something they cannot act on, on the card they
              // look at most. A boundary that is missing is worth saying, because
              // there is a button under it and five minutes of work to do; a
              // boundary that is present is simply how a field normally is, and
              // labelling the ordinary case leaves every card shouting a word that
              // carries no decision.
              const Icon(Icons.chevron_right, color: Palette.inkMuted),
            ],
          ),
          if (_hasBoundary == false) ...[
            const SizedBox(height: Space.md),
            Text(
              tr(
                'या शेताची हद्द अजून नोंदवलेली नाही. एकदाच करायचे आहे, सुमारे '
                    'पाच मिनिटे लागतात.',
                'This field has no boundary yet. It takes about five '
                    'minutes, once.',
              ),
              style: text.bodyMedium?.copyWith(color: Palette.inkMuted),
            ),
            const SizedBox(height: Space.md),
            OutlinedButton.icon(
              onPressed: widget.onTrace,
              icon: const Icon(Icons.map_outlined, size: 20),
              label: Text(tr('हद्द नोंदवा', 'Record boundary')),
            ),
          ],
        ],
      ),
    );
  }
}

/// How old a cached answer is.
class _Age extends StatelessWidget {
  const _Age(this.age);

  final String age;

  @override
  Widget build(BuildContext context) {
    return Padding(
      padding: const EdgeInsets.only(left: 4, bottom: Space.sm),
      child: Text(
        Language.of(age),
        style: Theme.of(context).textTheme.labelSmall,
      ),
    );
  }
}
