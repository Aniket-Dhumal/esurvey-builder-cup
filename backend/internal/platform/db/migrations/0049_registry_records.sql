-- The farmer registry, held where every instance can read it.
--
-- # What this replaces
--
-- Every registry record lived in each API process's memory, loaded from files,
-- and every farmer and parcel listing walked all of them: filtered, sorted and
-- paged in Go on every request. Fine for a trial directory of a few records;
-- not for Maharashtra, where the registry is crores of farmers and a live
-- AgriStack cannot be enumerated at all -- it answers for one farmer at a
-- time, slowly, and sometimes not.
--
-- So the platform keeps what it has been told: a record lands here the first
-- time anything asks for that farmer (a sign-in, a lookup, a claim), and is
-- refreshed from the registry when it is older than the deployment allows.
-- Listings page and filter here, with the caller's jurisdiction in the query.
-- A registry that is down serves the last record it gave rather than refusing
-- a farmer who is standing in their field.
--
-- # What is never stored
--
-- A full Aadhaar number. The record carries the last four digits and the
-- registry's own hash; the constraint below refuses anything longer, so a
-- registry that one day sends the whole number cannot put it here.

CREATE TABLE IF NOT EXISTS registry_farmers (
    farmer_id             text PRIMARY KEY CHECK (length(farmer_id) BETWEEN 1 AND 64),
    farmer_name           text NOT NULL DEFAULT '',
    state_lgd_code        text NOT NULL DEFAULT '',
    district_lgd_code     text NOT NULL DEFAULT '',
    sub_district_lgd_code text NOT NULL DEFAULT '',
    village_lgd_code      text NOT NULL DEFAULT '',
    mobile_no             text NOT NULL DEFAULT '',
    aadhaar_last4         text NOT NULL DEFAULT ''
        CHECK (aadhaar_last4 ~ '^[0-9Xx*]{0,4}$'),

    -- The listing's columns, computed once on write rather than per request.
    parcels       integer NOT NULL DEFAULT 0,
    with_boundary integer NOT NULL DEFAULT 0,
    crops         integer NOT NULL DEFAULT 0,
    extent_ha     double precision NOT NULL DEFAULT 0,

    record     jsonb NOT NULL,
    record_sha text NOT NULL,
    -- Where it came from ("file:<name>", "agristack"), for an operator asking
    -- why a record says what it says.
    source     text NOT NULL,
    -- When the registry last answered for this farmer, changed or not.
    fetched_at timestamptz NOT NULL DEFAULT now(),
    changed_at timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT registry_farmers_no_full_aadhaar CHECK (
        coalesce(record #>> '{FarmerData,aadhaar}', '') ~ '^[0-9Xx*]{0,4}$'
    )
);

-- An officer's listing: their villages, by name.
CREATE INDEX IF NOT EXISTS registry_farmers_village_idx
    ON registry_farmers (village_lgd_code, farmer_name, farmer_id);
CREATE INDEX IF NOT EXISTS registry_farmers_name_idx
    ON registry_farmers (farmer_name, farmer_id);
-- Search by any part of a name, id or mobile.
CREATE INDEX IF NOT EXISTS registry_farmers_search_trgm
    ON registry_farmers
    USING gin ((farmer_name || ' ' || farmer_id || ' ' || mobile_no) gin_trgm_ops);

CREATE TABLE IF NOT EXISTS registry_parcels (
    farmer_id        text NOT NULL REFERENCES registry_farmers (farmer_id) ON DELETE CASCADE,
    farm_id          text NOT NULL,
    survey_number    text NOT NULL DEFAULT '',
    farmer_name      text NOT NULL DEFAULT '',
    village_lgd_code text NOT NULL DEFAULT '',
    owner_extent_ha  double precision NOT NULL DEFAULT 0,
    has_boundary     boolean NOT NULL DEFAULT false,
    crop_name        text NOT NULL DEFAULT '',
    joint_owners     integer NOT NULL DEFAULT 0,
    PRIMARY KEY (farmer_id, farm_id)
);

CREATE INDEX IF NOT EXISTS registry_parcels_village_idx
    ON registry_parcels (village_lgd_code, survey_number, farm_id);
CREATE INDEX IF NOT EXISTS registry_parcels_search_trgm
    ON registry_parcels
    USING gin ((survey_number || ' ' || farm_id || ' ' || farmer_name || ' ' || crop_name) gin_trgm_ops);
