package survey

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/agribid/esurvey-platform/backend/internal/blob"
)

// walkStore is a store that can actually keep media, which every test here
// needs: a refusal only means something if the accepted case reaches the
// evidence store.
func walkStore(t *testing.T) (*Store, *pgxpool.Pool) {
	t.Helper()
	_, pool := testStations(t)
	dir, err := blob.NewDir(t.TempDir())
	if err != nil {
		t.Fatalf("evidence store: %v", err)
	}
	return NewStore(pool, dir), pool
}

// What the directed walk actually requires.
//
// Two things, and they are easy to confuse. The farmer must be *in the field*:
// that is the geofence, it is mandatory, and it is what stops a claim being
// evidenced from somebody else's land. The farmer must *move between clips*:
// that is the walk, and it exists because corners differ.
//
// What is deliberately not required is standing on the assigned coordinate.
// The points are a suggestion of where to go; a farmer who cannot reach one --
// deep water, a bund, a neighbour's crop between them and it -- has walked
// perfectly well by recording somewhere else in their own field.

// atStation is a clip from one point of the walk.
func atStation(reportID, farmID string, station int, lat, lon float64) New {
	in := aCapture(reportID, farmID)
	in.StationNo = &station
	in.Lat, in.Lon = lat, lon
	// Distinct bytes per station, so the duplicate-image gate is not what is
	// being measured here.
	in.Media = strings.NewReader("clip bytes for station " + string(rune('0'+station)))
	return in
}

func TestTheWalkMustMoveBetweenPoints(t *testing.T) {
	store, pool := walkStore(t)
	placeBoundary(t, pool, "ST-9")
	reportID := makeReport(t, pool)
	ctx := context.Background()

	if _, err := store.Submit(ctx, atStation(reportID, "ST-9", 1, 18.1700, 75.8131)); err != nil {
		t.Fatalf("the first point: %v", err)
	}

	// The second point, recorded without moving: about four metres away,
	// which is inside the parcel and inside the handset's own error.
	_, err := store.Submit(ctx, atStation(reportID, "ST-9", 2, 18.17003, 75.81313))
	if !errors.Is(err, ErrSamePosition) {
		t.Fatalf("got %v, want ErrSamePosition: four clips from one spot are "+
			"one reading filed four times", err)
	}
	// And the refusal has to tell the farmer what to do about it.
	if !strings.Contains(err.Error(), "m from the last one") {
		t.Errorf("the refusal does not say how far they moved: %q", err)
	}
}

func TestAPointRecordedElsewhereInTheFieldIsAccepted(t *testing.T) {
	// The whole change: no coordinate is enforced, only that the walk moved.
	// This position is nowhere near any assigned station -- it is simply
	// another part of the same parcel.
	store, pool := walkStore(t)
	placeBoundary(t, pool, "ST-9")
	reportID := makeReport(t, pool)
	ctx := context.Background()

	if _, err := store.Submit(ctx, atStation(reportID, "ST-9", 1, 18.1700, 75.8131)); err != nil {
		t.Fatalf("the first point: %v", err)
	}
	// Roughly 40 m away, still well inside the parcel.
	c, err := store.Submit(ctx, atStation(reportID, "ST-9", 2, 18.17036, 75.81310))
	if err != nil {
		t.Fatalf("a clip from another part of the farmer's own field was refused: %v", err)
	}
	if !c.Trusted() {
		t.Errorf("gates failed on an ordinary second point: %v", c.GatesFailed)
	}
}

func TestRerecordingTheSamePointWhereItWasFirstTriedIsAllowed(t *testing.T) {
	// A clip the phone failed to upload is re-recorded where it was taken.
	// Refusing that would strand the farmer at a point they have already
	// walked to, for having walked to it twice.
	store, pool := walkStore(t)
	placeBoundary(t, pool, "ST-9")
	reportID := makeReport(t, pool)
	ctx := context.Background()

	if _, err := store.Submit(ctx, atStation(reportID, "ST-9", 1, 18.1700, 75.8131)); err != nil {
		t.Fatalf("the first attempt: %v", err)
	}
	again := atStation(reportID, "ST-9", 1, 18.17001, 75.81311)
	again.Media = strings.NewReader("a second clip of station one")
	if _, err := store.Submit(ctx, again); err != nil {
		t.Fatalf("re-recording the same point was refused: %v", err)
	}
}

func TestTheGeofenceStillOutranksTheWalk(t *testing.T) {
	// Moving far enough is not a way out of the parcel: a farmer who walks
	// 200 m into the neighbouring field has satisfied the spread rule and
	// must still be refused.
	store, pool := walkStore(t)
	placeBoundary(t, pool, "ST-9")
	reportID := makeReport(t, pool)
	ctx := context.Background()

	if _, err := store.Submit(ctx, atStation(reportID, "ST-9", 1, 18.1700, 75.8131)); err != nil {
		t.Fatalf("the first point: %v", err)
	}
	_, err := store.Submit(ctx, atStation(reportID, "ST-9", 2, 18.1800, 75.8131))
	if !errors.Is(err, ErrOutsideParcel) {
		t.Fatalf("got %v, want ErrOutsideParcel", err)
	}
}

func TestAnUnmappedParcelFencesTheClaimToOnePlace(t *testing.T) {
	// No boundary, so there is no geofence and never was one. The claim's own
	// first capture becomes the field: photographs from the next village are
	// refused, and photographs from across the same field are not.
	store, pool := walkStore(t)
	reportID := makeReport(t, pool)
	ctx := context.Background()

	first := aCapture(reportID, "ST-UNMAPPED")
	if _, err := store.Submit(ctx, first); err != nil {
		t.Fatalf("the first capture on an unmapped parcel: %v", err)
	}

	near := aCapture(reportID, "ST-UNMAPPED")
	near.Lat, near.Lon = 18.17050, 75.81340
	near.Media = strings.NewReader("a second photograph from the same field")
	if _, err := store.Submit(ctx, near); err != nil {
		t.Fatalf("a second photograph from the same field was refused: %v", err)
	}

	far := aCapture(reportID, "ST-UNMAPPED")
	far.Lat, far.Lon = 18.2500, 75.9000 // about 12 km away
	far.Media = strings.NewReader("a photograph from somewhere else entirely")
	_, err := store.Submit(ctx, far)
	if !errors.Is(err, ErrAwayFromField) {
		t.Fatalf("got %v, want ErrAwayFromField", err)
	}
}

func TestTheSpreadFitsTheField(t *testing.T) {
	// Of 437 mapped parcels in the first real village imported, three are
	// under thirty metres across and the smallest is nine. A flat fifteen
	// metres would ask those farmers to stand outside their own field, which
	// the geofence then refuses -- a rule contradicting itself on the plots
	// least able to argue with it.
	for _, c := range []struct {
		what   string
		areaM2 float64
		want   float64
	}{
		{"an unmapped parcel, with no area to go on", 0, MinStationSpacingM},
		{"the smallest plot in Malegaon, 9 m across", 81, SmallestStationSpacingM},
		{"a tenth of a hectare, 32 m across", 1000, 10.5},
		{"a quarter hectare, 50 m across", 2500, 16.7},
		{"a hectare, 100 m across", 10_000, MinStationSpacingM},
		{"the largest in the village, 17.8 ha", 178_000, MinStationSpacingM},
	} {
		got := SpacingFor(c.areaM2)
		want := min(max(c.want, SmallestStationSpacingM), MinStationSpacingM)
		if got < want-0.1 || got > want+0.1 {
			t.Errorf("%s: spacing %.1f m, want %.1f m", c.what, got, want)
		}
	}

	// A bigger field asks for more, never less.
	if SpacingFor(200) > SpacingFor(2000) {
		t.Error("a smaller field asked for a longer walk than a larger one")
	}
}

// aNeighboursClaim is a second claim, by a different farmer, on the same farm.
//
// A survey number is routinely shared -- joint owners, a household's parcels, a
// neighbour's holding under one number -- so two claims on one farm_id is the
// ordinary case rather than an edge one. It reuses the event makeReport created,
// because a calamity is declared for a place and everybody in it claims against
// the same one.
func aNeighboursClaim(t *testing.T, pool *pgxpool.Pool, farmID string) string {
	t.Helper()
	ctx := context.Background()

	var eventID string
	if err := pool.QueryRow(ctx,
		`SELECT id FROM survey_events WHERE reference = 'ST-EVT-1'`,
	).Scan(&eventID); err != nil {
		t.Fatalf("finding the event makeReport created: %v", err)
	}

	var reportID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO crop_loss_reports
			(reference, survey_event_id, farmer_id, farmer_name,
			 village_lgd_code, village_name, sub_district_lgd_code,
			 sub_district_name, district_lgd_code, district_name,
			 season, crop_year, loss_category, calamity_type, occurred_at)
		VALUES ('ST-RPT-2', $1, '64453969224', 'Sunanda Balasaheb Dhumal',
		        '562111', 'Malegaon', '4246', 'Barshi', 'ST-496',
		        'Station Test District', 'kharif', '2026-27',
		        'standing_crop', 'hailstorm', now() - interval '9 days')
		RETURNING id`, eventID).Scan(&reportID); err != nil {
		t.Fatalf("creating the neighbour's claim: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM crop_loss_reports WHERE reference = 'ST-RPT-2'`)
	})

	addLine(t, pool, reportID, farmID)
	return reportID
}

func TestGroundAnotherClaimHasFilmedIsRefused(t *testing.T) {
	// The rule a shared survey number needs. Two farmers hold land under one
	// number, and neither may submit the other's ground as evidence of their own
	// loss -- otherwise one walk could be filed twice, by two people, for two
	// payouts.
	//
	// This is the first test to assert the refusal actually fires. There was a
	// test that the list of taken points was built correctly and none that a
	// capture onto that ground was refused, which is how the two queries answering
	// the same question came apart without anything failing.
	store, pool := walkStore(t)
	placeBoundary(t, pool, "ST-47")
	mine := makeReport(t, pool)
	addLine(t, pool, mine, "ST-47")
	theirs := aNeighboursClaim(t, pool, "ST-47")
	ctx := context.Background()

	if _, err := store.Submit(ctx,
		atStation(theirs, "ST-47", 1, 18.1700, 75.8131)); err != nil {
		t.Fatalf("the neighbour's own clip: %v", err)
	}

	_, err := store.Submit(ctx, atStation(mine, "ST-47", 1, 18.1700, 75.8131))
	if !errors.Is(err, ErrPointTakenInSurvey) {
		t.Fatalf("got %v, want ErrPointTakenInSurvey: this ground is already "+
			"somebody else's evidence", err)
	}
	// And it has to say whose, because "go to your own share" is the only thing
	// the farmer standing there can act on.
	if !strings.Contains(err.Error(), "already") {
		t.Errorf("the refusal does not say the ground is taken: %q", err)
	}
}

func TestGroundAWithdrawnClaimFilmedIsFreeAgain(t *testing.T) {
	// The other half, and the half that was missing.
	//
	// TakenPoints was taught that a withdrawn claim evidences nothing and its
	// ground is free; the refusal query was not. So the handset was handed a list
	// with the withdrawn claim's points left out, let the farmer record there, and
	// the office refused the clip -- a farmer walking the field twice and being
	// told, wrongly, that somebody else has that ground.
	//
	// The two queries answer one question. A difference between them is always a
	// farmer sent back.
	store, pool := walkStore(t)
	placeBoundary(t, pool, "ST-48")
	mine := makeReport(t, pool)
	addLine(t, pool, mine, "ST-48")
	theirs := aNeighboursClaim(t, pool, "ST-48")
	ctx := context.Background()

	if _, err := store.Submit(ctx,
		atStation(theirs, "ST-48", 1, 18.1700, 75.8131)); err != nil {
		t.Fatalf("the neighbour's own clip: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`UPDATE crop_loss_reports SET state = 'withdrawn' WHERE id = $1`,
		theirs); err != nil {
		t.Fatalf("withdrawing the neighbour's claim: %v", err)
	}

	if _, err := store.Submit(ctx,
		atStation(mine, "ST-48", 1, 18.1700, 75.8131)); err != nil {
		t.Fatalf("got %v; a claim nobody is making does not hold ground", err)
	}

	// And the list the handset is given agrees, which is the point: the phone must
	// permit exactly what the office will accept.
	taken, err := store.TakenPoints(ctx, "ST-48", mine, "56567763672")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range taken {
		if p.SameFarmer {
			continue
		}
		t.Errorf("the handset is still fenced off ground at %v, %v", p.Lat, p.Lon)
	}
}
