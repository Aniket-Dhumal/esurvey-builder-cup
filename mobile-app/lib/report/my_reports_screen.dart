import 'dart:async';

import 'package:flutter/material.dart';

import '../core/farmer_data.dart';
import '../core/language.dart';
import '../core/voice.dart';
import 'assessment_report_screen.dart';
import '../ui/theme.dart';
import 'progress_screen.dart';
import 'report_card.dart';

/// Every report this farmer has filed, and what was decided on each.
///
/// # Why this is a tab and not a section of Home
///
/// Home answers "what needs doing now" -- a live event, something stuck in the
/// queue. This answers "what happened to what I sent", which is a different
/// question asked at a different time, usually weeks later and usually
/// anxiously. Buried at the bottom of a scrolling home screen it was reachable
/// only by people who already knew it was there.
///
/// # Why it works offline
///
/// The list is served from the cache first and refreshed behind it. A farmer
/// checking on a claim from their own field has no signal, and an empty screen
/// with a spinner would tell them their reports had disappeared.
class MyReportsScreen extends StatefulWidget {
  const MyReportsScreen({
    super.key,
    required this.data,
    required this.voice,
    required this.onAppeal,
  });

  final FarmerData data;
  final Voice voice;

  /// Objecting to a decision. Null-returning for reports with nothing decided
  /// yet -- the button is not shown at all rather than shown and refused.
  final Future<void> Function(Map<String, dynamic> report) onAppeal;

  @override
  State<MyReportsScreen> createState() => _MyReportsScreenState();
}

class _MyReportsScreenState extends State<MyReportsScreen> {
  Cached<List<dynamic>> _reports = const Cached(null, null, fresh: false);
  bool _loading = true;

  /// What has happened to one claim, in full.
  Future<void> _openProgress(Map<String, dynamic> report) async {
    final farmerId = widget.data.store.farmer ?? '';
    final grant = farmerId.isEmpty
        ? null
        : await widget.data.store.grantFor(farmerId);
    if (!mounted) return;
    await Navigator.of(context).push(
      MaterialPageRoute<void>(
        builder: (_) => ProgressScreen(
          data: widget.data,
          voice: widget.voice,
          report: report,
          grant: grant,
        ),
      ),
    );
  }

  @override
  void initState() {
    super.initState();
    unawaited(_load());
  }

  Future<void> _load() async {
    // Cache first, so something is on screen immediately even with no signal.
    // Whose reports: the farmer picked on this handset, not the account. One
    // signed-in number may act for several, and the office answers for the
    // one named.
    final farmerId = widget.data.store.farmer ?? '';
    // The grant is what lets the office answer for a farmer this account acts
    // for rather than is. Without it this screen was empty for every farmer
    // added by id and date of birth -- which is now most of them.
    final grant = farmerId.isEmpty
        ? null
        : await widget.data.store.grantFor(farmerId);
    final cached = await widget.data.reports(farmerId, refresh: false);
    if (mounted && cached.value != null) {
      setState(() {
        _reports = cached;
        _loading = false;
      });
    }

    final fresh = await widget.data.reports(farmerId, grant: grant);
    if (!mounted) return;
    setState(() {
      _reports = fresh;
      _loading = false;
    });
  }

  /// Whether the claim has a decision, and so an assessment report.
  bool _decided(dynamic report) =>
      const {'adjudicated', 'closed'}.contains('${(report as Map)['state']}');

  /// The farmer's copy of the decided claim's assessment report.
  Future<void> _openReport(Map<String, dynamic> report) async {
    final farmerId = widget.data.store.farmer ?? '';
    final grant = farmerId.isEmpty
        ? null
        : await widget.data.store.grantFor(farmerId);
    if (!mounted) return;
    await Navigator.of(context).push(
      MaterialPageRoute<void>(
        builder: (_) => AssessmentReportScreen(
          data: widget.data,
          reportId: '${report['id'] ?? ''}',
          reference: '${report['reference'] ?? ''}',
          grant: grant,
        ),
      ),
    );
  }

  /// Whether there is a decision to object to.
  ///
  /// An objection needs something to be against. Offering it on a report the
  /// office has not decided yet would collect objections to nothing and teach
  /// farmers the button does not work.
  bool _appealable(Map<String, dynamic> report) =>
      report['adjudicated_percent'] != null;

  @override
  Widget build(BuildContext context) {
    final reports = _reports.value ?? const [];

    return Scaffold(
      appBar: AppBar(title: Text(tr('माझ्या नोंदी', 'My reports'))),
      body: RefreshIndicator(
        onRefresh: _load,
        child: ListView(
          padding: Space.page,
          children: [
            // How old the list is, when it came from the cache. A farmer
            // looking at a decision needs to know whether it is this
            // morning's answer or last week's.
            if (!_reports.fresh && _reports.age != null)
              Padding(
                padding: const EdgeInsets.only(left: 4, bottom: Space.md),
                child: Text(
                  Language.of(_reports.age!),
                  style: Theme.of(context).textTheme.labelSmall,
                ),
              ),
            if (_loading && reports.isEmpty)
              const Padding(
                padding: EdgeInsets.only(top: 48),
                child: Center(child: CircularProgressIndicator()),
              )
            else if (reports.isEmpty)
              EmptyState(
                icon: Icons.description_outlined,
                message: tr(
                  'अजून कोणतीही नोंद पाठवलेली नाही.',
                  'You have not sent any reports yet.',
                ),
              )
            else
              for (final report in reports)
                ReportCard(
                  report: report as Map<String, dynamic>,
                  onOpen: () => unawaited(_openProgress(report)),
                  onReport: _decided(report)
                      ? () => unawaited(_openReport(report))
                      : null,
                  onAppeal: _appealable(report)
                      ? () => unawaited(widget.onAppeal(report))
                      : null,
                ),
          ],
        ),
      ),
    );
  }
}
