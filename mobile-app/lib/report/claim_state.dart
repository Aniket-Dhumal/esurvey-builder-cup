import '../core/db.dart';
import 'journey.dart';

/// What a claim on this phone actually is, and what happens to it next.
///
/// # Why this is derived and not read
///
/// The `journeys` table stores a `stage`, and for a long time every screen read
/// it and believed it. It is a label written after the fact, by an app that can
/// be killed between two writes, and the facts it describes live in other tables:
/// whether the draft has been released into the queue, whether the office has
/// given it a reference, how much evidence is on the phone, whether any of that
/// evidence was refused.
///
/// Every time those disagreed, a farmer lost something:
///
///   * A walk finished and the app killed before the stage moved left a claim
///     with all four clips that nothing would send and nothing would mention --
///     two of them on the first handset to try it.
///   * A journey marked `done` whose draft was never released was hidden from the
///     unfinished list by its own label while sitting outside the queue.
///   * A claim the office had already accepted was offered back for review,
///     which would have filed it twice.
///
/// So nothing here trusts the label. The state is computed from the rows, the
/// label is a hint at most, and a disagreement resolves towards whatever the
/// farmer's work actually is.
///
/// # The machine
///
///     form ──────────► evidence ──────────► filed ─────► settled
///       │                  │                  ▲             ▲
///       │                  │                  │             │
///       │                  └── all clips ─────┘             │
///       │                      taken: released              │
///       │                      into the queue               │
///       │                                                   │
///       └── abandoned ───────────────────────────► (deleted) │
///                                                            │
///     refused ◄── the office would not take some evidence ────┘
///
/// The only transition a farmer performs deliberately is form → evidence. The
/// rest follow from work being done: finishing the walk files the claim, the
/// queue settles it, and a refusal the farmer can act on pulls it sideways into
/// [ClaimStage.refused] until they have.
enum ClaimStage {
  /// The form is part-filled. Nothing has been claimed yet.
  form,

  /// The claim exists on this phone and is short of its evidence.
  evidence,

  /// Complete and released into the queue. The office may not have it yet -- a
  /// fortnight in a dead zone is an ordinary wait -- but nothing more is asked
  /// of the farmer.
  filed,

  /// The office has it, by reference. Nothing on this phone is owed.
  settled,

  /// Some evidence was refused for a reason only the farmer can fix, by
  /// recording it again somewhere else. The claim may already be filed: a claim
  /// can be both accepted and short of a clip.
  refused,
}

/// A claim, as the rows on this phone describe it.
class ClaimState {
  const ClaimState({
    required this.stage,
    required this.journey,
    required this.clipsDone,
    required this.clipsWanted,
    required this.refusedClips,
    required this.filedReference,
  });

  /// Work out what a claim is from what is actually recorded about it.
  ///
  /// [draft] is the row from `drafts`, which is where filing really shows: its
  /// status leaves `holding` when the claim is released into the queue, and its
  /// `server_id` arrives when the office answers. [progress] counts the evidence
  /// on the phone. [refusedClips] is how many of this claim's clips the office
  /// refused for a reason the farmer can fix.
  ///
  /// A null [draft] is a claim whose own row has gone -- a cleared sign-out
  /// caught mid-write, or a delete that half-happened. Treated as [form]: there
  /// is nothing to send and nothing to claim, and the journey is the only thing
  /// left to tidy away.
  factory ClaimState.derive({
    required Journey journey,
    required Map<String, Object?>? draft,
    required EvidenceProgress? progress,
    int refusedClips = 0,
  }) {
    final reference = '${draft?['reference'] ?? ''}';
    final serverId = '${draft?['server_id'] ?? ''}';
    final status = '${draft?['status'] ?? ''}';
    final held = status == Store.statusHolding;
    final done = progress?.done ?? 0;
    final wanted = progress?.total ?? 0;

    ClaimStage stage;
    if (draft == null) {
      stage = ClaimStage.form;
    } else if (serverId.isNotEmpty) {
      // The office answered. That is the strongest fact available and it
      // outranks every label: a claim with a reference has been accepted, and
      // offering it for review again would file it twice.
      stage = ClaimStage.settled;
    } else if (!held && status != 'failed') {
      // Released into the queue and not yet answered. Whether it has left the
      // phone is the queue's business, not the farmer's.
      stage = ClaimStage.filed;
    } else if (journey.stage == 'form' && done == 0) {
      // Still being typed. Guarded on the evidence too: a journey whose label
      // says `form` but which has clips against it was interrupted after the
      // walk began, and sending the farmer back to the form would lose them.
      stage = ClaimStage.form;
    } else {
      stage = ClaimStage.evidence;
    }

    // A refusal the farmer can fix pulls the claim sideways, whatever else is
    // true of it -- including a claim the office has already accepted. One was:
    // it had its reference and three of its four clips had been refused, and
    // because the label said `done` nothing on the phone said so.
    if (refusedClips > 0 && stage != ClaimStage.form) {
      stage = ClaimStage.refused;
    }

    return ClaimState(
      stage: stage,
      journey: journey,
      clipsDone: done,
      clipsWanted: wanted,
      refusedClips: refusedClips,
      filedReference: reference,
    );
  }

  final ClaimStage stage;
  final Journey journey;

  /// Clips standing against this claim, and how many it wants. A refused clip
  /// does not stand -- see Store.stationsComplete.
  final int clipsDone;
  final int clipsWanted;

  /// How many clips the office refused for a reason the farmer can fix.
  final int refusedClips;

  /// The office's own reference, once it has answered.
  final String filedReference;

  /// Whether the farmer still owes this claim work in a field.
  ///
  /// The question the home screen is really asking when it decides what to put
  /// in front of somebody. A filed claim owes nothing even though it may not have
  /// left the phone; a refused one owes a walk even though it has been accepted.
  bool get owesFieldWork =>
      stage == ClaimStage.form ||
      stage == ClaimStage.evidence ||
      stage == ClaimStage.refused;

  /// Whether finishing the evidence is all that is left.
  ///
  /// True where the walk is short. False for a claim that is merely waiting on a
  /// network, which is not something a farmer can help with and must never be
  /// shown to them as unfinished work.
  bool get evidenceIncomplete =>
      stage == ClaimStage.evidence && clipsDone < clipsWanted;

  /// Whether the evidence is whole and the claim should already have gone.
  ///
  /// The state that cost two farmers their walk: every clip taken, and the draft
  /// never released, so the queue did not have it and the unfinished list did not
  /// show it. Reaching this now means the release did not happen -- the app was
  /// killed between the last clip and the write -- and the answer is to release
  /// it rather than to ask the farmer for anything.
  bool get shouldHaveBeenFiled =>
      stage == ClaimStage.evidence &&
      clipsWanted > 0 &&
      clipsDone >= clipsWanted;
}
