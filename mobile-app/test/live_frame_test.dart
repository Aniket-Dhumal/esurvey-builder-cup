import 'dart:io';
import 'dart:math' as math;
import 'dart:typed_data';

import 'package:esurvey/live/live_frame.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:image/image.dart' as img;
import 'package:path/path.dart' as p;

/// The one picture the assistant is given, and the ceiling it has to fit under.
///
/// The ceiling is the part worth a test of its own. It is written down twice -- in
/// Dart because the handset has to aim below it, in Python because the relay has to
/// enforce it -- and this repo has already been burnt once by two ends each testing
/// faithfully against its own copy of a number.
///
/// When those two copies drift, the failure does not look like a wrong constant. It
/// looks like an assistant that has nothing to look at, on some farms and not others,
/// after the farmer has already paid for the upload: the handset sends a frame it
/// believes is fine, the relay refuses it, and the refusal becomes a fault entry
/// nobody reads while the farmer stands in a field being asked again to show
/// something they have already shown.

/// Where the relay's own source is, from wherever the tests were started.
///
/// `flutter test` runs with the package directory as its working directory, so the
/// relay is one level up from there. Ancestors are tried as well rather than that
/// being assumed, because a test that fails on somebody's runner for a reason which
/// is not the constant is a test people learn to skip -- and this is the one test
/// here that must not be skipped.
File relaySource() {
  const relative = 'ai-services/src/esurvey_ai/live.py';
  final tried = <String>[];
  var dir = Directory.current;
  for (var up = 0; up < 5; up++) {
    final candidate = File(p.join(dir.path, relative));
    tried.add(candidate.path);
    if (candidate.existsSync()) return candidate;
    final parent = dir.parent;
    if (parent.path == dir.path) break;
    dir = parent;
  }
  fail(
    'The relay source could not be read, so the frame ceiling on this side was '
    'never checked against the one the relay enforces. That is a failure and not '
    'a skip: an unchecked ceiling is how the two ends drift. Looked in:\n  '
    '${tried.join('\n  ')}',
  );
}

/// The integer a Python arithmetic constant states.
///
/// Sums of products of literals, which is how both ends write a size in kilobytes.
/// Anything else fails loudly rather than being guessed at: a ceiling this test
/// could not read is a ceiling this test is not checking.
int statedInteger(String expression) {
  var total = 0;
  for (final term in expression.split('+')) {
    var product = 1;
    for (final factor in term.split('*')) {
      final value = int.tryParse(factor.trim());
      if (value == null) {
        fail(
          'the relay states its frame ceiling as "${expression.trim()}", which '
          'this test cannot evaluate -- teach it the new form rather than '
          'deleting the check',
        );
      }
      product *= value;
    }
    total += product;
  }
  return total;
}

/// A picture with as much detail in it as a field ever has.
///
/// Pseudo-random texture over a green wash, deterministically seeded. A canopy is the
/// hard case for JPEG -- thousands of small edges, no flat areas to spend nothing on
/// -- and a test built on a smooth gradient would prove the budget against a picture
/// nobody takes.
img.Image canopy({required int width, required int height}) {
  final noise = math.Random(20260930);
  final image = img.Image(width: width, height: height);
  for (var y = 0; y < height; y++) {
    for (var x = 0; x < width; x++) {
      image.setPixelRgb(
        x,
        y,
        40 + noise.nextInt(70),
        90 + noise.nextInt(110),
        30 + noise.nextInt(60),
      );
    }
  }
  return image;
}

void main() {
  test('the frame ceiling is the same number the relay itself enforces', () {
    // The whole reason the Dart constant is allowed to exist. It is a duplicate of a
    // Python one -- there is no importing across that boundary -- so the only thing
    // keeping the handset's idea of "small enough" and the relay's idea of
    // "acceptable" in agreement is this assertion failing when somebody changes one.
    final source = relaySource().readAsStringSync();
    final stated = RegExp(
      r'^MAX_FRAME_BYTES\s*=\s*([^\n#]+)',
      multiLine: true,
    ).firstMatch(source);

    if (stated == null) {
      fail(
        'MAX_FRAME_BYTES was not found in ${relaySource().path}. Either it was '
        'renamed -- in which case rename it here too -- or the relay has stopped '
        'stating a ceiling, which would mean nothing bounds what a handset may '
        'send.',
      );
    }

    expect(
      liveFrameMaxBytes,
      statedInteger(stated.group(1)!),
      reason:
          'the handset aims below a ceiling the relay no longer enforces, or '
          'enforces at a different number',
    );
  });

  group('aiming below the ceiling rather than at it', () {
    test('the qualities are tried from the best downwards', () {
      // A farmer gets whichever of these fits, so the order is which picture they
      // pay for. Stepping upwards would send the worst one available every time and
      // the careful list would buy nothing.
      expect(liveFrameQualities, isNotEmpty);
      for (var i = 1; i < liveFrameQualities.length; i++) {
        expect(
          liveFrameQualities[i],
          lessThan(liveFrameQualities[i - 1]),
          reason: 'quality ${liveFrameQualities[i]} is tried after a worse one',
        );
      }
    });

    test('a handset photograph comes back inside the ceiling', () async {
      // Twelve megapixels is not the thing being tested -- a two megapixel still of
      // a dense canopy is already several hundred kilobytes, which is the property
      // that matters. If this ever returns something larger than the ceiling the
      // relay refuses it, and the farmer has paid for an upload that bought nothing.
      final still = Uint8List.fromList(
        img.encodeJpg(canopy(width: 1600, height: 1200), quality: 92),
      );
      expect(
        still.length,
        greaterThan(liveFrameMaxBytes),
        reason: 'a fixture already small enough would prove nothing',
      );

      final frame = await liveFrameFrom(still);

      expect(frame, isNotNull);
      expect(frame!.length, lessThanOrEqualTo(liveFrameMaxBytes));
    });

    test('and comes back at the width the model is going to read anyway', () async {
      // The saving is nearly all here. A larger frame costs seconds on a
      // quarter-megabit link and buys nothing the model looks at, and the farmer is
      // standing in the field for every one of those seconds.
      final still = Uint8List.fromList(
        img.encodeJpg(canopy(width: 1600, height: 1200), quality: 92),
      );

      final frame = await liveFrameFrom(still);

      expect(img.decodeJpg(frame!)!.width, liveFrameWidth);
    });
  });

  group('a still that cannot be prepared', () {
    test('comes back as nothing rather than as an exception', () async {
      // Bytes that are not a picture at all -- a camera that handed back a truncated
      // file, which happens when a clip is being written at the same moment. The
      // conversation carries on by voice, which is most of its value, and nothing
      // interrupts the farmer to say a frame was lost.
      final notAPicture = Uint8List.fromList(List<int>.filled(64, 0x41));

      expect(await liveFrameFrom(notAPicture), isNull);
    });
  });
}
