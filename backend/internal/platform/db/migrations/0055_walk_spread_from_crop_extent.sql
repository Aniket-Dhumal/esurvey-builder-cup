-- How far a walk must spread, decided by the crop being claimed.
--
-- # What was wrong
--
-- The spacing a farmer had to move between clips was computed from the area of
-- the parcel boundary -- that is, of the whole survey number. A survey number is
-- routinely larger than, and shared by more than, the farmer filing the claim.
--
-- The shape the trial has to handle: a 10 ha survey number in which one farmer
-- owns 2 ha, split 1 ha soybean and 1 ha udad, while a neighbour in the same
-- survey number owns 5 ha of soybean. That farmer files two claims, one per
-- crop. Under the old rule all three claims were asked for the same walk,
-- because all three sit on one polygon: sqrt(100000)/3 is 105 m, capped at 15,
-- so every one of them asked for fifteen metres. Four clips fifteen metres apart
-- on a 5 ha holding sample one corner of it, and the assessment could not tell
-- that from a walk across the whole field.
--
-- # What decides it now
--
-- The claim's own sown extent. Four points enclose a quadrilateral, so for that
-- quadrilateral to reach a given fraction of the crop's area the points must be
-- about sqrt(fraction * area) apart: 87 m for 1 ha at three quarters, 194 m for
-- 5 ha. That is a real traverse of the crop, and it scales with what is being
-- claimed rather than with who else happens to share the survey number.
--
-- # Why nothing caps it by the shape of the plot
--
-- It looked as though something should: a 200 m by 10 m ribbon cannot hold four
-- points spread across ten metres. But it can hold them sixty metres apart along
-- its length, so the narrow way across bounds nothing. An earlier draft of this
-- capped by the oriented envelope's shorter side and asked a ribbon for less
-- spread than the plot could easily give.
--
-- What four points can really be spread across a given polygon has no tidy
-- closed form, and guessing at one produced a figure that was wrong in both
-- directions. So the figure asked for is the one the crop needs, floored by the
-- rule the office used before this, and a farmer who has genuinely reached the
-- limit of their ground is answered in the walk rather than in this arithmetic.
--
-- # Why it is frozen with the stations
--
-- So there is one rule in one place. The app used to hold its own copy of this
-- arithmetic, and a farmer could be allowed to record something the office then
-- refused -- which is exactly what happened: three clips of a filed claim were
-- refused SAME_POSITION on every pass. The figure is computed here, written down
-- beside the four points, and sent to the handset with them. The handset obeys it
-- rather than deriving it, so the two cannot disagree, and it travels before the
-- walk so it works with no signal.

ALTER TABLE report_sampling_stations
    -- Metres. Written on every row of a report's set; the same value on each,
    -- because it is a property of the walk rather than of a point.
    ADD COLUMN IF NOT EXISTS required_spacing_m double precision;

COMMENT ON COLUMN report_sampling_stations.required_spacing_m IS
    'How far this claim requires the walk to move between clips, from the crop '
    'sown extent and capped by the parcel width. Frozen with the points and '
    'sent to the handset, so the app never permits what the office refuses.';

-- The fraction of the crop extent four clips should between them enclose.
--
-- Three quarters. Not all of it: a farmer cannot stand on their own boundary,
-- standing water and a neighbour's crop are real, and a rule that demands the
-- whole extent is a rule that strands somebody in the mud watching a disabled
-- button.
CREATE OR REPLACE FUNCTION walk_coverage_target() RETURNS double precision
LANGUAGE sql IMMUTABLE AS $$ SELECT 0.75::double precision $$;

-- The spread one claim requires, in metres.
--
-- p_sown_ha is the claim's own sown extent and is what decides the figure.
-- p_farm_id supplies the ceiling: no walk may ask for more than the parcel is
-- wide, whatever the area says.
CREATE OR REPLACE FUNCTION walk_required_spacing(
    p_farm_id text,
    p_sown_ha double precision
) RETURNS double precision
LANGUAGE plpgsql STABLE AS $$
DECLARE
    g           geometry;
    parcel_m2   double precision;
    sown_m2     double precision;
    ideal       double precision;
    floor_m     double precision;
    answer      double precision;
BEGIN
    SELECT geom INTO g FROM parcel_boundaries WHERE farm_id = p_farm_id;

    sown_m2 := COALESCE(p_sown_ha, 0) * 10000.0;

    -- The old rule, kept as a floor.
    --
    -- An app too old to read the frozen figure still applies this one, so the
    -- floor is what stops the office refusing a clip that such an app permitted.
    -- It is removed once no handset in the field predates this.
    IF g IS NULL THEN
        floor_m := 15.0;
        parcel_m2 := 0;
    ELSE
        parcel_m2 := ST_Area(g::geography);
        floor_m := least(greatest(sqrt(parcel_m2) / 3.0, 5.0), 15.0);
    END IF;

    -- Nothing to scale from: the claim recorded no sown area. The floor is the
    -- honest answer rather than a figure invented from the parcel.
    IF sown_m2 <= 0 THEN
        RETURN floor_m;
    END IF;

    ideal := sqrt(walk_coverage_target() * sown_m2);

    -- The floor wins where the two disagree. Below it the office refuses, and a
    -- refused clip is a farmer's walk spent twice; above it the walk is merely
    -- harder.
    answer := greatest(ideal, floor_m);
    RETURN answer;
END $$;

COMMENT ON FUNCTION walk_required_spacing(text, double precision) IS
    'Metres a claim requires between clips: sqrt(0.75 * sown area), floored by '
    'the pre-0055 parcel rule. One rule, frozen with the stations and obeyed by '
    'the handset, so the app never permits what the office refuses.';
