import 'package:flutter/material.dart';

import '../core/dates.dart';
import '../core/language.dart';
import '../ui/theme.dart';

/// One report the office has.
class ReportCard extends StatelessWidget {
  const ReportCard({
    super.key,
    required this.report,
    this.onAppeal,
    this.onOpen,
    this.onReport,
  });

  final Map<String, dynamic> report;

  /// Null while the report has nothing decided to object to.
  final VoidCallback? onAppeal;

  /// Opens what has happened to this claim: the stages, what it is waiting
  /// for, and what each officer recorded.
  final VoidCallback? onOpen;

  /// Opens the assessment report. Null until the claim is decided.
  final VoidCallback? onReport;

  @override
  Widget build(BuildContext context) {
    final text = Theme.of(context).textTheme;
    final state = '${report['state'] ?? ''}';
    final decided = (report['adjudicated_percent'] as num?)?.toDouble();
    final filed = report['filed_at'] ?? report['created_at'];
    final (fg, bg) = _stateColour(state);

    return AppCard(
      onTap: onOpen,
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Row(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              const IconBadge(Icons.description_outlined),
              const SizedBox(width: Space.md),
              Expanded(
                child: Column(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    Text(
                      '${report['reference'] ?? ''}',
                      style: text.titleMedium,
                    ),
                    if (filed != null)
                      Text(
                        tr(
                          'पाठवली ${formatDay(filed)}',
                          'Sent ${formatDay(filed)}',
                        ),
                        style: text.bodySmall,
                      ),
                    const SizedBox(height: Space.sm),
                    StatusPill(
                      _stateLabel(state),
                      foreground: fg,
                      background: bg,
                    ),
                  ],
                ),
              ),
              if (onOpen != null)
                const Padding(
                  padding: EdgeInsets.only(top: 8),
                  child: Icon(Icons.chevron_right, color: Palette.inkMuted),
                ),
            ],
          ),

          // The decision itself. Without it a farmer was told a decision had
          // been recorded and never what it was.
          if (decided != null) ...[
            const SizedBox(height: Space.md),
            Container(
              width: double.infinity,
              padding: const EdgeInsets.all(Space.md),
              decoration: BoxDecoration(
                color: Palette.canvas,
                borderRadius: BorderRadius.circular(Radii.md),
              ),
              child: Row(
                children: [
                  Text(
                    '${decided.round()}%',
                    style: text.headlineSmall?.copyWith(
                      color: Palette.primaryDark,
                    ),
                  ),
                  const SizedBox(width: Space.md),
                  Expanded(
                    child: Text(
                      tr(
                        'नोंदवलेले पीक नुकसान. ही रकमेची नोंद नाही.',
                        'Crop loss recorded. This is not an amount of money.',
                      ),
                      style: text.bodySmall,
                    ),
                  ),
                ],
              ),
            ),
          ],

          // The objection has to be reachable from the decision itself: the
          // farmer who cannot easily reach an office is who the appeal is for.
          if (onReport != null || onAppeal != null) ...[
            const SizedBox(height: Space.sm),
            Wrap(
              spacing: Space.sm,
              children: [
                if (onReport != null)
                  TextButton.icon(
                    onPressed: onReport,
                    icon: const Icon(Icons.assignment_outlined, size: 18),
                    label: Text(tr('अहवाल पहा', 'View report')),
                  ),
                if (onAppeal != null)
                  TextButton.icon(
                    onPressed: onAppeal,
                    icon: const Icon(Icons.gavel_outlined, size: 18),
                    label: Text(tr('आक्षेप नोंदवा', 'Object to this')),
                  ),
              ],
            ),
          ],
        ],
      ),
    );
  }

  /// The state in words a farmer recognises.
  ///
  /// None of them mentions a visit. Nobody visits: what the farmer sent is
  /// what gets examined, and promising an officer leaves them waiting at home.
  String _stateLabel(String state) => switch (state) {
    'draft' => tr('अपूर्ण', 'Not sent yet'),
    'submitted' => tr('पाठवली', 'Sent'),
    'acknowledged' => tr('कार्यालयाला मिळाली', 'Received by the office'),
    'assigned' => tr('अधिकाऱ्यांकडे', 'With the officers'),
    'under_verification' => tr('तपासणी सुरू', 'Being examined'),
    'verified' => tr('तपासणी पूर्ण', 'Examined'),
    'adjudicated' => tr('निर्णय झाला', 'Decided'),
    'closed' => tr('निकाली', 'Closed'),
    'withdrawn' => tr('मागे घेतली', 'Withdrawn'),
    _ => state,
  };

  (Color, Color) _stateColour(String state) => switch (state) {
    'adjudicated' || 'closed' => (Palette.negligibleFg, Palette.negligibleBg),
    'withdrawn' => (Palette.severeFg, Palette.severeBg),
    'draft' => (Palette.moderateFg, Palette.moderateBg),
    _ => (Palette.infoFg, Palette.infoBg),
  };
}
