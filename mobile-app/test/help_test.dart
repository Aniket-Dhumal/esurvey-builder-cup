import 'package:esurvey/core/voice.dart';
import 'package:esurvey/help/help_screen.dart';
import 'package:esurvey/help/questions.dart';
import 'package:esurvey/ui/theme.dart';
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:flutter_tts/flutter_tts.dart';

/// What the app is allowed to tell a farmer.
///
/// Most of what follows is about the answers rather than the screen, because
/// the answers are the part that can do harm. Somebody who has just lost a
/// crop and is told a number, or a date, will plan around it. Neither is this
/// app's to give: it records damage, and compensation is decided elsewhere
/// under rules it has no part in.

/// A speech engine that reports Marathi and records what it was asked to say.
class _FakeTts implements FlutterTts {
  final spoken = <String>[];
  var stops = 0;

  @override
  Future<dynamic> get getLanguages async => ['mr-IN', 'en-US'];

  @override
  Future<dynamic> speak(String text, {bool? focus}) async {
    spoken.add(text);
    return 1;
  }

  @override
  Future<dynamic> stop() async {
    stops++;
    return 1;
  }

  // Every other setter on the engine -- language, rate, pitch -- accepts and
  // does nothing. Listing them individually would be a page of noise.
  @override
  dynamic noSuchMethod(Invocation invocation) async => 1;
}

/// An engine that fails at everything, which is a real handset with no
/// speech package installed.
class _BrokenTts implements FlutterTts {
  @override
  dynamic noSuchMethod(Invocation invocation) async =>
      throw Exception('no engine on this handset');
}

Widget wrap(Widget child) => MaterialApp(theme: esurveyTheme(), home: child);

void main() {
  visitPromiseTests();
  group('what the answers may not say', () {
    test('no answer promises an amount', () {
      // The single most damaging thing this app could tell somebody who has
      // just lost a crop. They would plan around it and it would not arrive.
      final money = RegExp(
        r'(₹|rupee|Rs\.?\s*\d|\bहजार\b|\bलाख\b|\d+\s*(रुपये|रु))',
        caseSensitive: false,
      );

      for (final q in questions) {
        expect(money.hasMatch(q.answerMr), isFalse, reason: q.en);
        expect(money.hasMatch(q.answerEn), isFalse, reason: q.en);
      }
    });

    test('no answer promises a date or a deadline', () {
      // "An officer will come within seven days" is a sentence nobody in this
      // system can keep, and the farmer who waits eight days believes they
      // have been forgotten.
      final promise = RegExp(
        r'within \d|\bin \d+ (day|week)|\bदिवसात\b|\bआठवड्यात\b',
        caseSensitive: false,
      );

      for (final q in questions) {
        expect(promise.hasMatch(q.answerMr), isFalse, reason: q.en);
        expect(promise.hasMatch(q.answerEn), isFalse, reason: q.en);
      }
    });

    test('the money question is answered, not avoided', () {
      // It is the first thing anybody wants to know. Leaving it out would
      // send them to the office for the one answer this app is certain of --
      // that it does not decide and does not pay.
      final q = questions.firstWhere((q) => q.en.contains('money'));
      expect(q.answerEn.toLowerCase(), contains('does not'));
    });
  });

  group('every question is fully answered', () {
    test('both languages, on the question and the answer', () {
      for (final q in questions) {
        for (final text in [q.mr, q.en, q.answerMr, q.answerEn]) {
          expect(text.trim(), isNotEmpty, reason: q.en);
        }
      }
    });

    test('the Marathi is Devanagari and the English is not', () {
      // A pair that is the same string twice is a translation somebody
      // forgot, and it reads as Marathi to nobody.
      final devanagari = RegExp(r'[ऀ-ॿ]');

      for (final q in questions) {
        expect(devanagari.hasMatch(q.mr), isTrue, reason: q.en);
        expect(devanagari.hasMatch(q.answerMr), isTrue, reason: q.en);
        expect(devanagari.hasMatch(q.en), isFalse, reason: q.en);
        expect(devanagari.hasMatch(q.answerEn), isFalse, reason: q.en);
      }
    });

    test('questions are asked as questions', () {
      for (final q in questions) {
        expect(q.mr.trim(), endsWith('?'), reason: q.en);
        expect(q.en.trim(), endsWith('?'), reason: q.en);
      }
    });

    test('no two questions are the same', () {
      expect(questions.map((q) => q.en).toSet(), hasLength(questions.length));
    });
  });

  group('the screen', () {
    testWidgets('every question can be reached by scrolling', (tester) async {
      // Scrolled to rather than asserted in place: the list is longer than
      // any phone screen, so "is it there" and "can somebody get to it" are
      // different questions and only the second one matters.
      await tester.pumpWidget(wrap(HelpScreen(voice: Voice(tts: _FakeTts()))));

      for (final q in questions) {
        await tester.scrollUntilVisible(find.text(q.mr), 120);
        expect(find.text(q.mr), findsOneWidget, reason: q.en);
      }
    });

    testWidgets('tapping a question shows its answer, silently', (
      tester,
    ) async {
      // The voice is kept for the field walk. A help screen read at home
      // talking at the farmer is what gets the voice switched off for good.
      final tts = _FakeTts();
      final voice = Voice(tts: tts);
      await voice.init();

      await tester.pumpWidget(wrap(HelpScreen(voice: voice)));

      final first = questions.first;
      expect(find.text(first.answerMr), findsNothing);

      await tester.tap(find.text(first.mr));
      await tester.pumpAndSettle();

      expect(find.text(first.answerMr), findsOneWidget);
      expect(tts.spoken, isEmpty);
    });

    testWidgets('only the region\'s language is shown', (tester) async {
      // Marathi for Maharashtra, and nothing stacked under it.
      await tester.pumpWidget(wrap(HelpScreen(voice: Voice(tts: _FakeTts()))));

      final first = questions.first;
      await tester.tap(find.text(first.mr));
      await tester.pumpAndSettle();

      expect(find.text(first.en), findsNothing);
      expect(find.text(first.answerEn), findsNothing);
    });

    testWidgets('opening one answer closes the one before it', (tester) async {
      // A list that expands everything is a wall of text.
      final voice = Voice(tts: _FakeTts());
      await voice.init();

      await tester.pumpWidget(wrap(HelpScreen(voice: voice)));

      await tester.tap(find.text(questions[0].mr));
      await tester.pumpAndSettle();
      expect(find.text(questions[0].answerMr), findsOneWidget);

      await tester.tap(find.text(questions[1].mr));
      await tester.pumpAndSettle();

      expect(find.text(questions[0].answerMr), findsNothing);
      expect(find.text(questions[1].answerMr), findsOneWidget);
    });

    testWidgets('a handset that cannot speak still answers', (tester) async {
      final voice = Voice(tts: _BrokenTts());
      await voice.init();
      expect(voice.available, isFalse);

      await tester.pumpWidget(wrap(HelpScreen(voice: voice)));

      await tester.tap(find.text(questions.first.mr));
      await tester.pumpAndSettle();

      expect(find.text(questions.first.answerMr), findsOneWidget);
    });
  });
}

/// Nothing the app tells a farmer may promise a visit.
///
/// Officers do not go to the field: the photographs and the boundary the
/// farmer sends are what gets examined. Copy that says somebody is coming
/// leaves a farmer waiting at home for a visit that was never going to
/// happen, and it is the kind of sentence that creeps back every time
/// somebody writes reassuring text.
void visitPromiseTests() {
  test('no answer promises that an officer will come', () {
    // "Will somebody come and inspect my field?" is answered honestly --
    // usually not, and here is the route if the photographs cannot show it.
    // What is refused is a promise.
    final promises = RegExp(
      r'(will visit|will come and (see|look|inspect)|a visit will be|'
      r'officers will (visit|come)|येऊन पाहणी करतील|पाहणीसाठी.{0,12}येतील)',
      caseSensitive: false,
    );

    for (final q in questions) {
      for (final text in [q.answerMr, q.answerEn]) {
        if (promises.hasMatch(text)) {
          fail('${q.en}: promises a field visit — "$text"');
        }
      }
    }
  });

  test('the estimate caveat says what actually decides the figure', () {
    // A farmer moving a slider is entitled to know who settles the number and
    // on what. "An officer will visit" was neither.
    expect(Lines.estimateIsYours.en, contains('photographs'));
    expect(Lines.estimateIsYours.en, isNot(contains('visit')));
    expect(Lines.estimateIsYours.mr, contains('फोटो'));
  });

  test('a farmer who needs eyes on the field has a route', () {
    // Some losses a photograph cannot carry: water that drained before the
    // phone came out, root damage under a canopy that still looks green. A
    // system that never visits needs that exception stated, or those claims
    // have nowhere to go.
    final q = questions.firstWhere((q) => q.en.contains('come and inspect'));
    expect(q.answerEn.toLowerCase(), contains('object'));
    expect(q.answerMr, contains('आक्षेप'));
  });
}
