import 'package:esurvey/core/dates.dart';
import 'package:flutter_test/flutter_test.dart';

/// The date the server would not accept.
///
/// A survey event carries `incident_start` as a SQL date, so the API sends
/// "2026-08-20". The report's `occurred_at` is a Go time.Time and refuses
/// anything that is not RFC 3339. The app passed the date straight through,
/// so every report it filed came back 400 -- and because the queue records a
/// permanent refusal against the row and moves on, the farmer saw a report
/// that had been sent and an office that never received it.
///
/// It survived 148 tests because every one of them answers through a mock
/// client that accepts whatever it is given. The first real server rejected
/// the first real report.

void main() {
  group('the incident date becomes an instant', () {
    test('a plain date is sent as RFC 3339', () {
      final out = occurredAtFrom('2026-08-20');

      // The shape the server demands. Parsing it back is the assertion that
      // matters -- this is exactly what Go's json.Unmarshal does, and what it
      // refused.
      expect(DateTime.tryParse(out), isNotNull);
      expect(out, contains('T'));
      expect(
        out,
        endsWith('Z'),
        reason:
            'the server is given UTC, not a '
            'local time whose offset it has to guess',
      );
      expect(
        out,
        startsWith('2026-08-20'),
        reason: 'the day the farmer is claiming for must not move',
      );
    });

    test('midnight UTC, because the hour was never asked for', () {
      // The form does not ask what time the hail came, deliberately: nobody
      // should lose a claim over whether it was four or five. So the instant
      // is the start of the day the event names, not an invented hour.
      final parsed = DateTime.parse(occurredAtFrom('2026-08-20'));

      expect(parsed.isUtc, isTrue);
      expect(parsed.hour, 0);
      expect(parsed.minute, 0);
    });

    test('a timestamp that is already RFC 3339 keeps its day', () {
      // Should the events API ever start sending an instant, the day must
      // still survive the round trip.
      final out = occurredAtFrom('2026-08-20T14:30:00Z');
      expect(out, startsWith('2026-08-20'));
    });

    test('a missing or unreadable date still files', () {
      // Wrong by a few days at worst, and the report goes. Refusing would
      // lose the claim over a field the farmer never filled in.
      for (final bad in [null, '', 'not a date', 'null']) {
        final out = occurredAtFrom(bad);
        expect(
          DateTime.tryParse(out),
          isNotNull,
          reason: '$bad produced something the server would refuse: $out',
        );
      }
    });

    test('what the bug looked like', () {
      // The old behaviour, kept as a statement of what is being prevented: a
      // bare date is not a valid RFC 3339 instant, and a server that wants
      // one will refuse it.
      const raw = '2026-08-20';
      expect(raw.contains('T'), isFalse);
      expect(occurredAtFrom(raw), isNot(raw));
    });
  });
}
