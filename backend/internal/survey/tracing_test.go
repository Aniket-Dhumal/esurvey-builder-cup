package survey

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// This is the piece every other analysis stands on, so the tests are about
// two things: whether a walk becomes the right polygon, and whether the
// uncertainty in a phone's position survives into the record instead of being
// averaged away.

func testTracing(t *testing.T) (*Store, *pgxpool.Pool) {
	t.Helper()

	url := os.Getenv("ESURVEY_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set ESURVEY_TEST_DATABASE_URL to run the boundary tracing tests")
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
			`DELETE FROM parcel_boundaries WHERE farm_id LIKE 'TR-%'`)
	}
	clean()
	t.Cleanup(clean)

	return NewStore(pool, nil), pool
}

// square walks a roughly square plot of the given side, in metres, starting
// at a point in Solapur district.
func square(sideM float64, accuracyM float64) []TracePoint {
	const lat0, lon0 = 18.1700, 75.8130
	// A degree of latitude is about 111 km; longitude shrinks by the cosine.
	dLat := sideM / 111_000
	dLon := sideM / (111_000 * math.Cos(lat0*math.Pi/180))

	corners := [][2]float64{
		{lat0, lon0},
		{lat0, lon0 + dLon},
		{lat0 + dLat, lon0 + dLon},
		{lat0 + dLat, lon0},
	}
	out := make([]TracePoint, 0, len(corners))
	for i, c := range corners {
		out = append(out, TracePoint{
			Lat: c[0], Lon: c[1], AccuracyM: accuracyM,
			At: time.Now().Add(time.Duration(i) * time.Minute),
		})
	}
	return out
}

func trace(points []TracePoint) Trace {
	return Trace{
		FarmID:         "TR-1",
		FarmerID:       "TR-FARMER",
		SurveyNumber:   "453",
		VillageLGDCode: "562100",
		Points:         points,
	}
}

// ---------------------------------------------------------------------------
// A walk becomes a field
// ---------------------------------------------------------------------------

func TestAWalkAroundAFieldBecomesThatField(t *testing.T) {
	s, _ := testTracing(t)

	// A hundred metres square is one hectare.
	b, tr, err := s.RecordTrace(context.Background(), trace(square(100, 6)))
	if err != nil {
		t.Fatal(err)
	}
	if b.Source != "field_traced" {
		t.Errorf("source = %q", b.Source)
	}
	if b.PolygonAreaHa < 0.9 || b.PolygonAreaHa > 1.1 {
		t.Errorf("a hundred-metre square came out as %.3f ha, want about 1",
			b.PolygonAreaHa)
	}
	if tr.WorstAccuracyM != 6 {
		t.Errorf("worst accuracy = %v", tr.WorstAccuracyM)
	}
}

func TestTheRingIsClosedForTheFarmer(t *testing.T) {
	// A walk ends where it started only if somebody was careful. Refusing an
	// unclosed ring would fail most real walks for a formatting rule.
	s, _ := testTracing(t)

	if _, _, err := s.RecordTrace(context.Background(), trace(square(100, 6))); err != nil {
		t.Fatalf("an unclosed walk was refused: %v", err)
	}
}

func TestPositionsGoOutLongitudeFirst(t *testing.T) {
	// The engine rejects latitude-first with LATLON_SWAPPED, and a boundary
	// sent the wrong way round is a field in the Indian Ocean.
	ring := closedRing(square(100, 6))
	for _, pos := range ring {
		if pos[0] < 68 || pos[0] > 98 {
			t.Errorf("longitude %v is outside India; the pair may be swapped", pos[0])
		}
		if pos[1] < 6 || pos[1] > 38 {
			t.Errorf("latitude %v is outside India", pos[1])
		}
	}
	if ring[0] != ring[len(ring)-1] {
		t.Error("the ring is not closed")
	}
}

func TestAWalkThatCrossesItselfIsRepairedRatherThanRefused(t *testing.T) {
	// Somebody stepping back around a tree produces a bow-tie. Refusing a
	// boundary over a detour would fail the walks taken most carefully.
	s, _ := testTracing(t)

	const lat0, lon0 = 18.1700, 75.8130
	d := 100.0 / 111_000
	bowtie := []TracePoint{
		{Lat: lat0, Lon: lon0, AccuracyM: 5},
		{Lat: lat0 + d, Lon: lon0 + d, AccuracyM: 5},
		{Lat: lat0 + d, Lon: lon0, AccuracyM: 5},
		{Lat: lat0, Lon: lon0 + d, AccuracyM: 5},
	}

	b, _, err := s.RecordTrace(context.Background(), trace(bowtie))
	if err != nil {
		t.Fatalf("a self-crossing walk was refused: %v", err)
	}
	if b.PolygonAreaHa <= 0 {
		t.Errorf("the repaired boundary encloses %v ha", b.PolygonAreaHa)
	}
}

// ---------------------------------------------------------------------------
// What a phone cannot do
// ---------------------------------------------------------------------------

func TestAPositionTooCoarseToDrawWithIsRefusedWithSomethingToDo(t *testing.T) {
	// Fifty metres of uncertainty could put the boundary on a neighbour's
	// field, and the farmer can fix it by standing in the open.
	s, _ := testTracing(t)

	_, _, err := s.RecordTrace(context.Background(), trace(square(100, 80)))
	if err == nil {
		t.Fatal("a boundary was drawn from an 80 m fix")
	}
	if !strings.Contains(err.Error(), "walk it again") {
		t.Errorf("the refusal gives nothing to do: %v", err)
	}
	if !strings.Contains(err.Error(), "neighbour") {
		t.Errorf("the refusal does not say why it matters: %v", err)
	}
}

func TestTheWorstCornerIsWhatTheBoundaryCarriesNotTheAverage(t *testing.T) {
	// One bad corner is what the edge could be out by. Averaging it against
	// three good ones would understate the uncertainty by a factor of four.
	s, _ := testTracing(t)

	pts := square(100, 4)
	pts[2].AccuracyM = 28

	_, tr, err := s.RecordTrace(context.Background(), trace(pts))
	if err != nil {
		t.Fatal(err)
	}
	if tr.WorstAccuracyM != 28 {
		t.Errorf("worst accuracy = %v, want 28", tr.WorstAccuracyM)
	}

	var said bool
	for _, n := range tr.Notes {
		if strings.Contains(n, "28 m") {
			said = true
		}
	}
	if !said {
		t.Errorf("the uncertainty is not stated in metres of field: %v", tr.Notes)
	}
}

func TestTooFewCornersEnclosesNothing(t *testing.T) {
	s, _ := testTracing(t)
	for _, n := range []int{0, 1, 2} {
		if _, _, err := s.RecordTrace(context.Background(),
			trace(square(100, 5)[:n])); err == nil {
			t.Errorf("%d corners produced a boundary", n)
		}
	}
}

func TestATapWithoutAWalkIsRefused(t *testing.T) {
	// Three taps in the same place enclose a few square metres, which is not
	// a field and would make every derived figure meaningless.
	s, _ := testTracing(t)

	_, _, err := s.RecordTrace(context.Background(), trace(square(2, 5)))
	if err == nil {
		t.Fatal("a two-metre square was accepted as a field")
	}
	if !strings.Contains(err.Error(), "Walk the whole edge") {
		t.Errorf("the refusal does not say what to do: %v", err)
	}
}

func TestAWalkThatWrappedAVillageIsRefused(t *testing.T) {
	s, _ := testTracing(t)

	_, _, err := s.RecordTrace(context.Background(), trace(square(3000, 5)))
	if err == nil {
		t.Fatal("a 900-hectare boundary was accepted")
	}
}

func TestARefusedWalkLeavesNoBoundaryBehind(t *testing.T) {
	// The implausible-area check runs after the write, because the area is
	// PostGIS's to compute. A refused walk must not leave a row that a later
	// assessment would read as this farmer's field.
	s, pool := testTracing(t)

	if _, _, err := s.RecordTrace(context.Background(), trace(square(2, 5))); err == nil {
		t.Fatal("expected a refusal")
	}

	var n int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM parcel_boundaries WHERE farm_id = 'TR-1'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Error("a refused walk left a boundary in the table")
	}
}

func TestANonsensePositionIsRefused(t *testing.T) {
	s, _ := testTracing(t)

	pts := square(100, 5)
	pts[1].Lat = math.NaN()
	if _, _, err := s.RecordTrace(context.Background(), trace(pts)); err == nil {
		t.Fatal("a NaN corner was accepted")
	}

	pts = square(100, 5)
	pts[2].Lon = 400
	if _, _, err := s.RecordTrace(context.Background(), trace(pts)); err == nil {
		t.Fatal("a longitude of 400 was accepted")
	}
}

// ---------------------------------------------------------------------------
// The registry, and who wins
// ---------------------------------------------------------------------------

func TestAWalkThatDisagreesWithTheRegistryIsReportedNotCorrected(t *testing.T) {
	// A jointly held survey number, a plot divided between family, and a walk
	// that cut a corner all look identical from here. Choosing between them
	// is not this code's business.
	s, _ := testTracing(t)

	tr0 := trace(square(100, 5)) // about one hectare
	tr0.RegistryExtentHa = 2.5

	b, tr, err := s.RecordTrace(context.Background(), tr0)
	if err != nil {
		t.Fatal(err)
	}
	if b.PolygonAreaHa > 2 {
		t.Fatalf("the walk enclosed %v ha; the fixture is wrong", b.PolygonAreaHa)
	}
	if tr.AreaDisagreement == nil {
		t.Fatal("no disagreement reported against a 2.5 ha registry extent")
	}
	if *tr.AreaDisagreement > -50 {
		t.Errorf("disagreement = %v%%, want a large negative", *tr.AreaDisagreement)
	}

	var explained bool
	for _, n := range tr.Notes {
		if strings.Contains(n, "jointly held") {
			explained = true
			if strings.Contains(n, "corrected to the other") &&
				!strings.Contains(n, "rather than one being") {
				t.Errorf("the note implies a correction: %s", n)
			}
		}
	}
	if !explained {
		t.Errorf("the disagreement carries no ordinary explanation: %v", tr.Notes)
	}
}

func TestASmallDisagreementIsNotRemarkedOn(t *testing.T) {
	// A walk is not a survey, and a few per cent says nothing.
	s, _ := testTracing(t)

	tr0 := trace(square(100, 5))
	tr0.RegistryExtentHa = 1.03

	_, tr, err := s.RecordTrace(context.Background(), tr0)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range tr.Notes {
		if strings.Contains(n, "jointly held") {
			t.Errorf("a three per cent difference was remarked on: %s", n)
		}
	}
}

func TestTheRegistryIsNeverOverwritten(t *testing.T) {
	// Where AgriStack has an answer it is the authority, and a farmer redrawing
	// it would be redrawing the cadastre.
	s, pool := testTracing(t)

	_, err := pool.Exec(context.Background(), `
		INSERT INTO parcel_boundaries (
			farm_id, farmer_id, survey_number, village_lgd_code, geom, source
		) VALUES (
			'TR-1', 'TR-FARMER', '453', '562100',
			ST_Multi(ST_GeomFromText(
				'POLYGON((75.813 18.170, 75.814 18.170, 75.814 18.171, 75.813 18.171, 75.813 18.170))',
				4326)),
			'agristack'
		)`)
	if err != nil {
		t.Fatal(err)
	}

	_, _, err = s.RecordTrace(context.Background(), trace(square(100, 5)))
	if err == nil {
		t.Fatal("a farmer overwrote a registry boundary")
	}
	if !strings.Contains(err.Error(), "land registry") {
		t.Errorf("the refusal does not say why: %v", err)
	}
}

func TestRewalkingReplacesTheBoundaryAndDropsTheConfirmation(t *testing.T) {
	// A farmer who walks it again has corrected themselves, and an officer's
	// agreement was about the boundary that is now gone.
	s, pool := testTracing(t)
	ctx := context.Background()

	if _, _, err := s.RecordTrace(ctx, trace(square(100, 5))); err != nil {
		t.Fatal(err)
	}

	var officer string
	if err := pool.QueryRow(ctx, `
		INSERT INTO users (kind, full_name, username, email, password_hash)
		VALUES ('officer', 'Boundary Officer', 'tr.officer@esurvey-test.in', 'tr.officer@esurvey-test.in', 'x')
		RETURNING id::text`).Scan(&officer); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM users WHERE username = 'tr.officer@esurvey-test.in'`)
	})

	if err := s.Confirm(ctx, "TR-1", officer); err != nil {
		t.Fatal(err)
	}

	// Walked again, larger.
	b, _, err := s.RecordTrace(ctx, trace(square(150, 5)))
	if err != nil {
		t.Fatal(err)
	}
	if b.PolygonAreaHa < 1.8 {
		t.Errorf("the re-walk did not replace the boundary: %v ha", b.PolygonAreaHa)
	}

	_, tr, err := s.TracedBoundary(ctx, "TR-1")
	if err != nil {
		t.Fatal(err)
	}
	if tr.ConfirmedAt != "" {
		t.Error("the old confirmation survived a re-walk")
	}
}

// ---------------------------------------------------------------------------
// Reading it back
// ---------------------------------------------------------------------------

func TestTheBoundaryComesBackAsGeoJSONTheEngineCanTake(t *testing.T) {
	// The whole reason for tracing one: it has to reach the analytics engine
	// in the shape the engine accepts.
	s, _ := testTracing(t)
	ctx := context.Background()

	if _, _, err := s.RecordTrace(ctx, trace(square(100, 5))); err != nil {
		t.Fatal(err)
	}

	geo, _, err := s.TracedBoundary(ctx, "TR-1")
	if err != nil {
		t.Fatal(err)
	}

	var g struct {
		Type        string          `json:"type"`
		Coordinates json.RawMessage `json:"coordinates"`
	}
	if err := json.Unmarshal(geo, &g); err != nil {
		t.Fatalf("the boundary is not GeoJSON: %v", err)
	}
	if g.Type != "MultiPolygon" && g.Type != "Polygon" {
		t.Errorf("type = %q", g.Type)
	}
	if len(g.Coordinates) == 0 {
		t.Error("no coordinates")
	}
}

func TestAnUnconfirmedBoundarySaysSoOnEveryRead(t *testing.T) {
	// Every figure derived from it inherits that it was drawn by the person
	// claiming on it.
	s, _ := testTracing(t)
	ctx := context.Background()

	if _, _, err := s.RecordTrace(ctx, trace(square(100, 5))); err != nil {
		t.Fatal(err)
	}

	_, tr, err := s.TracedBoundary(ctx, "TR-1")
	if err != nil {
		t.Fatal(err)
	}
	var said bool
	for _, n := range tr.Notes {
		if strings.Contains(n, "not yet confirmed") {
			said = true
		}
	}
	if !said {
		t.Errorf("an unconfirmed boundary reads as any other: %v", tr.Notes)
	}
}

func TestTheWalkItselfIsKept(t *testing.T) {
	// A boundary is evidence. Two years later somebody has to be able to ask
	// how this plot was established.
	s, _ := testTracing(t)
	ctx := context.Background()

	pts := square(100, 5)
	if _, _, err := s.RecordTrace(ctx, trace(pts)); err != nil {
		t.Fatal(err)
	}

	_, tr, err := s.TracedBoundary(ctx, "TR-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(tr.Points) != len(pts) {
		t.Errorf("kept %d of %d corners", len(tr.Points), len(pts))
	}
	if tr.TracedAt == "" {
		t.Error("no time recorded for the walk")
	}
}

func TestAParcelWithNoBoundaryIsAKnownState(t *testing.T) {
	s, _ := testTracing(t)
	if _, _, err := s.TracedBoundary(context.Background(), "TR-NOTHING"); err == nil {
		t.Fatal("a parcel with no boundary returned one")
	}
}

func TestConfirmingWhatWasNeverTracedIsRefused(t *testing.T) {
	s, _ := testTracing(t)
	if err := s.Confirm(context.Background(), "TR-NOTHING", ""); err == nil {
		t.Fatal("a boundary that does not exist was confirmed")
	}
}
