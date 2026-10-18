package weather

import (
	"fmt"
	"math"
	"sort"
	"time"
)

// The windows a fortnight cannot see.
//
// A drought is not visible in fourteen days. Nor is a monsoon that arrived
// three weeks late, making the sowing date on the form impossible. Nor is the
// week above 38 °C during flowering that cost the yield before the hailstorm
// arrived and took the blame for it. All three are ordinary in Maharashtra and
// all three were invisible.
//
// Everything here is arithmetic over a series that was already fetched, so it
// costs nothing beyond the wider range, and every figure is reproducible from
// the record.

// Window is what the weather did between two dates.
type Window struct {
	From string `json:"from"`
	To   string `json:"to"`
	Days int    `json:"days"`

	RainMM   float64 `json:"rain_mm"`
	RainDays int     `json:"rain_days"`
	// TraceDays is days holding less than a rain day but more than nothing.
	// In a reanalysis these are mostly an artefact of averaging convection
	// over a grid cell, and a total built from them overstates what reached
	// the crop. Reported so the officer can see that for themselves.
	TraceDays   int     `json:"trace_rain_days"`
	HeavyDays   int     `json:"heavy_rain_days"`
	MaxDailyMM  float64 `json:"max_daily_rain_mm"`
	MaxDailyOn  string  `json:"max_daily_rain_on,omitempty"`
	MaxGustKmh  float64 `json:"max_gust_kmh"`
	MaxGustOn   string  `json:"max_gust_on,omitempty"`
	HailDays    int     `json:"hail_coded_days"`
	ThunderDays int     `json:"thunderstorm_days"`

	HottestC float64 `json:"hottest_c"`
	ColdestC float64 `json:"coldest_c"`

	// LongestDrySpell is the run that matters for a drought claim, and the
	// single most useful number a fourteen-day window could never produce.
	LongestDrySpell     int    `json:"longest_dry_spell_days"`
	LongestDrySpellFrom string `json:"longest_dry_spell_from,omitempty"`

	Missing int `json:"missing_days"`
}

// Thresholds, named so they can be argued with.
const (
	// rainDayMM is what counts as a day it rained. A tenth of a millimetre is
	// dew on the instrument, not rain on the crop.
	rainDayMM = 2.5
	// heavyRainMM is the India Meteorological Department's "heavy rainfall"
	// threshold for a day.
	heavyRainMM = 64.5
	// dryDayMM is what counts as a dry day for a spell. It is the same bar as
	// rainDayMM, and deliberately so: a day that was not a rain day was a dry
	// day, and the two statements should not be able to disagree.
	//
	// It was 1.0 mm, on the reasoning that a millimetre should not be counted
	// as dry. That reasoning is wrong for a reanalysis. ERA5 resolves
	// convection by spreading it across an eleven-kilometre cell, so a
	// downpour on one village becomes a thin film over all of them and a day
	// with no rain at all acquires a millimetre or two. Barshi, kharif 2026:
	// 49 days at or above 2.5 mm and a further 52 days holding nothing but a
	// trace. At a 1.0 mm bar those traces break every spell they touch.
	//
	// The district declared drought over 1-20 September. Across that window
	// the grid holds 37.4 mm on three rain days -- a drought, plainly. At
	// 1.0 mm the longest dry spell computed as four days; at 2.5 mm it is
	// fifteen, from 5 September. The declaration and the record agree. Only
	// the threshold disagreed with both.
	dryDayMM = rainDayMM
	// traceMM is the floor of the band between nothing and a rain day. Days in
	// it are counted and reported, because a season total assembled largely
	// from traces reads as a good season and was not one.
	traceMM = 0.1
)

// Between summarises a window of the series.
func (s *Series) Between(from, to string) (Window, error) {
	start, err := time.Parse(dayLayout, from)
	if err != nil {
		return Window{}, fmt.Errorf("from %q: %w", from, err)
	}
	end, err := time.Parse(dayLayout, to)
	if err != nil {
		return Window{}, fmt.Errorf("to %q: %w", to, err)
	}
	if end.Before(start) {
		return Window{}, fmt.Errorf("%s is before %s", to, from)
	}

	w := Window{
		From:     from,
		To:       to,
		HottestC: math.Inf(-1),
		ColdestC: math.Inf(1),
	}
	span := int(end.Sub(start).Hours()/24) + 1

	in := make([]Daily, 0, span)
	for _, d := range s.Days {
		t, err := time.Parse(dayLayout, d.Date)
		if err != nil || t.Before(start) || t.After(end) {
			continue
		}
		in = append(in, d)
	}
	sort.Slice(in, func(i, j int) bool { return in[i].Date < in[j].Date })

	w.Days = len(in)
	w.Missing = span - len(in)
	if len(in) == 0 {
		w.HottestC, w.ColdestC = 0, 0
		return w, nil
	}

	var dry, bestDry int
	var dryStart, bestDryStart string

	for _, d := range in {
		w.RainMM += d.RainMM
		if d.RainMM >= rainDayMM {
			w.RainDays++
		} else if d.RainMM >= traceMM {
			w.TraceDays++
		}
		if d.RainMM >= heavyRainMM {
			w.HeavyDays++
		}
		if d.RainMM > w.MaxDailyMM {
			w.MaxDailyMM, w.MaxDailyOn = d.RainMM, d.Date
		}
		if d.GustMaxKmh > w.MaxGustKmh {
			w.MaxGustKmh, w.MaxGustOn = d.GustMaxKmh, d.Date
		}
		if d.Hail() {
			w.HailDays++
		}
		if d.Thunder() {
			w.ThunderDays++
		}
		w.HottestC = math.Max(w.HottestC, d.TMaxC)
		w.ColdestC = math.Min(w.ColdestC, d.TMinC)

		if d.RainMM < dryDayMM {
			if dry == 0 {
				dryStart = d.Date
			}
			dry++
			if dry > bestDry {
				bestDry, bestDryStart = dry, dryStart
			}
			continue
		}
		dry = 0
	}

	w.LongestDrySpell, w.LongestDrySpellFrom = bestDry, bestDryStart
	w.RainMM = round1(w.RainMM)
	w.MaxDailyMM = round1(w.MaxDailyMM)
	w.MaxGustKmh = round1(w.MaxGustKmh)
	w.HottestC = round1(w.HottestC)
	w.ColdestC = round1(w.ColdestC)

	return w, nil
}

// Record is every window worth asking about, for one claim.
type Record struct {
	Point  Point  `json:"point"`
	Source string `json:"source"`

	// Season is sowing to incident. The window a drought lives in.
	Season Window `json:"season"`
	// Sowing is the fortnight around the declared sowing date. A crop sown
	// into ground that had no rain for three weeks was not sown then.
	Sowing Window `json:"sowing"`
	// Incident is the window the loss happened in: the one a district declared
	// where this assessment belongs to a survey event, and otherwise the
	// fortnight before the date the farmer gave.
	//
	// The declared window has to win. A district that declares drought over
	// 1-20 September is naming the period it wants examined; a farmer filing
	// under that declaration writes the date they noticed, which for a slow
	// loss is early in it or before it. Barshi, 2026: the reported date was
	// 1 September, so a fortnight back landed on 18 August to 1 September --
	// the two weeks immediately *preceding* the declared drought, holding the
	// last of the rain. The specialist was shown the wettest fortnight
	// available and asked whether drought was consistent with it.
	Incident Window `json:"incident"`
	// IncidentSource says which of the two it is, because the reader cannot
	// tell from the dates and the conclusion depends on it.
	IncidentSource string `json:"incident_window_source"`

	Notes []string `json:"notes,omitempty"`
}

// Point is where the record was read, and how coarsely.
type Point struct {
	Lat          float64 `json:"lat"`
	Lon          float64 `json:"lon"`
	ResolutionKm float64 `json:"resolution_km"`
}

// incidentWindowDays is the fortnight before the loss, used only when no
// district declared a window.
const incidentWindowDays = 14

// What the incident window was taken from.
const (
	IncidentDeclared  = "survey_event_declared_window"
	IncidentFortnight = "fortnight_before_reported_date"
)

// sowingWindowDays is how far either side of the declared sowing date to look
// for the rain that made sowing possible.
const sowingWindowDays = 10

// Span is a window someone else decided: the incident period of a survey
// event, as a district declared it.
type Span struct{ From, To string }

// Valid reports whether both ends parse and run in order.
func (sp Span) Valid() bool {
	if sp.From == "" || sp.To == "" {
		return false
	}
	f, err := time.Parse(dayLayout, sp.From)
	if err != nil {
		return false
	}
	t, err := time.Parse(dayLayout, sp.To)
	return err == nil && !t.Before(f)
}

// Assemble builds every window for one claim from a single fetch.
//
// declared is the incident window of the survey event this claim was filed
// under, where there is one. Pass the zero Span otherwise and the incident
// window falls back to the fortnight before the reported date.
func (s *Series) Assemble(sowing, incident string, declared Span) (*Record, error) {
	sow, err := time.Parse(dayLayout, sowing)
	if err != nil {
		return nil, fmt.Errorf("sowing date %q: %w", sowing, err)
	}
	inc, err := time.Parse(dayLayout, incident)
	if err != nil {
		return nil, fmt.Errorf("incident date %q: %w", incident, err)
	}

	r := &Record{
		Point:  Point{Lat: s.Lat, Lon: s.Lon, ResolutionKm: s.ResolutionKm},
		Source: s.Source,
	}

	// The season runs to whichever comes later. A farmer who reports on the
	// first day of a declared drought must not have the rest of it fall
	// outside the season the drought is judged in.
	seasonEnd := incident
	if declared.Valid() && declared.To > seasonEnd {
		seasonEnd = declared.To
	}
	if r.Season, err = s.Between(sowing, seasonEnd); err != nil {
		return nil, err
	}
	if r.Sowing, err = s.Between(
		sow.AddDate(0, 0, -sowingWindowDays).Format(dayLayout),
		sow.AddDate(0, 0, sowingWindowDays).Format(dayLayout),
	); err != nil {
		return nil, err
	}

	from, to := inc.AddDate(0, 0, -incidentWindowDays).Format(dayLayout), incident
	r.IncidentSource = IncidentFortnight
	if declared.Valid() {
		from, to = declared.From, declared.To
		r.IncidentSource = IncidentDeclared
	}
	if r.Incident, err = s.Between(from, to); err != nil {
		return nil, err
	}

	r.Notes = notesFor(r)
	return r, nil
}

// dryAtSowingDays is the run with no useful rain around a declared sowing
// date that makes the date worth questioning. Three weeks: a dryland farmer
// waits for rain to sow, and a fortnight of waiting is ordinary.
const dryAtSowingDays = 21

// longDrySpellDays is the run within a season that is worth reporting whether
// or not drought was claimed. A fortnight without useful rain is the bar
// ordinarily used for moisture stress in kharif, and was 21 here -- which at
// the old dry-day threshold meant a run had to be three weeks of a drizzling
// reanalysis before anything was said about it. Barshi's fifteen days from
// 5 September, inside a declared drought, cleared neither.
const longDrySpellDays = 14

// tracePastRainDays is when the trace band is worth naming: more days holding
// nothing but a trace than days that actually rained. The season total is then
// mostly an artefact of averaging convection over a grid cell.
const tracePastRainDays = 1.0

func notesFor(r *Record) []string {
	var out []string

	if r.Season.Missing > 0 {
		out = append(out, fmt.Sprintf(
			"%d day(s) of the season have no record, so the seasonal totals below "+
				"are lower bounds", r.Season.Missing))
	}

	if r.Sowing.Days > 0 && r.Sowing.LongestDrySpell >= dryAtSowingDays {
		out = append(out, fmt.Sprintf(
			"there was no useful rain for %d days around the declared sowing date, "+
				"which is long for a dryland sowing. The date may be approximate, "+
				"or the crop may have been sown under irrigation.",
			r.Sowing.LongestDrySpell))
	}

	if r.Season.LongestDrySpell >= longDrySpellDays {
		out = append(out, fmt.Sprintf(
			"the longest dry spell of the season ran %d days from %s, with no day "+
				"reaching %.1f mm",
			r.Season.LongestDrySpell, r.Season.LongestDrySpellFrom, rainDayMM))
	}

	// The instrument, stated before its totals are relied on.
	//
	// 468 mm reads as an adequate season. 468 mm of which half the wet days
	// held under 2.5 mm does not, and the difference is the whole question
	// when drought is what is being assessed.
	if r.Season.RainDays > 0 &&
		float64(r.Season.TraceDays) >= tracePastRainDays*float64(r.Season.RainDays) {
		out = append(out, fmt.Sprintf(
			"of the season's %d rain days a further %d days held only a trace "+
				"(under %.1f mm). A reanalysis spreads a downpour across an "+
				"eleven-kilometre cell, so the seasonal total is an upper bound on "+
				"what reached this field",
			r.Season.RainDays, r.Season.TraceDays, rainDayMM))
	}

	if r.IncidentSource == IncidentDeclared {
		out = append(out, fmt.Sprintf(
			"the window below is the one the survey event declared (%s to %s), not "+
				"a fortnight before the date the farmer gave",
			r.Incident.From, r.Incident.To))
		if r.Incident.LongestDrySpell >= longDrySpellDays {
			out = append(out, fmt.Sprintf(
				"within the declared window the longest dry spell ran %d days from "+
					"%s: %.1f mm fell across %d days, on %d of which it rained",
				r.Incident.LongestDrySpell, r.Incident.LongestDrySpellFrom,
				r.Incident.RainMM, r.Incident.Days, r.Incident.RainDays))
		}
	}

	if r.Incident.HailDays > 0 {
		out = append(out, fmt.Sprintf(
			"hail was coded on %d day(s) of the incident window. The code means "+
				"hail was recorded somewhere in an eleven-kilometre cell, not that "+
				"it fell on this field.", r.Incident.HailDays))
	}

	if r.Incident.MaxGustKmh >= 60 {
		out = append(out, fmt.Sprintf(
			"the strongest gust of the incident window was %.0f km/h on %s",
			r.Incident.MaxGustKmh, r.Incident.MaxGustOn))
	}

	if r.Incident.HeavyDays > 0 {
		out = append(out, fmt.Sprintf(
			"%d day(s) of the incident window exceeded the heavy-rainfall "+
				"threshold, the wettest being %.0f mm on %s",
			r.Incident.HeavyDays, r.Incident.MaxDailyMM, r.Incident.MaxDailyOn))
	}

	return out
}

func round1(v float64) float64 {
	if math.IsInf(v, 0) || math.IsNaN(v) {
		return 0
	}
	return math.Round(v*10) / 10
}
