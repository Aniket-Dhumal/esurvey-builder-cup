import 'dart:async';

import 'package:flutter/material.dart';

import '../core/db.dart';
import '../core/farmer_data.dart';
import '../core/language.dart';
import '../ui/theme.dart';

/// Starting a claim for the farmer this phone is working for.
///
/// # Why it does not ask for the Farmer ID
///
/// It used to. The farmer had already been picked -- confirmed against the
/// registry with their ID and date of birth -- and the tab then asked for the
/// same ID again, as if the phone had forgotten who it was working for. The
/// record is on the phone; this starts from it.
class NewReportTab extends StatefulWidget {
  const NewReportTab({
    super.key,
    required this.data,
    required this.store,
    required this.onStart,
    required this.onSwitchFarmer,
  });

  final FarmerData data;
  final Store store;

  /// Opens the claim for this record: the calamity, then the form.
  final Future<void> Function(Map<String, Object?> record) onStart;

  final VoidCallback onSwitchFarmer;

  @override
  State<NewReportTab> createState() => _NewReportTabState();
}

class _NewReportTabState extends State<NewReportTab> {
  Map<String, dynamic>? _record;
  bool _loading = true;
  bool _starting = false;

  @override
  void initState() {
    super.initState();
    unawaited(_load());
  }

  Future<void> _load() async {
    final id = widget.store.farmer;
    if (id == null) {
      setState(() => _loading = false);
      return;
    }
    final cached = await widget.data.record(id, refresh: false);
    if (!mounted) return;
    setState(() {
      _record = cached.value;
      _loading = cached.value == null;
    });

    final grant = await widget.store.grantFor(id);
    final fresh = await widget.data.record(id, grant: grant);
    if (!mounted) return;
    setState(() {
      _record = fresh.value ?? _record;
      _loading = false;
    });
  }

  Future<void> _start() async {
    final record = _record;
    if (record == null || _starting) return;
    setState(() => _starting = true);
    try {
      await widget.onStart(record.cast<String, Object?>());
    } finally {
      if (mounted) setState(() => _starting = false);
    }
  }

  @override
  Widget build(BuildContext context) {
    final text = Theme.of(context).textTheme;
    final record = _record;

    return Scaffold(
      appBar: AppBar(title: Text(tr('नवीन नोंद', 'New report'))),
      body: RefreshIndicator(
        onRefresh: _load,
        child: ListView(
          padding: Space.page,
          children: [
            if (_loading)
              const Padding(
                padding: EdgeInsets.only(top: 48),
                child: Center(child: CircularProgressIndicator()),
              )
            else if (record == null)
              EmptyState(
                icon: Icons.cloud_off_outlined,
                message: tr(
                  'या शेतकऱ्याची माहिती फोनवर नाही. नेटवर्क आल्यावर खाली ओढून '
                      'पुन्हा प्रयत्न करा.',
                  'This farmer’s record is not on the phone. Pull down to try '
                      'again when there is a signal.',
                ),
              )
            else ...[
              AppCard(
                child: Row(
                  children: [
                    const IconBadge(Icons.person_outline),
                    const SizedBox(width: Space.md),
                    Expanded(
                      child: Column(
                        crossAxisAlignment: CrossAxisAlignment.start,
                        children: [
                          Text(
                            '${record['farmer_name'] ?? ''}',
                            style: text.titleMedium,
                          ),
                          Text(
                            tr(
                              'ओळख क्र. ${record['farmer_id'] ?? ''}',
                              'ID ${record['farmer_id'] ?? ''}',
                            ),
                            style: text.bodySmall,
                          ),
                        ],
                      ),
                    ),
                    TextButton(
                      onPressed: widget.onSwitchFarmer,
                      child: Text(tr('बदला', 'Change')),
                    ),
                  ],
                ),
              ),
              SectionHeader(tr('नोंद कशी करायची', 'How it works')),
              AppCard(
                child: Column(
                  children: [
                    _Step(
                      n: 1,
                      title: tr(
                        'आपत्ती व शेत निवडा',
                        'Choose calamity and field',
                      ),
                    ),
                    _Step(
                      n: 2,
                      title: tr(
                        'नुकसानाची थोडक्यात माहिती भरा',
                        'Fill in a few details about the loss',
                      ),
                    ),
                    _Step(
                      n: 3,
                      title: tr(
                        'शेतात जाऊन फोटो व व्हिडिओ घ्या',
                        'Take photos and video in the field',
                      ),
                    ),
                    _Step(
                      n: 4,
                      title: tr('तपासून पाठवा', 'Check and send'),
                      last: true,
                    ),
                  ],
                ),
              ),
              const SizedBox(height: Space.sm),
              BigButton(
                label: _starting
                    ? tr('उघडत आहे…', 'Opening…')
                    : tr('नोंद सुरू करा', 'Start report'),
                icon: Icons.arrow_forward,
                onPressed: _starting ? null : _start,
              ),
            ],
          ],
        ),
      ),
    );
  }
}

class _Step extends StatelessWidget {
  const _Step({required this.n, required this.title, this.last = false});

  final int n;
  final String title;
  final bool last;

  @override
  Widget build(BuildContext context) {
    return Padding(
      padding: EdgeInsets.only(bottom: last ? 0 : Space.md),
      child: Row(
        children: [
          CircleAvatar(
            radius: 14,
            backgroundColor: Palette.primarySoft,
            foregroundColor: Palette.primaryDark,
            child: Text(
              '$n',
              style: const TextStyle(fontSize: 13, fontWeight: FontWeight.w600),
            ),
          ),
          const SizedBox(width: Space.md),
          Expanded(
            child: Text(title, style: Theme.of(context).textTheme.bodyMedium),
          ),
        ],
      ),
    );
  }
}
