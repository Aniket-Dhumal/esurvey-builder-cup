-- The names the state writes, against the names this platform holds.
--
-- # Why a table and not a rule
--
-- Maharashtra's rainfall portal and this platform disagree on the spelling of
-- administrative units they both take from the same government:
--
--     portal          this platform
--     N.solapur       Solapur North
--     S.solapur       Solapur South
--     Sangola         Sangole
--     Mangalwedha     Mangalvedhe
--     Aagalgaon       Agalgaon        (circle)
--     Naari           Nari            (circle)
--     Pangari         Pangri          (circle)
--     Khandvi         Khandavi        (circle)
--     Upaledu         Upale Dumala    (circle)
--
-- Four of eleven Solapur talukas, and half its circles. Every one is obvious to
-- somebody who knows the district. None is safely derivable.
--
-- "N.solapur" to "Solapur North" is not a transliteration difference, it is an
-- abbreviation with the qualifier moved. "Upaledu" to "Upale Dumala" is a
-- truncation. And a rule loose enough to join "Sangola" to "Sangole" -- treat a
-- trailing vowel as optional -- would also join genuinely different places: this
-- state contains Karmala and Karmali, Wagholi and Wagholi, Jeur twice inside one
-- district. A wrong join attaches one taluka's rainfall deficit to another
-- taluka's farmers, silently, with nothing in the record to show it happened.
--
-- So the mapping is data, written by a person, one row at a time, with who wrote
-- it and why. A name nobody has mapped stays unmatched and gets printed at the
-- end of every load. That is a visible gap; a fuzzy match is an invisible error.
--
-- # It is deliberately not clever
--
-- No trigram index, no similarity threshold, no "did you mean". Those belong in
-- a tool that *suggests* rows for a person to approve, which would be a fine
-- thing to build and is not this.

CREATE TABLE rainfall_unit_aliases (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),

    -- The source whose spelling this is. An alias is not universal: a different
    -- portal may abbreviate differently, and one that spelled "Solapur North"
    -- correctly must not inherit this row.
    source text NOT NULL,

    unit_level jurisdiction_level NOT NULL
               CHECK (unit_level IN ('sub_district', 'circle')),

    -- The name exactly as the source publishes it, and the district it was
    -- published under. Scoped to the district because a circle name is only
    -- unique within one, and barely even then.
    published_name        text NOT NULL
                          CHECK (length(published_name) BETWEEN 1 AND 200),
    source_district_code  text NOT NULL,

    -- What it is. Not a name: the jurisdiction itself, so a later rename of
    -- either side cannot silently break the mapping.
    jurisdiction_id uuid NOT NULL REFERENCES jurisdictions (id) ON DELETE CASCADE,

    -- Who decided, and on what basis. An alias is a small administrative
    -- judgement and the next person needs to be able to see it was made on
    -- purpose and by whom.
    note       text NOT NULL CHECK (length(note) BETWEEN 1 AND 500),
    created_by uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now(),

    -- One meaning per published name per source per district. A second row for
    -- the same name would be exactly the ambiguity this table exists to remove.
    UNIQUE (source, unit_level, source_district_code, published_name)
);

CREATE INDEX rainfall_unit_aliases_lookup
    ON rainfall_unit_aliases (source, unit_level, source_district_code);

COMMENT ON TABLE rainfall_unit_aliases IS
    'Published unit names mapped to jurisdictions by hand. Deliberately not a '
    'similarity rule: a wrong join attaches one unit''s rainfall deficit to '
    'another unit''s farmers with nothing in the record to show it.';

-- The four Solapur talukas found unmatched by the first load.
--
-- Seeded rather than left for somebody to type because they are unambiguous and
-- because Solapur is where the trial is running. Written as a lookup by name so
-- this migration is inert on a database that does not hold Solapur -- a fresh
-- one, or a deployment of another state -- rather than failing on a missing
-- jurisdiction.
INSERT INTO rainfall_unit_aliases
    (source, unit_level, published_name, source_district_code,
     jurisdiction_id, note)
SELECT 'maharain_mh_gov_in', 'sub_district', v.published, '12', j.id,
       'Maharain abbreviates or spells this taluka differently; confirmed '
       'against the eleven talukas of Solapur, which both sides agree on in '
       'number and in every other name.'
  FROM (VALUES
      ('N.solapur',   'Solapur North'),
      ('S.solapur',   'Solapur South'),
      ('Sangola',     'Sangole'),
      ('Mangalwedha', 'Mangalvedhe')
  ) AS v(published, ours)
  JOIN jurisdictions d
    ON d.level = 'district' AND lower(d.name) = 'solapur'
  JOIN jurisdictions j
    ON j.level = 'sub_district'
   AND lower(j.name) = lower(v.ours)
   AND d.id = ANY (j.ancestor_ids)
ON CONFLICT DO NOTHING;
