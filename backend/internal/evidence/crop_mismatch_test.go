package evidence

import (
	"strings"
	"testing"

	"github.com/agribid/esurvey-platform/backend/internal/ai"
	"github.com/agribid/esurvey-platform/backend/internal/vision"
)

// A claim filed for a crop that is not in the field.
//
// The walk is the only evidence that can catch it: the registry records what
// was sown months ago, and Sentinel-2 at ten metres cannot tell soybean from
// sugarcane on two hectares. What matters as much as raising the flag is not
// raising it wrongly -- "the model could not tell" is silence, and reading it
// as an accusation costs a farmer their claim.

func boolPtr(b bool) *bool { return &b }

func TestAConfidentMismatchHoldsTheClaimForInvestigation(t *testing.T) {
	p := &Packet{GroundCrop: &vision.CropCheck{
		Declared:   "Soybean",
		Seen:       "sugarcane",
		Matches:    boolPtr(false),
		Confidence: 0.93,
		Detail:     "tall jointed canes in wide rows, no trifoliate leaves",
	}}

	ind := p.CropMismatch()
	if ind == nil {
		t.Fatal("no indicator for a field that holds a different crop")
	}
	if ind.Code != "CROP_NOT_AS_DECLARED" {
		t.Errorf("code = %q", ind.Code)
	}
	if ind.Severity != ai.FraudCritical {
		t.Errorf("severity = %q; a confident identification of another crop "+
			"routes the claim to a person", ind.Severity)
	}
	// An officer has to be able to check it against the same video.
	for _, want := range []string{"Soybean", "sugarcane", "trifoliate", "93%"} {
		if !strings.Contains(ind.Detail, want) {
			t.Errorf("detail does not mention %q: %s", want, ind.Detail)
		}
	}
	// And it must read as evidence to weigh, not as a verdict.
	if !strings.Contains(ind.Detail, "officers' own inspection settles it") {
		t.Errorf("detail reads as a verdict rather than a flag: %s", ind.Detail)
	}
}

func TestAnUnsureMismatchFlagsWithoutHoldingTheClaim(t *testing.T) {
	// The cost of being wrong falls on a farmer whose field really does hold
	// what they declared, so an unsure model does not get to stop the claim.
	p := &Packet{GroundCrop: &vision.CropCheck{
		Declared: "Jowar", Seen: "a cereal, species unclear",
		Matches: boolPtr(false), Confidence: 0.4,
	}}

	ind := p.CropMismatch()
	if ind == nil {
		t.Fatal("an unsure mismatch was dropped entirely")
	}
	if ind.Severity != ai.FraudWarning {
		t.Errorf("severity = %q, want warning at 0.4 confidence", ind.Severity)
	}
}

func TestSilenceIsNeverReadAsAnAccusation(t *testing.T) {
	// Each of these is a case where nothing can be said about the crop's
	// identity. None of them may produce a flag against a farmer.
	cases := map[string]*Packet{
		"no packet at all":       nil,
		"no ground crop check":   {},
		"crop could not be told": {GroundCrop: &vision.CropCheck{Declared: "Soybean", Seen: ""}},
		"nothing was declared":   {GroundCrop: &vision.CropCheck{Seen: "soybean"}},
		"the crop matches":       {GroundCrop: &vision.CropCheck{Declared: "Soybean", Seen: "soybean", Matches: boolPtr(true)}},
		"unidentifiable after damage": {GroundCrop: &vision.CropCheck{
			Declared: "Soybean", Seen: "flattened, silt-covered, unidentifiable",
		}},
	}
	for name, p := range cases {
		if ind := p.CropMismatch(); ind != nil {
			t.Errorf("%s produced a fraud flag: %+v", name, ind)
		}
	}
}

func TestAMismatchWithNoNameForWhatWasSeenStillReads(t *testing.T) {
	p := &Packet{GroundCrop: &vision.CropCheck{
		Declared: "Soybean", Matches: boolPtr(false), Confidence: 0.85,
	}}
	ind := p.CropMismatch()
	if ind == nil {
		t.Fatal("no indicator")
	}
	if !strings.Contains(ind.Detail, "an unidentified crop") {
		t.Errorf("detail = %s", ind.Detail)
	}
}
