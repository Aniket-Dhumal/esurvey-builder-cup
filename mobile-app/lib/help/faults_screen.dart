import 'dart:async';

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';

import '../core/db.dart';
import '../core/language.dart';
import '../ui/theme.dart';

/// What has gone wrong on this phone, for somebody who can read it.
///
/// # Why a screen, and why a plain one
///
/// The fault log is worth nothing if the only way to read it is a cable. The
/// trial was diagnosed through `adb logcat`, which needs the handset in your hand
/// and the farmer's day; a screen needs a phone call.
///
/// It is deliberately not designed for the farmer. It is English-labelled where
/// the text is technical, it is reached from Help rather than shown, and it says
/// up front that the farmer has done nothing wrong. Dressing a stack trace up in
/// Marathi would make it look like something they were meant to act on.
///
/// Nothing is sent anywhere. A crash reporter on a government application
/// handling identified farmers' land records would be a data-sharing decision,
/// not a robustness one. Copying to the clipboard is as far as this goes, and the
/// person holding the phone chooses it.
class FaultsScreen extends StatefulWidget {
  const FaultsScreen({super.key, required this.store});

  final Store store;

  @override
  State<FaultsScreen> createState() => _FaultsScreenState();
}

class _FaultsScreenState extends State<FaultsScreen> {
  List<Map<String, Object?>>? _rows;

  @override
  void initState() {
    super.initState();
    unawaited(_load());
  }

  Future<void> _load() async {
    final rows = await widget.store.faults();
    if (!mounted) return;
    setState(() => _rows = rows);
  }

  String _asText() {
    final rows = _rows ?? const [];
    return rows
        .map(
          (r) =>
              '${_when(r['at'])}  [${r['where_at']}]\n'
              '${r['message']}\n${r['stack']}\n',
        )
        .join('\n');
  }

  static String _when(Object? at) {
    if (at is! int) return '';
    return DateTime.fromMillisecondsSinceEpoch(at).toLocal().toString();
  }

  @override
  Widget build(BuildContext context) {
    final rows = _rows;
    final text = Theme.of(context).textTheme;

    return Scaffold(
      appBar: AppBar(
        title: Text(tr('तांत्रिक नोंदी', 'Technical faults')),
        actions: [
          if (rows != null && rows.isNotEmpty)
            IconButton(
              tooltip: tr('कॉपी करा', 'Copy'),
              icon: const Icon(Icons.copy_all_outlined),
              onPressed: () async {
                await Clipboard.setData(ClipboardData(text: _asText()));
                if (!context.mounted) return;
                ScaffoldMessenger.of(context).showSnackBar(
                  SnackBar(content: Text(tr('कॉपी झाले', 'Copied'))),
                );
              },
            ),
        ],
      ),
      body: rows == null
          ? const Center(child: CircularProgressIndicator())
          : ListView(
              padding: Space.page,
              children: [
                Padding(
                  padding: const EdgeInsets.all(4),
                  child: Text(
                    tr(
                      'ॲपमध्ये काही अडचण आल्यास ती येथे नोंदवली जाते. '
                          'तुमच्याकडून काही चूक झालेली नाही, आणि तुमचा दावा '
                          'यामुळे बाधित होत नाही. ही माहिती फक्त या फोनवर राहते '
                          'आणि कुठेही पाठवली जात नाही.',
                      'When something goes wrong inside the app it is written '
                          'down here. You have done nothing wrong and your claim '
                          'is not affected. This stays on this phone and is not '
                          'sent anywhere.',
                    ),
                    style: text.bodyMedium?.copyWith(color: Palette.inkMuted),
                  ),
                ),
                const SizedBox(height: 12),
                if (rows.isEmpty)
                  Card(
                    child: Padding(
                      padding: const EdgeInsets.all(16),
                      child: Text(
                        tr(
                          'काहीही अडचण नोंदवलेली नाही.',
                          'Nothing has gone wrong.',
                        ),
                      ),
                    ),
                  )
                else
                  for (final r in rows) _FaultCard(row: r),
              ],
            ),
    );
  }
}

class _FaultCard extends StatelessWidget {
  const _FaultCard({required this.row});

  final Map<String, Object?> row;

  @override
  Widget build(BuildContext context) {
    final text = Theme.of(context).textTheme;
    final stack = '${row['stack'] ?? ''}';

    return Card(
      margin: const EdgeInsets.only(bottom: 10),
      child: Padding(
        padding: const EdgeInsets.all(12),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Row(
              children: [
                Chip(
                  label: Text('${row['where_at']}'),
                  visualDensity: VisualDensity.compact,
                ),
                const SizedBox(width: 8),
                Expanded(
                  child: Text(
                    _FaultsScreenState._when(row['at']),
                    style: text.bodySmall?.copyWith(color: Palette.inkMuted),
                  ),
                ),
              ],
            ),
            const SizedBox(height: 6),
            Text('${row['message']}', style: text.bodyMedium),
            if (stack.isNotEmpty) ...[
              const SizedBox(height: 6),
              // Scrollable rather than wrapped: a stack frame is long, and
              // wrapping it turns four frames into a wall.
              SingleChildScrollView(
                scrollDirection: Axis.horizontal,
                child: Text(
                  stack,
                  style: text.bodySmall?.copyWith(
                    fontFamily: 'monospace',
                    color: Palette.inkMuted,
                  ),
                ),
              ),
            ],
          ],
        ),
      ),
    );
  }
}
