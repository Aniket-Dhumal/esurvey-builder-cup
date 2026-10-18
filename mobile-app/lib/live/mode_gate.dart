import 'dart:async';

import 'package:flutter/material.dart';

import '../core/api.dart';
import '../core/db.dart';
import '../core/language.dart';
import '../core/link_quality.dart';
import '../core/live_consent.dart';
import '../core/voice.dart';
import '../ui/theme.dart';
import 'live_consent_screen.dart';
import 'survey_mode.dart';

/// The one place the survey's engine is chosen.
///
/// # Why this is a gate and not a check inside the walk
///
/// Because the choice has to be made before the first clip and then left alone. The
/// walk used to ask for itself, which meant the link was measured when the camera
/// screen opened and the assistant could appear at one point and be gone at the
/// next -- a farmer being talked to and then not being talked to, with no way to
/// read that except as a fault.
///
/// A gate also puts the consent question where it belongs. Asking "may the
/// assistant hear you" in the middle of a walk interrupts the one task the farmer
/// came to do; asking before the walk starts is a question with nothing waiting on
/// it.
///
/// # Why it sits after the field gate
///
/// The measurement is about where the farmer is standing. A form filled at home on
/// wifi says nothing about the field, and the field is where every clip is
/// recorded -- so this runs once the farmer has been established at their farm, and
/// what it measures is the link they will actually be using.
///
/// # Why it never blocks
///
/// It cannot refuse anybody anything. Every outcome -- no build flag, no consent, a
/// farm with no signal, a probe that failed -- continues into the same walk, and
/// the only difference is whether a talk button is on it. The gate exists to decide
/// quickly and get out of the way, so it shows a line rather than a spinner with no
/// explanation, and it gives up rather than waiting.
class ModeGate extends StatefulWidget {
  const ModeGate({
    super.key,
    required this.store,
    required this.voice,
    required this.api,
    required this.farmerId,
    required this.builder,
    this.probe,
    this.decide,
  });

  final Store store;
  final Voice voice;

  /// Null on a build or a route with no way to reach the platform, which is an
  /// offline survey and no measurement.
  final Api? api;
  final String farmerId;

  /// The survey, given what was decided.
  final Widget Function(SurveyMode mode) builder;

  /// Replaced in tests.
  final LinkProbe? probe;
  final Future<SurveyMode> Function()? decide;

  @override
  State<ModeGate> createState() => _ModeGateState();
}

class _ModeGateState extends State<ModeGate> {
  SurveyMode? _mode;
  late final LiveConsent _consent = LiveConsent(widget.store);

  /// How long the farmer waits for this before it stops mattering.
  ///
  /// Six seconds. The probe has its own timeouts and a cache, so this is the
  /// backstop for a connection that accepts a request and then says nothing --
  /// which is the characteristic failure of a rural link and would otherwise hold a
  /// farmer on a blank screen in front of their field. The survey is what they came
  /// for; the assistant can be missed.
  static const _decideWithin = Duration(seconds: 6);

  @override
  void initState() {
    super.initState();
    unawaited(_decide());
  }

  Future<void> _decide() async {
    final api = widget.api;
    if (api == null) {
      if (mounted) setState(() => _mode = const SurveyMode.offline());
      return;
    }

    SurveyMode mode;
    try {
      mode =
          await (widget.decide ??
                  () => decideSurveyMode(
                    api: api,
                    consent: _consent,
                    farmerId: widget.farmerId,
                    probe: widget.probe,
                  ))()
              .timeout(_decideWithin);
    } catch (_) {
      // A probe that threw, or one that never answered. Offline, with nothing said:
      // the farmer was not promised a conversation, so there is nothing to explain.
      mode = const SurveyMode.offline();
    }
    if (!mounted) return;

    // The choice is put here, before the walk, and it is put every time.
    //
    // A farmer who has never been asked gets the consent screen, which is itself the
    // choice. One who agreed on an earlier visit gets a short question instead,
    // because agreeing once to be recorded is not the same as wanting a conversation
    // during this particular walk -- and because this is the moment to decide it.
    // The alternative was a button on the camera screen, which asked somebody to
    // choose between two ways of working while they were already doing one of them.
    if (mode.live) {
      final asked = await _consent.asked(widget.farmerId);
      if (!mounted) return;
      final wants = asked ? await _offerTheChoice() : await _ask();
      if (!mounted) return;
      if (!wants) mode = const SurveyMode.offline();
    }

    if (!mounted) return;
    setState(() => _mode = mode);
  }

  /// Ask a farmer who has already agreed whether they want it this time.
  ///
  /// Both answers the same size, for the reason the consent screen gives at more
  /// length: a large primary button beside a small grey link has already chosen.
  /// Refusing costs nothing -- every instruction needed to file a claim is scripted
  /// and needs no network -- so both answers are real.
  Future<bool> _offerTheChoice() async {
    var wants = false;
    await Navigator.of(context).push<void>(
      MaterialPageRoute(
        builder: (sheet) => Scaffold(
          appBar: AppBar(
            title: Text(tr('पाहणी कशी करायची?', 'How should this survey run?')),
          ),
          body: SafeArea(
            child: ListView(
              padding: Space.page,
              children: [
                Text(
                  tr(
                    'या शेतात नेटवर्क चांगले आहे. सहाय्यकाशी बोलत पाहणी करू '
                        'शकता — काय चित्रित करायचे ते तो सांगेल.',
                    'The connection here is good. You can do this survey while '
                        'talking to the assistant, which will tell you what to '
                        'film.',
                  ),
                  style: Theme.of(context).textTheme.titleMedium,
                ),
                const SizedBox(height: Space.md),
                Text(
                  tr(
                    'दोन्ही प्रकारे नोंद पूर्ण होते. बोलल्याने फक्त जास्त '
                        'मार्गदर्शन मिळते.',
                    'The claim is completed either way. Talking only adds '
                        'guidance.',
                  ),
                  style: Theme.of(context).textTheme.bodyMedium,
                ),
                const SizedBox(height: Space.xl),
                FilledButton.icon(
                  onPressed: () {
                    wants = true;
                    Navigator.of(sheet).maybePop();
                  },
                  icon: const Icon(Icons.record_voice_over_outlined, size: 20),
                  label: Text(
                    tr(
                      'सहाय्यकाशी बोलत पाहणी करा',
                      'Survey with the assistant',
                    ),
                  ),
                  style: FilledButton.styleFrom(
                    minimumSize: const Size.fromHeight(52),
                  ),
                ),
                const SizedBox(height: Space.md),
                OutlinedButton.icon(
                  onPressed: () => Navigator.of(sheet).maybePop(),
                  icon: const Icon(Icons.videocam_outlined, size: 20),
                  label: Text(tr('नेहमीप्रमाणे पाहणी करा', 'Survey as usual')),
                  style: OutlinedButton.styleFrom(
                    minimumSize: const Size.fromHeight(52),
                  ),
                ),
              ],
            ),
          ),
        ),
      ),
    );
    // Backing out is not choosing to talk. The walk opens either way.
    return wants;
  }

  Future<bool> _ask() async {
    var agreed = false;
    await Navigator.of(context).push<void>(
      MaterialPageRoute(
        builder: (sheet) => LiveConsentScreen(
          voice: widget.voice,
          consent: _consent,
          farmerId: widget.farmerId,
          onDecided: (granted) {
            agreed = granted;
            Navigator.of(sheet).maybePop();
          },
        ),
      ),
    );
    // A farmer who used the back button decided nothing, which is treated as no.
    return agreed;
  }

  @override
  Widget build(BuildContext context) {
    final mode = _mode;
    if (mode != null) return widget.builder(mode);

    // Deciding. A line rather than a bare spinner, because a farmer standing in a
    // field in front of a blank screen assumes the app has stopped -- the same
    // reason the walk says "finding your location" out loud.
    return Scaffold(
      body: Center(
        child: Padding(
          padding: Space.page,
          child: Column(
            mainAxisSize: MainAxisSize.min,
            children: [
              const SizedBox(
                width: 28,
                height: 28,
                child: CircularProgressIndicator(strokeWidth: 3),
              ),
              const SizedBox(height: Space.lg),
              Text(
                tr('जोडणी तपासत आहे…', 'Checking the connection…'),
                textAlign: TextAlign.center,
                style: Theme.of(context).textTheme.titleMedium,
              ),
              const SizedBox(height: Space.sm),
              Text(
                tr(
                  'पाहणी कोणत्याही परिस्थितीत सुरू होईल.',
                  'The survey starts either way.',
                ),
                textAlign: TextAlign.center,
                style: Theme.of(context).textTheme.bodySmall,
              ),
            ],
          ),
        ),
      ),
    );
  }
}
