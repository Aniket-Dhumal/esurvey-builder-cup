import 'dart:convert';

import 'session.dart';

/// Farmers who signed out of this phone with claims still unsent.
///
/// # Why a phone holds more than one farmer's credentials
///
/// A handset in a village is shared. Farmer A files a claim with no signal,
/// hands the phone to farmer B, and signs out. Deleting A's claim at that
/// moment loses it; sending it under B's session is refused by the server, and
/// should be. So A's credentials are parked here, in the keystore, and the
/// queue goes on sending A's work under A's own name until the office has all
/// of it. Then A's rows, files and credentials are removed from the phone.
///
/// B never sees any of it: every screen reads only the signed-in farmer's
/// rows.
///
/// If A's parked token expires before the work has gone, the credentials are
/// forgotten but the rows are not. They stay, hidden, until A signs in on this
/// phone again.
class Accounts {
  Accounts({SecureStore? storage})
    : _storage = storage ?? const KeystoreStore();

  final SecureStore _storage;

  static const _kParked = 'parked_owners';

  static String _prefix(String owner) => 'parked.$owner.';

  /// Owners with parked credentials, oldest first.
  Future<List<String>> parked() async {
    final raw = await _storage.read(_kParked);
    if (raw == null || raw.isEmpty) return const [];
    try {
      return [for (final o in jsonDecode(raw) as List<dynamic>) '$o'];
    } catch (_) {
      return const [];
    }
  }

  /// A session over an owner's parked credentials, loaded.
  Future<Session> session(String owner) async {
    final s = Session(storage: _storage, prefix: _prefix(owner));
    await s.load();
    return s;
  }

  /// Keep [active]'s credentials so its queue can go on sending after it
  /// signs out. Does nothing for a session that has no owner or no token.
  Future<void> park(Session active) async {
    final owner = active.ownerId;
    if (owner == null || !active.signedIn) return;
    final parkedSession = Session(storage: _storage, prefix: _prefix(owner));
    await parkedSession.copyFrom(active);
    final owners = await parked();
    if (!owners.contains(owner)) {
      await _write([...owners, owner]);
    }
  }

  /// Forget an owner's parked credentials. Their queued rows are untouched.
  ///
  /// Called when their work has all gone, when their token is refused, and
  /// when they sign in on this phone again -- at which point the fresh
  /// session supersedes the parked one.
  Future<void> forget(String owner) async {
    await Session(storage: _storage, prefix: _prefix(owner)).end();
    final owners = await parked();
    if (owners.contains(owner)) {
      await _write([
        for (final o in owners)
          if (o != owner) o,
      ]);
    }
  }

  Future<void> _write(List<String> owners) =>
      _storage.write(_kParked, jsonEncode(owners));
}
