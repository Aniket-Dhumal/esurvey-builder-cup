-- A claim filed in the app is declared, not signed.
--
-- # What was wrong
--
-- The handset recorded farmer_signature_kind = 'thumb' on every claim, and the
-- server then stamped farmer_signed_at with the moment it arrived. What the
-- farmer actually did was tick a box that says "I declare that what I have
-- entered is true". No thumb was pressed against anything, and no image of one
-- exists anywhere in this system.
--
-- So every claim filed from the app carried a statement about how consent was
-- given that was not true. At an appeal, "signed with a thumb impression at
-- 14:32" invites a request to produce it, and there is nothing to produce.
--
-- # Why the column is not simply dropped
--
-- An officer filing on a farmer's behalf in the console is standing next to
-- them, and picks 'signature' or 'thumb' from a dropdown. That is a real
-- observation of a real act, and those rows are evidence. The column stays
-- and keeps its meaning; what changes is that the app no longer has to invent
-- a value to satisfy a constraint.
--
-- The accepted declaration is what a digital filing rests on, and it is
-- already recorded in declaration_accepted with the farmer's authenticated
-- session behind it. That is a stronger record than a checkbox relabelled as a
-- thumbprint, because it is what happened.
--
-- # The one existing row
--
-- The demo claim filed from the app carries 'thumb'. It is corrected to NULL:
-- leaving it would keep a false statement in the record, and the whole point
-- of this migration is that the statement was never true.

ALTER TABLE crop_loss_reports DROP CONSTRAINT submitted_is_declared;

-- A filed claim must be declared. How it was signed is recorded where somebody
-- witnessed a signing and left blank where nobody did.
ALTER TABLE crop_loss_reports ADD CONSTRAINT submitted_is_declared CHECK (
    state IN ('draft', 'withdrawn')
    OR declaration_accepted
);

-- Claims filed from the app, where no signing was witnessed. Identified by
-- having no verifications yet and a signature kind the handset hardcoded; an
-- officer-filed row is left exactly as the officer recorded it.
UPDATE crop_loss_reports r
   SET farmer_signature_kind = NULL,
       farmer_signed_at = NULL
 WHERE r.farmer_signature_kind = 'thumb'
   AND r.reported_channel = 'farmer_app';
