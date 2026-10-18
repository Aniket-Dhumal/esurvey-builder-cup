-- Removing a provider's error body from assessments already stored with it.
--
-- # What leaked
--
-- While the Gemini API key's IP restriction refused every call, the reasoning
-- service recorded each specialist's failure as the exception's own string --
-- which for the Google SDK is the whole response body. It was stored in the
-- assessment's reasoning, its degraded reason and each specialist's error, and
-- it carried the calling server's public IP address, the Google Cloud project
-- number and the key's restriction policy.
--
-- Those fields are printed on the assessment dossier, shown on the officer's
-- screen and read at appeals. The code that wrote them is fixed in two layers
-- (ai-services failures.py, and saferr.Withheld on the way into this table);
-- this cleans the rows written before it was.
--
-- # Why the whole field is replaced
--
-- Not the matching fragment. A partly redacted provider error is still a
-- provider error on a compensation record, and the sentence around it was
-- built around it. The replacement says what happened and where the detail is.
--
-- # What this does not touch
--
-- Everything that is evidence: figures, confidence, triage, the evidence
-- packet, crop presence verdicts, fraud indicators. Only the text of the four
-- fields that carried the failure, and only where the fingerprint is present;
-- a key that was absent stays absent. The audit log held no copy (checked
-- before writing this) and is append-only in any case.
--
-- Idempotent: a cleaned row no longer matches, so a second run changes nothing.

CREATE OR REPLACE FUNCTION pg_temp.provider_error(t text) RETURNS boolean
LANGUAGE sql IMMUTABLE AS $$
    SELECT t IS NOT NULL AND t ~ (
        '\m[0-9]{1,3}(\.[0-9]{1,3}){3}\M'
        || '|projects/[0-9]+'
        || '|AIza[0-9A-Za-z_-]{20,}'
        || '|googleapis\.com'
        || '|PERMISSION_DENIED|RESOURCE_EXHAUSTED|UNAUTHENTICATED'
        || '|API_KEY_[A-Z_]+'
        || '|callerIp|ErrorInfo|@type'
        || '|\{\s*[''"]error[''"]\s*:'
        || '|Traceback \(most recent call last\)'
    )
$$;

-- Replace one top-level text field, only if it carries the fingerprint.
CREATE OR REPLACE FUNCTION pg_temp.withhold(doc jsonb, field text, sentence text)
RETURNS jsonb LANGUAGE sql IMMUTABLE AS $$
    SELECT CASE
        WHEN pg_temp.provider_error(doc ->> field)
        THEN jsonb_set(doc, ARRAY[field], to_jsonb(sentence))
        ELSE doc
    END
$$;

-- The same for each specialist's error, preserving order and every other key.
CREATE OR REPLACE FUNCTION pg_temp.withhold_specialists(doc jsonb, sentence text)
RETURNS jsonb LANGUAGE sql IMMUTABLE AS $$
    SELECT CASE
        WHEN jsonb_typeof(doc -> 'specialists') <> 'array' THEN doc
        ELSE jsonb_set(doc, '{specialists}', (
            SELECT COALESCE(jsonb_agg(
                     CASE WHEN pg_temp.provider_error(s ->> 'error')
                          THEN jsonb_set(s, '{error}', to_jsonb(sentence))
                          ELSE s END
                     ORDER BY ord), '[]'::jsonb)
              FROM jsonb_array_elements(doc -> 'specialists')
                   WITH ORDINALITY AS e(s, ord)))
    END
$$;

UPDATE assessments
   SET assessment =
       pg_temp.withhold_specialists(
         pg_temp.withhold(
           pg_temp.withhold(
             pg_temp.withhold(
               assessment,
               'reasoning',
               'This assessment was produced without the reasoning panel, '
               || 'because the model service could not be used. No damage '
               || 'figure is stated. The details are in the service log rather '
               || 'than on this record.'),
             'crop_presence_reasoning',
             'the model service could not be used; the details are in the service log'),
           'degraded_reason',
           'the model service could not be used; the details are in the service log'),
         'the model service could not be used; the details are in the service log')
 WHERE pg_temp.provider_error(assessment::text);
