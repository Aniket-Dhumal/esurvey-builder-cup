import 'dart:io';
import 'dart:typed_data';

import 'package:esurvey/core/api.dart';
import 'package:esurvey/core/db.dart';
import 'package:esurvey/core/link_quality.dart';
import 'package:esurvey/core/live_consent.dart';
import 'package:esurvey/core/session.dart';
import 'package:esurvey/core/voice.dart';
import 'package:esurvey/live/live_audio.dart';
import 'package:esurvey/live/live_session.dart';
import 'package:esurvey/live/live_talk.dart';
import 'package:esurvey/live/survey_mode.dart';
import 'package:esurvey/ui/theme.dart';
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:flutter_tts/flutter_tts.dart';
import 'package:http/testing.dart';
import 'package:http/http.dart' as http;
import 'package:sqflite_common_ffi/sqflite_ffi.dart';

/// Offering the assistant, and -- mostly -- not offering it.
///
/// Five things can each say no: the build flag, the farmer's agreement, the link, the
/// office's budget and the microphone. Four of those refusals have to be invisible. A
/// farmer told "the connection here is too slow for talking" reads it as a fault in an
/// app that is about to work perfectly well without it, and the walk they are standing
/// in the middle of does not need the assistant for anything.
///
/// So most of what is asserted here is absence: that the widget renders nothing at
/// all, and that nothing is spent finding out. The one thing it must actively do is
/// pass each request the assistant makes up to the walk, once, with the model that
/// made it -- because that is what an officer reads a year later when asking why a
/// clip was framed the way it was, and "the assistant suggested it" is only useful if
/// it says which assistant.

/// A secure store kept in memory, as the other suites do.
class _FakeStore implements SecureStore {
  final Map<String, String> values = {};

  @override
  Future<String?> read(String key) async => values[key];

  @override
  Future<void> write(String key, String value) async => values[key] = value;

  @override
  Future<void> delete(String key) async => values.remove(key);

  @override
  Future<void> deleteAll() async => values.clear();
}

/// A speech engine that reports Marathi and says nothing aloud.
class _FakeTts implements FlutterTts {
  @override
  Future<dynamic> get getLanguages async => ['mr-IN'];

  // Every other call on the engine accepts and does nothing. Listing them one by one
  // would be a page of noise.
  @override
  dynamic noSuchMethod(Invocation invocation) async => 1;
}

/// A link good enough to talk over, and a count of how often it was measured.
///
/// The count is half the point. The real probe downloads and uploads 128 KB, which is
/// real traffic on a connection a farmer may be paying for by the megabyte, so the
/// cheap local questions are asked first and a farmer who is not going to be offered
/// the assistant is never measured for.
class _CountingLink extends LinkProbe {
  _CountingLink({required super.api});

  int measurements = 0;

  @override
  Future<LinkQuality> measure({bool force = false}) async {
    measurements++;
    return LinkQuality(
      downKbps: 900,
      upKbps: 900,
      rttMs: 180,
      at: DateTime.now(),
    );
  }
}

/// A link comfortably past the bar, for a survey that decided live.
LinkQuality get talkable =>
    LinkQuality(downKbps: 900, upKbps: 900, rttMs: 180, at: DateTime.now());

/// The microphone and the speaker, replaced by nothing at all.
class _SilentAudio extends LiveAudio {
  final played = <Uint8List>[];

  @override
  Future<bool> startCapture(void Function(Uint8List pcm) onChunk) async => true;

  @override
  Future<void> stopCapture() async {}

  @override
  Future<void> prepareSpeaker() async {}

  @override
  Future<void> play(Uint8List pcm) async => played.add(pcm);

  @override
  Future<void> dispose() async {}
}

/// A session the test speaks for.
///
/// Subclassed rather than hidden behind an interface because the widget takes a
/// [LiveSession]: keeping the real type means a change to its surface breaks this
/// rather than sliding past it.
class _StubSession extends LiveSession {
  _StubSession({
    required super.api,
    required super.consent,
    required super.probe,
  }) : super(farmerId: 'F-1');

  final requests = <String>[];

  @override
  List<String> get askedFor => List.unmodifiable(requests);

  @override
  LiveState get state => LiveState.listening;

  @override
  Future<bool> offer() async => true;

  @override
  Future<bool> start({
    String? crop,
    String? calamity,
    String? season,
    String? district,
  }) async => true;

  @override
  Future<LiveOutcome> stop([String why = 'closed']) async =>
      LiveOutcome(seconds: 0, endedBecause: why);

  /// The assistant asking for something, as the relay would deliver it.
  void asksFor(String request) {
    requests.add(request);
    notifyListeners();
  }

  /// A notification carrying nothing new -- a line of speech, a change of state.
  void stirs() => notifyListeners();
}

late Directory _tmp;

/// The database, opened outside the widget test body.
///
/// sqflite talks to a background isolate, and inside a [testWidgets] body the clock is
/// the test's own: a real await on it never returns and the test hangs until the
/// harness gives up. Opened in setUp, where the clock is still the ordinary one.
late Store store;

Future<Api> reachableApi() async {
  final session = Session(storage: _FakeStore());
  await session.save(
    accessToken: 'a',
    accessTtl: const Duration(minutes: 15),
    refreshToken: 'r',
    principal: {'farmer_id': 'F-1'},
  );
  return Api(
    session: session,
    client: MockClient((_) async => http.Response('{}', 200)),
  );
}

Widget wrap(Widget child) => MaterialApp(
  theme: esurveyTheme(),
  home: Scaffold(body: child),
);

/// The talk button, found by its icon rather than its type.
///
/// `find.byType(OutlinedButton)` matches the runtime type exactly, and
/// `OutlinedButton.icon` builds a private subclass -- so a type finder here reports
/// "no button" whether or not one is on screen, which is the shape of a test that
/// passes for the wrong reason. The icon is also the thing a farmer actually sees.
const talkIcon = Icons.record_voice_over_outlined;

// There is no longer a helper here that waits for the widget's own gates to finish.
//
// It existed because this widget asked three questions of its own before it would
// render -- two of the database and one of the network -- and inside a widget test
// the clock is the test's own, so each await needed a turn of the real event loop
// and a pump to deliver its answer. It also hung a run for ten minutes the first
// time somebody used pumpAndSettle instead.
//
// None of that is needed now: the questions are asked once, before the survey, and
// this widget is handed the answer. A test that constructs a mode renders on the
// first pump. If a gate ever moves back in here, this is the shape of the helper it
// will need, and the reason it was not worth keeping is that nothing should need it.

void main() {
  setUpAll(() {
    sqfliteFfiInit();
    databaseFactory = databaseFactoryFfi;
    _tmp = Directory.systemTemp.createTempSync('esurvey-live-talk-test');
  });

  setUp(() async {
    final dir = Directory(
      '${_tmp.path}/${DateTime.now().microsecondsSinceEpoch}',
    )..createSync(recursive: true);
    store = await Store.open(directory: dir.path);
    // Agreed to, so that consent is never the thing doing the refusing below. A
    // farmer who has agreed is the case where every other refusal has to hold on its
    // own.
    await LiveConsent(store).record(farmerId: 'F-1', granted: true);
  });

  tearDown(() async => store.close());

  tearDownAll(() {
    try {
      _tmp.deleteSync(recursive: true);
    } catch (_) {
      // Windows holds handles briefly after close.
    }
  });

  test('a build that did not ask for the assistant decides offline', () async {
    // liveGuidanceEnabled is a compile-time define, off by default, and off in every
    // build nobody deliberately turned on -- including the trial handsets.
    //
    // Asserted against the decision rather than against the widget, because the
    // decision is where it now lives: the engine is chosen once, before the survey
    // starts, and the walk is handed the answer. A farm must not be charged for a
    // measurement to answer a question the build already settled.
    final probe = _CountingLink(api: await reachableApi());

    final mode = await decideSurveyMode(
      api: await reachableApi(),
      consent: LiveConsent(store),
      farmerId: 'F-1',
      probe: probe,
    );

    expect(liveGuidanceEnabled, isFalse, reason: 'the premise of this test');
    expect(mode.live, isFalse);
    expect(
      probe.measurements,
      0,
      reason: 'a farm was charged for a measurement nobody was going to use',
    );
  });

  test('nothing is decided, and nothing measured, before the farmer is '
      'known', () async {
    // The survey can be opened while the report is still loading. Consent is stored
    // per farmer because a village handset acts for several of them, and an allowance
    // is spent against one of them too, so an empty id has nobody to ask and nobody
    // to charge.
    //
    // In this build the flag refuses first, so what this pins is the second half of
    // the same guard: a build with live guidance on must not start measuring for
    // whoever the handset is about to decide the farmer is.
    final probe = _CountingLink(api: await reachableApi());

    final mode = await decideSurveyMode(
      api: await reachableApi(),
      consent: LiveConsent(store),
      farmerId: '',
      probe: probe,
    );

    expect(mode.live, isFalse);
    expect(probe.measurements, 0);
  });

  testWidgets('an offline survey shows no sign of the assistant', (
    tester,
  ) async {
    // What the walk renders when the decision went the other way. Not a disabled
    // button and not an explanation: the reason is interesting to us and useless to a
    // farmer who is about to complete a claim without any of it.
    //
    // Nothing at all is the assertion -- no icon anywhere, and not one word of text.
    await tester.pumpWidget(
      wrap(
        LiveTalk(
          store: store,
          voice: Voice(tts: _FakeTts()),
          api: await reachableApi(),
          farmerId: 'F-1',
          mode: const SurveyMode.offline(),
        ),
      ),
    );
    await tester.pump();

    expect(find.byIcon(talkIcon), findsNothing);
    expect(find.byType(Icon), findsNothing);
    expect(find.byType(Text), findsNothing);
  });

  testWidgets('and it measures nothing, because the measurement is already '
      'made', (tester) async {
    // The widget used to probe the link itself when the walk screen opened, so the
    // assistant could appear at one point of a walk and be gone at the next as the
    // signal moved -- and each appearance cost 192 KB of a farmer's connection to
    // decide. The survey's engine is now settled before the first clip, and this is
    // what holds it settled.
    final probe = _CountingLink(api: await reachableApi());

    await tester.pumpWidget(
      wrap(
        LiveTalk(
          store: store,
          voice: Voice(tts: _FakeTts()),
          api: await reachableApi(),
          farmerId: 'F-1',
          mode: const SurveyMode.offline(),
          probe: probe,
        ),
      ),
    );
    await tester.pump();

    expect(probe.measurements, 0);
  });

  // Everything above is what the compile-time flag lets a test see. With
  // liveGuidanceEnabled false the widget settles on "not offered" before its first
  // build, so the talk button never renders and the code behind it -- opening a
  // session, reporting what the assistant asked for -- is not reachable by tapping
  // anything. The session seam exists but only _open() ever reads it.
  //
  // So the case below drives the real path only in a build made with the flag on
  // (--dart-define=ESURVEY_LIVE_GUIDANCE=true, and on its own: the cases above and
  // link_quality_test pin the default, so a whole-suite run with the define on is
  // meant to fail them) and asserts the flag's own contract otherwise. Written rather
  // than left out because the rule it pins is the part of this widget whose output
  // ends up in a claim file, read by somebody years later.
  testWidgets('each request the assistant makes reaches the walk once, and '
      'carries the model that made it', (tester) async {
    final api = await reachableApi();
    final session = _StubSession(
      api: api,
      consent: LiveConsent(store),
      probe: _CountingLink(api: api),
    );
    // Named by the office in the grant, never compiled into the handset: an app
    // pinned to a model name would keep stamping it after the office moved on.
    session.model = 'gemini-live-under-test';

    final reported = <({String request, String? model})>[];
    var grabs = 0;

    await tester.pumpWidget(
      wrap(
        LiveTalk(
          store: store,
          voice: Voice(tts: _FakeTts()),
          api: api,
          farmerId: 'F-1',
          // The survey decided live. Constructed here rather than measured, which is
          // the whole point of the decision being made once and carried.
          mode: SurveyMode(engine: SurveyEngine.live, link: talkable),
          session: session,
          audio: _SilentAudio(),
          probe: _CountingLink(api: api),
          grabFrame: () async {
            grabs++;
            return null;
          },
          onAskedFor: (request, model) =>
              reported.add((request: request, model: model)),
        ),
      ),
    );
    await tester.pump();

    // This runs in every build, and it used to be skipped in all of them.
    //
    // The flag used to be checked inside this widget, so with liveGuidanceEnabled
    // off the button never rendered and the whole reporting path -- the thing an
    // officer eventually reads -- was unreachable by tapping. The flag now belongs to
    // the decision, which is made before the survey starts, and this widget trusts
    // the answer it is handed.
    //
    // There is nothing to tap any more either. A farmer who chose to be talked to
    // has already chosen, so the conversation opens with the walk rather than waiting
    // behind a button on the camera screen.
    await tester.pumpAndSettle();

    session.asksFor('शेंगा फोडून आतले दाणे दाखवा');
    await tester.pump();

    expect(reported, hasLength(1));
    expect(reported.single.request, 'शेंगा फोडून आतले दाणे दाखवा');
    expect(reported.single.model, 'gemini-live-under-test');
    expect(
      grabs,
      1,
      reason: 'the assistant has just said it wants to see something',
    );

    // A notification that brings no new request. Reporting the same one twice would
    // stamp it on a second clip, and an officer would read two directed observations
    // where the farmer was asked once.
    session.stirs();
    await tester.pump();
    expect(reported, hasLength(1));
    expect(grabs, 1);

    session.asksFor('जवळून दाखवा');
    await tester.pump();
    expect(reported.map((r) => r.request), [
      'शेंगा फोडून आतले दाणे दाखवा',
      'जवळून दाखवा',
    ]);
  });
}
