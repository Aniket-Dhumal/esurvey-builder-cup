import 'package:flutter/material.dart';

/// What a farmer actually wants to ask, and the honest answer.
///
/// # Why this is data and not widgets
///
/// The answers are the part that matters, and the part most likely to drift
/// into something the platform cannot deliver. Kept as plain values they can
/// be asserted against -- that every question is answered in both languages,
/// and that nothing here promises money, a date, or an outcome. A layout test
/// cannot check any of that.
///
/// # Why these questions
///
/// They are the ones asked at a taluka office counter: what happens next,
/// when will somebody come, how much will I get. An app that answers only the
/// questions it finds convenient sends the farmer to the counter anyway,
/// which is the queue this whole platform exists to shorten.
///
/// # What the answers may not do
///
/// Promise an amount, promise a date, or imply this app pays anything. It
/// does not: it records damage, and compensation is decided elsewhere under
/// rules this app has no part in. Saying otherwise would be the single most
/// damaging thing the app could tell somebody who has just lost a crop --
/// they would plan around it, and it would not arrive.
class Question {
  const Question({
    required this.icon,
    required this.mr,
    required this.en,
    required this.answerMr,
    required this.answerEn,
  });

  final IconData icon;

  /// The question, as somebody would ask it.
  final String mr;
  final String en;

  /// The answer, spoken as well as shown.
  final String answerMr;
  final String answerEn;
}

const questions = <Question>[
  Question(
    icon: Icons.route,
    mr: 'नोंद केल्यावर पुढे काय होते?',
    en: 'What happens after I file?',
    answerMr:
        'तुमची नोंद तालुका कार्यालयात जाते. तुम्ही पाठवलेले फोटो दोन '
        'अधिकारी एकमेकांपासून वेगळे तपासतात. त्यासोबत तुमच्या शेताची उपग्रह '
        'छायाचित्रे आणि हवामानाच्या नोंदीही तपासल्या जातात. अंतिम आकडा '
        'अधिकारी ठरवतात.',
    answerEn:
        'Your report goes to the taluka office. Two officers examine the '
        'photographs you sent, independently of each other, alongside satellite '
        'pictures of your field and the weather record. The final figure is '
        'decided by the officers, not by this app.',
  ),
  Question(
    icon: Icons.schedule,
    mr: 'कोणी शेतात येऊन पाहणी करेल का?',
    en: 'Will somebody come and inspect my field?',
    answerMr:
        'सहसा नाही. तुम्ही पाठवलेले फोटो, शेताची हद्द, उपग्रह छायाचित्रे '
        'आणि हवामान नोंदी यांवरून अधिकारी निर्णय घेतात — त्यामुळे तुम्हाला '
        'कोणाची वाट पाहावी लागत नाही आणि निर्णय लवकर होतो. '
        'फोटोंत तुमचे नुकसान दिसत नसेल, तर तसे कारण देऊन आक्षेप नोंदवा; '
        'अशा वेळी प्रत्यक्ष पाहणी होऊ शकते.',
    answerEn:
        'Usually not. Officers decide from the photographs you sent, your '
        'field boundary, satellite pictures and the weather record — so you do '
        'not have to wait for anybody, and the decision comes sooner. If the '
        'photographs cannot show your loss, object on that ground and a visit '
        'can be arranged.',
  ),
  Question(
    icon: Icons.account_balance,
    mr: 'मला किती पैसे मिळतील?',
    en: 'How much money will I get?',
    answerMr:
        'हे ॲप पैसे ठरवत नाही आणि पैसे देतही नाही. हे ॲप फक्त नुकसानाची '
        'नोंद ठेवते. मदत किती मिळेल हे शासनाच्या नियमांनुसार ठरते आणि ती '
        'वेगळ्या मार्गाने दिली जाते.',
    answerEn:
        'This app does not decide any amount and does not pay anything. '
        'It records the damage. What help is given is decided by government '
        'rules, and it reaches you separately.',
  ),
  Question(
    icon: Icons.my_location,
    mr: 'फोटो काढताना जागा का लागते?',
    en: 'Why does it need my location?',
    answerMr:
        'त्यामुळे फोटो तुमच्या शेताशी जोडला जातो. जागेशिवाय हा फोटो '
        'कोणत्या शेताचा आहे हे कार्यालयाला ठरवता येत नाही आणि नोंद कमजोर '
        'राहते.',
    answerEn:
        'So the photograph is tied to your field. Without it the office '
        'cannot tell which field the photograph belongs to, and the report '
        'carries less weight.',
  ),
  Question(
    icon: Icons.cloud_off,
    mr: 'नेटवर्क नसेल तर?',
    en: 'What if there is no network?',
    answerMr:
        'सर्व काही चालते. नोंद आणि फोटो या फोनवर सुरक्षित राहतात. '
        'नेटवर्क आल्यावर ते आपोआप जातात — ॲप बंद असले तरी.',
    answerEn:
        'Everything still works. The report and the photographs stay '
        'safe on this phone, and they go on their own when a signal comes, '
        'even if the app is closed.',
  ),
  Question(
    icon: Icons.straighten,
    mr: 'हद्द चालत मोजणे म्हणजे काय?',
    en: 'What is walking the boundary?',
    answerMr:
        'फोन हातात घेऊन शेताच्या कडेने चालायचे. ॲप कोपरे नोंदवते आणि '
        'क्षेत्र मोजते. एकदा केले की पुरे — पुन्हा करावे लागत नाही.',
    answerEn:
        'You walk along the edge of your field holding the phone. The '
        'app records the corners and works out the area. Once done it stays '
        'done; you do not have to walk it again.',
  ),
  Question(
    icon: Icons.visibility,
    mr: 'माझे फोटो कोण पाहते?',
    en: 'Who sees my photographs?',
    answerMr:
        'तुमची नोंद हाताळणारे अधिकारी. फोटो पुरावा म्हणून नोंदीसोबत '
        'ठेवले जातात.',
    answerEn:
        'The officers handling your report. The photographs are kept '
        'with the report as its evidence.',
  ),
  Question(
    icon: Icons.edit_off,
    mr: 'नोंद पाठवल्यावर बदलता येते का?',
    en: 'Can I change my report after sending it?',
    answerMr:
        'पाठवल्यावर या ॲपमधून बदलता येत नाही. काही चुकले असेल तर '
        'आणखी फोटो पाठवा, किंवा निर्णय झाल्यावर आक्षेप नोंदवा.',
    answerEn:
        'Not from this app once it has been sent. If something is wrong, '
        'send more photographs, or object once the decision is made.',
  ),
  Question(
    icon: Icons.help_outline,
    mr: 'नोंद नाकारली तर काय?',
    en: 'What if my report is refused?',
    answerMr:
        'कारण ॲपमध्ये दिसेल. तुम्ही तालुका कार्यालयात जाऊन विचारू '
        'शकता — नोंद नाकारली म्हणजे विषय संपला असे नाही.',
    answerEn:
        'The reason is shown in the app. You can go to the taluka '
        'office and ask about it — a refusal is not the end of the matter.',
  ),
  Question(
    icon: Icons.phone_android,
    mr: 'माझी माहिती फोनवर कुठे राहते?',
    en: 'Where does the app keep my details?',
    answerMr:
        'पाठवेपर्यंत सर्व काही या फोनवरच राहते. फोन दुसऱ्याचा असेल तर '
        'बाहेर पडा — त्याने तुमची माहिती आणि फोटो फोनवरून निघून जातात.',
    answerEn:
        'Everything stays on this phone until it is sent. If the phone '
        'belongs to somebody else, sign out — that removes your details and '
        'your photographs from it.',
  ),
];
