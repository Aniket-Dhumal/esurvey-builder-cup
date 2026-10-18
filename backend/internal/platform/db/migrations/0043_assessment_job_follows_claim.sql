-- An assessment job goes with the claim it was queued for.
--
-- assessment_jobs.crop_loss_report_id was ON DELETE SET NULL, and the index
-- that stops two live jobs for the same parcel and incident keys on
-- COALESCE(crop_loss_report_id::text, ''). So deleting a claim turned its live
-- job into a job for no claim -- and where a second live job already existed
-- for the same parcel and date, that null collided with it, the unique index
-- refused the update, and the claim could not be deleted at all.
--
-- Found when two claims for one parcel were removed together. SET NULL was the
-- wrong meaning besides: a job for a claim that no longer exists is not a
-- free-standing request somebody still wants, it is work for nothing, and the
-- worker would have produced an assessment attached to no claim. An officer's
-- own request, which never named a claim, is unaffected.
--
-- Named explicitly rather than by the generated name, so this is not tied to
-- whatever Postgres happened to call it.

ALTER TABLE assessment_jobs
    DROP CONSTRAINT IF EXISTS assessment_jobs_crop_loss_report_id_fkey;

ALTER TABLE assessment_jobs
    ADD CONSTRAINT assessment_jobs_crop_loss_report_id_fkey
    FOREIGN KEY (crop_loss_report_id) REFERENCES crop_loss_reports (id)
    ON DELETE CASCADE;
