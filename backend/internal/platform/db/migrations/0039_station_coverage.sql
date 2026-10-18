-- A directed point counts when it has both a still and a clip.
--
-- 0017 defined stations_captured as the number of distinct points with a
-- video against them, and 0019 fixed the content-type test. Both count a
-- point as done on the clip alone.
--
-- The handset does not. A station asks the farmer for a photograph and then a
-- clip, and the walk is not finished until both are on the phone -- so a
-- farmer whose camera failed on the still was told the point was not done
-- while the server had already counted it (docs/EVIDENCE_REVALIDATION.md, V7).
--
-- With the counter now on the officer's screen (V8) the two have to agree.
-- Where they disagreed the officer's figure was the generous one, which is
-- the wrong direction for a number that says how much of the field the claim
-- actually covers.
--
-- Old rows are recounted at the bottom, so a claim filed before this reads
-- the same way as one filed after it.

CREATE OR REPLACE FUNCTION refresh_report_capture_counts() RETURNS trigger AS $$
BEGIN
    UPDATE crop_loss_reports r
    SET capture_count = (
            SELECT count(*) FROM survey_captures c WHERE c.report_id = r.id),
        captures_flagged = (
            SELECT count(*) FROM survey_captures c
            WHERE c.report_id = r.id AND cardinality(c.gates_failed) > 0),
        stations_captured = (
            SELECT count(*) FROM (
                SELECT c.station_no
                FROM survey_captures c
                WHERE c.report_id = r.id AND c.station_no IS NOT NULL
                GROUP BY c.station_no
                HAVING bool_or(c.content_type LIKE 'video/%')
                   AND bool_or(c.content_type NOT LIKE 'video/%')
            ) whole)
    WHERE r.id = COALESCE(NEW.report_id, OLD.report_id);
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;

-- Recount every report that has captures at a directed point. Reports with no
-- station captures are left alone: their counter is already zero and there are
-- far more of them.
UPDATE crop_loss_reports r
SET stations_captured = (
        SELECT count(*) FROM (
            SELECT c.station_no
            FROM survey_captures c
            WHERE c.report_id = r.id AND c.station_no IS NOT NULL
            GROUP BY c.station_no
            HAVING bool_or(c.content_type LIKE 'video/%')
               AND bool_or(c.content_type NOT LIKE 'video/%')
        ) whole)
WHERE EXISTS (
    SELECT 1 FROM survey_captures c
    WHERE c.report_id = r.id AND c.station_no IS NOT NULL);
