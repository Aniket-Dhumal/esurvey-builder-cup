import 'dart:io';

import 'package:esurvey/core/db.dart';
import 'package:esurvey/core/live_consent.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:sqflite_common_ffi/sqflite_ffi.dart';

/// Agreeing to be recorded, which is not the same as agreeing to be filmed.
///
/// The camera permission lets the app make a recording the office receives, and a
/// farmer who grants it understands that. Live guidance sends their voice and pictures
/// of their field to a company outside the platform, continuously, while they talk.
/// Nobody would read the first as covering the second, and Android will not ask on our
/// behalf -- the microphone permission is never even requested, because the walk
/// records with audio off.

late Directory _tmp;

Future<Store> freshStore() async {
  final dir = Directory('${_tmp.path}/${DateTime.now().microsecondsSinceEpoch}')
    ..createSync(recursive: true);
  return Store.open(directory: dir.path);
}

void main() {
  setUpAll(() {
    sqfliteFfiInit();
    databaseFactory = databaseFactoryFfi;
    _tmp = Directory.systemTemp.createTempSync('esurvey-consent-test');
  });

  tearDownAll(() {
    try {
      _tmp.deleteSync(recursive: true);
    } catch (_) {
      // Windows holds handles briefly after close.
    }
  });

  test('nobody has agreed until they say so', () async {
    // Absence is not consent. A farmer who has never been asked, and one whose
    // answer was lost, are both treated as not having agreed: the assistant is
    // simply not offered, which is the same as a farm with no signal.
    final store = await freshStore();
    final consent = LiveConsent(store);

    expect(await consent.granted('F-1'), isFalse);
    expect(await consent.asked('F-1'), isFalse);

    await store.close();
  });

  test('an agreement is remembered', () async {
    final store = await freshStore();
    final consent = LiveConsent(store);

    await consent.record(farmerId: 'F-1', granted: true);

    expect(await consent.granted('F-1'), isTrue);
    expect(await consent.asked('F-1'), isTrue);

    await store.close();
  });

  test('a refusal is remembered too, so it is not asked again', () async {
    // Without storing the no, the app cannot tell "has not been asked" from "said
    // no", and would ask on every screen open -- which is how a farmer learns to tap
    // whatever makes a dialog go away.
    final store = await freshStore();
    final consent = LiveConsent(store);

    await consent.record(farmerId: 'F-1', granted: false);

    expect(await consent.granted('F-1'), isFalse);
    expect(await consent.asked('F-1'), isTrue, reason: 'do not ask again');

    await store.close();
  });

  test('one farmer agreeing says nothing about another', () async {
    // A handset in a village is shared. A household's phone acts for several
    // farmers, and being recorded is a decision each of them makes for themselves.
    final store = await freshStore();
    final consent = LiveConsent(store);

    await consent.record(farmerId: 'F-1', granted: true);

    expect(await consent.granted('F-1'), isTrue);
    expect(await consent.granted('F-2'), isFalse);
    expect(await consent.asked('F-2'), isFalse);

    await store.close();
  });

  test('it can be withdrawn', () async {
    // An agreement that cannot be taken back is not much of an agreement.
    final store = await freshStore();
    final consent = LiveConsent(store);

    await consent.record(farmerId: 'F-1', granted: true);
    await consent.withdraw('F-1');

    expect(await consent.granted('F-1'), isFalse);

    await store.close();
  });

  test('an empty farmer id is never treated as consenting', () async {
    // A screen reached before the farmer is known would otherwise write an
    // agreement against nobody, which the next farmer would inherit.
    final store = await freshStore();
    final consent = LiveConsent(store);

    await consent.record(farmerId: '', granted: true);

    expect(await consent.granted(''), isFalse);

    await store.close();
  });

  group('what the farmer is told', () {
    test('lists what is sent rather than what is withheld', () {
      // A notice that lists absences invites the reader to assume everything else
      // stays, and it stops being true the moment somebody adds a field.
      for (final text in [liveConsentTerms.mr, liveConsentTerms.en]) {
        expect(text, contains('Google'));
      }
      expect(liveConsentTerms.en, contains('your voice'));
      expect(liveConsentTerms.en, contains('photographs of your field'));
      expect(liveConsentTerms.mr, contains('तुमचा आवाज'));
    });

    test('names the four context fields the session actually carries', () {
      // Checkable against liveURL in the Go service, which builds the address from
      // exactly these. If that list grows, this test is what makes the notice
      // visibly wrong rather than quietly so.
      expect(liveConsentTerms.en, contains('which crop'));
      expect(liveConsentTerms.en, contains('which calamity'));
      expect(liveConsentTerms.en, contains('which season'));
      expect(liveConsentTerms.en, contains('which district'));
    });

    test('says the identifiers are not sent', () {
      expect(liveConsentTerms.en, contains('Aadhaar'));
      expect(liveConsentTerms.en, contains('farmer id'));
      expect(liveConsentTerms.mr, contains('आधार'));
    });

    test('says the claim works without it', () {
      // The sentence that makes this a real choice rather than a gate with one door.
      expect(liveConsentTerms.en, contains('completed without this'));
      expect(liveConsentTerms.mr, contains('नोंद पूर्ण होते'));
    });

    test('says it can be withdrawn', () {
      expect(liveConsentTerms.en, contains('withdraw'));
      expect(liveConsentTerms.mr, contains('काढून'));
    });
  });

  test('consent given under older wording is asked again', () async {
    // Consent is to what was actually said. Holding somebody to an agreement about
    // something they were not told is not consent, so a material change to the
    // wording puts the question again.
    final store = await freshStore();
    final consent = LiveConsent(store);

    await store.put('live_consent:F-1', {
      'granted': true,
      'at': DateTime.now().toUtc().toIso8601String(),
      'terms': termsVersion - 1,
    });

    expect(
      await consent.asked('F-1'),
      isFalse,
      reason: 'older wording means the question has not been put',
    );

    await store.close();
  });
}
