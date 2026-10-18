package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/agribid/esurvey-platform/backend/internal/identity"
	"github.com/agribid/esurvey-platform/backend/internal/rbac"
)

// The product administrator runs the product, not the states on it.

func productAdmin() *rbac.Principal {
	return &rbac.Principal{
		UserID: "00000000-0000-0000-0000-00000000ad01", Kind: rbac.KindOfficer,
		Grants: []rbac.Grant{{Role: rbac.RoleSystemAdmin}},
	}
}

func TestTheProductAdministratorAppointsStatesAndNothingBelow(t *testing.T) {
	f := seedTwoStates(t)
	ctx := context.Background()
	admin := productAdmin()

	if err := f.srv.mayPost(ctx, admin, rbac.RoleStateOfficer, f.ids["SS-A"]); err != nil {
		t.Errorf("appointing a State Officer was refused: %v", err)
	}
	if err := f.srv.mayPost(ctx, admin, rbac.RoleSystemAdmin, ""); err != nil {
		t.Errorf("adding a product administrator was refused: %v", err)
	}
	for _, role := range []rbac.Role{rbac.RoleDistrictMagistrate, rbac.RoleTehsildar, rbac.RoleTalathi} {
		if err := f.srv.mayPost(ctx, admin, role, f.ids["SS-A-D"]); !errors.Is(err, errNotDelegable) {
			t.Errorf("the product administrator could create a %s: %v", role, err)
		}
	}

	collector := &identity.User{ID: "c1", Grants: []rbac.Grant{{
		Role: rbac.RoleDistrictMagistrate, JurisdictionID: f.ids["SS-A-D"]}}}
	if err := f.srv.mayManage(ctx, admin, collector); !errors.Is(err, errNotDelegable) {
		t.Errorf("the product administrator could manage a Collector: %v", err)
	}
	stateOfficer := &identity.User{ID: "s1", Grants: []rbac.Grant{{
		Role: rbac.RoleStateOfficer, JurisdictionID: f.ids["SS-A"]}}}
	if err := f.srv.mayManage(ctx, admin, stateOfficer); err != nil {
		t.Errorf("the product administrator could not manage a State Officer: %v", err)
	}
	if err := f.srv.mayManage(ctx, admin, &identity.User{ID: admin.UserID,
		Grants: []rbac.Grant{{Role: rbac.RoleSystemAdmin}}}); !errors.Is(err, errNotDelegable) {
		t.Errorf("an administrator could manage its own account: %v", err)
	}
}

func TestTheProductAdministratorListsOnlyTheAccountsItManages(t *testing.T) {
	f := seedTwoStates(t)
	ctx := context.Background()

	mk := func(email, name string, role rbac.Role, jur string) {
		t.Helper()
		var id string
		if err := f.pool.QueryRow(ctx, `
			INSERT INTO users (kind, full_name, username, email, password_hash)
			VALUES ('officer', $1, $2, $2, 'x') RETURNING id`, name, email).Scan(&id); err != nil {
			t.Fatalf("seeding %s: %v", email, err)
		}
		if _, err := f.pool.Exec(ctx, `
			INSERT INTO user_roles (user_id, role, jurisdiction_id)
			VALUES ($1, $2::officer_role, NULLIF($3, '')::uuid)`, id, string(role), jur); err != nil {
			t.Fatalf("granting %s: %v", email, err)
		}
	}
	t.Cleanup(func() {
		_, _ = f.pool.Exec(context.Background(),
			`DELETE FROM users WHERE username LIKE 'pa.%@esurvey-test.in'`)
	})
	mk("pa.state@esurvey-test.in", "PA State Officer", rbac.RoleStateOfficer, f.ids["SS-A"])
	mk("pa.talathi@esurvey-test.in", "PA Talathi", rbac.RoleTalathi, f.ids["SS-A-V"])

	rec := call(productAdmin(), f.srv.listUsers, "/api/v1/admin/users?limit=200&q=pa.", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d %s", rec.Code, rec.Body)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "pa.state@esurvey-test.in") {
		t.Error("the State Officer is missing from the administrator's list")
	}
	if strings.Contains(body, "pa.talathi@esurvey-test.in") {
		t.Error("the administrator can see a state's own village officer")
	}
}

func TestThePlatformOverviewIsCountsNotRecords(t *testing.T) {
	f := seedTwoStates(t)
	ctx := context.Background()

	if _, err := f.pool.Exec(ctx, `
		INSERT INTO crop_loss_reports (
			reference, survey_event_id, farmer_id, farmer_name,
			village_lgd_code, village_name, sub_district_lgd_code,
			sub_district_name, district_lgd_code, district_name,
			season, crop_year, loss_category, calamity_type, occurred_at,
			reported_channel, declaration_accepted, documents_promised,
			farmer_signature_kind, farmer_signed_at, state)
		VALUES ('CLR-PA-1', $1, 'F-PA-SECRET', 'Secret Farmer Name',
			'SS-B-V', 'SS-B-V', 'SS-B-T', 'SS-B-T', 'SS-B-D', 'SS-B-D',
			'kharif', '2026-27', 'standing_crop', 'hailstorm', now() - interval '2 days',
			'farmer_app', true, '{}', 'thumb', now(), 'assigned')`, f.events["B"]); err != nil {
		t.Fatalf("seeding a claim: %v", err)
	}
	t.Cleanup(func() {
		_, _ = f.pool.Exec(context.Background(),
			`DELETE FROM crop_loss_reports WHERE reference = 'CLR-PA-1'`)
	})

	rec := call(productAdmin(), f.srv.platformOverview, "/", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d %s", rec.Code, rec.Body)
	}
	body := rec.Body.String()
	for _, secret := range []string{"Secret Farmer Name", "F-PA-SECRET", "CLR-PA-1"} {
		if strings.Contains(body, secret) {
			t.Errorf("the overview carries client data: %q", secret)
		}
	}

	var ov struct {
		States []struct {
			StateCode    string `json:"state_code"`
			WithOfficers int    `json:"with_officers"`
		} `json:"states"`
		Districts []struct {
			DistrictCode string `json:"district_code"`
			WithOfficers int    `json:"with_officers"`
		} `json:"districts"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &ov); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, s := range ov.States {
		if s.StateCode == "SS-B" {
			found = true
			if s.WithOfficers != 1 {
				t.Errorf("state SS-B counts %d claims with officers, want 1", s.WithOfficers)
			}
		}
	}
	if !found {
		t.Error("a loaded state is missing from the overview")
	}
}

func TestThePlatformHealthReportsComponentsAndLoad(t *testing.T) {
	f := seedTwoStates(t)
	f.srv.Metrics = NewMetrics()
	f.srv.Metrics.observe("GET", "/x", 200, 5e6)
	f.srv.Metrics.observe("GET", "/x", 503, 9e6)

	rec := call(productAdmin(), f.srv.platformHealth, "/", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d %s", rec.Code, rec.Body)
	}
	var h struct {
		Components []struct {
			Key    string `json:"key"`
			Status string `json:"status"`
		} `json:"components"`
		Load struct {
			Requests5m  uint64  `json:"requests_5m"`
			ErrorRate5m float64 `json:"error_rate_5m"`
		} `json:"load"`
		Database map[string]any `json:"database"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &h); err != nil {
		t.Fatal(err)
	}
	db := false
	for _, c := range h.Components {
		if c.Key == "postgres" && c.Status == "up" {
			db = true
		}
	}
	if !db {
		t.Errorf("the database is not reported up: %s", rec.Body)
	}
	if h.Load.Requests5m != 2 || h.Load.ErrorRate5m != 0.5 {
		t.Errorf("load = %d requests, %.2f errors; want 2 and 0.50",
			h.Load.Requests5m, h.Load.ErrorRate5m)
	}
	if h.Database["pool_max"] == nil {
		t.Error("the database pool is not reported")
	}
}
