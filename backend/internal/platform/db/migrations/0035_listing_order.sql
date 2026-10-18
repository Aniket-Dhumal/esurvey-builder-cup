-- The officer's listing, served from an index instead of a sort.
--
-- # What it cost
--
-- The worklist orders by contested first, then oldest -- and no index matched
-- that, so every page read the whole of crop_loss_reports, computed each
-- report's crop-line totals through a lateral join, sorted the lot and threw
-- all but fifty rows away. Measured at 200,000 reports in one district: 1.0 to
-- 1.3 seconds for the first page, and it grows with the season
-- (docs/SYNC_SCALE_REVIEW.md).
--
-- With the order in an index, the scan stops once it has the page: the lateral
-- join runs for the rows returned rather than for the district.
--
-- The column order is the ORDER BY, direction included. reported_at ascends
-- because the queue is oldest-first -- the claim that has waited longest is
-- the one a supervisor should see.

CREATE INDEX IF NOT EXISTS crop_loss_reports_worklist_idx
    ON crop_loss_reports (contested DESC, reported_at ASC, id ASC);

-- The same order within one village, for the scopes that are one village: a
-- talathi's list is a handful of rows among a district's hundreds of
-- thousands, and walking the district-wide index to find them would read past
-- everyone else's.
CREATE INDEX IF NOT EXISTS crop_loss_reports_village_worklist_idx
    ON crop_loss_reports (village_lgd_code, contested DESC, reported_at ASC, id ASC);
