-- Letting a farmer draw their own plot.
--
-- AgriStack returns plot_geometry: null for every farmer this platform can
-- reach. Without a boundary the assessment refuses outright, the geofence on
-- every photograph has nothing to check against, and the prior-season and
-- neighbourhood baselines have no parcel to read. The whole analysis stack
-- stands on a field boundary that no live farmer has.
--
-- The design anticipated this -- there is a boundary_point capture kind, the
-- source column already admits 'field_traced', and the sync uses ST_MakeValid
-- because "a traced boundary from a phone is often a little untidy". Nothing
-- ever joined them up. This is that join.
--
-- # Why the walk is kept and not just the polygon
--
-- A boundary is evidence. An officer looking at a claim two years later needs
-- to be able to ask how the plot was established, and "somebody walked it on
-- this date with this GPS accuracy" is an answer; a polygon with no
-- provenance is not. The points also carry the accuracy at each corner, which
-- is the only honest way to say how much the boundary could be out by.
--
-- # Why a traced boundary is usable before anyone confirms it
--
-- A farmer who walks their field at eight in the morning should not wait for
-- an officer to visit before their claim can be assessed. The boundary is
-- labelled as traced on every figure derived from it, an officer's
-- confirmation is recorded when it comes, and the difference between the two
-- states is visible rather than blocking.

ALTER TABLE parcel_boundaries
    -- The walk itself: [{lat, lon, accuracy_m, at}], in the order taken.
    ADD COLUMN traced_points jsonb,

    -- The worst GPS accuracy at any corner. One bad corner is what the
    -- boundary could be out by, and averaging it away would understate that.
    ADD COLUMN worst_accuracy_m numeric(8, 2),

    -- Who walked it, and when they finished.
    ADD COLUMN traced_by  uuid REFERENCES users (id) ON DELETE SET NULL,
    ADD COLUMN traced_at  timestamptz,

    -- An officer standing in the field agreeing that this is the plot. Not
    -- required to use the boundary; recorded because a confirmed boundary and
    -- an unconfirmed one should not look alike at an appeal.
    ADD COLUMN confirmed_by uuid REFERENCES users (id) ON DELETE SET NULL,
    ADD COLUMN confirmed_at timestamptz,

    -- The extent the registry holds for this survey number, copied at tracing
    -- time. The two disagreeing is a fact worth surfacing -- a jointly held
    -- survey number, a plot split between brothers, or a walk that cut a
    -- corner -- and it is never resolved automatically in either direction.
    ADD COLUMN registry_extent_ha numeric(12, 4);

-- A confirmation names an officer and a time, or neither.
ALTER TABLE parcel_boundaries
    ADD CONSTRAINT confirmation_is_complete CHECK (
        (confirmed_by IS NULL) = (confirmed_at IS NULL)
    );

COMMENT ON COLUMN parcel_boundaries.traced_points IS
    'The walk, in order, with the GPS accuracy at each corner. Kept because a '
    'boundary is evidence: two years later somebody has to be able to ask how '
    'this plot was established.';

COMMENT ON COLUMN parcel_boundaries.registry_extent_ha IS
    'The owner extent AgriStack held when the plot was walked. A disagreement '
    'with polygon_area_ha is surfaced and never resolved automatically: a '
    'jointly held survey number and a walk that cut a corner look identical '
    'from here.';

-- Finding the unconfirmed boundaries in a village, for an officer's round.
--
-- Partial: confirmed boundaries are the majority once a district has been
-- through, and an index over all of them would be mostly dead weight.
CREATE INDEX parcel_boundaries_unconfirmed_idx
    ON parcel_boundaries (village_lgd_code, traced_at)
    WHERE source = 'field_traced' AND confirmed_at IS NULL;
