import 'language.dart';

// Dates on the wire.
//
// The server and this app disagree about what a date is, and the disagreement
// is not visible until a real server parses a real report.

/// The incident date as an instant the server will accept.
///
/// # Why this exists at all
///
/// A survey event carries `incident_start` as a SQL date, so the API sends
/// "2026-08-20". The report's `occurred_at` is a Go time.Time and refuses
/// anything that is not RFC 3339. Passing the date straight through made
/// every report a 400 -- INVALID_BODY, before the server's own validation
/// even ran.
///
/// The farmer sees none of that. The queue treats a 400 as a refusal the
/// server will repeat, records it and moves on, which is right for a genuine
/// refusal and exactly wrong here: the app shows the report as sent and the
/// office never receives it.
///
/// # What it produces
///
/// Midnight UTC on the day the event names. The hour is not known -- the form
/// deliberately does not ask, because nobody should lose a claim over whether
/// the hail came at four or at five -- and the assessment is made against the
/// event's window rather than the hour.
///
/// An unparseable or missing date falls back to now. Wrong by a few days at
/// worst and the report goes; refusing would lose the claim over a field the
/// farmer never filled in.
String occurredAtFrom(Object? incidentStart) {
  final day = DateTime.tryParse('${incidentStart ?? ''}');
  if (day == null) return DateTime.now().toUtc().toIso8601String();
  return DateTime.utc(day.year, day.month, day.day).toIso8601String();
}

const _monthsMr = [
  'जानेवारी', 'फेब्रुवारी', 'मार्च', 'एप्रिल', 'मे', 'जून', //
  'जुलै', 'ऑगस्ट', 'सप्टेंबर', 'ऑक्टोबर', 'नोव्हेंबर', 'डिसेंबर',
];
const _monthsEn = [
  'Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', //
  'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec',
];

/// A day as a person says it: "20 ऑगस्ट 2026", not "2026-08-20".
///
/// Anything unparseable is shown as it came, so a malformed date is still
/// visible rather than silently blank.
String formatDay(Object? value) {
  final raw = '${value ?? ''}';
  final d = DateTime.tryParse(raw);
  if (d == null) return raw;
  final local = d.isUtc && raw.length > 10 ? d.toLocal() : d;
  final months = Language.isMarathi ? _monthsMr : _monthsEn;
  return '${local.day} ${months[local.month - 1]} ${local.year}';
}
