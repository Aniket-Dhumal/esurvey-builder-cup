import 'dart:math' as math;

import 'sensors.dart';

/// Reading a quantity out of what a farmer said.
///
/// # Why this is parsed here and not taken from the model
///
/// Because these numbers decide compensation.
///
/// A model asked "what percentage did they say" will answer with a number, and
/// almost always the right one. Almost always is the problem: "सतरा" (17) and
/// "सत्तर" (70) differ by one vowel, and a model that mishears one for the other
/// produces a confident 70 with nothing to show it was a guess. The same claim then
/// carries a figure four times too large, through an approval ladder that has no
/// reason to doubt it.
///
/// So the model transcribes and this parses. A transcription is checkable -- the
/// words are on screen and the farmer hears them read back -- and a parse either
/// succeeds or says it could not. There is no confident wrong answer available.
///
/// # Why the units are local
///
/// Because a farmer says guntha and acre, not hectares. The record is kept in
/// hectares because that is what AgriStack holds, so the conversion has to happen
/// somewhere, and it must happen where it can be tested: forty guntha to the
/// hectare is the kind of constant that is wrong in one place for a year.
///
/// # What it refuses
///
/// Anything ambiguous. A farmer who says "दोन" and nothing else has given a number
/// with no unit, and guessing which they meant -- two hectares, two acres, two
/// guntha -- would be a guess about an area that differs by a hundredfold. The
/// caller asks again instead.

// The conversions live in sensors.dart, beside gunthaOf, which the boundary screen
// has always used. One definition rather than two: they were briefly two, and the
// pair disagreed by a factor of two and a half -- forty guntha is an acre, not a
// hectare.

/// What a spoken quantity turned out to be.
class SpokenQuantity {
  const SpokenQuantity({
    required this.value,
    required this.unit,
    required this.spoken,
  });

  /// The number, in [unit].
  final double value;

  /// 'percent', 'hectare', 'acre', 'guntha', or '' for a bare number.
  final String unit;

  /// What was said, kept so it can be read back for confirmation.
  final String spoken;

  /// The area in hectares, where this is an area at all.
  double? get hectares => switch (unit) {
    'hectare' => value,
    'acre' => value * hectaresPerAcre,
    'guntha' => value * hectaresPerGuntha,
    _ => null,
  };

  /// The percentage, where this is one.
  double? get percent => unit == 'percent' ? value : null;

  /// How to read it back to the farmer, so they can hear what was understood.
  ///
  /// In their own unit, not converted. A farmer who said half a guntha and hears
  /// "nought point zero zero five hectares" cannot tell whether that is what they
  /// meant, and the whole point of reading it back is that they can.
  String get readBack {
    final n = _tidy(value);
    return switch (unit) {
      'percent' => '$n टक्के',
      'hectare' => '$n हेक्टर',
      'acre' => '$n एकर',
      'guntha' => '$n गुंठे',
      _ => n,
    };
  }

  static String _tidy(double v) {
    if (v == v.roundToDouble()) return v.round().toString();
    return v
        .toStringAsFixed(2)
        .replaceFirst(RegExp(r'0+$'), '')
        .replaceFirst(RegExp(r'\.$'), '');
  }

  @override
  String toString() => '$value $unit';
}

/// Marathi number words, up to what anybody says about a field.
///
/// Written out rather than generated. The irregular teens and the twenties are
/// exactly where a generated table goes wrong, and a farmer saying "एकोणतीस" is not
/// helped by an algorithm that nearly works.
const Map<String, int> _marathiNumbers = {
  'शून्य': 0,
  'एक': 1,
  'दोन': 2,
  'तीन': 3,
  'चार': 4,
  'पाच': 5,
  'सहा': 6,
  'सात': 7,
  'आठ': 8,
  'नऊ': 9,
  'दहा': 10,
  'अकरा': 11,
  'बारा': 12,
  'तेरा': 13,
  'चौदा': 14,
  'पंधरा': 15,
  'सोळा': 16,
  'सतरा': 17,
  'अठरा': 18,
  'एकोणीस': 19,
  'वीस': 20,
  'एकवीस': 21,
  'बावीस': 22,
  'तेवीस': 23,
  'चोवीस': 24,
  'पंचवीस': 25,
  'सव्वीस': 26,
  'सत्तावीस': 27,
  'अठ्ठावीस': 28,
  'एकोणतीस': 29,
  'तीस': 30,
  'पस्तीस': 35,
  'चाळीस': 40,
  'पंचेचाळीस': 45,
  'पन्नास': 50,
  'पंचावन्न': 55,
  'साठ': 60,
  'पासष्ट': 65,
  'सत्तर': 70,
  'पंचाहत्तर': 75,
  'ऐंशी': 80,
  'पंचाऐंशी': 85,
  'नव्वद': 90,
  'पंच्याण्णव': 95,
  'शंभर': 100,
};

/// Fractions a farmer says instead of a decimal.
///
/// "दीड" is one and a half and is said far more often than "एक पूर्णांक पाच". A
/// parser that does not know it fails on the commonest way to describe a plot.
const Map<String, double> _marathiFractions = {
  'अर्धा': 0.5,
  'अर्धी': 0.5,
  'अर्धे': 0.5,
  'पाऊण': 0.75,
  'सव्वा': 1.25,
  'दीड': 1.5,
  'अडीच': 2.5,
  'साडेतीन': 3.5,
};

/// Unit words, Marathi and English, as they are actually said.
const Map<String, String> _units = {
  'टक्के': 'percent',
  'टक्का': 'percent',
  'percent': 'percent',
  '%': 'percent',
  'हेक्टर': 'hectare',
  'हेक्टरी': 'hectare',
  'hectare': 'hectare',
  'hectares': 'hectare',
  'ha': 'hectare',
  'एकर': 'acre',
  'acre': 'acre',
  'acres': 'acre',
  'गुंठा': 'guntha',
  'गुंठे': 'guntha',
  'गुंठी': 'guntha',
  'guntha': 'guntha',
  'gunthas': 'guntha',
};

/// Devanagari digits, which a transcriber may return instead of ASCII.
const Map<String, String> _devanagariDigits = {
  '०': '0',
  '१': '1',
  '२': '2',
  '३': '3',
  '४': '4',
  '५': '5',
  '६': '6',
  '७': '7',
  '८': '8',
  '९': '9',
};

/// Read a quantity out of a transcription, or return null.
///
/// Null is a real answer and the caller must treat it as one: it means the farmer
/// should be asked again, not that a default should be used. A defaulted area or
/// percentage is a figure nobody said that an officer will nonetheless sign.
SpokenQuantity? parseSpokenQuantity(String said) {
  final original = said.trim();
  if (original.isEmpty) return null;

  var text = original.toLowerCase();
  _devanagariDigits.forEach((dev, ascii) => text = text.replaceAll(dev, ascii));

  final unit = _findUnit(text);

  // Digits win over words when both are present. A transcriber given "seventy
  // percent" often returns "70 टक्के", and the digits are the surer half.
  final digits = RegExp(r'(\d+(?:[.,]\d+)?)').firstMatch(text);
  if (digits != null) {
    final raw = digits.group(1)!.replaceAll(',', '.');
    final value = double.tryParse(raw);
    if (value != null && value >= 0) {
      return _checked(value, unit, original);
    }
  }

  final words = text
      .split(RegExp(r'[\s,]+'))
      .where((w) => w.isNotEmpty)
      .toList();

  // A fraction on its own, or a fraction and a whole: "दीड" or "अडीच एकर".
  for (final w in words) {
    final fraction = _marathiFractions[w];
    if (fraction != null) return _checked(fraction, unit, original);
  }

  // Number words, combined where they are said as a pair: "तीस पाच" is not
  // idiomatic, but "एक पूर्णांक पाच" is a decimal and "शंभर" is a hundred, so only
  // the largest single word is taken. Adding a summing rule would turn "दोन तीन"
  // into five, which nobody said.
  int? best;
  for (final w in words) {
    final n = _marathiNumbers[w];
    if (n != null && (best == null || n > best)) best = n;
  }
  if (best != null) return _checked(best.toDouble(), unit, original);

  return null;
}

/// Refuse a figure that cannot be what the farmer meant.
///
/// A percentage above a hundred, or an area a farmer could not be standing in, is
/// a mis-hearing rather than a measurement. Returning null sends the caller back to
/// ask, which is the only safe answer: the alternative is clamping, and a clamped
/// figure looks exactly like a stated one.
SpokenQuantity? _checked(double value, String unit, String spoken) {
  if (value.isNaN || value.isInfinite || value < 0) return null;
  if (unit == 'percent' && value > 100) return null;
  // Twenty hectares is fifty acres. A smallholder saying that has been misheard,
  // and the registry's own extent is the check that matters anyway.
  if (unit != 'percent' && unit.isNotEmpty && value > 200) return null;
  return SpokenQuantity(value: value, unit: unit, spoken: spoken);
}

String _findUnit(String text) {
  // Longest match first, so "हेक्टरी" is not read as something shorter inside it.
  final keys = _units.keys.toList()
    ..sort((a, b) => b.length.compareTo(a.length));
  for (final key in keys) {
    if (text.contains(key)) return _units[key]!;
  }
  return '';
}

/// Whether a spoken area agrees with what the registry holds.
///
/// # Why this is asked at all
///
/// Because the sown extent now decides how far the walk asks a farmer to move, and
/// a mistyped or misheard extent is unrecoverable in the field: half a guntha
/// entered as a hundred hectares asks for hundreds of metres on a plot seven metres
/// across, and the record button never comes alive.
///
/// The registry's own figure is the check. A farmer may legitimately claim less than
/// they own -- part of the plot, one crop of two -- so more is the suspicious
/// direction, not less.
bool areaLooksPlausible(double claimedHa, double registryHa) {
  if (claimedHa <= 0) return false;
  if (registryHa <= 0) return true; // nothing to check against
  // A tenth over is rounding and a farmer's own estimate; twice over is a
  // different unit misheard.
  return claimedHa <= registryHa * 1.1;
}

/// The nearest sensible correction when an area looks wrong.
///
/// Offered rather than applied. "Did you mean half a guntha?" is a question a
/// farmer can answer; silently substituting it is a figure nobody said.
SpokenQuantity? likelyMeant(SpokenQuantity said, double registryHa) {
  final ha = said.hectares;
  if (ha == null || registryHa <= 0) return null;
  if (areaLooksPlausible(ha, registryHa)) return null;

  // The same number in a smaller unit is the commonest mishearing: guntha heard as
  // hectare is a fortyfold error, acre as hectare is two and a half.
  for (final unit in const ['guntha', 'acre']) {
    final candidate = SpokenQuantity(
      value: said.value,
      unit: unit,
      spoken: said.spoken,
    );
    final asHa = candidate.hectares!;
    if (areaLooksPlausible(asHa, registryHa) && asHa >= registryHa * 0.1) {
      return candidate;
    }
  }
  return null;
}

/// How much of a hectare a figure is, for reading a small plot back sensibly.
///
/// A farmer with half a guntha hears "अर्धा गुंठा", not "0.005 हेक्टर". Forty to the
/// hectare, so anything under a tenth of a hectare is clearer in guntha.
String describeArea(double hectares) {
  if (hectares <= 0) return '0';
  if (hectares < 0.1) {
    final guntha = hectares / hectaresPerGuntha;
    final tidy = (guntha * 100).round() / 100;
    return '${SpokenQuantity._tidy(tidy)} गुंठे';
  }
  return '${SpokenQuantity._tidy((hectares * 100).round() / 100)} हेक्टर';
}

/// A hectare figure in acres, for reading back to a farmer who speaks in acres.
double acresOf(double hectares) => hectares / hectaresPerAcre;

/// Guntha of a hectare figure, rounded to something a person would say.
///
/// Named apart from sensors' [gunthaOf], which returns the exact figure: nobody
/// says "98.84 guntha", and a screen that prints it looks like a machine rather
/// than an answer.
int gunthaRounded(double hectares) => math.max(0, gunthaOf(hectares).round());
