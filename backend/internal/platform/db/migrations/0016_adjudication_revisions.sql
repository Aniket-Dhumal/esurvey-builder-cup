-- An upheld appeal has to change the figure on the report.
--
-- DecideGrievance already refuses to uphold an appeal without a revised
-- percentage -- "saying the farmer was right and leaving the figure where it
-- was is a contradiction" -- and then writes that percentage onto the
-- grievance row and nowhere else. The report keeps its original
-- adjudicated_percent. The farmer wins and the record still says they lost.
--
-- Everything downstream reads crop_loss_reports.adjudicated_percent: the
-- rollups, the worklist, and the assessment report. All of them would carry
-- the superseded number.
--
-- # Why a new row rather than an edit
--
-- report_adjudications is append-only by trigger, and that is right: who
-- decided what, on what evidence, and when, is exactly what an inquiry asks
-- afterwards. So a revision is a new adjudication that names the one it
-- replaces, and the chain stays readable in both directions.

ALTER TABLE report_adjudications
    -- One per report was the rule. It becomes one *original* per report, with
    -- a chain of revisions after it; the partial indexes below carry that.
    DROP CONSTRAINT report_adjudications_report_id_key,

    -- The adjudication this one replaces. Null on the original.
    ADD COLUMN supersedes_id uuid
        REFERENCES report_adjudications (id) ON DELETE RESTRICT,

    -- The appeal that caused the revision. Required on a revision: an
    -- adjudicated figure may only be changed by a process a farmer can see
    -- and can themselves have started, never by an officer changing their
    -- mind off the record.
    ADD COLUMN grievance_id uuid
        REFERENCES grievances (id) ON DELETE RESTRICT,

    ADD CONSTRAINT report_adjudications_revision_needs_appeal CHECK (
        (supersedes_id IS NULL AND grievance_id IS NULL)
        OR (supersedes_id IS NOT NULL AND grievance_id IS NOT NULL)
    );

-- Exactly one original decision per report, as before.
CREATE UNIQUE INDEX report_adjudications_one_original
    ON report_adjudications (report_id)
    WHERE supersedes_id IS NULL;

-- And no forks: an adjudication may be superseded once. Without this, two
-- officers deciding two appeals concurrently would leave the report with two
-- equally current figures and no rule for choosing between them.
CREATE UNIQUE INDEX report_adjudications_one_successor
    ON report_adjudications (supersedes_id)
    WHERE supersedes_id IS NOT NULL;

-- The current figure is the end of the chain: the row nothing supersedes.
CREATE INDEX report_adjudications_chain_idx
    ON report_adjudications (report_id, decided_at DESC);

COMMENT ON COLUMN report_adjudications.supersedes_id IS
    'The adjudication this one replaces. Null on the original. The current '
    'figure for a report is the row no other row supersedes.';

COMMENT ON COLUMN report_adjudications.grievance_id IS
    'The upheld appeal that caused this revision. Required on a revision: an '
    'adjudicated figure changes only through a process the farmer can see.';
