import 'dart:async';

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';

import '../core/farmer_data.dart';
import '../core/language.dart';
import '../core/voice.dart';
import '../ui/theme.dart';
import 'assessment_report_screen.dart';

/// What has happened to one claim.
///
/// # The question this answers
///
/// A farmer files in August and hears nothing. Weeks later they want to know
/// one thing: is anybody doing anything, or has it been forgotten. Until now
/// the only answer was a status word on a card, which said what the claim was
/// and never what it was waiting for.
///
/// # Why the office sends keys and this screen holds the words
///
/// Every sentence here is in the app. The office sends `received`, `field`,
/// `awaiting_second_officer`; this turns them into Marathi. So the screen a
/// farmer opens standing in their own field, with no signal, reads exactly as
/// it would in an office -- from the copy of the answer this phone already
/// has.
///
/// # What is shown and when
///
/// The stages always. The officers' remarks and the loss figure only once both
/// village officers have recorded: they examine the claim blind to each other,
/// and a figure released early would reach the second officer through the
/// farmer. The office enforces that; this screen renders what it is given and
/// says plainly what is still to come.
class ProgressScreen extends StatefulWidget {
  const ProgressScreen({
    super.key,
    required this.data,
    required this.voice,
    required this.report,
    this.grant,
  });

  final FarmerData data;
  final Voice voice;

  /// The report as the office last described it: its id, reference and state.
  final Map<String, dynamic> report;

  /// Proof this handset may ask about this farmer's claims, for a farmer whose
  /// registered number is not the one that signed in.
  final String? grant;

  @override
  State<ProgressScreen> createState() => _ProgressScreenState();
}

class _ProgressScreenState extends State<ProgressScreen> {
  Cached<Map<String, dynamic>?> _progress = const Cached(
    null,
    null,
    fresh: false,
  );
  bool _loading = true;

  String get _id => '${widget.report['id'] ?? ''}';

  @override
  void initState() {
    super.initState();
    unawaited(_load());
  }

  Future<void> _load() async {
    // What the phone knows first, so the screen is never blank while a request
    // that may never arrive is in flight.
    final cached = await widget.data.progress(_id, refresh: false);
    if (mounted) {
      setState(() {
        _progress = cached;
        _loading = cached.value == null;
      });
    }

    final fresh = await widget.data.progress(_id, grant: widget.grant);
    if (!mounted) return;
    setState(() {
      _progress = fresh;
      _loading = false;
    });
  }

  Future<void> _openReport() => Navigator.of(context).push(
    MaterialPageRoute<void>(
      builder: (_) => AssessmentReportScreen(
        data: widget.data,
        reportId: _id,
        reference: '${widget.report['reference'] ?? ''}',
        grant: widget.grant,
      ),
    ),
  );

  @override
  Widget build(BuildContext context) {
    final p = _progress.value;
    final reference = '${p?['reference'] ?? widget.report['reference'] ?? ''}';
    final stages = (p?['stages'] as List<dynamic>? ?? const [])
        .map((s) => (s as Map).cast<String, dynamic>())
        .toList();
    // The report exists once the claim is decided. The office's own state
    // says so, and so does the decision stage when the list is older than the
    // progress.
    final decided =
        const {'adjudicated', 'closed'}.contains(widget.report['state']) ||
        stages.any((s) => s['key'] == 'decision' && s['state'] == 'done');

    return Scaffold(
      appBar: AppBar(title: Text(tr('नोंदीची प्रगती', 'Claim progress'))),
      body: RefreshIndicator(
        onRefresh: _load,
        child: ListView(
          padding: Space.page,
          children: [
            _Receipt(reference: reference),

            if (_loading && p == null)
              const Padding(
                padding: EdgeInsets.only(top: 48),
                child: Center(child: CircularProgressIndicator()),
              )
            else if (p == null)
              EmptyState(
                icon: Icons.cloud_off_outlined,
                message: tr(
                  'सध्या माहिती मिळाली नाही. नेटवर्क आल्यावर इथे दिसेल. तुमची '
                      'नोंद कार्यालयाकडे सुरक्षित आहे.',
                  'Nothing could be fetched just now. It will appear when '
                      'there is a signal. Your claim is safe with the office.',
                ),
              )
            else ...[
              SectionHeader(
                tr('टप्पे', 'Stages'),
                trailing: _progress.age == null
                    ? null
                    : Text(
                        Language.of(_progress.age!),
                        style: Theme.of(context).textTheme.labelSmall,
                      ),
              ),
              AppCard(
                child: Column(
                  children: [
                    for (var i = 0; i < stages.length; i++)
                      _StageRow(stage: stages[i], last: i == stages.length - 1),
                  ],
                ),
              ),
              SectionHeader(tr('निकाल', 'Outcome')),
              _Figure(percent: (p['assessed_percent'] as num?)?.toDouble()),
              if (decided) _ReportLink(onOpen: _openReport),
              _Remarks(
                remarks: (p['remarks'] as List<dynamic>? ?? const [])
                    .map((r) => (r as Map).cast<String, dynamic>())
                    .toList(),
                decided: p['assessed_percent'] != null,
              ),
            ],
          ],
        ),
      ),
    );
  }
}

/// The full assessment report, once there is a decision to explain.
class _ReportLink extends StatelessWidget {
  const _ReportLink({required this.onOpen});

  final VoidCallback onOpen;

  @override
  Widget build(BuildContext context) {
    final text = Theme.of(context).textTheme;
    return AppCard(
      onTap: onOpen,
      child: Row(
        children: [
          const IconBadge(Icons.assignment_outlined),
          const SizedBox(width: Space.md),
          Expanded(
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                Text(
                  tr('मूल्यांकन अहवाल पहा', 'View the assessment report'),
                  style: text.titleMedium,
                ),
                Text(
                  tr(
                    'पुरावे, तपासणी आणि निर्णय कसा झाला ते',
                    'The evidence, the checks, and how it was decided',
                  ),
                  style: text.bodySmall,
                ),
              ],
            ),
          ),
          const Icon(Icons.chevron_right, color: Palette.inkMuted),
        ],
      ),
    );
  }
}

/// The number the claim is tracked by.
///
/// Shown first. It is the one thing a farmer needs when they ring the office
/// or walk into it.
class _Receipt extends StatelessWidget {
  const _Receipt({required this.reference});

  final String reference;

  @override
  Widget build(BuildContext context) {
    final text = Theme.of(context).textTheme;
    return Container(
      width: double.infinity,
      padding: const EdgeInsets.all(Space.lg),
      decoration: BoxDecoration(
        color: Palette.primarySoft,
        borderRadius: BorderRadius.circular(Radii.lg),
      ),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Text(
            tr('नोंद क्रमांक', 'Claim number'),
            style: text.labelMedium?.copyWith(color: Palette.primaryDark),
          ),
          const SizedBox(height: Space.xs),
          Row(
            children: [
              Expanded(
                child: SelectableText(
                  reference,
                  style: text.titleLarge?.copyWith(
                    letterSpacing: 0.5,
                    color: Palette.primaryDark,
                  ),
                ),
              ),
              IconButton(
                icon: const Icon(Icons.copy_rounded, size: 20),
                color: Palette.primaryDark,
                tooltip: tr('कॉपी करा', 'Copy'),
                onPressed: reference.isEmpty
                    ? null
                    : () {
                        unawaited(
                          Clipboard.setData(ClipboardData(text: reference)),
                        );
                        ScaffoldMessenger.of(context).showSnackBar(
                          SnackBar(
                            content: Text(tr('क्रमांक कॉपी केला', 'Copied')),
                          ),
                        );
                      },
              ),
            ],
          ),
          Text(
            tr(
              'कार्यालयाशी बोलताना हा क्रमांक सांगा.',
              'Give this number when you speak to the office.',
            ),
            style: text.bodySmall?.copyWith(color: Palette.primaryDark),
          ),
        ],
      ),
    );
  }
}

/// One step of the claim's life, on a vertical timeline.
class _StageRow extends StatelessWidget {
  const _StageRow({required this.stage, required this.last});

  final Map<String, dynamic> stage;
  final bool last;

  @override
  Widget build(BuildContext context) {
    final text = Theme.of(context).textTheme;
    final key = '${stage['key']}';
    final state = '${stage['state']}';
    final why = '${stage['why'] ?? ''}';

    final (icon, colour) = switch (state) {
      'done' => (Icons.check_circle, Palette.primary),
      'current' => (Icons.radio_button_checked, Palette.primary),
      // Current, and not moving by itself. Drawn apart from "in progress",
      // because a claim that has stopped must not read as one being worked on.
      'attention' => (Icons.error, Palette.moderateFg),
      _ => (Icons.circle_outlined, Palette.outline),
    };

    final title = _stageTitle(key);
    final note = why.isEmpty ? null : _whyLine(why);
    final pending = state == 'pending';

    return IntrinsicHeight(
      child: Row(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          SizedBox(
            width: 24,
            child: Column(
              children: [
                Icon(icon, color: colour, size: 22),
                if (!last)
                  Expanded(
                    child: Container(
                      width: 2,
                      margin: const EdgeInsets.symmetric(vertical: 2),
                      color: state == 'done'
                          ? Palette.primary
                          : Palette.outlineSoft,
                    ),
                  ),
              ],
            ),
          ),
          const SizedBox(width: Space.md),
          Expanded(
            child: Padding(
              padding: EdgeInsets.only(bottom: last ? 0 : Space.lg),
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  Text(
                    tr(title.mr, title.en),
                    style: text.titleSmall?.copyWith(
                      fontSize: 15,
                      fontWeight: pending ? FontWeight.w400 : FontWeight.w600,
                      color: pending ? Palette.inkMuted : Palette.ink,
                    ),
                  ),
                  if (note != null) ...[
                    const SizedBox(height: 2),
                    Text(
                      tr(note.mr, note.en),
                      style: text.bodySmall?.copyWith(
                        color: state == 'attention'
                            ? Palette.moderateFg
                            : Palette.inkMuted,
                      ),
                    ),
                  ],
                ],
              ),
            ),
          ),
        ],
      ),
    );
  }
}

/// The five stages, in words a farmer recognises.
///
/// Not the office's own names. "Field verification" is what the process is
/// called; "two officers are checking" is what is happening.
({String mr, String en}) _stageTitle(String key) => switch (key) {
  'received' => (mr: 'कार्यालयाला मिळाली', en: 'Received by the office'),
  'checked' => (mr: 'नोंदींवरून तपासणी', en: 'Checked against records'),
  'field' => (mr: 'अधिकाऱ्यांकडून तपासणी', en: 'Examined by officers'),
  'decision' => (mr: 'निर्णय', en: 'Decision'),
  'closed' => (mr: 'पूर्ण', en: 'Finished'),
  _ => (mr: key, en: key),
};

/// Why a stage is where it is.
///
/// Only the ones a farmer can act on or be reassured by. Which rung a claim
/// sits at and whether the two officers disagreed never reach this phone: the
/// office does not send them.
({String mr, String en})? _whyLine(String why) => switch (why) {
  'evidence_arriving' => (
    mr: 'तुमचे फोटो अजून पाठवले जात आहेत. नेटवर्क आल्यावर आपोआप जातील.',
    en: 'Your photos are still being sent. They go by themselves with signal.',
  ),
  'evidence_short' => (
    mr: 'काही फोटो पोहोचले नाहीत. त्याशिवाय नोंद पुढे पाठवली आहे.',
    en: 'Some photos never arrived. The claim went on without them.',
  ),
  'assessing' => (
    mr: 'उपग्रह आणि हवामानाची माहिती तपासली जात आहे.',
    en: 'Satellite and weather records are being checked.',
  ),
  'assessment_unavailable' => (
    mr: 'ही तपासणी होऊ शकली नाही. अधिकारी तुमचे फोटो पाहून ठरवतील.',
    en: 'That check could not be done. Officers will decide from your photos.',
  ),
  'awaiting_both_officers' => (
    mr: 'तलाठी आणि कृषी सहाय्यक यांच्याकडे दिली आहे.',
    en: 'With the Talathi and the Krushi Sahayak.',
  ),
  'awaiting_second_officer' => (
    mr: 'दोघांपैकी एकाने नोंद केली आहे. दुसऱ्याची बाकी आहे.',
    en: 'One of the two has recorded. The other is still to.',
  ),
  'no_authority' => (
    mr: 'अधिकाऱ्याकडे देण्याचे काम सुरू आहे.',
    en: 'It is being given to an officer.',
  ),
  'returned_for_more' => (
    mr: 'आणखी तपासणीसाठी परत पाठवली आहे.',
    en: 'Sent back for another look.',
  ),
  'withdrawn' => (
    mr: 'ही नोंद मागे घेतली आहे.',
    en: 'This claim was withdrawn.',
  ),
  _ => null,
};

/// The figure, once there is one.
class _Figure extends StatelessWidget {
  const _Figure({required this.percent});

  final double? percent;

  @override
  Widget build(BuildContext context) {
    final text = Theme.of(context).textTheme;
    if (percent == null) {
      return EmptyState(
        icon: Icons.hourglass_empty_rounded,
        message: tr(
          'दोन्ही अधिकाऱ्यांची नोंद झाल्यावर नुकसानीची टक्केवारी इथे दिसेल.',
          'The loss percentage appears here once both officers have recorded.',
        ),
      );
    }
    return AppCard(
      child: Row(
        children: [
          SizedBox(
            width: 72,
            height: 72,
            child: Stack(
              fit: StackFit.expand,
              children: [
                CircularProgressIndicator(
                  value: (percent! / 100).clamp(0, 1),
                  strokeWidth: 6,
                  backgroundColor: Palette.canvasSunk,
                ),
                Center(
                  child: Text(
                    '${percent!.round()}%',
                    style: text.titleMedium?.copyWith(
                      color: Palette.primaryDark,
                    ),
                  ),
                ),
              ],
            ),
          ),
          const SizedBox(width: Space.lg),
          Expanded(
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                Text(
                  tr('नोंदवलेले नुकसान', 'Loss recorded'),
                  style: text.titleMedium,
                ),
                const SizedBox(height: 2),
                // Said every time the figure is. A percentage next to a claim
                // reads as money to anybody who has been waiting for money.
                Text(
                  tr(
                    'ही रकमेची नोंद नाही. पुढच्या टप्प्यात आकडा बदलू शकतो.',
                    'Not an amount of money. The figure can still change.',
                  ),
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

/// What each officer wrote.
class _Remarks extends StatelessWidget {
  const _Remarks({required this.remarks, required this.decided});

  final List<Map<String, dynamic>> remarks;
  final bool decided;

  @override
  Widget build(BuildContext context) {
    final text = Theme.of(context).textTheme;
    if (remarks.isEmpty) return const SizedBox.shrink();

    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        SectionHeader(tr('अधिकाऱ्यांचे शेरे', 'Officer remarks')),
        for (final r in remarks)
          AppCard(
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                Row(
                  children: [
                    const IconBadge(Icons.person_outline, size: 32),
                    const SizedBox(width: Space.md),
                    Expanded(
                      child: Text(
                        _roleLabel('${r['role']}'),
                        style: text.titleSmall,
                      ),
                    ),
                    if (r['percent'] != null)
                      StatusPill.info('${(r['percent'] as num).round()}%'),
                  ],
                ),
                if ('${r['text'] ?? ''}'.isNotEmpty) ...[
                  const SizedBox(height: Space.sm),
                  Text('${r['text']}', style: text.bodyMedium),
                ],
              ],
            ),
          ),
      ],
    );
  }
}

/// The rank, not the person.
///
/// The office sends no name: the remark is what explains the decision, and a
/// name mostly tells a farmer who to go and find.
String _roleLabel(String role) => switch (role) {
  'talathi' => tr('तलाठी', 'Talathi'),
  'krushi_sahayak' => tr('कृषी सहाय्यक', 'Krushi Sahayak'),
  'mandal_officer' => tr('मंडळ अधिकारी', 'Circle Officer'),
  'tehsildar' => tr('तहसीलदार', 'Tehsildar'),
  'district_magistrate' => tr('जिल्हाधिकारी', 'District Collector'),
  _ => role,
};
