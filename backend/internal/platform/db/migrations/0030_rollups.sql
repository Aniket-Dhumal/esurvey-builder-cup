-- Aggregates that do not scan the claims table.
--
-- # Why now, when the live GROUP BY is correct
--
-- Because it is correct at this volume and cannot be at the next one. Every
-- aggregate in the platform is computed live: report.Overview runs a GROUP BY
-- over crop_loss_reports on each dashboard load, and the state-level view
-- added alongside this migration would run it over the whole state.
--
-- The planning estimate is order 10^6 claims a season statewide. A State
-- Officer's dashboard is then a full scan of the largest table in the
-- database, on every page load, for the officer least likely to tolerate a
-- slow page and most likely to have the whole state in scope.
--
-- # Why the rollup is keyed at the village and not at every level
--
-- A rollup per district, per taluka and per village would be three tables to
-- keep consistent and, worse, a district row that has to be recomputed every
-- time any one of its two thousand villages changes.
--
-- So there is one row per (event, village, crop) -- the leaf -- and everything
-- above it is summed at query time across jurisdictions.ancestor_ids, which is
-- already indexed because it is how every authorization check works. A state
-- query touches roughly 45,000 village rows times the open events rather than
-- 10^6 claims, and that ceiling is fixed by the size of Maharashtra rather
-- than by how many claims get filed.
--
-- # Why percentages are stored as numerators
--
-- A damage percentage cannot be averaged upward. Two villages at 60% do not
-- make a taluka at 60% unless they had the same area affected, and a total
-- loss on a tenth of a hectare beside a scratch on four hectares does not
-- average to half.
--
-- So the row stores sum(percent x affected area) beside the affected area
-- itself. Any level above divides one total by the other and gets the true
-- area-weighted figure. Storing the percentage directly would produce a number
-- that looks right at every level and is wrong at all of them but the leaf.

CREATE TABLE IF NOT EXISTS report_rollups (
    survey_event_id  uuid NOT NULL
        REFERENCES survey_events (id) ON DELETE CASCADE,
    village_lgd_code text NOT NULL,
    -- The empty string for a line that named no crop, rather than NULL: this
    -- is a primary key column, and NULL in one would make the row
    -- unaddressable by the upsert that maintains it.
    crop_name text NOT NULL DEFAULT '',

    reports  integer NOT NULL DEFAULT 0,
    -- Distinct within this key only. Summing it upward over-counts a
    -- household that filed in two villages, which is why the state view
    -- counts farmers separately rather than adding this column up.
    farmers  integer NOT NULL DEFAULT 0,

    area_sown_ha     numeric(16, 4) NOT NULL DEFAULT 0,
    area_affected_ha numeric(16, 4) NOT NULL DEFAULT 0,

    -- Work, by who owes it.
    unresolved  integer NOT NULL DEFAULT 0,
    adjudicated integer NOT NULL DEFAULT 0,
    contested   integer NOT NULL DEFAULT 0,
    filed_late  integer NOT NULL DEFAULT 0,

    -- Numerators. See the note above: divide by area_affected_ha to get the
    -- area-weighted percentage at any level.
    declared_pct_area    numeric(20, 4) NOT NULL DEFAULT 0,
    adjudicated_pct_area numeric(20, 4) NOT NULL DEFAULT 0,
    -- The affected area behind the adjudicated numerator, which is not the
    -- whole affected area: a report still climbing the ladder has no
    -- adjudicated figure, and including its area in the denominator would
    -- drag every district's average toward zero as claims are filed.
    adjudicated_area_ha numeric(16, 4) NOT NULL DEFAULT 0,

    refreshed_at timestamptz NOT NULL DEFAULT now(),

    PRIMARY KEY (survey_event_id, village_lgd_code, crop_name)
);

-- The village is the join key for every rollup upward.
CREATE INDEX IF NOT EXISTS report_rollups_village_idx
    ON report_rollups (village_lgd_code);
CREATE INDEX IF NOT EXISTS report_rollups_crop_idx
    ON report_rollups (crop_name);

-- --------------------------------------------------------------------------
-- What has changed since the last refresh
-- --------------------------------------------------------------------------
--
-- # Why a queue and not a full rebuild on a timer
--
-- A full rebuild is the same full scan the rollup exists to avoid, moved off
-- the request and onto a ticker. It would still take minutes at 10^6 claims
-- and it would still be running when the next tick arrived.
--
-- So a trigger records which (event, village) pairs a write touched, and the
-- sweeper recomputes only those. During an event that is a handful of
-- villages a minute; at rest it is nothing at all.
--
-- # Why the queue is a table and not Redis
--
-- The mark has to commit with the write that caused it. A report that is
-- filed and a mark that is lost leave a rollup that is quietly wrong and
-- never repaired, and nothing downstream can tell -- an aggregate has no
-- individual row to check against. The same outbox reasoning as the
-- notification queue.

CREATE TABLE IF NOT EXISTS report_rollup_dirty (
    survey_event_id  uuid NOT NULL,
    village_lgd_code text NOT NULL,
    marked_at        timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (survey_event_id, village_lgd_code)
);

CREATE OR REPLACE FUNCTION mark_rollup_dirty() RETURNS trigger AS $$
BEGIN
    -- Both sides on an update: a report whose village was corrected leaves
    -- the old village's rollup wrong as well as the new one's.
    IF TG_OP <> 'INSERT' AND OLD.survey_event_id IS NOT NULL THEN
        INSERT INTO report_rollup_dirty (survey_event_id, village_lgd_code)
        VALUES (OLD.survey_event_id, OLD.village_lgd_code)
        ON CONFLICT DO NOTHING;
    END IF;
    IF TG_OP <> 'DELETE' AND NEW.survey_event_id IS NOT NULL THEN
        INSERT INTO report_rollup_dirty (survey_event_id, village_lgd_code)
        VALUES (NEW.survey_event_id, NEW.village_lgd_code)
        ON CONFLICT DO NOTHING;
    END IF;
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS crop_loss_reports_rollup ON crop_loss_reports;
CREATE TRIGGER crop_loss_reports_rollup
    AFTER INSERT OR UPDATE OR DELETE ON crop_loss_reports
    FOR EACH ROW EXECUTE FUNCTION mark_rollup_dirty();

-- The lines carry the areas and the crop names, so they dirty the rollup too.
-- They have no event or village of their own, so the mark is looked up.
CREATE OR REPLACE FUNCTION mark_rollup_dirty_from_item() RETURNS trigger AS $$
DECLARE
    rid uuid;
BEGIN
    rid := COALESCE(NEW.report_id, OLD.report_id);
    INSERT INTO report_rollup_dirty (survey_event_id, village_lgd_code)
    SELECT r.survey_event_id, r.village_lgd_code
      FROM crop_loss_reports r
     WHERE r.id = rid AND r.survey_event_id IS NOT NULL
    ON CONFLICT DO NOTHING;
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS crop_loss_report_items_rollup ON crop_loss_report_items;
CREATE TRIGGER crop_loss_report_items_rollup
    AFTER INSERT OR UPDATE OR DELETE ON crop_loss_report_items
    FOR EACH ROW EXECUTE FUNCTION mark_rollup_dirty_from_item();

-- Everything that already exists is dirty, so the first sweep builds the
-- table. Deliberately not built here: a migration that aggregates the whole
-- claims table is a migration that can time out on a restore, and the sweeper
-- does the same work in bounded batches with the API already serving.
INSERT INTO report_rollup_dirty (survey_event_id, village_lgd_code)
SELECT DISTINCT survey_event_id, village_lgd_code
  FROM crop_loss_reports
 WHERE survey_event_id IS NOT NULL
ON CONFLICT DO NOTHING;
