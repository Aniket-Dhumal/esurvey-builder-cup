-- Self-survey: the farmer's own evidence, and the parcel geometry it is
-- checked against.
--
-- This is the piece everything else has been waiting on. The assessment agent
-- refuses to state a damage percentage without ground imagery -- correctly,
-- because Sentinel-2 at 10 m over a one-hectare smallholding cannot measure
-- how badly a crop was hurt. Until now no ground imagery could exist, so
-- every assessment refused. This migration is what lets one arrive.
--
-- # Why the farmer captures it
--
-- A village pair cannot walk every parcel in a taluka after a hailstorm. The
-- farmer is already standing in the field; the question is only whether what
-- they send back can be trusted. That is what the gates below are for.
--
-- # What the gates can and cannot do
--
-- They make casual fraud hard and they record what they checked, so an officer
-- can weigh a capture rather than take it on faith. They do not make forgery
-- impossible -- a determined actor with a rooted handset can lie about
-- position. The honest posture is that this evidence supports a human
-- decision; it does not replace one.

CREATE EXTENSION IF NOT EXISTS postgis;

-- ---------------------------------------------------------------------------
-- Parcel boundaries
-- ---------------------------------------------------------------------------

-- Geometry, at last, in the database rather than in a JSON file.
--
-- It has to be here because the geofence is a spatial query: "is this
-- photograph inside the plot it claims to be of" is ST_Contains, and doing it
-- in Go over a decoded polygon would be slower, less correct at the edges, and
-- untestable in SQL.
CREATE TABLE parcel_boundaries (
    id      uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    farm_id text NOT NULL UNIQUE,

    farmer_id        text NOT NULL,
    survey_number    text NOT NULL,
    village_lgd_code text NOT NULL,

    geom geometry(MultiPolygon, 4326) NOT NULL,

    -- The polygon's own area. Not the owner's extent: a survey number is
    -- often jointly held, and the two numbers disagreeing is a fact about
    -- ownership rather than an error. See mock_files/README.md.
    polygon_area_ha numeric(12, 4),

    -- Where the boundary came from, carried on every derived figure.
    -- AgriStack returns plot_geometry: null for every farmer we can reach, so
    -- in practice this is 'mock' or 'field_traced' today.
    source text NOT NULL DEFAULT 'mock'
        CHECK (source IN ('agristack', 'mock', 'field_traced', 'imported')),

    captured_by uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),

    -- A boundary that is not a closed, valid ring will silently break every
    -- containment test run against it.
    CONSTRAINT boundary_is_valid CHECK (ST_IsValid(geom))
);

CREATE INDEX parcel_boundaries_geom_idx ON parcel_boundaries USING gist (geom);
CREATE INDEX parcel_boundaries_farmer_idx ON parcel_boundaries (farmer_id);
CREATE INDEX parcel_boundaries_village_idx ON parcel_boundaries (village_lgd_code);

CREATE TRIGGER parcel_boundaries_touch BEFORE UPDATE ON parcel_boundaries
    FOR EACH ROW EXECUTE FUNCTION touch_updated_at();

-- ---------------------------------------------------------------------------
-- Captures
-- ---------------------------------------------------------------------------

CREATE TYPE capture_kind AS ENUM (
    -- A wide view establishing the plot and its surroundings.
    'wide_shot',
    -- Close on the damage itself: lodging, shattering, submersion, scorch.
    'damage_closeup',
    -- The crop at close range, for identification and growth stage.
    'crop_closeup',
    -- A point on the boundary, for tracing a plot that has no geometry.
    'boundary_point'
);

CREATE TABLE survey_captures (
    id        uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    report_id uuid NOT NULL REFERENCES crop_loss_reports (id) ON DELETE CASCADE,

    -- Which crop line this evidences. Null for a capture of the whole visit.
    line_no integer CHECK (line_no > 0),

    kind capture_kind NOT NULL,
    caption text,

    -- --- the media ---------------------------------------------------------
    -- Stored on a volume, not in the row. A photograph is hundreds of
    -- kilobytes and no listing wants it; bytea would drag it through every
    -- query that touched the table. When object storage exists this becomes a
    -- key instead of a path and nothing else changes.
    media_path   text NOT NULL,
    content_type text NOT NULL CHECK (content_type IN ('image/jpeg', 'image/png', 'image/webp')),
    byte_size    integer NOT NULL CHECK (byte_size > 0),
    -- Content hash. Two captures of the same bytes are the same photograph
    -- submitted twice, which is worth being able to see.
    sha256 text NOT NULL CHECK (sha256 ~ '^[0-9a-f]{64}$'),

    -- --- where and when ----------------------------------------------------
    -- Taken from the device at the moment of capture, not from file metadata.
    -- EXIF is stripped by most browsers and is trivially editable; a live
    -- reading at least has to be lied about deliberately.
    captured_at timestamptz NOT NULL,
    lat         double precision NOT NULL CHECK (lat BETWEEN -90 AND 90),
    lon         double precision NOT NULL CHECK (lon BETWEEN -180 AND 180),
    accuracy_m  double precision,
    altitude_m  double precision,
    heading_deg double precision,

    -- Generated so the spatial index does the work rather than a function on
    -- every row at query time.
    geom geometry(Point, 4326)
        GENERATED ALWAYS AS (ST_SetSRID(ST_MakePoint(lon, lat), 4326)) STORED,

    device_label text,
    -- The gap between capturing and uploading. A photograph taken three days
    -- ago and submitted now is not necessarily false, and it is worth seeing.
    uploaded_at timestamptz NOT NULL DEFAULT now(),

    -- --- what the gates found ---------------------------------------------
    inside_parcel          boolean,
    distance_from_parcel_m double precision,
    -- Named rather than scored. A gate that says "78% confident" cannot be
    -- put in front of a farmer; "this photograph was taken 340 m outside the
    -- plot" can.
    gates_passed text[] NOT NULL DEFAULT '{}',
    gates_failed text[] NOT NULL DEFAULT '{}',

    captured_by uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX survey_captures_report_idx ON survey_captures (report_id, line_no);
CREATE INDEX survey_captures_geom_idx ON survey_captures USING gist (geom);
CREATE INDEX survey_captures_hash_idx ON survey_captures (sha256);
-- Captures that failed a gate are the ones an officer should look at first.
CREATE INDEX survey_captures_flagged_idx ON survey_captures (report_id)
    WHERE cardinality(gates_failed) > 0;

-- Append-only. A capture whose position could be edited after the gates ran
-- is a capture whose gates mean nothing.
CREATE FUNCTION survey_captures_are_final() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION
        'a submitted capture cannot be % ; submit another', lower(TG_OP);
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER survey_captures_no_rewrite
    BEFORE UPDATE ON survey_captures
    FOR EACH ROW EXECUTE FUNCTION survey_captures_are_final();

-- ---------------------------------------------------------------------------
-- The geofence
-- ---------------------------------------------------------------------------

-- How far outside its plot a capture may sit and still count.
--
-- Consumer GNSS on a mid-range handset is good to roughly 5-15 m under open
-- sky, worse under a canopy. A farmer photographing damage from the bund at
-- the edge of their own plot is standing a few metres outside the polygon and
-- is not committing fraud. Twenty-five metres is wide enough to absorb that
-- and narrow enough that the neighbouring survey number is still out.
CREATE FUNCTION capture_geofence(p_farm_id text, p_lon double precision, p_lat double precision)
RETURNS TABLE (inside boolean, distance_m double precision)
AS $$
    SELECT
        ST_DWithin(b.geom::geography,
                   ST_SetSRID(ST_MakePoint(p_lon, p_lat), 4326)::geography, 25),
        ST_Distance(b.geom::geography,
                    ST_SetSRID(ST_MakePoint(p_lon, p_lat), 4326)::geography)
    FROM parcel_boundaries b
    WHERE b.farm_id = p_farm_id;
$$ LANGUAGE sql STABLE;

-- ---------------------------------------------------------------------------
-- Evidence on the report
-- ---------------------------------------------------------------------------

-- Lifted so a listing can show which reports have evidence without joining.
ALTER TABLE crop_loss_reports
    ADD COLUMN capture_count integer NOT NULL DEFAULT 0,
    ADD COLUMN captures_flagged integer NOT NULL DEFAULT 0;

CREATE FUNCTION refresh_report_capture_counts() RETURNS trigger AS $$
BEGIN
    UPDATE crop_loss_reports r
    SET capture_count = (
            SELECT count(*) FROM survey_captures c WHERE c.report_id = r.id),
        captures_flagged = (
            SELECT count(*) FROM survey_captures c
            WHERE c.report_id = r.id AND cardinality(c.gates_failed) > 0)
    WHERE r.id = COALESCE(NEW.report_id, OLD.report_id);
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER survey_captures_count
    AFTER INSERT OR DELETE ON survey_captures
    FOR EACH ROW EXECUTE FUNCTION refresh_report_capture_counts();
