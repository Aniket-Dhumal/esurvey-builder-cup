import 'dart:convert';

import 'package:esurvey/auth/sign_in_screen.dart';
import 'package:esurvey/core/api.dart';
import 'package:esurvey/core/session.dart';
import 'package:esurvey/core/voice.dart';
import 'package:esurvey/ui/theme.dart';
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';

class _FakeStore implements SecureStore {
  final values = <String, String>{};

  @override
  Future<String?> read(String key) async => values[key];

  @override
  Future<void> write(String key, String value) async => values[key] = value;

  @override
  Future<void> delete(String key) async => values.remove(key);

  @override
  Future<void> deleteAll() async => values.clear();
}

Future<void> _open(WidgetTester tester, http.Client client) async {
  final session = Session(storage: _FakeStore());
  await tester.pumpWidget(
    MaterialApp(
      theme: esurveyTheme(),
      home: SignInScreen(
        api: Api(session: session, client: client),
        session: session,
        voice: Voice(),
        onSignedIn: () {},
      ),
    ),
  );
}

void main() {
  testWidgets('a code sent a moment ago is not "could not reach the server"', (
    tester,
  ) async {
    // The farmer pressed send twice. The office answered 429 OTP_COOLDOWN --
    // it had just sent a code, which is still good -- and the screen said the
    // server could not be reached and kept them on the number step, hiding
    // the code already on its way.
    final client = MockClient(
      (_) async => http.Response(
        jsonEncode({
          'error': {
            'code': 'OTP_COOLDOWN',
            'message':
                'a code was sent recently; wait before requesting another',
          },
        }),
        429,
        headers: {'content-type': 'application/json'},
      ),
    );
    await _open(tester, client);

    await tester.enterText(find.byType(TextField), '9527464552');
    await tester.tap(find.byType(FilledButton));
    await tester.pumpAndSettle();

    expect(find.textContaining('सर्व्हरशी संपर्क'), findsNothing);
    expect(find.text('कोड टाका'), findsOneWidget);
    expect(find.textContaining('कोड आत्ताच पाठवला'), findsOneWidget);
  });

  testWidgets('a number with no network says so', (tester) async {
    final client = MockClient((_) async => throw http.ClientException('down'));
    await _open(tester, client);

    await tester.enterText(find.byType(TextField), '9527464552');
    await tester.tap(find.byType(FilledButton));
    await tester.pumpAndSettle();

    expect(find.textContaining('नेटवर्क नाही'), findsOneWidget);
    expect(find.text('लॉगिन करा'), findsWidgets);
  });
}
