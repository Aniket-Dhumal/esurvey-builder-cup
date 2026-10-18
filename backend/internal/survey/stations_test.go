package survey

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// The stations are the answer to "who chose this evidence". So these tests are
// about two things: that the four points really land on the farmer's own
// field, and that they do not move once the farmer has been sent to them.

// referenceParcel is the Malegaon survey number 453 boundary from mock_files:
// a strip roughly 100 m across and 630 m long, which is the shape that breaks
// naive placement. Its bounding box is half again as wide as the parcel, so
// anything placed by bounding box lands in the neighbour's field.
const referenceParcel = `{"type":"MultiPolygon","coordinates":[[[
[75.813582,18.1727367],[75.8128843,18.1727976],[75.8128041,18.1717538],
[75.8128121,18.1706871],[75.8126999,18.1695823],[75.812645,18.1685947],
[75.8127158,18.1680983],[75.8128221,18.1670804],[75.8135658,18.1672486],
[75.813371,18.1683002],[75.8133622,18.1697051],[75.8136012,18.1706221],
[75.8136012,18.1727167],[75.813582,18.1727367]]]]}`

func testStations(t *testing.T) (*Store, *pgxpool.Pool) {
	t.Helper()

	url := os.Getenv("ESURVEY_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set ESURVEY_TEST_DATABASE_URL to run the station tests")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatalf("bad ESURVEY_TEST_DATABASE_URL: %v", err)
	}
	cfg.MaxConns = 4
	cfg.MinConns = 0

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Skipf("no database: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Skipf("no database: %v", err)
	}
	t.Cleanup(pool.Close)

	clean := func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM parcel_boundaries WHERE farm_id LIKE 'ST-%'`)
	}
	clean()
	t.Cleanup(clean)

	return NewStore(pool, nil), pool
}

// placeBoundary puts the reference parcel in under the given farm id.
func placeBoundary(t *testing.T, pool *pgxpool.Pool, farmID string) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `
		INSERT INTO parcel_boundaries
			(farm_id, farmer_id, survey_number, village_lgd_code, geom, source)
		VALUES ($1, '56567763672', '453', '562111',
		        ST_GeomFromGeoJSON($2), 'mock')
		ON CONFLICT (farm_id) DO UPDATE
		SET geom = EXCLUDED.geom, updated_at = now()`,
		farmID, referenceParcel)
	if err != nil {
		t.Fatalf("placing the boundary: %v", err)
	}
}

// stationsOf calls the placement function directly, without the freezing
// layer, which is what the geometry tests want to look at.
func stationsOf(t *testing.T, pool *pgxpool.Pool, farmID string, downhill *float64) []Station {
	t.Helper()
	rows, err := pool.Query(context.Background(), `
		SELECT station_no, lat, lon, role, placement
		FROM parcel_sampling_stations($1, $2) ORDER BY station_no`,
		farmID, downhill)
	if err != nil {
		t.Fatalf("placing stations: %v", err)
	}
	defer rows.Close()

	var out []Station
	for rows.Next() {
		var s Station
		if err := rows.Scan(&s.StationNo, &s.Lat, &s.Lon, &s.Role, &s.Placement); err != nil {
			t.Fatalf("scanning a station: %v", err)
		}
		out = append(out, s)
	}
	return out
}

func TestEveryStationLandsOnTheFarmersOwnField(t *testing.T) {
	// The one that matters most. The geofence blocks captures outside the
	// parcel, so a station placed outside it would have the app directing a
	// farmer to stand somewhere the app then refuses them for standing --
	// with no way through and no explanation that makes sense.
	store, pool := testStations(t)
	_ = store
	placeBoundary(t, pool, "ST-1")

	for _, s := range stationsOf(t, pool, "ST-1", nil) {
		var inside bool
		var distance float64
		err := pool.QueryRow(context.Background(), `
			SELECT ST_Contains(b.geom, ST_SetSRID(ST_MakePoint($2, $3), 4326)),
			       ST_Distance(b.geom::geography,
			                   ST_SetSRID(ST_MakePoint($2, $3), 4326)::geography)
			FROM parcel_boundaries b WHERE b.farm_id = $1`,
			"ST-1", s.Lon, s.Lat).Scan(&inside, &distance)
		if err != nil {
			t.Fatalf("checking station %d: %v", s.StationNo, err)
		}
		if !inside {
			t.Errorf("station %d (%s) is %.1f m outside the parcel",
				s.StationNo, s.Role, distance)
		}
	}
}

func TestTheStationsAreSpreadAcrossTheParcel(t *testing.T) {
	// Four points clustered at one end would satisfy "four stations" and
	// none of the reason for having them. On a 630 m strip they should be
	// most of a hundred metres apart.
	store, pool := testStations(t)
	_ = store
	placeBoundary(t, pool, "ST-2")

	all := stationsOf(t, pool, "ST-2", nil)
	if len(all) != 4 {
		t.Fatalf("got %d stations, want 4", len(all))
	}

	for i := 1; i < len(all); i++ {
		var metres float64
		err := pool.QueryRow(context.Background(), `
			SELECT ST_Distance(
				ST_SetSRID(ST_MakePoint($1, $2), 4326)::geography,
				ST_SetSRID(ST_MakePoint($3, $4), 4326)::geography)`,
			all[i-1].Lon, all[i-1].Lat, all[i].Lon, all[i].Lat).Scan(&metres)
		if err != nil {
			t.Fatalf("measuring leg %d: %v", i, err)
		}
		if metres < 50 {
			t.Errorf("stations %d and %d are only %.0f m apart; the walk is "+
				"not sampling the field", all[i-1].StationNo, all[i].StationNo, metres)
		}
	}
}

func TestTheMiddleStationsAreOnOppositeFlanks(t *testing.T) {
	// "Both sides of the farm". A strip sampled only down its spine says
	// nothing about either flank, which is where a bund or a low corner
	// usually is.
	store, pool := testStations(t)
	_ = store
	placeBoundary(t, pool, "ST-3")

	all := stationsOf(t, pool, "ST-3", nil)
	var left, right *Station
	for i := range all {
		switch all[i].Role {
		case "mid_left":
			left = &all[i]
		case "mid_right":
			right = &all[i]
		}
	}
	if left == nil || right == nil {
		t.Fatal("the two flank stations are missing")
	}

	// Measured across the parcel, not along it, so compare at the same
	// latitude band: the two should be tens of metres apart.
	var metres float64
	err := pool.QueryRow(context.Background(), `
		SELECT ST_Distance(
			ST_SetSRID(ST_MakePoint($1, $2), 4326)::geography,
			ST_SetSRID(ST_MakePoint($3, $2), 4326)::geography)`,
		left.Lon, left.Lat, right.Lon).Scan(&metres)
	if err != nil {
		t.Fatalf("measuring the flanks: %v", err)
	}
	if metres < 20 {
		t.Errorf("the flank stations are %.0f m apart across a ~100 m parcel; "+
			"they are not on opposite sides", metres)
	}
}

func TestTheLowStationIsTheEndWaterRunsTo(t *testing.T) {
	// Aspect is the direction the slope faces, which is the direction water
	// runs. Station 1 is the low end, so it must sit at whichever end the
	// water is heading for -- and must move when the slope does.
	store, pool := testStations(t)
	_ = store
	placeBoundary(t, pool, "ST-4")

	north, south := 0.0, 180.0
	toNorth := stationsOf(t, pool, "ST-4", &north)
	toSouth := stationsOf(t, pool, "ST-4", &south)

	if len(toNorth) != 4 || len(toSouth) != 4 {
		t.Fatal("expected four stations for each aspect")
	}
	if toNorth[0].Placement != "terrain" {
		t.Errorf("an aspect was supplied; placement is %q", toNorth[0].Placement)
	}

	// Water running north means the northern end is the low one, so station 1
	// sits further north than it does when the water runs south.
	if !(toNorth[0].Lat > toSouth[0].Lat) {
		t.Errorf("station 1 is at lat %.6f with water running north and "+
			"%.6f with it running south; the low end is not following the "+
			"slope", toNorth[0].Lat, toSouth[0].Lat)
	}

	// And the high station is the other end, in both cases.
	if !(toNorth[3].Lat < toSouth[3].Lat) {
		t.Errorf("the high station did not move with the slope: %.6f vs %.6f",
			toNorth[3].Lat, toSouth[3].Lat)
	}
}

func TestAFlatParcelStillGetsFourStations(t *testing.T) {
	// Around Barshi a metre of relief across a whole parcel is common, so
	// there is often no downhill to speak of. That is not a degraded result
	// and must not be a missing one -- the same four points, placed along the
	// long axis, and labelled honestly as geometric.
	store, pool := testStations(t)
	_ = store
	placeBoundary(t, pool, "ST-5")

	all := stationsOf(t, pool, "ST-5", nil)
	if len(all) != 4 {
		t.Fatalf("a parcel with no aspect got %d stations, want 4", len(all))
	}
	for _, s := range all {
		if s.Placement != "geometric" {
			t.Errorf("station %d claims %q placement with no aspect supplied",
				s.StationNo, s.Placement)
		}
	}
}

func TestAParcelWithNoBoundaryCannotBeSampled(t *testing.T) {
	// The ordinary state of a real parcel: AgriStack returns no geometry, so
	// there is nothing to place stations on. It must come back as the
	// "trace it first" signal rather than as an empty walk the farmer is
	// somehow expected to complete.
	store, _ := testStations(t)

	_, err := store.Stations(context.Background(),
		"00000000-0000-4000-8000-00000000dead", "ST-nonexistent", nil)
	if !errors.Is(err, ErrNoBoundary) {
		t.Errorf("got %v, want ErrNoBoundary", err)
	}
}

// makeReport builds the minimal chain a report needs to exist: a district, an
// event in it, and the report itself. Only the columns without defaults are
// filled; everything else is the schema's business.
func makeReport(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	ctx := context.Background()

	// A district must hang off a state: the hierarchy refuses a root that is
	// not one, which is the constraint that stops a stray jurisdiction
	// becoming its own island outside everyone's scope.
	var stateID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO jurisdictions (level, code, name)
		VALUES ('state', 'ST-27', 'Station Test State')
		ON CONFLICT (level, code) DO UPDATE SET name = EXCLUDED.name
		RETURNING id`).Scan(&stateID); err != nil {
		t.Fatalf("creating the state: %v", err)
	}

	var districtID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO jurisdictions (level, code, name, parent_id)
		VALUES ('district', 'ST-496', 'Station Test District', $1)
		ON CONFLICT (level, code) DO UPDATE SET name = EXCLUDED.name
		RETURNING id`, stateID).Scan(&districtID); err != nil {
		t.Fatalf("creating the district: %v", err)
	}

	var eventID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO survey_events
			(reference, calamity_type, title, district_id,
			 incident_start, incident_end, survey_closes_at)
		VALUES ('ST-EVT-1', 'hailstorm', 'Station test event', $1,
		        now() - interval '10 days', now() - interval '9 days',
		        now() + interval '30 days')
		RETURNING id`, districtID).Scan(&eventID); err != nil {
		t.Fatalf("creating the event: %v", err)
	}

	var reportID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO crop_loss_reports
			(reference, survey_event_id, farmer_id, farmer_name,
			 village_lgd_code, village_name, sub_district_lgd_code,
			 sub_district_name, district_lgd_code, district_name,
			 season, crop_year, loss_category, calamity_type, occurred_at)
		VALUES ('ST-RPT-1', $1, '56567763672', 'Harichndra Yedaba Dhumal',
		        '562111', 'Malegaon', '4246', 'Barshi', 'ST-496',
		        'Station Test District', 'kharif', '2026-27',
		        'standing_crop', 'hailstorm', now() - interval '9 days')
		RETURNING id`, eventID).Scan(&reportID); err != nil {
		t.Fatalf("creating the report: %v", err)
	}

	t.Cleanup(func() {
		bg := context.Background()
		_, _ = pool.Exec(bg, `DELETE FROM crop_loss_reports WHERE reference = 'ST-RPT-1'`)
		_, _ = pool.Exec(bg, `DELETE FROM survey_events WHERE reference = 'ST-EVT-1'`)
		_, _ = pool.Exec(bg, `DELETE FROM jurisdictions WHERE code IN ('ST-496', 'ST-27')`)
	})

	return reportID
}

func TestTheStationsDoNotMoveOnceTheFarmerHasBeenSentToThem(t *testing.T) {
	// The whole claim the report makes is that the farmer was *directed* to
	// these points rather than choosing them. That is only true if the points
	// were fixed before the walk -- so a second call, mid-walk, must return
	// exactly what the first one did.
	store, pool := testStations(t)
	placeBoundary(t, pool, "ST-6")
	reportID := makeReport(t, pool)
	ctx := context.Background()

	first, err := store.Stations(ctx, reportID, "ST-6", nil)
	if err != nil {
		t.Fatalf("assigning stations: %v", err)
	}
	if len(first) != 4 {
		t.Fatalf("got %d stations, want 4", len(first))
	}

	second, err := store.Stations(ctx, reportID, "ST-6", nil)
	if err != nil {
		t.Fatalf("re-reading stations: %v", err)
	}

	for i := range first {
		if first[i] != second[i] {
			t.Errorf("station %d moved between visits:\n  was %+v\n  now %+v",
				first[i].StationNo, first[i], second[i])
		}
	}
}

func TestAnAspectArrivingLaterDoesNotMoveAFrozenStation(t *testing.T) {
	// The terrain analysis can complete after the farmer has already set off.
	// If that reordered the stations, somebody halfway through a walk would
	// find station 1 had become station 4 -- and the report's claim that they
	// were directed to a fixed set would be false.
	store, pool := testStations(t)
	placeBoundary(t, pool, "ST-7")
	reportID := makeReport(t, pool)
	ctx := context.Background()

	before, err := store.Stations(ctx, reportID, "ST-7", nil)
	if err != nil {
		t.Fatalf("assigning stations: %v", err)
	}

	downhill := 180.0
	after, err := store.Stations(ctx, reportID, "ST-7", &downhill)
	if err != nil {
		t.Fatalf("re-reading stations: %v", err)
	}

	for i := range before {
		if before[i] != after[i] {
			t.Errorf("station %d changed when the aspect arrived:\n  was %+v\n  now %+v",
				before[i].StationNo, before[i], after[i])
		}
	}
	if after[0].Placement != "geometric" {
		t.Errorf("the frozen set should still read %q, got %q",
			"geometric", after[0].Placement)
	}
}

func TestACorrectedBoundaryRebuildsTheStations(t *testing.T) {
	// The one exception to freezing, and the reason it is not a loophole: a
	// station frozen against the old polygon can fall outside the corrected
	// one, where the geofence refuses the very capture the app is directing
	// the farmer to take. Stale stations are rebuilt; nothing else is.
	store, pool := testStations(t)
	placeBoundary(t, pool, "ST-8")
	reportID := makeReport(t, pool)
	ctx := context.Background()

	before, err := store.Stations(ctx, reportID, "ST-8", nil)
	if err != nil {
		t.Fatalf("assigning stations: %v", err)
	}

	// The farmer re-walks the boundary and it comes out shifted.
	if _, err := pool.Exec(ctx, `
		UPDATE parcel_boundaries
		SET geom = ST_Translate(geom, 0.002, 0.002), updated_at = now()
		WHERE farm_id = 'ST-8'`); err != nil {
		t.Fatalf("moving the boundary: %v", err)
	}

	after, err := store.Stations(ctx, reportID, "ST-8", nil)
	if err != nil {
		t.Fatalf("re-reading stations: %v", err)
	}
	if len(after) != 4 {
		t.Fatalf("got %d stations after the correction, want 4", len(after))
	}

	var moved bool
	for i := range before {
		if before[i] != after[i] {
			moved = true
		}
	}
	if !moved {
		t.Error("the boundary moved and the stations did not; the farmer " +
			"would be directed outside their own corrected parcel")
	}

	// And the rebuilt set must land on the corrected parcel, not the old one.
	for _, s := range after {
		var inside bool
		if err := pool.QueryRow(ctx, `
			SELECT ST_Contains(b.geom, ST_SetSRID(ST_MakePoint($2, $3), 4326))
			FROM parcel_boundaries b WHERE b.farm_id = $1`,
			"ST-8", s.Lon, s.Lat).Scan(&inside); err != nil {
			t.Fatalf("checking station %d: %v", s.StationNo, err)
		}
		if !inside {
			t.Errorf("rebuilt station %d is outside the corrected parcel", s.StationNo)
		}
	}
}

// --- the record that says the farmer was directed ---------------------------

// A claim's evidence is chosen by the person being assessed. What makes that
// answerable at an appeal is the record of where they were sent, which was
// written by nothing until now (docs/EVIDENCE_REVALIDATION.md, V2).
func TestEvidenceFromAStationRecordsWhereTheFarmerWasDirected(t *testing.T) {
	_, pool := testStations(t)
	placeBoundary(t, pool, "ST-30")
	reportID := makeReport(t, pool)
	// The store that can actually hold media: see uploadStore.
	store := uploadStore(t, pool)
	ctx := context.Background()

	// Nothing is recorded until evidence arrives from a directed point.
	before, err := store.CoverageFor(ctx, reportID)
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != 0 {
		t.Fatalf("%d stations recorded before the walk", len(before))
	}

	station := 2
	offset := 8.0
	in := aCapture(reportID, "ST-30")
	in.StationNo = &station
	in.StationOffsetM = &offset
	if _, err := store.Submit(ctx, in); err != nil {
		t.Fatalf("submitting a station capture: %v", err)
	}

	coverage, err := store.CoverageFor(ctx, reportID)
	if err != nil {
		t.Fatal(err)
	}
	if len(coverage) != 4 {
		t.Fatalf("%d points recorded; the walk is four", len(coverage))
	}

	var captured int
	for _, c := range coverage {
		if c.Station.Lat == 0 || c.Station.Lon == 0 {
			t.Errorf("station %d has no position", c.Station.StationNo)
		}
		if !c.Captured {
			continue
		}
		captured++
		if c.Station.StationNo != station {
			t.Errorf("station %d reports evidence; it came from %d",
				c.Station.StationNo, station)
		}
		if c.OffsetM == nil || *c.OffsetM != offset {
			t.Errorf("offset = %v, want the %.0f m the farmer stood away", c.OffsetM, offset)
		}
		if c.HasVideo {
			t.Error("a photograph was recorded as the station's clip")
		}
	}
	if captured != 1 {
		t.Fatalf("%d points report evidence; one was captured", captured)
	}
}

func TestTheDirectedPointsAreNotMovedByLaterEvidence(t *testing.T) {
	// "Fixed before the walk" is the claim the report makes. A second capture
	// must find the same four points, not a fresh placement.
	_, pool := testStations(t)
	placeBoundary(t, pool, "ST-31")
	reportID := makeReport(t, pool)
	store := uploadStore(t, pool)
	ctx := context.Background()

	first := 1
	in := aCapture(reportID, "ST-31")
	in.StationNo = &first
	if _, err := store.Submit(ctx, in); err != nil {
		t.Fatal(err)
	}
	was, err := store.CoverageFor(ctx, reportID)
	if err != nil {
		t.Fatal(err)
	}

	third := 3
	second := aCapture(reportID, "ST-31")
	second.StationNo = &third
	// Another part of the same field: the walk has to move between points
	// (MinStationSpacingM), and this test is about the assigned points not
	// moving, not about that rule.
	second.Lat, second.Lon = 18.17036, 75.81310
	second.Media = bytes.NewReader([]byte("a different photograph of the same field"))
	if _, err := store.Submit(ctx, second); err != nil {
		t.Fatal(err)
	}
	now, err := store.CoverageFor(ctx, reportID)
	if err != nil {
		t.Fatal(err)
	}

	for i := range was {
		if was[i].Station != now[i].Station {
			t.Fatalf("station %d moved between captures: %+v then %+v",
				i+1, was[i].Station, now[i].Station)
		}
	}
}

// The counter the officer reads must mean what the handset means by a point.
//
// The walk asks for a photograph and then a clip at each directed point, and
// the handset does not call a point done until both are on the phone. The
// server's counter used to be satisfied by the clip alone, so a farmer whose
// still failed was told to go back while the office had already counted the
// point (docs/EVIDENCE_REVALIDATION.md, V7 and V8; migration 0039).
func TestAPointCountsOnlyWhenItHasBothAStillAndAClip(t *testing.T) {
	_, pool := testStations(t)
	placeBoundary(t, pool, "ST-33")
	reportID := makeReport(t, pool)
	store := uploadStore(t, pool)
	ctx := context.Background()

	counted := func() int {
		var n int
		if err := pool.QueryRow(ctx,
			`SELECT stations_captured FROM crop_loss_reports WHERE id = $1`,
			reportID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	station := 1
	offset := 6.0

	// The still alone. Half a point is not a point.
	still := aCapture(reportID, "ST-33")
	still.StationNo = &station
	still.StationOffsetM = &offset
	if _, err := store.Submit(ctx, still); err != nil {
		t.Fatalf("submitting the photograph: %v", err)
	}
	if n := counted(); n != 0 {
		t.Fatalf("stations_captured = %d after a photograph alone; want 0", n)
	}

	// The clip completes it.
	clip := aCapture(reportID, "ST-33")
	clip.StationNo = &station
	clip.StationOffsetM = &offset
	clip.Kind = KindStationVideo
	clip.ContentType = "video/mp4"
	clip.Media = strings.NewReader("not really an mp4, but bytes")
	if _, err := store.Submit(ctx, clip); err != nil {
		t.Fatalf("submitting the clip: %v", err)
	}
	if n := counted(); n != 1 {
		t.Fatalf("stations_captured = %d once the point has both; want 1", n)
	}

	// A clip at a second point, with no photograph behind it, is not a
	// second point.
	second := 2
	lone := aCapture(reportID, "ST-33")
	lone.StationNo = &second
	// Far enough to be a second point of the walk rather than the same one.
	lone.Lat, lone.Lon = 18.17036, 75.81310
	lone.StationOffsetM = &offset
	lone.Kind = KindStationVideo
	lone.ContentType = "video/mp4"
	lone.Media = strings.NewReader("not really an mp4 either")
	if _, err := store.Submit(ctx, lone); err != nil {
		t.Fatalf("submitting the second clip: %v", err)
	}
	if n := counted(); n != 1 {
		t.Fatalf("stations_captured = %d with a clip and no still at the "+
			"second point; want 1", n)
	}
}

// addLine puts a report's line on a farm, which is what ties a claim to ground.
//
// makeReport does not: most station tests never need it, because the stations hang
// off the boundary rather than off the claim's lines. TakenPoints does need it --
// the whole question it answers is "what has been filmed on this farm", and the
// line is the only thing that says which farm a claim is about.
func addLine(t *testing.T, pool *pgxpool.Pool, reportID, farmID string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO crop_loss_report_items
			(report_id, line_no, survey_number, farm_id, crop_name,
			 area_sown_ha, area_affected_ha, damage_percent)
		VALUES ($1, 1, '453', $2, 'Soybean', 2.0, 1.5, 60)`,
		reportID, farmID); err != nil {
		t.Fatalf("adding the claim's line: %v", err)
	}
}

func TestAWithdrawnClaimsGroundIsFreeToWalkAgain(t *testing.T) {
	// A survey number is shared, so one claim's points are fenced off from every
	// other claim on the same farm. That is the rule, and it is the right rule --
	// two farmers must not both submit the same ground as their own evidence.
	//
	// A withdrawn claim is not one of those. It is a walk somebody abandoned, and
	// it evidences nothing: the clips are kept because they were really recorded,
	// but nobody is claiming anything with them. Leaving its points fenced meant a
	// farmer who withdrew a claim and came back to redo it was refused at every
	// point of their own field by their own abandoned walk -- and on a small plot,
	// where the spacing rule already holds the four points close together, refused
	// everywhere there was to stand.
	//
	// This is also what makes "clear the claims and test again" work at all. The
	// alternative is deleting the evidence, and evidence that was really recorded
	// in a real field is not a thing to delete to unblock a test.
	store, pool := testStations(t)
	placeBoundary(t, pool, "ST-46")
	reportID := makeReport(t, pool)
	addLine(t, pool, reportID, "ST-46")

	ctx := context.Background()
	station := 1
	in := aCapture(reportID, "ST-46")
	in.StationNo = &station
	if _, err := uploadStore(t, pool).Submit(ctx, in); err != nil {
		t.Fatalf("submitting the walked point: %v", err)
	}

	// Asked as a different farmer, which is the case the fence exists for.
	taken, err := store.TakenPoints(ctx, "ST-46", "", "another-farmer")
	if err != nil {
		t.Fatal(err)
	}
	if len(taken) != 1 {
		t.Fatalf("%d points are taken; the standing claim walked one", len(taken))
	}

	if _, err := pool.Exec(ctx,
		`UPDATE crop_loss_reports SET state = 'withdrawn' WHERE id = $1`,
		reportID); err != nil {
		t.Fatalf("withdrawing the claim: %v", err)
	}

	taken, err = store.TakenPoints(ctx, "ST-46", "", "another-farmer")
	if err != nil {
		t.Fatal(err)
	}
	if len(taken) != 0 {
		t.Fatalf("%d points are still taken by a claim nobody is making", len(taken))
	}
}
