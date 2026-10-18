-- Verification of a Crop Loss Assessment Report by the village's allocated
-- authorities.
--
-- This is the step that turns "a farmer says the crop was damaged" into "the
-- administration finds that it was, by this much". Until now the platform
-- could accept a report and reason about satellite evidence; it could not
-- decide anything. Nothing else it does matters if this step is wrong.
--
-- # Who verifies
--
-- The Talathi and the Krushi Sahayak posted to the village the land sits in.
-- Not any officer with reach over that village -- a Tehsildar can see it and
-- is not its allocated authority. Scope says who may look; assignment says who
-- must act, and they are different questions.
--
-- # Why the pair record blind
--
-- The two verify different things: the Talathi the land record, ownership and
-- survey-number identity; the Krushi Sahayak the crop, its condition and
-- whether the loss is agronomically plausible. They are a pair, not a
-- sequence. If the second sees the first's number before recording their own,
-- the agreement between them means nothing -- and that agreement is the only
-- honest measure this system will ever have of how good its own assessment is.
--
-- # Why they do not see a model damage figure
--
-- They see the crop-presence finding and the fraud indicators before the
-- visit, because those are actionable in the field: "the record says soybean
-- sown in June, the curve peaks in October, check what is actually growing".
-- They do not see a damage percentage, and today the model does not produce
-- one without ground imagery anyway. An officer anchored to a number is an
-- officer whose finding cannot be used to calibrate that number.

-- ---------------------------------------------------------------------------
-- The report state machine grows
-- ---------------------------------------------------------------------------

-- From an enum to a checked text column.
--
-- PostgreSQL enums cannot gain a value and have it used inside the same
-- transaction, so every future state would arrive as a two-migration dance.
-- This state machine is going to keep growing -- grievance and appeal are
-- still to come -- and a CHECK constraint makes each addition one line.
-- The constraint and the partial index below both hold literals that were
-- resolved to report_state when they were created; they must go before the
-- column can change type, and come back afterwards against text.
ALTER TABLE crop_loss_reports DROP CONSTRAINT submitted_is_declared;
DROP INDEX crop_loss_reports_pending_idx;

ALTER TABLE crop_loss_reports ALTER COLUMN state DROP DEFAULT;
ALTER TABLE crop_loss_reports ALTER COLUMN state TYPE text USING state::text;
ALTER TABLE crop_loss_reports ALTER COLUMN state SET DEFAULT 'draft';
DROP TYPE report_state;

ALTER TABLE crop_loss_reports ADD CONSTRAINT submitted_is_declared CHECK (
    state IN ('draft', 'withdrawn')
    OR (declaration_accepted AND farmer_signature_kind IS NOT NULL)
);

-- The queue an officer opens: filed, not yet allocated.
CREATE INDEX crop_loss_reports_pending_idx ON crop_loss_reports (reported_at)
    WHERE state IN ('submitted', 'acknowledged');

ALTER TABLE crop_loss_reports ADD CONSTRAINT report_state_known CHECK (
    state IN (
        'draft',
        'submitted',
        'acknowledged',
        'assigned',
        'under_verification',
        'verified',
        'adjudicated',
        'closed',
        'withdrawn'
    )
);

-- ---------------------------------------------------------------------------
-- Who the village's authorities are
-- ---------------------------------------------------------------------------

-- The officers allocated to a village, by live grant.
--
-- A posting at circle or taluka level counts: acting charge over a
-- neighbouring area is routine, and an officer holding it is genuinely the
-- village's authority for as long as they hold it. What does not count is a
-- role that is not a field role -- a Tehsildar has reach and is not the
-- allocated verifier.
CREATE FUNCTION village_authorities(p_village_code text)
RETURNS TABLE (user_id uuid, officer_role text, full_name text, jurisdiction_level text)
AS $$
    SELECT u.id, ur.role::text, u.full_name, j.level::text
    FROM user_roles ur
    JOIN users u ON u.id = ur.user_id
    JOIN jurisdictions j ON j.id = ur.jurisdiction_id
    JOIN jurisdictions v ON v.level = 'village' AND v.code = p_village_code
    WHERE ur.revoked_at IS NULL
      AND u.status = 'active'
      AND ur.role IN ('talathi', 'krushi_sahayak')
      AND (j.id = v.id OR j.id = ANY (v.ancestor_ids))
    -- Nearest posting first: a village-level Talathi outranks one holding
    -- acting charge from the taluka.
    ORDER BY array_position(
        ARRAY['village', 'circle', 'sub_district', 'district', 'state'],
        j.level::text
    );
$$ LANGUAGE sql STABLE;

-- ---------------------------------------------------------------------------
-- Assignment
-- ---------------------------------------------------------------------------

CREATE TABLE report_assignments (
    id        uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    report_id uuid NOT NULL REFERENCES crop_loss_reports (id) ON DELETE CASCADE,

    officer_id uuid NOT NULL REFERENCES users (id) ON DELETE RESTRICT,
    role       officer_role NOT NULL,

    -- village_pair is the Talathi and Krushi Sahayak; arbitration is the
    -- Circle Officer resolving their disagreement; escalation is the Tehsildar.
    tier text NOT NULL CHECK (tier IN ('village_pair', 'arbitration', 'escalation')),

    assigned_at timestamptz NOT NULL DEFAULT now(),
    -- Null when the system allocated it, which is the ordinary case. Set when
    -- a supervisor reassigned it, and then it is worth knowing who.
    assigned_by uuid REFERENCES users (id) ON DELETE SET NULL,

    -- Five tiers is five places for a report to stall. The deadline is what
    -- stops the hierarchy reproducing its own delays with better logging.
    due_at       timestamptz NOT NULL,
    completed_at timestamptz,

    -- Set when a reassignment supersedes this row. Never deleted: who was
    -- asked to verify, and when, is exactly what an inquiry asks afterwards.
    superseded_at timestamptz,
    superseded_reason text,

    UNIQUE (report_id, officer_id, tier)
);

CREATE INDEX report_assignments_officer_idx
    ON report_assignments (officer_id, completed_at, due_at)
    WHERE superseded_at IS NULL;
CREATE INDEX report_assignments_report_idx ON report_assignments (report_id);
-- The worklist query: mine, open, oldest deadline first.
CREATE INDEX report_assignments_open_idx ON report_assignments (due_at)
    WHERE completed_at IS NULL AND superseded_at IS NULL;

-- ---------------------------------------------------------------------------
-- Findings
-- ---------------------------------------------------------------------------

CREATE TABLE report_verifications (
    id        uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    report_id uuid NOT NULL REFERENCES crop_loss_reports (id) ON DELETE CASCADE,

    officer_id uuid NOT NULL REFERENCES users (id) ON DELETE RESTRICT,
    role       officer_role NOT NULL,
    tier       text NOT NULL
        CHECK (tier IN ('village_pair', 'arbitration', 'escalation')),

    outcome text NOT NULL CHECK (outcome IN (
        'confirmed',        -- the loss is as reported, or close to it
        'partly_confirmed', -- a loss occurred, of a different extent
        'rejected',         -- no loss attributable to the notified calamity
        'not_traceable'     -- the parcel or the farmer could not be found
    )),

    -- The officer's own figure. Null on rejected and not_traceable, where
    -- there is no percentage to state.
    damage_percent   numeric(5, 2) CHECK (damage_percent BETWEEN 0 AND 100),
    area_affected_ha numeric(10, 4) CHECK (area_affected_ha >= 0),

    -- What they actually saw. Required: a finding with no reasoning is a
    -- number nobody can argue with, and the whole point of a panchnama is
    -- that it can be argued with.
    findings text NOT NULL CHECK (length(btrim(findings)) >= 10),

    visited_at timestamptz,

    -- What the officer could see when they recorded this.
    --
    -- Kept because it is the difference between a finding that can calibrate
    -- the model and one that cannot. An officer who saw a model figure and
    -- then wrote a similar one has told us nothing about the model.
    saw_peer_finding     boolean NOT NULL DEFAULT false,
    saw_model_assessment boolean NOT NULL DEFAULT false,

    submitted_at timestamptz NOT NULL DEFAULT now(),

    -- One finding per officer per tier. A finding is not a draft that gets
    -- edited after the peer's arrives.
    UNIQUE (report_id, officer_id, tier),

    CONSTRAINT figure_matches_outcome CHECK (
        (outcome IN ('rejected', 'not_traceable') AND damage_percent IS NULL)
        OR (outcome IN ('confirmed', 'partly_confirmed') AND damage_percent IS NOT NULL)
    )
);

CREATE INDEX report_verifications_report_idx ON report_verifications (report_id, tier);
CREATE INDEX report_verifications_officer_idx ON report_verifications (officer_id, submitted_at DESC);

-- Append-only. A verification an officer can revise after seeing their
-- colleague's is not an independent verification, and the blindness the whole
-- design rests on would be a convention rather than a guarantee.
CREATE FUNCTION report_verifications_are_final() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION
        'a recorded verification cannot be % ; record a fresh one at the next tier',
        lower(TG_OP);
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER report_verifications_no_rewrite
    BEFORE UPDATE OR DELETE ON report_verifications
    FOR EACH ROW EXECUTE FUNCTION report_verifications_are_final();

-- Per-survey-number findings, mirroring the report's own crop table.
CREATE TABLE report_verification_items (
    verification_id uuid NOT NULL
        REFERENCES report_verifications (id) ON DELETE CASCADE,
    line_no integer NOT NULL CHECK (line_no > 0),

    -- What was actually found growing, which is not always what was reported.
    crop_name        text,
    area_sown_ha     numeric(10, 4) CHECK (area_sown_ha >= 0),
    area_affected_ha numeric(10, 4) CHECK (area_affected_ha >= 0),
    damage_percent   numeric(5, 2) CHECK (damage_percent BETWEEN 0 AND 100),

    PRIMARY KEY (verification_id, line_no),
    CONSTRAINT verified_affected_within_sown CHECK (
        area_sown_ha IS NULL OR area_affected_ha IS NULL
        OR area_affected_ha <= area_sown_ha
    )
);

-- ---------------------------------------------------------------------------
-- Adjudication
-- ---------------------------------------------------------------------------

-- The one number that leaves this system, and the record of what the officer
-- who set it was looking at when they did.
--
-- shown_* is not decoration. It is the raw material for every question worth
-- asking later: how far apart were the two officers, how close was the model,
-- does an officer who sees a model figure land nearer to it. None of that can
-- be reconstructed afterwards from anything else, which is why it is captured
-- at the moment of the decision rather than derived from one.
CREATE TABLE report_adjudications (
    id        uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    -- One per report. The final figure has exactly one writer.
    report_id uuid NOT NULL UNIQUE
        REFERENCES crop_loss_reports (id) ON DELETE CASCADE,

    officer_id uuid NOT NULL REFERENCES users (id) ON DELETE RESTRICT,
    role       officer_role NOT NULL,

    -- What was in front of them.
    shown_first_percent  numeric(5, 2),
    shown_second_percent numeric(5, 2),
    shown_model_percent  numeric(5, 2),
    divergence           numeric(5, 2),
    route text NOT NULL CHECK (route IN ('consensus', 'arbitration', 'escalation')),

    -- What they decided.
    adjudicated_percent numeric(5, 2) NOT NULL
        CHECK (adjudicated_percent BETWEEN 0 AND 100),
    adjudicated_area_ha numeric(10, 4) NOT NULL
        CHECK (adjudicated_area_ha >= 0),
    reason text NOT NULL CHECK (length(btrim(reason)) >= 10),

    decided_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX report_adjudications_officer_idx
    ON report_adjudications (officer_id, decided_at DESC);
CREATE INDEX report_adjudications_route_idx ON report_adjudications (route, decided_at DESC);

CREATE TRIGGER report_adjudications_no_rewrite
    BEFORE UPDATE OR DELETE ON report_adjudications
    FOR EACH ROW EXECUTE FUNCTION report_verifications_are_final();

-- The adjudicated figure lifted onto the report, for listing and rollup.
ALTER TABLE crop_loss_reports
    ADD COLUMN adjudicated_percent numeric(5, 2)
        CHECK (adjudicated_percent BETWEEN 0 AND 100),
    ADD COLUMN adjudicated_area_ha numeric(10, 4),
    ADD COLUMN adjudicated_at timestamptz;

CREATE INDEX crop_loss_reports_adjudicated_idx
    ON crop_loss_reports (adjudicated_at DESC)
    WHERE adjudicated_at IS NOT NULL;
