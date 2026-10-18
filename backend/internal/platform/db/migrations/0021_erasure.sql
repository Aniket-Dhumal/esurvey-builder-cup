-- Let an erased farmer account exist without a mobile number.
--
-- # The constraint, and why it was right
--
-- farmer_has_handset required every farmer account to carry a mobile number.
-- That is correct for an account somebody uses: the number is the only way a
-- farmer signs in, and the only way the platform reaches them about a claim.
-- An account without one is unreachable and unable to authenticate, which is
-- not an account.
--
-- # Why erasure needs an exception
--
-- A data-protection erasure request has to remove the mobile number: it is the
-- single most identifying and most contactable thing held about a farmer. But
-- the account row cannot be deleted -- audit_log references it ON DELETE SET
-- NULL, and audit_log is append-only, so the delete is refused. Nor should it
-- be deletable: "which account did this" is what keeps a decision about public
-- money answerable years later.
--
-- So the row stays, emptied. As written, the constraint refused exactly the
-- operation the DPDP Act requires, and nothing could satisfy both.
--
-- # Why the exception is scoped to deactivated accounts
--
-- Not "farmers may have no mobile". A deactivated account cannot sign in and
-- is never messaged, so neither reason for the rule applies to it -- while an
-- active farmer account still cannot exist without a number, which is what the
-- constraint was there to protect.
--
-- A dedicated 'erased' status was considered and rejected: it would put the
-- fact of an erasure into a column any query can group by, turning "who asked
-- to be forgotten" into a reportable list. Deactivated is deactivated, for
-- whatever reason.

ALTER TABLE users DROP CONSTRAINT farmer_has_handset;

-- In words: a farmer never has a password, and has a mobile number unless the
-- account has been deactivated.
--
-- One CHECK rather than two, because two would name the wrong one on failure
-- and send whoever hits it looking in the wrong place.
ALTER TABLE users ADD CONSTRAINT farmer_has_handset CHECK (
    kind <> 'farmer'
    OR (
        password_hash IS NULL
        AND (mobile_e164 IS NOT NULL OR status = 'deactivated')
    )
);
