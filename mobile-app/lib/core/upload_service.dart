import 'dart:async';

import 'package:flutter/foundation.dart';
import 'package:flutter_foreground_task/flutter_foreground_task.dart';

import 'background.dart';
import 'db.dart';

/// Keeps an upload running while the phone is in a pocket.
///
/// # The defect this exists for
///
/// Uploads ran off a thirty-second timer in the app, which Android suspends the
/// moment the screen sleeps, with WorkManager waking roughly every fifteen
/// minutes behind it. A farmer who started an upload and put their phone away
/// had stopped it: the trial lost most of an hour that way, and the farmer had
/// no way to know, because from their side the app had said the recordings were
/// on their way.
///
/// An Android foreground service is the only thing that keeps a long upload
/// alive across a locked screen, and the price is a notification the farmer
/// cannot dismiss while it runs. That is the right trade for a claim: a
/// notification is mildly annoying and a stranded claim is a lost payment.
///
/// # What it does not change
///
/// It does not replace WorkManager. The periodic and due schedules still exist
/// and still do the work when the app is not running at all; this only covers
/// the case the timer used to -- an upload in progress, on a phone whose owner
/// has stopped looking at it.
///
/// Nor does it risk sending anything twice. The pass it runs takes the same
/// device-wide lease every other pass takes, which lives in SQLite precisely
/// because the isolates cannot see each other's memory -- see
/// [Store.claimSyncLease]. A third isolate is no more dangerous than the second.
class UploadService {
  UploadService._();

  /// The notification channel, which the farmer can mute from settings if the
  /// notification bothers them. Muting it does not stop the upload.
  static const _channelId = 'esurvey_upload';

  static bool _initialised = false;

  /// Prepare the service. Safe to call more than once.
  static void prepare() {
    if (_initialised) return;
    _initialised = true;

    FlutterForegroundTask.init(
      androidNotificationOptions: AndroidNotificationOptions(
        channelId: _channelId,
        channelName: 'Sending recordings',
        channelDescription:
            'Shown while your recordings are being sent to the office.',
        // Low, deliberately. A farmer walking home from their field should not
        // have their phone make a noise every time a chunk lands.
        channelImportance: NotificationChannelImportance.LOW,
        priority: NotificationPriority.LOW,
        onlyAlertOnce: true,
      ),
      iosNotificationOptions: const IOSNotificationOptions(
        showNotification: true,
        playSound: false,
      ),
      foregroundTaskOptions: ForegroundTaskOptions(
        // The handler drives its own loop, so no repeat event is wanted: a
        // second pass starting while the first is uploading would only fail to
        // take the lease and waste a wake-up.
        eventAction: ForegroundTaskEventAction.nothing(),
        autoRunOnBoot: false,
        allowWakeLock: true,
        allowWifiLock: true,
      ),
    );
  }

  /// Start sending, with a notification, if there is anything to send.
  ///
  /// Returns whether the service is now running. A refusal is not an error worth
  /// showing anybody: the queue is still there, WorkManager still has it, and
  /// the farmer has nothing to do about a notification permission they declined.
  static Future<bool> start({
    required int items,
    bool Function()? supported,
  }) async {
    if (!(supported?.call() ??
        defaultTargetPlatform == TargetPlatform.android)) {
      return false;
    }
    if (items <= 0) return false;

    prepare();

    if (await FlutterForegroundTask.isRunningService) {
      // Already sending. Update the count rather than restart, which would
      // interrupt an upload to say the same thing.
      await update(items: items, sent: 0);
      return true;
    }

    try {
      final result = await FlutterForegroundTask.startService(
        serviceTypes: const [ForegroundServiceTypes.dataSync],
        notificationTitle: _title(items, 0),
        notificationText: _text(items, 0),
        callback: uploadServiceEntryPoint,
      );
      return result is ServiceRequestSuccess;
    } catch (e) {
      // Notifications refused, or the platform said no. The queue is
      // unaffected and WorkManager will still drain it.
      debugPrint('upload service could not start: $e');
      return false;
    }
  }

  /// Revise the notification as clips land.
  static Future<void> update({required int items, required int sent}) async {
    try {
      if (!await FlutterForegroundTask.isRunningService) return;
      await FlutterForegroundTask.updateService(
        notificationTitle: _title(items, sent),
        notificationText: _text(items, sent),
      );
    } catch (e) {
      debugPrint('upload service could not update its notification: $e');
    }
  }

  /// Stop sending and take the notification away.
  ///
  /// Called when the queue empties, and from the handler itself when its pass
  /// finishes: a notification still saying "sending" over an empty queue is a
  /// lie the farmer has no way to check.
  static Future<void> stop() async {
    try {
      if (!await FlutterForegroundTask.isRunningService) return;
      await FlutterForegroundTask.stopService();
    } catch (e) {
      debugPrint('upload service could not stop: $e');
    }
  }

  static String _title(int items, int sent) =>
      'eSurvey · नोंदी पाठवत आहे / Sending your recordings';

  static String _text(int items, int sent) {
    if (items <= 0) return 'पूर्ण झाले / Done';
    final done = sent.clamp(0, items);
    return '$done / $items पाठवले · sent';
  }
}

/// The isolate the service runs the upload in.
///
/// Top-level and annotated: the tree shaker cannot see the platform calling it
/// and would drop it from the release build, leaving a service that works in
/// debug and does nothing in the build that ships. The same hazard as
/// [backgroundEntryPoint], and it has bitten this app's background sync before.
@pragma('vm:entry-point')
void uploadServiceEntryPoint() {
  FlutterForegroundTask.setTaskHandler(_UploadTaskHandler());
}

class _UploadTaskHandler extends TaskHandler {
  @override
  Future<void> onStart(DateTime timestamp, TaskStarter starter) async {
    // One pass, then stop. The pass has its own budget and its own retry
    // schedule; holding the service open past it would keep a notification up
    // and a wake lock held over a queue that is waiting on a backoff.
    try {
      await drainInServiceIsolate();
    } catch (e) {
      debugPrint('upload service pass failed: $e');
    } finally {
      await FlutterForegroundTask.stopService();
    }
  }

  @override
  void onRepeatEvent(DateTime timestamp) {
    // Not used: eventAction is nothing(). onStart drives the pass.
  }

  @override
  Future<void> onDestroy(DateTime timestamp, bool isTimeout) async {
    if (isTimeout) {
      // Android took the service away. Whatever was sent is acknowledged and
      // the rest is still queued, so the next WorkManager wake picks it up.
      debugPrint('upload service was stopped by the platform on timeout');
    }
  }
}
