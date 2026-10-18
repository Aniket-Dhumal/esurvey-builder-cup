package retention

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Erasing one person, on request.
//
// The failure that matters is not "something was left behind" in the abstract
// -- it is a specific column still holding a name or a mobile number after
// somebody was told they had been erased. So these tests read the rows back.

const erasureFarmer = "ERASE-TEST-99999"

func erasureSweeper(t *testing.T) (*Sweeper, *pgxpool.Pool) {
	t.Helper()

	url := os.Getenv("ESURVEY_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set ESURVEY_TEST_DATABASE_URL to run the erasure tests")
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
		bg := context.Background()
		_, _ = pool.Exec(bg, `DELETE FROM grievances WHERE farmer_id = $1`, erasureFarmer)
		_, _ = pool.Exec(bg, `DELETE FROM crop_loss_reports WHERE farmer_id = $1`, erasureFarmer)
		_, _ = pool.Exec(bg, `DELETE FROM survey_events WHERE reference = 'ERASE-EVT'`)
		_, _ = pool.Exec(bg, `DELETE FROM users WHERE agristack_farmer_id = $1`, erasureFarmer)
		_, _ = pool.Exec(bg, `DELETE FROM users WHERE username = 'erase-test-officer@esurvey-test.in'`)
		_, _ = pool.Exec(bg, `DELETE FROM jurisdictions WHERE code IN ('ERASE-D', 'ERASE-S')`)
	}
	clean()
	t.Cleanup(clean)

	return New(pool, nil, slog.New(slog.NewTextHandler(io.Discard, nil))), pool
}

// subject creates a farmer with an account and one settled report.
func subject(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	ctx := context.Background()

	var stateID, districtID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO jurisdictions (level, code, name)
		VALUES ('state', 'ERASE-S', 'Erase Test State') RETURNING id`).
		Scan(&stateID); err != nil {
		t.Fatalf("state: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO jurisdictions (level, code, name, parent_id)
		VALUES ('district', 'ERASE-D', 'Erase Test District', $1) RETURNING id`,
		stateID).Scan(&districtID); err != nil {
		t.Fatalf("district: %v", err)
	}

	if _, err := pool.Exec(ctx, `
		INSERT INTO users (kind, full_name, mobile_e164, agristack_farmer_id,
		                   aadhaar_hash, aadhaar_last4, status)
		VALUES ('farmer', 'Harichndra Yedaba Dhumal', '+919527400001', $1,
		        repeat('a', 64), '4138', 'active')`, erasureFarmer); err != nil {
		t.Fatalf("account: %v", err)
	}

	var eventID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO survey_events (reference, calamity_type, title, district_id,
		                           incident_start, incident_end, survey_closes_at)
		VALUES ('ERASE-EVT', 'hailstorm', 'Erase test', $1,
		        now() - interval '10 days', now() - interval '9 days',
		        now() + interval '30 days') RETURNING id`, districtID).
		Scan(&eventID); err != nil {
		t.Fatalf("event: %v", err)
	}

	var reportID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO crop_loss_reports
			(reference, survey_event_id, farmer_id, farmer_name, farmer_mobile,
			 farmer_address, village_lgd_code, village_name,
			 sub_district_lgd_code, sub_district_name, district_lgd_code,
			 district_name, season, crop_year, loss_category, calamity_type,
			 occurred_at, state, declaration_accepted, farmer_signature_kind)
		VALUES ('ERASE-RPT-1', $1, $2, 'Harichndra Yedaba Dhumal',
		        '+919527400001', 'malegaon, ta barshi', '562111', 'Malegaon',
		        '4246', 'Barshi', 'ERASE-D', 'Erase Test District',
		        'kharif', '2026-27', 'standing_crop', 'hailstorm',
		        now() - interval '9 days', 'closed', true, 'thumb')
		RETURNING id`, eventID, erasureFarmer).Scan(&reportID); err != nil {
		t.Fatalf("report: %v", err)
	}

	return reportID
}

func TestADryRunCountsAndChangesNothing(t *testing.T) {
	// The first call must never write. This is the only irreversible
	// operation here, and the number it reports is what somebody looks at
	// before making it real.
	s, pool := erasureSweeper(t)
	subject(t, pool)

	res, err := s.ErasePerson(context.Background(), erasureFarmer, false)
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if !res.DryRun {
		t.Error("a call without confirmation reported itself as a real run")
	}
	if res.Reports != 1 || res.Accounts != 1 {
		t.Errorf("counted %d reports and %d accounts, expected 1 and 1",
			res.Reports, res.Accounts)
	}

	var name string
	if err := pool.QueryRow(context.Background(),
		`SELECT farmer_name FROM crop_loss_reports WHERE farmer_id = $1`,
		erasureFarmer).Scan(&name); err != nil {
		t.Fatalf("reading back: %v", err)
	}
	if name != "Harichndra Yedaba Dhumal" {
		t.Errorf("a dry run changed the record: farmer_name is now %q", name)
	}
}

func TestErasureRemovesThePersonAndKeepsTheDecision(t *testing.T) {
	// Both halves matter. A crop loss assessment is a decision about public
	// money: the figures have to survive for audit. The person does not.
	s, pool := erasureSweeper(t)
	subject(t, pool)
	ctx := context.Background()

	if _, err := s.ErasePerson(ctx, erasureFarmer, true); err != nil {
		t.Fatalf("erasing: %v", err)
	}

	var name, season string
	var mobile, address *string
	var anonymisedAt *time.Time
	if err := pool.QueryRow(ctx, `
		SELECT farmer_name, farmer_mobile, farmer_address, season, anonymised_at
		FROM crop_loss_reports WHERE farmer_id = $1`, erasureFarmer).
		Scan(&name, &mobile, &address, &season, &anonymisedAt); err != nil {
		t.Fatalf("reading the report back: %v", err)
	}

	if mobile != nil {
		t.Errorf("the mobile number survived erasure: %q", *mobile)
	}
	if address != nil {
		t.Errorf("the address survived erasure: %q", *address)
	}
	if name == "Harichndra Yedaba Dhumal" {
		t.Error("the name survived erasure")
	}
	if anonymisedAt == nil {
		t.Error("the report is not marked as anonymised, so a later sweep " +
			"cannot tell it has already been done")
	}

	// And the record of the claim itself is intact.
	if season != "kharif" {
		t.Errorf("the assessment lost data it must keep: season is %q", season)
	}
}

func TestErasureEmptiesTheAccountWithoutDeletingIt(t *testing.T) {
	// Deleting is impossible -- the audit log references the account, and the
	// reference is ON DELETE SET NULL, which the append-only trigger refuses.
	// That is correct: "who did this" must stay answerable. So the account is
	// emptied and deactivated instead, and this test is the proof it really
	// was emptied rather than merely disabled.
	s, pool := erasureSweeper(t)
	subject(t, pool)
	ctx := context.Background()

	if _, err := s.ErasePerson(ctx, erasureFarmer, true); err != nil {
		t.Fatalf("erasing: %v", err)
	}

	var fullName, status string
	var mobile, aadhaarHash, aadhaarLast4, farmerID *string
	if err := pool.QueryRow(ctx, `
		SELECT full_name, status, mobile_e164, aadhaar_hash, aadhaar_last4,
		       agristack_farmer_id
		FROM users WHERE agristack_farmer_id = $1`, erasureFarmer).
		Scan(&fullName, &status, &mobile, &aadhaarHash, &aadhaarLast4,
			&farmerID); err != nil {
		t.Fatalf("the account was deleted, not emptied: %v", err)
	}

	for label, v := range map[string]*string{
		"mobile_e164":   mobile,
		"aadhaar_hash":  aadhaarHash,
		"aadhaar_last4": aadhaarLast4,
	} {
		if v != nil {
			t.Errorf("%s survived erasure: %q", label, *v)
		}
	}
	if fullName == "Harichndra Yedaba Dhumal" {
		t.Error("the account still carries the person's name")
	}
	if status != "deactivated" {
		t.Errorf("the emptied account is %q; it must not be signable-into", status)
	}
	// Kept deliberately: it is the registry's key, and without it a second
	// request for the same person could not be matched to anything.
	if farmerID == nil || *farmerID != erasureFarmer {
		t.Error("the registry id was dropped, so this erasure cannot be verified")
	}
}

func TestErasureIsRefusedWhileAnObjectionIsOpen(t *testing.T) {
	// Erasing somebody mid-appeal destroys the evidence their own appeal
	// rests on. A request arriving during an appeal is likelier to be
	// somebody acting in that person's name than the person abandoning their
	// own claim, and the cost of being wrong runs one way.
	s, pool := erasureSweeper(t)
	reportID := subject(t, pool)
	ctx := context.Background()

	if _, err := pool.Exec(ctx, `
		INSERT INTO grievances (report_id, farmer_id, farmer_name,
		                        village_lgd_code, reference, ground,
		                        statement, state, due_at)
		VALUES ($1, $2, 'Harichndra Yedaba Dhumal', '562111', 'ERASE-GRV-1',
		        'damage_percent_low',
		        'The plot below Ramrao''s well was flattened.', 'open',
		        now() + interval '30 days')`,
		reportID, erasureFarmer); err != nil {
		t.Fatalf("opening a grievance: %v", err)
	}

	if _, err := s.ErasePerson(ctx, erasureFarmer, true); !errors.Is(err, ErrOpenGrievance) {
		t.Fatalf("got %v, want ErrOpenGrievance", err)
	}

	// And nothing was touched on the way to refusing.
	var name string
	if err := pool.QueryRow(ctx,
		`SELECT farmer_name FROM crop_loss_reports WHERE farmer_id = $1`,
		erasureFarmer).Scan(&name); err != nil {
		t.Fatalf("reading back: %v", err)
	}
	if name != "Harichndra Yedaba Dhumal" {
		t.Error("a refused erasure still changed the record")
	}
}

func TestErasingTwiceIsSafe(t *testing.T) {
	// A request repeated, or a person erased by two operators at once. The
	// second must not fail, and must report nothing left to do.
	s, pool := erasureSweeper(t)
	subject(t, pool)
	ctx := context.Background()

	if _, err := s.ErasePerson(ctx, erasureFarmer, true); err != nil {
		t.Fatalf("first erasure: %v", err)
	}

	again, err := s.ErasePerson(ctx, erasureFarmer, true)
	if err != nil {
		t.Fatalf("second erasure: %v", err)
	}
	if again.Reports != 0 || again.Accounts != 0 {
		t.Errorf("the second run found %d reports and %d accounts still to erase",
			again.Reports, again.Accounts)
	}
}

func TestAnUnknownSubjectAnswersLikeAnErasedOne(t *testing.T) {
	// Otherwise this becomes an oracle for which farmer ids exist: ask to
	// erase, and the answer tells you whether there was anything there.
	s, pool := erasureSweeper(t)
	subject(t, pool)
	ctx := context.Background()

	if _, err := s.ErasePerson(ctx, erasureFarmer, true); err != nil {
		t.Fatalf("erasing: %v", err)
	}

	erased, err := s.ErasePerson(ctx, erasureFarmer, false)
	if err != nil {
		t.Fatalf("re-querying an erased subject: %v", err)
	}
	unknown, err := s.ErasePerson(ctx, "NO-SUCH-FARMER-00000", false)
	if err != nil {
		t.Fatalf("querying an unknown subject: %v", err)
	}

	if erased.Reports != unknown.Reports || erased.Accounts != unknown.Accounts {
		t.Errorf("an erased subject (%+v) is distinguishable from an unknown "+
			"one (%+v)", erased, unknown)
	}
}

func TestErasureRedactsAnObjectionsNameAndItsWords(t *testing.T) {
	// An objection carries the person twice: their name, and a statement they
	// wrote themselves. The statement is the one a name-only redaction misses,
	// and it is some of the most re-identifying text here -- "the plot below
	// Ramrao's well" names somebody without naming them.
	s, pool := erasureSweeper(t)
	reportID := subject(t, pool)
	ctx := context.Background()

	// A decided objection needs the officer who decided it: the schema
	// refuses a decision with nobody's name on it, which is the right rule.
	var officerID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO users (kind, full_name, username, email, password_hash, status)
		VALUES ('officer', 'Erase Test Officer', 'erase-test-officer@esurvey-test.in', 'erase-test-officer@esurvey-test.in',
		        'x', 'active')
		RETURNING id`).Scan(&officerID); err != nil {
		t.Fatalf("deciding officer: %v", err)
	}

	if _, err := pool.Exec(ctx, `
		INSERT INTO grievances (report_id, farmer_id, farmer_name,
		                        village_lgd_code, reference, ground,
		                        statement, state, due_at,
		                        decided_at, decided_by, decision_reason)
		VALUES ($1, $2, 'Harichndra Yedaba Dhumal', '562111', 'ERASE-GRV-2',
		        'damage_percent_low',
		        'The plot below Ramrao''s well was flattened.', 'rejected',
		        now() + interval '30 days', now(), $3,
		        'the photographs do not support a higher figure')`,
		reportID, erasureFarmer, officerID); err != nil {
		t.Fatalf("opening a decided grievance: %v", err)
	}

	res, err := s.ErasePerson(ctx, erasureFarmer, true)
	if err != nil {
		t.Fatalf("erasing: %v", err)
	}
	if res.Grievances != 1 {
		t.Errorf("counted %d objections to redact, expected 1", res.Grievances)
	}

	var name, statement, ground, state string
	if err := pool.QueryRow(ctx, `
		SELECT farmer_name, statement, ground, state
		FROM grievances WHERE reference = 'ERASE-GRV-2'`).
		Scan(&name, &statement, &ground, &state); err != nil {
		t.Fatalf("reading the objection back: %v", err)
	}

	if name == "Harichndra Yedaba Dhumal" {
		t.Error("the objection still names the person")
	}
	if strings.Contains(statement, "Ramrao") {
		t.Errorf("the farmer's own words survived erasure: %q", statement)
	}

	// And the objection is still auditable: an officer can see one was made,
	// on what ground, and how it ended.
	if ground != "damage_percent_low" {
		t.Errorf("the ground was lost: %q", ground)
	}
	if state != "rejected" {
		t.Errorf("the outcome was lost: %q", state)
	}
}
