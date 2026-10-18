import 'dart:async';

import 'package:flutter/foundation.dart';
import 'package:flutter/material.dart';

import '../core/api.dart';
import '../core/db.dart';
import '../core/faults.dart';
import '../core/language.dart';
import '../core/link_quality.dart';
import '../core/live_consent.dart';
import '../core/voice.dart';
import '../ui/theme.dart';
import 'live_audio.dart';
import 'live_session.dart';
import 'survey_mode.dart';

/// The assistant, on the screen where it is any use.
///
/// # Why this is one widget and not wiring spread through the walk
///
/// Because everything about it is conditional. The build flag, the farmer's
/// agreement, the link, the office's budget and the microphone can each say no, and a
/// walk screen that asked all five questions itself would have the conversation's
/// lifecycle tangled through a screen whose real job is recording evidence. Kept
/// whole, the walk holds one widget and one callback, and this can be deleted without
/// touching the walk.
///
/// # What it renders when the answer is no
///
/// Nothing at all -- not a disabled button, not an explanation. The reasons are
/// interesting to us and useless to a farmer: "the link here is too slow" reads as a
/// fault in an app that is about to work perfectly well without it. The one exception
/// is a session that ends early, which they watched happen and deserve a sentence
/// about.
///
/// # Why the scripted voice wins
///
/// See [Voice.busy]. The walk's own instructions are what get the claim filed; the
/// assistant's advice is an addition, and two voices out of one speaker is neither.
class LiveTalk extends StatefulWidget {
  const LiveTalk({
    super.key,
    required this.store,
    required this.voice,
    required this.api,
    required this.farmerId,
    required this.mode,
    this.reportId,
    this.crop,
    this.calamity,
    this.season,
    this.onAskedFor,
    this.grabFrame,
    this.audio,
    this.session,
    this.probe,
  });

  final Store store;
  final Voice voice;
  final Api api;

  /// Whose consent governs, and whose daily allowance is spent.
  ///
  /// Used for those two things and sent to the office, which already knows it. It is
  /// not sent onward to the model: the relay is given the crop, the calamity, the
  /// season and the district, and nothing that names a person.
  final String farmerId;

  /// What was decided before the survey started. See ModeGate.
  ///
  /// Passed in rather than worked out here, and that is the point: this widget used
  /// to measure the link itself when the walk screen opened, so the assistant could
  /// appear at one point of a walk and be gone at the next as the signal moved. A
  /// farmer who is being talked to and then is not reads that as a fault, and the
  /// measurement was real traffic on the connection it was being careful about.
  final SurveyMode mode;
  final String? reportId;
  final String? crop;
  final String? calamity;
  final String? season;

  /// Called with each thing the assistant asks the farmer to show, and which model
  /// asked for it.
  ///
  /// The walk stores the latest against the next clip, which is what lets an officer
  /// tell a directed observation from whatever the farmer happened to point at -- and
  /// the model, because a year later "the assistant suggested it" is only useful if
  /// it says which assistant.
  final void Function(String request, String? model)? onAskedFor;

  /// A still of what the camera is seeing, or null if now is not the moment.
  ///
  /// Asked for only when the assistant has asked for something -- never on a timer,
  /// and never while a clip is recording. The walk owns the camera and the answer,
  /// including the answer "not now".
  final Future<Uint8List?> Function()? grabFrame;

  /// Replaced in tests, which is the only way to hold a conversation with no
  /// microphone, no speaker, no network and no model.
  final LiveAudio? audio;
  final LiveSession? session;
  final LinkProbe? probe;

  @override
  State<LiveTalk> createState() => _LiveTalkState();
}

class _LiveTalkState extends State<LiveTalk> {
  LiveSession? _session;
  LiveAudio? _audio;
  late final LiveConsent _consent = LiveConsent(widget.store);

  /// The probe the decision already used, so a session does not measure again.
  ///
  /// The gate measured this farm once before the walk began. Handing the same probe
  /// to the session keeps that one answer authoritative for the survey: it caches
  /// for three minutes, but only within itself, so a probe built here would be a
  /// second measurement of the same link to answer a question already answered.
  late final LinkProbe _probe = widget.probe ?? LinkProbe(api: widget.api);

  /// Why a session stopped, for the one case worth a sentence.
  String? _endedNote;

  /// How many requests have been passed up, so the same one is not sent twice.
  int _reported = 0;

  bool _busy = false;

  /// Whether the assistant is on offer.
  ///
  /// Decided before the survey started and not revisited here -- see [LiveTalk.mode].
  /// A session that ends early sets this false, because the reason it ended is a
  /// reason not to offer another immediately.
  bool _offered = true;

  @override
  void initState() {
    super.initState();
    // The conversation begins with the walk, because the farmer already chose it.
    //
    // There was a "talk to the assistant" button on this screen, and it was the
    // wrong shape: it asked somebody to choose between two ways of working while
    // they were already doing one of them, on the screen where their attention
    // belongs to a camera and a field. The choice is made before the walk opens now
    // -- see ModeGate -- so by the time this is built the answer is yes and the only
    // thing left is to connect.
    //
    // Guarded on the mode all the same. Without it an offline survey opened a session
    // it had already been refused and put the refusal on the camera screen, which is
    // the one thing an offline survey must never do: say a word about a feature the
    // farmer was never offered and does not need.
    if (widget.mode.live) unawaited(_open());
  }

  /// Open the conversation.
  Future<void> _open() async {
    if (_busy) return;
    setState(() {
      _busy = true;
      _endedNote = null;
    });

    final session =
        widget.session ??
        LiveSession(
          api: widget.api,
          consent: _consent,
          probe: _probe,
          farmerId: widget.farmerId,
          reportId: widget.reportId,
        );
    _session = session;
    session.addListener(_onSession);

    // Offer re-runs the gates the session itself owns. Cheap, and it is what makes
    // the session the single place that decides -- rather than this screen's own
    // reading of the same conditions, taken a minute earlier.
    if (!await session.offer()) {
      if (!mounted) return;
      setState(() {
        _busy = false;
        _offered = false;
        _endedNote = session.unavailableBecause;
      });
      return;
    }

    final started = await session.start(
      crop: widget.crop,
      calamity: widget.calamity,
      season: widget.season,
    );
    if (!mounted) return;
    if (!started) {
      setState(() {
        _busy = false;
        _endedNote = session.unavailableBecause;
      });
      return;
    }

    // Who holds the microphone depends on which transport won, so this cannot be
    // decided until the session is open.
    //
    // Over WebRTC the platform captures, encodes and sends it, and decodes and plays
    // the reply -- Dart never sees a sample. Starting our own capture there would be
    // a second claimant on one microphone, and Android gives it to neither: the
    // recorder fails or the track goes silent, and both look like an assistant that
    // is not listening. Over the socket Dart does both, because nothing else will.
    if (!session.audioIsOurs) {
      setState(() => _busy = false);
      return;
    }

    final audio = widget.audio ?? LiveAudio();
    _audio = audio;
    await audio.prepareSpeaker();
    session.onAudio = _play;

    // A microphone that will not stay open ends the conversation rather than leaving
    // a farmer talking into nothing while the meter runs. It picks itself up three
    // times first -- the camera opening takes the audio focus and the stream dies --
    // so this is the case where it genuinely will not hold.
    audio.onLost = (why) {
      Faults.note('live.mic.lost', why);
      if (!mounted) return;
      unawaited(session.stop('the microphone was lost'));
      setState(
        () => _endedNote = tr(
          'मायक्रोफोन बंद पडला, म्हणून संवाद थांबला. पाहणी सुरू ठेवा.',
          'The microphone stopped, so the conversation ended. Carry on with '
              'the walk.',
        ),
      );
    };

    final listening = await audio.startCapture(session.sendAudio);
    if (!mounted) return;
    if (!listening) {
      // Connected, and cannot hear. Ended rather than left open: a farmer talking
      // into a session that receives nothing gets silence back and no way to tell
      // why, and the session is being billed by the minute meanwhile.
      await session.stop('no microphone');
      if (!mounted) return;
      setState(() {
        _busy = false;
        _endedNote = tr(
          'मायक्रोफोन सुरू झाला नाही. पाहणी सुरू ठेवा.',
          'The microphone would not start. Carry on with the walk.',
        );
      });
      return;
    }

    setState(() => _busy = false);
  }

  /// Play a chunk of the assistant's voice, unless the walk is talking.
  void _play(Uint8List pcm) {
    if (widget.voice.busy) return;
    unawaited(_audio?.play(pcm));
  }

  void _onSession() {
    final session = _session;
    if (session == null || !mounted) return;

    // Anything newly asked for goes to the walk, which stamps it on the next clip.
    final asked = session.askedFor;
    while (_reported < asked.length) {
      final request = asked[_reported++];
      widget.onAskedFor?.call(request, session.model);
      // And the assistant is given something to look at, because it has just said
      // it wants to see something. One frame, here and nowhere else: this is the
      // only moment in the walk where a picture is worth what it costs to send.
      unawaited(_showIt());
    }

    if (session.state == LiveState.ended) {
      unawaited(_audio?.stopCapture());
      setState(
        () => _endedNote = tr(
          'सहाय्यकाशी संवाद थांबला. पाहणी सुरू ठेवा.',
          'The conversation has ended. Carry on with the walk.',
        ),
      );
      return;
    }
    setState(() {});
  }

  Future<void> _showIt() async {
    final grab = widget.grabFrame;
    final session = _session;
    if (grab == null || session == null) return;
    try {
      final jpeg = await grab();
      if (jpeg != null) session.sendFrame(jpeg);
    } catch (e) {
      // A still that could not be taken -- the camera is recording, or busy. The
      // conversation carries on by voice, which is most of it.
      Faults.note('live.frame', e);
    }
  }

  Future<void> _close() async {
    final session = _session;
    if (session == null) return;
    setState(() => _busy = true);
    await _audio?.stopCapture();
    await session.stop('the farmer closed it');
    if (!mounted) return;
    setState(() => _busy = false);
  }

  @override
  void dispose() {
    // A conversation belongs to this screen. Left running it would keep the
    // microphone open and the meter going after the farmer walked away from it.
    _session?.removeListener(_onSession);
    unawaited(_session?.stop('the screen closed'));
    unawaited(_audio?.dispose());
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final session = _session;
    final live =
        session != null &&
        (session.state == LiveState.listening ||
            session.state == LiveState.speaking);

    if (!live) {
      // An offline survey renders nothing here at all -- not a disabled control and
      // not an explanation. The reasons are useful to us and useless to a farmer,
      // for whom "the link here is too slow" reads as a fault in an app that is
      // about to work perfectly well without it.
      if (!widget.mode.live || !_offered) {
        return _endedNote == null
            ? const SizedBox.shrink()
            : _note(_endedNote!);
      }
      // Connecting, or connected and between turns. Said rather than left blank,
      // because a farmer who chose to be talked to and hears nothing needs to know
      // it is coming.
      return Padding(
        padding: const EdgeInsets.only(bottom: Space.md),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.stretch,
          children: [
            if (_endedNote != null) _note(_endedNote!),
            if (_endedNote == null)
              _note(tr('सहाय्यक जोडत आहे…', 'Connecting the assistant…')),
          ],
        ),
      );
    }

    return Container(
      margin: const EdgeInsets.only(bottom: Space.md),
      padding: const EdgeInsets.all(Space.md),
      decoration: BoxDecoration(
        color: Colors.white10,
        borderRadius: BorderRadius.circular(Radii.md),
        border: Border.all(color: Colors.white24),
      ),
      child: Row(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Icon(
            session.state == LiveState.speaking
                ? Icons.volume_up
                : Icons.mic_none,
            size: 20,
            color: const Color(0xFF7BD389),
          ),
          const SizedBox(width: Space.md),
          Expanded(
            child: Text(
              // What it last said, on screen as well as aloud. A field is a noisy
              // place and a sentence that was missed is a sentence that may as well
              // not have been spoken.
              session.said ?? tr('बोला…', 'Speak…'),
              maxLines: 3,
              overflow: TextOverflow.ellipsis,
              style: const TextStyle(color: Colors.white, fontSize: 15),
            ),
          ),
          const SizedBox(width: Space.sm),
          IconButton(
            onPressed: _busy ? null : () => unawaited(_close()),
            icon: const Icon(Icons.stop_circle_outlined),
            color: Colors.white70,
            tooltip: tr('संवाद थांबवा', 'Stop the conversation'),
          ),
        ],
      ),
    );
  }

  Widget _note(String text) => Padding(
    padding: const EdgeInsets.only(bottom: Space.sm),
    child: Text(
      text,
      style: const TextStyle(color: Colors.white60, fontSize: 13.5),
    ),
  );
}
