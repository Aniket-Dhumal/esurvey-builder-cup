-- What live guidance cost, and the caps that stop it costing without limit.
--
-- # Why this is recorded at all
--
-- Because the model is billed by the minute and a session is held open by a farmer
-- rather than by a request. A handset in a pocket with the screen on is a session;
-- so is a farmer who set the phone down to look at their crop. The relay already
-- ends a session after eight minutes and after ninety seconds of silence, which
-- bounds one session -- and bounds nothing about how many sessions a day.
--
-- A feature nobody measures is a feature nobody can cap, and the first anybody would
-- know about a runaway month is the invoice.
--
-- # What the caps are for
--
-- Not to save money in the ordinary case. A farmer walking four points and asking
-- two questions costs a few minutes, and that is what the feature is for. The caps
-- exist for the cases nobody intends: a handset stuck in a reconnect loop, a device
-- left in a vehicle, a claim reopened forty times.
--
-- So they are generous per claim and firm per day. A farmer who reaches the daily
-- ceiling loses the assistant and keeps everything else: the scripted guidance, the
-- walk, the spacing rule and the filing all work with no network, which is what makes
-- it safe to cut the assistant off rather than the farmer.
--
-- # Why the farmer is identified and the session is not
--
-- The row carries the farmer whose claim was walked, because that is what a cap has
-- to be counted against. It does not carry what was said, what was heard, or any
-- frame: the conversation is not evidence and is not kept. What is kept is how long
-- it ran and how much went through it.

CREATE TABLE IF NOT EXISTS live_session_spend (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),

    -- Who was walking, and against which claim where there is one. A session may
    -- open before a claim is filed, which is the ordinary case: the walk happens
    -- first.
    farmer_id text NOT NULL,
    report_id uuid REFERENCES crop_loss_reports(id) ON DELETE SET NULL,

    -- Which account held the handset. A household shares a phone, so the farmer and
    -- the account are not always the same person, and a cap per farmer would let one
    -- handset run several.
    account_user_id uuid REFERENCES users(id) ON DELETE SET NULL,

    opened_at  timestamptz NOT NULL DEFAULT now(),
    closed_at  timestamptz,

    -- Rounded up to the second. Billing is per minute and the arithmetic should err
    -- against us rather than against the ceiling.
    seconds integer NOT NULL DEFAULT 0,

    -- What went through it, for telling a long conversation from a stuck
    -- reconnecting client: the second has many short sessions and little audio.
    audio_bytes_in  bigint NOT NULL DEFAULT 0,
    audio_bytes_out bigint NOT NULL DEFAULT 0,
    frames_in       integer NOT NULL DEFAULT 0,

    -- Why it ended, in the relay's own words: closed, idle, session limit, failed.
    -- A day of sessions that all ended 'failed' is a fault, not usage.
    ended_because text NOT NULL DEFAULT '',

    model text NOT NULL DEFAULT ''
);

-- The two questions a cap asks: how much has this farmer used today, and how much
-- has this handset. Both are "sum the seconds since midnight", so both want the
-- same shape of index.
CREATE INDEX IF NOT EXISTS live_session_spend_farmer_day_idx
    ON live_session_spend (farmer_id, opened_at DESC);
CREATE INDEX IF NOT EXISTS live_session_spend_account_day_idx
    ON live_session_spend (account_user_id, opened_at DESC);

COMMENT ON TABLE live_session_spend IS
    'One row per live guidance session: how long it ran and how much went through '
    'it. Never what was said -- the conversation is not evidence and is not kept. '
    'Exists so the feature can be capped, because the model is billed by the minute '
    'and a session is held open by a farmer rather than by a request.';
