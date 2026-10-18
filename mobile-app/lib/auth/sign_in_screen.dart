import 'dart:async';

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';

import '../core/api.dart';
import '../core/language.dart';
import '../core/session.dart';
import '../core/voice.dart';
import '../ui/theme.dart';

/// Sign-in: mobile number in, one-time code back by SMS.
///
/// This is the one screen in the app that genuinely needs the network -- an
/// SMS code cannot reach a handset with no signal. So everything here is
/// built around not wasting a round trip: the number is checked locally
/// before anything is sent, and the resend button counts down visibly
/// instead of letting a farmer press it into the server's rate limiter.
class SignInScreen extends StatefulWidget {
  const SignInScreen({
    super.key,
    required this.api,
    required this.session,
    required this.voice,
    required this.onSignedIn,
    this.notice,
  });

  final Api api;
  final Session session;
  final Voice voice;
  final VoidCallback onSignedIn;

  /// Work waiting on this phone, said above the form: the returning farmer's
  /// own claim that needs them to sign in, or another farmer's still going.
  final ({String mr, String en})? notice;

  @override
  State<SignInScreen> createState() => _SignInScreenState();
}

enum _Step { mobile, code }

class _SignInScreenState extends State<SignInScreen> {
  final _mobileCtrl = TextEditingController();
  final _codeCtrl = TextEditingController();

  _Step _step = _Step.mobile;
  bool _busy = false;

  /// The normalised ten digits the code actually went to -- shown back to the
  /// farmer so a typo is caught by reading, not by a code that never arrives.
  String _mobile = '';

  String? _errorMr;
  String? _errorEn;

  int _cooldown = 0;
  Timer? _cooldownTimer;

  static const _offlineMr =
      'नेटवर्क नाही. लॉगिनसाठी नेटवर्क लागते, कारण कोड SMS ने येतो. बाकीचे ॲप नेटवर्कशिवाय चालते.';
  static const _offlineEn =
      'No network. Signing in needs a connection for the SMS code — the rest of this app works offline, but this step cannot.';

  static const _transientMr =
      'सर्व्हरशी संपर्क होऊ शकला नाही. थोड्या वेळाने पुन्हा प्रयत्न करा.';
  static const _transientEn = 'Could not reach the server. Try again shortly.';

  // The code could not be sent. Two different things, and a farmer standing
  // in a field decides what to do next from which one they are told: press
  // again in a minute, or stop pressing and go and ask somebody.
  static const _smsBusyMr =
      'कोड पाठवता आला नाही. दोन मिनिटांनी पुन्हा "कोड पाठवा" दाबा.';
  static const _smsBusyEn =
      'The code could not be sent. Wait two minutes and press Send code again.';

  static const _smsBrokenMr =
      'सध्या कोड पाठवता येत नाहीत. ही आमच्याकडची अडचण आहे, तुमच्या नंबरमध्ये '
      'काही चूक नाही. कृपया गावातील कृषी सहाय्यकांकडे विचारा.';
  static const _smsBrokenEn =
      'Codes cannot be sent at the moment. This is a fault at our end, not '
      'with your number. Please ask your village agriculture assistant.';

  // An officer's number at the farmer sign-in. Said plainly where the server
  // is willing to say it, because the alternative is a screen that reports a
  // code was sent and a code that never comes.
  static const _officerMr =
      'हा नंबर अधिकाऱ्याच्या खात्याचा आहे. अधिकारी eSurvey कन्सोलवर ईमेल आणि '
      'पासवर्डने लॉगिन करतात, या ॲपमध्ये नाही.';
  static const _officerEn =
      'This number belongs to an officer account. Officers sign in on the '
      'eSurvey console with an email and password, not in this app.';

  @override
  void dispose() {
    _cooldownTimer?.cancel();
    _mobileCtrl.dispose();
    _codeCtrl.dispose();
    super.dispose();
  }

  /// Ten digits out of whatever was typed or pasted, or null.
  ///
  /// Farmers copy their number from contacts or read it off a recharge slip:
  /// "+91 98765 43210", "09876543210", "98765 43210" all mean the same ten
  /// digits. Rejecting those spellings costs a round trip on a bad
  /// connection just to be told what this function already knows.
  String? _normalise(String raw) {
    var digits = raw.replaceAll(RegExp(r'\D'), '');

    // The same forms the server's own NormaliseIndianMobile accepts, and for
    // the same reason: one number is written six ways depending on who typed
    // it into what. A client stricter than the server blocks a farmer for a
    // number that would have worked.
    if (digits.length == 14 && digits.startsWith('0091')) {
      digits = digits.substring(4);
    } else if (digits.length == 13 && digits.startsWith('091')) {
      digits = digits.substring(3);
    } else if (digits.length == 12 && digits.startsWith('91')) {
      digits = digits.substring(2);
    } else if (digits.length == 11 && digits.startsWith('0')) {
      digits = digits.substring(1);
    }
    if (digits.length != 10) return null;
    // Indian mobiles start 6-9. Anything else is a landline or a typo, and
    // the SMS would silently go nowhere.
    if (!RegExp(r'^[6-9]').hasMatch(digits)) return null;
    return digits;
  }

  String get _prettyMobile =>
      '${_mobile.substring(0, 5)} ${_mobile.substring(5)}';

  void _startCooldown() {
    _cooldownTimer?.cancel();
    setState(() => _cooldown = 60);
    _cooldownTimer = Timer.periodic(const Duration(seconds: 1), (t) {
      if (!mounted) {
        t.cancel();
        return;
      }
      setState(() {
        _cooldown -= 1;
        if (_cooldown <= 0) t.cancel();
      });
    });
  }

  Future<void> _sendCode({bool resend = false}) async {
    final mobile = resend ? _mobile : _normalise(_mobileCtrl.text);
    if (mobile == null || mobile.isEmpty) {
      setState(() {
        _errorMr =
            'मोबाईल नंबर १० अंकी हवा आणि ६, ७, ८ किंवा ९ ने सुरू व्हायला हवा.';
        _errorEn = 'The number must be 10 digits, starting with 6, 7, 8 or 9.';
      });
      return;
    }

    setState(() {
      _busy = true;
      _errorMr = null;
      _errorEn = null;
    });

    final result = await widget.api.postJson('/api/v1/auth/farmer/otp', {
      'mobile': mobile,
    });
    if (!mounted) return;

    setState(() {
      _busy = false;
      if (result.offline) {
        _errorMr = _offlineMr;
        _errorEn = _offlineEn;
      } else if (result.ok) {
        _mobile = mobile;
        _step = _Step.code;
        _codeCtrl.clear();
      } else if (result.code == 'OTP_COOLDOWN') {
        // A code went to this number a moment ago and is still good. Sending
        // the farmer back to wait on the number screen -- which is what they
        // got, worded as "could not reach the server" -- hid the code they
        // already had.
        _mobile = mobile;
        _step = _Step.code;
        _errorMr = Language.of(result.message);
        _errorEn = Language.of(result.message);
      } else if (result.code == 'OFFICER_HANDSET') {
        _errorMr = _officerMr;
        _errorEn = _officerEn;
      } else if (result.code == 'SMS_UNAVAILABLE') {
        _errorMr = _smsBusyMr;
        _errorEn = _smsBusyEn;
      } else if (result.code == 'SMS_UNDELIVERABLE') {
        // Not "try again shortly": nothing this farmer does will make it
        // work until somebody fixes the gateway, and pressing a button in a
        // field on a dying battery is a real cost.
        _errorMr = _smsBrokenMr;
        _errorEn = _smsBrokenEn;
      } else if (result.permanent || result.status == 429) {
        // The server's own words: it knows why it refused (blocked number,
        // too many codes) and a generic string here would hide the one fact
        // the farmer can act on.
        _errorMr = Language.of(result.message);
        _errorEn = Language.of(result.message);
      } else {
        _errorMr = _transientMr;
        _errorEn = _transientEn;
      }
    });

    if (result.ok) _startCooldown();
  }

  Future<void> _verify() async {
    final code = _codeCtrl.text.replaceAll(RegExp(r'\D'), '');
    if (code.length != 6) {
      setState(() {
        _errorMr = 'कोड ६ अंकी असतो. SMS मधील पूर्ण कोड टाका.';
        _errorEn = 'The code has 6 digits. Enter the whole code from the SMS.';
      });
      return;
    }

    setState(() {
      _busy = true;
      _errorMr = null;
      _errorEn = null;
    });

    final result = await widget.api.postJson('/api/v1/auth/farmer/verify', {
      'mobile': _mobile,
      'code': code,
    });
    if (!mounted) return;

    if (result.ok) {
      final body = result.body;
      final token = body?['access_token'];
      if (token is String && token.isNotEmpty) {
        // Saved before onSignedIn so the first screen the farmer lands on
        // already has a session to make requests with.
        await widget.session.save(
          accessToken: token,
          accessTtl: Duration(
            seconds: (body?['expires_in'] as num?)?.toInt() ?? 900,
          ),
          refreshToken: body?['refresh_token'] as String?,
          principal: body?['user'] as Map<String, Object?>?,
        );
        if (!mounted) return;
        widget.onSignedIn();
        return;
      }
      // A 200 with no token is a server fault, not the farmer's. Retryable.
    }

    setState(() {
      _busy = false;
      if (result.offline) {
        _errorMr = _offlineMr;
        _errorEn = _offlineEn;
      } else if (result.permanent || result.status == 429) {
        _errorMr = Language.of(result.message);
        _errorEn = Language.of(result.message);
      } else {
        _errorMr = _transientMr;
        _errorEn = _transientEn;
      }
    });
  }

  void _changeNumber() {
    // The cooldown deliberately keeps ticking: stepping back is not a new
    // SMS, and the server's rate limit does not reset either.
    setState(() {
      _step = _Step.mobile;
      _codeCtrl.clear();
      _errorMr = null;
      _errorEn = null;
    });
  }

  @override
  Widget build(BuildContext context) {
    final text = Theme.of(context).textTheme;
    final mobileStep = _step == _Step.mobile;
    return Scaffold(
      body: SingleChildScrollView(
        // The whole screen scrolls, hero included: on a short handset with the
        // keyboard up there is not enough room for both.
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.stretch,
          children: [
            const _Hero(),
            Padding(
              padding: const EdgeInsets.fromLTRB(20, 4, 20, 32),
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.stretch,
                children: [
                  Text(
                    mobileStep
                        ? tr('लॉगिन करा', 'Sign in')
                        : tr('कोड टाका', 'Enter the code'),
                    style: text.headlineSmall,
                  ),
                  const SizedBox(height: Space.xs),
                  Text(
                    mobileStep
                        ? tr(
                            'तुमच्या हातातल्या फोनचा नंबर टाका. त्यावर SMS ने '
                                '६ अंकी कोड येईल. हा नंबर सातबाऱ्यावरचा नसला '
                                'तरी चालेल.',
                            'Use the phone in your hand. A 6-digit code will '
                                'arrive on it by SMS. It does not have to be '
                                'the number in the land records.',
                          )
                        : tr(
                            '+91 $_prettyMobile वर पाठवलेला ६ अंकी कोड टाका.',
                            'Enter the 6-digit code sent to +91 $_prettyMobile.',
                          ),
                    style: text.bodyMedium?.copyWith(color: Palette.inkMuted),
                  ),
                  if (widget.notice != null) ...[
                    const SizedBox(height: Space.lg),
                    Notice(
                      tone: Tone.warning,
                      icon: Icons.info_outline,
                      title: tr(widget.notice!.mr, widget.notice!.en),
                      margin: EdgeInsets.zero,
                    ),
                  ],
                  const SizedBox(height: Space.xl),
                  ...(mobileStep ? _mobileStep() : _codeStep()),
                ],
              ),
            ),
          ],
        ),
      ),
    );
  }

  List<Widget> _mobileStep() {
    return [
      TextField(
        controller: _mobileCtrl,
        keyboardType: TextInputType.phone,
        autofillHints: const [AutofillHints.telephoneNumber],
        // '+', spaces and dashes are let through on purpose: numbers arrive
        // pasted from contacts exactly as stored there, and a field that
        // eats characters looks broken. _normalise sorts it out on submit.
        inputFormatters: [
          FilteringTextInputFormatter.allow(RegExp(r'[0-9+\s-]')),
        ],
        style: const TextStyle(
          fontSize: 18,
          height: 1.4,
          letterSpacing: 0.5,
          color: Palette.ink,
        ),
        decoration: InputDecoration(
          labelText: tr('मोबाईल नंबर', 'Mobile number'),
          hintText: '98765 43210',
          prefixIcon: const Icon(Icons.phone_iphone),
        ),
        onSubmitted: (_) => _busy ? null : _sendCode(),
      ),
      const SizedBox(height: Space.lg),
      ..._errorBox(),
      BigButton(
        label: _busy
            ? tr('पाठवत आहे…', 'Sending…')
            : tr('कोड पाठवा', 'Send code'),
        onPressed: _busy ? null : _sendCode,
      ),
    ];
  }

  List<Widget> _codeStep() {
    return [
      TextField(
        controller: _codeCtrl,
        keyboardType: TextInputType.number,
        autofillHints: const [AutofillHints.oneTimeCode],
        inputFormatters: [
          FilteringTextInputFormatter.digitsOnly,
          LengthLimitingTextInputFormatter(6),
        ],
        textAlign: TextAlign.center,
        // Spaced: the code is often read off a second handset or relayed
        // aloud by whoever in the household received the SMS.
        style: const TextStyle(
          fontSize: 24,
          height: 1.4,
          letterSpacing: 10,
          fontWeight: FontWeight.w500,
          color: Palette.ink,
        ),
        decoration: const InputDecoration(hintText: '••••••'),
        onSubmitted: (_) => _busy ? null : _verify(),
      ),
      const SizedBox(height: Space.lg),
      ..._errorBox(),
      BigButton(
        label: _busy
            ? tr('तपासत आहे…', 'Checking…')
            : tr('लॉगिन करा', 'Sign in'),
        onPressed: _busy ? null : _verify,
      ),
      const SizedBox(height: Space.sm),
      Row(
        children: [
          TextButton(
            onPressed: _busy ? null : _changeNumber,
            child: Text(tr('नंबर बदला', 'Change number')),
          ),
          const Spacer(),
          TextButton(
            onPressed: (_busy || _cooldown > 0)
                ? null
                : () => _sendCode(resend: true),
            child: Text(
              _cooldown > 0
                  ? tr(
                      'पुन्हा पाठवा ($_cooldown से.)',
                      'Resend (${_cooldown}s)',
                    )
                  : tr('कोड पुन्हा पाठवा', 'Resend code'),
            ),
          ),
        ],
      ),
    ];
  }

  List<Widget> _errorBox() {
    final mr = _errorMr;
    final en = _errorEn;
    if (mr == null || en == null) return const [];
    return [
      Notice(
        tone: Tone.danger,
        icon: Icons.error_outline,
        title: tr(mr, Language.of(en)),
        margin: const EdgeInsets.only(bottom: Space.lg),
      ),
    ];
  }
}

/// The mark, over a drawn sunrise.
///
/// # Why this is not the photograph
///
/// The supplied artwork is 600x400 with its type burned in. On a handset that
/// is a 3x upscale of text that was already small, and it looked it. Cropping
/// to the photograph alone still meant shipping a megabyte of raster to a
/// farmer on a metered connection, to decorate the one screen they see before
/// they can do anything.
///
/// A gradient costs nothing to download, is exact on any screen density, and
/// is made of the same greens as everything below it. The mark stays a real
/// image because that is what a mark is.
///
/// # Why a band and not a background
///
/// A form laid over a picture is the version that looks best in a mockup and
/// worst in a field. Every colour in this app is chosen to survive direct
/// Maharashtra sun, and a sunrise behind a text field spends that budget on
/// scenery. Above the form it costs nothing.
class _Hero extends StatelessWidget {
  const _Hero();

  @override
  Widget build(BuildContext context) {
    return SizedBox(
      height: 150,
      width: double.infinity,
      child: DecoratedBox(
        decoration: const BoxDecoration(
          gradient: LinearGradient(
            begin: Alignment.topCenter,
            end: Alignment.bottomCenter,
            // Sky, a warm band at the horizon, then the crop -- and out to
            // the page tint, so the band meets the form without a seam.
            colors: [
              Color(0xFFCFE4F2),
              Color(0xFFE8F0DD),
              Color(0xFFF7EDC9),
              Color(0xFFCBDCA4),
              Palette.canvas,
            ],
            stops: [0.0, 0.34, 0.52, 0.74, 1.0],
          ),
        ),
        child: Center(
          child: Padding(
            padding: const EdgeInsets.fromLTRB(24, 8, 24, 24),
            child: Image.asset(
              'assets/brand/logo-esurvey.png',
              height: 54,
              fit: BoxFit.contain,
              // The screen must still work if the mark is ever dropped from
              // the bundle: a sign-in nobody can reach because a decoration
              // is missing would be an absurd way to lose a claim.
              errorBuilder: (_, _, _) => const SizedBox.shrink(),
            ),
          ),
        ),
      ),
    );
  }
}
