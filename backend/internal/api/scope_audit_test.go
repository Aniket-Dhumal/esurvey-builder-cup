package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/agribid/esurvey-platform/backend/internal/audit"
	"github.com/agribid/esurvey-platform/backend/internal/blob"
	"github.com/agribid/esurvey-platform/backend/internal/rbac"
	"github.com/agribid/esurvey-platform/backend/internal/report"
	"github.com/agribid/esurvey-platform/backend/internal/survey"
)

// The whole-application scope review, route by route. Each test is one gap
// that let an officer -- or a farmer -- reach past their own area.

func officerAt(role rbac.Role, level rbac.Level, id, code string) *rbac.Principal {
	return &rbac.Principal{
		UserID: "00000000-0000-0000-0000-0000000000aa",
		Kind:   rbac.KindOfficer,
		Grants: []rbac.Grant{{
			Role: role, JurisdictionID: id, JurisdictionCode: code,
			JurisdictionLevel: level,
		}},
	}
}

func aFarmerPrincipal() *rbac.Principal {
	return &rbac.Principal{
		UserID: "00000000-0000-0000-0000-0000000000ff",
		Kind:   rbac.KindFarmer, FarmerID: "F-SS-A",
		Grants: []rbac.Grant{{Role: rbac.RoleFarmer}},
	}
}

// Officer findings, the brief and the field measurements reach a farmer only
// through /progress, after both village officers have recorded.
func TestAFarmerCannotReadTheOfficersFindingsMidReview(t *testing.T) {
	s := &Server{Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	farmer := aFarmerPrincipal()

	for name, h := range map[string]http.HandlerFunc{
		"findings":     s.reportFindings,
		"brief":        s.verificationBrief,
		"measurements": s.listMeasurements,
	} {
		rec := call(farmer, h, "/", map[string]string{"reportID": "any"})
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s answered a farmer with %d, want 403", name, rec.Code)
		}
	}
}

func TestTheJurisdictionTreeIsWalkedOnlyInsideTheCallersArea(t *testing.T) {
	f := seedTwoStates(t)
	a := f.stateOfficerOf("A")

	get := func(path string) string {
		rec := call(a, f.srv.listJurisdictions, path, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", path, rec.Code, rec.Body)
		}
		return rec.Body.String()
	}

	if body := get("/?parent_id=" + f.ids["SS-A-D"]); !strings.Contains(body, "SS-A-T") {
		t.Error("the officer's own district's talukas are missing")
	}
	if body := get("/?parent_id=" + f.ids["SS-B-D"]); strings.Contains(body, "SS-B-T") {
		t.Error("another state's talukas are listed")
	}
	if body := get("/"); strings.Contains(body, "SS-B") {
		t.Error("the roots -- every state -- are listed to an officer")
	}
}

func TestOpenEventsAreCountedForTheCallersOwnArea(t *testing.T) {
	f := seedTwoStates(t)

	count := func(p *rbac.Principal) int {
		rec := call(p, f.srv.overview, "/", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("overview: %d %s", rec.Code, rec.Body)
		}
		var ov struct {
			OpenEvents int `json:"open_events"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &ov); err != nil {
			t.Fatal(err)
		}
		return ov.OpenEvents
	}

	// Only this test's events are counted by reference below, so compare
	// across callers rather than against a fixed total.
	state := count(f.stateOfficerOf("A"))
	tehsildar := count(officerAt(rbac.RoleTehsildar, rbac.LevelSubDistrict,
		f.ids["SS-A-T"], "SS-A-T"))
	if state != 1 {
		t.Errorf("state officer counts %d open events in their state, want 1", state)
	}
	if tehsildar != 1 {
		t.Errorf("a Tehsildar counts %d open events over their taluka, want 1 -- "+
			"an event is declared on the district above them", tehsildar)
	}
	if n := count(aFarmerPrincipal()); n != 0 {
		t.Errorf("a farmer is shown %d open events across every state", n)
	}
}

func TestAnOfficerWithNoPostingReachesNoGrievance(t *testing.T) {
	f := seedTwoStates(t)
	ctx := context.Background()

	var reportID string
	if err := f.pool.QueryRow(ctx, `
		INSERT INTO crop_loss_reports (
			reference, survey_event_id, farmer_id, farmer_name,
			village_lgd_code, village_name, sub_district_lgd_code,
			sub_district_name, district_lgd_code, district_name,
			season, crop_year, loss_category, calamity_type, occurred_at,
			reported_channel, declaration_accepted, documents_promised,
			farmer_signature_kind, farmer_signed_at, state)
		VALUES ('CLR-SS-B', $1, 'F-SS-B', 'SS Farmer',
			'SS-B-V', 'SS-B-V', 'SS-B-T', 'SS-B-T', 'SS-B-D', 'SS-B-D',
			'kharif', '2026-27', 'standing_crop', 'hailstorm', now() - interval '4 days',
			'farmer_app', true, '{}', 'thumb', now(), 'adjudicated')
		RETURNING id`, f.events["B"]).Scan(&reportID); err != nil {
		t.Fatalf("seeding the report: %v", err)
	}
	var grievanceID string
	if err := f.pool.QueryRow(ctx, `
		INSERT INTO grievances (reference, report_id, farmer_id, farmer_name,
			village_lgd_code, ground, statement, due_at)
		VALUES ('GRV-SS-B', $1, 'F-SS-B', 'SS Farmer', 'SS-B-V',
			'damage_percent_low', 'the whole field was lost, not half', now() + interval '15 days')
		RETURNING id`, reportID).Scan(&grievanceID); err != nil {
		t.Fatalf("seeding the grievance: %v", err)
	}
	t.Cleanup(func() {
		c := context.Background()
		_, _ = f.pool.Exec(c, `DELETE FROM grievances WHERE reference = 'GRV-SS-B'`)
		_, _ = f.pool.Exec(c, `DELETE FROM crop_loss_reports WHERE reference = 'CLR-SS-B'`)
	})

	f.srv.Reports = report.NewStore(f.pool)
	f.srv.Audit = audit.New(f.pool, f.srv.Log)
	nobody := &rbac.Principal{
		UserID: "00000000-0000-0000-0000-0000000000bb", Kind: rbac.KindOfficer,
		Grants: []rbac.Grant{{Role: rbac.RoleTehsildar}},
	}
	rec := call(nobody, f.srv.getGrievance, "/", map[string]string{"grievanceID": grievanceID})
	if rec.Code == http.StatusOK {
		t.Fatalf("an officer with no posting opened another state's grievance: %s", rec.Body)
	}
	if rec := call(f.stateOfficerOf("A"), f.srv.getGrievance, "/",
		map[string]string{"grievanceID": grievanceID}); rec.Code == http.StatusOK {
		t.Error("an officer in another state opened the grievance")
	}
	if rec := call(f.stateOfficerOf("B"), f.srv.getGrievance, "/",
		map[string]string{"grievanceID": grievanceID}); rec.Code != http.StatusOK {
		t.Errorf("the state's own officer could not open it: %d %s", rec.Code, rec.Body)
	}
}

func TestABoundaryIsConfirmedOnlyByAnOfficerOfItsArea(t *testing.T) {
	f := seedTwoStates(t)
	ctx := context.Background()

	if _, err := f.pool.Exec(ctx, `
		INSERT INTO parcel_boundaries (farm_id, farmer_id, survey_number,
			village_lgd_code, geom, source)
		VALUES ('FARM-SS-B', 'F-SS-B', '1', 'SS-B-V',
			ST_Multi(ST_GeomFromText('POLYGON((75.81 18.16,75.82 18.16,75.82 18.17,75.81 18.17,75.81 18.16))', 4326)),
			'field_traced')`); err != nil {
		t.Fatalf("seeding the boundary: %v", err)
	}
	t.Cleanup(func() {
		_, _ = f.pool.Exec(context.Background(),
			`DELETE FROM parcel_boundaries WHERE farm_id = 'FARM-SS-B'`)
	})

	dir, err := blob.NewDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f.srv.Captures = survey.NewStore(f.pool, dir)
	f.srv.Audit = audit.New(f.pool, f.srv.Log)

	talathiA := officerAt(rbac.RoleTalathi, rbac.LevelVillage, f.ids["SS-A-V"], "SS-A-V")
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(nil))
	req.SetPathValue("farmID", "FARM-SS-B")
	req = req.WithContext(rbac.WithPrincipal(req.Context(), talathiA))
	rec := httptest.NewRecorder()
	f.srv.confirmBoundary(rec, req)
	if rec.Code == http.StatusOK {
		t.Fatal("a Talathi in another state confirmed the boundary")
	}

	var confirmed bool
	if err := f.pool.QueryRow(ctx, `
		SELECT confirmed_by IS NOT NULL FROM parcel_boundaries WHERE farm_id = 'FARM-SS-B'`).
		Scan(&confirmed); err != nil {
		t.Fatal(err)
	}
	if confirmed {
		t.Error("the refused confirmation was written anyway")
	}
}
