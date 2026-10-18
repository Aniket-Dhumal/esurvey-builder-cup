import 'dart:ui' as ui;

import 'package:flutter/foundation.dart';
import 'package:image/image.dart' as img;

/// Turning what the camera saw into something that fits down a farm's link.
///
/// # Why a still is resized at all
///
/// A handset's still is eight to twelve megapixels and three or four megabytes. On
/// the link this feature is built for -- a quarter of a megabit up, which is what a
/// village gets -- that is a minute and a half of uploading, during which the farmer
/// is standing in a field waiting for an assistant that has not been given anything
/// to look at yet. The relay caps a frame at eighty kilobytes for exactly this
/// reason, and refuses anything larger rather than letting one frame eat a
/// conversation.
///
/// # Why 640 pixels
///
/// Because of what is being asked. The assistant is looking at a canopy for colour
/// and at a pod for whether it filled -- differences that survive a small picture --
/// and the model works at a few hundred pixels internally regardless. A larger frame
/// costs seconds on the link and buys nothing the model reads.
///
/// # Why the platform scales and this only encodes
///
/// Decoding a twelve megapixel JPEG in Dart takes seconds on a 2 GB handset, which is
/// the handset this runs on. [ui.instantiateImageCodec] does it in the platform's own
/// decoder and will scale while decoding, so the large picture is never a Dart object
/// at all; what reaches the encoder here is already 640 pixels wide, and encoding
/// that is tens of milliseconds.
const int liveFrameWidth = 640;

/// The relay's ceiling, restated here so this can aim below it.
///
/// Duplicated deliberately rather than imported from the relay, which is Python. It
/// is asserted against the relay's own constant by a test, so the two cannot drift
/// without something failing.
const int liveFrameMaxBytes = 80 * 1024;

/// The qualities tried, in order, until one fits.
///
/// Stepping down rather than guessing once: a picture of a bare field compresses to
/// nothing and a picture of a dense canopy does not, so the same quality gives wildly
/// different sizes and only measuring the result tells you which you got.
const List<int> liveFrameQualities = [70, 55, 40];

/// A JPEG of [original], small enough for the relay, or null if it cannot be made.
///
/// Null rather than a large frame. A frame the relay refuses has cost the farmer the
/// upload and bought nothing, and the conversation continues by voice -- which is
/// most of its value -- so failing to produce one is not worth interrupting anybody
/// over.
Future<Uint8List?> liveFrameFrom(Uint8List original) async {
  try {
    final rgba = await _scaledRgba(original);
    if (rgba == null) return null;
    return await compute(_encodeUnder, rgba);
  } catch (e) {
    debugPrint('[live frame] the still could not be prepared: $e');
    return null;
  }
}

/// The still, scaled by the platform, as raw pixels.
Future<_Rgba?> _scaledRgba(Uint8List original) async {
  final codec = await ui.instantiateImageCodec(
    original,
    targetWidth: liveFrameWidth,
  );
  final frame = await codec.getNextFrame();
  final image = frame.image;
  try {
    final data = await image.toByteData(format: ui.ImageByteFormat.rawRgba);
    if (data == null) return null;
    return _Rgba(
      bytes: data.buffer.asUint8List(),
      width: image.width,
      height: image.height,
    );
  } finally {
    // The platform holds the pixels until this is called, and a walk sends several
    // frames. Released in a finally because an encode that throws should not also
    // leak a bitmap.
    image.dispose();
    codec.dispose();
  }
}

/// Encode, stepping the quality down until it fits.
///
/// Runs in an isolate -- this is the one part that is Dart doing pixel work, and on a
/// cheap handset it is long enough to drop frames from the camera preview if it ran
/// on the main thread. A farmer watching the preview stutter while the assistant
/// thinks would read it as the app hanging.
Uint8List? _encodeUnder(_Rgba rgba) {
  final image = img.Image.fromBytes(
    width: rgba.width,
    height: rgba.height,
    bytes: rgba.bytes.buffer,
    numChannels: 4,
  );
  for (final quality in liveFrameQualities) {
    final encoded = img.encodeJpg(image, quality: quality);
    if (encoded.length <= liveFrameMaxBytes) return encoded;
  }
  // Even the lowest quality was too large, which means a picture of something very
  // detailed at 640 pixels. Dropped rather than sent to be refused.
  return null;
}

/// Raw pixels, sized, on their way to an isolate.
///
/// A plain holder because that is all that can cross: a ui.Image cannot be sent to an
/// isolate, which is why the scaling happens on this side and the bytes travel.
class _Rgba {
  const _Rgba({required this.bytes, required this.width, required this.height});

  final Uint8List bytes;
  final int width;
  final int height;
}
