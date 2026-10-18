import 'dart:math' as math;

import 'api.dart';

/// Deciding what an answer from the office means for the work that asked.
///
/// # The rule this file exists to enforce
///
/// Work is given up only when the office has refused it. Never because it was
/// tried a number of times.
///
/// The queue used to stop after five attempts, and it counted a server error
/// as an attempt. With the foreground timer firing every thirty seconds, five
/// server errors took two and a half minutes -- so a short outage on the
/// morning after a district-wide event, exactly when every handset in the
/// district regains signal together, left every farmer's claim sitting on their
/// phone, no longer being tried, with nothing on the screen to say so.
///
/// A server that is failing, slow or overloaded says nothing about the claim.
/// Such work waits, for longer each time, and tries again.
///
/// # Why the waits are random
///
/// A tower restored after a storm brings a village back online in the same
/// second. If every phone waited exactly thirty seconds, then sixty, then two
/// minutes, they would all arrive together at each of those instants, and the
/// server would see the same spike again and again, each one as likely to fail
/// as the last. Spreading each wait across a range breaks the lockstep.
enum Verdict {
  /// Accepted.
  sent,

  /// Nothing was reached. Stop the pass; keep everything as it is.
  offline,

  /// The office could not deal with it right now. Wait and try again.
  transient,

  /// The office asked every request from this phone to wait. Stop the pass.
  throttled,

  /// This build of the app is too old to send. Stop and keep the queue until
  /// the farmer updates.
  upgradeRequired,

  /// The office would not say who this phone is. Stop the pass and keep
  /// everything: an authorisation problem is never a verdict on the data.
  ///
  /// Separated from [permanent] deliberately. A 401 means a token the server
  /// would not accept, and a 403 means a grant it did not find -- and neither is
  /// a statement about the clip. Both are fixed by signing in again or by the
  /// office issuing the grant, at which point the same bytes are perfectly
  /// acceptable.
  ///
  /// Treated as permanent, they marked a farmer's recording `failed`, which is
  /// terminal: `pendingCaptures` excludes it and nothing ever retries. The trial
  /// had a grant bug that produced exactly these codes on a neighbouring route,
  /// and a capture that had hit it would have been abandoned with no way back.
  unauthorized,

  /// The office refused this item for a reason retrying will not change.
  permanent,
}

/// Codes a 409 carries that mean "not yet", not "no".
const _retryableConflicts = {
  // Another copy of this very request is still being processed.
  'IDEMPOTENCY_IN_PROGRESS',
  // The server discarded a corrupt upload and wants it sent again.
  'UPLOAD_RESTARTED',
  // The district has paused its own event and means to resume it. Reading
  // this as a refusal marked the farmer's claim failed and stopped sending
  // it -- permanently, over a state somebody intended to undo that
  // afternoon.
  'EVENT_PAUSED',
};

/// Refusals a farmer fixes by recording the point again somewhere else.
///
/// # Why this set exists
///
/// Resending the same bytes cannot change any of these answers -- the office is
/// refusing where the recording was made, and the file carries that with it.
/// Three clips of one claim were refused SAME_POSITION on every pass, and the
/// only thing the app put in front of the farmer was a retry button, so each
/// tap produced the same refusal. They had four recordings and a claim with
/// one.
///
/// So these are separated from the refusals nobody can act on. A farmer holding
/// one of them should be offered the camera, not the queue.
///
/// MOCKED_POSITION is deliberately absent. It is fixed by turning off a mock
/// location app, not by walking; re-recording with one still running produces
/// the identical refusal, and sending a farmer back into the field for that
/// would be the retry treadmill with a longer walk.
const recordAgainRefusals = {
  // Recorded where another point of the same walk already was.
  'SAME_POSITION',
  // Recorded outside the parcel's own boundary.
  'OUTSIDE_PARCEL',
  // Recorded somewhere other than where the rest of the claim was gathered.
  'AWAY_FROM_FIELD',
  // Recorded on ground another claim on the same survey number already filmed.
  //
  // A survey number is routinely shared: one farmer's soybean and udad are two
  // claims on it, and a neighbour's holding is a third. The remedy is the camera,
  // like the others, but somewhere quite different -- the farmer's own share of
  // the survey number rather than further along inside this claim.
  'POINT_TAKEN_IN_SURVEY',
};

/// Whether [code] names a refusal re-recording fixes.
bool recordAgainFixes(String? code) =>
    code != null && recordAgainRefusals.contains(code);

/// What an answer means.
Verdict classify(Result r) {
  if (r.offline) return Verdict.offline;
  if (r.ok) return Verdict.sent;

  final status = r.status;
  if (status == 426) return Verdict.upgradeRequired;

  // Asked to slow down, with a time: every request from this phone should
  // honour it, not only the one that was told.
  if ((status == 429 || status == 503) && r.retryAfter != null) {
    return Verdict.throttled;
  }

  if (status >= 500) return Verdict.transient;
  if (status == 408 || status == 425 || status == 429) return Verdict.transient;
  if (status == 409 && _retryableConflicts.contains(r.code)) {
    return Verdict.transient;
  }
  // The server swept an upload it thought abandoned. Not a verdict on the
  // clip: the next pass begins it again under the same key.
  if (status == 404 && r.code == 'UPLOAD_NOT_FOUND') return Verdict.transient;

  // Who this phone is, not what it sent.
  //
  // These used to fall through to permanent, which marks a capture `failed` --
  // terminal, never retried, the farmer's recording gone. A 401 is a token the
  // server would not accept and a 403 is a grant it did not find; the bytes are
  // fine in both cases and are acceptable the moment somebody signs in or the
  // grant is issued.
  //
  // It was not reachable, and only just: a 401 is caught a layer up, where a
  // failed refresh is reported as offline, and offline stops the pass before a
  // second item can be tried with a dead token. That is three separate facts
  // holding at once, and any one of them changing would reopen a silent
  // data-loss path. Classified here instead, so it cannot.
  if (status == 401 || status == 403) return Verdict.unauthorized;

  return Verdict.permanent;
}

/// How long to wait before trying an item again.
class RetryPolicy {
  const RetryPolicy({
    this.base = const Duration(seconds: 30),
    this.cap = const Duration(hours: 6),
  });

  /// The first wait, before jitter.
  final Duration base;

  /// The longest wait. Six hours: a handset out of signal overnight tries a
  /// few times through the night and is ready when the farmer wakes, rather
  /// than waiting a day because it doubled past sense.
  final Duration cap;

  /// The wait after [attempts] failed tries.
  ///
  /// Exponential, capped, with equal jitter: half the wait is fixed and half
  /// is random. Pure random waits can come out at zero, which is a retry storm
  /// with extra steps; half-fixed keeps a floor while still spreading a crowd
  /// of phones across the other half.
  ///
  /// A server that said how long to wait is believed, if it asked for longer.
  Duration delay(int attempts, {Duration? retryAfter, math.Random? random}) {
    final rnd = random ?? math.Random();
    final exponent = math.min(attempts, 20);
    final raw = base.inMilliseconds * math.pow(2, exponent);
    final capped = math.min(raw, cap.inMilliseconds.toDouble()).toInt();
    final half = capped ~/ 2;
    final jittered = half + (half > 0 ? rnd.nextInt(half + 1) : 0);
    final wait = Duration(milliseconds: jittered);
    if (retryAfter != null && retryAfter > wait) {
      return retryAfter > cap ? cap : retryAfter;
    }
    return wait;
  }
}
