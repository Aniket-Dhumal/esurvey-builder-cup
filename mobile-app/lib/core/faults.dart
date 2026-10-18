import 'dart:async';

import 'package:flutter/foundation.dart';

import 'db.dart';

/// Errors nobody was going to hear about.
///
/// # Why this exists
///
/// The app had no error handler of any kind: no `FlutterError.onError`, no
/// `PlatformDispatcher.instance.onError`, no guarded zone. It also has ninety-odd
/// `unawaited(...)` calls, which is the right pattern for fire-and-forget work
/// and means ninety-odd places where a throw goes nowhere.
///
/// In a farmer's hands those two facts combine badly. An error in some unrelated
/// future — a voice engine that is not installed, a notification channel that
/// will not open — either killed the app or vanished. Killing it is the worse
/// one: a farmer three points into a walk loses the walk, and the only record of
/// what went wrong is a phone with no signal in a village.
///
/// # What it does
///
/// Two things, and deliberately not more.
///
/// It keeps the app alive. An unhandled error in a future the app was not
/// waiting on must not take down a walk in progress, so the handler reports the
/// error as handled. That is not a way of hiding bugs: every one is written down.
///
/// It writes the fault to the phone, where it survives a restart and can be read
/// back when the handset next reaches somebody who can look. A farmer cannot
/// report a stack trace and should not be asked to. The trial was diagnosed
/// through `adb logcat` over a cable, which needs the phone in your hand; a
/// stored fault needs only the phone to come back.
///
/// # What it is not
///
/// Not crash reporting. Nothing is sent anywhere: the faults sit in the app's own
/// database until somebody looks, because a crash reporter on a government
/// application handling identified farmers' land would be a data-sharing decision
/// and not a robustness one.
///
/// Not a substitute for handling errors where they happen. Every `catch` already
/// in this app stays. This is the floor under them.
class Faults {
  Faults._();

  /// How many are kept. The newest are the useful ones, and an unbounded table
  /// on a 2 GB handset is its own defect.
  static const keep = 50;

  static Store? _store;
  static bool _installed = false;

  /// Begin catching. Call once, before `runApp`.
  ///
  /// [store] may be null at that point -- the database opens after this -- and
  /// is attached later by [attach]. Faults raised in between are logged but not
  /// stored, which is the correct trade: the alternative is holding startup for
  /// a database in order to record errors that have not happened yet.
  static void install({Store? store}) {
    _store = store;
    if (_installed) return;
    _installed = true;

    // Framework errors: a build that threw, a layout that overflowed.
    final previous = FlutterError.onError;
    FlutterError.onError = (details) {
      // The console first, unchanged, so a developer sees what they always saw.
      previous?.call(details);
      _record(
        'flutter',
        details.exception,
        details.stack,
        library: details.library,
        context: details.context?.toString(),
      );
    };

    // Everything else: an error in a future nobody awaited, which is the case
    // this app has ninety of.
    PlatformDispatcher.instance.onError = (error, stack) {
      _record('async', error, stack);
      // Handled. A farmer mid-walk must not lose it to a throw in an unrelated
      // future, and the fault is written down rather than swallowed.
      return true;
    };
  }

  /// Give the fault log somewhere to write, once the database is open.
  static void attach(Store store) => _store = store;

  /// Record a fault the app caught itself but could not act on.
  ///
  /// For the places that already catch and `debugPrint`. Those messages were
  /// only ever visible over a cable.
  static void note(String where, Object error, [StackTrace? stack]) =>
      _record(where, error, stack);

  static void _record(
    String where,
    Object error,
    StackTrace? stack, {
    String? library,
    String? context,
  }) {
    // Never throws. A fault handler that fails has nowhere to report it and
    // would turn one error into a loop.
    try {
      final line = StringBuffer()
        ..write(error.toString())
        ..write(library == null ? '' : '  [$library]')
        ..write(context == null ? '' : '  $context');

      debugPrint('[fault:$where] $line');

      final store = _store;
      if (store == null) return;

      // Trimmed hard. A full Dart stack is several kilobytes and the useful
      // part is the top: the frames below it are the framework's own.
      final trace = stack?.toString().split('\n').take(12).join('\n') ?? '';

      unawaited(
        store
            .recordFault(where: where, message: line.toString(), stack: trace)
            .catchError((Object e) {
              debugPrint('[fault] could not be written down: $e');
            }),
      );
    } catch (e) {
      debugPrint('[fault] handler itself failed: $e');
    }
  }
}
