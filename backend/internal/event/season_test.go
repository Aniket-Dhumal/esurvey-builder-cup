package event

import (
	"testing"
	"time"
)

// Where an incident falls in the agricultural calendar.
//
// A crop loss report must state a season and a crop year -- validate refuses
// one without a crop year -- and until this existed nothing told a client
// what they were. The farmer app sent an empty string and every report it
// filed came back 400, which the farmer met as a report that never arrived.
//
// The crop year matters beyond validation: it selects the threshold yield the
// whole assessment is expressed against. Filing a February loss under the
// wrong year would measure it against the wrong denominator.

func on(s string) time.Time {
	d, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}
	return d
}

func TestTheSeasonAnIncidentFallsIn(t *testing.T) {
	for _, c := range []struct{ date, season string }{
		// Kharif: sown with the monsoon, harvested before winter.
		{"2026-06-01", "kharif"},
		{"2026-08-20", "kharif"}, // the hailstorm this was found on
		{"2026-10-31", "kharif"},

		// Rabi: sown after the monsoon, harvested in spring. It straddles the
		// new year, which is where an off-by-one lands.
		{"2026-11-01", "rabi"},
		{"2026-12-31", "rabi"},
		{"2027-01-01", "rabi"},
		{"2027-03-31", "rabi"},

		// Summer: the short irrigated crop between them.
		{"2027-04-01", "summer"},
		{"2027-05-31", "summer"},
	} {
		got, _ := SeasonAndCropYear(on(c.date))
		if got != c.season {
			t.Errorf("%s is %s, not %s", c.date, c.season, got)
		}
	}
}

func TestTheCropYearRunsJulyToJune(t *testing.T) {
	// The half a rabi season that falls after New Year is the case worth
	// pinning: a loss in February 2027 belongs to 2026-27, and calling it
	// 2027-28 would measure the claim against a threshold yield for a crop
	// that had not been sown yet.
	for _, c := range []struct{ date, year string }{
		{"2026-06-01", "2026-27"},
		{"2026-08-20", "2026-27"},
		{"2026-12-31", "2026-27"},
		{"2027-01-01", "2026-27"},
		{"2027-05-31", "2026-27"},
		{"2027-06-01", "2027-28"},
		{"2029-12-01", "2029-30"},
		{"2030-01-01", "2029-30"},
	} {
		_, got := SeasonAndCropYear(on(c.date))
		if got != c.year {
			t.Errorf("%s falls in crop year %s, not %s", c.date, c.year, got)
		}
	}
}

func TestTheCropYearIsTheShapeTheDatabaseAccepts(t *testing.T) {
	// crop_loss_reports checks crop_year against ^[0-9]{4}(-[0-9]{2})?$. A
	// value this function produced that the column refused would turn a 400
	// into a 500, which is a worse failure with the same outcome for the
	// farmer.
	for _, d := range []string{
		"2026-01-15", "2026-06-30", "2026-07-01", "2029-12-31", "2031-04-09",
	} {
		_, y := SeasonAndCropYear(on(d))
		if len(y) != 7 || y[4] != '-' {
			t.Errorf("%s produced %q, which is not YYYY-YY", d, y)
		}
		for i, r := range y {
			if i == 4 {
				continue
			}
			if r < '0' || r > '9' {
				t.Errorf("%s produced %q, which is not all digits", d, y)
				break
			}
		}
	}
}

func TestTheDecadeRolloverKeepsTwoDigits(t *testing.T) {
	// 2099-00, not 2099-100. Nobody will run this then; the arithmetic is
	// still either right or wrong.
	_, y := SeasonAndCropYear(on("2099-08-01"))
	if y != "2099-00" {
		t.Errorf("the century rollover produced %q", y)
	}
}
