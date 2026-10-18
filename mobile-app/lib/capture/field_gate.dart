import 'dart:async';

import 'package:flutter/material.dart';

import '../core/language.dart';
import '../core/parcel_location.dart';
import '../core/sensors.dart';
import '../core/voice.dart';
import '../ui/theme.dart';
import 'station_guide.dart';

/// How far from the nearest assigned point still counts as at the farm, for a
/// parcel whose outline the phone does not have.
const double nearStationWithinM = 100;

/// Whether [fix] is at the farm, and how far away it is if not.
///
/// The parcel's outline when there is one, with the claim form's margin;
/// otherwise the nearest assigned point. Callers with neither have nothing to
/// fence against and do not ask.
({bool near, double distance}) atFarm({
  required Fix fix,
  required ParcelShape shape,
  List<Station>? stations,
}) {
  if (!shape.isEmpty) {
    final at = locate(fix: fix, shape: shape);
    return (near: at.atParcel, distance: at.distanceM);
  }
  final points = stations ?? const <Station>[];
  if (points.isEmpty) return (near: true, distance: 0);
  final nearest = points
      .map((s) => metresBetween(fix.lat, fix.lon, s.lat, s.lon))
      .reduce((a, b) => a < b ? a : b);
  return (near: nearest <= nearStationWithinM, distance: nearest);
}

/// Nothing of the survey opens until the farmer is at their farm.
///
/// # Why a gate and not a warning
///
/// The walk used to open wherever the farmer happened to be -- a claim resumed
/// from home, in a village three hundred kilometres away, opened the camera and
/// told them to "walk 329,730 metres south". Nobody can act on that, and every
/// clip recorded from there is refused by the office for being outside the
/// parcel. The survey belongs at the field, so it starts at the field.
///
/// # What decides "at the farm"
///
/// The parcel's outline when the phone has it, with the same margin the claim
/// form uses. Without an outline, the nearest of the points the walk will send
/// the farmer to. With neither -- a photograph-only claim on a parcel the
/// registry never mapped -- there is nothing to measure against, and the office
/// does not fence such a claim either, so nothing is held back here.
///
/// # Why it does not close again
///
/// Once open, the walk is left open. A GPS jump in a field with poor sky would
/// otherwise snatch the camera away mid-clip; the walk screen already refuses
/// to record outside the parcel point by point.
class FieldGate extends StatefulWidget {
  const FieldGate({
    super.key,
    required this.positioning,
    required this.voice,
    required this.shape,
    required this.surveyNumber,
    required this.builder,
    this.stations,
    this.simulate = false,
  });

  final Positioning positioning;
  final Voice voice;
  final ParcelShape shape;
  final List<Station>? stations;
  final String surveyNumber;

  /// Debug builds only: the walk is being exercised from a desk.
  final bool simulate;

  /// The survey itself, given whether it runs on a simulated position.
  final Widget Function(bool simulate) builder;

  @override
  State<FieldGate> createState() => _FieldGateState();
}

enum _Where { locating, away }

class _FieldGateState extends State<FieldGate> {
  StreamSubscription<PositionState>? _positions;
  late bool _simulate = widget.simulate;
  _Where _where = _Where.locating;
  double? _distanceM;
  bool _opened = false;
  bool _toldToGo = false;

  bool get _unfenced =>
      widget.shape.isEmpty && (widget.stations?.isEmpty ?? true);

  @override
  void initState() {
    super.initState();
    if (_simulate || _unfenced) {
      _opened = true;
      return;
    }
    _judge(widget.positioning.best);
    _positions = widget.positioning.stream.listen((s) => _judge(s.fix));
    unawaited(widget.positioning.start());
  }

  @override
  void dispose() {
    unawaited(_positions?.cancel());
    super.dispose();
  }

  void _judge(Fix? fix) {
    if (!mounted || _opened) return;
    if (fix == null) {
      setState(() => _where = _Where.locating);
      return;
    }

    final (:near, :distance) = atFarm(
      fix: fix,
      shape: widget.shape,
      stations: widget.stations,
    );

    if (near) {
      unawaited(_positions?.cancel());
      setState(() => _opened = true);
      return;
    }

    setState(() {
      _where = _Where.away;
      _distanceM = distance;
    });
    // Said once. This is a field screen, and somebody walking towards their
    // farm with the phone in a pocket should hear why nothing has opened.
    if (!_toldToGo) {
      _toldToGo = true;
      unawaited(
        widget.voice.say(
          'पाहणी सुरू करण्यासाठी आधी तुमच्या शेतात जा. शेतात पोहोचल्यावर '
          'पाहणी आपोआप सुरू होईल.',
          en:
              'Go to your farm to start the survey. It starts by itself when '
              'you arrive.',
        ),
      );
    }
  }

  @override
  Widget build(BuildContext context) {
    if (_opened) return widget.builder(_simulate);

    final text = Theme.of(context).textTheme;
    final away = _where == _Where.away;

    return Scaffold(
      appBar: AppBar(title: Text(tr('शेत पाहणी', 'Field survey'))),
      body: SafeArea(
        child: ListView(
          padding: const EdgeInsets.fromLTRB(24, 40, 24, 32),
          children: [
            Center(
              child: Container(
                width: 96,
                height: 96,
                decoration: BoxDecoration(
                  color: away ? Palette.moderateBg : Palette.primarySoft,
                  shape: BoxShape.circle,
                ),
                child: away
                    ? const Icon(
                        Icons.agriculture_outlined,
                        size: 44,
                        color: Palette.moderateFg,
                      )
                    : const Padding(
                        padding: EdgeInsets.all(32),
                        child: CircularProgressIndicator(strokeWidth: 3),
                      ),
              ),
            ),
            const SizedBox(height: Space.xl),
            Text(
              away
                  ? tr('आधी तुमच्या शेतात जा', 'Go to your farm first')
                  : tr('तुमचे ठिकाण शोधत आहे…', 'Finding your location…'),
              textAlign: TextAlign.center,
              style: text.titleLarge,
            ),
            const SizedBox(height: Space.sm),
            Text(
              away
                  ? tr(
                      'पाहणी शेतातच करावी लागते. सर्व्हे नं. '
                          '${widget.surveyNumber} येथे पोहोचल्यावर पाहणी आपोआप '
                          'सुरू होईल.',
                      'The survey has to be done at the field. It starts by '
                          'itself when you reach survey no. '
                          '${widget.surveyNumber}.',
                    )
                  : tr(
                      'मोकळ्या जागी थोडा वेळ थांबा.',
                      'Wait a moment in the open.',
                    ),
              textAlign: TextAlign.center,
              style: text.bodyMedium?.copyWith(color: Palette.inkMuted),
            ),
            if (away && _distanceM != null) ...[
              const SizedBox(height: Space.xl),
              Center(
                child: StatusPill.moderate(
                  tr(
                    'शेत सुमारे ${_pretty(_distanceM!)} दूर आहे',
                    'The farm is about ${_pretty(_distanceM!)} away',
                  ),
                  icon: Icons.place_outlined,
                ),
              ),
            ],
            const SizedBox(height: Space.xl),
            Notice(
              tone: Tone.info,
              icon: Icons.offline_pin_outlined,
              title: tr(
                'तुमची नोंद फोनवर जतन आहे',
                'Your claim is saved on this phone',
              ),
              message: tr(
                'शेतात गेल्यावर मुख्यपृष्ठावरून "पुढे सुरू ठेवा" दाबा.',
                'At the farm, tap "Continue" on the home screen.',
              ),
            ),
            if (canSimulateLocation) ...[
              const SizedBox(height: Space.xl),
              const Divider(),
              const SizedBox(height: Space.md),
              Text(
                'DEBUG BUILD ONLY',
                textAlign: TextAlign.center,
                style: text.labelSmall?.copyWith(letterSpacing: 1.2),
              ),
              const SizedBox(height: Space.sm),
              OutlinedButton.icon(
                onPressed: () => setState(() {
                  _simulate = true;
                  _opened = true;
                }),
                icon: const Icon(Icons.bug_report_outlined, size: 20),
                label: const Text('Pretend I am at the field'),
              ),
            ],
          ],
        ),
      ),
    );
  }

  /// Metres near the farm, kilometres when it is not remotely near.
  String _pretty(double m) {
    if (m < 1000) {
      return tr('${(m / 10).round() * 10} मीटर', '${(m / 10).round() * 10} m');
    }
    return tr('${(m / 1000).round()} किमी', '${(m / 1000).round()} km');
  }
}
