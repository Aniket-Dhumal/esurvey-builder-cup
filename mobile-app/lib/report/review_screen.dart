import 'dart:async';

import 'package:flutter/material.dart';

import '../core/background.dart';
import '../core/db.dart';
import '../core/language.dart';
import '../core/sensors.dart';
import '../core/sync.dart';
import '../core/voice.dart';
import '../ui/theme.dart';
import 'journey.dart';

/// What will be sent, before it is sent.
///
/// # Why a claim waits here
///
/// The report used to enter the send queue the moment the form was filed, so
/// a farmer interrupted between the form and the walk had already filed a
/// claim with no evidence behind it -- and the office could not tell a claim
/// whose photographs were still coming from one whose photographs never would
/// (docs/EVIDENCE_REVALIDATION.md, V4).
///
/// It was decided on 2026-09-14 that everything is required before a claim
/// goes. So this screen is where the claim is completed: it lists what is on
/// the phone, names what is missing, and sends only when the walk is whole.
///
/// # Why nothing is lost by waiting
///
/// The claim is on the phone from the moment the form is filed, visible on
/// the home screen as work in progress. Waiting here changes when the office
/// hears about it, not whether the farmer's work survives -- a distinction
/// the queue has always kept.
class ReviewScreen extends StatefulWidget {
  const ReviewScreen({
    super.key,
    required this.store,
    required this.voice,
    required this.positioning,
    required this.sync,
    required this.journey,
    this.simulate = false,
  });

  final Store store;
  final Voice voice;
  final Positioning positioning;
  final Sync sync;
  final Journey journey;

  /// Debug builds only: the walk was done from a desk.
  final bool simulate;

  @override
  State<ReviewScreen> createState() => _ReviewScreenState();
}

class _ReviewScreenState extends State<ReviewScreen> {
  EvidenceProgress? _progress;
  Map<String, Object?>? _draft;
  bool _sending = false;

  @override
  void initState() {
    super.initState();
    unawaited(_load());
  }

  Future<void> _load() async {
    final progress = await evidenceProgress(widget.store, widget.journey);
    final draft = await widget.store.draft(widget.journey.id);
    if (!mounted) return;
    setState(() {
      _progress = progress;
      _draft = draft;
    });
  }

  /// Puts the claim in the queue. From here the sync engine owns it.
  Future<void> _send() async {
    if (_sending) return;
    setState(() => _sending = true);
    await widget.store.releaseDraft(widget.journey.id);
    await widget.store.setJourneyStage(widget.journey.id, 'done');
    // Nothing is unfinished any more. The reminder is cancelled before the
    // send rather than after it: from here the queue owns the claim, and a
    // farmer whose claim is waiting on a network should not be told their
    // evidence is incomplete.
    unawaited(BackgroundSync.stopReminding());
    unawaited(widget.sync.sendNow());

    if (!mounted) return;
    ScaffoldMessenger.of(context).showSnackBar(
      SnackBar(
        content: Text(
          tr(
            'नोंद पाठवायला दिली. नेटवर्क आल्यावर आपोआप जाईल.',
            'Claim queued. It sends by itself when there is a signal.',
          ),
        ),
      ),
    );
    Navigator.of(context).pop(true);
  }

  Future<void> _capture() async {
    await Navigator.of(context).push(
      evidenceRoute(
        store: widget.store,
        voice: widget.voice,
        positioning: widget.positioning,
        journey: widget.journey,
        simulate: widget.simulate,
      ),
    );
    if (mounted) await _load();
  }

  @override
  Widget build(BuildContext context) {
    final progress = _progress;
    if (progress == null) {
      return const Scaffold(body: Center(child: CircularProgressIndicator()));
    }

    final text = Theme.of(context).textTheme;
    final draft = _draft;

    return Scaffold(
      appBar: AppBar(title: Text(tr('तपासा आणि पाठवा', 'Check and send'))),
      body: ListView(
        padding: Space.page,
        children: [
          Padding(
            padding: const EdgeInsets.fromLTRB(4, 4, 4, Space.md),
            child: Text(
              tr(
                'हे कार्यालयाकडे जाईल.',
                'This is what will go to the office.',
              ),
              style: text.bodyMedium?.copyWith(color: Palette.inkMuted),
            ),
          ),

          if (draft != null)
            Card(
              child: Padding(
                padding: const EdgeInsets.symmetric(vertical: Space.sm),
                child: Column(
                  children: [
                    _Line(
                      icon: Icons.landscape_outlined,
                      label: tr('शेत', 'Field'),
                      value: tr(
                        'सर्व्हे नं. ${draft['survey_number']}',
                        'Survey no. ${draft['survey_number']}',
                      ),
                    ),
                    _Line(
                      icon: Icons.grass_outlined,
                      label: tr('पीक', 'Crop'),
                      value: '${draft['crop_name']}',
                    ),
                    _Line(
                      icon: Icons.crop_square,
                      label: tr('नुकसानग्रस्त क्षेत्र', 'Area damaged'),
                      value: tr(
                        '${draft['area_sown_ha']} पैकी ${draft['area_affected_ha']} हेक्टर',
                        '${draft['area_affected_ha']} of ${draft['area_sown_ha']} ha',
                      ),
                    ),
                    _Line(
                      icon: Icons.percent,
                      label: tr('तुमचा अंदाज', 'Your estimate'),
                      value: '${(draft['damage_percent'] as num).round()}%',
                    ),
                  ],
                ),
              ),
            ),

          SectionHeader(tr('पुरावा', 'Evidence')),
          _EvidenceCard(progress: progress),
          const SizedBox(height: Space.lg),

          if (progress.complete)
            BigButton(
              label: _sending
                  ? tr('पाठवत आहे…', 'Sending…')
                  : tr('नोंद पाठवा', 'Send the claim'),
              icon: Icons.send_rounded,
              onPressed: _sending ? null : _send,
            )
          else
            BigButton(
              label: tr('बाकीची ठिकाणे पूर्ण करा', 'Finish the points'),
              icon: Icons.photo_camera_outlined,
              onPressed: _capture,
            ),

          const SizedBox(height: Space.md),
          Text(
            progress.complete
                ? tr(
                    'पाठवल्यावर नेटवर्क आल्यावर आपोआप जाईल.',
                    'Once sent it goes by itself when there is a signal.',
                  )
                : tr(
                    'पूर्ण पुरावा असल्याशिवाय नोंद पाठवली जात नाही. तोपर्यंत ती '
                        'फोनवर सुरक्षित आहे.',
                    'A claim is not sent until its evidence is complete. Until '
                        'then it is safe on this phone.',
                  ),
            textAlign: TextAlign.center,
            style: text.labelMedium,
          ),
        ],
      ),
    );
  }
}

/// What has been gathered, and what has not.
class _EvidenceCard extends StatelessWidget {
  const _EvidenceCard({required this.progress});

  final EvidenceProgress progress;

  @override
  Widget build(BuildContext context) {
    final text = Theme.of(context).textTheme;
    final complete = progress.complete;
    final tone = complete ? Tone.success : Tone.warning;
    return AppCard(
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Row(
            children: [
              IconBadge(
                complete ? Icons.check_circle_outline : Icons.pending_outlined,
                color: tone.foreground,
                background: tone.background,
              ),
              const SizedBox(width: Space.md),
              Expanded(
                child: Text(
                  tr(
                    'शेतातील ठिकाणे ${progress.done}/${progress.total}',
                    'Field points ${progress.done}/${progress.total}',
                  ),
                  style: text.titleMedium,
                ),
              ),
            ],
          ),
          const SizedBox(height: Space.md),
          ClipRRect(
            borderRadius: BorderRadius.circular(99),
            child: LinearProgressIndicator(
              value: progress.total == 0 ? 0 : progress.done / progress.total,
              minHeight: 6,
              color: complete ? Palette.primary : Palette.moderateFg,
            ),
          ),
          const SizedBox(height: Space.md),
          Text(
            complete
                ? tr(
                    'सर्व ठिकाणांचे व्हिडिओ जमा आहेत.',
                    'Every point has been filmed.',
                  )
                // Not "go to each point": the farmer records from wherever
                // they are standing in the field. What is needed is a video
                // for each one.
                : tr(
                    'प्रत्येक ठिकाणाचा व्हिडिओ आवश्यक आहे.',
                    'Each point needs its video before the claim can go.',
                  ),
            style: text.bodySmall,
          ),
        ],
      ),
    );
  }
}

class _Line extends StatelessWidget {
  const _Line({required this.icon, required this.label, required this.value});

  final IconData icon;
  final String label;
  final String value;

  @override
  Widget build(BuildContext context) {
    final text = Theme.of(context).textTheme;
    return Padding(
      padding: const EdgeInsets.symmetric(horizontal: Space.lg, vertical: 10),
      child: Row(
        children: [
          Icon(icon, size: 20, color: Palette.inkMuted),
          const SizedBox(width: Space.md),
          Expanded(child: Text(label, style: text.bodyMedium)),
          const SizedBox(width: Space.sm),
          Flexible(
            child: Text(
              value,
              textAlign: TextAlign.end,
              style: text.titleSmall?.copyWith(fontSize: 15),
            ),
          ),
        ],
      ),
    );
  }
}
