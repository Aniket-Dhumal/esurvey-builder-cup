package dossier

import (
	"strings"
	"testing"

	"github.com/agribid/esurvey-platform/backend/internal/pointndvi"
)

// The section that prints the optical reading at each filmed point.
//
// Two things are tested here and they are both about honesty rather than
// layout: that the section never appears when the analysis did not run, and
// that the two language halves report the same numbers about the same things.

func scv(v float64) *float64 { return &v }

func TestNoPerPointAnalysisPrintsNoSection(t *testing.T) {
	// Not an empty heading. A reader counting sections would take a bare §6b
	// for an analysis that failed rather than one that never ran.
	if PointOptical(nil).Any() {
		t.Error("a section was printed for an analysis that did not run")
	}
}

func TestTheParcelFigureIsRepeatedNotReplaced(t *testing.T) {
	// The whole reason this is its own section: a reader must be able to see
	// that the field figure in §6 was not quietly swapped for a figure derived
	// from four small patches.
	s := PointOptical(&pointndvi.Comparison{
		RadiusM:          20,
		FieldRRVLPercent: 34,
		Readings: []pointndvi.Reading{{
			StationNo: 1, AreaM2: 1250, Observations: 4,
			RRVLPercent: 36, AgainstFieldPoints: 2,
		}},
	})
	if !strings.Contains(s.Rows[0].Reference.EN, "parcel 34%") {
		t.Errorf("the row does not carry the parcel's own figure to compare "+
			"against: %q", s.Rows[0].Reference.EN)
	}
	if s.Rows[0].Role != Corroborates {
		t.Error("a per-point reading must not be marked as determining the " +
			"loss figure; the parcel series still does that")
	}
}

func TestTheDisagreementBetweenFootageAndSatelliteIsNamedWithItsPoint(t *testing.T) {
	// An allegation about one clip. An officer overruling it has to know which.
	s := PointOptical(&pointndvi.Comparison{
		RadiusM:          20,
		FieldRRVLPercent: 8,
		Readings: []pointndvi.Reading{{
			StationNo: 3, AreaM2: 1100, Observations: 5,
			RRVLPercent: 9, AgainstFieldPoints: 1, FootageSCV: scv(84),
		}},
		Contradictions: []pointndvi.Contradiction{{
			StationNo: 3, FootageSCV: 84, PointRRVL: 9,
			AreaM2: 1100, Observations: 5,
		}},
	})

	var found string
	for _, n := range s.Notes {
		if strings.Contains(n.EN, "Point 3:") {
			found = n.EN
		}
	}
	if found == "" {
		t.Fatal("the disagreement was not reported against its point")
	}
	// It has to close off the excuse, which is the only reason reading at the
	// point rather than over the field is worth doing.
	if !strings.Contains(found, "averages the whole field") {
		t.Errorf("the note does not say why the usual explanation fails "+
			"here: %q", found)
	}
	for _, n := range s.Notes {
		if n.MR == n.EN {
			t.Errorf("a note is not translated: %q", n.EN)
		}
	}
}

func TestAWeakReadingSaysSoInBothLanguagesWithTheSameNumbers(t *testing.T) {
	// Tf shares one argument list across the two halves, so a Marathi format
	// string whose placeholders are in a different order than the English one
	// silently prints the numbers in the wrong slots -- an error invisible to
	// anyone reviewing in one language.
	s := PointOptical(&pointndvi.Comparison{
		RadiusM:          20,
		FieldRRVLPercent: 10,
		Readings: []pointndvi.Reading{{
			StationNo: 4, AreaM2: 320, Observations: 4,
			RRVLPercent: 70, AgainstFieldPoints: 60,
			Weak: true, WeakBecause: pointndvi.SmallPatch,
		}},
	})
	eff := s.Rows[0].Effect

	// The area belongs to the area unit and the radius to the radius unit, in
	// both halves.
	if !strings.Contains(eff.EN, "320 m²") || !strings.Contains(eff.EN, "20 m of") {
		t.Errorf("the English half puts the numbers in the wrong places: %q", eff.EN)
	}
	if !strings.Contains(eff.MR, "320 चौ.मी.") {
		t.Errorf("the Marathi half does not report 320 as the area: %q", eff.MR)
	}
	if !strings.Contains(eff.MR, "20 मीटर") {
		t.Errorf("the Marathi half does not report 20 as the radius: %q", eff.MR)
	}

	// And nothing may rest on it.
	if len(s.Rows[0].Caveats) == 0 {
		t.Error("a weak reading carries no caveat saying nothing rests on it")
	}
}

func TestACloudedPatchIsReportedRatherThanPassedOver(t *testing.T) {
	// Silence would let the absence read as corroboration of whatever the
	// footage claimed at that point.
	s := PointOptical(&pointndvi.Comparison{
		RadiusM: 20, FieldRRVLPercent: 40,
		CloudedPoints: []int{2},
	})
	var said bool
	for _, n := range s.Notes {
		if strings.Contains(n.EN, "Point 2:") &&
			strings.Contains(n.EN, "no cloud-free pass") {
			said = true
		}
	}
	if !said {
		t.Errorf("the clouded point was not reported: %v", s.Notes)
	}
}

func TestTheSectionAlwaysStatesWhatItCannotSupport(t *testing.T) {
	// A dozen mixed pixels. Every finding above is only as good as that, and a
	// reader must not have to know it already.
	s := PointOptical(&pointndvi.Comparison{
		RadiusM: 20, FieldRRVLPercent: 40,
		Readings: []pointndvi.Reading{{
			StationNo: 1, AreaM2: 1250, Observations: 4, RRVLPercent: 41,
		}},
	})
	var limited bool
	for _, n := range s.Notes {
		if strings.Contains(n.EN, "Limitation") {
			limited = true
		}
	}
	if !limited {
		t.Error("the section does not say what it cannot support")
	}
}
