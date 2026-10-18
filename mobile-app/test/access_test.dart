import 'package:esurvey/core/access.dart';
import 'package:flutter_test/flutter_test.dart';

/// What blocks a walk and what does not.
///
/// The rule worth pinning: the camera and a precise location are required,
/// because without the first there is nothing to record and without the second a
/// recording cannot be placed inside the farmer's own boundary — and a recording
/// that cannot be placed is not evidence.
///
/// Notifications are not required. An upload runs without them and a claim
/// completes. Blocking a claim over a convenience would be the wrong trade, and
/// it is the kind of line that quietly moves.

AccessReport report({
  Access camera = Access.granted,
  Access location = Access.granted,
  Access service = Access.granted,
  Access notifications = Access.granted,
}) => AccessReport(
  camera: camera,
  location: location,
  locationService: service,
  notifications: notifications,
);

void main() {
  group('what a walk needs', () {
    test('everything granted can walk', () {
      expect(report().canWalk, isTrue);
      expect(report().complete, isTrue);
    });

    test('no camera cannot walk', () {
      expect(report(camera: Access.denied).canWalk, isFalse);
      expect(report(camera: Access.blocked).canWalk, isFalse);
      expect(report(camera: Access.off).canWalk, isFalse);
    });

    test('no location cannot walk', () {
      expect(report(location: Access.denied).canWalk, isFalse);
      expect(report(location: Access.blocked).canWalk, isFalse);
    });

    test('an approximate location cannot walk', () {
      // The case a granted permission hides. On Android 12 and later the
      // farmer can grant an approximate location, which is several hundred
      // metres wide: the walk would run, every fix would fail the geofence, and
      // the farmer would be told they were outside their own field.
      expect(report(location: Access.reduced).canWalk, isFalse);
    });

    test('location switched off cannot walk, even when granted', () {
      // A granted permission over a disabled radio yields nothing, and the two
      // are separate settings with separate remedies.
      expect(report(service: Access.off).canWalk, isFalse);
    });

    test('no notifications can still walk', () {
      final r = report(notifications: Access.denied);
      expect(
        r.canWalk,
        isTrue,
        reason:
            'a claim must not be blocked over a convenience: the upload '
            'runs and the claim completes without notifications',
      );
      expect(r.complete, isFalse, reason: 'but something is still missing');
    });
  });

  group('where the farmer has to go', () {
    test('a refusal that can be asked again does not need settings', () {
      expect(report(camera: Access.denied).settingsOnly, isFalse);
      expect(report(location: Access.denied).settingsOnly, isFalse);
    });

    test('a permanent refusal needs settings', () {
      // Android stops delivering the prompt after two refusals, and asking
      // again then does nothing at all — so the screen must send the farmer to
      // Settings rather than offer a button that silently fails.
      expect(report(camera: Access.blocked).settingsOnly, isTrue);
      expect(report(location: Access.blocked).settingsOnly, isTrue);
    });

    test('an approximate location needs settings', () {
      // The permission is granted, so asking again would change nothing: only
      // Settings can make it precise.
      expect(report(location: Access.reduced).settingsOnly, isTrue);
    });

    test('a broken camera is not a settings problem', () {
      // Telling a farmer to change a setting that is already right is worse
      // than telling them the camera is broken: they will hunt through Settings
      // and then give up on the claim.
      expect(report(camera: Access.off).settingsOnly, isFalse);
    });
  });

  group('the Access values themselves', () {
    test('only granted is usable', () {
      for (final a in Access.values) {
        expect(a.usable, a == Access.granted, reason: '$a');
      }
    });

    test('the two that only settings can fix are marked as such', () {
      expect(Access.blocked.needsSettings, isTrue);
      expect(Access.reduced.needsSettings, isTrue);

      for (final a in [Access.granted, Access.denied, Access.off]) {
        expect(a.needsSettings, isFalse, reason: '$a');
      }
    });
  });

  group('the microphone', () {
    test('is still not part of what a claim requires', () {
      // This test previously said the microphone was not asked for at all, because
      // nothing recorded sound: the camera runs with enableAudio: false and the walk
      // never wanted it. Its comment said that if somebody added audio recording,
      // the test should fail and they should have to decide deliberately rather than
      // inherit a permission.
      //
      // Somebody did. A farmer may now hold a spoken conversation with the
      // assistant, and that needs the microphone.
      //
      // The decision taken, deliberately, is that it stays out of *this* flow. The
      // access screen asks for what a claim cannot be made without -- the camera and
      // a precise location -- and a claim can be made without ever speaking to
      // anybody. So the report still carries no microphone, and a farmer who never
      // uses the assistant is never asked for one.
      final fields = report().toString();
      expect(fields.contains('mic'), isFalse);
      expect(fields.contains('audio'), isFalse);
    });

    test('is asked for where it is used, and nowhere else', () {
      // Named on AccessCheck rather than folded into request(), so that reading the
      // access flow shows the microphone is not in it. Asserted on the API rather
      // than by prompting, which needs a platform channel and a handset.
      expect(AccessCheck().requestMicrophone, isA<Function>());
    });
  });
}
