import 'package:esurvey/report/parcel_detail_screen.dart';
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';

/// What the land record says, shown to the person it is about.
///
/// # Why this screen is worth a test
///
/// Because it is the only place a wrong land record becomes visible before it costs
/// somebody money. The sown area is AgriStack's figure rather than anything the
/// farmer types, and both the payout and the walk's spacing are derived from it -- so
/// a farmer who can see that the registry thinks they sowed a quarter hectare, when
/// they sowed half, can take it to the taluka office while the survey window is
/// still open.
///
/// The cases that matter are the incomplete ones. A registry record missing a crop,
/// or missing an area, is the ordinary state of this data and must read as "the
/// record is incomplete" rather than as the app being broken or the farmer being
/// refused.
///
/// The assertions are on the Marathi, because that is what is on the screen: tr()
/// answers in the language of the place and this app is built for one. Asserting on
/// the English would be asserting on a string no farmer is shown.
Widget wrap(Widget child) => MaterialApp(home: child);

const _parcel = {
  'farm_id': 'PICT-TEST-0001',
  'survey_number': '999',
  'sub_survey_number': '1',
  'owner_extent': '0.25',
  'owner_name_ror': 'पीआयसीटी चाचणी शेत',
};

void main() {
  testWidgets('every crop on the field is shown, not just the first', (
    tester,
  ) async {
    // One farm carries several crops and each is claimed separately. A screen
    // showing one of them would hide the existence of the other.
    await tester.pumpWidget(
      wrap(
        const ParcelDetailScreen(
          parcel: _parcel,
          crops: [
            {'crop_name': 'Soybean', 'sown_area_ha': 0.25},
            {'crop_name': 'Udad', 'sown_area_ha': 0.25},
          ],
        ),
      ),
    );

    expect(find.text('Soybean'), findsOneWidget);
    expect(find.text('Udad'), findsOneWidget);
  });

  testWidgets('the sown area says what it decides', (tester) async {
    // A number with no explanation is a number nobody checks. This one governs the
    // payout and the walking distance, and saying so is what turns a farmer into
    // somebody who reads it.
    await tester.pumpWidget(
      wrap(
        const ParcelDetailScreen(
          parcel: _parcel,
          crops: [
            {'crop_name': 'Soybean', 'sown_area_ha': 0.25},
          ],
        ),
      ),
    );

    expect(
      find.textContaining('चालण्याचे अंतर यावरून ठरते'),
      findsOneWidget,
      reason: 'the figure is shown without saying what turns on it',
    );
  });

  testWidgets('a crop with no recorded area says the form will ask', (
    tester,
  ) async {
    // The office omits an area it cannot read rather than guessing at one, so this
    // is a real state. It must not look like a refusal: the claim is still
    // possible and the form asks.
    await tester.pumpWidget(
      wrap(
        const ParcelDetailScreen(
          parcel: _parcel,
          crops: [
            {'crop_name': 'Soybean'},
          ],
        ),
      ),
    );

    expect(find.textContaining('नोंद करताना विचारले जाईल'), findsOneWidget);
  });

  testWidgets('a field with no crop recorded is not treated as a refusal', (
    tester,
  ) async {
    // The crop registry is filled in by somebody else and is often behind. A
    // farmer meeting this needs to know a claim is still possible.
    await tester.pumpWidget(
      wrap(const ParcelDetailScreen(parcel: _parcel, crops: [])),
    );

    expect(find.textContaining('पिकाची नोंद अजून झालेली नाही'), findsOneWidget);
    expect(find.textContaining('नुकसान कळवताना'), findsOneWidget);
  });

  testWidgets('the share is distinguished from the field', (tester) async {
    // A survey number is routinely shared between joint owners, so the share and
    // the field are different numbers -- and a farmer comparing the 0.25 here with
    // the 1.46 the polygon measures needs to know which is which.
    await tester.pumpWidget(
      wrap(const ParcelDetailScreen(parcel: _parcel, crops: [])),
    );

    expect(find.textContaining('तुमचा हिस्सा'), findsOneWidget);
  });

  testWidgets('and it says where a wrong figure gets corrected', (
    tester,
  ) async {
    // Otherwise a farmer who spots an error asks the app to fix it, and the app
    // cannot: none of this is the app's to change.
    await tester.pumpWidget(
      wrap(const ParcelDetailScreen(parcel: _parcel, crops: [])),
    );

    expect(find.textContaining('तलाठी कार्यालयात'), findsOneWidget);
    expect(find.textContaining('अ‍ॅपमध्ये बदलता येत नाही'), findsOneWidget);
  });

  testWidgets('a sub-survey number is shown with its parent', (tester) async {
    // Two plots on one survey number is how one farm carries two fields, and 999/1
    // is what distinguishes them on paper.
    await tester.pumpWidget(
      wrap(const ParcelDetailScreen(parcel: _parcel, crops: [])),
    );

    expect(find.text('999/1'), findsOneWidget);
  });
}
