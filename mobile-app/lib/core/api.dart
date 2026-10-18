import 'dart:async';
import 'dart:convert';
import 'dart:io';

import 'package:flutter/foundation.dart';
import 'package:http/http.dart' as http;

import 'config.dart';
import 'pinning.dart';
import 'session.dart';

import 'messages.dart';

/// Talking to the platform.
///
/// # The single most dangerous thing in this file
///
/// The server keeps refresh tokens in families and detects reuse: presenting
/// the same refresh token twice is treated as a stolen token and ends every
/// session in the family. That is the right behaviour on the server.
///
/// It means the client must never refresh twice at once. A queue draining
/// after signal returns fires a dozen requests together; if each of them sees
/// a 401 and refreshes, eleven of those present a token that has already been
/// spent, the server reads it as theft, and the farmer is signed out of a
/// handset holding the only copy of their claim.
///
/// So a refresh is single-flight: the first request to find an expired token
/// performs it and every other waits on the same future.
///
/// # What counts as a failure
///
/// A network error is not a rejection. It is the ordinary state of a handset
/// in a field and the caller should keep the work queued, so it comes back as
/// [Offline] rather than as an exception that reads like something went
/// wrong. A 4xx is the server declining and no number of retries changes it.
class Api {
  /// Builds a client, pinned when the build supplied pins.
  ///
  /// An injected client is used as given -- that is how the tests reach this
  /// class -- so pinning applies to the client this constructor makes, which
  /// is the one every handset uses.
  ///
  /// An unpinned build gets the ordinary client and the system trust store is
  /// the only check, which is where this app has always been. A pinned build
  /// additionally requires the server's certificate to be one it was built to
  /// accept -- the thing a device carrying somebody else's certificate
  /// authority otherwise defeats.
  Api({required this.session, http.Client? client, this.onSignedOut})
    : _client = client ?? _build();

  static http.Client _build() {
    final pinning = Pinning.fromEnvironment();
    return pinning.enabled ? PinnedClient(pinning) : http.Client();
  }

  final Session session;
  final http.Client _client;

  /// Called when the refresh token is gone or refused.
  ///
  /// The session has lapsed, not ended: the farmer must sign in again before
  /// anything more can be sent, but who they are and everything they queued
  /// stays on the phone. Nothing is deleted from here.
  final void Function()? onSignedOut;

  /// The in-flight refresh, if one is running. Every other caller waits on
  /// this rather than starting a second.
  Future<bool>? _refreshing;

  void close() => _client.close();

  // --- the two failures a caller cares about -------------------------------

  /// [farmerGrant] proves this account may read one farmer's record, for the
  /// farmers whose AgriStack number is not the one that signed in. The handset
  /// holds it beside the farmer; see Store.linkedFarmers.
  Future<Result> get(String path, {String? farmerGrant}) => _send(() async {
    final uri = Uri.parse('${Config.apiBase}$path');
    return _client
        .get(uri, headers: _headers(farmerGrant: farmerGrant))
        .timeout(Config.requestTimeout);
  });

  /// A JSON write.
  ///
  /// [idempotencyKey] is the queued item's own key, chosen when it was queued
  /// and never changed. Sent on every attempt, so a request whose reply was
  /// lost is recognised by the server as the same request and answered with
  /// what it already did, rather than done a second time.
  Future<Result> postJson(
    String path,
    Object body, {
    String? idempotencyKey,
    String? farmerGrant,
  }) => _send(() async {
    final uri = Uri.parse('${Config.apiBase}$path');
    return _client
        .post(
          uri,
          headers: {
            ..._headers(
              idempotencyKey: idempotencyKey,
              farmerGrant: farmerGrant,
            ),
            'Content-Type': 'application/json',
          },
          body: jsonEncode(body),
        )
        .timeout(Config.requestTimeout);
  });

  /// A multipart upload, for photographs.
  Future<Result> postMultipart(
    String path,
    Map<String, String> fields,
    Future<http.MultipartFile> Function() file, {
    String? idempotencyKey,
    String? farmerGrant,
  }) => _send(() async {
    final uri = Uri.parse('${Config.apiBase}$path');
    // The file is built per attempt. A MultipartFile is a stream that can be
    // read once, so the retry after a refreshed token would otherwise send an
    // empty body.
    final req = http.MultipartRequest('POST', uri)
      ..headers.addAll(
        _headers(idempotencyKey: idempotencyKey, farmerGrant: farmerGrant),
      )
      ..fields.addAll(fields)
      ..files.add(await file());
    final streamed = await _client.send(req).timeout(Config.uploadTimeout);
    return http.Response.fromStream(streamed);
  });

  /// One chunk of a resumable upload, as raw bytes.
  ///
  /// Raw rather than multipart or base64-in-JSON. The body is already opaque
  /// bytes; base64 would add a third to every chunk, on the connection least
  /// able to afford it, for no gain at either end.
  ///
  /// Given the upload timeout rather than the request one: a two-megabyte
  /// chunk over 2G takes minutes, and treating that as a timeout would abandon
  /// an upload that was working.
  Future<Result> putBytes(String path, List<int> body, {String? farmerGrant}) =>
      _send(() async {
        final uri = Uri.parse('${Config.apiBase}$path');
        final req = http.Request('PUT', uri)
          ..headers.addAll(_headers(farmerGrant: farmerGrant))
          ..headers['Content-Type'] = 'application/octet-stream'
          ..bodyBytes = body;
        final streamed = await _client.send(req).timeout(Config.uploadTimeout);
        return http.Response.fromStream(streamed);
      });

  Map<String, String> _headers({String? idempotencyKey, String? farmerGrant}) {
    final token = session.accessToken;
    return {
      'Accept': 'application/json',
      'X-App-Version': Config.appVersion,
      if (token != null && token.isNotEmpty) 'Authorization': 'Bearer $token',
      if (idempotencyKey != null && idempotencyKey.isNotEmpty)
        'Idempotency-Key': idempotencyKey,
      // Not a credential of its own: it says which of this account's farmers
      // is being asked about, and the server checks it was issued to this
      // account. See the backend's auth.FarmerGranted.
      if (farmerGrant != null && farmerGrant.isNotEmpty)
        'X-Farmer-Grant': farmerGrant,
    };
  }

  /// Send, refreshing once if the token has expired or is refused.
  Future<Result> _send(Future<http.Response> Function() attempt) async {
    // Refresh before sending when the token is known to be spent, rather than
    // spending a request to be told so. On a rural connection that round trip
    // is seconds.
    if (session.signedIn && !session.accessUsable) {
      if (!await _refresh()) return const Result.offline();
    }

    http.Response res;
    try {
      res = await attempt();
    } on SocketException {
      return const Result.offline();
    } on TimeoutException {
      return const Result.offline();
    } on http.ClientException {
      return const Result.offline();
    }

    if (res.statusCode != 401) return Result.of(res);

    // A 401 with nothing to refresh with is simply a 401. Reporting it as
    // offline would tell the caller to keep the work and try again, when what
    // is actually needed is for somebody to sign in.
    if (!session.signedIn) return Result.of(res);

    // The server refused this token. One refresh, one retry -- a loop here
    // would hammer a server that has already said no.
    if (!await _refresh()) return const Result.offline();

    try {
      res = await attempt();
    } on SocketException {
      return const Result.offline();
    } on TimeoutException {
      return const Result.offline();
    } on http.ClientException {
      return const Result.offline();
    }
    return Result.of(res);
  }

  /// Exchange the refresh token, once, however many callers are waiting.
  Future<bool> _refresh() {
    final running = _refreshing;
    if (running != null) return running;

    final future = _doRefresh();
    _refreshing = future;
    // Cleared however it ends, so a failed refresh does not wedge every
    // later request on a future that will never succeed again.
    future.whenComplete(() => _refreshing = null);
    return future;
  }

  Future<bool> _doRefresh() async {
    // Another process on this phone may have refreshed already. Its token is
    // in the keystore; ours may be the one it spent.
    final held = session.refreshToken;
    await session.reload();
    if (session.refreshToken != held && session.accessUsable) return true;

    final token = session.refreshToken;
    if (token == null || token.isEmpty) {
      _signedOut();
      return false;
    }

    http.Response res;
    try {
      res = await _client
          .post(
            Uri.parse('${Config.apiBase}/api/v1/auth/refresh'),
            headers: {'Content-Type': 'application/json'},
            body: jsonEncode({'refresh_token': token}),
          )
          .timeout(Config.requestTimeout);
    } catch (e) {
      // Could not ask. Not a logout: the token may be perfectly good and the
      // handset simply has no signal, which is where this app lives.
      debugPrint('refresh unreachable: $e');
      return false;
    }

    if (res.statusCode == 200) {
      final body = jsonDecode(res.body) as Map<String, Object?>;
      await session.save(
        accessToken: body['access_token'] as String,
        accessTtl: Duration(
          seconds: (body['expires_in'] as num?)?.toInt() ?? 900,
        ),
        refreshToken: body['refresh_token'] as String?,
        principal: body['user'] as Map<String, Object?>?,
      );
      return true;
    }

    if (res.statusCode == 401 || res.statusCode == 403) {
      // The refresh token is spent, revoked, or was replayed. This is the
      // only thing that signs a farmer out.
      _signedOut();
      return false;
    }

    // A 5xx is the server having a bad day. The token is probably fine and
    // the farmer stays signed in; the queue will try again.
    debugPrint('refresh failed: ${res.statusCode}');
    return false;
  }

  void _signedOut() {
    unawaited(session.lapse());
    onSignedOut?.call();
  }
}

/// What a request came back as.
///
/// Three outcomes rather than an exception, because two of them are ordinary:
/// a handset in a field is offline most of the time, and the caller's correct
/// response is to leave the work queued rather than to treat it as an error.
class Result {
  const Result._(
    this.status,
    this.body,
    this.offline, {
    this.retryAfter,
    this.replayed = false,
    this.document,
    this.byteCount = 0,
    this.headers = const {},
  });

  const Result.offline() : this._(0, null, true);

  factory Result.of(http.Response res) {
    Map<String, Object?>? decoded;
    if (res.body.isNotEmpty) {
      try {
        final parsed = jsonDecode(res.body);
        if (parsed is Map<String, Object?>) decoded = parsed;
      } catch (_) {
        // A gateway returning HTML. Not worth failing over; the status says
        // enough and the body would only fill a log with markup.
      }
    }
    final type = res.headers['content-type'] ?? '';
    return Result._(
      res.statusCode,
      decoded,
      false,
      retryAfter: parseRetryAfter(res.headers['retry-after']),
      replayed: res.headers['idempotency-replayed'] == 'true',
      // A page the office rendered for the farmer to keep. Only on success:
      // a gateway's HTML error page is not a document.
      document:
          res.statusCode >= 200 &&
              res.statusCode < 300 &&
              type.startsWith('text/html')
          ? res.body
          : null,
      // How much came back, and what the headers said.
      //
      // Both for measuring the link rather than for reading a reply: the network
      // probe needs the size it actually received and the thresholds the office
      // stated, and neither is in the decoded body. Kept as a count rather than
      // the bytes themselves -- holding a copy of every response body to measure
      // one of them would be a memory cost paid on every request.
      byteCount: res.bodyBytes.length,
      headers: res.headers,
    );
  }

  /// How long the server asked this phone to wait, when it said.
  final Duration? retryAfter;

  /// The server had already done this and answered with what it did then.
  final bool replayed;

  /// How many bytes the body was, for measuring throughput.
  final int byteCount;

  /// The response headers, lower-cased by the client.
  final Map<String, String> headers;

  /// A rendered document the office sent, such as an assessment report.
  final String? document;

  /// 0 when nothing was reached.
  final int status;
  final Map<String, Object?>? body;

  /// Nothing was reached. Keep the work.
  final bool offline;

  bool get ok => status >= 200 && status < 300;

  /// The server declined and will decline again. A caller should stop
  /// retrying and show the reason.
  bool get permanent => status >= 400 && status < 500 && status != 429;

  /// The API's stable error code, when it sent one.
  String? get code {
    final err = body?['error'];
    if (err is Map && err['code'] is String) return err['code'] as String;
    return null;
  }

  /// What to show the farmer.
  ///
  /// Not the server's words. Those are written for a program — `farmer_id is
  /// required`, `the access token has expired; refresh it` — and they are in
  /// English only, which is the wrong language for most of the people holding
  /// this phone. The code is stable, so the sentence is looked up from it and
  /// written here, in both languages. The server's wording stays in [detail]
  /// for the log.
  String get message =>
      offline ? messageForStatus(0) : messageForCode(code, status);

  /// What the server actually said. For debugPrint, never for the screen.
  String? get detail {
    final err = body?['error'];
    if (err is Map && err['message'] is String) return err['message'] as String;
    if (body?['message'] is String) return body!['message'] as String;
    return null;
  }
}

/// Reads a Retry-After header: seconds, or an HTTP date.
///
/// Anything unreadable is ignored rather than guessed at; the retry policy's
/// own wait applies instead. Bounded to a day, so a misconfigured proxy cannot
/// park a farmer's queue for a month with one header.
Duration? parseRetryAfter(String? value, {DateTime? now}) {
  if (value == null) return null;
  final v = value.trim();
  if (v.isEmpty) return null;
  final seconds = int.tryParse(v);
  Duration? d;
  if (seconds != null) {
    d = Duration(seconds: seconds);
  } else {
    try {
      final at = HttpDate.parse(v);
      d = at.difference(now ?? DateTime.now().toUtc());
    } catch (_) {
      return null;
    }
  }
  if (d.isNegative) return Duration.zero;
  const ceiling = Duration(days: 1);
  return d > ceiling ? ceiling : d;
}
