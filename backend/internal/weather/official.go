package weather

import (
	"fmt"
	"time"
)

// Rainfall as the state records it, against the normal for that place.
//
// # Why this exists beside the reanalysis
//
// Everything else in this package comes from ERA5, which is a model's
// reconstruction of the atmosphere, not a set of readings. Over peninsular India
// it is wrong about how much rain fell, and by a lot: for Barshi taluka in kharif
// 2026 it reported 505 mm where the state's own gauges recorded 286 mm.
//
// Worse, it carries no normal. A drought determination in India is a departure
// from normal -- the Manual for Drought Management asks for rainfall deficiency,
// dry spells at critical stages and sown area -- and a platform holding only
// absolute millimetres cannot express the finding at all. "505 mm" reads as an
// adequate season. 286 mm against a normal of 516.6 mm is 55%, and the month the
// crop was filling grain was 16%.
//
// So the division of labour is:
//
//	Official   how much rain fell, and how that compares to normal
//	Series     which days it fell on -- dry spells, heat, gusts
//
// The reanalysis remains useful for the second. It is a daily field and the
// state publishes no daily figures for the current year. What it must not do any
// more is supply the amount.
//
// # It is quoted, not recomputed
//
// Every figure here is stored as the state published it, including its rounding.
// A percentage this platform derived that differs in the first decimal from the
// one on the state's own page is an argument an officer should never have to
// have at an appeal.
type Official struct {
	// Source names the publisher, and Unit names the administrative unit the
	// figures are for. Neither is decoration: the unit is coarser than a parcel
	// and a reader has to know what they are being shown.
	Source    string `json:"source"`
	UnitLevel string `json:"unit_level"`
	UnitName  string `json:"unit_name"`
	District  string `json:"district_name"`

	CropYear int `json:"crop_year"`

	// Months are the months of the season published so far, in order.
	Months []OfficialPeriod `json:"months"`
	// Season is the cumulative from the start of the season to the date the
	// state last published for. The figure a drought determination rests on.
	Season *OfficialPeriod `json:"season_to_date,omitempty"`

	// PublishedAt is when the state said these figures were current as of.
	PublishedAt *time.Time `json:"published_at,omitempty"`
}

// OfficialPeriod is one period's actual against its normal.
type OfficialPeriod struct {
	// Month is 1-12, or 0 for the season-to-date cumulative.
	Month int    `json:"month,omitempty"`
	Label string `json:"label"`
	From  string `json:"from"`
	To    string `json:"to"`

	NormalMM        float64 `json:"normal_mm"`
	ActualMM        float64 `json:"actual_mm"`
	PercentOfNormal float64 `json:"percent_of_normal"`
}

// DeficitPoints is how far below normal, in percentage points. Negative where
// more rain fell than normally does.
func (p OfficialPeriod) DeficitPoints() float64 { return 100 - p.PercentOfNormal }

// Thresholds from the Manual for Drought Management, named so they can be
// argued with rather than buried in a comparison.
const (
	// SevereDeficitPercent is the rainfall deficiency at which drought is
	// ordinarily indicated: rain at or below half the normal. Maharashtra's own
	// primary indicator is "50% below normal", which is this figure.
	SevereDeficitPercent = 50.0
	// FailedMonthPercent is a month so far below normal that the crop in it had
	// no water to speak of, whatever the season total says.
	FailedMonthPercent = 25.0
)

// SeasonDeficient reports whether the season to date is at or under half its
// normal -- the state's own primary drought indicator.
func (o *Official) SeasonDeficient() bool {
	return o != nil && o.Season != nil &&
		o.Season.PercentOfNormal <= SevereDeficitPercent
}

// FailedMonths are the months that received a quarter of their normal or less.
//
// Reported separately from the season total because they are the finding a
// seasonal figure hides. Barshi's season came to 55% of normal, which on its own
// reads as a poor year rather than a disaster; inside it, August was 16% and
// September 31%, consecutively, through grain fill. A crop does not experience a
// seasonal average.
func (o *Official) FailedMonths() []OfficialPeriod {
	if o == nil {
		return nil
	}
	var out []OfficialPeriod
	for _, m := range o.Months {
		if m.PercentOfNormal <= FailedMonthPercent {
			out = append(out, m)
		}
	}
	return out
}

// Driest is the worst month of the season published so far.
func (o *Official) Driest() *OfficialPeriod {
	if o == nil || len(o.Months) == 0 {
		return nil
	}
	worst := o.Months[0]
	for _, m := range o.Months[1:] {
		if m.PercentOfNormal < worst.PercentOfNormal {
			worst = m
		}
	}
	return &worst
}

// Summary is one line an officer can read, and the line a specialist is given.
//
// Written here rather than at each call site so the report, the model prompt and
// the API all quote the same sentence. Three of them disagreeing about what the
// state said would be its own defect.
func (o *Official) Summary() string {
	if o == nil || o.Season == nil {
		return ""
	}
	s := fmt.Sprintf(
		"%s %s: %.1f mm against a normal of %.1f mm for %s, which is %.1f%% of "+
			"normal -- a deficit of %.1f percentage points",
		o.UnitName, unitWord(o.UnitLevel),
		o.Season.ActualMM, o.Season.NormalMM, o.Season.Label,
		o.Season.PercentOfNormal, o.Season.DeficitPoints())

	if failed := o.FailedMonths(); len(failed) > 0 {
		s += ". Within it"
		for i, m := range failed {
			switch {
			case i == 0:
				s += " "
			case i == len(failed)-1:
				s += " and "
			default:
				s += ", "
			}
			s += fmt.Sprintf("%s received %.1f mm of a normal %.1f mm (%.1f%%)",
				m.Label, m.ActualMM, m.NormalMM, m.PercentOfNormal)
		}
	}
	return s + "."
}

// StalenessDays is how many days old the published figure is, and whether that
// could be told.
func (o *Official) StalenessDays(now time.Time) (int, bool) {
	if o == nil || o.PublishedAt == nil {
		return 0, false
	}
	d := int(now.Sub(*o.PublishedAt).Hours() / 24)
	if d < 0 {
		d = 0
	}
	return d, true
}

func unitWord(level string) string {
	switch level {
	case "sub_district":
		return "taluka"
	case "circle":
		return "revenue circle"
	default:
		return level
	}
}
