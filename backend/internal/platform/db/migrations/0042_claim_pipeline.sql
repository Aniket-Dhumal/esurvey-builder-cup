-- A claim moves itself from arriving to being verified.
--
-- Until now nothing did. A claim landed in `submitted` and waited: for an
-- officer to acknowledge it, for somebody to request an assessment, and for an
-- allocation that no screen in the console could even ask for. Three manual
-- steps, none of which happens on the evening a district takes ten thousand
-- claims.
--
-- It was decided on 2026-09-16 that the claim advances on its own: complete
-- receipt acknowledges it and gives the farmer the reference to track, the
-- assessment is queued without being asked for, and when the assessment
-- finishes the claim is allocated to the village's Talathi and Krushi Sahayak.
-- The ladder above them is unchanged.
--
-- # What "complete" means
--
-- The claim arrives first and its photographs and clips follow one at a time,
-- so the server cannot tell "still uploading" from "that was everything". The
-- handset now says: it holds the claim until the walk is whole
-- (docs/EVIDENCE_REVALIDATION.md, V4), so it knows the full set before it
-- sends anything, and it declares the count.
--
-- A claim whose evidence never finishes arriving -- a phone lost on the way
-- home, an upload that will never succeed -- is released on a deadline anyway
-- and marked incomplete, because a farmer's claim waiting for ever on a
-- photograph that is never coming is the worse failure.

ALTER TABLE crop_loss_reports
    -- How many photographs and clips the handset said it would send. Null for
    -- a filing with no queue behind it -- a counter, a call centre -- which is
    -- complete on arrival.
    ADD COLUMN IF NOT EXISTS expected_captures integer
        CHECK (expected_captures IS NULL OR expected_captures BETWEEN 0 AND 200),

    -- When everything promised had landed. The instant the pipeline waits for.
    ADD COLUMN IF NOT EXISTS evidence_complete_at timestamptz,

    -- Released on the deadline rather than by arriving. An officer seeing this
    -- is looking at a claim whose evidence is short, and should be told so
    -- rather than left to count photographs.
    ADD COLUMN IF NOT EXISTS evidence_incomplete boolean NOT NULL DEFAULT false,

    -- The assessment this claim is waiting on, and how it ended. 'skipped'
    -- means the pipeline gave up on producing one and sent the claim to the
    -- officers regardless: a satellite service having a bad week is not a
    -- reason to hold somebody's claim.
    ADD COLUMN IF NOT EXISTS assessment_job_id uuid,
    ADD COLUMN IF NOT EXISTS assessment_state text NOT NULL DEFAULT 'none'
        CHECK (assessment_state IN ('none', 'queued', 'done', 'skipped')),

    -- Scheduling for the worker that advances the claim. The same shape as
    -- every other queue here: attempts, a next-attempt instant, and the last
    -- error kept where somebody can read it.
    ADD COLUMN IF NOT EXISTS pipeline_attempts integer NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS pipeline_next_at timestamptz NOT NULL DEFAULT now(),
    ADD COLUMN IF NOT EXISTS pipeline_error text;

COMMENT ON COLUMN crop_loss_reports.expected_captures IS
    'How many captures the handset promised. Complete when capture_count reaches it.';

-- Everything filed before this pipeline existed is complete: its evidence
-- either arrived long ago or never will, and leaving it null would leave every
-- old claim waiting on a deadline that already passed.
UPDATE crop_loss_reports
SET evidence_complete_at = COALESCE(evidence_complete_at, reported_at)
WHERE evidence_complete_at IS NULL;

-- The worker's queue: claims that need advancing, oldest first. Partial,
-- because the overwhelming majority of rows are past this and a pipeline that
-- scans the whole table every few seconds is a pipeline that gets turned off.
CREATE INDEX IF NOT EXISTS crop_loss_reports_pipeline_idx
    ON crop_loss_reports (pipeline_next_at, reported_at)
    WHERE state IN ('submitted', 'acknowledged');

-- Claims still waiting for evidence, for the deadline sweep.
CREATE INDEX IF NOT EXISTS crop_loss_reports_awaiting_evidence_idx
    ON crop_loss_reports (reported_at)
    WHERE evidence_complete_at IS NULL;

-- ---------------------------------------------------------------------------
-- Completion is stamped where the counting already happens
-- ---------------------------------------------------------------------------
--
-- The trigger that maintains capture_count is the one place that knows a
-- capture arrived, and it already recomputes from scratch. Stamping the
-- instant here makes it exactly-once without a second writer racing it: the
-- row is already being updated, and the stamp is set only while it is null.
--
-- Migration 0039's station rule is carried forward unchanged.
CREATE OR REPLACE FUNCTION refresh_report_capture_counts() RETURNS trigger AS $$
BEGIN
    UPDATE crop_loss_reports r
    SET capture_count = (
            SELECT count(*) FROM survey_captures c WHERE c.report_id = r.id),
        captures_flagged = (
            SELECT count(*) FROM survey_captures c
            WHERE c.report_id = r.id AND cardinality(c.gates_failed) > 0),
        stations_captured = (
            SELECT count(*) FROM (
                SELECT c.station_no
                FROM survey_captures c
                WHERE c.report_id = r.id AND c.station_no IS NOT NULL
                GROUP BY c.station_no
                HAVING bool_or(c.content_type LIKE 'video/%')
                   AND bool_or(c.content_type NOT LIKE 'video/%')
            ) whole),
        evidence_complete_at = CASE
            WHEN r.evidence_complete_at IS NOT NULL THEN r.evidence_complete_at
            WHEN r.expected_captures IS NULL THEN r.evidence_complete_at
            WHEN (SELECT count(*) FROM survey_captures c WHERE c.report_id = r.id)
                 >= r.expected_captures THEN now()
            ELSE r.evidence_complete_at
        END,
        -- A claim that has just become complete is due for the pipeline now,
        -- whatever backoff it was carrying from an earlier attempt.
        pipeline_next_at = CASE
            WHEN r.evidence_complete_at IS NULL
                 AND r.expected_captures IS NOT NULL
                 AND (SELECT count(*) FROM survey_captures c WHERE c.report_id = r.id)
                     >= r.expected_captures
            THEN now()
            ELSE r.pipeline_next_at
        END
    WHERE r.id = COALESCE(NEW.report_id, OLD.report_id);
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;

-- ---------------------------------------------------------------------------
-- An assessment nobody asked for
-- ---------------------------------------------------------------------------
--
-- requested_by was NOT NULL because every assessment was requested by an
-- officer at a keyboard. The pipeline queues them on receipt, and there is no
-- officer to name: inventing one -- a service account, the last person to
-- touch the claim -- would put a name in the audit trail that did not decide
-- anything.
--
-- Null means the system, and reads that way on the screen.
ALTER TABLE assessment_jobs ALTER COLUMN requested_by DROP NOT NULL;

COMMENT ON COLUMN assessment_jobs.requested_by IS
    'The officer who asked. Null when the claim pipeline queued it on receipt.';

-- ---------------------------------------------------------------------------
-- A receipt with no officer's name on it
-- ---------------------------------------------------------------------------
--
-- acknowledged_at and acknowledged_by had to be set together: the
-- acknowledgement was the पोहोच पावती, a named officer at a recorded time
-- confirming the claim reached the administration, and a timestamp with
-- nobody behind it would have been a status flag pretending to be a receipt.
--
-- That was right while an officer issued it. It was decided on 2026-09-16
-- that complete receipt on the server is the acknowledgement -- the farmer
-- gets their reference to track the moment everything has landed, rather than
-- waiting for somebody to be at a desk.
--
-- So the instant may now stand alone, and null means the system received it.
-- The pairing is still enforced in the other direction: a name without a time
-- is a record of nothing.
ALTER TABLE crop_loss_reports
    DROP CONSTRAINT IF EXISTS acknowledged_has_officer;

ALTER TABLE crop_loss_reports
    ADD CONSTRAINT acknowledged_has_officer
    CHECK (acknowledged_by IS NULL OR acknowledged_at IS NOT NULL);

COMMENT ON COLUMN crop_loss_reports.acknowledged_by IS
    'The officer who acknowledged. Null when the system did, on complete receipt.';
