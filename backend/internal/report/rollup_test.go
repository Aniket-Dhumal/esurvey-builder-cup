package report

import (
	"context"
	"math"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/agribid/esurvey-platform/backend/internal/rbac"
)

// Database-backed, because everything under test is a trigger, an aggregate
// and an index. The property that matters — an area-weighted percentage that
// stays correct as it is summed upward — is invisible above the pool.

func rollupCount(t *testing.T, pool *pgxpool.Pool, reportID string) (int, float64, float64) {
	t.Helper()
	var reports int
	var affected, num float64
	if err := pool.QueryRow(context.Background(), `
		SELECT COALESCE(sum(ru.reports), 0),
		       COALESCE(sum(ru.area_affected_ha), 0),
		       COALESCE(sum(ru.declared_pct_area), 0)
		  FROM report_rollups ru
		  JOIN crop_loss_reports r ON r.survey_event_id = ru.survey_event_id
		 WHERE r.id = $1`, reportID).Scan(&reports, &affected, &num); err != nil {
		t.Fatalf("reading the rollup: %v", err)
	}
	return reports, affected, num
}

func TestFilingAReportMarksItsRollupDirty(t *testing.T) {
	s, pool := testStore(t)
	f := seedAdjudicated(t, s, pool)
	ctx := context.Background()

	// The seed wrote a report, so the trigger should have marked its key.
	var marked int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM report_rollup_dirty d
		  JOIN crop_loss_reports r ON r.survey_event_id = d.survey_event_id
		                          AND r.village_lgd_code = d.village_lgd_code
		 WHERE r.id = $1`, f.reportID).Scan(&marked); err != nil {
		t.Fatalf("reading the dirty queue: %v", err)
	}
	if marked == 0 {
		t.Fatal("a filed report left no mark for the rollup sweeper")
	}
}

// markDirty queues this report's (event, village) for the sweeper.
//
// The trigger does this on every write and has its own test. Doing it
// explicitly here makes these tests independent of what other suites -- which
// run in parallel against this database -- have queued or already swept.
func markDirty(t *testing.T, pool *pgxpool.Pool, reportID string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO report_rollup_dirty (survey_event_id, village_lgd_code)
		SELECT survey_event_id, village_lgd_code
		  FROM crop_loss_reports WHERE id = $1`, reportID); err != nil {
		t.Fatalf("marking the rollup dirty: %v", err)
	}
}

// drainRollups sweeps until done reports the work this test cares about has
// been recomputed.
//
// One sweep clears a bounded batch of the oldest marks, and this database is
// shared: other suites run in parallel and queue their own villages. A test
// that swept once and asserted would be asserting about their backlog.
func drainRollups(t *testing.T, s *Store, done func() bool) {
	t.Helper()
	ctx := context.Background()
	for i := 0; i < 200; i++ {
		if done() {
			return
		}
		if _, err := s.RefreshRollups(ctx); err != nil {
			t.Fatalf("refreshing: %v", err)
		}
	}
	if !done() {
		t.Fatal("the rollup never appeared after a hundred sweeps")
	}
}

func TestTheSweepBuildsTheRollup(t *testing.T) {
	s, pool := testStore(t)
	f := seedAdjudicated(t, s, pool)
	ctx := context.Background()

	markDirty(t, pool, f.reportID)
	drainRollups(t, s, func() bool {
		reports, _, _ := rollupCount(t, pool, f.reportID)
		return reports > 0
	})

	reports, _, _ := rollupCount(t, pool, f.reportID)
	if reports == 0 {
		t.Fatal("the sweep produced no rollup for a report that exists")
	}

	// And it clears what it did, or the next sweep does the same work again
	// for ever.
	var left int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM report_rollup_dirty d
		  JOIN crop_loss_reports r ON r.survey_event_id = d.survey_event_id
		                          AND r.village_lgd_code = d.village_lgd_code
		 WHERE r.id = $1`, f.reportID).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != 0 {
		t.Fatalf("the sweep left %d marks behind", left)
	}
}

// The reason percentages are stored as numerators: a mean of means is wrong
// whenever the areas differ, and the error is invisible because every figure
// still looks like a plausible percentage.
func TestTheWeightedPercentageIsNotAMeanOfMeans(t *testing.T) {
	s, pool := testStore(t)
	f := seedAdjudicated(t, s, pool)
	ctx := context.Background()

	// One line at 90% over a tenth of a hectare, one at 10% over four.
	// The mean of the two percentages is 50. The truth is far nearer 12.
	if _, err := pool.Exec(ctx, `
		DELETE FROM crop_loss_report_items WHERE report_id = $1`, f.reportID); err != nil {
		t.Fatal(err)
	}
	for i, v := range []struct {
		crop           string
		sown, affected float64
		damage         float64
	}{
		{"Sugarcane", 0.1, 0.1, 90},
		{"Sorghum", 4.0, 4.0, 10},
	} {
		if _, err := pool.Exec(ctx, `
			INSERT INTO crop_loss_report_items (
				report_id, line_no, survey_number, crop_name,
				area_sown_ha, area_affected_ha, damage_percent)
			VALUES ($1, $2, $3, $4, $5, $6, $7)`,
			f.reportID, i+1, "RV/1", v.crop, v.sown, v.affected, v.damage); err != nil {
			t.Fatalf("seeding a line: %v", err)
		}
	}

	markDirty(t, pool, f.reportID)
	drainRollups(t, s, func() bool {
		_, affected, _ := rollupCount(t, pool, f.reportID)
		return affected > 0
	})

	_, affected, num := rollupCount(t, pool, f.reportID)
	if affected == 0 {
		t.Fatal("no affected area in the rollup")
	}
	got := num / affected
	want := (90*0.1 + 10*4.0) / 4.1
	if math.Abs(got-want) > 0.01 {
		t.Fatalf("weighted damage = %.3f, want %.3f", got, want)
	}
	if math.Abs(got-50) < 1 {
		t.Fatal("the rollup averaged the percentages instead of weighting them")
	}
}

// A crop that leaves must not leave its row behind claiming an area nobody
// reports any more.
func TestACropRemovedFromAReportLeavesNoRollupRow(t *testing.T) {
	s, pool := testStore(t)
	f := seedAdjudicated(t, s, pool)
	ctx := context.Background()

	if _, err := pool.Exec(ctx, `
		INSERT INTO crop_loss_report_items (
			report_id, line_no, survey_number, crop_name,
			area_sown_ha, area_affected_ha, damage_percent)
		VALUES ($1, 90, 'RV/9', 'Sugarcane', 1, 1, 50)`, f.reportID); err != nil {
		t.Fatal(err)
	}
	markDirty(t, pool, f.reportID)
	drainRollups(t, s, func() bool {
		var n int
		_ = pool.QueryRow(context.Background(),
			`SELECT count(*) FROM report_rollups WHERE crop_name = 'Sugarcane'`).Scan(&n)
		return n > 0
	})

	var before int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM report_rollups WHERE crop_name = 'Sugarcane'`).
		Scan(&before); err != nil {
		t.Fatal(err)
	}
	if before == 0 {
		t.Fatal("the added crop never reached the rollup")
	}

	if _, err := pool.Exec(ctx,
		`DELETE FROM crop_loss_report_items WHERE report_id = $1 AND line_no = 90`,
		f.reportID); err != nil {
		t.Fatal(err)
	}
	markDirty(t, pool, f.reportID)
	drainRollups(t, s, func() bool {
		var n int
		_ = pool.QueryRow(context.Background(),
			`SELECT count(*) FROM report_rollups WHERE crop_name = 'Sugarcane'`).Scan(&n)
		return n == 0
	})

	var after int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM report_rollups WHERE crop_name = 'Sugarcane'`).
		Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != 0 {
		t.Fatalf("a removed crop left %d rollup rows behind", after)
	}
}

func TestTheStatewideViewReadsTheRollups(t *testing.T) {
	s, pool := testStore(t)
	f := seedAdjudicated(t, s, pool)
	ctx := context.Background()

	markDirty(t, pool, f.reportID)
	drainRollups(t, s, func() bool {
		reports, _, _ := rollupCount(t, pool, f.reportID)
		return reports > 0
	})

	view, err := s.StatewideView(ctx, scopeOf(t, pool, "RV-ST"), "")
	if err != nil {
		t.Fatalf("building the statewide view: %v", err)
	}
	if len(view.Districts) == 0 {
		t.Fatal("no district rows")
	}
	if view.Totals.Reports == 0 {
		t.Fatal("the totals are empty with a report on record")
	}
	if view.Totals.Farmers != 1 {
		t.Fatalf("farmers = %d, want the one household that filed", view.Totals.Farmers)
	}
	if len(view.Filed) != TrendDays {
		t.Fatalf("the trend has %d days, want %d", len(view.Filed), TrendDays)
	}
	if len(view.Events) == 0 {
		t.Fatal("no events listed")
	}
	_ = f
}

// The scope is the whole guarantee. A rollup leak does not show one report
// from the next district — it shows a count that quietly includes it.
func TestTheStatewideViewIsScoped(t *testing.T) {
	s, pool := testStore(t)
	f := seedAdjudicated(t, s, pool)
	ctx := context.Background()
	markDirty(t, pool, f.reportID)

	drainRollups(t, s, func() bool {
		var n int
		_ = pool.QueryRow(context.Background(), `
			SELECT count(*) FROM report_rollups ru
			  JOIN crop_loss_reports r ON r.survey_event_id = ru.survey_event_id
			 WHERE r.farmer_id = 'F-RV-1'`).Scan(&n)
		return n > 0
	})

	empty, err := s.StatewideView(ctx, rbac.Scope{}, "")
	if err != nil {
		t.Fatal(err)
	}
	if empty.Totals.Reports != 0 || len(empty.Districts) != 0 {
		t.Fatal("an empty scope was answered with somebody else's numbers")
	}
}
