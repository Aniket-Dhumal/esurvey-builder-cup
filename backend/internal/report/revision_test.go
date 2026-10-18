package report

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/agribid/esurvey-platform/backend/internal/platform/dbtest"
	"github.com/agribid/esurvey-platform/backend/internal/platform/page"
	"github.com/agribid/esurvey-platform/backend/internal/rbac"
)

// An upheld appeal has to change the figure on the report.
//
// DecideGrievance already refused to uphold an appeal without a revised
// percentage -- "saying the farmer was right and leaving the figure where it
// was is a contradiction" -- and then wrote that percentage onto the grievance
// row and nowhere else. crop_loss_reports.adjudicated_percent kept the
// original. Every rollup, worklist and assessment report reads that column, so
// a farmer could win their appeal and find the old number on their own report.
//
// These are the first database-backed tests for the verification store. They
// exist here rather than as unit tests because the property being checked is
// that two writes land together, which is not observable above the pool.

func testStore(t *testing.T) (*Store, *pgxpool.Pool) {
	t.Helper()

	url := os.Getenv("ESURVEY_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set ESURVEY_TEST_DATABASE_URL to run the adjudication tests")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Capped for the same reason as the other database suites: the default is
	// the core count, and several packages share one Postgres.
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
		// Findings and decisions are final by trigger, and the trigger fires
		// on the cascade too -- so deleting the report they belong to is
		// refused, which is the guarantee working rather than a fault. Test
		// data therefore has to lift it deliberately and put it back.
		for _, t := range []string{"report_verifications", "report_adjudications"} {
			_, _ = pool.Exec(c, `ALTER TABLE `+t+` DISABLE TRIGGER USER`)
		}

		// Dependency order, and not the obvious one: user_roles holds
		// jurisdictions with RESTRICT, so the officers go before the places
		// they are posted to. Otherwise the jurisdiction delete fails quietly
		// and the next test collides on a duplicate code.
		// By farmer as well as by reference: a report filed through Create
		// gets a generated reference, and one left behind would hold the
		// event, and through it the jurisdictions, in place.
		_, _ = pool.Exec(c, `DELETE FROM crop_loss_reports WHERE reference LIKE 'RV-%' OR farmer_id = 'F-RV-1'`)
		_, _ = pool.Exec(c, `DELETE FROM survey_events WHERE reference LIKE 'RV-%'`)
		_, _ = pool.Exec(c, `DELETE FROM users WHERE username LIKE 'rv.%'`)
		_, _ = pool.Exec(c, `DELETE FROM jurisdictions WHERE code LIKE 'RV-%'`)

		for _, t := range []string{"report_verifications", "report_adjudications"} {
			_, _ = pool.Exec(c, `ALTER TABLE `+t+` ENABLE TRIGGER USER`)
		}
	}
	clean()
	t.Cleanup(clean)

	return NewStore(pool), pool
}

// fixture is one report, verified by its village pair and adjudicated, which
// is the state an appeal is filed against.
type fixture struct {
	reportID  string
	talathi   string
	sahayak   string
	mandal    string
	tehsildar string
}

func seedAdjudicated(t *testing.T, s *Store, pool *pgxpool.Pool) fixture {
	t.Helper()
	ctx := context.Background()

	jur := map[string]string{}
	for _, r := range []struct{ level, code, name, parent string }{
		{"state", "RV-ST", "Revision State", ""},
		{"district", "RV-D1", "Revision District", "RV-ST"},
		{"sub_district", "RV-T1", "Revision Taluka", "RV-D1"},
		{"village", "RV-V1", "Revision Village", "RV-T1"},
	} {
		var parent any
		if r.parent != "" {
			parent = jur[r.parent]
		}
		var id string
		if err := pool.QueryRow(ctx, `
			INSERT INTO jurisdictions (level, code, name, parent_id)
			VALUES ($1::jurisdiction_level, $2, $3, $4::uuid)
			RETURNING id`, r.level, r.code, r.name, parent).Scan(&id); err != nil {
			t.Fatalf("seeding %s: %v", r.code, err)
		}
		jur[r.code] = id
	}

	officer := func(username, name string, role rbac.Role, at string) string {
		var id string
		if err := pool.QueryRow(ctx, `
			INSERT INTO users (username, email, full_name, kind, status, password_hash)
			-- Not a credential: a syntactically valid placeholder, because an
			-- officer row without one is refused by officer_has_password. No
			-- test here signs in.
			VALUES ($1 || '@esurvey-test.in', $1 || '@esurvey-test.in', $2, 'officer', 'active',
			        '$argon2id$v=19$m=65536,t=3,p=4$c2VlZHNlZWRzZWVk$notarealhash')
			RETURNING id`,
			username, name).Scan(&id); err != nil {
			t.Fatalf("seeding %s: %v", username, err)
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO user_roles (user_id, role, jurisdiction_id)
			VALUES ($1, $2::officer_role, $3)`,
			id, string(role), jur[at]); err != nil {
			t.Fatalf("granting %s: %v", username, err)
		}
		return id
	}

	f := fixture{
		talathi:   officer("rv.talathi", "Revision Talathi", rbac.RoleTalathi, "RV-V1"),
		sahayak:   officer("rv.sahayak", "Revision Sahayak", rbac.RoleKrushiSahayak, "RV-V1"),
		mandal:    officer("rv.mandal", "Revision Mandal", rbac.RoleMandalOfficer, "RV-T1"),
		tehsildar: officer("rv.tehsildar", "Revision Tehsildar", rbac.RoleTehsildar, "RV-T1"),
	}

	var eventID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO survey_events (
			reference, calamity_type, title, district_id,
			incident_start, incident_end, survey_closes_at, state,
			declared_by, declared_at)
		VALUES ('RV-EVT-1', 'hailstorm', 'Revision Test Event', $1,
		        current_date - 10, current_date - 9, now() + interval '30 days',
		        'open', $2, now())
		RETURNING id`, jur["RV-D1"], f.tehsildar).Scan(&eventID); err != nil {
		t.Fatalf("seeding event: %v", err)
	}

	if err := pool.QueryRow(ctx, `
		INSERT INTO crop_loss_reports (
			reference, survey_event_id, farmer_id, farmer_name,
			village_lgd_code, village_name,
			sub_district_lgd_code, sub_district_name,
			district_lgd_code, district_name,
			season, crop_year, loss_category, calamity_type, occurred_at,
			state, declaration_accepted,
			farmer_signature_kind, farmer_signed_at)
		VALUES ('RV-RPT-1', $1, 'F-RV-1', 'Revision Farmer',
		        'RV-V1', 'Revision Village', 'RV-T1', 'Revision Taluka',
		        'RV-D1', 'Revision District',
		        'kharif', '2026-27', 'standing_crop', 'hailstorm',
		        now() - interval '10 days', 'verified', true,
		        'thumb', now() - interval '9 days')
		RETURNING id`, eventID).Scan(&f.reportID); err != nil {
		t.Fatalf("seeding report: %v", err)
	}

	// The village pair, blind to each other, then a decision on the
	// consensus route.
	for _, v := range []struct {
		officer string
		role    rbac.Role
		pct     float64
	}{
		{f.talathi, rbac.RoleTalathi, 60},
		{f.sahayak, rbac.RoleKrushiSahayak, 62},
	} {
		pct := v.pct
		if _, err := s.RecordFinding(ctx, NewFinding{
			ReportID:      f.reportID,
			OfficerID:     v.officer,
			Role:          v.role,
			Tier:          TierVillagePair,
			Outcome:       OutcomeConfirmed,
			DamagePercent: &pct,
			Findings:      "Standing crop flattened across most of the plot.",
		}); err != nil {
			t.Fatalf("recording a finding: %v", err)
		}
	}

	if _, err := s.Adjudicate(ctx, NewAdjudication{
		ReportID:           f.reportID,
		OfficerID:          f.mandal,
		Role:               rbac.RoleMandalOfficer,
		AdjudicatedPercent: 61,
		AdjudicatedAreaHa:  1.2,
		Reason:             "Both findings agree within the consensus band.",
	}); err != nil {
		t.Fatalf("adjudicating: %v", err)
	}

	return f
}

func fileGrievance(t *testing.T, s *Store, f fixture) string {
	t.Helper()
	g, err := s.FileGrievance(context.Background(), NewGrievance{
		ReportID:  f.reportID,
		Ground:    GroundDamagePercentLow,
		Statement: "The whole plot was lost, not sixty per cent of it.",
		Channel:   "app",
	}, rbac.Scope{FarmerID: "F-RV-1"})
	if err != nil {
		t.Fatalf("filing a grievance: %v", err)
	}
	return g.ID
}

func reportFigure(t *testing.T, pool *pgxpool.Pool, reportID string) float64 {
	t.Helper()
	var pct float64
	if err := pool.QueryRow(context.Background(),
		`SELECT adjudicated_percent FROM crop_loss_reports WHERE id = $1`,
		reportID).Scan(&pct); err != nil {
		t.Fatalf("reading the report figure: %v", err)
	}
	return pct
}

func TestAnUpheldAppealChangesTheFigureOnTheReport(t *testing.T) {
	s, pool := testStore(t)
	f := seedAdjudicated(t, s, pool)
	gid := fileGrievance(t, s, f)

	if got := reportFigure(t, pool, f.reportID); got != 61 {
		t.Fatalf("the report should start at the adjudicated figure, got %v", got)
	}

	revised := 85.0
	if _, err := s.DecideGrievance(context.Background(), GrievanceDecision{
		GrievanceID:    gid,
		State:          GrievanceUpheld,
		Reason:         "The photographs show the whole plot flattened.",
		RevisedPercent: &revised,
		OfficerID:      f.tehsildar,
		Role:           rbac.RoleTehsildar,
	}); err != nil {
		t.Fatalf("deciding the grievance: %v", err)
	}

	// The point of the whole change: the report carries the new figure, not
	// the one the farmer successfully appealed against.
	if got := reportFigure(t, pool, f.reportID); got != revised {
		t.Errorf("the report still says %v after the appeal was upheld", got)
	}

	// And the current adjudication is the revision, since that is what every
	// caller asks for.
	adj, err := s.Adjudication(context.Background(), f.reportID)
	if err != nil {
		t.Fatalf("reading the adjudication: %v", err)
	}
	if adj.AdjudicatedPercent != revised {
		t.Errorf("Adjudication returned %v, not the revision",
			adj.AdjudicatedPercent)
	}
	if adj.Role != rbac.RoleTehsildar {
		t.Errorf("the revision records %s, not the officer who made it", adj.Role)
	}
}

func TestTheOriginalDecisionSurvivesTheRevision(t *testing.T) {
	// report_adjudications is append-only by trigger, and rightly: who decided
	// what, on what was in front of them, and when, is exactly what an inquiry
	// asks afterwards. A revision must add to that record, never replace it.
	s, pool := testStore(t)
	f := seedAdjudicated(t, s, pool)
	gid := fileGrievance(t, s, f)

	revised := 85.0
	if _, err := s.DecideGrievance(context.Background(), GrievanceDecision{
		GrievanceID:    gid,
		State:          GrievanceUpheld,
		Reason:         "The photographs show the whole plot flattened.",
		RevisedPercent: &revised,
		OfficerID:      f.tehsildar,
		Role:           rbac.RoleTehsildar,
	}); err != nil {
		t.Fatalf("deciding the grievance: %v", err)
	}

	var count int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM report_adjudications WHERE report_id = $1`,
		f.reportID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("expected the original and the revision, found %d rows", count)
	}

	// The chain reads in both directions, and the revision names the appeal
	// that caused it -- an adjudicated figure changes only through a process
	// the farmer can see.
	var supersedes, grievance *string
	if err := pool.QueryRow(context.Background(), `
		SELECT supersedes_id::text, grievance_id::text
		FROM report_adjudications
		WHERE report_id = $1 AND supersedes_id IS NOT NULL`,
		f.reportID).Scan(&supersedes, &grievance); err != nil {
		t.Fatal(err)
	}
	if supersedes == nil || *supersedes == "" {
		t.Error("the revision does not name the decision it replaced")
	}
	if grievance == nil || *grievance != gid {
		t.Error("the revision does not name the appeal that caused it")
	}
}

func TestARejectedAppealLeavesTheFigureStanding(t *testing.T) {
	// Which is what rejecting an appeal means.
	s, pool := testStore(t)
	f := seedAdjudicated(t, s, pool)
	gid := fileGrievance(t, s, f)

	if _, err := s.DecideGrievance(context.Background(), GrievanceDecision{
		GrievanceID: gid,
		State:       GrievanceRejected,
		Reason:      "The satellite record and the photographs agree with 61%.",
		OfficerID:   f.tehsildar,
		Role:        rbac.RoleTehsildar,
	}); err != nil {
		t.Fatalf("deciding the grievance: %v", err)
	}

	if got := reportFigure(t, pool, f.reportID); got != 61 {
		t.Errorf("a rejected appeal moved the figure to %v", got)
	}

	var count int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM report_adjudications WHERE report_id = $1`,
		f.reportID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("a rejected appeal wrote %d adjudications", count-1)
	}
}

func TestOneDecisionCannotBeRevisedTwice(t *testing.T) {
	// Two officers deciding two appeals concurrently would otherwise leave the
	// report with two equally current figures and no rule for choosing between
	// them. The partial unique index refuses the fork.
	s, pool := testStore(t)
	f := seedAdjudicated(t, s, pool)

	current, err := s.Adjudication(context.Background(), f.reportID)
	if err != nil {
		t.Fatal(err)
	}
	gid := fileGrievance(t, s, f)

	first := Revision{
		ReportID:       f.reportID,
		GrievanceID:    gid,
		OfficerID:      f.tehsildar,
		Role:           rbac.RoleTehsildar,
		RevisedPercent: 85,
		RevisedAreaHa:  1.2,
		Reason:         "The photographs show the whole plot flattened.",
	}
	if _, err := s.Revise(context.Background(), first); err != nil {
		t.Fatalf("the first revision should be accepted: %v", err)
	}

	// A second revision naming the *same* original, which is the fork.
	tx, err := pool.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	if err := s.revise(context.Background(), tx, first, current); err == nil {
		t.Error("a second revision of the same decision was accepted")
	} else if !errors.Is(err, ErrBadState) {
		t.Errorf("expected ErrBadState for a duplicate revision, got %v", err)
	}
}

func TestOverdueIsVisibleToSomebodyOtherThanTheOfficerWhoIsLate(t *testing.T) {
	// Worklist filters on the officer's own id, so the seven-day deadline was
	// visible only to the person breaching it. An officer who has stopped
	// opening the console is precisely the one whose reports are late.
	s, pool := testStore(t)
	f := seedAdjudicated(t, s, pool)
	ctx := context.Background()

	// Put the village pair's assignments in the past. Allocate sets due_at
	// seven days out; nothing else can make one late inside a test.
	if _, err := pool.Exec(ctx, `
		INSERT INTO report_assignments
			(report_id, officer_id, role, tier, due_at, assigned_at)
		VALUES ($1, $2, 'talathi', 'village_pair',
		        now() - interval '9 days', now() - interval '16 days')`,
		f.reportID, f.talathi); err != nil {
		t.Fatalf("seeding a late assignment: %v", err)
	}

	// The report has to still be open for a late assignment to matter.
	if _, err := pool.Exec(ctx,
		`UPDATE crop_loss_reports SET state = 'under_verification' WHERE id = $1`,
		f.reportID); err != nil {
		t.Fatal(err)
	}

	res, err := s.Overdue(ctx, rbac.Scope{}, page.Params{Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	// An empty scope grants nothing, which is the right default.
	if len(res.Items) != 0 {
		t.Error("an empty scope should see nothing")
	}

	var districtID string
	if err := pool.QueryRow(ctx,
		`SELECT id::text FROM jurisdictions WHERE code = 'RV-D1'`).
		Scan(&districtID); err != nil {
		t.Fatal(err)
	}

	res, err = s.Overdue(ctx,
		rbac.Scope{JurisdictionIDs: []string{districtID}}, page.Params{Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Items) != 1 {
		t.Fatalf("a supervisor over the district should see the late "+
			"assignment, saw %d", len(res.Items))
	}

	e := res.Items[0]
	if e.OfficerName == "" {
		t.Error("a supervisor needs to know who it is with")
	}
	if e.DaysOverdue < 8 {
		t.Errorf("nine days past due reported as %d days late", e.DaysOverdue)
	}
	if e.Reference == "" || e.VillageName == "" {
		t.Error("the entry should name the claim and the village")
	}
}

func TestACompletedOrDecidedReportIsNotOverdue(t *testing.T) {
	// A claim that has been decided is not late, whatever its assignment says,
	// and a supervisor's list full of settled work is a list nobody reads.
	s, pool := testStore(t)
	f := seedAdjudicated(t, s, pool)
	ctx := context.Background()

	if _, err := pool.Exec(ctx, `
		INSERT INTO report_assignments
			(report_id, officer_id, role, tier, due_at, assigned_at)
		VALUES ($1, $2, 'talathi', 'village_pair',
		        now() - interval '9 days', now() - interval '16 days')`,
		f.reportID, f.talathi); err != nil {
		t.Fatal(err)
	}

	var districtID string
	if err := pool.QueryRow(ctx,
		`SELECT id::text FROM jurisdictions WHERE code = 'RV-D1'`).
		Scan(&districtID); err != nil {
		t.Fatal(err)
	}

	// seedAdjudicated leaves the report adjudicated.
	res, err := s.Overdue(ctx,
		rbac.Scope{JurisdictionIDs: []string{districtID}}, page.Params{Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Items) != 0 {
		t.Errorf("an adjudicated report is still listed as overdue")
	}
}
