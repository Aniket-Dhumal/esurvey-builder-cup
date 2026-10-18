import 'config.dart';

/// The language a screen is written in.
///
/// # One language, the region's
///
/// Screens used to stack Marathi over English on every label, button and
/// message. It read as clutter to the farmer it was meant to help, and it
/// doubled the height of every card. A farmer in Maharashtra reads Marathi;
/// the app now shows Marathi and nothing else.
///
/// The language follows the state the deployment serves, not the handset's
/// locale. A farmer's phone is often set to English by whoever sold it, and
/// that says nothing about what they read.
///
/// # Adding a region
///
/// Only Marathi is written today. A state without its own strings falls back
/// to English, which every line in the app already carries -- never to
/// Marathi, which would be a language nobody there reads.
enum AppLanguage {
  marathi('mr'),
  english('en');

  const AppLanguage(this.code);

  final String code;
}

class Language {
  const Language._();

  /// The language every screen renders in. Set once at startup from the
  /// region; tests may set it directly.
  static AppLanguage current = forState(Config.stateCode);

  /// The language of a state, by its LGD state code.
  static AppLanguage forState(String stateCode) => switch (stateCode) {
    '27' => AppLanguage.marathi, // Maharashtra
    _ => AppLanguage.english,
  };

  static bool get isMarathi => current == AppLanguage.marathi;

  /// One of a pair, in the region's language.
  static String pick(String mr, String en) => isMarathi ? mr : en;

  /// The region's half of a "मराठी · English" line.
  ///
  /// Only a line that really is a pair is split: Devanagari before the dot and
  /// Latin after it. A survey number and a date joined by the same dot are one
  /// fact, and splitting them would lose half of it.
  static String of(String line) {
    final at = line.indexOf(' · ');
    if (at <= 0) return line;
    final left = line.substring(0, at);
    final right = line.substring(at + 3);
    if (!_devanagari.hasMatch(left) || !_latinStart.hasMatch(right)) {
      return line;
    }
    return isMarathi ? left.trim() : right.trim();
  }

  static final _devanagari = RegExp('[ऀ-ॿ]');
  static final _latinStart = RegExp(r'^\s*[A-Za-z0-9"“‘(]');
}

/// Shorthand for [Language.pick].
String tr(String mr, String en) => Language.pick(mr, en);
