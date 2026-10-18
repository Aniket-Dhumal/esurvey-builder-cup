-- The audit log, partitioned by month.
--
-- # Why
--
-- One row per audited action, kept for ever, on one table with nine indexes.
-- At a lakh of claims a day and roughly six audited actions each, that is
-- about 180 GB a year before indexes (docs/PLATFORM_SCALE_REVIEW.md, A2) --
-- on a single VM's disk, in a table nothing may delete from, because an audit
-- log the application can rewrite is not evidence.
--
-- Monthly partitions change what that means:
--
--   * A month can be detached, archived to the evidence bucket and dropped as
--     one operation, with the act visible in the schema rather than as a
--     DELETE somebody ran.
--   * The indexes an officer's search uses are per month, so they stay the
--     size of a month rather than of a decade.
--   * Autovacuum works a partition at a time instead of a table that never
--     stops growing.
--
-- # What is preserved
--
-- The hash chain. Rows are copied with their recorded entry_hash and
-- prev_hash, with the chain trigger disabled for the copy, so every hash after
-- this migration links to exactly the same predecessor it linked to before.
-- `audit_log_verify()` must still return no rows afterwards, and the suite
-- checks that.
--
-- The primary key becomes (id, at): a partitioned table's unique index must
-- contain the partition key. id is still unique in practice -- it comes from
-- the same sequence -- and every query that used it still works.

-- ---------------------------------------------------------------------------
-- The new shape
-- ---------------------------------------------------------------------------

ALTER TABLE audit_log RENAME TO audit_log_unpartitioned;
ALTER SEQUENCE audit_log_id_seq RENAME TO audit_log_entries_id_seq;

-- Renaming a table leaves its indexes under their old names, which the new
-- table wants. They go with the old table at the end of this migration.
ALTER INDEX audit_log_pkey RENAME TO audit_log_unpartitioned_pkey;
ALTER INDEX audit_log_at_idx RENAME TO audit_log_unpartitioned_at_idx;
ALTER INDEX audit_log_action_idx RENAME TO audit_log_unpartitioned_action_idx;
ALTER INDEX audit_log_actor_idx RENAME TO audit_log_unpartitioned_actor_idx;
ALTER INDEX audit_log_resource_idx RENAME TO audit_log_unpartitioned_resource_idx;
ALTER INDEX audit_log_denials_idx RENAME TO audit_log_unpartitioned_denials_idx;
ALTER INDEX audit_log_action_trgm RENAME TO audit_log_unpartitioned_action_trgm;
ALTER INDEX audit_log_actor_label_trgm RENAME TO audit_log_unpartitioned_label_trgm;
ALTER INDEX audit_log_resource_id_trgm RENAME TO audit_log_unpartitioned_res_trgm;

CREATE TABLE audit_log (
    id                bigint NOT NULL DEFAULT nextval('audit_log_entries_id_seq'),
    at                timestamptz NOT NULL DEFAULT now(),
    actor_user_id     uuid,
    actor_label       text,
    actor_role        text,
    actor_ip          inet,
    action            text NOT NULL,
    resource_type     text,
    resource_id       text,
    jurisdiction_code text,
    outcome           text NOT NULL
        CHECK (outcome IN ('allow', 'deny', 'error')),
    detail            jsonb NOT NULL DEFAULT '{}'::jsonb,
    request_id        text,
    entry_hash        text,
    prev_hash         text,
    PRIMARY KEY (id, at)
) PARTITION BY RANGE (at);

ALTER SEQUENCE audit_log_entries_id_seq OWNED BY audit_log.id;

-- The indexes the console's audit search and the chain both need. Declared on
-- the parent, so every partition -- including ones made years from now --
-- carries them.
CREATE INDEX audit_log_at_idx ON audit_log (at DESC);
CREATE INDEX audit_log_id_idx ON audit_log (id DESC);
CREATE INDEX audit_log_action_idx ON audit_log (action, at DESC);
CREATE INDEX audit_log_actor_idx ON audit_log (actor_user_id, at DESC);
CREATE INDEX audit_log_resource_idx ON audit_log (resource_type, resource_id);
CREATE INDEX audit_log_denials_idx ON audit_log (at DESC) WHERE outcome = 'deny';
CREATE INDEX audit_log_action_trgm ON audit_log USING gin (action gin_trgm_ops);
CREATE INDEX audit_log_actor_label_trgm ON audit_log USING gin (actor_label gin_trgm_ops)
    WHERE actor_label IS NOT NULL;
CREATE INDEX audit_log_resource_id_trgm ON audit_log USING gin (resource_id gin_trgm_ops)
    WHERE resource_id IS NOT NULL;

-- ---------------------------------------------------------------------------
-- Partitions, and how they are made
-- ---------------------------------------------------------------------------

-- ensure_audit_partition makes the month containing `when` if it is missing.
--
-- Called by housekeeping, months ahead of time. A log that cannot take a write
-- at midnight on the first of the month is an outage, so this never waits to
-- be needed.
CREATE OR REPLACE FUNCTION ensure_audit_partition(p_when timestamptz)
RETURNS text AS $$
DECLARE
    from_ts timestamptz := date_trunc('month', p_when);
    to_ts   timestamptz := date_trunc('month', p_when) + interval '1 month';
    name    text := 'audit_log_' || to_char(from_ts, 'YYYY_MM');
BEGIN
    IF to_regclass(name) IS NULL THEN
        EXECUTE format(
            'CREATE TABLE %I PARTITION OF audit_log FOR VALUES FROM (%L) TO (%L)',
            name, from_ts, to_ts);
    END IF;
    RETURN name;
END;
$$ LANGUAGE plpgsql;

COMMENT ON FUNCTION ensure_audit_partition(timestamptz) IS
    'Creates the monthly audit partition containing the given instant, if it '
    'does not exist. Called by housekeeping ahead of time.';

-- Everything already recorded, plus the current month and the next two, so a
-- deployment that is never swept still takes writes for a quarter.
DO $$
DECLARE
    m timestamptz;
    first_month timestamptz;
BEGIN
    SELECT date_trunc('month', COALESCE(min(at), now()))
      INTO first_month FROM audit_log_unpartitioned;

    m := first_month;
    WHILE m <= date_trunc('month', now()) + interval '2 months' LOOP
        PERFORM ensure_audit_partition(m);
        m := m + interval '1 month';
    END LOOP;
END $$;

-- ---------------------------------------------------------------------------
-- The rows, with their hashes
-- ---------------------------------------------------------------------------

ALTER TABLE audit_log DISABLE TRIGGER USER;

INSERT INTO audit_log (
    id, at, actor_user_id, actor_label, actor_role, actor_ip, action,
    resource_type, resource_id, jurisdiction_code, outcome, detail,
    request_id, entry_hash, prev_hash)
SELECT id, at, actor_user_id, actor_label, actor_role, actor_ip, action,
       resource_type, resource_id, jurisdiction_code, outcome, detail,
       request_id, entry_hash, prev_hash
  FROM audit_log_unpartitioned;

ALTER TABLE audit_log ENABLE TRIGGER USER;

-- The old table goes now that every row of it, with its recorded hashes, is in
-- the partitioned one. Nothing is lost: the count is asserted by the suite, and
-- audit_log_verify() still walks the chain from the first entry.
DROP TABLE audit_log_unpartitioned;

-- ---------------------------------------------------------------------------
-- The guarantees, re-armed on the new table
-- ---------------------------------------------------------------------------
--
-- Row triggers declared on a partitioned parent fire for every partition,
-- including ones created later, so these are stated once here.

CREATE TRIGGER audit_log_no_rewrite
    BEFORE UPDATE OR DELETE ON audit_log
    FOR EACH ROW EXECUTE FUNCTION audit_log_is_append_only();

CREATE TRIGGER audit_log_chain_link
    BEFORE INSERT ON audit_log
    FOR EACH ROW EXECUTE FUNCTION audit_log_chain();

COMMENT ON TABLE audit_log IS
    'Who did what, and whether it was allowed. Append-only by trigger, hash-'
    'chained by id, partitioned by month so a year can be archived and '
    'detached rather than deleted.';
