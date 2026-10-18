-- Assessments as queued work, not as a request somebody waits on.
--
-- # What this replaces
--
-- POST /api/v1/assessments ran the whole pipeline inline: Earth Engine for
-- NDVI and terrain, radar, the weather archive, ground vision, then the panel
-- of models -- minutes of work, on an officer's open connection, behind a
-- five-minute context.
--
-- Three things follow from that shape, and all three bite at scale
-- (docs/PLATFORM_SCALE_REVIEW.md, A1):
--
--   * The satellite service admits four Earth Engine jobs at once. A lakh of
--     assessments is roughly seventeen days of engine time, and nothing
--     recorded where it had got to.
--   * A deploy, a restart or a dropped connection lost the work. There was
--     nothing to resume: the request was the only record that it had been
--     asked for.
--   * Back-pressure reached the officer as a failure. Past the queue limit the
--     engine answers 503, and the console showed a refusal for a claim that
--     needed to wait its turn.
--
-- So the request records the intent and returns; workers drain the queue at
-- the rate the engine and the models actually allow; the console polls. The
-- same shape as the notification outbox, for the same reason.

CREATE TYPE assessment_job_state AS ENUM (
    'queued',   -- waiting for a worker
    'running',  -- claimed; claimed_at is the lease
    'done',     -- assessment_id is set
    'failed'    -- gave up; error says why
);

CREATE TABLE IF NOT EXISTS assessment_jobs (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),

    -- What to assess. Validated at enqueue -- the registry record, the
    -- parcel and the officer's scope -- so a worker never discovers that the
    -- request was nonsense.
    farmer_id     text NOT NULL,
    farm_id       text NOT NULL,
    incident_date date NOT NULL,
    sowing_date   date,
    crop_loss_report_id uuid REFERENCES crop_loss_reports (id) ON DELETE SET NULL,

    -- Whose request it is. The worker rebuilds this officer's principal to
    -- read ground imagery under their scope, and the assessment records them
    -- as its author -- the queue must not become a way to assess a parcel
    -- nobody is accountable for.
    requested_by uuid NOT NULL REFERENCES users (id),
    -- Where the parcel is, for the officer's own listing of their requests.
    village_lgd_code text NOT NULL DEFAULT '',

    state    assessment_job_state NOT NULL DEFAULT 'queued',
    attempts integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    -- When a queued job may next be tried. Backoff lives here rather than in
    -- a sleep, so a restart keeps the schedule.
    next_attempt_at timestamptz NOT NULL DEFAULT now(),

    -- The lease. A worker that dies leaves 'running' behind; a job whose
    -- claim is older than the lease is taken back.
    claimed_by text,
    claimed_at timestamptz,

    assessment_id uuid REFERENCES assessments (id) ON DELETE SET NULL,
    -- What went wrong, in words an officer can act on. Never a raw provider
    -- message: see internal/saferr.
    error text,

    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    finished_at timestamptz,

    CONSTRAINT done_has_assessment CHECK (
        state <> 'done' OR assessment_id IS NOT NULL),
    CONSTRAINT failed_has_reason CHECK (
        state <> 'failed' OR error IS NOT NULL)
);

-- One live job per question. Two officers asking for the same parcel and the
-- same incident within a minute of each other is one piece of Earth Engine
-- work, not two -- and a console that retries a request whose reply was lost
-- must not queue a second.
CREATE UNIQUE INDEX IF NOT EXISTS assessment_jobs_live_idx
    ON assessment_jobs (farmer_id, farm_id, incident_date,
                        COALESCE(crop_loss_report_id::text, ''))
    WHERE state IN ('queued', 'running');

-- The claim: what is due, oldest first.
CREATE INDEX IF NOT EXISTS assessment_jobs_due_idx
    ON assessment_jobs (next_attempt_at)
    WHERE state = 'queued';

-- Expired leases, for the sweeper that takes crashed work back.
CREATE INDEX IF NOT EXISTS assessment_jobs_running_idx
    ON assessment_jobs (claimed_at)
    WHERE state = 'running';

-- An officer's own recent requests, for the console.
CREATE INDEX IF NOT EXISTS assessment_jobs_requester_idx
    ON assessment_jobs (requested_by, created_at DESC);

CREATE TRIGGER assessment_jobs_touch BEFORE UPDATE ON assessment_jobs
    FOR EACH ROW EXECUTE FUNCTION touch_updated_at();

COMMENT ON TABLE assessment_jobs IS
    'Requested assessments, worked by a pool sized to the Earth Engine and '
    'model quotas. The request records intent and returns; the console polls.';
