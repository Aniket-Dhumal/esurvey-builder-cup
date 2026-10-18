@TestOn('vm')
library;

import 'dart:convert';
import 'dart:io';

import 'package:esurvey/core/pinning.dart';
import 'package:flutter_test/flutter_test.dart';

/// Pinning, against a real TLS server.
///
/// The policy tests next door check what a build accepts. These check the part
/// that actually protects anybody: a live HTTPS connection, a certificate the
/// client trusts, and whether the request goes through or is refused on the
/// strength of the pin alone.
///
/// It matters that this is a real server rather than a mock. The first attempt
/// at pinning in this app compiled, satisfied the HttpClient interface, and
/// would have thrown on the first header set. No mock would have caught that,
/// because the bug was in exactly the plumbing a mock replaces.

/// A context that trusts one certificate and nothing else.
///
/// Trust is granted deliberately so that a refusal below is unambiguously the
/// pin's doing. A test where TLS itself rejects the certificate would pass
/// while proving nothing about pinning.
SecurityContext trusting(String certPath) =>
    SecurityContext(withTrustedRoots: false)..setTrustedCertificates(certPath);

Future<HttpServer> serveTLS(String certPath, String keyPath) async {
  final ctx = SecurityContext()
    ..useCertificateChain(certPath)
    ..usePrivateKey(keyPath);

  final server = await HttpServer.bindSecure(
    InternetAddress.loopbackIPv4,
    0,
    ctx,
  );
  server.listen((req) async {
    await req.drain<void>();
    req.response
      ..statusCode = 200
      ..headers.contentType = ContentType.json
      ..write(jsonEncode({'ok': true, 'method': req.method}));
    await req.response.close();
  });
  return server;
}

/// The pin of what a server actually presents.
///
/// Taken from a live connection rather than parsed out of the PEM: Dart
/// exposes no PEM parser, and more importantly this is the certificate the
/// client will really see, computed by the same code the client uses.
Future<String> pinOfServer(HttpServer server, String trustedCert) async {
  final socket = await SecureSocket.connect(
    server.address.address,
    server.port,
    context: trusting(trustedCert),
  );
  final cert = socket.peerCertificate;
  await socket.close();
  if (cert == null) {
    throw StateError('the test server presented no certificate');
  }
  return Pinning.pinOf(cert);
}

void main() {
  const fixtures = 'test/fixtures';
  const cert = '$fixtures/pin_test_cert.pem';
  const key = '$fixtures/pin_test_key.pem';
  const otherCert = '$fixtures/pin_other_cert.pem';
  const otherKey = '$fixtures/pin_other_key.pem';

  late HttpServer server;
  late HttpServer other;
  late Uri url;
  late String livePin;
  late String otherPin;

  setUpAll(() async {
    if (!File(cert).existsSync() || !File(otherCert).existsSync()) {
      throw StateError(
        'test certificates are missing from $fixtures; regenerate with '
        'openssl req -x509 -newkey rsa:2048 -nodes -days 3650',
      );
    }

    server = await serveTLS(cert, key);
    url = Uri.parse('https://127.0.0.1:${server.port}/healthz');
    livePin = await pinOfServer(server, cert);

    // A second server only so that the "wrong" pin is a real certificate's
    // pin rather than an invented string.
    other = await serveTLS(otherCert, otherKey);
    otherPin = await pinOfServer(other, otherCert);
  });

  tearDownAll(() async {
    await server.close(force: true);
    await other.close(force: true);
  });

  PinnedClient clientWith(String pins) => PinnedClient(
    Pinning.fromEnvironment(pins),
    inner: HttpClient(context: trusting(cert)),
  );

  test('the two fixtures are genuinely different certificates', () {
    // Otherwise every test below passes for the wrong reason.
    expect(livePin, isNot(otherPin));
    expect(livePin, startsWith('sha256/'));
  });

  test('a pinned client reaches a server whose pin it holds', () async {
    final client = clientWith('$livePin,$otherPin');
    addTearDown(client.close);

    final res = await client.get(url);

    expect(res.statusCode, 200);
    expect(jsonDecode(res.body)['ok'], isTrue);
  });

  test('a pinned client refuses a trusted server it does not pin', () async {
    // The case pinning exists for, and the reason it is worth having at all.
    //
    // The certificate is trusted — this client is configured to trust it, the
    // way a handset trusts a certificate authority somebody installed — and
    // the request is refused anyway, purely because the pin does not match.
    // That is the difference between "TLS says this is fine" and "this is
    // ours".
    const unrelated = 'sha256/AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=';
    final client = clientWith('$otherPin,$unrelated');
    addTearDown(client.close);

    await expectLater(client.get(url), throwsA(isA<TlsException>()));
  });

  test('an unpinned client is unaffected', () async {
    // An unpinned build must behave exactly as it did before pinning existed.
    final client = clientWith('');
    addTearDown(client.close);

    final res = await client.get(url);
    expect(res.statusCode, 200);
  });

  test('a POST with a body and headers goes through', () async {
    // The failure that killed the first attempt: request plumbing that
    // compiled and then threw on the first header set. This sends a real one.
    final client = clientWith('$livePin,$otherPin');
    addTearDown(client.close);

    final res = await client.post(
      url,
      headers: {
        'Content-Type': 'application/json',
        'Authorization': 'Bearer a-token',
      },
      body: jsonEncode({'mobile': '+919527464552'}),
    );

    expect(res.statusCode, 200);
    expect(jsonDecode(res.body)['method'], 'POST');
  });

  test('response headers and status reach the caller', () async {
    // StreamedResponse is assembled by hand in PinnedClient, so the fields it
    // fills in are worth checking rather than assuming.
    final client = clientWith('$livePin,$otherPin');
    addTearDown(client.close);

    final res = await client.get(url);
    expect(res.headers['content-type'], contains('application/json'));
    expect(res.statusCode, 200);
  });

  test('several requests reuse one client', () async {
    // A pinned client is built once and used for the life of the app, so it
    // has to survive more than one request — including after a refusal.
    final client = clientWith('$livePin,$otherPin');
    addTearDown(client.close);

    for (var i = 0; i < 3; i++) {
      expect((await client.get(url)).statusCode, 200);
    }
  });
}
