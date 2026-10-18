-- How long each assessment ran, and a way to read the last hour cheaply.
--
-- Capacity for a lakh of claims a day is a question of how many assessments
-- run at once and how long each takes. The first is now configured per
-- upstream (internal/platform/gate); the second was never recorded -- the
-- claim was cleared on finishing, and created_at to finished_at includes the
-- time spent queued, which is the number that grows under load rather than
-- the one that explains it.

ALTER TABLE assessment_jobs ADD COLUMN IF NOT EXISTS run_ms integer
    CHECK (run_ms IS NULL OR run_ms >= 0);

COMMENT ON COLUMN assessment_jobs.run_ms IS
    'Milliseconds the finishing attempt ran, from its claim to done or failed.';

-- The health page's "last hour", without reading the table.
CREATE INDEX IF NOT EXISTS assessment_jobs_finished_idx
    ON assessment_jobs (finished_at)
    WHERE finished_at IS NOT NULL;
