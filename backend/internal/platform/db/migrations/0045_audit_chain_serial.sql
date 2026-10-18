-- Concurrent audit entries chain one after another, not side by side.
--
-- # The fault
--
-- audit_log_chain found an entry's predecessor as "the row with the highest id
-- below mine", locked FOR UPDATE. Under concurrency that is not the row
-- written before this one:
--
--   * the id is drawn from the sequence when the INSERT starts, not when it
--     commits, so two writers can hold ids 1140 and 1141 while neither has
--     committed; and
--   * FOR UPDATE waits for a locked row but does not look again for rows
--     committed while it waited.
--
-- Three assessment workers finishing in the same millisecond therefore all
-- chained onto entry 1139, and audit_log_verify() reported the honest log as
-- broken at 1141 -- which is to say, reported tampering that never happened.
-- A chain that cries wolf under ordinary load is a chain nobody will believe
-- on the day it is right.
--
-- # The fix
--
-- Each insert takes one transaction-level advisory lock before it links, and
-- only then draws its id. Ids are therefore handed out in the order entries
-- are chained, and the predecessor query -- a fresh statement, with a fresh
-- snapshot, run after the lock is held -- sees every entry committed before it.
-- The column loses its default, so no id is drawn and thrown away: the trigger
-- is the only thing that assigns one, and ids stay consecutive in chain order.
--
-- The lock is held until the writing transaction commits. Audit entries are
-- written in their own short transactions (internal/audit), so this serialises
-- the audit writes and nothing else.
--
-- # What is not repaired
--
-- A fork already in a log stays there: the log is append-only, and rewriting
-- entries to make verification pass is precisely what the chain exists to
-- expose. It is reported by audit_log_verify() as it always was.

CREATE OR REPLACE FUNCTION audit_log_chain() RETURNS trigger AS $$
DECLARE
    prev text;
    seed text;
BEGIN
    -- One writer links at a time.
    PERFORM pg_advisory_xact_lock(hashtext('esurvey.audit_log_chain'));

    -- The id is drawn now, under the lock, so ids follow chain order.
    NEW.id := nextval('audit_log_entries_id_seq');

    SELECT entry_hash INTO prev
    FROM audit_log
    WHERE id < NEW.id
    ORDER BY id DESC
    LIMIT 1;

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
        CASE WHEN NEW.jurisdiction_id IS NULL THEN ''
             ELSE '|' || NEW.jurisdiction_id::text END,
        'UTF8')), 'hex');

    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

-- The trigger assigns every id. A default would draw one at the start of the
-- INSERT only for the trigger to replace it, leaving a gap per entry.
ALTER TABLE audit_log ALTER COLUMN id DROP DEFAULT;
