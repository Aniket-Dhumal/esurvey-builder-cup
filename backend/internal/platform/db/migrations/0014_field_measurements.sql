-- What an officer measured, rather than what they judged.
--
-- Today an officer records a percentage and a paragraph. The paragraph is
-- required and is genuinely useful, but the percentage behind it rests on
-- nothing that can be checked: it is a judgement written as a number, and at
-- an appeal there is no way to distinguish a careful one from a hurried one.
--
-- A quadrat changes that. Counting plants in a square metre against what
-- should be there, counting how many are lodged, and counting damaged pods in
-- a sample are all objective, all quick, and all reproducible by the next
-- officer standing in the same field. A figure derived from those survives an
-- appeal; a figure asserted does not.
--
-- # Why the counts are stored and the percentages are not
--
-- Percentages are derived in code from the counts, so the derivation can be
-- corrected without rewriting history and an officer can see the arithmetic
-- rather than a number. A stored percentage whose inputs are gone is exactly
-- the thing this table exists to replace.
--
-- # Why this is not required
--
-- An officer in a flooded field at dusk cannot always count anything, and a
-- system that refused their finding without measurements would get invented
-- measurements. What it does instead is record which findings rest on counts,
-- so an adjudicator can weigh two disagreeing officers on something better
-- than seniority.

-- A quadrat photograph is different evidence from a damage close-up: it is
-- the frame the counts were taken in, and it needs a scale in it -- a tape, a
-- ruler, a standard-sized object -- or the counts cannot be checked.
ALTER TYPE capture_kind ADD VALUE IF NOT EXISTS 'quadrat';

CREATE TABLE field_measurements (
    id        uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    report_id uuid NOT NULL REFERENCES crop_loss_reports (id) ON DELETE CASCADE,

    -- Which crop line was measured. Null where the visit produced one
    -- measurement for a single-crop report.
    line_no integer CHECK (line_no > 0),

    recorded_by uuid NOT NULL REFERENCES users (id) ON DELETE RESTRICT,
    recorded_at timestamptz NOT NULL DEFAULT now(),

    -- --- the quadrat --------------------------------------------------------
    -- Side length in metres. One metre is the usual square; row crops are
    -- sometimes sampled over a longer, narrower frame, so it is recorded
    -- rather than assumed.
    quadrat_side_m numeric(4, 2) NOT NULL CHECK (quadrat_side_m > 0 AND quadrat_side_m <= 10),

    -- Expected stand comes from the recommended spacing for the crop, which
    -- the officer looks up or knows. Stored because it is the denominator of
    -- the only truly objective figure here, and because two officers using
    -- different expectations should be visibly doing so.
    plants_expected integer CHECK (plants_expected >= 0),
    plants_present  integer CHECK (plants_present >= 0),
    plants_lodged   integer CHECK (plants_lodged >= 0),

    -- --- the harvested organ ------------------------------------------------
    -- Pods, heads, bolls or bunches, whichever this crop is sold as. The
    -- sample size is recorded so a count of three damaged out of five is not
    -- read as a count of three damaged out of a hundred.
    organ_sampled integer CHECK (organ_sampled >= 0),
    organ_damaged integer CHECK (organ_damaged >= 0),

    soil_type text,
    notes     text,

    -- The frame the counts were taken in. Not required -- a quadrat counted
    -- and not photographed is still better than nothing -- but a measurement
    -- with one attached is checkable and one without is testimony.
    capture_id uuid REFERENCES survey_captures (id) ON DELETE SET NULL,

    -- A lodged plant is a plant, and a damaged pod is a sampled pod. Both
    -- inversions are the kind of data-entry error that inflates a figure, and
    -- both are caught here rather than in a form validator a bulk import
    -- would bypass.
    CONSTRAINT lodged_within_present CHECK (
        plants_lodged IS NULL OR plants_present IS NULL
        OR plants_lodged <= plants_present
    ),
    CONSTRAINT damaged_within_sampled CHECK (
        organ_damaged IS NULL OR organ_sampled IS NULL
        OR organ_damaged <= organ_sampled
    ),
    -- A row that measured nothing is a row that should not have been written.
    CONSTRAINT measured_something CHECK (
        plants_present IS NOT NULL OR organ_sampled IS NOT NULL
    )
);

CREATE INDEX field_measurements_report_idx
    ON field_measurements (report_id, line_no);

-- Which verification rested on counts.
--
-- The point of the whole table: an adjudicator facing two officers who
-- disagree can see that one of them counted and the other did not, which is a
-- better basis for preferring a finding than which of them is senior.
ALTER TABLE report_verifications
    ADD COLUMN measurements_taken boolean NOT NULL DEFAULT false;

COMMENT ON COLUMN report_verifications.measurements_taken IS
    'Whether this finding rests on quadrat counts recorded in '
    'field_measurements, rather than on visual judgement alone.';
