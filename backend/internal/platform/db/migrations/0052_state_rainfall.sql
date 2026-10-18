-- Rainfall as the state itself records it, with the normal beside it.
--
-- # Why this table exists
--
-- Every rainfall figure this platform held came from ERA5 reanalysis, and two
-- things were wrong with that at once.
--
-- The amount was wrong. For Barshi taluka, kharif 2026, ERA5 reported 505 mm
-- where Maharashtra's own gauges recorded 286 mm. A reanalysis resolves
-- convection by averaging it over an eleven-kilometre cell, which over
-- peninsular India inflates the total and erases the dry spells inside it.
--
-- The comparison was missing entirely. A drought determination in India is a
-- departure from normal -- the Manual for Drought Management asks for rainfall
-- deficiency against the long-period average, dry spells at critical stages,
-- and sown area -- and this platform had no normal for anywhere. It could
-- report "505 mm" and could not say whether that was a good season or half of
-- one. For Barshi the answer is 55.4% of normal, and in August 15.6%.
--
-- So this table stores what the state publishes: actual and normal, together,
-- per administrative unit and period.
--
-- # The unit of record is the taluka
--
-- Drought is declared at taluka level in Maharashtra -- the September 2026
-- declaration names 265 of 358 talukas -- so the taluka figure is the one an
-- officer defending an appeal needs to cite. It is also the finest unit this
-- platform can currently resolve a parcel to with certainty: villages hang off
-- sub_district in jurisdictions, and no village-to-circle mapping exists.
--
-- Revenue-circle rows are stored anyway, because the circle series is
-- published, is finer than the taluka, and becomes usable the moment a
-- village-to-circle mapping is loaded. Storing them now means the history is
-- already here when that happens rather than being backfilled later.
--
-- # What is deliberately not here
--
-- No daily series. The state publishes circle-wise daily rainfall only for
-- completed years -- the current year returns "Year should be LESS THAN 2026" --
-- so a dry spell inside this season cannot be computed from this source. Dry
-- spells continue to come from the reanalysis, which is adequate for the
-- distribution of rain across days even where it is wrong about the amount.
-- That division is the honest one, and the assessment should say which source
-- each figure came from.

CREATE TYPE rainfall_period_kind AS ENUM ('month', 'season_to_date');

CREATE TABLE state_rainfall (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),

    -- The administrative unit, as the publishing state identifies it.
    --
    -- unit_level is one of the jurisdiction levels this source publishes at.
    -- unit_name is the name as published, kept verbatim: it is how the row is
    -- matched and how it will be quoted in a report, and normalising it would
    -- lose the ability to show an officer the name they will see on the state's
    -- own page.
    unit_level   jurisdiction_level NOT NULL
                 CHECK (unit_level IN ('sub_district', 'circle')),
    unit_name    text NOT NULL CHECK (length(unit_name) BETWEEN 1 AND 200),

    -- Where it sits, in the publisher's own codes. These are not LGD codes:
    -- the state portal carries its own numbering, and inventing an LGD code for
    -- a row that does not have one would be worse than recording the code the
    -- source actually used.
    source_state_code    text NOT NULL,
    source_division_code text NOT NULL,
    source_district_code text NOT NULL,
    district_name        text NOT NULL,

    -- The jurisdiction this was matched to, where it could be matched.
    --
    -- Nullable on purpose. A published name that matches nothing is still a
    -- true observation and is still stored; what it is not is attachable to an
    -- assessment. A row with a null jurisdiction is a row waiting for a name
    -- reconciliation, not a row to discard.
    jurisdiction_id uuid REFERENCES jurisdictions (id) ON DELETE SET NULL,

    crop_year   integer NOT NULL CHECK (crop_year BETWEEN 1900 AND 2200),
    period_kind rainfall_period_kind NOT NULL,
    -- 1-12 for a month; null for a season-to-date cumulative.
    period_month integer CHECK (period_month BETWEEN 1 AND 12),
    period_start date NOT NULL,
    period_end   date NOT NULL,

    normal_mm         numeric(8,1) NOT NULL CHECK (normal_mm >= 0),
    actual_mm         numeric(8,1) NOT NULL CHECK (actual_mm >= 0),
    -- Carried as published rather than recomputed. The state's rounding is what
    -- will be quoted back at an officer, and a figure this platform derived
    -- that differs in the first decimal from the one on the state's page is an
    -- argument nobody needs to have.
    percent_of_normal numeric(7,1) NOT NULL CHECK (percent_of_normal >= 0),

    source      text NOT NULL,
    -- The instant the source's own page said it was current as of, which is not
    -- when this row was written. A report quoting a rainfall figure has to be
    -- able to say how stale it was.
    observed_at timestamptz,
    fetched_at  timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT rainfall_month_iff_monthly
        CHECK ((period_kind = 'month') = (period_month IS NOT NULL)),
    CONSTRAINT rainfall_period_in_order CHECK (period_end >= period_start),

    -- One row per unit per period per source. Re-running the loader updates in
    -- place: the current month's figure changes every day and the season total
    -- with it, so replacement is the normal operation, not an error.
    UNIQUE (source, unit_level, unit_name, source_district_code,
            crop_year, period_kind, period_month)
);

-- The lookup an assessment makes: this unit, this year, every period.
CREATE INDEX state_rainfall_by_jurisdiction
    ON state_rainfall (jurisdiction_id, crop_year, period_kind, period_month)
    WHERE jurisdiction_id IS NOT NULL;

-- The reconciliation lookup: what has not been matched to a jurisdiction yet.
CREATE INDEX state_rainfall_unmatched
    ON state_rainfall (source, unit_level, district_name)
    WHERE jurisdiction_id IS NULL;

COMMENT ON TABLE state_rainfall IS
    'Rainfall as published by the state, actual against normal, per '
    'administrative unit and period. The authoritative amount; the reanalysis '
    'remains the source for how rain was distributed across days.';
