import 'dart:async';
import 'dart:convert';

import 'package:flutter/material.dart';

import '../capture/station_guide.dart';
import '../core/api.dart';
import '../core/dates.dart';
import '../core/background.dart';
import '../core/farmer_data.dart';
import '../core/language.dart';
import '../core/reminders.dart';
import '../core/parcel_location.dart';
import '../core/db.dart';
import '../core/sensors.dart';
import '../core/sync.dart';
import '../core/voice.dart';
import '../ui/theme.dart';
import 'journey.dart';
import 'review_screen.dart';
import '../core/messages.dart';

/// Filing a report, before any photograph is taken.
///
/// # Why this is short
///
/// The platform's paper form asks for a dozen things. Most of them this app
/// already knows -- the farmer, the village, the crop, the survey number all
/// come from the registry, and the calamity and its dates come from the event
/// the district notified. Asking a farmer to re-enter what the office already
/// has is how a form gets abandoned halfway.
///
/// So there are three real questions: which of your fields, how much of it is
/// damaged, and do you accept the declaration. Everything else is carried.
///
/// # Why the damage figure is a farmer's estimate and labelled as one
///
/// They are standing in the field and nobody is better placed to say. It is
/// also the number an officer will check and the panel will weigh against
/// satellite evidence, so it is recorded as a claim rather than as a
/// measurement -- and the farmer is told that plainly, because a number they
/// think is final is a number they will argue about later.
class FileReportScreen extends StatefulWidget {
  const FileReportScreen({
    super.key,
    required this.store,
    required this.voice,
    required this.positioning,
    required this.sync,
    required this.api,
    required this.farmerId,
    required this.event,
    required this.parcels,
    this.onFiled,
    this.resume,
    this.startOn,
  });

  final Store store;
  final Voice voice;
  final Positioning positioning;
  final Sync sync;
  final Api api;

  final String farmerId;

  /// The event this report is filed against. A report with no event is a claim
  /// against nothing, for a calamity nobody declared.
  final Map<String, dynamic> event;

  final List<dynamic> parcels;

  /// The parcel and crop the farmer already chose, where they chose on the way in.
  ///
  /// The home screen offers one entry per claimable crop, so by the time this
  /// screen opens the choice is usually made -- and asking it again on the form
  /// would be asking somebody to repeat themselves. Null when the form is reached
  /// without one, which is a resumed draft or a build that does not offer crops.
  final ({String farmId, Map<String, dynamic> crop})? startOn;

  final VoidCallback? onFiled;

  /// A claim started earlier and interrupted, to continue rather than begin.
  final Journey? resume;

  @override
  State<FileReportScreen> createState() => _FileReportScreenState();
}

class _FileReportScreenState extends State<FileReportScreen> {
  Map<String, dynamic>? _parcel;

  /// Where the farmer is, relative to the parcel they picked.
  ///
  /// The app used to show only how precise its own fix was -- "±9 m" -- which
  /// says nothing about whether that fix is anywhere near the land. A farmer
  /// six hundred kilometres away could fill this whole form and photograph
  /// five things before the server refused any of it.
  ParcelFix? _where;
  ParcelShape _shape = const ParcelShape([]);
  StreamSubscription<PositionState>? _positions;

  /// Debug builds only, and never present in a release binary. Lets the whole
  /// walk be exercised from a desk instead of from Solapur.
  bool _simulate = false;

  /// The four points the server assigned for this parcel, fetched here where
  /// there is still a network and carried into the walk, which happens in a
  /// field.
  List<Station>? _stations;
  bool _fetchingStations = false;
  final _cropController = TextEditingController();

  /// Ground other claims on this survey number have already recorded.
  ///
  /// Fetched with the four points and written onto the journey, so the walk holds
  /// it with no signal.
  List<TakenPoint> _takenPoints = const [];

  final _sownController = TextEditingController();
  final _affectedController = TextEditingController();

  /// The crops the registry holds for the chosen parcel, narrowed to this
  /// event's season and year where it says.
  ///
  /// The crop is not typed any more. A farmer typing "soyabean" produces a
  /// crop the agronomic table cannot find, and the assessment then reports
  /// that no growth stage could be established -- for a spelling. The registry
  /// already carries crop_name, and the app has been receiving it per parcel
  /// all along without looking at it.
  List<Map<String, dynamic>> _registryCrops = const [];

  /// The sown area of the crop now chosen, as AgriStack records it.
  ///
  /// Blank where the registry encodes it ambiguously -- the office omits the figure
  /// rather than guessing, and a farmer then has the one case where they type it.
  /// Everywhere else this is the land record's own number and the form shows it
  /// rather than asking for it.
  void _setSownFromCrop() {
    final sown = _crop?['sown_area_ha'];
    _sownController.text = sown == null ? '' : '$sown';
  }

  /// Whether the registry gave a sown area for the chosen crop.
  ///
  /// The one condition under which the field may be typed into. See the field's
  /// own note for why it is otherwise read-only.
  bool get _sownIsFromRegistry => _crop?['sown_area_ha'] != null;

  /// Which of them this claim is for. Only ever chosen where the registry
  /// holds more than one crop on the parcel.
  Map<String, dynamic>? _crop;

  /// What the loss is being claimed as, from the event.
  ///
  /// Narrowed by the server to the categories this event's calamity permits,
  /// so nothing here can be chosen that the office will refuse afterwards.
  /// Null until the options are known.
  String? _lossCategory;

  double _damagePercent = 50;
  bool _declared = false;
  bool _saving = false;
  String? _error;

  final _cropFocus = FocusNode();
  final _sownFocus = FocusNode();
  final _affectedFocus = FocusNode();

  /// The journey row this form is saved into as the farmer types. Also the
  /// draft's local id once filed, so the evidence and the form are one claim.
  String? _journeyId;
  Timer? _saveSoon;

  @override
  void initState() {
    super.initState();

    // Where the farmer is, updated as they move. Started before the parcel is
    // chosen so the answer is ready the moment it is.
    _positions = widget.positioning.stream.listen((state) {
      if (!mounted) return;
      setState(
        () =>
            _where = locate(fix: state.fix, shape: _shape, simulate: _simulate),
      );
    });
    unawaited(widget.positioning.start());
    _speakPrompts();

    // One parcel is the ordinary case: choosing between one thing is not a
    // choice, so it is made for them. A resumed claim picks its own.
    final resume = widget.resume;
    if (resume != null) {
      final match = widget.parcels.where(
        (p) => (p as Map)['farm_id'] == resume.farmId,
      );
      if (match.isNotEmpty) _select(match.first, attach: false);
      _restore(resume);
    } else if (widget.startOn != null) {
      // The choice was made on the home screen, which offers one entry per
      // claimable crop. Made again here it would be a farmer answering the same
      // question twice on consecutive screens.
      final start = widget.startOn!;
      final match = widget.parcels.where(
        (p) => (p as Map)['farm_id'] == start.farmId,
      );
      if (match.isNotEmpty) {
        _select(match.first);
        _chooseCrop(start.crop);
      }
    } else if (widget.parcels.length == 1) {
      _select(widget.parcels.first);
    }
  }

  /// Settle on a crop, and with it the sown area the walk is sized from.
  ///
  /// One path for both ways in -- the farmer tapping a crop on this form, and the
  /// home screen having already chosen one -- because they must agree about what
  /// choosing a crop means, and a second copy of it is a second copy to keep in
  /// step.
  void _chooseCrop(Map<String, dynamic> crop) {
    _crop = crop;
    _cropController.text = '${crop['crop_name'] ?? ''}';
    _setSownFromCrop();
  }

  @override
  void dispose() {
    // Whatever was typed in the last moment before leaving is kept too.
    if (_saveSoon?.isActive ?? false) {
      _saveSoon!.cancel();
      unawaited(_saveForm());
    }
    _cropController.dispose();
    _sownController.dispose();
    _affectedController.dispose();
    _cropFocus.dispose();
    _sownFocus.dispose();
    _affectedFocus.dispose();
    unawaited(_positions?.cancel());
    unawaited(widget.voice.stop());
    super.dispose();
  }

  /// Say what each field wants, as the farmer reaches it.
  ///
  /// # Why the form speaks at all
  ///
  /// This screen is filled standing in a damaged field, in sun, by somebody who
  /// may read Marathi slowly or not at all. The walk has spoken its instructions
  /// from the beginning and the form did not, so the two halves of the same job
  /// behaved differently -- and the half with the numbers in it, the ones that
  /// decide the claim, was the silent one.
  ///
  /// On focus rather than all at once: a farmer hears what the field under their
  /// finger is asking for, when they are looking at it. Reading the whole form
  /// aloud on arrival would be a paragraph nobody waits through.
  void _speakPrompts() {
    void onFocus(FocusNode node, String mr, String en) {
      node.addListener(() {
        if (!node.hasFocus || !mounted) return;
        widget.voice.say(mr, en: en);
      });
    }

    onFocus(
      _sownFocus,
      'या पिकाखाली किती क्षेत्र पेरले आहे, ते हेक्टरमध्ये सांगा.',
      'How much land is sown with this crop, in hectares?',
    );
    onFocus(
      _affectedFocus,
      'त्यातले किती क्षेत्र खराब झाले आहे, ते हेक्टरमध्ये सांगा.',
      'How much of that land is damaged, in hectares?',
    );
    onFocus(
      _cropFocus,
      'कोणत्या पिकाचे नुकसान झाले, ते निवडा.',
      'Which crop was damaged?',
    );
  }

  /// Put a saved form back on screen.
  void _restore(Journey j) {
    _journeyId = j.id;
    final f = j.form;
    if (f['crop'] is String) _cropController.text = f['crop'] as String;
    if (f['sown'] is String) _sownController.text = f['sown'] as String;
    if (f['affected'] is String) {
      _affectedController.text = f['affected'] as String;
    }
    final damage = f['damage'];
    if (damage is num) _damagePercent = damage.toDouble().clamp(0, 100);
    _declared = f['declared'] == true;
  }

  /// Continue the claim already started for this parcel, or start one.
  ///
  /// So picking a field that already has a claim in progress picks up that
  /// claim instead of beginning a second one alongside it.
  Future<void> _attachJourney() async {
    final farmId = '${_parcel?['farm_id'] ?? ''}';
    final eventId = '${widget.event['id'] ?? ''}';
    if (farmId.isEmpty) return;

    // Already claimed for this calamity: there is nothing to file, and the
    // walk would be refused after it was done. Said here instead.
    final filed = await widget.store.filedClaimFor(
      eventId: eventId,
      farmId: farmId,
    );
    if (!mounted) return;
    if (filed != null) {
      // Unless the office has since lost it.
      //
      // The guard above matches what this handset sent, which is the right thing
      // to match -- but it cannot see the office deleting a claim underneath it,
      // and a deleted claim then blocked this parcel for good. The phone said
      // "नोंद झाली आहे" about a claim nothing had a record of, and there was no
      // way through it short of wiping the app.
      if (await _officeHasLost(filed)) {
        await widget.store.forgetFiledClaim('${filed['local_id']}');
      } else {
        if (!mounted) return;
        setState(() => _alreadyFiled = filed);
        return;
      }
      if (!mounted) return;
    }

    final existing = await widget.store.openJourneyFor(
      eventId: eventId,
      farmId: farmId,
    );
    if (!mounted) return;

    if (existing != null) {
      final j = Journey.fromRow(existing);
      if (j.stage == 'evidence') {
        // Already filed: the form is done and the evidence is what is left.
        await Navigator.of(context).pushReplacement(
          evidenceRoute(
            store: widget.store,
            voice: widget.voice,
            positioning: widget.positioning,
            journey: j,
            simulate: _simulate,
            onFinished: () => unawaited(widget.sync.run()),
          ),
        );
        return;
      }
      setState(() => _restore(j));
      return;
    }

    // Not saved yet: a farmer who opens the form and backs straight out has
    // started nothing, and should not find a claim to continue on Home. The
    // first thing they change saves it.
    _journeyId = DateTime.now().microsecondsSinceEpoch.toString();
  }

  /// Whether the office has no record of a claim this phone believes it filed.
  ///
  /// # Why only a definite answer counts
  ///
  /// Offline is not an answer. Nor is a 500, nor a refused token. Reading any of
  /// those as "the claim is gone" would let a farmer walk their field a second
  /// time for a claim that does exist, which the office refuses as ALREADY_FILED
  /// -- the exact waste the guard is there to prevent. So only a plain "no such
  /// report" from the office moves anything, and every other outcome leaves the
  /// claim standing.
  ///
  /// A claim with no server id was never acknowledged, so there is nothing to ask
  /// about and it keeps blocking: it is either still in the queue or it failed,
  /// and both are the farmer's claim rather than a ghost.
  Future<bool> _officeHasLost(Map<String, Object?> filed) async {
    final serverId = '${filed['server_id'] ?? ''}';
    if (serverId.isEmpty) return false;

    final grant = await widget.store.grantFor('${filed['farmer_id'] ?? ''}');
    final res = await widget.api.get(
      '/api/v1/reports/${Uri.encodeComponent(serverId)}/progress',
      farmerGrant: grant,
    );
    // 404 is the office saying it has no such report. 403 is not: that is a
    // question about this phone, and the claim may be perfectly real.
    return !res.offline && res.status == 404;
  }

  /// The claim this parcel already has for this calamity, if any.
  Map<String, Object?>? _alreadyFiled;

  void _changed() {
    setState(() {});
    _saveSoon?.cancel();
    _saveSoon = Timer(const Duration(milliseconds: 400), () {
      unawaited(_saveForm());
    });
  }

  Future<void> _saveForm() async {
    final id = _journeyId;
    final parcel = _parcel;
    if (id == null || parcel == null) return;
    try {
      await widget.store.putJourney({
        'local_id': id,
        'farmer_id': widget.farmerId,
        'event_id': '${widget.event['id'] ?? ''}',
        'farm_id': '${parcel['farm_id'] ?? ''}',
        'survey_number': '${parcel['survey_number'] ?? ''}',
        'stage': 'form',
        'form': {
          'crop': _cropController.text,
          'sown': _sownController.text,
          'affected': _affectedController.text,
          'damage': _damagePercent,
          'declared': _declared,
        },
        'event': widget.event,
        'parcel': parcel,
      });
    } catch (e) {
      // Saving as they type is a convenience on top of filing, which saves
      // for real. A failure here must not interrupt the form.
      debugPrint('[report.autosave] $e');
    }
  }

  void _select(dynamic parcel, {bool attach = true}) {
    final p = parcel as Map<String, dynamic>;
    setState(() {
      _parcel = p;

      // What the registry says is growing here. Read, not asked.
      _registryCrops = cropsFor(p, widget.event);
      _crop = _registryCrops.length == 1 ? _registryCrops.first : null;
      _cropController.text = _crop == null
          ? ''
          : '${_crop!['crop_name'] ?? ''}';
      // The sown area belongs to the crop, not to the parcel.
      //
      // It was taken from the parcel's owner_extent and left editable, on the
      // reasoning that the owner's share is a starting point and the sown area may
      // be less. Both halves of that were wrong. One farm carries several crops --
      // soybean on part of it and udad on another -- so the parcel's extent is not
      // any one crop's sown area, and showing it prefilled a claim for a quarter
      // guntha of udad with the whole half guntha of the farm. And it is AgriStack's
      // figure: a farmer editing it is editing the land record's own number, which
      // is what the claim is measured and paid against.
      _setSownFromCrop();

      // The polygon the registry sent, so the distance question can be
      // answered on the handset with no request and no signal.
      _shape = ParcelShape.fromGeoJson(p['plot_geometry']);
      _where = locate(
        fix: widget.positioning.best,
        shape: _shape,
        simulate: _simulate,
      );
    });

    unawaited(_fetchStations());
    if (attach) unawaited(_attachJourney());
  }

  /// The four points the server assigns for this parcel.
  ///
  /// Fetched here, not during the walk. Placement is deterministic given a
  /// boundary and an aspect, so the points do not depend on a report -- which
  /// matters, because the report does not exist on the server yet and the walk
  /// happens where there is no signal. Asking now, on a screen that already
  /// needed a network to reach, is what lets the field work offline.
  Future<void> _fetchStations() async {
    final farmId = _parcel?['farm_id'];
    if (farmId == null || _fetchingStations) return;

    setState(() => _fetchingStations = true);

    // The crop's own extent, which is what sizes the walk.
    //
    // A survey number is routinely shared, so the parcel cannot answer it: the
    // same polygon carries this farmer's soybean, their udad and a neighbour's
    // whole holding, and each is a different walk. The farmer has just typed the
    // sown area on this form, and it is the only place that number exists.
    final sownHa = double.tryParse(_sownController.text) ?? 0;

    // Cache first. The points were prefetched for every parcel the last time
    // this phone had a signal, so a farmer filling the form in a dead zone
    // still walks the four points instead of dropping to photographs.
    final data = FarmerData(store: widget.store, api: widget.api);
    var cached = await data.stationPlan(
      '$farmId',
      forFarmer: widget.farmerId,
      sownHa: sownHa,
    );
    if (!mounted) return;

    // Nothing under this crop's extent, and no signal to fetch it: fall back to
    // whatever the prefetch stored for the parcel. The points themselves are the
    // same either way -- placement does not depend on the crop -- and what is
    // missing is only the office's spacing figure, which spacingFor then works
    // out on the phone from the sown area. Better a walk guided by arithmetic
    // done here than no walk at all.
    if (((cached.value?['stations'] as List?) ?? const []).isEmpty &&
        sownHa > 0) {
      cached = await data.stationPlan('$farmId', forFarmer: widget.farmerId);
      if (!mounted) return;
    }

    final list = cached.value?['stations'] as List?;
    // Ground other claims on this survey number have already filmed. Carried
    // into the walk so it refuses that ground where the farmer is standing,
    // rather than the office refusing the clip a day later.
    final taken = decodeTakenPoints(cached.value?['taken_points']);
    setState(() {
      _fetchingStations = false;
      _takenPoints = taken;
      if (list != null && list.isNotEmpty) {
        _stations = [
          for (final s in list) Station.fromJson(s as Map<String, Object?>),
        ];
      } else {
        // A parcel with no boundary answers 409, and no stations is a real
        // answer rather than a failure -- _offerBoundaryWalk takes it from
        // here. Left null so the caller can tell "not fetched" from "none".
        _stations = null;
      }
    });
  }

  /// Offer the boundary walk when the parcel has no outline.
  ///
  /// Returns once the farmer has either walked it -- in which case stations
  /// are fetched and the caller finds them -- or declined, in which case the
  /// caller falls back to photographs as before.
  ///
  /// Never blocks the claim. A farmer who says no, or who loses the signal
  /// between the walk and the placement, still files their evidence; they
  /// simply file the weaker kind, which is the situation this method exists
  /// to make rarer rather than to forbid.

  bool get _complete =>
      _parcel != null &&
      _cropController.text.trim().isNotEmpty &&
      // Where the event allows a choice, it has to be made. Falling back to a
      // standing crop silently would file a post-harvest loss as the wrong
      // kind of claim, and the measurement that follows is a different one.
      (lossCategoriesOf(widget.event).length < 2 || _lossCategory != null) &&
      (double.tryParse(_sownController.text) ?? 0) > 0 &&
      (double.tryParse(_affectedController.text) ?? 0) > 0 &&
      _declared;

  Future<void> _file() async {
    if (!_complete || _saving) return;

    final sown = double.parse(_sownController.text);
    final affected = double.parse(_affectedController.text);

    // Caught here rather than by the server, because a farmer standing in a
    // field should not wait for a round trip to be told something the phone
    // could see. The server refuses it too; this is the kinder half.
    if (affected > sown) {
      setState(
        () => _error = tr(Lines.affectedTooBig.mr, Lines.affectedTooBig.en),
      );
      return;
    }

    setState(() {
      _saving = true;
      _error = null;
    });

    final localId =
        _journeyId ?? DateTime.now().microsecondsSinceEpoch.toString();
    _journeyId = localId;
    _saveSoon?.cancel();
    final event = widget.event;

    try {
      await widget.store.putDraft({
        'local_id': localId,
        'survey_event_id': event['id'],
        'farmer_id': widget.farmerId,
        'season': event['season'] ?? 'kharif',
        'crop_year': event['crop_year'] ?? '',
        // What the farmer said it is, where the event allowed a choice.
        // Standing crop otherwise, which is what every claim was filed as
        // before the choice existed.
        'loss_category': _lossCategory ?? 'standing_crop',
        'calamity_type': event['calamity_type'],
        // The event's own start. A farmer who cannot remember the exact hour
        // of a hailstorm should not be blocked, and the event window is what
        // the claim is judged against anyway.
        //
        // As a timestamp, not the date the event carries. incident_start is a
        // SQL date and serialises as "2026-08-20"; the server's occurred_at is
        // a time.Time and refuses anything that is not RFC 3339. Passing the
        // date straight through made every report a 400, which the farmer met
        // as a report that never arrived.
        'occurred_at': occurredAtFrom(event['incident_start']),
        'farm_id': _parcel!['farm_id'],
        'survey_number': _parcel!['survey_number'] ?? '',
        'crop_name': _cropController.text.trim(),
        'area_sown_ha': sown,
        'area_affected_ha': affected,
        'damage_percent': _damagePercent,
        'declaration_accepted': 1,
        // No signature kind.
        //
        // This recorded 'thumb' on every claim filed from a phone, and the
        // office then stamped the moment it arrived as when the farmer signed.
        // Nobody pressed a thumb against anything and no image of one exists.
        // What the farmer did is tick the declaration below, by an
        // authenticated account, and that is what is recorded. An officer
        // filing beside a farmer still records a real signature or thumb
        // impression, because they watched it happen.
        'stations': _stations == null
            ? null
            // The shared encoder, not a second copy of it. This file had its own
            // inline version and the two drifted: the office's spacing figure was
            // added to Station and to the walk's rule, and neither list literal
            // learned to carry it.
            : jsonEncode(encodeStations(_stations!)),
        // Held, not queued. The claim goes when its evidence is complete and
        // the farmer presses send on the review screen -- see
        // Store.statusHolding and review_screen.dart.
        'status': Store.statusHolding,
        'attempts': 0,
        // This instant, not the one the claim arrives at. The office measures
        // the 48-hour intimation window on it, so a farmer who files here and
        // reaches a signal on the third day is not recorded as late.
        //
        // Set once: a claim re-saved after a correction was still filed now,
        // and a second press of File should not move the farmer's own filing
        // later.
        'filed_at':
            (await widget.store.draft(localId))?['filed_at'] ??
            DateTime.now().millisecondsSinceEpoch,
      });

      // The claim is now on the phone and nothing will send it until its
      // evidence is complete. If the farmer walks away here, this is the only
      // thing that will ever tell them -- the office has not heard of the
      // claim and cannot. See reminders.dart.
      //
      // The permission is asked for now rather than at startup: a farmer who
      // has just filed a claim can judge what a reminder about it is for.
      unawaited(Reminders().requestPermission());
      unawaited(BackgroundSync.remindAbout(Reminders.after));

      // The form is done. From here a resumed claim goes to the evidence.
      await widget.store.putJourney({
        'local_id': localId,
        'farmer_id': widget.farmerId,
        'event_id': '${event['id'] ?? ''}',
        'farm_id': '${_parcel!['farm_id'] ?? ''}',
        'survey_number': '${_parcel!['survey_number'] ?? ''}',
        'stage': 'evidence',
        'event': event,
        'parcel': _parcel,
        'stations': _stations == null ? null : encodeStations(_stations!),
        'taken_points': _takenPoints.isEmpty
            ? null
            : encodeTakenPoints(_takenPoints),
        'evidence_mode': _stations == null ? null : 'stations',
      });

      if (!mounted) return;
      widget.onFiled?.call();

      // Straight into the evidence. The report is on the phone now, so there
      // is nothing to lose by going on, and a farmer who is sent back to a
      // list here will often not come back.
      //
      // The directed walk when the parcel has assigned points, and the old
      // photograph sequence when it does not -- refusing evidence entirely
      // because AgriStack sent no geometry would punish the farmer for
      // somebody else's data.
      //
      // Every claim walks, boundary or no boundary.
      //
      // The clips are the heaviest evidence a claim carries -- the
      // ground-vision measurement reads them, and that weighs more in the
      // specification than radar or NDVI. This screen used to stop a farmer
      // whose parcel had no outline and ask them to walk its perimeter first,
      // because the office places the four points from that outline. Half the
      // parcels in a village have no outline, and a farmer standing in a
      // hailed field was being asked for a survey before they could report
      // the damage.
      //
      // So an unmapped field walks four points of the farmer's own instead,
      // held apart by the same spacing rule. The boundary is asked for later,
      // away from the storm: it is what lets the satellite evidence run, and
      // that can be added to a claim already filed.
      final stations = _stations;
      await widget.store.putJourney({
        'local_id': localId,
        'stations': stations == null || stations.isEmpty
            ? null
            : encodeStations(stations),
        // Written again here rather than relied on from above: a farmer who
        // reached this screen offline, typed the sown area and only then found a
        // signal has these now and did not before.
        'taken_points': _takenPoints.isEmpty
            ? null
            : encodeTakenPoints(_takenPoints),
        'evidence_mode': 'stations',
      });
      final journey = await widget.store.journey(localId);
      if (!mounted || journey == null) return;

      await Navigator.of(context).pushReplacement(
        evidenceRoute(
          sync: widget.sync,
          store: widget.store,
          voice: widget.voice,
          positioning: widget.positioning,
          journey: Journey.fromRow(journey),
          simulate: _simulate,
          onFinished: null,
        ),
      );
      if (!mounted) return;

      // A completed walk has already filed the claim -- evidenceRoute releases
      // it the moment the last clip is saved -- so there is nothing left to
      // review and nothing left to press. Offering the send button here would
      // ask a farmer who has just been told the claim is complete to complete it
      // again.
      //
      // The review screen is still the answer for a journey that reached
      // `review` and stopped: an older build left claims in that state, and they
      // must still be sendable.
      final after = await widget.store.journey(localId);
      if (!mounted) return;
      if (after != null && Journey.fromRow(after).atReview) {
        await _review(localId);
      }
    } catch (e) {
      if (!mounted) return;
      setState(() {
        _saving = false;
        _error = deviceError(
          'साठवता आले नाही',
          'Could not save. Try again.',
          e,
          'report.save',
        );
      });
    }
  }

  /// The last step: what will be sent, and the button that sends it.
  Future<void> _review(String localId) async {
    final row = await widget.store.journey(localId);
    if (!mounted || row == null) return;
    await Navigator.of(context).push(
      MaterialPageRoute<void>(
        builder: (_) => ReviewScreen(
          store: widget.store,
          voice: widget.voice,
          positioning: widget.positioning,
          sync: widget.sync,
          journey: Journey.fromRow(row),
          simulate: _simulate,
        ),
      ),
    );
  }

  @override
  Widget build(BuildContext context) {
    // The refusal comes before the form, not after five photographs.
    //
    // A farmer who is not at their field cannot evidence a loss there, and
    // finding that out at submission -- which is where the fence used to bite
    // -- means everything they typed and photographed was wasted.
    final where = _where;
    // where.known, because an unknown position is not a distance.
    //
    // locate() answers with an infinite distance when there is no fix yet and
    // when the parcel has no outline, and neither means the farmer is far from
    // their field. Without this the screen told them they were away from a field
    // it could not locate, and then threw while formatting the distance.
    //
    // With no position the form simply opens. The fence that matters is the one
    // in front of the walk, which blocks on the same question and can wait for a
    // fix; blocking the form on it only stops a farmer filling in a claim indoors
    // and coming back out.
    if (_parcel != null &&
        where != null &&
        where.known &&
        !where.atParcel &&
        !_simulate) {
      return _AwayFromField(
        surveyNumber: '${_parcel!['survey_number'] ?? ''}',
        distance: where.pretty,
        onSimulate: canSimulateLocation
            ? () => setState(() {
                _simulate = true;
                _where = locate(
                  fix: widget.positioning.best,
                  shape: _shape,
                  simulate: true,
                );
              })
            : null,
      );
    }

    // Already claimed, for this calamity, on this handset. Said before the
    // form rather than after the walk: the office refuses the second claim,
    // and finding that out having filmed four points is finding it out too
    // late.
    final filed = _alreadyFiled;
    if (filed != null) {
      return _AlreadyFiled(
        surveyNumber: '${filed['survey_number'] ?? ''}',
        reference: '${filed['reference'] ?? ''}',
        onBack: () => Navigator.of(context).pop(),
      );
    }

    final text = Theme.of(context).textTheme;

    return Scaffold(
      appBar: AppBar(title: Text(tr('नुकसान कळवा', 'Report damage'))),
      body: ListView(
        padding: Space.page,
        children: [
          _EventBanner(event: widget.event),

          if (widget.parcels.length > 1) ...[
            SectionHeader(tr('कोणते शेत?', 'Which field?')),
            Card(
              clipBehavior: Clip.antiAlias,
              child: RadioGroup<String>(
                groupValue: _parcel?['farm_id'] as String?,
                onChanged: (farmId) {
                  final chosen = widget.parcels.firstWhere(
                    (p) => (p as Map)['farm_id'] == farmId,
                  );
                  _select(chosen);
                },
                child: Column(
                  children: [
                    for (final parcel in widget.parcels)
                      RadioListTile<String>(
                        value: (parcel as Map)['farm_id'] as String,
                        title: Text(
                          tr(
                            'सर्व्हे नं. ${parcel['survey_number'] ?? ''}',
                            'Survey no. ${parcel['survey_number'] ?? ''}',
                          ),
                        ),
                        subtitle: Text(
                          tr(
                            '${parcel['owner_extent'] ?? ''} हेक्टर',
                            '${parcel['owner_extent'] ?? ''} ha',
                          ),
                        ),
                      ),
                  ],
                ),
              ),
            ),
          ],

          _LossCategoryField(
            options: lossCategoriesOf(widget.event),
            chosen: _lossCategory,
            onChosen: (code) {
              setState(() => _lossCategory = code);
              _changed();
            },
          ),

          SectionHeader(tr('पिकाची माहिती', 'Crop details')),
          AppCard(
            child: Column(
              children: [
                _CropField(
                  crops: _registryCrops,
                  chosen: _crop,
                  typed: _cropController,
                  onChosen: (c) {
                    setState(() => _chooseCrop(c));
                    _changed();
                  },
                  onTyped: () => _changed(),
                ),
                const SizedBox(height: Space.md),
                // The sown area, shown rather than asked for.
                //
                // # Why this cannot be typed into
                //
                // It is AgriStack's figure. The claim is measured against it, the
                // walk is sized from it, and the payout is calculated on it -- so a
                // farmer editing it is editing the land record, and every check
                // downstream that compares the claim to the registry is comparing
                // the registry to itself.
                //
                // The one exception is a registry that encodes the area ambiguously.
                // The office omits the figure rather than guess at it, and then
                // somebody has to supply one; that is the only case where this
                // accepts typing, and it says so on the field.
                TextField(
                  controller: _sownController,
                  focusNode: _sownFocus,
                  readOnly: !_sownIsFromRegistry,
                  canRequestFocus: _sownIsFromRegistry,
                  keyboardType: const TextInputType.numberWithOptions(
                    decimal: true,
                  ),
                  textInputAction: TextInputAction.next,
                  onChanged: (_) => _changed(),
                  decoration: InputDecoration(
                    labelText: tr('पेरणी केलेले क्षेत्र', 'Area sown'),
                    suffixText: tr('हेक्टर', 'ha'),
                    prefixIcon: const Icon(Icons.crop_square),
                    filled: !_sownIsFromRegistry,
                    helperText: _sownIsFromRegistry
                        ? tr(
                            'हे क्षेत्र सात-बारा नोंदीतून घेतले आहे.',
                            'Taken from the land record.',
                          )
                        : tr(
                            'नोंदीत क्षेत्र नाही. तलाठ्याकडून विचारून भरा.',
                            'The land record has no area for this crop. Ask at '
                                'the office before filling it in.',
                          ),
                  ),
                ),
                const SizedBox(height: Space.md),
                TextField(
                  controller: _affectedController,
                  focusNode: _affectedFocus,
                  keyboardType: const TextInputType.numberWithOptions(
                    decimal: true,
                  ),
                  onChanged: (_) => _changed(),
                  decoration: InputDecoration(
                    labelText: tr('नुकसानग्रस्त क्षेत्र', 'Area damaged'),
                    suffixText: tr('हेक्टर', 'ha'),
                    prefixIcon: const Icon(Icons.broken_image_outlined),
                  ),
                ),
              ],
            ),
          ),

          SectionHeader(tr('किती नुकसान झाले?', 'How much is lost?')),
          AppCard(
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                Row(
                  crossAxisAlignment: CrossAxisAlignment.end,
                  children: [
                    Text(
                      '${_damagePercent.round()}%',
                      style: text.headlineMedium?.copyWith(
                        color: Palette.primaryDark,
                      ),
                    ),
                    const SizedBox(width: Space.sm),
                    Padding(
                      padding: const EdgeInsets.only(bottom: 4),
                      child: Text(
                        tr('पिकाचे नुकसान', 'of the crop'),
                        style: text.bodySmall,
                      ),
                    ),
                  ],
                ),
                Slider(
                  value: _damagePercent,
                  min: 0,
                  max: 100,
                  divisions: 20,
                  label: '${_damagePercent.round()}%',
                  onChanged: (v) {
                    _damagePercent = v;
                    _changed();
                  },
                  // Spoken when they let go, not while they drag. A slider
                  // reports every fraction of a movement, and a voice reading
                  // each one aloud is unusable -- and every line would cut off
                  // the last.
                  //
                  // It is read back because it is the number the whole claim
                  // turns on, and it is set by dragging rather than typing: a
                  // farmer who meant forty and left it at sixty has no other way
                  // to notice.
                  onChangeEnd: (v) => widget.voice.say(
                    'पिकाचे ${v.round()} टक्के नुकसान झाले आहे.',
                    en: '${v.round()} percent of the crop is damaged.',
                  ),
                ),
                // Said plainly. A number a farmer thinks is final is a number
                // they will argue about later, and this one is the start of an
                // assessment rather than the end of one.
                Text(
                  tr(
                    'हा तुमचा अंदाज आहे. अंतिम आकडा अधिकारी तपासणीनंतर ठरवतील.',
                    'This is your estimate. Officers decide the final figure '
                        'after examining the evidence.',
                  ),
                  style: text.bodySmall,
                ),
              ],
            ),
          ),

          const SizedBox(height: Space.xs),
          Card(
            clipBehavior: Clip.antiAlias,
            child: CheckboxListTile(
              value: _declared,
              onChanged: (v) {
                _declared = v ?? false;
                _changed();
              },
              controlAffinity: ListTileControlAffinity.leading,
              title: Text(
                tr(
                  'वरील माहिती खरी आहे असे मी जाहीर करतो/करते.',
                  'I declare that what I have entered is true.',
                ),
                style: text.bodyMedium,
              ),
            ),
          ),

          if (_error != null) ...[
            const SizedBox(height: Space.md),
            Notice(
              tone: Tone.danger,
              icon: Icons.error_outline,
              title: Language.of(_error!),
              margin: EdgeInsets.zero,
            ),
          ],

          const SizedBox(height: Space.xl),
          BigButton(
            label: _saving
                ? tr('जतन करत आहे…', 'Saving…')
                : tr('पुढे: फोटो व व्हिडिओ', 'Next: photos and video'),
            icon: Icons.arrow_forward,
            onPressed: _complete && !_saving ? _file : null,
          ),
          const SizedBox(height: Space.md),
          Row(
            mainAxisAlignment: MainAxisAlignment.center,
            children: [
              const Icon(
                Icons.offline_pin_outlined,
                size: 16,
                color: Palette.inkMuted,
              ),
              const SizedBox(width: Space.xs),
              Flexible(
                child: Text(
                  tr(
                    'फोनवर जतन होते. नेटवर्कशिवायही पुढे जाता येते.',
                    'Saved on the phone. Works without network.',
                  ),
                  style: text.labelMedium,
                ),
              ),
            ],
          ),
        ],
      ),
    );
  }
}

class _EventBanner extends StatelessWidget {
  const _EventBanner({required this.event});

  final Map<String, dynamic> event;

  @override
  Widget build(BuildContext context) {
    final text = Theme.of(context).textTheme;
    return AppCard(
      margin: EdgeInsets.zero,
      child: Row(
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
                Text('${event['title'] ?? ''}', style: text.titleMedium),
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
    );
  }
}

/// Shown when the farmer is not at the field they picked.
///
/// A screen rather than a banner: this is the end of the road for now, and
/// somebody who opened the app to file a claim needs to understand why they
/// cannot, not watch a message disappear after four seconds.
class _AwayFromField extends StatelessWidget {
  const _AwayFromField({
    required this.surveyNumber,
    required this.distance,
    this.onSimulate,
  });

  final String surveyNumber;
  final String distance;

  /// Debug builds only. Absent from a release binary, because
  /// canSimulateLocation is a compile-time constant there.
  final VoidCallback? onSimulate;

  @override
  Widget build(BuildContext context) {
    final text = Theme.of(context).textTheme;
    return Scaffold(
      appBar: AppBar(title: Text(tr('नुकसान कळवा', 'Report damage'))),
      body: SafeArea(
        child: ListView(
          padding: const EdgeInsets.fromLTRB(24, 48, 24, 32),
          children: [
            Center(
              child: Container(
                width: 88,
                height: 88,
                decoration: const BoxDecoration(
                  color: Palette.severeBg,
                  shape: BoxShape.circle,
                ),
                child: const Icon(
                  Icons.wrong_location_outlined,
                  size: 40,
                  color: Palette.severeFg,
                ),
              ),
            ),
            const SizedBox(height: Space.xl),
            Text(
              tr('तुम्ही शेतात नाही', 'You are not at your field'),
              textAlign: TextAlign.center,
              style: text.titleLarge,
            ),
            const SizedBox(height: Space.sm),
            Text(
              tr(
                'सर्व्हे नं. $surveyNumber येथून $distance दूर आहे.',
                'Survey no. $surveyNumber is $distance away.',
              ),
              textAlign: TextAlign.center,
              style: text.titleMedium?.copyWith(color: Palette.severeFg),
            ),
            const SizedBox(height: Space.lg),
            Text(
              tr(
                'नुकसानीची नोंद शेतातूनच करावी लागते, म्हणजे पुरावा त्याच '
                    'जमिनीचा आहे हे सिद्ध होते.',
                'A loss has to be reported from the field itself, so the '
                    'evidence is provably of that land.',
              ),
              textAlign: TextAlign.center,
              style: text.bodyMedium?.copyWith(color: Palette.inkMuted),
            ),
            if (onSimulate != null) ...[
              const SizedBox(height: Space.xxl),
              const Divider(),
              const SizedBox(height: Space.lg),
              Text(
                'DEBUG BUILD ONLY',
                textAlign: TextAlign.center,
                style: text.labelSmall?.copyWith(letterSpacing: 1.2),
              ),
              const SizedBox(height: Space.sm),
              OutlinedButton.icon(
                onPressed: onSimulate,
                icon: const Icon(Icons.bug_report_outlined, size: 20),
                label: const Text('Pretend I am at the field'),
              ),
              const SizedBox(height: Space.sm),
              Text(
                'Not present in a release build. Captures still carry the '
                'real GPS position, so the office refuses them.',
                textAlign: TextAlign.center,
                style: text.labelSmall,
              ),
            ],
          ],
        ),
      ),
    );
  }
}

/// This parcel already has a claim against this calamity.
class _AlreadyFiled extends StatelessWidget {
  const _AlreadyFiled({
    required this.surveyNumber,
    required this.reference,
    required this.onBack,
  });

  final String surveyNumber;

  /// The claim's number, where the office has answered with one. A claim
  /// still in the queue has none yet, and saying so is better than showing an
  /// empty space where a number should be.
  final String reference;

  final VoidCallback onBack;

  @override
  Widget build(BuildContext context) {
    final text = Theme.of(context).textTheme;
    return Scaffold(
      appBar: AppBar(title: Text(tr('नुकसान कळवा', 'Report damage'))),
      body: Padding(
        padding: Space.page,
        child: Column(
          mainAxisAlignment: MainAxisAlignment.center,
          children: [
            const Icon(Icons.task_alt, size: 64, color: Palette.primary),
            const SizedBox(height: Space.lg),
            Text(
              tr(
                'सर्वे $surveyNumber ची नोंद झाली आहे',
                'Survey $surveyNumber is already reported',
              ),
              textAlign: TextAlign.center,
              style: text.titleLarge,
            ),
            const SizedBox(height: Space.sm),
            Text(
              reference.isEmpty
                  ? tr(
                      'या आपत्तीसाठी या शेताची नोंद आधीच केली आहे. ती '
                          '"माझ्या नोंदी" मध्ये दिसेल. पुन्हा नोंद करायची '
                          'गरज नाही.',
                      'This field has already been reported for this '
                          'calamity. You will find it under My reports. '
                          'There is no need to report it again.',
                    )
                  : tr(
                      'या आपत्तीसाठी या शेताची नोंद $reference या क्रमांकाने '
                          'झाली आहे. ती "माझ्या नोंदी" मध्ये पहा.',
                      'This field is already reported for this calamity, as '
                          '$reference. See it under My reports.',
                    ),
              textAlign: TextAlign.center,
              style: text.bodyMedium?.copyWith(color: Palette.inkMuted),
            ),
            const SizedBox(height: Space.xl),
            BigButton(
              label: tr('माझ्या नोंदी', 'My reports'),
              icon: Icons.description_outlined,
              onPressed: onBack,
            ),
          ],
        ),
      ),
    );
  }
}

/// The registry's crops for one parcel, narrowed to an event's season.
///
/// # Why the season narrows it
///
/// A parcel carries a crop record per season and year. A hailstorm declared for
/// kharif 2026 is not a claim about last rabi's wheat, and offering that wheat
/// would invite a claim the office refuses -- or worse, does not.
///
/// Where the registry's records carry no season at all, every crop on the
/// parcel is offered rather than none: an incomplete record should not stop a
/// farmer filing, and the farmer choosing from their own parcel's crops is
/// still better than the farmer typing one.
List<Map<String, dynamic>> cropsFor(
  Map<String, dynamic> parcel,
  Map<String, dynamic>? event,
) {
  final all = <Map<String, dynamic>>[
    for (final c in (parcel['crops'] as List<dynamic>? ?? const []))
      if (c is Map) c.cast<String, dynamic>(),
  ];
  if (all.isEmpty) return const [];

  final season = '${event?['season'] ?? ''}'.trim().toLowerCase();
  final year = '${event?['crop_year'] ?? ''}'.trim();
  if (season.isEmpty && year.isEmpty) return all;

  final matching = all.where((c) {
    final cs = '${c['season'] ?? ''}'.trim().toLowerCase();
    final cy = '${c['year'] ?? ''}'.trim();
    final seasonOk = season.isEmpty || cs.isEmpty || cs == season;
    final yearOk = year.isEmpty || cy.isEmpty || cy == year;
    return seasonOk && yearOk;
  }).toList();

  return matching.isEmpty ? all : matching;
}

/// The loss categories an event permits, as the server narrowed them.
///
/// Empty where the event predates the field or its calamity is one this build
/// does not know. The form then falls back to a standing crop, which is what
/// every claim was filed as before the choice existed.
List<Map<String, dynamic>> lossCategoriesOf(Map<String, dynamic>? event) => [
  for (final c in (event?['loss_categories'] as List<dynamic>? ?? const []))
    if (c is Map) c.cast<String, dynamic>(),
];

/// The crop, as the registry has it.
///
/// # Why it is not a text field any more
///
/// A farmer typing the crop is a farmer typing a spelling. "soyabean" is not in
/// the agronomic table, so the assessment reports that no growth stage could be
/// established -- a real consequence for a claim, caused by a transliteration.
/// The registry already knows, and the app already receives it.
///
/// Three states, and the third is the reason this is a widget rather than a
/// read-only box:
///
///   - One crop on the parcel for this season: shown, not editable.
///   - Several: the farmer picks which of *their own* recorded crops the claim
///     is for. A choice between two registry records is not data entry.
///   - None: the registry has no crop for this parcel. AgriStack returns an
///     empty crop array for every farmer we can reach today, so this is the
///     ordinary case rather than the edge one, and a read-only empty field
///     would mean nobody could file at all. It falls back to typing, and says
///     plainly that what is typed is not from the record.
class _CropField extends StatelessWidget {
  const _CropField({
    required this.crops,
    required this.chosen,
    required this.typed,
    required this.onChosen,
    required this.onTyped,
  });

  final List<Map<String, dynamic>> crops;
  final Map<String, dynamic>? chosen;
  final TextEditingController typed;
  final ValueChanged<Map<String, dynamic>> onChosen;
  final VoidCallback onTyped;

  @override
  Widget build(BuildContext context) {
    final text = Theme.of(context).textTheme;

    if (crops.isEmpty) {
      return Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          TextField(
            controller: typed,
            onChanged: (_) => onTyped(),
            textInputAction: TextInputAction.next,
            decoration: InputDecoration(
              labelText: tr('पीक', 'Crop'),
              hintText: tr('उदा. सोयाबीन', 'e.g. Soybean'),
              prefixIcon: const Icon(Icons.grass_outlined),
            ),
          ),
          const SizedBox(height: Space.xs),
          Text(
            tr(
              'या सर्व्हे नंबरसाठी नोंदीत पीक आढळले नाही, म्हणून तुम्ही ते '
                  'स्वतः लिहीत आहात.',
              'The record holds no crop for this survey number, so this is '
                  'what you are telling us rather than what the record says.',
            ),
            style: text.bodySmall,
          ),
        ],
      );
    }

    if (crops.length == 1) {
      final only = crops.first;
      return InputDecorator(
        decoration: InputDecoration(
          labelText: tr('पीक', 'Crop'),
          prefixIcon: const Icon(Icons.grass_outlined),
          // Not a disabled text field: a greyed-out box reads as something the
          // farmer ought to be able to change and cannot.
          border: InputBorder.none,
          helperText: tr('नोंदीनुसार', 'From your land record'),
        ),
        child: Text('${only['crop_name'] ?? ''}', style: text.titleMedium),
      );
    }

    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        Padding(
          padding: const EdgeInsets.only(bottom: Space.xs),
          child: Text(
            tr(
              'या शेतात नोंदीनुसार एकापेक्षा जास्त पिके आहेत. कोणत्या पिकाचे '
                  'नुकसान झाले?',
              'Your record shows more than one crop on this field. Which one '
                  'was damaged?',
            ),
            style: text.bodyMedium,
          ),
        ),
        RadioGroup<String>(
          groupValue: '${chosen?['crop_name'] ?? ''}',
          onChanged: (name) => onChosen(
            crops.firstWhere((c) => '${c['crop_name'] ?? ''}' == name),
          ),
          child: Column(
            children: [
              for (final c in crops)
                RadioListTile<String>(
                  value: '${c['crop_name'] ?? ''}',
                  title: Text('${c['crop_name'] ?? ''}'),
                  subtitle: '${c['sowing_date'] ?? ''}'.isEmpty
                      ? null
                      : Text(
                          tr(
                            'पेरणी ${c['sowing_date']}',
                            'sown ${c['sowing_date']}',
                          ),
                        ),
                  contentPadding: EdgeInsets.zero,
                ),
            ],
          ),
        ),
      ],
    );
  }
}

/// What kind of loss is being claimed.
///
/// # Why this is a choice at all
///
/// The app filed every claim as a standing-crop loss. A farmer whose harvested
/// crop was lying in the field when unseasonal rain took it had no way to say
/// so, and the two are not the same measurement: one is read off a growing
/// crop, the other off a cut one.
///
/// # Why the options come from the event
///
/// Each category permits only certain calamities, and the office refuses the
/// mismatch. A drought is never a post-harvest loss. Offering the choice anyway
/// would let a farmer pick it, walk four points of their field, and be refused
/// afterwards -- so the server sends the categories this event's calamity
/// actually allows and this renders those.
///
/// Nothing is drawn where there is only one: a question with a single answer is
/// a tap that teaches the farmer nothing.
class _LossCategoryField extends StatelessWidget {
  const _LossCategoryField({
    required this.options,
    required this.chosen,
    required this.onChosen,
  });

  final List<Map<String, dynamic>> options;
  final String? chosen;
  final ValueChanged<String> onChosen;

  @override
  Widget build(BuildContext context) {
    if (options.length < 2) return const SizedBox.shrink();

    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        SectionHeader(tr('कोणत्या प्रकारचे नुकसान?', 'What kind of loss?')),
        Card(
          clipBehavior: Clip.antiAlias,
          child: RadioGroup<String>(
            groupValue: chosen,
            onChanged: (code) {
              if (code != null) onChosen(code);
            },
            child: Column(
              children: [
                for (final o in options)
                  RadioListTile<String>(
                    value: '${o['code'] ?? ''}',
                    title: Text(tr('${o['mr'] ?? ''}', '${o['en'] ?? ''}')),
                  ),
              ],
            ),
          ),
        ),
      ],
    );
  }
}
