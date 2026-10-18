-- Where to send the farmer.
--
-- Four points across their own parcel, so the evidence is a sample of the
-- field rather than a selection from it. A farmer who films only the
-- waterlogged corner turns a 30% loss into an 80% one -- usually without
-- meaning to, because the wet patch is the part that feels worth showing.
--
-- The geometry lives here rather than in Go because PostGIS already has the
-- hard parts: an oriented envelope gives the parcel's long axis for free, and
-- containment against a real MultiPolygon is not something to reimplement.
--
-- See docs/FARMER_APP_REDESIGN.md §7.

-- Four sampling stations across a parcel, ordered low to high where there is
-- a low and a high.
--
-- p_downhill_deg is the terrain aspect: the compass bearing the slope faces,
-- which is the direction water runs. Pass NULL where the parcel has too
-- little relief for aspect to mean anything -- around Barshi that is the
-- common case, a metre across a whole parcel, and it is not a degraded
-- result. The same four points come back either way; only which end is
-- called "low" changes, and the placement column says which happened.
--
-- Stations 2 and 3 sit off the centre line on opposite flanks. A strip parcel
-- 100 m wide sampled only down its spine says nothing about either side,
-- which is half of what the farmer was asked to walk.
CREATE FUNCTION parcel_sampling_stations(
    p_farm_id      text,
    p_downhill_deg double precision DEFAULT NULL
)
RETURNS TABLE (
    station_no integer,
    lat        double precision,
    lon        double precision,
    role       text,
    placement  text
) AS $$
DECLARE
    g_ll    geometry;
    utm     integer;
    g       geometry;
    ring    geometry;
    p1 geometry; p2 geometry; p3 geometry; p4 geometry;
    len_a double precision;
    len_b double precision;
    a_start geometry;
    a_end   geometry;
    axis    geometry;
    width   double precision;
    perp_x  double precision := 0;
    perp_y  double precision := 0;
    bearing double precision;
    apart   double precision;
    fracs   double precision[] := ARRAY[0.125, 0.375, 0.625, 0.875];
    offs    double precision[];
    roles   text[] := ARRAY['low', 'mid_left', 'mid_right', 'high'];
    plc     text;
    i       integer;
    pt      geometry;
BEGIN
    SELECT b.geom INTO g_ll FROM parcel_boundaries b WHERE b.farm_id = p_farm_id;
    IF g_ll IS NULL THEN
        -- No boundary, no stations. The app reads an empty set as "trace the
        -- boundary first", which is the only honest answer: a geofence that
        -- does not exist cannot be walked to.
        RETURN;
    END IF;

    -- Work in metres, not degrees. Every distance below -- the axis length,
    -- the flank offset -- is a real-world measurement, and a degree of
    -- longitude at this latitude is not a degree of latitude.
    utm := 32600 + floor((ST_X(ST_Centroid(g_ll)) + 180) / 6)::integer + 1;
    g   := ST_Transform(g_ll, utm);

    -- The minimum-area rotated rectangle. Its long side is the parcel's long
    -- axis, which on a strip parcel is nothing like the bounding box's.
    ring := ST_ExteriorRing(ST_OrientedEnvelope(g));
    p1 := ST_PointN(ring, 1);
    p2 := ST_PointN(ring, 2);
    p3 := ST_PointN(ring, 3);
    p4 := ST_PointN(ring, 4);

    len_a := ST_Distance(p1, p2);
    len_b := ST_Distance(p2, p3);

    -- The axis runs down the middle, from the centre of one short end to the
    -- centre of the other.
    IF len_a >= len_b THEN
        a_start := ST_Centroid(ST_MakeLine(p4, p1));
        a_end   := ST_Centroid(ST_MakeLine(p2, p3));
        width   := len_b;
        IF width > 0 THEN
            perp_x := (ST_X(p3) - ST_X(p2)) / width;
            perp_y := (ST_Y(p3) - ST_Y(p2)) / width;
        END IF;
    ELSE
        a_start := ST_Centroid(ST_MakeLine(p1, p2));
        a_end   := ST_Centroid(ST_MakeLine(p3, p4));
        width   := len_a;
        IF width > 0 THEN
            perp_x := (ST_X(p2) - ST_X(p1)) / width;
            perp_y := (ST_Y(p2) - ST_Y(p1)) / width;
        END IF;
    END IF;

    axis := ST_MakeLine(a_start, a_end);

    -- A quarter of the width off centre: clearly on one flank, and still well
    -- inside a parcel whose edges are where the boundary is least certain.
    offs := ARRAY[0, -0.25 * width, 0.25 * width, 0];

    -- Which end is downhill.
    --
    -- The bearing of the axis, compared with the direction water runs. If the
    -- two point roughly the same way then walking the axis goes downhill, so
    -- the low end is the far end and the order is reversed.
    plc := 'geometric';
    IF p_downhill_deg IS NOT NULL THEN
        bearing := degrees(atan2(ST_X(a_end) - ST_X(a_start),
                                 ST_Y(a_end) - ST_Y(a_start)));
        -- Angular separation in [0, 180], sign and wrap-around removed.
        apart := abs(((bearing - p_downhill_deg + 540)::numeric % 360)::double precision - 180);
        IF apart < 90 THEN
            fracs := ARRAY[0.875, 0.625, 0.375, 0.125];
            offs  := ARRAY[0, 0.25 * width, -0.25 * width, 0];
        END IF;
        plc := 'terrain';
    END IF;

    FOR i IN 1..4 LOOP
        pt := ST_LineInterpolatePoint(axis, fracs[i]);
        IF offs[i] <> 0 THEN
            pt := ST_Translate(pt, perp_x * offs[i], perp_y * offs[i]);
        END IF;

        -- The oriented envelope is a rectangle and the parcel is not. A point
        -- on the axis of the rectangle can still fall outside a parcel that
        -- bends, and directing a farmer to stand outside their own field
        -- would be refused by the very geofence this is feeding.
        IF NOT ST_Contains(g, pt) THEN
            pt := ST_ClosestPoint(g, pt);
        END IF;

        pt := ST_Transform(pt, 4326);

        station_no := i;
        lon        := ST_X(pt);
        lat        := ST_Y(pt);
        role       := roles[i];
        placement  := plc;
        RETURN NEXT;
    END LOOP;
END;
$$ LANGUAGE plpgsql STABLE;

COMMENT ON FUNCTION parcel_sampling_stations(text, double precision) IS
    'Four sampling stations across a parcel, ordered low to high when a downhill bearing is given. Returns no rows when the parcel has no boundary, which the app reads as "trace it first".';
