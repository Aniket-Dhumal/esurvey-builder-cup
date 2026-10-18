package weather

import (
	"strings"
	"testing"
	"time"
)

// barshiOfficial is what Maharashtra published for Barshi taluka on the day of
// the trial. The whole reason this type exists is that ERA5 reported 505 mm for
// the same season with no normal at all.
func barshiOfficial() *Official {
	at := time.Date(2026, 9, 28, 21, 44, 0, 0, time.UTC)
	return &Official{
		Source:    "maharain_mh_gov_in",
		UnitLevel: "sub_district",
		UnitName:  "Barshi",
		District:  "Solapur",
		CropYear:  2026,
		Months: []OfficialPeriod{
			{Month: 6, Label: "June 2026", NormalMM: 107.3, ActualMM: 81.6, PercentOfNormal: 76.0},
			{Month: 7, Label: "July 2026", NormalMM: 123.7, ActualMM: 134.6, PercentOfNormal: 108.8},
			{Month: 8, Label: "August 2026", NormalMM: 124.5, ActualMM: 19.4, PercentOfNormal: 15.6},
			{Month: 9, Label: "September 2026", NormalMM: 161.1, ActualMM: 50.4, PercentOfNormal: 31.3},
		},
		Season: &OfficialPeriod{
			Label:    "1 June to 28 September 2026",
			NormalMM: 516.6, ActualMM: 286.0, PercentOfNormal: 55.4,
		},
		PublishedAt: &at,
	}
}

func TestTheDeficitIsMeasuredInPercentagePoints(t *testing.T) {
	o := barshiOfficial()
	if got := o.Season.DeficitPoints(); got < 44.5 || got > 44.7 {
		t.Errorf("season deficit = %.1f, want 44.6 points", got)
	}
	// July had more than its normal, which must read as a negative deficit
	// rather than as a surplus counted the wrong way.
	if got := o.Months[1].DeficitPoints(); got > 0 {
		t.Errorf("July deficit = %.1f; a wet month is not a deficit", got)
	}
}

func TestTheStatesOwnDroughtIndicatorIsApplied(t *testing.T) {
	// Maharashtra's primary indicator is rainfall 50% below normal. Barshi's
	// season is 55.4% OF normal, so it is a 44.6-point shortfall and does NOT
	// meet that bar on the seasonal figure alone -- which is exactly why the
	// monthly rows matter and why this is not fudged to look decisive.
	o := barshiOfficial()
	if o.SeasonDeficient() {
		t.Error("55.4% of normal is not at or below half of normal")
	}

	o.Season.PercentOfNormal = 48.9 // the circle figure for the same place
	if !o.SeasonDeficient() {
		t.Error("48.9% of normal is below half and should be flagged")
	}
}

func TestTheFailedMonthsAreTheFindingTheSeasonHides(t *testing.T) {
	// A season at 55% reads as a poor year. Inside it, August at 16% and
	// September at 31% through grain fill is a crop failure. A crop does not
	// experience a seasonal average.
	o := barshiOfficial()
	failed := o.FailedMonths()
	if len(failed) != 1 || failed[0].Month != 8 {
		t.Fatalf("failed months = %+v, want August alone under a quarter", failed)
	}
	if d := o.Driest(); d == nil || d.Month != 8 {
		t.Errorf("driest month = %+v, want August", d)
	}
}

func TestTheSummaryQuotesTheUnitAndBothFigures(t *testing.T) {
	// One sentence, used by the report, the model prompt and the API alike. If
	// they each wrote their own, three parts of the system would disagree about
	// what the state said.
	s := barshiOfficial().Summary()

	for _, want := range []string{
		"Barshi taluka", "286.0 mm", "516.6 mm", "55.4%", "44.6 percentage points",
		"August 2026", "19.4 mm",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("the summary omits %q: %s", want, s)
		}
	}
}

func TestAnAbsentRecordSaysNothingRatherThanZero(t *testing.T) {
	// Every accessor is called on a nil record in the ordinary case of a
	// district nobody has loaded. Returning zeroes would put "0.0 mm against a
	// normal of 0.0" on a report.
	var o *Official
	if o.Summary() != "" {
		t.Error("a nil record produced a summary")
	}
	if o.SeasonDeficient() {
		t.Error("a nil record reported a deficit")
	}
	if o.FailedMonths() != nil || o.Driest() != nil {
		t.Error("a nil record produced months")
	}
	if _, ok := o.StalenessDays(time.Now()); ok {
		t.Error("a nil record reported an age")
	}
}

func TestAMonthsOnlyRecordDoesNotClaimASeason(t *testing.T) {
	o := barshiOfficial()
	o.Season = nil
	if o.Summary() != "" {
		t.Error("a record with no season total produced a season summary")
	}
	if o.SeasonDeficient() {
		t.Error("a record with no season total reported a season deficit")
	}
}

func TestStalenessIsReportedInDays(t *testing.T) {
	// A rainfall figure's age is part of the evidence: a deficit published a
	// fortnight ago may have been closed since.
	o := barshiOfficial()
	d, ok := o.StalenessDays(o.PublishedAt.AddDate(0, 0, 3))
	if !ok || d != 3 {
		t.Errorf("staleness = %d, %v; want 3 days", d, ok)
	}
	// A clock behind the publisher must not produce a negative age.
	if d, _ := o.StalenessDays(o.PublishedAt.AddDate(0, 0, -2)); d != 0 {
		t.Errorf("staleness = %d for a past clock, want 0", d)
	}
}
