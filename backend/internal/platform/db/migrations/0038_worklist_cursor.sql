-- The worklist order, written so a cursor can use it.
--
-- 0035 indexed (contested DESC, reported_at, id), which serves the ORDER BY.
-- It does not serve paging *past* a given row: a cursor's "everything after
-- this one" is a row comparison, and SQL row comparisons order every column
-- the same way -- so a mixed DESC/ASC key has to be written as an OR, which
-- the planner cannot answer from the index. Measured: 10 ms for a page by
-- offset, 780 ms for the same page by cursor.
--
-- contested is a boolean, so `NOT contested` ascending is the same order as
-- `contested` descending -- the contested claims first, which is the point of
-- the sort. With the key expressed that way the whole thing is one ascending
-- comparison the index answers directly.

DROP INDEX IF EXISTS crop_loss_reports_worklist_idx;
DROP INDEX IF EXISTS crop_loss_reports_village_worklist_idx;

CREATE INDEX IF NOT EXISTS crop_loss_reports_worklist_idx
    ON crop_loss_reports ((NOT contested), reported_at, id);

-- The same order within one village, for the scopes that are one village: a
-- talathi's list is a handful of rows among a district's hundreds of
-- thousands.
CREATE INDEX IF NOT EXISTS crop_loss_reports_village_worklist_idx
    ON crop_loss_reports (village_lgd_code, (NOT contested), reported_at, id);
