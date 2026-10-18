-- Directed sampling, video evidence, and resumable upload.
--
-- Three changes that arrived together because they are the same change: the
-- app stops accepting whatever evidence a farmer offers and starts saying
-- where to stand, what to record there, and how a 48 MB walk-through gets off
-- a handset on a 2G connection.
--
-- See docs/FARMER_APP_REDESIGN.md.

-- ---------------------------------------------------------------------------
-- Video joins the evidence set
-- ---------------------------------------------------------------------------

-- Deliberately not used anywhere in this migration. Postgres will add an enum
-- value inside a transaction but refuses to let the same transaction use it,
-- and every migration here runs in one.
ALTER TYPE capture_kind ADD VALUE IF NOT EXISTS 'station_video';

-- A still cannot show what a walk-through shows: whether damage is uniform or
-- patchy across the plot, which way lodged crop fell, how far standing water
-- actually reaches. The vision model reads all three from motion and none of
-- them reliably from one frame.
ALTER TABLE survey_captures
    DROP CONSTRAINT survey_captures_content_type_check;

ALTER TABLE survey_captures
    ADD CONSTRAINT survey_captures_content_type_check
    CHECK (content_type IN ('image/jpeg', 'image/png', 'image/webp', 'video/mp4'));

ALTER TABLE survey_captures
    -- Null for a photograph. Enforced server-side rather than trusted from the
    -- handset: a 15 s cap that only the client applies is not a cap, and the
    -- upload budget is the whole reason the cap exists.
    ADD COLUMN duration_ms integer
        CHECK (duration_ms IS NULL OR duration_ms BETWEEN 1 AND 20000),

    -- Which of the four assigned points this evidences, and how far from it
    -- the farmer actually stood.
    --
    -- The offset is the honest part. A station can be moved when it cannot be
    -- reached -- water too deep to wade, a bund, a neighbour's crop -- and the
    -- farmer is not asked to justify that. But an officer reading "station 4,
    -- captured 40 m short" knows exactly what they are looking at, which is
    -- the whole value of having assigned the point in the first place.
    ADD COLUMN station_no integer CHECK (station_no BETWEEN 1 AND 4),
    ADD COLUMN station_offset_m double precision
        CHECK (station_offset_m IS NULL OR station_offset_m >= 0),

    -- Whether Android reported the fix as coming from a mock provider.
    --
    -- The geofence refuses captures outside the parcel. A fence that stops
    -- honest farmers and waves through anyone who installed a location-spoofing
    -- app would be worse than no fence at all, because it would look like a
    -- control while being none. Nullable: older clients do not send it, and
    -- absent must not read as false.
    ADD COLUMN is_mocked boolean;

COMMENT ON COLUMN survey_captures.station_offset_m IS
    'Metres between the assigned station and where the capture was actually taken. A few metres is normal GNSS scatter; tens of metres means the station was moved because it could not be reached.';

-- Captures taken well away from their assigned point, and captures from a
-- mocked fix, are the ones worth an officer looking at first.
CREATE INDEX survey_captures_station_moved_idx ON survey_captures (report_id)
    WHERE station_offset_m > 25 OR is_mocked;

-- ---------------------------------------------------------------------------
-- The four stations
-- ---------------------------------------------------------------------------

-- Where the farmer was told to stand, fixed before the walk began.
--
-- # Why the server places these and not the app
--
-- So the report can say the farmer was *directed* to these points rather than
-- choosing them. That sentence is what answers a selective-evidence challenge
-- at appeal -- a farmer who films only the waterlogged corner turns a 30% loss
-- into an 80% one, usually without meaning to, because the wet patch is what
-- feels worth showing. The sentence is only true if the points existed before
-- the walk, and only provable if the server wrote them down.
--
-- # Why they are frozen
--
-- Recomputing on every visit would mean the assigned points drifted while a
-- farmer walked between them, and "directed" would stop being true.
--
-- The exception is a boundary that has itself changed, which is why the
-- version is carried here. A station frozen against a corrected boundary can
-- end up outside the parcel, where the geofence will refuse the very capture
-- the app is directing the farmer to take. Stale stations are recomputed;
-- unchanged ones never move.
CREATE TABLE report_sampling_stations (
    report_id  uuid NOT NULL REFERENCES crop_loss_reports (id) ON DELETE CASCADE,
    station_no integer NOT NULL CHECK (station_no BETWEEN 1 AND 4),

    lat double precision NOT NULL CHECK (lat BETWEEN -90 AND 90),
    lon double precision NOT NULL CHECK (lon BETWEEN -180 AND 180),

    geom geometry(Point, 4326)
        GENERATED ALWAYS AS (ST_SetSRID(ST_MakePoint(lon, lat), 4326)) STORED,

    -- What this point is for, so the app can say it out loud in Marathi and
    -- the report can name it.
    --
    -- mid_left and mid_right are the two flanks: a strip parcel 100 m wide
    -- sampled only down its spine says nothing about either side, which is
    -- half of what "both sides of the farm" was asking for.
    role text NOT NULL CHECK (role IN ('low', 'mid_left', 'mid_right', 'high')),

    -- How the ends were decided.
    --
    -- 'terrain': the parcel has enough relief that aspect gives a real
    -- downhill, so station 1 is the low end where water collects and station 4
    -- is the high ground a farmer would not otherwise have filmed.
    --
    -- 'geometric': it does not, so the ends are simply the ends of the long
    -- axis. Around Barshi that is the common case -- a metre of relief across
    -- a whole parcel -- and it is not a degraded mode. The stations are the
    -- same four points either way; only which end is called "low" changes.
    placement text NOT NULL CHECK (placement IN ('terrain', 'geometric')),

    -- The boundary these were computed against. See the note above on
    -- freezing: this is what distinguishes stale from merely old.
    boundary_updated_at timestamptz NOT NULL,

    created_at timestamptz NOT NULL DEFAULT now(),

    PRIMARY KEY (report_id, station_no)
);

CREATE INDEX report_sampling_stations_geom_idx
    ON report_sampling_stations USING gist (geom);

-- ---------------------------------------------------------------------------
-- Resumable upload
-- ---------------------------------------------------------------------------

-- An upload in progress.
--
-- Four videos is about 48 MB: roughly twenty-five times the evidence payload
-- this platform was built around, moving over 2G from a field. The existing
-- path sends a capture in one request, so a connection that drops at 80%
-- starts again at zero -- which for a required 48 MB is not a slow upload, it
-- is a report that never completes and is retried until the attempt budget
-- runs out and the farmer is told their claim failed.
--
-- So bytes arrive in chunks, each one acknowledged, and a resumed pass
-- continues from the last chunk the server actually has.
CREATE TABLE capture_uploads (
    id        uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    report_id uuid NOT NULL REFERENCES crop_loss_reports (id) ON DELETE CASCADE,

    -- The handset's own key for this file, so a retry resumes the upload it
    -- started rather than beginning a second one. Without it, an app that
    -- retried after losing its reply would leave half-finished uploads on the
    -- server for every dropped connection.
    upload_key text NOT NULL,

    content_type text NOT NULL
        CHECK (content_type IN ('image/jpeg', 'image/png', 'image/webp', 'video/mp4')),
    total_bytes  bigint NOT NULL CHECK (total_bytes > 0),
    chunk_size   integer NOT NULL CHECK (chunk_size > 0),

    -- What has actually landed. next_chunk is what the client is told to send,
    -- and is the entire resume protocol: ask, get a number, send that chunk.
    received_bytes bigint NOT NULL DEFAULT 0 CHECK (received_bytes >= 0),
    next_chunk     integer NOT NULL DEFAULT 0 CHECK (next_chunk >= 0),

    -- Declared up front and verified when the last chunk lands. A file
    -- assembled from chunks that arrived over three hours across two network
    -- changes deserves to be checked before it is called evidence.
    sha256 text NOT NULL CHECK (sha256 ~ '^[0-9a-f]{64}$'),

    -- Where the partial file is being assembled. Not the final media path:
    -- an incomplete upload must never be reachable as a capture.
    temp_path text NOT NULL,

    -- The capture metadata, held until there is a file to attach it to. The
    -- geofence was already applied on the handset and is applied again when
    -- the upload completes; these are the readings it is judged on.
    kind        text NOT NULL,
    line_no     integer CHECK (line_no > 0),
    caption     text,
    station_no  integer CHECK (station_no BETWEEN 1 AND 4),
    captured_at timestamptz NOT NULL,
    lat         double precision NOT NULL CHECK (lat BETWEEN -90 AND 90),
    lon         double precision NOT NULL CHECK (lon BETWEEN -180 AND 180),
    accuracy_m  double precision,
    altitude_m  double precision,
    heading_deg double precision,
    duration_ms integer,
    is_mocked   boolean,
    device_label text,

    -- Set when the upload finishes and the capture row exists. Kept rather
    -- than deleted so a client that retries a completed upload is told "done"
    -- instead of being allowed to start it again.
    capture_id uuid REFERENCES survey_captures (id) ON DELETE SET NULL,

    started_by uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),

    -- An abandoned upload holds disk. A farmer who starts a video and never
    -- regains signal leaves one behind, and on a district-wide event there
    -- will be many.
    expires_at timestamptz NOT NULL DEFAULT now() + interval '7 days',

    UNIQUE (report_id, upload_key),

    -- Cannot have received more than was declared.
    CONSTRAINT capture_uploads_within_total CHECK (received_bytes <= total_bytes)
);

CREATE INDEX capture_uploads_report_idx ON capture_uploads (report_id);
CREATE INDEX capture_uploads_expiry_idx ON capture_uploads (expires_at)
    WHERE capture_id IS NULL;

CREATE TRIGGER capture_uploads_touch BEFORE UPDATE ON capture_uploads
    FOR EACH ROW EXECUTE FUNCTION touch_updated_at();

-- ---------------------------------------------------------------------------
-- Evidence completeness on the report
-- ---------------------------------------------------------------------------

-- How many of the four stations have their video.
--
-- Lifted onto the report because "is this claim ready to assess" is asked by
-- every worklist query, and answering it by joining captures and grouping by
-- station on every row is the kind of query that is fine until a district
-- files four thousand claims in a morning.
ALTER TABLE crop_loss_reports
    ADD COLUMN stations_captured integer NOT NULL DEFAULT 0
        CHECK (stations_captured BETWEEN 0 AND 4);

-- Folded into the existing counter rather than given a trigger of its own.
-- Two AFTER triggers on survey_captures would mean two UPDATEs of the same
-- report row for every photograph, and on a district-wide event that is a
-- doubled write on the hottest table in the system for no gain.
CREATE OR REPLACE FUNCTION refresh_report_capture_counts() RETURNS trigger AS $$
BEGIN
    UPDATE crop_loss_reports r
    SET capture_count = (
            SELECT count(*) FROM survey_captures c WHERE c.report_id = r.id),
        captures_flagged = (
            SELECT count(*) FROM survey_captures c
            WHERE c.report_id = r.id AND cardinality(c.gates_failed) > 0),
        -- Distinct stations with video on them. A farmer who records two
        -- clips at station 2 and none at station 3 has covered two stations,
        -- not three.
        stations_captured = (
            SELECT count(DISTINCT c.station_no) FROM survey_captures c
            WHERE c.report_id = r.id
              AND c.station_no IS NOT NULL
              AND c.content_type = 'video/mp4')
    WHERE r.id = COALESCE(NEW.report_id, OLD.report_id);
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;
