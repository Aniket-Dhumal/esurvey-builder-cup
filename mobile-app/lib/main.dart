import 'dart:async';

import 'package:connectivity_plus/connectivity_plus.dart';
import 'package:flutter/material.dart';

import 'auth/sign_in_screen.dart';
import 'core/accounts.dart';
import 'core/api.dart';
import 'core/config.dart';
import 'core/dates.dart';
import 'core/language.dart';
import 'core/background.dart';
import 'core/upload_service.dart';
import 'core/db.dart';
import 'core/farmer_data.dart';
import 'core/faults.dart';
import 'core/sensors.dart';
import 'core/session.dart';
import 'core/storage.dart';
import 'core/sync.dart';
import 'core/voice.dart';
import 'appeal/appeal_screen.dart';
import 'home/home_screen.dart';
import 'onboarding/access_screen.dart';
import 'onboarding/onboarding_screen.dart';
import 'report/farmer_id_screen.dart';
import 'report/farmer_picker_screen.dart';
import 'report/file_report_screen.dart';
import 'report/my_reports_screen.dart';
import 'report/new_report_tab.dart';
import 'help/help_screen.dart';
import 'ui/shell.dart';
import 'ui/theme.dart';

/// eSurvey, for farmers.
///
/// # What the app decides at startup, and why in this order
///
/// The store opens first, because everything else is worthless without it: it
/// holds the queue, and the queue is the only copy of a claim until it
/// reaches the office. Then the session, from the keystore, so the app knows
/// whether this is a returning farmer before it paints anything.
///
/// Nothing here waits for the network. A farmer opening the app in a field
/// should reach their own screen in the time it takes to read the splash, and
/// an app that blocks on a request has already failed the one place it is
/// used most.
void main() async {
  WidgetsFlutterBinding.ensureInitialized();

  // Before anything else, so a fault raised while the app is still starting is
  // caught rather than lost.
  //
  // The app had no error handler of any kind, and it has ninety-odd
  // fire-and-forget futures -- so a throw in any of them either killed a walk in
  // progress or vanished. Killing it is the worse outcome: a farmer three points
  // into a walk loses the walk, and the record of why is on a phone with no
  // signal. Faults keeps the app alive and writes the reason to the database,
  // where it survives a restart. See Faults.
  Faults.install();

  // A release build pointed at the emulator's host route can never reach the
  // platform, and every screen in this app is built to wait through exactly that
  // silence -- so it waits forever and looks like a village with no signal. Said
  // once, in English, on the first screen, because the only person who can ever
  // see it is whoever built the APK.
  if (Config.pointsNowhere) {
    runApp(const _MisconfiguredBuild());
    return;
  }

  runApp(const EsurveyApp());
}

/// The screen nobody in a field should ever see.
///
/// Deliberately not in the app's language and deliberately not styled like the
/// rest of it: it is a message to whoever ran the build command, and dressing it
/// up as a farmer-facing error would invite somebody to ship it.
class _MisconfiguredBuild extends StatelessWidget {
  const _MisconfiguredBuild();

  @override
  Widget build(BuildContext context) => MaterialApp(
    debugShowCheckedModeBanner: false,
    home: Scaffold(
      backgroundColor: const Color(0xFF7F1D1D),
      body: Padding(
        padding: const EdgeInsets.all(28),
        child: Center(
          child: Column(
            mainAxisSize: MainAxisSize.min,
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              const Text(
                'This build has no server address.',
                style: TextStyle(
                  color: Colors.white,
                  fontSize: 22,
                  fontWeight: FontWeight.w700,
                ),
              ),
              const SizedBox(height: 16),
              Text(
                'It is pointed at ${Config.apiBase}, which is the Android '
                'emulator route to a development machine. No handset can reach '
                'it, so every request would wait until it timed out and the app '
                'would look like a phone with no signal.\n\n'
                'Rebuild with the address:\n\n'
                'flutter build apk --release --split-per-abi\n'
                '  --dart-define=ESURVEY_API=https://…',
                style: const TextStyle(
                  color: Colors.white,
                  fontSize: 15,
                  height: 1.5,
                ),
              ),
            ],
          ),
        ),
      ),
    ),
  );
}

class EsurveyApp extends StatefulWidget {
  const EsurveyApp({super.key});

  @override
  State<EsurveyApp> createState() => _EsurveyAppState();
}

/// What the farmer chose when told a finished claim's evidence is still here.
enum _SignOutChoice { send, anyway, cancel }

class _EsurveyAppState extends State<EsurveyApp> {
  Store? _store;
  Session? _session;
  final _accounts = Accounts();

  /// Said on the sign-in screen when this phone holds unsent work: the
  /// returning farmer's own, or somebody else's that is still going.
  ({String mr, String en})? _signInNotice;
  Api? _api;
  Sync? _sync;
  FarmerData? _data;

  final _voice = Voice();
  final _positioning = Positioning();

  /// Whether a network is available, as it changes. Lazy, so the platform
  /// channel is only opened once the home screen needs it.
  late final Stream<bool> _online = Connectivity().onConnectivityChanged.map(
    (results) => results.any((r) => r != ConnectivityResult.none),
  );

  bool _onboarded = false;

  /// Whether the farmer has been shown what the app needs access to.
  ///
  /// Shown once, straight after onboarding and before the number is asked
  /// for. It is skippable there: a farmer who declines can still sign in and
  /// read their claims, and the walk has its own gate that cannot be skipped
  /// because a walk without a camera is not a thing to be let into.
  bool _accessAsked = false;

  /// Which of the account's farmers the app is working as, or null when the
  /// picker is what should be showing.
  ///
  /// Held on the device rather than in memory so that closing the app and
  /// reopening it returns to the picker with the last choice at the top,
  /// rather than to whoever happened to be first in the list.
  String? _activeFarmer;
  bool _ready = false;
  String? _fatal;

  @override
  void initState() {
    super.initState();
    unawaited(_boot());
  }

  @override
  void dispose() {
    unawaited(_sync?.dispose());
    unawaited(_positioning.dispose());
    _api?.close();
    unawaited(_store?.close());
    super.dispose();
  }

  Future<void> _boot() async {
    try {
      final store = await Store.open();
      // The fault log has somewhere to write from here on. Anything that failed
      // before this line reached the console only, which is the correct trade:
      // holding startup for a database in order to record errors that have not
      // happened yet would slow every launch for every farmer.
      Faults.attach(store);
      final session = Session();
      await session.load();

      // Every screen reads the signed-in farmer's rows and nobody else's. A
      // lapsed session keeps its owner, so the farmer's own work is what the
      // sign-in screen talks about.
      store.owner = session.ownerId;
      if (session.signedIn && session.ownerId != null) {
        await store.adoptUnowned(session.ownerId!);
        await store.setDeviceValue(_activeOwnerKey, session.ownerId);
      }

      // The number stays signed in across a close and a reboot; which farmer
      // it was acting for is remembered too, but only so the picker can be
      // answered in one tap. A handset that is opened shows the list.
      final lastFarmer = await store.deviceValue(_activeFarmerKey);
      if (lastFarmer != null &&
          await store.linkedFarmer(lastFarmer, owner: session.ownerId) !=
              null) {
        _activeFarmer = lastFarmer;
        store.farmer = lastFarmer;
      }

      final api = Api(
        session: session,
        // The refresh token was refused. Not a sign-out: nothing is deleted,
        // and the farmer is asked to sign in again to send what is waiting.
        onSignedOut: () => unawaited(_handleLapsed()),
      );

      final sync = Sync(
        store: store,
        api: api,
        accounts: _accounts,
        onPurged: () => Photos.sweepOrphans(store),
        onStillPending: (after) => _stillPending(store, after),
      );
      final data = FarmerData(store: store, api: api);

      // Speech is set up in the background: a handset with no engine takes a
      // moment to say so, and blocking the first screen on it would delay
      // every farmer for the sake of the few whose phone cannot speak.
      //
      // The saved on/off preference is restored in the same pass. A farmer
      // who turned the voice off last week should not be spoken at again on
      // the next launch -- a preference that resets is not a preference.
      unawaited(
        _voice.init().then(
          (_) => _voice.restore(
            read: () => store.get<bool>('voice_enabled'),
            write: (on) => store.put('voice_enabled', on),
          ),
        ),
      );

      // The background drain, registered whether or not anything is queued
      // right now. It is the schedule that catches the farmer who files in a
      // field and never opens the app again -- so it has to be standing
      // before that happens, not arranged afterwards.
      //
      // Failing to register it is not fatal: the foreground timer still
      // drains the queue while the app is open, which is strictly worse and
      // still works. A farmer should not be stopped at the splash screen
      // because a scheduling API said no.
      unawaited(_startBackgroundSync());

      // Photograph files that no row points at, from a write that failed
      // between the file and the row. They can never be uploaded, because the
      // queue is made of rows, and they can never be deleted, because
      // deletion happens when a row is sent -- so they accumulate on the
      // phone least able to afford it. Swept at startup, when nothing is
      // being captured and the answer cannot race a shutter press.
      unawaited(Photos.sweepOrphans(store));

      final seen = await store.get<bool>('onboarding_seen') ?? false;
      final asked = await store.get<bool>('access_asked') ?? false;
      final notice = session.signedIn ? null : await _noticeFor(store, session);

      if (!mounted) return;
      setState(() {
        _store = store;
        _session = session;
        _api = api;
        _sync = sync;
        _data = data;
        _onboarded = seen;
        _accessAsked = asked;
        _signInNotice = notice;
        _ready = true;
      });
    } catch (e) {
      // Storage that will not open is the one failure this app cannot work
      // around: without it a photograph has nowhere to live. Said plainly
      // rather than crashing to a grey screen.
      if (!mounted) return;
      // The exception names a path inside the app's private storage and an
      // errno. A farmer can act on "free some storage", which the screen
      // already says; they can do nothing with the rest, so it goes to the log.
      debugPrint('[main.storage] $e');
      setState(() {
        _fatal = 'storage';
        _ready = true;
      });
    }
  }

  /// A pass left work behind. Hand it to the operating system, and where the
  /// work is evidence that could go right now, keep sending it.
  ///
  /// # Why a foreground service at all
  ///
  /// A claim is about 18 MB of video. Sending it off the app's own timer stops
  /// the moment the screen sleeps, and WorkManager behind it wakes roughly every
  /// fifteen minutes -- so a farmer who pocketed the phone had stopped the
  /// upload without being told. That cost most of an hour on the trial.
  ///
  /// The service runs exactly one pass and stops itself, so the notification
  /// cannot outlive the work: see [UploadService]. Two passes cannot overlap
  /// either -- every pass takes the same device-wide lease out of SQLite.
  ///
  /// # The case this gets slightly wrong, on purpose
  ///
  /// A zero wait also means "offline, waiting for a network", and starting a
  /// service then shows a notification over a pass that will immediately find no
  /// signal. It is self-limiting: that pass ends and the service stops itself,
  /// so the cost is a notification that appears and goes. Distinguishing the two
  /// would mean threading the stop reason through a callback that exists to say
  /// "wake me later", and a brief notification is the cheaper mistake.
  Future<void> _stillPending(Store store, Duration after) async {
    await BackgroundSync.wakeAfter(after);

    if (after > Duration.zero) {
      // Backing off -- a failing office, or a rate limit. WorkManager is the
      // right thing to wait with; a wake lock is not.
      return;
    }

    final waiting = await store.outstanding(
      owner: Store.everyone,
      only: Store.everyone,
    );
    if (waiting.captures <= 0) return;

    await UploadService.start(items: waiting.captures);
  }

  Future<void> _startBackgroundSync() async {
    try {
      await BackgroundSync.initialise();
      await BackgroundSync.schedule();
    } catch (e) {
      debugPrint('background sync not scheduled: $e');
    }
  }

  /// The device key recording whose work the screens last showed.
  static const _activeOwnerKey = 'active_owner';

  /// The farmer the account last worked as. Not a credential: the grant that
  /// lets this handset act for them lives in the farmers table, and this is
  /// only which of them was picked.
  static const _activeFarmerKey = 'active_farmer';

  /// Work as this farmer: scope every screen to them, and remember it.
  Future<void> _selectFarmer(String farmerId) async {
    final store = _store;
    if (store == null) return;
    store.farmer = farmerId;
    await store.touchFarmer(farmerId);
    await store.setDeviceValue(_activeFarmerKey, farmerId);
    if (mounted) setState(() => _activeFarmer = farmerId);
  }

  /// Put the picker back up, without signing anybody out.
  Future<void> _clearFarmer() async {
    final store = _store;
    store?.farmer = null;
    await store?.setDeviceValue(_activeFarmerKey, null);
    if (mounted) setState(() => _activeFarmer = null);
  }

  /// What the sign-in screen should say about work waiting on this phone.
  Future<({String mr, String en})?> _noticeFor(
    Store store,
    Session session,
  ) async {
    final mine = session.ownerId;
    if (mine != null &&
        (await store.outstanding(owner: mine, only: Store.everyone)).any) {
      final name = session.name;
      return (
        mr:
            '${name == null ? '' : '$name, '}तुमची नोंद अजून पाठवायची बाकी '
            'आहे. ती फोनवर सुरक्षित आहे. पाठवण्यासाठी पुन्हा साइन इन करा.',
        en:
            '${name == null ? '' : '$name, '}your claim has not been sent yet. '
            'It is safe on this phone. Sign in again to send it.',
      );
    }
    // Work left by an earlier sign-in, and whether this phone can still send it.
    //
    // The old message said "this phone is still sending another farmer's claim"
    // whenever any other account had rows. That holds only while that account's
    // parked credentials are usable. On the trial the farmer's session lapsed
    // mid-upload and they signed in again on a different number: the parked
    // token was dead, the sync engine forgot it, and two and a half recordings
    // sat on the phone invisible for an hour while the app reported nothing
    // wrong and this screen said they were on their way.
    //
    // Stranding a farmer's evidence is bad. Telling them it is being sent while
    // it is stranded is worse, because it removes the one action that recovers
    // it: signing back in on the number that recorded it.
    for (final who in await store.ownersOtherThan(mine)) {
      final waiting = await store.outstanding(owner: who, only: Store.everyone);
      if (!waiting.any) continue;

      final parked = await _accounts.session(who);
      if (parked.signedIn) {
        return (
          mr:
              'या फोनवर आधीच्या साइन-इनमधील ${waiting.total} नोंद पाठवायची '
              'बाकी आहे. ती आपोआप पाठवली जाईल; तुम्हाला ती दिसणार नाही.',
          en:
              'This phone still has ${waiting.total} item(s) to send from an '
              'earlier sign-in. They go on their own and you will not see them.',
        );
      }

      // Stranded. Name what is waiting and the one thing that recovers it.
      final tail = parked.mobileTail;
      final whose = tail == null ? '' : ' (•••• $tail)';
      return (
        mr:
            'या फोनवर आधीच्या साइन-इनमध्ये घेतलेली ${waiting.total} नोंद '
            'पाठवायची बाकी आहे$whose. ती फोनवर सुरक्षित आहे, पण त्या '
            'साइन-इनची मुदत संपली आहे — ती पाठवण्यासाठी त्याच नंबरने पुन्हा '
            'साइन इन करा.',
        en:
            'This phone holds ${waiting.total} item(s) recorded during an '
            'earlier sign-in$whose. They are safe on the phone, but that '
            'sign-in has expired, so they cannot be sent until you sign in '
            'again with that same number.',
      );
    }
    return null;
  }

  /// Offer to send, before offering to leave.
  ///
  /// Sending is the default and the emphasised action. Signing out is available
  /// and is described by what it costs rather than reassured away: the previous
  /// wording told farmers their work "keeps sending from this phone after you
  /// sign out", which is true only while the parked credentials survive -- and on
  /// the trial they did not.
  Future<_SignOutChoice> _askAboutWaitingEvidence(int filed) async {
    final ctx = _navigator.currentContext;
    if (ctx == null) {
      // No UI to ask with. Refuse rather than sign out silently: the caller is
      // a button that cannot have been pressed without a screen.
      return _SignOutChoice.cancel;
    }

    final choice = await showDialog<_SignOutChoice>(
      context: ctx,
      // Tapping outside a dialog about losing a claim should not be the way
      // past it.
      barrierDismissible: false,
      builder: (c) => AlertDialog(
        title: Text(tr('अगोदर नोंदी पाठवा', 'Send your recordings first')),
        content: Text(
          tr(
            'तुमच्या पूर्ण झालेल्या दाव्यातील $filed नोंदी अजून कार्यालयात '
                'पोहोचलेल्या नाहीत. त्या या फोनवर सुरक्षित आहेत, पण तुम्ही '
                'याच नंबरने साइन इन असतानाच पाठवल्या जातात. बाहेर पडलात तर '
                'ती याच फोनवर थांबतील आणि पुन्हा याच नंबरने साइन इन '
                'केल्याशिवाय कार्यालयात पोहोचणार नाहीत.',
            '$filed recording(s) from your completed claim have not reached '
                'the office. They are safe on this phone, but they are only '
                'sent while you are signed in with this number. If you sign '
                'out they wait here, and will not reach the office until you '
                'sign in again with this same number.',
          ),
        ),
        actions: [
          TextButton(
            onPressed: () => Navigator.of(c).pop(_SignOutChoice.anyway),
            child: Text(tr('तरीही बाहेर पडा', 'Sign out anyway')),
          ),
          FilledButton(
            onPressed: () => Navigator.of(c).pop(_SignOutChoice.send),
            child: Text(tr('थांबा आणि पाठवा', 'Wait and send')),
          ),
        ],
      ),
    );
    return choice ?? _SignOutChoice.cancel;
  }

  /// The farmer has seen what the app needs, whether or not they granted it.
  ///
  /// Recorded either way. A farmer who declined and was asked again on every
  /// launch would learn to tap past it, which is worse than asking once and
  /// letting the walk's own gate handle the rest.
  Future<void> _accessDone(Store store) async {
    await store.put('access_asked', true);
    if (!mounted) return;
    setState(() => _accessAsked = true);
  }

  /// A farmer has signed in -- the same one, or somebody else.
  Future<void> _afterSignIn() async {
    final store = _store;
    final session = _session;
    if (store == null || session == null) return;
    final owner = session.ownerId;

    final previous = await store.deviceValue(_activeOwnerKey);
    if (previous != owner) {
      // A different farmer. What the app cached about the last one -- their
      // land, their reports -- goes. Their queued work does not: it is hidden
      // from this farmer and keeps sending under its owner's name.
      await store.clearCachedPersonal();
    }
    store.owner = owner;
    // A number has been proved; which farmer it is acting for has not. The
    // picker asks, and until it is answered no screen is scoped to anybody.
    store.farmer = null;
    await store.setDeviceValue(_activeFarmerKey, null);
    if (mounted) setState(() => _activeFarmer = null);
    if (owner != null) {
      await store.adoptUnowned(owner);
      await store.setDeviceValue(_activeOwnerKey, owner);
      // Signed in again: the fresh session supersedes anything parked.
      await _accounts.forget(owner);
    }
    unawaited(_startBackgroundSync());
    if (mounted) setState(() => _signInNotice = null);
    unawaited(_sync?.run());
  }

  /// The server refused the refresh token.
  ///
  /// Everything stays: the queue, the claims in progress, who the farmer is.
  /// The app shows sign-in, which says their claim is waiting for them.
  Future<void> _handleLapsed() async {
    _sync?.stop();
    final store = _store;
    final session = _session;
    ({String mr, String en})? notice;
    if (store != null && session != null) {
      notice = await _noticeFor(store, session);
    }
    if (mounted) setState(() => _signInNotice = notice);
  }

  /// The farmer chose to sign out.
  ///
  /// Their unsent work is not deleted. Their credentials are parked so the
  /// queue can keep sending it, hidden from whoever signs in next, and it is
  /// removed from the phone once the office has all of it. With nothing
  /// waiting, their rows and files go now.
  Future<void> _signOut({bool force = false}) async {
    final store = _store;
    final session = _session;
    if (store == null || session == null) return;

    final owner = session.ownerId;

    // A finished claim whose recordings the office has not got holds the
    // sign-out.
    //
    // # Why here and not in the dialog
    //
    // Because this is not the only way out. The farmer picker has a logout icon
    // that calls straight through with no dialog, so a check in the home
    // screen's confirmation would have left that path open -- and an
    // unguarded second path is exactly the shape of the bug that stranded the
    // trial's recordings.
    //
    // # Why a finished claim and not any waiting work
    //
    // A draft still being gathered is work the farmer may be abandoning, and
    // holding a borrowed handset hostage over it would be wrong. A filed claim
    // is different: the farmer has done everything asked of them, the office has
    // the form, and only the evidence is missing. Losing that loses the claim.
    //
    // # Why it can still be overridden
    //
    // A refusal with no way past it would make a phone in a village with no
    // signal unusable by anybody until signal returned, which can be days. The
    // farmer is told plainly what signing out costs and has to choose it.
    if (!force && owner != null) {
      final filed = await store.unsentFiledEvidence(owner: owner);
      if (filed > 0) {
        final choice = await _askAboutWaitingEvidence(filed);
        if (choice != _SignOutChoice.anyway) {
          if (choice == _SignOutChoice.send) {
            // Into a pass now, with the foreground service behind it so it
            // survives the screen going dark.
            unawaited(_sync?.sendNow() ?? Future<void>.value());
          }
          return;
        }
      }
    }

    _sync?.stop();
    final waiting =
        owner != null &&
        (await store.outstanding(owner: owner, only: Store.everyone)).any;
    if (waiting) await _accounts.park(session);
    await session.end();

    store.owner = null;
    store.farmer = null;
    await store.setDeviceValue(_activeOwnerKey, null);
    await store.setDeviceValue(_activeFarmerKey, null);
    if (mounted) setState(() => _activeFarmer = null);
    await store.clearCachedPersonal();
    if (owner != null && !waiting) {
      await store.purgeOwnerIfDone(owner);
      // With nothing left to send there is nothing left to diagnose. A fault log
      // is about this app on this phone rather than about a person, but keeping
      // one past the session that produced it serves nobody -- and the next
      // farmer on a borrowed handset should not inherit it.
      await store.clearFaults();
    }
    // Every file whose row went is an orphan now. Removed straight away: the
    // next person may pick the phone up before the next startup.
    await Photos.sweepOrphans(store);

    final anyoneWaiting = (await store.outstanding(
      owner: Store.everyone,
      only: Store.everyone,
    )).any;
    if (!anyoneWaiting) {
      try {
        await BackgroundSync.cancel();
      } catch (e) {
        debugPrint('background sync not cancelled: $e');
      }
    }

    final notice = await _noticeFor(store, session);
    if (mounted) setState(() => _signInNotice = notice);
  }

  @override
  Widget build(BuildContext context) {
    return MaterialApp(
      title: 'eSurvey',
      navigatorKey: _navigator,
      theme: esurveyTheme(),
      debugShowCheckedModeBanner: false,
      home: _home(),
    );
  }

  Widget _home() {
    if (!_ready) return const _Splash();
    if (_fatal != null) return const _StorageFailure();

    final store = _store!;
    final session = _session!;

    if (!_onboarded) {
      return OnboardingScreen(
        voice: _voice,
        store: store,
        onDone: () => setState(() => _onboarded = true),
      );
    }

    // What the app needs access to, before it asks who the farmer is.
    //
    // # Why here
    //
    // It used to be only in front of the walk, which is where a refusal actually
    // costs something -- and that is still the gate that cannot be skipped. But
    // it meant the first thing a farmer saw was a box asking for their mobile
    // number, and the permissions arrived several screens later, in the middle of
    // filing a claim.
    //
    // The usual advice against asking at launch is about a *cold* prompt: a
    // system dialog appearing before the farmer has been told anything is one
    // they cannot judge and will refuse. That is answered by explaining first,
    // which is what this screen does -- so the advice does not apply and the
    // objection to asking early goes with it.
    //
    // # Why it can be skipped here and not at the walk
    //
    // A farmer who declines can still sign in, read their claims and see what
    // the office decided. None of that needs a camera. What needs a camera is
    // recording a walk, and that has its own gate.
    if (!_accessAsked) {
      return AccessScreen(
        voice: _voice,
        onReady: () => unawaited(_accessDone(store)),
        onSkip: () => unawaited(_accessDone(store)),
      );
    }

    if (!session.signedIn) {
      return SignInScreen(
        api: _api!,
        session: session,
        voice: _voice,
        notice: _signInNotice,
        onSignedIn: () => unawaited(_afterSignIn()),
      );
    }

    // Signed in, but not yet working as anybody. A mobile number may act for
    // several farmers -- a household shares a phone, and a farmer whose own
    // AgriStack number is long gone borrows one -- so the number proves the
    // session and this asks whose claim is being worked on.
    if (_activeFarmer == null) {
      return FarmerPickerScreen(
        store: store,
        voice: _voice,
        onPicked: (id) => unawaited(_workAs(id)),
        onAddFarmer: _addFarmer,
        onSignOut: () => unawaited(_signOut()),
      );
    }

    // Four tabs, each holding its own state. Home answers "what needs doing
    // now"; the rest are the places a farmer goes looking for something.
    return Shell(
      voice: _voice,
      home: HomeScreen(
        store: store,
        data: _data!,
        api: _api!,
        session: session,
        sync: _sync!,
        voice: _voice,
        positioning: _positioning,
        onSignOut: () => unawaited(_signOut()),
        onSwitchFarmer: () => unawaited(_clearFarmer()),
        online: _online,
      ),
      newReport: NewReportTab(
        data: _data!,
        store: store,
        onStart: _startReport,
        onSwitchFarmer: () => unawaited(_clearFarmer()),
      ),
      myReports: MyReportsScreen(
        data: _data!,
        voice: _voice,
        onAppeal: _appealFromTab,
      ),
      help: HelpScreen(voice: _voice, store: _store),
    );
  }

  /// Work as one of the account's farmers, from the picker.
  Future<void> _workAs(String farmerId) async {
    await _selectFarmer(farmerId);
    // Their own land and their own claims, which may be a different village
    // from the farmer worked as a moment ago.
    unawaited(_sync?.run());
  }

  /// Add a farmer to this handset, from the picker.
  ///
  /// Returns when the screen closes, whether or not one was added; the picker
  /// re-reads its list either way.
  Future<void> _addFarmer() async {
    final store = _store;
    final data = _data;
    if (store == null || data == null) return;

    await _navigator.currentState?.push<void>(
      MaterialPageRoute<void>(
        builder: (ctx) => Scaffold(
          appBar: AppBar(title: Text(tr('शेतकरी जोडा', 'Add a farmer'))),
          body: FarmerIdScreen(
            data: data,
            store: store,
            voice: _voice,
            onLoaded: (record) {
              // Added and picked in one move: somebody who has just typed an
              // ID and a date of birth is not looking for a list.
              final id = '${record['farmer_id'] ?? ''}';
              Navigator.of(ctx).pop();
              if (id.isNotEmpty) unawaited(_workAs(id));
            },
          ),
        ),
      ),
    );
  }

  /// The registry record is in hand, so the report can be filled from it.
  ///
  /// The record supplies the land: survey number, extent, crop, sowing date,
  /// and the boundary the geofence and the sampling stations are built on. The
  /// event has to come from somewhere else -- a report is a claim against a
  /// declared calamity, and which one is open is a fact about the village
  /// rather than about the farmer.
  Future<void> _startReport(Map<String, Object?> record) async {
    final data = _data;
    final store = _store;
    if (data == null || store == null) return;

    // This farmer's district, not the state's. The office sends every open
    // event to a farmer -- they hold no jurisdiction to be narrowed by -- and
    // filing against another district's event is gathered in full, walked,
    // photographed, and only then refused for a village it never covered.
    final farmerId = '${record['farmer_id'] ?? ''}';
    final theirRecord = farmerId.isEmpty
        ? null
        : (await data.record(farmerId, refresh: false)).value;
    final events = eventsForFarmer(
      (await data.events(
            villages: FarmerData.villagesOf(
              theirRecord ?? record.cast<String, dynamic>(),
            ),
          )).value ??
          const [],
      theirRecord ?? record.cast<String, dynamic>(),
    );
    if (!mounted) return;

    if (events.isEmpty) {
      // Nothing to claim against. Said plainly rather than opening an empty
      // form the farmer fills in and cannot send: an app that takes a report
      // nobody can act on is worse than one that says so at the start.
      _navigator.currentState?.push(
        MaterialPageRoute<void>(builder: (_) => const _NoOpenEvent()),
      );
      return;
    }

    // More than one calamity may be open over the same village -- a hailstorm
    // and unseasonal rain a fortnight apart. Taking the first silently filed
    // the claim against whichever the office happened to list first, and a
    // report is a claim against one declared calamity: the wrong one is the
    // wrong claim, adjudicated against the wrong window.
    var event = events.first as Map<String, dynamic>;
    if (events.length > 1) {
      final chosen = await _askWhichEvent(events);
      if (chosen == null || !mounted) return;
      event = chosen;
    }

    await _navigator.currentState?.push(
      MaterialPageRoute<void>(
        builder: (_) => FileReportScreen(
          store: store,
          voice: _voice,
          positioning: _positioning,
          sync: _sync!,
          api: _api!,
          farmerId: farmerId,
          event: event,
          parcels: (record['parcels'] as List<dynamic>?) ?? const [],
        ),
      ),
    );
  }

  /// Which calamity this claim is against, when more than one is open.
  Future<Map<String, dynamic>?> _askWhichEvent(List<dynamic> events) async {
    final ctx = _navigator.currentContext;
    if (ctx == null) return null;

    return showModalBottomSheet<Map<String, dynamic>>(
      context: ctx,
      builder: (sheet) => SafeArea(
        child: ListView(
          shrinkWrap: true,
          padding: const EdgeInsets.fromLTRB(0, 0, 0, 16),
          children: [
            Padding(
              padding: const EdgeInsets.fromLTRB(20, 0, 20, 8),
              child: Text(
                tr('कोणत्या आपत्तीसाठी नोंद?', 'Which calamity is this for?'),
                style: Theme.of(sheet).textTheme.titleLarge,
              ),
            ),
            for (final e in events)
              ListTile(
                leading: const IconBadge(
                  Icons.thunderstorm_outlined,
                  color: Palette.severeFg,
                  background: Palette.severeBg,
                ),
                title: Text('${(e as Map)['title'] ?? e['calamity_type']}'),
                subtitle: Text(
                  '${formatDay(e['incident_start'])} – '
                  '${formatDay(e['incident_end'])}',
                ),
                trailing: const Icon(Icons.chevron_right),
                onTap: () => Navigator.of(sheet).pop(e.cast<String, dynamic>()),
              ),
          ],
        ),
      ),
    );
  }

  Future<void> _appealFromTab(Map<String, dynamic> report) async {
    final store = _store;
    if (store == null || !mounted) return;
    await _navigator.currentState?.push<bool>(
      MaterialPageRoute<bool>(
        builder: (_) => AppealScreen(
          store: store,
          voice: _voice,
          sync: _sync!,
          report: report,
        ),
      ),
    );
  }

  final _navigator = GlobalKey<NavigatorState>();
}

class _Splash extends StatelessWidget {
  const _Splash();

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      body: Center(
        child: Column(
          mainAxisAlignment: MainAxisAlignment.center,
          children: [
            Image.asset(
              'assets/brand/logo-esurvey.png',
              height: 56,
              errorBuilder: (_, _, _) => Text(
                'eSurvey',
                style: Theme.of(context).textTheme.headlineMedium?.copyWith(
                  color: Palette.primaryDark,
                ),
              ),
            ),
            const SizedBox(height: 28),
            const SizedBox(
              width: 24,
              height: 24,
              child: CircularProgressIndicator(strokeWidth: 2.5),
            ),
          ],
        ),
      ),
    );
  }
}

/// The one failure the app cannot work around.
///
/// Without storage there is nowhere for a photograph to live, so carrying on
/// would mean accepting a claim and losing it. Better to say so on the first
/// screen than to fail at the moment somebody is standing in a field.
class _StorageFailure extends StatelessWidget {
  const _StorageFailure();

  @override
  Widget build(BuildContext context) {
    return _Message(
      icon: Icons.sd_card_alert_outlined,
      tone: Tone.danger,
      title: tr(
        'या फोनवर माहिती साठवता येत नाही',
        'This phone cannot store anything',
      ),
      body: tr(
        'त्यामुळे नोंद सुरक्षित ठेवता येणार नाही. फोन पुन्हा सुरू करा किंवा '
            'थोडी जागा मोकळी करा.',
        'So a report cannot be kept safely. Restart the phone, or free some '
            'storage.',
      ),
    );
  }
}

/// Shown when a farmer has their land in hand and there is nothing to claim
/// against.
///
/// A screen of its own rather than a snackbar: this is the end of the road for
/// today, and somebody who walked to a field to file needs to understand why
/// they cannot, not watch a message disappear after four seconds.
class _NoOpenEvent extends StatelessWidget {
  const _NoOpenEvent();

  @override
  Widget build(BuildContext context) {
    return _Message(
      appBarTitle: tr('नवीन नोंद', 'New report'),
      icon: Icons.event_available_outlined,
      tone: Tone.info,
      title: tr(
        'सध्या कोणतीही आपत्ती जाहीर नाही',
        'No calamity is declared right now',
      ),
      body: tr(
        'तुमच्या गावासाठी आपत्ती जाहीर झाल्यावर ती मुख्यपृष्ठावर दिसेल आणि '
            'तिथून नोंद करता येईल.',
        'When one is declared for your village it appears on the home screen, '
            'and you can report from there.',
      ),
    );
  }
}

/// A whole screen that says one thing.
class _Message extends StatelessWidget {
  const _Message({
    required this.icon,
    required this.tone,
    required this.title,
    required this.body,
    this.appBarTitle,
  });

  final IconData icon;
  final Tone tone;
  final String title;
  final String body;
  final String? appBarTitle;

  @override
  Widget build(BuildContext context) {
    final text = Theme.of(context).textTheme;
    return Scaffold(
      appBar: appBarTitle == null ? null : AppBar(title: Text(appBarTitle!)),
      body: SafeArea(
        child: Center(
          child: Padding(
            padding: const EdgeInsets.all(32),
            child: Column(
              mainAxisSize: MainAxisSize.min,
              children: [
                Container(
                  width: 88,
                  height: 88,
                  decoration: BoxDecoration(
                    color: tone.background,
                    shape: BoxShape.circle,
                  ),
                  child: Icon(icon, size: 40, color: tone.foreground),
                ),
                const SizedBox(height: 24),
                Text(
                  title,
                  textAlign: TextAlign.center,
                  style: text.titleLarge,
                ),
                const SizedBox(height: 8),
                Text(
                  body,
                  textAlign: TextAlign.center,
                  style: text.bodyMedium?.copyWith(color: Palette.inkMuted),
                ),
              ],
            ),
          ),
        ),
      ),
    );
  }
}
