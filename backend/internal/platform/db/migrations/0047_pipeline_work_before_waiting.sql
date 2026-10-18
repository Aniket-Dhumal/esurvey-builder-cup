-- The claim pipeline takes work before it looks at waiting claims.
--
-- One queue ordered by pipeline_next_at put a new claim's receipt behind every
-- claim waiting on an assessment, and each of those was due again every
-- minute. Measured with 20,000 claims waiting: one loop did 262 steps a second
-- against the 333 the polling alone needed, and 200 new claims were still not
-- acknowledged after 45 seconds. At a lakh of claims a day, with assessments
-- bounded by satellite and model quotas, that backlog is an ordinary
-- afternoon after a hailstorm.
--
-- So the pipeline asks two questions, in order: is there a claim with a step
-- to take (acknowledge, queue an assessment, allocate), and only if not, is
-- there a waiting claim to look at. Each gets its own partial index, so
-- neither question reads past the other's rows. The waiting claims are also
-- woken by their assessment ending (assessjob.wakeClaim), so the look is a
-- safety net and comes round every fifteen minutes or so rather than every
-- minute.

CREATE INDEX IF NOT EXISTS crop_loss_reports_pipeline_work_idx
    ON crop_loss_reports (pipeline_next_at, reported_at)
    WHERE state IN ('submitted', 'acknowledged')
      AND assessment_state <> 'queued';

CREATE INDEX IF NOT EXISTS crop_loss_reports_pipeline_waiting_idx
    ON crop_loss_reports (pipeline_next_at, reported_at)
    WHERE state IN ('submitted', 'acknowledged')
      AND assessment_state = 'queued';

-- Replaced by the two above: every row it held is in exactly one of them.
DROP INDEX IF EXISTS crop_loss_reports_pipeline_idx;
