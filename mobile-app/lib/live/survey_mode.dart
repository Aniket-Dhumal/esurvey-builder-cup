import '../core/api.dart';
import '../core/link_quality.dart';
import '../core/live_consent.dart';

/// Which engine a survey runs on, decided once before it starts.
///
/// # Why once, and why at the start
///
/// Because a farmer walking a field must not have the app change character
/// underneath them. Decided per screen, the assistant appeared at one point of the
/// walk and vanished at the next as the signal moved -- and a farmer who was being
/// talked to, then was not, has no way to read that except as a fault. It also
/// meant measuring the link again and again on a connection the measurement is
/// there to be careful about.
///
/// So the question is asked in one place, at the farm, before the first clip: can
/// this survey be a conversation, or is it the scripted walk? Whatever the answer,
/// it holds for the whole survey.
///
/// # Why at the farm and not at the form
///
/// Because the answer is about where the farmer is standing. A form filled at home
/// on wifi says nothing about the field, and the field is where every clip is
/// recorded. This is decided after the gate that establishes they have arrived.
///
/// # Why offline is not a failure
///
/// It is the design. Every instruction needed to file a claim is scripted and needs
/// no network: the walk, the spacing rule, the four clips, the filing and the
/// queue. Live guidance is an addition on top of a system that is complete without
/// it, which is why this can answer "offline" without telling the farmer anything
/// has gone wrong.
enum SurveyEngine {
  /// The scripted walk. No model, no microphone, no network needed.
  offline,

  /// The scripted walk, and a conversation alongside it.
  live,
}

/// What was decided, and on what evidence.
class SurveyMode {
  const SurveyMode({
    required this.engine,
    this.link = const LinkQuality.none(),
    this.because,
  });

  /// The offline walk, with nothing measured. What a build without live guidance
  /// gets, and what every failure to decide falls back to.
  const SurveyMode.offline({this.because})
    : engine = SurveyEngine.offline,
      link = const LinkQuality.none();

  final SurveyEngine engine;

  /// What the link measured, kept for the walk rather than measured again there.
  final LinkQuality link;

  /// Why it is offline, in the language of the place, or null where it is live.
  ///
  /// Held rather than shown everywhere: the walk says it once if it says it at
  /// all. A farmer does not need to be told, at every point, that a feature they
  /// were never offered is unavailable.
  final ({String mr, String en})? because;

  bool get live => engine == SurveyEngine.live;

  /// Whether frames may be sent without thinking about the cost.
  ///
  /// Read from the one measurement rather than re-measured. Ten megabits is the
  /// figure the office set for a link that carries pictures freely; below it the
  /// conversation is voice with a frame only when the assistant asks.
  bool get framesUnthrottled => live && link.framesUnthrottled;
}

/// Decide, once.
///
/// The order is by cost, and the cheap answers are all local: a build without the
/// feature, and a farmer who has already declined, are settled before anything
/// touches the network. Only then is the link measured, because a measurement is
/// real traffic on a connection the farmer may be paying for by the megabyte.
///
/// Consent is not asked for here. This says whether asking is worth doing -- there
/// is no sense putting a decision to somebody about a conversation their farm
/// cannot carry -- and the screen that owns the question puts it.
Future<SurveyMode> decideSurveyMode({
  required Api api,
  required LiveConsent consent,
  required String farmerId,
  LinkProbe? probe,
}) async {
  if (!liveGuidanceEnabled) {
    return const SurveyMode.offline();
  }
  if (farmerId.isEmpty) {
    // Nothing to hold consent against and nothing to charge a session to.
    return const SurveyMode.offline();
  }
  if (await consent.asked(farmerId) && !await consent.granted(farmerId)) {
    // Asked, and said no. Not asked again at the start of every survey: that is
    // how a person learns to tap whichever button makes a screen go away.
    return const SurveyMode.offline();
  }

  final link = await (probe ?? LinkProbe(api: api)).measure();
  if (!link.canTalk) {
    return SurveyMode.offline(
      because:
          link.refusal ??
          (
            mr: 'येथील नेटवर्क संवादासाठी पुरेसे नाही. पाहणी नेहमीप्रमाणे होईल.',
            en:
                'The connection here will not carry a conversation. The survey '
                'runs as usual.',
          ),
    );
  }
  return SurveyMode(engine: SurveyEngine.live, link: link);
}
