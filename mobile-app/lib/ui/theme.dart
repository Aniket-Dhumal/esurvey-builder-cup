import 'package:flutter/material.dart';

import '../core/language.dart';

/// Colour constants for eSurvey.
///
/// Calm by default. Colour is spent on the one thing a farmer has to act on --
/// a claim that did not send, a walk left half done -- and everything else sits
/// on quiet neutrals. A screen where every card has a thick coloured border
/// has no way left to say that one of them matters.
///
/// Text still clears 7:1 on its background: a phone is read outdoors, and
/// calm is not the same thing as faint.
class Palette {
  Palette._(); // Constants only; instantiating this is a mistake.

  /// The brand green. Matches the web console's --primary, so a farmer shown
  /// the console at a taluka office recognises it as the same product.
  static const Color primary = Color(0xFF1C6D20);

  /// Headings and text-grade green. 12:1 on white.
  static const Color primaryDark = Color(0xFF0F3D2A);

  /// The pale green behind selected items and the navigation indicator.
  static const Color primarySoft = Color(0xFFE2EFDA);

  /// The bright leaf from the mark. Decoration only -- never text.
  static const Color leaf = Color(0xFF529632);

  /// Body text. Near-black rather than pure black, which blooms on cheap LCDs.
  static const Color ink = Color(0xFF1A1D19);

  /// Secondary text. 7.4:1 on white, so it stays readable in sun.
  static const Color inkMuted = Color(0xFF50574D);

  static const Color surface = Color(0xFFFFFFFF);

  /// The page behind the cards.
  static const Color canvas = Color(0xFFF5F7F3);

  /// Grouped rows, empty states, and inputs at rest.
  static const Color canvasSunk = Color(0xFFECF0E8);

  /// Input borders.
  static const Color outline = Color(0xFFB8C2B1);

  /// Card borders and dividers. Decorative only.
  static const Color outlineSoft = Color(0xFFE0E6DB);

  // Tones. Every foreground holds >= 6:1 on its own tint, and hue is never the
  // only signal: each use carries words or an icon as well.

  static const Color negligibleFg = Color(0xFF14572B);
  static const Color negligibleBg = Color(0xFFE6F3E8);

  static const Color moderateFg = Color(0xFF6B4A00);
  static const Color moderateBg = Color(0xFFFCF1D8);

  static const Color severeFg = Color(0xFF9A1F1F);
  static const Color severeBg = Color(0xFFFBE9E7);

  static const Color infoFg = Color(0xFF1D4C73);
  static const Color infoBg = Color(0xFFE6EFF7);
}

/// Spacing, so screens share one rhythm.
class Space {
  Space._();

  static const double xs = 4;
  static const double sm = 8;
  static const double md = 12;
  static const double lg = 16;
  static const double xl = 24;
  static const double xxl = 32;

  /// The page's side margin.
  static const EdgeInsets page = EdgeInsets.fromLTRB(16, 8, 16, 32);
}

/// Corner radii.
class Radii {
  Radii._();

  static const double sm = 10;
  static const double md = 14;
  static const double lg = 18;
  static const double xl = 24;
}

/// The one theme for the whole app.
///
/// Material 3, with type a step lighter and smaller than the app used to use.
/// Targets stay large -- 52dp buttons, 48dp icons -- because the phone is held
/// in one hand outdoors; the weight came off the letters, not the targets.
ThemeData esurveyTheme() {
  const textTheme = TextTheme(
    headlineMedium: TextStyle(
      fontSize: 26,
      height: 1.3,
      fontWeight: FontWeight.w600,
      color: Palette.ink,
      letterSpacing: -0.2,
    ),
    headlineSmall: TextStyle(
      fontSize: 22,
      height: 1.35,
      fontWeight: FontWeight.w600,
      color: Palette.ink,
    ),
    titleLarge: TextStyle(
      fontSize: 19,
      height: 1.35,
      fontWeight: FontWeight.w600,
      color: Palette.ink,
    ),
    titleMedium: TextStyle(
      fontSize: 16,
      height: 1.4,
      fontWeight: FontWeight.w600,
      color: Palette.ink,
    ),
    titleSmall: TextStyle(
      fontSize: 14,
      height: 1.4,
      fontWeight: FontWeight.w600,
      color: Palette.ink,
    ),
    // Devanagari's shirorekha and stacked conjuncts need the taller line.
    bodyLarge: TextStyle(fontSize: 16, height: 1.5, color: Palette.ink),
    bodyMedium: TextStyle(fontSize: 15, height: 1.5, color: Palette.ink),
    bodySmall: TextStyle(fontSize: 13, height: 1.45, color: Palette.inkMuted),
    labelLarge: TextStyle(
      fontSize: 15,
      height: 1.3,
      fontWeight: FontWeight.w600,
    ),
    labelMedium: TextStyle(
      fontSize: 13,
      height: 1.35,
      fontWeight: FontWeight.w500,
      color: Palette.inkMuted,
    ),
    labelSmall: TextStyle(
      fontSize: 12,
      height: 1.35,
      fontWeight: FontWeight.w500,
      color: Palette.inkMuted,
    ),
  );

  const buttonText = TextStyle(
    fontSize: 15,
    fontWeight: FontWeight.w600,
    height: 1.3,
  );
  final buttonShape = RoundedRectangleBorder(
    borderRadius: BorderRadius.circular(Radii.md),
  );
  const buttonSize = Size(64, 52);

  return ThemeData(
    useMaterial3: true,

    // Noto Sans, bundled in the APK, with Noto Sans Devanagari behind it for
    // Marathi. Set once here so no screen falls back to the platform face.
    fontFamily: 'Noto Sans',
    fontFamilyFallback: const ['Noto Sans Devanagari'],

    colorScheme: const ColorScheme.light(
      primary: Palette.primary,
      onPrimary: Colors.white,
      primaryContainer: Palette.primarySoft,
      onPrimaryContainer: Palette.primaryDark,
      secondary: Palette.primaryDark,
      onSecondary: Colors.white,
      secondaryContainer: Palette.primarySoft,
      onSecondaryContainer: Palette.primaryDark,
      surface: Palette.surface,
      onSurface: Palette.ink,
      onSurfaceVariant: Palette.inkMuted,
      surfaceContainerLowest: Palette.surface,
      surfaceContainerLow: Palette.canvas,
      surfaceContainer: Palette.canvas,
      surfaceContainerHigh: Palette.canvasSunk,
      surfaceContainerHighest: Palette.canvasSunk,
      outline: Palette.outline,
      outlineVariant: Palette.outlineSoft,
      error: Palette.severeFg,
      onError: Colors.white,
      errorContainer: Palette.severeBg,
      onErrorContainer: Palette.severeFg,
    ),
    scaffoldBackgroundColor: Palette.canvas,
    textTheme: textTheme,
    materialTapTargetSize: MaterialTapTargetSize.padded,
    visualDensity: VisualDensity.standard,

    appBarTheme: const AppBarTheme(
      backgroundColor: Palette.canvas,
      foregroundColor: Palette.ink,
      surfaceTintColor: Colors.transparent,
      elevation: 0,
      scrolledUnderElevation: 0.5,
      shadowColor: Palette.outlineSoft,
      centerTitle: false,
      titleSpacing: 16,
      titleTextStyle: TextStyle(
        fontFamily: 'Noto Sans',
        fontFamilyFallback: ['Noto Sans Devanagari'],
        fontSize: 19,
        height: 1.3,
        fontWeight: FontWeight.w600,
        color: Palette.ink,
      ),
    ),

    cardTheme: CardThemeData(
      color: Palette.surface,
      surfaceTintColor: Colors.transparent,
      elevation: 0,
      margin: EdgeInsets.zero,
      shape: RoundedRectangleBorder(
        borderRadius: BorderRadius.circular(Radii.lg),
        side: const BorderSide(color: Palette.outlineSoft),
      ),
    ),

    navigationBarTheme: NavigationBarThemeData(
      height: 68,
      backgroundColor: Palette.surface,
      surfaceTintColor: Colors.transparent,
      elevation: 0,
      indicatorColor: Palette.primarySoft,
      labelTextStyle: WidgetStateProperty.resolveWith(
        (states) => TextStyle(
          fontSize: 12.5,
          height: 1.3,
          fontWeight: states.contains(WidgetState.selected)
              ? FontWeight.w600
              : FontWeight.w500,
          color: states.contains(WidgetState.selected)
              ? Palette.primaryDark
              : Palette.inkMuted,
        ),
      ),
      iconTheme: WidgetStateProperty.resolveWith(
        (states) => IconThemeData(
          size: 24,
          color: states.contains(WidgetState.selected)
              ? Palette.primaryDark
              : Palette.inkMuted,
        ),
      ),
    ),

    filledButtonTheme: FilledButtonThemeData(
      style: FilledButton.styleFrom(
        minimumSize: buttonSize,
        textStyle: buttonText,
        shape: buttonShape,
        backgroundColor: Palette.primary,
        foregroundColor: Colors.white,
        padding: const EdgeInsets.symmetric(horizontal: 20),
      ),
    ),
    elevatedButtonTheme: ElevatedButtonThemeData(
      style: ElevatedButton.styleFrom(
        minimumSize: buttonSize,
        textStyle: buttonText,
        shape: buttonShape,
        elevation: 0,
      ),
    ),
    outlinedButtonTheme: OutlinedButtonThemeData(
      style: OutlinedButton.styleFrom(
        minimumSize: buttonSize,
        textStyle: buttonText,
        shape: buttonShape,
        foregroundColor: Palette.primaryDark,
        side: const BorderSide(color: Palette.outline),
        padding: const EdgeInsets.symmetric(horizontal: 20),
      ),
    ),
    textButtonTheme: TextButtonThemeData(
      style: TextButton.styleFrom(
        minimumSize: const Size(48, 48),
        textStyle: buttonText,
        shape: buttonShape,
        foregroundColor: Palette.primary,
      ),
    ),
    iconButtonTheme: IconButtonThemeData(
      style: IconButton.styleFrom(
        minimumSize: const Size(48, 48),
        foregroundColor: Palette.ink,
      ),
    ),

    inputDecorationTheme: InputDecorationTheme(
      filled: true,
      fillColor: Palette.surface,
      contentPadding: const EdgeInsets.symmetric(horizontal: 16, vertical: 16),
      labelStyle: const TextStyle(color: Palette.inkMuted),
      floatingLabelStyle: const TextStyle(color: Palette.primary),
      hintStyle: const TextStyle(color: Palette.inkMuted),
      border: OutlineInputBorder(
        borderRadius: BorderRadius.circular(Radii.md),
        borderSide: const BorderSide(color: Palette.outline),
      ),
      enabledBorder: OutlineInputBorder(
        borderRadius: BorderRadius.circular(Radii.md),
        borderSide: const BorderSide(color: Palette.outline),
      ),
      focusedBorder: OutlineInputBorder(
        borderRadius: BorderRadius.circular(Radii.md),
        borderSide: const BorderSide(color: Palette.primary, width: 2),
      ),
      errorBorder: OutlineInputBorder(
        borderRadius: BorderRadius.circular(Radii.md),
        borderSide: const BorderSide(color: Palette.severeFg),
      ),
      focusedErrorBorder: OutlineInputBorder(
        borderRadius: BorderRadius.circular(Radii.md),
        borderSide: const BorderSide(color: Palette.severeFg, width: 2),
      ),
    ),

    listTileTheme: const ListTileThemeData(
      contentPadding: EdgeInsets.symmetric(horizontal: 16, vertical: 2),
      minVerticalPadding: 10,
      iconColor: Palette.inkMuted,
      titleTextStyle: TextStyle(
        fontFamily: 'Noto Sans',
        fontFamilyFallback: ['Noto Sans Devanagari'],
        fontSize: 15.5,
        height: 1.4,
        fontWeight: FontWeight.w500,
        color: Palette.ink,
      ),
      subtitleTextStyle: TextStyle(
        fontFamily: 'Noto Sans',
        fontFamilyFallback: ['Noto Sans Devanagari'],
        fontSize: 13,
        height: 1.4,
        color: Palette.inkMuted,
      ),
    ),

    dialogTheme: DialogThemeData(
      backgroundColor: Palette.surface,
      surfaceTintColor: Colors.transparent,
      shape: RoundedRectangleBorder(
        borderRadius: BorderRadius.circular(Radii.xl),
      ),
      titleTextStyle: textTheme.titleLarge,
      contentTextStyle: textTheme.bodyMedium,
    ),
    bottomSheetTheme: const BottomSheetThemeData(
      backgroundColor: Palette.surface,
      surfaceTintColor: Colors.transparent,
      showDragHandle: true,
      shape: RoundedRectangleBorder(
        borderRadius: BorderRadius.vertical(top: Radius.circular(Radii.xl)),
      ),
    ),
    snackBarTheme: SnackBarThemeData(
      behavior: SnackBarBehavior.floating,
      backgroundColor: Palette.ink,
      contentTextStyle: const TextStyle(
        fontSize: 14,
        height: 1.4,
        color: Colors.white,
      ),
      shape: RoundedRectangleBorder(
        borderRadius: BorderRadius.circular(Radii.sm),
      ),
    ),
    chipTheme: ChipThemeData(
      backgroundColor: Palette.surface,
      selectedColor: Palette.primarySoft,
      side: const BorderSide(color: Palette.outline),
      labelStyle: const TextStyle(fontSize: 14, color: Palette.ink),
      shape: RoundedRectangleBorder(
        borderRadius: BorderRadius.circular(Radii.sm),
      ),
    ),
    progressIndicatorTheme: const ProgressIndicatorThemeData(
      color: Palette.primary,
      linearTrackColor: Palette.canvasSunk,
    ),
    dividerTheme: const DividerThemeData(
      color: Palette.outlineSoft,
      thickness: 1,
      space: 1,
    ),
  );
}

/// A line of text in the region's language.
///
/// Callers still hand over both languages, so a region added later has its
/// words ready; only one is ever shown.
class BilingualText extends StatelessWidget {
  const BilingualText(
    this.marathi,
    this.english, {
    super.key,
    this.textAlign,
    this.style,
  });

  final String marathi;
  final String english;
  final TextAlign? textAlign;
  final TextStyle? style;

  @override
  Widget build(BuildContext context) {
    return Text(
      tr(marathi, english),
      style: style ?? Theme.of(context).textTheme.bodyMedium,
      textAlign: textAlign,
    );
  }
}

/// The one primary action on a screen: full width.
class BigButton extends StatelessWidget {
  const BigButton({
    super.key,
    required this.label,
    required this.onPressed,
    this.icon,
  });

  /// Either one language, or a "मराठी · English" pair.
  final String label;
  final VoidCallback? onPressed;
  final IconData? icon;

  @override
  Widget build(BuildContext context) {
    final text = Text(Language.of(label));
    return SizedBox(
      width: double.infinity,
      child: icon == null
          ? FilledButton(onPressed: onPressed, child: text)
          : FilledButton.icon(
              onPressed: onPressed,
              icon: Icon(icon, size: 20),
              label: text,
            ),
    );
  }
}

/// A small tinted label for a status or severity.
class StatusPill extends StatelessWidget {
  const StatusPill(
    this.label, {
    super.key,
    required this.foreground,
    required this.background,
    this.icon,
  });

  const StatusPill.negligible(this.label, {super.key, this.icon})
    : foreground = Palette.negligibleFg,
      background = Palette.negligibleBg;

  const StatusPill.moderate(this.label, {super.key, this.icon})
    : foreground = Palette.moderateFg,
      background = Palette.moderateBg;

  const StatusPill.severe(this.label, {super.key, this.icon})
    : foreground = Palette.severeFg,
      background = Palette.severeBg;

  const StatusPill.info(this.label, {super.key, this.icon})
    : foreground = Palette.infoFg,
      background = Palette.infoBg;

  final String label;
  final Color foreground;
  final Color background;
  final IconData? icon;

  @override
  Widget build(BuildContext context) {
    return Container(
      padding: const EdgeInsets.symmetric(horizontal: 10, vertical: 4),
      decoration: BoxDecoration(
        color: background,
        borderRadius: BorderRadius.circular(999),
      ),
      child: Row(
        mainAxisSize: MainAxisSize.min,
        children: [
          if (icon != null) ...[
            Icon(icon, size: 14, color: foreground),
            const SizedBox(width: 4),
          ],
          Flexible(
            child: Text(
              Language.of(label),
              style: TextStyle(
                fontSize: 12.5,
                height: 1.35,
                fontWeight: FontWeight.w600,
                color: foreground,
              ),
            ),
          ),
        ],
      ),
    );
  }
}

/// A white card on the page.
class AppCard extends StatelessWidget {
  const AppCard({
    super.key,
    required this.child,
    this.onTap,
    this.padding = const EdgeInsets.all(Space.lg),
    this.margin = const EdgeInsets.only(bottom: Space.md),
  });

  final Widget child;
  final VoidCallback? onTap;
  final EdgeInsetsGeometry padding;
  final EdgeInsetsGeometry margin;

  @override
  Widget build(BuildContext context) {
    return Padding(
      padding: margin,
      child: Card(
        clipBehavior: Clip.antiAlias,
        child: InkWell(
          onTap: onTap,
          child: Padding(padding: padding, child: child),
        ),
      ),
    );
  }
}

/// How much a [Notice] needs attention.
enum Tone {
  info(Palette.infoFg, Palette.infoBg),
  success(Palette.negligibleFg, Palette.negligibleBg),
  warning(Palette.moderateFg, Palette.moderateBg),
  danger(Palette.severeFg, Palette.severeBg);

  const Tone(this.foreground, this.background);

  final Color foreground;
  final Color background;
}

/// Something the farmer should know or act on, above the page's content.
///
/// Tinted, with no border: the tint and the icon carry the tone, and the
/// words carry the meaning.
class Notice extends StatelessWidget {
  const Notice({
    super.key,
    required this.tone,
    required this.icon,
    required this.title,
    this.message,
    this.below,
    this.actions = const [],
    this.margin = const EdgeInsets.only(bottom: Space.md),
  });

  final Tone tone;
  final IconData icon;
  final String title;
  final String? message;

  /// Anything that belongs between the words and the buttons: a progress bar,
  /// a figure, a small list. Indented to the text, not the icon.
  final Widget? below;
  final List<Widget> actions;
  final EdgeInsetsGeometry margin;

  @override
  Widget build(BuildContext context) {
    final text = Theme.of(context).textTheme;
    return Container(
      width: double.infinity,
      margin: margin,
      padding: const EdgeInsets.all(Space.lg),
      decoration: BoxDecoration(
        color: tone.background,
        borderRadius: BorderRadius.circular(Radii.lg),
      ),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Row(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              Icon(icon, color: tone.foreground, size: 22),
              const SizedBox(width: Space.md),
              Expanded(
                child: Column(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    Text(
                      Language.of(title),
                      style: text.titleMedium?.copyWith(color: tone.foreground),
                    ),
                    if (message != null) ...[
                      const SizedBox(height: 2),
                      Text(
                        Language.of(message!),
                        style: text.bodyMedium?.copyWith(color: Palette.ink),
                      ),
                    ],
                  ],
                ),
              ),
            ],
          ),
          if (below != null) ...[
            const SizedBox(height: Space.md),
            Padding(padding: const EdgeInsets.only(left: 34), child: below),
          ],
          if (actions.isNotEmpty) ...[
            const SizedBox(height: Space.md),
            Padding(
              padding: const EdgeInsets.only(left: 34),
              child: Wrap(
                spacing: Space.sm,
                runSpacing: Space.sm,
                children: actions,
              ),
            ),
          ],
        ],
      ),
    );
  }
}

/// A heading above a group of cards.
class SectionHeader extends StatelessWidget {
  const SectionHeader(this.title, {super.key, this.trailing});

  final String title;
  final Widget? trailing;

  @override
  Widget build(BuildContext context) {
    return Padding(
      padding: const EdgeInsets.fromLTRB(4, Space.lg, 4, Space.sm),
      child: Row(
        children: [
          Expanded(
            child: Text(
              Language.of(title),
              style: Theme.of(
                context,
              ).textTheme.titleMedium?.copyWith(color: Palette.inkMuted),
            ),
          ),
          ?trailing,
        ],
      ),
    );
  }
}

/// Nothing to show, said calmly.
class EmptyState extends StatelessWidget {
  const EmptyState({super.key, required this.icon, required this.message});

  final IconData icon;
  final String message;

  @override
  Widget build(BuildContext context) {
    return Container(
      width: double.infinity,
      margin: const EdgeInsets.only(bottom: Space.md),
      padding: const EdgeInsets.symmetric(
        horizontal: Space.lg,
        vertical: Space.xl,
      ),
      decoration: BoxDecoration(
        color: Palette.canvasSunk,
        borderRadius: BorderRadius.circular(Radii.lg),
      ),
      child: Column(
        children: [
          Icon(icon, color: Palette.inkMuted, size: 28),
          const SizedBox(height: Space.sm),
          Text(
            Language.of(message),
            textAlign: TextAlign.center,
            style: Theme.of(
              context,
            ).textTheme.bodyMedium?.copyWith(color: Palette.inkMuted),
          ),
        ],
      ),
    );
  }
}

/// A small round icon on a tint, leading a row.
class IconBadge extends StatelessWidget {
  const IconBadge(
    this.icon, {
    super.key,
    this.color = Palette.primary,
    this.background = Palette.primarySoft,
    this.size = 40,
  });

  final IconData icon;
  final Color color;
  final Color background;
  final double size;

  @override
  Widget build(BuildContext context) {
    return Container(
      width: size,
      height: size,
      decoration: BoxDecoration(
        color: background,
        borderRadius: BorderRadius.circular(Radii.md),
      ),
      child: Icon(icon, color: color, size: size * 0.55),
    );
  }
}
