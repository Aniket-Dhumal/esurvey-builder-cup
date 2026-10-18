import 'package:camera/camera.dart';
import 'package:esurvey/capture/encoding.dart';
import 'package:flutter_test/flutter_test.dart';

/// The rule this file exists to defend: a phone that will not record at the
/// quality we prefer must still record.
///
/// A farmer standing in a field whose camera refuses to start has lost the
/// claim. A farmer whose phone records 25 MB clips has a slow upload. Those are
/// not comparable costs, and every test below is about keeping the first from
/// happening in pursuit of the second.

const _camera = CameraDescription(
  name: 'back',
  lensDirection: CameraLensDirection.back,
  sensorOrientation: 0,
);

/// A controller that records which encoding it was asked for, and fails on the
/// rungs a test says the phone rejects.
class _FakeController extends CameraController {
  _FakeController(this.encoding, this.rejects)
    : super(_camera, ResolutionPreset.high);

  final ClipEncoding encoding;
  final bool Function(ClipEncoding) rejects;

  bool disposed = false;

  @override
  Future<void> initialize() async {
    if (rejects(encoding)) {
      throw CameraException('profile', 'this phone refuses ${encoding.label}');
    }
  }

  @override
  Future<void> dispose() async {
    disposed = true;
    // CameraController's dispose is @mustCallSuper, and the ladder relies on it
    // actually releasing the platform camera: a controller left holding one
    // makes every rung below it fail for the wrong reason.
    await super.dispose();
  }
}

/// Builds an encoder over a fake camera stack.
({ClipEncoder encoder, List<ClipEncoding> asked, List<_FakeController> built})
fake({bool Function(ClipEncoding)? rejects, List<CameraDescription>? cameras}) {
  final asked = <ClipEncoding>[];
  final built = <_FakeController>[];
  final reject = rejects ?? (_) => false;

  final encoder = ClipEncoder(
    cameras: () async => cameras ?? const [_camera],
    build: (c, e) {
      asked.add(e);
      final ctrl = _FakeController(e, reject);
      built.add(ctrl);
      return ctrl;
    },
  );
  return (encoder: encoder, asked: asked, built: built);
}

void main() {
  group('what the phone is asked for', () {
    test('the preferred profile is 15 fps at 2.5 Mbit/s', () async {
      // The decision this budget rests on: the office reads about a dozen still
      // keyframes per clip and never watches the footage, so halving the frame
      // rate doubles the bits in every frame the assessment looks at.
      final f = fake();
      await f.encoder.open();

      expect(f.asked.single.fps, 15);
      expect(f.asked.single.bitrate, 2500000);
      expect(f.encoder.encoding.label, 'preferred');
    });

    test('a claim of four clips fits the 20 MB budget', () async {
      final preferred = encodingLadder.first;
      final claim = preferred.expectedBytes(15) * 4;

      expect(claim, lessThan(claimBudgetBytes));
      // And it is a real reduction, not a rounding: the trial recorded 100 MB.
      expect(claim, lessThan(25 * 1024 * 1024));
    });
  });

  group('a phone that refuses what we prefer', () {
    test('steps down until something initialises', () async {
      // The failure mode this is all for. Some Android encoders refuse a
      // 15 fps profile outright.
      final f = fake(rejects: (e) => e.fps == 15);
      await f.encoder.open();

      expect(f.asked.map((e) => e.label), ['preferred', 'steady']);
      expect(f.encoder.encoding.label, 'steady');
      expect(f.encoder.encoding.fps, 20);
    });

    test('reaches the platform default rather than failing', () async {
      // Every rung with a frame rate refused, and one with a bitrate refused
      // too: the phone still records. This is exactly the trial handset's
      // behaviour, and it produced a usable claim.
      final f = fake(rejects: (e) => e.fps != null || e.bitrate != null);
      final c = await f.encoder.open();

      expect(c, isNotNull);
      expect(f.encoder.encoding.label, 'half-device');
      expect(f.encoder.encoding.fps, isNull);
      expect(f.encoder.encoding.bitrate, isNull);
      expect(f.encoder.atDeviceDefault, isTrue);
    });

    test(
      'a rejected controller is disposed before the next is opened',
      () async {
        // A controller that failed to initialise still holds a platform camera
        // on some devices, and the next rung cannot open one while it does. Left
        // undisposed, the ladder fails at every rung for the wrong reason.
        final f = fake(rejects: (e) => e.fps == 15);
        await f.encoder.open();

        expect(
          f.built.first.disposed,
          isTrue,
          reason: 'the refused controller was left holding the camera',
        );
        expect(
          f.built.last.disposed,
          isFalse,
          reason: 'the controller being returned must stay open',
        );
      },
    );

    test('only a phone with no working camera at all fails', () async {
      final f = fake(rejects: (_) => true);
      await expectLater(f.encoder.open(), throwsA(isA<CameraException>()));
    });

    test('a phone reporting no cameras says so plainly', () async {
      final f = fake(cameras: const []);
      await expectLater(
        f.encoder.open(),
        throwsA(
          isA<CameraException>().having((e) => e.code, 'code', 'no_camera'),
        ),
      );
    });
  });

  group('a phone that accepts a bitrate and ignores it', () {
    test(
      'an oversized clip steps the rung down for the rest of the walk',
      () async {
        // An encoder may take the setting and do as it likes, and the only way to
        // find out is to look at the file. Three more clips at the same size
        // would put the claim back at 100 MB.
        final f = fake();
        await f.encoder.open();
        expect(f.encoder.encoding.label, 'preferred');

        expect(f.encoder.sawClip(20 * 1024 * 1024), isTrue);
        expect(f.encoder.encoding.label, 'steady');
      },
    );

    test('an ordinary clip changes nothing', () async {
      // The ceiling sits above the preferred rung's 4.7 MB with room for a
      // scene the encoder finds hard -- wind in a canopy is genuinely
      // expensive -- so a normal walk never steps down.
      final f = fake();
      await f.encoder.open();

      expect(f.encoder.sawClip(5 * 1024 * 1024), isFalse);
      expect(f.encoder.sawClip(clipStepDownBytes), isFalse);
      expect(f.encoder.encoding.label, 'preferred');
    });

    test('the last rung does not step down further', () async {
      // At the platform default there is nothing left to ask for, and
      // reopening the camera between every station would cost the farmer time
      // for no gain.
      final f = fake(rejects: (e) => e.fps != null || e.bitrate != null);
      await f.encoder.open();

      expect(f.encoder.sawClip(30 * 1024 * 1024), isFalse);
      expect(f.encoder.encoding.label, 'half-device');
    });

    test(
      'a rung established at station one is kept for station four',
      () async {
        // Reopening must not silently go back to a profile the phone already
        // refused: that is four failed attempts per station.
        final f = fake(rejects: (e) => e.fps == 15);
        await f.encoder.open();
        final firstWalk = f.asked.length;

        await f.encoder.open();
        expect(
          f.asked.length,
          firstWalk + 1,
          reason: 'the refused rung was tried again',
        );
        expect(f.asked.last.label, 'steady');
      },
    );
  });

  group('a refused permission', _deniedGroup);

  group('a phone that discards the bitrate', _bitrateIgnoredGroup);

  group('the budget', () {
    test('is judged over a whole claim, not one clip', () async {
      expect(ClipEncoder.exceededBudget(19 * 1024 * 1024), isFalse);
      expect(ClipEncoder.exceededBudget(21 * 1024 * 1024), isTrue);
    });

    test('every bounded rung fits the claim budget', () async {
      // The flaw this replaces: the ladder used to raise the bitrate as the
      // frame rate rose, to hold per-frame quality constant. That put the
      // fallback rungs at 21, 25 and 29 MB a claim -- so a phone that refused
      // 15 fps quietly broke the budget the whole ladder exists to meet.
      //
      // The budget is the constraint. Per-frame quality is what gives way.
      for (final e in encodingLadder) {
        if (e.bitrate == null) continue;
        final claim = e.expectedBytes(15) * 4;
        expect(
          claim,
          lessThanOrEqualTo(claimBudgetBytes),
          reason:
              'rung "${e.label}" produces ${claim ~/ (1024 * 1024)} MB a '
              'claim, over the ${claimBudgetBytes ~/ (1024 * 1024)} MB budget',
        );
      }
    });

    test('the frame rate is what descends, not the size', () async {
      // Within one resolution. The ladder gives up frame quality rung by rung
      // while the bitrate stays put, so every rung costs the assessment a little
      // smoothness and none of them costs it the budget.
      //
      // Checked per resolution rather than across the whole ladder, because the
      // ladder does not only descend in frame rate any more: when it reaches the
      // pixel rungs it starts again at the preferred 15 fps, at half the size.
      // That is a step down, not a step back, and comparing the two halves as one
      // sequence would read it as the ladder climbing.
      for (final preset in {for (final e in encodingLadder) e.resolution}) {
        final bounded = encodingLadder
            .where((e) => e.resolution == preset && e.fps != null)
            .toList();
        for (var i = 1; i < bounded.length; i++) {
          expect(
            bounded[i].fps! > bounded[i - 1].fps!,
            isTrue,
            reason: 'the ladder should give up frame quality, not budget',
          );
        }
      }
    });

    test('and the pixels only ever descend with it', () async {
      // The property that makes the ladder a ladder. A rung further down must
      // never record more pixels than one above it: that would be the fallback
      // for a struggling encoder asking more of it than the rung it just
      // refused.
      for (var i = 1; i < encodingLadder.length; i++) {
        expect(
          encodingLadder[i - 1].coarserThan(encodingLadder[i]) ||
              encodingLadder[i - 1].resolution == encodingLadder[i].resolution,
          isTrue,
          reason:
              'rung "${encodingLadder[i].label}" records more pixels than '
              '"${encodingLadder[i - 1].label}" above it',
        );
      }
      expect(
        encodingLadder.last.bitrate,
        isNull,
        reason:
            'the last rung must be whatever the phone does, which is the '
            'only profile known to work everywhere',
      );
    });

    test('an unreadable clip file does not break the walk', () async {
      // A size that cannot be read loses the adaptation, not the recording.
      expect(await clipBytes('/no/such/file.mp4'), 0);
    });
  });
}

// --- a refused permission is not a fussy encoder ----------------------------

class _DeniedController extends CameraController {
  _DeniedController(this.code) : super(_camera, ResolutionPreset.high);

  final String code;
  bool disposed = false;

  @override
  Future<void> initialize() async {
    throw CameraException(code, 'the farmer refused the camera');
  }

  @override
  Future<void> dispose() async {
    disposed = true;
    await super.dispose();
  }
}

void _deniedGroup() {
  for (final code in [
    'CameraAccessDenied',
    'CameraAccessDeniedWithoutPrompt',
  ]) {
    test('$code stops the ladder instead of exhausting it', () async {
      // The whole point. Every rung fails when the permission is missing, so
      // the loop would try five profiles and tell the farmer "the camera would
      // not start at any recording quality" -- which reads as a broken phone
      // and sends them looking for another handset instead of into Settings.
      var attempts = 0;
      final built = <_DeniedController>[];
      final encoder = ClipEncoder(
        cameras: () async => const [_camera],
        build: (c, e) {
          attempts++;
          final ctrl = _DeniedController(code);
          built.add(ctrl);
          return ctrl;
        },
      );

      await expectLater(
        encoder.open(),
        throwsA(isA<CameraException>().having((e) => e.code, 'code', code)),
      );
      expect(
        attempts,
        1,
        reason: 'a refused permission was retried at every quality',
      );
      expect(
        built.single.disposed,
        isTrue,
        reason: 'the controller must not be left holding the camera',
      );
    });
  }

  test('a fussy encoder is still walked down the ladder', () async {
    // The distinction matters in both directions: an encoder that refuses a
    // profile must still get the next one tried.
    var attempts = 0;
    final encoder = ClipEncoder(
      cameras: () async => const [_camera],
      build: (c, e) {
        attempts++;
        return _FakeController(e, (x) => x.fps == 15);
      },
    );
    await encoder.open();
    expect(attempts, 2);
    expect(encoder.encoding.label, 'steady');
  });
}

// --- a phone that discards the bitrate --------------------------------------

void _bitrateIgnoredGroup() {
  test('asks for fewer pixels once a step is seen to change nothing', () async {
    // Measured on a Vivo V2253 during an office test: asked for 2.5 Mbit/s, got
    // 8.5, and the four rungs produced 15.99, 16.59, 16.49 and 16.47 MB. The
    // encoder discards videoBitrate, so the ladder walked every rung for nothing
    // -- and worse than nothing, because each step reopens the camera between
    // stations and costs the farmer time.
    //
    // An earlier build stopped there: it established the finding, logged it, and
    // left the remaining three clips at 16 MB each. A correct diagnosis followed
    // by no treatment. The bitrate is a request, and this phone discards
    // requests; the number of pixels is not one, so that is what the ladder asks
    // for next.
    final f = fake();
    await f.encoder.open();
    expect(f.encoder.encoding.label, 'preferred');

    // First oversized clip: stepping down is the right guess, nothing yet
    // suggests otherwise.
    expect(f.encoder.sawClip(15987121), isTrue);
    expect(f.encoder.encoding.label, 'steady');
    expect(f.encoder.bitrateIgnored, isFalse);

    // The next clip is no smaller, so the setting is not being honoured. The two
    // remaining 720p rungs are skipped, because they ask for the same thing in a
    // different voice and each one costs a camera reopen in the field.
    expect(
      f.encoder.sawClip(16586010),
      isTrue,
      reason: 'a phone that ignores the bitrate should be asked for less size',
    );
    expect(f.encoder.encoding.label, 'half');
    expect(
      f.encoder.encoding.resolution,
      ResolutionPreset.medium,
      reason: 'the one thing a capture session cannot quietly discard',
    );
    expect(
      f.encoder.bitrateIgnored,
      isFalse,
      reason:
          'there is still something left to ask for, so nothing has given up',
    );
  });

  test('gives up once there are no fewer pixels to ask for', () async {
    // The end of the road, on a phone that discards the bitrate at 720p and then
    // discards it again at 480p.
    //
    // The rungs below "half" are not tried, and that is the point rather than an
    // oversight: half-capped and half-device record the same 480p and differ only
    // in asking for less bitrate and less frame rate -- requests this phone has
    // twice been observed to throw away. Stepping to them would reopen the camera
    // between stations, cost the farmer the time, and produce the same file.
    //
    // They are still reachable, from open(), which is where a rung that the
    // encoder outright refuses is stepped past. This is the other axis: a rung
    // that works and produces a file too big.
    //
    // So it stops. What is left is how long a clip runs and how many a claim
    // carries, and those are decisions about evidence rather than about encoding.
    final f = fake();
    await f.encoder.open();

    expect(f.encoder.sawClip(15987121), isTrue);
    expect(f.encoder.sawClip(16586010), isTrue);
    expect(f.encoder.encoding.label, 'half');
    expect(f.encoder.bitrateIgnored, isFalse);

    // Still oversized at 480p, and no smaller than the clip before it.
    expect(
      f.encoder.sawClip(16488340),
      isFalse,
      reason: 'there is nothing further to ask for that this phone honours',
    );
    expect(f.encoder.encoding.label, 'half');
    expect(f.encoder.bitrateIgnored, isTrue);

    // And it stays stopped for the rest of the walk.
    expect(f.encoder.sawClip(16468088), isFalse);
    expect(f.encoder.sawClip(16470000), isFalse);
    expect(f.encoder.encoding.label, 'half');
  });

  test('keeps stepping while stepping is working', () async {
    // The other direction: a phone that does honour the bitrate should be walked
    // down until the clips fit.
    final f = fake();
    await f.encoder.open();

    expect(f.encoder.sawClip(20 * 1024 * 1024), isTrue);
    expect(f.encoder.encoding.label, 'steady');

    // Materially smaller, so the setting is being honoured -- step again.
    expect(f.encoder.sawClip(12 * 1024 * 1024), isTrue);
    expect(f.encoder.encoding.label, 'smooth');
    expect(f.encoder.bitrateIgnored, isFalse);
  });

  test('a clip within budget after a step ends the stepping quietly', () async {
    final f = fake();
    await f.encoder.open();

    expect(f.encoder.sawClip(20 * 1024 * 1024), isTrue);
    // Under the ceiling now: nothing more to do, and this is not evidence that
    // the phone ignores anything.
    expect(f.encoder.sawClip(4 * 1024 * 1024), isFalse);
    expect(f.encoder.bitrateIgnored, isFalse);
  });

  test('a few per cent of variation is not treated as being ignored', () async {
    // Wind in a canopy is genuinely more expensive to compress than still
    // leaves, so a clip is not a fixed-size thing. Only a step that achieved
    // nothing at all should stop the ladder.
    final f = fake();
    await f.encoder.open();

    expect(f.encoder.sawClip(20 * 1024 * 1024), isTrue);
    // 60% of the previous size: honoured, with ordinary scene variation on top.
    expect(f.encoder.sawClip(12 * 1024 * 1024), isTrue);
    expect(f.encoder.bitrateIgnored, isFalse);
  });
}
