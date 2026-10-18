import 'package:flutter/foundation.dart';

/// Where the app points, and the few numbers worth naming.
///
/// Compile-time rather than a settings screen: a farmer has no business being
/// asked for a server address, and a build that can be pointed anywhere is a
/// build somebody can point at a machine that collects claims.
///
///   flutter run --dart-define=ESURVEY_API=https://esurvey.example.gov.in
class Config {
  const Config._();

  /// The platform API.
  ///
  /// The default is the Android emulator's route to the host machine, which is
  /// what a developer wants and which no real handset can reach.
  ///
  /// It used to say here that a release build which forgot the define would fail
  /// loudly on the first request. It does not, and the claim cost a test round:
  /// an unreachable host does not refuse a connection, it goes unanswered, so the
  /// app sat on "sending" until the timeout and the only symptom was a farmer
  /// waiting. [pointsNowhere] is that promise actually kept.
  static const apiBase = String.fromEnvironment(
    'ESURVEY_API',
    defaultValue: 'http://10.0.2.2:8090',
  );

  /// Whether this is a release build pointed somewhere a handset cannot reach.
  ///
  /// # Why the app refuses to start rather than trying
  ///
  /// Because the failure is otherwise indistinguishable, from the outside, from a
  /// village with no signal -- which is the condition this app is built to work
  /// in, so every screen is designed to wait patiently through it. A build with
  /// the wrong address waits patiently forever, and the person holding the phone
  /// concludes the network is bad.
  ///
  /// The console learned the same lesson twice and now fails its build over it.
  /// Dart cannot fail a build, so this fails the launch: whoever made the APK sees
  /// the reason on the first screen, and no farmer is ever handed a build that
  /// gets this far, because the only way to reach it is to forget the define.
  ///
  /// Release only. A developer running against a local API is the case the default
  /// exists for.
  static bool get pointsNowhere => kReleaseMode && unreachable(apiBase);

  /// Whether [base] names somewhere a handset in a field cannot reach.
  ///
  /// Separate from [pointsNowhere] so it can be tested. The release check cannot
  /// be: kReleaseMode is a compile-time constant and is false under the test
  /// runner, so a test of the whole condition would only ever exercise the branch
  /// that does nothing -- which is how a guard comes to be shipped broken.
  static bool unreachable(String base) {
    const nowhere = {'10.0.2.2', 'localhost', '127.0.0.1', '::1', '0.0.0.0'};
    final host = Uri.tryParse(base)?.host ?? '';
    return host.isEmpty || nowhere.contains(host);
  }

  /// The state this build serves, by LGD state code. Decides the language
  /// every screen is written in; see Language.
  static const stateCode = String.fromEnvironment(
    'ESURVEY_STATE',
    defaultValue: '27',
  );

  /// This build's version, sent with every request.
  ///
  /// The server refuses writes from a build below its configured minimum with
  /// a 426, which the queue treats as "keep everything and ask the farmer to
  /// update" rather than as a refusal of the claim. Kept in step with
  /// pubspec.yaml; a release build can override it with the define.
  static const appVersion = String.fromEnvironment(
    'ESURVEY_APP_VERSION',
    defaultValue: '1.16.0',
  );

  /// How long a request may take before it is treated as a network failure.
  ///
  /// Generous. A rural connection that takes twenty seconds to send a
  /// photograph is working, and giving up on it at five would turn a slow
  /// upload into a failed one and burn a retry.
  static const requestTimeout = Duration(seconds: 45);

  /// Uploads get longer still: a photograph is megabytes over a connection
  /// that may be sharing a tower with a village.
  static const uploadTimeout = Duration(minutes: 3);

  /// How often the queue tries to drain while the app is open.
  static const syncInterval = Duration(seconds: 30);

  /// The accuracy beyond which a position is not usable for a boundary corner
  /// or a geofenced photograph.
  ///
  /// Fifty metres is a phone that has not seen the sky. A corner marked on
  /// that could put a boundary on a neighbour's field.
  static const worstUsableAccuracyM = 50.0;

  /// The accuracy at which a position is as good as a handset will give.
  static const goodAccuracyM = 10.0;
}
