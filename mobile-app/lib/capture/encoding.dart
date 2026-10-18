import 'dart:io';

import 'package:camera/camera.dart';
import 'package:flutter/foundation.dart';

/// What to ask a phone's encoder for, and what to do when it refuses.
///
/// # Why this is negotiated rather than set
///
/// A claim was 100 MB: four fifteen-second clips at whatever bitrate the
/// handset chose, which on the trial phone was about 13 Mbit/s for 720p. That
/// is the root cause of most of the trial's upload trouble -- chunks taking 9
/// to 25 seconds, one dying at 49.8 s, and a window long enough for the
/// farmer's session to lapse inside it.
///
/// The budget is now 20 MB for a whole claim, which is 5 MB a clip. The way to
/// spend it is fewer, better frames: the office reads about a dozen still
/// keyframes out of each clip and never watches the footage, so halving the
/// frame rate doubles the bits in every frame the assessment actually looks at.
///
/// # The part that must not go wrong
///
/// Asking an Android encoder for 15 fps is not always granted. Some devices
/// refuse the profile outright, some accept it and ignore it, some fail only
/// when recording starts rather than when the camera opens. A farmer standing
/// in a field whose phone will not record is a worse outcome than a large file
/// by a wide margin: the file is slow, the refusal loses the claim.
///
/// So every setting here is a request with a fallback behind it. The ladder
/// runs from what we want to what every Android phone can do, and the last rung
/// is the platform default -- exactly what the trial recorded, which is known to
/// work on a real handset. Recording is never prevented by this file.
@immutable
class ClipEncoding {
  const ClipEncoding({
    required this.label,
    this.fps,
    this.bitrate,
    this.resolution = ResolutionPreset.high,
  });

  /// A short name for the rung, stored with the capture so the office can see
  /// what the phone actually agreed to.
  final String label;

  /// Frames per second, or null to let the platform choose.
  final int? fps;

  /// Video bitrate in bits per second, or null for the platform default.
  final int? bitrate;

  /// How many pixels, which is the one lever a handset cannot ignore.
  ///
  /// A frame rate can be refused and a bitrate can be accepted and discarded --
  /// the trial handset does exactly that -- but a camera opened at 480p records
  /// 480p, because the resolution is the session the platform negotiates rather
  /// than a hint passed to the encoder. It is the last rung of the ladder for
  /// that reason: it always works, and it is the only thing that always works.
  final ResolutionPreset resolution;

  /// Roughly how many pixels a frame carries, for comparing two rungs.
  ///
  /// Only the ordering matters, so the presets' own nominal heights stand in for
  /// the real figure -- which depends on the sensor and is not knowable until a
  /// camera has opened.
  int get _pixelRank => switch (resolution) {
    ResolutionPreset.low => 1,
    ResolutionPreset.medium => 2,
    ResolutionPreset.high => 3,
    ResolutionPreset.veryHigh => 4,
    ResolutionPreset.ultraHigh => 5,
    ResolutionPreset.max => 6,
  };

  /// Whether [other] records fewer pixels than this rung.
  bool coarserThan(ClipEncoding other) => other._pixelRank < _pixelRank;

  /// The expected size of one clip at this rung, for a clip of [seconds].
  int expectedBytes(int seconds) {
    final b = bitrate;
    if (b == null) return 0;
    return (b * seconds) ~/ 8;
  }

  @override
  String toString() =>
      'ClipEncoding($label, fps: $fps, bitrate: $bitrate, '
      'resolution: ${resolution.name})';
}

/// The ladder, best first.
///
/// Every rung below the last holds the same 2.5 Mbit/s, so every one of them
/// fits the 20 MB claim budget. What descends is the frame rate, and with it the
/// bits available to each frame:
///
///   preferred  15 fps -- 167 kbit a frame. Twice what the trial recorded, at a
///              fifth of the size.
///   steady     20 fps -- 125 kbit a frame. For encoders that refuse 15.
///   smooth     24 fps -- 104 kbit a frame. Cinema rate, very widely supported.
///   capped     platform frame rate, bitrate capped. The frame rate is the part
///              devices are fussy about; a bitrate alone is usually honoured.
///   device     whatever the phone does. What the trial recorded: it works, and
///              it is about 25 MB a clip.
///
/// An earlier version raised the bitrate as the frame rate rose, to keep
/// per-frame quality constant. That was the wrong thing to hold fixed: it put
/// the fallback rungs at 21, 25 and 29 MB a claim, so a phone that refused
/// 15 fps quietly broke the budget the whole ladder exists to meet. The budget
/// is the constraint; per-frame quality is what gives way.
///
/// # Why the ladder now ends in pixels
///
/// Every rung above asks for something the encoder is free to refuse, and the
/// trial handset refuses all of it: a Vivo V2253 asked for 2.5 Mbit/s recorded at
/// 8.5, and all four of those rungs produced the same 16 MB clip. Four fifteen
/// second clips is 64 MB against a 20 MB budget, and no amount of asking moves it.
///
/// Resolution is different in kind. It is not a hint handed to the encoder, it is
/// the capture session the platform negotiates, so a camera opened at 480p records
/// 480p. It is therefore the floor of the ladder rather than another rung of the
/// same sort, and it is where a phone that discards bitrates lands.
///
/// 480p is a real cost and worth stating: the office reads about a dozen still
/// keyframes out of each clip, and those frames go from 1280x720 to 640x480. It
/// buys roughly a 2 to 2.5 times smaller file, which brings a claim from 64 MB to
/// somewhere near 28 -- still over budget, and less than half of what the trial
/// uploaded. The fifteen seconds are not touched, because duration is what the
/// ground vision measurement reads to tell a thirty per cent loss from an eighty
/// per cent one, and that is the signal this whole walk exists to capture.
///
/// The last rung asks for nothing at all beyond 480p. It is the rung that cannot
/// fail for want of a profile, which is the property the bottom of this ladder
/// has to have: a farmer standing in a field whose phone will not record has lost
/// the claim, and that is not comparable to a slow upload.
const List<ClipEncoding> encodingLadder = <ClipEncoding>[
  ClipEncoding(label: 'preferred', fps: 15, bitrate: 2500000),
  ClipEncoding(label: 'steady', fps: 20, bitrate: 2500000),
  ClipEncoding(label: 'smooth', fps: 24, bitrate: 2500000),
  ClipEncoding(label: 'capped', bitrate: 2500000),
  ClipEncoding(
    label: 'half',
    fps: 15,
    bitrate: 2500000,
    resolution: ResolutionPreset.medium,
  ),
  ClipEncoding(
    label: 'half-capped',
    bitrate: 2500000,
    resolution: ResolutionPreset.medium,
  ),
  ClipEncoding(label: 'half-device', resolution: ResolutionPreset.medium),
];

/// The size one clip may reach before the next station steps down a rung.
///
/// Set above the preferred rung's 4.7 MB with room for a scene the encoder
/// finds hard -- wind in a canopy is genuinely expensive to compress -- so an
/// ordinary clip does not trigger it. A clip past this means the phone is not
/// honouring the bitrate, and the remaining stations should ask for less rather
/// than repeat it three more times.
const int clipStepDownBytes = 8 * 1024 * 1024;

/// How much smaller a clip must get for a step-down to count as having worked.
///
/// Nine tenths. A clip is not a fixed-size thing -- wind in a canopy is
/// genuinely more expensive to compress than still leaves -- so a few per cent
/// either way says nothing. A step that was honoured at all shows up as far more
/// than a tenth: dropping 15 fps to 20 at the same bitrate should not change the
/// size, but dropping the bitrate should change it in proportion.
const double _worthwhile = 0.9;

/// The whole-claim budget, for reporting against.
const int claimBudgetBytes = 20 * 1024 * 1024;

/// Opens a camera at the best encoding the phone will accept.
///
/// Stateful across a walk: once a rung has been established it is reused for
/// the remaining stations, and a clip that comes out oversized steps the rung
/// down for the ones after it.
class ClipEncoder {
  ClipEncoder({
    this.ladder = encodingLadder,
    Future<List<CameraDescription>> Function()? cameras,
    CameraController Function(CameraDescription, ClipEncoding)? build,
  }) : _cameras = cameras ?? availableCameras,
       _build = build ?? _defaultBuild;

  final List<ClipEncoding> ladder;
  final Future<List<CameraDescription>> Function() _cameras;
  final CameraController Function(CameraDescription, ClipEncoding) _build;

  int _rung = 0;

  /// Whether the clip about to be judged was recorded just after a step-down,
  /// and how big the one before it was. Together they answer the only question
  /// that matters about a step: did it help?
  bool _steppedBefore = false;
  int? _bytesBeforeStep;

  bool _bitrateIgnored = false;

  /// Whether this phone has been observed to discard the bitrate it is given.
  ///
  /// Worth surfacing rather than hiding: on such a handset the size of a claim
  /// cannot be fixed by asking for less, and somebody deciding between a lower
  /// resolution and a shorter clip needs to know that is the choice.
  bool get bitrateIgnored => _bitrateIgnored;

  /// The rung in use, once a camera has opened.
  ClipEncoding get encoding => ladder[_rung];

  /// Whether the phone refused everything we preferred and is recording at its
  /// own bitrate, so this claim will be large.
  bool get atDeviceDefault => _rung >= ladder.length - 1;

  static CameraController _defaultBuild(
    CameraDescription camera,
    ClipEncoding e,
  ) {
    return CameraController(
      camera,
      // The rung's own size. 720p at the top, because the office reads the frames
      // at that size and 1080p would double the upload for detail nothing
      // measures; 480p at the bottom, for a phone that gives us no other way to
      // spend less.
      e.resolution,
      // Audio off: a clip of somebody's field should not also be a recording of
      // whatever their family happened to be saying nearby.
      enableAudio: false,
      fps: e.fps,
      videoBitrate: e.bitrate,
    );
  }

  /// Opens the camera, stepping down the ladder until one initialises.
  ///
  /// Throws only when every rung fails, which means the phone has no working
  /// camera rather than a fussy encoder -- and that is worth surfacing, because
  /// nothing in this app can proceed without one.
  Future<CameraController> open() async {
    final cameras = await _cameras();
    if (cameras.isEmpty) {
      throw CameraException('no_camera', 'This phone reported no cameras.');
    }

    Object? last;
    for (var i = _rung; i < ladder.length; i++) {
      final e = ladder[i];
      CameraController? c;
      try {
        c = _build(cameras.first, e);
        await c.initialize();
        _rung = i;
        if (i > 0) {
          debugPrint(
            'clip encoding: the phone refused the preferred profile; '
            'recording at "${e.label}" (fps ${e.fps}, bitrate ${e.bitrate})',
          );
        }
        return c;
      } on CameraException catch (err) {
        last = err;
        // A refused permission is not a fussy encoder, and must not be reported
        // as one.
        //
        // Every rung fails when the camera permission is missing, so the loop
        // would try five profiles and tell the farmer "the camera would not
        // start at any recording quality" -- which reads as a broken phone, and
        // sends them looking for another handset instead of into Settings.
        //
        // AccessGate should have caught this before the walk opened. It can
        // still happen: a farmer can revoke the permission from Settings while
        // the walk screen is in the background.
        if (err.code == 'CameraAccessDenied' ||
            err.code == 'CameraAccessDeniedWithoutPrompt') {
          try {
            await c?.dispose();
          } catch (_) {
            // Nothing to do; the throw below is what matters.
          }
          rethrow;
        }
        // A controller that failed to initialise still holds a platform
        // camera on some devices, and the next rung cannot open one while it
        // does.
        try {
          await c?.dispose();
        } catch (_) {
          // Nothing useful to do: the next attempt will fail too and the loop
          // will report that instead.
        }
      } catch (err) {
        last = err;
        try {
          await c?.dispose();
        } catch (_) {
          // As above.
        }
      }
    }
    throw CameraException(
      'no_usable_profile',
      'The camera would not start at any recording quality: $last',
    );
  }

  /// Called with each finished clip so the walk adapts.
  ///
  /// Returns true where the rung was stepped down, which means the caller
  /// should reopen the camera before the next station.
  bool sawClip(int bytes) {
    final stepped = _steppedBefore;
    final before = _bytesBeforeStep;
    _steppedBefore = false;

    if (bytes <= clipStepDownBytes) return false;
    if (_rung >= ladder.length - 1) return false;

    // Once established, it does not need re-establishing. Without this the
    // finding was made and then forgotten, and the next oversized clip stepped
    // again -- which is the behaviour this whole check exists to stop.
    if (_bitrateIgnored) return false;

    // Did the last step-down actually do anything?
    //
    // Measured on a Vivo V2253: asked for 2.5 Mbit/s and got 8.5, and the four
    // rungs produced 15.99, 16.59, 16.49 and 16.47 MB. The encoder discards
    // videoBitrate entirely, so the ladder walked every rung for nothing -- and
    // worse than nothing, because each step reopens the camera between stations
    // and costs the farmer time.
    //
    // A step that did not shrink the clip means the setting is not being
    // honoured, and no further step of the same kind will be either.
    //
    // So stop asking, and change what is being asked for. Everything above the
    // resolution rungs is a request the encoder may discard, and this phone has
    // just demonstrated that it does; walking the rest of them costs a camera
    // reopen between stations and buys nothing. Pixels are not a request, so the
    // ladder jumps to the first rung that records fewer of them.
    //
    // This is what the earlier version stopped short of. It established the
    // finding, said so in a log nobody in a field reads, and left the remaining
    // three clips at 16 MB each -- a correct diagnosis followed by no treatment.
    if (stepped && before != null && bytes > before * _worthwhile) {
      final coarser = _firstCoarserRung();
      if (coarser != null) {
        debugPrint(
          'clip encoding: stepping down changed nothing ($before then $bytes '
          'bytes), so this phone is ignoring the bitrate it was asked for. '
          'Asking for fewer pixels instead: "${ladder[coarser].label}".',
        );
        _rung = coarser;
        _steppedBefore = true;
        _bytesBeforeStep = bytes;
        return true;
      }

      // Already recording the fewest pixels the ladder offers, and still over.
      // Nothing left that this file controls: the remaining levers are the clip's
      // duration and how many clips a claim carries, and both of those are
      // decisions about evidence rather than about encoding.
      _bitrateIgnored = true;
      debugPrint(
        'clip encoding: stepping down changed nothing ($before then $bytes '
        'bytes) and there are no fewer pixels to ask for. Staying at '
        '"${encoding.label}" rather than reopening the camera for no gain.',
      );
      return false;
    }

    _rung++;
    _steppedBefore = true;
    _bytesBeforeStep = bytes;
    debugPrint(
      'clip encoding: a clip came out at $bytes bytes, over the '
      '$clipStepDownBytes ceiling; stepping to "${ladder[_rung].label}"',
    );
    return true;
  }

  /// The next rung down that records fewer pixels than the current one.
  ///
  /// Null where there is none, which is the bottom of the ladder. Searched rather
  /// than hard-coded so the ladder stays the one place that describes itself: a
  /// rung inserted or removed changes where this lands and nothing else has to be
  /// told about it.
  int? _firstCoarserRung() {
    for (var i = _rung + 1; i < ladder.length; i++) {
      if (ladder[_rung].coarserThan(ladder[i])) return i;
    }
    return null;
  }

  /// Whether [total] bytes across a claim has passed the budget.
  static bool exceededBudget(int total) => total > claimBudgetBytes;
}

/// The size on disk of a recorded clip, or zero where it cannot be read.
Future<int> clipBytes(String path) async {
  try {
    return await File(path).length();
  } catch (_) {
    // A clip whose size cannot be read is still a clip; it is only the
    // adaptation that loses its input.
    return 0;
  }
}
