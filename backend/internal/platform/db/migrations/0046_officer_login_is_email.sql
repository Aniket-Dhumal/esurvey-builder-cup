-- An officer's login is their email address, in the database as well.
--
-- internal/identity refuses any other login at the store, and the API refuses
-- it at sign-in and at creation. This makes the rule a property of the table,
-- so that a row written by some future path, a script or a restore cannot
-- bring back a login like "admin" or "dm.solapur".
--
-- The pattern is the part of identity.NormalizeEmail a CHECK can express:
-- lower case, one @, a dotted domain ending in an alphabetic suffix, and the
-- username identical to the email. The reserved-name rules (.local,
-- example.com) stay in the application, where the message can say why.
--
-- Farmers are untouched: they sign in with a mobile number and a one-time
-- code, and have no login name at all.

ALTER TABLE users ADD CONSTRAINT users_officer_login_is_email CHECK (
    kind <> 'officer'
    OR (
        email IS NOT NULL
        AND username = email
        AND email = lower(email)
        AND length(email) <= 254
        AND email ~ '^[a-z0-9!#$%&''*+/=?^_{|}~-]+(\.[a-z0-9!#$%&''*+/=?^_{|}~-]+)*@[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)*\.[a-z]{2,63}$'
    )
);
