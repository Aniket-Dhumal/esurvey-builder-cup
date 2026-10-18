package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/agribid/esurvey-platform/backend/internal/assessjob"
	"github.com/agribid/esurvey-platform/backend/internal/audit"
	"github.com/agribid/esurvey-platform/backend/internal/event"
	"github.com/agribid/esurvey-platform/backend/internal/identity"
	"github.com/agribid/esurvey-platform/backend/internal/rbac"
	"github.com/agribid/esurvey-platform/backend/internal/report"
)

// A state officer reads their own state, and no other.
//
// Every one of these answered across states before: an event by id, a
// parcel's eligibility, a village's officers, the taluka coverage, the
// assessment backlog and the whole audit log.

type twoStates struct {
	pool   *pgxpool.Pool
	srv    *Server
	ids    map[string]string
	events map[string]string
}

func seedTwoStates(t *testing.T) *twoStates {
	t.Helper()
	url := os.Getenv("ESURVEY_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set ESURVEY_TEST_DATABASE_URL to run the state scope test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Skipf("no database: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Skipf("no database: %v", err)
	}
	t.Cleanup(pool.Close)

	clean := func() {
		c := context.Background()
		_, _ = pool.Exec(c, `DELETE FROM assessment_jobs WHERE farmer_id LIKE 'F-SS-%'`)
		_, _ = pool.Exec(c, `DELETE FROM survey_events WHERE reference LIKE 'SE-SS-%'`)
		_, _ = pool.Exec(c, `DELETE FROM jurisdictions WHERE code LIKE 'SS-%'`)
	}
	clean()
	t.Cleanup(clean)

	ids := map[string]string{}
	for _, j := range []struct{ level, code, parent string }{
		{"state", "SS-A", ""},
		{"district", "SS-A-D", "SS-A"},
		{"sub_district", "SS-A-T", "SS-A-D"},
		{"village", "SS-A-V", "SS-A-T"},
		{"state", "SS-B", ""},
		{"district", "SS-B-D", "SS-B"},
		{"sub_district", "SS-B-T", "SS-B-D"},
		{"village", "SS-B-V", "SS-B-T"},
	} {
		var parent any
		if j.parent != "" {
			parent = ids[j.parent]
		}
		var id string
		if err := pool.QueryRow(ctx, `
			INSERT INTO jurisdictions (level, code, name, parent_id)
			VALUES ($1::jurisdiction_level, $2, $2, $3::uuid) RETURNING id`,
			j.level, j.code, parent).Scan(&id); err != nil {
			t.Fatalf("seeding %s: %v", j.code, err)
		}
		ids[j.code] = id
	}

	var officerID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO users (username, email, full_name, kind, status, password_hash)
		VALUES ('ss.officer@esurvey-test.in', 'ss.officer@esurvey-test.in', 'SS Officer', 'officer', 'active',
		        '$argon2id$v=19$m=65536,t=3,p=4$c2VlZHNlZWRzZWVk$notarealhash')
		ON CONFLICT (username) DO UPDATE SET full_name = EXCLUDED.full_name
		RETURNING id`).Scan(&officerID); err != nil {
		t.Fatalf("seeding the officer: %v", err)
	}

	events := map[string]string{}
	for _, st := range []string{"A", "B"} {
		var id string
		if err := pool.QueryRow(ctx, `
			INSERT INTO survey_events (reference, calamity_type, title, district_id,
				incident_start, incident_end, survey_closes_at, state,
				declared_by, declared_at)
			VALUES ($1, 'hailstorm', $1, $2, current_date - 5, current_date - 4,
				now() + interval '30 days', 'open', $3, now())
			RETURNING id`, "SE-SS-"+st, ids["SS-"+st+"-D"], officerID).Scan(&id); err != nil {
			t.Fatalf("seeding event %s: %v", st, err)
		}
		events[st] = id

		// Queued, and dated so that no worker will ever take it.
		//
		// Claim() picks `state = 'queued' AND next_attempt_at <= now()`, and
		// this test asserts a count of queued jobs. Any worker running
		// against the same database -- a sibling test, or the API container a
		// developer has up while running the suite -- would claim the row
		// between the seed and the assertion and flip it to 'running', which
		// is what made this test fail about one run in three. An hour in the
		// future keeps it queued and unclaimable, so the count is a fact
		// about scoping rather than about who else was awake.
		if _, err := pool.Exec(ctx, `
			INSERT INTO assessment_jobs (farmer_id, farm_id, incident_date,
				village_lgd_code, state, next_attempt_at)
			VALUES ($1, $2, current_date - 4, $3, 'queued', now() + interval '1 hour')`,
			"F-SS-"+st, "FARM-SS-"+st, "SS-"+st+"-V"); err != nil {
			t.Fatalf("seeding job %s: %v", st, err)
		}
	}

	srv := &Server{
		DB:       pool,
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Identity: identity.NewStore(pool),
		Events:   event.NewStore(pool),
		Jobs:     assessjob.New(pool),
		Reports:  report.NewStore(pool),
	}
	return &twoStates{pool: pool, srv: srv, ids: ids, events: events}
}

func (f *twoStates) stateOfficerOf(state string) *rbac.Principal {
	return &rbac.Principal{
		UserID: "00000000-0000-0000-0000-00000000ss0" + strings.ToLower(state),
		Kind:   rbac.KindOfficer,
		Grants: []rbac.Grant{{
			Role: rbac.RoleStateOfficer, JurisdictionID: f.ids["SS-"+state],
			JurisdictionCode: "SS-" + state, JurisdictionLevel: rbac.LevelState,
		}},
	}
}

func call(p *rbac.Principal, h http.HandlerFunc, path string, vals map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	for k, v := range vals {
		req.SetPathValue(k, v)
	}
	req = req.WithContext(rbac.WithPrincipal(req.Context(), p))
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec
}

func TestAStateOfficerReadsOnlyTheirOwnState(t *testing.T) {
	f := seedTwoStates(t)
	a := f.stateOfficerOf("A")

	// An event by id.
	if rec := call(a, f.srv.getEvent, "/", map[string]string{"eventID": f.events["A"]}); rec.Code != http.StatusOK {
		t.Errorf("own state's event: %d %s", rec.Code, rec.Body)
	}
	if rec := call(a, f.srv.getEvent, "/", map[string]string{"eventID": f.events["B"]}); rec.Code != http.StatusNotFound {
		t.Errorf("another state's event answered %d, want 404", rec.Code)
	}

	// The village's officers.
	if rec := call(a, f.srv.villageAuthorities, "/", map[string]string{"villageCode": "SS-A-V"}); rec.Code != http.StatusOK {
		t.Errorf("own village's authorities: %d %s", rec.Code, rec.Body)
	}
	if rec := call(a, f.srv.villageAuthorities, "/", map[string]string{"villageCode": "SS-B-V"}); rec.Code != http.StatusNotFound {
		t.Errorf("another state's village authorities answered %d, want 404", rec.Code)
	}

	// Taluka coverage.
	rec := call(a, f.srv.circleCoverage, "/", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("coverage: %d %s", rec.Code, rec.Body)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "SS-A-T") {
		t.Error("coverage is missing the officer's own taluka")
	}
	if strings.Contains(body, "SS-B-T") {
		t.Error("coverage shows another state's taluka")
	}

	// The assessment backlog counts this state's queue only.
	rec = call(a, f.srv.assessmentBacklog, "/", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("backlog: %d %s", rec.Code, rec.Body)
	}
	var backlog struct {
		Queued int `json:"queued"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &backlog); err != nil {
		t.Fatal(err)
	}
	if backlog.Queued != 1 {
		t.Errorf("backlog counts %d queued; one is in this state and one in another", backlog.Queued)
	}
}

func TestAnOfficerWithNoPostingSeesNoEvents(t *testing.T) {
	f := seedTwoStates(t)
	nobody := &rbac.Principal{UserID: "nobody", Kind: rbac.KindOfficer}

	rec := call(nobody, f.srv.listEvents, "/api/v1/events", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "SE-SS-") {
		t.Error("an officer with no posting was shown events -- an empty scope read as no restriction")
	}
}

func TestTheAuditLogIsTheReadersOwnArea(t *testing.T) {
	f := seedTwoStates(t)
	ctx := context.Background()

	for _, st := range []string{"A", "B"} {
		if _, err := f.pool.Exec(ctx, `
			INSERT INTO audit_log (action, outcome, jurisdiction_code, resource_id)
			VALUES ('test.state_scope', 'allow', $1, $2)`,
			"SS-"+st+"-V", "SS-AUDIT-"+st); err != nil {
			t.Fatalf("seeding audit %s: %v", st, err)
		}
	}

	rec := call(f.stateOfficerOf("A"), f.srv.listAudit,
		"/api/v1/admin/audit?action=test.state_scope&limit=100", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d %s", rec.Code, rec.Body)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "SS-AUDIT-A") {
		t.Error("the officer's own state's entry is missing")
	}
	if strings.Contains(body, "SS-AUDIT-B") {
		t.Error("another state's audit entry is readable")
	}

	// The product administrator reads platform actions only: a state's
	// entries are client data.
	admin := &rbac.Principal{
		UserID: "admin", Kind: rbac.KindOfficer,
		Grants: []rbac.Grant{{Role: rbac.RoleSystemAdmin}},
	}
	rec = call(admin, f.srv.listAudit, "/api/v1/admin/audit?action=test.state_scope&limit=100", nil)
	if strings.Contains(rec.Body.String(), "SS-AUDIT-") {
		t.Error("the product administrator reads a state's audit entries")
	}
}

// The gap migration 0044 closes: an LGD code is unique only within a level.
// Here one code is a village in state A and a district in state B.
func TestAnAuditEntryIsPlacedByIdentityNotByASharedCode(t *testing.T) {
	f := seedTwoStates(t)
	ctx := context.Background()

	if _, err := f.pool.Exec(ctx, `
		INSERT INTO jurisdictions (level, code, name, parent_id)
		VALUES ('village', 'SS-DUP', 'SS-DUP village', $1),
		       ('district', 'SS-DUP', 'SS-DUP district', $2)`,
		f.ids["SS-A-T"], f.ids["SS-B"]); err != nil {
		t.Fatalf("seeding the shared code: %v", err)
	}

	// An entry from before 0044: a code and nothing else. It cannot be placed,
	// so no officer's scoped view claims it.
	if _, err := f.pool.Exec(ctx, `
		INSERT INTO audit_log (action, outcome, jurisdiction_code, resource_id)
		VALUES ('test.shared_code', 'allow', 'SS-DUP', 'SS-DUP-LEGACY')`); err != nil {
		t.Fatalf("seeding a legacy entry: %v", err)
	}

	rec := audit.New(f.pool, f.srv.Log)
	// A claim's entry names a village code and is placed at the village.
	if err := rec.RecordSync(ctx, audit.Event{
		Action: "test.shared_code", Outcome: audit.OutcomeAllow,
		ResourceID: "SS-DUP-VILLAGE", JurisdictionCode: "SS-DUP",
	}); err != nil {
		t.Fatal(err)
	}
	// An event's entry names its district.
	if err := rec.RecordSync(ctx, audit.Event{
		Action: "test.shared_code", Outcome: audit.OutcomeAllow,
		ResourceID: "SS-DUP-DISTRICT", JurisdictionCode: "SS-DUP",
		JurisdictionLevel: "district",
	}); err != nil {
		t.Fatal(err)
	}

	read := func(state string) string {
		r := call(f.stateOfficerOf(state), f.srv.listAudit,
			"/api/v1/admin/audit?action=test.shared_code&limit=100", nil)
		if r.Code != http.StatusOK {
			t.Fatalf("state %s: %d %s", state, r.Code, r.Body)
		}
		return r.Body.String()
	}

	a, b := read("A"), read("B")
	if !strings.Contains(a, "SS-DUP-VILLAGE") || strings.Contains(b, "SS-DUP-VILLAGE") {
		t.Errorf("the village entry is not placed in state A alone (A=%v B=%v)",
			strings.Contains(a, "SS-DUP-VILLAGE"), strings.Contains(b, "SS-DUP-VILLAGE"))
	}
	if !strings.Contains(b, "SS-DUP-DISTRICT") || strings.Contains(a, "SS-DUP-DISTRICT") {
		t.Errorf("the district entry is not placed in state B alone (A=%v B=%v)",
			strings.Contains(a, "SS-DUP-DISTRICT"), strings.Contains(b, "SS-DUP-DISTRICT"))
	}
	if strings.Contains(a, "SS-DUP-LEGACY") || strings.Contains(b, "SS-DUP-LEGACY") {
		t.Error("an entry whose code names two jurisdictions was attributed to one of them")
	}
}
