import 'dart:io';
import 'dart:math' as math;

import 'package:crypto/crypto.dart';

import 'api.dart';
import 'retry.dart';

/// Sending a video off a handset that keeps losing signal.
///
/// # Why this is not one request
///
/// A report now carries four clips, about forty-eight megabytes. Sent whole,
/// a connection that drops at eighty per cent starts again at nothing -- and
/// on 2G, where the whole file takes over two hours, "starts again" means it
/// never finishes. The farmer is eventually told their claim failed, with no
/// way to see that it failed for want of a network rather than for want of
/// evidence.
///
/// So the file goes up in chunks. The server acknowledges each one and says
/// which it wants next; this asks, and continues from there. A pass that
/// manages three chunks before the signal goes has still made progress, and
/// the next pass keeps it.
///
/// # What makes it safe to retry
///
/// Every upload carries a key the handset chose. Sending it again resumes the
/// upload already in progress rather than starting a second one, so a reply
/// lost on a bad connection costs nothing. Without that, the retry that is
/// supposed to rescue the upload is what duplicates it.

/// How far one upload got.
class UploadOutcome {
  const UploadOutcome({
    required this.complete,
    required this.sentBytes,
    this.captureId,
    this.permanentFailure,
    this.permanentCode,
    this.verdict,
    this.retryAfter,
  });

  final bool complete;
  final int sentBytes;
  final String? captureId;

  /// What stopped an incomplete upload: no network, the office busy or
  /// failing, a request to slow down, or a build too old to send. Null when
  /// complete, or when the answer was unreadable and the upload simply stopped.
  final Verdict? verdict;

  /// How long the office asked this phone to wait, when it said.
  final Duration? retryAfter;

  /// Set when the server refused for a reason retrying will not fix -- outside
  /// the parcel, a mocked position, a file that did not match its hash. The
  /// queue records it and stops, rather than spending a farmer's battery
  /// being told the same thing five times.
  final String? permanentFailure;

  /// The office's stable code for that refusal, kept beside the message.
  ///
  /// The message is for the farmer to read; this is for the app to act on.
  /// Some refusals are fixed by recording the same point somewhere else --
  /// SAME_POSITION, OUTSIDE_PARCEL, AWAY_FROM_FIELD -- and the only honest
  /// thing to offer a farmer holding one of those is the camera, not a retry
  /// button that will be refused identically for ever. Deciding that from the
  /// message would mean matching on prose the office is free to reword.
  final String? permanentCode;

  bool get shouldRetry => !complete && permanentFailure == null;
}

/// Bytes per chunk.
///
/// # Why this is 512 KB and not 2 MB
///
/// It was 2 MB, and on the trial the handset changed IP twice mid-upload as it
/// hopped between cells. Each hop killed the TCP connection and produced
/// `reading the chunk: unexpected EOF` at the server, an HTTP 500, and a retry
/// that re-sent the whole chunk. At the throughput actually observed -- 100 to
/// 200 KB/s, with one chunk taking 49.8 s before dying -- a 2 MB chunk is
/// between ten and twenty seconds of exposure, and a village connection drops
/// inside that window often enough to matter.
///
/// 512 KB is three to five seconds. A dropped minute now costs seconds of
/// re-sending rather than megabytes, and the whole claim is 20 MB, so this is
/// forty requests rather than ten -- a cost paid in round trips, which are
/// cheap, to avoid one paid in re-sent bytes over a connection that is not.
///
/// Smaller would be worse: at 64 KB the per-request overhead and the server's
/// per-chunk bookkeeping start to dominate, and a clip becomes hundreds of
/// requests for no gain in resilience.
const int chunkBytes = 512 * 1024;

/// An incomplete outcome for an answer that was not a success.
UploadOutcome _stopped(Result r, int sent) {
  final verdict = classify(r);
  if (verdict == Verdict.permanent) {
    return UploadOutcome(
      complete: false,
      sentBytes: sent,
      permanentFailure: _reasonOf(r),
      permanentCode: r.code,
      verdict: verdict,
    );
  }
  return UploadOutcome(
    complete: false,
    sentBytes: sent,
    verdict: verdict,
    retryAfter: r.retryAfter,
  );
}

String _reasonOf(Result r) {
  final err = r.body?['error'];
  if (err is Map && err['message'] is String) return err['message'] as String;
  return 'the office refused this upload (${r.status})';
}

class Uploader {
  Uploader({required this.api, this.farmerGrant});

  final Api api;

  /// Which of the account's farmers this evidence belongs to, for a farmer
  /// whose registered number is not the one that signed in. The office
  /// resolves the claim through that farmer's scope, so a clip sent without it
  /// is answered "no such report".
  final String? farmerGrant;

  /// Sends one file, resuming if it has been tried before.
  ///
  /// [uploadKey] must be stable for a given file across retries -- the local
  /// row id is the natural choice. A key that changed per attempt would make
  /// every retry a fresh upload, which is the bug this whole mechanism exists
  /// to avoid.
  Future<UploadOutcome> send({
    required String reportId,
    required String uploadKey,
    required File file,
    required String contentType,
    required Map<String, Object?> metadata,
    String? knownSha256,
    Future<void> Function()? onChunk,
    bool Function()? shouldStop,
  }) async {
    if (!await file.exists()) {
      return const UploadOutcome(
        complete: false,
        sentBytes: 0,
        permanentFailure: 'the file is no longer on this phone',
      );
    }

    final total = await file.length();
    // The hash taken when the clip was saved. Re-hashing sixty megabytes on
    // every pass, on a phone, spends battery to learn what the row already
    // says.
    final digest = (knownSha256 != null && knownSha256.length == 64)
        ? knownSha256
        : await _sha256Of(file);

    final begun = await api.postJson('/api/v1/reports/$reportId/uploads', {
      ...metadata,
      'upload_key': uploadKey,
      'content_type': contentType,
      'total_bytes': total,
      'chunk_size': chunkBytes,
      'sha256': digest,
    }, farmerGrant: farmerGrant);

    if (!begun.ok) return _stopped(begun, 0);
    final body = begun.body;
    if (body == null) {
      return const UploadOutcome(complete: false, sentBytes: 0);
    }

    final uploadId = '${body['id'] ?? ''}';
    if (uploadId.isEmpty) {
      return const UploadOutcome(complete: false, sentBytes: 0);
    }

    // Already finished on a previous pass whose reply never arrived.
    final existingCapture = '${body['capture_id'] ?? ''}';
    if (existingCapture.isNotEmpty) {
      return UploadOutcome(
        complete: true,
        sentBytes: total,
        captureId: existingCapture,
      );
    }

    var next = (body['next_chunk'] as num?)?.toInt() ?? 0;
    var sent = (body['received_bytes'] as num?)?.toInt() ?? 0;

    // The size the *server* is counting in, not the size this build prefers.
    //
    // The server assembles by concatenating the parts it was given, in order, so
    // the slices of one upload have to be consistent with each other -- and
    // `next_chunk` counts them at whatever size the upload was begun with.
    //
    // This build asks for 512 KB where an earlier one asked for 2 MB. A farmer
    // who upgrades part-way through an upload resumes one the server is counting
    // in 2 MB chunks: computing the offset from this build's constant would slice
    // at 1.5 MB where the server already holds 6, and the assembled file would be
    // wrong. The server's own checksum catches that and restarts the upload, so
    // nothing is lost -- but it costs the farmer the whole clip again over a
    // village connection, which is the trade this whole release exists to avoid.
    //
    // Honouring the server's figure makes the resume correct rather than
    // self-healing.
    final size = (body['chunk_size'] as num?)?.toInt() ?? chunkBytes;
    final step = size > 0 ? size : chunkBytes;

    final handle = await file.open();
    try {
      while (sent < total) {
        // A background pass has minutes, not hours. Stopping between chunks
        // is free: the server has every chunk acknowledged so far.
        if (shouldStop?.call() ?? false) {
          return UploadOutcome(complete: false, sentBytes: sent);
        }
        final offset = next * step;
        if (offset >= total) break;

        await handle.setPosition(offset);
        final take = math.min(step, total - offset);
        final chunk = await handle.read(take);

        final result = await api.putBytes(
          '/api/v1/reports/$reportId/uploads/$uploadId/chunks/$next',
          chunk,
          farmerGrant: farmerGrant,
        );

        if (!result.ok) {
          // Offline, busy, restarted, throttled or refused. The bytes already
          // acknowledged stay acknowledged, and the next pass asks where it
          // got to -- which is also what finishes an upload whose assembly the
          // server could not complete the first time.
          return _stopped(result, sent);
        }

        final state = result.body;
        if (state == null) {
          return UploadOutcome(complete: false, sentBytes: sent);
        }

        // Taken from the server rather than counted here. If the two ever
        // disagree the server is right -- it is the one holding the bytes --
        // and following it is what makes a lost acknowledgement harmless.
        next = (state['next_chunk'] as num?)?.toInt() ?? next + 1;
        sent = (state['received_bytes'] as num?)?.toInt() ?? sent + take;
        // One clip over 2G outlasts the device-wide sync lease; renewing it
        // per chunk stops the other process starting a second pass that
        // uploads the same file alongside this one.
        await onChunk?.call();

        final captureId = '${state['capture_id'] ?? ''}';
        if (captureId.isNotEmpty) {
          return UploadOutcome(
            complete: true,
            sentBytes: sent,
            captureId: captureId,
          );
        }
      }
    } finally {
      await handle.close();
    }

    return UploadOutcome(complete: false, sentBytes: sent);
  }

  /// Hashes the file without loading it into memory.
  ///
  /// A fifty-megabyte read on a handset that is also holding a camera preview
  /// is how an app gets killed by the system mid-report.
  Future<String> _sha256Of(File file) async {
    final digest = await sha256.bind(file.openRead()).first;
    return digest.toString();
  }
}
