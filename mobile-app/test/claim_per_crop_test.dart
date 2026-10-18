import 'package:esurvey/report/file_report_screen.dart';
import 'package:flutter_test/flutter_test.dart';

/// One farm, several crops, and one claim for each.
///
/// # Why this is the ordinary case rather than an edge one
///
/// A farmer holds one registered farm on a survey number and grows soybean on part
/// of it and udad on another. Each is sown on its own date, damaged to its own
/// extent, walked at its own spacing and paid on its own figure -- so each is a
/// separate claim, and a home screen offering one button for the whole event sent a
/// farmer with two crops to a form where the second could not be reached.
///
/// The registry has always carried this: crops arrive as an array on the parcel,
/// joined by farm_id. What was missing was the app offering them and the office
/// accepting the second one.
void main() {
  group('the crops a claim can be filed for', () {
    test('both crops on one farm are offered', () {
      final parcel = {
        'farm_id': 'DEMO-OFFICE-0001',
        'survey_number': '102',
        'crops': [
          {
            'crop_name': 'Soybean',
            'season': 'Kharif',
            'year': '2026-2027',
            'sown_area_ha': 0.00253,
          },
          {
            'crop_name': 'Udad',
            'season': 'Kharif',
            'year': '2026-2027',
            'sown_area_ha': 0.00253,
          },
        ],
      };
      final event = {'season': 'kharif', 'crop_year': '2026-2027'};

      final crops = cropsFor(parcel, event);
      expect(crops.map((c) => c['crop_name']), ['Soybean', 'Udad']);
    });

    test('a crop from another season is not offered', () {
      // The event is the claim's frame: a rabi wheat crop is not damaged by a
      // kharif drought, and offering it would invite a claim the office refuses.
      final parcel = {
        'farm_id': 'F1',
        'crops': [
          {'crop_name': 'Soybean', 'season': 'Kharif', 'year': '2026-2027'},
          {'crop_name': 'Wheat', 'season': 'Rabi', 'year': '2026-2027'},
        ],
      };
      final crops = cropsFor(parcel, {
        'season': 'kharif',
        'crop_year': '2026-2027',
      });
      expect(crops.map((c) => c['crop_name']), ['Soybean']);
    });

    test('a registry that recorded no crop still leaves a way in', () {
      // An incomplete land record is not a reason to refuse somebody a claim. The
      // form asks what is growing in this one case, which is also the only case
      // where the sown area may be typed.
      final crops = cropsFor(
        {'farm_id': 'F1', 'crops': <dynamic>[]},
        {'season': 'kharif'},
      );
      expect(crops, isEmpty);
    });

    test('a crop with no season is offered rather than hidden', () {
      // Registry records are often incomplete in exactly this way. Hiding the crop
      // would leave a farmer with land, a declared calamity and no button.
      final crops = cropsFor(
        {
          'farm_id': 'F1',
          'crops': [
            {'crop_name': 'Soybean'},
          ],
        },
        {'season': 'kharif', 'crop_year': '2026-2027'},
      );
      expect(crops.map((c) => c['crop_name']), ['Soybean']);
    });
  });
}
