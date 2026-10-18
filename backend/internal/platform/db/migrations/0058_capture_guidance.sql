-- What the assistant asked the farmer to show, against the clip that answered it.
--
-- # Why this is on the capture and not in a conversation log
--
-- Because it is evidence about evidence. A drought claim whose canopy is green and
-- whose pods are empty is the case the guidance exists for: the farmer films the
-- green, the loss is in the pods, and nobody untrained films what is not there. When
-- the assistant says "शेंगा फोडून आतले दाणे दाखवा" and the next clip shows split pods,
-- that clip is a directed observation rather than a farmer's own selection -- and
-- that distinction is exactly what answers a selective-evidence challenge on appeal.
--
-- A transcript in a log file cannot do that. It is not attached to the clip, it is
-- not carried into the assessment, and nothing joins the two a year later when
-- somebody asks why this claim has a close-up of pod interiors and the one next to it
-- does not.
--
-- # What it is not
--
-- It is not a requirement, and nothing reads it as one. What makes a claim complete
-- is settled by the walk: four points, the spacing rule, computed offline and
-- reproducible without a model or a network. The assistant shapes what the *next*
-- clip shows; it cannot add a clip, and a claim walked with the assistant switched
-- off is complete on exactly the same terms as one walked with it on.
--
-- So this column being null means only that nothing was asked. It never means a
-- clip is missing something.
--
-- # Nullable, and stays nullable
--
-- Every clip recorded before this, every clip from a build without live guidance,
-- and every clip on a farm whose connection could not carry a session. That is most
-- clips, and a default would invent a direction for evidence nobody directed.

ALTER TABLE survey_captures
    -- What was asked for, in the words the farmer heard. Marathi, ordinarily.
    ADD COLUMN IF NOT EXISTS guidance_asked_for text,
    -- Which model said it, so an assessment can be read knowing what produced the
    -- direction. Not the session id: a session is a conversation and this is one
    -- sentence out of it.
    ADD COLUMN IF NOT EXISTS guidance_model text;

COMMENT ON COLUMN survey_captures.guidance_asked_for IS
    'What the live assistant asked the farmer to show in this clip, in the words '
    'they heard. Null where nothing was asked, which is most clips. Makes the clip '
    'a directed observation rather than the farmer''s own selection, which is what '
    'answers a selective-evidence challenge. Never a requirement: completeness is '
    'the walk''s business and is decided offline.';

COMMENT ON COLUMN survey_captures.guidance_model IS
    'The model that gave the direction, so an assessment can be read knowing what '
    'produced it.';
