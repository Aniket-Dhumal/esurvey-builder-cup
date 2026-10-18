import 'db.dart';

/// Whether this farmer has agreed to the assistant hearing them.
///
/// # Why this is asked separately from the camera
///
/// Because it is a different thing being agreed to. The camera permission lets the
/// app record a video that the office receives; a farmer who grants it understands
/// they are making a recording for a claim. Live guidance sends their voice, and
/// pictures of their field, to a company outside the platform, continuously, while
/// they talk. Nobody would read the first as covering the second.
///
/// Android will not ask on our behalf either: the microphone permission is not even
/// requested, because the walk records with audio disabled and the app deliberately
/// never asks for it (see the manifest note on RECORD_AUDIO). So the only thing that
/// can put this decision to a farmer is the app.
///
/// # Why it is per farmer and not per handset
///
/// A handset in a village is shared. A household's phone acts for several farmers,
/// and one of them agreeing to be recorded says nothing about the others. Stored
/// against the farmer, so the next person is asked for themselves.
///
/// # Why refusing costs nothing
///
/// Every instruction needed to file a claim is scripted and works with no network.
/// A farmer who says no keeps the guidance, the walk, the spacing rule and the
/// filing, and loses only the conversation -- which is what makes this a real
/// choice rather than a gate with one door.
class LiveConsent {
  const LiveConsent(this._store);

  final Store _store;

  static String _key(String farmerId) => 'live_consent:$farmerId';

  /// Whether this farmer has agreed.
  ///
  /// Absent is not consent. A farmer who has never been asked, and one whose answer
  /// was lost, are both treated as not having agreed -- the assistant simply is not
  /// offered, which is the same as a farm with no signal.
  Future<bool> granted(String farmerId) async {
    if (farmerId.isEmpty) return false;
    final raw = await _store.get(_key(farmerId));
    return raw is Map && raw['granted'] == true;
  }

  /// Record what they said, and when.
  ///
  /// The refusal is stored as well as the agreement. Without it the app cannot tell
  /// "has not been asked" from "said no", and would ask again every time the screen
  /// opened -- which is how a farmer learns to tap whatever makes a dialog go away.
  Future<void> record({required String farmerId, required bool granted}) async {
    if (farmerId.isEmpty) return;
    await _store.put(_key(farmerId), {
      'granted': granted,
      'at': DateTime.now().toUtc().toIso8601String(),
      // What they were told, by version. If the wording changes materially the
      // question is put again, because consent is to what was actually said.
      'terms': termsVersion,
    });
  }

  /// Whether they have been asked at all, whatever they answered.
  Future<bool> asked(String farmerId) async {
    if (farmerId.isEmpty) return false;
    final raw = await _store.get(_key(farmerId));
    if (raw is! Map) return false;
    // Asked under older wording counts as not asked. The alternative is holding
    // somebody to an agreement about something they were not told.
    return raw['terms'] == termsVersion;
  }

  /// Take it back.
  ///
  /// Reachable from Help, and it has to be: an agreement that cannot be withdrawn
  /// is not much of an agreement. Takes effect on the next session, because the one
  /// running is ended by the screen rather than by this.
  Future<void> withdraw(String farmerId) =>
      record(farmerId: farmerId, granted: false);
}

/// Which wording the farmer agreed to.
///
/// Bumped when what is being agreed to changes -- not for a typo. Everybody is then
/// asked again, which is correct and is also why it should not be bumped lightly.
const int termsVersion = 1;

/// What the farmer is told, in the language of the place.
///
/// Written to be read aloud as well as read: on this screen the voice speaks it, and
/// a sentence built for the eye does not survive being spoken.
/// What is sent is listed, rather than what is withheld.
///
/// A notice that lists absences invites the reader to assume everything else stays,
/// and it stops being true the moment somebody adds a field. Listing what goes is a
/// statement that can be checked against the code -- see liveURL in the Go service,
/// which builds the session address from an explicit four -- and it stays honest when
/// the code changes, because then the notice is visibly wrong rather than quietly so.
const liveConsentTerms = (
  mr: '''सहाय्यकाशी बोलण्यासाठी हे Google च्या सेवेकडे पाठवले जाते:

  • तुमचा आवाज, तुम्ही बोलत असताना
  • शेताचे फोटो, सहाय्यकाने मागितल्यावर
  • कोणते पीक, कोणती आपत्ती, कोणता हंगाम आणि कोणता जिल्हा

तुमचे नाव, आधार क्रमांक आणि शेतकरी क्रमांक पाठवले जात नाहीत.

हे न वापरताही तुमची नोंद पूर्ण होते. सूचना ऐकू येतील, चालण्याचे मार्गदर्शन
मिळेल आणि नोंद पाठवली जाईल — यासाठी नेटवर्कची गरज नाही.

तुम्ही नंतर कधीही ही परवानगी काढून घेऊ शकता.''',
  en: '''To talk to the assistant, this is sent to a Google service:

  • your voice, while you are speaking
  • photographs of your field, when the assistant asks for one
  • which crop, which calamity, which season and which district

Your name, Aadhaar number and farmer id are not sent.

Your claim is completed without this. You will still hear the guidance, still be
walked through the points, and the claim will still be sent — none of that needs
a network.

You can withdraw this permission at any time.''',
);
