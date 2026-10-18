import 'dart:async';

import 'package:flutter/material.dart';

import '../core/access.dart';
import '../core/language.dart';
import '../core/voice.dart';

/// Explains what the app needs, then asks for it.
///
/// # Why a screen and not just the prompts
///
/// The system prompts say "Allow eSurvey to take pictures and record video?" and
/// "Allow eSurvey to access this device's location?". Neither says why, and a
/// farmer who has been handed a phone by somebody from an office has every reason
/// to refuse a request they cannot judge. The usual advice — never ask at launch —
/// exists because of exactly that.
///
/// This screen is the judging. It says, in Marathi first, that a recording is
/// what a claim rests on and that the location is what shows the recording came
/// from their own field. The prompts follow, after they have chosen to go on.
///
/// # Why it blocks
///
/// Because the alternative is discovering it in a field. Without the camera there
/// is no claim at all; without a precise fix a recording cannot be placed inside
/// a boundary and is not evidence. A farmer standing at their first point is the
/// worst possible place to find that out, since the remedy is Settings and often
/// a signal.
///
/// Notifications do not block. Without them the upload still runs and the claim
/// still completes.
class AccessScreen extends StatefulWidget {
  const AccessScreen({
    super.key,
    required this.voice,
    required this.onReady,
    this.check,
    this.onSkip,
  });

  final Voice voice;

  /// Called once everything a walk needs is allowed.
  final VoidCallback onReady;

  /// Injected for tests.
  final AccessCheck? check;

  /// Offered where the farmer can carry on without recording -- reading their
  /// claims, say. Null where this screen is the only way forward.
  final VoidCallback? onSkip;

  @override
  State<AccessScreen> createState() => _AccessScreenState();
}

class _AccessScreenState extends State<AccessScreen> {
  late final AccessCheck _access = widget.check ?? AccessCheck();

  AccessReport? _report;
  bool _asking = false;
  bool _spoken = false;

  @override
  void initState() {
    super.initState();
    unawaited(_speak());
  }

  Future<void> _speak() async {
    if (_spoken) return;
    _spoken = true;
    widget.voice.say(
      'नोंद घेण्यासाठी ॲपला कॅमेरा आणि ठिकाणाची परवानगी लागते. '
      'पुढे जा दाबल्यावर फोन विचारेल.',
      en:
          'To record your field the app needs the camera and your location. '
          'Your phone will ask when you tap continue.',
    );
  }

  Future<void> _ask() async {
    setState(() => _asking = true);
    final report = await _access.request();
    if (!mounted) return;
    setState(() {
      _report = report;
      _asking = false;
    });

    if (report.canWalk) {
      widget.onReady();
    }
  }

  @override
  Widget build(BuildContext context) {
    final report = _report;

    return Scaffold(
      appBar: AppBar(title: Text(tr('आवश्यक परवानग्या', 'What the app needs'))),
      body: SafeArea(
        child: ListView(
          padding: const EdgeInsets.all(20),
          children: [
            Text(
              tr(
                'तुमच्या शेताची नोंद घेण्यासाठी ॲपला दोन गोष्टींची परवानगी '
                    'लागते. ती कशासाठी, हे खाली दिले आहे.',
                'To record your field the app needs to be allowed two things. '
                    'What each one is for is below.',
              ),
              style: Theme.of(context).textTheme.bodyLarge,
            ),
            const SizedBox(height: 20),

            _AccessTile(
              icon: Icons.videocam_outlined,
              title: tr('कॅमेरा', 'Camera'),
              // Said as what it is for, not as what is being taken.
              why: tr(
                'शेतातील चित्रीकरण हाच दाव्याचा मुख्य पुरावा आहे. कॅमेऱ्याशिवाय '
                    'नोंद घेता येत नाही.',
                'The recording of your field is the main evidence for the '
                    'claim. Without the camera there is nothing to record.',
              ),
              state: report?.camera,
              required: true,
            ),
            _AccessTile(
              icon: Icons.my_location_outlined,
              title: tr('ठिकाण (अचूक)', 'Location (precise)'),
              why: tr(
                'चित्रीकरण तुमच्याच शेतातून झाले हे ठिकाणावरून दिसते. '
                    'ढोबळ ठिकाण काही शेकडो मीटर रुंद असते, त्यामुळे ते पुरेसे '
                    'नाही — "अचूक" निवडावे.',
                'The location is what shows the recording came from your own '
                    'field. An approximate location is several hundred metres '
                    'wide, which is not enough: choose "precise".',
              ),
              state: report?.location,
              required: true,
            ),
            _AccessTile(
              icon: Icons.notifications_outlined,
              title: tr('सूचना', 'Notifications'),
              why: tr(
                'नोंदी पाठवताना किती झाले हे दिसते, आणि अपूर्ण दावा राहिला '
                    'तर आठवण मिळते. ही परवानगी नाकारली तरी दावा पूर्ण होतो.',
                'Shows how much of the sending is done, and reminds you about '
                    'an unfinished claim. The claim still works without it.',
              ),
              state: report?.notifications,
              required: false,
            ),

            const SizedBox(height: 8),

            // The microphone is conspicuously absent, and the farmer is told so.
            // A government application that says plainly what it does not take
            // is easier to trust than one that simply takes less.
            Card(
              color: Theme.of(context).colorScheme.surfaceContainerHighest,
              child: Padding(
                padding: const EdgeInsets.all(14),
                child: Row(
                  children: [
                    const Icon(Icons.mic_off_outlined),
                    const SizedBox(width: 12),
                    Expanded(
                      child: Text(
                        tr(
                          'ॲप आवाज रेकॉर्ड करत नाही. चित्रीकरण आवाजाशिवाय '
                              'होते, म्हणून मायक्रोफोनची परवानगी मागितली जात नाही.',
                          'The app does not record sound. Video is recorded '
                              'without audio, so microphone access is never asked '
                              'for.',
                        ),
                      ),
                    ),
                  ],
                ),
              ),
            ),

            const SizedBox(height: 20),

            if (report != null && !report.canWalk)
              _Remedy(report: report, access: _access, onRecheck: _ask),

            const SizedBox(height: 12),

            FilledButton.icon(
              onPressed: _asking ? null : _ask,
              icon: _asking
                  ? const SizedBox(
                      width: 18,
                      height: 18,
                      child: CircularProgressIndicator(strokeWidth: 2),
                    )
                  : const Icon(Icons.check),
              label: Text(
                report == null
                    ? tr('पुढे जा', 'Continue')
                    : tr('पुन्हा तपासा', 'Check again'),
              ),
            ),

            if (widget.onSkip != null) ...[
              const SizedBox(height: 8),
              TextButton(
                onPressed: widget.onSkip,
                child: Text(
                  // Deliberately vague about what comes next. This screen is
                  // shown before sign-in, where the farmer has no claims yet, so
                  // "show me my claims first" named something that was not there.
                  tr('नंतर विचारा', 'Ask me later'),
                ),
              ),
            ],
          ],
        ),
      ),
    );
  }
}

/// One permission, what it is for, and where it stands.
class _AccessTile extends StatelessWidget {
  const _AccessTile({
    required this.icon,
    required this.title,
    required this.why,
    required this.state,
    required this.required,
  });

  final IconData icon;
  final String title;
  final String why;
  final Access? state;
  final bool required;

  @override
  Widget build(BuildContext context) {
    final scheme = Theme.of(context).colorScheme;

    // Nothing is shown as wrong before the farmer has been asked. A red cross
    // against the camera on a screen that has not yet requested it reads as a
    // fault with the phone.
    final (IconData? mark, Color? colour) = switch (state) {
      null => (null, null),
      Access.granted => (Icons.check_circle, scheme.primary),
      Access.reduced => (Icons.error_outline, scheme.error),
      Access.off => (Icons.cancel_outlined, scheme.error),
      Access.blocked => (Icons.cancel_outlined, scheme.error),
      Access.denied => (
        required ? Icons.cancel_outlined : Icons.remove_circle_outline,
        required ? scheme.error : scheme.outline,
      ),
    };

    return Padding(
      padding: const EdgeInsets.only(bottom: 16),
      child: Row(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Icon(icon, size: 28),
          const SizedBox(width: 14),
          Expanded(
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                Row(
                  children: [
                    Text(title, style: Theme.of(context).textTheme.titleMedium),
                    if (!required) ...[
                      const SizedBox(width: 8),
                      Text(
                        tr('(ऐच्छिक)', '(optional)'),
                        style: Theme.of(context).textTheme.bodySmall,
                      ),
                    ],
                    if (mark != null) ...[
                      const SizedBox(width: 8),
                      Icon(mark, size: 18, color: colour),
                    ],
                  ],
                ),
                const SizedBox(height: 4),
                Text(why, style: Theme.of(context).textTheme.bodyMedium),
              ],
            ),
          ),
        ],
      ),
    );
  }
}

/// What to do about a refusal, which depends on which kind it was.
///
/// A permission that can be asked again needs no instruction: the button says
/// "check again" and the prompt reappears. One that cannot, and a switched-off
/// radio, both need Settings -- and they are *different* Settings pages, which is
/// the sort of thing that turns a two-minute fix into a support call.
class _Remedy extends StatelessWidget {
  const _Remedy({
    required this.report,
    required this.access,
    required this.onRecheck,
  });

  final AccessReport report;
  final AccessCheck access;
  final VoidCallback onRecheck;

  @override
  Widget build(BuildContext context) {
    final scheme = Theme.of(context).colorScheme;
    final rows = <Widget>[];

    if (report.locationService == Access.off) {
      rows.add(
        _action(
          context,
          tr(
            'या फोनवर ठिकाण (Location) बंद आहे. परवानगी दिली असली तरी बंद '
                'रेडिओतून ठिकाण मिळत नाही.',
            'Location is switched off on this phone. A granted permission over a '
                'switched-off radio still gives nothing.',
          ),
          tr('ठिकाण सुरू करा', 'Turn on location'),
          access.openLocationSettings,
        ),
      );
    }

    if (report.location == Access.reduced) {
      rows.add(
        _action(
          context,
          tr(
            'ठिकाणाची परवानगी "ढोबळ" आहे. ती काही शेकडो मीटर रुंद असते, '
                'त्यामुळे चित्रीकरण तुमच्या शेतात झाले हे सिद्ध करता येत नाही. '
                'सेटिंग्जमध्ये "अचूक ठिकाण" सुरू करा.',
            'Location is granted as approximate, which is several hundred metres '
                'wide — too coarse to show a recording was made on your field. Turn '
                'on precise location in Settings.',
          ),
          tr('सेटिंग्ज उघडा', 'Open settings'),
          access.openSettings,
        ),
      );
    }

    if (report.camera == Access.blocked || report.location == Access.blocked) {
      rows.add(
        _action(
          context,
          tr(
            'परवानगी नाकारली गेली आहे आणि ॲप ती पुन्हा विचारू शकत नाही. '
                'सेटिंग्जमधून द्यावी लागेल.',
            'A permission was refused in a way the app cannot ask about again. '
                'It has to be granted from Settings.',
          ),
          tr('सेटिंग्ज उघडा', 'Open settings'),
          access.openSettings,
        ),
      );
    }

    if (report.camera == Access.off) {
      rows.add(
        _action(
          context,
          tr(
            'या फोनचा कॅमेरा सुरू होत नाही. ही परवानगीची अडचण नाही — दुसरा '
                'फोन वापरावा लागेल.',
            'This phone\'s camera will not start. That is not a permission '
                'problem, and another handset will be needed.',
          ),
          null,
          null,
        ),
      );
    }

    if (rows.isEmpty) {
      rows.add(
        _action(
          context,
          tr(
            'नोंद घेण्यासाठी कॅमेरा आणि अचूक ठिकाण या दोन्हींची परवानगी लागते.',
            'Recording needs both the camera and a precise location.',
          ),
          null,
          null,
        ),
      );
    }

    return Card(
      color: scheme.errorContainer,
      child: Padding(
        padding: const EdgeInsets.all(14),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: rows,
        ),
      ),
    );
  }

  Widget _action(
    BuildContext context,
    String message,
    String? label,
    Future<void> Function()? onTap,
  ) {
    return Padding(
      padding: const EdgeInsets.only(bottom: 8),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Text(
            message,
            style: TextStyle(
              color: Theme.of(context).colorScheme.onErrorContainer,
            ),
          ),
          if (label != null && onTap != null)
            Align(
              alignment: Alignment.centerRight,
              child: TextButton(
                onPressed: () => unawaited(onTap()),
                child: Text(label),
              ),
            ),
        ],
      ),
    );
  }
}
