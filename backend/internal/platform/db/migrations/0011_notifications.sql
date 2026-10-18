-- Telling the farmer.
--
-- The platform decides things that matter to a farmer -- their event opened,
-- their report was acknowledged, an officer is coming, their appeal was
-- answered -- and told them none of it. A farmer had to already know to open
-- the app, on a handset, in a village, having heard about the scheme some
-- other way. That is not a notification gap, it is most of the adoption
-- problem.
--
-- # Why an outbox rather than sending inline
--
-- The thing that triggers a message is a database transaction: a report moves
-- state, an event is declared. Sending inside that transaction means an SMS
-- gateway timeout rolls back an acknowledgement the farmer is entitled to,
-- and sending after it means a crash between the two loses the message
-- silently. Writing the intent in the same transaction and delivering it
-- separately is the only arrangement where neither happens.
--
-- # Why it records what it could not send
--
-- No gateway is configured yet. The honest behaviour is to queue and report
-- the backlog, not to drop messages and look healthy -- when a provider is
-- connected, everything queued is still there, and until then the count is
-- the true measure of how much this platform is failing to tell people.

CREATE TYPE notification_channel AS ENUM ('sms', 'whatsapp', 'push', 'ivr');

CREATE TYPE notification_state AS ENUM (
    'queued',
    'sending',
    'sent',
    'failed',      -- retryable; attempts and next_attempt_at govern
    'abandoned',   -- retried enough; a person needs to look
    'suppressed'   -- deliberately not sent, with a reason
);

CREATE TABLE notifications (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),

    -- What happened. A code rather than a rendered string, so the message can
    -- be re-worded, translated, or re-rendered for a different channel
    -- without rewriting history.
    event_code text NOT NULL,

    -- Who it is about and who it goes to. The recipient is denormalised
    -- because a farmer changing their number should not retroactively change
    -- where a message was sent last year.
    farmer_id  text,
    recipient  text NOT NULL,
    channel    notification_channel NOT NULL DEFAULT 'sms',

    -- Marathi, and the language it was rendered in. A crop loss notice in
    -- English to a farmer in Barshi is a message that was technically
    -- delivered.
    language text NOT NULL DEFAULT 'mr',
    body     text NOT NULL CHECK (length(btrim(body)) > 0),

    -- What it refers to, for the audit trail and for a farmer ringing up
    -- about "the message I got".
    resource_type text,
    resource_id   text,

    state    notification_state NOT NULL DEFAULT 'queued',
    attempts integer NOT NULL DEFAULT 0,

    -- When to try next. Backoff lives in a column rather than in a sleeping
    -- goroutine, so a restart does not lose the schedule.
    next_attempt_at timestamptz NOT NULL DEFAULT now(),

    last_error   text,
    provider     text,
    provider_ref text,

    -- Deliberately not sent, and why. Quiet hours, a farmer who opted out, a
    -- number known bad. A suppressed message with no reason is a message
    -- nobody can explain the absence of.
    suppressed_reason text,

    created_at timestamptz NOT NULL DEFAULT now(),
    sent_at    timestamptz,
    updated_at timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT sent_has_time CHECK (state <> 'sent' OR sent_at IS NOT NULL),
    CONSTRAINT suppressed_has_reason CHECK (
        state <> 'suppressed' OR length(btrim(coalesce(suppressed_reason, ''))) > 0
    )
);

-- The dispatcher's query: what is due, oldest first.
CREATE INDEX notifications_due_idx ON notifications (next_attempt_at)
    WHERE state IN ('queued', 'failed');

CREATE INDEX notifications_farmer_idx ON notifications (farmer_id, created_at DESC);
CREATE INDEX notifications_resource_idx ON notifications (resource_type, resource_id);

-- The backlog, for readiness. A platform that has queued four thousand
-- messages and delivered none should say so on its own status page.
CREATE INDEX notifications_state_idx ON notifications (state);

CREATE TRIGGER notifications_touch BEFORE UPDATE ON notifications
    FOR EACH ROW EXECUTE FUNCTION touch_updated_at();

-- Whether a farmer wants to hear from us, per channel.
--
-- Consent is not implied by having filed a report. Under the DPDP Act a
-- notice about their own claim is arguably necessary rather than optional,
-- but the ability to stop is not, and a system that cannot record "stop"
-- cannot honour it.
CREATE TABLE notification_preferences (
    farmer_id text PRIMARY KEY,
    sms       boolean NOT NULL DEFAULT true,
    whatsapp  boolean NOT NULL DEFAULT false,
    push      boolean NOT NULL DEFAULT true,

    -- Set when a farmer replies STOP, or asks at a counter.
    opted_out_at timestamptz,
    opted_out_reason text,

    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TRIGGER notification_preferences_touch
    BEFORE UPDATE ON notification_preferences
    FOR EACH ROW EXECUTE FUNCTION touch_updated_at();
