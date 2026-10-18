-- The walk's spread stops being floored by the parcel it sits in.
--
-- # What was wrong
--
-- 0055 sized the walk from the crop's sown extent, then floored it with the old
-- parcel rule, on this reasoning: an app too old to read the frozen figure still
-- applies the parcel rule, so the floor stops the office refusing a clip such an
-- app had permitted.
--
-- That reasoning does not hold. The old rule is `min(sqrt(parcel)/3, 15)`, and for
-- a small crop on a larger parcel it is *larger* than the crop figure -- so an old
-- app demands more walking than the office requires and nothing it permits is ever
-- refused. The floor could only ever raise the office's demand. It protected
-- nothing.
--
-- What it did do is strand people. A farmer with half a guntha -- about 50 square
-- metres, seven metres across -- was required to walk 14.4 metres, because the
-- parcel around their share is 1875 square metres. There is no way to satisfy that
-- inside their own plot, so the record button never became available and the claim
-- could not be made at all.
--
-- # What floors it now
--
-- Five metres, and nothing else. Not a policy: it is the resolution of the
-- instrument. A handset's fix is good to about five metres on a clear day, so two
-- points closer together than that cannot be shown to be different places -- and
-- the whole purpose of the spacing rule is to show that four clips came from four
-- parts of the crop. Below five metres there is nothing to demonstrate.
--
-- It follows that a crop extent under about 100 square metres cannot support four
-- distinguishable points, whatever this function returns. That is a fact about GPS
-- rather than a rule, and it belongs in front of whoever is registering such a
-- plot rather than buried here.
--
-- A claim with no sown extent recorded still falls back to the parcel rule, which
-- is the only thing there is to go on.

CREATE OR REPLACE FUNCTION walk_required_spacing(
    p_farm_id text,
    p_sown_ha double precision
) RETURNS double precision
LANGUAGE plpgsql STABLE AS $$
DECLARE
    g         geometry;
    parcel_m2 double precision;
    sown_m2   double precision;
BEGIN
    SELECT geom INTO g FROM parcel_boundaries WHERE farm_id = p_farm_id;
    sown_m2 := COALESCE(p_sown_ha, 0) * 10000.0;

    -- Nothing to scale from: the claim recorded no sown area, so the parcel is
    -- all there is. This is the pre-0055 rule, kept for exactly that case.
    IF sown_m2 <= 0 THEN
        IF g IS NULL THEN
            RETURN 15.0;
        END IF;
        parcel_m2 := ST_Area(g::geography);
        RETURN least(greatest(sqrt(parcel_m2) / 3.0, 5.0), 15.0);
    END IF;

    -- Four points enclosing walk_coverage_target() of the crop are about
    -- sqrt(target * area) apart. Floored at the resolution of the instrument.
    RETURN greatest(sqrt(walk_coverage_target() * sown_m2), 5.0);
END $$;

COMMENT ON FUNCTION walk_required_spacing(text, double precision) IS
    'Metres a claim requires between clips: sqrt(0.75 * sown extent), floored at '
    '5 m because a handset cannot distinguish closer points. Falls back to the '
    'parcel rule only when no sown extent was recorded. Frozen with the stations '
    'and obeyed by the handset, so the app never permits what the office refuses.';
