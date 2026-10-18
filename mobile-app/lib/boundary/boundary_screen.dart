import 'dart:async';

import 'package:flutter/material.dart';

import '../core/config.dart';
import '../core/db.dart';
import '../core/language.dart';
import '../core/sensors.dart';
import '../core/sync.dart';
import '../core/voice.dart';
import '../ui/voice_button.dart';
import '../ui/theme.dart';
import '../core/messages.dart';

/// Walking the edge of a plot.
///
/// # Why a farmer is asked to do this at all
///
/// The land registry returns no boundary for their field. Without one the app
/// cannot check that a photograph was taken inside their own plot, the
/// satellite cannot find the field to look at, and no assessment can be made
/// at all. Five minutes of walking, once per plot, replaces a record that does
/// not exist -- and it serves every claim they ever file on that land.
///
/// # Why the walk is saved before it is sent
///
/// A farmer walks their boundary standing in the field, which is exactly where
/// there is no signal. Sending it at the moment they press finish and
/// reporting a failure throws away five minutes of somebody pacing a field
/// edge in the sun, and they have no way to know it was lost until they walk
/// it again.
///
/// # Why a poor fix blocks a corner rather than warning about one
///
/// A corner marked on a fifty-metre fix does not merely add noise: it moves
/// the whole edge, silently, and the boundary can end up on a neighbour's
/// field. That is not a thing to let somebody do and regret. Standing still in
/// the open for half a minute fixes it, and the screen says so.
class BoundaryScreen extends StatefulWidget {
  const BoundaryScreen({
    super.key,
    required this.store,
    required this.voice,
    required this.positioning,
    required this.sync,
    required this.farmId,
    required this.surveyNumber,
    this.registryExtentHa,
    this.onDone,
  });

  final Store store;
  final Voice voice;
  final Positioning positioning;
  final Sync sync;

  final String farmId;
  final String surveyNumber;

  /// What the registry holds for this survey number, so the farmer can see
  /// their walk against it as they go. The two disagreeing is a fact worth
  /// showing, and never one this app resolves.
  final double? registryExtentHa;

  final VoidCallback? onDone;

  @override
  State<BoundaryScreen> createState() => _BoundaryScreenState();
}

class _BoundaryScreenState extends State<BoundaryScreen> {
  final _corners =
      <({double lat, double lon, double accuracyM, DateTime at})>[];

  PositionState _position = const PositionState(FixStatus.idle);
  StreamSubscription<PositionState>? _sub;

  bool _saving = false;
  String? _saved;

  @override
  void initState() {
    super.initState();
    _sub = widget.positioning.stream.listen((s) {
      if (!mounted) return;
      setState(() => _position = s);
    });
    // A fresh walk. Without this the best fix from another field follows them.
    widget.positioning.resetBest();
    unawaited(widget.positioning.start());

    WidgetsBinding.instance.addPostFrameCallback((_) {
      unawaited(
        widget.voice.say(Lines.boundaryStart.mr, en: Lines.boundaryStart.en),
      );
    });
  }

  /// What "say that again" repeats. A boundary walk is long and the
  /// instruction that matters changes once, at the first corner: before it,
  /// how to start; after it, what a corner is for.
  ({String mr, String en}) get _lastLine =>
      _corners.isEmpty ? Lines.boundaryStart : Lines.cornerRecorded;

  @override
  void dispose() {
    unawaited(_sub?.cancel());
    unawaited(widget.voice.stop());
    super.dispose();
  }

  bool get _canDrop {
    final fix = _position.fix;
    return fix != null && fix.accuracyM <= Config.worstUsableAccuracyM;
  }

  double get _areaHa =>
      hectaresOf([for (final c in _corners) (lat: c.lat, lon: c.lon)]);

  double get _worstAccuracy =>
      _corners.fold(0.0, (m, c) => c.accuracyM > m ? c.accuracyM : m);

  void _drop() {
    final fix = _position.fix;
    if (fix == null || !_canDrop) return;

    setState(() {
      _corners.add((
        lat: fix.lat,
        lon: fix.lon,
        accuracyM: fix.accuracyM,
        at: fix.at,
      ));
    });
    unawaited(
      widget.voice.repeat(Lines.cornerRecorded.mr, en: Lines.cornerRecorded.en),
    );
  }

  Future<void> _finish() async {
    if (_corners.length < 3 || _saving) return;
    setState(() => _saving = true);

    try {
      await widget.store.putBoundary(
        farmId: widget.farmId,
        surveyNumber: widget.surveyNumber,
        points: [
          for (final c in _corners)
            {
              'lat': c.lat,
              'lon': c.lon,
              'accuracy_m': c.accuracyM,
              'at': c.at.toUtc().toIso8601String(),
            },
        ],
        areaHa: _areaHa,
        worstAccuracyM: _worstAccuracy,
      );

      if (!mounted) return;
      setState(() => _saved = 'हद्द या फोनवर सुरक्षित आहे.');
      unawaited(
        widget.voice.repeat(Lines.boundaryDone.mr, en: Lines.boundaryDone.en),
      );
      widget.onDone?.call();

      // Sent if anything can get out. A failure changes nothing: it is already
      // queued and the sync engine will keep trying.
      unawaited(widget.sync.run());
    } catch (e) {
      if (!mounted) return;
      setState(() => _saving = false);
      ScaffoldMessenger.of(context).showSnackBar(
        SnackBar(
          content: Text(
            Language.of(
              deviceError(
                'साठवता आले नाही',
                'Could not save. Try again.',
                e,
                'boundary.save',
              ),
            ),
          ),
        ),
      );
    }
  }

  @override
  Widget build(BuildContext context) {
    if (_saved != null) return _done();
    final text = Theme.of(context).textTheme;

    return Scaffold(
      appBar: AppBar(
        title: Text(tr('शेताची हद्द', 'Field boundary')),
        actions: [
          SayAgainButton(voice: widget.voice, line: _lastLine, compact: true),
          VoiceButton(voice: widget.voice),
        ],
      ),
      body: Column(
        children: [
          _AccuracyBar(state: _position),
          Expanded(
            child: ListView(
              padding: Space.page,
              children: [
                const SizedBox(height: Space.sm),
                Text(
                  tr(
                    'सर्व्हे नं. ${widget.surveyNumber}',
                    'Survey no. ${widget.surveyNumber}',
                  ),
                  style: text.titleMedium,
                ),
                const SizedBox(height: Space.xs),
                Text(
                  tr(
                    'शेताच्या कडेने चाला. प्रत्येक कोपऱ्यावर थांबून खालील बटण '
                        'दाबा.',
                    'Walk the edge of the field. Stop at each corner and '
                        'press the button below.',
                  ),
                  style: text.bodyMedium?.copyWith(color: Palette.inkMuted),
                ),
                const SizedBox(height: Space.lg),
                _Tally(
                  corners: _corners.length,
                  areaHa: _areaHa,
                  registryExtentHa: widget.registryExtentHa,
                  worstAccuracyM: _worstAccuracy,
                ),
                if (!_canDrop && _position.status == FixStatus.ready)
                  Notice(
                    tone: Tone.danger,
                    icon: Icons.gps_not_fixed,
                    title: tr(
                      'ठिकाण नीट मिळत नाही',
                      'The position is too uncertain',
                    ),
                    message: tr(
                      'मोकळ्या जागी जा आणि थोडा वेळ थांबा. आत्ता नोंदवलेला '
                          'कोपरा शेजाऱ्याच्या शेतावर जाऊ शकतो.',
                      'Move into the open and wait a moment. A corner marked '
                          'now could land on a neighbour’s field.',
                    ),
                  ),
              ],
            ),
          ),
          _Controls(
            corners: _corners.length,
            canDrop: _canDrop,
            saving: _saving,
            onDrop: _drop,
            onUndo: _corners.isEmpty
                ? null
                : () => setState(() => _corners.removeLast()),
            onRestart: _corners.isEmpty ? null : () => setState(_corners.clear),
            onFinish: _corners.length >= 3 ? _finish : null,
          ),
        ],
      ),
    );
  }

  Widget _done() {
    final text = Theme.of(context).textTheme;
    return Scaffold(
      appBar: AppBar(title: Text(tr('हद्द नोंदवली', 'Boundary saved'))),
      body: SafeArea(
        child: Padding(
          padding: const EdgeInsets.all(24),
          child: Column(
            children: [
              const Spacer(),
              Container(
                width: 88,
                height: 88,
                decoration: const BoxDecoration(
                  color: Palette.negligibleBg,
                  shape: BoxShape.circle,
                ),
                child: const Icon(
                  Icons.check_rounded,
                  size: 44,
                  color: Palette.negligibleFg,
                ),
              ),
              const SizedBox(height: Space.xl),
              Text(
                tr(_saved!, 'Your field boundary is saved on this phone.'),
                textAlign: TextAlign.center,
                style: text.titleLarge,
              ),
              const SizedBox(height: Space.sm),
              // Said plainly, because a farmer who thinks it has been sent
              // will worry when they see it still queued, and one who thinks
              // it has been lost will walk the field again.
              Text(
                tr(
                  'नेटवर्क आल्यावर ती आपोआप कार्यालयाला पाठवली जाईल.',
                  'It goes to the office by itself when there is a signal.',
                ),
                textAlign: TextAlign.center,
                style: text.bodyMedium?.copyWith(color: Palette.inkMuted),
              ),
              const Spacer(),
              BigButton(
                label: tr('पूर्ण', 'Done'),
                onPressed: () => Navigator.of(context).maybePop(),
              ),
            ],
          ),
        ),
      ),
    );
  }
}

/// The fix, always visible.
///
/// This is what decides whether the boundary lands on their field or the next
/// one, and it is the one thing on screen a farmer can actually improve by
/// standing still.
class _AccuracyBar extends StatelessWidget {
  const _AccuracyBar({required this.state});

  final PositionState state;

  @override
  Widget build(BuildContext context) {
    final fix = state.fix;
    final described = fix == null ? null : describeAccuracy(fix.accuracyM);

    final (bg, fg) = switch (described?.tone) {
      AccuracyTone.good => (Palette.negligibleBg, Palette.negligibleFg),
      AccuracyTone.fair => (Palette.moderateBg, Palette.moderateFg),
      _ => (Palette.severeBg, Palette.severeFg),
    };

    return Container(
      width: double.infinity,
      color: bg,
      padding: const EdgeInsets.symmetric(horizontal: 16, vertical: 10),
      child: Row(
        children: [
          Icon(Icons.gps_fixed, size: 18, color: fg),
          const SizedBox(width: 10),
          Expanded(
            child: Text(
              fix == null
                  ? Language.of(
                      state.reason ?? 'ठिकाण शोधत आहे… · Finding your position',
                    )
                  : '${tr(described!.mr, described.en)} · '
                        '±${fix.accuracyM.round()} m',
              style: TextStyle(
                color: fg,
                fontSize: 14,
                height: 1.4,
                fontWeight: FontWeight.w500,
              ),
            ),
          ),
        ],
      ),
    );
  }
}

/// What the walk encloses so far.
class _Tally extends StatelessWidget {
  const _Tally({
    required this.corners,
    required this.areaHa,
    required this.worstAccuracyM,
    this.registryExtentHa,
  });

  final int corners;
  final double areaHa;
  final double worstAccuracyM;
  final double? registryExtentHa;

  @override
  Widget build(BuildContext context) {
    final text = Theme.of(context).textTheme;
    return AppCard(
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Row(
            children: [
              _Stat(value: '$corners', label: tr('कोपरे', 'Corners')),
              const SizedBox(width: Space.xl),
              if (areaHa > 0)
                // Guntha alongside hectares, because a farmer in Maharashtra
                // says guntha and the record is kept in hectares.
                _Stat(
                  value: areaHa.toStringAsFixed(2),
                  label: tr(
                    'हेक्टर (${gunthaOf(areaHa).toStringAsFixed(0)} गुंठे)',
                    'hectares',
                  ),
                ),
            ],
          ),
          if ((registryExtentHa ?? 0) > 0 || worstAccuracyM > 0) ...[
            const SizedBox(height: Space.md),
            const Divider(),
            const SizedBox(height: Space.md),
          ],
          if (registryExtentHa != null && registryExtentHa! > 0)
            Text(
              tr(
                'नोंदीनुसार ${registryExtentHa!.toStringAsFixed(2)} हेक्टर',
                'Registry: ${registryExtentHa!.toStringAsFixed(2)} ha',
              ),
              style: text.bodySmall,
            ),
          if (worstAccuracyM > 0)
            Text(
              tr(
                'सर्वात कमजोर कोपरा ±${worstAccuracyM.round()} m',
                'Weakest corner ±${worstAccuracyM.round()} m',
              ),
              style: text.bodySmall,
            ),
        ],
      ),
    );
  }
}

class _Stat extends StatelessWidget {
  const _Stat({required this.value, required this.label});

  final String value;
  final String label;

  @override
  Widget build(BuildContext context) {
    final text = Theme.of(context).textTheme;
    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        Text(
          value,
          style: text.headlineMedium?.copyWith(color: Palette.primaryDark),
        ),
        Text(label, style: text.bodySmall),
      ],
    );
  }
}

class _Controls extends StatelessWidget {
  const _Controls({
    required this.corners,
    required this.canDrop,
    required this.saving,
    required this.onDrop,
    this.onUndo,
    this.onRestart,
    this.onFinish,
  });

  final int corners;
  final bool canDrop;
  final bool saving;
  final VoidCallback onDrop;
  final VoidCallback? onUndo;
  final VoidCallback? onRestart;
  final VoidCallback? onFinish;

  @override
  Widget build(BuildContext context) {
    return DecoratedBox(
      decoration: const BoxDecoration(
        color: Palette.surface,
        border: Border(top: BorderSide(color: Palette.outlineSoft)),
      ),
      child: SafeArea(
        top: false,
        child: Padding(
          padding: const EdgeInsets.fromLTRB(16, 12, 16, 12),
          child: Column(
            children: [
              // The primary act, and the largest thing on the screen: somebody
              // walking a field edge presses this a dozen times without
              // looking closely at it.
              SizedBox(
                width: double.infinity,
                child: FilledButton.icon(
                  onPressed: canDrop ? onDrop : null,
                  style: FilledButton.styleFrom(
                    minimumSize: const Size.fromHeight(60),
                  ),
                  icon: const Icon(Icons.add_location_alt_outlined),
                  label: Text(tr('हा कोपरा नोंदवा', 'Mark this corner')),
                ),
              ),
              const SizedBox(height: Space.sm),
              Row(
                children: [
                  TextButton.icon(
                    onPressed: onUndo,
                    icon: const Icon(Icons.undo, size: 20),
                    label: Text(tr('मागे', 'Undo')),
                  ),
                  TextButton.icon(
                    onPressed: onRestart,
                    icon: const Icon(Icons.restart_alt, size: 20),
                    label: Text(tr('पुन्हा सुरू', 'Restart')),
                  ),
                  const Spacer(),
                  FilledButton.tonal(
                    onPressed: saving ? null : onFinish,
                    child: Text(
                      saving
                          ? tr('जतन…', 'Saving…')
                          : corners < 3
                          ? tr('किमान ३ कोपरे', 'Min. 3 corners')
                          : tr('पूर्ण करा', 'Finish'),
                    ),
                  ),
                ],
              ),
            ],
          ),
        ),
      ),
    );
  }
}
