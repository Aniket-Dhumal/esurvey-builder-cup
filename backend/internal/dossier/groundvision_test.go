package dossier

import (
	"strings"
	"testing"

	"github.com/agribid/esurvey-platform/backend/internal/groundcv"
)

// The section that is usually empty, and has to say so.
//
// No vision pipeline exists yet, so every dossier printed today reaches this
// section with nothing in it. That is exactly when it matters: a report that
// silently omits the heaviest evidence in its own specification reads far
// stronger than it is, and the officer signing it has no way to know.

func textOf(x Text) string { return strings.ToLower(x.EN) }

func TestAnAbsentVideoIsStatedNotOmitted(t *testing.T) {
	s := GroundVision(nil, "")

	if len(s.Rows) == 0 {
		t.Fatal("the section rendered nothing at all; the absence has to be printed")
	}
	if !s.Rows[0].Absent {
		t.Error("the row was not marked absent")
	}

	// Determines, not Corroborates. This is not a nice-to-have that went
	// missing; it is the input the specification leans on hardest.
	if s.Rows[0].Role != Determines {
		t.Errorf("role = %v, want Determines", s.Rows[0].Role)
	}

	// And the officer is told what the absence costs, in both languages.
	var explained bool
	for _, n := range s.Notes {
		if strings.Contains(textOf(n), "heaviest") && n.MR != "" {
			explained = true
		}
	}
	if !explained {
		t.Error("nothing told the officer what was missing or why it matters")
	}
}

func TestAMeasuredVideoIsShownWithItsWorking(t *testing.T) {
	s := GroundVision(&groundcv.Score{
		SCV: 37, LodgingLoss: 50, SubmergedRatio: 0.30, NecroticRatio: 0.20,
		LodgingClass: groundcv.LodgingSevere, StemAngleDegree: 60,
		Quality: 0.88, Keyframes: 18, CropPixelShare: 0.71,
	}, "")

	if len(s.Rows) != 3 {
		t.Fatalf("rows = %d, want 3", len(s.Rows))
	}
	if s.Rows[0].Absent {
		t.Error("a measured video was rendered as absent")
	}

	// The arithmetic is shown, because a farmer objecting to 37% is entitled
	// to see which of the three components carried it.
	if len(s.Rows[0].Working) == 0 {
		t.Fatal("the score was printed with no working")
	}
	w := textOf(s.Rows[0].Working[0])
	for _, want := range []string{"0.45", "0.35", "0.20", "lodging", "submerged", "necrotic"} {
		if !strings.Contains(w, want) {
			t.Errorf("the working does not show %q: %s", want, w)
		}
	}
}

func TestAFrameFullOfSkyIsFlagged(t *testing.T) {
	// The ratios divide by crop pixels, so a clip pointed at the horizon still
	// returns a number between 0 and 100. The note is what stops it being read
	// as a measurement of a field.
	s := GroundVision(&groundcv.Score{
		SCV: 60, LodgingClass: groundcv.LodgingSevere, StemAngleDegree: 55,
		Quality: 0.9, Keyframes: 10, CropPixelShare: 0.06,
	}, "")

	var flagged bool
	for _, n := range s.Notes {
		if strings.Contains(textOf(n), "sky") {
			flagged = true
		}
	}
	if !flagged {
		t.Error("a clip that was mostly sky was presented without qualification")
	}
}

func TestPoorFootageSaysWhatTheSpecificationDoesAboutIt(t *testing.T) {
	s := GroundVision(&groundcv.Score{
		SCV: 40, LodgingClass: groundcv.LodgingMild, StemAngleDegree: 30,
		Quality: 0.35, Keyframes: 6, CropPixelShare: 0.6,
	}, "")

	var noted bool
	for _, n := range s.Notes {
		if strings.Contains(textOf(n), "quality") {
			noted = true
		}
	}
	if !noted {
		t.Error("low-quality footage was scored without saying so")
	}
}

func TestTheReasonForAnAbsenceIsCarriedThrough(t *testing.T) {
	// The assembler's own words reach the page, so "no pipeline configured"
	// and "no video was captured for this report" do not read alike.
	const why = "no survey video has been captured for this report"
	s := GroundVision(nil, why)

	if got := s.Rows[0].AbsentWhy.EN; !strings.Contains(got, why) {
		t.Errorf("observed = %q, want it to carry %q", got, why)
	}
}
