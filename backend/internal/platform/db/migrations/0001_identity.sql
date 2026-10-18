-- Identity, jurisdiction and audit.
--
-- The authorization model this schema serves has two independent axes:
--
--   * what a person may do   -- their role's permissions (defined in Go, not
--                               here; see internal/rbac)
--   * where they may do it   -- their jurisdiction, resolved through the
--                               administrative tree below
--
-- Both must pass. A Tehsildar has assessment:review everywhere in their own
-- taluka and nowhere else, and neither half of that sentence is expressible
-- without the other.

CREATE EXTENSION IF NOT EXISTS pgcrypto;

-- ---------------------------------------------------------------------------
-- Jurisdictions
-- ---------------------------------------------------------------------------

-- Levels follow Maharashtra's revenue administration.
--
-- 'circle' (mandal / saza) sits between the taluka and the village and has no
-- LGD code of its own -- LGD stops at sub_district and resumes at village. It
-- is carried here with a locally assigned code because a Circle Officer's
-- jurisdiction is otherwise inexpressible. Every village belongs to exactly
-- one circle where circles are defined, so the tree stays a tree: a village
-- hangs off its circle when one exists and off its sub-district when it does
-- not.
CREATE TYPE jurisdiction_level AS ENUM (
    'state', 'district', 'sub_district', 'circle', 'village'
);

CREATE TABLE jurisdictions (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    level      jurisdiction_level NOT NULL,
    -- LGD code, except for circles, which carry a locally assigned one.
    code       text NOT NULL CHECK (length(code) BETWEEN 1 AND 32),
    name       text NOT NULL CHECK (length(name) BETWEEN 1 AND 200),
    parent_id  uuid REFERENCES jurisdictions (id) ON DELETE RESTRICT,

    -- Materialised ancestor chain, root first, self excluded.
    --
    -- Containment ("is this village inside my jurisdiction?") is the hottest
    -- authorization question in the system: it runs on every read of every
    -- assessment. A recursive CTE per request would not survive a
    -- district-wide listing, so the chain is denormalised and maintained by
    -- the trigger below.
    ancestor_ids uuid[] NOT NULL DEFAULT '{}',

    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),

    UNIQUE (level, code),
    -- Exactly one root, and it must be a state.
    CONSTRAINT jurisdiction_root_is_state
        CHECK ((parent_id IS NULL) = (level = 'state'))
);

CREATE INDEX jurisdictions_ancestors_idx ON jurisdictions USING gin (ancestor_ids);
CREATE INDEX jurisdictions_parent_idx ON jurisdictions (parent_id);
CREATE INDEX jurisdictions_code_idx ON jurisdictions (code);

-- Maintains ancestor_ids, and refuses a cycle rather than looping forever.
CREATE FUNCTION jurisdiction_set_ancestors() RETURNS trigger AS $$
DECLARE
    parent_ancestors uuid[];
BEGIN
    IF NEW.parent_id IS NULL THEN
        NEW.ancestor_ids := '{}';
    ELSE
        SELECT ancestor_ids INTO parent_ancestors
        FROM jurisdictions WHERE id = NEW.parent_id;

        IF NEW.id = ANY (parent_ancestors) OR NEW.id = NEW.parent_id THEN
            RAISE EXCEPTION 'jurisdiction % would form a cycle', NEW.id;
        END IF;

        NEW.ancestor_ids := parent_ancestors || NEW.parent_id;
    END IF;

    NEW.updated_at := now();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER jurisdictions_ancestors
    BEFORE INSERT OR UPDATE OF parent_id ON jurisdictions
    FOR EACH ROW EXECUTE FUNCTION jurisdiction_set_ancestors();

-- ---------------------------------------------------------------------------
-- Users
-- ---------------------------------------------------------------------------

CREATE TYPE user_status AS ENUM ('active', 'suspended', 'deactivated');

-- The two kinds of account authenticate differently and it is worth being
-- explicit about which is which: an officer has a password, a farmer has a
-- handset. Neither path is available to the other.
CREATE TYPE user_kind AS ENUM ('officer', 'farmer');

CREATE TABLE users (
    id        uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    kind      user_kind NOT NULL,
    status    user_status NOT NULL DEFAULT 'active',

    full_name text NOT NULL CHECK (length(full_name) BETWEEN 1 AND 200),
    email     text,

    -- Officer credentials.
    username      text UNIQUE,
    password_hash text,
    password_changed_at    timestamptz,
    must_change_password   boolean NOT NULL DEFAULT false,

    -- Farmer credentials. Mobile numbers are stored E.164 so that the same
    -- handset cannot register twice under two formats.
    mobile_e164 text UNIQUE CHECK (mobile_e164 ~ '^\+[1-9][0-9]{7,14}$'),

    -- Link to the registry. Never the raw Aadhaar: the hash is the registry's
    -- own key and the last four digits are all a human ever needs to see.
    agristack_farmer_id text UNIQUE,
    aadhaar_hash        text CHECK (aadhaar_hash ~ '^[0-9a-f]{64}$'),
    aadhaar_last4       text CHECK (aadhaar_last4 ~ '^[0-9]{4}$'),

    last_login_at timestamptz,
    created_by    uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),

    -- An officer without a password could not sign in; a farmer with one
    -- would have a second, weaker way in that nothing else audits.
    CONSTRAINT officer_has_password CHECK (
        kind <> 'officer' OR (username IS NOT NULL AND password_hash IS NOT NULL)
    ),
    CONSTRAINT farmer_has_handset CHECK (
        kind <> 'farmer' OR (mobile_e164 IS NOT NULL AND password_hash IS NULL)
    )
);

CREATE INDEX users_kind_status_idx ON users (kind, status);
CREATE INDEX users_farmer_id_idx ON users (agristack_farmer_id)
    WHERE agristack_farmer_id IS NOT NULL;

-- ---------------------------------------------------------------------------
-- Role grants
-- ---------------------------------------------------------------------------

-- The role list is fixed by administrative structure, so it is an enum here
-- and a matrix in Go. What each role may DO is deliberately not in this
-- database: a permission matrix is a policy artifact that belongs in version
-- control, where a change to it is reviewed like any other change. What is
-- stored is only who holds which role, and where.
CREATE TYPE officer_role AS ENUM (
    'system_admin',
    'state_officer',
    'district_magistrate',
    'tehsildar',
    'mandal_officer',
    'talathi',
    'krushi_sahayak',
    'farmer'
);

-- A grant is (person, role, place). Several rows per person is not an edge
-- case here: additional charge of a neighbouring circle is routine in revenue
-- administration, and a model that cannot express it gets worked around with
-- shared logins.
CREATE TABLE user_roles (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id         uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    role            officer_role NOT NULL,
    jurisdiction_id uuid REFERENCES jurisdictions (id) ON DELETE RESTRICT,

    granted_by uuid REFERENCES users (id) ON DELETE SET NULL,
    granted_at timestamptz NOT NULL DEFAULT now(),
    -- Set when a posting ends. Rows are never deleted: who could see what,
    -- when, is exactly what an audit asks afterwards.
    revoked_at timestamptz,

    -- Two roles are not posted anywhere. A farmer's authority is over their
    -- own records; a system administrator's is over the account roll, which
    -- is not a place either. Every other role is meaningless without one.
    CONSTRAINT role_has_jurisdiction CHECK (
        (role IN ('farmer', 'system_admin')) = (jurisdiction_id IS NULL)
    )
);

-- One live grant per (person, role, place).
CREATE UNIQUE INDEX user_roles_live_idx
    ON user_roles (user_id, role, COALESCE(jurisdiction_id, '00000000-0000-0000-0000-000000000000'::uuid))
    WHERE revoked_at IS NULL;
CREATE INDEX user_roles_user_idx ON user_roles (user_id) WHERE revoked_at IS NULL;
CREATE INDEX user_roles_jurisdiction_idx ON user_roles (jurisdiction_id) WHERE revoked_at IS NULL;

-- ---------------------------------------------------------------------------
-- Audit log
-- ---------------------------------------------------------------------------

CREATE TABLE audit_log (
    id         bigserial PRIMARY KEY,
    at         timestamptz NOT NULL DEFAULT now(),

    -- Null for events with no authenticated actor, which is most failed
    -- sign-ins and exactly the case an investigation cares about.
    actor_user_id uuid REFERENCES users (id) ON DELETE SET NULL,
    actor_label   text,
    actor_role    text,
    actor_ip      inet,

    action        text NOT NULL,
    resource_type text,
    resource_id   text,
    -- Where the acted-on thing was, so a query can ask what happened in a
    -- taluka without joining through resources that may since have moved.
    jurisdiction_code text,

    outcome    text NOT NULL CHECK (outcome IN ('allow', 'deny', 'error')),
    detail     jsonb NOT NULL DEFAULT '{}'::jsonb,
    request_id text
);

CREATE INDEX audit_log_at_idx ON audit_log (at DESC);
CREATE INDEX audit_log_actor_idx ON audit_log (actor_user_id, at DESC);
CREATE INDEX audit_log_action_idx ON audit_log (action, at DESC);
CREATE INDEX audit_log_resource_idx ON audit_log (resource_type, resource_id);
CREATE INDEX audit_log_denials_idx ON audit_log (at DESC) WHERE outcome = 'deny';

-- Append-only, enforced in the database rather than by convention.
--
-- An audit log the application can rewrite is not evidence. Retention work
-- that genuinely needs to remove rows drops this trigger deliberately, which
-- is itself a visible act.
CREATE FUNCTION audit_log_is_append_only() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'audit_log is append-only (attempted %)', TG_OP;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER audit_log_no_rewrite
    BEFORE UPDATE OR DELETE ON audit_log
    FOR EACH ROW EXECUTE FUNCTION audit_log_is_append_only();

-- ---------------------------------------------------------------------------
-- updated_at
-- ---------------------------------------------------------------------------

CREATE FUNCTION touch_updated_at() RETURNS trigger AS $$
BEGIN
    NEW.updated_at := now();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER users_touch BEFORE UPDATE ON users
    FOR EACH ROW EXECUTE FUNCTION touch_updated_at();
