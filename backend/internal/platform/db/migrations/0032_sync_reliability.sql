-- Making the handset's queue safe to retry, at the scale of a district event.
--
-- # The failure this exists to stop
--
-- A farmer in a village files a report. The request reaches the server, the
-- report is written, and the reply is lost on the way back -- a tower handover,
-- a 2G connection dropping, a 45-second timeout that fires a moment before the
-- answer arrives. To the handset that is indistinguishable from a request that
-- never arrived, so it keeps the report queued and sends it again.
--
-- Every write this platform accepted from the app did it again. A lost reply
-- became a second claim for the same parcel. A lost reply on a photograph
-- became a second copy of it -- and the second copy then failed the
-- distinct-image gate, which exists to catch one photograph submitted for two
-- plots, so the farmer's own retry was recorded against their claim as a
-- reused photograph.
--
-- On a handful of test phones this is rare. Across lakhs of surveys filed from
-- the edge of coverage during one event, lost replies are routine, and so were
-- the duplicates.
--
-- # Three pieces, and why each lives where it does
--
-- 1. idempotency_keys -- a generic, durable record of what each client-chosen
--    key already did, so any write can be retried and the original answer
--    replayed. Postgres rather than Redis, because the retry can arrive days
--    later: a phone that lost its reply in the field may not see signal again
--    until the farmer is home, and a cache that evicts the key in the meantime
--    re-executes the write.
--
-- 2. crop_loss_reports.client_ref -- the report's own idempotency, enforced by
--    a unique index inside the same transaction as the insert. The generic
--    table cannot close one window on its own: a process that dies after the
--    report commits and before the key is marked complete would, on retry,
--    execute again. The unique index makes that retry find the report instead.
--
-- 3. device_sync_status -- one row per handset saying how its queue is doing,
--    so an operator can find the phones whose claims are stuck before the
--    farmers do.

-- --------------------------------------------------------------------------
-- 1. Idempotency keys
-- --------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS idempotency_keys (
    -- Scoped to the caller. A key is only meaningful to the account that chose
    -- it, and scoping it means one account can never replay another's answer
    -- by guessing a key.
    principal_id  uuid NOT NULL,
    key           text NOT NULL CHECK (length(key) BETWEEN 8 AND 128),

    -- What the key was first used for. A key reused for a different operation
    -- is a client bug, refused rather than replayed.
    method        text NOT NULL,
    route         text NOT NULL,

    -- For diagnosis only; see the note on matching in idempotency.go.
    request_hash  text,

    -- Null while the first request is still running.
    status        integer,
    response_body bytea,
    content_type  text,

    created_at    timestamptz NOT NULL DEFAULT now(),
    completed_at  timestamptz,

    PRIMARY KEY (principal_id, key)
);

-- The sweeper deletes by age.
CREATE INDEX IF NOT EXISTS idempotency_keys_created_idx
    ON idempotency_keys (created_at);

COMMENT ON TABLE idempotency_keys IS
    'What each client-chosen Idempotency-Key already did, so a write retried '
    'after a lost reply replays the first answer instead of executing again. '
    'Kept 30 days: a handset can be out of signal for a week and still retry.';

-- --------------------------------------------------------------------------
-- 2. The report's own client reference
-- --------------------------------------------------------------------------

ALTER TABLE crop_loss_reports
    ADD COLUMN IF NOT EXISTS client_ref text
        CHECK (client_ref IS NULL OR length(client_ref) BETWEEN 8 AND 128);

-- Per farmer, not global. A reference is chosen by one handset and has no
-- meaning outside the claims of the farmer it was filed for. Partial, so every
-- report filed before this -- and every one filed at a counter by an officer,
-- which has no queue behind it -- stays unconstrained.
CREATE UNIQUE INDEX IF NOT EXISTS crop_loss_reports_client_ref_idx
    ON crop_loss_reports (farmer_id, client_ref)
    WHERE client_ref IS NOT NULL;

-- --------------------------------------------------------------------------
-- 3. Photographs: a retried upload is the same photograph
-- --------------------------------------------------------------------------
--
-- No unique index. Existing data may already carry duplicates from exactly the
-- bug described above, and a migration that failed on them would take the
-- deployment down with it. The check is made under an advisory lock in
-- survey.Submit instead, and this index makes that lookup cheap.

CREATE INDEX IF NOT EXISTS survey_captures_report_hash_idx
    ON survey_captures (report_id, sha256);

-- How many times an upload has been restarted after failing its checksum.
--
-- A chunk corrupted in transit is fixed by sending again from the start. A
-- checksum that is simply wrong -- a client bug, a file changed on the phone
-- after it was hashed -- is not, and without a count the restart would loop
-- for ever over the connection least able to afford it. Two restarts, then a
-- refusal the handset shows the farmer.
ALTER TABLE capture_uploads
    ADD COLUMN IF NOT EXISTS restarts integer NOT NULL DEFAULT 0
        CHECK (restarts >= 0);

-- --------------------------------------------------------------------------
-- 4. How each handset's queue is doing
-- --------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS device_sync_status (
    -- Chosen by the app at install and kept for the life of the install.
    device_id       text NOT NULL CHECK (length(device_id) BETWEEN 8 AND 128),
    principal_id    uuid NOT NULL,

    app_version     text NOT NULL,
    platform        text NOT NULL,

    -- What is still waiting on the phone, by kind.
    pending_reports     integer NOT NULL DEFAULT 0 CHECK (pending_reports >= 0),
    pending_captures    integer NOT NULL DEFAULT 0 CHECK (pending_captures >= 0),
    pending_boundaries  integer NOT NULL DEFAULT 0 CHECK (pending_boundaries >= 0),
    pending_appeals     integer NOT NULL DEFAULT 0 CHECK (pending_appeals >= 0),
    -- Refused for good: the farmer has to be told, and somebody may have to
    -- help them.
    failed_items        integer NOT NULL DEFAULT 0 CHECK (failed_items >= 0),
    pending_bytes       bigint  NOT NULL DEFAULT 0 CHECK (pending_bytes >= 0),

    -- The age of the oldest thing waiting. The single most useful number for
    -- finding a stuck phone: a queue of one item that has waited nine days is
    -- a farmer who thinks their claim was filed.
    oldest_pending_at   timestamptz,
    last_success_at     timestamptz,
    last_error_code     text,

    reported_at     timestamptz NOT NULL DEFAULT now(),

    PRIMARY KEY (device_id)
);

CREATE INDEX IF NOT EXISTS device_sync_status_oldest_idx
    ON device_sync_status (oldest_pending_at)
    WHERE oldest_pending_at IS NOT NULL;
CREATE INDEX IF NOT EXISTS device_sync_status_principal_idx
    ON device_sync_status (principal_id);
