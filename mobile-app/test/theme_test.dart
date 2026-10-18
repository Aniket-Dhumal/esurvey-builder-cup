import 'dart:math' as math;

import 'package:esurvey/core/language.dart';
import 'package:esurvey/ui/theme.dart';
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';

/// The design decisions, locked down.
///
/// These are not aesthetic preferences and they are easy to erode: a tap
/// target shrinks because a row got crowded, a caption drops below the
/// readable floor because a string got longer. Each one here answers a fact
/// about the user — one hand, wet fingers, direct sun, reading Marathi slowly
/// — so each one is asserted rather than trusted.

Widget wrap(Widget child) => MaterialApp(
  theme: esurveyTheme(),
  home: Scaffold(body: Center(child: child)),
);

/// Contrast ratio between two opaque colours, per WCAG.
///
/// Written out rather than pulled from a package: it is eight lines, and a
/// contrast assertion nobody can read is one nobody will trust when it fails.
double contrast(Color a, Color b) {
  double channel(double c) =>
      c <= 0.03928 ? c / 12.92 : math.pow((c + 0.055) / 1.055, 2.4).toDouble();

  double luminance(Color c) =>
      0.2126 * channel(c.r) + 0.7152 * channel(c.g) + 0.0722 * channel(c.b);

  final l1 = luminance(a);
  final l2 = luminance(b);
  return (math.max(l1, l2) + 0.05) / (math.min(l1, l2) + 0.05);
}

void main() {
  group('a farmer can actually hit the controls', () {
    testWidgets('a primary button is at least 52dp tall', (tester) async {
      // Above Material's 40dp filled-button default: one hand, in sun, with
      // wet fingers. The weight came off the letters, not the targets.
      await tester.pumpWidget(wrap(BigButton(label: 'पुढे', onPressed: () {})));

      final size = tester.getSize(find.byType(FilledButton));
      expect(size.height, greaterThanOrEqualTo(52));
    });

    testWidgets('a primary button spans the width it is given', (tester) async {
      // Full width so it can be reached with either thumb without
      // re-gripping, and found by position when glare hides the label.
      await tester.pumpWidget(
        MaterialApp(
          theme: esurveyTheme(),
          home: Scaffold(
            body: SizedBox(
              width: 400,
              child: BigButton(label: 'पुढे', onPressed: () {}),
            ),
          ),
        ),
      );

      expect(tester.getSize(find.byType(FilledButton)).width, 400);
    });

    testWidgets('a disabled button is still the same size', (tester) async {
      // A control that changes size when it becomes unavailable moves
      // everything under it, and somebody mid-press hits the wrong thing.
      await tester.pumpWidget(
        wrap(const BigButton(label: 'पुढे', onPressed: null)),
      );
      expect(
        tester.getSize(find.byType(FilledButton)).height,
        greaterThanOrEqualTo(52),
      );
    });
  });

  group('the type carries both scripts', () {
    // None of this is visible in an English test run, which is exactly why it
    // is tested. Marathi falling back to a platform font looks fine to anyone
    // reading the Latin and wrong to every farmer using the app.
    //
    // Asserted on the resolved TextStyles rather than on ThemeData, because
    // ThemeData takes fontFamily and fontFamilyFallback as arguments and folds
    // them into each style -- the styles are what actually reach the glyphs.
    test('every text style resolves Latin in Noto Sans', () {
      final t = esurveyTheme().textTheme;
      for (final e in <String, TextStyle?>{
        'bodyLarge': t.bodyLarge,
        'bodyMedium': t.bodyMedium,
        'titleMedium': t.titleMedium,
        'labelLarge': t.labelLarge,
        'headlineSmall': t.headlineSmall,
      }.entries) {
        expect(e.value?.fontFamily, 'Noto Sans', reason: e.key);
      }
    });

    test('every text style can fall through to Devanagari', () {
      // Noto Sans does not draw Devanagari and Noto Sans Devanagari does not
      // draw Latin. Without the fallback on a style, every Marathi string
      // rendered with it lands in whatever face the handset happened to ship.
      final t = esurveyTheme().textTheme;
      for (final e in <String, TextStyle?>{
        'bodyLarge': t.bodyLarge,
        'bodyMedium': t.bodyMedium,
        'titleMedium': t.titleMedium,
        'labelLarge': t.labelLarge,
        'headlineSmall': t.headlineSmall,
      }.entries) {
        expect(
          e.value?.fontFamilyFallback,
          contains('Noto Sans Devanagari'),
          reason: '${e.key}: the bilingual half of this app depends on it',
        );
      }
    });

    test('the pair matches what the console renders', () {
      // A farmer's report should look the same on the handset that captured it
      // as on the console that decides it. Both name Noto Sans first and Noto
      // Sans Devanagari behind it; see web-platform/app/layout.tsx.
      final b = esurveyTheme().textTheme.bodyMedium;
      expect(b?.fontFamily, 'Noto Sans');
      expect(b?.fontFamilyFallback?.first, 'Noto Sans Devanagari');
    });
  });

  group('one language, the region\'s', () {
    // Stacking Marathi over English on every label doubled the height of
    // every card and read as clutter to the farmer it was meant to help. The
    // region decides the language; both halves stay in the code so a region
    // added later has its words ready.
    tearDown(() => Language.current = AppLanguage.marathi);

    testWidgets('Maharashtra reads Marathi, and only Marathi', (tester) async {
      Language.current = AppLanguage.marathi;
      await tester.pumpWidget(
        wrap(const BilingualText('शेताची हद्द', 'Field boundary')),
      );

      expect(find.text('शेताची हद्द'), findsOneWidget);
      expect(find.text('Field boundary'), findsNothing);
    });

    testWidgets('a region with no strings of its own falls back to English', (
      tester,
    ) async {
      // Never to Marathi, which would be a language nobody there reads.
      Language.current = Language.forState('09');
      await tester.pumpWidget(
        wrap(const BilingualText('शेताची हद्द', 'Field boundary')),
      );

      expect(find.text('Field boundary'), findsOneWidget);
      expect(find.text('शेताची हद्द'), findsNothing);
    });

    test('a stored pair is split, and only a real pair', () {
      Language.current = AppLanguage.marathi;
      expect(
        Language.of('कोड बरोबर नाही. · That code is not correct.'),
        'कोड बरोबर नाही.',
      );
      // A survey number and a date joined by the same dot are one fact.
      expect(Language.of('SE-2026 · 20 Aug'), 'SE-2026 · 20 Aug');
    });

    testWidgets('body text stays above the readable floor', (tester) async {
      await tester.pumpWidget(
        wrap(const BilingualText('शेताची हद्द', 'Field boundary')),
      );
      final text = tester.widget<Text>(find.text('शेताची हद्द'));
      expect(text.style!.fontSize!, greaterThanOrEqualTo(14));
    });

    testWidgets('Devanagari has room to render', (tester) async {
      // The shirorekha and conjuncts sit above and below the Latin band. A
      // tight line height clips them, and a clipped Marathi glyph is a
      // different word.
      await tester.pumpWidget(
        wrap(const BilingualText('शेताची हद्द', 'Field boundary')),
      );

      final mr = tester.widget<Text>(find.text('शेताची हद्द'));
      expect(mr.style!.height!, greaterThanOrEqualTo(1.3));
    });
  });

  group('status is never colour alone', () {
    testWidgets('a pill always carries its words', (tester) async {
      // In sun the pale tint is the first thing to bleach away, and for a
      // colour-blind reader it was never a signal to begin with.
      await tester.pumpWidget(
        wrap(const StatusPill.negligible('हद्द नोंदवली')),
      );
      expect(find.text('हद्द नोंदवली'), findsOneWidget);
    });

    testWidgets('the label is what carries the meaning, not the colour', (
      tester,
    ) async {
      // The three severities differ in hue and barely at all in lightness --
      // every foreground is dark because every one is text on a pale tint,
      // and making one lighter to separate them would cost it contrast
      // against its own background. That is the right trade, and it means a
      // reader who cannot separate red from green has only the words.
      //
      // So the words are not optional, and this asserts they are there for
      // every severity rather than trusting that they will be.
      for (final pill in [
        const StatusPill.negligible('निकाली'),
        const StatusPill.moderate('प्रतीक्षेत'),
        const StatusPill.severe('नाकारली'),
      ]) {
        await tester.pumpWidget(wrap(pill));
        expect(find.text(pill.label), findsOneWidget);
        expect(pill.label.trim(), isNotEmpty);
      }
    });
  });

  group('readable in direct sun', () {
    test('body text clears the AA threshold on white', () {
      // 4.5:1 is the floor indoors. Sun is worse than indoors, which is why
      // the palette aims well past it.
      expect(contrast(Palette.ink, Palette.surface), greaterThan(4.5));
    });

    test('even the most muted text stays legible', () {
      // A caption nobody can read in a field is a caption that is not there.
      expect(contrast(Palette.inkMuted, Palette.surface), greaterThan(4.5));
    });

    test('each severity foreground works on its own tint', () {
      final pairs = [
        (Palette.negligibleFg, Palette.negligibleBg),
        (Palette.moderateFg, Palette.moderateBg),
        (Palette.severeFg, Palette.severeBg),
      ];

      for (final (fg, bg) in pairs) {
        expect(contrast(fg, bg), greaterThan(4.5));
      }
    });
  });
}
