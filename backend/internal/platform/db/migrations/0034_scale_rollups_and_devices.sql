-- Three things that only hurt at scale: a queue that makes writers wait, a
-- health page that reads every row, and one more count(*)+1 reference.

-- --------------------------------------------------------------------------
-- 1. The rollup queue stops blocking the filings that feed it
-- --------------------------------------------------------------------------
--
-- The mark was INSERT ... ON CONFLICT DO NOTHING on a primary key of
-- (event, village). Postgres makes the second inserter *wait* for the first
-- transaction to finish before it can know whether it conflicts -- so two
-- farmers filing in the same village at the same moment serialised on the
-- mark, for the length of the whole filing transaction. On the morning after
-- an event that is every filing in a village, one after another.
--
-- So the queue becomes append-only: an insert that conflicts with nothing
-- never waits. Duplicates are the price, and they are cheap -- the sweeper
-- already groups by (event, village), and the rows are three columns wide.
--
-- It also fixes a lost update. The sweeper deleted by (event, village) after
-- recomputing, which deleted marks made *while* it was recomputing: a report
-- filed in that window left a rollup that was quietly wrong until the next
-- report in that village. Claimed rows are now deleted by identity, so a mark
-- that arrived during the sweep survives it.

ALTER TABLE report_rollup_dirty DROP CONSTRAINT IF EXISTS report_rollup_dirty_pkey;

CREATE INDEX IF NOT EXISTS report_rollup_dirty_marked_idx
    ON report_rollup_dirty (marked_at);

COMMENT ON TABLE report_rollup_dirty IS
    'Append-only queue of (event, village) pairs whose rollup is stale. '
    'Deliberately without a unique key: a writer must never wait on it. '
    'The sweeper groups duplicates and deletes the rows it claimed.';

CREATE OR REPLACE FUNCTION mark_rollup_dirty() RETURNS trigger AS $$
BEGIN
    IF TG_OP <> 'INSERT' AND OLD.survey_event_id IS NOT NULL THEN
        INSERT INTO report_rollup_dirty (survey_event_id, village_lgd_code)
        VALUES (OLD.survey_event_id, OLD.village_lgd_code);
    END IF;
    IF TG_OP <> 'DELETE' AND NEW.survey_event_id IS NOT NULL THEN
        INSERT INTO report_rollup_dirty (survey_event_id, village_lgd_code)
        VALUES (NEW.survey_event_id, NEW.village_lgd_code);
    END IF;
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;

CREATE OR REPLACE FUNCTION mark_rollup_dirty_from_item() RETURNS trigger AS $$
DECLARE
    rid uuid;
BEGIN
    rid := COALESCE(NEW.report_id, OLD.report_id);
    INSERT INTO report_rollup_dirty (survey_event_id, village_lgd_code)
    SELECT r.survey_event_id, r.village_lgd_code
      FROM crop_loss_reports r
     WHERE r.id = rid AND r.survey_event_id IS NOT NULL;
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;

-- --------------------------------------------------------------------------
-- 2. Device health without reading every device
-- --------------------------------------------------------------------------
--
-- The console's health page counted and summed across the whole table on
-- every load. At a lakh of handsets that is a sequential scan per refresh, on
-- the same pool the write path uses.
--
-- A stored total gives the partial indexes something to be partial on: the
-- questions the page actually asks -- which phones have work waiting, which
-- have been stuck for a day, which have refusals -- are all about the small
-- minority of rows, and are now answered from an index over that minority.

ALTER TABLE device_sync_status
    ADD COLUMN IF NOT EXISTS pending_total integer
        GENERATED ALWAYS AS (pending_reports + pending_captures
                           + pending_boundaries + pending_appeals) STORED;

CREATE INDEX IF NOT EXISTS device_sync_status_pending_idx
    ON device_sync_status (pending_total, oldest_pending_at)
    INCLUDE (pending_reports, pending_captures, pending_bytes)
    WHERE pending_total > 0;

CREATE INDEX IF NOT EXISTS device_sync_status_failed_idx
    ON device_sync_status (failed_items)
    WHERE failed_items > 0;

CREATE INDEX IF NOT EXISTS device_sync_status_reported_idx
    ON device_sync_status (reported_at);

-- --------------------------------------------------------------------------
-- 3. Survey event references from a counter, like report references
-- --------------------------------------------------------------------------
--
-- The last count(*)+1 in the platform. Events are created by officers one at
-- a time, so a collision is unlikely rather than impossible -- and "unlikely"
-- is what migration 0033 was written about.

CREATE TABLE IF NOT EXISTS event_reference_counters (
    district_id uuid    NOT NULL,
    year        text    NOT NULL,
    last_number integer NOT NULL CHECK (last_number >= 0),
    PRIMARY KEY (district_id, year)
);

COMMENT ON TABLE event_reference_counters IS
    'The last event reference number issued per district and year. Advanced '
    'atomically inside the transaction that creates the event.';

INSERT INTO event_reference_counters (district_id, year, last_number)
SELECT district_id,
       extract(year FROM incident_start)::text,
       max(COALESCE(substring(reference FROM '-([0-9]+)$')::integer, 0))
  FROM survey_events
 WHERE reference LIKE 'SE-%'
 GROUP BY district_id, extract(year FROM incident_start)::text
ON CONFLICT (district_id, year) DO UPDATE
   SET last_number = GREATEST(event_reference_counters.last_number,
                              EXCLUDED.last_number);
