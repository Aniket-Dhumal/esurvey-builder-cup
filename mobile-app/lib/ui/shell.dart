import 'package:flutter/material.dart';

import '../core/language.dart';
import '../core/voice.dart';
import 'theme.dart';

/// The four places a farmer can be.
///
/// # Why a bottom bar and not a stack
///
/// Until now every screen was pushed onto a stack from Home, so the only way
/// back was a back arrow at the top of the screen -- out of reach on a large
/// handset held one-handed, and invisible as a concept to anyone who has not
/// used many apps. A farmer who tapped into "my reports" and wanted the events
/// list had no way to see that the events list was still there.
///
/// A bottom bar makes the whole app visible at once and puts every target
/// under a thumb. It also removes the state problem the stack created: a
/// half-filled report survived nothing, because leaving the screen destroyed
/// it.
enum Tab {
  home(
    icon: Icons.home_outlined,
    active: Icons.home,
    mr: 'मुख्यपृष्ठ',
    en: 'Home',
  ),
  newReport(
    icon: Icons.add_circle_outline,
    active: Icons.add_circle,
    mr: 'नवीन नोंद',
    en: 'New report',
  ),
  myReports(
    icon: Icons.description_outlined,
    active: Icons.description,
    mr: 'माझ्या नोंदी',
    en: 'My reports',
  ),
  help(icon: Icons.help_outline, active: Icons.help, mr: 'मदत', en: 'Help');

  const Tab({
    required this.icon,
    required this.active,
    required this.mr,
    required this.en,
  });

  final IconData icon;
  final IconData active;
  final String mr;
  final String en;
}

/// The app's frame: four tabs, each keeping its own state.
class Shell extends StatefulWidget {
  const Shell({
    super.key,
    required this.voice,
    required this.home,
    required this.newReport,
    required this.myReports,
    required this.help,
  });

  final Voice voice;

  final Widget home;
  final Widget newReport;
  final Widget myReports;
  final Widget help;

  @override
  State<Shell> createState() => ShellState();
}

class ShellState extends State<Shell> {
  Tab _tab = Tab.home;

  /// Lets a screen move the farmer to another tab: filing a report from the
  /// events list should land on the form, not leave them to find it.
  void go(Tab tab) => setState(() => _tab = tab);

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      // IndexedStack rather than swapping widgets: a half-filled report has to
      // survive a glance at Help. Rebuilding the form would throw away
      // everything typed, in a field, which is how a farmer loses a claim to
      // an app rather than to a hailstorm.
      body: IndexedStack(
        index: Tab.values.indexOf(_tab),
        children: [
          widget.home,
          widget.newReport,
          widget.myReports,
          widget.help,
        ],
      ),
      bottomNavigationBar: DecoratedBox(
        decoration: const BoxDecoration(
          border: Border(top: BorderSide(color: Palette.outlineSoft)),
        ),
        child: NavigationBar(
          selectedIndex: Tab.values.indexOf(_tab),
          onDestinationSelected: (i) => setState(() => _tab = Tab.values[i]),
          labelBehavior: NavigationDestinationLabelBehavior.alwaysShow,
          destinations: [
            for (final tab in Tab.values)
              NavigationDestination(
                icon: Icon(tab.icon),
                selectedIcon: Icon(tab.active),
                label: tr(tab.mr, tab.en),
              ),
          ],
        ),
      ),
    );
  }
}
