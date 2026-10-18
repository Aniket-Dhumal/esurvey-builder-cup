/// What the app must be allowed to do, checked before a farmer needs it.
///
/// # The failure this prevents
///
/// Every permission used to be asked for at the moment it was first needed,
/// which put the camera prompt on the walk screen and the location prompt a
/// second later. A farmer who declined either, or whose phone had location
/// switched off, found out standing in their field — the one place where nothing
/// can be fixed, because fixing it means Settings, and often signal.
///
/// Worse, a refused camera did not read as a refusal. The recording profile
/// ladder tries five encoder settings and every one of them fails when the
/// permission is missing, so the farmer was told "the camera would not start at
/// any recording quality" — which sounds like a broken phone.
///
/// # Why asking early is not the usual mistake
///
/// Asking for permissions at launch is ordinarily bad advice: a cold system
/// prompt before the farmer has done anything is a prompt they cannot judge, and
/// they refuse it. The notification permission in this app is deliberately
/// deferred for exactly that reason, and still is.
///
/// What changed is that the ask is no longer cold. The farmer is shown, in their
/// own language, what each permission is for and what the app cannot do without
/// it, and the system prompt follows their decision to continue. That is a
/// different act from a prompt appearing unbidden.
///
/// # What is required and what is not
///
///   camera            required. Nothing can be recorded without it.
///   precise location  required. A recording that cannot be placed inside the
///                     farmer's own boundary is not evidence, and an
///                     approximate fix is several hundred metres wide.
///   location switched on  required, and separate: a granted permission over a
///                     disabled radio still yields nothing.
///   notifications     not required. Without them the upload still runs and the
///                     claim still completes; the farmer just cannot see
///                     progress or be reminded of unfinished work.
///
/// The microphone is **not** asked for, and should not be. The camera runs with
/// `enableAudio: false`, and the plugin checks the audio permission only when
/// audio is enabled — so nothing in this app records sound. Asking a farmer for
/// microphone access on a government application handling their land records,
/// for a feature that does not exist, would be an overreach. (RECORD_AUDIO is
/// still in the built manifest, by merge from the camera plugin; see
/// docs/FIELD_TRIAL_2026-09-28.md finding 24.)
library;

import 'package:camera/camera.dart';
import 'package:record/record.dart';
import 'package:flutter/foundation.dart';
import 'package:geolocator/geolocator.dart';

import 'reminders.dart';

/// Whether one thing the app needs is available.
enum Access {
  /// Allowed.
  granted,

  /// Refused, and the farmer can be asked again.
  denied,

  /// Refused in a way the app cannot ask about again. Only Settings will do.
  blocked,

  /// Granted, but not well enough to be useful — an approximate location where
  /// a precise one is needed.
  reduced,

  /// Switched off at the device level rather than refused to this app.
  off,
}

extension AccessX on Access {
  bool get usable => this == Access.granted;

  /// Whether asking again can achieve anything, or whether only Settings can.
  bool get needsSettings => this == Access.blocked || this == Access.reduced;
}

/// What the app is currently allowed to do.
@immutable
class AccessReport {
  const AccessReport({
    required this.camera,
    required this.location,
    required this.locationService,
    required this.notifications,
  });

  final Access camera;
  final Access location;
  final Access locationService;
  final Access notifications;

  /// Whether a farmer can record a walk right now.
  ///
  /// Notifications are not in this: an upload runs without them. Including them
  /// would block a claim over a convenience.
  bool get canWalk =>
      camera.usable && location.usable && locationService.usable;

  /// Whether anything at all is missing, including the optional.
  bool get complete => canWalk && notifications.usable;

  /// Whether the farmer must go to Settings rather than be asked again.
  bool get settingsOnly =>
      camera.needsSettings ||
      location.needsSettings ||
      notifications.needsSettings;

  @override
  String toString() =>
      'AccessReport(camera: $camera, location: $location, '
      'service: $locationService, notifications: $notifications)';
}

/// Checks and requests what the app needs, without any UI of its own.
class AccessCheck {
  AccessCheck({
    Future<List<CameraDescription>> Function()? cameras,
    Reminders? reminders,
  }) : _cameras = cameras ?? availableCameras,
       _reminders = reminders ?? Reminders();

  final Future<List<CameraDescription>> Function() _cameras;
  final Reminders _reminders;

  /// What is allowed now, asking for nothing.
  ///
  /// Safe to call on every launch: it opens no prompt and takes milliseconds.
  Future<AccessReport> check() async {
    return AccessReport(
      camera: await _cameraAccess(request: false),
      location: await _locationAccess(request: false),
      locationService: await _serviceAccess(),
      notifications: await _notificationAccess(),
    );
  }

  /// Ask for everything that is missing and can still be asked for.
  ///
  /// Ordered deliberately: camera first, because it is the one a farmer
  /// understands immediately from a screen that has just explained recording,
  /// and a refusal there tells us to stop rather than stack three prompts.
  Future<AccessReport> request() async {
    final camera = await _cameraAccess(request: true);
    final location = await _locationAccess(request: true);
    final service = await _serviceAccess();

    // Last, and never blocking. A farmer who has just granted the camera and
    // their location has earned the benefit of the doubt on a third prompt, but
    // a refusal here costs them nothing they need.
    var notifications = await _notificationAccess();
    if (notifications == Access.denied) {
      await _reminders.requestPermission();
      notifications = await _notificationAccess();
    }

    return AccessReport(
      camera: camera,
      location: location,
      locationService: service,
      notifications: notifications,
    );
  }

  /// Ask for the microphone, for a farmer about to talk to the assistant.
  ///
  /// # Why this is not in request()
  ///
  /// Because it is not needed to make a claim. The walk records with audio disabled
  /// and the access flow has always, deliberately, left the microphone alone: a
  /// permission asked for at launch and never used is the fastest way to teach
  /// somebody to refuse the next one.
  ///
  /// The assistant is the one thing that needs it, and it is optional -- off in most
  /// builds, declined by some farmers, impossible on a slow farm. So it is asked for
  /// at the moment a farmer agrees to talk, and a farmer who never does is never
  /// asked.
  ///
  /// Returns what the permission is now, prompting once if it can be prompted for.
  Future<Access> requestMicrophone() async {
    final recorder = AudioRecorder();
    try {
      if (await recorder.hasPermission()) return Access.granted;
      // hasPermission prompts on the first call and answers from the system after
      // that, so a false here is a refusal rather than an absence of a question.
      return Access.denied;
    } catch (e) {
      // A handset with no microphone, or a platform channel that failed. Treated as
      // denied: the assistant is simply not offered, which is the same as a farm
      // with no signal.
      debugPrint('[access.microphone] $e');
      return Access.denied;
    } finally {
      await recorder.dispose();
    }
  }

  /// Open the app's own settings page, for a permission that can no longer be
  /// asked about.
  Future<void> openSettings() => Geolocator.openAppSettings();

  /// Open the phone's location settings, for a switched-off radio.
  Future<void> openLocationSettings() => Geolocator.openLocationSettings();

  // --- camera ---------------------------------------------------------------

  /// The camera plugin has no permission API of its own: opening a controller is
  /// what triggers the prompt, and a refusal arrives as a CameraException with
  /// the code below.
  ///
  /// So a check without a prompt is not possible here, and [request] is the
  /// honest name for what this does. The controller is opened at the lowest
  /// resolution available and disposed immediately: nothing is recorded, and a
  /// prompt the farmer has already answered does not reappear.
  Future<Access> _cameraAccess({required bool request}) async {
    if (!request) {
      // Nothing can be learned without prompting, so an unprompted check
      // reports what a prompt would most likely find: unknown, treated as
      // deniable. The screen asks anyway.
      return Access.denied;
    }

    List<CameraDescription> cameras;
    try {
      cameras = await _cameras();
    } catch (e) {
      debugPrint('[access] cameras could not be listed: $e');
      return Access.off;
    }
    if (cameras.isEmpty) return Access.off;

    CameraController? c;
    try {
      c = CameraController(
        cameras.first,
        // The cheapest thing that still opens the device: this exists to
        // trigger the permission, not to record.
        ResolutionPreset.low,
        enableAudio: false,
      );
      await c.initialize();
      return Access.granted;
    } on CameraException catch (e) {
      // The two the plugin raises for a refusal. Anything else is a camera
      // fault rather than a permission, and must not be reported as one --
      // telling a farmer to change a setting that is already right is worse
      // than telling them the camera is broken.
      if (e.code == 'CameraAccessDenied') return Access.denied;
      if (e.code == 'CameraAccessDeniedWithoutPrompt') return Access.blocked;
      debugPrint(
        '[access] camera failed for a reason other than permission: '
        '${e.code} ${e.description}',
      );
      return Access.off;
    } catch (e) {
      debugPrint('[access] camera check failed: $e');
      return Access.off;
    } finally {
      try {
        await c?.dispose();
      } catch (_) {
        // A controller that never initialised throws on dispose. The walk
        // opens its own.
      }
    }
  }

  // --- location -------------------------------------------------------------

  Future<Access> _locationAccess({required bool request}) async {
    try {
      var p = await Geolocator.checkPermission();
      if (request && p == LocationPermission.denied) {
        p = await Geolocator.requestPermission();
      }

      switch (p) {
        case LocationPermission.denied:
          return Access.denied;
        case LocationPermission.deniedForever:
          return Access.blocked;
        case LocationPermission.unableToDetermine:
          return Access.denied;
        case LocationPermission.whileInUse:
        case LocationPermission.always:
          break;
      }

      // Granted is not enough. On Android 12 and later the farmer can grant
      // an approximate location, which is several hundred metres wide -- and a
      // recording that cannot be placed inside the farmer's own boundary is not
      // evidence of anything. Reported separately because the remedy is
      // different: the permission is granted, and only Settings can make it
      // precise.
      try {
        final accuracy = await Geolocator.getLocationAccuracy();
        if (accuracy == LocationAccuracyStatus.reduced) {
          return Access.reduced;
        }
      } catch (e) {
        // Not every platform answers. A granted permission whose precision
        // cannot be read is treated as granted: the walk measures its own
        // accuracy per fix and refuses a poor one there.
        debugPrint('[access] location precision unknown: $e');
      }
      return Access.granted;
    } catch (e) {
      debugPrint('[access] location check failed: $e');
      return Access.denied;
    }
  }

  Future<Access> _serviceAccess() async {
    try {
      return await Geolocator.isLocationServiceEnabled()
          ? Access.granted
          : Access.off;
    } catch (e) {
      debugPrint('[access] location service check failed: $e');
      return Access.off;
    }
  }

  // --- notifications --------------------------------------------------------

  Future<Access> _notificationAccess() async {
    try {
      return await _reminders.permitted() ? Access.granted : Access.denied;
    } catch (e) {
      debugPrint('[access] notification check failed: $e');
      return Access.denied;
    }
  }
}
