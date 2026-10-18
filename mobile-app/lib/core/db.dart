import 'dart:convert';
import 'dart:io';
import 'dart:math' as math;

import 'package:path/path.dart' as p;
import 'package:sqflite/sqflite.dart';

import 'retry.dart';

/// Everything the phone knows, whether or not it has ever had a signal.
///
/// # Why this is SQLite and not a cache
///
/// A farmer photographs a damaged field standing in it, with no signal, and
/// walks away believing the claim is filed. Whatever is on this phone is the
/// claim until it reaches the office -- there is no other copy. So this is the
/// system of record for as long as the farmer is offline, and it is written
/// with the care that implies: every row carries what it is, how many times
/// sending it has been tried, and why the last attempt failed.
///
/// # What is deliberately not here
///
/// Anything scoped to a person that could outlive them on a shared handset.
/// The queue is cleared on sign-out, because a phone in a village is borrowed,
/// and one farmer's photographs must not be sitting in the app when the next
/// person opens it.
class Store {
  Store._(this._db);

  final Database _db;
  Database get raw => _db;

  static const _fileName = 'esurvey.db';
  static const _version = 12;

  /// [ownConnection] is for code running beside the app rather than as it:
  /// the background worker.
  ///
  /// # Why the worker must not share the app's connection
  ///
  /// sqflite hands out one native connection per database file within a
  /// process, and on Android WorkManager runs its Dart callback in a second
  /// engine *inside the app's own process* whenever the app is open. The
  /// worker opened the file, did its pass, and closed it in `finally` -- which
  /// closed the app's connection too.
  ///
  /// Seen on a handset: the worker finished at 09:02:29.9 and the app's first
  /// `database_closed` came at 09:02:29.909. From then on every write failed --
  /// the farmer filled in the form, pressed save fifteen times, and nothing was
  /// kept, with nothing on screen to say so. The one outcome this app is built
  /// to rule out.
  ///
  /// With its own connection the worker closes only what it opened. Two
  /// connections to one file are ordinary SQLite; the sync lease is what stops
  /// the two sides sending the same row, and it lives in the data.
  static Future<Store> open({
    String? directory,
    bool ownConnection = false,
  }) async {
    final dir = directory ?? await getDatabasesPath();
    final db = await openDatabase(
      p.join(dir, _fileName),
      version: _version,
      singleInstance: !ownConnection,
      onConfigure: (db) async {
        // Off by default in SQLite, and this schema depends on it: a capture
        // whose draft has gone is a photograph of nothing.
        await db.execute('PRAGMA foreign_keys = ON');
      },
      onCreate: _create,
      onUpgrade: _upgrade,
    );
    return Store._(db);
  }

  static Future<void> _create(Database db, int version) async {
    // A report the farmer has filled in. It exists here before it exists
    // anywhere, and `server_id` is the only thing that says it has landed.
    await db.execute('''
      CREATE TABLE drafts (
        local_id        TEXT PRIMARY KEY,
        server_id       TEXT,
        reference       TEXT,

        survey_event_id TEXT NOT NULL,
        farmer_id       TEXT NOT NULL,
        season          TEXT NOT NULL,
        crop_year       TEXT NOT NULL,
        loss_category   TEXT NOT NULL,
        calamity_type   TEXT NOT NULL,
        occurred_at     TEXT NOT NULL,

        farm_id          TEXT NOT NULL,
        survey_number    TEXT NOT NULL,
        crop_name        TEXT NOT NULL,
        area_sown_ha     REAL NOT NULL,
        area_affected_ha REAL NOT NULL,
        damage_percent   REAL NOT NULL,

        declaration_accepted INTEGER NOT NULL DEFAULT 0,
        signature_kind       TEXT NOT NULL DEFAULT '',

        status      TEXT NOT NULL DEFAULT 'pending',
        attempts    INTEGER NOT NULL DEFAULT 0,
        last_error  TEXT,
        -- The four points the server assigned for this parcel, as JSON.
        --
        -- Carried on the draft rather than fetched during the walk, because
        -- the walk happens in a field: the app asks for these when it fetches
        -- the parcel, which is a step that already needs a network, and the
        -- farmer can then be guided with no signal at all.
        stations TEXT,

        -- When the farmer completed the form, which is not when the claim
        -- reaches the office. The form is filled in a field with no signal
        -- and this row waits -- for its evidence, and then for a network.
        -- The office measures the 48-hour intimation window on this, so it
        -- has to be the moment of filing rather than the moment of arrival.
        -- Distinct from created_at, which is when the row was first written.
        filed_at INTEGER,

        created_at  INTEGER NOT NULL,
        updated_at  INTEGER NOT NULL
      )
    ''');
    await db.execute('CREATE INDEX drafts_status ON drafts (status)');

    // A photograph, and everything the handset knew at the moment it was
    // taken. The position is read live rather than from the file, because
    // browsers and cameras both strip EXIF and EXIF is trivially editable; a
    // live reading has to be lied about deliberately.
    await db.execute('''
      CREATE TABLE captures (
        local_id       TEXT PRIMARY KEY,
        draft_local_id TEXT NOT NULL
          REFERENCES drafts (local_id) ON DELETE CASCADE,

        kind    TEXT NOT NULL,
        stage   TEXT NOT NULL,
        caption TEXT NOT NULL DEFAULT '',

        -- The file on disk. Not a blob: a photograph is megabytes and no
        -- listing wants to drag it through every query that touches the row.
        file_path    TEXT NOT NULL,
        content_type TEXT NOT NULL,
        byte_size    INTEGER NOT NULL,
        sha256       TEXT NOT NULL,

        lat         REAL,
        lon         REAL,
        accuracy_m  REAL,
        heading_deg REAL,
        captured_at TEXT NOT NULL,
        device      TEXT NOT NULL DEFAULT '',

        status     TEXT NOT NULL DEFAULT 'pending',
        attempts   INTEGER NOT NULL DEFAULT 0,
        last_error TEXT,
        -- The office's own code for a refusal, beside the message.
        --
        -- The message is what the farmer reads; this is what the app acts on.
        -- Some refusals are fixed only by recording the same point somewhere
        -- else, and no amount of resending will ever fix them: three clips of
        -- one claim were refused SAME_POSITION on every pass, and the only
        -- thing the app offered the farmer was the retry button that caused
        -- it. Deciding that from the message would mean matching on prose the
        -- office is free to reword.
        last_error_code TEXT,
        -- Which of the four assigned points this evidences, and how far from
        -- it the farmer actually stood. Null for a capture outside the
        -- directed walk.
        --
        -- The offset is kept whether it is two metres or forty. The farmer is
        -- never asked to justify moving, so this number is the only account
        -- an officer gets of it.
        station_no       INTEGER,
        station_offset_m REAL,

        -- Length of a clip. Null for a still.
        duration_ms INTEGER,

        -- Whether Android called the fix mocked. Null where the platform did
        -- not say, which is not the same as false.
        is_mocked INTEGER,

        -- What the phone's encoder agreed to record this clip at.
        --
        -- A claim is budgeted at 20 MB and the app asks for 15 fps at
        -- 2.5 Mbit/s to hit it, but an Android encoder may refuse a frame rate
        -- or accept a bitrate and ignore it. When a claim arrives at 100 MB
        -- instead of 20 that is a fact about the handset, and the office should
        -- be able to read which profile was used rather than infer it from the
        -- file size. Null for a capture recorded before this was stored.
        encoding_profile     TEXT,
        encoding_fps         INTEGER,
        encoding_bitrate_bps INTEGER,

        -- What the live assistant asked the farmer to show in this clip, in the
        -- words they heard, and what said it.
        --
        -- Null for most clips, and that means nothing was asked -- never that a
        -- clip is missing something. What makes a claim complete is the walk:
        -- four points and the spacing rule, decided on this phone with no network
        -- and no model. The assistant shapes what the next clip shows; it cannot
        -- add one.
        --
        -- Kept against the clip rather than in a conversation log because it is
        -- evidence about evidence: a close-up of split pods that the assistant
        -- asked for is a directed observation rather than the farmer's own
        -- selection, and nothing joins a log line to a clip a year later.
        guidance_asked_for   TEXT,
        guidance_model       TEXT,

        -- Resumable upload state. upload_key is chosen once and never
        -- changes, so a retry resumes the upload it started rather than
        -- beginning a second one; upload_id and sent_bytes are what the
        -- server last acknowledged.
        upload_key TEXT,
        upload_id  TEXT,
        sent_bytes INTEGER NOT NULL DEFAULT 0,

        created_at INTEGER NOT NULL,
        updated_at INTEGER NOT NULL
      )
    ''');
    await db.execute(
      'CREATE INDEX captures_draft ON captures (draft_local_id)',
    );
    await db.execute('CREATE INDEX captures_status ON captures (status)');

    // A walked plot boundary.
    //
    // Keyed by parcel rather than by a local id: walking the same field again
    // is a correction, not a second boundary, and keeping both would mean the
    // correction races the original on the next sync.
    await db.execute('''
      CREATE TABLE boundaries (
        farm_id       TEXT PRIMARY KEY,
        survey_number TEXT NOT NULL,
        points        TEXT NOT NULL,

        -- Whose parcel this is. One signed-in number may act for several
        -- farmers, and a boundary walked for one of them is not the others'
        -- to see. Null on rows from before the list existed.
        farmer_id TEXT,

        area_ha          REAL NOT NULL,
        worst_accuracy_m REAL NOT NULL,

        status     TEXT NOT NULL DEFAULT 'pending',
        attempts   INTEGER NOT NULL DEFAULT 0,
        last_error TEXT,
        created_at INTEGER NOT NULL,
        updated_at INTEGER NOT NULL
      )
    ''');
    await db.execute('CREATE INDEX boundaries_status ON boundaries (status)');

    await _createFarmers(db);

    // Small facts the app needs before it can reach anything: who is signed
    // in, which events are open, what the registry said about their land.
    // Written on purpose and cleared on sign-out.
    await db.execute('''
      CREATE TABLE cache (
        key        TEXT PRIMARY KEY,
        value      TEXT NOT NULL,
        updated_at INTEGER NOT NULL
      )
    ''');

    await _createAppeals(db);
    await _addReliableSync(db);
    await _addOwnersAndJourneys(db);
    // Somewhere to write down an error nobody was going to hear about. On the
    // create path as well as the upgrade one, because a fresh install is the
    // one most likely to hit a fault worth reading.
    await _addFaults(db);
    // Last: these index owner_id, which the step above is what adds.
    await _indexByFarmer(db);
  }

  /// Appeals queue like everything else.
  ///
  /// The report this objects to is already at the office, so unlike a draft
  /// there is nothing local for it to reference -- it carries the server's
  /// report id. What it shares with the rest of the queue is the reason for
  /// queueing at all: a farmer reads a decision they disagree with wherever
  /// they happen to be, and requiring signal at that moment would mean the
  /// objection is lost or never made.
  static Future<void> _createAppeals(Database db) async {
    await db.execute('''
      CREATE TABLE appeals (
        local_id  TEXT PRIMARY KEY,
        report_id TEXT NOT NULL,
        reference TEXT,

        ground    TEXT NOT NULL,
        statement TEXT NOT NULL,

        server_id  TEXT,
        status     TEXT NOT NULL DEFAULT 'pending',
        attempts   INTEGER NOT NULL DEFAULT 0,
        last_error TEXT,
        created_at INTEGER NOT NULL,
        updated_at INTEGER NOT NULL
      )
    ''');
    await db.execute('CREATE INDEX appeals_status ON appeals (status)');
  }

  /// Existing installs get the new table and keep their queue.
  ///
  /// A farmer mid-claim when the app updates must not lose the photographs
  /// already on the phone, which is what dropping and recreating would do.
  static Future<void> _upgrade(Database db, int from, int to) async {
    if (from < 2) {
      await _createAppeals(db);
    }
    if (from < 3) {
      await _addDirectedSampling(db);
    }
    if (from < 4) {
      await _addReliableSync(db);
    }
    if (from < 5) {
      await _addOwnersAndJourneys(db);
    }
    if (from < 6) {
      await _addFiledAt(db);
    }
    if (from < 7) {
      await _addFarmerList(db);
    }
    if (from < 8) {
      await _addEncodingProfile(db);
    }
    if (from < 9) {
      await _addFaults(db);
    }
    if (from < 10) {
      await _addRefusalCode(db);
    }
    if (from < 11) {
      await _addTakenPoints(db);
    }
    if (from < 12) {
      await _addGuidance(db);
    }
  }

  /// Remember what the assistant asked for, against the clip that answered it.
  static Future<void> _addGuidance(Database db) async {
    await _addColumnIfMissing(db, 'captures', 'guidance_asked_for', 'TEXT');
    await _addColumnIfMissing(db, 'captures', 'guidance_model', 'TEXT');
  }

  /// Remember which ground other claims on the survey number have filmed.
  ///
  /// Null on every journey started before this, which reads as "nothing known
  /// to be taken" -- the same behaviour as before, with the office still
  /// catching it on arrival.
  static Future<void> _addTakenPoints(Database db) async {
    await _addColumnIfMissing(db, 'journeys', 'taken_points', 'TEXT');
  }

  /// Keep the office's code for a refusal, not only its wording.
  ///
  /// Without it the app cannot tell a refusal the farmer can act on from one
  /// they cannot, so it offered the same retry button for both -- and for a
  /// clip refused SAME_POSITION that button can only ever produce the same
  /// refusal again.
  ///
  /// # Why nothing is backfilled
  ///
  /// Every clip already refused on a phone has null here, so it is not yet
  /// offered as a walk: it is still counted on the stuck card and still revived
  /// by "try again". One more refusal fixes that -- this build records the code
  /// -- and the card appears straight afterwards. It costs the farmer one tap.
  ///
  /// The alternative is reading the code back out of the stored sentence, and
  /// that is the thing this column exists to stop. The office is free to reword
  /// a message; a released app that depends on the wording breaks silently when
  /// it does, and the failure looks like a farmer with no way to fix a clip.
  static Future<void> _addRefusalCode(Database db) async {
    await _addColumnIfMissing(db, 'captures', 'last_error_code', 'TEXT');
  }

  /// The people this handset can act for, and which claim belongs to which.
  ///
  /// One mobile number signs in, and that number may be the one AgriStack has
  /// for several farmers -- a household shares a phone, and a farmer whose own
  /// registered number is long gone borrows one. Until now the app held a
  /// single farmer per signed-in account, so the second person had nowhere to
  /// be.
  ///
  /// The list is kept here and nowhere else. It was decided on 2026-09-16
  /// that the office should not hold a register of which households share a
  /// handset; the server checks each farmer once and hands back a grant, and
  /// this table is where the grant and the farmer live together.
  static Future<void> _addFarmerList(Database db) async {
    await _createFarmers(db);

    // Existing rows belong to whoever filed them, which the row already says.
    // Boundaries are the exception: they are keyed by parcel and never
    // carried a farmer, so the column is added empty and filled as parcels
    // are walked. An unattributed boundary stays visible to everyone on the
    // account, which is what it was before this.
    final columns = await db.rawQuery('PRAGMA table_info(boundaries)');
    if (!columns.any((c) => c['name'] == 'farmer_id')) {
      await db.execute('ALTER TABLE boundaries ADD COLUMN farmer_id TEXT');
    }
    await _indexByFarmer(db);
  }

  /// What the farmer-scoped screens read by.
  ///
  /// Every listing on the phone now asks "whose is this", and by the end of a
  /// season a handset that files for a household holds hundreds of rows. The
  /// capture scope reaches its farmer through the draft it evidences, so
  /// drafts are indexed on (owner, farmer) and captures on their draft, which
  /// is what turns that subquery from a scan into a lookup.
  static Future<void> _indexByFarmer(Database db) async {
    for (final statement in const [
      'CREATE INDEX IF NOT EXISTS drafts_farmer '
          'ON drafts (owner_id, farmer_id, status)',
      'CREATE INDEX IF NOT EXISTS journeys_farmer '
          'ON journeys (owner_id, farmer_id, stage)',
      'CREATE INDEX IF NOT EXISTS boundaries_farmer '
          'ON boundaries (owner_id, farmer_id, status)',
      'CREATE INDEX IF NOT EXISTS farmers_used '
          'ON farmers (owner_id, last_used_at)',
    ]) {
      await db.execute(statement);
    }
  }

  static Future<void> _createFarmers(Database db) async {
    await db.execute('''
      CREATE TABLE IF NOT EXISTS farmers (
        -- The account whose list this is: one signed-in mobile number.
        owner_id  TEXT NOT NULL,
        farmer_id TEXT NOT NULL,

        name    TEXT NOT NULL DEFAULT '',
        village TEXT NOT NULL DEFAULT '',

        -- Proof that this account answered for this farmer, and when it stops
        -- being proof. Presented when refreshing the record; when it expires
        -- the farmer is asked to confirm the date of birth again.
        grant_token      TEXT,
        grant_expires_at INTEGER,

        added_at     INTEGER NOT NULL,
        last_used_at INTEGER NOT NULL,

        PRIMARY KEY (owner_id, farmer_id)
      )
    ''');
  }

  /// When the farmer filed, as distinct from when the office hears about it.
  ///
  /// The office measured the 48-hour intimation window on arrival, so a
  /// farmer who filled the form on the first morning and found a signal on
  /// the third day was recorded as having filed late. That flag is the one an
  /// officer reads when weighing whether to believe a claim, and it was a
  /// wrong fact about somebody who did nothing wrong.
  ///
  /// Left null on existing rows. A draft already queued has no honest answer
  /// -- the moment has passed and guessing it would be inventing a record --
  /// so the server falls back to arrival for those, exactly as before.
  static Future<void> _addFiledAt(Database db) async {
    // Checked rather than assumed, like _addReliableSync: an upgrade is run
    // against whatever shape the phone actually has, and a statement that
    // throws here leaves the database unopenable with the farmer's claims
    // inside it.
    final columns = await db.rawQuery('PRAGMA table_info(drafts)');
    if (columns.any((c) => c['name'] == 'filed_at')) return;
    await db.execute('ALTER TABLE drafts ADD COLUMN filed_at INTEGER');
  }

  /// Whose work each row is, and the claims a farmer has started.
  ///
  /// # owner_id
  ///
  /// The account that queued the row, and so the only account whose token may
  /// send it. Until now a phone held one farmer's work and sign-out deleted
  /// it; now another farmer's unsent claims stay on the phone, hidden, and go
  /// on sending under their owner's parked credentials. Every screen reads
  /// only the signed-in farmer's rows.
  ///
  /// Existing rows are left NULL here -- the database does not know who is
  /// signed in -- and [adoptUnowned] gives them to the signed-in farmer at
  /// startup. Before this version only one farmer's work could be on a phone,
  /// so that is always the right owner.
  ///
  /// # journeys
  ///
  /// A claim in progress, from choosing the parcel to the last piece of
  /// evidence. The draft only exists once the form is filed, so without this
  /// a farmer interrupted while typing started again, and one interrupted on
  /// the walk was sent back to the photograph sequence instead of the station
  /// they had reached.
  ///
  /// Existing unsent drafts become journeys at the evidence stage, so the
  /// home screen can offer to finish them; one whose evidence is complete is
  /// marked done the first time it is looked at.
  static Future<void> _addOwnersAndJourneys(Database db) async {
    for (final table in queueTables) {
      final columns = await db.rawQuery('PRAGMA table_info($table)');
      if (!columns.any((c) => c['name'] == 'owner_id')) {
        await db.execute('ALTER TABLE $table ADD COLUMN owner_id TEXT');
      }
      await db.execute(
        'CREATE INDEX IF NOT EXISTS ${table}_owner ON $table (owner_id, status)',
      );
    }

    await db.execute('''
      CREATE TABLE IF NOT EXISTS journeys (
        local_id   TEXT PRIMARY KEY,
        owner_id   TEXT,
        farmer_id  TEXT NOT NULL DEFAULT '',
        event_id   TEXT NOT NULL DEFAULT '',
        farm_id    TEXT NOT NULL DEFAULT '',
        survey_number TEXT NOT NULL DEFAULT '',

        -- form | evidence | done
        stage      TEXT NOT NULL DEFAULT 'form',

        -- What the farmer typed, as JSON, saved as they type.
        form       TEXT,
        -- The event and parcel the claim is against, as JSON, so a resumed
        -- form needs no network to be shown again.
        event      TEXT,
        parcel     TEXT,
        -- The four assigned points, as JSON, when the walk is directed.
        stations   TEXT,
        -- Points on this survey number that other claims already recorded.
        --
        -- Carried so the walk can refuse them where the farmer is standing,
        -- rather than the office refusing them a day later when the clip
        -- arrives: a survey number is routinely shared, and a refusal on arrival
        -- is a walk already spent.
        taken_points TEXT,
        -- stations | photographs
        evidence_mode TEXT,

        created_at INTEGER NOT NULL,
        updated_at INTEGER NOT NULL
      )
    ''');
    await db.execute(
      'CREATE INDEX IF NOT EXISTS journeys_owner ON journeys (owner_id, stage)',
    );

    final now = DateTime.now().millisecondsSinceEpoch;
    await db.execute(
      'INSERT OR IGNORE INTO journeys (local_id, owner_id, farmer_id, event_id, '
      '  farm_id, survey_number, stage, stations, evidence_mode, created_at, '
      '  updated_at) '
      "SELECT local_id, owner_id, farmer_id, survey_event_id, farm_id, "
      "  survey_number, 'evidence', stations, "
      "  CASE WHEN stations IS NULL THEN 'photographs' ELSE 'stations' END, "
      '  created_at, ? '
      "FROM drafts WHERE server_id IS NULL AND status <> 'failed'",
      [now],
    );
  }

  /// The four queued tables, which share everything below.
  static const queueTables = ['drafts', 'captures', 'boundaries', 'appeals'];

  /// Tables whose rows belong to one farmer.
  static const ownedTables = [...queueTables, 'journeys'];

  /// What makes the queue safe to retry and safe to leave alone.
  ///
  /// # sync_key
  ///
  /// The idempotency key each item is sent with, chosen once and never
  /// changed. The old local ids are microsecond timestamps, which are unique
  /// on one phone and nowhere else -- a reinstall starts the same clock again,
  /// and a key the server has already seen from this farmer would be answered
  /// with the old report instead of filing the new one. A random key cannot
  /// collide that way.
  ///
  /// # next_attempt_at
  ///
  /// When an item may next be tried, in milliseconds. Zero means now. It is
  /// what replaces the attempt budget: an item the office could not take right
  /// now waits a little longer each time and is never abandoned.
  ///
  /// # The rows the old budget abandoned
  ///
  /// Under schema 3 an item gave up after five attempts, and server errors
  /// counted. Those rows are still `pending` with attempts at or above five,
  /// invisible to every query and to the farmer. They were never refused, so
  /// they are put back in the queue here -- which on the first launch after
  /// this update is the moment a farmer's lost claim quietly goes after all.
  ///
  /// # device
  ///
  /// A table of its own because [clearPersonalData] empties the cache on
  /// sign-out, and the phone does not become a different phone when a
  /// different farmer signs in.
  static Future<void> _addReliableSync(Database db) async {
    for (final table in queueTables) {
      final columns = await db.rawQuery('PRAGMA table_info($table)');
      bool has(String name) => columns.any((c) => c['name'] == name);

      if (!has('sync_key')) {
        await db.execute('ALTER TABLE $table ADD COLUMN sync_key TEXT');
      }
      if (!has('next_attempt_at')) {
        await db.execute(
          'ALTER TABLE $table ADD COLUMN next_attempt_at INTEGER NOT NULL DEFAULT 0',
        );
      }
      // Every existing row gets a key of its own.
      await db.execute(
        "UPDATE $table SET sync_key = 'q' || lower(hex(randomblob(16))) "
        'WHERE sync_key IS NULL',
      );
      await db.execute(
        'CREATE INDEX IF NOT EXISTS ${table}_due ON $table (status, next_attempt_at)',
      );
    }

    // Revive what the old budget abandoned. Refused rows are 'failed' and
    // stay failed; sent rows are left alone.
    for (final table in queueTables) {
      await db.execute(
        'UPDATE $table SET attempts = 0, next_attempt_at = 0 '
        "WHERE status NOT IN ('sent', 'failed')",
      );
    }

    await db.execute('''
      CREATE TABLE IF NOT EXISTS device (
        key   TEXT PRIMARY KEY,
        value TEXT NOT NULL
      )
    ''');
  }

  /// The directed walk: which point each capture belongs to, and how far an
  /// upload has got.
  ///
  /// Added rather than recreated, because a farmer upgrading mid-season has
  /// photographs queued against drafts that have not reached the office. A
  /// rebuild would take them, on the one pass where the app finally had
  /// signal.
  /// Record what the phone's encoder actually agreed to.
  ///
  /// Added rather than recreated: a farmer upgrading mid-walk has clips queued
  /// against a draft that has not reached the office, and a rebuild would take
  /// them on the one pass where the app finally had signal.
  /// Added one column at a time, each skipped where it is already there.
  ///
  /// Not defensiveness for its own sake. A migration that fails part-way leaves
  /// the database at the old version, so the next open runs it again -- and an
  /// unguarded `ALTER TABLE ... ADD COLUMN` on a column that did land throws
  /// `duplicate column name` every time from then on. The farmer's app would
  /// never open its own database again, with a full queue inside it.
  /// Somewhere for errors nobody was going to hear about.
  ///
  /// See Faults. The app had no error handler at all, so a throw in one of its
  /// ninety-odd fire-and-forget futures either killed a walk or vanished, and
  /// the only way to find out which was a cable and adb logcat.
  static Future<void> _addFaults(Database db) async {
    await db.execute(
      'CREATE TABLE IF NOT EXISTS faults ('
      '  id       INTEGER PRIMARY KEY AUTOINCREMENT,'
      // Which handler caught it: 'flutter' for a build or layout, 'async' for a
      // future nobody awaited, or the name a caller passed to Faults.note.
      '  where_at TEXT NOT NULL,'
      '  message  TEXT NOT NULL,'
      // The top frames only. A full Dart stack is kilobytes and the useful part
      // is the top; the rest belongs to the framework.
      '  stack    TEXT NOT NULL,'
      '  at       INTEGER NOT NULL'
      ')',
    );
    await db.execute(
      'CREATE INDEX IF NOT EXISTS faults_at ON faults (at DESC)',
    );
  }

  static Future<void> _addEncodingProfile(Database db) async {
    await _addColumnIfMissing(db, 'captures', 'encoding_profile', 'TEXT');
    await _addColumnIfMissing(db, 'captures', 'encoding_fps', 'INTEGER');
    await _addColumnIfMissing(
      db,
      'captures',
      'encoding_bitrate_bps',
      'INTEGER',
    );
  }

  /// Add [column] to [table] unless it is already present.
  static Future<void> _addColumnIfMissing(
    Database db,
    String table,
    String column,
    String type,
  ) async {
    final columns = await db.rawQuery('PRAGMA table_info($table)');
    if (columns.any((c) => c['name'] == column)) return;
    await db.execute('ALTER TABLE $table ADD COLUMN $column $type');
  }

  static Future<void> _addDirectedSampling(Database db) async {
    for (final statement in const [
      'ALTER TABLE captures ADD COLUMN station_no INTEGER',
      'ALTER TABLE captures ADD COLUMN station_offset_m REAL',
      'ALTER TABLE captures ADD COLUMN duration_ms INTEGER',
      'ALTER TABLE captures ADD COLUMN is_mocked INTEGER',
      'ALTER TABLE captures ADD COLUMN upload_key TEXT',
      'ALTER TABLE captures ADD COLUMN upload_id TEXT',
      'ALTER TABLE captures ADD COLUMN sent_bytes INTEGER NOT NULL DEFAULT 0',
      // The four points the server assigned, as JSON, so the walk works with
      // no signal at all. Fetched when the parcel was fetched, which is a
      // step that already needed a network.
      'ALTER TABLE drafts ADD COLUMN stations TEXT',
    ]) {
      await db.execute(statement);
    }
  }

  Future<void> close() => _db.close();

  /// The signed-in farmer's account, whose rows every screen reads.
  ///
  /// Null while nobody is signed in, and in tests that never sign anybody in
  /// -- in which case nothing is filtered. Methods that take an `owner` use
  /// this unless given another; pass [everyone] to read across owners.
  String? owner;

  /// An `owner` argument meaning every farmer's rows.
  static const everyone = '*';

  /// Which of the account's farmers the screens are showing.
  ///
  /// One signed-in mobile number may act for several farmers -- a household
  /// shares a phone, and a farmer whose own registered number is long gone
  /// borrows one. [owner] says whose credentials send a row; this says whose
  /// claim it is, and they are different questions.
  ///
  /// Null means every farmer on the account, which is what the sync engine
  /// wants: work is sent under the account that queued it regardless of which
  /// farmer is on screen. Screens set it, so that switching to one farmer
  /// does not put another's claims in front of them.
  String? farmer;

  (String, List<Object?>) _scope(String? given) {
    final o = given ?? owner;
    if (o == null || o == everyone) return ('1 = 1', const []);
    return ('owner_id = ?', [o]);
  }

  /// The account scope for [table], narrowed to the selected farmer.
  ///
  /// Each table reaches the farmer differently: drafts and journeys carry the
  /// id, a capture belongs to whichever draft it evidences, and a boundary is
  /// attributed to the farmer whose parcel was walked. A table with no way to
  /// answer is left at the account scope rather than silently showing
  /// nothing.
  (String, List<Object?>) _scopeIn(
    String table, {
    String? owner,
    String? only,
  }) {
    final (w, a) = _scope(owner);
    final f = only ?? farmer;
    if (f == null || f == everyone) return (w, a);

    switch (table) {
      case 'drafts':
      case 'journeys':
        return ('$w AND farmer_id = ?', [...a, f]);
      case 'captures':
        return (
          '$w AND draft_local_id IN '
              '(SELECT local_id FROM drafts WHERE farmer_id = ?)',
          [...a, f],
        );
      case 'boundaries':
        // A boundary walked before the list existed has no farmer against it.
        // Left visible rather than hidden: it is the farmer's own work, and
        // hiding it would strand a parcel outline nobody could reach.
        return ('$w AND (farmer_id = ? OR farmer_id IS NULL)', [...a, f]);
      default:
        return (w, a);
    }
  }

  // --- the farmers this handset can act for --------------------------------

  /// Every farmer added on this handset under the signed-in account.
  ///
  /// Most recently used first: a household that files for three people files
  /// for the same one twice running far more often than it alternates.
  Future<List<Map<String, Object?>>> linkedFarmers({String? owner}) async {
    final (w, a) = _scope(owner);
    return _db.query(
      'farmers',
      where: w,
      whereArgs: a,
      // Tie-broken on insertion order, because the timestamp has millisecond
      // resolution and linking two people takes less than a millisecond. Without
      // it the order of a tie is whatever SQLite happens to return, so a handset
      // acting for a household could show them in a different order on each open
      // -- and the one the farmer used last, which is the whole point of the sort,
      // would not reliably be first.
      orderBy: 'last_used_at DESC, rowid DESC',
    );
  }

  Future<Map<String, Object?>?> linkedFarmer(
    String farmerId, {
    String? owner,
  }) async {
    final (w, a) = _scope(owner);
    final rows = await _db.query(
      'farmers',
      where: '$w AND farmer_id = ?',
      whereArgs: [...a, farmerId],
      limit: 1,
    );
    return rows.isEmpty ? null : rows.first;
  }

  /// Remember a farmer this account has answered for, with the grant that
  /// proves it. Re-adding the same farmer refreshes the grant and the name
  /// rather than making a second row.
  Future<void> linkFarmer({
    required String farmerId,
    required String name,
    required String village,
    String? grantToken,
    int? grantExpiresAt,
    String? owner,
  }) async {
    final now = DateTime.now().millisecondsSinceEpoch;
    final o = owner ?? this.owner ?? '';
    final existing = await linkedFarmer(farmerId, owner: o);
    await _db.insert('farmers', {
      'owner_id': o,
      'farmer_id': farmerId,
      'name': name,
      'village': village,
      'grant_token': grantToken,
      'grant_expires_at': grantExpiresAt,
      'added_at': existing?['added_at'] ?? now,
      'last_used_at': now,
    }, conflictAlgorithm: ConflictAlgorithm.replace);
  }

  /// Note that this farmer was the one picked, so the list stays in the order
  /// the household actually uses it.
  Future<void> touchFarmer(String farmerId, {String? owner}) async {
    final (w, a) = _scope(owner);
    await _db.update(
      'farmers',
      {'last_used_at': DateTime.now().millisecondsSinceEpoch},
      where: '$w AND farmer_id = ?',
      whereArgs: [...a, farmerId],
    );
  }

  /// The grant this handset holds for one farmer, if it still stands.
  ///
  /// Across every account on the phone: the queue sends another farmer's work
  /// under their own parked credentials long after they have handed the
  /// handset back, and it needs their grant to do it.
  ///
  /// Null when there is none, which is the ordinary case for the farmer whose
  /// own number signed in -- the office needs no grant for them.
  Future<String?> grantFor(String farmerId) async {
    if (farmerId.isEmpty) return null;
    final rows = await _db.query(
      'farmers',
      columns: ['grant_token', 'grant_expires_at'],
      where: 'farmer_id = ? AND grant_token IS NOT NULL',
      whereArgs: [farmerId],
      orderBy: 'grant_expires_at DESC',
      limit: 1,
    );
    if (rows.isEmpty) return null;
    final expires = rows.first['grant_expires_at'] as int?;
    if (expires != null && expires <= DateTime.now().millisecondsSinceEpoch) {
      // Expired. Sending it would be refused; the farmer is asked to confirm
      // this person again when they next open their list.
      return null;
    }
    return rows.first['grant_token'] as String?;
  }

  /// Take a farmer off this handset.
  ///
  /// Their claims are not deleted here. Work that has not reached the office
  /// is the farmer's only copy, and removing them from a list is not a
  /// decision to destroy it -- the caller checks first. See [heldSince] and
  /// [outstanding].
  Future<void> unlinkFarmer(String farmerId, {String? owner}) async {
    final (w, a) = _scope(owner);
    await _db.delete(
      'farmers',
      where: '$w AND farmer_id = ?',
      whereArgs: [...a, farmerId],
    );
  }

  /// Give rows from before owners were recorded to [to].
  ///
  /// Only one farmer's work could be on a phone until then, and that farmer
  /// is the one signed in -- sign-out used to delete everything.
  Future<void> adoptUnowned(String to) async {
    for (final table in ownedTables) {
      await _db.update(table, {'owner_id': to}, where: 'owner_id IS NULL');
    }
  }

  /// Every account with rows on this phone, other than [except].
  Future<List<String>> ownersOtherThan(String? except) async {
    final found = <String>{};
    for (final table in ownedTables) {
      final rows = await _db.rawQuery(
        'SELECT DISTINCT owner_id FROM $table WHERE owner_id IS NOT NULL',
      );
      for (final r in rows) {
        final o = r['owner_id'] as String;
        if (o != except) found.add(o);
      }
    }
    return found.toList()..sort();
  }

  /// Remove a farmer's rows once nothing of theirs is still to be sent.
  ///
  /// Returns whether it removed them. Refused rows hold it back for
  /// [refusedKeptFor]: they will never reach the office, but they are the only
  /// record the farmer has of what was refused, so they are kept a while in
  /// case the farmer signs in on this phone again to see why.
  ///
  /// The photograph files are left to `Photos.sweepOrphans`, which the caller
  /// runs: with the rows gone every such file is an orphan.
  Future<bool> purgeOwnerIfDone(
    String who, {
    Duration refusedKeptFor = const Duration(days: 30),
    DateTime? now,
  }) async {
    if ((await outstanding(owner: who, only: everyone)).any) return false;

    final cutoff = (now ?? DateTime.now())
        .subtract(refusedKeptFor)
        .millisecondsSinceEpoch;
    for (final table in queueTables) {
      final rows = await _db.rawQuery(
        "SELECT COUNT(*) AS n FROM $table WHERE owner_id = ? "
        "AND status = 'failed' AND updated_at > ?",
        [who, cutoff],
      );
      if (((rows.first['n'] as int?) ?? 0) > 0) return false;
    }

    await _db.transaction((t) async {
      for (final table in [
        'appeals',
        'captures',
        'drafts',
        'boundaries',
        'journeys',
        // The people this account could act for, and the grants that let it.
        //
        // A phone in a village is borrowed. Leaving these behind leaves the
        // previous household's names, their Farmer IDs, and tokens valid for
        // ninety days sitting on somebody else's handset -- and nothing else
        // deletes them, because the full wipe that does is called from
        // nowhere in the app.
        'farmers',
      ]) {
        await t.delete(table, where: 'owner_id = ?', whereArgs: [who]);
      }
    });
    return true;
  }

  /// Cached answers that describe a person -- their land, their reports.
  ///
  /// Cleared when a different farmer signs in. The engine's own keys
  /// (underscored: the lease, a pause the office asked for) and the phone's
  /// preferences describe the phone, not the farmer, and stay.
  Future<void> clearCachedPersonal() async {
    await _db.delete(
      'cache',
      where: "key NOT LIKE '\\_\\_%' ESCAPE '\\' AND key NOT IN (?, ?)",
      whereArgs: const ['voice_enabled', 'onboarding_seen'],
    );
  }

  /// A small fact about this install, kept across sign-outs.
  Future<String?> deviceValue(String key) async {
    final rows = await _db.query(
      'device',
      where: 'key = ?',
      whereArgs: [key],
      limit: 1,
    );
    return rows.isEmpty ? null : rows.first['value'] as String?;
  }

  Future<void> setDeviceValue(String key, String? value) async {
    if (value == null) {
      await _db.delete('device', where: 'key = ?', whereArgs: [key]);
      return;
    }
    await _db.insert('device', {
      'key': key,
      'value': value,
    }, conflictAlgorithm: ConflictAlgorithm.replace);
  }

  /// Write a row, updating it if it is already there.
  ///
  /// Deliberately not ConflictAlgorithm.replace, which is INSERT OR REPLACE
  /// and in SQLite means DELETE then INSERT -- so a draft updated with its
  /// server id would be deleted first, and ON DELETE CASCADE would take every
  /// photograph queued against it. Silently, on the one pass where the report
  /// finally succeeded, leaving a farmer with a filed report and none of the
  /// evidence they walked a field to gather.
  ///
  /// An update-then-insert is a real upsert: the row keeps its identity and
  /// nothing referencing it is disturbed.
  Future<void> _upsert(
    String table,
    String keyColumn,
    Object key,
    Map<String, Object?> values,
  ) async {
    final changed = await _db.update(
      table,
      values,
      where: '$keyColumn = ?',
      whereArgs: [key],
    );
    if (changed == 0) {
      await _db.insert(table, {
        ...values,
        // A queued item's key is born with it. Given here, where every insert
        // passes, so no screen that queues work can forget it.
        if (queueTables.contains(table) && values['sync_key'] == null)
          'sync_key': newSyncKey(),
        // And its owner, for the same reason: whoever is signed in when work
        // is queued is the only account that may send it.
        if (ownedTables.contains(table) && values['owner_id'] == null)
          'owner_id': owner,
      });
    }
  }

  // --- drafts -------------------------------------------------------------

  Future<void> putDraft(Map<String, Object?> draft) async {
    final now = DateTime.now().millisecondsSinceEpoch;
    await _upsert('drafts', 'local_id', draft['local_id'] as Object, {
      ...draft,
      'created_at': draft['created_at'] ?? now,
      'updated_at': now,
    });
  }

  Future<Map<String, Object?>?> draft(String localId) async {
    final rows = await _db.query(
      'drafts',
      where: 'local_id = ?',
      whereArgs: [localId],
      limit: 1,
    );
    return rows.isEmpty ? null : rows.first;
  }

  Future<List<Map<String, Object?>>> drafts({String? owner}) {
    final (w, a) = _scopeIn('drafts', owner: owner);
    return _db.query(
      'drafts',
      where: w,
      whereArgs: a,
      orderBy: 'created_at DESC',
    );
  }

  /// A claim that is filled in but not yet sendable.
  ///
  /// # Why a claim waits for its evidence
  ///
  /// The draft used to enter the queue the moment the form was filed, so a
  /// farmer interrupted between the form and the walk had already filed a
  /// claim with nothing behind it -- and the office could not tell a claim
  /// whose photographs were still coming from one whose photographs never
  /// would (docs/EVIDENCE_REVALIDATION.md, V4).
  ///
  /// So it is held here until the evidence is complete, and released by the
  /// farmer on the review screen. Held work is on the phone, in the farmer's
  /// own list of claims in progress; it is not in the send queue and is not
  /// counted as waiting to go.
  static const statusHolding = 'holding';

  /// A claim this phone filed that the office no longer has.
  ///
  /// # Why this status exists
  ///
  /// [filedClaimFor] stops a farmer walking a field twice for one calamity, by
  /// matching any draft for that event and parcel which has left `holding`. It
  /// cannot know the office's copy was deleted underneath it -- and a deleted
  /// claim then blocked that handset from ever filing on that parcel again. It
  /// happened the first time a test claim was cleared at the office: the phone
  /// answered "नोंद झाली आहे" to a claim nothing had a record of.
  ///
  /// Not `holding`, which would say the claim is the farmer's to send and put it
  /// back in the queue. This says the opposite: it was sent, the office does not
  /// have it, and nothing is owed either way.
  static const statusWithdrawn = 'withdrawn';

  /// Put a held claim into the queue. What "send" means.
  Future<void> releaseDraft(String localId) async {
    await _db.update(
      'drafts',
      {
        'status': 'pending',
        'attempts': 0,
        'next_attempt_at': 0,
        'last_error': null,
        'updated_at': DateTime.now().millisecondsSinceEpoch,
      },
      where: 'local_id = ? AND status = ?',
      whereArgs: [localId, statusHolding],
    );
  }

  /// How many claims have been sitting unfinished for longer than [age].
  ///
  /// A held claim is one the farmer filled in and has not completed the
  /// evidence for, so it is not in the queue and nothing will ever send it.
  /// The only thing that can say so is this phone -- see reminders.dart.
  ///
  /// Measured on `updated_at`, which moves every time the farmer adds a
  /// photograph: somebody working through the walk over an afternoon is not
  /// somebody who has forgotten.
  /// [now] is the instant to measure from, for tests that cannot wait a day.
  Future<int> heldSince(Duration age, {String? owner, DateTime? now}) async {
    final (scope, args) = _scope(owner);
    final before = (now ?? DateTime.now()).subtract(age).millisecondsSinceEpoch;
    final rows = await _db.rawQuery(
      'SELECT count(*) AS n FROM drafts '
      'WHERE status = ? AND updated_at <= ? AND server_id IS NULL AND $scope',
      [statusHolding, before, ...args],
    );
    return (rows.first['n'] as int?) ?? 0;
  }

  /// Drafts that have not reached the office.
  ///
  /// `server_id IS NULL` rather than a status, because status is what this
  /// app believes and server_id is what actually happened.
  Future<List<Map<String, Object?>>> pendingDrafts({
    DateTime? now,
    String? owner,
  }) {
    final (w, a) = _scope(owner);
    return _db.query(
      'drafts',
      where:
          "server_id IS NULL AND status NOT IN ('failed', 'holding') "
          'AND $_due AND $w',
      whereArgs: [..._dueArgs(now), ...a],
      orderBy: 'created_at',
    );
  }

  Future<void> deleteDraft(String localId) =>
      _db.delete('drafts', where: 'local_id = ?', whereArgs: [localId]);

  // --- captures -----------------------------------------------------------

  Future<void> putCapture(Map<String, Object?> capture) async {
    final now = DateTime.now().millisecondsSinceEpoch;
    await _upsert('captures', 'local_id', capture['local_id'] as Object, {
      ...capture,
      'created_at': capture['created_at'] ?? now,
      'updated_at': now,
    });
  }

  Future<List<Map<String, Object?>>> capturesFor(String draftLocalId) =>
      _db.query(
        'captures',
        where: 'draft_local_id = ?',
        whereArgs: [draftLocalId],
        orderBy: 'created_at',
      );

  /// Write down a fault, keeping only the newest [Faults.keep].
  ///
  /// Trimmed on write rather than by a sweep: an unbounded table on a 2 GB
  /// handset is its own defect, and a fault storm is exactly when nobody is
  /// running a sweep.
  Future<void> recordFault({
    required String where,
    required String message,
    required String stack,
    int keep = 50,
  }) async {
    await _db.insert('faults', {
      'where_at': where,
      'message': message,
      'stack': stack,
      'at': DateTime.now().millisecondsSinceEpoch,
    });
    await _db.rawDelete(
      'DELETE FROM faults WHERE id NOT IN '
      '(SELECT id FROM faults ORDER BY at DESC, id DESC LIMIT ?)',
      [keep],
    );
  }

  /// The faults on this phone, newest first, for somebody who can read them.
  Future<List<Map<String, Object?>>> faults({int limit = 50}) =>
      _db.query('faults', orderBy: 'at DESC, id DESC', limit: limit);

  /// Forget them. Called when the farmer signs out with nothing waiting: a
  /// fault log is about this app on this phone, not about a person, but there is
  /// no reason to keep one past the session that produced it.
  Future<void> clearFaults() => _db.delete('faults');

  /// Evidence still to be sent for claims the farmer has finished filing.
  ///
  /// The count the sign-out guard turns on. It delegates to [outstanding] rather
  /// than asking its own question, because [_outstandingWhere] already draws
  /// exactly this line -- an unsent capture whose draft is not still being
  /// gathered -- and a second copy of that predicate would be a second thing to
  /// keep correct. The first version of this method did repeat the SQL, and the
  /// two were already identical the day it was written.
  ///
  /// What the name adds is the intent, and the scope. A draft still being
  /// gathered is work the farmer may be abandoning, and holding a borrowed
  /// handset over it would be wrong; a filed claim is one where the farmer has
  /// done everything asked of them and only the evidence is missing. Across
  /// every farmer this account acts for, because the account is what sends.
  Future<int> unsentFiledEvidence({String? owner}) async {
    final waiting = await outstanding(owner: owner, only: everyone);
    return waiting.captures;
  }

  Future<List<Map<String, Object?>>> pendingCaptures({
    DateTime? now,
    String? owner,
  }) {
    final (w, a) = _scope(owner);
    return _db.query(
      'captures',
      where:
          "status NOT IN ('sent', 'failed') AND $_due AND $w "
          // A photograph whose claim is still being gathered goes when the
          // claim does. Sending it earlier would put evidence at the office
          // for a report that has not been filed.
          "AND draft_local_id NOT IN "
          "(SELECT local_id FROM drafts WHERE status = 'holding')",
      whereArgs: [..._dueArgs(now), ...a],
      orderBy: 'created_at',
    );
  }

  // --- boundaries ---------------------------------------------------------

  Future<void> putBoundary({
    required String farmId,
    required String surveyNumber,
    required List<Map<String, Object?>> points,
    required double areaHa,
    required double worstAccuracyM,
    String status = 'pending',
    int attempts = 0,
    String? lastError,
    int? nextAttemptAt,

    /// Whose parcel this is. Defaults to the farmer on screen, which is who
    /// walked it. Without it every farmer on the handset counted every
    /// boundary as their own work.
    String? farmerId,
  }) async {
    final now = DateTime.now().millisecondsSinceEpoch;
    final existing = await boundary(farmId);
    final encoded = jsonEncode(points);
    // Walking the field again is a correction, which is a new request to the
    // server. Sent under the first walk's key it would be answered with the
    // first walk. Keeping the key for anything else -- recording an attempt,
    // marking it sent -- is what makes a retry the same request.
    final walkedAgain = existing != null && existing['points'] != encoded;
    await _upsert('boundaries', 'farm_id', farmId, {
      if (walkedAgain) 'sync_key': newSyncKey(),
      'next_attempt_at': ?nextAttemptAt,
      'farm_id': farmId,
      'survey_number': surveyNumber,
      'points': encoded,
      'farmer_id': farmerId ?? farmer,
      'area_ha': areaHa,
      'worst_accuracy_m': worstAccuracyM,
      'status': status,
      'attempts': attempts,
      'last_error': lastError,
      'created_at': existing?['created_at'] ?? now,
      'updated_at': now,
    });
  }

  Future<Map<String, Object?>?> boundary(String farmId) async {
    final rows = await _db.query(
      'boundaries',
      where: 'farm_id = ?',
      whereArgs: [farmId],
      limit: 1,
    );
    return rows.isEmpty ? null : rows.first;
  }

  Future<List<Map<String, Object?>>> pendingBoundaries({
    DateTime? now,
    String? owner,
  }) {
    final (w, a) = _scope(owner);
    return _db.query(
      'boundaries',
      where: "status NOT IN ('sent', 'failed') AND $_due AND $w",
      whereArgs: [..._dueArgs(now), ...a],
      orderBy: 'created_at',
    );
  }

  /// Every photograph file this database still refers to, in any state.
  ///
  /// Deliberately not filtered by status: a sent capture's file is deleted
  /// when it is sent, and a failed one's file is the farmer's only copy of
  /// evidence an officer may yet ask about. Both are still referenced, and
  /// anything not in this list is referenced by nothing.
  Future<List<String>> allCaptureFilePaths() async {
    final rows = await _db.query('captures', columns: ['file_path']);
    return [
      for (final row in rows)
        if (row['file_path'] is String) row['file_path'] as String,
    ];
  }

  // --- appeals -------------------------------------------------------------

  Future<void> putAppeal(Map<String, Object?> row) async {
    final now = DateTime.now().millisecondsSinceEpoch;
    await _upsert('appeals', 'local_id', row['local_id'] as Object, {
      ...row,
      'created_at': row['created_at'] ?? now,
      'updated_at': now,
    });
  }

  Future<List<Map<String, Object?>>> pendingAppeals({
    DateTime? now,
    String? owner,
  }) {
    final (w, a) = _scope(owner);
    return _db.query(
      'appeals',
      where: "server_id IS NULL AND status <> 'failed' AND $_due AND $w",
      whereArgs: [..._dueArgs(now), ...a],
      orderBy: 'created_at',
    );
  }

  /// Due now -- or due so far in the future that the clock must have moved.
  ///
  /// A farmer who sets the phone's clock back a day, or a handset whose clock
  /// was wrong when it last synced, would otherwise leave items waiting until a
  /// time that is really yesterday plus a day. No legitimate wait is longer
  /// than the retry cap, so anything further off is treated as due.
  static const _due = '(next_attempt_at <= ? OR next_attempt_at > ?)';

  static List<Object> _dueArgs(DateTime? now) {
    final t = (now ?? DateTime.now()).millisecondsSinceEpoch;
    return [t, t + const Duration(hours: 7).inMilliseconds];
  }

  /// The earliest moment anything waiting may be tried, or null if nothing
  /// is waiting. What the background scheduler is asked to wake for.
  Future<DateTime?> nextDueAt({String? owner}) async {
    final (w, a) = _scope(owner);
    int? earliest;
    for (final (table, where) in _outstandingWhere) {
      final rows = await _db.rawQuery(
        'SELECT MIN(next_attempt_at) AS t FROM $table WHERE $where AND $w',
        a,
      );
      final t = rows.first['t'] as int?;
      if (t != null && (earliest == null || t < earliest)) earliest = t;
    }
    return earliest == null
        ? null
        : DateTime.fromMillisecondsSinceEpoch(earliest);
  }

  /// When the oldest item still waiting was queued. The number an operator
  /// uses to find a stuck phone.
  Future<DateTime?> oldestPendingAt({String? owner}) async {
    final (w, a) = _scope(owner);
    int? oldest;
    for (final (table, where) in _outstandingWhere) {
      final rows = await _db.rawQuery(
        'SELECT MIN(created_at) AS t FROM $table WHERE $where AND $w',
        a,
      );
      final t = rows.first['t'] as int?;
      if (t != null && (oldest == null || t < oldest)) oldest = t;
    }
    return oldest == null ? null : DateTime.fromMillisecondsSinceEpoch(oldest);
  }

  /// Bytes of photographs and clips still to go.
  Future<int> pendingBytes({String? owner}) async {
    final (w, a) = _scope(owner);
    final rows = await _db.rawQuery(
      'SELECT COALESCE(SUM(byte_size - sent_bytes), 0) AS n FROM captures '
      "WHERE status NOT IN ('sent', 'failed') AND $w",
      a,
    );
    return (rows.first['n'] as int?) ?? 0;
  }

  /// How far the evidence still queued has got, in bytes.
  ///
  /// # Why bytes and not a count of files
  ///
  /// Because a count barely moves. A claim is four clips and a form, and on a
  /// village connection the first clip alone can take twenty minutes -- during
  /// which "5 waiting to send" says "5 waiting to send" the entire time, and a
  /// farmer watching it concludes nothing is happening and starts pressing
  /// things. The chunked uploader acknowledges every half-megabyte, so the bytes
  /// do move, and they are what the farmer is actually waiting for.
  ///
  /// Refused items are left out of both halves. They are not going anywhere, and
  /// a bar that can never fill is worse than no bar.
  Future<({int sent, int total})> uploadProgress({String? owner}) async {
    final (w, a) = _scope(owner);
    final rows = await _db.rawQuery(
      'SELECT COALESCE(SUM(sent_bytes), 0) AS sent, '
      '       COALESCE(SUM(byte_size), 0) AS total FROM captures '
      "WHERE status NOT IN ('sent', 'failed') AND $w",
      a,
    );
    return (
      sent: (rows.first['sent'] as int?) ?? 0,
      total: (rows.first['total'] as int?) ?? 0,
    );
  }

  /// Items the office refused, across the queue.
  Future<int> failedCount({String? owner}) async {
    final (w, a) = _scope(owner);
    var n = 0;
    for (final table in queueTables) {
      final rows = await _db.rawQuery(
        "SELECT COUNT(*) AS n FROM $table WHERE status = 'failed' AND $w",
        a,
      );
      n += (rows.first['n'] as int?) ?? 0;
    }
    return n;
  }

  /// Every table's "still to be sent", regardless of when it is due.
  static const _outstandingWhere = [
    ('drafts', "server_id IS NULL AND status NOT IN ('failed', 'holding')"),
    // A photograph whose claim is still being gathered is not waiting to be
    // sent; it is part of what the farmer is still collecting. Counting it
    // would put a queue card on the home screen for work nobody has finished.
    (
      'captures',
      "status NOT IN ('sent', 'failed') AND draft_local_id NOT IN "
          "(SELECT local_id FROM drafts WHERE status = 'holding')",
    ),
    ('boundaries', "status NOT IN ('sent', 'failed')"),
    ('appeals', "server_id IS NULL AND status <> 'failed'"),
  ];

  // --- the device ----------------------------------------------------------

  /// This install's identity, created on first use and kept across sign-outs.
  Future<String> deviceId() async {
    final rows = await _db.query(
      'device',
      where: 'key = ?',
      whereArgs: ['device_id'],
      limit: 1,
    );
    if (rows.isNotEmpty) return rows.first['value'] as String;
    final id = 'dev-${newSyncKey().substring(1)}';
    await _db.insert('device', {
      'key': 'device_id',
      'value': id,
    }, conflictAlgorithm: ConflictAlgorithm.ignore);
    // Read back rather than returned, so two isolates asking at once agree.
    final again = await _db.query(
      'device',
      where: 'key = ?',
      whereArgs: ['device_id'],
      limit: 1,
    );
    return again.first['value'] as String;
  }

  /// Every appeal on this phone, newest first, so a farmer can see that the
  /// objection they made is recorded even before it has been sent.
  Future<List<Map<String, Object?>>> appeals({String? owner}) {
    final (w, a) = _scopeIn('appeals', owner: owner);
    return _db.query(
      'appeals',
      where: w,
      whereArgs: a,
      orderBy: 'created_at DESC',
    );
  }

  /// Whether this report already has an objection on this phone.
  ///
  /// The server allows one open grievance at a time and refuses a second.
  /// Checking here means a farmer is told before they write one out, rather
  /// than after they have queued it and it fails days later.
  Future<bool> hasAppeal(String reportID) async {
    final rows = await _db.query(
      'appeals',
      where: 'report_id = ?',
      whereArgs: [reportID],
      limit: 1,
    );
    return rows.isNotEmpty;
  }

  // --- the sync lease ------------------------------------------------------

  /// The key the lease lives under. Underscored so it cannot collide with a
  /// cached endpoint, and inside the cache table so [clearPersonalData] takes
  /// it along with everything else on a sign-out.
  static const _leaseKey = '__sync_lease';

  /// Claim the exclusive right to run a sync pass, device-wide.
  ///
  /// # Why this is not just a bool in the Sync object
  ///
  /// There are two isolates. The app has one and the background worker has
  /// another, they cannot see each other's memory, and they open the same
  /// database file. A farmer who has the app open when the worker fires has
  /// two sync passes reading the same queue -- and the second one sends every
  /// photograph the first one is still uploading. The office receives each
  /// piece of evidence twice, from the same farmer, with the same timestamp,
  /// which is exactly what a fraud check is built to notice.
  ///
  /// So the lock has to live where both isolates can see it, and the only
  /// thing they share is the database.
  ///
  /// # Why one statement
  ///
  /// Read-then-write would be two statements with a gap between them, and the
  /// gap is the whole problem. `ON CONFLICT ... WHERE` makes the test and the
  /// claim a single atomic statement: SQLite takes its write lock once, and
  /// exactly one caller comes back with a row changed.
  Future<bool> claimSyncLease({
    Duration ttl = const Duration(minutes: 15),
  }) async {
    final now = DateTime.now().millisecondsSinceEpoch;

    // A lease older than the ttl belongs to an isolate Android killed without
    // warning -- which it does routinely. Without an expiry that one death
    // would stop this phone syncing forever.
    final stale = now - ttl.inMilliseconds;

    final changed = await _db.rawUpdate(
      'INSERT INTO cache(key, value, updated_at) VALUES(?, ?, ?) '
      'ON CONFLICT(key) DO UPDATE SET updated_at = excluded.updated_at '
      'WHERE cache.updated_at <= ?',
      [_leaseKey, '"held"', now, stale],
    );

    return changed > 0;
  }

  /// Push the lease's expiry forward, called as each item goes.
  ///
  /// A pass with thirty photographs on a village connection can outlast any
  /// ttl worth setting, and a lease that expires underneath a pass that is
  /// still running is the same double-send it exists to prevent. Renewing per
  /// item means the ttl only has to cover one upload, not all of them.
  Future<void> touchSyncLease() async {
    await _db.update(
      'cache',
      {'updated_at': DateTime.now().millisecondsSinceEpoch},
      where: 'key = ?',
      whereArgs: [_leaseKey],
    );
  }

  Future<void> releaseSyncLease() async {
    await _db.delete('cache', where: 'key = ?', whereArgs: [_leaseKey]);
  }

  // --- cache --------------------------------------------------------------

  Future<void> put(String key, Object? value) async {
    await _upsert('cache', 'key', key, {
      'key': key,
      'value': jsonEncode(value),
      'updated_at': DateTime.now().millisecondsSinceEpoch,
    });
  }

  Future<T?> get<T>(String key) async {
    final rows = await _db.query(
      'cache',
      where: 'key = ?',
      whereArgs: [key],
      limit: 1,
    );
    if (rows.isEmpty) return null;
    return jsonDecode(rows.first['value'] as String) as T?;
  }

  /// When a cached value was written, so a screen can say how old it is.
  ///
  /// A farmer looking at a list of open events with no signal is entitled to
  /// know it is yesterday's list rather than today's.
  Future<DateTime?> cachedAt(String key) async {
    final rows = await _db.query(
      'cache',
      columns: ['updated_at'],
      where: 'key = ?',
      whereArgs: [key],
      limit: 1,
    );
    if (rows.isEmpty) return null;
    return DateTime.fromMillisecondsSinceEpoch(rows.first['updated_at'] as int);
  }

  // --- sign-out -----------------------------------------------------------

  /// Everything that belongs to a person.
  ///
  /// A handset in a village is borrowed. One farmer's photographs must not be
  /// sitting in the app when the next person opens it, and their queue must
  /// not silently upload under somebody else's session.
  /// Remove everything belonging to the farmer who was signed in.
  ///
  /// This deletes rows. It does not delete the photograph files, because this
  /// file has no business knowing where they live -- but they must go too, and
  /// on a borrowed handset that is the whole point of signing out. With every
  /// row gone each file is an orphan, so `Photos.sweepOrphans` removes all of
  /// them; the caller runs it immediately rather than waiting for the sweep at
  /// next startup, because the next person may pick the phone up before then.
  Future<void> clearPersonalData() async {
    await _db.transaction((t) async {
      await t.delete('appeals');
      await t.delete('captures');
      await t.delete('drafts');
      await t.delete('boundaries');
      await t.delete('journeys');
      await t.delete('farmers');
      await t.delete('cache');
    });
  }

  // --- what gave up, and what was never finished ---------------------------

  /// Items the office refused, and what the farmer should be shown.
  ///
  /// # Why this has to be asked separately
  ///
  /// [outstanding] counts what is still being tried, and a refused item is not.
  /// Without this the queue card would disappear the moment the office said
  /// no, and a farmer would conclude the report went. That silence -- reading
  /// as success -- is the worst outcome this app can produce.
  ///
  /// Only refusals land here now. Work the office could not take right now is
  /// never abandoned; it waits and tries again, and stays in [outstanding].
  ///
  /// A clip refused over where it was recorded is not counted either. It is a
  /// refusal, but not one this card's button can do anything about, and showing
  /// it here offered the farmer a retry that is now deliberately a no-op.
  /// Those have their own card, from [capturesToRecordAgain].
  Future<Outstanding> stuck({String? owner, String? only}) async {
    Future<int> count(String table) async {
      final (w, a) = _scopeIn(table, owner: owner, only: only);
      final held = table == 'captures'
          ? ' AND (last_error_code IS NULL OR last_error_code NOT IN '
                '($_recordAgainList))'
          : '';
      final rows = await _db.rawQuery(
        "SELECT COUNT(*) AS n FROM $table WHERE status = 'failed'$held AND $w",
        a,
      );
      return (rows.first['n'] as int?) ?? 0;
    }

    return Outstanding(
      drafts: await count('drafts'),
      captures: await count('captures'),
      boundaries: await count('boundaries'),
      appeals: await count('appeals'),
    );
  }

  /// Everything still waiting becomes due now.
  ///
  /// For the farmer who presses "try now": they are looking at the screen and
  /// have just walked into signal, which the backoff cannot know. Attempts are
  /// kept, so if the office is still failing the next wait is as long as it
  /// would have been.
  Future<void> makeDue({String? owner}) async {
    final (w, a) = _scope(owner);
    for (final table in queueTables) {
      await _db.update(
        table,
        {'next_attempt_at': 0},
        where: "status NOT IN ('sent', 'failed') AND $w",
        whereArgs: a,
      );
    }
  }

  /// Put everything refused back in the queue, when the farmer asks.
  ///
  /// A refusal is usually final, but not always: a boundary corrected since,
  /// a report the office reopened, a photograph the farmer walked back into
  /// the field to retake. The farmer has information the refusal did not.
  ///
  /// # What is held back
  ///
  /// A clip the office refused over where it was recorded. Those bytes carry
  /// the position with them, so sending them again asks the same question and
  /// gets the same answer -- and the farmer, who has been given a button, taps
  /// it. Three clips of one claim went round that loop, and no number of taps
  /// could have ended it. They are offered the camera instead, by
  /// [capturesToRecordAgain].
  Future<void> retryStuck({String? owner}) async {
    final (w, a) = _scope(owner);
    for (final table in queueTables) {
      final held = table == 'captures'
          ? " AND (last_error_code IS NULL OR last_error_code NOT IN "
                '($_recordAgainList))'
          : '';
      await _db.update(
        table,
        {'attempts': 0, 'status': 'pending', 'next_attempt_at': 0},
        where: "status = 'failed'$held AND $w",
        whereArgs: a,
      );
    }
  }

  /// The refusal codes of [recordAgainRefusals], as a SQL list.
  ///
  /// Built from the Dart set rather than written out again, so a code added
  /// there cannot be missed here -- which would put a clip back in the queue
  /// that the office will refuse for ever.
  static final _recordAgainList = recordAgainRefusals
      .map((c) => "'$c'")
      .join(', ');

  /// Clips the office refused over where they were recorded, by claim.
  ///
  /// These are the farmer's to fix, and the only fix is the camera: walk to a
  /// different part of the field and record that point again. Grouped by the
  /// claim they belong to, because that is what the walk reopens.
  Future<List<RecordAgain>> capturesToRecordAgain({String? owner}) async {
    final (w, a) = _scopeIn('captures', owner: owner);
    final rows = await _db.rawQuery(
      'SELECT draft_local_id, station_no, local_id, last_error, '
      '       last_error_code '
      "  FROM captures WHERE status = 'failed' "
      "   AND last_error_code IN ($_recordAgainList) AND $w "
      '  ORDER BY station_no',
      a,
    );

    final byClaim = <String, List<Map<String, Object?>>>{};
    for (final row in rows) {
      byClaim.putIfAbsent('${row['draft_local_id']}', () => []).add(row);
    }
    return [
      for (final entry in byClaim.entries)
        RecordAgain(
          draftLocalId: entry.key,
          // A clip taken outside the directed walk has no station number.
          // Nulls are dropped rather than becoming point zero: the walk is
          // reopened at points, and a point that does not exist cannot be one.
          stations: [
            for (final row in entry.value)
              if (row['station_no'] != null) row['station_no'] as int,
          ],
          localIds: [for (final row in entry.value) '${row['local_id']}'],
          reason: '${entry.value.first['last_error'] ?? ''}',
          code: '${entry.value.first['last_error_code'] ?? ''}',
        ),
    ];
  }

  /// Forget the refused clip of one point, now that a replacement exists.
  ///
  /// # Why this is not done when the walk opens
  ///
  /// It was, for one build, and it cost a farmer three recordings. Deleting the
  /// refused rows up front is what made [stationsComplete] send the walk to
  /// those points -- and a farmer who opened the walk and closed it without
  /// recording was left with nothing: no clips, no card, no sign anything had
  /// ever been wrong. The office kept the one clip it had already accepted and
  /// the claim was assessed on it.
  ///
  /// So nothing is thrown away until something has replaced it. The refused
  /// clip is what keeps the point unstanding and keeps the card on the screen;
  /// both of those are load-bearing until the farmer has actually recorded
  /// again.
  Future<void> replaceRefusedAt(String draftLocalId, int stationNo) async {
    final rows = await _db.rawQuery(
      'SELECT local_id FROM captures '
      '  WHERE draft_local_id = ? AND station_no = ? '
      "    AND status = 'failed' "
      '    AND last_error_code IN ($_recordAgainList)',
      [draftLocalId, stationNo],
    );
    await _forgetCaptures([for (final r in rows) '${r['local_id']}']);
  }

  /// Delete capture rows and the files they point at.
  Future<void> _forgetCaptures(List<String> localIds) async {
    for (final id in localIds) {
      final rows = await _db.query(
        'captures',
        columns: ['file_path'],
        where: 'local_id = ?',
        whereArgs: [id],
      );
      for (final row in rows) {
        final path = '${row['file_path'] ?? ''}';
        if (path.isEmpty) continue;
        try {
          final file = File(path);
          if (file.existsSync()) file.deleteSync();
        } catch (_) {
          // The row goes regardless. A file that cannot be deleted is the
          // orphan sweep's problem, and keeping the row to match it would keep
          // the farmer's claim stuck instead.
        }
      }
      await _db.delete('captures', where: 'local_id = ?', whereArgs: [id]);
    }
  }

  /// Every local draft with the number of photographs attached to it.
  ///
  /// A draft with too few is one the farmer was interrupted in the middle of
  /// -- a flat battery, a dropped phone, a back press. Without this there is
  /// no route back into the capture sequence at all: it is only ever reached
  /// by pushing forward from the report form, so the walk to the field is
  /// simply lost.
  ///
  /// A LEFT JOIN rather than a count per draft, because this runs on every
  /// home screen build.
  Future<List<({Map<String, Object?> draft, int photographs})>>
  draftsWithPhotographs({String? owner}) async {
    final (w, a) = _scope(owner);
    final rows = await _db.rawQuery(
      'SELECT d.*, COUNT(c.local_id) AS photographs '
      'FROM drafts d LEFT JOIN captures c '
      '  ON c.draft_local_id = d.local_id '
      'WHERE ${w.replaceAll('owner_id', 'd.owner_id')} '
      'GROUP BY d.local_id '
      'ORDER BY d.local_id DESC',
      a,
    );

    return [
      for (final row in rows)
        (
          draft: {
            for (final e in row.entries)
              if (e.key != 'photographs') e.key: e.value,
          },
          photographs: (row['photographs'] as int?) ?? 0,
        ),
    ];
  }

  // --- journeys -------------------------------------------------------------

  /// Create or update a claim in progress. JSON-able values are encoded.
  Future<void> putJourney(Map<String, Object?> journey) async {
    final now = DateTime.now().millisecondsSinceEpoch;
    Object? enc(Object? v) => v == null || v is String ? v : jsonEncode(v);
    final id = journey['local_id'] as String;
    // Saved on every keystroke; when the claim was started must not move.
    final existing = await this.journey(id);
    await _upsert('journeys', 'local_id', id, {
      for (final e in journey.entries)
        e.key:
            const {
              'form',
              'event',
              'parcel',
              'stations',
              'taken_points',
            }.contains(e.key)
            ? enc(e.value)
            : e.value,
      'created_at': journey['created_at'] ?? existing?['created_at'] ?? now,
      'updated_at': now,
    });
  }

  Future<Map<String, Object?>?> journey(String localId) async {
    final rows = await _db.query(
      'journeys',
      where: 'local_id = ?',
      whereArgs: [localId],
      limit: 1,
    );
    return rows.isEmpty ? null : rows.first;
  }

  /// Claims started and not finished, newest first.
  /// Claims the farmer still has something to do about.
  ///
  /// A journey marked 'done' whose draft is still holding is included, and
  /// deliberately. 'done' means sent, and only the review screen sends; a
  /// version of the home screen marked any journey with complete evidence
  /// 'done' without releasing its draft, which left claims that were walked
  /// in full, never queued, and never shown again. Those handsets still hold
  /// them, so this is how they come back rather than being walked twice.
  Future<List<Map<String, Object?>>> openJourneys({String? owner}) {
    final (w, a) = _scopeIn('journeys', owner: owner);
    // A subquery rather than a join: the scope clause names bare columns, and
    // drafts carries a farmer_id and an owner_id of its own, so a join makes
    // every one of them ambiguous.
    return _db.query(
      'journeys',
      where:
          "(stage IN ('form', 'evidence', 'review') "
          "  OR (stage = 'done' AND local_id IN "
          "      (SELECT local_id FROM drafts WHERE status = '$statusHolding'))) "
          'AND $w',
      whereArgs: a,
      orderBy: 'updated_at DESC',
    );
  }

  /// The claim already filed for this event and parcel, if there is one.
  ///
  /// One loss, one claim: a farmer who walks the same field twice for the
  /// same calamity files twice, and the office then assesses one field twice
  /// and pays it twice. The server refuses the second (ALREADY_FILED), but
  /// only after the walk -- so this is how the app declines to send somebody
  /// back into their field for nothing.
  ///
  /// Matched on what this handset sent. A claim filed at a counter or from
  /// another phone is not here, and the server's refusal is what catches it.
  Future<Map<String, Object?>?> filedClaimFor({
    required String eventId,
    required String farmId,
    String? owner,
  }) async {
    final (w, a) = _scopeIn('drafts', owner: owner);
    final rows = await _db.query(
      'drafts',
      where:
          'survey_event_id = ? AND farm_id = ? '
          "AND status NOT IN ('holding', '$statusWithdrawn') AND $w",
      whereArgs: [eventId, farmId, ...a],
      orderBy: 'updated_at DESC',
      limit: 1,
    );
    return rows.isEmpty ? null : rows.first;
  }

  /// Record that the office does not have a claim this phone filed.
  ///
  /// Only ever called with the office's own answer in hand -- a definite "no such
  /// report", not a failed request. Guessing it from silence would let a farmer
  /// file a duplicate of a claim that does exist, which the office refuses and
  /// which costs them the walk.
  Future<void> forgetFiledClaim(String localId) => _db.update(
    'drafts',
    {
      'status': statusWithdrawn,
      'updated_at': DateTime.now().millisecondsSinceEpoch,
    },
    where: 'local_id = ?',
    whereArgs: [localId],
  );

  /// The unfinished claim against this event and parcel, if there is one.
  ///
  /// So choosing a parcel already being claimed continues that claim rather
  /// than starting a second.
  Future<Map<String, Object?>?> openJourneyFor({
    required String eventId,
    required String farmId,
    String? owner,
  }) async {
    final (w, a) = _scopeIn('journeys', owner: owner);
    final rows = await _db.query(
      'journeys',
      where:
          "stage IN ('form', 'evidence', 'review') AND event_id = ? AND farm_id = ? "
          'AND $w',
      whereArgs: [eventId, farmId, ...a],
      orderBy: 'updated_at DESC',
      limit: 1,
    );
    return rows.isEmpty ? null : rows.first;
  }

  Future<void> setJourneyStage(String localId, String stage) => _db.update(
    'journeys',
    {'stage': stage, 'updated_at': DateTime.now().millisecondsSinceEpoch},
    where: 'local_id = ?',
    whereArgs: [localId],
  );

  Future<void> deleteJourney(String localId) =>
      _db.delete('journeys', where: 'local_id = ?', whereArgs: [localId]);

  /// Which assigned points have their clip.
  ///
  /// The clip, and only the clip. A still used to be required too -- the walk
  /// recorded both at each point and a point was not done until both had
  /// saved. The still is gone: the frame the report prints is taken out of the
  /// clip by the office, so there is no second file that can fail to save and
  /// no way for the two to disagree about where they were recorded.
  /// A point the office refused over where the clip was taken is not done.
  ///
  /// That is what lets the walk be reopened without destroying anything. The
  /// refused clip stays on the phone until a replacement is actually recorded:
  /// the walk resumes at the first point with nothing standing, finds this one
  /// unstanding, and sends the farmer there.
  ///
  /// It was the other way round for one build, and it cost three recordings.
  /// The rows were deleted up front so this query would skip those points, and
  /// a farmer who opened the walk and closed it without recording was left with
  /// no clips, no card, and no sign that anything had ever been wrong.
  Future<Set<int>> stationsComplete(String draftLocalId) async {
    final rows = await _db.rawQuery(
      'SELECT station_no FROM captures '
      'WHERE draft_local_id = ? AND station_no IS NOT NULL '
      "  AND content_type LIKE 'video/%' "
      '  AND (last_error_code IS NULL OR last_error_code NOT IN '
      "      ($_recordAgainList)) "
      'GROUP BY station_no',
      [draftLocalId],
    );
    return {for (final r in rows) r['station_no'] as int};
  }

  /// Where each point of the walk was actually recorded.
  ///
  /// The walk's rule is that it moves: the next clip must be somewhere the
  /// walk has not already been. Answering that needs the positions the
  /// earlier clips were taken at, not the coordinates they were assigned --
  /// a farmer who could not reach the assigned spot and recorded thirty
  /// metres away has walked, and the next point is measured from where they
  /// actually stood.
  /// `?, ?, ?` for [n] bound values.
  ///
  /// So a set of codes defined once in Dart can be used in SQL without being
  /// written out a second time. Two copies of one list is how the app came to fence
  /// ground the office considers free.
  static String _placeholders(int n) => List.filled(n, '?').join(', ');

  Future<List<({int stationNo, double lat, double lon})>> recordedStationPoints(
    String draftLocalId,
  ) async {
    final rows = await _db.rawQuery(
      'SELECT station_no, lat, lon FROM captures '
      'WHERE draft_local_id = ? AND station_no IS NOT NULL '
      'AND lat IS NOT NULL AND lon IS NOT NULL '
      // Not the ones the office refused.
      //
      // The walk keeps a refused clip until a replacement exists, so the farmer
      // who backs out of a re-walk still has something -- but the office never
      // stored it, so from the office's side that ground was never filmed and is
      // free. Counting it here fenced the farmer off the one place they were
      // sent back to re-record, and on a small plot, where the spacing rule
      // already holds the points close together, off everywhere there was to
      // stand.
      //
      // The rule is that this must permit exactly what the office accepts. Being
      // stricter is not the safe direction: it does not prevent a refusal, it
      // prevents a recording.
      'AND (last_error_code IS NULL OR last_error_code NOT IN '
      '(${_placeholders(recordAgainRefusals.length)}))',
      [draftLocalId, ...recordAgainRefusals],
    );
    return [
      for (final r in rows)
        (
          stationNo: r['station_no'] as int,
          lat: (r['lat'] as num).toDouble(),
          lon: (r['lon'] as num).toDouble(),
        ),
    ];
  }

  /// Which assigned points already have their clip on this phone.
  ///
  /// A point is done when its video is saved. The still is written first, so a
  /// walk interrupted between the two returns to that point for the clip.
  Future<Set<int>> stationsWithVideo(String draftLocalId) async {
    final rows = await _db.rawQuery(
      'SELECT DISTINCT station_no FROM captures '
      "WHERE draft_local_id = ? AND station_no IS NOT NULL "
      "AND content_type LIKE 'video/%'",
      [draftLocalId],
    );
    return {for (final r in rows) r['station_no'] as int};
  }

  /// How much is still waiting to be sent.
  ///
  /// Counted in SQLite rather than by reading the rows and taking `.length`.
  /// This runs on every home screen refresh and after every sync pass, and
  /// the rows it would otherwise load include the whole of each queued draft.
  ///
  /// Everything not yet sent and not refused, whether or not it is due. An item
  /// waiting out a retry is still the farmer's work in flight, and the card
  /// saying so must not blink out between attempts.
  /// [only] narrows to one farmer, defaulting to the one on screen. Pass
  /// [everyone] for the whole account: the queue is not the screen, and work
  /// that stops being counted because somebody switched farmer is work the
  /// scheduler stops waking up for.
  Future<Outstanding> outstanding({String? owner, String? only}) async {
    Future<int> count(String table, String where) async {
      final (w, a) = _scopeIn(table, owner: owner, only: only);
      final rows = await _db.rawQuery(
        'SELECT COUNT(*) AS n FROM $table WHERE $where AND $w',
        a,
      );
      return (rows.first['n'] as int?) ?? 0;
    }

    final where = {for (final (t, w) in _outstandingWhere) t: w};
    return Outstanding(
      drafts: await count('drafts', where['drafts']!),
      captures: await count('captures', where['captures']!),
      boundaries: await count('boundaries', where['boundaries']!),
      appeals: await count('appeals', where['appeals']!),
    );
  }
}

/// A fresh idempotency key: 'q' and 32 hex digits from a secure source.
///
/// The server accepts 8 to 128 characters of letters, digits and a little
/// punctuation; this is well inside that, and too random to collide across
/// every phone in the state.
String newSyncKey() {
  final rnd = math.Random.secure();
  final b = StringBuffer('q');
  for (var i = 0; i < 16; i++) {
    b.write(rnd.nextInt(256).toRadixString(16).padLeft(2, '0'));
  }
  return b.toString();
}

class Outstanding {
  const Outstanding({
    required this.drafts,
    required this.captures,
    required this.boundaries,
    this.appeals = 0,
  });

  final int drafts;
  final int captures;
  final int boundaries;
  final int appeals;

  int get total => drafts + captures + boundaries + appeals;
  bool get any => total > 0;
}

/// The points of one claim the farmer has to record again.
///
/// The office refused these clips over where they were taken, which the bytes
/// carry with them, so nothing the queue does will get them accepted. What is
/// left is a walk: back to the field, to different points, with the camera.
class RecordAgain {
  const RecordAgain({
    required this.draftLocalId,
    required this.stations,
    required this.localIds,
    required this.reason,
    required this.code,
  });

  /// The claim these belong to. Already filed, in the case this was built for:
  /// the office has the report and is short of its evidence.
  final String draftLocalId;

  /// Which points of the walk to record again.
  final List<int> stations;

  /// The rows to forget once the farmer agrees to walk again. Kept separate
  /// from [stations] because a clip outside the directed walk has no point
  /// number but still has to go.
  final List<String> localIds;

  /// What the office said, in its own words, for the farmer to read.
  final String reason;

  /// What the office said, as a code, for the app to act on.
  final String code;

  int get count => localIds.length;
}
