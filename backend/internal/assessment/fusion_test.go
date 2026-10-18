package assessment

import (
	"math"
	"testing"

	"github.com/agribid/esurvey-platform/backend/internal/groundcv"
)

// The specification's fusion, checked against the specification.
//
// The weight table is quoted from the source document rather than derived, so
// a later edit that adjusts a branch by feel fails here and has to argue with
// the document instead of with the tests.

func ground(scv, quality float64) *groundcv.Score {
	return &groundcv.Score{SCV: scv, Quality: quality}
}

func TestTheWeightTableIsTheSpecifications(t *testing.T) {
	// if   q_cv >= 0.85 and cloud <  0.20: 0.50, 0.35, 0.15
	// elif q_cv >= 0.85 and cloud >= 0.20: 0.65, 0.15, 0.20
	// elif q_cv <  0.60:                   0.20, 0.55, 0.25
	// else:                                0.35, 0.45, 0.20
	cases := []struct {
		name            string
		quality, cloud  float64
		wCV, wSat, wDEM float64
	}{
		{"good video, clear sky", 0.90, 0.10, 0.50, 0.35, 0.15},
		{"exactly at both thresholds", 0.85, 0.19, 0.50, 0.35, 0.15},
		{"good video under cloud", 0.90, 0.60, 0.65, 0.15, 0.20},
		{"cloud exactly at the threshold", 0.85, 0.20, 0.65, 0.15, 0.20},
		{"poor video", 0.40, 0.10, 0.20, 0.55, 0.25},
		{"poor video just under the line", 0.59, 0.50, 0.20, 0.55, 0.25},
		{"usable but not good", 0.70, 0.10, 0.35, 0.45, 0.20},
		{"usable, at the lower bound", 0.60, 0.90, 0.35, 0.45, 0.20},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := Reconcile(FusionInput{
				Ground: ground(50, c.quality), SSat: 40, SDEM: 30, CloudCover: c.cloud,
			})
			if f == nil {
				t.Fatal("no composite")
			}
			if f.WeightGroundCV != c.wCV || f.WeightSatellite != c.wSat || f.WeightTerrain != c.wDEM {
				t.Errorf("weights = %.2f/%.2f/%.2f, want %.2f/%.2f/%.2f",
					f.WeightGroundCV, f.WeightSatellite, f.WeightTerrain,
					c.wCV, c.wSat, c.wDEM)
			}
			if s := f.WeightGroundCV + f.WeightSatellite + f.WeightTerrain; math.Abs(s-1) > 1e-9 {
				t.Errorf("weights sum to %v, not 1", s)
			}
		})
	}
}

func TestCloudShiftsTheWeightOntoTheGround(t *testing.T) {
	// The point of the whole exercise. Same field, same footage; the only
	// change is that cloud has taken the optical record away. The ground
	// signal must go up, not down, because it is the one that can still see.
	clear := Reconcile(FusionInput{Ground: ground(70, 0.9), SSat: 20, SDEM: 30, CloudCover: 0.05})
	cloudy := Reconcile(FusionInput{Ground: ground(70, 0.9), SSat: 20, SDEM: 30, CloudCover: 0.80})

	if cloudy.WeightGroundCV <= clear.WeightGroundCV {
		t.Errorf("cloud did not shift weight onto the ground: %.2f -> %.2f",
			clear.WeightGroundCV, cloudy.WeightGroundCV)
	}
	if cloudy.WeightSatellite >= clear.WeightSatellite {
		t.Errorf("cloud did not take weight off the satellite: %.2f -> %.2f",
			clear.WeightSatellite, cloudy.WeightSatellite)
	}
	if cloudy.CDP <= clear.CDP {
		t.Error("with the ground reporting more damage than the sky, losing the " +
			"optical record should raise the composite, not lower it")
	}
}

func TestAWorkedComposite(t *testing.T) {
	// Good video, clear sky: 0.50, 0.35, 0.15.
	//   0.50*80 + 0.35*40 + 0.15*20 = 40 + 14 + 3 = 57
	f := Reconcile(FusionInput{
		Ground: ground(80, 0.90), SSat: 40, SDEM: 20, CloudCover: 0.10,
	})
	if math.Abs(f.CDP-57) > 1e-6 {
		t.Errorf("CDP = %v, want 57", f.CDP)
	}

	// confidence = q_cv * (1 - diff/100) * (1 - 0.4*cloud)
	//            = 0.90 * (1 - 40/100) * (1 - 0.04) = 0.90*0.60*0.96 = 0.5184
	if math.Abs(f.Confidence-0.5184) > 1e-6 {
		t.Errorf("confidence = %v, want 0.5184", f.Confidence)
	}
	if math.Abs(f.InterModalDiff-40) > 1e-9 {
		t.Errorf("diff = %v, want 40", f.InterModalDiff)
	}
}

func TestTriageFollowsTheSpecificationsThresholds(t *testing.T) {
	// green: confidence >= 0.85 and diff <= 8
	// red:   confidence <  0.60 or  diff >  25
	// amber: otherwise
	cases := []struct {
		name             string
		confidence, diff float64
		want             string
	}{
		{"agreeing and certain", 0.90, 4, TriageGreen},
		{"exactly on both green bounds", 0.85, 8, TriageGreen},
		{"certain but disagreeing", 0.90, 12, TriageAmber},
		{"wide disagreement", 0.90, 26, TriageRed},
		{"too uncertain", 0.55, 2, TriageRed},
		{"middling", 0.70, 15, TriageAmber},
	}
	for _, c := range cases {
		if got := triageForComposite(c.confidence, c.diff); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

func TestNoGroundScoreMeansNoComposite(t *testing.T) {
	// Satellite cannot measure damage — Verify enforces that on the published
	// figure, and a composite computed mostly from NDVI would smuggle the same
	// number back in under another name.
	if f := Reconcile(FusionInput{Ground: nil, SSat: 90, SDEM: 80, CloudCover: 0}); f != nil {
		t.Fatalf("a composite was computed with no ground evidence: %+v", f)
	}
}

func TestDivergenceEscalatesToAHuman(t *testing.T) {
	pct := func(v float64) *float64 { return &v }

	f := Reconcile(FusionInput{Ground: ground(80, 0.9), SSat: 75, SDEM: 40, CloudCover: 0.1})

	// CDP here is 0.50*80 + 0.35*75 + 0.15*40 = 40 + 26.25 + 6 = 72.25
	if math.Abs(f.CDP-72.25) > 1e-6 {
		t.Fatalf("CDP = %v, want 72.25", f.CDP)
	}

	if f.Diverges(pct(70)) {
		t.Error("a two-point difference should not escalate")
	}
	if !f.Diverges(pct(20)) {
		t.Error("a fifty-point difference about the same field must escalate")
	}

	// And it must actually reach the routing, not merely be computable.
	v := Verified{DamagePercent: pct(20), Confidence: 0.9, Fusion: f}
	if got := TriageFor(v); got != TriageRed {
		t.Errorf("triage = %q, want %q when the composite and the panel disagree",
			got, TriageRed)
	}
}

func TestAMissingCrossCheckNeverEscalatesOnItsOwn(t *testing.T) {
	pct := func(v float64) *float64 { return &v }

	// Nil fusion is the ordinary case until a vision pipeline exists. It is an
	// absence of evidence about the panel's figure, not evidence against it.
	var f *Fusion
	if f.Diverges(pct(60)) {
		t.Error("an absent composite reported a disagreement")
	}

	// And a composite with nothing to compare against is equally silent.
	real := Reconcile(FusionInput{Ground: ground(80, 0.9), SSat: 10, SDEM: 10, CloudCover: 0})
	if real.Diverges(nil) {
		t.Error("a refused assessment carries no figure, so nothing can diverge from it")
	}

	v := Verified{DamagePercent: pct(60), Confidence: 0.9}
	if got := TriageFor(v); got == TriageRed {
		t.Error("a claim was escalated purely because the cross-check was absent")
	}
}

func TestEverythingStaysInRange(t *testing.T) {
	// Inputs arrive from a vision model, an NDVI series and a DEM, and any of
	// them can hand over something out of range. Nothing here may emit a
	// percentage above 100 or a confidence above 1.
	for _, in := range []FusionInput{
		{Ground: ground(1000, 5), SSat: 900, SDEM: 900, CloudCover: 9},
		{Ground: ground(-50, -1), SSat: -900, SDEM: -900, CloudCover: -9},
	} {
		f := Reconcile(in)
		if f.CDP < 0 || f.CDP > 100 {
			t.Errorf("CDP = %v, outside [0,100]", f.CDP)
		}
		if f.Confidence < 0 || f.Confidence > 1 {
			t.Errorf("confidence = %v, outside [0,1]", f.Confidence)
		}
	}
}
