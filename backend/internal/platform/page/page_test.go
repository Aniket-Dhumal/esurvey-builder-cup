package page

import (
	"net/http/httptest"
	"testing"
)

func params(query string) (Params, error) {
	return FromRequest(httptest.NewRequest("GET", "/x?"+query, nil))
}

func TestDefaults(t *testing.T) {
	p, err := params("")
	if err != nil {
		t.Fatal(err)
	}
	if p.Limit != DefaultLimit || p.Offset != 0 || p.Query != "" {
		t.Errorf("defaults = %+v", p)
	}
}

func TestLimitIsClamped(t *testing.T) {
	// A caller asking for ten thousand rows is making a mistake, not an
	// attack. Clamping keeps a mis-typed URL from being a broken page, where
	// rejecting would make it one.
	cases := map[string]int{
		"limit=10000": MaxLimit,
		"limit=0":     DefaultLimit,
		"limit=-5":    DefaultLimit,
		"limit=25":    25,
		"limit=":      DefaultLimit,
	}
	for q, want := range cases {
		p, err := params(q)
		if err != nil {
			t.Errorf("%s: %v", q, err)
			continue
		}
		if p.Limit != want {
			t.Errorf("%s -> limit %d, want %d", q, p.Limit, want)
		}
	}
}

func TestADeepOffsetIsRefusedRatherThanServedSlowly(t *testing.T) {
	// Offset paging makes the database walk the rows it skips. Past a point
	// the honest answer is "narrow the search", not a query that takes
	// seconds and gets blamed on the network.
	if _, err := params("offset=20001"); err == nil {
		t.Error("an offset past the limit was accepted")
	}
	if _, err := params("offset=20000"); err != nil {
		t.Errorf("the boundary offset was refused: %v", err)
	}
}

func TestANegativeOffsetIsZero(t *testing.T) {
	p, err := params("offset=-10")
	if err != nil {
		t.Fatal(err)
	}
	if p.Offset != 0 {
		t.Errorf("offset = %d, want 0", p.Offset)
	}
}

func TestNonNumericPagingIsAnError(t *testing.T) {
	// Silently treating "abc" as the default would show page one while the
	// caller believes they asked for something else.
	for _, q := range []string{"limit=abc", "offset=xyz"} {
		if _, err := params(q); err == nil {
			t.Errorf("%s was accepted", q)
		}
	}
}

func TestQueryIsNormalised(t *testing.T) {
	// An officer pasting a name out of a spreadsheet brings whitespace with
	// it. "Dhumal " finding nothing when "Dhumal" finds four is how people
	// learn to stop trusting a search box.
	cases := map[string]string{
		"q=Dhumal":         "Dhumal",
		"q=%20Dhumal%20":   "Dhumal",
		"q=Hari%20%20chnd": "Hari chnd",
		"q=%20%20":         "",
		"q=":               "",
	}
	for q, want := range cases {
		p, err := params(q)
		if err != nil {
			t.Fatal(err)
		}
		if p.Query != want {
			t.Errorf("%s -> %q, want %q", q, p.Query, want)
		}
	}
}

func TestALongQueryIsTruncatedNotRejected(t *testing.T) {
	long := ""
	for i := 0; i < 500; i++ {
		long += "a"
	}
	p, err := params("q=" + long)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Query) != MaxQueryLen {
		t.Errorf("query length = %d, want %d", len(p.Query), MaxQueryLen)
	}
}

func TestLikeEscapesWildcards(t *testing.T) {
	// Without this, searching for "50_percent" matches "50Xpercent" -- an
	// underscore is a single-character wildcard the user did not type.
	cases := map[string]string{
		"barshi":     "%barshi%",
		"50_percent": `%50\_percent%`,
		"100%":       `%100\%%`,
		`back\slash`: `%back\\slash%`,
	}
	for in, want := range cases {
		if got := Like(in); got != want {
			t.Errorf("Like(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestWrapComputesHasMore(t *testing.T) {
	cases := []struct {
		name    string
		items   int
		total   int
		offset  int
		limit   int
		hasMore bool
	}{
		{"first page of many", 50, 4312, 0, 50, true},
		{"last page exactly", 12, 62, 50, 50, false},
		{"single short page", 7, 7, 0, 50, false},
		{"empty result", 0, 0, 0, 50, false},
		{"middle page", 50, 200, 50, 50, true},
		// The page that ends exactly on the total. Off by one here means a
		// pager offering a next page that comes back empty.
		{"page ending on the total", 50, 100, 50, 50, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			items := make([]int, c.items)
			got := Wrap(items, c.total, Params{Limit: c.limit, Offset: c.offset})
			if got.HasMore != c.hasMore {
				t.Errorf("HasMore = %v, want %v", got.HasMore, c.hasMore)
			}
			if got.Total != c.total {
				t.Errorf("Total = %d, want %d", got.Total, c.total)
			}
		})
	}
}

func TestWrapNeverReturnsNilItems(t *testing.T) {
	// A JSON null here becomes a crash in a client that maps over it.
	got := Wrap[int](nil, 0, Params{Limit: 50})
	if got.Items == nil {
		t.Fatal("Items is nil; a client mapping over it would fail")
	}
	if len(got.Items) != 0 {
		t.Errorf("Items = %v, want empty", got.Items)
	}
}
