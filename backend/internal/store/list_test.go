package store

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

// Listing assessments: paging, search, and the scope that decides who sees
// what. The scope is the part worth testing hardest — a rollup or a listing
// that leaks does not show a neighbouring row, it shows a count that quietly
// includes it.

func testStore(t *testing.T) (*Store, *pgxpool.Pool) {
	t.Helper()

	url := os.Getenv("ESURVEY_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set ESURVEY_TEST_DATABASE_URL to run the assessment listing tests")
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
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM assessments WHERE farmer_id LIKE 'T-%'`)
	}
	clean()
	t.Cleanup(clean)

	seedJurisdictions(t, pool)
	return New(pool), pool
}

// Two villages under one taluka, created by the fixture rather than taken
// from the reference import.
//
// The tests need a second village to prove that one does not leak into the
// other's listing, and a test that depends on 44,911 imported rows being
// present is a test that skips on a fresh database — which is exactly where
// it is most worth running.
const (
	villageA = "T-VILLAGE-A"
	villageB = "T-VILLAGE-B"
)

// seedJurisdictions builds state → district → taluka → two villages.
//
// The ancestor chain is maintained by a trigger, so inserting in order is
// enough; the scope predicate then matches on ancestor_ids without this test
// knowing how that works.
func seedJurisdictions(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()

	rows := []struct{ level, code, name, parent string }{
		{"state", "T-ST", "Test State", ""},
		{"district", "T-DI", "Test District", "T-ST"},
		{"sub_district", "T-SD", "Test Taluka", "T-DI"},
		{"village", villageA, "Test Village A", "T-SD"},
		{"village", villageB, "Test Village B", "T-SD"},
	}

	for _, r := range rows {
		var parent any
		if r.parent != "" {
			var id string
			if err := pool.QueryRow(ctx,
				`SELECT id::text FROM jurisdictions WHERE code = $1`, r.parent).
				Scan(&id); err != nil {
				t.Fatalf("parent %s missing: %v", r.parent, err)
			}
			parent = id
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO jurisdictions (level, code, name, parent_id)
			VALUES ($1::jurisdiction_level, $2, $3, $4::uuid)
			ON CONFLICT (level, code) DO NOTHING`,
			r.level, r.code, r.name, parent); err != nil {
			t.Fatalf("seeding %s %s: %v", r.level, r.code, err)
		}
	}

	t.Cleanup(func() {
		// Children first: parent_id is ON DELETE RESTRICT.
		for i := len(rows) - 1; i >= 0; i-- {
			_, _ = pool.Exec(context.Background(),
				`DELETE FROM jurisdictions WHERE code = $1`, rows[i].code)
		}
	})
}

func seed(t *testing.T, pool *pgxpool.Pool, n int, village, crop string) {
	t.Helper()
	ctx := context.Background()

	for i := 0; i < n; i++ {
		_, err := pool.Exec(ctx, `
			INSERT INTO assessments (
				farmer_id, farm_id, survey_number, village_lgd_code,
				sowing_date, incident_date, owner_extent_hectares,
				crop_name, assessment, satellite, terrain,
				triage_tier, crop_presence, damage_percent, created_at
			) VALUES (
				$1, $2, $3, $4,
				'2025-06-20', '2025-10-12', 1.6,
				$5, '{}'::jsonb, '{}'::jsonb, '{}'::jsonb,
				'GREEN_ROUTINE', 'confirmed', NULL, now() - make_interval(mins => $6)
			)`,
			fmt.Sprintf("T-%s-%d", village, i),
			fmt.Sprintf("MH%s%04d", village, i),
			fmt.Sprintf("%d", 400+i),
			village, crop, i)
		if err != nil {
			t.Fatalf("seeding assessment %d: %v", i, err)
		}
	}
}

// scopeFor resolves a village to the jurisdiction ids an officer posted there
// would carry.
func scopeFor(t *testing.T, pool *pgxpool.Pool, village string) rbac.Scope {
	t.Helper()
	var id string
	err := pool.QueryRow(context.Background(),
		`SELECT id::text FROM jurisdictions WHERE level = 'village' AND code = $1`,
		village).Scan(&id)
	if err != nil {
		t.Fatalf("village %s was not seeded: %v", village, err)
	}
	return rbac.Scope{JurisdictionIDs: []string{id}}
}

func TestListPagesWithATrueTotal(t *testing.T) {
	// The total is the count of matching rows, not the size of the page. A
	// console showing the page size under that name cannot know whether to
	// offer a next page.
	s, pool := testStore(t)
	seed(t, pool, 12, villageA, "Soybean")
	scope := scopeFor(t, pool, villageA)

	first, err := s.List(context.Background(), Filter{
		Scope: scope,
		Page:  page.Params{Limit: 5, Offset: 0},
	})
	if err != nil {
		t.Fatal(err)
	}

	if first.Total != 12 {
		t.Errorf("total = %d, want 12", first.Total)
	}
	if len(first.Items) != 5 {
		t.Errorf("returned %d rows, want 5", len(first.Items))
	}
	if !first.HasMore {
		t.Error("HasMore is false on the first of three pages")
	}
}

func TestTheLastPageDoesNotOfferAnother(t *testing.T) {
	// Off by one here is a pager that offers a page which comes back empty.
	s, pool := testStore(t)
	seed(t, pool, 12, villageA, "Soybean")
	scope := scopeFor(t, pool, villageA)

	last, err := s.List(context.Background(), Filter{
		Scope: scope,
		Page:  page.Params{Limit: 5, Offset: 10},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(last.Items) != 2 {
		t.Errorf("returned %d rows, want 2", len(last.Items))
	}
	if last.HasMore {
		t.Error("HasMore is true on the last page")
	}
}

func TestPagesDoNotOverlapOrSkip(t *testing.T) {
	// Ordering has to be total. Paging over rows that share a timestamp with
	// no tiebreak shows some twice and others not at all.
	s, pool := testStore(t)
	seed(t, pool, 12, villageA, "Soybean")
	scope := scopeFor(t, pool, villageA)

	seen := map[string]bool{}
	for offset := 0; offset < 12; offset += 5 {
		p, err := s.List(context.Background(), Filter{
			Scope: scope,
			Page:  page.Params{Limit: 5, Offset: offset},
		})
		if err != nil {
			t.Fatal(err)
		}
		for _, a := range p.Items {
			if seen[a.ID] {
				t.Errorf("assessment %s appeared on two pages", a.ID)
			}
			seen[a.ID] = true
		}
	}
	if len(seen) != 12 {
		t.Errorf("paging saw %d of 12 rows", len(seen))
	}
}

func TestSearchNarrowsTheTotalAsWellAsThePage(t *testing.T) {
	// A search that filters the rows but not the count leaves a pager
	// promising pages that do not exist.
	s, pool := testStore(t)
	seed(t, pool, 6, villageA, "Soybean")
	seed(t, pool, 4, villageB, "Cotton")
	scope := rbac.Scope{JurisdictionIDs: append(
		scopeFor(t, pool, villageA).JurisdictionIDs,
		scopeFor(t, pool, villageB).JurisdictionIDs...)}

	all, err := s.List(context.Background(), Filter{Scope: scope,
		Page: page.Params{Limit: 50}})
	if err != nil {
		t.Fatal(err)
	}
	if all.Total != 10 {
		t.Fatalf("unfiltered total = %d, want 10", all.Total)
	}

	cotton, err := s.List(context.Background(), Filter{Scope: scope,
		Page: page.Params{Limit: 50, Query: "Cotton"}})
	if err != nil {
		t.Fatal(err)
	}
	if cotton.Total != 4 {
		t.Errorf("search total = %d, want 4", cotton.Total)
	}
	if len(cotton.Items) != 4 {
		t.Errorf("search returned %d rows, want 4", len(cotton.Items))
	}
	// Summary does not carry the crop, so the check is on the village those
	// rows were seeded into. Worth noting: crop_name is searchable and not
	// returned, so an officer can find a row by crop and not see the crop
	// that matched.
	for _, a := range cotton.Items {
		if a.VillageLGDCode != villageB {
			t.Errorf("a search for Cotton returned a row from %s", a.VillageLGDCode)
		}
	}
}

func TestSearchMatchesTheFieldsAnOfficerActuallyHas(t *testing.T) {
	// Farm id from AgriStack, survey number off a 7/12 extract, or the crop.
	// Substring in every case, which is what the trigram indexes serve.
	s, pool := testStore(t)
	seed(t, pool, 3, villageA, "Soybean")
	scope := scopeFor(t, pool, villageA)

	// Derived from the fixture rather than written out, so a change to the
	// seed cannot leave these searching for something that no longer exists
	// and passing because nothing does.
	queries := []string{
		"MH" + villageA, // farm id, as AgriStack issues it
		"40",            // part of a survey number
		"Soy",           // the crop
		"T-" + villageA, // the farmer id
	}
	for _, q := range queries {
		r, err := s.List(context.Background(), Filter{Scope: scope,
			Page: page.Params{Limit: 50, Query: q}})
		if err != nil {
			t.Fatal(err)
		}
		if r.Total == 0 {
			t.Errorf("searching %q found nothing", q)
		}
	}

	none, err := s.List(context.Background(), Filter{Scope: scope,
		Page: page.Params{Limit: 50, Query: "zzz-no-such-thing"}})
	if err != nil {
		t.Fatal(err)
	}
	if none.Total != 0 {
		t.Errorf("a nonsense search matched %d rows", none.Total)
	}
}

func TestAWildcardInASearchIsNotAWildcard(t *testing.T) {
	// page.Like escapes them. Without that, "%" matches everything and a
	// search box becomes a way to dump the table.
	s, pool := testStore(t)
	seed(t, pool, 5, villageA, "Soybean")
	scope := scopeFor(t, pool, villageA)

	for _, q := range []string{"%", "_", "%%"} {
		r, err := s.List(context.Background(), Filter{Scope: scope,
			Page: page.Params{Limit: 50, Query: q}})
		if err != nil {
			t.Fatal(err)
		}
		if r.Total != 0 {
			t.Errorf("searching %q matched %d rows; wildcards are not escaped",
				q, r.Total)
		}
	}
}

func TestAnOfficerSeesOnlyTheirOwnVillages(t *testing.T) {
	// The one that matters. Every other test here is about a pager being
	// tidy; this is about a farmer's record reaching someone with no business
	// seeing it.
	s, pool := testStore(t)
	seed(t, pool, 5, villageA, "Soybean")
	seed(t, pool, 3, villageB, "Cotton")

	onlyA, err := s.List(context.Background(), Filter{
		Scope: scopeFor(t, pool, villageA),
		Page:  page.Params{Limit: 50},
	})
	if err != nil {
		t.Fatal(err)
	}
	if onlyA.Total != 5 {
		t.Errorf("an officer in one village sees %d assessments, want 5", onlyA.Total)
	}
	for _, a := range onlyA.Items {
		if a.VillageLGDCode != villageA {
			t.Errorf("village %s leaked into a listing scoped to %s",
				a.VillageLGDCode, villageA)
		}
	}
}

func TestAnEmptyScopeSeesNothing(t *testing.T) {
	// An officer with no posting is not an officer with a global view. This
	// is the failure mode where a missing grant becomes unrestricted access.
	s, pool := testStore(t)
	seed(t, pool, 5, villageA, "Soybean")

	r, err := s.List(context.Background(), Filter{
		Scope: rbac.Scope{},
		Page:  page.Params{Limit: 50},
	})
	if err != nil {
		t.Fatal(err)
	}
	if r.Total != 0 || len(r.Items) != 0 {
		t.Errorf("an empty scope returned %d rows (total %d)", len(r.Items), r.Total)
	}
}

func TestAFarmerSeesOnlyTheirOwn(t *testing.T) {
	s, pool := testStore(t)
	seed(t, pool, 4, villageA, "Soybean")

	mine := "T-" + villageA + "-2"
	r, err := s.List(context.Background(), Filter{
		Scope: rbac.Scope{FarmerID: mine},
		Page:  page.Params{Limit: 50},
	})
	if err != nil {
		t.Fatal(err)
	}
	if r.Total != 1 {
		t.Fatalf("a farmer sees %d assessments, want 1", r.Total)
	}
	if r.Items[0].FarmerID != mine {
		t.Errorf("returned %s for a scope of %s", r.Items[0].FarmerID, mine)
	}
}

func TestItemsIsNeverNull(t *testing.T) {
	// A JSON null becomes a crash in a client that maps over it.
	s, pool := testStore(t)
	_ = pool

	r, err := s.List(context.Background(), Filter{
		Scope: rbac.Scope{FarmerID: "T-nobody"},
		Page:  page.Params{Limit: 50},
	})
	if err != nil {
		t.Fatal(err)
	}
	if r.Items == nil {
		t.Error("Items is nil; a client mapping over it would fail")
	}
}
