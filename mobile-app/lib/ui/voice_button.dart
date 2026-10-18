import 'dart:async';

import 'package:flutter/material.dart';

import '../core/voice.dart';

/// The control that turns the spoken guidance off, and back on.
///
/// # Why it has to exist
///
/// Without it, a farmer who does not want the phone talking turns the volume
/// down instead -- and then misses the capture instructions, which are the
/// ones that decide whether a photograph is usable at all. Giving the voice
/// its own switch keeps the ringer out of it.
///
/// # Why it is hidden rather than disabled when there is no engine
///
/// On a handset with no speech package there is nothing to turn off. A greyed
/// control invites somebody to press it repeatedly and conclude the app is
/// broken; absence says the same thing without the fight. The instruction
/// text is on screen either way.
///
/// # Why turning it on speaks
///
/// It is the only way to know it worked. Turning it off needs no confirmation
/// -- the silence is the confirmation -- but turning it on and hearing
/// nothing is indistinguishable from a phone whose engine has failed.
class VoiceButton extends StatelessWidget {
  const VoiceButton({super.key, required this.voice, this.color});

  final Voice voice;

  /// Set on the dark screens. The capture view is black, and an icon left to
  /// inherit the default would be a dark shape nobody can see.
  final Color? color;

  @override
  Widget build(BuildContext context) {
    return ListenableBuilder(
      listenable: voice,
      builder: (context, _) {
        // A handset that cannot speak used to make this control vanish, so the
        // farmer got silence and no way to find out why -- which is exactly the
        // report that came back from the field: "the voice guide was not
        // working". Nothing was wrong that the app could not have explained.
        //
        // It stays, struck through, and a tap says what is missing and what to do
        // about it.
        if (voice.started && !voice.available) {
          return IconButton(
            tooltip: 'आवाज उपलब्ध नाही · No voice on this phone',
            icon: Icon(Icons.voice_over_off_outlined, color: color),
            onPressed: () => _explainSilence(context, voice),
          );
        }

        final on = voice.enabled;
        return IconButton(
          tooltip: on
              ? 'आवाज बंद करा · Turn the voice off'
              : 'आवाज सुरू करा · Turn the voice on',
          icon: Icon(on ? Icons.volume_up : Icons.volume_off, color: color),
          onPressed: () {
            voice.setEnabled(!on);
            if (!on) {
              unawaited(voice.repeat(Lines.voiceOn.mr, en: Lines.voiceOn.en));
            }
          },
        );
      },
    );
  }
}

/// "Say that again."
///
/// A farmer who missed an instruction because a tractor went past has no way
/// to ask for it a second time -- [Voice.say] deliberately refuses to repeat
/// its own last line, which is right while a sequence is running and wrong
/// the moment somebody wants to hear it again. This is the way to ask.
///
/// Hidden when the voice is off or unavailable, for the same reason as
/// [VoiceButton]: a control that cannot do anything should not be there.
class SayAgainButton extends StatelessWidget {
  const SayAgainButton({
    super.key,
    required this.voice,
    required this.line,
    this.compact = false,
    this.color,
  });

  final Voice voice;

  /// What to say again. Usually whatever the screen last said.
  final ({String mr, String en}) line;

  /// Icon only. For the capture view, where the screen is the camera and a
  /// labelled button would cover the field the farmer is aiming at -- and
  /// where this is needed most, because a tractor going past costs the
  /// instruction and there is otherwise no way to ask for it again.
  final bool compact;

  final Color? color;

  @override
  Widget build(BuildContext context) {
    return ListenableBuilder(
      listenable: voice,
      builder: (context, _) {
        if (!voice.available || !voice.enabled) {
          return const SizedBox.shrink();
        }

        void again() => unawaited(voice.repeat(line.mr, en: line.en));

        if (compact) {
          return IconButton(
            onPressed: again,
            tooltip: 'पुन्हा सांगा · Say that again',
            icon: Icon(Icons.replay, color: color),
          );
        }

        return TextButton.icon(
          onPressed: again,
          icon: Icon(Icons.replay, color: color),
          label: const Text('पुन्हा सांगा · Say that again'),
          style: TextButton.styleFrom(
            foregroundColor: color,
            // The same 56dp floor as every other control: this one is pressed
            // by somebody who is already having difficulty.
            minimumSize: const Size(0, 56),
          ),
        );
      },
    );
  }
}

/// Why this phone is not speaking, and what would fix it.
///
/// The route through Settings is described in words rather than opened with an
/// intent. The text-to-speech settings intent exists, and where it lands differs
/// by manufacturer -- Vivo, Samsung and Xiaomi each bury it somewhere else -- so a
/// button that opens the wrong page on half the handsets in Maharashtra is worse
/// than a sentence naming the destination.
Future<void> _explainSilence(BuildContext context, Voice voice) async {
  final why = voice.unavailableBecause;
  final small = Theme.of(context).textTheme.bodySmall;

  await showDialog<void>(
    context: context,
    builder: (ctx) => AlertDialog(
      title: const Text('आवाज उपलब्ध नाही · No voice on this phone'),
      content: SingleChildScrollView(
        child: Column(
          mainAxisSize: MainAxisSize.min,
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            // The reassurance first. A farmer who thinks the app is broken may
            // stop, and nothing here stops a claim being filed.
            const Text(
              'या फोनवर बोलणारा आवाज बसवलेला नाही, म्हणून सूचना ऐकू येत नाहीत. '
              'सर्व सूचना पडद्यावर लिहिलेल्या असतात, त्यामुळे दावा भरण्यात '
              'कोणतीही अडचण येणार नाही.',
            ),
            const SizedBox(height: 10),
            const Text(
              'आवाज सुरू करण्यासाठी फोनच्या Settings मध्ये Language & input '
              '(किंवा Additional settings) → Text-to-speech येथे जाऊन Google चा '
              'आवाज आणि मराठी भाषा डाउनलोड करा.',
            ),
            const SizedBox(height: 10),
            const Text(
              'The instructions are always written on the screen, so a claim can '
              'be filed without the voice. To enable it, install a '
              'text-to-speech voice and the Marathi language from the settings '
              'of the phone.',
            ),
            if (why != null) ...[
              const SizedBox(height: 12),
              // The technical reason, for whoever is helping over the phone.
              // Small and last: it is not addressed to the farmer.
              Text(why, style: small),
            ],
          ],
        ),
      ),
      actions: [
        TextButton(
          onPressed: () => Navigator.of(ctx).pop(),
          child: const Text('ठीक · OK'),
        ),
      ],
    ),
  );
}
