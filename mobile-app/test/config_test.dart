import 'package:esurvey/core/config.dart';
import 'package:flutter_test/flutter_test.dart';

/// The addresses a release build must refuse to start on.
///
/// # Why this is worth a test at all
///
/// Because the failure it prevents is silent and looks like something else
/// entirely. This app is built for a village with no signal, so every screen
/// waits patiently through an unanswered request -- and a build pointed at a
/// development machine is an unanswered request forever. The first time it
/// happened the only symptom was a farmer watching "sending" on a sign-in screen,
/// and the config file claimed, wrongly, that such a build would fail loudly on
/// its first request.
///
/// The release half of the condition cannot be tested: kReleaseMode is a
/// compile-time constant and is false here, so a test of the whole thing would
/// exercise only the branch that does nothing. The address half is where the
/// judgement lives, and it is what this covers.
void main() {
  group('an address no handset can reach', () {
    test('the emulator route to a development machine is refused', () {
      // The default, and the one that actually shipped.
      expect(Config.unreachable('http://10.0.2.2:8090'), isTrue);
    });

    test('so is every name for the machine the build ran on', () {
      for (final base in [
        'http://localhost:8090',
        'https://localhost',
        'http://127.0.0.1:8090',
        'http://[::1]:8090',
        'http://0.0.0.0:8090',
      ]) {
        expect(Config.unreachable(base), isTrue, reason: base);
      }
    });

    test('and so is an address that is not one', () {
      // An empty define, or a value somebody pasted a newline into. Refusing is
      // right: there is nothing to try, and trying would be the same silence.
      for (final base in ['', 'not a url', '://', 'esurvey.mindstrix.com']) {
        expect(Config.unreachable(base), isTrue, reason: '"$base"');
      }
    });
  });

  group('an address a handset can reach', () {
    test('the platform is accepted', () {
      expect(Config.unreachable('https://esurvey.mindstrix.com'), isFalse);
    });

    test('a port and a path do not make it unreachable', () {
      expect(Config.unreachable('https://esurvey.mindstrix.com:8443'), isFalse);
      expect(Config.unreachable('https://esurvey.mindstrix.com/api'), isFalse);
    });

    test('a private address on the office network is accepted', () {
      // A handset on the same wifi can reach it, and refusing it would stop the
      // one setup where this is tested on a real phone against a real server
      // without a public address.
      expect(Config.unreachable('http://192.168.1.14:8090'), isFalse);
    });
  });

  test('this build knows where it is pointed', () {
    // Under the test runner kReleaseMode is false, so the guard must stand down
    // whatever the address is -- a developer running against a local API is the
    // case the default exists for, and a guard that fired here would make the
    // suite unrunnable.
    expect(Config.pointsNowhere, isFalse);
  });
}
