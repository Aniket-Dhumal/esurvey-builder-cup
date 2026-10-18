-- Claim references from a counter, not a count.
--
-- # The failure this replaces
--
-- A reference was CLR-<year>-<district>-<count of reports so far + 1>, read
-- inside the filing transaction with nothing locked. Two farmers filing in the
-- same district at the same moment read the same count, built the same
-- reference, and the unique constraint refused the second -- answered 400, so
-- the farmer's phone recorded their claim as rejected. Measured: 10 of 16
-- simultaneous filings in one district refused. On the morning after a
-- district-wide event that is most claims.
--
-- The count also scanned every report the district had ever filed, inside the
-- transaction, so each filing got slower as the season went on.
--
-- # Why a row per district and year
--
-- One INSERT ... ON CONFLICT DO UPDATE ... RETURNING on a single row is atomic
-- and costs one index lookup whatever the size of the district. It is taken
-- inside the filing transaction, immediately before the report is inserted,
-- so the row is held only for the insert and commit; a rolled-back filing
-- gives its number back.

CREATE TABLE IF NOT EXISTS report_reference_counters (
    district_lgd_code text    NOT NULL,
    year              text    NOT NULL,
    last_number       integer NOT NULL CHECK (last_number >= 0),
    PRIMARY KEY (district_lgd_code, year)
);

COMMENT ON TABLE report_reference_counters IS
    'The last reference number issued per district and crop year. Advanced '
    'atomically inside the filing transaction, just before the insert.';

-- Carry on from the references already issued, so nothing issued before this
-- migration can be issued again. The highest number in use, not the count:
-- they differ wherever a report was ever removed.
INSERT INTO report_reference_counters (district_lgd_code, year, last_number)
SELECT district_lgd_code,
       left(crop_year, 4),
       max(COALESCE(substring(reference FROM '-([0-9]+)$')::integer, 0))
  FROM crop_loss_reports
 WHERE reference LIKE 'CLR-%'
 GROUP BY district_lgd_code, left(crop_year, 4)
ON CONFLICT (district_lgd_code, year) DO UPDATE
   SET last_number = GREATEST(report_reference_counters.last_number,
                              EXCLUDED.last_number);
