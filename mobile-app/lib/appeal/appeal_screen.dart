import 'dart:async';

import 'package:flutter/material.dart';

import '../core/db.dart';
import '../core/sync.dart';
import '../core/voice.dart';
import '../core/language.dart';
import '../ui/theme.dart';
import '../core/messages.dart';

/// Objecting to a decision, from the app that made the claim.
///
/// # Why this has to be here
///
/// A farmer files from their own field on their own phone, and the decision
/// comes back to that phone. Until now the only way to contest it was the web
/// console or a trip to the taluka office -- so the one moment a farmer most
/// needs the system to answer them was the one moment it sent them somewhere
/// else. Somebody who cannot easily reach an office is exactly the person the
/// appeal exists for.
///
/// # Why it queues
///
/// A farmer reads a decision they disagree with wherever they happen to be,
/// which on this platform means probably without signal. Requiring a network
/// at that moment means the objection is written, lost, and never made again:
/// the anger passes, the deadline does not wait, and the record stands. So it
/// goes into the same queue as everything else and leaves when there is a
/// signal.
class AppealScreen extends StatefulWidget {
  const AppealScreen({
    super.key,
    required this.store,
    required this.voice,
    required this.sync,
    required this.report,
    this.onFiled,
  });

  final Store store;
  final Voice voice;
  final Sync sync;

  /// The report being objected to, as the server described it.
  final Map<String, dynamic> report;

  final VoidCallback? onFiled;

  @override
  State<AppealScreen> createState() => _AppealScreenState();
}

/// The grounds, carried in the app rather than fetched.
///
/// They are stable, they are short, and this screen must open with no signal
/// -- a farmer choosing why they disagree cannot be made to wait for a list.
/// The codes are the server's own; the words are this app's, because the
/// server's labels are written for an officer's screen.
const _grounds = <({String code, String mr, String en, IconData icon})>[
  (
    code: 'damage_percent_low',
    mr: 'नुकसान कमी दाखवले आहे',
    en: 'The damage recorded is less than what happened',
    icon: Icons.trending_down,
  ),
  (
    code: 'area_wrong',
    mr: 'क्षेत्र चुकीचे आहे',
    en: 'The area assessed is not the land I lost',
    icon: Icons.crop_free,
  ),
  (
    code: 'evidence_ignored',
    mr: 'माझे फोटो विचारात घेतले नाहीत',
    en: 'My photographs were not taken into account',
    icon: Icons.photo_library,
  ),
  (
    // The exception path a no-visit system needs: some losses a photograph
    // cannot carry, and without this they have nowhere to go.
    code: 'not_inspected',
    mr: 'फोटोत दिसत नाही, प्रत्यक्ष पाहणी हवी',
    en: 'The photographs cannot show it; I want somebody to come and see',
    icon: Icons.travel_explore,
  ),
  (code: 'other', mr: 'इतर काही', en: 'Something else', icon: Icons.more_horiz),
];

class _AppealScreenState extends State<AppealScreen> {
  final _statement = TextEditingController();
  final _statementFocus = FocusNode();

  String? _ground;
  bool _saving = false;
  String? _error;

  @override
  void dispose() {
    _statement.dispose();
    _statementFocus.dispose();
    unawaited(widget.voice.stop());
    super.dispose();
  }

  bool get _complete => _ground != null && _statement.text.trim().length >= 10;

  Future<void> _file() async {
    if (!_complete || _saving) return;

    final reportID = '${widget.report['id'] ?? ''}';
    if (reportID.isEmpty) {
      setState(
        () => _error = tr(Lines.appealNoReport.mr, Lines.appealNoReport.en),
      );
      return;
    }

    setState(() {
      _saving = true;
      _error = null;
    });

    try {
      // Refused here rather than by the server days later. One open objection
      // at a time is the server's rule, and a farmer who writes a second one
      // should be told while they are still looking at the screen.
      if (await widget.store.hasAppeal(reportID)) {
        if (!mounted) return;
        setState(() {
          _saving = false;
          _error = tr(Lines.appealAlreadyFiled.mr, Lines.appealAlreadyFiled.en);
        });
        return;
      }

      await widget.store.putAppeal({
        'local_id': DateTime.now().microsecondsSinceEpoch.toString(),
        'report_id': reportID,
        'ground': _ground,
        'statement': _statement.text.trim(),
        'status': 'pending',
        'attempts': 0,
      });

      unawaited(widget.sync.run());
      widget.onFiled?.call();

      if (!mounted) return;
      ScaffoldMessenger.of(context).showSnackBar(
        SnackBar(content: Text(tr(Lines.appealSaved.mr, Lines.appealSaved.en))),
      );
      Navigator.of(context).pop(true);
    } catch (e) {
      if (!mounted) return;
      setState(() {
        _saving = false;
        _error = deviceError(
          'साठवता आले नाही',
          'Could not save. Try again.',
          e,
          'appeal.save',
        );
      });
    }
  }

  @override
  Widget build(BuildContext context) {
    final text = Theme.of(context).textTheme;
    final decided = (widget.report['adjudicated_percent'] as num?)?.toDouble();

    return Scaffold(
      appBar: AppBar(title: Text(tr('आक्षेप नोंदवा', 'Object to a decision'))),
      body: ListView(
        padding: Space.page,
        children: [
          _WhatIsBeingAppealed(
            reference: '${widget.report['reference'] ?? ''}',
            decided: decided,
          ),

          SectionHeader(
            tr('कशाबद्दल आक्षेप आहे?', 'What is your objection about?'),
          ),
          Card(
            clipBehavior: Clip.antiAlias,
            child: RadioGroup<String>(
              groupValue: _ground,
              onChanged: (v) => setState(() => _ground = v),
              child: Column(
                children: [
                  for (final g in _grounds)
                    RadioListTile<String>(
                      value: g.code,
                      secondary: Icon(g.icon),
                      title: Text(tr(g.mr, g.en)),
                    ),
                ],
              ),
            ),
          ),

          SectionHeader(tr('तुमच्या शब्दांत सांगा', 'In your own words')),
          TextField(
            controller: _statement,
            focusNode: _statementFocus,
            minLines: 4,
            maxLines: 8,
            onChanged: (_) => setState(() {}),
            decoration: InputDecoration(
              hintText: tr(
                'उदा. संपूर्ण शेतातील पीक आडवे झाले होते, अर्धे नाही.',
                'e.g. The whole field was flattened, not half of it.',
              ),
              helperText: tr('हे अधिकारी वाचतील.', 'An officer reads this.'),
            ),
          ),

          if (_error != null) ...[
            const SizedBox(height: Space.md),
            Notice(
              tone: Tone.danger,
              icon: Icons.error_outline,
              title: Language.of(_error!),
              margin: EdgeInsets.zero,
            ),
          ],

          const SizedBox(height: Space.xl),
          BigButton(
            label: _saving
                ? tr('जतन करत आहे…', 'Saving…')
                : tr('आक्षेप नोंदवा', 'Record my objection'),
            onPressed: _complete && !_saving ? _file : null,
          ),
          const SizedBox(height: Space.md),
          Text(
            tr(
              'नेटवर्क नसले तरी चालेल. नेटवर्क आल्यावर आपोआप जाईल.',
              'No network needed. It sends by itself when a signal comes.',
            ),
            textAlign: TextAlign.center,
            style: text.labelMedium,
          ),
        ],
      ),
    );
  }
}

class _WhatIsBeingAppealed extends StatelessWidget {
  const _WhatIsBeingAppealed({required this.reference, this.decided});

  final String reference;
  final double? decided;

  @override
  Widget build(BuildContext context) {
    final text = Theme.of(context).textTheme;
    return AppCard(
      margin: EdgeInsets.zero,
      child: Row(
        children: [
          const IconBadge(Icons.description_outlined),
          const SizedBox(width: Space.md),
          Expanded(
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                Text(reference, style: text.titleMedium),
                if (decided != null)
                  Text(
                    tr(
                      'नोंदवलेले नुकसान ${decided!.round()}%',
                      'Recorded loss ${decided!.round()}%',
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
