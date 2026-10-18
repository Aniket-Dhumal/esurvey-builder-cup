import 'dart:convert';

import 'package:flutter_secure_storage/flutter_secure_storage.dart';

/// Who is signed in, and staying signed in with no signal.
///
/// # The failure this exists to prevent
///
/// Access tokens last fifteen minutes. A farmer standing in a field with a
/// dead signal, halfway through photographing their damage, whose token
/// expires, must not be shown a sign-in screen -- their photographs are on
/// the phone, unsent, and a sign-in screen asks for an OTP that needs the
/// network they do not have. They would lose the session, the queue would
/// stop, and the only copy of their claim would sit on a handset behind a
/// login they cannot pass.
///
/// So an expired access token is not a signed-out state. It is a token that
/// needs refreshing when there is a signal, and until then the farmer keeps
/// working. The queue keeps accepting. Nothing is thrown away.
///
/// Signed out means one thing only: the refresh token is gone or the server
/// refused it. Everything else is a network problem wearing a costume.
///
/// # Why the tokens are in the keystore
///
/// flutter_secure_storage puts them behind the Android keystore, so a
/// refresh token is not readable from a backup or by another app on a rooted
/// handset. This is the one thing a native app gives that a browser cannot,
/// and it is the reason the refresh token can be long-lived enough for a
/// farmer to file one claim a year without signing in again.
/// The four operations a session needs from secure storage.
///
/// A narrow interface rather than the plugin's own class: the plugin's method
/// signatures carry options for five platforms and change between major
/// versions, and a session has no business depending on any of that. It also
/// makes the offline-survival reasoning testable without a platform channel,
/// which is the part worth testing.
abstract class SecureStore {
  Future<String?> read(String key);
  Future<void> write(String key, String value);
  Future<void> delete(String key);
  Future<void> deleteAll();
}

/// The keystore-backed implementation.
///
/// The default Android options are already what is wanted: AES-GCM for the
/// data with RSA-OAEP key wrapping, on API 23 and above. Nothing needs
/// turning on.
class KeystoreStore implements SecureStore {
  const KeystoreStore([this._storage = const FlutterSecureStorage()]);

  final FlutterSecureStorage _storage;

  @override
  Future<String?> read(String key) => _storage.read(key: key);

  @override
  Future<void> write(String key, String value) =>
      _storage.write(key: key, value: value);

  @override
  Future<void> delete(String key) => _storage.delete(key: key);

  @override
  Future<void> deleteAll() => _storage.deleteAll();
}

class Session {
  /// [prefix] namespaces every key, so more than one farmer's credentials can
  /// live in the same keystore. The signed-in farmer uses no prefix -- the
  /// keys every existing install already has. A farmer who signed out with
  /// work still unsent is kept under `parked.<owner>.` by [Accounts], so the
  /// queue can go on sending their claims under their own name.
  Session({SecureStore? storage, this.prefix = ''})
    : _storage = storage ?? const KeystoreStore();

  final SecureStore _storage;
  final String prefix;

  String get _kAccess => '${prefix}access_token';
  String get _kAccessExpiry => '${prefix}access_expires_at';
  String get _kRefresh => '${prefix}refresh_token';
  String get _kPrincipal => '${prefix}principal';

  /// How close to expiry a token is treated as already expired.
  ///
  /// A token with thirty seconds left will expire mid-upload on a slow rural
  /// connection, and a refused upload costs a retry cycle.
  static const _skew = Duration(minutes: 1);

  String? _access;
  DateTime? _accessExpiry;
  String? _refresh;
  Map<String, Object?>? _principal;

  bool _loaded = false;

  /// Read what the keystore holds. Safe to call repeatedly.
  Future<void> load() async {
    if (_loaded) return;
    _access = await _storage.read(_kAccess);
    _refresh = await _storage.read(_kRefresh);

    final expiry = await _storage.read(_kAccessExpiry);
    _accessExpiry = expiry == null ? null : DateTime.tryParse(expiry)?.toUtc();

    final who = await _storage.read(_kPrincipal);
    if (who != null) {
      _principal = jsonDecode(who) as Map<String, Object?>;
    }
    _loaded = true;
  }

  /// Read the keystore again, whatever is already in memory.
  ///
  /// The app and the background worker are separate processes sharing one
  /// keystore. When the worker refreshes, the app's copy of the refresh
  /// token is the one just spent; presenting it again is what used to sign
  /// the farmer out. Called before every refresh.
  Future<void> reload() async {
    _loaded = false;
    await load();
  }

  /// Whether this farmer is signed in.
  ///
  /// Deliberately not "has a valid access token". A refresh token is what
  /// makes somebody signed in; the access token is a detail of how the next
  /// request will be made, and its expiry while offline is not a logout.
  bool get signedIn => (_refresh ?? '').isNotEmpty;

  /// Whether a request can be made right now without refreshing first.
  bool get accessUsable {
    if ((_access ?? '').isEmpty) return false;
    final expiry = _accessExpiry;
    if (expiry == null) return true;
    return DateTime.now().toUtc().isBefore(expiry.subtract(_skew));
  }

  String? get accessToken => _access;
  String? get refreshToken => _refresh;

  /// Who the farmer is, as the server last described them.
  ///
  /// Held so that a screen can greet somebody by name and know their farmer
  /// id with no signal at all. It is a copy of what the server said, not a
  /// claim this app makes.
  Map<String, Object?>? get principal => _principal;

  String? get farmerId => _principal?['farmer_id'] as String?;

  /// The last four digits of the mobile this account signs in with, for saying
  /// which account some work belongs to without printing the number.
  ///
  /// Masked deliberately. A field phone is often the only one in a household
  /// and gets handed around, so the sign-in screen naming work left by a
  /// previous sign-in must identify it to the person it belongs to without
  /// disclosing a whole number to whoever is holding the phone.
  String? get mobileTail {
    for (final key in const ['mobile_e164', 'mobile', 'phone']) {
      final v = _principal?[key];
      if (v is String && v.length >= 4) {
        return v.substring(v.length - 4);
      }
    }
    return null;
  }

  String? get name =>
      (_principal?['full_name'] ?? _principal?['name']) as String?;

  /// Whose work this session sends: the account the server signed in.
  ///
  /// Every queued row carries it. The account id rather than the Farmer ID,
  /// because the token authorises the account -- and a row is only ever sent
  /// with the token of the account that queued it.
  String? get ownerId {
    final p = _principal;
    if (p == null) return null;
    for (final key in const ['id', 'user_id', 'farmer_id']) {
      final v = p[key];
      if (v is String && v.isNotEmpty) return v;
    }
    return null;
  }

  /// When the access token stops being usable, for copying a session whole.
  DateTime? get accessExpiry => _accessExpiry;

  /// Take on another session's credentials exactly, expiry included.
  Future<void> copyFrom(Session other) async {
    _access = other._access;
    _accessExpiry = other._accessExpiry;
    _refresh = other._refresh;
    _principal = other._principal;
    Future<void> put(String key, String? value) =>
        value == null ? _storage.delete(key) : _storage.write(key, value);
    await put(_kAccess, _access);
    await put(_kAccessExpiry, _accessExpiry?.toIso8601String());
    await put(_kRefresh, _refresh);
    await put(_kPrincipal, _principal == null ? null : jsonEncode(_principal));
    _loaded = true;
  }

  Future<void> save({
    required String accessToken,
    required Duration accessTtl,
    String? refreshToken,
    Map<String, Object?>? principal,
  }) async {
    _access = accessToken;
    _accessExpiry = DateTime.now().toUtc().add(accessTtl);

    await _storage.write(_kAccess, accessToken);
    await _storage.write(_kAccessExpiry, _accessExpiry!.toIso8601String());

    if (refreshToken != null) {
      _refresh = refreshToken;
      await _storage.write(_kRefresh, refreshToken);
    }
    if (principal != null) {
      _principal = principal;
      await _storage.write(_kPrincipal, jsonEncode(principal));
    }
    _loaded = true;
  }

  /// Forget the access token but stay signed in.
  ///
  /// What a 401 means when the refresh token is still good: this particular
  /// token is spent. The farmer has not been signed out and their queue keeps
  /// working.
  Future<void> forgetAccess() async {
    _access = null;
    _accessExpiry = null;
    await _storage.delete(_kAccess);
    await _storage.delete(_kAccessExpiry);
  }

  /// The server refused the refresh token: the tokens go, the farmer does not.
  ///
  /// # Why this is not [end]
  ///
  /// This used to sign the farmer out and delete their queue. A refresh token
  /// lasts thirty days, so a farmer who filed from a field and did not reach a
  /// signal for a month came back to find the claim gone from the phone --
  /// deleted because a token had expired, which is not something they did.
  ///
  /// So who they were is kept. Their work stays queued under their name, and
  /// the sign-in screen can greet them and say it is waiting for them.
  Future<void> lapse() async {
    _access = null;
    _accessExpiry = null;
    _refresh = null;
    await _storage.delete(_kAccess);
    await _storage.delete(_kAccessExpiry);
    await _storage.delete(_kRefresh);
  }

  /// Forget this session entirely: tokens and who it was.
  ///
  /// Only this session's own keys. Other farmers' parked credentials share the
  /// keystore and must survive somebody else signing out.
  Future<void> end() async {
    _access = null;
    _accessExpiry = null;
    _refresh = null;
    _principal = null;
    for (final key in [_kAccess, _kAccessExpiry, _kRefresh, _kPrincipal]) {
      await _storage.delete(key);
    }
  }
}
