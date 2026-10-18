import 'dart:io';

import 'package:esurvey/core/db.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:sqflite_common_ffi/sqflite_ffi.dart';

/// One phone, one mobile number, several farmers.
///
/// A household shares a handset. One number may be the one AgriStack holds for
/// several people, and a farmer whose own registered number is long gone
/// borrows a relative's. The app held exactly one farmer per signed-in account
/// until now, so the second person had nowhere to be.
///
/// Two things have to hold. Each farmer's work is theirs alone on the screen --
/// a phone passed across a table must not show one person's claims to another.
/// And the queue is not scoped that way at all: work is sent under the account
/// that queued it, whoever is on screen, because a claim that stops sending
/// when somebody switches farmer is a claim that never arrives.

late Directory _tmp;

Future<Store> freshStore() async {
  final dir = Directory('${_tmp.path}/${DateTime.now().microsecondsSinceEpoch}')
    ..createSync(recursive: true);
  return Store.open(directory: dir.path);
}

Map<String, Object?> draftFor(
  String localId,
  String farmerId, {
  String owner = 'acct-1',
}) => {
  'local_id': localId,
  'owner_id': owner,
  'survey_event_id': 'E1',
  'farmer_id': farmerId,
  'season': 'kharif',
  'crop_year': '2026-27',
  'loss_category': 'standing_crop',
  'calamity_type': 'hailstorm',
  'occurred_at': '2026-08-01T10:00:00Z',
  'farm_id': '453',
  'survey_number': '453',
  'crop_name': 'Soybean',
  'area_sown_ha': 1.6,
  'area_affected_ha': 1.2,
  'damage_percent': 70.0,
  'declaration_accepted': 1,
  'signature_kind': 'thumb',
  'status': 'pending',
  'attempts': 0,
};

void main() {
  setUpAll(() {
    sqfliteFfiInit();
    databaseFactory = databaseFactoryFfi;
    _tmp = Directory.systemTemp.createTempSync('esurvey-farmers-test');
  });

  tearDownAll(() {
    try {
      _tmp.deleteSync(recursive: true);
    } catch (_) {
      // Windows holds handles briefly after close.
    }
  });

  group('the people this handset can act for', () {
    test('added, listed, and the last one used comes first', () async {
      final store = await freshStore();
      store.owner = 'acct-1';

      await store.linkFarmer(
        farmerId: 'F-1',
        name: 'Sitabai',
        village: 'V1',
        grantToken: 'g1',
      );
      await store.linkFarmer(
        farmerId: 'F-2',
        name: 'Hari',
        village: 'V1',
        grantToken: 'g2',
      );

      final list = await store.linkedFarmers();
      expect(list, hasLength(2));
      expect(list.first['farmer_id'], 'F-2', reason: 'newest use first');

      await store.touchFarmer('F-1');
      final after = await store.linkedFarmers();
      expect(
        after.first['farmer_id'],
        'F-1',
        reason:
            'a household files for the same person twice running far '
            'more often than it alternates',
      );
    });

    test(
      'adding the same farmer again refreshes rather than duplicates',
      () async {
        final store = await freshStore();
        store.owner = 'acct-1';

        await store.linkFarmer(
          farmerId: 'F-1',
          name: 'Sitabai',
          village: 'V1',
          grantToken: 'old',
        );
        await store.linkFarmer(
          farmerId: 'F-1',
          name: 'Sitabai Patil',
          village: 'V1',
          grantToken: 'new',
        );

        final list = await store.linkedFarmers();
        expect(list, hasLength(1));
        expect(list.first['name'], 'Sitabai Patil');
        expect(list.first['grant_token'], 'new');
      },
    );

    test('another account on the same phone sees its own list', () async {
      // A borrowed handset. The farmers one number has confirmed are not the
      // next number's to see.
      final store = await freshStore();

      store.owner = 'acct-1';
      await store.linkFarmer(farmerId: 'F-1', name: 'Sitabai', village: 'V1');
      store.owner = 'acct-2';
      await store.linkFarmer(farmerId: 'F-9', name: 'Ramrao', village: 'V2');

      expect(await store.linkedFarmers(), hasLength(1));
      store.owner = 'acct-1';
      final mine = await store.linkedFarmers();
      expect(mine, hasLength(1));
      expect(mine.first['farmer_id'], 'F-1');
    });
  });

  group('a grant, which is what lets the office answer', () {
    test('found for the farmer it belongs to', () async {
      final store = await freshStore();
      store.owner = 'acct-1';
      await store.linkFarmer(
        farmerId: 'F-1',
        name: 'Sitabai',
        village: 'V1',
        grantToken: 'g1',
        grantExpiresAt: DateTime.now()
            .add(const Duration(days: 30))
            .millisecondsSinceEpoch,
      );

      expect(await store.grantFor('F-1'), 'g1');
      expect(await store.grantFor('F-2'), isNull);
    });

    test('an expired one is not offered', () async {
      // Sending it would only be refused. The farmer is asked to confirm the
      // person again instead.
      final store = await freshStore();
      store.owner = 'acct-1';
      await store.linkFarmer(
        farmerId: 'F-1',
        name: 'Sitabai',
        village: 'V1',
        grantToken: 'stale',
        grantExpiresAt: DateTime.now()
            .subtract(const Duration(days: 1))
            .millisecondsSinceEpoch,
      );

      expect(await store.grantFor('F-1'), isNull);
    });

    test(
      'found across accounts, because the queue outlives the session',
      () async {
        // A farmer who handed the phone back still has work sending under their
        // own parked credentials, and the office needs their grant to take it.
        final store = await freshStore();
        store.owner = 'acct-1';
        await store.linkFarmer(
          farmerId: 'F-1',
          name: 'Sitabai',
          village: 'V1',
          grantToken: 'g1',
        );

        store.owner = 'acct-2';
        expect(await store.grantFor('F-1'), 'g1');
      },
    );
  });

  group('whose claims are on the screen', () {
    test('only the selected farmer\'s', () async {
      final store = await freshStore();
      store.owner = 'acct-1';
      await store.putDraft(draftFor('d1', 'F-1'));
      await store.putDraft(draftFor('d2', 'F-2'));

      store.farmer = 'F-1';
      final mine = await store.drafts();
      expect(mine, hasLength(1));
      expect(mine.first['local_id'], 'd1');

      store.farmer = 'F-2';
      expect((await store.drafts()).first['local_id'], 'd2');
    });

    test('and the queue card counts only theirs', () async {
      final store = await freshStore();
      store.owner = 'acct-1';
      await store.putDraft(draftFor('d1', 'F-1'));
      await store.putDraft(draftFor('d2', 'F-2'));

      store.farmer = 'F-1';
      final shown = await store.outstanding();
      expect(
        shown.drafts,
        1,
        reason: 'one person\'s claim must not appear in another\'s queue',
      );
    });

    test('with no farmer picked, the account sees everything', () async {
      // What the sync engine reads. A claim that stopped sending because
      // somebody switched farmer is a claim that never arrives.
      final store = await freshStore();
      store.owner = 'acct-1';
      await store.putDraft(draftFor('d1', 'F-1'));
      await store.putDraft(draftFor('d2', 'F-2'));

      store.farmer = null;
      expect((await store.outstanding()).drafts, 2);
      expect(await store.pendingDrafts(), hasLength(2));
    });

    test('the queue is not narrowed by whoever is on screen', () async {
      final store = await freshStore();
      store.owner = 'acct-1';
      await store.putDraft(draftFor('d1', 'F-1'));
      await store.putDraft(draftFor('d2', 'F-2'));

      store.farmer = 'F-1';
      expect(
        await store.pendingDrafts(),
        hasLength(2),
        reason:
            'the sync engine sends every farmer\'s work under the account '
            'that queued it, whoever is being looked at',
      );
    });
  });

  group('what the scheduler is told', () {
    test("another farmer's waiting work still counts", () async {
      // The bug this guards: outstanding() follows the farmer on screen, and
      // the scheduler asks it whether anything on the phone is waiting. With
      // one farmer selected it answered for them alone, so switching to a
      // farmer with nothing queued stopped the background sync -- and the
      // other person's claim never left the handset.
      final store = await freshStore();
      store.owner = 'acct-1';
      await store.putDraft(draftFor('d1', 'F-1'));

      store.farmer = 'F-2';
      expect(
        (await store.outstanding()).any,
        isFalse,
        reason: 'F-2 has nothing of their own on screen',
      );
      expect(
        (await store.outstanding(
          owner: Store.everyone,
          only: Store.everyone,
        )).any,
        isTrue,
        reason:
            "the phone is still carrying F-1's claim, and the scheduler "
            'has to keep waking for it',
      );
    });

    test("a departed account's work is found whoever is on screen", () async {
      // A farmer who handed the phone back has work sending under their own
      // parked credentials. Narrowing that check to the farmer now on screen
      // would purge them while their claim was still queued.
      final store = await freshStore();
      store.owner = 'acct-1';
      await store.putDraft(draftFor('d1', 'F-1', owner: 'acct-9'));

      store.owner = 'acct-1';
      store.farmer = 'F-2';
      expect(
        (await store.outstanding(owner: 'acct-9', only: Store.everyone)).any,
        isTrue,
      );
    });
  });

  test('a departing farmer takes their list off the phone', () async {
    // The path the app actually uses. clearPersonalData wipes everything and
    // is called from nowhere in the app -- so the list was surviving sign-out
    // on a borrowed handset, leaving the previous household's names, their
    // Farmer IDs and tokens valid for ninety days for whoever picked it up.
    final store = await freshStore();
    store.owner = 'acct-1';
    await store.linkFarmer(
      farmerId: 'F-1',
      name: 'Sitabai',
      village: 'V1',
      grantToken: 'g1',
    );

    // Nothing of theirs is waiting, so they go now.
    expect(await store.purgeOwnerIfDone('acct-1'), isTrue);
    expect(await store.linkedFarmers(owner: 'acct-1'), isEmpty);
    expect(
      await store.grantFor('F-1'),
      isNull,
      reason: 'the grant leaves with the farmer it belonged to',
    );
  });

  test('a farmer with work still sending keeps their grant', () async {
    // The other half. Their claim is still going under their own parked
    // credentials, and the office will not take it without the grant.
    final store = await freshStore();
    store.owner = 'acct-1';
    await store.linkFarmer(
      farmerId: 'F-1',
      name: 'Sitabai',
      village: 'V1',
      grantToken: 'g1',
    );
    await store.putDraft(draftFor('d1', 'F-1'));

    expect(await store.purgeOwnerIfDone('acct-1'), isFalse);
    expect(await store.grantFor('F-1'), 'g1');
  });

  test('signing out takes the list with it', () async {
    // A phone in a village is borrowed. The farmers one person confirmed must
    // not be sitting in the app when the next person opens it.
    final store = await freshStore();
    store.owner = 'acct-1';
    await store.linkFarmer(
      farmerId: 'F-1',
      name: 'Sitabai',
      village: 'V1',
      grantToken: 'g1',
    );

    await store.clearPersonalData();
    expect(await store.linkedFarmers(), isEmpty);
    expect(await store.grantFor('F-1'), isNull);
  });
}
