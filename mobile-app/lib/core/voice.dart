import 'dart:async';

import 'package:flutter/foundation.dart';
import 'package:flutter_tts/flutter_tts.dart';

import 'faults.dart';

/// Spoken guidance, in Marathi.
///
/// # Why this is not a nicety
///
/// A farmer standing in a wet field, holding a phone in one hand, is not
/// reading a paragraph. Many will read Marathi slowly and some not at all.
/// Speech is how an instruction actually arrives, and the whole guided
/// capture depends on the instruction arriving before the photograph is taken
/// rather than after it is rejected.
///
/// # What is honest about it
///
/// Marathi text-to-speech is not on every handset. Where it is missing this
/// falls back to Hindi, which most Marathi speakers follow, then to English,
/// then to nothing. The text is always on screen regardless: speech is a
/// second channel, never the only one. An app whose instructions are
/// inaudible on a cheap phone is an app that does not work on the phones it
/// is for.
///
/// # Why it never speaks twice
///
/// A rebuild that repeats the same line is the fastest way to make somebody
/// turn the voice off, and a farmer who turns the voice off loses the only
/// channel that was working for them.
class Voice extends ChangeNotifier {
  Voice({FlutterTts? tts}) : _tts = tts ?? FlutterTts();

  final FlutterTts _tts;

  /// Preference order: Marathi, then the nearest language most speakers
  /// follow, then English.
  static const _preferred = <String>[
    'mr-IN',
    'mr',
    'hi-IN',
    'hi',
    'en-IN',
    'en-GB',
    'en-US',
  ];

  String? _language;
  bool _ready = false;
  bool _enabled = true;
  String _last = '';

  /// Why the handset cannot speak, where it cannot.
  ///
  /// A farmer reported the voice guide simply not working, and there was no way
  /// to tell from here whether the engine was missing, the language was, or the
  /// preference was off -- because nothing recorded it and nothing showed it. The
  /// control hid itself when [available] was false, so the farmer got silence and
  /// no explanation, and support got nothing to go on.
  String? _unavailableBecause;

  /// The line that was asked for before the engine had finished starting.
  ///
  /// init() runs unawaited at launch, deliberately: enumerating an Android TTS
  /// engine takes a moment on a cheap handset and blocking the first screen on it
  /// would delay every farmer. The cost was that any line spoken in that window
  /// was dropped on the floor -- and on the walk the first instruction is the one
  /// that matters most.
  ///
  /// Only the latest is kept. Speaking a backlog of stale instructions at
  /// somebody who has already moved on would be worse than saying nothing.
  ({String mr, String? en})? _pending;

  /// The language actually being used, so a screen can say if it is not
  /// Marathi rather than leaving somebody wondering why the phone speaks
  /// Hindi at them.
  String? get language => _language;

  /// Whether the handset can speak any of the languages above.
  bool get available => _language != null;

  /// Why not, in words, where it cannot. Null while starting up or when it can.
  String? get unavailableBecause => _unavailableBecause;

  /// Whether the engine has finished being asked what it can do.
  bool get started =>
      _ready && (_language != null || _unavailableBecause != null);

  bool get enabled => _enabled;

  /// Whether the voice is speaking a language other than Marathi.
  bool get isFallback =>
      _language != null && !_language!.toLowerCase().startsWith('mr');

  Future<void> init() async {
    if (_ready) return;
    _ready = true;

    try {
      // Make sure there is an engine to ask.
      //
      // getLanguages queries the phone's *default* text-to-speech engine, and on
      // a Vivo handset checked during this investigation Google TTS was installed
      // while `tts_default_synth` was null -- no default set at all. An engine
      // that is present but not selected answers with nothing, which this app
      // then reported as "no voice on this phone".
      //
      // So where no language comes back, an installed engine is chosen explicitly
      // and the question asked again. Nothing is changed on the device: the
      // selection applies to this app's own TTS session.
      var languages = await _tts.getLanguages;
      if ((languages as List<dynamic>? ?? const []).isEmpty) {
        await _selectAnEngine();
        languages = await _tts.getLanguages;
      }
      final available = <String>[
        for (final l in (languages as List<dynamic>? ?? const []))
          l.toString().toLowerCase(),
      ];

      for (final want in _preferred) {
        final exact = available.firstWhere(
          (l) => l == want.toLowerCase(),
          orElse: () => '',
        );
        if (exact.isNotEmpty) {
          _language = exact;
          break;
        }
        final prefix = want.split('-').first.toLowerCase();
        final near = available.firstWhere(
          (l) => l.startsWith(prefix),
          orElse: () => '',
        );
        if (near.isNotEmpty) {
          _language = near;
          break;
        }
      }

      if (_language != null) {
        await _tts.setLanguage(_language!);
        // A shade slower than default: instructions given over wind, to
        // someone who is also looking at a field.
        await _tts.setSpeechRate(0.45);
        await _tts.setPitch(1.0);
        await _tts.awaitSpeakCompletion(true);
      } else {
        // An engine that answered, and has none of the languages this app can
        // use. Recorded rather than left as a silent false, because "no voice"
        // and "no Marathi voice" need different things done about them.
        _unavailableBecause = available.isEmpty
            ? 'the phone reported no text-to-speech languages at all'
            : 'the phone speaks none of Marathi, Hindi or English '
                  '(it offers: ${available.take(6).join(', ')})';
      }
    } catch (e) {
      // A handset with no engine at all. The text is on screen regardless,
      // and an app that crashed here would be unusable on exactly the cheap
      // phones it is written for.
      debugPrint('voice unavailable: $e');
      _language = null;
      _unavailableBecause = 'this phone has no text-to-speech engine: $e';
    }

    if (_unavailableBecause != null) {
      // Written down, so the next farmer who says "the voice did not work" can
      // be answered from Help rather than guessed about.
      Faults.note('voice.unavailable', _unavailableBecause!);
    }

    // Anything asked for while this was running.
    final waiting = _pending;
    _pending = null;
    notifyListeners();
    if (waiting != null && _language != null) {
      await say(waiting.mr, en: waiting.en);
    }
  }

  /// Choose an installed engine, where the phone has not chosen one.
  ///
  /// Best effort and silent about failure: this runs only when the phone has
  /// already told us it has no languages, so the worst case is that it still has
  /// none and [init] reports why.
  Future<void> _selectAnEngine() async {
    try {
      final engines = await _tts.getEngines;
      final list = <String>[
        for (final e in (engines as List<dynamic>? ?? const [])) e.toString(),
      ];
      if (list.isEmpty) return;

      // Google's, where it is there. It is the one that actually carries Marathi;
      // most OEM engines do not.
      final pick = list.firstWhere(
        (e) => e.contains('google'),
        orElse: () => list.first,
      );
      await _tts.setEngine(pick);
      debugPrint('voice: no default engine was set; chose $pick');
      Faults.note('voice.engine', 'no default engine was set; chose $pick');
    } catch (e) {
      debugPrint('voice: could not choose an engine: $e');
    }
  }

  /// Turn the voice off, or back on.
  ///
  /// # Why this has to be reachable and has to persist
  ///
  /// A farmer in a noisy market, or standing next to somebody who is asleep,
  /// or who simply finds it patronising, will want it off. Without a control
  /// they turn the phone's volume down instead -- and then miss the capture
  /// instructions, which are the ones that decide whether the photograph is
  /// usable.
  ///
  /// It persists because a preference that resets every launch is not a
  /// preference. [restore] reads it back at startup.
  void setEnabled(bool on) {
    if (_enabled == on) return;
    _enabled = on;
    if (!on) {
      _last = '';
      unawaited(stop());
    }
    unawaited(_persist?.call(on));
    notifyListeners();
  }

  /// Where the preference is written. Injected rather than reaching for the
  /// store, so this file stays free of the database and remains testable
  /// without one.
  Future<void> Function(bool)? _persist;

  /// Load the saved preference, and remember where to write it back.
  ///
  /// Defaults to on: somebody who has never chosen should hear the guidance,
  /// because the farmer who needs it most is the one least likely to go
  /// looking for a setting to switch it on.
  Future<void> restore({
    required Future<bool?> Function() read,
    required Future<void> Function(bool) write,
  }) async {
    _persist = write;
    try {
      _enabled = await read() ?? true;
    } catch (e) {
      debugPrint('voice preference unreadable: $e');
      _enabled = true;
    }
    notifyListeners();
  }

  /// Say a line, once.
  ///
  /// [mr] is spoken where the engine speaks an Indic language and [en] where
  /// it does not -- an English voice reading Devanagari produces noise, and
  /// noise is worse than silence when somebody is trying to follow an
  /// instruction.
  Future<void> say(String mr, {String? en}) async {
    if (!_enabled) return;

    // Asked before the engine finished starting. Held rather than dropped: this
    // is how the walk's first instruction -- the one that matters most -- used to
    // go missing on a phone that took a second to enumerate its voices.
    if (_language == null) {
      if (_unavailableBecause == null) _pending = (mr: mr, en: en);
      return;
    }

    if (_last == mr) return;
    _last = mr;

    final indic = _language!.startsWith('mr') || _language!.startsWith('hi');
    final text = indic ? mr : en;
    if (text == null || text.isEmpty) return;

    // Anything held for the engine is now stale: a line has been spoken since,
    // and that line is the current one.
    //
    // Without this, init's flush could speak a held line *after* a newer one had
    // already gone out -- so a farmer at station two would hear the instruction
    // for station one last. Dropping a line is a missed instruction; playing an
    // out-of-date one over the top of the right one is a wrong instruction, which
    // is worse.
    _pending = null;

    // One line at a time.
    //
    // This used to stop the engine and start the new line immediately, so every
    // announcement cut off the one before it. A farmer walking heard the first
    // three words of each instruction and the whole of none of them -- and the
    // walk speaks often: a point recorded, how much further to go, a nudge to
    // press the button.
    //
    // So a line waits for the one speaking to finish.
    if (_speaking) {
      _queued = text;
      return;
    }
    await _drain(text);
  }

  /// Whether the engine is part-way through a line.
  bool _speaking = false;

  /// Whether a scripted line holds the floor right now.
  ///
  /// # Why anything outside this class needs to know
  ///
  /// Because the assistant speaks through a different speaker. Its voice is PCM fed
  /// to the audio player, not an utterance in this queue, so the queue cannot hold it
  /// back and the two would talk over each other -- and the one that matters is this
  /// one: the scripted lines are the walk's instructions, and the assistant is advice.
  ///
  /// The caller drops the assistant's audio while this is true rather than buffering
  /// it. Speech held back and played afterwards answers a moment that has passed,
  /// which is the same fault as a queued line surviving a screen being closed.
  bool get busy => _speaking || _queued != null;

  /// The one line waiting its turn, if any.
  ///
  /// # Why one and not a list
  ///
  /// Because these lines go stale. A queue that kept everything would tell a
  /// farmer who had already arrived to walk twelve metres further, in order,
  /// after the arrival itself -- and an out-of-date instruction played over the
  /// top of the right one is worse than a missed line. That was the reasoning
  /// behind dropping stale held lines when the engine was still starting, and it
  /// holds here for the same reason.
  ///
  /// So a newer line replaces whatever was waiting. The farmer hears the current
  /// line to its end, then the most recent thing that has happened, and never a
  /// backlog of things that were true a minute ago.
  String? _queued;

  /// The longest a single line may hold the queue.
  ///
  /// Vendor engines do not always report that they finished. Without a cap, one
  /// utterance that never completes would leave [_speaking] true for the rest of
  /// the walk and the voice silent -- turning an unreliable engine into no voice
  /// at all. Twelve seconds is several times the longest line here.
  static const _longestLine = Duration(seconds: 12);

  /// Speak [text], then whatever arrived while it was speaking.
  Future<void> _drain(String text) async {
    _speaking = true;
    try {
      var line = text;
      while (true) {
        try {
          await _tts.speak(line).timeout(_longestLine);
        } on TimeoutException {
          // The engine never said it finished. Carry on rather than going silent
          // for the rest of the walk; the line may well have been spoken.
          debugPrint('voice: no completion for "$line" within $_longestLine');
          Faults.note('voice.stuck', 'no completion within $_longestLine');
        } catch (e) {
          debugPrint('voice failed: $e');
          Faults.note('voice.speak', e);
        }

        final next = _queued;
        _queued = null;
        if (next == null) return;
        line = next;
      }
    } finally {
      _speaking = false;
    }
  }

  /// Say a line even if it was the last one said.
  ///
  /// For a farmer pressing "say that again", which is the one case where
  /// repeating is exactly what was asked for.
  Future<void> repeat(String mr, {String? en}) async {
    _last = '';
    await say(mr, en: en);
  }

  /// Stop speaking. Never throws.
  ///
  /// Called from `dispose`, where a failure has nowhere to go and would
  /// surface as an unhandled async error while a screen is being torn down.
  /// [say] has always guarded itself for the same reason -- an engine that
  /// refuses must cost the farmer the audio and nothing else.
  Future<void> stop() async {
    _last = '';
    // Nothing waiting should start speaking after a deliberate stop.
    _queued = null;
    _speaking = false;
    try {
      await _tts.stop();
    } catch (e) {
      debugPrint('voice stop failed: $e');
    }
  }
}

/// Every line the app speaks, in one place.
///
/// Marathi is what is spoken and shown; English is what a non-Marathi handset
/// says instead, and what an English-reading officer sees in review. They are
/// written as a pair so a change to one is a visible change to the other.
class Lines {
  const Lines._();

  // --- first run ---------------------------------------------------------

  static const welcome = (
    mr: 'नमस्कार. या ॲपमधून आपण आपल्या शेतातील नुकसान कळवू शकता.',
    en: 'Welcome. This app lets you report damage to your field.',
  );

  static const whatYouCanDo = (
    mr:
        'तीन गोष्टी करता येतात. शेताची हद्द नोंदवणे, नुकसानीचे फोटो घेणे, '
        'आणि आपल्या नोंदीची स्थिती पाहणे.',
    en:
        'There are three things you can do. Record your field boundary, '
        'photograph the damage, and check on your report.',
  );

  static const worksWithoutNetwork = (
    mr:
        'नेटवर्क नसले तरी सर्व काही चालते. फोटो फोनवर सुरक्षित राहतात आणि '
        'नेटवर्क आल्यावर आपोआप पाठवले जातात.',
    en:
        'Everything works without a network. Photographs stay safe on the '
        'phone and are sent by themselves when a signal returns.',
  );

  // --- the boundary walk --------------------------------------------------

  static const boundaryStart = (
    mr: 'शेताच्या कडेने चाला. प्रत्येक कोपऱ्यावर थांबून बटण दाबा.',
    en:
        'Walk along the edge of your field. Stop at each corner and press '
        'the button.',
  );

  static const cornerRecorded = (
    mr: 'कोपरा नोंदवला. पुढच्या कोपऱ्यावर जा.',
    en: 'Corner recorded. Walk to the next corner.',
  );

  static const boundaryDone = (
    mr: 'शेताची हद्द नोंदवली गेली आहे.',
    en: 'Your field boundary has been recorded.',
  );

  // --- position -----------------------------------------------------------

  static const findingPosition = (
    mr: 'कृपया आपल्या शेतात उभे रहा. फोन आपले ठिकाण शोधत आहे.',
    en: 'Please stand in your field. The phone is finding your position.',
  );

  static const positionGood = (
    mr: 'ठिकाण मिळाले. आता पुढे जाऊ शकता.',
    en: 'Position found. You can continue.',
  );

  static const positionPoor = (
    mr: 'ठिकाण नीट मिळत नाही. मोकळ्या जागी जा आणि थोडा वेळ थांबा.',
    en: 'The position is not clear. Move into the open and wait a moment.',
  );

  static const outsideField = (
    mr: 'आपण शेताच्या बाहेर दिसत आहात. शेतात जाऊन फोटो घ्या.',
    en:
        'You appear to be outside the field. Move into it before '
        'photographing.',
  );

  // --- capture ------------------------------------------------------------

  static const holdSteady = (
    mr: 'फोन स्थिर धरा.',
    en: 'Hold the phone steady.',
  );

  static const tiltDown = (
    mr: 'फोन थोडा खाली वाकवा.',
    en: 'Tilt the phone down a little.',
  );

  static const tiltUp = (
    mr: 'फोन थोडा वर करा.',
    en: 'Tilt the phone up a little.',
  );

  static const photoSaved = (
    mr: 'फोटो सुरक्षित आहे.',
    en: 'The photograph is saved.',
  );

  static const allDone = (
    mr: 'सर्व फोटो झाले. आपली नोंद फोनवर सुरक्षित आहे.',
    en: 'All the photographs are done. Your report is safe on this phone.',
  );

  // --- sending ------------------------------------------------------------

  static const queued = (
    mr: 'नेटवर्क नाही. सर्व काही फोनवर सुरक्षित आहे आणि नंतर आपोआप जाईल.',
    en:
        'There is no network. Everything is safe on the phone and will go by '
        'itself later.',
  );

  static const sent = (
    mr: 'आपली नोंद कार्यालयाला मिळाली आहे.',
    en: 'Your report has reached the office.',
  );

  // --- signing in ---------------------------------------------------------

  static const askMobile = (
    mr: 'आपला मोबाईल नंबर टाका. त्यावर एक कोड येईल.',
    en: 'Enter your mobile number. A code will come to it.',
  );

  static const askCode = (
    mr: 'SMS मध्ये आलेला कोड टाका.',
    en: 'Enter the code that came in the SMS.',
  );

  static const signInNeedsNetwork = (
    mr: 'साइन इन करण्यासाठी नेटवर्क लागते. बाकीचे ॲप नेटवर्कशिवाय चालते.',
    en: 'Signing in needs a network. The rest of the app works without one.',
  );

  // --- filling in the report ----------------------------------------------
  //
  // The most text on any screen in this app, and the screen where somebody
  // who reads slowly is most likely to give up. Each field is spoken as it is
  // reached rather than all at once, because four instructions in a row is
  // not guidance, it is a paragraph.

  static const askCrop = (
    mr: 'कोणते पीक होते ते सांगा. उदाहरणार्थ सोयाबीन.',
    en: 'Which crop was it? For example, soybean.',
  );

  static const askSown = (
    mr: 'किती क्षेत्रावर पेरणी केली होती? हेक्टरमध्ये.',
    en: 'How much area did you sow? In hectares.',
  );

  static const askAffected = (
    mr: 'त्यापैकी किती क्षेत्राचे नुकसान झाले? हेक्टरमध्ये.',
    en: 'How much of that area is damaged? In hectares.',
  );

  static const askDamage = (
    mr: 'पिकाचे किती नुकसान झाले ते सरकवून सांगा.',
    en: 'Slide to say how much of the crop is lost.',
  );

  static const askDeclaration = (
    mr: 'दिलेली माहिती खरी आहे का? खरी असेल तर खूण करा.',
    en: 'Is what you have entered true? Tick the box if it is.',
  );

  static const affectedTooBig = (
    mr: 'नुकसानग्रस्त क्षेत्र पेरणीच्या क्षेत्रापेक्षा जास्त असू शकत नाही.',
    en: 'The damaged area cannot be larger than the area you sowed.',
  );

  static const estimateIsYours = (
    mr: 'हा तुमचा अंदाज आहे. तुम्ही पाठवलेले फोटो तपासून अधिकारी अंतिम आकडा ठरवतील.',
    en:
        'This is your estimate. Officers will examine the photographs you '
        'send and decide the final figure.',
  );

  // --- the phone itself ---------------------------------------------------

  static const storageLow = (
    mr:
        'फोनमध्ये जागा कमी आहे. काही फोटो किंवा व्हिडिओ काढून टाका, नाहीतर '
        'नवीन फोटो साठवता येणार नाहीत.',
    en:
        'This phone is low on space. Delete some photos or videos, or new '
        'photographs cannot be saved.',
  );

  static const storageFull = (
    mr:
        'फोनमध्ये जागा नाही. फोटो साठवता आला नाही. जागा करून पुन्हा प्रयत्न '
        'करा.',
    en:
        'This phone has no space left. The photograph could not be saved. '
        'Free some space and try again.',
  );

  // --- objecting to a decision --------------------------------------------

  static const appealGround = (
    mr: 'कशाबद्दल आक्षेप आहे ते निवडा.',
    en: 'Choose what your objection is about.',
  );

  static const appealWhy = (
    mr: 'काय घडले ते तुमच्या शब्दांत सांगा. हे अधिकारी वाचतील.',
    en: 'Say what happened in your own words. An officer reads this.',
  );

  static const appealSaved = (
    mr: 'तुमचा आक्षेप नोंदवला आहे. नेटवर्क आल्यावर तो कार्यालयात जाईल.',
    en:
        'Your objection is recorded. It goes to the office when there is a '
        'signal.',
  );

  static const appealAlreadyFiled = (
    mr: 'या नोंदीवर तुमचा आक्षेप आधीच नोंदवलेला आहे.',
    en: 'You have already objected to this report.',
  );

  static const appealNoReport = (
    mr:
        'ही नोंद अद्याप कार्यालयात पोहोचलेली नाही, त्यामुळे आक्षेप नोंदवता '
        'येणार नाही.',
    en:
        'This report has not reached the office yet, so it cannot be objected '
        'to.',
  );

  static const voiceOn = (mr: 'आवाज सुरू आहे.', en: 'The voice is on.');
}
