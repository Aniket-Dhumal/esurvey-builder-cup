import 'dart:async';
import 'dart:io';
import 'dart:typed_data';

import 'package:camera/camera.dart';
import 'package:crypto/crypto.dart';
import 'package:flutter/material.dart';
import 'package:path/path.dart' as p;

import '../core/api.dart';
import '../core/db.dart';
import '../core/faults.dart';
import '../core/language.dart';
import '../core/sensors.dart';
import '../core/parcel_location.dart';
import '../core/storage.dart';
import '../core/voice.dart';
import '../ui/theme.dart';
import 'encoding.dart';
import 'station_guide.dart';
import '../report/journey.dart';
import '../core/messages.dart';
import '../live/live_frame.dart';
import '../live/survey_mode.dart';
import '../live/live_talk.dart';

/// The directed walk: four points, a clip at each.
///
/// # What this screen is for
///
/// Every other piece of evidence in this system was recorded wherever the
/// farmer happened to stand. That is not fraud — the damaged corner is the
/// part that feels worth showing — but it means a thirty per cent loss and an
/// eighty per cent loss can produce identical evidence, and nothing downstream
/// can tell them apart.
///
/// So the server places four points across the parcel before the walk begins,
/// and this screen takes the farmer to each one in turn.
///
/// # Why the points arrive as a parameter
///
/// They were fetched when the parcel was, on a screen that already needed a
/// network. This one runs in a field. Placement is deterministic given a
/// boundary and an aspect, so the points do not depend on the report — which
/// is just as well, because the report is still on the phone and has no
/// server id to ask about.
///
/// # Why fifteen seconds
///
/// Four clips at 720p is about forty-eight megabytes, which is over two hours
/// on 2G. Every second added here is a report that finishes later, on a
/// connection that may not last. Fifteen is enough to walk a few paces and pan
/// across the damage, which is what the vision model actually reads.
class StationWalkScreen extends StatefulWidget {
  const StationWalkScreen({
    super.key,
    required this.store,
    required this.voice,
    required this.positioning,
    required this.draftLocalId,
    required this.stations,
    this.shape = const ParcelShape([]),
    this.sownHa = 0,
    this.takenPoints = const [],
    this.simulate = false,
    this.onFinished,
    this.api,
    this.farmerId = '',
    this.mode = const SurveyMode.offline(),
    this.crop,
    this.calamity,
    this.season,
  });

  final Store store;
  final Voice voice;
  final Positioning positioning;

  /// How the assistant is reached, where there is one.
  ///
  /// Null on a build with no live guidance, and in most tests. Everything below the
  /// assistant -- the walk, the spacing rule, the scripted voice, the filing -- works
  /// with this absent, which is the whole reason it is optional rather than required.
  final Api? api;

  /// Whose consent and whose daily allowance govern the conversation.
  ///
  /// Not passed onward to the model. The relay is told the crop, the calamity, the
  /// season and the district that the platform resolved, and nothing that identifies
  /// a person -- see
  /// liveConsentTerms, which says so to the farmer.
  final String farmerId;

  /// Which engine this survey runs on, chosen once before the walk began.
  ///
  /// Offline by default, which is what every route that does not decide gets: a
  /// walk with no assistant is the complete product, and a screenshot harness or a
  /// test should not have to opt out of a conversation.
  final SurveyMode mode;

  /// What the conversation is about, in the words the form recorded.
  ///
  /// No district among them. The handset holds an LGD code and nothing that turns it
  /// into a place name; the platform resolves that from the farmer it has already
  /// looked up. See Journey, which says so at more length.
  final String? crop;
  final String? calamity;
  final String? season;

  /// The report these captures belong to, still local-only.
  final String draftLocalId;

  /// The four points, in the order they are to be walked.
  /// The four points the office placed, or empty for a field with no
  /// recorded boundary.
  ///
  /// Empty is not a lesser walk: the farmer chooses where to stand and the
  /// spacing rule still holds them apart, so the claim carries four clips
  /// from four parts of the field either way. What is missing is only the
  /// office's say in where -- and demanding a boundary trace before a farmer
  /// standing in a damaged field may film it cost more than it bought.
  final List<Station> stations;

  /// The parcel's outline, for judging a point the farmer cannot reach.
  /// Empty where the registry sent no geometry, in which case the server's
  /// fence is the only one -- and there is nothing to fence against.
  final ParcelShape shape;

  /// The extent of the crop being walked, in hectares.
  ///
  /// Used only where the office sent no spacing figure with the points -- a stale
  /// station cache, or a server too old to send one -- in which case the same
  /// arithmetic is done here. See spacingFor.
  final double sownHa;

  /// Ground other claims on this survey number have already filmed, which this
  /// walk may not count as its own. See TakenPoint.
  final List<TakenPoint> takenPoints;

  /// Debug builds only: treat the farmer as standing at each point. Never
  /// true in a release binary — see core/parcel_location.dart.
  final bool simulate;

  final VoidCallback? onFinished;

  @override
  State<StationWalkScreen> createState() => _StationWalkScreenState();
}

class _StationWalkScreenState extends State<StationWalkScreen> {
  CameraController? _camera;
  StreamSubscription<PositionState>? _positions;

  int _index = 0;
  Fix? _fix;

  /// Where the earlier points were actually recorded, so the next one can be
  /// required to be somewhere else. Read from the phone's own rows, which
  /// survive the app being killed mid-walk.
  List<RecordedPoint> _recorded = const [];

  /// Whether the farmer may record where they are standing, and why not.
  WalkStatus? _status;

  bool _recording = false;
  int _secondsLeft = 0;
  Timer? _countdown;

  String? _errorMr;
  String? _errorEn;

  /// The last thing said, so the countdown does not repeat itself every
  /// second. A voice that recites a new number every second is noise, and a
  /// farmer stops listening to it well before they reach the point.
  String _lastSpoken = '';

  static const _clipSeconds = 15;

  /// How long a farmer waits for a fix before being told that is what is
  /// happening.
  ///
  /// Three seconds. A fix indoors or under canopy can take half a minute, and a
  /// screen that says nothing while a farmer stands in a field with a dead button
  /// reads as a broken app -- they close it and open it again, which starts the
  /// wait over.
  static const _sayLocatingAfter = Duration(seconds: 3);

  /// How long the record button stays live and untouched before the farmer is
  /// told to press it.
  ///
  /// The button is on the screen and is not the thing a farmer is looking at:
  /// they are looking at their crop, holding the phone at arm's length, and the
  /// voice is what they are following. Eight seconds is long enough not to nag
  /// somebody who was already reaching for it.
  static const _nudgeAfter = Duration(seconds: 8);

  /// How often the nudge repeats while nothing happens.
  static const _nudgeAgainEvery = Duration(seconds: 20);

  Timer? _locating;
  Timer? _nudge;

  /// Whether the farmer has been told a fix is being taken, so it is said once
  /// per wait rather than every time the timer comes round.
  bool _saidLocating = false;

  /// Since when the farmer has been free to record and has not. Null whenever
  /// they are not, so walking out of the field and back restarts the wait rather
  /// than being nagged the moment they return.
  DateTime? _ableSince;

  /// When the nudge was last spoken, so it repeats slowly instead of every tick.
  DateTime? _lastNudged;

  /// When the farmer was last told to walk on, so the distance is announced a
  /// little after that rather than over the top of it.
  DateTime? _walkedSince;

  Timer? _walkOn;

  /// How long after being told to walk on the farmer is told how much further.
  ///
  /// Ten seconds. Long enough that they have started moving -- a distance
  /// announced before the first step is a distance measured from the wrong place
  /// -- and short enough to be an answer to "am I there yet" rather than a
  /// reminder of something they had forgotten.
  static const _sayDistanceAfter = Duration(seconds: 10);

  /// Negotiates what this phone's encoder will actually accept, and steps down
  /// if a clip comes out over budget. Held for the whole walk so a rung
  /// established at station one is reused at station four.
  final _encoder = ClipEncoder();

  /// The last thing the assistant asked to be shown, and which model asked.
  ///
  /// Stamped on the next clip and then cleared, so it marks the clip that answers the
  /// request rather than every clip after it. A farmer who is asked to film a pod and
  /// then walks to three more points has one directed clip, not four.
  String? _askedFor;
  String? _askedForModel;

  /// What the clips of this walk have come to, for the budget note.
  int _clipBytesSoFar = 0;

  /// How many clips this walk wants. Four, placed or not.
  int get _total =>
      widget.stations.isNotEmpty ? widget.stations.length : freeWalkPoints;

  /// The point being suggested, where the office placed any. Null on a field
  /// with no boundary, and the walk then guides by spacing alone.
  Station? get _target =>
      _index < widget.stations.length ? widget.stations[_index] : null;

  /// Whether the farmer may record here.
  ///
  /// Two conditions, and neither is an assigned coordinate: they must be
  /// inside their own field, and somewhere the walk has not already been.
  /// See walkStatus.
  ///
  /// It used to require standing within twelve metres of a surveyed point,
  /// which in a wet field or under tree cover is a farmer in the mud watching
  /// a disabled button -- while the evidence that matters, several clips from
  /// across the same field, was obtainable the whole time.
  bool get _canRecord =>
      _camera != null &&
      (widget.simulate || (_fixIsLive && (_status?.canRecord ?? false)));

  @override
  void initState() {
    super.initState();
    unawaited(_start());
    unawaited(_loadRecorded());
    _watchForSilence();
  }

  /// Say what the screen is doing when it has been doing it for a while.
  ///
  /// Two silences are worth breaking, and both of them look to a farmer like the
  /// app has stopped: waiting for a position, and waiting for them to press a
  /// button they are not looking at.
  ///
  /// One timer for both rather than two chained to state changes: the screen has
  /// several ways in and out of each condition -- a fix arriving, a clip
  /// finishing, the farmer walking out of the field and back -- and a timer that
  /// simply asks "what is true now" cannot be left armed for a condition that has
  /// passed.
  void _watchForSilence() {
    _locating = Timer.periodic(_sayLocatingAfter, (_) {
      if (!mounted || _recording) return;

      if (_status == null) {
        if (_saidLocating) return;
        _saidLocating = true;
        widget.voice.say(
          'स्थान तपासत आहे. थोडा वेळ थांबा.',
          en: 'Checking your location. Please wait a moment.',
        );
        return;
      }
      // A fix arrived, so the next wait is told about again.
      _saidLocating = false;
    });

    // Ticked more often than it speaks, so the first nudge lands at
    // _nudgeAfter rather than at the repeat interval -- eight seconds of standing
    // there able to record, not twenty.
    // How much further to go, on its own cadence.
    //
    // Not on position events: walking generates one about every metre, and
    // re-speaking the instruction on each one talks over itself, so a farmer
    // hears a stream of half-sentences. This says the remaining distance every
    // ten seconds instead, which is a walking pace rather than a GPS pace.
    //
    // Voice.say drops a line identical to the one before it, so a farmer who has
    // stopped is not told the same number over and over -- it speaks again when
    // the number actually changes, which is when they have moved.
    _walkOn = Timer.periodic(_sayDistanceAfter, (_) {
      if (!mounted || _recording) return;
      final short = _status?.shortfallM;
      if (short == null || short <= 0) return;

      // Not before they have had a chance to start walking.
      final since = _walkedSince;
      if (since != null &&
          DateTime.now().difference(since) < _sayDistanceAfter) {
        return;
      }

      final say = spokenDistance(short);
      widget.voice.say(
        'अजून $say मीटर चालत जा.',
        en: 'Walk $say metres further.',
      );
      // So the status line does not repeat what was just said.
      _lastSpoken = _status?.mr ?? '';
    });

    _nudge = Timer.periodic(const Duration(seconds: 2), (_) {
      if (!mounted || _recording || !_canRecord) {
        // Not their turn to press anything. The wait starts again when it is.
        _ableSince = null;
        return;
      }
      final now = DateTime.now();
      _ableSince ??= now;
      if (now.difference(_ableSince!) < _nudgeAfter) return;
      final last = _lastNudged;
      if (last != null && now.difference(last) < _nudgeAgainEvery) return;

      _lastNudged = now;
      widget.voice.say(
        'स्क्रीनवरील "व्हिडिओ काढा" हे बटण दाबा.',
        en: 'Press the "Record" button on the screen.',
      );
    });
  }

  @override
  void dispose() {
    _locating?.cancel();
    _nudge?.cancel();
    _walkOn?.cancel();
    _countdown?.cancel();
    // Whatever was still speaking or waiting its turn belongs to this screen.
    //
    // A farmer who recorded a clip and left immediately afterwards heard "this
    // point is recorded, now walk on" again when they next opened the walk: the
    // line was mid-utterance or next in the queue when the screen went, and the
    // queue does not know a screen has closed. An instruction for a moment that
    // has passed is worse than a missed one.
    unawaited(widget.voice.stop());
    unawaited(_positions?.cancel());
    unawaited(_camera?.dispose());
    super.dispose();
  }

  Future<void> _start() async {
    // A walk the phone was killed during opens at the first point still
    // without its clip. Starting from point one would ask the farmer to walk
    // back across the field to re-film what is already on the phone.
    final done = await widget.store.stationsComplete(widget.draftLocalId);
    if (!mounted) return;
    final resumeAt = widget.stations.isNotEmpty
        ? firstStationToCapture(widget.stations, done)
        // With no placed points the clips are numbered in the order they are
        // taken, so the next one is simply the next number not yet done.
        : firstFreePointToCapture(done);
    if (resumeAt >= _total) {
      widget.onFinished?.call();
      Navigator.of(context).pop(true);
      return;
    }
    setState(() => _index = resumeAt);

    await _openCamera();
    if (!mounted) return;

    _positions = widget.positioning.stream.listen(_onPosition);
    await widget.positioning.start();

    // This point is done; the next thing they do is walk.
    //
    // One utterance, not two. Voice.say stops whatever is speaking before it
    // starts, so following this with the station announcement would cut it off
    // mid-sentence -- and the farmer would hear neither properly. How far to walk
    // follows on the timer, once they have had a chance to start.
    widget.voice.say(
      'येथील नोंदणी झाली. आता पुढे चालत जा.',
      en: 'This point is recorded. Now walk on.',
    );
    _lastSpoken = '';
  }

  /// Opens the camera at the best recording quality this phone will accept.
  ///
  /// The encoder negotiates: 15 fps at 2.5 Mbit/s is asked for first, and a
  /// phone that refuses it gets progressively safer profiles down to its own
  /// default. Recording is never prevented by the size budget -- a farmer in a
  /// field whose phone will not record has lost the claim, which is far worse
  /// than a large upload.
  Future<void> _openCamera() async {
    try {
      final controller = await _encoder.open();
      if (!mounted) {
        await controller.dispose();
        return;
      }
      setState(() => _camera = controller);
    } catch (e) {
      if (!mounted) return;
      setState(() {
        _errorMr = 'कॅमेरा सुरू करता आला नाही.';
        _errorEn = deviceError(
          'कॅमेरा सुरू झाला नाही',
          'The camera could not be started.',
          e,
          'walk.camera',
        );
        // The farmer is told something is wrong; this is for whoever helps them
        // afterwards, who otherwise needs a cable to find out what.
        Faults.note('walk.camera', e);
      });
    }
  }

  /// Reopens the camera after the encoder stepped down a rung.
  ///
  /// A controller cannot change its bitrate once initialised, so the only way
  /// for the remaining stations to record smaller clips is a fresh one.
  Future<void> _reopenCamera() async {
    final old = _camera;
    setState(() => _camera = null);
    try {
      await old?.dispose();
    } catch (_) {
      // Disposing a controller that has already lost its platform camera
      // throws, and there is nothing to do about it: the reopen below is what
      // matters.
    }
    await _openCamera();
  }

  void _onPosition(PositionState state) {
    final fix = state.fix;
    if (fix == null) return;
    _fix = fix;
    _reassess();
  }

  /// Whether the position in hand was taken now rather than remembered.
  ///
  /// The remembered one is shown, because a screen that knows roughly where the
  /// farmer is beats an empty wait while the receiver warms up. It may be minutes
  /// old though, and it is these coordinates the office judges the walk on -- a
  /// clip stamped with where the farmer used to be is wrong evidence rather than
  /// missing evidence. So it guides and it does not record.
  /// Whether the position in hand describes where the farmer is standing now.
  ///
  /// It asked only whether the fix came from Android's cache, which missed the other
  /// way a position goes stale and the way the walk actually met: a live stream that
  /// stops delivering. The last reading stayed in memory with the cache flag false,
  /// so it read as current for the rest of the walk while the farmer moved away from
  /// the place it described -- and the button stayed enabled, so a clip could be
  /// stamped with the previous point's coordinates. See Fix.current.
  bool get _fixIsLive => _fix?.current ?? false;

  /// Work out whether the farmer may record here, from the latest position and
  /// everything already recorded.
  ///
  /// # Why this is not only called on a position event
  ///
  /// It used to be, and that made the spacing guard useless in the one case it
  /// exists for.
  ///
  /// The position stream is created with `distanceFilter: 1`, so it emits only
  /// when the handset moves a metre. A farmer who records a clip and does not
  /// move gets no further events -- so the verdict was never recalculated, it
  /// still said "you can record here" from before the last clip existed, and
  /// they could take all four in the same spot.
  ///
  /// The office then refused three of them:
  ///
  ///   422 SAME_POSITION -- "this point of the walk was recorded where another
  ///   one already was: 0 m from the last one, and this field needs 14 m"
  ///
  /// which the app classified as permanent and discarded. The farmer had four
  /// recordings and a claim with one.
  ///
  /// Indoors this is certain, because the fix barely changes. In a field it is
  /// merely likely: a farmer who walks fifteen metres under tree cover may get no
  /// event at all if the fix does not resolve the movement.
  void _reassess() {
    final fix = _fix;
    if (fix == null) return;

    // A position that is no longer current judges nothing.
    //
    // The verdict on screen has to agree with the button, and the button is
    // disabled while the fix is stale. Leaving the last verdict up would say "stop
    // here and record the video" beside a button that does nothing, which is the
    // sort of thing a farmer taps twenty times before giving up on the app.
    if (!fix.current) {
      if (!mounted) return;
      setState(() => _status = null);
      return;
    }

    final target = _target;

    final status = walkStatus(
      fix: fix,
      shape: widget.shape,
      // A free walk numbers its clips in the order they are taken, so the
      // point being recorded is simply the next one.
      station: target?.stationNo ?? _index + 1,
      recorded: _recorded,
      // How far this claim must spread: the office's figure, which arrived with
      // the points, or the same arithmetic done here from the crop's extent.
      stations: widget.stations,
      sownHa: widget.sownHa,
      takenPoints: widget.takenPoints,
    );
    if (!mounted) return;
    setState(() => _status = status);

    // The spoken line is the verdict, not the distance to a point nobody is
    // required to reach: "you can record here" is what a farmer needs, and a
    // countdown to a coordinate they are not being held to is noise.
    //
    // A shortfall is left to the timer above. Spoken from here it would be
    // re-announced on every position event -- about one a metre -- each one
    // stopping the last mid-word.
    if (status.shortfallM == null && status.mr != _lastSpoken) {
      _lastSpoken = status.mr;
      widget.voice.say(status.mr, en: status.en);
    }
  }

  /// Re-reads where this walk has already recorded.
  ///
  /// From the phone's rows rather than from memory, so a walk resumed after
  /// the app was killed still knows where it has been.
  Future<void> _loadRecorded() async {
    final points = await widget.store.recordedStationPoints(
      widget.draftLocalId,
    );
    if (!mounted) return;
    setState(() => _recorded = points);

    // And judge again, now that there is something to judge against.
    //
    // This is loaded asynchronously while the position is arriving, and the
    // position now arrives at once -- the stream replays the fix the phone
    // already has. So the first verdict was reached with an empty list: on
    // re-entering a walk, standing exactly where the last clip was taken, the
    // screen said "you can record here" and meant it, because as far as it knew
    // nothing had been recorded anywhere.
    //
    // The office would have refused that clip as SAME_POSITION, which is the
    // fault this walk has already cost a farmer three recordings over.
    _reassess();
  }

  void _announceStation() {
    final target = _target;
    final n = _index + 1;
    if (target == null) {
      // No placed point to name, so the farmer is told what the walk wants of
      // them instead: another part of the same field.
      widget.voice.say(
        'ठिकाण $n, $_total पैकी. शेताच्या दुसऱ्या भागात जाऊन व्हिडिओ काढा.',
        en:
            'Point $n of $_total. Move to another part of the field and '
            'record.',
      );
      _lastSpoken = '';
      return;
    }
    widget.voice.say(
      'ठिकाण $n, $_total पैकी. ${target.label.mr}.',
      en: 'Point $n of $_total. ${target.label.en}.',
    );
    _lastSpoken = '';
  }

  Future<void> _record() async {
    final camera = _camera;
    if (camera == null || _recording || !_canRecord) return;

    setState(() {
      _recording = true;
      _secondsLeft = _clipSeconds;
    });

    widget.voice.say(
      'व्हिडिओ सुरू. शेतावरून हळू फिरवा.',
      en: 'Recording. Pan slowly across the field.',
    );

    try {
      // The clip only.
      //
      // A still used to be taken here as well and uploaded beside it: two
      // records of one moment, twelve megabytes of video plus a photograph,
      // eight files a farmer's phone had to push over a village connection.
      // The frame the report prints is now taken out of the clip by the
      // office, which costs the farmer nothing and cannot disagree with the
      // clip about where it was recorded.
      await camera.startVideoRecording();
    } catch (e) {
      if (!mounted) return;
      setState(() {
        _recording = false;
        _errorMr = 'व्हिडिओ सुरू करता आला नाही.';
        _errorEn = deviceError(
          'रेकॉर्डिंग सुरू झाले नाही',
          'Recording could not be started.',
          e,
          'walk.record',
        );
        Faults.note('walk.record', e);
      });
      return;
    }

    _countdown = Timer.periodic(const Duration(seconds: 1), (t) {
      if (!mounted) {
        t.cancel();
        return;
      }
      setState(() => _secondsLeft--);
      if (_secondsLeft <= 0) {
        t.cancel();
        unawaited(_stop());
      }
    });
  }

  Future<void> _stop() async {
    final camera = _camera;
    if (camera == null) return;

    _countdown?.cancel();

    XFile clip;
    try {
      clip = await camera.stopVideoRecording();
    } catch (e) {
      if (!mounted) return;
      setState(() {
        _recording = false;
        _errorMr = 'व्हिडिओ जतन करता आला नाही.';
        _errorEn = deviceError(
          'क्लिप साठवता आली नाही',
          'The clip could not be saved.',
          e,
          'walk.save',
        );
        // The worst of the three: the recording happened and did not survive.
        Faults.note('walk.save', e);
      });
      return;
    }

    try {
      await _saveStation(_index + 1, _target, clip);
    } catch (e) {
      if (!mounted) return;
      setState(() {
        _recording = false;
        _errorMr = 'पुरावा फोनवर साठवता आला नाही. जागा शिल्लक आहे का ते पहा.';
        _errorEn = deviceError(
          'पुरावा साठवता आला नाही',
          'The evidence could not be stored. Check there is space left.',
          e,
          'walk.store',
        );
      });
      return;
    }

    // The point just recorded becomes part of what the next one must move
    // away from.
    await _loadRecorded();
    if (!mounted) return;
    setState(() {
      _recording = false;
      _index++;
    });

    // And the verdict is recalculated straight away, rather than waiting for a
    // position event that will not arrive while the farmer stands still. Without
    // this the button stayed enabled and the next clip was taken here again.
    _reassess();

    // They have just pressed it, so the nudge starts counting again from here
    // rather than firing at the next point they reach.
    _ableSince = null;
    _lastNudged = null;
    // And the walking-distance announcements start counting from this clip.
    _walkedSince = DateTime.now();

    if (_index >= _total) {
      // The claim is complete and goes on its own from here -- there is no
      // button left to press. A farmer who pocketed the phone at the last point
      // used to have walked the whole field and filed nothing.
      widget.voice.say(
        'नोंद पूर्ण झाली. नेटवर्क आल्यावर आपोआप पाठवली जाईल.',
        en: 'The claim is complete. It sends by itself once there is signal.',
      );
      widget.onFinished?.call();
      if (mounted) Navigator.of(context).pop(true);
      return;
    }

    _announceStation();
  }

  /// Writes the clip to disk and queues it.
  ///
  /// The row carries the station number and how far from it the farmer stood.
  /// The offset is recorded whether it is two metres or forty: the farmer is
  /// never asked to justify moving, so this number is the only account an
  /// officer gets of it.
  /// [stationNo] is what the clip is filed under; [target] is the placed
  /// point it belongs to, where the office placed any.
  /// A still of what the camera is seeing, for the assistant that just asked to see
  /// something.
  ///
  /// # Why this refuses while a clip is recording
  ///
  /// Because a still taken mid-recording is, on several Android handsets, a recording
  /// that stops. The clip is the evidence and the frame is advice about the next one,
  /// so there is nothing to weigh: the answer during a recording is no, and the
  /// assistant is given the frame a few seconds later when the farmer is walking.
  ///
  /// Returns null rather than throwing on any of it. A conversation that continues by
  /// voice alone has lost very little, and nothing here is worth interrupting a walk.
  Future<Uint8List?> _grabFrame() async {
    final camera = _camera;
    if (camera == null || _recording) return null;
    XFile? shot;
    try {
      shot = await camera.takePicture();
      final original = await File(shot.path).readAsBytes();
      return await liveFrameFrom(original);
    } catch (e) {
      Faults.note('live.frame', e);
      return null;
    } finally {
      // The still is never evidence and never belongs in the claim. Deleted whatever
      // happened to it: a walk that sends several frames would otherwise leave
      // several megabytes of stills in the cache of a phone that has none to spare.
      final taken = shot;
      if (taken != null) {
        unawaited(
          File(taken.path).delete().catchError((_) => File(taken.path)),
        );
      }
    }
  }

  Future<void> _saveStation(int stationNo, Station? target, XFile clip) async {
    final dir = await Photos.directory();
    final fix = _fix;

    // Distance from the assigned point to where the capture was actually
    // taken. Zero under simulation, which is honest: the position was not
    // real, so neither is any offset computed from it. Zero too on a field
    // with no placed points: there is no assigned coordinate to be away
    // from, and inventing one would put a number in front of an officer that
    // means nothing.
    final offset = (widget.simulate || fix == null || target == null)
        ? 0.0
        : metresBetween(fix.lat, fix.lon, target.lat, target.lon);

    Future<int> put(
      XFile file,
      String kind,
      String contentType,
      String extension, {
      int? durationMs,
    }) async {
      final localId =
          '${DateTime.now().microsecondsSinceEpoch}-$stationNo-$kind';
      final dest = p.join(dir.path, '$localId$extension');
      await File(file.path).copy(dest);
      unawaited(File(file.path).delete().catchError((_) => File(file.path)));

      // Hashed as a stream. Read whole, a clip is tens of megabytes held
      // while the camera is still open -- on a 2 GB handset, where Android
      // kills the app mid-walk (docs/SYNC_SCALE_REVIEW.md, H4).
      final size = await File(dest).length();
      final digest = await sha256.bind(File(dest).openRead()).first;

      try {
        await widget.store.putCapture({
          'local_id': localId,
          'draft_local_id': widget.draftLocalId,
          'kind': kind,
          'stage': 'station_$stationNo',
          'caption': target == null
              ? 'Point $stationNo, chosen by the farmer'
              : 'Station $stationNo: ${target.label.en}',
          'file_path': dest,
          'content_type': contentType,
          'byte_size': size,
          // What the phone agreed to record at. Stored because a claim that
          // arrives at 100 MB rather than 20 is a fact about the handset, and
          // the office should be able to see which one without guessing.
          'encoding_profile': _encoder.encoding.label,
          'encoding_fps': _encoder.encoding.fps,
          'encoding_bitrate_bps': _encoder.encoding.bitrate,
          // What the assistant asked for, against the clip that answers it.
          //
          // Stored on the clip rather than on the claim because it is a fact about
          // this recording: an officer reading a close-up of a pod needs to know it
          // was asked for, and an officer reading the other three needs to know they
          // were not.
          'guidance_asked_for': _askedFor,
          'guidance_model': _askedForModel,
          'sha256': digest.toString(),
          'lat': fix?.lat,
          'lon': fix?.lon,
          'accuracy_m': fix?.accuracyM,
          'heading_deg': fix?.headingDeg,
          'captured_at': DateTime.now().toUtc().toIso8601String(),
          'device': 'android',
          'station_no': stationNo,
          'station_offset_m': offset,
          'duration_ms': durationMs,
          'is_mocked': fix?.isMocked == null ? null : (fix!.isMocked! ? 1 : 0),
          // Chosen once and never changed, so a retry resumes the upload it
          // started rather than beginning a second one.
          'upload_key': localId,
          'sent_bytes': 0,
          'status': 'pending',
          'attempts': 0,
        });
      } catch (e) {
        // The row is what makes a capture real: the queue is built from rows,
        // so a file with no row will never be sent and never be cleaned up.
        try {
          await File(dest).delete();
        } catch (_) {
          // The sweep at next startup will take it.
        }
        rethrow;
      }
      return size;
    }

    final bytes = await put(
      clip,
      'station_video',
      'video/mp4',
      '.mp4',
      durationMs: _clipSeconds * 1000,
    );

    // The request has been answered. Cleared here rather than when the next request
    // arrives, so a farmer who was asked for something once does not carry the stamp
    // across the rest of the walk.
    _askedFor = null;
    _askedForModel = null;

    // A clip the office refused over where it was taken, at this same point, is
    // now superseded and can go.
    //
    // Here rather than when the walk opened: the refused clip is what keeps this
    // point unstanding and keeps the card on the home screen, so throwing it
    // away before there was a replacement left a farmer who backed out of the
    // walk with nothing at all. One build did exactly that and lost three
    // recordings.
    await widget.store.replaceRefusedAt(widget.draftLocalId, stationNo);

    // What the phone actually produced, fed back so the rest of the walk
    // adapts.
    //
    // An encoder may accept a bitrate and ignore it, and the only way to find
    // out is to look at the file. A first clip three times its budget means the
    // remaining three would be too, so the rung steps down and the camera is
    // reopened -- a controller cannot change its bitrate once initialised.
    _clipBytesSoFar += bytes;
    if (ClipEncoder.exceededBudget(_clipBytesSoFar)) {
      // Logged rather than shown. There is nothing the farmer can do about
      // their phone's encoder, and a warning at station three would only
      // frighten somebody into stopping a walk that is working. The office can
      // see it from the stored profile and the byte sizes.
      debugPrint(
        'clip budget: this walk has reached $_clipBytesSoFar bytes, '
        'over the $claimBudgetBytes budget, at profile '
        '"${_encoder.encoding.label}"',
      );
    }
    if (_encoder.sawClip(bytes) && mounted) {
      await _reopenCamera();
    }
  }

  @override
  Widget build(BuildContext context) {
    if (_errorMr != null) {
      return Scaffold(
        appBar: AppBar(title: Text(tr('शेत पाहणी', 'Field walk'))),
        body: Center(
          child: Padding(
            padding: const EdgeInsets.all(32),
            child: Column(
              mainAxisSize: MainAxisSize.min,
              children: [
                Container(
                  width: 88,
                  height: 88,
                  decoration: const BoxDecoration(
                    color: Palette.severeBg,
                    shape: BoxShape.circle,
                  ),
                  child: const Icon(
                    Icons.videocam_off_outlined,
                    size: 40,
                    color: Palette.severeFg,
                  ),
                ),
                const SizedBox(height: Space.xl),
                Text(
                  tr(_errorMr!, Language.of(_errorEn ?? '')),
                  textAlign: TextAlign.center,
                  style: Theme.of(context).textTheme.titleMedium,
                ),
              ],
            ),
          ),
        ),
      );
    }

    final target = _target;
    final status = _status;
    final camera = _camera;
    final total = _total;
    final api = widget.api;

    return Scaffold(
      backgroundColor: Colors.black,
      body: SafeArea(
        child: Column(
          children: [
            if (widget.simulate)
              Container(
                width: double.infinity,
                color: const Color(0xFF7F1D1D),
                padding: const EdgeInsets.symmetric(
                  vertical: 6,
                  horizontal: 12,
                ),
                child: const Text(
                  'DEBUG · simulated position',
                  style: TextStyle(color: Colors.white, fontSize: 12),
                ),
              ),

            // Which point, out of how many. A farmer who cannot see how much
            // is left will assume it is endless and stop.
            Padding(
              padding: const EdgeInsets.fromLTRB(4, 4, 12, 8),
              child: Row(
                children: [
                  IconButton(
                    onPressed: () => Navigator.of(context).maybePop(),
                    icon: const Icon(Icons.close, color: Colors.white),
                    tooltip: tr('बंद करा', 'Close'),
                  ),
                  Expanded(
                    child: Column(
                      crossAxisAlignment: CrossAxisAlignment.start,
                      children: [
                        Text(
                          tr(
                            'ठिकाण ${_index + 1} / $total',
                            'Point ${_index + 1} of $total',
                          ),
                          style: const TextStyle(
                            color: Colors.white,
                            fontSize: 16,
                            fontWeight: FontWeight.w600,
                          ),
                        ),
                        const SizedBox(height: 6),
                        ClipRRect(
                          borderRadius: BorderRadius.circular(99),
                          child: LinearProgressIndicator(
                            value: total == 0 ? 0 : _index / total,
                            minHeight: 4,
                            backgroundColor: Colors.white24,
                            color: Palette.leaf,
                          ),
                        ),
                      ],
                    ),
                  ),
                  const SizedBox(width: Space.md),
                  if (_fix != null)
                    _Chip(
                      icon: Icons.gps_fixed,
                      label: '± ${_fix!.accuracyM.round()} m',
                      color: _fix!.good
                          ? const Color(0xFF7BD389)
                          : const Color(0xFFF2B35B),
                    ),
                ],
              ),
            ),

            Expanded(
              child: camera != null
                  ? ClipRRect(
                      borderRadius: BorderRadius.circular(Radii.lg),
                      child: CameraPreview(camera),
                    )
                  : Center(
                      child: Text(
                        tr('कॅमेरा सुरू होत आहे…', 'Starting camera…'),
                        style: const TextStyle(color: Colors.white70),
                      ),
                    ),
            ),

            // The instruction. Read at arm's length, outdoors, by somebody who
            // is walking -- and spoken as well, which is why this screen keeps
            // the voice.
            Container(
              width: double.infinity,
              padding: const EdgeInsets.fromLTRB(20, 16, 20, 16),
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  if (target != null)
                    Text(
                      tr(target.label.mr, target.label.en),
                      style: const TextStyle(
                        color: Colors.white60,
                        fontSize: 13.5,
                      ),
                    ),
                  const SizedBox(height: 4),
                  Row(
                    children: [
                      Expanded(
                        child: Text(
                          _recording
                              ? tr(
                                  'व्हिडिओ सुरू — $_secondsLeft सेकंद',
                                  'Recording — ${_secondsLeft}s',
                                )
                              : (status == null
                                    ? (_fix == null
                                          ? tr(
                                              'स्थान शोधत आहे…',
                                              'Finding location…',
                                            )
                                          // A fix in hand that has gone stale: the
                                          // receiver stopped sending and is being
                                          // asked again. Said differently from a
                                          // cold start, because it is a different
                                          // thing and the farmer has already
                                          // waited once.
                                          : tr(
                                              'ठिकाण पुन्हा तपासत आहे…',
                                              'Checking your position again…',
                                            ))
                                    : tr(status.mr, status.en)),
                          style: const TextStyle(
                            color: Colors.white,
                            fontSize: 19,
                            height: 1.35,
                            fontWeight: FontWeight.w500,
                          ),
                        ),
                      ),
                    ],
                  ),
                  const SizedBox(height: Space.lg),

                  // The assistant, where there is one. Renders nothing at all on a
                  // build without it, on a farm too slow for it, or for a farmer who
                  // declined -- which is why it sits here unconditionally rather than
                  // behind a check this screen would have to keep in step with.
                  //
                  // Above the record button and below the instruction, because that is
                  // the order they matter in: what to do, then what somebody suggests
                  // about it, then the button.
                  if (api != null &&
                      widget.farmerId.isNotEmpty &&
                      widget.mode.live)
                    LiveTalk(
                      store: widget.store,
                      voice: widget.voice,
                      api: api,
                      farmerId: widget.farmerId,
                      mode: widget.mode,
                      crop: widget.crop,
                      calamity: widget.calamity,
                      season: widget.season,
                      grabFrame: _grabFrame,
                      onAskedFor: (request, model) {
                        // Held, not acted on. The assistant does not start a
                        // recording and does not require one: the farmer presses the
                        // button, and if they press it the clip carries what was
                        // asked for.
                        _askedFor = request;
                        _askedForModel = model;
                      },
                    ),

                  if (_recording)
                    ClipRRect(
                      borderRadius: BorderRadius.circular(99),
                      child: LinearProgressIndicator(
                        value: 1 - (_secondsLeft / _clipSeconds),
                        minHeight: 6,
                        backgroundColor: Colors.white24,
                        color: const Color(0xFFE5484D),
                      ),
                    )
                  else ...[
                    SizedBox(
                      width: double.infinity,
                      child: FilledButton.icon(
                        onPressed: _canRecord ? _record : null,
                        icon: const Icon(Icons.fiber_manual_record, size: 18),
                        label: Text(tr('व्हिडिओ काढा', 'Record')),
                        style: FilledButton.styleFrom(
                          minimumSize: const Size.fromHeight(54),
                          backgroundColor: const Color(0xFFD93A3F),
                          disabledBackgroundColor: Colors.white12,
                          disabledForegroundColor: Colors.white38,
                        ),
                      ),
                    ),
                  ],
                ],
              ),
            ),
          ],
        ),
      ),
    );
  }
}

/// A small status chip on the dark camera screen.
class _Chip extends StatelessWidget {
  const _Chip({required this.icon, required this.label, required this.color});

  final IconData icon;
  final String label;
  final Color color;

  @override
  Widget build(BuildContext context) {
    return Container(
      padding: const EdgeInsets.symmetric(horizontal: 10, vertical: 5),
      decoration: BoxDecoration(
        color: Colors.white10,
        borderRadius: BorderRadius.circular(99),
      ),
      child: Row(
        mainAxisSize: MainAxisSize.min,
        children: [
          Icon(icon, size: 14, color: color),
          const SizedBox(width: 4),
          Text(label, style: TextStyle(color: color, fontSize: 12.5)),
        ],
      ),
    );
  }
}
