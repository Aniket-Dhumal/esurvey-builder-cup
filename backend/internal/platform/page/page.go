// Package page is the pagination and search contract every listing shares.
//
// # Why one contract
//
// Every table in the console had grown its own arrangement: some endpoints
// took limit and offset and some did not, none took a search term, and every
// one of them returned `"total": len(items)` — the size of the page just
// returned, not the number of rows that matched. That number is worse than
// no number. A console showing "total: 50" when 4,312 reports match has told
// the officer something false, and there is no way for the page to know it
// should offer a next page.
//
// # Why offset paging, and where it stops working
//
// Offset is what a numbered pager needs, and a numbered pager is what an
// officer working through a district actually wants — "page 4 of 87" is a
// place they can come back to. The cost is that the database must walk the
// rows it skips, so a deep offset gets slower the further in it goes.
//
// That is acceptable here and the reason is a property of the data, not of
// the code: every listing is scoped to a jurisdiction, and the largest scope
// any single officer holds is one district. Deep paging into a whole state
// would need keyset cursors; a Collector paging through their own district's
// reports will not reach the depth where the difference shows. If a state-wide
// listing is ever wanted, this is the thing to revisit — see MaxOffset, which
// refuses rather than quietly serving a slow query.
package page

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

const (
	// DefaultLimit is one screen of rows.
	DefaultLimit = 50
	// MaxLimit bounds what one request may ask for. A caller wanting
	// everything is either building a report — which should page — or making
	// a mistake that would otherwise hold a connection open while it
	// serialises a district.
	MaxLimit = 200
	// MaxOffset is where offset paging stops being honest.
	//
	// Twenty thousand rows deep, the database is walking twenty thousand rows
	// to discard them. Refusing is better than serving it slowly: the caller
	// gets an error naming the problem instead of a timeout they will blame
	// on the network.
	MaxOffset = 20_000

	// CountCeiling bounds the work a total costs.
	//
	// # Why a total is the expensive half of a page
	//
	// Walking rows is already bounded: MaxOffset refuses a page deeper than
	// 20,000, so the worst a listing can make the database skip is twenty
	// thousand rows. The count had no such bound. It runs the same predicates
	// over the whole table, and on the fastest-growing table here -- audit_log,
	// one row per request that touches a record -- that is a sequential scan
	// on every page load, for a number shown in small type next to a pager.
	//
	// So the count stops at this many and the console says "5,000+". An
	// officer deciding whether to narrow their search is served exactly as
	// well by that as by 4,312,908, and the query costs the same at ten
	// million rows as at ten thousand.
	CountCeiling = 5_000
	// MaxQueryLen bounds a search term. Nothing legitimate is longer, and a
	// trigram search over a very long string is expensive to no purpose.
	MaxQueryLen = 100
)

// Params is what a client may ask for.
type Params struct {
	Limit  int
	Offset int
	// Query is a free-text search. Empty means no filtering.
	Query string

	// Cursor continues from where a previous page stopped, instead of
	// counting rows to skip.
	//
	// # Why both this and Offset
	//
	// A numbered pager is what an officer working a district wants -- "page 4
	// of 87" is a place to come back to -- and it needs offsets. But the
	// database walks the rows an offset skips, so past MaxOffset the listing
	// is refused rather than served slowly, and a state-wide list has nowhere
	// to go. A cursor pages indefinitely at constant cost; it simply cannot
	// jump to page 40. Callers that offer "next" use it; callers that offer
	// page numbers use Offset.
	//
	// Opaque on purpose: it encodes the sort key of the last row returned, and
	// a client that reads it is a client that breaks when the sort changes.
	Cursor string
}

// FromRequest reads limit, offset and q from the query string.
//
// Out-of-range values are clamped rather than rejected. A limit of 10,000 is
// a caller asking for too much, not an attack, and clamping keeps a
// mis-typed URL from being a broken page. An offset past MaxOffset is
// different: silently clamping it would show the wrong rows.
func FromRequest(r *http.Request) (Params, error) {
	q := r.URL.Query()

	p := Params{Limit: DefaultLimit}

	if v := strings.TrimSpace(q.Get("limit")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return p, &Error{"limit must be a number"}
		}
		p.Limit = n
	}
	if p.Limit <= 0 {
		p.Limit = DefaultLimit
	}
	if p.Limit > MaxLimit {
		p.Limit = MaxLimit
	}

	if v := strings.TrimSpace(q.Get("offset")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return p, &Error{"offset must be a number"}
		}
		if n < 0 {
			n = 0
		}
		if n > MaxOffset {
			return p, &Error{
				"that page is too deep to serve; narrow the search instead"}
		}
		p.Offset = n
	}

	if v := strings.TrimSpace(q.Get("cursor")); v != "" {
		if len(v) > 200 {
			return p, &Error{"that cursor is not one this listing issued"}
		}
		p.Cursor = v
	}

	p.Query = CleanQuery(q.Get("q"))
	return p, nil
}

// Cursor encodes where a page stopped, opaquely.
//
// Base64 of the values, joined -- not encryption and not meant to be: it hides
// the shape from a client that would otherwise depend on it, and a malformed
// or stale cursor is refused by the listing that decodes it rather than
// trusted.
func Cursor(parts ...string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strings.Join(parts, "\x1f")))
}

// DecodeCursor reads a cursor back, and says whether it is one we issued with
// the expected number of parts.
func DecodeCursor(c string, parts int) ([]string, bool) {
	raw, err := base64.RawURLEncoding.DecodeString(c)
	if err != nil {
		return nil, false
	}
	got := strings.Split(string(raw), "\x1f")
	if len(got) != parts {
		return nil, false
	}
	return got, true
}

// Error is a bad pagination request, which is a 400 rather than a 500.
type Error struct{ Message string }

func (e *Error) Error() string { return e.Message }

// CleanQuery normalises a search term.
//
// Collapsing whitespace matters more than it looks: an officer pasting a name
// out of a spreadsheet brings a trailing space with it, and "Dhumal " finding
// nothing when "Dhumal" finds four is the kind of thing that makes people
// stop trusting search.
func CleanQuery(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > MaxQueryLen {
		s = s[:MaxQueryLen]
	}
	return s
}

// Result wraps one page of anything.
//
// Total is the number of rows that matched, not the number returned. It is
// what lets a console say "showing 50 of 4,312" and know whether to offer a
// next page — the thing every listing was previously unable to do.
type Result[T any] struct {
	Items  []T `json:"items"`
	Total  int `json:"total"`
	Limit  int `json:"limit"`
	Offset int `json:"offset"`

	// TotalIsLower says Total is a floor rather than the answer: there are at
	// least this many matches and the count stopped looking. The console shows
	// "5,000+" rather than a number it would otherwise state as fact.
	TotalIsLower bool `json:"total_is_lower_bound,omitempty"`

	HasMore bool   `json:"has_more"`
	Query   string `json:"query,omitempty"`

	// NextCursor continues this listing past the offset ceiling. Empty when
	// there is no next page, or when the listing does not offer cursors.
	NextCursor string `json:"next_cursor,omitempty"`
}

// Wrap builds a Result, filling in what the caller should not have to.
func Wrap[T any](items []T, total int, p Params) Result[T] {
	if items == nil {
		// A JSON null here becomes a crash in a client that maps over it.
		items = []T{}
	}
	// A count that hit the ceiling is a floor, not a total. Saying so here
	// rather than at each call site means a listing that switches to a bounded
	// count cannot forget to mention it.
	capped := total >= CountCeiling

	return Result[T]{
		Items:        items,
		Total:        total,
		Limit:        p.Limit,
		Offset:       p.Offset,
		TotalIsLower: capped,
		// With a capped count there are always more pages as far as anyone
		// here knows, and offering a next page that turns out to be empty is a
		// smaller harm than hiding rows that exist.
		HasMore: capped || p.Offset+len(items) < total,
		Query:   p.Query,
	}
}

// CountSQL wraps a counting query so it stops at CountCeiling.
//
// The inner query selects a constant and is limited, so the planner stops
// after the ceiling is reached rather than counting to the end of the table.
// Postgres cannot push a LIMIT through an aggregate on its own, which is why
// the subquery is written out rather than left to the optimiser.
//
//	CountSQL("SELECT 1 FROM audit_log a WHERE " + where)
//	  -> SELECT count(*) FROM (SELECT 1 FROM audit_log a WHERE ... LIMIT 5000) c
func CountSQL(inner string) string {
	return fmt.Sprintf("SELECT count(*) FROM (%s LIMIT %d) _bounded", inner, CountCeiling)
}

// Like turns a search term into a case-insensitive ILIKE pattern.
//
// The % wrapping is what makes this a substring search, which is what an
// officer means when they type three letters of a village name. It is also
// what makes a plain index useless and a trigram index necessary — see
// migration 0008.
//
// The escaping is not optional: a term containing % or _ would otherwise be a
// wildcard the user did not ask for, so searching for "50_percent" would
// quietly match "50Xpercent".
func Like(query string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return "%" + r.Replace(query) + "%"
}
