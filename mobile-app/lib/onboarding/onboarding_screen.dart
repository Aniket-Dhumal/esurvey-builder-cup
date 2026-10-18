import 'package:flutter/material.dart';

import '../core/db.dart';
import '../core/language.dart';
import '../core/voice.dart';
import '../ui/theme.dart';

/// The first-run walkthrough: three short pages, then sign-in.
///
/// Silent. The voice is kept for the field, where the farmer's eyes are on the
/// crop and not the screen; a tour that talks at somebody sitting at home
/// teaches them to turn the voice off before it is ever useful.
class OnboardingScreen extends StatefulWidget {
  const OnboardingScreen({
    super.key,
    required this.voice,
    required this.store,
    required this.onDone,
  });

  final Voice voice;
  final Store store;
  final VoidCallback onDone;

  @override
  State<OnboardingScreen> createState() => _OnboardingScreenState();
}

class _Page {
  const _Page({
    required this.icon,
    required this.titleMr,
    required this.titleEn,
    required this.bodyMr,
    required this.bodyEn,
  });

  final IconData icon;
  final String titleMr;
  final String titleEn;
  final String bodyMr;
  final String bodyEn;
}

class _OnboardingScreenState extends State<OnboardingScreen> {
  static const _pages = [
    _Page(
      icon: Icons.agriculture_outlined,
      titleMr: 'पिकाचे नुकसान कळवा',
      titleEn: 'Report crop loss',
      bodyMr:
          'आपत्तीमुळे पिकाचे नुकसान झाले तर या ॲपवरून थेट कार्यालयाला '
          'कळवा.',
      bodyEn:
          'When a calamity damages your crop, report it to the office '
          'straight from this app.',
    ),
    _Page(
      icon: Icons.photo_camera_outlined,
      titleMr: 'शेतातून पुरावा द्या',
      titleEn: 'Evidence from your field',
      bodyMr:
          'ॲप तुम्हाला शेतातील ठिकाणी घेऊन जाईल आणि फोटो व व्हिडिओ घेण्यास '
          'मदत करेल.',
      bodyEn:
          'The app guides you to points in your field and helps you take '
          'photos and video.',
    ),
    _Page(
      icon: Icons.cloud_off_outlined,
      titleMr: 'नेटवर्कशिवायही चालते',
      titleEn: 'Works without network',
      bodyMr:
          'सर्व काही फोनवर जतन होते आणि नेटवर्क आल्यावर आपोआप पाठवले '
          'जाते.',
      bodyEn:
          'Everything is saved on the phone and sent by itself when there is '
          'a signal.',
    ),
  ];

  final _controller = PageController();
  int _page = 0;
  bool _finishing = false;

  @override
  void dispose() {
    _controller.dispose();
    super.dispose();
  }

  Future<void> _finish() async {
    // The last button and Skip both land here, and a double tap must not
    // fire onDone twice.
    if (_finishing) return;
    _finishing = true;
    await widget.store.put('onboarding_seen', true);
    widget.onDone();
  }

  void _next() {
    if (_page == _pages.length - 1) {
      _finish();
      return;
    }
    _controller.nextPage(
      duration: const Duration(milliseconds: 250),
      curve: Curves.easeOut,
    );
  }

  @override
  Widget build(BuildContext context) {
    final last = _page == _pages.length - 1;

    return Scaffold(
      body: SafeArea(
        child: Column(
          children: [
            Padding(
              padding: const EdgeInsets.fromLTRB(20, 8, 8, 0),
              child: Row(
                children: [
                  Image.asset(
                    'assets/brand/logo-esurvey.png',
                    height: 32,
                    errorBuilder: (_, _, _) => const SizedBox.shrink(),
                  ),
                  const Spacer(),
                  // A farmer who has used this before must not be trapped in
                  // a walkthrough.
                  if (!last)
                    TextButton(
                      onPressed: _finish,
                      child: Text(tr('वगळा', 'Skip')),
                    ),
                ],
              ),
            ),
            Expanded(
              child: PageView.builder(
                controller: _controller,
                itemCount: _pages.length,
                onPageChanged: (i) => setState(() => _page = i),
                itemBuilder: (context, i) => _WalkPage(page: _pages[i]),
              ),
            ),
            _Dots(count: _pages.length, current: _page),
            Padding(
              padding: const EdgeInsets.fromLTRB(20, 24, 20, 16),
              child: BigButton(
                label: last
                    ? tr('सुरू करा', 'Get started')
                    : tr('पुढे', 'Next'),
                onPressed: _next,
              ),
            ),
          ],
        ),
      ),
    );
  }
}

class _WalkPage extends StatelessWidget {
  const _WalkPage({required this.page});

  final _Page page;

  @override
  Widget build(BuildContext context) {
    final text = Theme.of(context).textTheme;
    return Padding(
      padding: const EdgeInsets.symmetric(horizontal: 32),
      child: Column(
        mainAxisAlignment: MainAxisAlignment.center,
        children: [
          Container(
            width: 112,
            height: 112,
            decoration: const BoxDecoration(
              color: Palette.primarySoft,
              shape: BoxShape.circle,
            ),
            child: Icon(page.icon, size: 52, color: Palette.primary),
          ),
          const SizedBox(height: 36),
          Text(
            tr(page.titleMr, page.titleEn),
            textAlign: TextAlign.center,
            style: text.headlineSmall,
          ),
          const SizedBox(height: Space.md),
          Text(
            tr(page.bodyMr, page.bodyEn),
            textAlign: TextAlign.center,
            style: text.bodyLarge?.copyWith(color: Palette.inkMuted),
          ),
        ],
      ),
    );
  }
}

/// Which of the three pages this is.
class _Dots extends StatelessWidget {
  const _Dots({required this.count, required this.current});

  final int count;
  final int current;

  @override
  Widget build(BuildContext context) {
    return Row(
      mainAxisAlignment: MainAxisAlignment.center,
      children: [
        for (var i = 0; i < count; i++)
          AnimatedContainer(
            duration: const Duration(milliseconds: 200),
            // The current dot is wider, not merely darker: colour difference
            // is the first thing sunlight erases.
            width: i == current ? 24 : 8,
            height: 8,
            margin: const EdgeInsets.symmetric(horizontal: 4),
            decoration: BoxDecoration(
              color: i == current ? Palette.primary : Palette.outline,
              borderRadius: BorderRadius.circular(999),
            ),
          ),
      ],
    );
  }
}
