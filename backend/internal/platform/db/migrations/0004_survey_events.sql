-- Survey events: the district's notification that a calamity has occurred.
--
-- Nothing here existed before, and its absence was a hole. Without it,
-- `incident_date` is a field an officer types, there is nothing to validate a
-- claim's timing against, and there is no unit to aggregate over -- "how bad
-- was the October hailstorm in Barshi" is not a question the schema could
-- answer.
--
-- The District Magistrate declares an event and names the talukas or circles
-- it covers. Only farmers whose land sits under one of those areas may be
-- surveyed against it. That single constraint removes retrospective claiming
-- and claiming from an unaffected taluka in one stroke.

CREATE TYPE calamity_type AS ENUM (
    'flood',
    'cyclone',
    'hailstorm',
    'unseasonal_rain',
    'drought',
    'pest_attack',
    'landslide',
    'cloudburst',
    'frost',
    'fire'
);

-- draft is editable; everything from declared onward is not, because officers
-- and farmers are acting on it by then.
CREATE TYPE survey_event_state AS ENUM (
    'draft',
    'declared',
    'open',
    'closed',
    'assessment_complete',
    'cancelled'
);

CREATE TABLE survey_events (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),

    -- Human-facing identifier, quoted on paper and in a grievance. Unique
    -- per district per year: SE-2026-496-001.
    reference text NOT NULL UNIQUE,

    calamity_type calamity_type NOT NULL,
    title         text NOT NULL CHECK (length(title) BETWEEN 3 AND 200),
    description   text,

    -- The district order or government resolution this mirrors, where one
    -- exists. Nullable: the platform can run ahead of the paperwork, but the
    -- reference is what makes the event auditable against the record.
    notification_ref text,

    -- The declaring district. An event belongs to exactly one; a calamity
    -- crossing districts is two events, because two Collectors sign.
    district_id uuid NOT NULL REFERENCES jurisdictions (id) ON DELETE RESTRICT,

    -- When the calamity happened. A claim's incident date must fall inside.
    incident_start date NOT NULL,
    incident_end   date NOT NULL,

    -- When surveying is permitted. Closing is a deliberate act with a date,
    -- because the deadline is contestable and someone has to own it.
    survey_opens_at  timestamptz,
    survey_closes_at timestamptz NOT NULL,

    state survey_event_state NOT NULL DEFAULT 'draft',

    declared_by uuid REFERENCES users (id) ON DELETE SET NULL,
    declared_at timestamptz,
    closed_by   uuid REFERENCES users (id) ON DELETE SET NULL,
    closed_at   timestamptz,

    created_by uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT incident_window_ordered CHECK (incident_end >= incident_start),
    CONSTRAINT survey_closes_after_incident
        CHECK (survey_closes_at::date >= incident_end),
    -- A declared event has a declarer. Recorded, not inferred from the log.
    CONSTRAINT declared_has_declarer CHECK (
        (state IN ('draft', 'cancelled')) OR
        (declared_by IS NOT NULL AND declared_at IS NOT NULL)
    )
);

CREATE INDEX survey_events_district_idx ON survey_events (district_id, created_at DESC);
CREATE INDEX survey_events_state_idx ON survey_events (state, survey_closes_at);
CREATE INDEX survey_events_window_idx ON survey_events (incident_start, incident_end);

-- ---------------------------------------------------------------------------
-- The areas an event covers
-- ---------------------------------------------------------------------------

-- Talukas or circles within the declaring district. Not villages: a
-- village-by-village notification is not how a calamity is declared, and it
-- would make a hailstorm across a taluka a hundred-row form.
CREATE TABLE survey_event_areas (
    event_id        uuid NOT NULL REFERENCES survey_events (id) ON DELETE CASCADE,
    jurisdiction_id uuid NOT NULL REFERENCES jurisdictions (id) ON DELETE RESTRICT,
    added_by        uuid REFERENCES users (id) ON DELETE SET NULL,
    added_at        timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (event_id, jurisdiction_id)
);

CREATE INDEX survey_event_areas_jurisdiction_idx
    ON survey_event_areas (jurisdiction_id);

-- An area must be a taluka or circle, and it must lie inside the district
-- that declared the event.
--
-- Enforced here rather than only in the application: this is the rule that
-- stops one Collector notifying a calamity in another Collector's district,
-- and a rule with that consequence should not depend on every future write
-- path remembering it.
CREATE FUNCTION survey_event_area_is_valid() RETURNS trigger AS $$
DECLARE
    j            jurisdictions%ROWTYPE;
    ev_district  uuid;
    ev_state     survey_event_state;
BEGIN
    SELECT district_id, state INTO ev_district, ev_state
    FROM survey_events WHERE id = NEW.event_id;

    IF ev_state NOT IN ('draft', 'declared') THEN
        RAISE EXCEPTION
            'event % is %; its areas can no longer be changed',
            NEW.event_id, ev_state;
    END IF;

    SELECT * INTO j FROM jurisdictions WHERE id = NEW.jurisdiction_id;

    IF j.level NOT IN ('sub_district', 'circle') THEN
        RAISE EXCEPTION
            'an event area must be a taluka or a circle, not a %', j.level;
    END IF;

    IF NOT (ev_district = ANY (j.ancestor_ids)) THEN
        RAISE EXCEPTION
            'jurisdiction % is outside the declaring district', j.code;
    END IF;

    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER survey_event_areas_valid
    BEFORE INSERT OR UPDATE ON survey_event_areas
    FOR EACH ROW EXECUTE FUNCTION survey_event_area_is_valid();

CREATE TRIGGER survey_events_touch BEFORE UPDATE ON survey_events
    FOR EACH ROW EXECUTE FUNCTION touch_updated_at();

-- ---------------------------------------------------------------------------
-- Assessments belong to an event
-- ---------------------------------------------------------------------------

-- Nullable for now. Assessments predate events and the ad-hoc path is still
-- how the reference parcel is exercised. It becomes NOT NULL when claims
-- land, at which point every assessment hangs off a claim and every claim off
-- an event.
ALTER TABLE assessments
    ADD COLUMN survey_event_id uuid REFERENCES survey_events (id) ON DELETE RESTRICT;

CREATE INDEX assessments_event_idx ON assessments (survey_event_id, created_at DESC);

-- ---------------------------------------------------------------------------
-- Eligibility
-- ---------------------------------------------------------------------------

-- Is this village covered by this event?
--
-- The same array-overlap containment the authorization layer uses, so a
-- village inside a selected circle, or inside a selected taluka, or inside a
-- circle inside a selected taluka, all answer true without three cases.
CREATE FUNCTION village_in_event(p_event_id uuid, p_village_code text)
RETURNS boolean AS $$
    SELECT EXISTS (
        SELECT 1
        FROM survey_event_areas a
        JOIN jurisdictions v
          ON v.level = 'village' AND v.code = p_village_code
        WHERE a.event_id = p_event_id
          AND (a.jurisdiction_id = v.id OR a.jurisdiction_id = ANY (v.ancestor_ids))
    );
$$ LANGUAGE sql STABLE;
