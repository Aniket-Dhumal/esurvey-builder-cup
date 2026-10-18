-- The approval ladder, the crops an event covers, and the two flags that
-- decide how a report is allowed to move.
--
-- # What changes about how a report travels
--
-- Until now the Circle Officer and the Tehsildar were conditional. A report
-- was verified by the Talathi and the Krushi Sahayak, and if those two agreed
-- it was adjudicated; only a disagreement sent it upward, which is what
-- report_verifications.tier records as 'arbitration' and 'escalation'. A
-- report where the pair agreed never met a supervisor at all.
--
-- The workflow this platform is being built for puts every report through four
-- rungs in order: the village pair, then the Circle, then the Tehsil, then the
-- District. Not because agreement is doubted, but because each level is
-- accountable for what it passes upward, and an approval nobody recorded is an
-- accountability nobody holds.
--
-- # Why the ladder is its own table
--
-- report_verifications holds findings: what an officer saw on a parcel.
-- report_adjudications holds a decision that supersedes them. Neither is an
-- approval, which is a third thing -- one level accepting what the level below
-- sent, possibly after changing the number.
--
-- Putting approvals in either would overload a table whose meaning is already
-- settled, and would make "who approved this" a query that has to know which
-- rows are findings and which are sign-offs. A separate table keeps each row
-- one fact.
--
-- # Why every prior figure is kept
--
-- Any level may revise the percentage, and the District's is final. That is a
-- deliberate choice and it has a cost: four officers may each have written a
-- different number against one farmer's field. If only the last survived, an
-- audit could not answer who decided what a farmer was paid, which is the
-- single question an audit of this system exists to answer. So each rung
-- records what it received and what it passed on, and a change requires a
-- reason.

-- --------------------------------------------------------------------------
-- The crops an event covers
-- --------------------------------------------------------------------------
--
-- A Collector declaring a hailstorm names the crops it damaged. Soybean and
-- cotton are flattened by the same storm that a standing sugarcane shrugs off,
-- and paying all three alike is neither accurate nor affordable.
--
-- Codes are AgriStack's, so the comparison against a farmer's crop_sown_data
-- is an equality test rather than a string match on names that differ by
-- spelling, language and case between two systems.

CREATE TABLE IF NOT EXISTS survey_event_crops (
    event_id   uuid NOT NULL REFERENCES survey_events(id) ON DELETE CASCADE,
    crop_code  text NOT NULL,
    crop_name  text NOT NULL,
    added_by   uuid REFERENCES users(id) ON DELETE SET NULL,
    added_at   timestamptz NOT NULL DEFAULT now(),

    PRIMARY KEY (event_id, crop_code),

    CONSTRAINT survey_event_crops_code_not_blank CHECK (btrim(crop_code) <> ''),
    CONSTRAINT survey_event_crops_name_not_blank CHECK (btrim(crop_name) <> '')
);

COMMENT ON TABLE survey_event_crops IS
    'The crops a survey event covers, by AgriStack crop code. An event with no '
    'rows here covers every crop, which is the correct reading of a Collector '
    'who did not narrow it.';

-- --------------------------------------------------------------------------
-- Where a report has reached
-- --------------------------------------------------------------------------

DO $$ BEGIN
    CREATE TYPE approval_level AS ENUM (
        'village_pair', 'circle', 'tehsil', 'district');
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

DO $$ BEGIN
    CREATE TYPE approval_decision AS ENUM ('approved', 'returned');
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

CREATE TABLE IF NOT EXISTS report_approvals (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    report_id   uuid NOT NULL REFERENCES crop_loss_reports(id) ON DELETE CASCADE,

    level       approval_level NOT NULL,
    officer_id  uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    role        officer_role NOT NULL,

    decision    approval_decision NOT NULL,

    -- What this rung received, and what it passed on. Equal when the officer
    -- approved without changing anything, which is the ordinary case and is
    -- worth being able to see rather than infer from a null.
    percent_before  numeric(5,2),
    percent_after   numeric(5,2),
    area_before_ha  numeric(10,4),
    area_after_ha   numeric(10,4),

    -- Required by trigger when the figure changed, and when returning.
    reason      text,

    decided_at  timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT report_approvals_percent_range CHECK (
        (percent_before IS NULL OR percent_before BETWEEN 0 AND 100) AND
        (percent_after  IS NULL OR percent_after  BETWEEN 0 AND 100)),

    CONSTRAINT report_approvals_area_positive CHECK (
        (area_before_ha IS NULL OR area_before_ha >= 0) AND
        (area_after_ha  IS NULL OR area_after_ha  >= 0))
);

-- One approval per level per report. A second sign-off at the same rung is
-- either a double submit or a supervisor overwriting a colleague, and neither
-- should be silent.
CREATE UNIQUE INDEX IF NOT EXISTS report_approvals_one_per_level
    ON report_approvals (report_id, level)
    WHERE decision = 'approved';

CREATE INDEX IF NOT EXISTS report_approvals_by_report
    ON report_approvals (report_id, decided_at DESC);

CREATE INDEX IF NOT EXISTS report_approvals_by_officer
    ON report_approvals (officer_id, decided_at DESC);

COMMENT ON TABLE report_approvals IS
    'One rung of the approval ladder. Holds what the level received and what '
    'it passed upward, so a figure revised four times can still be traced to '
    'the officer who set it.';

-- A change to the figure, or a return, must say why.
--
-- In the trigger rather than in Go because this is the row an audit reads. A
-- check that lives only in the application is a check that a migration, a
-- script or a second service can walk around.
CREATE OR REPLACE FUNCTION report_approvals_require_reason()
RETURNS trigger AS $$
BEGIN
    IF NEW.decision = 'returned' AND btrim(coalesce(NEW.reason, '')) = '' THEN
        RAISE EXCEPTION
            'a report sent back must say why'
            USING ERRCODE = 'check_violation';
    END IF;

    IF NEW.decision = 'approved'
       AND NEW.percent_after IS DISTINCT FROM NEW.percent_before
       AND btrim(coalesce(NEW.reason, '')) = '' THEN
        RAISE EXCEPTION
            'changing the damage percentage requires a reason'
            USING ERRCODE = 'check_violation';
    END IF;

    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS report_approvals_reason ON report_approvals;
CREATE TRIGGER report_approvals_reason
    BEFORE INSERT OR UPDATE ON report_approvals
    FOR EACH ROW EXECUTE FUNCTION report_approvals_require_reason();

-- --------------------------------------------------------------------------
-- The two flags on the report itself
-- --------------------------------------------------------------------------

ALTER TABLE crop_loss_reports
    -- The rung the report is waiting at. NULL until the village pair have both
    -- recorded, because before that there is nothing for a supervisor to
    -- approve.
    ADD COLUMN IF NOT EXISTS approval_level approval_level,

    -- The pair disagreed by more than the platform's threshold, or one of them
    -- refused the claim outright. Such a report still climbs the same ladder,
    -- but it sorts to the top of the queue, the Circle must give a reason, and
    -- it cannot be swept up in a bulk approval.
    ADD COLUMN IF NOT EXISTS contested boolean NOT NULL DEFAULT false,

    -- The farmer's AgriStack crop is not one the event covers.
    --
    -- Not a refusal. AgriStack returns an empty crop_sown_data for every
    -- farmer this platform has been able to reach, so refusing on a mismatch
    -- would lock out the genuine claimant far more often than it would catch
    -- anybody. It is a discrepancy for the village pair to settle standing in
    -- the field, which is the only place it can honestly be settled.
    ADD COLUMN IF NOT EXISTS crop_mismatch boolean NOT NULL DEFAULT false,

    -- What AgriStack said the crop was, kept so the officer sees the actual
    -- disagreement rather than a boolean.
    ADD COLUMN IF NOT EXISTS registry_crop_name text;

CREATE INDEX IF NOT EXISTS crop_loss_reports_awaiting_level
    ON crop_loss_reports (approval_level, contested DESC, reported_at)
    WHERE approval_level IS NOT NULL AND state <> 'closed';

COMMENT ON COLUMN crop_loss_reports.approval_level IS
    'The rung this report is waiting at. A level may only act on reports '
    'sitting at its own rung, which is what stops a Tehsildar approving '
    'something the Circle has not seen.';
