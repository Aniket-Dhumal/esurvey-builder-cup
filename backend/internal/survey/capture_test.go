package survey

import (
	"testing"
	"time"
)

// The capture gates are the anti-fraud boundary. Two decisions run through
// them and they pull in opposite directions:
//
//   - Withholding a photograph from the model when it does not show what it
//     claims to. Failing this lets a photograph of a different field, or of
//     last season, become evidence of this loss.
//   - Not withholding a photograph because the farmer had no signal for a
//     week. Failing this discards exactly the evidence this system exists to
//     collect, from exactly the farmer it exists for.
//
// Everything below is about keeping those two apart.

func at(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

// --- what may reach the model ----------------------------------------------

func TestOnlyMisleadingFailuresWithholdAPhotograph(t *testing.T) {
	cases := []struct {
		name   string
		failed []string
		usable bool
	}{
		{"nothing failed", nil, true},

		// The two that disqualify. In both cases the image does not show what
		// it claims, and the model has no way to know that.
		{"taken outside the plot", []string{GateInsideParcel}, false},
		{"taken before the loss", []string{GateAfterIncident}, false},

		// The rest are provenance. A model told an image is ten days old can
		// reason about that; withholding it entirely cannot be reasoned with.
		{"uploaded late", []string{GateFreshCapture}, true},
		{"poor position fix", []string{GatePositionFix}, true},
		{"outside the notified window", []string{GateWithinWindow}, true},
		{"submitted twice", []string{GateDistinctImage}, true},

		{
			name:   "every caveat at once is still usable",
			failed: []string{GateFreshCapture, GatePositionFix, GateWithinWindow, GateDistinctImage},
			usable: true,
		},
		{
			name:   "one disqualifier among caveats still withholds",
			failed: []string{GateFreshCapture, GateInsideParcel, GatePositionFix},
			usable: false,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cap := &Capture{GatesFailed: c.failed}
			if got := cap.UsableAsEvidence(); got != c.usable {
				t.Errorf("UsableAsEvidence() = %v, want %v", got, c.usable)
			}
		})
	}
}

func TestCaveatsCarryEveryNonDisqualifyingFailure(t *testing.T) {
	// A caveat that does not reach the model is a caveat that does not exist:
	// the image is shown as though it were sound.
	cap := &Capture{GatesFailed: []string{
		GateFreshCapture, GatePositionFix, GateWithinWindow, GateDistinctImage,
	}}

	got := cap.Caveats()
	if len(got) != 4 {
		t.Fatalf("caveats = %v, want one per failure", got)
	}
	for _, c := range got {
		if c == "" {
			t.Error("an empty caveat says nothing to the model")
		}
	}
}

func TestADisqualifiedCaptureDoesNotAlsoCaveat(t *testing.T) {
	// It is not shown at all, so describing it would be describing something
	// nobody sees — and would read, in a log, as though it had been used.
	cap := &Capture{GatesFailed: []string{GateInsideParcel, GateAfterIncident}}
	if got := cap.Caveats(); len(got) != 0 {
		t.Errorf("caveats = %v, want none", got)
	}
}

func TestTrustedIsStricterThanUsable(t *testing.T) {
	// The distinction the officer's screen depends on: a late upload is shown
	// to the model *and* flagged to the officer. Collapsing the two would
	// either hide the flag or discard the photograph.
	cap := &Capture{GatesFailed: []string{GateFreshCapture}}
	if cap.Trusted() {
		t.Error("a capture with a failed gate reported itself trusted")
	}
	if !cap.UsableAsEvidence() {
		t.Error("a late upload was withheld from the model")
	}
}

func TestEveryGateHasAnExplanation(t *testing.T) {
	// The officer reads these. A gate that fails with no sentence beside it
	// is a red mark nobody can act on.
	for _, g := range []string{
		GateInsideParcel, GatePositionFix, GateWithinWindow,
		GateAfterIncident, GateFreshCapture, GateDistinctImage,
	} {
		if GateExplanations[g] == "" {
			t.Errorf("gate %q has no explanation", g)
		}
	}
}

// --- the predicates ---------------------------------------------------------

func TestPositionFixUsable(t *testing.T) {
	cases := map[float64]bool{
		4:     true,  // a good handset fix
		100:   true,  // exactly the limit
		100.1: false, // just past it
		250:   false, // a cell-tower fix, useless inside a plot
		0:     false, // no fix at all, reported as zero
		-1:    false, // nonsense from a device
	}
	for accuracy, want := range cases {
		if got := PositionFixUsable(accuracy); got != want {
			t.Errorf("PositionFixUsable(%v) = %v, want %v", accuracy, got, want)
		}
	}
}

func TestAfterIncident(t *testing.T) {
	incident := at("2025-10-12T14:00:00Z")

	cases := []struct {
		name     string
		captured time.Time
		want     bool
	}{
		{"hours after the hail", at("2025-10-12T17:30:00Z"), true},
		{"the next morning", at("2025-10-13T07:00:00Z"), true},
		{"at the recorded moment", incident, true},

		// The incident time is what a farmer said later, from memory. An hour
		// of grace stops an honest capture failing on a rounding error.
		{"half an hour before", at("2025-10-12T13:30:00Z"), true},
		{"exactly one hour before", at("2025-10-12T13:00:00Z"), true},

		// Past the grace it is unambiguous: this is not a photograph of that
		// damage, whatever else it shows.
		{"two hours before", at("2025-10-12T12:00:00Z"), false},
		{"the day before", at("2025-10-11T14:00:00Z"), false},
		{"last season", at("2025-03-01T09:00:00Z"), false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := AfterIncident(c.captured, incident); got != c.want {
				t.Errorf("AfterIncident = %v, want %v", got, c.want)
			}
		})
	}
}

func TestWithinWindowComparesByDay(t *testing.T) {
	// The window is recorded as a date. A photograph taken at 06:00 local on
	// the opening day is 00:30 UTC, and must not fail for that.
	cases := []struct {
		name     string
		captured time.Time
		start    string
		want     bool
	}{
		{"early on the opening day", at("2025-10-12T00:30:00Z"), "2025-10-12", true},
		{"late on the opening day", at("2025-10-12T23:45:00Z"), "2025-10-12", true},
		{"days after it opened", at("2025-10-20T10:00:00Z"), "2025-10-12", true},
		{"the day before it opened", at("2025-10-11T23:59:00Z"), "2025-10-12", false},
		{"a month early", at("2025-09-12T10:00:00Z"), "2025-10-12", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := WithinWindow(c.captured, c.start); got != c.want {
				t.Errorf("WithinWindow = %v, want %v", got, c.want)
			}
		})
	}
}

func TestFreshCapture(t *testing.T) {
	now := at("2025-10-20T12:00:00Z")

	cases := []struct {
		name     string
		captured time.Time
		want     bool
	}{
		{"uploaded the same hour", at("2025-10-20T11:00:00Z"), true},
		{"three days later", at("2025-10-17T12:00:00Z"), true},
		{"exactly seven days", at("2025-10-13T12:00:00Z"), true},

		// A week without signal after the storm that caused the loss. Failed
		// here, but a caveat rather than a refusal -- see the test above.
		{"eight days", at("2025-10-12T12:00:00Z"), false},
		{"a month", at("2025-09-20T12:00:00Z"), false},

		// A handset with a wrong clock, or a deliberate future timestamp.
		// Not "stale", so this gate passes it; the incident gate is what
		// catches a capture that cannot belong to this loss.
		{"future timestamp", at("2025-10-25T12:00:00Z"), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := FreshCapture(c.captured, now); got != c.want {
				t.Errorf("FreshCapture = %v, want %v", got, c.want)
			}
		})
	}
}

// --- what may be submitted at all -------------------------------------------

func TestValidKind(t *testing.T) {
	for _, k := range []Kind{
		KindWideShot, KindDamageCloseup, KindCropCloseup, KindBoundaryPoint,
	} {
		if !ValidKind(k) {
			t.Errorf("%q was rejected", k)
		}
	}
	for _, k := range []Kind{"", "selfie", "WIDE_SHOT", "document"} {
		if ValidKind(k) {
			t.Errorf("%q was accepted", k)
		}
	}
}

func TestAcceptedMediaAndItsLimits(t *testing.T) {
	// Stills and clips have different ceilings, and a type that is not here
	// must be refused by name rather than stored for something downstream to
	// choke on.
	for _, ct := range []string{"image/jpeg", "image/png", "image/webp"} {
		limit, ok := acceptedMedia[ct]
		if !ok || limit != MaxBytes {
			t.Errorf("%s limit = %d, want %d", ct, limit, MaxBytes)
		}
		if IsVideo(ct) {
			t.Errorf("%s was classified as video", ct)
		}
	}

	for _, ct := range []string{"video/mp4", "video/webm", "video/quicktime"} {
		limit, ok := acceptedMedia[ct]
		if !ok || limit != MaxVideoBytes {
			t.Errorf("%s limit = %d, want %d", ct, limit, MaxVideoBytes)
		}
		if !IsVideo(ct) {
			t.Errorf("%s was not classified as video", ct)
		}
	}

	for _, ct := range []string{
		"application/pdf", "image/svg+xml", "text/html", "", "image/gif",
	} {
		if _, ok := acceptedMedia[ct]; ok {
			t.Errorf("%q is accepted and should not be", ct)
		}
	}
}

func TestExtensionMatchesTheContentType(t *testing.T) {
	// The stored key carries this extension. A clip saved as .jpg is a file
	// nothing downstream will play.
	cases := map[string]string{
		"image/jpeg":      ".jpg",
		"image/png":       ".png",
		"image/webp":      ".webp",
		"video/mp4":       ".mp4",
		"video/webm":      ".webm",
		"video/quicktime": ".mov",
	}
	for ct, want := range cases {
		if got := extensionFor(ct); got != want {
			t.Errorf("extensionFor(%q) = %q, want %q", ct, got, want)
		}
	}
}

func TestAPositionIsRequiredAtSubmission(t *testing.T) {
	// Migration 0010 made lat and lon nullable so a capture can be forgotten.
	// That removed the database's guarantee, so this is now the only thing
	// standing between a positionless capture and the geofence.
	valid := [][2]float64{
		{18.1727, 75.8135}, // the reference parcel
		{-33.86, 151.20},   // southern and eastern
		{8.08, 77.55},      // Kanyakumari
	}
	for _, p := range valid {
		if !ValidPosition(p[0], p[1]) {
			t.Errorf("ValidPosition(%v, %v) = false, want true", p[0], p[1])
		}
	}

	invalid := [][2]float64{
		{0, 0},   // null island, and the shape of an uninitialised read
		{91, 75}, // past the pole
		{-91, 75},
		{18, 181}, // past the antimeridian
		{18, -181},
	}
	for _, p := range invalid {
		if ValidPosition(p[0], p[1]) {
			t.Errorf("ValidPosition(%v, %v) = true, want false", p[0], p[1])
		}
	}
}
