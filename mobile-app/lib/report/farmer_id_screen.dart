import 'package:flutter/material.dart';
import 'package:flutter/services.dart';

import '../core/db.dart';
import '../core/farmer_data.dart';
import '../core/language.dart';
import '../core/voice.dart';
import '../ui/theme.dart';

/// Adding a farmer to this handset: which farmer, and therefore which land.
///
/// # Why the ID is not enough on its own
///
/// Sign-in is a mobile number and a one-time code, so anyone reaching this
/// screen holds some SIM. The Farmer ID proves nothing beside it: it is
/// printed on documents and known to half the village.
///
/// The strict rule was that a farmer may load only the record whose
/// registered mobile is the number they verified. That is the safest rule and
/// it locks out a great many of the people this app is for -- AgriStack holds
/// whatever number was given at enrolment, which is routinely one the farmer
/// no longer has, and they cannot re-enrol while standing in a hailed field.
///
/// So it was decided on 2026-09-16 that the record is identified by the ID
/// together with the date of birth the registry holds. The office checks the
/// pair, counts wrong answers and locks out an account that keeps guessing;
/// this screen checks the shape of the date first so a farmer's typo is not
/// spent against that budget.
///
/// # Why one handset holds several farmers
///
/// A household shares a phone. One number may be the one AgriStack has for
/// several people, and a farmer whose own number is gone borrows a relative's.
/// Each is added here once and then picked from a list; the list lives on this
/// phone and nowhere else, so the office holds no register of which households
/// share a handset.
///
/// # Why it is worth typing at all
///
/// Everything after this is filled in from the registry: the survey number,
/// the crop, the sown area, the sowing date, the boundary the geofence uses.
/// The farmer confirms rather than types, which is the difference between a
/// form somebody abandons in a field and one they finish.
class FarmerIdScreen extends StatefulWidget {
  const FarmerIdScreen({
    super.key,
    required this.data,
    required this.store,
    required this.voice,
    required this.onLoaded,
  });

  final FarmerData data;
  final Store store;
  final Voice voice;

  /// Called with the registry record once it is in hand, and added to the
  /// handset's list of farmers.
  final void Function(Map<String, Object?> record) onLoaded;

  @override
  State<FarmerIdScreen> createState() => _FarmerIdScreenState();
}

class _FarmerIdScreenState extends State<FarmerIdScreen> {
  final _controller = TextEditingController();
  final _dobController = TextEditingController();

  bool _loading = false;
  String? _errorMr;
  String? _errorEn;

  @override
  void dispose() {
    _controller.dispose();
    _dobController.dispose();
    super.dispose();
  }

  String get _id => _controller.text.trim();
  String get _dob => _dobController.text.trim();

  /// Whether the date is a date at all, checked here rather than at the
  /// office.
  ///
  /// The office counts wrong answers and stops an account that keeps guessing.
  /// A farmer who typed the month where the day goes has not guessed at
  /// anything, and spending their small budget on a typo would lock them out
  /// of their own record for a quarter of an hour.
  bool get _dobLooksLikeADate => _parsedDob() != null;

  DateTime? _parsedDob() {
    final parts = _dob.replaceAll('/', '-').split('-');
    if (parts.length != 3) return null;
    final d = int.tryParse(parts[0]);
    final m = int.tryParse(parts[1]);
    final y = int.tryParse(parts[2]);
    if (d == null || m == null || y == null) return null;
    if (y < 1900 || y > DateTime.now().year) return null;
    final when = DateTime(y, m, d);
    // Rejects 31-02: DateTime rolls such a date forward rather than refusing.
    if (when.day != d || when.month != m || when.year != y) return null;
    return when;
  }

  bool get _ready => _id.isNotEmpty && _dobLooksLikeADate && !_loading;

  void _fail(String mr, String en) {
    setState(() {
      _loading = false;
      _errorMr = mr;
      _errorEn = en;
    });
  }

  Future<void> _load() async {
    if (!_ready) return;

    setState(() {
      _loading = true;
      _errorMr = null;
      _errorEn = null;
    });

    final result = await widget.data.lookup(_id, _dob);
    if (!mounted) return;

    if (result.offline) {
      // Nothing was reached, which says nothing about the details. Sending
      // somebody to check a document that was never the problem costs them a
      // walk back to the house and the belief that the app works.
      _fail(
        'नेटवर्क नाही. नवीन शेतकरी जोडण्यासाठी एकदा नेटवर्क लागते. '
            'आधी जोडलेले शेतकरी नेटवर्कशिवाय चालतात.',
        'No network. Adding a farmer needs a connection once. Farmers already '
            'on this phone work without one.',
      );
      return;
    }

    final found = result.found;
    if (found == null) {
      // One message for every refusal the office can give here: no such
      // farmer, wrong date, or too many tries. Distinguishing them would tell
      // somebody holding a neighbour's ID which half they had right.
      _fail(
        'हा ओळख क्रमांक आणि जन्मतारीख जुळत नाहीत. ७/१२ उतारा पाहून '
            'पुन्हा तपासा. काही वेळ थांबून पुन्हा प्रयत्न करावा लागू शकतो.',
        'That Farmer ID and date of birth do not match a record. Check them '
            'against the 7/12 extract. After a few wrong tries you may have '
            'to wait a little before trying again.',
      );
      return;
    }

    // Remembered on this phone with the grant it just earned, so this farmer
    // can be picked from the list next time without answering again.
    final record = found.record;
    final parcels = (record['parcels'] as List<dynamic>?) ?? const [];
    await widget.store.linkFarmer(
      farmerId: '${record['farmer_id'] ?? _id}',
      name: '${record['farmer_name'] ?? ''}',
      village: parcels.isEmpty
          ? ''
          : '${(parcels.first as Map)['village_lgd_code'] ?? ''}',
      grantToken: found.grant,
      grantExpiresAt: found.expires?.millisecondsSinceEpoch,
    );

    if (!mounted) return;
    setState(() => _loading = false);
    widget.onLoaded(record);
  }

  @override
  Widget build(BuildContext context) {
    final text = Theme.of(context).textTheme;
    return ListView(
      padding: const EdgeInsets.fromLTRB(20, 12, 20, 32),
      children: [
        Text(
          tr(
            'शेतकरी ओळख क्रमांक आणि नोंदणीतील जन्मतारीख टाका.',
            'Enter the Farmer ID and the date of birth on the record.',
          ),
          style: text.bodyMedium?.copyWith(color: Palette.inkMuted),
        ),
        const SizedBox(height: Space.xl),

        TextField(
          controller: _controller,
          keyboardType: TextInputType.number,
          inputFormatters: [FilteringTextInputFormatter.digitsOnly],
          // Read off a document at arm's length: spaced digits are the
          // difference between reading it once and reading it four times.
          style: const TextStyle(fontSize: 18, letterSpacing: 1.5),
          decoration: InputDecoration(
            labelText: tr('शेतकरी ओळख क्रमांक', 'Farmer ID'),
            hintText: '56567763672',
            prefixIcon: const Icon(Icons.badge_outlined),
            helperText: tr(
              '७/१२ उताऱ्यावर किंवा शेतकरी नोंदणी कार्डावर असतो',
              'On the 7/12 extract or the farmer registration card',
            ),
          ),
          onChanged: (_) => setState(() {}),
          onSubmitted: (_) => _load(),
        ),

        const SizedBox(height: Space.lg),

        // Typed rather than picked from a calendar. A date picker opened at
        // this year is a long scroll back to a birth year, and the farmer is
        // reading the date off a document in front of them.
        TextField(
          controller: _dobController,
          keyboardType: TextInputType.number,
          inputFormatters: [_DashedDate()],
          style: const TextStyle(fontSize: 18, letterSpacing: 1.5),
          decoration: InputDecoration(
            labelText: tr('जन्मतारीख', 'Date of birth'),
            hintText: tr('दिवस-महिना-वर्ष', 'DD-MM-YYYY'),
            prefixIcon: const Icon(Icons.cake_outlined),
            helperText: tr('उदा. 01-01-1973', 'e.g. 01-01-1973'),
          ),
          onChanged: (_) => setState(() {}),
          onSubmitted: (_) => _load(),
        ),

        if (_errorMr != null) ...[
          const SizedBox(height: Space.lg),
          Notice(
            tone: Tone.danger,
            icon: Icons.error_outline,
            title: tr(_errorMr!, _errorEn ?? ''),
            margin: EdgeInsets.zero,
          ),
        ],

        const SizedBox(height: Space.xl),

        SizedBox(
          width: double.infinity,
          child: FilledButton(
            onPressed: _ready ? _load : null,
            child: _loading
                ? const SizedBox(
                    height: 22,
                    width: 22,
                    child: CircularProgressIndicator(
                      strokeWidth: 2,
                      color: Colors.white,
                    ),
                  )
                : Text(tr('पुढे', 'Continue')),
          ),
        ),

        const SizedBox(height: Space.xl),
        Row(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            const Icon(
              Icons.shield_outlined,
              size: 18,
              color: Palette.inkMuted,
            ),
            const SizedBox(width: Space.sm),
            Expanded(
              child: Text(
                tr(
                  'ही माहिती नोंदणीशी जुळवून पाहिली जाते. अनेक वेळा चुकल्यास '
                      'थोडा वेळ थांबावे लागेल.',
                  'These details are checked against the registry. After '
                      'several wrong tries you will need to wait a little.',
                ),
                style: text.labelMedium,
              ),
            ),
          ],
        ),
      ],
    );
  }
}

/// Puts the dashes in a typed date, so the farmer only enters digits.
///
/// The keypad on these handsets is the number pad -- reaching a dash means
/// switching layouts, twice, in the middle of a date read off a document.
class _DashedDate extends TextInputFormatter {
  @override
  TextEditingValue formatEditUpdate(
    TextEditingValue old,
    TextEditingValue next,
  ) {
    final digits = next.text.replaceAll(RegExp(r'[^0-9]'), '');
    final capped = digits.length > 8 ? digits.substring(0, 8) : digits;

    final b = StringBuffer();
    for (var i = 0; i < capped.length; i++) {
      if (i == 2 || i == 4) b.write('-');
      b.write(capped[i]);
    }
    final text = b.toString();
    return TextEditingValue(
      text: text,
      selection: TextSelection.collapsed(offset: text.length),
    );
  }
}
