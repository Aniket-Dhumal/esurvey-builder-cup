package assessment

import (
	"fmt"
	"time"
)

const isoDate = "2006-01-02"

// daysBetween returns the whole days from a to b, both YYYY-MM-DD.
func daysBetween(a, b string) (int, error) {
	ta, err := time.Parse(isoDate, a)
	if err != nil {
		return 0, fmt.Errorf("parsing date %q: %w", a, err)
	}
	tb, err := time.Parse(isoDate, b)
	if err != nil {
		return 0, fmt.Errorf("parsing date %q: %w", b, err)
	}
	return int(tb.Sub(ta).Hours() / 24), nil
}
