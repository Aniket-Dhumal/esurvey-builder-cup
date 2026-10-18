-- The calamity an assessment was requested against, carried through the queue.
--
-- assessments.survey_event_id has existed since survey events were built, with
-- an index on it, and nothing has ever written it: every assessment in the
-- database has a null event. The console asks an officer to choose the
-- incident before anything else -- it is what dates the work -- and then threw
-- that choice away at the door, because the job row had nowhere to put it.
--
-- Nullable, and stays nullable. An assessment can legitimately have no event:
-- the pipeline runs one for every complete claim, and a claim already names
-- its own event, while a screening run against a parcel out of season names
-- none.

ALTER TABLE assessment_jobs
    ADD COLUMN survey_event_id uuid REFERENCES survey_events (id);

COMMENT ON COLUMN assessment_jobs.survey_event_id IS
    'The declared calamity this assessment is for, chosen by the officer who '
    'requested it. Carried to assessments.survey_event_id when the job runs.';
