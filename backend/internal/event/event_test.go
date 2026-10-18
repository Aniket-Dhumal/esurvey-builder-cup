package event

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// The package documents one rule: a farmer may be surveyed against an event
// only if their land sits inside a taluka or circle that event names, and
// only while the event is open. These tests are that sentence, taken apart.
//
// Two of them exist because getting them wrong is not a bug in a listing. An
// area outside the declaring district would let one Collector notify a
// calamity in another Collector's jurisdiction. An event whose areas resolve
// to no villages opens to nobody, and every farmer under it is told they are
// not covered by a notification that names their taluka.

func testEvents(t *testing.T) (*Store, *pgxpool.Pool) {
	t.Helper()

	url := os.Getenv("ESURVEY_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set ESURVEY_TEST_DATABASE_URL to run the survey event tests")
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

	clean := func() {
		c := context.Background()
		// Events first: survey_event_areas holds jurisdictions with RESTRICT.
		_, _ = pool.Exec(c, `DELETE FROM survey_events WHERE reference LIKE 'SE-%-TE-%'`)
		_, _ = pool.Exec(c, `DELETE FROM jurisdictions WHERE code LIKE 'TE-%'`)
		// The reference counter outlives the events it numbered -- that is the
		// point of it -- so a suite that deletes its districts takes their
		// counters with them, or the next run starts at -002.
		_, _ = pool.Exec(c, `DELETE FROM event_reference_counters c
			WHERE NOT EXISTS (SELECT 1 FROM jurisdictions j WHERE j.id = c.district_id)`)
		// Officers last: declared_by is ON DELETE SET NULL, and nulling it
		// under a declared event would trip declared_has_declarer.
		_, _ = pool.Exec(c, `DELETE FROM users WHERE username LIKE 'te.%'`)
	}
	clean()
	t.Cleanup(clean)

	return NewStore(pool), pool
}

// The fixture, by code. Two districts, so "outside the declaring district" is
// a case rather than a hypothetical.
const (
	districtOne = "TE-D1"
	districtTwo = "TE-D2"

	talukaWithVillages = "TE-T1" // holds circleOne, villageInCircle, villageOutsideCircle
	talukaEmpty        = "TE-T2" // named, but resolves to nobody
	talukaOtherDistrct = "TE-T3" // inside districtTwo

	circleOne = "TE-C1"

	villageInCircle      = "TE-V1" // under circleOne, under talukaWithVillages
	villageOutsideCircle = "TE-V2" // under talukaWithVillages directly
	villageOtherDistrict = "TE-V3" // under talukaOtherDistrct
)

// seed builds the hierarchy and returns jurisdiction ids by code.
//
//	state TE-ST
//	├── district TE-D1
//	│   ├── taluka TE-T1
//	│   │   ├── circle TE-C1
//	│   │   │   └── village TE-V1
//	│   │   └── village TE-V2
//	│   └── taluka TE-T2          (no villages)
//	└── district TE-D2
//	    └── taluka TE-T3
//	        └── village TE-V3
func seed(t *testing.T, pool *pgxpool.Pool) map[string]string {
	t.Helper()
	ctx := context.Background()

	rows := []struct{ level, code, name, parent string }{
		{"state", "TE-ST", "Test State", ""},
		{"district", districtOne, "District One", "TE-ST"},
		{"district", districtTwo, "District Two", "TE-ST"},
		{"sub_district", talukaWithVillages, "Taluka One", districtOne},
		{"sub_district", talukaEmpty, "Taluka Two", districtOne},
		{"sub_district", talukaOtherDistrct, "Taluka Three", districtTwo},
		{"circle", circleOne, "Circle One", talukaWithVillages},
		{"village", villageInCircle, "Village One", circleOne},
		{"village", villageOutsideCircle, "Village Two", talukaWithVillages},
		{"village", villageOtherDistrict, "Village Three", talukaOtherDistrct},
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
			RETURNING id::text`,
			r.level, r.code, r.name, parent).Scan(&id)
		if err != nil {
			t.Fatalf("seeding %s: %v", r.code, err)
		}
		ids[r.code] = id
	}

	// An officer to sign declarations with. A declared event must name one.
	var officer string
	if err := pool.QueryRow(ctx, `
		INSERT INTO users (kind, full_name, username, email, password_hash)
		VALUES ('officer', 'Test Collector', 'te.collector@esurvey-test.in', 'te.collector@esurvey-test.in', 'x')
		RETURNING id::text`).Scan(&officer); err != nil {
		t.Fatalf("seeding the officer: %v", err)
	}
	ids[officer_] = officer

	return ids
}

// officer_ is the key the seeded declaring officer is stored under.
const officer_ = "TE-OFFICER"

// draft creates an event over the named areas, in the given district.
func draft(t *testing.T, s *Store, ids map[string]string, district string, areas ...string) *Event {
	t.Helper()
	e, err := s.Create(context.Background(), New{
		CalamityType:   "hailstorm",
		Title:          "Test hailstorm",
		DistrictID:     ids[district],
		IncidentStart:  "2026-03-01",
		IncidentEnd:    "2026-03-05",
		SurveyClosesAt: time.Now().Add(30 * 24 * time.Hour),
		AreaCodes:      areas,
	})
	if err != nil {
		t.Fatalf("creating an event over %v: %v", areas, err)
	}
	return e
}

// opened walks an event to the state where surveys are accepted.
func opened(t *testing.T, s *Store, ids map[string]string, district string, areas ...string) *Event {
	t.Helper()
	e := draft(t, s, ids, district, areas...)
	ctx := context.Background()
	if _, err := s.Transition(ctx, e.ID, StateDeclared, ids[officer_]); err != nil {
		t.Fatalf("declaring: %v", err)
	}
	e, err := s.Transition(ctx, e.ID, StateOpen, "")
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	return e
}

// ---------------------------------------------------------------------------
// The window, with no database in the way
// ---------------------------------------------------------------------------

func TestSurveyingIsOpenOnlyInsideTheWindow(t *testing.T) {
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	past := now.Add(-24 * time.Hour)
	future := now.Add(24 * time.Hour)

	cases := []struct {
		name  string
		event Event
		want  bool
	}{
		{"open, no start, closing later",
			Event{State: StateOpen, SurveyClosesAt: future}, true},
		{"open, started already",
			Event{State: StateOpen, SurveyOpensAt: &past, SurveyClosesAt: future}, true},
		{"open, but not yet started",
			Event{State: StateOpen, SurveyOpensAt: &future, SurveyClosesAt: future.Add(time.Hour)}, false},
		{"open, but the deadline passed",
			Event{State: StateOpen, SurveyClosesAt: past}, false},
		{"declared is not open",
			Event{State: StateDeclared, SurveyClosesAt: future}, false},
		{"closed is not open",
			Event{State: StateClosed, SurveyClosesAt: future}, false},
		{"cancelled is not open",
			Event{State: StateCancelled, SurveyClosesAt: future}, false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.event.SurveyOpen(now); got != c.want {
				t.Errorf("SurveyOpen = %v, want %v", got, c.want)
			}
		})
	}
}

func TestTheDeadlineIsExclusive(t *testing.T) {
	// A survey started at the closing instant is late. Accepting it means the
	// deadline an officer published is not the deadline the system keeps.
	at := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	e := Event{State: StateOpen, SurveyClosesAt: at}
	if e.SurveyOpen(at) {
		t.Error("a survey at the exact closing instant was accepted")
	}
}

func TestTheCalamityListCannotBeChangedByACaller(t *testing.T) {
	// CalamityTypes feeds a form. If it handed out the package's own slice, a
	// caller sorting or truncating it for display would silently rewrite what
	// ValidCalamity accepts for the life of the process.
	first := CalamityTypes()
	if len(first) == 0 {
		t.Fatal("no calamity types")
	}
	original := first[0]
	first[0] = "not-a-calamity"

	if !ValidCalamity(original) {
		t.Errorf("%q stopped being valid after a caller wrote to the returned slice", original)
	}
	if ValidCalamity("not-a-calamity") {
		t.Error("a caller's write to the returned slice became an accepted type")
	}
}

// ---------------------------------------------------------------------------
// Creating
// ---------------------------------------------------------------------------

func TestAnEventNamingNoAreaIsRefused(t *testing.T) {
	// It would cover nobody, and nothing later would say so.
	s, pool := testEvents(t)
	ids := seed(t, pool)

	_, err := s.Create(context.Background(), New{
		CalamityType:   "flood",
		Title:          "Covers nobody",
		DistrictID:     ids[districtOne],
		IncidentStart:  "2026-03-01",
		IncidentEnd:    "2026-03-05",
		SurveyClosesAt: time.Now().Add(24 * time.Hour),
	})
	if !errors.Is(err, ErrBadArea) {
		t.Errorf("error = %v, want ErrBadArea", err)
	}
}

func TestAnUnknownCalamityIsRefused(t *testing.T) {
	s, pool := testEvents(t)
	ids := seed(t, pool)

	_, err := s.Create(context.Background(), New{
		CalamityType:   "volcano",
		Title:          "Not in Maharashtra",
		DistrictID:     ids[districtOne],
		IncidentStart:  "2026-03-01",
		IncidentEnd:    "2026-03-05",
		SurveyClosesAt: time.Now().Add(24 * time.Hour),
		AreaCodes:      []string{talukaWithVillages},
	})
	if !errors.Is(err, ErrBadArea) {
		t.Errorf("error = %v, want ErrBadArea", err)
	}
}

func TestAnEventBeginsAsADraft(t *testing.T) {
	// Declaring is a separate act by a named officer. An event that arrived
	// already declared would mean a notification nobody signed.
	s, pool := testEvents(t)
	ids := seed(t, pool)

	e := draft(t, s, ids, districtOne, talukaWithVillages)
	if e.State != StateDraft {
		t.Errorf("state = %s, want draft", e.State)
	}
	if e.DeclaredBy != "" || e.DeclaredAt != nil {
		t.Error("a draft arrived with a declaration on it")
	}
}

func TestReferencesAreNumberedPerDistrictAndYear(t *testing.T) {
	s, pool := testEvents(t)
	ids := seed(t, pool)

	first := draft(t, s, ids, districtOne, talukaWithVillages)
	second := draft(t, s, ids, districtOne, talukaEmpty)
	other := draft(t, s, ids, districtTwo, talukaOtherDistrct)

	if want := "SE-2026-" + districtOne + "-001"; first.Reference != want {
		t.Errorf("first reference = %q, want %q", first.Reference, want)
	}
	if want := "SE-2026-" + districtOne + "-002"; second.Reference != want {
		t.Errorf("second reference = %q, want %q", second.Reference, want)
	}
	// The other district starts its own sequence; the numbers are a district's
	// records, not a national counter.
	if want := "SE-2026-" + districtTwo + "-001"; other.Reference != want {
		t.Errorf("the second district's first reference = %q, want %q",
			other.Reference, want)
	}
}

// Two officers declaring at the same moment in one district, which is what a
// state-wide storm looks like from a Collectorate. The references used to be
// count(*)+1 and would collide (docs/SYNC_SCALE_REVIEW.md).
func TestEventsCreatedAtOnceInOneDistrictGetDistinctReferences(t *testing.T) {
	s, pool := testEvents(t)
	ids := seed(t, pool)

	const copies = 8
	refs := make([]string, copies)
	errs := make([]error, copies)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < copies; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			e, err := s.Create(context.Background(), New{
				CalamityType:   "hailstorm",
				Title:          "Simultaneous hailstorm",
				DistrictID:     ids[districtOne],
				IncidentStart:  "2026-03-01",
				IncidentEnd:    "2026-03-05",
				SurveyClosesAt: time.Now().Add(30 * 24 * time.Hour),
				AreaCodes:      []string{talukaWithVillages},
			})
			errs[i] = err
			if e != nil {
				refs[i] = e.Reference
			}
		}(i)
	}
	close(start)
	wg.Wait()

	seen := map[string]bool{}
	for i, err := range errs {
		if err != nil {
			t.Fatalf("event %d was refused: %v", i, err)
		}
		if seen[refs[i]] {
			t.Fatalf("reference %s was issued twice", refs[i])
		}
		seen[refs[i]] = true
	}
}

func TestAnAreaOutsideTheDeclaringDistrictIsRefused(t *testing.T) {
	// The rule that stops one Collector notifying a calamity in another
	// Collector's district. It lives in a database trigger so that no future
	// write path can forget it; this is the test that it is actually armed.
	s, pool := testEvents(t)
	ids := seed(t, pool)

	_, err := s.Create(context.Background(), New{
		CalamityType:   "flood",
		Title:          "Reaching into the next district",
		DistrictID:     ids[districtOne],
		IncidentStart:  "2026-03-01",
		IncidentEnd:    "2026-03-05",
		SurveyClosesAt: time.Now().Add(24 * time.Hour),
		AreaCodes:      []string{talukaOtherDistrct},
	})
	if err == nil {
		t.Fatal("a district declared a calamity over another district's taluka")
	}
	if !strings.Contains(err.Error(), "outside the declaring district") {
		t.Errorf("error = %v, want it to name the reason", err)
	}
}

func TestAVillageCannotBeNamedAsAnArea(t *testing.T) {
	// Events are declared over talukas and circles. A village-level area would
	// be a notification of a different shape than the one officers publish.
	s, pool := testEvents(t)
	ids := seed(t, pool)

	_, err := s.Create(context.Background(), New{
		CalamityType:   "flood",
		Title:          "Too fine grained",
		DistrictID:     ids[districtOne],
		IncidentStart:  "2026-03-01",
		IncidentEnd:    "2026-03-05",
		SurveyClosesAt: time.Now().Add(24 * time.Hour),
		AreaCodes:      []string{villageInCircle},
	})
	if !errors.Is(err, ErrBadArea) {
		t.Errorf("error = %v, want ErrBadArea", err)
	}
}

func TestVillagesAreCountedOnceWhenACircleSitsInsideANamedTaluka(t *testing.T) {
	// Naming both a taluka and a circle inside it is ordinary — an officer
	// clicking the taluka and then a circle for emphasis. Summing the areas
	// would report three villages where there are two, and the number is what
	// tells an officer whether their notification reaches anybody.
	s, pool := testEvents(t)
	ids := seed(t, pool)

	e := draft(t, s, ids, districtOne, talukaWithVillages, circleOne)
	if e.VillageCount != 2 {
		t.Errorf("village count = %d, want 2 (the union, not the sum)", e.VillageCount)
	}
	if len(e.Areas) != 2 {
		t.Fatalf("areas = %d, want 2", len(e.Areas))
	}
}

func TestAnAreaCarriesItsOwnVillageCount(t *testing.T) {
	// The count per area is how an officer notices, before opening, that a
	// circle has no villages mapped to it.
	s, pool := testEvents(t)
	ids := seed(t, pool)

	e := draft(t, s, ids, districtOne, talukaEmpty)
	if len(e.Areas) != 1 {
		t.Fatalf("areas = %d, want 1", len(e.Areas))
	}
	if e.Areas[0].VillageCount != 0 {
		t.Errorf("the empty taluka reports %d villages, want 0",
			e.Areas[0].VillageCount)
	}
}

// ---------------------------------------------------------------------------
// The lifecycle
// ---------------------------------------------------------------------------

func TestAnEventCannotSkipDeclaration(t *testing.T) {
	s, pool := testEvents(t)
	ids := seed(t, pool)

	e := draft(t, s, ids, districtOne, talukaWithVillages)
	_, err := s.Transition(context.Background(), e.ID, StateOpen, "")
	if !errors.Is(err, ErrBadState) {
		t.Errorf("error = %v, want ErrBadState for draft to open", err)
	}
}

func TestAnEventCoveringNoVillagesCannotOpen(t *testing.T) {
	// The guard that matters most here. An event over a taluka whose circles
	// were never mapped opens to nobody: every farmer under it is told their
	// village is not covered by a notification that names their taluka. It is
	// refused at the moment an officer would publish it, when the fix is
	// still cheap.
	s, pool := testEvents(t)
	ids := seed(t, pool)

	e := draft(t, s, ids, districtOne, talukaEmpty)
	ctx := context.Background()
	if _, err := s.Transition(ctx, e.ID, StateDeclared, ids[officer_]); err != nil {
		t.Fatalf("declaring: %v", err)
	}

	_, err := s.Transition(ctx, e.ID, StateOpen, "")
	if !errors.Is(err, ErrBadState) {
		t.Fatalf("error = %v, want ErrBadState", err)
	}
	if !strings.Contains(err.Error(), "no villages") {
		t.Errorf("the refusal does not say why: %v", err)
	}
}

func TestDeclaringRecordsWhoDidIt(t *testing.T) {
	s, pool := testEvents(t)
	ids := seed(t, pool)

	e := draft(t, s, ids, districtOne, talukaWithVillages)
	e, err := s.Transition(context.Background(), e.ID, StateDeclared, ids[officer_])
	if err != nil {
		t.Fatal(err)
	}
	if e.DeclaredBy != ids[officer_] {
		t.Errorf("declared_by = %q, want the officer", e.DeclaredBy)
	}
	if e.DeclaredAt == nil {
		t.Error("a declaration with no time on it")
	}
}

func TestADeclarationMustNameAnOfficer(t *testing.T) {
	// A notification is a thing a Collector signs. The database says so too,
	// but a check constraint reaches the officer as an SQLSTATE; this is the
	// test that they get a sentence instead.
	s, pool := testEvents(t)
	ids := seed(t, pool)

	e := draft(t, s, ids, districtOne, talukaWithVillages)
	_, err := s.Transition(context.Background(), e.ID, StateDeclared, "")
	if !errors.Is(err, ErrBadState) {
		t.Fatalf("error = %v, want ErrBadState", err)
	}
	if !strings.Contains(err.Error(), "officer") {
		t.Errorf("the refusal does not say what is missing: %v", err)
	}
}

func TestACancelledEventIsFinal(t *testing.T) {
	s, pool := testEvents(t)
	ids := seed(t, pool)

	e := draft(t, s, ids, districtOne, talukaWithVillages)
	ctx := context.Background()
	if _, err := s.Transition(ctx, e.ID, StateCancelled, ""); err != nil {
		t.Fatalf("cancelling: %v", err)
	}
	for _, to := range []State{StateDeclared, StateOpen, StateDraft} {
		if _, err := s.Transition(ctx, e.ID, to, ""); !errors.Is(err, ErrBadState) {
			t.Errorf("moving a cancelled event to %s gave %v, want ErrBadState", to, err)
		}
	}
}

func TestAClosedEventCanBeReopened(t *testing.T) {
	// Deliberate: a deadline extended after representations is a real act, and
	// making it impossible pushes it into direct database edits.
	s, pool := testEvents(t)
	ids := seed(t, pool)

	e := opened(t, s, ids, districtOne, talukaWithVillages)
	ctx := context.Background()
	if _, err := s.Transition(ctx, e.ID, StateClosed, ""); err != nil {
		t.Fatalf("closing: %v", err)
	}
	e, err := s.Transition(ctx, e.ID, StateOpen, "")
	if err != nil {
		t.Fatalf("reopening: %v", err)
	}
	if e.State != StateOpen {
		t.Errorf("state = %s, want open", e.State)
	}
}

// ---------------------------------------------------------------------------
// Eligibility — the sentence the package exists for
// ---------------------------------------------------------------------------

func TestAVillageInsideANamedTalukaIsEligible(t *testing.T) {
	s, pool := testEvents(t)
	ids := seed(t, pool)

	e := opened(t, s, ids, districtOne, talukaWithVillages)

	for _, v := range []string{villageInCircle, villageOutsideCircle} {
		got, err := s.CheckParcel(context.Background(), e.ID, v, "2026-03-02", time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if !got.Eligible {
			t.Errorf("village %s is not eligible under its own taluka: %s", v, got.Reason)
		}
	}
}

func TestNamingACircleCoversOnlyItsVillages(t *testing.T) {
	// The reason circles exist in the model. An event declared over one
	// circle must not pull in the rest of the taluka.
	s, pool := testEvents(t)
	ids := seed(t, pool)

	e := opened(t, s, ids, districtOne, circleOne)
	ctx := context.Background()

	inside, err := s.CheckParcel(ctx, e.ID, villageInCircle, "2026-03-02", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !inside.Eligible {
		t.Errorf("the circle's own village is not eligible: %s", inside.Reason)
	}

	outside, err := s.CheckParcel(ctx, e.ID, villageOutsideCircle, "2026-03-02", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if outside.Eligible {
		t.Error("a village outside the named circle was eligible")
	}
}

func TestAVillageInAnotherDistrictIsNotEligible(t *testing.T) {
	s, pool := testEvents(t)
	ids := seed(t, pool)

	e := opened(t, s, ids, districtOne, talukaWithVillages)

	got, err := s.CheckParcel(context.Background(), e.ID,
		villageOtherDistrict, "2026-03-02", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if got.Eligible {
		t.Error("a village in another district was eligible")
	}
	if !strings.Contains(got.Reason, villageOtherDistrict) {
		t.Errorf("the reason does not name the village: %q", got.Reason)
	}
}

func TestAnIncidentDateOutsideTheEventWindowIsRefused(t *testing.T) {
	// The window is what ties a claim to the calamity that was notified.
	// Without it an event becomes a standing invitation to file for any loss.
	s, pool := testEvents(t)
	ids := seed(t, pool)

	e := opened(t, s, ids, districtOne, talukaWithVillages)
	ctx := context.Background()

	for _, date := range []string{"2026-02-28", "2026-03-06"} {
		got, err := s.CheckParcel(ctx, e.ID, villageInCircle, date, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if got.Eligible {
			t.Errorf("incident date %s was accepted for a window of %s to %s",
				date, e.IncidentStart, e.IncidentEnd)
		}
	}

	// Both ends are inclusive: a loss on the first or last notified day is
	// exactly what the event covers.
	for _, date := range []string{"2026-03-01", "2026-03-05"} {
		got, err := s.CheckParcel(ctx, e.ID, villageInCircle, date, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if !got.Eligible {
			t.Errorf("incident date %s was refused at the edge of the window: %s",
				date, got.Reason)
		}
	}
}

func TestSurveyingIsRefusedBeforeTheEventOpens(t *testing.T) {
	s, pool := testEvents(t)
	ids := seed(t, pool)

	e := draft(t, s, ids, districtOne, talukaWithVillages)
	if _, err := s.Transition(context.Background(), e.ID, StateDeclared, ids[officer_]); err != nil {
		t.Fatal(err)
	}

	got, err := s.CheckParcel(context.Background(), e.ID,
		villageInCircle, "2026-03-02", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if got.Eligible {
		t.Error("a survey was accepted against an event that has only been declared")
	}
	if !strings.Contains(got.Reason, e.Reference) {
		t.Errorf("the reason does not name the event: %q", got.Reason)
	}
}

func TestSurveyingIsRefusedAfterTheDeadline(t *testing.T) {
	s, pool := testEvents(t)
	ids := seed(t, pool)

	e := opened(t, s, ids, districtOne, talukaWithVillages)

	// Asked as of a day after the deadline, rather than by moving the clock.
	after := e.SurveyClosesAt.Add(24 * time.Hour)
	got, err := s.CheckParcel(context.Background(), e.ID,
		villageInCircle, "2026-03-02", after)
	if err != nil {
		t.Fatal(err)
	}
	if got.Eligible {
		t.Error("a survey was accepted after the deadline")
	}
}

func TestCheckingAgainstAnEventThatDoesNotExist(t *testing.T) {
	s, _ := testEvents(t)

	_, err := s.CheckParcel(context.Background(),
		"00000000-0000-0000-0000-000000000000", villageInCircle,
		"2026-03-02", time.Now())
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("error = %v, want ErrNotFound", err)
	}
}
