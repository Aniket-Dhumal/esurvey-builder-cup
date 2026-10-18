package api

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/agribid/esurvey-platform/backend/internal/identity"
	"github.com/agribid/esurvey-platform/backend/internal/rbac"
)

// The state appoints each district's authority, inside its own state, and
// nothing else.
func TestAStateOfficerAppointsDistrictAuthoritiesInTheirOwnState(t *testing.T) {
	url := os.Getenv("ESURVEY_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set ESURVEY_TEST_DATABASE_URL to run the delegation test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Skipf("no database: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := pool.Ping(ctx); err != nil {
		t.Skipf("no database: %v", err)
	}

	clean := func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM jurisdictions WHERE code LIKE 'SD-%'`)
	}
	clean()
	t.Cleanup(clean)

	ids := map[string]string{}
	for _, j := range []struct{ level, code, parent string }{
		{"state", "SD-S1", ""},
		{"district", "SD-D1", "SD-S1"},
		{"sub_district", "SD-T1", "SD-D1"},
		{"state", "SD-S2", ""},
		{"district", "SD-D2", "SD-S2"},
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

	s := &Server{Identity: identity.NewStore(pool)}
	state := &rbac.Principal{
		UserID: "state-1", Kind: rbac.KindOfficer,
		Grants: []rbac.Grant{{
			Role: rbac.RoleStateOfficer, JurisdictionID: ids["SD-S1"],
			JurisdictionCode: "SD-S1", JurisdictionLevel: rbac.LevelState,
		}},
	}

	if !state.Can(rbac.PermUserCreate) {
		t.Fatal("a state officer cannot reach the account form at all")
	}
	if err := s.mayPost(ctx, state, rbac.RoleDistrictMagistrate, ids["SD-D1"]); err != nil {
		t.Errorf("appointing a Collector in their own state was refused: %v", err)
	}

	for name, c := range map[string]struct {
		role rbac.Role
		jur  string
	}{
		"a Collector in another state":    {rbac.RoleDistrictMagistrate, ids["SD-D2"]},
		"a Tehsildar, past the Collector": {rbac.RoleTehsildar, ids["SD-T1"]},
		"another state officer, a peer":   {rbac.RoleStateOfficer, ids["SD-S1"]},
		"an unposted administrator":       {rbac.RoleSystemAdmin, ""},
	} {
		if err := s.mayPost(ctx, state, c.role, c.jur); !errors.Is(err, errNotDelegable) {
			t.Errorf("%s: err = %v, want a refusal", name, err)
		}
	}
}
