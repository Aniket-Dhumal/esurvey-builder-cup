import 'dart:convert';
import 'dart:io';

import 'package:crypto/crypto.dart';
import 'package:http/http.dart' as http;

/// Certificate pinning, for when the system trust store is not enough.
///
/// # What this defends against
///
/// A handset trusts every certificate authority its vendor shipped, plus any
/// an administrator, an employer, or an attacker with physical access has
/// installed. Any one of them can issue a certificate for this API's hostname,
/// and TLS alone will accept it. On a shared handset in a village, or one that
/// has passed through a repair shop, that is not a theoretical concern.
///
/// Pinning says: it is not enough for the certificate to be trusted, it must
/// be *ours*.
///
/// # Why this pins the key and not the certificate
///
/// A certificate expires, typically every ninety days with an automated
/// issuer. Pinning the certificate means every renewal breaks every installed
/// app until it is updated -- which, for farmers on a village connection who
/// update when they remember to, means the app stops working and they have no
/// idea why in the middle of a claim window.
///
/// The public key survives renewal, provided the key is reused. So this pins
/// the SHA-256 of the SubjectPublicKeyInfo: renewals pass, and only a
/// deliberate key rotation needs a release.
///
/// # Why there must be more than one pin
///
/// A single pin and a lost private key is an application that can never talk
/// to its server again, on every handset, permanently. The backup pin is the
/// key held offline for exactly that day. Refusing to configure fewer than two
/// is deliberate: the failure it prevents is unrecoverable and the temptation
/// to skip it is highest when there is only one key so far.
///
/// # Why it is off by default
///
/// Pinning against a service with no TLS pins nothing, and pinning before
/// certificate rotation is a practised procedure is how an organisation
/// discovers both problems at once, during an incident. So a build pins only
/// when it is given pins:
///
///     flutter build apk --dart-define=ESURVEY_PINS=sha256/AAA…,sha256/BBB…
///
/// Obtain a pin from the deployment itself rather than from a file, so that
/// what is pinned is what is actually served:
///
///     openssl s_client -connect host:443 </dev/null 2>/dev/null ///       | openssl x509 -outform der | openssl dgst -sha256 -binary | base64
///
/// # How it is enforced
///
/// [PinnedClient] checks the certificate once the connection exists and before
/// any of the response reaches the caller. Dart offers no callback for a
/// certificate the system already trusts, which is precisely the case that
/// matters -- an attacker's certificate authority installed on the handset is
/// trusted, and TLS alone accepts it.
///
/// The first attempt at that wrapped HttpClient and delegated through
/// noSuchMethod. It compiled, satisfied the interface, and would have thrown
/// on the first header set: an app unable to make any request, discovered on a
/// handset rather than in a test. It was replaced by writing the request path
/// out in full, and by testing against a real TLS server -- see
/// test/pinning_transport_test.dart, where a *trusted* certificate is refused
/// on the strength of the pin alone.
class Pinning {
  const Pinning._(this.pins);

  /// Reads the pins a build was given.
  ///
  /// An empty or single-pin configuration disables pinning rather than
  /// half-applying it. Half-applied pinning is the worst of both: it can lock
  /// farmers out without meaningfully raising the bar.
  factory Pinning.fromEnvironment([String raw = _fromBuild]) {
    final pins = raw
        .split(',')
        .map((p) => p.trim())
        .where((p) => p.startsWith(_prefix) && p.length > _prefix.length)
        .toSet();

    if (pins.length < 2) return const Pinning._(<String>{});
    return Pinning._(pins);
  }

  static const _fromBuild = String.fromEnvironment('ESURVEY_PINS');
  static const _prefix = 'sha256/';

  /// The accepted SubjectPublicKeyInfo digests, `sha256/` + base64.
  final Set<String> pins;

  /// Whether this build pins at all.
  bool get enabled => pins.length >= 2;

  /// Whether a certificate's public key is one this build accepts.
  ///
  /// Returns true when pinning is off, so an unpinned build is unaffected
  /// rather than refusing everything.
  bool accepts(X509Certificate cert) {
    if (!enabled) return true;
    return pins.contains(pinOf(cert));
  }

  /// The pin for a certificate, in the form the build define uses.
  ///
  /// Computed over the DER bytes. Dart's X509Certificate does not expose the
  /// SubjectPublicKeyInfo on its own, so this hashes the whole certificate --
  /// which means a renewal that reuses the key still changes the pin, and the
  /// rotation advantage described above is not fully realised on this
  /// platform. Two pins therefore matter more here, not less: the backup is
  /// what covers the renewal, and both must be updated in the release before
  /// the certificate they replace expires.
  static String pinOf(X509Certificate cert) =>
      '$_prefix${base64.encode(sha256.convert(cert.der).bytes)}';
}

/// An http client that checks the pin on every response.
///
/// # Why this is written out rather than wrapping HttpClient
///
/// Dart offers no callback for a certificate the system already trusts, so the
/// check has to happen once a connection exists. The obvious route -- wrap
/// HttpClient and hand everything to the inner one -- means delegating every
/// member of a large interface, and a first attempt at that used noSuchMethod
/// and would have thrown on the first header set.
///
/// Building the request here instead is about forty lines and has no gaps: it
/// is the whole path, visible, with the certificate check in the middle of it.
class PinnedClient extends http.BaseClient {
  PinnedClient(this._pinning, {HttpClient? inner})
    : _inner = inner ?? HttpClient();

  final Pinning _pinning;
  final HttpClient _inner;

  @override
  Future<http.StreamedResponse> send(http.BaseRequest request) async {
    final out = await _inner.openUrl(request.method, request.url);

    out.followRedirects = request.followRedirects;
    out.maxRedirects = request.maxRedirects;
    out.persistentConnection = request.persistentConnection;
    request.headers.forEach(out.headers.set);

    final body = await request.finalize().toBytes();
    // Set after the headers, and only when there is one: a content length on a
    // GET makes some servers wait for a body that never comes.
    if (body.isNotEmpty) {
      out.contentLength = body.length;
      out.add(body);
    }

    final response = await out.close();

    // The certificate is only knowable once the connection exists. Checked
    // before a single byte of the body is handed to the caller, so a response
    // from the wrong server is never acted on.
    guard(_pinning, response.certificate);

    final headers = <String, String>{};
    response.headers.forEach((name, values) {
      headers[name] = values.join(',');
    });

    return http.StreamedResponse(
      response,
      response.statusCode,
      contentLength: response.contentLength == -1
          ? null
          : response.contentLength,
      request: request,
      headers: headers,
      isRedirect: response.isRedirect,
      persistentConnection: response.persistentConnection,
      reasonPhrase: response.reasonPhrase,
    );
  }

  @override
  void close() => _inner.close(force: true);
}

/// Applies pinning to a connection's certificate, or throws.
///
/// Kept separate from the HttpClient so the failure is a refusal this app can
/// explain, rather than a socket error a farmer sees as "no internet" while
/// somebody reads their session.
void guard(Pinning pinning, X509Certificate? cert) {
  if (!pinning.enabled) return;
  if (cert == null) {
    throw const TlsException('the server presented no certificate to pin');
  }
  if (!pinning.accepts(cert)) {
    throw TlsException(
      'the server certificate is trusted by this device but is not one this '
      'app was built to accept (${Pinning.pinOf(cert)})',
    );
  }
}
