-- Search that stays fast as the tables grow.
--
-- Every listing in the console is about to gain a search box, and the search
-- an officer actually performs is a substring one: they type three letters of
-- a village or the tail of a reference number. `ILIKE '%barshi%'` cannot use
-- a B-tree index -- a B-tree can find a prefix and nothing else -- so without
-- what follows, every keystroke is a sequential scan of the table.
--
-- At today's nineteen reports that is invisible. At a district's worth after
-- one bad season it is a search box that takes seconds and gets slower every
-- week, which is the kind of thing that is only ever noticed in the field.
--
-- pg_trgm indexes the three-character sequences in a string, so a substring
-- match becomes an index lookup. The cost is index size and slower writes;
-- both are the right trade for a table that is read on every screen and
-- written once per report.

CREATE EXTENSION IF NOT EXISTS pg_trgm;

-- --- crop loss reports ------------------------------------------------------
--
-- The busiest table, and the one with the most ways to look something up: an
-- officer has a reference number from a farmer on the phone, or a name, or
-- knows only the village. One index per column rather than a concatenated
-- one, because the planner can combine them and a combined index cannot be
-- used for a single column.
CREATE INDEX IF NOT EXISTS crop_loss_reports_reference_trgm
    ON crop_loss_reports USING gin (reference gin_trgm_ops);
CREATE INDEX IF NOT EXISTS crop_loss_reports_farmer_name_trgm
    ON crop_loss_reports USING gin (farmer_name gin_trgm_ops);
CREATE INDEX IF NOT EXISTS crop_loss_reports_village_name_trgm
    ON crop_loss_reports USING gin (village_name gin_trgm_ops);

-- Mobile numbers are searched by their tail: a farmer reads out the last four
-- digits, or an officer has the number from a call log without the country
-- code. That is a substring search too, not a prefix one.
CREATE INDEX IF NOT EXISTS crop_loss_reports_mobile_trgm
    ON crop_loss_reports USING gin (farmer_mobile gin_trgm_ops)
    WHERE farmer_mobile IS NOT NULL;

-- --- assessments ------------------------------------------------------------
CREATE INDEX IF NOT EXISTS assessments_farm_id_trgm
    ON assessments USING gin (farm_id gin_trgm_ops);
CREATE INDEX IF NOT EXISTS assessments_survey_number_trgm
    ON assessments USING gin (survey_number gin_trgm_ops);
CREATE INDEX IF NOT EXISTS assessments_crop_name_trgm
    ON assessments USING gin (crop_name gin_trgm_ops)
    WHERE crop_name IS NOT NULL;

-- --- officers ---------------------------------------------------------------
--
-- Small today and small for a long time -- a district has tens of officers,
-- not thousands. Indexed anyway because the admin screen searches the same
-- way as every other screen, and an unindexed search that happens to be fast
-- is a difference in behaviour nobody documented.
CREATE INDEX IF NOT EXISTS users_full_name_trgm
    ON users USING gin (full_name gin_trgm_ops);
CREATE INDEX IF NOT EXISTS users_username_trgm
    ON users USING gin (username gin_trgm_ops)
    WHERE username IS NOT NULL;

-- --- survey events ----------------------------------------------------------
CREATE INDEX IF NOT EXISTS survey_events_reference_trgm
    ON survey_events USING gin (reference gin_trgm_ops);

-- --- jurisdictions ----------------------------------------------------------
--
-- 45,305 rows, and the one table where this is already load-bearing rather
-- than anticipatory: an officer choosing a village from 44,911 of them is
-- typing into a search box, and every keystroke is a query.
CREATE INDEX IF NOT EXISTS jurisdictions_name_trgm
    ON jurisdictions USING gin (name gin_trgm_ops);
CREATE INDEX IF NOT EXISTS jurisdictions_code_trgm
    ON jurisdictions USING gin (code gin_trgm_ops);

-- --- audit ------------------------------------------------------------------
--
-- Append-only and the fastest-growing table here: one row per request that
-- touches a record. It is read by someone investigating a specific thing --
-- an action, an officer, a record -- so the search has to hold up when the
-- table is the largest in the database.
CREATE INDEX IF NOT EXISTS audit_log_action_trgm
    ON audit_log USING gin (action gin_trgm_ops);

-- Who did it. An investigation usually starts from a person, and actor_label
-- is what the console shows, so it is what someone will type.
CREATE INDEX IF NOT EXISTS audit_log_actor_label_trgm
    ON audit_log USING gin (actor_label gin_trgm_ops)
    WHERE actor_label IS NOT NULL;

-- The record a search is about: "what happened to report CLR-2025-000041".
CREATE INDEX IF NOT EXISTS audit_log_resource_id_trgm
    ON audit_log USING gin (resource_id gin_trgm_ops)
    WHERE resource_id IS NOT NULL;

-- No index is added for the audit listing's ordering: audit_log_at_idx
-- (at DESC) already exists from migration 0001.
