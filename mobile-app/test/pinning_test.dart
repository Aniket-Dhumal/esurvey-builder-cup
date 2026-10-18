import 'package:esurvey/core/pinning.dart';
import 'package:flutter_test/flutter_test.dart';

/// Certificate pinning.
///
/// The dangerous failure here is not "an attacker gets through" -- it is
/// "every farmer is locked out of a claim window because a certificate was
/// renewed". So most of these tests are about pinning refusing to switch
/// itself on in configurations that would eventually do that.

const pinA = 'sha256/AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=';
const pinB = 'sha256/BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB=';

void main() {
  group('when pinning switches on', () {
    test('two pins enable it', () {
      final p = Pinning.fromEnvironment('$pinA,$pinB');
      expect(p.enabled, isTrue);
      expect(p.pins, hasLength(2));
    });

    test('one pin does not', () {
      // A single pin plus a lost private key is an app that can never reach
      // its server again, on every handset, permanently. Refusing to run in
      // that configuration is the point -- the temptation to ship one pin is
      // highest when there is only one key so far.
      expect(Pinning.fromEnvironment(pinA).enabled, isFalse);
    });

    test('no pins does not', () {
      expect(Pinning.fromEnvironment('').enabled, isFalse);
    });

    test('a duplicated pin is still one pin', () {
      // Two identical pins are not a backup. Deduplicated before counting, so
      // pasting the same value twice does not buy false confidence.
      expect(Pinning.fromEnvironment('$pinA,$pinA').enabled, isFalse);
    });

    test('malformed entries are discarded, not guessed at', () {
      final p = Pinning.fromEnvironment('not-a-pin,$pinA,sha256/,$pinB');
      expect(p.pins, {pinA, pinB});
      expect(p.enabled, isTrue);
    });

    test('surrounding whitespace does not break a pin', () {
      // Build defines get pasted from documents and wrapped by shells.
      final p = Pinning.fromEnvironment('  $pinA , $pinB  ');
      expect(p.enabled, isTrue);
      expect(p.pins, {pinA, pinB});
    });

    test('one good pin beside junk still refuses', () {
      // The junk does not count towards the two, so this is a one-pin build
      // and must stay off.
      expect(
        Pinning.fromEnvironment('$pinA,garbage,also-garbage').enabled,
        isFalse,
      );
    });
  });

  group('an unpinned build is unaffected', () {
    test('it accepts, rather than refusing everything', () {
      // The failure mode to avoid: pinning being "on" with nothing to match,
      // which would take every handset offline at once.
      final p = Pinning.fromEnvironment('');
      expect(p.enabled, isFalse);
      expect(p.pins, isEmpty);
    });

    test('guard does nothing without pins', () {
      expect(() => guard(Pinning.fromEnvironment(''), null), returnsNormally);
    });
  });

  group('guarding a connection', () {
    test('a pinned build with no certificate refuses', () {
      // Nothing to check is not the same as nothing to worry about.
      expect(
        () => guard(Pinning.fromEnvironment('$pinA,$pinB'), null),
        throwsA(isA<Exception>()),
      );
    });
  });

  group('the pin format', () {
    test('pins carry the algorithm they were computed with', () {
      // sha256/ is not decoration: it is what makes a future migration to
      // another digest expressible, instead of two incompatible base64 blobs
      // that look alike.
      for (final pin in Pinning.fromEnvironment('$pinA,$pinB').pins) {
        expect(pin, startsWith('sha256/'));
      }
    });

    test('a bare digest with no algorithm is rejected', () {
      // And rejecting it leaves one valid pin, which is not enough to pin
      // with -- so the whole configuration comes back disabled rather than
      // silently running on a single pin. That is the safe direction: the
      // build is unpinned and works, instead of pinned to one key and one
      // lost key away from bricking every handset.
      const bare = 'AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=';
      final p = Pinning.fromEnvironment('$bare,$pinB');
      expect(p.enabled, isFalse);
      expect(p.pins, isEmpty);
    });
  });
}
