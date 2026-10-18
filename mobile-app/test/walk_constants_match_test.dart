import 'dart:io';

import 'package:esurvey/capture/station_guide.dart';
import 'package:esurvey/core/parcel_location.dart';
import 'package:flutter_test/flutter_test.dart';

/// The numbers the app and the office must agree on, read out of the office's source.
///
/// # Why this test exists
///
/// Because the walk's whole contract is that the app permits exactly what the office
/// accepts. Every place the two disagree is a farmer who filmed a clip standing in
/// their own field, walked home, and was told to go back and do it again -- and the
/// clip was never wrong, the two rules were.
///
/// Until now nothing checked it. Each side had its own tests, each passing against its
/// own idea of the numbers, and the comments on both sides asserted they matched. That
/// is the exact shape of a fault this project has already shipped twice: an officer
/// agent sending a header nothing read, and a relay ticket whose format each end
/// verified against its own assumption. Both times two suites were green.
///
/// # Why it reads the Go and the SQL rather than restating them
///
/// A copy of a number is not a check on it. These parse the office's own source, so a
/// change on that side fails here rather than being discovered by a farmer. If the
/// source moves or changes shape, this fails loudly and somebody teaches it the new
/// shape -- which is the intended cost, and far below the cost of finding out in a
/// field.
///
/// # What it deliberately does not check
///
/// The sanity ceiling on the spacing, which is a convex-hull diameter on the app and
/// ST_MaxDistance in a UTM projection at the office. Those are not one number and
/// cannot be compared by reading a constant; they are two computations over two copies
/// of a geometry. What closes that gap is the office's own frozen figure reaching the
/// walk -- see encodeStations -- rather than a test of two formulas.
File _goSource(String file) => _repoFile('backend/internal/survey/$file');

File _sqlSource(String file) =>
    _repoFile('backend/internal/platform/db/migrations/$file');

/// A file in the repository, found by walking up from wherever the test runs.
File _repoFile(String relative) {
  var dir = Directory.current;
  for (var up = 0; up < 5; up++) {
    final candidate = File('${dir.path}/$relative');
    if (candidate.existsSync()) return candidate;
    final parent = dir.parent;
    if (parent.path == dir.path) break;
    dir = parent;
  }
  // Failed, not skipped. An unchecked constant is the whole thing this guards
  // against, and a test that quietly stands down is worse than no test: the next
  // real divergence reads as the same known silence.
  fail(
    'could not find $relative from ${Directory.current.path}. This test must '
    "read the office's own source; restating the number here would make it a "
    'copy rather than a check.',
  );
}

/// One Go constant, as a double.
///
/// The pattern is built by concatenation rather than interpolation because a raw
/// string cannot interpolate, and a non-raw one would read every backslash in it as an
/// escape -- which is how the first version of this file came to hold the pattern
/// `names*=s*(...)` and report all four constants missing.
double _goConst(String file, String name) {
  final src = _goSource(file).readAsStringSync();
  final m = RegExp(
    'const $name'
    r'\s*=\s*([0-9.]+)',
  ).firstMatch(src);
  if (m == null) {
    fail(
      '$name is no longer declared in survey/$file. If it moved, point this '
      'test at its new home; do not delete the check.',
    );
  }
  return double.parse(m.group(1)!);
}

/// One number out of a migration, by the expression that surrounds it.
double _sqlNumber(String file, RegExp pattern, String describe) {
  final src = _sqlSource(file).readAsStringSync();
  final m = pattern.firstMatch(src);
  if (m == null) {
    fail(
      '$describe could no longer be found in $file. The office\'s rule has '
      'changed shape; read it and decide whether the app must follow, then '
      'teach this test the new shape.',
    );
  }
  return double.parse(m.group(1)!);
}

void main() {
  group('the fences', () {
    test("the parcel tolerance is the office's", () {
      // How far outside their own boundary a farmer may stand and still record.
      // The office refuses beyond it outright, so a looser figure here is a walk
      // spent twice.
      //
      // Read from the geofence function rather than from the Go constant beside
      // it: GeofenceBufferM exists in capture.go but the query does not use it, so
      // the number that governs is the literal in the SQL.
      expect(
        atParcelWithinM,
        _sqlNumber(
          '0007_self_survey.sql',
          RegExp(r'ST_DWithin\([\s\S]*?4326\)::geography,\s*([0-9.]+)\s*\)'),
          'the tolerance in capture_geofence',
        ),
      );
    });

    test("the unmapped-field radius is the office's", () {
      // The only fence a claim with no recorded boundary has: every clip within
      // this distance of the first. The app did not check it at all, so a farmer
      // on an unmapped parcel could walk out of range, film, and be refused.
      expect(
        unmappedFieldRadiusM,
        _goConst('capture.go', 'UnmappedFieldRadiusM'),
      );
    });
  });

  group('the spacing rule', () {
    test("the smallest spacing is the office's", () {
      // Half a guntha is about seven metres across. Above this figure the rule
      // refuses every point of a plot that small -- which it did, once.
      expect(
        smallestSpacingM,
        _goConst('capture.go', 'SmallestStationSpacingM'),
      );
    });

    test("the parcel rule's ceiling is the office's", () {
      expect(minSpacingM, _goConst('capture.go', 'MinStationSpacingM'));
    });

    test("the coverage target is the office's", () {
      // What fraction of the sown extent the four points are meant to enclose.
      // The office holds it in a function so one number serves the whole rule.
      expect(
        coverageTarget,
        _sqlNumber(
          '0055_walk_spread_from_crop_extent.sql',
          RegExp(r'FUNCTION walk_coverage_target[\s\S]*?SELECT\s+([0-9.]+)'),
          'the value walk_coverage_target returns',
        ),
      );
    });
  });
}
