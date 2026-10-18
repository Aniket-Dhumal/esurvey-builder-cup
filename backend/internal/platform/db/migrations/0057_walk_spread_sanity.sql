-- Nothing is asked of a farmer that their ground cannot hold.
--
-- # What this guards
--
-- The sown extent is a number somebody typed, and 0056 made the whole walk depend
-- on it. A wrong one is unrecoverable without this: half a guntha entered as 100
-- hectares asks for 866 metres between clips on a plot seven metres across, so the
-- record button never becomes available and the claim cannot be filed at all. A
-- registry figure delivered in the wrong unit does exactly the same thing, and
-- neither is rare enough to leave unhandled.
--
-- Found by probing the rule rather than by it happening: 100 ha against the office
-- demo plot returned 866 m, with no complaint from anything.
--
-- # The ceiling
--
-- The diagonal of the parcel over root two -- which is what a four-corner
-- arrangement of a square parcel genuinely supports, corner to corner. Two points
-- inside a shape cannot be further apart than its diagonal, so no arrangement
-- beats that by much, and it scales with the actual ground rather than with a
-- typed figure.
--
-- Deliberately not the parcel's narrowest width. 0055 tried that and had to be
-- corrected: a 200 m by 10 m ribbon was clamped to 10 m when four points sit sixty
-- metres apart along its length without difficulty.
--
-- The floor still wins over the ceiling where they cross, because five metres is
-- the resolution of the handset and below it nothing can be demonstrated at all.

CREATE OR REPLACE FUNCTION walk_required_spacing(
    p_farm_id text,
    p_sown_ha double precision
) RETURNS double precision
LANGUAGE plpgsql STABLE AS $$
DECLARE
    g         geometry;
    utm       integer;
    parcel_m2 double precision;
    sown_m2   double precision;
    ideal     double precision;
    across    double precision;
BEGIN
    SELECT geom INTO g FROM parcel_boundaries WHERE farm_id = p_farm_id;
    sown_m2 := COALESCE(p_sown_ha, 0) * 10000.0;

    -- No extent recorded: the parcel is all there is to go on, which is the rule
    -- every claim used before the crop decided it.
    IF sown_m2 <= 0 THEN
        IF g IS NULL THEN
            RETURN 15.0;
        END IF;
        parcel_m2 := ST_Area(g::geography);
        RETURN least(greatest(sqrt(parcel_m2) / 3.0, 5.0), 15.0);
    END IF;

    ideal := sqrt(walk_coverage_target() * sown_m2);

    -- The ceiling, where there is an outline to measure.
    IF g IS NOT NULL THEN
        utm := 32600 + floor((ST_X(ST_Centroid(g)) + 180) / 6)::integer + 1;
        across := ST_MaxDistance(ST_Transform(g, utm), ST_Transform(g, utm));
        IF across > 0 THEN
            ideal := least(ideal, across / sqrt(2.0));
        END IF;
    END IF;

    RETURN greatest(ideal, 5.0);
END $$;

COMMENT ON FUNCTION walk_required_spacing(text, double precision) IS
    'Metres a claim requires between clips: sqrt(0.75 * sown extent), capped by '
    'the parcel diagonal over root two so a mistyped extent cannot ask for more '
    'ground than exists, and floored at 5 m because a handset cannot distinguish '
    'closer points. Falls back to the parcel rule when no extent was recorded.';
