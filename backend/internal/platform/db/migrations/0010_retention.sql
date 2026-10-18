-- What this platform is allowed to keep, and for how long.
--
-- It holds the most sensitive combination this kind of system can: a named
-- person, their Aadhaar reference, their land, photographs of that land, and
-- a GPS trace accurate to a few metres of where they stood. None of it had an
-- expiry. "We keep everything forever" is not a retention policy, it is the
-- absence of one, and under the DPDP Act it is also not lawful — personal
-- data may be kept only as long as the purpose it was collected for requires.
--
-- # What this migration does and does not do
--
-- It records the decision and gives the platform the machinery to act on it.
-- It deliberately deletes nothing on its own: the periods below are a
-- starting position, not a legal opinion, and the one irreversible operation
-- in this system should not begin running because a migration was applied.
-- Enabling it is a deliberate act, and marked as one.
--
-- # Why anonymise rather than delete
--
-- A crop loss assessment is a public record of public money. Deleting the
-- report would erase the fact that a claim was made and decided, which is
-- exactly what an audit needs years later. So the record survives and the
-- person is removed from it: the assessment, the area and the figure stay,
-- the name, the mobile, the Aadhaar hash and the photographs go.
--
-- The audit log is not touched at all. It is append-only by design, it is
-- what answers a grievance, and it holds identifiers rather than content.

CREATE TABLE retention_policy (
    -- One row per class of data. Named rather than numbered so a policy
    -- change reads as a policy change in the migration history.
    subject text PRIMARY KEY,

    -- How long after the record's own clock starts. Null means "keep", which
    -- has to be stated explicitly rather than being what happens when nobody
    -- decided.
    retain_for interval,

    -- What happens at the end of it.
    action text NOT NULL CHECK (action IN ('anonymise', 'delete', 'keep')),

    -- Off until someone turns it on. The purge is the one operation here
    -- that cannot be undone by a revert.
    enabled boolean NOT NULL DEFAULT false,

    basis text NOT NULL,

    updated_at timestamptz NOT NULL DEFAULT now()
);

INSERT INTO retention_policy (subject, retain_for, action, basis) VALUES
    (
        'evidence_media', '7 years', 'delete',
        'Photographs and video of a named person''s land. The longest an '
        'assessment is realistically challenged or audited, after which the '
        'images serve no purpose the figures do not. The report survives; '
        'the images do not.'
    ),
    (
        'capture_location', '7 years', 'anonymise',
        'The GPS fix places a person in a field on a date. Once the '
        'assessment is beyond challenge it is the most re-identifying field '
        'left, and the gate results it produced are kept without it.'
    ),
    (
        'farmer_contact', '7 years', 'anonymise',
        'Name, mobile and Aadhaar hash on a settled report. The claim, the '
        'area and the decision remain a public record of public money; the '
        'person does not have to.'
    ),
    (
        'audit_log', NULL, 'keep',
        'Append-only, and what answers a grievance years later. Holds '
        'identifiers and outcomes rather than personal content, so it is the '
        'one place retention does not apply.'
    ),
    (
        'assessment_imagery', '3 years', 'delete',
        'NDVI and terrain overlays are derived from public satellite data and '
        'can be recomputed. They are stored for convenience, and convenience '
        'is not a reason to hold imagery of a person''s land indefinitely.'
    );

-- Which records have already been through it. A purge that cannot say what it
-- did is a purge nobody can answer a question about afterwards.
CREATE TABLE retention_runs (
    id         bigserial PRIMARY KEY,
    subject    text NOT NULL REFERENCES retention_policy (subject),
    ran_at     timestamptz NOT NULL DEFAULT now(),
    -- Counted, never listed. A log of which people were forgotten would
    -- defeat the forgetting.
    affected   integer NOT NULL,
    dry_run    boolean NOT NULL,
    cutoff     timestamptz NOT NULL,
    note       text
);

CREATE INDEX retention_runs_subject_idx ON retention_runs (subject, ran_at DESC);

-- Marks a report whose personal fields have been cleared, so the anonymiser
-- is idempotent and a reader can tell an empty name from a missing one.
ALTER TABLE crop_loss_reports
    ADD COLUMN IF NOT EXISTS anonymised_at timestamptz;

CREATE INDEX crop_loss_reports_retention_idx
    ON crop_loss_reports (reported_at)
    WHERE anonymised_at IS NULL;

-- A capture's position has to be removable, which it was not: lat and lon
-- were NOT NULL, so the anonymiser could not have run.
--
-- Absence now means one thing and is never ambiguous, because anonymised_at
-- says so. A capture arriving without a position is still refused: that check
-- moves into survey.Submit, which is the only place that can tell "never had
-- one" from "no longer has one" -- a constraint here can forbid the first
-- only by also forbidding the second.
ALTER TABLE survey_captures
    ALTER COLUMN lat DROP NOT NULL,
    ALTER COLUMN lon DROP NOT NULL,
    ADD COLUMN IF NOT EXISTS anonymised_at timestamptz;

-- Either a capture has a position or it has neither half of one. A row with
-- a latitude and no longitude is a bug, not a partially forgotten record.
ALTER TABLE survey_captures
    ADD CONSTRAINT capture_position_is_whole
    CHECK ((lat IS NULL) = (lon IS NULL));

CREATE TRIGGER retention_policy_touch BEFORE UPDATE ON retention_policy
    FOR EACH ROW EXECUTE FUNCTION touch_updated_at();
