import 'dart:io';
import 'package:esurvey/core/link_quality.dart';
import 'package:flutter_test/flutter_test.dart';

/// Whether a farm's connection can carry a conversation.
///
/// The decision this makes is narrow -- offer live guidance, or do not -- and the
/// important property is which way it fails. A farm wrongly judged capable opens a
/// session that stutters and a farmer concludes the app is broken; a farm wrongly
/// judged incapable loses nothing it had, because every instruction needed to file a
/// claim is scripted and works with no network at all.
///
/// So these check that it refuses on any doubt.

LinkQuality q({
  double down = 1000,
  double up = 1000,
  double rtt = 200,
  DateTime? at,
  int min = 300,
  int maxRtt = 600,
  int unthrottled = 10000,
}) => LinkQuality(
  downKbps: down,
  upKbps: up,
  rttMs: rtt,
  at: at ?? DateTime.now(),
  minKbps: min,
  maxRttMs: maxRtt,
  unthrottledKbps: unthrottled,
);

void main() {
  group('whether to offer talking', () {
    test('a good link on both sides can talk', () {
      expect(q().canTalk, isTrue);
      expect(q().refusal, isNull);
    });

    test('a fast download with a slow upload cannot', () {
      // The case usually forgotten, and the one that matters most on cellular: the
      // uplink is the asymmetric half. A link that downloads at two megabits and
      // uploads at a hundred and twenty kilobits carries the assistant's voice to
      // the farmer perfectly and the farmer's voice nowhere.
      expect(q(down: 2000, up: 120).canTalk, isFalse);
      expect(q(down: 2000, up: 120).refusal, isNotNull);
    });

    test('a fast link with a long round trip cannot', () {
      // Throughput is not latency. At a second and a half each way a spoken
      // exchange stops being a conversation: the farmer starts talking over the
      // answer, and both sides interrupt each other for the rest of the session.
      expect(q(down: 5000, up: 5000, rtt: 1500).canTalk, isFalse);
    });

    test('exactly at the threshold is allowed', () {
      // Whichever way this falls it should be deliberate rather than accidental.
      // A farm measuring exactly the stated minimum has met it.
      expect(q(down: 300, up: 300, rtt: 600).canTalk, isTrue);
    });

    test('just under the threshold is refused', () {
      expect(q(down: 299, up: 300, rtt: 600).canTalk, isFalse);
      expect(q(down: 300, up: 299, rtt: 600).canTalk, isFalse);
      expect(q(down: 300, up: 300, rtt: 601).canTalk, isFalse);
    });

    test('a link that could not be measured is refused, and says so', () {
      // Not silently: a farmer offered a talking assistant on one visit and not
      // the next assumes the app is broken unless told.
      const none = LinkQuality.none();
      expect(none.canTalk, isFalse);
      expect(none.refusal, isNotNull);
      expect(none.refusal!.mr, contains('नेटवर्क'));
    });
  });

  group('the ten megabit figure', () {
    test('is not what decides whether talking is offered', () {
      // It was considered as the floor and would have switched the feature off in
      // almost every real field: the design targets a quarter of a megabit because
      // that is what rural cellular gives. A ten megabit floor would mean the
      // bandwidth work never runs and the assistant only ever speaks in an office.
      expect(q(down: 400, up: 400).canTalk, isTrue);
    });

    test('is where frames stop being rationed', () {
      expect(q(down: 400, up: 400).framesUnthrottled, isFalse);
      expect(q(down: 20000, up: 20000).framesUnthrottled, isTrue);
    });

    test('and a slow link never sends frames freely, however fast it looks '
        'in one direction', () {
      expect(q(down: 50000, up: 200).framesUnthrottled, isFalse);
    });
  });

  group('the thresholds come from the office', () {
    test('so a handset cannot hold a stale idea of good enough', () {
      // Carried in the probe's headers rather than compiled in: what counts as
      // sufficient depends on what the relay can sustain, which is not something a
      // handset can know.
      final strict = q(down: 400, up: 400, min: 1000);
      expect(strict.canTalk, isFalse);

      final lenient = q(down: 120, up: 120, min: 100);
      expect(lenient.canTalk, isTrue);
    });
  });

  test('live guidance is off unless a build turned it on', () {
    // So a trial can run without it, and a handset in a field cannot be surprised
    // by a feature nobody tested there.
    expect(liveGuidanceEnabled, isFalse);
  });

  group('the fallback thresholds match the ones the server states', () {
    // Compiled in only for a reply that carried no headers. Drifting from the
    // server's own constants would have a handset deciding by one rule while the
    // relay was built for another.
    //
    // These used to assert the numbers as literals, which is a copy rather than a
    // check: when the floor was re-derived from what Opus actually needs, the server
    // moved to 100 and this went on asserting 300 -- so the one test whose job was to
    // catch a divergence became the thing reporting one. It reads the Go now.

    test('the WebRTC floor', () {
      expect(fallbackMinKbps, _goConst('LiveMinimumKbps'));
      expect(const LinkQuality.none().minKbps, fallbackMinKbps);
    });

    test('the socket fallback floor, which is six times as much', () {
      // Raw PCM is 256 kbit/s up and 384 down against Opus's 24 each way, so the two
      // transports cannot share a threshold. A handset that judged the socket by the
      // WebRTC figure would open a session that stalls.
      expect(fallbackSocketMinKbps, _goConst('LiveSocketMinimumKbps'));
      expect(const LinkQuality.none().socketMinKbps, fallbackSocketMinKbps);
    });

    test('and the round trip', () {
      expect(fallbackMaxRttMs, _goConst('LiveMaxRTTms'));
      expect(const LinkQuality.none().maxRttMs, fallbackMaxRttMs);
    });
  });
}

/// One constant, read out of the Go that states it.
///
/// A number restated in two languages is two numbers. This parses the office's own
/// source so a change there fails here, rather than being discovered by a farmer
/// whose farm is refused on a link that would have carried the conversation.
int _goConst(String name) {
  var dir = Directory.current;
  for (var up = 0; up < 5; up++) {
    final f = File('${dir.path}/backend/internal/api/netprobe_handlers.go');
    if (f.existsSync()) {
      final m = RegExp(
        'const $name'
        r'\s*=\s*([0-9_]+)',
      ).firstMatch(f.readAsStringSync());
      if (m == null) {
        fail(
          '$name is no longer declared in netprobe_handlers.go. If it moved, '
          'point this test at its new home; do not delete the check.',
        );
      }
      return int.parse(m.group(1)!.replaceAll('_', ''));
    }
    final parent = dir.parent;
    if (parent.path == dir.path) break;
    dir = parent;
  }
  // Failed, not skipped. An unchecked threshold is the whole thing this guards
  // against, and a test that quietly stands down is worse than no test.
  fail(
    'could not find backend/internal/api/netprobe_handlers.go from '
    '${Directory.current.path}',
  );
}
