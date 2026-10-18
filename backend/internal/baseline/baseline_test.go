package baseline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/agribid/esurvey-platform/backend/internal/satellite"
)

// Two things are being tested here and the second matters as much as the
// first: whether the baselines say the right thing, and how many billable
// analyses it took to say it. A correct denominator that quadruples the cost
// of every claim is not a feature anybody can afford to switch on.

// fakeEngine records what was asked of it and returns what it is told to.
type fakeEngine struct {
	mu    sync.Mutex
	calls []satellite.NDVIRequest
	// byWindow keys a canned series on the sowing date, so a test can give
	// each season a different curve.
	byWindow map[string][]satellite.NDVIPoint
	fail     map[string]error
	fallback []satellite.NDVIPoint
}

func (f *fakeEngine) NDVI(
	_ context.Context, req satellite.NDVIRequest,
) (*satellite.NDVIResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, req)

	if err, ok := f.fail[req.SowingDate]; ok {
		return nil, err
	}
	if ts, ok := f.byWindow[req.SowingDate]; ok {
		return &satellite.NDVIResult{TimeSeries: ts}, nil
	}
	return &satellite.NDVIResult{TimeSeries: f.fallback}, nil
}

func (f *fakeEngine) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func quiet() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// series builds a rise to a peak and a fall to an end value.
func series(from string, peak, end float64) []satellite.NDVIPoint {
	start, _ := time.Parse(dayLayout, from)
	d := func(n int) string { return start.AddDate(0, 0, n).Format(dayLayout) }
	return []satellite.NDVIPoint{
		{Date: d(0), NDVI: 0.16},
		{Date: d(20), NDVI: peak * 0.6},
		{Date: d(40), NDVI: peak},
		{Date: d(60), NDVI: end},
	}
}

// testStore builds a store against the test database, or without one.
func testStore(t *testing.T, eng Engine) *Store {
	t.Helper()

	url := os.Getenv("ESURVEY_TEST_DATABASE_URL")
	if url == "" {
		// The computation is testable without a database; only the cache is
		// not. Running without one keeps the arithmetic covered everywhere.
		return NewStore(nil, eng, quiet())
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
		return NewStore(nil, eng, quiet())
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return NewStore(nil, eng, quiet())
	}
	t.Cleanup(pool.Close)

	// This package's own products, before and after -- never the whole table.
	//
	// Two reasons. The cache is keyed by geometry and window rather than by
	// test, so two tests asking about the same parcel over the same season
	// share an entry and the second is served the first one's canned series;
	// the first run of this suite proved it by making the engine calls
	// vanish. And rscache has its own suite against the same table, running
	// in parallel with this one under `go test ./...`, so clearing
	// everything would delete rows out from under a package that has no idea
	// this one exists. The names are namespaced for the same reason: rscache
	// clears `test_%`, so a product called test_ndvi here would be swept by
	// a suite running beside this one.
	clean := func() {
		_, _ = pool.Exec(context.Background(), `
			DELETE FROM remote_sensing_cache
			WHERE product IN ('ndvi_parcel', 'ndvi_neighbourhood', 'ndvi_selftest')`)
	}
	clean()
	t.Cleanup(clean)

	return NewStore(pool, eng, quiet())
}

var testGeom = satellite.Geometry{
	Type:        "Polygon",
	Coordinates: json.RawMessage(`[[[73.8,18.5],[73.81,18.5],[73.81,18.51],[73.8,18.51],[73.8,18.5]]]`),
}

// ---------------------------------------------------------------------------
// A1 -- the parcel against itself
// ---------------------------------------------------------------------------

func TestAFallFromAHistoryOfGoodSeasonsReadsAsALoss(t *testing.T) {
	// The case the whole class exists for. This parcel normally reaches about
	// 0.73; this year it reached 0.31. That is a loss whatever the farmer
	// declared.
	eng := &fakeEngine{byWindow: map[string][]satellite.NDVIPoint{
		"2025-06-15": series("2025-06-15", 0.71, 0.65),
		"2024-06-15": series("2024-06-15", 0.76, 0.70),
		"2023-06-15": series("2023-06-15", 0.69, 0.66),
	}}
	s := testStore(t, eng)

	h, err := s.ParcelHistory(context.Background(), testGeom, "F1",
		"2026-06-15", "2026-08-14", 0.31)
	if err != nil {
		t.Fatal(err)
	}
	if len(h.Seasons) != 3 {
		t.Fatalf("read %d prior seasons, want 3: %v", len(h.Seasons), h.Caveats)
	}
	if h.Verdict != VerdictBelowNormal {
		t.Errorf("verdict = %s, want below_normal (mean %v, this year %v)",
			h.Verdict, h.MeanPeak, h.ThisSeasonPeak)
	}
	if h.ZScore == nil {
		t.Fatal("no z-score from three seasons with spread")
	}
	if *h.ZScore > -1 {
		t.Errorf("z = %v, want well below -1", *h.ZScore)
	}
}

func TestAFallToWhatThisLandAlwaysDoesIsNotALoss(t *testing.T) {
	// The other half, and the reason a denominator is worth having. A parcel
	// that has never exceeded 0.45 has not lost anything by reaching 0.44 --
	// though without this the drop from a good early peak would look like it.
	eng := &fakeEngine{byWindow: map[string][]satellite.NDVIPoint{
		"2025-06-15": series("2025-06-15", 0.44, 0.40),
		"2024-06-15": series("2024-06-15", 0.47, 0.42),
		"2023-06-15": series("2023-06-15", 0.43, 0.39),
	}}
	s := testStore(t, eng)

	h, err := s.ParcelHistory(context.Background(), testGeom, "F1",
		"2026-06-15", "2026-08-14", 0.45)
	if err != nil {
		t.Fatal(err)
	}
	if h.Verdict != VerdictNormal {
		t.Errorf("verdict = %s, want normal", h.Verdict)
	}
}

func TestAClaimAgainstAnUnusuallyGoodYearIsSaidPlainly(t *testing.T) {
	// Not an accusation. The farmer's loss from that peak is real; the point
	// is that the denominator is larger than an average year, and an officer
	// deciding a figure should be told.
	eng := &fakeEngine{byWindow: map[string][]satellite.NDVIPoint{
		"2025-06-15": series("2025-06-15", 0.50, 0.45),
		"2024-06-15": series("2024-06-15", 0.52, 0.47),
		"2023-06-15": series("2023-06-15", 0.48, 0.44),
	}}
	s := testStore(t, eng)

	h, err := s.ParcelHistory(context.Background(), testGeom, "F1",
		"2026-06-15", "2026-08-14", 0.78)
	if err != nil {
		t.Fatal(err)
	}
	if h.Verdict != VerdictAboveNormal {
		t.Fatalf("verdict = %s, want above_normal", h.Verdict)
	}
	var explained bool
	for _, c := range h.Caveats {
		if strings.Contains(c, "unusually good") {
			explained = true
		}
	}
	if !explained {
		t.Errorf("nothing explains what an above-normal year means: %v", h.Caveats)
	}
}

func TestASeasonThatCannotBeReadIsSkippedNotFatal(t *testing.T) {
	// The archive does not reach every year, and a parcel may not have been
	// cropped. Losing the whole baseline because one season is missing would
	// throw away the two that answered.
	eng := &fakeEngine{
		byWindow: map[string][]satellite.NDVIPoint{
			"2025-06-15": series("2025-06-15", 0.71, 0.65),
			"2024-06-15": series("2024-06-15", 0.76, 0.70),
		},
		fail: map[string]error{
			"2023-06-15": errors.New("window precedes the archive"),
		},
	}
	s := testStore(t, eng)

	h, err := s.ParcelHistory(context.Background(), testGeom, "F1",
		"2026-06-15", "2026-08-14", 0.31)
	if err != nil {
		t.Fatalf("one missing season failed the whole baseline: %v", err)
	}
	if len(h.Seasons) != 2 {
		t.Errorf("kept %d seasons, want 2", len(h.Seasons))
	}
	var said bool
	for _, c := range h.Caveats {
		if strings.Contains(c, "2023") {
			said = true
		}
	}
	if !said {
		t.Errorf("the missing season is not mentioned: %v", h.Caveats)
	}
}

func TestNoPriorSeasonMeansNoBaselineAndSaysSo(t *testing.T) {
	// Reporting "normal" here would be inventing a comparison.
	eng := &fakeEngine{fail: map[string]error{
		"2025-06-15": errors.New("no"),
		"2024-06-15": errors.New("no"),
		"2023-06-15": errors.New("no"),
	}}
	s := testStore(t, eng)

	h, err := s.ParcelHistory(context.Background(), testGeom, "F1",
		"2026-06-15", "2026-08-14", 0.31)
	if err != nil {
		t.Fatal(err)
	}
	if h.Verdict != VerdictUnknown {
		t.Errorf("verdict = %s with no prior season read", h.Verdict)
	}
	if h.Confidence != confidenceLow {
		t.Errorf("confidence = %s", h.Confidence)
	}
	if len(h.Caveats) == 0 {
		t.Error("nothing says the figure stands on nothing")
	}
}

func TestASeasonWithAlmostNoPassesIsNotABaseline(t *testing.T) {
	// A single cloud-free pass says where the crop was on one day, not what
	// the parcel is capable of.
	eng := &fakeEngine{byWindow: map[string][]satellite.NDVIPoint{
		"2025-06-15": {{Date: "2025-07-20", NDVI: 0.70}},
		"2024-06-15": series("2024-06-15", 0.76, 0.70),
		"2023-06-15": series("2023-06-15", 0.69, 0.66),
	}}
	s := testStore(t, eng)

	h, err := s.ParcelHistory(context.Background(), testGeom, "F1",
		"2026-06-15", "2026-08-14", 0.31)
	if err != nil {
		t.Fatal(err)
	}
	if len(h.Seasons) != 2 {
		t.Errorf("kept %d seasons; the one-pass season should not count", len(h.Seasons))
	}
}

func TestAParcelThatNeverVariesDoesNotGetAnExaggeratedScore(t *testing.T) {
	// Three identical seasons give a standard deviation near zero, and a
	// z-score against it would make a trivial difference look extreme.
	eng := &fakeEngine{byWindow: map[string][]satellite.NDVIPoint{
		"2025-06-15": series("2025-06-15", 0.70, 0.65),
		"2024-06-15": series("2024-06-15", 0.70, 0.65),
		"2023-06-15": series("2023-06-15", 0.70, 0.65),
	}}
	s := testStore(t, eng)

	h, err := s.ParcelHistory(context.Background(), testGeom, "F1",
		"2026-06-15", "2026-08-14", 0.69)
	if err != nil {
		t.Fatal(err)
	}
	if h.ZScore != nil {
		t.Errorf("a z-score of %v was reported against a spread of %v",
			*h.ZScore, h.StdDev)
	}
	if h.Verdict != VerdictNormal {
		t.Errorf("verdict = %s; 0.69 against 0.70 is not a loss", h.Verdict)
	}
}

func TestThePriorWindowsAreTheSameCalendarWindowInEarlierYears(t *testing.T) {
	// Not "365 days back". The question is what this land does at this point
	// in its cycle, and a monsoon does not shift by leap days.
	eng := &fakeEngine{fallback: series("2020-06-15", 0.7, 0.6)}
	s := testStore(t, eng)

	if _, err := s.ParcelHistory(context.Background(), testGeom, "F1",
		"2026-06-15", "2026-08-14", 0.5); err != nil {
		t.Fatal(err)
	}

	want := map[string]string{
		"2025-06-15": "2025-08-14",
		"2024-06-15": "2024-08-14",
		"2023-06-15": "2023-08-14",
	}
	if len(eng.calls) != 3 {
		t.Fatalf("made %d calls, want 3", len(eng.calls))
	}
	for _, c := range eng.calls {
		end, ok := want[c.SowingDate]
		if !ok {
			t.Errorf("unexpected window starting %s", c.SowingDate)
			continue
		}
		if c.IncidentDate != end {
			t.Errorf("window %s ends %s, want %s", c.SowingDate, c.IncidentDate, end)
		}
	}
}

// ---------------------------------------------------------------------------
// A2 -- the parcel against its neighbours
// ---------------------------------------------------------------------------

func TestAFallSharedWithTheNeighboursCorroboratesTheClaim(t *testing.T) {
	// The strongest thing this system can say without a photograph: the event
	// is visible on ground the claimant does not control.
	eng := &fakeEngine{fallback: series("2026-06-15", 0.70, 0.35)} // ~50% fall
	s := testStore(t, eng)

	p, err := s.Neighbourhood(context.Background(), 18.51, 73.81,
		"2026-06-15", "2026-08-14", 55)
	if err != nil {
		t.Fatal(err)
	}
	if p.Verdict != PeerRegional {
		t.Errorf("verdict = %s, want regional_event (%s)", p.Verdict, p.Reason)
	}
	if p.Reason == "" {
		t.Error("no reason an officer could read")
	}
}

func TestAFallNobodyElseHadIsFlaggedWithoutBeingCalledFalse(t *testing.T) {
	// A genuinely localised loss and a misreported one look identical from
	// orbit. Saying which would be inventing a finding.
	eng := &fakeEngine{fallback: series("2026-06-15", 0.70, 0.68)} // no fall
	s := testStore(t, eng)

	p, err := s.Neighbourhood(context.Background(), 18.51, 73.81,
		"2026-06-15", "2026-08-14", 60)
	if err != nil {
		t.Fatal(err)
	}
	if p.Verdict != PeerLocal {
		t.Fatalf("verdict = %s, want local_only", p.Verdict)
	}
	for _, word := range []string{"fraud", "false claim", "fabricat"} {
		if strings.Contains(strings.ToLower(p.Reason), word) {
			t.Errorf("the reason accuses rather than reports: %s", p.Reason)
		}
	}
	if !strings.Contains(p.Reason, "reason to look") {
		t.Errorf("the reason does not frame itself as a trigger: %s", p.Reason)
	}
}

func TestNeighboursFallingWhileThisParcelDidNotIsReported(t *testing.T) {
	eng := &fakeEngine{fallback: series("2026-06-15", 0.70, 0.30)}
	s := testStore(t, eng)

	p, err := s.Neighbourhood(context.Background(), 18.51, 73.81,
		"2026-06-15", "2026-08-14", 5)
	if err != nil {
		t.Fatal(err)
	}
	if p.Verdict != PeerContradicted {
		t.Errorf("verdict = %s", p.Verdict)
	}
	if p.RelativeAnomaly >= 0 {
		t.Errorf("relative anomaly = %v; this parcel fared better, so it should "+
			"be negative", p.RelativeAnomaly)
	}
}

func TestACloudedNeighbourhoodIsInconclusiveRatherThanZero(t *testing.T) {
	eng := &fakeEngine{fallback: []satellite.NDVIPoint{{Date: "2026-07-01", NDVI: 0.6}}}
	s := testStore(t, eng)

	p, err := s.Neighbourhood(context.Background(), 18.51, 73.81,
		"2026-06-15", "2026-08-14", 55)
	if err != nil {
		t.Fatal(err)
	}
	if p.Verdict != PeerInconclusive {
		t.Errorf("verdict = %s with one pass over the area", p.Verdict)
	}
	if p.Reason == "" {
		t.Error("no reason given")
	}
}

func TestTheNeighbourhoodIsAlwaysCaveatedAsAnAreaMean(t *testing.T) {
	// It includes roads, boundaries and other people's crops. Read as a
	// quantity to subtract it would be badly wrong.
	eng := &fakeEngine{fallback: series("2026-06-15", 0.70, 0.35)}
	s := testStore(t, eng)

	p, err := s.Neighbourhood(context.Background(), 18.51, 73.81,
		"2026-06-15", "2026-08-14", 55)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Caveats) == 0 {
		t.Fatal("no caveat on an area mean")
	}
}

func TestNeighboursShareOneAnalysisBetweenThem(t *testing.T) {
	// The cost argument in one test. Four hundred farmers filing against the
	// same square kilometre after a hailstorm must not buy four hundred
	// identical analyses.
	eng := &fakeEngine{fallback: series("2026-06-15", 0.70, 0.35)}
	s := testStore(t, eng)

	// Points scattered across roughly a kilometre.
	coords := [][2]float64{
		{18.505, 73.805}, {18.507, 73.808}, {18.503, 73.802},
		{18.509, 73.809}, {18.506, 73.804},
	}
	keys := map[string]bool{}
	for _, c := range coords {
		p, err := s.Neighbourhood(context.Background(), c[0], c[1],
			"2026-06-15", "2026-08-14", 55)
		if err != nil {
			t.Fatal(err)
		}
		keys[p.GridCell] = true
	}

	if len(keys) != 1 {
		t.Errorf("five nearby parcels produced %d grid cells: %v", len(keys), keys)
	}
}

func TestALocalityIsAlwaysTheSameSquare(t *testing.T) {
	// The key must be stable, or the cache never hits.
	a, ga := neighbourhood(18.5061, 73.8043, 3000)
	b, gb := neighbourhood(18.5064, 73.8041, 3000)
	if a != b {
		t.Errorf("two points 40 m apart snapped to %s and %s", a, b)
	}
	if string(ga.Coordinates) != string(gb.Coordinates) {
		t.Error("the same cell produced two different polygons")
	}
}

func TestFarApartParcelsDoNotShareANeighbourhood(t *testing.T) {
	a, _ := neighbourhood(18.50, 73.80, 3000)
	b, _ := neighbourhood(19.90, 75.30, 3000) // Aurangabad, not Pune
	if a == b {
		t.Errorf("two districts share the cell %s", a)
	}
}

func TestTheNeighbourhoodPolygonIsAClosedRingInLonLat(t *testing.T) {
	// The engine rejects latitude-first with LATLON_SWAPPED, which would fail
	// every peer baseline in the system.
	_, g := neighbourhood(18.5, 73.8, 3000)
	if g.Type != "Polygon" {
		t.Errorf("type = %s", g.Type)
	}
	var ring [][][2]float64
	if err := json.Unmarshal(g.Coordinates, &ring); err != nil {
		t.Fatalf("coordinates do not decode: %v", err)
	}
	if len(ring) != 1 || len(ring[0]) != 5 {
		t.Fatalf("ring has %d positions, want a closed 5", len(ring[0]))
	}
	if ring[0][0] != ring[0][4] {
		t.Error("the ring is not closed")
	}
	for _, pos := range ring[0] {
		if pos[0] < 68 || pos[0] > 98 {
			t.Errorf("longitude %v is outside India; positions may be swapped", pos[0])
		}
		if pos[1] < 6 || pos[1] > 38 {
			t.Errorf("latitude %v is outside India", pos[1])
		}
	}
}

func TestALongitudeDegreeIsNarrowerFurtherNorth(t *testing.T) {
	// Ignoring the cosine would make a neighbourhood in Nagpur a different
	// size from one in Sindhudurg, and the comparison would silently change
	// meaning with latitude.
	width := func(lat float64) float64 {
		_, g := neighbourhood(lat, 75, 3000)
		var ring [][][2]float64
		_ = json.Unmarshal(g.Coordinates, &ring)
		return ring[0][1][0] - ring[0][0][0]
	}
	south, north := width(16), width(21)
	if north <= south {
		t.Errorf("the cell spans %v degrees at 16N and %v at 21N; it should be "+
			"wider in degrees further north", south, north)
	}
}

// ---------------------------------------------------------------------------
// The cache
// ---------------------------------------------------------------------------

func requireDB(t *testing.T, s *Store) {
	t.Helper()
	if s.pool == nil {
		t.Skip("set ESURVEY_TEST_DATABASE_URL to run the cache tests")
	}
}

func TestASettledWindowIsBoughtOnce(t *testing.T) {
	// The whole cost argument for prior seasons: an archive window from three
	// years ago cannot change, so paying for it twice buys nothing.
	eng := &fakeEngine{fallback: series("2023-06-15", 0.70, 0.60)}
	s := testStore(t, eng)
	requireDB(t, s)

	for i := 0; i < 3; i++ {
		if _, err := s.ndvi(context.Background(), "ndvi_selftest", "testkey",
			satellite.NDVIRequest{
				Geometry: testGeom, SowingDate: "2023-06-15", IncidentDate: "2023-08-14",
			}); err != nil {
			t.Fatal(err)
		}
	}

	if eng.count() != 1 {
		t.Errorf("bought %d analyses for one settled window", eng.count())
	}
}

func TestWithoutADatabaseTheEngineIsStillAsked(t *testing.T) {
	// A cache failure should cost money, not correctness.
	eng := &fakeEngine{fallback: series("2023-06-15", 0.70, 0.60)}
	s := NewStore(nil, eng, quiet())

	for i := 0; i < 2; i++ {
		if _, err := s.ndvi(context.Background(), "ndvi_selftest", "k",
			satellite.NDVIRequest{
				Geometry: testGeom, SowingDate: "2023-06-15", IncidentDate: "2023-08-14",
			}); err != nil {
			t.Fatal(err)
		}
	}
	if eng.count() != 2 {
		t.Errorf("calls = %d, want 2 with no cache", eng.count())
	}
}

// ---------------------------------------------------------------------------
// What an assessment says it stood on
// ---------------------------------------------------------------------------

func TestAnAssessmentWithNoBaselineSaysSoInWords(t *testing.T) {
	var d *Denominators
	if d.Grounded() {
		t.Error("a nil set of denominators reported itself as grounded")
	}
	if !strings.Contains(d.Summary(), "no baseline") {
		t.Errorf("summary = %q", d.Summary())
	}

	empty := &Denominators{}
	if empty.Grounded() {
		t.Error("an empty set reported itself as grounded")
	}
}

func TestAnInconclusiveBaselineDoesNotCountAsGrounding(t *testing.T) {
	d := &Denominators{
		History: &History{Verdict: VerdictUnknown},
		Peer:    &Peer{Verdict: PeerInconclusive},
	}
	if d.Grounded() {
		t.Error("two inconclusive baselines were counted as grounding a figure")
	}
}

func TestTheSummaryNamesEveryDenominatorItHad(t *testing.T) {
	d := &Denominators{
		History: &History{Verdict: VerdictBelowNormal},
		Peer:    &Peer{Verdict: PeerRegional},
		Yield: &ThresholdYield{
			YieldKgPerHa: 1450, UnitName: "Haveli", UnitLevel: "sub_district",
		},
	}
	if !d.Grounded() {
		t.Fatal("three denominators were not counted as grounding")
	}
	s := d.Summary()
	for _, want := range []string{"below", "surrounding fields fell", "1450", "Haveli"} {
		if !strings.Contains(s, want) {
			t.Errorf("the summary omits %q: %s", want, s)
		}
	}
}

func TestAThresholdYieldAloneGroundsAFigure(t *testing.T) {
	// It is the statutory denominator; nothing else is needed for the figure
	// to mean something.
	d := &Denominators{Yield: &ThresholdYield{YieldKgPerHa: 1200, UnitName: "Pune"}}
	if !d.Grounded() {
		t.Error("an issued threshold yield did not ground the figure")
	}
}

func TestTheSummaryReadsAsASentence(t *testing.T) {
	d := &Denominators{History: &History{Verdict: VerdictNormal}}
	s := d.Summary()
	if !strings.HasSuffix(s, ".") {
		t.Errorf("summary does not end as a sentence: %q", s)
	}
	if strings.Contains(s, ", and") && strings.Count(s, ",") < 1 {
		t.Errorf("summary punctuation is wrong: %q", s)
	}
	fmt.Sscan("") // keep fmt imported for the helpers above
}
