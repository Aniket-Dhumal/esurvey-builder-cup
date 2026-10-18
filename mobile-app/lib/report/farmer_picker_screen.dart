import 'dart:async';

import 'package:flutter/material.dart';

import '../core/db.dart';
import '../core/language.dart';
import '../core/voice.dart';
import '../ui/theme.dart';

/// Whose claim is being worked on.
///
/// # Why this screen exists
///
/// Sign-in proves a mobile number. It does not say which farmer that number
/// is acting for, and until now the app assumed the answer was one person for
/// ever. A household shares a handset; a farmer whose own AgriStack number is
/// long gone borrows a relative's; a son files for his mother. All of them had
/// nowhere to be.
///
/// So the number stays signed in -- across closing the app, across a reboot --
/// and this is what opens: the people this phone can act for, and a way to add
/// one more.
///
/// # Why the list is on the phone and nowhere else
///
/// It was decided on 2026-09-16 that the office holds no register of which
/// households share a handset. Each farmer was confirmed once against the
/// registry, and what came back is kept here beside them. A new phone starts
/// with an empty list and each farmer is added again -- the cost of the office
/// not keeping that map.
///
/// # Why no work is shown here
///
/// A farmer's claims, queue and photographs appear once they are picked, and
/// not before. The list is read by whoever is holding the phone, which is
/// often not the person whose claims they are.
class FarmerPickerScreen extends StatefulWidget {
  const FarmerPickerScreen({
    super.key,
    required this.store,
    required this.voice,
    required this.onPicked,
    required this.onAddFarmer,
    required this.onSignOut,
  });

  final Store store;
  final Voice voice;

  /// Called with the farmer id the app should work as.
  final void Function(String farmerId) onPicked;

  /// Open the screen that adds one, and come back when it has.
  final Future<void> Function() onAddFarmer;

  final VoidCallback onSignOut;

  @override
  State<FarmerPickerScreen> createState() => _FarmerPickerScreenState();

  /// A grant that has run out, or was never issued.
  ///
  /// The office will not take a claim for a farmer this handset cannot show a
  /// grant for. Working as them anyway would let somebody fill in a form,
  /// walk four points and photograph a field, and be refused on sending -- the
  /// failure this app keeps having to be saved from. So an expired farmer is
  /// confirmed again first, which costs one date of birth and a moment of
  /// signal.
  ///
  /// A farmer with no grant at all is the account's own, whose registered
  /// number signed in: the office needs nothing further for them.
  static bool needsConfirming(Map<String, Object?> farmer) {
    if (farmer['grant_token'] == null) return false;
    final expires = farmer['grant_expires_at'] as int?;
    if (expires == null) return false;
    return expires <= DateTime.now().millisecondsSinceEpoch;
  }
}

class _FarmerPickerScreenState extends State<FarmerPickerScreen> {
  List<Map<String, Object?>>? _farmers;

  @override
  void initState() {
    super.initState();
    unawaited(_load());
  }

  Future<void> _load() async {
    final farmers = await widget.store.linkedFarmers();
    if (!mounted) return;
    setState(() => _farmers = farmers);
  }

  /// Taking somebody off this phone.
  ///
  /// Refused while any of their work is still on it. An unsent claim is the
  /// farmer's only copy, and a list tidied at the wrong moment would destroy
  /// it silently -- the office has never heard of it.
  Future<void> _remove(Map<String, Object?> farmer) async {
    final id = '${farmer['farmer_id']}';
    // This farmer's own unsent work, not the handset's. Removing them takes
    // the grant that lets the office answer for them, so anything of theirs
    // still queued would stop being sendable.
    final unsent = await widget.store.outstanding(
      owner: Store.everyone,
      only: id,
    );
    if (!mounted) return;

    if (unsent.any) {
      ScaffoldMessenger.of(context).showSnackBar(
        SnackBar(
          content: Text(
            tr(
              'यांचे काम अजून कार्यालयाकडे गेलेले नाही. ते गेल्यावर काढता येईल.',
              'Their work has not reached the office yet. They can be removed '
                  'once it has.',
            ),
          ),
        ),
      );
      return;
    }

    await widget.store.unlinkFarmer(id);
    await _load();
  }

  Future<void> _add() async {
    await widget.onAddFarmer();
    if (mounted) await _load();
  }

  @override
  Widget build(BuildContext context) {
    final farmers = _farmers;
    final text = Theme.of(context).textTheme;

    return Scaffold(
      appBar: AppBar(
        title: Text(tr('शेतकरी निवडा', 'Choose farmer')),
        actions: [
          IconButton(
            icon: const Icon(Icons.logout),
            tooltip: tr('बाहेर पडा', 'Sign out'),
            onPressed: widget.onSignOut,
          ),
          const SizedBox(width: 4),
        ],
      ),
      body: farmers == null
          ? const Center(child: CircularProgressIndicator())
          : ListView(
              padding: Space.page,
              children: [
                if (farmers.isEmpty)
                  Padding(
                    padding: const EdgeInsets.symmetric(vertical: Space.xl),
                    child: Column(
                      children: [
                        Container(
                          width: 88,
                          height: 88,
                          decoration: const BoxDecoration(
                            color: Palette.primarySoft,
                            shape: BoxShape.circle,
                          ),
                          child: const Icon(
                            Icons.person_add_alt,
                            size: 40,
                            color: Palette.primary,
                          ),
                        ),
                        const SizedBox(height: Space.lg),
                        Text(
                          tr('शेतकरी जोडा', 'Add a farmer'),
                          style: text.titleLarge,
                        ),
                        const SizedBox(height: Space.xs),
                        Text(
                          tr(
                            'ज्यांच्यासाठी नोंद करायची आहे त्यांचा शेतकरी ओळख '
                                'क्रमांक आणि जन्मतारीख लागेल. स्वतःसाठी नोंद '
                                'करायची असेल तरी स्वतःला असेच जोडा.',
                            'You will need the Farmer ID and date of birth of '
                                'the person you are filing for. Add yourself '
                                'the same way if the claim is your own.',
                          ),
                          textAlign: TextAlign.center,
                          style: text.bodyMedium?.copyWith(
                            color: Palette.inkMuted,
                          ),
                        ),
                      ],
                    ),
                  )
                else ...[
                  Padding(
                    padding: const EdgeInsets.fromLTRB(4, 4, 4, Space.md),
                    child: Text(
                      tr(
                        'कोणासाठी काम करायचे आहे?',
                        'Who are you working for?',
                      ),
                      style: text.bodyMedium?.copyWith(color: Palette.inkMuted),
                    ),
                  ),
                  for (final f in farmers)
                    _FarmerCard(
                      farmer: f,
                      stale: FarmerPickerScreen.needsConfirming(f),
                      onPick: () => FarmerPickerScreen.needsConfirming(f)
                          ? _add()
                          : widget.onPicked('${f['farmer_id']}'),
                      onRemove: () => _remove(f),
                    ),
                ],

                const SizedBox(height: Space.sm),

                // Below the list, always. A household that files for three
                // people picks far more often than it adds, and a farmer who
                // has just signed in needs this to be the obvious next step.
                if (farmers.isEmpty)
                  BigButton(
                    label: tr('शेतकरी जोडा', 'Add farmer'),
                    icon: Icons.add,
                    onPressed: _add,
                  )
                else
                  OutlinedButton.icon(
                    onPressed: _add,
                    icon: const Icon(Icons.add, size: 20),
                    label: Text(tr('आणखी शेतकरी जोडा', 'Add another farmer')),
                  ),

                const SizedBox(height: Space.xl),
                Row(
                  children: [
                    const Icon(
                      Icons.lock_outline,
                      size: 16,
                      color: Palette.inkMuted,
                    ),
                    const SizedBox(width: Space.sm),
                    Expanded(
                      child: Text(
                        tr(
                          'ही यादी फक्त या फोनवर राहते.',
                          'This list stays on this phone only.',
                        ),
                        style: text.labelMedium,
                      ),
                    ),
                  ],
                ),
              ],
            ),
    );
  }
}

class _FarmerCard extends StatelessWidget {
  const _FarmerCard({
    required this.farmer,
    required this.stale,
    required this.onPick,
    required this.onRemove,
  });

  final Map<String, Object?> farmer;

  /// Their confirmation has run out, so the office would refuse a claim for
  /// them. Said on the card rather than discovered at the end of a walk.
  final bool stale;
  final VoidCallback onPick;
  final VoidCallback onRemove;

  @override
  Widget build(BuildContext context) {
    final text = Theme.of(context).textTheme;
    final name = '${farmer['name'] ?? ''}';
    final id = '${farmer['farmer_id'] ?? ''}';
    final initial = name.isEmpty ? '#' : name.characters.first;

    return AppCard(
      onTap: onPick,
      padding: const EdgeInsets.fromLTRB(
        Space.lg,
        Space.md,
        Space.xs,
        Space.md,
      ),
      child: Row(
        children: [
          CircleAvatar(
            radius: 22,
            backgroundColor: Palette.primarySoft,
            foregroundColor: Palette.primaryDark,
            child: Text(
              initial,
              style: const TextStyle(fontSize: 18, fontWeight: FontWeight.w600),
            ),
          ),
          const SizedBox(width: Space.md),
          Expanded(
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                Text(name.isEmpty ? id : name, style: text.titleMedium),
                Text(tr('ओळख क्र. $id', 'ID $id'), style: text.bodySmall),
                if (stale) ...[
                  const SizedBox(height: Space.xs),
                  StatusPill.moderate(
                    tr(
                      'जन्मतारखेने पुन्हा खात्री करा',
                      'Confirm again with DOB',
                    ),
                    icon: Icons.refresh,
                  ),
                ],
              ],
            ),
          ),
          PopupMenuButton<String>(
            tooltip: tr('अधिक', 'More'),
            icon: const Icon(Icons.more_vert, color: Palette.inkMuted),
            onSelected: (_) => onRemove(),
            itemBuilder: (_) => [
              PopupMenuItem(
                value: 'remove',
                child: Text(tr('या फोनवरून काढा', 'Remove from this phone')),
              ),
            ],
          ),
        ],
      ),
    );
  }
}
