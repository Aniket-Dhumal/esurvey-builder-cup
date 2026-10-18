-- Letting an officer who has forgotten their password back in.
--
-- # What was missing
--
-- Everything. mail.PurposePasswordReset was declared and referenced nowhere;
-- there was no route, no token and no screen. An officer who forgot their
-- password had one remedy, and it was a database edit.
--
-- The invitation flow does not help and should not be made to. Re-inviting a
-- claimed account is refused on purpose -- an invitation is a first
-- credential, a reset is a replacement, and conflating them would let anyone
-- holding user:update silently issue a new way into a colleague's account.
--
-- # Why this reuses account_invites rather than adding a table
--
-- The two are the same object: a single-use, expiring, hashed token that ends
-- in the owner setting a password nobody else has seen. The differences are
-- who may ask for one and what it is called. A second table would be the same
-- columns, the same indexes and a second place to get the hashing wrong.

ALTER TABLE account_invites
    ADD COLUMN IF NOT EXISTS purpose text NOT NULL DEFAULT 'invite'
        CHECK (purpose IN ('invite', 'reset'));

-- The live-token index has to be per purpose.
--
-- One index over user_id alone would mean a password reset silently revoking
-- an outstanding invitation, or refusing to issue because one exists. They
-- are different objects with different lifetimes and an account can
-- legitimately have neither, either, or -- briefly -- both.
DROP INDEX IF EXISTS account_invites_live_idx;
CREATE UNIQUE INDEX account_invites_live_idx
    ON account_invites (user_id, purpose)
 WHERE accepted_at IS NULL AND revoked_at IS NULL;

-- A reset is requested by whoever is at the keyboard, so unlike an invitation
-- it is not attributable to an officer who created it. created_by is already
-- nullable; this records where the request came from instead, which is what
-- an inquiry into a hijacked account would ask for.
ALTER TABLE account_invites
    ADD COLUMN IF NOT EXISTS requested_ip text;
