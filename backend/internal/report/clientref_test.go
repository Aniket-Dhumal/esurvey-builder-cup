package report

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// One claim, however many times it arrives.
//
// Database-backed because the guarantee is a unique index inside the insert's
// own transaction -- the one protection that holds even when the process dies
// between committing the report and recording that it did.

func newFiling(t *testing.T, pool *pgxpool.Pool, ref string) New {
	t.Helper()
	var eventID string
	if err := pool.QueryRow(context.Background(),
		`SELECT id FROM survey_events WHERE reference = 'RV-EVT-1'`).Scan(&eventID); err != nil {
		t.Fatalf("finding the fixture event: %v", err)
	}
	// The fixture event covers no area; a filing needs its taluka covered.
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO survey_event_areas (event_id, jurisdiction_id)
		SELECT $1, id FROM jurisdictions WHERE code = 'RV-T1'
		ON CONFLICT DO NOTHING`, eventID); err != nil {
		t.Fatalf("covering the fixture taluka: %v", err)
	}
	return New{
		SurveyEventID:       eventID,
		FarmerID:            "F-RV-1",
		FarmerName:          "Revision Farmer",
		VillageCode:         "RV-V1",
		Season:              Kharif,
		CropYear:            "2026-27",
		LossCategory:        "standing_crop",
		CalamityType:        "hailstorm",
		OccurredAt:          time.Now().Add(-9 * 24 * time.Hour),
		ReportedChannel:     "farmer_app",
		DeclarationAccepted: true,
		FarmerSignatureKind: "thumb",
		DocumentsPromised:   []string{},
		Items: []Item{{
			SurveyNumber: "453", CropName: "Soybean",
			AreaSownHa: 1.2, AreaAffectedHa: 1.0, DamagePercent: 60,
		}},
		ClientRef: ref,
	}
}

func countByRef(t *testing.T, pool *pgxpool.Pool, ref string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM crop_loss_reports WHERE client_ref = $1`, ref).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestAReportSentTwiceIsFiledOnce(t *testing.T) {
	s, pool := testStore(t)
	seedAdjudicated(t, s, pool)
	ctx := context.Background()

	first, err := s.Create(ctx, newFiling(t, pool, "draft-0000000001"))
	if err != nil {
		t.Fatalf("first send: %v", err)
	}
	if first.Replayed {
		t.Fatal("the first send was marked a replay")
	}

	again, err := s.Create(ctx, newFiling(t, pool, "draft-0000000001"))
	if err != nil {
		t.Fatalf("resend: %v", err)
	}
	if !again.Replayed || again.ID != first.ID || again.Reference != first.Reference {
		t.Fatalf("the resend filed %s (%s, replayed=%v); want the original %s",
			again.ID, again.Reference, again.Replayed, first.ID)
	}
	if n := countByRef(t, pool, "draft-0000000001"); n != 1 {
		t.Fatalf("%d reports for one filing sent twice", n)
	}
}

// A retry of a report that arrived on time must not be refused because the
// retry itself arrives after the window closed.
func TestARetryAfterTheDeadlineStillFindsTheReport(t *testing.T) {
	s, pool := testStore(t)
	seedAdjudicated(t, s, pool)
	ctx := context.Background()

	first, err := s.Create(ctx, newFiling(t, pool, "draft-0000000002"))
	if err != nil {
		t.Fatal(err)
	}

	// The window closes while the phone is out of signal.
	if _, err := pool.Exec(ctx, `
		UPDATE survey_events SET survey_closes_at = now() - interval '1 day'
		 WHERE reference = 'RV-EVT-1'`); err != nil {
		t.Fatal(err)
	}

	again, err := s.Create(ctx, newFiling(t, pool, "draft-0000000002"))
	if err != nil {
		t.Fatalf("a retry of an on-time report was refused after the deadline: %v", err)
	}
	if again.ID != first.ID {
		t.Fatal("the retry did not find the report")
	}

	// A new filing after the deadline is still refused.
	if _, err := s.Create(ctx, newFiling(t, pool, "draft-0000000099")); err == nil {
		t.Fatal("a new report after the deadline was accepted")
	}
}

// The foreground timer and the background worker racing: one report.
func TestConcurrentSendsOfOneReportFileOne(t *testing.T) {
	s, pool := testStore(t)
	seedAdjudicated(t, s, pool)

	const copies = 16
	ids := make([]string, copies)
	errs := make([]error, copies)
	var wg sync.WaitGroup
	for i := 0; i < copies; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r, err := s.Create(context.Background(), newFiling(t, pool, "draft-0000000003"))
			errs[i] = err
			if r != nil {
				ids[i] = r.ID
			}
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("send %d: %v", i, err)
		}
	}
	for _, id := range ids {
		if id != ids[0] {
			t.Fatalf("concurrent sends filed different reports: %v", ids)
		}
	}
	if n := countByRef(t, pool, "draft-0000000003"); n != 1 {
		t.Fatalf("%d reports from %d concurrent sends of one filing", n, copies)
	}
}

// Two genuinely different claims from the same farmer stay two claims, and a
// report filed without a reference -- at a counter -- is never deduplicated.
func TestDifferentReferencesAreDifferentReports(t *testing.T) {
	s, pool := testStore(t)
	seedAdjudicated(t, s, pool)
	ctx := context.Background()

	// Different claims, so different parcels: one loss per parcel per event
	// is enforced at filing, and two drafts about the same field are the
	// duplicate that rule exists to refuse.
	first := newFiling(t, pool, "draft-0000000004")
	first.Items[0].SurveyNumber = "ref-a"
	a, err := s.Create(ctx, first)
	if err != nil {
		t.Fatal(err)
	}
	second := newFiling(t, pool, "draft-0000000005")
	second.Items[0].SurveyNumber = "ref-b"
	b, err := s.Create(ctx, second)
	if err != nil {
		t.Fatal(err)
	}
	if a.ID == b.ID {
		t.Fatal("two different filings were merged")
	}

	// Filed at a counter, with no client reference: never deduplicated by
	// reference, and still one claim per parcel.
	counterOne := newFiling(t, pool, "")
	counterOne.Items[0].SurveyNumber = "ref-c"
	c, err := s.Create(ctx, counterOne)
	if err != nil {
		t.Fatal(err)
	}
	counterTwo := newFiling(t, pool, "")
	counterTwo.Items[0].SurveyNumber = "ref-d"
	d, err := s.Create(ctx, counterTwo)
	if err != nil {
		t.Fatal(err)
	}
	if c.ID == d.ID || c.Replayed || d.Replayed {
		t.Fatal("reports with no reference were deduplicated")
	}
}
