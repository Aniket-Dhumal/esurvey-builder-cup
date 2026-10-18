import 'dart:async';

import 'package:flutter/material.dart';
import 'package:webview_flutter/webview_flutter.dart';

import '../core/farmer_data.dart';
import '../core/language.dart';
import '../ui/theme.dart';

/// The farmer's copy of their decided claim's assessment report.
///
/// # Why the office renders it and the phone only shows it
///
/// It is the same document the office prints and keeps: the evidence, what
/// each source said and could not say, the computer's reasoning in full, and
/// the officers' findings. A second rendering written in the app would drift
/// from it, and a farmer at an appeal must be holding the office's copy.
///
/// # Why it is kept on the phone
///
/// The farmer shows it at an office, or reads it again in their field, and
/// neither has a signal to count on. The last copy fetched opens offline.
///
/// # What the page may do
///
/// Nothing but be read. No script runs and no link leaves it: it is a record,
/// and a page that could navigate would be a browser inside the app.
class AssessmentReportScreen extends StatefulWidget {
  const AssessmentReportScreen({
    super.key,
    required this.data,
    required this.reportId,
    required this.reference,
    this.grant,
  });

  final FarmerData data;
  final String reportId;
  final String reference;
  final String? grant;

  @override
  State<AssessmentReportScreen> createState() => _AssessmentReportScreenState();
}

class _AssessmentReportScreenState extends State<AssessmentReportScreen> {
  AssessmentReport? _report;
  WebViewController? _controller;
  bool _loading = true;

  @override
  void initState() {
    super.initState();
    unawaited(_load());
  }

  Future<void> _load() async {
    setState(() => _loading = true);
    final report = await widget.data.assessmentReport(
      widget.reportId,
      grant: widget.grant,
    );
    if (!mounted) return;

    WebViewController? controller;
    final html = report.html;
    if (html != null) {
      controller = WebViewController()
        ..setJavaScriptMode(JavaScriptMode.disabled)
        ..setBackgroundColor(Colors.white)
        ..setNavigationDelegate(
          NavigationDelegate(
            onNavigationRequest: (_) => NavigationDecision.prevent,
          ),
        );
      await controller.loadHtmlString(inRegionLanguage(html));
    }
    if (!mounted) return;
    setState(() {
      _report = report;
      _controller = controller;
      _loading = false;
    });
  }

  @override
  Widget build(BuildContext context) {
    final report = _report;
    final controller = _controller;

    return Scaffold(
      appBar: AppBar(
        title: Text(tr('मूल्यांकन अहवाल', 'Assessment report')),
        actions: [
          IconButton(
            tooltip: tr('पुन्हा आणा', 'Refresh'),
            onPressed: _loading ? null : _load,
            icon: const Icon(Icons.refresh),
          ),
        ],
        bottom: report != null && report.html != null && !report.fresh
            ? PreferredSize(
                preferredSize: const Size.fromHeight(32),
                child: Container(
                  width: double.infinity,
                  color: Palette.infoBg,
                  padding: const EdgeInsets.symmetric(
                    horizontal: 16,
                    vertical: 6,
                  ),
                  child: Text(
                    tr(
                      'फोनवर जतन केलेली प्रत दाखवत आहे',
                      'Showing the copy saved on this phone',
                    ),
                    style: const TextStyle(
                      color: Palette.infoFg,
                      fontSize: 12.5,
                    ),
                  ),
                ),
              )
            : null,
      ),
      body: _loading
          ? const Center(child: CircularProgressIndicator())
          : controller != null
          ? WebViewWidget(controller: controller)
          : _Unavailable(report: report, onRetry: _load),
    );
  }
}

/// The document shows both languages in full, one after the other. On the
/// farmer's phone only the region's half is shown -- the printed copy keeps
/// both.
String inRegionLanguage(String html) {
  final hide = Language.isMarathi ? 'en' : 'mr';
  const marker = '</head>';
  final style =
      '<style>section.report[lang="$hide"]{display:none!important}</style>';
  final at = html.indexOf(marker);
  if (at < 0) return html;
  return html.substring(0, at) + style + html.substring(at);
}

class _Unavailable extends StatelessWidget {
  const _Unavailable({required this.report, required this.onRetry});

  final AssessmentReport? report;
  final VoidCallback onRetry;

  @override
  Widget build(BuildContext context) {
    final text = Theme.of(context).textTheme;
    final notYet = report?.notYet ?? false;
    final offline = report?.offline ?? false;

    final (icon, title, body) = notYet
        ? (
            Icons.hourglass_empty_rounded,
            tr('अहवाल अजून तयार नाही', 'The report is not ready yet'),
            tr(
              'तुमच्या नोंदीवर निर्णय झाल्यावर मूल्यांकन अहवाल इथे पाहता येईल.',
              'The assessment report can be viewed here once your claim is '
                  'decided.',
            ),
          )
        : offline
        ? (
            Icons.cloud_off_outlined,
            tr('नेटवर्क नाही', 'No network'),
            tr(
              'अहवाल एकदा नेटवर्क असताना उघडा. त्यानंतर तो नेटवर्कशिवायही '
                  'पाहता येईल.',
              'Open the report once with a signal. After that it opens '
                  'without one.',
            ),
          )
        : (
            Icons.description_outlined,
            tr('अहवाल मिळाला नाही', 'The report could not be fetched'),
            tr(
              'थोड्या वेळाने पुन्हा प्रयत्न करा.',
              'Try again in a little while.',
            ),
          );

    return Center(
      child: Padding(
        padding: const EdgeInsets.all(32),
        child: Column(
          mainAxisSize: MainAxisSize.min,
          children: [
            Container(
              width: 88,
              height: 88,
              decoration: const BoxDecoration(
                color: Palette.canvasSunk,
                shape: BoxShape.circle,
              ),
              child: Icon(icon, size: 40, color: Palette.inkMuted),
            ),
            const SizedBox(height: Space.xl),
            Text(title, textAlign: TextAlign.center, style: text.titleLarge),
            const SizedBox(height: Space.sm),
            Text(
              body,
              textAlign: TextAlign.center,
              style: text.bodyMedium?.copyWith(color: Palette.inkMuted),
            ),
            if (!notYet) ...[
              const SizedBox(height: Space.xl),
              OutlinedButton.icon(
                onPressed: onRetry,
                icon: const Icon(Icons.refresh, size: 20),
                label: Text(tr('पुन्हा प्रयत्न करा', 'Try again')),
              ),
            ],
          ],
        ),
      ),
    );
  }
}
