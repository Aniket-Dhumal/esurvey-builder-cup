-- A farmer's right to be heard again.
--
-- Until now a farmer whose report was adjudicated at a figure they disagree
-- with, or closed without a figure at all, had nowhere to go. The platform
-- could record a decision and could show it to them; it had no way for them
-- to say it was wrong. For a government loss-assessment scheme that is not a
-- missing feature, it is a missing right -- appeal is ordinarily statutory,
-- and a scheme without one is one farmers learn not to trust.
--
-- # What a grievance is, and is not
--
-- It is not a second assessment. Re-running the panel on the same evidence
-- produces the same answer, and inviting that would make the appeal
-- theatre. A grievance names *what the farmer says was missed*, and it is
-- answered by a person senior to the one who decided -- which is why the
-- table records who it was escalated past, not merely who answered it.
--
-- # Why it is a separate table rather than a report state
--
-- A report has one life; a grievance can happen more than once, and each one
-- has its own filing, its own deadline and its own outcome. Folding it into
-- the report would either lose the earlier one or turn the state column into
-- a history nobody can query.

CREATE TYPE grievance_ground AS ENUM (
    -- The four things a farmer actually disputes. Free text as well, below,
    -- but a category is what lets a Collector see that thirty grievances in
    -- one taluka are all about the same thing.
    'area_wrong',          -- the extent assessed is not the land they lost
    'damage_percent_low',  -- the figure does not match what they saw
    'not_inspected',       -- nobody came, or came to the wrong plot
    'evidence_ignored',    -- photographs were filed and not considered
    'other'
);

CREATE TYPE grievance_state AS ENUM (
    'open',        -- filed, awaiting an officer
    'under_review',
    'upheld',      -- the farmer was right; the figure changes
    'partly_upheld',
    'rejected',    -- the original decision stands
    'withdrawn'    -- the farmer no longer wants it pursued
);

CREATE TABLE grievances (
    id        uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    reference text NOT NULL UNIQUE,

    report_id uuid NOT NULL REFERENCES crop_loss_reports (id) ON DELETE CASCADE,

    -- Denormalised for the same reason the report denormalises them: every
    -- later authorization decision about this row is made against them, and
    -- a village reassigned next year must not silently move an old
    -- grievance into someone else's jurisdiction.
    farmer_id        text NOT NULL,
    farmer_name      text NOT NULL,
    village_lgd_code text NOT NULL,

    ground grievance_ground NOT NULL,
    -- What the farmer says, in their words. A category alone cannot carry
    -- "they measured the wrong survey number" -- and that sentence is
    -- usually the whole grievance.
    statement text NOT NULL CHECK (length(btrim(statement)) >= 10),

    -- What they say the figure should be. Optional: a farmer may know they
    -- were treated unfairly without having a number in mind, and demanding
    -- one would filter out the honest ones.
    claimed_percent numeric(5, 2) CHECK (claimed_percent BETWEEN 0 AND 100),

    -- The decision being appealed, captured at filing time. If the report is
    -- re-adjudicated later, this still says what was actually disputed.
    disputed_percent numeric(5, 2),
    disputed_at      timestamptz,

    state grievance_state NOT NULL DEFAULT 'open',

    filed_at timestamptz NOT NULL DEFAULT now(),
    filed_by uuid REFERENCES users (id) ON DELETE SET NULL,
    -- A grievance filed at a counter by an officer on the farmer's behalf is
    -- still the farmer's grievance. This records who pressed the button.
    filed_channel text NOT NULL DEFAULT 'app'
        CHECK (filed_channel IN ('app', 'counter', 'phone', 'written')),

    -- The deadline. A grievance with no deadline is a grievance that is
    -- never answered, which is the failure mode this whole table exists to
    -- prevent.
    due_at timestamptz NOT NULL,

    -- Who must answer it. Deliberately not the officer who decided: an
    -- appeal answered by the person appealed against is not an appeal.
    assigned_to   uuid REFERENCES users (id) ON DELETE SET NULL,
    escalated_past uuid REFERENCES users (id) ON DELETE SET NULL,

    -- The outcome.
    decided_at      timestamptz,
    decided_by      uuid REFERENCES users (id) ON DELETE RESTRICT,
    decision_reason text,
    revised_percent numeric(5, 2) CHECK (revised_percent BETWEEN 0 AND 100),

    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),

    -- An outcome without a reason is not an outcome a farmer can be shown.
    CONSTRAINT decided_has_reason CHECK (
        state IN ('open', 'under_review', 'withdrawn')
        OR (decided_at IS NOT NULL AND decided_by IS NOT NULL
            AND length(btrim(coalesce(decision_reason, ''))) >= 10)
    ),

    -- Upholding a grievance without changing the figure is a contradiction:
    -- it says the farmer was right and leaves them where they were.
    CONSTRAINT upheld_revises CHECK (
        state NOT IN ('upheld', 'partly_upheld') OR revised_percent IS NOT NULL
    )
);

CREATE INDEX grievances_report_idx  ON grievances (report_id, filed_at DESC);
CREATE INDEX grievances_farmer_idx  ON grievances (farmer_id, filed_at DESC);
CREATE INDEX grievances_village_idx ON grievances (village_lgd_code, filed_at DESC);
CREATE INDEX grievances_assigned_idx ON grievances (assigned_to, due_at)
    WHERE state IN ('open', 'under_review');

-- The queue an officer works from: open, oldest deadline first.
CREATE INDEX grievances_open_idx ON grievances (due_at)
    WHERE state IN ('open', 'under_review');

-- Searchable like every other listing.
CREATE INDEX grievances_reference_trgm
    ON grievances USING gin (reference gin_trgm_ops);
CREATE INDEX grievances_farmer_name_trgm
    ON grievances USING gin (farmer_name gin_trgm_ops);

CREATE TRIGGER grievances_touch BEFORE UPDATE ON grievances
    FOR EACH ROW EXECUTE FUNCTION touch_updated_at();

-- A grievance reference a farmer can read out over the phone.
CREATE SEQUENCE grievance_reference_seq;

CREATE OR REPLACE FUNCTION next_grievance_reference() RETURNS text AS $$
BEGIN
    RETURN 'GRV-' || to_char(now(), 'YYYY') || '-'
           || lpad(nextval('grievance_reference_seq')::text, 6, '0');
END;
$$ LANGUAGE plpgsql;
