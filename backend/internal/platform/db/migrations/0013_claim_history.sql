-- Looking up what has been claimed here before.
--
-- Almost all of this needed no migration. The facts have been in the database
-- since the first report was filed, and crop_loss_reports is already indexed
-- by village, by farmer and by event from 0005. Nothing has ever read them:
-- a parcel claimed on in four consecutive years, a farmer whose last three
-- claims were rejected, a lone report from a village where nobody else filed
-- against the same event -- all recorded, none of it reaching the officer
-- deciding the next one.
--
-- # What this is for, and what it is not for
--
-- Every one of those patterns has an innocent explanation and usually has
-- one. Land beside a nallah floods most years, so the farmer beside it claims
-- most years and is right every time. A village where one person filed may be
-- a village where one person has a handset. These are reasons for an officer
-- to look, never findings, and the code that builds them refuses to state one
-- without its ordinary explanation alongside.
--
-- # Why only one index
--
-- The history look-ups filter on a farmer or a parcel first, which is
-- selective enough that a dozen rows survive to be sorted. Adding indexes to
-- order twelve rows would cost write throughput on every report filed to save
-- nothing. Only the parcel look-up had no index to filter on at all.

-- Claims against the same registry parcel, across years. Partial: farm_id is
-- null for every report filed against land AgriStack has not linked, which is
-- most of them today, and those rows have nothing to find here.
CREATE INDEX crop_loss_report_items_farm_idx
    ON crop_loss_report_items (farm_id)
    WHERE farm_id IS NOT NULL;
