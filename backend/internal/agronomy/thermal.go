package agronomy

import (
	"fmt"
	"math"
	"sort"
)

// Thermal time, and the stress the same temperatures caused.
//
// # Why a crop is not placed by counting days
//
// The panel reasons today from "114 days after sowing". A crop develops on
// accumulated heat, not on the calendar: the same soybean variety reaches
// flowering weeks apart between a cool Konkan season and a hot Marathwada
// one. Placing a claim in the wrong stage changes the answer to the only
// question that matters at assessment -- whether the plant could have made
// the loss up -- and it does so invisibly.
//
// # Why this is computed here rather than by the model
//
// The panel has a weather tool and could be asked to do this arithmetic. It
// should not be. A figure that enters an official record needs to be
// reproducible on demand during an appeal, and a number a language model
// arrived at by calling a tool is not reproducible in that sense. Everything
// in this file is a pure function over a temperature series: same input, same
// output, testable to the decimal, and printable next to the figure it
// produced.

// Day is one day of temperature, as every weather source reports it.
type Day struct {
	Date  string
	TMaxC float64
	TMinC float64
}

// DailyGDD is the growing degree days contributed by one day.
//
// The capped-mean method: the daily mean is taken between the crop's base and
// cutoff temperatures before subtracting the base. Both caps matter in
// Maharashtra. Without the upper cutoff a 44 °C Vidarbha May accumulates heat
// the plant did not actually use, and the crop is placed weeks ahead of where
// it stands in the field.
func DailyGDD(crop *Crop, d Day) float64 {
	if crop == nil {
		return 0
	}
	hi, lo := d.TMaxC, d.TMinC
	if hi < lo {
		hi, lo = lo, hi
	}

	cutoff := crop.CutoffTempC
	if cutoff <= 0 {
		cutoff = math.Inf(1)
	}
	hi = math.Min(hi, cutoff)
	lo = math.Min(lo, cutoff)

	// Nights below the base contribute nothing rather than negative heat: the
	// plant does not un-develop, and letting cold nights subtract would place
	// a rabi crop permanently behind where it stands.
	hi = math.Max(hi, crop.BaseTempC)
	lo = math.Max(lo, crop.BaseTempC)

	return math.Max(0, (hi+lo)/2-crop.BaseTempC)
}

// Thermal is the accumulated picture at a point in the season.
type Thermal struct {
	Crop     string `json:"crop"`
	Duration string `json:"duration_class"`

	// DurationAssumed says the variety was not declared and the middle of the
	// range was used. It travels with the figure so nobody reads an assumed
	// class as a stated one.
	DurationAssumed bool `json:"duration_assumed"`

	AccumulatedGDD float64 `json:"accumulated_gdd"`
	MaturityGDD    float64 `json:"maturity_gdd"`

	// PercentOfCycle is how far through its life the crop had got. This is
	// the figure an officer can sanity-check against what they saw.
	PercentOfCycle float64 `json:"percent_of_cycle"`

	Stage       string `json:"stage"`
	StageEn     string `json:"stage_en"`
	StageMr     string `json:"stage_mr"`
	StageClass  string `json:"stage_class"`
	Recoverable bool   `json:"recoverable"`

	// CalendarDays is what a day count would have said, kept so the
	// difference between the two is visible rather than silently corrected.
	CalendarDays  int    `json:"calendar_days"`
	CalendarStage string `json:"calendar_stage,omitempty"`

	DaysCovered int      `json:"days_covered"`
	DaysMissing int      `json:"days_missing"`
	Confidence  string   `json:"confidence"`
	Caveats     []string `json:"caveats,omitempty"`

	Provenance string `json:"provenance"`
}

// StageOn places a crop in its cycle on a given date.
//
// days need not be complete or sorted. Missing days are reported rather than
// interpolated: a series with a fortnight absent has accumulated less heat
// than the crop did, the stage will read early, and the caller needs to know
// that rather than receive a confident wrong stage.
func StageOn(crop *Crop, durationKey string, days []Day, sowing, on string) (Thermal, error) {
	if crop == nil {
		return Thermal{}, fmt.Errorf("no crop model")
	}
	start, err := parseDay(sowing)
	if err != nil {
		return Thermal{}, fmt.Errorf("sowing date %q: %w", sowing, err)
	}
	end, err := parseDay(on)
	if err != nil {
		return Thermal{}, fmt.Errorf("date %q: %w", on, err)
	}
	if end.Before(start) {
		return Thermal{}, fmt.Errorf("%s is before the sowing date %s", on, sowing)
	}

	span := int(end.Sub(start).Hours()/24) + 1

	inWindow := make([]Day, 0, len(days))
	seen := map[string]bool{}
	for _, d := range days {
		t, err := parseDay(d.Date)
		if err != nil || seen[d.Date] {
			continue
		}
		if t.Before(start) || t.After(end) {
			continue
		}
		seen[d.Date] = true
		inWindow = append(inWindow, d)
	}
	sort.Slice(inWindow, func(i, j int) bool { return inWindow[i].Date < inWindow[j].Date })

	var total float64
	for _, d := range inWindow {
		total += DailyGDD(crop, d)
	}

	dur, matched := crop.DurationFor(durationKey)
	maturity := dur.MaturityG
	if maturity <= 0 {
		maturity = crop.MaturityGDD()
	}

	t := Thermal{
		Crop:            crop.Key,
		Duration:        dur.Key,
		DurationAssumed: !matched,
		AccumulatedGDD:  math.Round(total),
		MaturityGDD:     maturity,
		CalendarDays:    int(end.Sub(start).Hours() / 24),
		DaysCovered:     len(inWindow),
		DaysMissing:     span - len(inWindow),
		Provenance:      crop.Provenance,
	}
	if maturity > 0 {
		t.PercentOfCycle = math.Round(total/maturity*1000) / 10
	}

	if s, ok := stageForVariety(crop, maturity, total); ok {
		t.Stage, t.StageEn, t.StageMr = s.Key, s.NameEn, s.NameMr
		t.StageClass, t.Recoverable = s.Class(), s.Recoverable
	}

	// What counting days would have said, for comparison.
	if dur.DaysMax > 0 && maturity > 0 {
		mid := float64(dur.DaysMin+dur.DaysMax) / 2
		if mid > 0 {
			asIfAverage := float64(t.CalendarDays) / mid * maturity
			if s, ok := stageForVariety(crop, maturity, asIfAverage); ok {
				t.CalendarStage = s.Key
			}
		}
	}

	switch {
	case t.DaysMissing > span/4:
		t.Confidence = ConfidenceLow
	case t.DaysMissing > 0:
		t.Confidence = ConfidenceModerate
	default:
		t.Confidence = ConfidenceHigh
	}

	if t.DaysMissing > 0 {
		t.Caveats = append(t.Caveats, fmt.Sprintf(
			"%d of %d days have no temperature record, so the accumulated heat is "+
				"an underestimate and the true stage is at or beyond the one reported",
			t.DaysMissing, span))
	}
	if t.DurationAssumed {
		t.Caveats = append(t.Caveats,
			"the variety's duration was not declared, so a medium-duration cycle "+
				"was assumed; a short or long variety would place this crop "+
				"differently")
	}
	if t.CalendarStage != "" && t.CalendarStage != t.Stage {
		t.Caveats = append(t.Caveats, fmt.Sprintf(
			"counting days would have placed this crop at %s rather than %s; the "+
				"season ran %s than average",
			t.CalendarStage, t.Stage,
			map[bool]string{true: "warmer", false: "cooler"}[t.PercentOfCycle > float64(t.CalendarDays)/float64(dur.DaysMax)*100]))
	}

	return t, nil
}

// Stress is what the temperatures themselves did, apart from development.
//
// Separate from the stage because it is a different mechanism: heat during
// flowering aborts set whether or not the calamity claimed happened, and a
// claim of hail damage on a crop that had already been through a week above
// its heat threshold at flowering is a different assessment.
type Stress struct {
	HeatThresholdC float64 `json:"heat_threshold_c,omitempty"`
	ColdThresholdC float64 `json:"cold_threshold_c,omitempty"`

	HeatDays int `json:"heat_days"`
	ColdDays int `json:"cold_days"`

	// HeatDaysAtFlowering is the count that actually costs yield. The rest is
	// context.
	HeatDaysAtFlowering int `json:"heat_days_at_flowering"`

	HottestC float64 `json:"hottest_c"`
	ColdestC float64 `json:"coldest_c"`

	DaysCovered int      `json:"days_covered"`
	Notes       []string `json:"notes,omitempty"`
}

// StressOver counts stress days between two dates, marking those that fell in
// a reproductive stage.
func StressOver(crop *Crop, durationKey string, days []Day, sowing, from, to string) (Stress, error) {
	if crop == nil {
		return Stress{}, fmt.Errorf("no crop model")
	}
	start, err := parseDay(from)
	if err != nil {
		return Stress{}, fmt.Errorf("from %q: %w", from, err)
	}
	end, err := parseDay(to)
	if err != nil {
		return Stress{}, fmt.Errorf("to %q: %w", to, err)
	}

	s := Stress{
		HeatThresholdC: crop.HeatStressC,
		ColdThresholdC: crop.ColdStressC,
		HottestC:       math.Inf(-1),
		ColdestC:       math.Inf(1),
	}

	sorted := append([]Day(nil), days...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Date < sorted[j].Date })

	// Which days the crop was flowering on, accumulated once.
	//
	// Asking StageOn per stress day would re-sum the whole season for each,
	// which is quadratic in the length of the record. A season is only a few
	// hundred days, but this runs per parcel per assessment and there is no
	// reason for it to.
	reproductive := reproductiveDays(crop, durationKey, sorted, sowing)

	for _, d := range sorted {
		t, err := parseDay(d.Date)
		if err != nil || t.Before(start) || t.After(end) {
			continue
		}
		s.DaysCovered++
		s.HottestC = math.Max(s.HottestC, d.TMaxC)
		s.ColdestC = math.Min(s.ColdestC, d.TMinC)

		if crop.HeatStressC > 0 && d.TMaxC >= crop.HeatStressC {
			s.HeatDays++
			if reproductive[d.Date] {
				s.HeatDaysAtFlowering++
			}
		}
		if crop.ColdStressC > 0 && d.TMinC <= crop.ColdStressC {
			s.ColdDays++
		}
	}

	if s.DaysCovered == 0 {
		return Stress{}, fmt.Errorf("no temperature records between %s and %s", from, to)
	}
	if math.IsInf(s.HottestC, -1) {
		s.HottestC = 0
	}
	if math.IsInf(s.ColdestC, 1) {
		s.ColdestC = 0
	}

	if crop.HeatStressC == 0 {
		s.Notes = append(s.Notes,
			"no heat threshold is established for this crop, so no claim is made "+
				"about heat stress")
	}
	if s.HeatDaysAtFlowering > 0 {
		s.Notes = append(s.Notes, fmt.Sprintf(
			"%d day(s) above %.0f °C fell while the crop was flowering, which "+
				"reduces set independently of the calamity claimed",
			s.HeatDaysAtFlowering, crop.HeatStressC))
	}

	return s, nil
}

// reproductiveDays marks the dates on which the crop was in a reproductive
// stage, from one pass over the series.
func reproductiveDays(crop *Crop, durationKey string, sorted []Day, sowing string) map[string]bool {
	out := map[string]bool{}
	start, err := parseDay(sowing)
	if err != nil {
		return out
	}

	dur, _ := crop.DurationFor(durationKey)
	maturity := dur.MaturityG
	if maturity <= 0 {
		maturity = crop.MaturityGDD()
	}

	var total float64
	for _, d := range sorted {
		t, err := parseDay(d.Date)
		if err != nil || t.Before(start) {
			continue
		}
		total += DailyGDD(crop, d)
		if s, ok := stageForVariety(crop, maturity, total); ok && s.Class() == ClassReproductive {
			out[d.Date] = true
		}
	}
	return out
}

// stageForVariety places accumulated heat inside one variety's cycle.
//
// The stage table is written against the crop's default cycle. A short
// duration variety reaches the same stage on less heat and a long one on
// more, so the accumulation is scaled into the table's frame before lookup.
// Both callers go through here: a season placed one way for the stage and
// another way for the stress days would be two answers to one question.
func stageForVariety(crop *Crop, maturity, accumulated float64) (Stage, bool) {
	scaled := accumulated
	if def := crop.MaturityGDD(); def > 0 && maturity > 0 {
		scaled = accumulated * def / maturity
	}
	return crop.StageAt(scaled)
}
