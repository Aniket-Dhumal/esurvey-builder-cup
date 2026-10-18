-- Giving the damage percentage a denominator.
--
-- A farmer declares 60%. An officer records 60%. The panel is asked whether
-- 60% is plausible. Nowhere does this system hold what the crop would have
-- yielded had the calamity not happened, so "60% of what?" has no answer in
-- the data model. The figure reads as a measurement and is a judgement.
--
-- Two answers, in increasing order of authority.
--
-- The first is what this land normally does. If a parcel peaked at 0.74 NDVI
-- this year against 0.71, 0.76 and 0.69 in the three seasons before, a fall
-- to 0.31 is a real loss. If it peaked at 0.74 against a history of 0.45, the
-- "loss" is a return to normal and possibly a claim against an unusually good
-- year. The second is what the surrounding fields did in the same fortnight,
-- which separates a regional calamity from a local one from a false claim --
-- and is the strongest fraud signal available that needs no photograph.
--
-- The third is the statutory one, threshold yield, which this system cannot
-- derive and can only be given.

-- ---------------------------------------------------------------------------
-- The cache that makes the first two affordable
-- ---------------------------------------------------------------------------

-- Every baseline is another billable analysis against the Farm Analytics
-- Engine. Asking for five prior seasons per parcel, per assessment, would
-- multiply the cost of every claim by six and re-derive an answer that cannot
-- have changed.
--
-- Two properties make this cheap instead:
--
-- A closed window over an immutable archive has a permanent answer. Sentinel-2
-- passes from three seasons ago are not going to be re-flown, so expires_at is
-- null and the row is kept. Only a window whose end is recent can still gain
-- passes as the archive catches up, and those rows expire.
--
-- A neighbourhood is shared. The peer baseline is keyed by a snapped grid
-- cell rather than by parcel, so every farmer filing from the same locality
-- answers from one analysis rather than one each. In a village-wide hailstorm
-- -- which is the case this system exists for -- that is the difference
-- between one call and four hundred.
CREATE TABLE remote_sensing_cache (
    product      text NOT NULL,
    -- What was analysed: a hash of the geometry for a parcel, or a snapped
    -- grid cell for a neighbourhood. Opaque here on purpose, so a new product
    -- does not need a schema change to key itself differently.
    cache_key    text NOT NULL,
    window_start date NOT NULL,
    window_end   date NOT NULL,

    result       jsonb NOT NULL,

    computed_at  timestamptz NOT NULL DEFAULT now(),
    -- Null means the answer cannot change and the row is kept indefinitely.
    expires_at   timestamptz,

    -- How many times this row saved an analysis. Not bookkeeping for its own
    -- sake: it is the only way to know whether the cache is earning its keep
    -- before the bill arrives.
    hits         integer NOT NULL DEFAULT 0,

    PRIMARY KEY (product, cache_key, window_start, window_end)
);

CREATE INDEX remote_sensing_cache_expiry_idx
    ON remote_sensing_cache (expires_at)
    WHERE expires_at IS NOT NULL;

-- ---------------------------------------------------------------------------
-- Threshold yield
-- ---------------------------------------------------------------------------

-- The statutory denominator. Every settlement in an Indian crop scheme rests
-- on it: typically the average of the best five of the last seven years for
-- that crop in that notified unit, established by Crop Cutting Experiments.
--
-- This platform cannot derive it and must not pretend to. The table exists so
-- that where the Department of Agriculture supplies the figure, an assessment
-- can state a shortfall against it rather than a percentage of nothing -- and
-- where it does not, the absence is visible rather than silently papered over
-- with a satellite proxy.
CREATE TABLE threshold_yields (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),

    crop            text NOT NULL,
    -- The notified unit. A circle where circles are mapped, a taluka where
    -- they are not -- which is most of Maharashtra today.
    jurisdiction_id uuid NOT NULL REFERENCES jurisdictions (id) ON DELETE RESTRICT,
    season          text NOT NULL,
    crop_year       text NOT NULL,

    yield_kg_per_ha numeric(10, 2) NOT NULL CHECK (yield_kg_per_ha > 0),

    -- How the figure was arrived at, in the issuing body's own terms. Stored
    -- rather than assumed, because two districts may use different bases in
    -- the same year and an assessment that does not say which it used cannot
    -- be reproduced.
    basis           text NOT NULL,
    source          text NOT NULL,

    issued_by       uuid REFERENCES users (id) ON DELETE SET NULL,
    issued_at       timestamptz NOT NULL DEFAULT now(),
    created_at      timestamptz NOT NULL DEFAULT now(),

    UNIQUE (crop, jurisdiction_id, season, crop_year)
);

CREATE INDEX threshold_yields_lookup_idx
    ON threshold_yields (jurisdiction_id, crop, crop_year);

-- ---------------------------------------------------------------------------
-- What was actually used, recorded with the assessment
-- ---------------------------------------------------------------------------

-- An assessment must be reproducible during an appeal, and that means saying
-- which baseline it stood on. A figure derived against three prior seasons is
-- a different claim from the same figure derived against none, and a year
-- later nobody will remember which this was.
ALTER TABLE assessments
    ADD COLUMN baseline jsonb;

COMMENT ON COLUMN assessments.baseline IS
    'The denominators available when this assessment was made: prior-season '
    'NDVI for the same parcel, the neighbourhood comparison, and the threshold '
    'yield where one was issued. Recorded so the figure can be reproduced.';
