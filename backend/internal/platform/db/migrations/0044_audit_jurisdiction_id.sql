-- Where an audit entry happened, by identity rather than by code.
--
-- # The gap
--
-- audit_log recorded a place as jurisdiction_code alone, and an officer's view
-- of the log was scoped by matching that code against the jurisdictions under
-- their postings. LGD codes are unique only within a level: a district, a
-- taluka and a village can share a number. An entry from a village in another
-- state could therefore appear to an officer whose own district carried the
-- same code -- and an entry from their own area could be attributed to the
-- wrong place in the other direction.
--
-- # What changes
--
-- New entries carry jurisdiction_id, the exact node, resolved when the entry
-- is written (see internal/audit). Scoping reads the id.
--
-- # What does not
--
-- Entries already written are not touched. The log is append-only by trigger
-- and that is not relaxed here, even for a column the hash does not cover: a
-- migration that rewrites audit rows is the thing the trigger exists to make
-- impossible. Older entries keep their code, and the reader treats a code
-- that names more than one jurisdiction as unattributable -- hidden from an
-- officer's scoped view rather than guessed at. The administrator's view is
-- unchanged.
--
-- # The chain
--
-- jurisdiction_id is hashed when present and contributes nothing when absent,
-- so every existing entry recomputes to exactly the hash it recorded, and a new
-- entry's place cannot be altered without breaking the chain.

ALTER TABLE audit_log ADD COLUMN jurisdiction_id uuid;

CREATE INDEX audit_log_jurisdiction_idx
    ON audit_log (jurisdiction_id, at DESC)
    WHERE jurisdiction_id IS NOT NULL;

CREATE OR REPLACE FUNCTION audit_log_chain() RETURNS trigger AS $$
DECLARE
    prev text;
    seed text;
BEGIN
    SELECT entry_hash INTO prev
    FROM audit_log
    WHERE id < NEW.id
    ORDER BY id DESC
    LIMIT 1
    FOR UPDATE;

    IF prev IS NULL THEN
        prev := 'esurvey-audit-genesis';
    END IF;

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
        COALESCE(NEW.request_id, '') ||
        -- Appended only when set, so entries without it hash as they always
        -- did.
        CASE WHEN NEW.jurisdiction_id IS NULL THEN ''
             ELSE '|' || NEW.jurisdiction_id::text END,
        'UTF8')), 'hex');

    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

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

    start_id := COALESCE(
        p_from,
        (SELECT from_id FROM audit_chain_start),
        0);

    FOR r IN
        SELECT * FROM audit_log WHERE id >= start_id ORDER BY id
    LOOP
        IF r.entry_hash IS NULL THEN
            broken_at  := r.id;
            reason     := 'the entry carries no hash but sits inside the chain';
            recorded   := NULL;
            recomputed := NULL;
            RETURN NEXT;
            RETURN;
        END IF;

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
            COALESCE(r.request_id, '') ||
            CASE WHEN r.jurisdiction_id IS NULL THEN ''
                 ELSE '|' || r.jurisdiction_id::text END,
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

COMMENT ON COLUMN audit_log.jurisdiction_id IS
    'The exact jurisdiction the entry happened in. Set on entries written after 0044; older entries carry jurisdiction_code only, which is ambiguous across levels.';
