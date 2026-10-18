package report

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/agribid/esurvey-platform/backend/internal/platform/page"
	"github.com/agribid/esurvey-platform/backend/internal/rbac"
)

// Filing at the scale of a district-wide event.
//
// The morning after a hailstorm every handset in the district regains signal
// within the same hour. These tests file the way that morning does: many
// different farmers, at once, into one district.

// Different claims filed at the same moment in one district must all land.
//
// References used to be count(*)+1 and collided: 10 of 16 of these were
// refused as invalid (docs/SYNC_SCALE_REVIEW.md, C1).
func TestDistinctFilingsAtOnceInOneDistrictAllLand(t *testing.T) {
	s, pool := testStore(t)
	seedAdjudicated(t, s, pool)

	const farmers = 32
	errs := make([]error, farmers)
	refs := make([]string, farmers)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < farmers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			in := newFiling(t, pool, fmt.Sprintf("draft-scale-%06d", i))
			// Distinct filings means distinct parcels. Thirty-two claims on
			// one field is not throughput, it is the duplicate the office
			// now refuses.
			in.Items[0].SurveyNumber = fmt.Sprintf("scale-%06d", i)
			r, err := s.Create(context.Background(), in)
			errs[i] = err
			if r != nil {
				refs[i] = r.Reference
			}
		}(i)
	}
	close(start)
	wg.Wait()

	failed := 0
	for i, err := range errs {
		if err != nil {
			failed++
			t.Errorf("filing %d was refused: %v", i, err)
		}
	}
	seen := map[string]bool{}
	for _, ref := range refs {
		if ref == "" {
			continue
		}
		if seen[ref] {
			t.Errorf("reference %s was issued twice", ref)
		}
		seen[ref] = true
	}
	if failed > 0 {
		t.Fatalf("%d of %d concurrent filings in one district were refused", failed, farmers)
	}
}

// Filing one more report must not cost more because the district already has
// many. Set ESURVEY_SCALE=1: it seeds a district-sized table.
func TestFilingCostDoesNotGrowWithTheDistrict(t *testing.T) {
	if os.Getenv("ESURVEY_SCALE") == "" {
		t.Skip("set ESURVEY_SCALE=1 to seed a district-sized table")
	}
	s, pool := testStore(t)
	seedAdjudicated(t, s, pool)
	ctx := context.Background()

	timeOne := func(label string, i int) time.Duration {
		begin := time.Now()
		if _, err := s.Create(ctx, newFiling(t, pool, fmt.Sprintf("draft-cost-%s-%d", label, i))); err != nil {
			t.Fatalf("filing (%s): %v", label, err)
		}
		return time.Since(begin)
	}

	var small time.Duration
	for i := 0; i < 5; i++ {
		small += timeOne("small", i)
	}
	small /= 5

	// A district's season: copies of the fixture report under generated
	// references.
	const volume = 200000
	seedStart := time.Now()
	if _, err := pool.Exec(ctx, `
		INSERT INTO crop_loss_reports (
			reference, survey_event_id, farmer_id, farmer_name,
			village_lgd_code, village_name, sub_district_lgd_code,
			sub_district_name, district_lgd_code, district_name,
			season, crop_year, loss_category, calamity_type, occurred_at,
			reported_channel, declaration_accepted, documents_promised,
			farmer_signature_kind, farmer_signed_at, state, client_ref)
		SELECT 'RV-SEED-' || g, r.survey_event_id, 'F-RV-1', r.farmer_name,
		       r.village_lgd_code, r.village_name, r.sub_district_lgd_code,
		       r.sub_district_name, r.district_lgd_code, r.district_name,
		       r.season, r.crop_year, r.loss_category, r.calamity_type,
		       r.occurred_at, r.reported_channel, r.declaration_accepted,
		       r.documents_promised, r.farmer_signature_kind,
		       r.farmer_signed_at, r.state, 'seed-client-' || g
		  FROM generate_series(1, $1) g,
		       (SELECT * FROM crop_loss_reports
		         WHERE client_ref = 'draft-cost-small-0') r`, volume); err != nil {
		t.Fatalf("seeding: %v", err)
	}
	_, _ = pool.Exec(ctx, `ANALYZE crop_loss_reports`)
	t.Logf("seeded %d reports in %s", volume, time.Since(seedStart).Round(time.Millisecond))

	var large time.Duration
	for i := 0; i < 5; i++ {
		large += timeOne("large", i)
	}
	large /= 5

	t.Logf("one filing: %s with a near-empty district, %s with %d reports",
		small.Round(time.Microsecond), large.Round(time.Microsecond), volume)
	if large > 10*small && large > 50*time.Millisecond {
		t.Errorf("filing slowed %.0fx as the district grew", float64(large)/float64(small))
	}
}

// Filing must not wait on the rollup queue.
//
// The queue's mark was ON CONFLICT DO NOTHING on (event, village), and Postgres
// makes the second inserter wait for the first transaction before it can know
// whether it conflicts -- so two farmers filing in one village serialised on
// it for the length of the whole filing (docs/SYNC_SCALE_REVIEW.md, M9).
func TestOneVillagesFilingsDoNotWaitOnEachOther(t *testing.T) {
	s, pool := testStore(t)
	f := seedAdjudicated(t, s, pool)
	ctx := context.Background()

	var event, village string
	if err := pool.QueryRow(ctx,
		`SELECT survey_event_id, village_lgd_code FROM crop_loss_reports WHERE id = $1`,
		f.reportID).Scan(&event, &village); err != nil {
		t.Fatal(err)
	}

	// One filing in progress: its mark is written and its transaction open.
	held, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = held.Rollback(context.Background()) }()
	if _, err := held.Exec(ctx,
		`INSERT INTO report_rollup_dirty (survey_event_id, village_lgd_code)
		 VALUES ($1, $2)`, event, village); err != nil {
		t.Fatal(err)
	}

	// A second filing in the same village, with no patience at all.
	second, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = second.Rollback(context.Background()) }()
	if _, err := second.Exec(ctx, `SET LOCAL lock_timeout = '500ms'`); err != nil {
		t.Fatal(err)
	}
	if _, err := second.Exec(ctx,
		`INSERT INTO report_rollup_dirty (survey_event_id, village_lgd_code)
		 VALUES ($1, $2)`, event, village); err != nil {
		t.Fatalf("a second filing in the same village waited on the first: %v", err)
	}

	// Both marks are there, and the sweeper treats them as one piece of work.
	if err := second.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := held.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	var marks int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM report_rollup_dirty
		 WHERE survey_event_id = $1 AND village_lgd_code = $2`,
		event, village).Scan(&marks); err != nil {
		t.Fatal(err)
	}
	if marks < 2 {
		t.Fatalf("%d marks; both filings should have left one", marks)
	}
	drainRollups(t, s, func() bool {
		var left int
		_ = pool.QueryRow(ctx, `
			SELECT count(*) FROM report_rollup_dirty
			 WHERE survey_event_id = $1 AND village_lgd_code = $2`,
			event, village).Scan(&left)
		return left == 0
	})
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM report_rollup_dirty
		 WHERE survey_event_id = $1 AND village_lgd_code = $2`,
		event, village).Scan(&marks); err != nil {
		t.Fatal(err)
	}
	if marks != 0 {
		rows, _ := pool.Query(ctx, `
			SELECT marked_at, now() FROM report_rollup_dirty
			 WHERE survey_event_id = $1 AND village_lgd_code = $2`, event, village)
		for rows.Next() {
			var at, now time.Time
			_ = rows.Scan(&at, &now)
			t.Logf("  left: marked_at=%s now=%s", at, now)
		}
		rows.Close()
		var pairs int
		_ = pool.QueryRow(ctx, `SELECT count(*) FROM (
			SELECT DISTINCT survey_event_id, village_lgd_code
			  FROM report_rollup_dirty) q`).Scan(&pairs)
		t.Fatalf("%d marks left after the sweep (%d distinct pairs queued)", marks, pairs)
	}
}

// What an officer's screen costs when the district has had a season.
//
// The listing is the screen a taluka office lives in: a page of reports, a
// total beside the pager, and a search box. Set ESURVEY_SCALE=1.
func TestOfficerListingAtDistrictScale(t *testing.T) {
	if os.Getenv("ESURVEY_SCALE") == "" {
		t.Skip("set ESURVEY_SCALE=1 to seed a district-sized table")
	}
	s, pool := testStore(t)
	f := seedAdjudicated(t, s, pool)
	ctx := context.Background()

	var district string
	if err := pool.QueryRow(ctx,
		`SELECT id::text FROM jurisdictions WHERE code = 'RV-D1'`).Scan(&district); err != nil {
		t.Fatal(err)
	}

	const volume = 200000
	if _, err := pool.Exec(ctx, `
		INSERT INTO crop_loss_reports (
			reference, survey_event_id, farmer_id, farmer_name,
			village_lgd_code, village_name, sub_district_lgd_code,
			sub_district_name, district_lgd_code, district_name,
			season, crop_year, loss_category, calamity_type, occurred_at,
			reported_channel, declaration_accepted, documents_promised,
			farmer_signature_kind, farmer_signed_at, state, client_ref)
		SELECT 'RV-LIST-' || g, r.survey_event_id, 'F-RV-' || g,
		       'Farmer ' || g,
		       r.village_lgd_code, r.village_name, r.sub_district_lgd_code,
		       r.sub_district_name, r.district_lgd_code, r.district_name,
		       r.season, r.crop_year, r.loss_category, r.calamity_type,
		       r.occurred_at, r.reported_channel, r.declaration_accepted,
		       r.documents_promised, r.farmer_signature_kind,
		       r.farmer_signed_at, r.state, 'list-client-' || g
		  FROM generate_series(1, $1) g,
		       (SELECT * FROM crop_loss_reports WHERE id = $2) r`,
		volume, f.reportID); err != nil {
		t.Fatalf("seeding: %v", err)
	}
	// Lines too: the listing joins them for each row it returns.
	if _, err := pool.Exec(ctx, `
		INSERT INTO crop_loss_report_items (report_id, line_no, survey_number,
			farm_id, crop_name, area_sown_ha, area_affected_ha, damage_percent)
		SELECT id, 1, '453', 'RV-FARM-1', 'Soybean', 1.2, 1.0, 60
		  FROM crop_loss_reports WHERE reference LIKE 'RV-LIST-%'`); err != nil {
		t.Fatalf("seeding lines: %v", err)
	}
	_, _ = pool.Exec(ctx, `ANALYZE crop_loss_reports`)
	_, _ = pool.Exec(ctx, `ANALYZE crop_loss_report_items`)

	// Where the time goes, when it goes somewhere.
	if os.Getenv("ESURVEY_EXPLAIN") != "" {
		rows, err := pool.Query(ctx, `
			EXPLAIN (ANALYZE, BUFFERS)
			SELECT r.id FROM crop_loss_reports r
			 WHERE ($1::uuid[] IS NOT NULL AND EXISTS (
			     SELECT 1 FROM jurisdictions v
			      WHERE v.level = 'village' AND v.code = r.village_lgd_code
			        AND (v.id = ANY($1) OR v.ancestor_ids && $1)))
			 ORDER BY r.contested DESC, r.reported_at ASC, r.id ASC
			 LIMIT 50`, []string{district})
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var line string
			_ = rows.Scan(&line)
			t.Log("  " + line)
		}
		rows.Close()
	}

	scope := rbac.Scope{JurisdictionIDs: []string{district}}
	measure := func(label string, f Filter) {
		t.Helper()
		var slowest time.Duration
		var got int
		for i := 0; i < 3; i++ {
			began := time.Now()
			res, err := s.List(ctx, f)
			took := time.Since(began)
			if err != nil {
				t.Fatalf("%s: %v", label, err)
			}
			got = len(res.Items)
			if took > slowest {
				slowest = took
			}
		}
		t.Logf("%-28s %s (%d rows)", label, slowest.Round(time.Millisecond), got)
		if slowest > time.Second {
			t.Errorf("%s took %s at %d reports", label, slowest, volume)
		}
	}

	measure("first page", Filter{Scope: scope, Page: page.Params{Limit: 50}})
	measure("page 20", Filter{Scope: scope, Page: page.Params{Limit: 50, Offset: 1000}})
	measure("deepest allowed page", Filter{
		Scope: scope, Page: page.Params{Limit: 50, Offset: page.MaxOffset - 50}})
	measure("search by name", Filter{
		Scope: scope, Page: page.Params{Limit: 50, Query: "Farmer 12345"}})
	measure("search by reference", Filter{
		Scope: scope, Page: page.Params{Limit: 50, Query: "RV-LIST-9999"}})
	measure("the approval queue", Filter{
		Scope: scope, AwaitingLevel: "talathi", Page: page.Params{Limit: 50}})

	// Past the offset ceiling, where a numbered pager gives up. A cursor walks
	// as deep as anyone asks, at the same cost per page.
	seen := map[string]bool{}
	cursor := ""
	var deepest time.Duration
	for pageNo := 0; pageNo < 30; pageNo++ {
		began := time.Now()
		res, err := s.List(ctx, Filter{
			Scope: scope,
			Page:  page.Params{Limit: 50, Cursor: cursor},
		})
		if err != nil {
			t.Fatalf("cursor page %d: %v", pageNo, err)
		}
		if took := time.Since(began); took > deepest {
			deepest = took
		}
		for _, item := range res.Items {
			if seen[item.ID] {
				t.Fatalf("cursor paging returned %s twice", item.ID)
			}
			seen[item.ID] = true
		}
		if res.NextCursor == "" {
			break
		}
		cursor = res.NextCursor
	}
	t.Logf("%-28s %s (%d rows over 30 pages)", "cursor paging",
		deepest.Round(time.Millisecond), len(seen))
	if len(seen) < 1000 {
		t.Fatalf("cursor paging walked %d rows over thirty pages", len(seen))
	}
	if deepest > 500*time.Millisecond {
		t.Errorf("a cursor page took %s; it should not grow with depth", deepest)
	}
}
