import 'package:flutter/material.dart';

import '../core/language.dart';
import '../ui/theme.dart';

/// One field, as the land record describes it.
///
/// # Why a farmer is shown this at all
///
/// Because the claim is measured against it and they have never seen it. The sown
/// area on the form is AgriStack's figure, not something they type, and the walk's
/// spacing is derived from it -- so a farmer whose claim comes back smaller than they
/// expected is owed the numbers it was calculated from, in advance rather than in an
/// argument.
///
/// It is also the one place a wrong land record becomes visible before it costs
/// anybody anything. A farmer who can see that the registry thinks they hold a
/// quarter hectare of soybean, when they sowed half, can take that to the office
/// while the survey window is still open.
///
/// # Why nothing here can be edited
///
/// None of it is the app's to change. These are the record of rights and the crop
/// registry, and the remedy for a wrong figure is the taluka office, not a text
/// field on a phone. Saying so plainly is more use than a form that appears to
/// accept a correction and quietly discards it.
class ParcelDetailScreen extends StatelessWidget {
  const ParcelDetailScreen({
    super.key,
    required this.parcel,
    required this.crops,
    this.hasBoundary,
  });

  final Map<String, dynamic> parcel;

  /// The crops the registry records on this land, already narrowed to the season
  /// the survey is for. Several is the ordinary case: one farm carries soybean on
  /// part of it and udad on another, and each is claimed on its own.
  final List<Map<String, dynamic>> crops;

  /// Whether a boundary exists, where the caller has already worked it out. Null
  /// leaves the question off the screen rather than guessing at it.
  final bool? hasBoundary;

  @override
  Widget build(BuildContext context) {
    final text = Theme.of(context).textTheme;
    final survey = '${parcel['survey_number'] ?? ''}';
    final sub = '${parcel['sub_survey_number'] ?? ''}'.trim();
    final extent = '${parcel['owner_extent'] ?? ''}'.trim();
    final owner = '${parcel['owner_name_ror'] ?? ''}'.trim();

    return Scaffold(
      appBar: AppBar(
        title: Text(tr('सर्व्हे नं. $survey', 'Survey no. $survey')),
      ),
      body: ListView(
        padding: Space.page,
        children: [
          SectionHeader(tr('जमिनीची नोंद', 'The land record')),
          AppCard(
            child: Column(
              children: [
                if (owner.isNotEmpty)
                  _Line(
                    label: tr('नोंदीतील नाव', 'Name on the record'),
                    // The 7/12's own spelling, which is Devanagari and is what a
                    // farmer would see on paper. Shown in preference to the
                    // transliterated name for that reason.
                    value: owner,
                  ),
                _Line(
                  label: tr('सर्व्हे नंबर', 'Survey number'),
                  value: sub.isEmpty || sub == '*' ? survey : '$survey/$sub',
                ),
                if (extent.isNotEmpty)
                  _Line(
                    label: tr('तुमचे क्षेत्र', 'Your share'),
                    value: tr('$extent हेक्टर', '$extent ha'),
                    // A survey number is routinely shared between joint owners, so
                    // the share and the field are different numbers and a farmer
                    // comparing them needs to know which is which.
                    note: tr(
                      'या सर्व्हे नंबरमधील तुमचा हिस्सा.',
                      'Your share of this survey number.',
                    ),
                  ),
                if (hasBoundary != null)
                  _Line(
                    label: tr('हद्द', 'Boundary'),
                    value: hasBoundary == true
                        ? tr('नोंदलेली आहे', 'Recorded')
                        : tr('नोंदलेली नाही', 'Not recorded'),
                  ),
              ],
            ),
          ),

          const SizedBox(height: Space.lg),
          SectionHeader(tr('पिकांची नोंद', 'The crop record')),
          if (crops.isEmpty)
            const AppCard(child: _Empty())
          else
            for (final crop in crops) _CropCard(crop: crop),

          const SizedBox(height: Space.lg),
          Text(
            // The remedy, because a farmer who spots a wrong figure will otherwise
            // ask the app to fix it and the app cannot.
            tr(
              'ही माहिती सात-बारा आणि पीक पाहणीच्या नोंदीतून घेतली आहे. '
                  'यात चूक असल्यास तलाठी कार्यालयात दुरुस्त करून घ्या — '
                  'अ‍ॅपमध्ये बदलता येत नाही.',
              'This comes from the land record and the crop registry. If '
                  'anything is wrong, have it corrected at the taluka office; it '
                  'cannot be changed in the app.',
            ),
            style: text.bodySmall?.copyWith(color: Palette.inkMuted),
          ),
        ],
      ),
    );
  }
}

/// One crop, and the figures a claim for it will be measured against.
class _CropCard extends StatelessWidget {
  const _CropCard({required this.crop});

  final Map<String, dynamic> crop;

  @override
  Widget build(BuildContext context) {
    final name = '${crop['crop_name'] ?? ''}'.trim();
    final sown = crop['sown_area_ha'];
    final season = '${crop['season'] ?? ''}'.trim();
    final year = '${crop['year'] ?? ''}'.trim();
    final sowing = '${crop['sowing_date'] ?? ''}'.trim();
    final irrigation = '${crop['irrigation_type'] ?? ''}'.trim();

    return Padding(
      padding: const EdgeInsets.only(bottom: Space.md),
      child: AppCard(
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Row(
              children: [
                const IconBadge(Icons.grass_outlined),
                const SizedBox(width: Space.md),
                Expanded(
                  child: Text(
                    name.isEmpty ? tr('पीक', 'Crop') : name,
                    style: Theme.of(context).textTheme.titleMedium,
                  ),
                ),
              ],
            ),
            const SizedBox(height: Space.sm),
            if (sown != null)
              _Line(
                label: tr('पेरणी केलेले क्षेत्र', 'Area sown'),
                value: tr('$sown हेक्टर', '$sown ha'),
                // The figure that decides how far apart the walk's points are, and
                // what the claim is paid on. Worth naming as such: a farmer who
                // understands this number is a farmer who checks it.
                note: tr(
                  'नुकसानीची रक्कम आणि चालण्याचे अंतर यावरून ठरते.',
                  'The payout and the walking distance are worked out from this.',
                ),
              )
            else
              _Line(
                label: tr('पेरणी केलेले क्षेत्र', 'Area sown'),
                value: tr('नोंद नाही', 'Not recorded'),
                note: tr(
                  'नोंदणीत क्षेत्र नाही, म्हणून नोंद करताना विचारले जाईल.',
                  'The registry has no area, so the form will ask for it.',
                ),
              ),
            if (season.isNotEmpty || year.isNotEmpty)
              _Line(
                label: tr('हंगाम', 'Season'),
                value: [season, year].where((v) => v.isNotEmpty).join(' '),
              ),
            if (sowing.isNotEmpty)
              _Line(label: tr('पेरणीची तारीख', 'Sown on'), value: sowing),
            if (irrigation.isNotEmpty)
              _Line(
                label: tr('ओलिताचा प्रकार', 'Irrigation'),
                value: irrigation,
              ),
          ],
        ),
      ),
    );
  }
}

class _Empty extends StatelessWidget {
  const _Empty();

  @override
  Widget build(BuildContext context) => Text(
    // Not an error, and not the farmer's fault. The crop registry is filled in by
    // somebody else and is often behind; a claim is still possible and the form
    // will ask what is growing.
    tr(
      'या शेतावर पिकाची नोंद अजून झालेली नाही. नुकसान कळवताना कोणते पीक '
          'आहे ते विचारले जाईल.',
      'No crop is recorded on this field yet. You will be asked which crop '
          'when you report damage.',
    ),
    style: Theme.of(context).textTheme.bodyMedium,
  );
}

/// A label and its value, with an optional sentence about what the value governs.
class _Line extends StatelessWidget {
  const _Line({required this.label, required this.value, this.note});

  final String label;
  final String value;
  final String? note;

  @override
  Widget build(BuildContext context) {
    final text = Theme.of(context).textTheme;
    return Padding(
      padding: const EdgeInsets.symmetric(vertical: 6),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Row(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              Expanded(
                child: Text(
                  label,
                  style: text.bodyMedium?.copyWith(color: Palette.inkMuted),
                ),
              ),
              const SizedBox(width: Space.md),
              Flexible(
                child: Text(
                  value,
                  textAlign: TextAlign.end,
                  style: text.bodyLarge?.copyWith(fontWeight: FontWeight.w600),
                ),
              ),
            ],
          ),
          if (note != null)
            Padding(
              padding: const EdgeInsets.only(top: 2),
              child: Text(
                note!,
                style: text.bodySmall?.copyWith(color: Palette.inkMuted),
              ),
            ),
        ],
      ),
    );
  }
}
