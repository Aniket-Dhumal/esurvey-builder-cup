-- When the farmer filed, as distinct from when the claim reached the office.
--
-- The 48-hour intimation window was measured against `now()` at insert, which
-- is the moment the report arrived on this server. For a claim filed at a
-- counter those are the same instant. For the handset they are not, and the
-- gap between them is the whole point of the app: the farmer fills the form
-- in a field with no signal, and the queue delivers it when a network appears
-- -- hours or days later, and later still now that a claim is deliberately
-- held until its evidence is complete (docs/EVIDENCE_REVALIDATION.md, V4).
--
-- So a farmer who filed within the window and reached a signal on the third
-- day was recorded as having filed late. The flag is only ever read by an
-- officer weighing the claim, which makes it a wrong fact about a farmer who
-- did nothing wrong.
--
-- Both instants are now kept. filed_at is what the handset says, reported_at
-- remains when the office received it, and the window is measured on
-- filed_at. Null for every claim filed before this and for any channel that
-- does not report one -- a counter filing, where reported_at is the truth.
--
-- The handset's clock is not trusted, it is recorded. A clock set back would
-- make a late claim look timely, so the server refuses a filed_at that is in
-- the future or earlier than the loss itself, and keeps both instants where
-- an officer can see the gap.

ALTER TABLE crop_loss_reports
    ADD COLUMN IF NOT EXISTS filed_at timestamptz;

COMMENT ON COLUMN crop_loss_reports.filed_at IS
    'When the farmer completed the form on the handset. Null for a counter '
    'filing. The intimation window is measured on this where present, and on '
    'reported_at otherwise.';
