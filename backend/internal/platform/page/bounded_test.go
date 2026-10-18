package page

import (
	"strings"
	"testing"
)

// The count is the expensive half of a page.
//
// Walking rows was already bounded: MaxOffset refuses anything deeper than
// twenty thousand, so the worst a listing can make the database skip is a
// known number. The count had no such bound — it ran the listing's predicates
// over the whole table on every page load, for a figure printed in small type
// beside a pager. On audit_log, which gains a row per request that touches a
// record, that is a sequential scan of the largest table in the system to
// render "4,312,908".

func TestTheCountStopsAtTheCeiling(t *testing.T) {
	got := CountSQL("SELECT 1 FROM audit_log a WHERE a.action = $1")

	// The LIMIT has to be inside the subquery. Postgres cannot push a limit
	// through an aggregate, so a LIMIT on the outer count would bound the
	// number of result rows — which is already one — and change nothing about
	// the work done.
	inner := got[strings.Index(got, "(")+1 : strings.LastIndex(got, ")")]
	if !strings.Contains(inner, "LIMIT") {
		t.Errorf("the limit is not inside the counted subquery:\n  %s", got)
	}
	if !strings.Contains(inner, "SELECT 1 FROM audit_log") {
		t.Errorf("the caller's query did not survive wrapping:\n  %s", got)
	}
	if !strings.Contains(got, "count(*)") {
		t.Errorf("not a counting query:\n  %s", got)
	}
}

func TestPlaceholdersAreNotDisturbed(t *testing.T) {
	// The caller passes the same arguments to the bounded count as to the
	// unbounded one, so renumbering a placeholder would silently bind the
	// wrong value to the wrong predicate.
	got := CountSQL("SELECT 1 FROM t WHERE a = $1 AND b = $2 AND c ILIKE $3")
	for _, p := range []string{"$1", "$2", "$3"} {
		if !strings.Contains(got, p) {
			t.Errorf("%s was lost:\n  %s", p, got)
		}
	}
	if strings.Contains(got, "$4") {
		t.Errorf("a placeholder was invented:\n  %s", got)
	}
}

func TestACountBelowTheCeilingIsExact(t *testing.T) {
	// The ordinary case, and it must not be marked as a floor: a district with
	// three hundred reports should read "300", not "300+".
	r := Wrap([]int{1, 2, 3}, 300, Params{Limit: 50, Offset: 0})

	if r.TotalIsLower {
		t.Error("an exact count was reported as a lower bound")
	}
	if r.Total != 300 {
		t.Errorf("total = %d, want 300", r.Total)
	}
	if !r.HasMore {
		t.Error("300 matches with 3 shown should offer another page")
	}
}

func TestACountAtTheCeilingIsAFloor(t *testing.T) {
	r := Wrap([]int{1, 2, 3}, CountCeiling, Params{Limit: 50, Offset: 0})

	if !r.TotalIsLower {
		t.Fatal("a capped count was presented as the total")
	}
	// And there is always a next page as far as anything here knows. Offering
	// one that turns out empty is a smaller harm than hiding rows that exist.
	if !r.HasMore {
		t.Error("a capped count did not offer a next page")
	}
}

func TestTheLastPageOfAnExactCountOffersNoMore(t *testing.T) {
	// 120 matches, showing rows 101-120. Nothing follows.
	r := Wrap(make([]int, 20), 120, Params{Limit: 50, Offset: 100})
	if r.HasMore {
		t.Error("the last page offered another")
	}
	if r.TotalIsLower {
		t.Error("120 is below the ceiling and is exact")
	}
}

func TestAnEmptyPageIsAnEmptySliceNotNull(t *testing.T) {
	// A JSON null here becomes a crash in a client that maps over it.
	r := Wrap[int](nil, 0, Params{Limit: 50})
	if r.Items == nil {
		t.Fatal("nil items would serialise as null")
	}
	if len(r.Items) != 0 {
		t.Errorf("items = %v, want empty", r.Items)
	}
	if r.HasMore {
		t.Error("nothing matched, so nothing follows")
	}
}

func TestTheCeilingIsHighEnoughToBeUseful(t *testing.T) {
	// Low enough to bound the work, high enough that an ordinary district's
	// listing still shows a real number rather than a permanent "N+".
	if CountCeiling < 1000 {
		t.Errorf("CountCeiling = %d, low enough that ordinary listings would "+
			"all read as approximations", CountCeiling)
	}
	if CountCeiling > MaxOffset {
		t.Errorf("CountCeiling = %d exceeds MaxOffset = %d, so the pager would "+
			"offer pages the server refuses to serve", CountCeiling, MaxOffset)
	}
}
