package weather

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// The windows are the point of this package, so most of what is tested is
// arithmetic over a canned series: the dry spell a fortnight cannot see, the
// sowing date the rain record contradicts, the hail code that means less than
// it looks like.

// archive stands in for Open-Meteo.
func archive(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	c := New(nil)
	c.ArchiveURL = srv.URL
	c.HTTP.Timeout = 10 * time.Second
	return c
}

// canned builds an Open-Meteo response from daily values.
type day struct {
	date       string
	tmax, tmin float64
	rain       float64
	gust       float64
	code       int
}

func canned(days ...day) string {
	var t, mx, mn, rn, hrs, wind, gust, code []string
	for _, d := range days {
		t = append(t, `"`+d.date+`"`)
		mx = append(mx, fmt.Sprintf("%v", d.tmax))
		mn = append(mn, fmt.Sprintf("%v", d.tmin))
		rn = append(rn, fmt.Sprintf("%v", d.rain))
		hrs = append(hrs, "0")
		wind = append(wind, "10")
		gust = append(gust, fmt.Sprintf("%v", d.gust))
		code = append(code, fmt.Sprintf("%d", d.code))
	}
	j := func(v []string) string { return "[" + strings.Join(v, ",") + "]" }
	return `{"daily":{"time":` + j(t) +
		`,"temperature_2m_max":` + j(mx) +
		`,"temperature_2m_min":` + j(mn) +
		`,"precipitation_sum":` + j(rn) +
		`,"precipitation_hours":` + j(hrs) +
		`,"wind_speed_10m_max":` + j(wind) +
		`,"wind_gusts_10m_max":` + j(gust) +
		`,"weather_code":` + j(code) + `}}`
}

// run builds a series of identical days.
func run(from string, n int, d day) []day {
	start, _ := time.Parse(dayLayout, from)
	out := make([]day, n)
	for i := range out {
		out[i] = d
		out[i].date = start.AddDate(0, 0, i).Format(dayLayout)
	}
	return out
}

// past returns a date safely inside the archive's reach.
func past(daysAgo int) string {
	return time.Now().AddDate(0, 0, -daysAgo).Format(dayLayout)
}

// ---------------------------------------------------------------------------
// Fetching
// ---------------------------------------------------------------------------

func TestTheArchiveIsAskedForEveryVariableTheAssessmentUses(t *testing.T) {
	// A variable that is silently not requested comes back as an empty array,
	// reads as zero, and turns a 90 km/h gust into a calm day.
	var got string
	c := archive(t, func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Query().Get("daily")
		_, _ = io.WriteString(w, canned(day{date: past(30), tmax: 30, tmin: 20}))
	})

	if _, err := c.Range(context.Background(), 18.5, 73.8, past(30), past(29)); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"temperature_2m_max", "temperature_2m_min",
		"precipitation_sum", "wind_gusts_10m_max", "weather_code",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the request does not ask for %s", want)
		}
	}
}

func TestTheRecordIsReadInTheZoneTheOfficerReadsIt(t *testing.T) {
	// A hailstorm at 16:40 IST lands on the previous day in UTC, and the
	// incident date on the form would then miss it.
	var tz string
	c := archive(t, func(w http.ResponseWriter, r *http.Request) {
		tz = r.URL.Query().Get("timezone")
		_, _ = io.WriteString(w, canned(day{date: past(30), tmax: 30, tmin: 20}))
	})

	if _, err := c.Range(context.Background(), 18.5, 73.8, past(30), past(29)); err != nil {
		t.Fatal(err)
	}
	if tz != "Asia/Kolkata" {
		t.Errorf("timezone = %q", tz)
	}
}

func TestPointsInsideOneGridCellAskOneQuestion(t *testing.T) {
	// The reanalysis has eleven-kilometre resolution, so two farms a
	// kilometre apart get identical numbers. Snapping loses nothing and lets
	// a locality answer from one fetch.
	var asked []string
	c := archive(t, func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.URL.Query().Get("latitude")+","+
			r.URL.Query().Get("longitude"))
		_, _ = io.WriteString(w, canned(day{date: past(30), tmax: 30, tmin: 20}))
	})

	for _, p := range [][2]float64{{18.51, 73.81}, {18.53, 73.83}, {18.49, 73.79}} {
		if _, err := c.Range(context.Background(), p[0], p[1], past(30), past(29)); err != nil {
			t.Fatal(err)
		}
	}

	first := asked[0]
	for _, a := range asked {
		if a != first {
			t.Errorf("three points in one cell asked about %s and %s", first, a)
		}
	}
}

func TestTheArchivesOwnRefusalReachesTheCaller(t *testing.T) {
	// It refuses several things precisely -- a date beyond its reach, a bad
	// coordinate -- and paraphrasing that into "weather unavailable" loses the
	// only useful part of the message.
	c := archive(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w,
			`{"error":true,"reason":"Parameter 'start_date' is out of allowed range"}`)
	})

	_, err := c.Range(context.Background(), 18.5, 73.8, past(30), past(29))
	if err == nil {
		t.Fatal("a refusal came back as success")
	}
	if !strings.Contains(err.Error(), "out of allowed range") {
		t.Errorf("the archive's reason was lost: %v", err)
	}
}

func TestAWindowBeyondTheArchiveIsTrimmedAndTheShortfallReported(t *testing.T) {
	// The archive trails real time by about five days. A window running to
	// today comes back short, and reporting it as a calm few days would be a
	// fabrication.
	c := archive(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, canned(run(past(10), 5, day{tmax: 30, tmin: 20})...))
	})

	s, err := c.Range(context.Background(), 18.5, 73.8, past(10),
		time.Now().Format(dayLayout))
	if err != nil {
		t.Fatal(err)
	}
	if s.Missing <= 0 {
		t.Errorf("missing = %d; days beyond the archive should be reported", s.Missing)
	}
}

func TestAWindowEntirelyBeyondTheArchiveIsAnError(t *testing.T) {
	c := archive(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, canned())
	})

	tomorrow := time.Now().AddDate(0, 0, 1).Format(dayLayout)
	if _, err := c.Range(context.Background(), 18.5, 73.8, tomorrow, tomorrow); err == nil {
		t.Fatal("a window entirely in the future returned a series")
	}
}

func TestAMissingVariableDoesNotPanic(t *testing.T) {
	// Open-Meteo omits a variable it has no value for, which decodes to a
	// short array. Indexing blind would panic on exactly the days that are
	// missing.
	c := archive(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"daily":{
			"time":["`+past(30)+`","`+past(29)+`"],
			"temperature_2m_max":[31.0,32.0],
			"temperature_2m_min":[20.0],
			"precipitation_sum":[],
			"weather_code":[0,0]
		}}`)
	})

	s, err := c.Range(context.Background(), 18.5, 73.8, past(30), past(29))
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Days) != 2 {
		t.Fatalf("days = %d", len(s.Days))
	}
	if s.Days[1].TMinC != 0 {
		t.Errorf("a missing minimum became %v", s.Days[1].TMinC)
	}
}

func TestTheSeriesIsAlwaysInDateOrder(t *testing.T) {
	c := archive(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, canned(
			day{date: past(28), tmax: 30, tmin: 20},
			day{date: past(30), tmax: 31, tmin: 21},
			day{date: past(29), tmax: 32, tmin: 22},
		))
	})

	s, err := c.Range(context.Background(), 18.5, 73.8, past(30), past(28))
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i < len(s.Days); i++ {
		if s.Days[i-1].Date > s.Days[i].Date {
			t.Fatalf("out of order at %d: %s then %s", i, s.Days[i-1].Date, s.Days[i].Date)
		}
	}
}

func TestAReversedRangeIsRefused(t *testing.T) {
	c := archive(t, func(w http.ResponseWriter, r *http.Request) {})
	if _, err := c.Range(context.Background(), 18.5, 73.8, past(10), past(30)); err == nil {
		t.Error("a range ending before it starts was accepted")
	}
}

// ---------------------------------------------------------------------------
// The windows
// ---------------------------------------------------------------------------

func seriesOf(days ...day) *Series {
	s := &Series{Lat: 18.5, Lon: 73.8, Source: SourceERA5, ResolutionKm: era5GridKm}
	for _, d := range days {
		s.Days = append(s.Days, Daily{
			Date: d.date, TMaxC: d.tmax, TMinC: d.tmin,
			RainMM: d.rain, GustMaxKmh: d.gust, WeatherCode: d.code,
		})
	}
	return s
}

func TestTheLongestDrySpellIsFound(t *testing.T) {
	// The single most useful number a fourteen-day window could never
	// produce, and the one a drought claim turns on.
	days := run("2026-06-15", 60, day{tmax: 33, tmin: 23})
	// Rain at the start, then a long dry run, then rain again.
	days[0].rain = 20
	days[1].rain = 15
	for i := 2; i < 40; i++ {
		days[i].rain = 0
	}
	days[40].rain = 25

	s := seriesOf(days...)
	w, err := s.Between("2026-06-15", "2026-08-13")
	if err != nil {
		t.Fatal(err)
	}
	if w.LongestDrySpell != 38 {
		t.Errorf("longest dry spell = %d days, want 38", w.LongestDrySpell)
	}
	if w.LongestDrySpellFrom != "2026-06-17" {
		t.Errorf("dry spell starts %s, want 2026-06-17", w.LongestDrySpellFrom)
	}
}

func TestATraceOfRainDoesNotBreakADrySpell(t *testing.T) {
	// A tenth of a millimetre is dew on the instrument, not rain on the crop.
	days := run("2026-06-15", 30, day{tmax: 33, tmin: 23, rain: 0.2})
	s := seriesOf(days...)

	w, err := s.Between("2026-06-15", "2026-07-14")
	if err != nil {
		t.Fatal(err)
	}
	if w.LongestDrySpell != 30 {
		t.Errorf("dry spell = %d; a trace of rain broke it", w.LongestDrySpell)
	}
	if w.RainDays != 0 {
		t.Errorf("rain days = %d; a trace is not a rain day", w.RainDays)
	}
}

func TestHeavyRainfallUsesTheDepartmentsThreshold(t *testing.T) {
	days := run("2026-06-15", 5, day{tmax: 30, tmin: 24})
	days[1].rain = 64.5 // exactly the threshold
	days[3].rain = 40   // heavy to a person, not to the definition

	s := seriesOf(days...)
	w, err := s.Between("2026-06-15", "2026-06-19")
	if err != nil {
		t.Fatal(err)
	}
	if w.HeavyDays != 1 {
		t.Errorf("heavy days = %d, want 1", w.HeavyDays)
	}
	if w.MaxDailyMM != 64.5 || w.MaxDailyOn != "2026-06-16" {
		t.Errorf("wettest day = %v on %s", w.MaxDailyMM, w.MaxDailyOn)
	}
}

func TestHailAndThunderAreCountedSeparately(t *testing.T) {
	// Every hail day is a thunderstorm day; most thunderstorm days are not
	// hail days. Collapsing them would turn every storm into a hail event.
	s := seriesOf(
		day{date: "2026-03-01", tmax: 33, tmin: 20, code: 95},
		day{date: "2026-03-02", tmax: 32, tmin: 20, code: 96},
		day{date: "2026-03-03", tmax: 31, tmin: 19, code: 99},
		day{date: "2026-03-04", tmax: 34, tmin: 21, code: 61},
	)
	w, err := s.Between("2026-03-01", "2026-03-04")
	if err != nil {
		t.Fatal(err)
	}
	if w.HailDays != 2 {
		t.Errorf("hail days = %d, want 2", w.HailDays)
	}
	if w.ThunderDays != 3 {
		t.Errorf("thunder days = %d, want 3", w.ThunderDays)
	}
}

func TestTheStrongestGustIsKeptWithItsDate(t *testing.T) {
	// A cyclone claim turns on it, and a figure with no date cannot be
	// matched against the incident the farmer reported.
	days := run("2026-03-01", 10, day{tmax: 33, tmin: 22, gust: 20})
	days[6].gust = 88

	s := seriesOf(days...)
	w, err := s.Between("2026-03-01", "2026-03-10")
	if err != nil {
		t.Fatal(err)
	}
	if w.MaxGustKmh != 88 {
		t.Errorf("max gust = %v", w.MaxGustKmh)
	}
	if w.MaxGustOn != "2026-03-07" {
		t.Errorf("max gust on %s, want 2026-03-07", w.MaxGustOn)
	}
}

func TestAWindowWithNoRecordsIsEmptyRatherThanInfinite(t *testing.T) {
	// The hottest and coldest start at infinity. Leaking that into a record
	// would print +Inf on an officer's screen.
	s := seriesOf(day{date: "2026-03-01", tmax: 33, tmin: 22})
	w, err := s.Between("2026-06-01", "2026-06-10")
	if err != nil {
		t.Fatal(err)
	}
	if w.Days != 0 {
		t.Errorf("days = %d", w.Days)
	}
	if w.HottestC != 0 || w.ColdestC != 0 {
		t.Errorf("hottest %v, coldest %v for an empty window", w.HottestC, w.ColdestC)
	}
	if w.Missing != 10 {
		t.Errorf("missing = %d, want 10", w.Missing)
	}
}

func TestDaysOutsideTheWindowAreNotCounted(t *testing.T) {
	s := seriesOf(
		day{date: "2026-05-31", tmax: 45, tmin: 30, rain: 100},
		day{date: "2026-06-01", tmax: 30, tmin: 20},
		day{date: "2026-06-02", tmax: 31, tmin: 21},
		day{date: "2026-06-03", tmax: 45, tmin: 30, rain: 100},
	)
	w, err := s.Between("2026-06-01", "2026-06-02")
	if err != nil {
		t.Fatal(err)
	}
	if w.RainMM != 0 {
		t.Errorf("rain = %v; days outside the window were counted", w.RainMM)
	}
	if w.HottestC != 31 {
		t.Errorf("hottest = %v, want 31", w.HottestC)
	}
}

// ---------------------------------------------------------------------------
// The record
// ---------------------------------------------------------------------------

func TestASeasonLongDrySpellIsReportedEvenWhenHailWasClaimed(t *testing.T) {
	// A crop that had already been through five weeks without rain is a
	// different assessment from one that had not, whatever the farmer
	// reported. A fortnight before the incident cannot see it.
	days := run("2026-06-15", 100, day{tmax: 34, tmin: 24})
	days[0].rain = 30
	for i := 1; i < 40; i++ {
		days[i].rain = 0
	}
	days[40].rain = 20
	for i := 41; i < 100; i++ {
		days[i].rain = 5
	}
	// The hailstorm.
	days[95].code = 96
	days[95].gust = 70

	s := seriesOf(days...)
	r, err := s.Assemble("2026-06-15", "2026-09-18", Span{})
	if err != nil {
		t.Fatal(err)
	}

	if r.Season.LongestDrySpell < 30 {
		t.Errorf("season dry spell = %d days", r.Season.LongestDrySpell)
	}
	if r.Incident.LongestDrySpell >= 30 {
		t.Errorf("the fortnight window somehow saw the dry spell: %d",
			r.Incident.LongestDrySpell)
	}

	var mentioned bool
	for _, n := range r.Notes {
		if strings.Contains(n, "longest dry spell of the season") {
			mentioned = true
		}
	}
	if !mentioned {
		t.Errorf("the dry spell is not surfaced: %v", r.Notes)
	}
}

func TestASowingDateWithNoRainAroundItIsQuestioned(t *testing.T) {
	// A dryland farmer waits for rain to sow. Three weeks of nothing around
	// the declared date means the date is approximate, or the crop was sown
	// under irrigation -- both worth knowing, neither an accusation.
	days := run("2026-05-20", 90, day{tmax: 38, tmin: 26})
	for i := 40; i < 90; i++ {
		days[i].rain = 8
	}

	s := seriesOf(days...)
	r, err := s.Assemble("2026-06-15", "2026-08-01", Span{})
	if err != nil {
		t.Fatal(err)
	}

	var flagged bool
	for _, n := range r.Notes {
		if strings.Contains(n, "sowing date") {
			flagged = true
			if !strings.Contains(n, "irrigation") {
				t.Errorf("the note offers no ordinary explanation: %s", n)
			}
		}
	}
	if !flagged {
		t.Errorf("a dry sowing window was not remarked on: %v", r.Notes)
	}
}

func TestTheHailNoteSaysWhatTheCodeActuallyMeans(t *testing.T) {
	// It means hail was recorded somewhere in an eleven-kilometre cell. An
	// officer reading "hail: yes" would take it for confirmation.
	days := run("2026-03-01", 20, day{tmax: 33, tmin: 21})
	days[18].code = 96

	s := seriesOf(days...)
	r, err := s.Assemble("2026-01-01", "2026-03-20", Span{})
	if err != nil {
		t.Fatal(err)
	}

	var found bool
	for _, n := range r.Notes {
		if strings.Contains(n, "hail") {
			found = true
			if !strings.Contains(n, "eleven-kilometre") {
				t.Errorf("the hail note overstates what the code means: %s", n)
			}
		}
	}
	if !found {
		t.Errorf("a hail-coded day produced no note: %v", r.Notes)
	}
}

func TestTheRecordCarriesTheResolutionItWasReadAt(t *testing.T) {
	// Nothing downstream should read a district-scale figure as a field-scale
	// one.
	s := seriesOf(day{date: "2026-03-01", tmax: 33, tmin: 21})
	r, err := s.Assemble("2026-02-01", "2026-03-01", Span{})
	if err != nil {
		t.Fatal(err)
	}
	if r.Point.ResolutionKm != era5GridKm {
		t.Errorf("resolution = %v", r.Point.ResolutionKm)
	}
	if r.Source != SourceERA5 {
		t.Errorf("source = %q", r.Source)
	}
}

func TestAnIncompleteSeasonSaysItsTotalsAreLowerBounds(t *testing.T) {
	s := seriesOf(run("2026-06-15", 10, day{tmax: 33, tmin: 23, rain: 5})...)
	r, err := s.Assemble("2026-06-15", "2026-09-15", Span{})
	if err != nil {
		t.Fatal(err)
	}
	var said bool
	for _, n := range r.Notes {
		if strings.Contains(n, "lower bounds") {
			said = true
		}
	}
	if !said {
		t.Errorf("a season with most days missing did not qualify its totals: %v",
			r.Notes)
	}
}

func TestTheWindowsFeedTheAgronomyPackageUnchanged(t *testing.T) {
	// Thermal time must be computed from the same numbers the officer sees,
	// or the stage and the weather record will disagree in a way nobody can
	// reconcile.
	s := seriesOf(
		day{date: "2026-06-15", tmax: 33, tmin: 23},
		day{date: "2026-06-16", tmax: 34, tmin: 24},
	)
	days := s.Days2Agronomy()
	if len(days) != 2 {
		t.Fatalf("converted %d days", len(days))
	}
	if days[0].TMaxC != 33 || days[1].TMinC != 24 {
		t.Errorf("the conversion changed the numbers: %+v", days)
	}
}

// ---------------------------------------------------------------------------
// Against the real archive
// ---------------------------------------------------------------------------

func TestTheRealArchiveAnswersTheShapeWeParse(t *testing.T) {
	// Guarded, because a test suite that fails when a third party is down is
	// a test suite people learn to ignore. It is worth having because the
	// parallel-array shape is the one thing httptest cannot verify: a rename
	// upstream would leave every field zero and every window calm.
	if os.Getenv("ESURVEY_TEST_LIVE_WEATHER") == "" {
		t.Skip("set ESURVEY_TEST_LIVE_WEATHER=1 to check the live archive")
	}

	c := New(nil)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	s, err := c.Range(ctx, 18.52, 73.86, past(40), past(10))
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Days) < 25 {
		t.Fatalf("got %d days for a thirty-day window", len(s.Days))
	}

	var warm, named int
	for _, d := range s.Days {
		if d.TMaxC > 5 && d.TMaxC < 55 {
			warm++
		}
		if d.Date != "" {
			named++
		}
	}
	if warm < len(s.Days)/2 {
		t.Errorf("only %d of %d days have a plausible maximum; the variable "+
			"names may have changed upstream", warm, len(s.Days))
	}
	if named != len(s.Days) {
		t.Errorf("%d days have no date", len(s.Days)-named)
	}

	raw, _ := json.Marshal(s.Days[0])
	t.Logf("first day: %s", raw)
}

// ---------------------------------------------------------------------------
// The Google cross-check
// ---------------------------------------------------------------------------

func googleAt(t *testing.T, key string, h http.HandlerFunc) *GoogleClient {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	g := NewGoogle(key)
	g.BaseURL = srv.URL
	g.HTTP.Timeout = 10 * time.Second
	return g
}

func TestWithNoKeyTheLiveSourceIsSimplyOff(t *testing.T) {
	// The ordinary deployed state, and not a fault. Reporting it as an error
	// would fill the log with something nobody should act on.
	g := NewGoogle("")
	if g.Enabled() {
		t.Error("a client with no key reports itself enabled")
	}
	r, ok, err := g.LastDay(context.Background(), 18.5, 73.8)
	if err != nil {
		t.Errorf("an unconfigured client errored: %v", err)
	}
	if ok || r != nil {
		t.Error("an unconfigured client returned a reading")
	}
}

func TestTheLiveReadingIsSummarisedOverTheHoursItCovers(t *testing.T) {
	g := googleAt(t, "k", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"historyHours":[
			{"interval":{"startTime":"2026-03-01T10:00:00Z"},
			 "temperature":{"degrees":31.0,"unit":"CELSIUS"},
			 "precipitation":{"qpf":{"quantity":4.0,"unit":"MILLIMETERS"}},
			 "wind":{"gust":{"value":40.0,"unit":"KILOMETERS_PER_HOUR"}}},
			{"interval":{"startTime":"2026-03-01T11:00:00Z"},
			 "temperature":{"degrees":27.5,"unit":"CELSIUS"},
			 "precipitation":{"qpf":{"quantity":8.0,"unit":"MILLIMETERS"}},
			 "wind":{"gust":{"value":72.0,"unit":"KILOMETERS_PER_HOUR"}}}
		]}`)
	})

	got, ok, err := g.LastDay(context.Background(), 18.5, 73.8)
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if got.RainMM != 12 {
		t.Errorf("rain = %v, want 12 (the hours summed)", got.RainMM)
	}
	if got.MaxGustKmh != 72 {
		t.Errorf("max gust = %v", got.MaxGustKmh)
	}
	if got.MaxTempC != 31 || got.MinTempC != 27.5 {
		t.Errorf("temps = %v to %v", got.MinTempC, got.MaxTempC)
	}
	if got.Hours != 2 {
		t.Errorf("hours = %d", got.Hours)
	}
}

func TestGoogleSOwnRefusalReachesTheOperator(t *testing.T) {
	// "API not enabled" and "billing not configured" are operator actions.
	// Paraphrasing them into "weather unavailable" sends somebody debugging
	// the wrong thing.
	g := googleAt(t, "k", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"error":{"code":403,
			"message":"Weather API has not been used in project 501512 before or it is disabled",
			"status":"PERMISSION_DENIED"}}`)
	})

	_, _, err := g.LastDay(context.Background(), 18.5, 73.8)
	if err == nil {
		t.Fatal("a refusal came back as success")
	}
	if !strings.Contains(err.Error(), "has not been used") {
		t.Errorf("the API's own message was lost: %v", err)
	}
	if !strings.Contains(err.Error(), "PERMISSION_DENIED") {
		t.Errorf("the status was lost: %v", err)
	}
}

func TestAnEmptyHistoryIsNotAReading(t *testing.T) {
	g := googleAt(t, "k", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"historyHours":[]}`)
	})
	_, ok, err := g.LastDay(context.Background(), 18.5, 73.8)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("an empty history was reported as a reading")
	}
}

func TestTheKeyIsSentAndNeverLogged(t *testing.T) {
	var got string
	g := googleAt(t, "secret-key", func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Query().Get("key")
		_, _ = io.WriteString(w, `{"historyHours":[]}`)
	})
	if _, _, err := g.LastDay(context.Background(), 18.5, 73.8); err != nil {
		t.Fatal(err)
	}
	if got != "secret-key" {
		t.Errorf("key = %q", got)
	}

	// The failure path must not put it in the message an operator pastes into
	// a ticket.
	g2 := googleAt(t, "secret-key", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"error":{"message":"denied","status":"PERMISSION_DENIED"}}`)
	})
	_, _, err := g2.LastDay(context.Background(), 18.5, 73.8)
	if err == nil {
		t.Fatal("expected a refusal")
	}
	if strings.Contains(err.Error(), "secret-key") {
		t.Errorf("the API key is in the error message: %v", err)
	}
}

func TestACrossCheckReportsDisagreementInWords(t *testing.T) {
	// An eleven-kilometre grid can miss a convective cell entirely, which is
	// exactly the case a hail claim turns on.
	live := &Recent{Hours: 24, RainMM: 38, MaxGustKmh: 75}
	grid := Window{MaxDailyMM: 0.4, MaxGustKmh: 22}

	s := CrossCheck(grid, live)
	if !strings.Contains(s, "convective") {
		t.Errorf("the disagreement is not explained: %s", s)
	}
}

func TestACrossCheckWithNothingLiveSaysNothing(t *testing.T) {
	if s := CrossCheck(Window{}, nil); s != "" {
		t.Errorf("a cross-check with no live reading produced %q", s)
	}
}

func TestTwoAgreeingSourcesSayTheyAgree(t *testing.T) {
	live := &Recent{Hours: 24, RainMM: 15, MaxGustKmh: 30}
	grid := Window{MaxDailyMM: 18, MaxGustKmh: 35}

	s := CrossCheck(grid, live)
	if !strings.Contains(s, "agrees") {
		t.Errorf("two agreeing sources produced %q", s)
	}
}

// ---------------------------------------------------------------------------
// Barshi, September 2026 -- the drought the platform could not see
// ---------------------------------------------------------------------------

// barshiSeptember is what the archive actually returned for 18.2N 75.8E,
// 1 to 20 September 2026: the window the Collector declared drought over.
//
// Thirty-seven millimetres across twenty days, three of which reached 2.5 mm.
// Kept verbatim because the whole argument is about how a threshold reads
// exactly these numbers.
var barshiSeptember = []float64{
	0.80, 5.50, 1.40, 4.00, 1.20, 2.10, 0.40, 1.40, 1.10, 0.40,
	0.70, 0.10, 1.20, 0.00, 0.30, 0.30, 0.30, 2.10, 0.50, 13.60,
}

func barshi() *Series {
	start, _ := time.Parse(dayLayout, "2026-09-01")
	s := &Series{Lat: 18.2, Lon: 75.8, Source: SourceERA5, ResolutionKm: era5GridKm}
	for i, mm := range barshiSeptember {
		s.Days = append(s.Days, Daily{
			Date:   start.AddDate(0, 0, i).Format(dayLayout),
			TMaxC:  33,
			TMinC:  22,
			RainMM: mm,
		})
	}
	return s
}

func TestADroughtIsNotBrokenByReanalysisDrizzle(t *testing.T) {
	// The defect this threshold was changed for. Seven days of the declared
	// window hold between 1.0 and 2.5 mm -- a model artefact of averaging
	// convection over an eleven-kilometre cell, not rain that reached a
	// soybean root zone. At the old 1.0 mm bar each of them broke the spell,
	// and twenty days of drought computed as four.
	w, err := barshi().Between("2026-09-01", "2026-09-20")
	if err != nil {
		t.Fatal(err)
	}

	if w.LongestDrySpell < 14 {
		t.Errorf("longest dry spell = %d days; a drizzling reanalysis has "+
			"hidden a drought the district declared", w.LongestDrySpell)
	}
	if w.LongestDrySpellFrom != "2026-09-05" {
		t.Errorf("the spell starts at %q, want 2026-09-05", w.LongestDrySpellFrom)
	}
	if w.RainDays != 3 {
		t.Errorf("rain days = %d, want 3", w.RainDays)
	}
	if w.TraceDays < 10 {
		t.Errorf("trace days = %d; the band that caused this is not being "+
			"counted", w.TraceDays)
	}
}

func TestADryDayAndANotRainyDayAreTheSameDay(t *testing.T) {
	// The two thresholds disagreeing is what produced a window reporting both
	// "it rained on 3 of 20 days" and "the longest dry spell was 4 days".
	if dryDayMM != rainDayMM {
		t.Errorf("dryDayMM = %v and rainDayMM = %v; a day that was not a rain "+
			"day must be a dry day", dryDayMM, rainDayMM)
	}
}

func TestADeclaredWindowIsWhatTheWeatherIsJudgedOver(t *testing.T) {
	// Barshi's claim was reported on 1 September, the first day of a drought
	// the district declared to 20 September. A fortnight back from the
	// reported date is 18 August to 1 September -- the two weeks immediately
	// before the declaration, holding the last of the rain. The specialist
	// was shown those and asked whether drought was consistent with them.
	s := barshi()
	// The fortnight before the reported date, wet.
	start, _ := time.Parse(dayLayout, "2026-08-18")
	for i := 0; i < 14; i++ {
		s.Days = append(s.Days, Daily{
			Date:  start.AddDate(0, 0, i).Format(dayLayout),
			TMaxC: 31, TMinC: 23, RainMM: 12,
		})
	}

	r, err := s.Assemble("2026-06-15", "2026-09-01",
		Span{From: "2026-09-01", To: "2026-09-20"})
	if err != nil {
		t.Fatal(err)
	}

	if r.IncidentSource != IncidentDeclared {
		t.Fatalf("incident window source = %q", r.IncidentSource)
	}
	if r.Incident.From != "2026-09-01" || r.Incident.To != "2026-09-20" {
		t.Errorf("incident window = %s..%s, want the declared 2026-09-01..2026-09-20",
			r.Incident.From, r.Incident.To)
	}
	if r.Incident.LongestDrySpell < 14 {
		t.Errorf("the declared window shows a %d-day dry spell",
			r.Incident.LongestDrySpell)
	}

	// And the season must not stop on the reported date, or nineteen days of
	// the declared drought fall outside the season it is judged in.
	if r.Season.To != "2026-09-20" {
		t.Errorf("season ends %s; the declared window runs past it", r.Season.To)
	}

	var said bool
	for _, n := range r.Notes {
		if strings.Contains(n, "the survey event declared") {
			said = true
		}
	}
	if !said {
		t.Errorf("the record does not say which window it used: %v", r.Notes)
	}
}

func TestWithNoDeclarationTheFortnightStillApplies(t *testing.T) {
	r, err := barshi().Assemble("2026-09-01", "2026-09-20", Span{})
	if err != nil {
		t.Fatal(err)
	}
	if r.IncidentSource != IncidentFortnight {
		t.Errorf("incident window source = %q", r.IncidentSource)
	}
	if r.Incident.From != "2026-09-06" {
		t.Errorf("incident window starts %s, want a fortnight before the "+
			"reported date", r.Incident.From)
	}
}

func TestASeasonBuiltFromTracesSaysSo(t *testing.T) {
	// 468 mm reads as an adequate season. 468 mm of which half the wet days
	// held under 2.5 mm does not, and drought is exactly the question where
	// the difference decides the answer.
	days := run("2026-06-15", 100, day{tmax: 33, tmin: 22, rain: 1.2})
	for i := 0; i < 100; i += 4 {
		days[i].rain = 9
	}
	c := archive(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, canned(days...))
	})
	s, err := c.Range(context.Background(), 18.2, 75.8, past(120), past(20))
	if err != nil {
		t.Fatal(err)
	}

	r, err := s.Assemble(days[0].date, days[len(days)-1].date, Span{})
	if err != nil {
		t.Fatal(err)
	}

	var said bool
	for _, n := range r.Notes {
		if strings.Contains(n, "only a trace") && strings.Contains(n, "upper bound") {
			said = true
		}
	}
	if !said {
		t.Errorf("a season assembled from traces is reported as rainfall "+
			"without qualification: %v", r.Notes)
	}
}
