-- Say where the audit chain begins, so its verifier stops crying wolf.
--
-- # The defect
--
-- 0020 added a hash chain to audit_log and hashed every row written from then
-- on. The rows already in the table were not backfilled, because they could
-- not be: their hashes would have to be computed now, by us, which is exactly
-- the operation the chain exists to make impossible to do undetectably.
--
-- Leaving them alone was right. Leaving audit_log_verify() to start at row one
-- was not. It walks from the beginning, meets a row carrying no hash, and
-- reports the log as broken -- and it will report that for as long as those
-- rows exist. On this deployment that is 386 of 479 rows, so the answer is
-- "broken" every single time it is asked.
--
-- A tamper-evidence check that always says tampered is worse than none. The
-- one before it at least admits it cannot tell you; this one trains whoever
-- runs it to see the alarm and move on, which is precisely the habit an
-- attacker needs them to have.
--
-- # Why a recorded watermark rather than "skip rows without a hash"
--
-- Skipping unhashed rows would fix the noise and open a hole: an attacker who
-- can write to the table could null out entry_hash on the rows they altered
-- and the verifier would step over them. The chain has to know where it is
-- supposed to start, and a missing hash at or after that point has to be a
-- break rather than a row to ignore.
--
-- So the boundary is written down once, now, while it is still a fact about
-- history rather than a claim about the present.

CREATE TABLE IF NOT EXISTS audit_chain_start (
    -- One row, enforced. Two watermarks would be two answers to "where does
    -- the chain begin", and an auditor would have no way to choose.
    only_row boolean PRIMARY KEY DEFAULT true CHECK (only_row),

    from_id  bigint NOT NULL,
    reason   text   NOT NULL,
    set_at   timestamptz NOT NULL DEFAULT now()
);

INSERT INTO audit_chain_start (only_row, from_id, reason)
SELECT true,
       -- The first hashed row, or -- on a deployment where nothing has been
       -- written since 0020 -- the next id to be issued, so the chain begins
       -- with the next entry rather than with rows that never had a hash.
       COALESCE(
           (SELECT min(id) FROM audit_log WHERE entry_hash IS NOT NULL),
           (SELECT COALESCE(max(id), 0) + 1 FROM audit_log)),
       'entries before this id were written before migration 0020 added the '
       'hash chain and carry no hash; they are outside the chain by '
       'construction and cannot be brought into it without computing their '
       'hashes now, which is the forgery the chain exists to detect'
ON CONFLICT (only_row) DO NOTHING;

COMMENT ON TABLE audit_chain_start IS
    'Where the audit hash chain begins. Rows before from_id predate the chain '
    'and are outside it; a row at or after from_id with no hash is a break.';

-- Verification, from the watermark, treating a missing hash inside the chain
-- as the break it is.
--
-- Reads the same seed the writer used, so it must run in a session where
-- esurvey.audit_seed is set to the same value. A wrong seed reports the first
-- row as broken, which is the correct outcome: without the seed you cannot
-- verify the log, and you should not be told that you can.
CREATE OR REPLACE FUNCTION audit_log_verify(p_from bigint DEFAULT NULL)
RETURNS TABLE (
    broken_at    bigint,
    reason       text,
    recorded     text,
    recomputed   text
) AS $$
DECLARE
    r        record;
    expected text;
    seed     text;
    computed text;
    start_id bigint;
BEGIN
    seed := COALESCE(current_setting('esurvey.audit_seed', true), '');
    expected := NULL;

    -- An explicit argument still wins, so an auditor can point the check at
    -- any row they like and satisfy themselves rather than be satisfied.
    start_id := COALESCE(
        p_from,
        (SELECT from_id FROM audit_chain_start),
        0);

    FOR r IN
        SELECT * FROM audit_log WHERE id >= start_id ORDER BY id
    LOOP
        -- Inside the chain, a row with no hash is not a row to skip. It is
        -- either corruption or somebody removing the evidence of a change,
        -- and those are the same thing from here.
        IF r.entry_hash IS NULL THEN
            broken_at  := r.id;
            reason     := 'the entry carries no hash but sits inside the chain';
            recorded   := NULL;
            recomputed := NULL;
            RETURN NEXT;
            RETURN;
        END IF;

        -- The first row examined chains onto whatever it recorded; from then
        -- on the link must match what the previous row actually hashed to.
        IF expected IS NULL THEN
            expected := r.prev_hash;
        END IF;

        IF r.prev_hash IS DISTINCT FROM expected THEN
            broken_at  := r.id;
            reason     := 'the link to the previous entry does not match';
            recorded   := r.prev_hash;
            recomputed := expected;
            RETURN NEXT;
            RETURN;
        END IF;

        computed := encode(sha256(convert_to(
            seed || '|' || COALESCE(r.prev_hash, '') || '|' ||
            r.id::text || '|' ||
            r.at::text || '|' ||
            COALESCE(r.actor_user_id::text, '') || '|' ||
            COALESCE(r.actor_label, '') || '|' ||
            COALESCE(r.actor_role, '') || '|' ||
            COALESCE(r.actor_ip::text, '') || '|' ||
            r.action || '|' ||
            COALESCE(r.resource_type, '') || '|' ||
            COALESCE(r.resource_id, '') || '|' ||
            COALESCE(r.jurisdiction_code, '') || '|' ||
            r.outcome || '|' ||
            r.detail::text || '|' ||
            COALESCE(r.request_id, ''),
            'UTF8')), 'hex');

        IF computed IS DISTINCT FROM r.entry_hash THEN
            broken_at  := r.id;
            reason     := 'the entry does not hash to what it recorded';
            recorded   := r.entry_hash;
            recomputed := computed;
            RETURN NEXT;
            RETURN;
        END IF;

        expected := r.entry_hash;
    END LOOP;

    RETURN;
END;
$$ LANGUAGE plpgsql STABLE;

COMMENT ON FUNCTION audit_log_verify(bigint) IS
    'Returns no rows when the audit chain is intact from audit_chain_start.from_id, or one row naming the first entry that does not verify. Pass an explicit id to check from anywhere else. Requires esurvey.audit_seed to be set to the value the entries were written with.';
