-- Assessments.
--
-- These were held in memory while the input was a handful of mock parcels.
-- They move here now because authorization needs them to be: a Talathi may
-- only see assessments in their village, and "which village" has to be a
-- column the query can filter on, not a field a reviewer trusts the API to
-- check after loading everything.

CREATE TABLE assessments (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),

    -- Registry identifiers, as AgriStack returns them.
    farmer_id     text NOT NULL,
    farm_id       text NOT NULL,
    survey_number text NOT NULL,

    -- The jurisdiction the parcel sits in, copied at assessment time.
    --
    -- Denormalised on purpose. Village codes are the filter on every list
    -- query, and a boundary redrawn next year must not retroactively change
    -- who was allowed to see a claim decided this year.
    village_lgd_code      text NOT NULL,
    sub_district_lgd_code text,
    district_lgd_code     text,
    state_lgd_code        text,

    sowing_date   date NOT NULL,
    incident_date date NOT NULL,
    crop_name     text,

    -- The owner's share, which is what a loss would be apportioned over --
    -- not the polygon area.
    owner_extent_hectares numeric(12, 4) NOT NULL
        CHECK (owner_extent_hectares > 0),

    -- The agent's verified judgement and the deterministic figures it
    -- reasoned over, stored whole.
    --
    -- jsonb rather than columns because these are the record of what one
    -- model version concluded from one set of evidence. Promoting fields to
    -- columns would invite updating them, and a past assessment is not a
    -- mutable row -- it is what was decided, on the evidence available, by a
    -- named prompt version. The few fields that must be queryable are lifted
    -- out below.
    assessment jsonb NOT NULL,
    satellite  jsonb NOT NULL,
    terrain    jsonb NOT NULL,

    -- Lifted out of `assessment` for indexing. Kept in step by the trigger
    -- below rather than by the application, so a second writer cannot
    -- disagree with the first.
    triage_tier    text NOT NULL,
    crop_presence  text NOT NULL,
    damage_percent numeric(5, 2) CHECK (damage_percent BETWEEN 0 AND 100),
    confidence     numeric(4, 3) NOT NULL CHECK (confidence BETWEEN 0 AND 1),
    critical_fraud boolean NOT NULL DEFAULT false,

    -- Every cloud-free observation, so a reviewer can see where the gaps fall.
    ndvi_series jsonb,

    model          text,
    prompt_version text,

    created_by uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX assessments_village_idx ON assessments (village_lgd_code, created_at DESC);
CREATE INDEX assessments_district_idx ON assessments (district_lgd_code, created_at DESC);
CREATE INDEX assessments_farmer_idx ON assessments (farmer_id, created_at DESC);
CREATE INDEX assessments_farm_idx ON assessments (farm_id, incident_date);
CREATE INDEX assessments_triage_idx ON assessments (triage_tier, created_at DESC);
CREATE INDEX assessments_created_idx ON assessments (created_at DESC);
-- The queue an officer actually opens: what needs attention, newest first.
CREATE INDEX assessments_escalated_idx ON assessments (created_at DESC)
    WHERE triage_tier = 'RED_ANOMALY_ESCALATE';

-- Keeps the lifted columns honest.
CREATE FUNCTION assessments_lift_columns() RETURNS trigger AS $$
BEGIN
    NEW.crop_presence := COALESCE(NEW.assessment ->> 'crop_presence', 'inconclusive');
    NEW.confidence := COALESCE((NEW.assessment ->> 'confidence')::numeric, 0);

    -- Null is the ordinary case, not a missing value: satellite evidence
    -- alone cannot measure damage, so most assessments carry no figure.
    NEW.damage_percent := (NEW.assessment ->> 'damage_percent')::numeric;

    NEW.model := NEW.assessment ->> 'model';
    NEW.prompt_version := NEW.assessment ->> 'prompt_version';

    -- Guarded on the type, not with COALESCE. An absent key gives SQL NULL,
    -- which COALESCE handles, but a present key holding JSON null gives the
    -- jsonb scalar 'null', which it does not -- and jsonb_array_elements
    -- raises on a scalar. Go marshals an empty slice to null, so that is the
    -- common case rather than the exotic one.
    NEW.critical_fraud := EXISTS (
        SELECT 1
        FROM jsonb_array_elements(
            CASE WHEN jsonb_typeof(NEW.assessment -> 'fraud_indicators') = 'array'
                 THEN NEW.assessment -> 'fraud_indicators'
                 ELSE '[]'::jsonb
            END
        ) AS f
        WHERE f ->> 'severity' = 'critical'
    );

    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER assessments_lift
    BEFORE INSERT OR UPDATE ON assessments
    FOR EACH ROW EXECUTE FUNCTION assessments_lift_columns();

-- ---------------------------------------------------------------------------
-- Report imagery
-- ---------------------------------------------------------------------------

-- Held apart from the assessment row because each overlay is a base64 PNG of
-- tens of kilobytes and no list query wants them. When object storage exists
-- these become keys instead of payloads; the separation is what makes that a
-- contained change.
CREATE TABLE assessment_images (
    assessment_id uuid PRIMARY KEY
        REFERENCES assessments (id) ON DELETE CASCADE,
    ndvi_mid_date   jsonb,
    ndvi_last_visit jsonb,
    terrain_slope   text,
    terrain_aspect  text,
    created_at      timestamptz NOT NULL DEFAULT now()
);
