import 'package:esurvey/core/language.dart';
import 'package:esurvey/report/assessment_report_screen.dart';
import 'package:flutter_test/flutter_test.dart';

void main() {
  const page =
      '<html><head><title>x</title></head><body>'
      '<section class="report" lang="mr">मराठी</section>'
      '<section class="report" lang="en">English</section></body></html>';

  tearDown(() => Language.current = AppLanguage.marathi);

  test('the phone shows the region\'s half of the document', () {
    // The printed document carries both languages in full. On the farmer's
    // phone, only theirs.
    Language.current = AppLanguage.marathi;
    final shown = inRegionLanguage(page);
    expect(shown, contains('section.report[lang="en"]{display:none'));
    expect(shown, isNot(contains('[lang="mr"]{display:none')));
    expect(shown, contains('मराठी'), reason: 'nothing is removed, only hidden');
  });

  test('a page with no head is shown as it came', () {
    expect(inRegionLanguage('<p>x</p>'), '<p>x</p>');
  });
}
