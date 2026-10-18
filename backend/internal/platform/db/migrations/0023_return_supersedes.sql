-- Let a returned report climb again.
--
-- # The defect this fixes, which 0022 introduced
--
-- 0022 gave report_approvals a unique index over (report_id, level) for
-- approved rows, on the reasoning that one level signs once. That is true of a
-- report that only ever goes up.
--
-- A report can also come back down. When the Tehsildar returns one to the
-- Circle, the Circle has to approve it a second time, and the row from its
-- first approval refuses the second. The same thing happens one rung lower
-- and worse: returning to the village pair leaves the Talathi unable to record
-- a fresh finding, because report_verifications has carried a unique
-- constraint over (report_id, officer_id, tier) since long before this ladder
-- existed.
--
-- So the return path was unbuildable as the schema stood. Both tables now mark
-- the old row superseded instead, and uniqueness applies only to rows that
-- still stand.
--
-- # Why superseded rather than deleted
--
-- The whole reason every prior figure is kept is that an audit has to be able
-- to answer who decided what a farmer was paid. A report that went up to the
-- Tehsil at 40%, came back, was re-surveyed at 65% and went up again has two
-- Circle approvals in its history, and both are true things that happened.
-- Deleting the first would leave a record that reads as though the Circle
-- always thought 65%.

-- --------------------------------------------------------------------------
-- Approvals
-- --------------------------------------------------------------------------

ALTER TABLE report_approvals
    ADD COLUMN IF NOT EXISTS superseded_at timestamptz,

    -- The return that knocked this approval down, so the history reads as a
    -- sequence of events rather than as a set of rows with gaps in it.
    ADD COLUMN IF NOT EXISTS superseded_by uuid
        REFERENCES report_approvals(id) ON DELETE SET NULL;

DROP INDEX IF EXISTS report_approvals_one_per_level;

-- One standing approval per level. Superseded ones are history and do not
-- compete.
CREATE UNIQUE INDEX IF NOT EXISTS report_approvals_one_standing_per_level
    ON report_approvals (report_id, level)
    WHERE decision = 'approved' AND superseded_at IS NULL;

COMMENT ON COLUMN report_approvals.superseded_at IS
    'Set when a level above returned the report, so this approval no longer '
    'stands and the level may approve again. The row is kept: an audit has to '
    'see that the Circle once passed this report at a different figure.';

-- --------------------------------------------------------------------------
-- Findings
-- --------------------------------------------------------------------------

ALTER TABLE report_verifications
    ADD COLUMN IF NOT EXISTS superseded_at timestamptz;

-- The constraint predates the ladder and has no partial form, so it is
-- replaced by an index that can have one.
ALTER TABLE report_verifications
    DROP CONSTRAINT IF EXISTS report_verifications_report_id_officer_id_tier_key;

CREATE UNIQUE INDEX IF NOT EXISTS report_verifications_one_standing
    ON report_verifications (report_id, officer_id, tier)
    WHERE superseded_at IS NULL;

COMMENT ON COLUMN report_verifications.superseded_at IS
    'Set when the Circle returned the report to the village pair for a fresh '
    'look. The original finding is kept, because what an officer recorded '
    'before being asked to look again is exactly what a dispute turns on.';

-- --------------------------------------------------------------------------
-- How many times this report has been sent back
-- --------------------------------------------------------------------------
--
-- A report on its fourth trip up the ladder is not the same case as one on its
-- first, and nothing else on the row says so. Cheap to keep, and the number an
-- officer wants when deciding whether the problem is the evidence or the
-- process.

ALTER TABLE crop_loss_reports
    ADD COLUMN IF NOT EXISTS returned_count integer NOT NULL DEFAULT 0;

-- Separately, because ALTER TABLE takes ADD CONSTRAINT and not a bare
-- CONSTRAINT clause alongside ADD COLUMN. DO block so a re-run is silent.
DO $$ BEGIN
    ALTER TABLE crop_loss_reports
        ADD CONSTRAINT crop_loss_reports_returned_count_sane
        CHECK (returned_count >= 0);
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;
