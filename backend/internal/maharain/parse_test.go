package maharain

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// The fixtures are the portal's real responses for Solapur on 28 September
// 2026, saved unedited. The markup is malformed in a specific way -- the whole
// body inside one <tr> -- and a fixture someone tidied would stop testing the
// thing most likely to break.

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func find(t *testing.T, r *Report, name string) UnitRainfall {
	t.Helper()
	for _, u := range r.Units {
		if u.Name == name {
			return u
		}
	}
	t.Fatalf("%q is not in the report; it holds %d units", name, len(r.Units))
	return UnitRainfall{}
}

func TestTheTalukaReportIsReadColumnForColumn(t *testing.T) {
	// Every figure here was read off the portal's own page by eye. If the
	// column arithmetic drifts by one, Barshi's rainfall is reported against
	// another taluka's normal and nothing downstream can tell.
	r, err := parseMonthly(fixture(t, "tehsil_solapur_2026-09-28.html"))
	if err != nil {
		t.Fatal(err)
	}

	if r.CropYear != 2026 {
		t.Errorf("crop year = %d", r.CropYear)
	}
	if len(r.Units) != 11 {
		t.Errorf("units = %d, want 11 talukas in Solapur", len(r.Units))
	}
	// Four months published in September, plus the cumulative.
	if len(r.Periods) != 5 {
		t.Fatalf("periods = %d, want 4 months and the cumulative", len(r.Periods))
	}
	for i, want := range []int{6, 7, 8, 9, 0} {
		if r.Periods[i].Month != want {
			t.Errorf("period %d is month %d, want %d", i, r.Periods[i].Month, want)
		}
	}
	if !r.Periods[4].Cumulative() {
		t.Error("the last period should be the season-to-date cumulative")
	}

	b := find(t, r, "Barshi")
	for i, want := range []Reading{
		{NormalMM: 107.3, ActualMM: 81.6, PercentOfNormal: 76.0},   // June
		{NormalMM: 123.7, ActualMM: 134.6, PercentOfNormal: 108.8}, // July
		{NormalMM: 124.5, ActualMM: 19.4, PercentOfNormal: 15.6},   // August
		{NormalMM: 161.1, ActualMM: 50.4, PercentOfNormal: 31.3},   // September
		{NormalMM: 516.6, ActualMM: 286.0, PercentOfNormal: 55.4},  // season
	} {
		if got := b.Values[i]; got != want {
			t.Errorf("Barshi period %d = %+v, want %+v", i, got, want)
		}
	}
}

func TestTheDroughtIsVisibleInTheDeparture(t *testing.T) {
	// The point of taking this source at all. ERA5 reported 505 mm for this
	// season and had no normal to compare it against, so it could not say
	// whether that was a good season. The state's own gauges say it was 55% of
	// one, and that August was 16% of one.
	r, err := parseMonthly(fixture(t, "tehsil_solapur_2026-09-28.html"))
	if err != nil {
		t.Fatal(err)
	}
	b := find(t, r, "Barshi")

	season := b.Values[len(b.Values)-1]
	if season.Deficit() < 40 {
		t.Errorf("season deficit = %.1f points; the declared drought is not "+
			"visible in the figure", season.Deficit())
	}
	if aug := b.Values[2]; aug.PercentOfNormal > 25 {
		t.Errorf("August = %.1f%% of normal; expected the month the crop failed "+
			"in to be far below", aug.PercentOfNormal)
	}
}

func TestTheCircleReportReadsWithTheSameParser(t *testing.T) {
	// Both levels publish the same seventeen-column shape. Sharing the parser
	// is only safe if that is actually true, so it is asserted rather than
	// assumed.
	r, err := parseMonthly(fixture(t, "circle_solapur_2026-09-28.html"))
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Units) != 91 {
		t.Errorf("units = %d, want 91 revenue circles in Solapur", len(r.Units))
	}

	b := find(t, r, "Barshi")
	season := b.Values[len(b.Values)-1]
	want := Reading{NormalMM: 516.6, ActualMM: 252.7, PercentOfNormal: 48.9}
	if season != want {
		t.Errorf("Barshi circle season = %+v, want %+v", season, want)
	}
	// The circle is drier than the taluka that contains it, which is the whole
	// reason the circle series is worth storing.
	if season.PercentOfNormal >= 55.4 {
		t.Errorf("the circle figure (%.1f%%) should differ from the taluka's "+
			"55.4%%; if they are identical the wrong report was fetched",
			season.PercentOfNormal)
	}
}

func TestTheSerialAndNameSurviveTheMalformedBody(t *testing.T) {
	// 91 circles arrive inside a single <tr>. The split is by counting columns,
	// so the first and last units are where an off-by-one shows up.
	r, err := parseMonthly(fixture(t, "circle_solapur_2026-09-28.html"))
	if err != nil {
		t.Fatal(err)
	}
	first, last := r.Units[0], r.Units[len(r.Units)-1]
	if first.Serial != 1 {
		t.Errorf("first serial = %d", first.Serial)
	}
	if last.Serial != len(r.Units) {
		t.Errorf("last serial = %d with %d units; the body did not divide "+
			"evenly into units", last.Serial, len(r.Units))
	}
	for _, u := range r.Units {
		if u.Name == "" {
			t.Errorf("unit %d has no name", u.Serial)
		}
		if len(u.Values) != len(r.Periods) {
			t.Errorf("%s has %d readings for %d periods",
				u.Name, len(u.Values), len(r.Periods))
		}
	}
}

func TestTheGenerationTimeIsReadInLocalTime(t *testing.T) {
	// A report generated at 9:44 pm IST read as UTC lands on the following day,
	// and a rainfall figure's age is part of the evidence.
	r, err := parseMonthly(fixture(t, "circle_solapur_2026-09-28.html"))
	if err != nil {
		t.Fatal(err)
	}
	if r.ObservedAt == nil {
		t.Skip("this fixture carries no generation timestamp")
	}
	if got := r.ObservedAt.Format("2006-01-02 15:04"); got != "2026-09-28 21:44" {
		t.Errorf("observed at %s, want 2026-09-28 21:44 IST", got)
	}
}

// --- refusals ---------------------------------------------------------------

func TestAMisalignedBodyIsRefusedRatherThanGuessed(t *testing.T) {
	// The failure that matters. A body that does not divide into whole units
	// means the columns have moved, and reading it anyway would put one unit's
	// rainfall against another's normal -- a wrong figure that looks right.
	h := `<table>
	<tr><th>Sr</th><th>Tehsil</th><th>June, 2026</th><th>1st June to 28 Sep</th></tr>
	<tr><td>1</td><td>Barshi</td><td>107.3</td><td>81.6</td><td>76.0</td>
	    <td>516.6</td><td>286.0</td><td>55.4</td><td>2</td><td>Karmala</td></tr>
	</table>`

	_, err := parseMonthly([]byte(h))
	if err == nil {
		t.Fatal("a body with a trailing partial unit was accepted")
	}
	if !strings.Contains(err.Error(), "against another") {
		t.Errorf("the refusal should say what the risk is: %v", err)
	}
}

func TestAReportWithNoMonthHeaderIsRefused(t *testing.T) {
	// The portal returns its own login page, or a plain error sentence, for
	// several kinds of bad request. Either would parse as zero units and read
	// as a district with no rainfall.
	for _, body := range []string{
		"Year should be LESS THAN 2026...",
		"<table><tr><td>1</td><td>Barshi</td><td>107.3</td></tr></table>",
		"",
	} {
		if _, err := parseMonthly([]byte(body)); err == nil {
			t.Errorf("%q was accepted as a report", body)
		}
	}
}

func TestANonNumericFigureIsRefused(t *testing.T) {
	h := `<table>
	<tr><th>Sr</th><th>June, 2026</th><th>1st June to 28 Sep</th></tr>
	<tr><td>1</td><td>Barshi</td><td>107.3</td><td>oops</td><td>76.0</td>
	    <td>516.6</td><td>286.0</td><td>55.4</td></tr>
	</table>`
	_, err := parseMonthly([]byte(h))
	if err == nil {
		t.Fatal("a non-numeric rainfall figure was accepted")
	}
	if !strings.Contains(err.Error(), "oops") {
		t.Errorf("the refusal should quote the offending value: %v", err)
	}
}

func TestABlankGaugeReadsAsZeroNotAsAnError(t *testing.T) {
	// The portal prints a dash for a unit that reported nothing, and its own
	// totals treat that as zero.
	for _, in := range []string{"", "-", "--", "NA", " 0.0 "} {
		v, err := parseMM(in)
		if err != nil {
			t.Errorf("%q: %v", in, err)
		}
		if v != 0 {
			t.Errorf("%q read as %v", in, v)
		}
	}
}

// --- the district handler ---------------------------------------------------

func TestTheDistrictHandlerIsReadAndItsBlankRowDropped(t *testing.T) {
	body := []byte(`{"status":200,"response":"District Details Found.","data":` +
		`[{"district_code":"15","district_name":"Kolhapur"},` +
		`{"district_code":"12","district_name":"Solapur"},` +
		`{"district_code":"","district_name":""}]}`)

	got, err := parseDistricts(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("districts = %d, want the blank-coded row dropped", len(got))
	}
	if got[1].Code != "12" || got[1].Name != "Solapur" {
		t.Errorf("second district = %+v", got[1])
	}
}

func TestAnEmptyDivisionIsNotAnError(t *testing.T) {
	// Divisions 7 and 8 answer with an empty list. That is how the range of
	// real divisions was established, and it must not read as a failure.
	got, err := parseDistricts([]byte(
		`{"status":200,"response":"District Details Found.","data":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("districts = %d", len(got))
	}
}

func TestANonJSONAnswerFromTheHandlerIsRefused(t *testing.T) {
	if _, err := parseDistricts([]byte("<html>login</html>")); err == nil {
		t.Fatal("an HTML page was accepted as the district list")
	}
}

// --- being a guest on somebody else's server --------------------------------

func TestReportsAreSpacedApart(t *testing.T) {
	// Each report is generated on demand over a whole district. Two in quick
	// succession is the behaviour that gets a scraper blocked, and this one has
	// no fallback source.
	c := &Client{}
	start := time.Now()
	for i := 0; i < 3; i++ {
		if err := c.pace(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	// Two gaps between three requests.
	if elapsed := time.Since(start); elapsed < 2*MinInterval {
		t.Errorf("three requests took %v; they should be at least %v apart",
			elapsed, MinInterval)
	}
}

func TestTheFirstRequestIsNotDelayed(t *testing.T) {
	c := &Client{}
	start := time.Now()
	if err := c.pace(context.Background()); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > time.Second {
		t.Error("the first request should go straight out")
	}
}

func TestAskingInALoopIsRefused(t *testing.T) {
	// The failure this cap exists for: a bug in district enumeration asking for
	// the same report over and over is indistinguishable, from the portal's
	// side, from an attack.
	c := &Client{lastReport: time.Now().Add(-time.Hour)}
	c.reports = MaxReportsPerRun
	err := c.pace(context.Background())
	if err == nil {
		t.Fatal("the cap did not stop the run")
	}
	if !strings.Contains(err.Error(), "public service") {
		t.Errorf("the refusal should say why: %v", err)
	}
}

func TestPacingHonoursCancellation(t *testing.T) {
	// A shutdown must not be held up for six seconds per pending request.
	c := &Client{lastReport: time.Now()}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := c.pace(ctx); err == nil {
		t.Error("a cancelled context should abandon the wait")
	}
}

func TestTheClientIdentifiesItself(t *testing.T) {
	// An untraceable scraper deserves to be blocked. The operators should be
	// able to see who this is.
	c, err := New()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(c.UserAgent, "eSurvey") {
		t.Errorf("user agent = %q", c.UserAgent)
	}
}
