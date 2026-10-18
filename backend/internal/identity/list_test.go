package identity

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/agribid/esurvey-platform/backend/internal/platform/page"
	"github.com/agribid/esurvey-platform/backend/internal/rbac"
)

// Listing officer accounts. Two things are worth testing and one of them is
// subtle: the scope, and the fact that a user holding two roles must be
// counted once. The role join multiplies a user by their grants, so a total
// computed without DISTINCT promises rows that are not there.

func testIdentity(t *testing.T) (*Store, *pgxpool.Pool) {
	t.Helper()

	url := os.Getenv("ESURVEY_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set ESURVEY_TEST_DATABASE_URL to run the officer listing tests")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// A small pool on purpose. pgxpool defaults MaxConns to the core count --
	// sixteen here -- and `go test ./...` runs these packages in parallel
	// against one Postgres that also has the running API attached. Four pools
	// of sixteen exhausts max_connections, and the symptom is not an error
	// but Begin blocking until the package times out. A test needs two.
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
		c := context.Background()
		_, _ = pool.Exec(c,
			`DELETE FROM user_roles WHERE user_id IN
			 (SELECT id FROM users WHERE username LIKE 'tst.%')`)
		_, _ = pool.Exec(c, `DELETE FROM users WHERE username LIKE 'tst.%'`)
	}
	clean()
	t.Cleanup(clean)

	return NewStore(pool), pool
}

const (
	villageA = "TI-VILLAGE-A"
	villageB = "TI-VILLAGE-B"
)

func seedJurisdictions(t *testing.T, pool *pgxpool.Pool) map[string]string {
	t.Helper()
	ctx := context.Background()

	rows := []struct{ level, code, name, parent string }{
		{"state", "TI-ST", "Test State", ""},
		{"district", "TI-DI", "Test District", "TI-ST"},
		{"sub_district", "TI-SD", "Test Taluka", "TI-DI"},
		{"village", villageA, "Test Village A", "TI-SD"},
		{"village", villageB, "Test Village B", "TI-SD"},
	}

	ids := map[string]string{}
	for _, r := range rows {
		var parent any
		if r.parent != "" {
			parent = ids[r.parent]
		}
		var id string
		err := pool.QueryRow(ctx, `
			INSERT INTO jurisdictions (level, code, name, parent_id)
			VALUES ($1::jurisdiction_level, $2, $3, $4::uuid)
			ON CONFLICT (level, code) DO UPDATE SET name = EXCLUDED.name
			RETURNING id::text`,
			r.level, r.code, r.name, parent).Scan(&id)
		if err != nil {
			t.Fatalf("seeding %s: %v", r.code, err)
		}
		ids[r.code] = id
	}

	t.Cleanup(func() {
		c := context.Background()
		for i := len(rows) - 1; i >= 0; i-- {
			_, _ = pool.Exec(c, `DELETE FROM jurisdictions WHERE code = $1`, rows[i].code)
		}
	})
	return ids
}

// officer creates an account posted to one jurisdiction.
func officer(
	t *testing.T, pool *pgxpool.Pool, username, name string,
	role rbac.Role, jurisdictionID string,
) string {
	t.Helper()
	ctx := context.Background()

	var id string
	if err := pool.QueryRow(ctx, `
		INSERT INTO users (kind, full_name, username, email, password_hash)
		VALUES ('officer', $1, $2 || '@esurvey-test.in', $2 || '@esurvey-test.in', 'x')
		RETURNING id::text`, name, username).Scan(&id); err != nil {
		t.Fatalf("creating %s: %v", username, err)
	}

	if role != "" {
		if _, err := pool.Exec(ctx, `
			INSERT INTO user_roles (user_id, role, jurisdiction_id, granted_by)
			VALUES ($1::uuid, $2::officer_role, $3::uuid, $1::uuid)`,
			id, string(role), jurisdictionID); err != nil {
			t.Fatalf("granting %s to %s: %v", role, username, err)
		}
	}
	return id
}

func TestOfficersArePagedWithATrueTotal(t *testing.T) {
	s, pool := testIdentity(t)
	ids := seedJurisdictions(t, pool)

	for i := 0; i < 7; i++ {
		officer(t, pool, fmt.Sprintf("tst.a%d", i), fmt.Sprintf("Officer A%d", i),
			rbac.RoleTalathi, ids[villageA])
	}

	r, err := s.List(context.Background(), ListFilter{
		Kind:            rbac.KindOfficer,
		JurisdictionIDs: []string{ids[villageA]},
		Page:            page.Params{Limit: 3},
	})
	if err != nil {
		t.Fatal(err)
	}
	if r.Total != 7 {
		t.Errorf("total = %d, want 7", r.Total)
	}
	if len(r.Items) != 3 {
		t.Errorf("returned %d, want 3", len(r.Items))
	}
	if !r.HasMore {
		t.Error("HasMore is false with four more to come")
	}
}

func TestAnOfficerWithTwoRolesIsCountedOnce(t *testing.T) {
	// The role join multiplies a user by their grants. Without DISTINCT the
	// total says two where there is one person, and the pager offers a page
	// that comes back short.
	s, pool := testIdentity(t)
	ids := seedJurisdictions(t, pool)

	id := officer(t, pool, "tst.dual", "Dual Role", rbac.RoleTalathi, ids[villageA])
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO user_roles (user_id, role, jurisdiction_id, granted_by)
		VALUES ($1::uuid, 'krushi_sahayak'::officer_role, $2::uuid, $1::uuid)`,
		id, ids[villageA]); err != nil {
		t.Fatal(err)
	}

	r, err := s.List(context.Background(), ListFilter{
		Kind:            rbac.KindOfficer,
		JurisdictionIDs: []string{ids[villageA]},
		Page:            page.Params{Limit: 50},
	})
	if err != nil {
		t.Fatal(err)
	}
	if r.Total != 1 {
		t.Errorf("total = %d for one person holding two roles, want 1", r.Total)
	}
	if len(r.Items) != 1 {
		t.Errorf("returned %d rows for one person, want 1", len(r.Items))
	}
}

func TestAnOfficerSeesOnlyTheirOwnReach(t *testing.T) {
	// An account listing that leaks shows who is posted where in another
	// district, which is a map of the administration.
	s, pool := testIdentity(t)
	ids := seedJurisdictions(t, pool)

	officer(t, pool, "tst.here", "Posted Here", rbac.RoleTalathi, ids[villageA])
	officer(t, pool, "tst.elsewhere", "Posted Elsewhere", rbac.RoleTalathi, ids[villageB])

	r, err := s.List(context.Background(), ListFilter{
		Kind:            rbac.KindOfficer,
		JurisdictionIDs: []string{ids[villageA]},
		Page:            page.Params{Limit: 50},
	})
	if err != nil {
		t.Fatal(err)
	}
	if r.Total != 1 {
		t.Fatalf("total = %d, want 1", r.Total)
	}
	if r.Items[0].Username != "tst.here@esurvey-test.in" {
		t.Errorf("the wrong officer was returned: %q", r.Items[0].Username)
	}
}

func TestAPostingAboveAVillageIncludesIt(t *testing.T) {
	// A Tehsildar posted to the taluka sees the officers in its villages.
	// This is the ancestor chain doing its job, and getting it wrong in the
	// other direction hides staff from the person responsible for them.
	s, pool := testIdentity(t)
	ids := seedJurisdictions(t, pool)

	officer(t, pool, "tst.village", "Village Officer", rbac.RoleTalathi, ids[villageA])

	r, err := s.List(context.Background(), ListFilter{
		Kind:            rbac.KindOfficer,
		JurisdictionIDs: []string{ids["TI-SD"]},
		Page:            page.Params{Limit: 50},
	})
	if err != nil {
		t.Fatal(err)
	}
	if r.Total != 1 {
		t.Errorf("a taluka posting sees %d of its village officers, want 1", r.Total)
	}
}

func TestSearchNarrowsBothRowsAndTotal(t *testing.T) {
	s, pool := testIdentity(t)
	ids := seedJurisdictions(t, pool)

	officer(t, pool, "tst.pawar", "S. Pawar", rbac.RoleTalathi, ids[villageA])
	officer(t, pool, "tst.jagtap", "M. Jagtap", rbac.RoleKrushiSahayak, ids[villageA])
	officer(t, pool, "tst.kale", "P. Kale", rbac.RoleTalathi, ids[villageA])

	for _, q := range []string{"Pawar", "pawar", "tst.paw"} {
		r, err := s.List(context.Background(), ListFilter{
			Kind:            rbac.KindOfficer,
			Query:           q,
			JurisdictionIDs: []string{ids[villageA]},
			Page:            page.Params{Limit: 50},
		})
		if err != nil {
			t.Fatal(err)
		}
		if r.Total != 1 {
			t.Errorf("searching %q matched %d, want 1", q, r.Total)
		}
	}
}

func TestAWildcardInASearchIsNotAWildcard(t *testing.T) {
	// The escaping page.Like applies. Without it, "%" would list every
	// officer the caller can reach — a search box as an export button.
	s, pool := testIdentity(t)
	ids := seedJurisdictions(t, pool)

	officer(t, pool, "tst.one", "One Officer", rbac.RoleTalathi, ids[villageA])

	for _, q := range []string{"%", "_"} {
		r, err := s.List(context.Background(), ListFilter{
			Kind:            rbac.KindOfficer,
			Query:           q,
			JurisdictionIDs: []string{ids[villageA]},
			Page:            page.Params{Limit: 50},
		})
		if err != nil {
			t.Fatal(err)
		}
		if r.Total != 0 {
			t.Errorf("searching %q matched %d rows; wildcards are not escaped",
				q, r.Total)
		}
	}
}

func TestGrantsAreLoadedForEachRow(t *testing.T) {
	// The console shows an officer's posting beside their name. A listing
	// that returns accounts without grants shows a roll of people with no
	// indication of where any of them work.
	s, pool := testIdentity(t)
	ids := seedJurisdictions(t, pool)

	officer(t, pool, "tst.granted", "Granted Officer", rbac.RoleTalathi, ids[villageA])

	r, err := s.List(context.Background(), ListFilter{
		Kind:            rbac.KindOfficer,
		JurisdictionIDs: []string{ids[villageA]},
		Page:            page.Params{Limit: 50},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Items) != 1 {
		t.Fatalf("returned %d rows", len(r.Items))
	}
	if len(r.Items[0].Grants) == 0 {
		t.Error("the officer came back with no grants; their posting is invisible")
	}
}

func TestItemsIsNeverNull(t *testing.T) {
	s, pool := testIdentity(t)
	ids := seedJurisdictions(t, pool)

	r, err := s.List(context.Background(), ListFilter{
		Kind:            rbac.KindOfficer,
		Query:           "nobody-by-this-name",
		JurisdictionIDs: []string{ids[villageA]},
		Page:            page.Params{Limit: 50},
	})
	if err != nil {
		t.Fatal(err)
	}
	if r.Items == nil {
		t.Error("Items is nil; a client mapping over it would fail")
	}
}
