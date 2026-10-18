-- Let the database accept the video the Go layer already accepts.
--
-- 0017 widened content_type to 'video/mp4' only, while survey.acceptedMedia
-- has carried video/mp4, video/webm and video/quicktime all along. That gap
-- is the worst shape a validation bug takes: the handset's clip passes every
-- check the application makes, gets written to the evidence store, and is then
-- refused by a constraint on the way into the row -- so the bytes are kept,
-- the capture does not exist, and the farmer is told their upload failed for
-- no reason anyone can see from the app.
--
-- quicktime matters specifically. iOS records it by default, so leaving it out
-- refuses the iPhone.

ALTER TABLE survey_captures
    DROP CONSTRAINT survey_captures_content_type_check;

ALTER TABLE survey_captures
    ADD CONSTRAINT survey_captures_content_type_check
    CHECK (content_type IN (
        'image/jpeg', 'image/png', 'image/webp',
        'video/mp4', 'video/webm', 'video/quicktime'));

ALTER TABLE capture_uploads
    DROP CONSTRAINT capture_uploads_content_type_check;

ALTER TABLE capture_uploads
    ADD CONSTRAINT capture_uploads_content_type_check
    CHECK (content_type IN (
        'image/jpeg', 'image/png', 'image/webp',
        'video/mp4', 'video/webm', 'video/quicktime'));

-- The station coverage counter in 0017 tested content_type = 'video/mp4',
-- which would have counted an iPhone's walk-through as no coverage at all --
-- a farmer who recorded all four stations being told none of them were done.
CREATE OR REPLACE FUNCTION refresh_report_capture_counts() RETURNS trigger AS $$
BEGIN
    UPDATE crop_loss_reports r
    SET capture_count = (
            SELECT count(*) FROM survey_captures c WHERE c.report_id = r.id),
        captures_flagged = (
            SELECT count(*) FROM survey_captures c
            WHERE c.report_id = r.id AND cardinality(c.gates_failed) > 0),
        stations_captured = (
            SELECT count(DISTINCT c.station_no) FROM survey_captures c
            WHERE c.report_id = r.id
              AND c.station_no IS NOT NULL
              AND c.content_type LIKE 'video/%')
    WHERE r.id = COALESCE(NEW.report_id, OLD.report_id);
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;
