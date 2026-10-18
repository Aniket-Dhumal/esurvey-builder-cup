-- Letting the pair converge, and fixing the trigger that made returning a
-- report impossible.
--
-- # The defect
--
-- 0006 made report_verifications append-only with a trigger that raises on
-- every UPDATE and every DELETE. 0023 then added superseded_at to the same
-- table and wrote
--
--     UPDATE report_verifications SET superseded_at = now() ...
--
-- into the return path. That statement is an UPDATE, so the trigger refuses
-- it, and every attempt by a Circle Officer to send a report back to the
-- village pair fails with "a recorded verification cannot be update". The
-- return path has never worked against a real database. It is not visible in
-- the Go tests because they do not run a Postgres with these triggers on it.
--
-- # Why the answer is a narrower trigger and not no trigger
--
-- The reason for append-only is still true, and it is the strongest guarantee
-- in the verification design: a finding an officer can rewrite after seeing
-- their colleague's is not an independent finding, and the blindness the pair
-- rests on would be a convention rather than something the database enforces.
--
-- So the trigger keeps refusing everything except the one transition the
-- design needs: stamping superseded_at on a row that does not yet carry it,
-- changing nothing else. A superseded row cannot come back, a figure cannot
-- be edited, and nothing can be deleted. Enforced by comparing the whole row,
-- not by listing the columns that must not change -- a column added next year
-- is protected without anybody remembering to add it here.

-- # Why the exemption is keyed on the table
--
-- One function serves two triggers: report_verifications and
-- report_adjudications. An adjudication has no superseded_at column at all,
-- and reading OLD.superseded_at on that row would fail with "record has no
-- field" -- turning a clear refusal into a confusing one. So the row is
-- compared as jsonb, and the exemption applies to the verifications table
-- only. Adjudications stay absolutely append-only, which is right: the final
-- figure is what a farmer was paid on.

CREATE OR REPLACE FUNCTION report_verifications_are_final() RETURNS trigger AS $$
DECLARE
    o jsonb;
    n jsonb;
BEGIN
    IF TG_OP = 'UPDATE' AND TG_TABLE_NAME = 'report_verifications' THEN
        o := to_jsonb(OLD);
        n := to_jsonb(NEW);
        IF  o->>'superseded_at' IS NULL
        AND n->>'superseded_at' IS NOT NULL
        AND (n - 'superseded_at' - 'superseded_reason')
          = (o - 'superseded_at' - 'superseded_reason')
        THEN
            RETURN NEW;
        END IF;
    END IF;

    IF TG_OP = 'UPDATE' THEN
        RAISE EXCEPTION
            'a recorded verification cannot be edited; it may only be '
            'superseded, and a fresh finding recorded in its place';
    END IF;

    RAISE EXCEPTION
        'a recorded verification cannot be deleted; supersede it instead';
END;
$$ LANGUAGE plpgsql;

-- --------------------------------------------------------------------------
-- Why a finding was superseded
-- --------------------------------------------------------------------------
--
-- There are now two reasons, and they mean opposite things about the officer
-- who recorded it. A finding superseded because the Circle sent the report
-- back is one an officer was told to redo. A finding superseded because the
-- officer themselves revised it after reading their colleague's is the pair
-- converging, which is the behaviour this platform wants and should not be
-- read later as an officer being corrected.
--
-- Without this column the two are indistinguishable in the record, and an
-- audit asking "how often is this officer sent back" would count both.

ALTER TABLE report_verifications
    ADD COLUMN IF NOT EXISTS superseded_reason text
        CHECK (superseded_reason IN ('returned', 'amended'));

COMMENT ON COLUMN report_verifications.superseded_reason IS
    'returned: a level above sent the report back and the pair looked again. '
    'amended: the officer revised their own finding after seeing their '
    'colleague''s. The second is the pair agreeing, not an officer being '
    'corrected, and the two must not be counted together.';

-- Backfill: every superseded row that exists today got there through the
-- return path, because it was the only path -- and, as above, it did not
-- actually work, so in practice there are none.
UPDATE report_verifications
   SET superseded_reason = 'returned'
 WHERE superseded_at IS NOT NULL AND superseded_reason IS NULL;
