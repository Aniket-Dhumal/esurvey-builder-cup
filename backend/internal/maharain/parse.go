package maharain

import (
	"encoding/json"
	"fmt"
	"html"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Report is one district's rainfall, month by month, at one level.
type Report struct {
	Level    Level
	District District

	CropYear int
	// Periods are the months published, in order, followed by the
	// season-to-date cumulative. Derived from the report's own header rather
	// than assumed: in October the portal publishes six columns where in
	// September it published five, and a parser that assumed June-to-September
	// would silently read October's rain as September's.
	Periods []Period
	Units   []UnitRainfall

	// ObservedAt is the moment the portal said the figures were current as of,
	// where it printed one.
	ObservedAt *time.Time
}

// Period is one column group of the report.
type Period struct {
	// Month is 1-12, or 0 for the season-to-date cumulative.
	Month int
	Year  int
	// Start and End bound the period. For the cumulative these are the season
	// start and the day the report was generated for, both read from the
	// column heading ("1st June to 28 Sep").
	Start, End time.Time
	// Label is the heading as published, kept for a report to quote.
	Label string
}

// Cumulative reports whether this is the season-to-date column.
func (p Period) Cumulative() bool { return p.Month == 0 }

// UnitRainfall is one taluka's or circle's row.
type UnitRainfall struct {
	Serial int
	Name   string
	// Values is one reading per period, in the same order as Report.Periods.
	Values []Reading
}

// Reading is what fell against what normally falls.
type Reading struct {
	NormalMM        float64
	ActualMM        float64
	PercentOfNormal float64
}

// Deficit is how far below normal, in percentage points. Negative where rain
// exceeded the normal.
func (r Reading) Deficit() float64 { return 100 - r.PercentOfNormal }

// --- district handler -------------------------------------------------------

type districtResponse struct {
	Status   int        `json:"status"`
	Response string     `json:"response"`
	Data     []District `json:"data"`
}

func parseDistricts(body []byte) ([]District, error) {
	// The handler answers JSON with a text/html content type, and the page's
	// own JavaScript JSON.parses it, so trimming and decoding is what the
	// portal itself does.
	var out districtResponse
	if err := json.Unmarshal(trimJSON(body), &out); err != nil {
		return nil, fmt.Errorf("the district handler did not answer JSON: %w", err)
	}
	if out.Status != 200 {
		return nil, fmt.Errorf("the district handler answered status %d: %s",
			out.Status, out.Response)
	}

	kept := make([]District, 0, len(out.Data))
	for _, d := range out.Data {
		// The handler emits a blank-coded row; its own JavaScript skips it.
		if strings.TrimSpace(d.Code) == "" {
			continue
		}
		kept = append(kept, d)
	}
	return kept, nil
}

func trimJSON(b []byte) []byte {
	s := strings.TrimSpace(string(b))
	if i := strings.IndexAny(s, "{["); i > 0 {
		s = s[i:]
	}
	return []byte(s)
}

// --- monthly report ---------------------------------------------------------

var (
	rowRe   = regexp.MustCompile(`(?is)<tr[^>]*>(.*?)</tr>`)
	cellRe  = regexp.MustCompile(`(?is)<t[dh][^>]*>(.*?)</t[dh]>`)
	tagRe   = regexp.MustCompile(`(?s)<[^>]+>`)
	wsRe    = regexp.MustCompile(`\s+`)
	monthRe = regexp.MustCompile(`(?i)^([A-Za-z]+)\s*,\s*(\d{4})$`)
	// "1st June to 28 Sep", and the variant with no space before the month.
	cumRe = regexp.MustCompile(
		`(?i)(\d{1,2})\s*(?:st|nd|rd|th)?\s*([A-Za-z]+)\s*to\s*(\d{1,2})\s*([A-Za-z]+)`)
	stampRe = regexp.MustCompile(
		`(\d{4})-(\d{2})-(\d{2})\s+(\d{1,2}):(\d{2}):(\d{2})\s*([ap]m)`)
)

// perPeriodColumns is how many columns each period occupies: normal, actual,
// and percentage of normal.
const perPeriodColumns = 3

// leadingColumns are the serial number and the unit name.
const leadingColumns = 2

func parseMonthly(body []byte) (*Report, error) {
	rows := tableRows(body)
	if len(rows) == 0 {
		return nil, fmt.Errorf("no table rows: %s", excerpt(body))
	}

	rep := &Report{}

	// The header naming the periods is the row whose cells are month labels.
	// Found by shape rather than by index: the portal prefixes a title row and
	// sometimes a generation timestamp, and counting rows from the top has
	// already been wrong once for the circle report.
	var header []string
	for _, r := range rows {
		if looksLikePeriodHeader(r) {
			header = r
			break
		}
	}
	if header == nil {
		return nil, fmt.Errorf(
			"no row of the report names the months it covers; the report's shape "+
				"has changed and its columns can no longer be identified: %s",
			excerpt(body))
	}

	periods, err := parsePeriods(header)
	if err != nil {
		return nil, err
	}
	rep.Periods = periods
	rep.CropYear = periods[0].Year

	if at := findStamp(rows); at != nil {
		rep.ObservedAt = at
	}

	// The body. The markup is not well formed: every unit of the district
	// arrives inside one <tr>, so the widest row is the body and it is split by
	// counting columns.
	width := leadingColumns + perPeriodColumns*len(periods)
	widest := rows[0]
	for _, r := range rows {
		if len(r) > len(widest) {
			widest = r
		}
	}
	if len(widest) < width {
		return nil, fmt.Errorf(
			"the report names %d periods, needing %d columns per unit, but its "+
				"widest row holds only %d cells",
			len(periods), width, len(widest))
	}
	if len(widest)%width != 0 {
		return nil, fmt.Errorf(
			"the body holds %d cells, which is not a whole number of %d-column "+
				"units; the columns cannot be aligned and a misalignment would "+
				"report one unit's rainfall against another's normal",
			len(widest), width)
	}

	for off := 0; off < len(widest); off += width {
		u, err := parseUnit(widest[off:off+width], len(periods))
		if err != nil {
			return nil, fmt.Errorf("unit at column %d: %w", off, err)
		}
		rep.Units = append(rep.Units, u)
	}
	if len(rep.Units) == 0 {
		return nil, fmt.Errorf("the report holds no units: %s", excerpt(body))
	}
	return rep, nil
}

func parseUnit(cells []string, periods int) (UnitRainfall, error) {
	var u UnitRainfall

	serial, err := strconv.Atoi(strings.TrimSpace(cells[0]))
	if err != nil {
		return u, fmt.Errorf("serial %q is not a number, so the columns are "+
			"not aligned where this parser believes they are", cells[0])
	}
	u.Serial = serial

	u.Name = strings.TrimSpace(cells[1])
	if u.Name == "" {
		return u, fmt.Errorf("serial %d has no name", serial)
	}

	for i := 0; i < periods; i++ {
		base := leadingColumns + i*perPeriodColumns
		var r Reading
		for j, dst := range []*float64{&r.NormalMM, &r.ActualMM, &r.PercentOfNormal} {
			v, err := parseMM(cells[base+j])
			if err != nil {
				return u, fmt.Errorf("%s, period %d, column %d: %w",
					u.Name, i+1, j+1, err)
			}
			*dst = v
		}
		u.Values = append(u.Values, r)
	}
	return u, nil
}

// parseMM reads one figure. A blank or a dash is a gauge that reported nothing,
// which the portal prints for a unit with no reading, and which is zero rain
// rather than missing data only because the portal's own totals treat it so.
func parseMM(s string) (float64, error) {
	s = strings.TrimSpace(s)
	s = strings.TrimSuffix(s, "%")
	s = strings.ReplaceAll(s, ",", "")
	if s == "" || s == "-" || s == "--" || s == "NA" {
		return 0, nil
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, fmt.Errorf("%q is not a rainfall figure", s)
	}
	if v < 0 {
		return 0, fmt.Errorf("%q is negative", s)
	}
	return v, nil
}

func looksLikePeriodHeader(cells []string) bool {
	months := 0
	for _, c := range cells {
		if monthRe.MatchString(strings.TrimSpace(c)) {
			months++
		}
	}
	return months >= 1 && cumRe.MatchString(strings.Join(cells, " "))
}

func parsePeriods(header []string) ([]Period, error) {
	var out []Period

	for _, raw := range header {
		c := strings.TrimSpace(raw)
		if m := monthRe.FindStringSubmatch(c); m != nil {
			mon, ok := monthNumber(m[1])
			if !ok {
				return nil, fmt.Errorf("%q does not name a month", m[1])
			}
			year, err := strconv.Atoi(m[2])
			if err != nil {
				return nil, fmt.Errorf("%q is not a year", m[2])
			}
			start := time.Date(year, time.Month(mon), 1, 0, 0, 0, 0, time.UTC)
			out = append(out, Period{
				Month: mon,
				Year:  year,
				Start: start,
				End:   start.AddDate(0, 1, -1),
				Label: c,
			})
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("the header names no months: %v", header)
	}

	// The cumulative column, whose heading carries the season's span.
	year := out[0].Year
	for _, raw := range header {
		c := wsRe.ReplaceAllString(strings.TrimSpace(raw), " ")
		m := cumRe.FindStringSubmatch(c)
		if m == nil {
			continue
		}
		fromDay, _ := strconv.Atoi(m[1])
		fromMon, okFrom := monthNumber(m[2])
		toDay, _ := strconv.Atoi(m[3])
		toMon, okTo := monthNumber(m[4])
		if !okFrom || !okTo {
			return nil, fmt.Errorf(
				"the cumulative column heading %q does not name two months", c)
		}
		out = append(out, Period{
			Month: 0,
			Year:  year,
			Start: time.Date(year, time.Month(fromMon), fromDay, 0, 0, 0, 0, time.UTC),
			End:   time.Date(year, time.Month(toMon), toDay, 0, 0, 0, 0, time.UTC),
			Label: c,
		})
		break
	}
	if !out[len(out)-1].Cumulative() {
		return nil, fmt.Errorf(
			"the report has no season-to-date column, which is the figure a "+
				"drought determination rests on: %v", header)
	}
	return out, nil
}

// monthNumber accepts the full name and the three-letter abbreviation, because
// the portal uses "September" in a month heading and "Sep" in the cumulative
// one.
func monthNumber(name string) (int, bool) {
	n := strings.ToLower(strings.TrimSpace(name))
	for i := time.January; i <= time.December; i++ {
		full := strings.ToLower(i.String())
		if n == full || n == full[:3] {
			return int(i), true
		}
	}
	return 0, false
}

func findStamp(rows [][]string) *time.Time {
	for _, r := range rows {
		for _, c := range r {
			m := stampRe.FindStringSubmatch(c)
			if m == nil {
				continue
			}
			n := func(s string) int { v, _ := strconv.Atoi(s); return v }
			hour := n(m[4])
			if strings.EqualFold(m[7], "pm") && hour != 12 {
				hour += 12
			}
			if strings.EqualFold(m[7], "am") && hour == 12 {
				hour = 0
			}
			// The portal is a Maharashtra government service and prints local
			// time. Reading it as UTC would move a report generated at 9:44 pm
			// into the following day.
			ist := time.FixedZone("IST", 5*3600+1800)
			t := time.Date(n(m[1]), time.Month(n(m[2])), n(m[3]),
				hour, n(m[5]), n(m[6]), 0, ist)
			return &t
		}
	}
	return nil
}

func tableRows(body []byte) [][]string {
	var out [][]string
	for _, m := range rowRe.FindAllStringSubmatch(string(body), -1) {
		var cells []string
		for _, c := range cellRe.FindAllStringSubmatch(m[1], -1) {
			text := html.UnescapeString(tagRe.ReplaceAllString(c[1], " "))
			cells = append(cells, strings.TrimSpace(wsRe.ReplaceAllString(text, " ")))
		}
		if len(cells) > 0 {
			out = append(out, cells)
		}
	}
	return out
}

func excerpt(body []byte) string {
	s := wsRe.ReplaceAllString(strings.TrimSpace(string(body)), " ")
	if len(s) > 200 {
		s = s[:200] + "..."
	}
	if s == "" {
		return "(the response was empty)"
	}
	return s
}
