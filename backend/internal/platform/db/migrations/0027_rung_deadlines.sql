-- A deadline for the four rungs above the village pair.
--
-- # What was missing
--
-- report_assignments carries a per-officer deadline and rows are created at
-- exactly one tier -- 'village_pair'. The Circle Officer, the Tehsildar and
-- the District Magistrate had none. A report reaching the tehsil could sit
-- there for a season with nothing marking it late and nobody above able to
-- see it.
--
-- That is the failure the development plan closes on: five tiers is faithful
-- to how Maharashtra works and is also five chances for a claim to stall.
-- Four of the five had no clock at all.
--
-- # Why the deadline belongs to the rung and not to an officer
--
-- Above the village pair a report is not assigned to a person. It waits at a
-- level, and whoever holds that posting today is who owes an answer -- which
-- may be a different officer next week, and may be two officers sharing a
-- charge. Writing per-officer rows would mean inventing an assignment nobody
-- made, and re-inventing it on every transfer.
--
-- So the report records when it arrived at its current rung, and lateness is
-- that instant against a per-rung allowance. Transfers, additional charge and
-- vacancies all stop mattering: the clock is on the rung.
--
-- # Why nothing escalates
--
-- Deliberately. Being late makes a report visible to the officer above and
-- puts it in their bell; it never moves the report on its own. A figure
-- nobody looked at is exactly what this platform exists to prevent, and an
-- automatic escalation would hand a supervisor a claim with no finding behind
-- it while quietly rewarding the officer who ignored it.

ALTER TABLE crop_loss_reports
    ADD COLUMN IF NOT EXISTS approval_level_since timestamptz;

-- Backfill from the record rather than from now().
--
-- Stamping every waiting report with the moment this migration ran would
-- reset the clock on precisely the reports that have been waiting longest --
-- the ones this column exists to surface.
--
-- The best available truth is the last decision that moved it: an approval
-- from the rung below, or a return from the rung above. Where there is
-- neither, the report has not moved since it was verified, so its own
-- updated_at is the closest honest answer.
UPDATE crop_loss_reports r
   SET approval_level_since = COALESCE(
        (SELECT max(a.decided_at)
           FROM report_approvals a
          WHERE a.report_id = r.id
            AND a.superseded_at IS NULL),
        r.updated_at)
 WHERE r.approval_level IS NOT NULL
   AND r.approval_level_since IS NULL;

-- The overdue query reads "waiting, and arrived before X". Partial, because a
-- report that has finished climbing is never late.
CREATE INDEX IF NOT EXISTS crop_loss_reports_waiting_since_idx
    ON crop_loss_reports (approval_level, approval_level_since)
 WHERE approval_level IS NOT NULL
   AND state NOT IN ('adjudicated', 'closed', 'withdrawn');
