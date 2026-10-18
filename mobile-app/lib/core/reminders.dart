import 'language.dart';
import 'package:flutter/foundation.dart';
import 'package:flutter_local_notifications/flutter_local_notifications.dart';

import 'db.dart';

/// Reminding a farmer about a claim that never left the phone.
///
/// # The failure this exists to prevent
///
/// A claim is not sent until its evidence is complete -- decided on
/// 2026-09-14, and the reason the form no longer queues the moment it is
/// filled (docs/EVIDENCE_REVALIDATION.md, V4). A farmer interrupted between
/// the form and the walk therefore has a whole claim on the phone that the
/// office will never hear about.
///
/// Before this, the only thing that would tell them was opening the app. The
/// background worker exists precisely because that does not happen: a farmer
/// files once a year and has no reason to reopen it. So an unfinished claim
/// sat on the handset until the season ended, with the farmer believing they
/// had filed.
///
/// # Why the phone reminds itself
///
/// Status updates are in-app only, decided on 2026-09-14. A local
/// notification does not break that: nothing leaves the handset, no server is
/// told, no SMS is sent, and it works in a village with no signal at all --
/// which is exactly where an unfinished claim is most likely to be sitting.
///
/// # Why it says so little
///
/// The text names no crop, no survey number and no figure. A notification is
/// read on a lock screen, and a lock screen is visible to whoever is holding
/// the phone -- which on a shared handset is often not the farmer whose claim
/// it is. It says something is unfinished and to open the app.
class Reminders {
  Reminders({FlutterLocalNotificationsPlugin? plugin})
    : _plugin = plugin ?? FlutterLocalNotificationsPlugin();

  final FlutterLocalNotificationsPlugin _plugin;

  /// How long a claim may sit unfinished before the phone says anything.
  ///
  /// A farmer who walks back to the house for a charger and returns in twenty
  /// minutes does not need telling. A day means they have slept on it.
  static const after = Duration(hours: 24);

  /// One notification, replaced rather than stacked: a farmer with two
  /// unfinished claims has one problem, which is that they have not opened
  /// the app.
  static const _id = 1;

  static const _channelId = 'unfinished';

  bool _ready = false;

  /// Prepare the plugin. Safe to call more than once.
  Future<void> init() async {
    if (_ready) return;
    try {
      await _plugin.initialize(
        const InitializationSettings(
          // The launcher icon, rather than an asset of its own: an icon that
          // does not exist makes Android drop the notification silently.
          android: AndroidInitializationSettings('@mipmap/ic_launcher'),
        ),
      );
      _ready = true;
    } catch (e) {
      // A phone that will not give us notifications is not a phone that
      // should stop working. The claim is still on it, still safe, still on
      // the home screen when the app is opened.
      debugPrint('[reminders] could not initialise: $e');
    }
  }

  /// Ask for permission, at the moment it first means something.
  ///
  /// Called when a claim is first held rather than at startup: a farmer who
  /// is asked before they have done anything has no way to judge the request
  /// and will refuse it.
  /// Whether this phone will actually show a notification.
  ///
  /// Asked rather than assumed. A farmer who declined the prompt, or who muted
  /// the channel afterwards, gets no reminder and no upload progress -- and the
  /// app should know that rather than believe it is telling them things.
  ///
  /// Treated as permitted where the platform cannot say. Reporting a working
  /// notification as broken would send the farmer to Settings for nothing.
  Future<bool> permitted() async {
    await init();
    try {
      final android = _plugin
          .resolvePlatformSpecificImplementation<
            AndroidFlutterLocalNotificationsPlugin
          >();
      if (android == null) return true;
      return await android.areNotificationsEnabled() ?? true;
    } catch (e) {
      debugPrint('[reminders] permission state unknown: $e');
      return true;
    }
  }

  Future<void> requestPermission() async {
    await init();
    try {
      await _plugin
          .resolvePlatformSpecificImplementation<
            AndroidFlutterLocalNotificationsPlugin
          >()
          ?.requestNotificationsPermission();
    } catch (e) {
      debugPrint('[reminders] permission request failed: $e');
    }
  }

  /// Say that something is unfinished. [count] claims are waiting.
  Future<void> showUnfinished(int count) async {
    await init();
    if (!_ready) return;

    final title = count == 1
        ? tr(
            'तुमची एक नोंद अजून पाठवलेली नाही',
            'One of your claims has not been sent',
          )
        : tr(
            'तुमच्या $count नोंदी अजून पाठवलेल्या नाहीत',
            '$count of your claims have not been sent',
          );
    final body = tr(
      'पुरावा अपूर्ण असल्याने ती फोनवरच आहे. ॲप उघडून पूर्ण करा.',
      'Evidence is incomplete, so it is still on this phone. Open the app to '
          'finish it.',
    );

    try {
      await _plugin.show(
        _id,
        title,
        body,
        NotificationDetails(
          android: AndroidNotificationDetails(
            _channelId,
            tr('अपूर्ण नोंदी', 'Unfinished claims'),
            channelDescription:
                'A claim that is filled in but has not been sent, because its '
                'evidence is not complete.',
            importance: Importance.defaultImportance,
            priority: Priority.defaultPriority,
          ),
        ),
      );
    } catch (e) {
      debugPrint('[reminders] could not show: $e');
    }
  }

  /// Take it back, because there is nothing left unfinished.
  Future<void> clear() async {
    await init();
    if (!_ready) return;
    try {
      await _plugin.cancel(_id);
    } catch (e) {
      debugPrint('[reminders] could not clear: $e');
    }
  }
}

/// Decide whether to remind, and do it. Runs in the background isolate.
///
/// Returns true when something was said, so the caller can log it. Held
/// claims are counted across every farmer on the handset: a borrowed phone
/// may be carrying somebody else's unfinished claim, and they are the person
/// least able to find out any other way.
Future<bool> remindAboutUnfinished(Store store, Reminders reminders) async {
  final held = await store.heldSince(Reminders.after, owner: Store.everyone);
  if (held == 0) {
    await reminders.clear();
    return false;
  }
  await reminders.showUnfinished(held);
  return true;
}
