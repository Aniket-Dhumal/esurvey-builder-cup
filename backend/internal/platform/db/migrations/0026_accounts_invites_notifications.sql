-- Officer accounts by email invitation, officer notifications, and editable
-- event coverage.

-- ---------------------------------------------------------------------------
-- An officer account can now exist before it has a password
-- ---------------------------------------------------------------------------
--
-- The old constraint required every officer row to carry a password hash,
-- which forced whoever created the account to choose the password and then
-- read it out to its owner. That is the weakest part of the whole sign-in
-- story: the credential exists, in plain text, in a WhatsApp message, before
-- its owner has touched it.
--
-- An invited account has a username and no hash until its owner sets one. It
-- cannot be signed in to in the meantime -- auth requires a hash to compare
-- against, and a NULL hash fails that comparison rather than passing it.

ALTER TABLE users DROP CONSTRAINT officer_has_password;

-- In words: an officer always has a username; the password arrives when they
-- accept their invitation.
ALTER TABLE users ADD CONSTRAINT officer_has_username CHECK (
    kind <> 'officer' OR username IS NOT NULL
);

-- Email is the username for accounts created from now on, so it has to be
-- unique among officers. A partial index rather than a column constraint:
-- ten officers already exist with no email at all, and farmers share this
-- table and have no email requirement.
--
-- lower(), because an officer who was invited as R.Deshmukh@… and types
-- r.deshmukh@… is the same person, and a second account for them is a second
-- set of postings that nobody will remember to revoke.
CREATE UNIQUE INDEX users_officer_email_idx
    ON users (lower(email))
    WHERE kind = 'officer' AND email IS NOT NULL;

-- ---------------------------------------------------------------------------
-- Invitations
-- ---------------------------------------------------------------------------
--
-- The token is stored as a SHA-256 hash and never in the clear, for the same
-- reason a password is. Anyone with read access to this table would otherwise
-- hold a working credential for every account that has not yet been claimed.
CREATE TABLE account_invites (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    -- Hex SHA-256 of the token that went out in the email.
    token_hash text NOT NULL UNIQUE CHECK (token_hash ~ '^[0-9a-f]{64}$'),
    email      text NOT NULL,

    expires_at  timestamptz NOT NULL,
    accepted_at timestamptz,
    -- Set when the invitation is superseded by a re-send. Rows are never
    -- deleted: "was this account ever invited, and when" is exactly the
    -- question asked after an account turns out to be one nobody recognises.
    revoked_at  timestamptz,

    created_by uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

-- One live invitation per account. Re-sending revokes the previous one, so an
-- invitation link that was forwarded to the wrong address stops working the
-- moment a fresh one is issued.
CREATE UNIQUE INDEX account_invites_live_idx ON account_invites (user_id)
    WHERE accepted_at IS NULL AND revoked_at IS NULL;

-- ---------------------------------------------------------------------------
-- Outbound email
-- ---------------------------------------------------------------------------
--
-- Queued, then sent -- the same shape as the farmer SMS queue in
-- internal/notify, and for the same reason. No SMTP server is configured yet.
-- A mailer that swallowed messages when unconfigured would report success
-- while every invitation went nowhere; a queue reports the backlog, and
-- everything in it is still there when credentials are set.
CREATE TABLE outbound_email (
    id       bigserial PRIMARY KEY,
    to_email text NOT NULL,
    subject  text NOT NULL,
    body     text NOT NULL,

    -- What this message is about, so a failed invitation can be traced back
    -- to the account it was for without parsing the body.
    purpose       text NOT NULL,
    resource_type text,
    resource_id   uuid,

    created_at timestamptz NOT NULL DEFAULT now(),
    sent_at    timestamptz,
    attempts   integer NOT NULL DEFAULT 0,
    last_error text
);

-- The sender's queue: oldest unsent first. Partial, because once a message is
-- sent it is history and never scanned again.
CREATE INDEX outbound_email_pending_idx ON outbound_email (created_at)
    WHERE sent_at IS NULL;

-- ---------------------------------------------------------------------------
-- Officer notifications
-- ---------------------------------------------------------------------------
--
-- Distinct from internal/notify, which tells a *farmer* by SMS. This is the
-- console's own bell: work that has arrived at this officer's rung, reports
-- returned to them for correction, events opened over their area.
--
-- One row per officer per fact, rather than one row per fact fanned out at
-- read time, because "have I already seen this" is per person and has to
-- survive the report moving on to somebody else.
CREATE TABLE officer_notifications (
    id      uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,

    -- A stable code, not a sentence: the wording is the console's to choose
    -- and to translate, and it will change more often than the fact will.
    kind  text NOT NULL,
    title text NOT NULL,
    body  text,
    -- Where the officer goes to act on it. A notification that cannot be
    -- acted on is a distraction with a badge count.
    link  text,

    resource_type text,
    resource_id   uuid,

    created_at timestamptz NOT NULL DEFAULT now(),
    read_at    timestamptz
);

-- The bell's query: this officer's unread, newest first.
CREATE INDEX officer_notifications_unread_idx
    ON officer_notifications (user_id, created_at DESC)
    WHERE read_at IS NULL;

CREATE INDEX officer_notifications_recent_idx
    ON officer_notifications (user_id, created_at DESC);

-- The same fact must not notify the same officer twice. A report that moves
-- to a rung, is returned, and arrives again is a new fact and gets a new row;
-- a report re-read by a poller is not.
CREATE UNIQUE INDEX officer_notifications_once_idx
    ON officer_notifications (user_id, kind, resource_type, resource_id)
    WHERE resource_id IS NOT NULL AND read_at IS NULL;

-- ---------------------------------------------------------------------------
-- An event's coverage can be edited while it is running
-- ---------------------------------------------------------------------------
--
-- The old rule froze the area list at `declared`, on the reasoning that
-- officers and farmers are acting on it from then on. In practice a calamity
-- does not respect the moment a notification was signed: a Collector learns on
-- day three that two more circles were hit, and had no way to say so except by
-- declaring a second event over the same storm.
--
-- So areas stay editable while the event is running, and freeze when filing
-- ends. What does *not* change is the other half of this trigger: an area is
-- still a taluka or a circle, and still has to lie inside the declaring
-- district. That is the rule that stops one Collector notifying a calamity in
-- another Collector's district, and it holds at every state.
--
-- Removal is guarded in the store rather than here, because the guard needs to
-- name how many reports are holding the circle, and a trigger can only refuse.
CREATE OR REPLACE FUNCTION survey_event_area_is_valid() RETURNS trigger AS $$
DECLARE
    j            jurisdictions%ROWTYPE;
    ev_district  uuid;
    ev_state     survey_event_state;
BEGIN
    SELECT district_id, state INTO ev_district, ev_state
    FROM survey_events WHERE id = NEW.event_id;

    IF ev_state NOT IN ('draft', 'declared', 'open', 'paused') THEN
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

-- Deletes were not covered before, because areas could not be removed at all.
-- Now that they can, the same window applies: coverage is editable while the
-- event is running and fixed once filing has ended.
CREATE FUNCTION survey_event_area_removable() RETURNS trigger AS $$
DECLARE
    ev_state survey_event_state;
BEGIN
    SELECT state INTO ev_state FROM survey_events WHERE id = OLD.event_id;

    -- The event itself is going: the areas go with it.
    IF NOT FOUND THEN
        RETURN OLD;
    END IF;

    IF ev_state NOT IN ('draft', 'declared', 'open', 'paused') THEN
        RAISE EXCEPTION
            'event % is %; its areas can no longer be changed',
            OLD.event_id, ev_state;
    END IF;

    RETURN OLD;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER survey_event_areas_removable
    BEFORE DELETE ON survey_event_areas
    FOR EACH ROW EXECUTE FUNCTION survey_event_area_removable();
