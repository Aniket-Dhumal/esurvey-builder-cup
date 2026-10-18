import 'dart:io';

import 'package:flutter/foundation.dart';
import 'package:path/path.dart' as p;
import 'package:path_provider/path_provider.dart';

import 'db.dart';

/// Where the photographs live, and whether there is room for more.
///
/// # Why this matters more here than in most apps
///
/// The handsets this runs on ship with 8 or 16 GB and are full of other
/// people's forwards. A farmer who is four photographs into a six-photograph
/// sequence when the phone runs out has lost the walk to the field, and will
/// find out by way of a Dart exception in a snack bar.
///
/// So there are two guards. One asks before the sequence starts, when there
/// is still time to delete something. The other catches the write that fails
/// anyway, and makes sure it does not leave rubbish behind.
class Photos {
  const Photos._();

  static const _folder = 'captures';

  /// Roughly four 720p frames.
  ///
  /// Not a guess at what the sequence needs -- it is a floor. A phone that
  /// cannot find this much has no business starting a capture, and one that
  /// can may still fill up later, which is what the write guard is for.
  static const probeBytes = 2 * 1024 * 1024;

  /// The photograph directory, created if it is not there.
  ///
  /// [into] overrides where it lives. Only tests pass it: the real answer
  /// comes from path_provider, which needs a platform channel and so cannot
  /// be reached from a unit test -- and the parts worth testing here are what
  /// happens when a write fails and which files survive a sweep, neither of
  /// which is about the path.
  static Future<Directory> directory({Directory? into}) async {
    final base = into ?? await getApplicationDocumentsDirectory();
    final dir = Directory(p.join(base.path, _folder));
    if (!dir.existsSync()) dir.createSync(recursive: true);
    return dir;
  }

  /// Whether a photograph can still be written.
  ///
  /// # Why this writes a file instead of asking how much space is free
  ///
  /// Free-space APIs need a platform plugin, and they answer a slightly
  /// different question than the one that matters: a filesystem can report
  /// space that a quota, a permission, or a read-only remount will not
  /// actually give you. Writing two megabytes to the exact directory the
  /// photographs go in tests the operation itself, costs a few milliseconds,
  /// and cannot be wrong about its own result.
  static Future<bool> hasRoom({int bytes = probeBytes, Directory? into}) async {
    File? probe;
    try {
      final dir = await directory(into: into);
      probe = File(p.join(dir.path, '.probe'));
      // One allocation rather than a loop: a filesystem that can take this in
      // a single write can take a JPEG.
      await probe.writeAsBytes(Uint8List(bytes), flush: true);
      return true;
    } catch (e) {
      debugPrint('no room for photographs: $e');
      return false;
    } finally {
      try {
        if (probe != null && probe.existsSync()) probe.deleteSync();
      } catch (_) {
        // Leaving a probe file behind is not worth failing over; the next
        // sweep takes it.
      }
    }
  }

  /// Delete photograph files that no row points at.
  ///
  /// # How they come to exist
  ///
  /// A photograph is written to disk and then recorded in the database, and
  /// those are two operations. If the second fails -- and the most likely
  /// reason it fails is the disk being full, which is exactly when this
  /// matters -- the file is left with nothing referencing it. It will never
  /// be uploaded, because the queue is made of rows, and it will never be
  /// deleted, because deletion happens when a row is sent.
  ///
  /// So they accumulate silently, on the phone least able to afford it, in
  /// the failure mode that is already about running out of space.
  ///
  /// Rows are read first and the directory second. Getting that order wrong
  /// would race a capture in flight: a file listed before its row was written
  /// would look like an orphan and be deleted out from under a farmer who had
  /// just taken it.
  static Future<int> sweepOrphans(Store store, {Directory? into}) async {
    try {
      final live = (await store.allCaptureFilePaths())
          .map((path) => p.basename(path))
          .toSet();

      final dir = await directory(into: into);
      var removed = 0;

      for (final entity in dir.listSync()) {
        if (entity is! File) continue;
        final name = p.basename(entity.path);
        if (live.contains(name)) continue;

        try {
          entity.deleteSync();
          removed++;
        } catch (e) {
          debugPrint('could not remove orphan $name: $e');
        }
      }

      if (removed > 0) debugPrint('swept $removed orphaned photographs');
      return removed;
    } catch (e) {
      // Housekeeping. It must never be the reason the app fails to start.
      debugPrint('orphan sweep failed: $e');
      return 0;
    }
  }
}
