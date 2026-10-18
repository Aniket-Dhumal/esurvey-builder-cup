package claims

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/agribid/esurvey-platform/backend/internal/platform/dbtest"
)

// The facts here were always in the database. What is under test is whether
// they are read correctly, and -- just as much -- whether they are stated in a
// way an officer can act on without the record turning into a suspicion score.

func testClaims(t *testing.T) (*Store, *pgxpool.Pool) {
	t.Helper()

	url := os.Getenv("ESURVEY_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set ESURVEY_TEST_DATABASE_URL to run the claim history tests")
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

	// Before anything else, so its release runs after the cleanup below.
	// clean() lifts the append-only trigger table-wide, which every other
	// suite sharing this database can see; see internal/platform/dbtest.
	dbtest.Serialise(t, pool)

	clean := func() {
		c := context.Background()

		// Verifications are append-only: a trigger refuses UPDATE and DELETE
		// on them, which is deliberate -- a finding an officer can revise
		// after seeing their colleague's is not an independent finding. It
		// also means a report cannot be cascade-deleted while it has any, so
		// the fixture lifts the trigger for exactly as long as it takes to
		// clear its own rows.
		//
		// Nothing in the application ever does this. Retention anonymises
		// rather than deletes, for the same reason the trigger exists.
		_, _ = pool.Exec(c, `ALTER TABLE report_verifications DISABLE TRIGGER report_verifications_no_rewrite`)
		defer func() {
			_, _ = pool.Exec(c, `ALTER TABLE report_verifications ENABLE TRIGGER report_verifications_no_rewrite`)
		}()

		// Reports cascade to items and verifications; events and
		// jurisdictions have to go after them.
		_, _ = pool.Exec(c, `DELETE FROM crop_loss_reports WHERE reference LIKE 'CH-%'`)
		_, _ = pool.Exec(c, `DELETE FROM survey_events WHERE reference LIKE 'SE-%-CH-%'`)
		_, _ = pool.Exec(c, `DELETE FROM users WHERE username LIKE 'ch.%'`)
		// Deepest level first. jurisdictions.parent_id restricts deletes, so a
		// single statement can try to remove a state before its district and
		// leave the whole fixture behind for the next test to collide with.
		for _, level := range []string{
			"village", "circle", "sub_district", "district", "state",
		} {
			_, _ = pool.Exec(c, `
				DELETE FROM jurisdictions
				WHERE code LIKE 'CH-%' AND level = $1::jurisdiction_level`, level)
		}
	}
	clean()
	t.Cleanup(clean)

	return NewStore(pool), pool
}

const (
	chVillageA = "CH-V1"
	chVillageB = "CH-V2"
	chTaluka   = "CH-T1"
	chDistrict = "CH-D1"
)

type fixture struct {
	pool     *pgxpool.Pool
	eventID  string
	officer  string
	villages map[string]string
	n        int
}

func seed(t *testing.T, pool *pgxpool.Pool) *fixture {
	t.Helper()
	ctx := context.Background()

	rows := []struct{ level, code, name, parent string }{
		{"state", "CH-ST", "History State", ""},
		{"district", chDistrict, "History District", "CH-ST"},
		{"sub_district", chTaluka, "History Taluka", chDistrict},
		{"village", chVillageA, "Wadgaon", chTaluka},
		{"village", chVillageB, "Nimgaon", chTaluka},
	}
	ids := map[string]string{}
	for _, r := range rows {
		var parent any
		if r.parent != "" {
			parent = ids[r.parent]
		}
		var id string
		if err := pool.QueryRow(ctx, `
			INSERT INTO jurisdictions (level, code, name, parent_id)
			VALUES ($1::jurisdiction_level, $2, $3, $4::uuid)
			RETURNING id::text`, r.level, r.code, r.name, parent).Scan(&id); err != nil {
			t.Fatalf("seeding %s: %v", r.code, err)
		}
		ids[r.code] = id
	}

	var officer string
	if err := pool.QueryRow(ctx, `
		INSERT INTO users (kind, full_name, username, email, password_hash)
		VALUES ('officer', 'History Officer', 'ch.officer@esurvey-test.in', 'ch.officer@esurvey-test.in', 'x')
		RETURNING id::text`).Scan(&officer); err != nil {
		t.Fatalf("seeding the officer: %v", err)
	}

	var eventID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO survey_events (
			reference, calamity_type, title, district_id,
			incident_start, incident_end, survey_closes_at, state,
			declared_by, declared_at
		) VALUES (
			'SE-2026-CH-001', 'hailstorm', 'History event', $1::uuid,
			'2026-03-01', '2026-03-05', now() + interval '30 days', 'open',
			$2::uuid, now()
		) RETURNING id::text`, ids[chDistrict], officer).Scan(&eventID); err != nil {
		t.Fatalf("seeding the event: %v", err)
	}

	return &fixture{pool: pool, eventID: eventID, officer: officer, villages: ids}
}

type report struct {
	farmerID string
	village  string
	farmID   string
	survey   string
	year     string
	calamity string
	occurred string
	percent  float64
	outcome  string // "" for none
}

func (f *fixture) file(t *testing.T, r report) string {
	t.Helper()
	ctx := context.Background()
	f.n++

	if r.village == "" {
		r.village = chVillageA
	}
	if r.year == "" {
		r.year = "2026-27"
	}
	if r.calamity == "" {
		r.calamity = "hailstorm"
	}
	if r.occurred == "" {
		r.occurred = "2026-03-02"
	}
	if r.survey == "" {
		r.survey = "42"
	}

	name := "Wadgaon"
	if r.village == chVillageB {
		name = "Nimgaon"
	}

	var id string
	err := f.pool.QueryRow(ctx, `
		INSERT INTO crop_loss_reports (
			reference, survey_event_id, farmer_id, farmer_name,
			village_lgd_code, village_name,
			sub_district_lgd_code, sub_district_name,
			district_lgd_code, district_name,
			season, crop_year, loss_category, calamity_type, occurred_at,
			reported_channel, state, declaration_accepted,
			farmer_signature_kind, farmer_signed_at
		) VALUES (
			$1, $2::uuid, $3, 'History Farmer',
			$4, $5, $6, 'History Taluka', $7, 'History District',
			'kharif', $8, 'standing_crop', $9::calamity_type, $10::timestamptz,
			'farmer_app', 'submitted', true,
			-- A submitted report carries the farmer's mark. The schema refuses
			-- one without it, which is the constraint doing its job.
			'thumb', now()
		) RETURNING id::text`,
		fmt.Sprintf("CH-%04d", f.n), f.eventID, r.farmerID,
		r.village, name, chTaluka, chDistrict,
		r.year, r.calamity, r.occurred).Scan(&id)
	if err != nil {
		t.Fatalf("filing a report: %v", err)
	}

	var farmID any
	if r.farmID != "" {
		farmID = r.farmID
	}
	if _, err := f.pool.Exec(ctx, `
		INSERT INTO crop_loss_report_items (
			report_id, line_no, survey_number, farm_id, crop_name,
			area_sown_ha, area_affected_ha, damage_percent
		) VALUES ($1::uuid, 1, $2, $3, 'Soybean', 2.0, 1.5, $4)`,
		id, r.survey, farmID, r.percent); err != nil {
		t.Fatalf("adding a line: %v", err)
	}

	if r.outcome != "" {
		var pct any
		if r.outcome == "confirmed" || r.outcome == "partly_confirmed" {
			pct = r.percent
		}
		if _, err := f.pool.Exec(ctx, `
			INSERT INTO report_verifications (
				report_id, officer_id, role, tier, outcome,
				damage_percent, findings
			) VALUES ($1::uuid, $2::uuid, 'talathi', 'village_pair', $3,
			          $4, 'Recorded during the history fixture setup.')`,
			id, f.officer, r.outcome, pct); err != nil {
			t.Fatalf("recording a verification: %v", err)
		}
	}

	return id
}

// ---------------------------------------------------------------------------
// The parcel
// ---------------------------------------------------------------------------

func TestEarlierClaimsOnTheSameParcelAreFound(t *testing.T) {
	s, pool := testClaims(t)
	f := seed(t, pool)

	f.file(t, report{farmerID: "F1", farmID: "FARM-1", year: "2023-24",
		occurred: "2023-08-10", percent: 40, outcome: "confirmed"})
	f.file(t, report{farmerID: "F1", farmID: "FARM-1", year: "2024-25",
		occurred: "2024-07-22", percent: 55, outcome: "confirmed"})

	h, err := s.For(context.Background(), Lookup{
		FarmerID: "F1", FarmID: "FARM-1", VillageCode: chVillageA,
		EventID: f.eventID, DeclaredPercent: 60,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(h.Parcel.Claims) != 2 {
		t.Fatalf("found %d prior claims on the parcel, want 2", len(h.Parcel.Claims))
	}
	if h.Parcel.DistinctYears != 2 {
		t.Errorf("distinct years = %d, want 2", h.Parcel.DistinctYears)
	}
	if h.Parcel.Confirmed != 2 {
		t.Errorf("confirmed = %d, want 2", h.Parcel.Confirmed)
	}
	// Newest first: an officer reads the recent history, not the oldest.
	if h.Parcel.Claims[0].CropYear != "2024-25" {
		t.Errorf("first claim is %s; the list is not newest first",
			h.Parcel.Claims[0].CropYear)
	}
}

func TestAParcelWithNoRegistryLinkIsMatchedBySurveyNumberInItsVillage(t *testing.T) {
	// The ordinary case while AgriStack returns no geometry for anybody. A
	// look-up that only matched farm_id would find no history for almost
	// every real report.
	s, pool := testClaims(t)
	f := seed(t, pool)

	f.file(t, report{farmerID: "F2", survey: "77", village: chVillageA,
		year: "2024-25", occurred: "2024-07-01", percent: 30})

	h, err := s.For(context.Background(), Lookup{
		FarmerID: "F2", SurveyNumber: "77", VillageCode: chVillageA,
		EventID: f.eventID, DeclaredPercent: 50,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(h.Parcel.Claims) != 1 {
		t.Errorf("found %d claims by survey number, want 1", len(h.Parcel.Claims))
	}
}

func TestTheSameSurveyNumberInAnotherVillageIsADifferentParcel(t *testing.T) {
	// Survey number 42 exists in every village in Maharashtra. Matching on it
	// alone would attach one farmer's history to another's land.
	s, pool := testClaims(t)
	f := seed(t, pool)

	f.file(t, report{farmerID: "OTHER", survey: "42", village: chVillageB,
		year: "2024-25", occurred: "2024-07-01", percent: 80})

	h, err := s.For(context.Background(), Lookup{
		FarmerID: "F3", SurveyNumber: "42", VillageCode: chVillageA,
		EventID: f.eventID, DeclaredPercent: 50,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(h.Parcel.Claims) != 0 {
		t.Errorf("survey 42 in another village was counted as this parcel's "+
			"history: %+v", h.Parcel.Claims)
	}
}

func TestTheReportBeingAssessedIsNotItsOwnHistory(t *testing.T) {
	s, pool := testClaims(t)
	f := seed(t, pool)

	id := f.file(t, report{farmerID: "F4", farmID: "FARM-4", percent: 60})

	h, err := s.For(context.Background(), Lookup{
		ReportID: id, FarmerID: "F4", FarmID: "FARM-4",
		VillageCode: chVillageA, EventID: f.eventID, DeclaredPercent: 60,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(h.Parcel.Claims) != 0 || len(h.Farmer.Claims) != 0 {
		t.Errorf("the report under assessment appeared in its own history: "+
			"parcel %d, farmer %d", len(h.Parcel.Claims), len(h.Farmer.Claims))
	}
}

func TestTheLatestVerificationIsTheOneThatStands(t *testing.T) {
	// A report verified at village level and then arbitrated shows the
	// arbitration. Showing the first would report an outcome that was
	// overturned.
	s, pool := testClaims(t)
	f := seed(t, pool)

	id := f.file(t, report{farmerID: "F5", farmID: "FARM-5", year: "2024-25",
		occurred: "2024-07-01", percent: 70, outcome: "rejected"})

	// A later arbitration, at a different tier.
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO report_verifications (
			report_id, officer_id, role, tier, outcome,
			damage_percent, findings, submitted_at
		) VALUES ($1::uuid, $2::uuid, 'tehsildar', 'arbitration',
		          'partly_confirmed', 45,
		          'Arbitrated after the village pair disagreed.',
		          now() + interval '1 hour')`,
		id, f.officer); err != nil {
		t.Fatal(err)
	}

	h, err := s.For(context.Background(), Lookup{
		FarmerID: "F5", FarmID: "FARM-5", VillageCode: chVillageA,
		EventID: f.eventID, DeclaredPercent: 60,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(h.Parcel.Claims) != 1 {
		t.Fatalf("claims = %d", len(h.Parcel.Claims))
	}
	if got := h.Parcel.Claims[0].Outcome; got != "partly_confirmed" {
		t.Errorf("outcome = %q, want the arbitration that stands", got)
	}
}

// ---------------------------------------------------------------------------
// The village
// ---------------------------------------------------------------------------

func TestOtherReportsFromTheVillageCorroborateTheEvent(t *testing.T) {
	// The most useful thing here. A calamity attested by six independent
	// people who do not control each other's fields is hard to doubt.
	s, pool := testClaims(t)
	f := seed(t, pool)

	for i := 0; i < 6; i++ {
		f.file(t, report{farmerID: fmt.Sprintf("N%d", i), village: chVillageA,
			survey: fmt.Sprintf("%d", 100+i), percent: 50})
	}

	h, err := s.For(context.Background(), Lookup{
		FarmerID: "F6", SurveyNumber: "42", VillageCode: chVillageA,
		EventID: f.eventID, DeclaredPercent: 55,
	})
	if err != nil {
		t.Fatal(err)
	}
	if h.Village.ReportsInVillage != 6 {
		t.Errorf("reports in village = %d, want 6", h.Village.ReportsInVillage)
	}
	if !hasSignal(h, SignalCorroborated) {
		t.Errorf("six neighbours did not produce a corroboration signal: %+v",
			h.Signals)
	}
}

func TestALoneClaimIsNotedWithoutBeingDoubted(t *testing.T) {
	// A village where one person filed is very often a village where one
	// person has a handset.
	s, pool := testClaims(t)
	f := seed(t, pool)

	for i := 0; i < 3; i++ {
		f.file(t, report{farmerID: fmt.Sprintf("E%d", i), village: chVillageB,
			survey: fmt.Sprintf("%d", 200+i), percent: 50})
	}

	h, err := s.For(context.Background(), Lookup{
		FarmerID: "F7", SurveyNumber: "42", VillageCode: chVillageA,
		EventID: f.eventID, DeclaredPercent: 55,
	})
	if err != nil {
		t.Fatal(err)
	}
	if h.Village.ReportsInVillage != 0 {
		t.Errorf("reports in this village = %d, want 0", h.Village.ReportsInVillage)
	}
	if h.Village.ReportsInEvent != 3 {
		t.Errorf("reports in event = %d, want 3", h.Village.ReportsInEvent)
	}

	sig, ok := signal(h, SignalLoneClaimInVillage)
	if !ok {
		t.Fatalf("no lone-claim signal: %+v", h.Signals)
	}
	if sig.Severity != SeverityInfo {
		t.Errorf("severity = %s; being first is not being suspicious", sig.Severity)
	}
	if !strings.Contains(sig.Innocent, "handset") {
		t.Errorf("the innocent reading is missing: %q", sig.Innocent)
	}
}

func TestAFigureFarAboveTheVillageMedianIsFlaggedForALook(t *testing.T) {
	s, pool := testClaims(t)
	f := seed(t, pool)

	for i, p := range []float64{25, 30, 35, 28} {
		f.file(t, report{farmerID: fmt.Sprintf("M%d", i), village: chVillageA,
			survey: fmt.Sprintf("%d", 300+i), percent: p})
	}

	h, err := s.For(context.Background(), Lookup{
		FarmerID: "F8", SurveyNumber: "42", VillageCode: chVillageA,
		EventID: f.eventID, DeclaredPercent: 90,
	})
	if err != nil {
		t.Fatal(err)
	}
	if h.Village.MedianPercentInVillage == nil {
		t.Fatal("no village median from four reports")
	}
	sig, ok := signal(h, SignalWellAboveNeighbours)
	if !ok {
		t.Fatalf("90%% against a median of %v produced no signal: %+v",
			*h.Village.MedianPercentInVillage, h.Signals)
	}
	if !strings.Contains(sig.Innocent, "uneven") {
		t.Errorf("the innocent reading does not mention uneven damage: %q",
			sig.Innocent)
	}
}

func TestUnderstatingALossIsTreatedAsSomethingToCheckNotToDoubt(t *testing.T) {
	// A farmer claiming less than their neighbours is the common case, and
	// the reason to notice is that they may be underselling an entitlement.
	s, pool := testClaims(t)
	f := seed(t, pool)

	for i, p := range []float64{70, 75, 80, 72} {
		f.file(t, report{farmerID: fmt.Sprintf("L%d", i), village: chVillageA,
			survey: fmt.Sprintf("%d", 400+i), percent: p})
	}

	h, err := s.For(context.Background(), Lookup{
		FarmerID: "F9", SurveyNumber: "42", VillageCode: chVillageA,
		EventID: f.eventID, DeclaredPercent: 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	sig, ok := signal(h, SignalWellBelowNeighbours)
	if !ok {
		t.Fatalf("no signal: %+v", h.Signals)
	}
	if sig.Severity != SeverityInfo {
		t.Errorf("severity = %s for a farmer claiming less than their "+
			"neighbours", sig.Severity)
	}
	if !strings.Contains(sig.Innocent, "entitled") {
		t.Errorf("the reading does not point at the entitlement: %q", sig.Innocent)
	}
}

func TestTooFewNeighboursForAMedianProducesNoComparison(t *testing.T) {
	// Two reports do not make a village median worth comparing against.
	s, pool := testClaims(t)
	f := seed(t, pool)

	f.file(t, report{farmerID: "P1", village: chVillageA, survey: "501", percent: 20})
	f.file(t, report{farmerID: "P2", village: chVillageA, survey: "502", percent: 25})

	h, err := s.For(context.Background(), Lookup{
		FarmerID: "F10", SurveyNumber: "42", VillageCode: chVillageA,
		EventID: f.eventID, DeclaredPercent: 95,
	})
	if err != nil {
		t.Fatal(err)
	}
	if hasSignal(h, SignalWellAboveNeighbours) {
		t.Error("a median from two reports was used for a comparison")
	}
}

// ---------------------------------------------------------------------------
// Signals
// ---------------------------------------------------------------------------

func TestARepeatedlyClaimedParcelIsFlaggedWithItsOrdinaryExplanation(t *testing.T) {
	// Land beside a nallah floods most years, and its owner has been right
	// every time. The flag exists; the reading beside it is what stops it
	// becoming a mark against a farmer.
	s, pool := testClaims(t)
	f := seed(t, pool)

	for i, y := range []string{"2022-23", "2023-24", "2024-25"} {
		f.file(t, report{farmerID: "R1", farmID: "FARM-R", year: y,
			occurred: fmt.Sprintf("202%d-08-01", 2+i), percent: 50,
			outcome: "confirmed"})
	}

	h, err := s.For(context.Background(), Lookup{
		FarmerID: "R1", FarmID: "FARM-R", VillageCode: chVillageA,
		EventID: f.eventID, DeclaredPercent: 60,
	})
	if err != nil {
		t.Fatal(err)
	}
	sig, ok := signal(h, SignalRepeatParcelClaims)
	if !ok {
		t.Fatalf("three years of claims produced no signal: %+v", h.Signals)
	}
	if !strings.Contains(sig.Innocent, "right every time") {
		t.Errorf("the innocent reading is missing or weak: %q", sig.Innocent)
	}
	if !h.Attention() {
		t.Error("a warning did not mark the claim for attention")
	}
}

func TestOneRejectionAmongConfirmationsIsNotASignal(t *testing.T) {
	s, pool := testClaims(t)
	f := seed(t, pool)

	f.file(t, report{farmerID: "R2", survey: "601", year: "2023-24",
		occurred: "2023-08-01", percent: 50, outcome: "confirmed"})
	f.file(t, report{farmerID: "R2", survey: "602", year: "2024-25",
		occurred: "2024-08-01", percent: 50, outcome: "confirmed"})
	f.file(t, report{farmerID: "R2", survey: "603", year: "2025-26",
		occurred: "2025-08-01", percent: 50, outcome: "rejected"})

	h, err := s.For(context.Background(), Lookup{
		FarmerID: "R2", SurveyNumber: "42", VillageCode: chVillageA,
		EventID: f.eventID, DeclaredPercent: 60,
	})
	if err != nil {
		t.Fatal(err)
	}
	if hasSignal(h, SignalPriorRejections) {
		t.Errorf("one rejection among two confirmations was flagged: %+v", h.Signals)
	}
}

func TestRepeatedRejectionsAreRaisedWithWhatRejectionActuallyMeans(t *testing.T) {
	s, pool := testClaims(t)
	f := seed(t, pool)

	for i, y := range []string{"2023-24", "2024-25", "2025-26"} {
		f.file(t, report{farmerID: "R3", survey: fmt.Sprintf("70%d", i), year: y,
			occurred: fmt.Sprintf("202%d-08-01", 3+i), percent: 50,
			outcome: "not_traceable"})
	}

	h, err := s.For(context.Background(), Lookup{
		FarmerID: "R3", SurveyNumber: "42", VillageCode: chVillageA,
		EventID: f.eventID, DeclaredPercent: 60,
	})
	if err != nil {
		t.Fatal(err)
	}
	sig, ok := signal(h, SignalPriorRejections)
	if !ok {
		t.Fatalf("three untraceable claims produced no signal: %+v", h.Signals)
	}
	if !strings.Contains(sig.Innocent, "record-keeping") {
		t.Errorf("the reading does not explain what not-traceable means: %q",
			sig.Innocent)
	}
}

func TestEverySignalCarriesItsInnocentReading(t *testing.T) {
	// The rule the whole package rests on. A flag with no counter-reading
	// will be used to discount a farmer, and will be right often enough that
	// the habit sticks.
	s, pool := testClaims(t)
	f := seed(t, pool)

	for i, y := range []string{"2022-23", "2023-24", "2024-25"} {
		f.file(t, report{farmerID: "S1", farmID: "FARM-S", year: y,
			occurred: fmt.Sprintf("202%d-08-01", 2+i), percent: 50,
			outcome: "rejected"})
	}
	for i, p := range []float64{20, 22, 25} {
		f.file(t, report{farmerID: fmt.Sprintf("SN%d", i), village: chVillageA,
			survey: fmt.Sprintf("%d", 800+i), percent: p})
	}

	h, err := s.For(context.Background(), Lookup{
		FarmerID: "S1", FarmID: "FARM-S", VillageCode: chVillageA,
		EventID: f.eventID, DeclaredPercent: 95,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(h.Signals) < 3 {
		t.Fatalf("expected several signals, got %d", len(h.Signals))
	}
	for _, sig := range h.Signals {
		if strings.TrimSpace(sig.Innocent) == "" {
			t.Errorf("signal %s has no innocent reading", sig.Code)
		}
		if strings.TrimSpace(sig.Detail) == "" {
			t.Errorf("signal %s has no detail", sig.Code)
		}
		if sig.Severity != SeverityInfo && sig.Severity != SeverityWarning {
			t.Errorf("signal %s has severity %q; nothing here is ever critical",
				sig.Code, sig.Severity)
		}
	}
}

func TestAFirstClaimSaysThatIsOrdinary(t *testing.T) {
	// The platform is new. Almost every claim will be a first claim for some
	// time, and that says nothing about anybody.
	s, pool := testClaims(t)
	f := seed(t, pool)

	h, err := s.For(context.Background(), Lookup{
		FarmerID: "NEW", SurveyNumber: "42", VillageCode: chVillageA,
		EventID: f.eventID, DeclaredPercent: 60,
	})
	if err != nil {
		t.Fatal(err)
	}
	sig, ok := signal(h, SignalFirstClaim)
	if !ok {
		t.Fatalf("no first-claim signal: %+v", h.Signals)
	}
	if sig.Severity != SeverityInfo {
		t.Errorf("a first claim was flagged as %s", sig.Severity)
	}
	if h.Attention() {
		t.Error("a farmer with no history was marked for attention")
	}
}

func TestTheSummaryReadsAsASentenceWithNothingOnRecord(t *testing.T) {
	s, pool := testClaims(t)
	f := seed(t, pool)

	h, err := s.For(context.Background(), Lookup{
		FarmerID: "NEW2", SurveyNumber: "42", VillageCode: chVillageA,
		EventID: f.eventID, DeclaredPercent: 60,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(h.Summary(), "Nothing on record") {
		t.Errorf("summary = %q", h.Summary())
	}
}

func TestALookupWithNothingToGoOnIsNotAnError(t *testing.T) {
	// A report with no farmer id, no parcel and no event still gets a
	// context; it is simply empty.
	s, _ := testClaims(t)

	h, err := s.For(context.Background(), Lookup{})
	if err != nil {
		t.Fatalf("an empty lookup errored: %v", err)
	}
	if h.Parcel.Claims == nil || h.Farmer.Claims == nil {
		t.Error("claim slices are nil; a client mapping over them would fail")
	}
}

func hasSignal(h *History, code string) bool {
	_, ok := signal(h, code)
	return ok
}

func signal(h *History, code string) (Signal, bool) {
	for _, s := range h.Signals {
		if s.Code == code {
			return s, true
		}
	}
	return Signal{}, false
}
