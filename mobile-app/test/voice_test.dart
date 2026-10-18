import 'dart:async';

import 'package:esurvey/core/voice.dart';
import 'package:fake_async/fake_async.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:flutter_tts/flutter_tts.dart';

/// The farmer's report: "voice guide was not working".
///
/// Nothing was wrong that the app could not have explained. Three things were:
///
///   1. A line spoken before the engine finished starting was dropped, and on
///      the walk the first instruction is the one that matters most.
///   2. A handset that cannot speak made the voice control vanish, so the farmer
///      got silence and no way to find out why.
///   3. Nothing recorded it, so support had nothing to go on either.

/// A TTS stand-in that answers with the languages a test gives it.
class _FakeTts extends FlutterTts {
  _FakeTts(
    this._languages, {
    this.throwOnLanguages = false,
    this.engines = const <String>[],
    this.languagesAfterEngine,
  });

  final List<String> _languages;
  final bool throwOnLanguages;

  /// What getEngines answers with.
  final List<String> engines;

  /// What getLanguages answers with once an engine has been chosen. Models the
  /// phone that has Google TTS installed and no default engine set: it reports no
  /// languages until one is selected.
  final List<String>? languagesAfterEngine;

  final spoken = <String>[];
  String? language;
  String? chosenEngine;

  @override
  Future<dynamic> get getLanguages async {
    if (throwOnLanguages) throw Exception('no engine');
    if (chosenEngine != null && languagesAfterEngine != null) {
      return languagesAfterEngine;
    }
    return _languages;
  }

  @override
  Future<dynamic> get getEngines async => engines;

  @override
  Future<dynamic> setEngine(String e) async => chosenEngine = e;

  @override
  Future<dynamic> setLanguage(String l) async => language = l;

  @override
  Future<dynamic> setSpeechRate(double r) async => 1;

  @override
  Future<dynamic> setPitch(double p) async => 1;

  @override
  Future<dynamic> awaitSpeakCompletion(bool b) async => 1;

  @override
  Future<dynamic> speak(String text, {bool? focus}) async {
    spoken.add(text);
    return 1;
  }

  @override
  Future<dynamic> stop() async => 1;
}

/// A stand-in that holds each line until the test releases it.
///
/// What the real engine does: speak() returns when the utterance finishes,
/// because awaitSpeakCompletion(true) is set. A stand-in that returns
/// immediately cannot show whether lines wait for each other.
class _SlowTts extends _FakeTts {
  _SlowTts(super.languages);

  final _turns = <Completer<void>>[];

  /// Whether a line is being spoken right now.
  bool get speaking => _turns.isNotEmpty && !_turns.last.isCompleted;

  /// Let the line that is speaking finish.
  Future<void> finishOne() async {
    _turns.last.complete();
    // Let the drain loop pick up whatever was waiting.
    await Future<void>.delayed(Duration.zero);
    await Future<void>.delayed(Duration.zero);
  }

  @override
  Future<dynamic> speak(String text, {bool? focus}) async {
    spoken.add(text);
    final turn = Completer<void>();
    _turns.add(turn);
    await turn.future;
    return 1;
  }
}

/// A stand-in whose engine never reports that it finished.
class _StuckTts extends _FakeTts {
  _StuckTts(super.languages);

  @override
  Future<dynamic> speak(String text, {bool? focus}) async {
    spoken.add(text);
    // Never completes, like a vendor engine that drops its completion callback.
    await Completer<void>().future;
    return 1;
  }
}

void main() {
  // FlutterTts installs a method-call handler in its constructor, which needs a
  // binding. Subclassing it is still the right stand-in: it keeps the real type,
  // so a signature change in the plugin breaks these tests rather than sliding
  // past them.
  setUpAll(TestWidgetsFlutterBinding.ensureInitialized);

  group('lines wait for each other', () {
    // The farmer's report: "sometimes the voice gets cut and the next one
    // starts".
    //
    // say() used to stop the engine and begin the new line immediately, so every
    // announcement cut off the one before it. The walk speaks often -- a point
    // recorded, how much further to go, a nudge to press the button -- so a
    // farmer walking heard the first few words of each and the whole of none.

    test('a second line waits rather than cutting the first off', () async {
      final tts = _SlowTts(['mr-IN']);
      final voice = Voice(tts: tts);
      await voice.init();

      unawaited(voice.say('पहिली ओळ'));
      await Future<void>.delayed(Duration.zero);
      expect(tts.spoken, ['पहिली ओळ']);

      // Arrives while the first is still speaking.
      unawaited(voice.say('दुसरी ओळ'));
      await Future<void>.delayed(Duration.zero);
      expect(
        tts.spoken,
        ['पहिली ओळ'],
        reason: 'the second line must not start while the first is speaking',
      );

      await tts.finishOne();
      expect(tts.spoken, ['पहिली ओळ', 'दुसरी ओळ']);
    });

    test('only the newest waiting line is kept', () async {
      // These lines go stale. A queue that kept everything would tell a farmer
      // who had already arrived to walk twelve metres further, in order, after
      // the arrival -- and a wrong instruction played over the right one is worse
      // than a missed line.
      final tts = _SlowTts(['mr-IN']);
      final voice = Voice(tts: tts);
      await voice.init();

      unawaited(voice.say('पहिली ओळ'));
      await Future<void>.delayed(Duration.zero);

      unawaited(voice.say('अजून १० मीटर चालत जा'));
      unawaited(voice.say('अजून ५ मीटर चालत जा'));
      unawaited(voice.say('इथे थांबा आणि व्हिडिओ काढा'));

      await tts.finishOne();
      expect(
        tts.spoken,
        ['पहिली ओळ', 'इथे थांबा आणि व्हिडिओ काढा'],
        reason: 'the two distances were out of date before their turn came',
      );
    });

    test('a deliberate stop drops what was waiting', () async {
      // Leaving the walk. Whatever was queued belongs to a screen the farmer has
      // left, and must not start speaking over the next one.
      final tts = _SlowTts(['mr-IN']);
      final voice = Voice(tts: tts);
      await voice.init();

      unawaited(voice.say('पहिली ओळ'));
      await Future<void>.delayed(Duration.zero);
      unawaited(voice.say('दुसरी ओळ'));

      await voice.stop();
      await tts.finishOne();

      expect(tts.spoken, ['पहिली ओळ']);
    });

    test('an engine that never finishes costs one line, not the walk', () async {
      // Vendor engines do not always report completion. Without a cap the first
      // such line would leave the queue held for the rest of the walk and the
      // voice silent -- an unreliable engine turned into no voice at all.
      fakeAsync((async) {
        final tts = _StuckTts(['mr-IN']);
        final voice = Voice(tts: tts);
        unawaited(voice.init());
        async.flushMicrotasks();

        unawaited(voice.say('अडकलेली ओळ'));
        async.flushMicrotasks();
        expect(tts.spoken, ['अडकलेली ओळ']);

        // A later line is held while the stuck one is still notionally speaking.
        unawaited(voice.say('पुढची ओळ'));
        async.flushMicrotasks();
        expect(tts.spoken, ['अडकलेली ओळ']);

        // Past the cap, the queue moves on.
        async.elapse(const Duration(seconds: 13));
        async.flushMicrotasks();
        expect(
          tts.spoken,
          ['अडकलेली ओळ', 'पुढची ओळ'],
          reason:
              'one silent line is a missed instruction; a silent walk is a '
              'farmer with no guidance at all',
        );
      });
    });
  });

  group('a line asked for before the engine has started', () {
    test('is spoken once it has, not dropped', () async {
      // The walk says "Recording. Pan slowly across the field." the instant
      // recording begins. init() runs unawaited at launch, so on a phone that
      // takes a moment to enumerate its voices that line used to vanish -- and
      // the moment it belonged to had passed by the time anything was ready.
      final tts = _FakeTts(['mr-IN']);
      final voice = Voice(tts: tts);

      // Deliberately not awaited: this is the race as it actually happens.
      final starting = voice.init();
      await voice.say('चित्रीकरण सुरू', en: 'Recording');
      expect(tts.spoken, isEmpty, reason: 'nothing can be said yet');

      await starting;
      expect(
        tts.spoken,
        ['चित्रीकरण सुरू'],
        reason: 'the held line should be spoken once the engine is ready',
      );
    });

    test('a held line is dropped once a newer one has been spoken', () async {
      // Found by this test failing. The flush could speak a held line *after* a
      // newer one had gone out, so a farmer at station two would hear the
      // instruction for station one last. A missed instruction is bad; a wrong
      // one played over the right one is worse.
      final tts = _FakeTts(['mr-IN']);
      final voice = Voice(tts: tts);

      final starting = voice.init();
      await voice.say('पहिली सूचना');
      await voice.say('दुसरी सूचना');
      await starting;

      expect(tts.spoken, ['दुसरी सूचना']);
    });

    test('is not held on a phone that cannot speak at all', () async {
      // Holding a line for an engine that will never arrive would leak it and
      // achieve nothing.
      final tts = _FakeTts(const [], throwOnLanguages: true);
      final voice = Voice(tts: tts);

      final starting = voice.init();
      await voice.say('काहीतरी');
      await starting;

      expect(tts.spoken, isEmpty);
    });
  });

  group('a phone with an engine but no default set', () {
    test('chooses one and asks again', () async {
      // The state found on the handset used for this investigation: Google TTS
      // installed, `tts_default_synth` null. getLanguages queries the *default*
      // engine, so an engine that is present but unselected answers with nothing
      // -- and the app reported "no voice on this phone".
      final tts = _FakeTts(
        const [],
        engines: const ['com.vendor.tts', 'com.google.android.tts'],
        languagesAfterEngine: const ['mr-IN', 'en-IN'],
      );
      final voice = Voice(tts: tts);
      await voice.init();

      expect(
        tts.chosenEngine,
        'com.google.android.tts',
        reason: 'Google carries Marathi; most OEM engines do not',
      );
      expect(voice.available, isTrue);
      expect(voice.language, 'mr-in');
      expect(voice.unavailableBecause, isNull);
    });

    test('falls back to whatever engine there is', () async {
      final tts = _FakeTts(
        const [],
        engines: const ['com.vendor.tts'],
        languagesAfterEngine: const ['en-IN'],
      );
      final voice = Voice(tts: tts);
      await voice.init();

      expect(tts.chosenEngine, 'com.vendor.tts');
      expect(voice.available, isTrue);
    });

    test('a phone with no engine at all still reports why', () async {
      final voice = Voice(tts: _FakeTts(const []));
      await voice.init();

      expect(voice.available, isFalse);
      expect(voice.unavailableBecause, contains('no text-to-speech languages'));
    });
  });

  group('a phone that cannot speak', () {
    test('says why, rather than reporting a bare false', () async {
      // "available == false" was all there was, so a farmer's "the voice did not
      // work" could not be answered: engine missing, language missing and
      // preference off all looked identical from here.
      final voice = Voice(tts: _FakeTts(const [], throwOnLanguages: true));
      await voice.init();

      expect(voice.available, isFalse);
      expect(voice.started, isTrue);
      expect(voice.unavailableBecause, contains('no text-to-speech engine'));
    });

    test('distinguishes no engine from no usable language', () async {
      // Different remedies: one needs an engine installed, the other needs a
      // language downloaded into the engine that is already there.
      final voice = Voice(tts: _FakeTts(['ta-IN', 'te-IN']));
      await voice.init();

      expect(voice.available, isFalse);
      expect(voice.unavailableBecause, contains('speaks none of'));
      // Lowercased, because that is the form the matching compared against and
      // so the form this app actually looked for.
      expect(
        voice.unavailableBecause,
        contains('ta-in'),
        reason: 'what it does offer is the useful part of the diagnosis',
      );
    });
  });

  group('the fallback ladder', () {
    test('prefers Marathi', () async {
      final tts = _FakeTts(['en-US', 'hi-IN', 'mr-IN']);
      final voice = Voice(tts: tts);
      await voice.init();

      expect(voice.language, 'mr-in');
      expect(voice.isFallback, isFalse);
      expect(voice.unavailableBecause, isNull);
    });

    test('falls back to Hindi and says it is a fallback', () async {
      // Most Marathi speakers follow Hindi for an instruction this short, but a
      // screen has to be able to say why the phone is speaking it.
      final voice = Voice(tts: _FakeTts(['en-US', 'hi-IN']));
      await voice.init();

      expect(voice.language, 'hi-in');
      expect(voice.available, isTrue);
      expect(voice.isFallback, isTrue);
    });

    test('speaks English text when only English is available', () async {
      final tts = _FakeTts(['en-IN']);
      final voice = Voice(tts: tts);
      await voice.init();
      await voice.say('मराठी', en: 'English');

      expect(
        tts.spoken,
        ['English'],
        reason: 'an English engine reading Devanagari says nothing useful',
      );
    });
  });

  group('the preference', () {
    test('silences the voice without losing the language', () async {
      final tts = _FakeTts(['mr-IN']);
      final voice = Voice(tts: tts);
      await voice.init();

      voice.setEnabled(false);
      await voice.say('काही');
      expect(tts.spoken, isEmpty);

      voice.setEnabled(true);
      await voice.say('काही');
      expect(tts.spoken, ['काही']);
    });

    test('a disabled voice does not hold a line for later', () async {
      // Turning the voice off and on again must not replay what was said while
      // it was off.
      final tts = _FakeTts(['mr-IN']);
      final voice = Voice(tts: tts);
      voice.setEnabled(false);

      final starting = voice.init();
      await voice.say('while off');
      await starting;
      voice.setEnabled(true);

      expect(tts.spoken, isEmpty);
    });
  });

  group('who holds the floor', () {
    // The live assistant speaks through a different speaker: its voice is PCM fed
    // straight to the audio player, not an utterance in this queue, so the queue
    // cannot hold it back and nothing else can either. Two voices out of one handset
    // in a field is neither of them.
    //
    // The scripted line wins, and the assistant's audio is dropped rather than
    // buffered -- speech held back and played afterwards answers a moment that has
    // passed, which is the same fault as a stale queued line surviving a screen.
    // Which means this flag is the whole mechanism, and a stuck true silences the
    // assistant for the rest of the walk while a stuck false talks over the
    // instructions that get the claim filed.

    test('a voice that has said nothing is not holding it', () async {
      final voice = Voice(tts: _FakeTts(['mr-IN']));
      await voice.init();

      expect(voice.busy, isFalse);
    });

    test('a line being spoken holds it, and lets go when it drains', () async {
      final tts = _SlowTts(['mr-IN']);
      final voice = Voice(tts: tts);
      await voice.init();

      unawaited(voice.say('कोपरा नोंदवला'));
      await Future<void>.delayed(Duration.zero);
      expect(voice.busy, isTrue);

      await tts.finishOne();
      expect(voice.busy, isFalse);
    });

    test('a line waiting its turn holds it too', () async {
      // Otherwise the assistant would be let through in the gap between two
      // instructions -- which is exactly where the next instruction is about to go,
      // and a farmer hearing advice over "stop here and record" records nowhere.
      final tts = _SlowTts(['mr-IN']);
      final voice = Voice(tts: tts);
      await voice.init();

      unawaited(voice.say('अजून १० मीटर चालत जा'));
      await Future<void>.delayed(Duration.zero);
      unawaited(voice.say('इथे थांबा आणि व्हिडिओ काढा'));
      await Future<void>.delayed(Duration.zero);

      await tts.finishOne();
      expect(
        voice.busy,
        isTrue,
        reason: 'the second line has the engine now',
      );

      await tts.finishOne();
      expect(voice.busy, isFalse);
    });

    test('a deliberate stop lets go of it at once', () async {
      // A screen being left. Whatever was queued belongs to it, and the floor has to
      // be free before the next screen's own guidance -- or the assistant on it --
      // needs it. Waiting for an engine that has just been told to stop to report
      // that it did would leave the next screen silent.
      final tts = _SlowTts(['mr-IN']);
      final voice = Voice(tts: tts);
      await voice.init();

      unawaited(voice.say('पहिली ओळ'));
      await Future<void>.delayed(Duration.zero);
      unawaited(voice.say('दुसरी ओळ'));
      await Future<void>.delayed(Duration.zero);

      await voice.stop();

      expect(voice.busy, isFalse);
    });

    test('turning the voice off lets go of it as well', () async {
      // A farmer standing next to somebody asleep switches the voice off. The
      // assistant is a separate agreement and is not switched off with it, so the
      // floor must actually be free rather than held by a queue nothing will drain.
      final tts = _SlowTts(['mr-IN']);
      final voice = Voice(tts: tts);
      await voice.init();

      unawaited(voice.say('पहिली ओळ'));
      await Future<void>.delayed(Duration.zero);

      voice.setEnabled(false);
      await Future<void>.delayed(Duration.zero);

      expect(voice.busy, isFalse);
    });
  });
}
