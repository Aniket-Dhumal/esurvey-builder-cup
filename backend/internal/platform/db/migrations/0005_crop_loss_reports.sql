-- The Crop Loss Assessment Report.
--
-- Parameters taken from the Maharashtra crop loss intimation form (पीक नुकसान
-- सूचना फॉर्म, 2019). Everything specific to crop insurance is deliberately
-- absent: no scheme name, no loanee/non-loanee status, no bank account, no
-- premium amount or receipt date, no proposal or cover note number, and no
-- insured-area column beside the sown area. This platform assesses loss and
-- stops there.
--
-- What survives from that form is the part that describes the loss itself, and
-- it survives because it is already the right shape: a farmer, a place, a
-- season, a table of survey numbers with sown and affected area, a cause with
-- a date and time, a declaration, and an acknowledgement.
--
-- Two things the form gets right that are worth naming.
--
-- It records महसूल मंडळ -- the revenue circle -- as a location field in its own
-- right, alongside village, taluka and district. That is the level LGD has no
-- code for and the level a Circle Officer is posted to, and its presence on the
-- official form is the argument for carrying circles in the jurisdiction tree.
--
-- It separates loss to a standing crop from post-harvest loss, and applies
-- different calamities to each. A cyclone flattening a harvested heap in the
-- field is not the same event as flood water standing in a growing crop, and an
-- assessment that conflates them measures the wrong thing.

CREATE TYPE loss_category AS ENUM (
    -- स्थानिक नैसर्गिक आपत्ती: inundation, hailstorm, landslide.
    'standing_crop',
    -- काढणी पश्चात नुकसान: cyclone, unseasonal rain, on a harvested crop
    -- lying in the field.
    'post_harvest'
);

CREATE TYPE crop_season AS ENUM ('kharif', 'rabi', 'summer');

CREATE TYPE report_state AS ENUM (
    'draft',
    'submitted',
    'acknowledged',
    'inspected',
    'closed',
    'withdrawn'
);

CREATE TABLE crop_loss_reports (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),

    -- Quoted on paper, in a grievance and on the acknowledgement slip.
    reference text NOT NULL UNIQUE,

    -- Every report is filed against a district's notification. Not nullable:
    -- a report with no event is a claim against nothing, filed for a calamity
    -- nobody declared.
    survey_event_id uuid NOT NULL
        REFERENCES survey_events (id) ON DELETE RESTRICT,

    -- --- the farmer ---------------------------------------------------------
    -- Copied from the registry at filing time rather than joined. A report is
    -- a record of what was stated on a date; a farmer who later changes their
    -- address has not changed what this report said.
    farmer_id      text NOT NULL,
    farmer_name    text NOT NULL,
    farmer_address text,
    farmer_mobile  text,
    farmer_email   text,

    -- --- the place ----------------------------------------------------------
    -- गाव / महसूल मंडळ / तालुका / जिल्हा, exactly the four the form asks for.
    -- Codes and names both: the code is what the system reasons over, the name
    -- is what a person reads on the printed report.
    village_lgd_code      text NOT NULL,
    village_name          text NOT NULL,
    circle_code           text,
    circle_name           text,
    sub_district_lgd_code text NOT NULL,
    sub_district_name     text NOT NULL,
    district_lgd_code     text NOT NULL,
    district_name         text NOT NULL,

    season    crop_season NOT NULL,
    crop_year text NOT NULL CHECK (crop_year ~ '^[0-9]{4}(-[0-9]{2})?$'),

    -- --- the loss -----------------------------------------------------------
    loss_category loss_category NOT NULL,
    calamity_type calamity_type NOT NULL,
    -- दिनांक आणि वेळ. Time, not just date: a hailstorm at 16:40 and one at
    -- 04:00 the next morning are different events to a surveyor.
    occurred_at timestamptz NOT NULL,

    -- --- filing -------------------------------------------------------------
    reported_at      timestamptz NOT NULL DEFAULT now(),
    reported_by      uuid REFERENCES users (id) ON DELETE SET NULL,
    reported_channel text NOT NULL DEFAULT 'field_officer'
        CHECK (reported_channel IN ('field_officer', 'farmer_app', 'call_centre')),

    -- The form makes intimation within 48 hours binding. Recorded rather than
    -- enforced: a village with no signal for three days is not a fraudulent
    -- claim, and refusing it at the door would push exactly the farmers this
    -- system exists for back to a paper queue. The officer sees the flag and
    -- decides.
    filed_late boolean NOT NULL DEFAULT false,
    late_reason text,

    -- --- the declaration ----------------------------------------------------
    -- "I will not alter the damaged crop, or any part of it, or its condition,
    -- before the inspection is complete."
    declaration_accepted boolean NOT NULL DEFAULT false,
    -- Proof of title the farmer undertakes to produce: 7/12 extract, lease or
    -- sharecropper agreement. The insurance premium receipt the original form
    -- also demanded is not among them.
    documents_promised text[] NOT NULL DEFAULT '{}',

    farmer_signature_kind text
        CHECK (farmer_signature_kind IN ('signature', 'thumb')),
    farmer_signed_at timestamptz,

    -- --- acknowledgement (पोहोच पावती) --------------------------------------
    acknowledged_by uuid REFERENCES users (id) ON DELETE SET NULL,
    acknowledged_at timestamptz,

    state report_state NOT NULL DEFAULT 'draft',

    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),

    -- A submitted report carries the farmer's declaration and mark. Without
    -- both it is a draft somebody has not finished, and calling it submitted
    -- would let it reach an inspection queue unsigned.
    CONSTRAINT submitted_is_declared CHECK (
        state = 'draft' OR state = 'withdrawn'
        OR (declaration_accepted AND farmer_signature_kind IS NOT NULL)
    ),
    CONSTRAINT acknowledged_has_officer CHECK (
        (acknowledged_at IS NULL) = (acknowledged_by IS NULL)
    )
);

CREATE INDEX crop_loss_reports_event_idx ON crop_loss_reports (survey_event_id, reported_at DESC);
CREATE INDEX crop_loss_reports_village_idx ON crop_loss_reports (village_lgd_code, reported_at DESC);
CREATE INDEX crop_loss_reports_district_idx ON crop_loss_reports (district_lgd_code, reported_at DESC);
CREATE INDEX crop_loss_reports_farmer_idx ON crop_loss_reports (farmer_id, reported_at DESC);
CREATE INDEX crop_loss_reports_state_idx ON crop_loss_reports (state, reported_at DESC);
-- The queue an officer opens: filed, not yet acknowledged.
CREATE INDEX crop_loss_reports_pending_idx ON crop_loss_reports (reported_at)
    WHERE state = 'submitted';

CREATE TRIGGER crop_loss_reports_touch BEFORE UPDATE ON crop_loss_reports
    FOR EACH ROW EXECUTE FUNCTION touch_updated_at();

-- ---------------------------------------------------------------------------
-- The crop table
-- ---------------------------------------------------------------------------

-- One row per survey number, as on the form. The insured-area column between
-- sown area and affected area is gone; nothing else about the table changes.
CREATE TABLE crop_loss_report_items (
    id        uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    report_id uuid NOT NULL REFERENCES crop_loss_reports (id) ON DELETE CASCADE,
    line_no   integer NOT NULL CHECK (line_no > 0),

    survey_number     text NOT NULL,
    sub_survey_number text,
    -- The registry parcel this line refers to, where one is known. Nullable:
    -- a farmer may report loss on land the registry has not yet linked.
    farm_id text,

    crop_name text NOT NULL,

    -- लागवडीखालील क्षेत्र / अंदाजित नुकसानग्रस्त क्षेत्र / नुकसानीची अंदाजित टक्केवारी
    area_sown_ha     numeric(10, 4) NOT NULL CHECK (area_sown_ha > 0),
    area_affected_ha numeric(10, 4) NOT NULL CHECK (area_affected_ha >= 0),
    damage_percent   numeric(5, 2) NOT NULL
        CHECK (damage_percent BETWEEN 0 AND 100),

    UNIQUE (report_id, line_no),

    -- More land damaged than sown is a data-entry error every time, and it is
    -- the one that inflates a payout. Caught here rather than in a form
    -- validator that a bulk import would bypass.
    CONSTRAINT affected_within_sown CHECK (area_affected_ha <= area_sown_ha)
);

CREATE INDEX crop_loss_report_items_report_idx ON crop_loss_report_items (report_id, line_no);
CREATE INDEX crop_loss_report_items_survey_idx ON crop_loss_report_items (survey_number);

-- ---------------------------------------------------------------------------
-- Which calamities belong to which loss category
-- ---------------------------------------------------------------------------

-- The form is specific about this and the specificity is the point: hail on a
-- standing crop is a localised calamity; a cyclone is only in scope once the
-- crop is cut and lying in the field. Allowing any calamity in either category
-- would let a report describe something that cannot happen.
CREATE FUNCTION report_calamity_is_valid() RETURNS trigger AS $$
BEGIN
    IF NEW.loss_category = 'standing_crop'
       AND NEW.calamity_type NOT IN ('flood', 'hailstorm', 'landslide',
                                     'cloudburst', 'drought', 'pest_attack',
                                     'frost', 'fire') THEN
        RAISE EXCEPTION
            '% is not a standing-crop calamity', NEW.calamity_type;
    END IF;

    IF NEW.loss_category = 'post_harvest'
       AND NEW.calamity_type NOT IN ('cyclone', 'unseasonal_rain', 'hailstorm') THEN
        RAISE EXCEPTION
            '% is not a post-harvest calamity; post-harvest loss covers '
            'cyclone, unseasonal rain and hail on a cut crop', NEW.calamity_type;
    END IF;

    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER crop_loss_reports_calamity
    BEFORE INSERT OR UPDATE OF loss_category, calamity_type ON crop_loss_reports
    FOR EACH ROW EXECUTE FUNCTION report_calamity_is_valid();

-- ---------------------------------------------------------------------------
-- Link to assessment
-- ---------------------------------------------------------------------------

-- An assessment is evidence about a report. Nullable while the ad-hoc
-- assessment path still exists.
ALTER TABLE assessments
    ADD COLUMN crop_loss_report_id uuid
        REFERENCES crop_loss_reports (id) ON DELETE SET NULL;

CREATE INDEX assessments_report_idx ON assessments (crop_loss_report_id);
