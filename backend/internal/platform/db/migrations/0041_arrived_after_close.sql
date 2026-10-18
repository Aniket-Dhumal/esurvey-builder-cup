-- A claim completed before the event closed and delivered after it.
--
-- The deadline is the farmer's to meet, and they meet it by filling in the
-- form -- not by finding a network. Until now the close was measured on
-- arrival, so a claim still in a phone's queue when the district closed the
-- event was refused outright: destroyed by this system's own delay, after the
-- app had told the farmer it was safe and would send itself. That delay is
-- now longer by design, because a claim waits for its evidence to be complete
-- (docs/EVIDENCE_REVALIDATION.md, V4).
--
-- Such a claim is now taken, on the filing instant the handset reported
-- (migration 0040). This column records that it happened, for two reasons.
--
-- The district closed its books on a count that did not include this claim,
-- and a figure that moves after it was signed off needs to say why.
--
-- And this is the one place where a handset's own clock decides whether a
-- claim exists at all rather than merely how it is flagged. The instant is
-- still checked -- not in the future, not before the loss, and before both
-- the advertised deadline and the moment the district actually stopped -- but
-- an officer looking at a late arrival should be able to see that it rests on
-- what a phone said about the time.

ALTER TABLE crop_loss_reports
    ADD COLUMN IF NOT EXISTS arrived_after_close boolean NOT NULL DEFAULT false;

COMMENT ON COLUMN crop_loss_reports.arrived_after_close IS
    'The claim was completed before the event stopped taking claims and '
    'reached the office afterwards. Admitted on the handset filed_at.';

-- Finding them is a question a district asks of one event at a time: "what
-- landed after I closed this?". Partial, because the answer is almost always
-- none of them.
CREATE INDEX IF NOT EXISTS crop_loss_reports_after_close_idx
    ON crop_loss_reports (survey_event_id)
    WHERE arrived_after_close;
