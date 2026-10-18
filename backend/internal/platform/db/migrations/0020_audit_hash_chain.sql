-- Tamper-evidence for the audit log.
--
-- # What the append-only trigger does and does not do
--
-- 0001 made audit_log append-only, which stops the application rewriting its
-- own history. It stops nothing done with direct database privileges: a
-- superuser can drop the trigger, edit a row, and put the trigger back, and
-- the table afterwards looks exactly as it did before.
--
-- That gap matters here more than it would elsewhere. This log is the record
-- of who decided what on a compensation claim -- who read a farmer's file, who
-- was refused, who adjudicated. The people with database access are the same
-- people the log exists to hold accountable, which is the situation
-- append-only alone cannot address.
--
-- # The chain
--
-- Every row carries a hash over its own contents and the hash of the row
-- before it. Changing any row changes its hash, which breaks every hash after
-- it. Removing a row breaks the link across the gap. Neither can be repaired
-- without recomputing the entire tail, and doing that requires the seed --
-- which is not in the database.
--
-- This does not make tampering impossible. It makes it *detectable*, which is
-- the honest goal: an auditor can verify the chain and get an answer, rather
-- than being told the log is trustworthy because it is a log.
--
-- # Why the seed is not stored here
--
-- Without one, anybody who can write rows can also recompute a consistent
-- chain over rows they invented. The seed is ESURVEY_AUDIT_CHAIN_SEED, held
-- wherever the JWT seed is held, and verification needs it. A deployment that
-- does not set one still gets the chain, which still catches accidental
-- corruption and anyone who does not know the chain exists.

ALTER TABLE audit_log
    -- SHA-256 over the previous row's hash and this row's fields.
    ADD COLUMN entry_hash text,
    ADD COLUMN prev_hash  text;

-- Written by a trigger, not by the application.
--
-- The application already cannot rewrite the log; letting it *compute* the
-- chain would mean the code being audited was also the code deciding what the
-- audit record hashes to. The database does it, from the row as actually
-- stored, at the moment it is stored.
CREATE FUNCTION audit_log_chain() RETURNS trigger AS $$
DECLARE
    prev text;
    seed text;
BEGIN
    -- The row immediately before this one. Locked so two concurrent inserts
    -- cannot both chain onto the same predecessor and produce a fork -- the
    -- one failure mode that would make an honest log look tampered with.
    SELECT entry_hash INTO prev
    FROM audit_log
    WHERE id < NEW.id
    ORDER BY id DESC
    LIMIT 1
    FOR UPDATE;

    -- Genesis. A constant rather than NULL so the first row's hash is still
    -- over something, and an attacker who truncates the table cannot restart
    -- the chain and have it look original.
    IF prev IS NULL THEN
        prev := 'esurvey-audit-genesis';
    END IF;

    -- Empty when unset, which still chains. See the note above on what that
    -- does and does not protect against.
    seed := COALESCE(current_setting('esurvey.audit_seed', true), '');

    NEW.prev_hash := prev;
    NEW.entry_hash := encode(sha256(convert_to(
        seed || '|' || prev || '|' ||
        NEW.id::text || '|' ||
        NEW.at::text || '|' ||
        COALESCE(NEW.actor_user_id::text, '') || '|' ||
        COALESCE(NEW.actor_label, '') || '|' ||
        COALESCE(NEW.actor_role, '') || '|' ||
        COALESCE(NEW.actor_ip::text, '') || '|' ||
        NEW.action || '|' ||
        COALESCE(NEW.resource_type, '') || '|' ||
        COALESCE(NEW.resource_id, '') || '|' ||
        COALESCE(NEW.jurisdiction_code, '') || '|' ||
        NEW.outcome || '|' ||
        NEW.detail::text || '|' ||
        COALESCE(NEW.request_id, ''),
        'UTF8')), 'hex');

    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

-- BEFORE INSERT, so the values are part of the row that lands rather than an
-- update to it -- which the append-only trigger would refuse anyway.
CREATE TRIGGER audit_log_chain_link
    BEFORE INSERT ON audit_log
    FOR EACH ROW EXECUTE FUNCTION audit_log_chain();

-- Verification.
--
-- Recomputes every row's hash from the row before it and reports the first
-- place the chain does not hold. Returns no rows when the log is intact, which
-- is the answer an auditor wants to be able to obtain for themselves rather
-- than be given.
--
-- Reads the same seed the writer used, so it must run in a session where
-- esurvey.audit_seed is set to the same value. A wrong seed reports the first
-- row as broken, which is the correct outcome: without the seed you cannot
-- verify the log, and you should not be told that you can.
CREATE FUNCTION audit_log_verify(p_from bigint DEFAULT 0)
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
BEGIN
    seed := COALESCE(current_setting('esurvey.audit_seed', true), '');
    expected := NULL;

    FOR r IN
        SELECT * FROM audit_log WHERE id >= p_from ORDER BY id
    LOOP
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
    'Returns no rows when the audit chain is intact, or one row naming the first entry that does not verify. Requires esurvey.audit_seed to be set to the value the entries were written with.';
