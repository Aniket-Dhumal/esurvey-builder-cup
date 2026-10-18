-- What the handset's encoder actually agreed to record a clip at.
--
-- # Why the office needs this
--
-- A claim is budgeted at about 20 MB and the app asks for 720p at 15 fps and
-- 2.5 Mbit/s to reach it. Whether it gets that is entirely up to the phone.
--
-- Measured on a Vivo V2253 in an office test: asked for 2.5 Mbit/s, recorded at
-- 8.5. The encoder discards the bitrate outright, and four fifteen-second clips
-- came to 65 MB rather than 18. That is not a fault anybody can act on from the
-- claim alone -- the clips are fine, there are just three times as many bytes of
-- them -- and without this column an operator looking at a 65 MB claim beside an
-- 18 MB one has no way to tell which is the handset and which is a longer walk.
--
-- The app has recorded it locally since its own schema version 8. It was never
-- sent, so it never left the phone, which made it useless for exactly the person
-- it was for.
--
-- # Nullable, and stays nullable
--
-- Every capture taken before this is null, and so is every capture from a build
-- that predates the app sending it. A default would invent a profile for clips
-- nobody measured.

ALTER TABLE survey_captures
    -- The rung of the app's own fallback ladder: 'preferred', 'steady',
    -- 'smooth', 'capped', or 'device' for whatever the phone does unaided.
    ADD COLUMN IF NOT EXISTS encoding_profile text,
    ADD COLUMN IF NOT EXISTS encoding_fps integer,
    -- What was *asked* for, not what was delivered. The delivered figure is
    -- byte_size over duration_ms, and the gap between the two is the finding.
    ADD COLUMN IF NOT EXISTS encoding_bitrate_bps integer;

COMMENT ON COLUMN survey_captures.encoding_bitrate_bps IS
    'The bitrate the handset was asked for. What it delivered is byte_size over '
    'duration_ms; where the two disagree the phone ignored the request, which is '
    'the usual reason a claim is far larger than its budget.';
