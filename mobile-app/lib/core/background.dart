import 'package:flutter/foundation.dart';
import 'package:workmanager/workmanager.dart';

import 'accounts.dart';
import 'api.dart';
import 'db.dart';
import 'reminders.dart';
import 'session.dart';
import 'storage.dart';
import 'sync.dart';

/// Sending the queue when the app is not open.
///
/// # The failure this exists to prevent
///
/// A farmer walks their boundary, files a report and photographs the damage,
/// standing in a field with no signal. All of it is on the phone and none of
/// it has moved. They put the phone in their pocket and go home.
///
/// With only a foreground timer, nothing happens next. The queue drains when
/// the app is reopened -- and the whole point of this app is that a farmer
/// files once a year and has no reason to reopen it. The evidence sits on the
/// handset until they do, or until the phone is lost, or until the claim
/// window closes. The report was filed as far as the farmer is concerned, and
/// the office never received it.
///
/// So the upload has to be able to happen without them. Android's WorkManager
/// survives the app being closed and the phone being rebooted, and will not
/// run the work until there is a network to run it over.
///
/// # What runs where
///
/// The handler below runs in a **separate isolate** with no access to
/// anything the app holds in memory. That is why [Sync] takes its store and
/// api as arguments rather than reaching for globals: the background isolate
/// builds its own of each, over the same database file and the same keystore,
/// and the sync engine cannot tell the difference.
///
/// The one thing the two isolates must agree on is that only one of them
/// syncs at a time, and they agree through the database -- see
/// [Store.claimSyncLease].

/// The task name the handler dispatches on. One task; the two schedules below
/// differ in when they run, not in what they do.
const _drainTask = 'drain';

/// Reminding the farmer about a claim that never left the phone. A separate
/// task because it is the opposite of a drain: it needs no network, and the
/// phone it has to wake is precisely the one that has not had a signal.
const _remindTask = 'remind';

const _periodicName = 'esurvey-drain-periodic';
const _urgentName = 'esurvey-drain-now';
const _dueName = 'esurvey-drain-due';
const _remindName = 'esurvey-remind-unfinished';

/// The entry point Android calls into. Top-level and annotated because the
/// tree shaker cannot see the native side calling it and would otherwise
/// remove it from the release build -- silently, leaving background sync that
/// works in debug and does nothing in the build that ships.
@pragma('vm:entry-point')
void backgroundEntryPoint() {
  Workmanager().executeTask((task, input) async {
    if (task == _remindTask) return _remind();
    if (task != _drainTask) return true;
    return _drain();
  });
}

/// One pass, for the foreground upload service's isolate.
///
/// The same function the WorkManager worker runs. Deliberately the same: two
/// implementations of "drain the queue" would be two things to keep correct, and
/// the second one would be the one nobody tested. The device-wide lease makes it
/// safe for either isolate to call.
Future<bool> drainInServiceIsolate() => _drain();

/// One pass, in the background isolate.
///
/// The bool is the whole contract with Android: true means done, false means
/// retry me later with backoff. Getting it backwards either gives up on a
/// farmer's evidence or wakes the phone forever.
Future<bool> _drain() async {
  Store? store;
  Sync? sync;

  try {
    final session = Session();
    await session.load();

    // Its own connection, so closing it cannot close the app's. See
    // Store.open.
    store = await Store.open(ownConnection: true);
    store.owner = session.ownerId;

    // Checked before anything touches the network, because this is the
    // ordinary case: the periodic schedule fires every fifteen minutes for
    // the rest of the app's life and almost every one of those finds an empty
    // queue. A SQLite count is microseconds; opening an HTTP client and
    // refreshing a token to discover the same thing is not.
    //
    // Across every farmer: one who signed out with claims unsent still has
    // work here, sent under their own parked credentials.
    if (!(await store.outstanding(
      owner: Store.everyone,
      only: Store.everyone,
    )).any) {
      return true;
    }

    final queue = store;
    sync = Sync(
      store: queue,
      // Android stops a worker at about ten minutes; stopping first releases
      // the lease cleanly.
      budget: const Duration(minutes: 8),
      api: Api(session: session),
      accounts: Accounts(),
      onPurged: () => Photos.sweepOrphans(queue),
      // Work that is waiting -- the office failing, or asking for a pause --
      // is scheduled for the moment it is due. Offline is left to Android's
      // own backoff below, which already waits for a network.
      onStillPending: (after) async {
        if (after > Duration.zero) await BackgroundSync.drainAt(after);
      },
    );
    final result = await sync.run();

    // Offline is the one case worth Android's retry. Everything else -- sent,
    // refused, or waiting with its own wake-up arranged above -- is recorded
    // against the row it belongs to, and the queue itself is the record of
    // what still needs doing.
    return !result.offline;
  } catch (e) {
    // Never rethrow: an uncaught error here is a crash in a process the
    // farmer cannot see, on a phone that will simply stop syncing.
    debugPrint('background drain failed: $e');
    return false;
  } finally {
    await sync?.dispose();
    await store?.close();
  }
}

/// Tell the farmer, if anything is still unfinished.
///
/// A held claim is one whose evidence is incomplete, so it is not in the
/// queue and no amount of signal will send it. Nothing but this phone can say
/// so: the office has never heard of the claim. See reminders.dart.
///
/// Returns true either way -- there is nothing for Android to retry. A phone
/// that could not show the notification will be asked again at the next
/// reminder, and the claim is on the home screen regardless.
Future<bool> _remind() async {
  Store? store;
  try {
    // Its own connection, so closing it cannot close the app's. See
    // Store.open.
    store = await Store.open(ownConnection: true);
    // Across every farmer: a borrowed handset may be carrying somebody
    // else's unfinished claim, and they have no other way to learn of it.
    final said = await remindAboutUnfinished(store, Reminders());
    if (said) await BackgroundSync.remindLater();
    return true;
  } catch (e) {
    debugPrint('background reminder failed: $e');
    return true;
  } finally {
    await store?.close();
  }
}

/// Scheduling, from the app side.
class BackgroundSync {
  const BackgroundSync._();

  /// Wire up the isolate. Called once, at startup, before [schedule].
  static Future<void> initialise() async {
    await Workmanager().initialize(backgroundEntryPoint);
  }

  /// The standing schedule: every fifteen minutes, once there is a network.
  ///
  /// Fifteen minutes is Android's floor for periodic work and not a choice.
  /// The constraint is what makes it cheap -- with no network the work is not
  /// run at all, so a phone that spends the day out of signal is not woken
  /// once for it.
  ///
  /// [ExistingPeriodicWorkPolicy.keep] because this is called on every launch
  /// and replacing the registration each time would reset the interval, so a
  /// farmer who opens the app often would push the next run away every time
  /// they looked at it.
  static Future<void> schedule() async {
    await Workmanager().registerPeriodicTask(
      _periodicName,
      _drainTask,
      frequency: const Duration(minutes: 15),
      constraints: Constraints(networkType: NetworkType.connected),
      existingWorkPolicy: ExistingPeriodicWorkPolicy.keep,
      backoffPolicy: BackoffPolicy.exponential,
      backoffPolicyDelay: const Duration(minutes: 1),
    );
  }

  /// Ask for a pass as soon as there is a network, rather than at the next
  /// fifteen-minute boundary.
  ///
  /// Called the moment a farmer finishes photographing. They are usually
  /// still in the field with no signal, so this rarely runs immediately --
  /// what it does is make the first moment of signal on the walk home the
  /// moment the evidence leaves, instead of up to fifteen minutes after it.
  ///
  /// [ExistingWorkPolicy.keep] so three photographs finished in a row queue
  /// one pass, not three.
  static Future<void> drainSoon() async {
    await Workmanager().registerOneOffTask(
      _urgentName,
      _drainTask,
      constraints: Constraints(networkType: NetworkType.connected),
      existingWorkPolicy: ExistingWorkPolicy.keep,
      backoffPolicy: BackoffPolicy.exponential,
      backoffPolicyDelay: const Duration(seconds: 30),
    );
  }

  /// Ask for a pass once [after] has passed, when waiting work falls due.
  ///
  /// [ExistingWorkPolicy.replace], unlike [drainSoon]: every pass recomputes
  /// the earliest due time from the queue itself, so the newest request is
  /// the right one. Keeping an older, later wake-up would leave an item that
  /// became due sooner waiting for it.
  static Future<void> drainAt(Duration after) async {
    await Workmanager().registerOneOffTask(
      _dueName,
      _drainTask,
      initialDelay: after,
      constraints: Constraints(networkType: NetworkType.connected),
      existingWorkPolicy: ExistingWorkPolicy.replace,
      backoffPolicy: BackoffPolicy.exponential,
      backoffPolicyDelay: const Duration(minutes: 1),
    );
  }

  /// Look for an unfinished claim once [after] has passed.
  ///
  /// No network constraint, unlike every other task here. A reminder is not
  /// an upload: the farmer it is for is the one whose phone has had no signal
  /// since they filled the form, and waiting for a network to tell them would
  /// wait exactly as long as the claim already has.
  ///
  /// [ExistingWorkPolicy.replace] so that finishing one point of the walk
  /// pushes the reminder out rather than leaving an older one to fire while
  /// the farmer is still working.
  static Future<void> remindAbout(Duration after) async {
    await Workmanager().registerOneOffTask(
      _remindName,
      _remindTask,
      initialDelay: after,
      existingWorkPolicy: ExistingWorkPolicy.replace,
    );
  }

  /// Ask again tomorrow, because something is still unfinished.
  static Future<void> remindLater() => remindAbout(Reminders.after);

  /// Stop reminding: there is nothing unfinished, or the claim has gone.
  static Future<void> stopReminding() async {
    await Workmanager().cancelByUniqueName(_remindName);
  }

  /// Either kind of wake-up, for the foreground engine.
  static Future<void> wakeAfter(Duration after) =>
      after > Duration.zero ? drainAt(after) : drainSoon();

  /// Stop everything. Called on sign-out, at the same time as the local data
  /// goes -- a background task that outlived the session would wake a phone
  /// belonging to somebody who has left.
  static Future<void> cancel() async {
    await Workmanager().cancelByUniqueName(_periodicName);
    await Workmanager().cancelByUniqueName(_urgentName);
    await Workmanager().cancelByUniqueName(_dueName);
    await Workmanager().cancelByUniqueName(_remindName);
  }
}
