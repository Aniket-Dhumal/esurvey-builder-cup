package calibration

import (
	"strings"
	"testing"
)

// This package will say almost nothing for a season, so what is tested is
// that it says nothing *honestly* — and above all that it never pools a
// blind finding with one taken after the officer saw the model's answer.

func f(v float64) *float64 { return &v }

func sample(declared float64, modelled, officer *float64, sawModel bool) Sample {
	return Sample{
		ReportReference: "CLR-1",
		AssessmentID:    "a-1",
		Declared:        declared,
		Modelled:        modelled,
		Officer:         officer,
		SawModel:        sawModel,
		Grounded:        true,
	}
}

func TestABlindFindingAndAnInformedOneAreNeverPooled(t *testing.T) {
	// The whole reason this package is careful. An officer who saw the
	// model's figure and wrote a similar one has told us they read it.
	// Pooling would produce an agreement rate that improves as the platform
	// shows officers more of its own answers.
	s := Summarise([]Sample{
		sample(60, f(55), f(30), false), // blind, 25 points out
		sample(60, f(55), f(30), false),
		sample(60, f(55), f(54), true), // informed, 1 point out
		sample(60, f(55), f(54), true),
	})

	if s.Blind.N != 2 || s.Informed.N != 2 {
		t.Fatalf("blind n=%d informed n=%d, want 2 and 2", s.Blind.N, s.Informed.N)
	}
	if s.Blind.MeanAbsError <= s.Informed.MeanAbsError {
		t.Errorf("blind %.1f and informed %.1f; the fixture intends them to differ",
			s.Blind.MeanAbsError, s.Informed.MeanAbsError)
	}
}

func TestAnchoringIsNamedWhenItShows(t *testing.T) {
	// If informed officers agree markedly better than blind ones, that is
	// what anchoring looks like and the report should say so rather than
	// leaving two numbers side by side for somebody to read the wrong way.
	s := Summarise([]Sample{
		sample(60, f(55), f(25), false),
		sample(60, f(55), f(20), false),
		sample(60, f(55), f(56), true),
		sample(60, f(55), f(54), true),
	})

	var said bool
	for _, n := range s.Notes {
		if strings.Contains(n, "anchoring") {
			said = true
			if !strings.Contains(n, "after the finding rather than before") {
				t.Errorf("the note does not say what to do about it: %s", n)
			}
		}
	}
	if !said {
		t.Errorf("anchoring was not named: %v", s.Notes)
	}
}

func TestWithNoBlindFindingNothingIsClaimedAboutTheModel(t *testing.T) {
	s := Summarise([]Sample{
		sample(60, f(55), f(54), true),
		sample(60, f(50), f(49), true),
	})

	if s.Blind.N != 0 {
		t.Fatalf("blind n = %d", s.Blind.N)
	}
	var said bool
	for _, n := range s.Notes {
		if strings.Contains(n, "nothing here says anything about") {
			said = true
		}
	}
	if !said {
		t.Errorf("the report claims something from informed findings alone: %v", s.Notes)
	}
}

func TestASmallSampleIsCalledADirectionNotAMeasurement(t *testing.T) {
	s := Summarise([]Sample{
		sample(60, f(55), f(50), false),
		sample(60, f(40), f(45), false),
	})

	var said bool
	for _, n := range s.Notes {
		if strings.Contains(n, "direction rather than a measurement") {
			said = true
		}
	}
	if !said {
		t.Errorf("two samples were reported without qualification: %v", s.Notes)
	}
}

// ---------------------------------------------------------------------------
// The arithmetic
// ---------------------------------------------------------------------------

func TestTheSignSaysWhichWayTheModelReadsIt(t *testing.T) {
	// Positive means the model read the loss higher than the officer did.
	// Getting the sign backwards would send somebody tuning it the wrong way.
	s := Summarise([]Sample{
		sample(60, f(70), f(50), false), // model 20 high
		sample(60, f(60), f(50), false), // model 10 high
	})
	if s.Blind.MeanError != 15 {
		t.Errorf("mean error = %v, want +15", s.Blind.MeanError)
	}
	if s.Blind.MeanAbsError != 15 {
		t.Errorf("mean absolute error = %v", s.Blind.MeanAbsError)
	}
}

func TestTheMedianResistsTheOneReportSomebodyMistyped(t *testing.T) {
	// A figure entered as 5 for 50 would drag a mean a long way. The median
	// is what an honest summary leads with.
	s := Summarise([]Sample{
		sample(60, f(52), f(50), false),
		sample(60, f(48), f(50), false),
		sample(60, f(51), f(50), false),
		sample(60, f(95), f(5), false), // the typo
	})
	if s.Blind.MedianAbsError > 5 {
		t.Errorf("median = %v; a single outlier reached it", s.Blind.MedianAbsError)
	}
	if s.Blind.MeanAbsError <= s.Blind.MedianAbsError {
		t.Errorf("mean %v is not above median %v; the fixture intends an outlier",
			s.Blind.MeanAbsError, s.Blind.MedianAbsError)
	}
}

func TestAgreementIsCountedAgainstSomethingThatMatters(t *testing.T) {
	// Ten points, because compensation bands are wider than that: a
	// difference smaller than a band changed nothing for the farmer.
	s := Summarise([]Sample{
		sample(60, f(50), f(45), false), // within
		sample(60, f(50), f(41), false), // within
		sample(60, f(50), f(30), false), // not
		sample(60, f(50), f(80), false), // not
	})
	if s.Blind.WithinTen != 50 {
		t.Errorf("within ten = %v%%, want 50", s.Blind.WithinTen)
	}
}

func TestAFarmersDeclarationIsComparedWithoutAnyModelAtAll(t *testing.T) {
	// Worth knowing on its own: whether farmers systematically over- or
	// under-claim needs no model, and is actionable either way.
	s := Summarise([]Sample{
		sample(80, nil, f(40), false),
		sample(70, nil, f(40), false),
	})
	if s.Farmer.N != 2 {
		t.Fatalf("farmer comparison n = %d", s.Farmer.N)
	}
	if s.Farmer.MeanError != 35 {
		t.Errorf("mean = %v, want +35", s.Farmer.MeanError)
	}
	if s.Blind.N != 0 {
		t.Errorf("a model comparison was produced with no model figure")
	}
}

func TestOverClaimingIsExplainedRatherThanAccused(t *testing.T) {
	// A farmer asked for a percentage of a loss whose denominator nobody has
	// told them will estimate high. That is a fact about the form.
	var samples []Sample
	for i := 0; i < 25; i++ {
		samples = append(samples, sample(80, nil, f(40), false))
	}
	s := Summarise(samples)

	var said bool
	for _, n := range s.Notes {
		if strings.Contains(n, "denominator") {
			said = true
			if strings.Contains(strings.ToLower(n), "dishonest") {
				t.Errorf("the note accuses: %s", n)
			}
		}
	}
	if !said {
		t.Errorf("systematic over-claiming was reported without its explanation: %v",
			s.Notes)
	}
}

func TestUnderClaimingIsFlaggedAsAnEntitlementProblem(t *testing.T) {
	var samples []Sample
	for i := 0; i < 25; i++ {
		samples = append(samples, sample(20, nil, f(60), false))
	}
	s := Summarise(samples)

	var said bool
	for _, n := range s.Notes {
		if strings.Contains(n, "entitled") {
			said = true
		}
	}
	if !said {
		t.Errorf("under-claiming was not framed as an entitlement problem: %v",
			s.Notes)
	}
}

// ---------------------------------------------------------------------------
// The behaviour nothing was measuring
// ---------------------------------------------------------------------------

func TestARefusalToStateAFigureIsCountedAsAResult(t *testing.T) {
	// Without ground imagery the panel is supposed to refuse. That refusal
	// rate is the behaviour this platform most wants to keep and the one
	// nothing has been watching.
	s := Summarise([]Sample{
		sample(60, nil, f(50), false), // refused
		sample(60, nil, f(40), false), // refused
		sample(60, f(55), f(50), false),
		sample(60, f(45), f(40), false),
	})

	if s.Refused != 2 {
		t.Errorf("refused = %d, want 2", s.Refused)
	}
	if s.RefusedPercent != 50 {
		t.Errorf("refusal rate = %v%%, want 50", s.RefusedPercent)
	}
	// A refusal is not a data gap, so it must not enter the error statistics.
	if s.Blind.N != 2 {
		t.Errorf("blind n = %d; refusals should not be compared", s.Blind.N)
	}
}

func TestARefusalRateNearZeroIsQuestionedRatherThanCelebrated(t *testing.T) {
	var samples []Sample
	for i := 0; i < 30; i++ {
		samples = append(samples, sample(60, f(55), f(50), false))
	}
	s := Summarise(samples)

	var said bool
	for _, n := range s.Notes {
		if strings.Contains(n, "worth checking rather than celebrating") {
			said = true
		}
	}
	if !said {
		t.Errorf("a zero refusal rate passed without comment: %v", s.Notes)
	}
}

func TestUngroundedAssessmentsAreCountedAndSaidPlainly(t *testing.T) {
	// If most assessments had no baseline, the comparison above is between
	// two judgements and should say so.
	var samples []Sample
	for i := 0; i < 10; i++ {
		x := sample(60, f(55), f(50), false)
		x.Grounded = i < 2
		samples = append(samples, x)
	}
	s := Summarise(samples)

	if s.GroundedPercent != 20 {
		t.Errorf("grounded = %v%%, want 20", s.GroundedPercent)
	}
	var said bool
	for _, n := range s.Notes {
		if strings.Contains(n, "between two judgements") {
			said = true
		}
	}
	if !said {
		t.Errorf("an ungrounded set was compared without qualification: %v", s.Notes)
	}
}

func TestNothingAtAllProducesNoFalseStatistics(t *testing.T) {
	s := Summarise(nil)
	if s.Reports != 0 || s.Blind.N != 0 || s.Farmer.N != 0 {
		t.Errorf("statistics were produced from nothing: %+v", s)
	}
	if s.RefusedPercent != 0 || s.GroundedPercent != 0 {
		t.Error("a rate was computed from no denominator")
	}
	if len(s.Notes) == 0 {
		t.Error("an empty set carried no explanation")
	}
}

func TestAnUnassessedReportIsNotCountedAsARefusal(t *testing.T) {
	// A report the panel never ran on is not a report where it declined.
	x := sample(60, nil, f(50), false)
	x.AssessmentID = ""
	s := Summarise([]Sample{x})

	if s.Refused != 0 {
		t.Errorf("refused = %d for a report with no assessment", s.Refused)
	}
	if s.AssessedReports != 0 {
		t.Errorf("assessed = %d", s.AssessedReports)
	}
}
