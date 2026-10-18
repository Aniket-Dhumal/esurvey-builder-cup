package assessment

import (
	"testing"

	"github.com/agribid/esurvey-platform/backend/internal/ai"
	"github.com/agribid/esurvey-platform/backend/internal/satellite"
)

func series(pts ...satellite.NDVIPoint) []satellite.NDVIPoint { return pts }

func pt(date string, v float64) satellite.NDVIPoint {
	return satellite.NDVIPoint{Date: date, NDVI: v}
}

// --- deterministic satellite metrics --------------------------------------

// An empty window is an absence of evidence, not zero of anything.
func TestScoreSatelliteRejectsEmptySeries(t *testing.T) {
	if _, err := ScoreSatellite(nil); err == nil {
		t.Fatal("expected an error for a wholly clouded window, got nil")
	}
}

func TestScoreSatelliteRRVL(t *testing.T) {
	// Baseline 0.80 falling to 0.30. Range to bare soil is 0.80-0.12 = 0.68,
	// so relative loss is 0.50/0.68 = 73.5%.
	got, err := ScoreSatellite(series(pt("2025-07-01", 0.80), pt("2025-08-01", 0.30)))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d := got.DeltaNDVI; d < 0.499 || d > 0.501 {
		t.Errorf("dNDVI = %.4f, want 0.50", d)
	}
	if r := got.RRVLPercent; r < 73.0 || r > 74.0 {
		t.Errorf("RRVL = %.2f%%, want ~73.5%%", r)
	}
}

func TestScoreSatelliteDetectsVegetation(t *testing.T) {
	got, _ := ScoreSatellite(series(pt("2025-08-01", 0.70), pt("2025-09-01", 0.62)))
	if got.VegetationDetected == nil || !*got.VegetationDetected {
		t.Error("a series peaking at 0.70 should report vegetation detected")
	}
}

func TestScoreSatelliteFlagsBareSoil(t *testing.T) {
	// The fraud case: a crop was declared, but nothing ever grew.
	got, _ := ScoreSatellite(series(pt("2025-08-01", 0.13), pt("2025-09-01", 0.12)))
	if got.VegetationDetected == nil || *got.VegetationDetected {
		t.Error("a series at bare soil should report no vegetation detected")
	}
}

func TestScoreSatelliteAbstainsOnOneReading(t *testing.T) {
	got, _ := ScoreSatellite(series(pt("2025-08-01", 0.70)))
	if got.VegetationDetected != nil {
		t.Error("one observation is not enough to judge presence")
	}
}

func TestLargestGapIsReported(t *testing.T) {
	got, _ := ScoreSatellite(series(
		pt("2025-07-19", 0.14), pt("2025-09-17", 0.36), pt("2025-10-12", 0.62)))
	if got.LargestGapDays < 55 || got.LargestGapDays > 65 {
		t.Errorf("largest gap = %d days, want ~60", got.LargestGapDays)
	}
}

func TestRRVLHandlesBaselineAtBareSoil(t *testing.T) {
	// Nothing was growing, so nothing could be lost.
	if got := RRVLPercent(0.10, 0.05); got != 0 {
		t.Errorf("RRVL = %.2f, want 0", got)
	}
}

// --- verification of the agent's answer -----------------------------------

func agentSaid(damage *float64, confidence float64, presence string) ai.Result {
	return ai.Result{
		DamagePercent: damage,
		Severity:      SeverityCatastrophic, // deliberately wrong; must be re-derived
		Confidence:    confidence,
		CropPresence:  presence,
		Reasoning:     "test",
		Model:         "gemini-test",
		PromptVersion: "test",
	}
}

func requestWith(images []ai.Image, pts ...ai.NDVIPoint) ai.AssessRequest {
	return ai.AssessRequest{
		AssessmentID: "test",
		IncidentDate: "2025-10-12",
		Parcel:       ai.Parcel{SurveyNumber: "453", OwnerExtentHectares: 1.6},
		Satellite: ai.SatelliteEvidence{
			TimeSeries:       pts,
			ObservationCount: len(pts),
		},
		Images: images,
	}
}

func f(v float64) *float64 { return &v }

var healthySeries = []ai.NDVIPoint{
	{Date: "2025-08-01", NDVI: 0.70},
	{Date: "2025-10-01", NDVI: 0.62},
}

func groundPhoto() ai.Image {
	return ai.Image{Kind: ai.ImageFieldPhoto, DataURI: "data:image/png;base64,AA=="}
}

func ndviOverlay() ai.Image {
	return ai.Image{Kind: ai.ImageNDVILast, DataURI: "data:image/png;base64,AA=="}
}

// The central rule: satellite alone cannot measure damage, so a figure
// produced without a field photograph is discarded however confident it
// sounded.
func TestVerifyDiscardsDamageWithoutGroundImagery(t *testing.T) {
	req := requestWith(nil, healthySeries...)
	got := Verify(agentSaid(f(62), 0.8, ai.PresenceConfirmed), req)

	if got.DamagePercent != nil {
		t.Errorf("damage = %v, want nil without ground imagery", *got.DamagePercent)
	}
	if !got.InsufficientEvidence {
		t.Error("discarding the figure should mark the assessment insufficient")
	}
	if !hasGuardrail(got.BackendGuardrails, "damage_requires_ground_imagery") {
		t.Errorf("guardrail not recorded: %v", got.BackendGuardrails)
	}
}

// A colourised satellite overlay is still satellite.
func TestVerifyRejectsOverlaysAsGroundTruth(t *testing.T) {
	req := requestWith([]ai.Image{ndviOverlay()}, healthySeries...)
	got := Verify(agentSaid(f(55), 0.7, ai.PresenceConfirmed), req)

	if got.DamagePercent != nil {
		t.Error("an NDVI overlay must not unlock a damage figure")
	}
}

func TestVerifyAllowsDamageWithGroundImagery(t *testing.T) {
	req := requestWith([]ai.Image{groundPhoto()}, healthySeries...)
	got := Verify(agentSaid(f(45), 0.7, ai.PresenceConfirmed), req)

	if got.DamagePercent == nil || *got.DamagePercent != 45 {
		t.Fatalf("damage = %v, want 45", got.DamagePercent)
	}
	// The agent claimed Catastrophic; 45% is Severe. The number wins.
	if got.Severity != SeveritySevere {
		t.Errorf("severity = %q, want %q", got.Severity, SeveritySevere)
	}
	if !hasGuardrail(got.BackendGuardrails, "severity_derived_from_damage_percent") {
		t.Errorf("severity should be re-derived: %v", got.BackendGuardrails)
	}
}

// Damage to a crop that never existed is incoherent.
func TestVerifyClearsDamageWhenNoCropPresent(t *testing.T) {
	req := requestWith([]ai.Image{groundPhoto()},
		ai.NDVIPoint{Date: "2025-08-01", NDVI: 0.13},
		ai.NDVIPoint{Date: "2025-10-01", NDVI: 0.12})
	got := Verify(agentSaid(f(40), 0.7, ai.PresenceAbsent), req)

	if got.DamagePercent != nil {
		t.Error("no crop present means no damage figure")
	}
	if !hasGuardrail(got.BackendGuardrails, "damage_cleared_no_crop_present") {
		t.Errorf("guardrail not recorded: %v", got.BackendGuardrails)
	}
}

// The backend re-derives presence from the series, so "nothing grew here"
// does not depend on a language model noticing.
func TestVerifyDowngradesPresenceContradictedBySeries(t *testing.T) {
	bare := requestWith(nil,
		ai.NDVIPoint{Date: "2025-08-01", NDVI: 0.13},
		ai.NDVIPoint{Date: "2025-10-01", NDVI: 0.12})
	got := Verify(agentSaid(nil, 0.5, ai.PresenceConfirmed), bare)

	if got.CropPresence != ai.PresenceInconclusive {
		t.Errorf("presence = %q, want inconclusive when the series is at bare soil",
			got.CropPresence)
	}
	if !hasGuardrail(got.BackendGuardrails, "presence_downgraded_series_at_bare_soil") {
		t.Errorf("guardrail not recorded: %v", got.BackendGuardrails)
	}
}

func TestVerifyDowngradesAbsenceContradictedBySeries(t *testing.T) {
	req := requestWith(nil, healthySeries...)
	got := Verify(agentSaid(nil, 0.5, ai.PresenceAbsent), req)

	if got.CropPresence != ai.PresenceInconclusive {
		t.Errorf("presence = %q, want inconclusive when the series shows vegetation",
			got.CropPresence)
	}
}

func TestVerifyCapsConfidenceWithoutGroundTruth(t *testing.T) {
	req := requestWith(nil, healthySeries...)
	got := Verify(agentSaid(nil, 0.95, ai.PresenceConfirmed), req)

	if got.Confidence > 0.6 {
		t.Errorf("confidence = %.2f, want capped at 0.60", got.Confidence)
	}
}

func TestVerifyClampsDamage(t *testing.T) {
	req := requestWith([]ai.Image{groundPhoto()}, healthySeries...)

	if got := Verify(agentSaid(f(180), 0.5, ai.PresenceConfirmed), req); *got.DamagePercent != 100 {
		t.Errorf("damage = %.1f, want 100", *got.DamagePercent)
	}
	if got := Verify(agentSaid(f(-20), 0.5, ai.PresenceConfirmed), req); *got.DamagePercent != 0 {
		t.Errorf("damage = %.1f, want 0", *got.DamagePercent)
	}
}

// --- triage ---------------------------------------------------------------

func TestTriageNeverFastTracks(t *testing.T) {
	v := Verified{
		CropPresence:  ai.PresenceConfirmed,
		Confidence:    0.95,
		DamagePercent: f(30),
	}
	if got := TriageFor(v); got == TriageGreen {
		t.Errorf("no assessment should fast-track without ground truth; got %q", got)
	}
}

func TestTriageEscalatesOnCriticalFraud(t *testing.T) {
	v := Verified{
		CropPresence: ai.PresenceConfirmed,
		Confidence:   0.6,
		FraudIndicators: []ai.FraudIndicator{
			{Code: "no_vegetation_detected", Severity: ai.FraudCritical},
		},
	}
	if got := TriageFor(v); got != TriageRed {
		t.Errorf("triage = %q, want %q on a critical fraud flag", got, TriageRed)
	}
}

func TestTriageEscalatesWhenNoCropPresent(t *testing.T) {
	v := Verified{CropPresence: ai.PresenceAbsent, Confidence: 0.8}
	if got := TriageFor(v); got != TriageRed {
		t.Errorf("triage = %q, want %q", got, TriageRed)
	}
}

func TestCriticalFraudIndicatorsFilters(t *testing.T) {
	in := []ai.FraudIndicator{
		{Code: "area_inconsistent", Severity: ai.FraudInfo},
		{Code: "crop_type_mismatch", Severity: ai.FraudWarning},
		{Code: "no_vegetation_detected", Severity: ai.FraudCritical},
	}
	if got := CriticalFraudIndicators(in); len(got) != 1 ||
		got[0].Code != "no_vegetation_detected" {
		t.Errorf("filter returned %v", got)
	}
}

func hasGuardrail(list []string, want string) bool {
	for _, g := range list {
		if g == want {
			return true
		}
	}
	return false
}
