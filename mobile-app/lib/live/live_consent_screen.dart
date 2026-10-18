import 'dart:async';

import 'package:flutter/material.dart';

import '../core/access.dart';
import '../core/language.dart';
import '../core/live_consent.dart';
import '../core/voice.dart';
import '../ui/theme.dart';

/// Asking a farmer whether the assistant may hear them.
///
/// # Why this is a screen and not a dialog
///
/// Because a dialog is something people dismiss. What is being agreed to is that a
/// farmer's voice and pictures of their field leave the platform, continuously, while
/// they talk -- and a decision of that kind should not be reachable by tapping the
/// nearest button to make something go away.
///
/// # Why the refusal is the same size as the agreement
///
/// A screen with a large primary button and a small grey link has already made the
/// choice. Refusing costs the farmer nothing: the guidance, the walk, the spacing rule
/// and the filing all work with no network, so both answers are real and the screen is
/// laid out as though they are.
///
/// # Why it speaks
///
/// The same reason the walk does. This is filled standing in a field by somebody who
/// may read Marathi slowly, and a consent notice nobody can read is not consent.
class LiveConsentScreen extends StatefulWidget {
  const LiveConsentScreen({
    super.key,
    required this.voice,
    required this.consent,
    required this.farmerId,
    required this.onDecided,
    this.access,
  });

  final Voice voice;
  final LiveConsent consent;
  final String farmerId;

  /// Called with what they chose. True only where they agreed *and* the handset gave
  /// the microphone: an agreement with no microphone is an assistant that cannot hear,
  /// and offering it would be a conversation the farmer talks into alone.
  final void Function(bool granted) onDecided;

  /// Replaced in tests.
  final AccessCheck? access;

  @override
  State<LiveConsentScreen> createState() => _LiveConsentScreenState();
}

class _LiveConsentScreenState extends State<LiveConsentScreen> {
  bool _busy = false;
  String? _micRefused;

  @override
  void initState() {
    super.initState();
    // Read aloud, once. The terms are long for a spoken line, so the voice gives the
    // shape of it and the screen carries the detail.
    widget.voice.say(
      'सहाय्यकाशी बोलण्यासाठी तुमचा आवाज आणि शेताचे फोटो पाठवावे लागतात. '
      'हे न वापरताही तुमची नोंद पूर्ण होते. स्क्रीनवर वाचून ठरवा.',
      en:
          'To talk to the assistant, your voice and photographs of your field '
          'must be sent. Your claim is completed without this. Read the screen '
          'and decide.',
    );
  }

  Future<void> _agree() async {
    if (_busy) return;
    setState(() {
      _busy = true;
      _micRefused = null;
    });

    // The microphone is asked for here and nowhere else. A farmer who declines the
    // assistant is never asked for one, which is the whole reason it is not in the
    // access flow with the camera.
    final access = widget.access ?? AccessCheck();
    final mic = await access.requestMicrophone();
    if (!mounted) return;

    if (mic != Access.granted) {
      // Recorded as a refusal, because from the app's point of view that is what it
      // is: the assistant cannot hear. Said plainly rather than left as a screen that
      // did nothing when tapped.
      await widget.consent.record(farmerId: widget.farmerId, granted: false);
      if (!mounted) return;
      setState(() {
        _busy = false;
        _micRefused = tr(
          'मायक्रोफोनची परवानगी मिळाली नाही, म्हणून बोलून संवाद करता येणार '
              'नाही. सूचना ऐकू येतील आणि नोंद पूर्ण होईल.',
          'Microphone permission was not given, so talking is not available. '
              'You will still hear the guidance and the claim will still be sent.',
        );
      });
      widget.onDecided(false);
      return;
    }

    await widget.consent.record(farmerId: widget.farmerId, granted: true);
    if (!mounted) return;
    widget.onDecided(true);
  }

  Future<void> _decline() async {
    if (_busy) return;
    setState(() => _busy = true);
    await widget.consent.record(farmerId: widget.farmerId, granted: false);
    if (!mounted) return;
    widget.onDecided(false);
  }

  @override
  Widget build(BuildContext context) {
    final text = Theme.of(context).textTheme;

    return Scaffold(
      appBar: AppBar(
        title: Text(tr('सहाय्यकाशी बोलणे', 'Talking to the assistant')),
      ),
      body: SafeArea(
        child: ListView(
          padding: Space.page,
          children: [
            Text(
              tr(
                'तुम्ही शेतात असताना सहाय्यकाशी बोलू शकता. काय चित्रित करायचे '
                    'ते तो सांगेल.',
                'You can talk to the assistant while you are in the field. It '
                    'will tell you what to film.',
              ),
              style: text.titleMedium,
            ),
            const SizedBox(height: Space.lg),
            AppCard(
              child: Text(
                tr(liveConsentTerms.mr, liveConsentTerms.en),
                style: text.bodyMedium,
              ),
            ),
            if (_micRefused != null) ...[
              const SizedBox(height: Space.lg),
              Notice(
                tone: Tone.warning,
                icon: Icons.mic_off_outlined,
                title: tr('बोलणे उपलब्ध नाही', 'Talking is not available'),
                message: _micRefused!,
              ),
            ],
            const SizedBox(height: Space.xl),

            // Both answers, the same size. A large primary button beside a small grey
            // link has already chosen for them.
            FilledButton.icon(
              onPressed: _busy ? null : () => unawaited(_agree()),
              icon: const Icon(Icons.mic, size: 20),
              label: Text(tr('होय, बोलणे सुरू करा', 'Yes, let me talk')),
              style: FilledButton.styleFrom(
                minimumSize: const Size.fromHeight(52),
              ),
            ),
            const SizedBox(height: Space.md),
            OutlinedButton.icon(
              onPressed: _busy ? null : () => unawaited(_decline()),
              icon: const Icon(Icons.block, size: 20),
              label: Text(
                tr('नको, नेहमीप्रमाणे पुढे जा', 'No, carry on as usual'),
              ),
              style: OutlinedButton.styleFrom(
                minimumSize: const Size.fromHeight(52),
              ),
            ),
            const SizedBox(height: Space.lg),
            Text(
              tr(
                'नंतर कधीही "मदत" मध्ये जाऊन ही परवानगी बदलू शकता.',
                'You can change this at any time under Help.',
              ),
              style: text.bodySmall,
            ),
          ],
        ),
      ),
    );
  }
}
