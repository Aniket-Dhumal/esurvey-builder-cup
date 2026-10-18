import 'dart:async';

import 'package:esurvey/live/live_audio.dart';
import 'package:flutter/foundation.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:record/record.dart';

/// A microphone that will not stay open, which on this handset is the ordinary case.
///
/// # The fault
///
/// The capture stopped and stayed stopped. One error from the platform cancelled the
/// subscription and nothing picked it up, so the farmer talked into a session that
/// received nothing, got silence back, and had no way to tell why -- while the
/// session's meter ran. The same shape as the position stream's fault, and for the
/// same reason: an error handler that reports and gives up.
///
/// Android drops a capture stream for its own reasons, and two of them happen every
/// walk: opening the camera takes the audio focus, and so does the speaker starting
/// to play the assistant's reply into the same handset. So it has to be picked up
/// again rather than mourned.
///
/// # Why it gives up eventually
///
/// Three attempts. A microphone that will not hold after three is not going to on
/// the fourth, and by then somebody has been talking to nobody for long enough. The
/// conversation ends and says so, which is the honest outcome -- the walk continues
/// either way, because the scripted guidance needs none of this.
class _FlakyRecorder extends AudioRecorder {
  final streams = <StreamController<Uint8List>>[];
  bool recording = false;

  @override
  Future<bool> hasPermission() async => true;

  @override
  Future<Stream<Uint8List>> startStream(RecordConfig config) async {
    final c = StreamController<Uint8List>();
    streams.add(c);
    recording = true;
    return c.stream;
  }

  @override
  Future<bool> isRecording() async => recording;

  @override
  Future<String?> stop() async {
    recording = false;
    return null;
  }

  @override
  Future<void> dispose() async {}

  int get opened => streams.length;
  StreamController<Uint8List> get latest => streams.last;

  void emit() => latest.add(Uint8List.fromList([1, 0, 2, 0]));

  Future<void> closeAll() async {
    for (final c in streams) {
      if (!c.isClosed) await c.close();
    }
  }
}

Future<void> _realSeconds(num s) =>
    Future<void>.delayed(Duration(milliseconds: (s * 1000).round()));

void main() {
  test('an error does not end the conversation', () async {
    final rec = _FlakyRecorder();
    final audio = LiveAudio(recorder: rec);
    final heard = <Uint8List>[];
    addTearDown(() async {
      await audio.dispose();
      await rec.closeAll();
    });

    expect(await audio.startCapture(heard.add), isTrue);
    rec.emit();
    await _realSeconds(0.1);
    expect(heard, hasLength(1));

    rec.latest.addError(Exception('the platform took the audio focus'));
    await _realSeconds(1.5);

    expect(
      rec.opened,
      greaterThan(1),
      reason: 'the microphone was never picked up again',
    );

    // And it hears again, which is the point rather than merely being retried.
    rec.emit();
    await _realSeconds(0.1);
    expect(heard, hasLength(2));
  });

  test('a capture that ends is picked up again', () async {
    // What Android does when the camera opens, and again when the speaker starts.
    final rec = _FlakyRecorder();
    final audio = LiveAudio(recorder: rec);
    addTearDown(() async {
      await audio.dispose();
      await rec.closeAll();
    });

    await audio.startCapture((_) {});
    rec.emit();
    await _realSeconds(0.1);

    await rec.latest.close();
    await _realSeconds(1.5);

    expect(rec.opened, greaterThan(1));
  });

  test(
    'a capture that goes quiet is not believed',
    () async {
      // The case neither an error nor an ending catches: still subscribed, delivering
      // nothing. The recorder produces a buffer every few tens of milliseconds while
      // it lives, and a field is never quiet enough to stop it.
      final rec = _FlakyRecorder();
      final audio = LiveAudio(recorder: rec);
      addTearDown(() async {
        await audio.dispose();
        await rec.closeAll();
      });

      await audio.startCapture((_) {});
      rec.emit();
      await _realSeconds(5.5);

      expect(
        rec.opened,
        greaterThan(1),
        reason:
            'four seconds of silence from a live microphone is not a quiet farmer',
      );
    },
    timeout: const Timeout(Duration(seconds: 40)),
  );

  test(
    'a microphone that will not hold ends the conversation and says why',
    () async {
      // The honest outcome. A session that cannot hear is worse than no session: the
      // farmer talks, nothing answers, and the meter runs.
      final rec = _FlakyRecorder();
      final audio = LiveAudio(recorder: rec);
      String? lost;
      addTearDown(() async {
        await audio.dispose();
        await rec.closeAll();
      });

      audio.onLost = (why) => lost = why;
      await audio.startCapture((_) {});

      // Every stream it is given dies at once, so the attempts run out.
      for (var i = 0; i < 6 && lost == null; i++) {
        if (!rec.latest.isClosed) await rec.latest.close();
        await _realSeconds(2.2);
      }

      expect(lost, isNotNull, reason: 'it kept trying and told nobody');
      expect(
        audio.listening,
        isFalse,
        reason: 'a session that cannot hear must not look like one that can',
      );
    },
    timeout: const Timeout(Duration(seconds: 60)),
  );

  test(
    'a deliberate stop is not treated as a failure',
    () async {
      // Otherwise closing the conversation would reopen the microphone behind it.
      final rec = _FlakyRecorder();
      final audio = LiveAudio(recorder: rec);
      addTearDown(() async {
        await audio.dispose();
        await rec.closeAll();
      });

      await audio.startCapture((_) {});
      final opened = rec.opened;
      await audio.stopCapture();
      await _realSeconds(2.5);

      expect(rec.opened, opened, reason: 'stopping it started it again');
      expect(audio.listening, isFalse);
    },
    timeout: const Timeout(Duration(seconds: 30)),
  );
}
