package dossier

import (
	"strings"
	"testing"

	"github.com/agribid/esurvey-platform/backend/internal/baseline"
	"github.com/agribid/esurvey-platform/backend/internal/fieldwork"
)

// The report decides somebody's compensation and is read in two languages by
// people with very different power to argue with it. What follows is mostly
// about the ways a document like that misleads without stating anything
// false: a half-translated sentence, a blank cell that reads as withheld, a
// corroborating figure laid out to look like a measurement.

func ptrF(v float64) *float64 { return &v }
func ptrI(v int) *int         { return &v }

// halfWritten is the bilingual invariant, stated once.
//
// Either a sentence exists in both languages or the field was never set.
// Exactly one side filled is the drift this package exists to prevent, and it
// is invisible to anybody reading only the English half -- which is everybody
// who reviews the code.
func halfWritten(t Text) bool { return (t.MR == "") != (t.EN == "") }

func checkRow(t *testing.T, where string, r Row) {
	t.Helper()
	for name, txt := range map[string]Text{
		"parameter": r.Parameter,
		"observed":  r.Observed,
		"source":    r.Source,
		"reference": r.Reference,
		"effect":    r.Effect,
		"evidence":  r.Evidence,
		"absentWhy": r.AbsentWhy,
	} {
		if halfWritten(txt) {
			t.Errorf("%s: %s is written in one language only: mr=%q en=%q",
				where, name, txt.MR, txt.EN)
		}
	}
	for i, w := range r.Working {
		if halfWritten(w) {
			t.Errorf("%s: working[%d] is one-sided: mr=%q en=%q", where, i, w.MR, w.EN)
		}
	}
	for i, c := range r.Caveats {
		if halfWritten(c) {
			t.Errorf("%s: caveat[%d] is one-sided: mr=%q en=%q", where, i, c.MR, c.EN)
		}
	}
}

func checkSection(t *testing.T, s Section) {
	t.Helper()
	if halfWritten(s.Title) {
		t.Errorf("§%s: title is one-sided", s.Number)
	}
	if halfWritten(s.Preamble) {
		t.Errorf("§%s: preamble is one-sided", s.Number)
	}
	for i, n := range s.Notes {
		if halfWritten(n) {
			t.Errorf("§%s: note[%d] is one-sided", s.Number, i)
		}
	}
	for i, r := range s.Rows {
		checkRow(t, "§"+s.Number+" row "+r.Parameter.EN, r)
		_ = i
	}
}

func TestATextIsNeverHalfWritten(t *testing.T) {
	// Every builder in the package, over the shapes most likely to leave a
	// sentence in one language: everything present, and nothing present.
	sections := []Section{
		FieldMeasurement(nil, Case{}),
		FieldMeasurement([]fieldwork.Measurement{fullMeasurement()},
			Case{Calamity: "hailstorm"}),
		// The same counts read against a claim lodging does not follow from.
		FieldMeasurement([]fieldwork.Measurement{fullMeasurement()},
			Case{Calamity: "drought"}),
		Baseline(nil),
		Baseline(&baseline.Denominators{}),
		Baseline(fullDenominators()),
	}
	for _, s := range sections {
		checkSection(t, s)
	}

	for _, r := range []Role{Determines, Corroborates, Context, Excluded} {
		if halfWritten(r.Label()) {
			t.Errorf("role %d has a label in one language only", r)
		}
	}
	for _, st := range []State{Provisional, Final} {
		if halfWritten(st.Label()) {
			t.Errorf("state %d has a stamp in one language only", st)
		}
	}
	for _, d := range []Denominator{
		DenominatorOf(nil),
		DenominatorOf(fullDenominators()),
	} {
		for _, txt := range []Text{d.Unit, d.Basis, d.AbsentWhy} {
			if halfWritten(txt) {
				t.Errorf("denominator text is one-sided: mr=%q en=%q", txt.MR, txt.EN)
			}
		}
	}
}

func TestAMissingLanguageFallsBackRatherThanBlanking(t *testing.T) {
	// A blank cell in a report reads as withheld. An untranslated sentence is
	// a shortcoming somebody can see and fix; a gap is a different and worse
	// claim about the document.
	only := T("", "English only")
	if got := only.In(Marathi); got != "English only" {
		t.Errorf("Marathi rendering blanked instead of falling back: %q", got)
	}

	other := T("मराठीच", "")
	if got := other.In(English); got != "मराठीच" {
		t.Errorf("English rendering blanked instead of falling back: %q", got)
	}
}

func TestBothLanguagesReportTheSameNumber(t *testing.T) {
	// Tf takes one argument list for both format strings, so the halves
	// cannot report different values. This asserts the property rather than
	// the implementation, because the day somebody adds a second argument
	// list is the day the two halves can disagree about a compensation
	// figure.
	got := Tf("घट %.0f%%", "loss %.0f%%", 62.0)
	if !strings.Contains(got.MR, "62") || !strings.Contains(got.EN, "62") {
		t.Fatalf("the two halves disagree: mr=%q en=%q", got.MR, got.EN)
	}
}

func TestOnlyMeasurementAndDenominatorDetermineTheFigure(t *testing.T) {
	// Kept narrow on purpose: over these two sections alone, the quadrat count
	// sets the magnitude and the threshold yield sets what it is a percentage
	// of. The whole document's set is asserted in build_test.go, which is
	// where a new Determines row would actually appear.
	d := &Dossier{}
	d.Add(FieldMeasurement([]fieldwork.Measurement{fullMeasurement()}, Case{Calamity: "hailstorm"}))
	d.Add(Baseline(fullDenominators()))

	want := map[string]bool{
		"Stand loss":             true,
		"Lodged plants":          true,
		"Harvested organ damage": true,
		"Issued threshold yield": true,
	}
	for _, r := range d.Determining() {
		if !want[r.Parameter.EN] {
			t.Errorf("%q is marked Determines in these two sections", r.Parameter.EN)
		}
	}
	if len(d.Determining()) == 0 {
		t.Fatal("nothing determines the figure, which cannot be right")
	}
}

func TestSatelliteEvidenceNeverClaimsToMeasureDamage(t *testing.T) {
	// assessment.Satellite says it in its own doc comment: these are not a
	// damage measurement. The report must not contradict the code it reports
	// on.
	s := Baseline(fullDenominators())
	for _, r := range s.Rows {
		if r.Parameter.EN == "Issued threshold yield" {
			continue
		}
		if r.Role == Determines {
			t.Errorf("%q is satellite-derived and marked Determines", r.Parameter.EN)
		}
	}
}

func TestAnAbsentParameterSaysSoInWords(t *testing.T) {
	s := FieldMeasurement(nil, Case{})

	if len(s.Rows) != 1 || !s.Rows[0].Absent {
		t.Fatal("a claim with no hand count should still print the row")
	}
	if s.Rows[0].AbsentWhy.Empty() {
		t.Error("an absent row must say why; a blank cell reads as withheld")
	}
	// A hand count of plants is officer data entry and is no longer asked
	// for, so its absence is the ordinary state rather than a gap. The row
	// stays -- a reader must not have to wonder whether one was taken -- but
	// it carries no weight and the section says what the figure does rest on.
	if s.Rows[0].Role != Context {
		t.Error("a measurement the system never asks for must not be marked " +
			"as determining the figure; that reads as a missing input")
	}
	if !strings.Contains(s.Preamble.EN, "not asked for in this system") {
		t.Errorf("the preamble should say a hand count is not part of this "+
			"system: %q", s.Preamble.EN)
	}
	for _, where := range []string{"7a", "11"} {
		if !strings.Contains(s.Preamble.EN, where) {
			t.Errorf("the preamble should point at what the figure does rest "+
				"on (§%s): %q", where, s.Preamble.EN)
		}
	}
}

func TestLodgingIsNeverAddedToStandLoss(t *testing.T) {
	// A lodged plant is still standing. Adding the two counts the same
	// plant's loss twice, and the report is where that mistake would become
	// somebody's compensation.
	s := FieldMeasurement([]fieldwork.Measurement{fullMeasurement()}, Case{Calamity: "hailstorm"})

	var stand, lodged *Row
	for i := range s.Rows {
		switch s.Rows[i].Parameter.EN {
		case "Stand loss":
			stand = &s.Rows[i]
		case "Lodged plants":
			lodged = &s.Rows[i]
		}
	}
	if stand == nil || lodged == nil {
		t.Fatal("both figures should be reported")
	}
	// The guarantee, not the wording: the row must say the two percentages are
	// kept apart. It used to read "the two are never added"; it now leads with
	// what lodging does to yield and states the separation after, which is the
	// same guarantee in the affirmative.
	if !strings.Contains(lodged.Effect.EN, "kept separate from stand loss") {
		t.Errorf("the lodging row must say the two are not added: %q",
			lodged.Effect.EN)
	}
	if !strings.Contains(lodged.Effect.EN, "twice") {
		t.Errorf("and must say why -- double-counting one plant: %q",
			lodged.Effect.EN)
	}
}

func TestEveryDerivedFigureShowsItsArithmetic(t *testing.T) {
	// fieldwork.Derived.Working exists so an officer reads a derivation
	// rather than a result. A number nobody can check is a number an appeal
	// discards.
	s := FieldMeasurement([]fieldwork.Measurement{fullMeasurement()}, Case{Calamity: "hailstorm"})
	for _, r := range s.Rows {
		if r.Role != Determines {
			continue
		}
		if len(r.Working) == 0 {
			t.Errorf("%q determines the figure and shows no arithmetic",
				r.Parameter.EN)
		}
	}
}

func TestNoThresholdYieldIsReportedAsJudgementNotMeasurement(t *testing.T) {
	// The structural problem LOSS_PARAMETERS.md opens with. Where the
	// denominator is missing, saying so costs nothing the system ever had --
	// and not saying so leaves a judgement reading as a measurement, which is
	// the one thing that document warns against.
	d := DenominatorOf(&baseline.Denominators{})

	if d.Known {
		t.Fatal("no issued yield, so no denominator is known")
	}
	if d.AbsentWhy.Empty() {
		t.Fatal("an unknown denominator must say why")
	}
	if !strings.Contains(d.AbsentWhy.EN, "judgement") {
		t.Errorf("it must say the figure is a judgement: %q", d.AbsentWhy.EN)
	}
	if d.AbsentWhy.MR == "" {
		t.Error("and it must say so in Marathi, which is the half the farmer reads")
	}
}

func TestAThinNeighbourhoodComparisonIsSuppressed(t *testing.T) {
	// Not for privacy -- the neighbourhood is a grid cell and names nobody.
	// A fall computed from two cloud-free passes has the shape of evidence
	// and none of the substance, and printing it beside a compensation figure
	// invites it to be argued over.
	thin := &baseline.Denominators{
		Peer: &baseline.Peer{
			Observations:      2,
			NeighbourhoodRRVL: 40,
			Verdict:           baseline.PeerRegional,
		},
	}

	s := Baseline(thin)
	var peer *Row
	for i := range s.Rows {
		if strings.Contains(s.Rows[i].Parameter.EN, "surrounding fields") {
			peer = &s.Rows[i]
		}
	}
	if peer == nil {
		t.Fatal("the comparison row should still appear")
	}
	if !peer.Absent {
		t.Error("a two-observation comparison should be suppressed, not printed")
	}
	if !strings.Contains(peer.AbsentWhy.EN, "2") {
		t.Errorf("and it should say how few there were: %q", peer.AbsentWhy.EN)
	}
}

func TestNoBaselineAtAllIsStatedAtTheTopOfTheSection(t *testing.T) {
	s := Baseline(&baseline.Denominators{})
	if !strings.Contains(s.Preamble.EN, "no baseline") {
		t.Errorf("the section must open by saying it has no baseline: %q",
			s.Preamble.EN)
	}
}

func TestAnEmptySectionIsNotPrintedAsAnEmptyHeading(t *testing.T) {
	// A reader counting headings would take the gap for a missing analysis.
	// Where the absence matters the builder adds an absent row instead, which
	// says so in words.
	d := &Dossier{}
	d.Add(Section{Number: "99", Title: T("रिकामा", "Empty")})

	if len(d.Sections) != 0 {
		t.Error("a section with nothing to say should not be printed")
	}
}

// --- fixtures ---------------------------------------------------------------

func fullMeasurement() fieldwork.Measurement {
	return fieldwork.Measurement{
		ID:             "m1",
		ReportID:       "r1",
		RecordedBy:     "u1",
		RecordedName:   "T. Patil",
		RecordedAt:     "2026-08-29",
		QuadratSideM:   1,
		PlantsExpected: ptrI(240),
		PlantsPresent:  ptrI(92),
		PlantsLodged:   ptrI(30),
		OrganSampled:   ptrI(50),
		OrganDamaged:   ptrI(35),
		SoilType:       "black cotton",
		CaptureID:      "c1",
	}
}

func fullDenominators() *baseline.Denominators {
	return &baseline.Denominators{
		Yield: &baseline.ThresholdYield{
			Crop:         "soybean",
			Season:       "kharif",
			CropYear:     "2026-27",
			YieldKgPerHa: 1180,
			Basis:        "best five of the last seven seasons",
			Source:       "District Agriculture Office",
			UnitName:     "Barshi",
			UnitLevel:    "taluka",
			IssuedAt:     "2026-06-01",
		},
		History: &baseline.History{
			ThisSeasonPeak: 0.41,
			Seasons: []baseline.SeasonPeak{
				{Year: 2023, PeakNDVI: 0.70},
				{Year: 2024, PeakNDVI: 0.68},
				{Year: 2025, PeakNDVI: 0.72},
			},
			MeanPeak:   0.70,
			StdDev:     0.02,
			ZScore:     ptrF(-14.5),
			Verdict:    baseline.VerdictBelowNormal,
			Confidence: "moderate",
		},
		Peer: &baseline.Peer{
			GridCell:              "18.17,75.81",
			RadiusM:               1000,
			NeighbourhoodPeak:     0.69,
			NeighbourhoodIncident: 0.60,
			NeighbourhoodRRVL:     13,
			Observations:          9,
			ParcelRRVL:            62,
			RelativeAnomaly:       49,
			Verdict:               baseline.PeerLocal,
		},
	}
}
