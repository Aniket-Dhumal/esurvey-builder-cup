import 'package:flutter/material.dart';

import '../core/db.dart';
import '../core/language.dart';
import '../core/voice.dart';
import '../ui/theme.dart';
import 'faults_screen.dart';
import 'questions.dart';

/// The questions a farmer would otherwise have to go to the office to ask.
///
/// # Why it no longer speaks
///
/// Tapping a question used to read the answer aloud. It was one more screen
/// talking at somebody sitting at home with the phone in front of them, and
/// the voice is kept for the field walk, where eyes are on the crop.
///
/// # Why one answer at a time
///
/// A list that expands everything is a wall of text. Opening a question
/// closes the one before it.
class HelpScreen extends StatefulWidget {
  const HelpScreen({super.key, required this.voice, this.store});

  final Voice voice;

  /// Opens the technical fault log, where one is available. Null in tests and
  /// anywhere the store is not to hand; the entry simply does not appear.
  final Store? store;

  @override
  State<HelpScreen> createState() => _HelpScreenState();
}

class _HelpScreenState extends State<HelpScreen> {
  int? _open;

  @override
  Widget build(BuildContext context) {
    final text = Theme.of(context).textTheme;
    return Scaffold(
      appBar: AppBar(title: Text(tr('मदत', 'Help'))),
      body: ListView(
        padding: Space.page,
        children: [
          Padding(
            padding: const EdgeInsets.fromLTRB(4, 4, 4, Space.md),
            child: Text(
              tr('नेहमी विचारले जाणारे प्रश्न', 'Common questions'),
              style: text.bodyMedium?.copyWith(color: Palette.inkMuted),
            ),
          ),
          Card(
            clipBehavior: Clip.antiAlias,
            child: Column(
              children: [
                for (var i = 0; i < questions.length; i++) ...[
                  if (i > 0) const Divider(indent: 56),
                  _QuestionTile(
                    question: questions[i],
                    open: _open == i,
                    onTap: () => setState(() => _open = _open == i ? null : i),
                  ),
                ],
              ],
            ),
          ),
          const SizedBox(height: Space.lg),
          // The honest floor of what this screen can do. Somebody whose
          // question is not here needs a person, and saying so is better than
          // leaving them tapping through answers that are not theirs.
          Notice(
            tone: Tone.info,
            icon: Icons.support_agent_outlined,
            title: tr('प्रश्न इथे नाही?', 'Question not here?'),
            message: tr(
              'तालुका कृषी कार्यालयात विचारा.',
              'Ask at the taluka agriculture office.',
            ),
          ),
          if (widget.store != null) ...[
            const SizedBox(height: Space.md),
            // Below the questions, quiet, and labelled for what it is. A farmer
            // should never feel invited into it; somebody helping them over the
            // phone needs to be able to find it in one instruction.
            Card(
              clipBehavior: Clip.antiAlias,
              child: ListTile(
                leading: const Icon(Icons.bug_report_outlined),
                title: Text(tr('तांत्रिक नोंदी', 'Technical faults')),
                subtitle: Text(
                  tr(
                    'ॲपमध्ये काही अडचण आली असल्यास ती येथे दिसते. मदत '
                        'करणाऱ्याला ही माहिती उपयोगी पडते.',
                    'Shows anything that has gone wrong inside the app. Useful '
                        'to whoever is helping you.',
                  ),
                ),
                trailing: const Icon(Icons.chevron_right),
                onTap: () => Navigator.of(context).push(
                  MaterialPageRoute<void>(
                    builder: (_) => FaultsScreen(store: widget.store!),
                  ),
                ),
              ),
            ),
          ],
        ],
      ),
    );
  }
}

class _QuestionTile extends StatelessWidget {
  const _QuestionTile({
    required this.question,
    required this.open,
    required this.onTap,
  });

  final Question question;
  final bool open;
  final VoidCallback onTap;

  @override
  Widget build(BuildContext context) {
    final text = Theme.of(context).textTheme;
    return InkWell(
      onTap: onTap,
      child: Padding(
        padding: const EdgeInsets.fromLTRB(16, 14, 12, 14),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Row(
              children: [
                Icon(question.icon, color: Palette.primary, size: 22),
                const SizedBox(width: Space.lg),
                Expanded(
                  child: Text(
                    tr(question.mr, question.en),
                    style: text.titleSmall?.copyWith(
                      fontSize: 15,
                      fontWeight: open ? FontWeight.w600 : FontWeight.w500,
                    ),
                  ),
                ),
                AnimatedRotation(
                  turns: open ? 0.5 : 0,
                  duration: const Duration(milliseconds: 200),
                  child: const Icon(Icons.expand_more, color: Palette.inkMuted),
                ),
              ],
            ),
            AnimatedSize(
              duration: const Duration(milliseconds: 200),
              alignment: Alignment.topCenter,
              child: open
                  ? Padding(
                      padding: const EdgeInsets.fromLTRB(38, 10, 8, 0),
                      child: Text(
                        tr(question.answerMr, question.answerEn),
                        style: text.bodyMedium?.copyWith(
                          color: Palette.inkMuted,
                        ),
                      ),
                    )
                  : const SizedBox(width: double.infinity),
            ),
          ],
        ),
      ),
    );
  }
}
