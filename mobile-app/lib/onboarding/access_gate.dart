import 'dart:async';

import 'package:flutter/material.dart';

import '../core/access.dart';
import '../core/db.dart';
import '../core/language.dart';
import '../core/voice.dart';
import 'access_screen.dart';

/// Nothing of a walk opens until the app is allowed to record and to place the
/// recording.
///
/// # Where this sits
///
/// Outside [FieldGate], which blocks until the farmer is standing at their farm.
/// That order is forced: FieldGate cannot know where the farmer is without the
/// location permission this gate is about.
///
/// # Why the first run is different
///
/// On a phone that has already granted everything this gate is invisible: asking
/// for a permission already held opens no prompt, so the check is silent and the
/// walk opens straight away.
///
/// The first time is the one that matters. If the gate simply asked, the system
/// prompts would appear before the farmer had been told what they were for --
/// "Allow eSurvey to take pictures and record video?" out of nowhere, to somebody
/// handed a phone by an office. So the first run shows the explaining screen and
/// the prompts follow their decision to go on. After that, the explanation is not
/// repeated: a farmer who has granted everything should not be made to read it
/// before each walk.
class AccessGate extends StatefulWidget {
  const AccessGate({
    super.key,
    required this.store,
    required this.voice,
    required this.child,
    this.check,
  });

  final Store store;
  final Voice voice;

  /// The walk, opened once recording is possible.
  final Widget child;

  /// Injected for tests.
  final AccessCheck? check;

  @override
  State<AccessGate> createState() => _AccessGateState();
}

class _AccessGateState extends State<AccessGate> {
  /// Set once the farmer has read what the permissions are for, so the
  /// explanation is shown once rather than before every walk.
  static const _explainedKey = 'access_explained';

  late final AccessCheck _access = widget.check ?? AccessCheck();

  bool _checking = true;
  bool _ready = false;

  @override
  void initState() {
    super.initState();
    unawaited(_gate());
  }

  Future<void> _gate() async {
    final explained = await widget.store.deviceValue(_explainedKey);
    if (explained == null) {
      // First walk on this phone. Explain, then ask.
      if (!mounted) return;
      setState(() => _checking = false);
      return;
    }

    // Everything already granted asks nothing and opens no prompt, so this is
    // silent on the ordinary path. Where something has been revoked since --
    // and a farmer can revoke from Settings between walks -- the prompts return
    // and the screen explains whatever is still missing.
    final report = await _access.request();
    if (!mounted) return;
    setState(() {
      _checking = false;
      _ready = report.canWalk;
    });
  }

  Future<void> _onReady() async {
    await widget.store.setDeviceValue(_explainedKey, 'yes');
    if (!mounted) return;
    setState(() => _ready = true);
  }

  @override
  Widget build(BuildContext context) {
    if (_checking) {
      return Scaffold(
        appBar: AppBar(title: Text(tr('शेत पाहणी', 'Field walk'))),
        body: const Center(child: CircularProgressIndicator()),
      );
    }
    if (_ready) return widget.child;

    return AccessScreen(
      voice: widget.voice,
      check: _access,
      onReady: () => unawaited(_onReady()),
      // No skip: this gate is in front of a walk, and a walk without a camera
      // is not a thing the farmer can usefully be let into. The back button is
      // still there, which is how they leave.
    );
  }
}
