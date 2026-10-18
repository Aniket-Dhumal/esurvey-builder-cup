package maharain

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func testStore(t *testing.T) *Store {
	t.Helper()

	url := os.Getenv("ESURVEY_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set ESURVEY_TEST_DATABASE_URL to run the storage tests")
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
		t.Skipf("no test database: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Skipf("no test database: %v", err)
	}
	t.Cleanup(pool.Close)

	// The fixtures are for Solapur in a year no real load will touch, so the
	// rows can be removed without disturbing anything a person loaded.
	clean := func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM state_rainfall WHERE crop_year = 2026
			   AND source_district_code = '12'`)
	}
	clean()
	t.Cleanup(clean)

	return &Store{Pool: pool}
}

func solapurTaluka(t *testing.T) *Report {
	t.Helper()
	r, err := parseMonthly(fixture(t, "tehsil_solapur_2026-09-28.html"))
	if err != nil {
		t.Fatal(err)
	}
	r.Level = LevelTaluka
	r.District = District{DivisionCode: "3", Code: "12", Name: "Solapur"}
	return r
}

func TestATalukaReportIsStoredAndMatchedToItsJurisdiction(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	rep := solapurTaluka(t)

	saved, err := s.Save(ctx, rep)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Rows != len(rep.Units)*len(rep.Periods) {
		t.Errorf("rows = %d, want %d units x %d periods",
			saved.Rows, len(rep.Units), len(rep.Periods))
	}
	// Taluka names come from LGD on both sides and should match exactly. A
	// regression here means the jurisdiction scoping broke, and every figure
	// silently stops reaching an assessment.
	if saved.Matched == 0 {
		t.Fatalf("no taluka matched a jurisdiction; unmatched: %v", saved.Unmatched)
	}

	id := jurisdictionID(t, s, "sub_district", "Barshi")
	if id == "" {
		t.Skip("Barshi is not in this database's jurisdictions")
	}

	got, err := s.ForJurisdiction(ctx, id, 2026)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(rep.Periods) {
		t.Fatalf("read back %d periods, wrote %d", len(got), len(rep.Periods))
	}

	// Months in order, season last.
	for i, want := range []int{6, 7, 8, 9} {
		if got[i].Month == nil || *got[i].Month != want {
			t.Errorf("period %d is not month %d", i, want)
		}
	}
	season := got[len(got)-1]
	if season.Kind != "season_to_date" {
		t.Errorf("last period kind = %q", season.Kind)
	}
	if season.ActualMM != 286.0 || season.NormalMM != 516.6 {
		t.Errorf("season = %.1f of %.1f mm, want 286.0 of 516.6",
			season.ActualMM, season.NormalMM)
	}
	if d := season.Deficit(); d < 44 || d > 45 {
		t.Errorf("season deficit = %.1f points, want about 44.6", d)
	}
}

func TestReloadingReplacesRatherThanDuplicates(t *testing.T) {
	// The reason this runs on a schedule: the current month's figure and the
	// season total change every day. A second run must update, or a month
	// accumulates a row per fetch and every reader sees whichever it happens to
	// pick.
	s := testStore(t)
	ctx := context.Background()

	rep := solapurTaluka(t)
	if _, err := s.Save(ctx, rep); err != nil {
		t.Fatal(err)
	}

	// The same periods, with September's rain grown by a day's fall.
	rep2 := solapurTaluka(t)
	for i := range rep2.Units {
		last := len(rep2.Units[i].Values) - 1
		rep2.Units[i].Values[last].ActualMM += 12.5
	}
	if _, err := s.Save(ctx, rep2); err != nil {
		t.Fatal(err)
	}

	id := jurisdictionID(t, s, "sub_district", "Barshi")
	if id == "" {
		t.Skip("Barshi is not in this database's jurisdictions")
	}
	got, err := s.ForJurisdiction(ctx, id, 2026)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(rep.Periods) {
		t.Fatalf("read back %d rows for %d periods; the second load duplicated",
			len(got), len(rep.Periods))
	}
	if season := got[len(got)-1]; season.ActualMM != 298.5 {
		t.Errorf("season actual = %.1f, want the updated 298.5", season.ActualMM)
	}
}

func TestAnUnmatchedNameIsStoredAndReported(t *testing.T) {
	// The circle report carries names this platform spells differently --
	// Aagalgaon against Agalgaon, Upaledu against Upale Dumala. Those rows are
	// true observations and are kept; what they are not is attached to a
	// jurisdiction. Silently dropping them would lose data, and fuzzy-matching
	// them would attach one circle's deficit to another's farmers.
	s := testStore(t)
	ctx := context.Background()

	rep, err := parseMonthly(fixture(t, "circle_solapur_2026-09-28.html"))
	if err != nil {
		t.Fatal(err)
	}
	rep.Level = LevelCircle
	rep.District = District{DivisionCode: "3", Code: "12", Name: "Solapur"}

	saved, err := s.Save(ctx, rep)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Rows != len(rep.Units)*len(rep.Periods) {
		t.Errorf("rows = %d; every unit should be stored whether or not it "+
			"matched", saved.Rows)
	}
	if len(saved.Unmatched) == 0 {
		t.Skip("this database's circle names all match, so there is nothing " +
			"to report")
	}

	// Every reported name must actually be absent from the match index, not
	// merely have been skipped.
	var stored int
	err = s.Pool.QueryRow(ctx, `
		SELECT count(*) FROM state_rainfall
		 WHERE crop_year = 2026 AND source_district_code = '12'
		   AND unit_level = 'circle' AND jurisdiction_id IS NULL`).Scan(&stored)
	if err != nil {
		t.Fatal(err)
	}
	if stored == 0 {
		t.Error("names were reported unmatched but no row was stored unmatched")
	}
}

func TestAmbiguousNamesInsideOneDistrictAreNotGuessed(t *testing.T) {
	// Solapur has two revenue circles called Jeur, in different talukas. A
	// match by name alone cannot tell them apart, and attaching one's rainfall
	// to the other would be wrong in a way nothing downstream could detect.
	s := testStore(t)
	ctx := context.Background()

	rep, err := parseMonthly(fixture(t, "circle_solapur_2026-09-28.html"))
	if err != nil {
		t.Fatal(err)
	}
	rep.Level = LevelCircle
	rep.District = District{DivisionCode: "3", Code: "12", Name: "Solapur"}
	if _, err := s.Save(ctx, rep); err != nil {
		t.Fatal(err)
	}

	var dupes int
	err = s.Pool.QueryRow(ctx, `
		SELECT count(*) FROM (
		  SELECT lower(name) FROM jurisdictions j
		   JOIN jurisdictions d ON d.id = ANY (j.ancestor_ids)
		  WHERE j.level = 'circle' AND d.level = 'district'
		    AND lower(d.name) = 'solapur'
		  GROUP BY lower(name) HAVING count(*) > 1) x`).Scan(&dupes)
	if err != nil {
		t.Fatal(err)
	}
	if dupes == 0 {
		t.Skip("no duplicated circle name in this database")
	}

	// A name that appears twice must have matched nothing.
	var matched int
	err = s.Pool.QueryRow(ctx, `
		SELECT count(*) FROM state_rainfall r
		 WHERE r.crop_year = 2026 AND r.source_district_code = '12'
		   AND r.unit_level = 'circle' AND r.jurisdiction_id IS NOT NULL
		   AND lower(r.unit_name) IN (
		     SELECT lower(name) FROM jurisdictions j
		      JOIN jurisdictions d ON d.id = ANY (j.ancestor_ids)
		     WHERE j.level = 'circle' AND d.level = 'district'
		       AND lower(d.name) = 'solapur'
		     GROUP BY lower(name) HAVING count(*) > 1)`).Scan(&matched)
	if err != nil {
		t.Fatal(err)
	}
	if matched > 0 {
		t.Errorf("%d rows were matched on a name that occurs twice in the "+
			"district", matched)
	}
}

func TestTheMatchKeyIgnoresOnlyCaseAndSpacing(t *testing.T) {
	// The boundary of what is considered the same name. Widening this is how a
	// wrong mapping gets introduced, so it is pinned.
	same := [][2]string{
		{"Barshi", "barshi"},
		{"Upale  Dumala", "Upale Dumala"},
		{" Kamati Bk. ", "Kamati Bk."},
	}
	for _, p := range same {
		if matchKey(p[0]) != matchKey(p[1]) {
			t.Errorf("%q and %q should match", p[0], p[1])
		}
	}

	different := [][2]string{
		{"Naari", "Nari"},
		{"Aagalgaon", "Agalgaon"},
		{"Upaledu", "Upale Dumala"},
		{"Pangari", "Pangri"},
		{"Khandvi", "Khandavi"},
	}
	for _, p := range different {
		if matchKey(p[0]) == matchKey(p[1]) {
			t.Errorf("%q and %q must NOT be matched automatically; they are a "+
				"reconciliation for somebody with local knowledge", p[0], p[1])
		}
	}
}

func jurisdictionID(t *testing.T, s *Store, level, name string) string {
	t.Helper()
	var id string
	err := s.Pool.QueryRow(context.Background(),
		`SELECT id::text FROM jurisdictions
		  WHERE level = $1::jurisdiction_level AND lower(name) = lower($2)
		  LIMIT 1`, level, name).Scan(&id)
	if err != nil {
		return ""
	}
	return id
}
